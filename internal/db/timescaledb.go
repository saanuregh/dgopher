package db

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"dgopher/internal/sqltext"

	"github.com/jackc/pgx/v5/pgconn"
)

// timescaledbDialect is PostgreSQL's Dialect but for the engine it names, so that
// the rules found through it are TimescaleDB's, and for what its extension
// adds to a database: its own schemas hidden, hypertables, continuous
// aggregates, chunks and jobs.
type timescaledbDialect struct{ postgresDialect }

func (timescaledbDialect) Engine() Engine { return TimescaleDB }

// timescaleExtensionQuery reads the schema of the database's TimescaleDB
// extension, no row where it has not created it.
const timescaleExtensionQuery = `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = 'timescaledb'`

// timescaleSchema is the schema of the database's TimescaleDB extension,
// "" where it has not created it: then the database is PostgreSQL's.
func timescaleSchema(ctx context.Context, q Querier) (string, error) {
	out, err := queryStrings(ctx, q, timescaleExtensionQuery)
	if err != nil || len(out) == 0 {
		return "", err
	}
	return out[0], nil
}

// timescaleOwnSchemasFilter is a condition leaving out TimescaleDB's own
// schemas, which hold its catalog, functions, views and chunks, named by
// column.
func timescaleOwnSchemasFilter(column string) string {
	return "\n  AND " + column + ` NOT LIKE '\_timescaledb\_%' AND ` + column + " NOT IN ('timescaledb_information', 'timescaledb_experimental')"
}

// Schemas: PostgreSQL's, but TimescaleDB's own.
func (timescaledbDialect) Schemas(ctx context.Context, q Querier) ([]string, error) {
	return postgresSchemas(ctx, q, timescaleOwnSchemasFilter("nspname"))
}

// timescaleObjectsQuery reads a schema's hypertables and continuous
// aggregates: name, mark, size and rows, by the functions of the
// extension's schema; a hypertable's own table holds none of its rows.
func timescaleObjectsQuery(extension string) string {
	fn := quoteDouble(extension) + "."
	return `SELECT h.hypertable_name, 'hypertable' || CASE WHEN h.compression_enabled THEN ', compressed' ELSE '' END,
  ` + fn + `hypertable_size(format('%I.%I', h.hypertable_schema, h.hypertable_name)::regclass),
  ` + fn + `approximate_row_count(format('%I.%I', h.hypertable_schema, h.hypertable_name)::regclass)
FROM timescaledb_information.hypertables h WHERE h.hypertable_schema = $1
UNION ALL
SELECT a.view_name, 'continuous aggregate',
  ` + fn + `hypertable_size(format('%I.%I', a.materialization_hypertable_schema, a.materialization_hypertable_name)::regclass), -1
FROM timescaledb_information.continuous_aggregates a WHERE a.view_schema = $1`
}

// Objects: PostgreSQL's, a hypertable marked as one, sized and counted by
// its chunks, and a continuous aggregate a materialized view, sized by
// what it keeps; both read once a schema. Should TimescaleDB's catalog
// fail, the objects are PostgreSQL's, unmarked.
func (d timescaledbDialect) Objects(ctx context.Context, q Querier, schema string) ([]Object, error) {
	extension, err := timescaleSchema(ctx, q)
	if err != nil {
		return nil, err
	}
	objs, err := d.postgresDialect.Objects(ctx, q, schema)
	if err != nil || extension == "" {
		return objs, err
	}
	type found struct {
		mark        string
		bytes, rows sql.NullInt64
	}
	marked := map[string]found{}
	err = scanRows(ctx, q, timescaleObjectsQuery(extension), []any{schema}, func(scan func(...any) error) error {
		var name string
		var f found
		if err := scan(&name, &f.mark, &f.bytes, &f.rows); err != nil {
			return err
		}
		marked[name] = f
		return nil
	})
	if err != nil {
		return objs, nil
	}
	for i := range objs {
		f, ok := marked[objs[i].Name]
		if !ok {
			continue
		}
		o := &objs[i]
		o.Engine = f.mark
		if o.Kind == KindView {
			o.Kind = KindMaterializedView
		}
		if f.bytes.Valid {
			o.Bytes = f.bytes.Int64
		}
		if f.rows.Valid && f.rows.Int64 >= 0 {
			o.Rows = f.rows.Int64
		}
	}
	return objs, nil
}

// timescaleDimensionsQuery reads a hypertable's dimensions, in order:
// column, whether it is a time (open) one, its chunk interval, of time or
// of integers, its integer now function and its number of partitions.
const timescaleDimensionsQuery = `SELECT d.column_name, d.dimension_type = 'Time', coalesce(d.time_interval::text, ''), coalesce(d.integer_interval::text, ''),
  coalesce(d.integer_now_func, ''), coalesce(d.num_partitions::text, '')
FROM timescaledb_information.dimensions d WHERE d.hypertable_schema = $1 AND d.hypertable_name = $2
ORDER BY d.dimension_number`

// timescaleCompressionQuery reads whether a hypertable is compressed, and
// by what it segments and orders.
const timescaleCompressionQuery = `SELECT h.compression_enabled, coalesce(s.segmentby, ''), coalesce(s.orderby, '')
FROM timescaledb_information.hypertables h
LEFT JOIN timescaledb_information.hypertable_compression_settings s ON s.hypertable = format('%I.%I', h.hypertable_schema, h.hypertable_name)::regclass
WHERE h.hypertable_schema = $1 AND h.hypertable_name = $2`

// timescaleCompressionQueryBefore214 is timescaleCompressionQuery before
// TimescaleDB 2.14, which kept the settings a column a row.
const timescaleCompressionQueryBefore214 = `SELECT h.compression_enabled,
  coalesce((SELECT string_agg(quote_ident(s.attname), ', ' ORDER BY s.segmentby_column_index) FROM timescaledb_information.compression_settings s
    WHERE s.hypertable_schema = h.hypertable_schema AND s.hypertable_name = h.hypertable_name AND s.segmentby_column_index IS NOT NULL), ''),
  coalesce((SELECT string_agg(quote_ident(s.attname) || CASE WHEN s.orderby_asc THEN '' ELSE ' DESC' END
      || CASE WHEN s.orderby_nullsfirst = s.orderby_asc THEN CASE WHEN s.orderby_nullsfirst THEN ' NULLS FIRST' ELSE ' NULLS LAST' END ELSE '' END,
      ', ' ORDER BY s.orderby_column_index) FROM timescaledb_information.compression_settings s
    WHERE s.hypertable_schema = h.hypertable_schema AND s.hypertable_name = h.hypertable_name AND s.orderby_column_index IS NOT NULL), '')
FROM timescaledb_information.hypertables h WHERE h.hypertable_schema = $1 AND h.hypertable_name = $2`

// timescaleAggregateQuery reads a continuous aggregate: its query,
// whether it reads what it keeps alone, and whether it is compressed.
const timescaleAggregateQuery = `SELECT a.view_definition, a.materialized_only, a.compression_enabled
FROM timescaledb_information.continuous_aggregates a WHERE a.view_schema = $1 AND a.view_name = $2`

// DDL: PostgreSQL's, a hypertable's followed by the statements that make
// it one, with its dimensions and compression, and a continuous
// aggregate's as the materialized view TimescaleDB makes it from. Should
// TimescaleDB's catalog fail, PostgreSQL's, ending with a comment saying
// so.
func (d timescaledbDialect) DDL(ctx context.Context, q Querier, schema string, obj Object) (string, error) {
	extension, err := timescaleSchema(ctx, q)
	if err != nil {
		return "", err
	}
	ddl, err := d.timescaleDDL(ctx, q, extension, schema, obj)
	var unread timescaleUnread
	if errors.As(err, &unread) {
		ddl, err = d.postgresDialect.DDL(ctx, q, schema, obj)
		if err == nil {
			ddl += "\n-- TimescaleDB's part of this definition could not be read: " + strings.ReplaceAll(unread.err.Error(), "\n", " ") + "\n"
		}
	}
	return ddl, err
}

// timescaleUnread is an error of TimescaleDB's catalog, for DDL to fall
// back to PostgreSQL's definition.
type timescaleUnread struct{ err error }

func (u timescaleUnread) Error() string { return u.err.Error() }

// timescaleDDL is DDL's definition where the extension is in schema
// extension, "" without it; an error of TimescaleDB's catalog is a
// timescaleUnread.
func (d timescaledbDialect) timescaleDDL(ctx context.Context, q Querier, extension, schema string, obj Object) (string, error) {
	if extension != "" && (obj.Kind == KindView || obj.Kind == KindMaterializedView) {
		var def string
		var materializedOnly, compressed bool
		found := false
		err := scanRows(ctx, q, timescaleAggregateQuery, []any{schema, obj.Name}, func(scan func(...any) error) error {
			found = true
			return scan(&def, &materializedOnly, &compressed)
		})
		if err != nil {
			return "", timescaleUnread{err}
		}
		if found {
			return continuousAggregateSQL(QualifiedName(d, schema, obj.Name), def, materializedOnly, compressed), nil
		}
	}
	ddl, err := d.postgresDialect.DDL(ctx, q, schema, obj)
	if err != nil || extension == "" || obj.Kind != KindTable {
		return ddl, err
	}
	var dims []timescaleDimension
	err = scanRows(ctx, q, timescaleDimensionsQuery, []any{schema, obj.Name}, func(scan func(...any) error) error {
		var dim timescaleDimension
		err := scan(&dim.column, &dim.time, &dim.interval, &dim.integerInterval, &dim.integerNow, &dim.partitions)
		dims = append(dims, dim)
		return err
	})
	if err != nil {
		return "", timescaleUnread{err}
	}
	if len(dims) == 0 {
		return ddl, nil
	}
	var compressed bool
	var segmentBy, orderBy string
	readCompression := func(query string) error {
		return scanRows(ctx, q, query, []any{schema, obj.Name}, func(scan func(...any) error) error {
			return scan(&compressed, &segmentBy, &orderBy)
		})
	}
	err = readCompression(timescaleCompressionQuery)
	if pg := (*pgconn.PgError)(nil); errors.As(err, &pg) && pg.Code == "42P01" {
		err = readCompression(timescaleCompressionQueryBefore214)
	}
	if err != nil {
		return "", timescaleUnread{err}
	}
	return ddl + "\n" + hypertableSQL(QualifiedName(d, schema, obj.Name), dims, compressed, segmentBy, orderBy), nil
}

// timescaleDimension is a dimension of a hypertable, as
// timescaleDimensionsQuery reads it.
type timescaleDimension struct {
	column                    string
	time                      bool
	interval, integerInterval string
	integerNow, partitions    string
}

// chunkInterval is how a time dimension's chunk interval is written: an
// interval, or a number for an integer column.
func (dim timescaleDimension) chunkInterval() string {
	if dim.interval != "" {
		return "INTERVAL " + quoteString(dim.interval)
	}
	return dim.integerInterval
}

// hypertableSQL makes table, made as a plain table, a hypertable of dims,
// compressed by segmentBy and orderBy when compressed, by
// create_hypertable's first form, which old and new 2.x versions take.
func hypertableSQL(table string, dims []timescaleDimension, compressed bool, segmentBy, orderBy string) string {
	var b strings.Builder
	name := quoteString(table)
	for i, dim := range dims {
		switch {
		case i == 0:
			fmt.Fprintf(&b, "SELECT create_hypertable(%s, %s, chunk_time_interval => %s);\n", name, quoteString(dim.column), dim.chunkInterval())
		case dim.time:
			fmt.Fprintf(&b, "SELECT add_dimension(%s, %s, chunk_time_interval => %s);\n", name, quoteString(dim.column), dim.chunkInterval())
		default:
			fmt.Fprintf(&b, "SELECT add_dimension(%s, %s, number_partitions => %s);\n", name, quoteString(dim.column), dim.partitions)
		}
		if dim.integerNow != "" {
			fmt.Fprintf(&b, "SELECT set_integer_now_func(%s, %s);\n", name, quoteString(dim.integerNow))
		}
	}
	if compressed {
		options := []string{"timescaledb.compress"}
		if segmentBy != "" {
			options = append(options, "timescaledb.compress_segmentby = "+quoteString(segmentBy))
		}
		if orderBy != "" {
			options = append(options, "timescaledb.compress_orderby = "+quoteString(orderBy))
		}
		fmt.Fprintf(&b, "ALTER TABLE %s SET (%s);\n", table, strings.Join(options, ", "))
	}
	return b.String()
}

// continuousAggregateSQL makes the continuous aggregate view of query,
// without its data, which its refresh policy, or a refresh, reads.
func continuousAggregateSQL(view, query string, materializedOnly, compressed bool) string {
	s := fmt.Sprintf("CREATE MATERIALIZED VIEW %s WITH (timescaledb.continuous, timescaledb.materialized_only = %t) AS\n%s\nWITH NO DATA;\n",
		view, materializedOnly, strings.TrimRight(query, "; \n"))
	if compressed {
		s += fmt.Sprintf("ALTER MATERIALIZED VIEW %s SET (timescaledb.compress = true);\n", view)
	}
	return s
}

// timescaleJobs is timescaledb_information.jobs as j, each job with what
// it acts on, on_schema and on_name: for a refresh policy its continuous
// aggregate, which TimescaleDB 2.14 and older name by the hypertable
// keeping its rows.
const timescaleJobs = `(SELECT j.*, coalesce(a.view_schema, j.hypertable_schema) AS on_schema, coalesce(a.view_name, j.hypertable_name) AS on_name
  FROM timescaledb_information.jobs j LEFT JOIN timescaledb_information.continuous_aggregates a
    ON a.materialization_hypertable_schema = j.hypertable_schema AND a.materialization_hypertable_name = j.hypertable_name) j`

// timescaleChunkItems is how many chunks of a schema are items, the
// newest: a hypertable may have thousands. A variable so tests can
// shorten it.
var timescaleChunkItems = 500

// timescaleItemsQuery reads a schema's newest chunks, up to $2, by
// hypertable and in order, and an item saying how many more there are,
// and its jobs: those on its hypertables and continuous aggregates, and
// those whose procedure is in it.
const timescaleItemsQuery = `SELECT kind, name, tbl, detail, id FROM (
SELECT 'chunk' AS kind, c.chunk_name AS name, c.hypertable_name AS tbl,
  c.primary_dimension || ' from ' || coalesce(c.range_start::text, c.range_start_integer::text) || ' to ' || coalesce(c.range_end::text, c.range_end_integer::text)
    || CASE WHEN c.is_compressed THEN ', compressed' ELSE '' END AS detail,
  format('%I.%I', c.chunk_schema, c.chunk_name) AS id, coalesce(c.range_start::text, lpad(c.range_start_integer::text, 20, '0')) AS sort
FROM (SELECT c.*, row_number() OVER (ORDER BY c.range_start DESC NULLS LAST, c.range_start_integer DESC NULLS LAST) AS newest
  FROM timescaledb_information.chunks c WHERE c.hypertable_schema = $1) c
WHERE c.newest <= $2::int
UNION ALL
SELECT 'chunk', format('%s older chunks not listed', count(*) - $2::int), '', '', '', ''
FROM timescaledb_information.chunks c WHERE c.hypertable_schema = $1 HAVING count(*) > $2::int
UNION ALL
SELECT 'job', j.application_name, coalesce(j.on_name, ''),
  'every ' || j.schedule_interval::text || CASE WHEN j.scheduled THEN '' ELSE ', paused' END, j.job_id::text, lpad(j.job_id::text, 20, '0')
FROM ` + timescaleJobs + ` WHERE coalesce(j.on_schema, j.proc_schema) = $1
) items ORDER BY kind, tbl, sort`

// Items: PostgreSQL's, and TimescaleDB's chunks and jobs, unless its
// catalog fails.
func (d timescaledbDialect) Items(ctx context.Context, q Querier, schema string) ([]Item, error) {
	extension, err := timescaleSchema(ctx, q)
	if err != nil {
		return nil, err
	}
	items, err := d.postgresDialect.Items(ctx, q, schema)
	if err != nil || extension == "" {
		return items, err
	}
	more, err := scanItems(ctx, q, schema, timescaleItemsQuery, schema, timescaleChunkItems)
	if err != nil {
		return items, nil
	}
	return append(items, more...), nil
}

// timescaleJobQuery reads a job: its procedure, the hypertable or
// continuous aggregate it acts on, its configuration and how often it runs.
const timescaleJobQuery = `SELECT j.proc_schema, j.proc_name, CASE WHEN j.on_name IS NULL THEN '' ELSE format('%I.%I', j.on_schema, j.on_name) END,
  coalesce(j.config::text, ''), j.schedule_interval::text
FROM ` + timescaleJobs + ` WHERE j.job_id = $1::int`

// ItemDDL: a job's is the statement that makes it again, a chunk's says
// what it holds, as TimescaleDB makes chunks itself, and the item of the
// chunks not listed where they are; the rest as on PostgreSQL.
func (d timescaledbDialect) ItemDDL(ctx context.Context, q Querier, it Item) (string, error) {
	switch it.Kind {
	case ItemChunk:
		if it.ID == "" {
			return fmt.Sprintf("-- DGopher lists the newest %d chunks of a schema; timescaledb_information.chunks lists them all.\n", timescaleChunkItems), nil
		}
		return fmt.Sprintf("-- %s holds the rows of %s with %s. TimescaleDB makes chunks as rows arrive; drop_chunks, compress_chunk and decompress_chunk act on them.\n",
			it.ID, QualifiedName(d, it.Schema, it.Table), it.Detail), nil
	case ItemJob:
		var job timescaleJob
		found := false
		err := scanRows(ctx, q, timescaleJobQuery, []any{it.ID}, func(scan func(...any) error) error {
			found = true
			return scan(&job.procSchema, &job.proc, &job.relation, &job.config, &job.schedule)
		})
		if err == nil && !found {
			err = fmt.Errorf("TimescaleDB has no job %s", it.ID)
		}
		if err != nil {
			return "", err
		}
		return job.definition() + "\n", nil
	}
	return d.postgresDialect.ItemDDL(ctx, q, it)
}

// timescaleJob is a TimescaleDB job, as timescaleJobQuery reads it.
type timescaleJob struct {
	procSchema, proc, relation, config, schedule string
}

// timescalePolicies are the functions that add each policy TimescaleDB
// runs, by its procedure, and the keys of its configuration they take
// first, in order; whether they take how often it runs.
var timescalePolicies = map[string]struct {
	add      string
	keys     []string
	schedule bool
}{
	"policy_retention":                    {"add_retention_policy", []string{"drop_after", "drop_created_before"}, true},
	"policy_compression":                  {"add_compression_policy", []string{"compress_after", "compress_created_before"}, true},
	"policy_refresh_continuous_aggregate": {"add_continuous_aggregate_policy", []string{"start_offset", "end_offset"}, true},
	"policy_reorder":                      {"add_reorder_policy", []string{"index_name"}, false},
}

// definition is the statement that makes the job again: a policy's add
// function, given its configuration, a job of the user's procedure
// add_job, else a comment saying what it runs.
func (j timescaleJob) definition() string {
	every := "INTERVAL " + quoteString(j.schedule)
	policy, isPolicy := timescalePolicies[j.proc]
	switch {
	case !strings.HasPrefix(j.procSchema, "_timescaledb_"):
		s := fmt.Sprintf("SELECT add_job(%s::regproc, %s", quoteString(QualifiedName(postgresDialect{}, j.procSchema, j.proc)), every)
		if j.config != "" {
			s += ", config => " + quoteString(j.config) + "::jsonb"
		}
		return s + ");"
	case isPolicy && j.relation != "":
		var config map[string]json.RawMessage
		if err := json.Unmarshal([]byte(j.config), &config); err == nil {
			args := []string{quoteString(j.relation)}
			for _, key := range policyKeys(config, policy.keys) {
				args = append(args, key+" => "+policyValue(key, config[key]))
			}
			if policy.schedule {
				args = append(args, "schedule_interval => "+every)
			}
			return fmt.Sprintf("SELECT %s(%s);", policy.add, strings.Join(args, ", "))
		}
	}
	return fmt.Sprintf("-- TimescaleDB runs %s.%s on %s every %s, with %s; DGopher cannot write the statement that makes it.", j.procSchema, j.proc, j.relation, j.schedule, j.config)
}

// policyKeys are the keys of a policy's configuration its add function
// takes: first the known ones, in order, then the rest by name, but the
// IDs TimescaleDB adds.
func policyKeys(config map[string]json.RawMessage, first []string) []string {
	var keys, rest []string
	for _, k := range first {
		if _, ok := config[k]; ok {
			keys = append(keys, k)
		}
	}
	for k := range config {
		if !slices.Contains(first, k) && k != "hypertable_id" && k != "mat_hypertable_id" {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

// policyValue writes a configuration value as its add function takes it:
// a number or a truth as it is, an index's name as a string, any other
// string as an interval.
func policyValue(key string, v json.RawMessage) string {
	var s string
	if len(v) == 0 || v[0] != '"' || json.Unmarshal(v, &s) != nil {
		return strings.ToUpper(string(v)) // a number, a truth or null
	}
	if key == "index_name" {
		return quoteString(s)
	}
	return "INTERVAL " + quoteString(s)
}

// timescaledbEngine is TimescaleDB: PostgreSQL's rules but for its name, how it is
// reached by default, and what its extension does otherwise.
type timescaledbEngine struct{ postgresEngine }

var _ SQLRules = timescaledbEngine{}

func (timescaledbEngine) Engine() Engine       { return TimescaleDB }
func (timescaledbEngine) dialect() Dialect     { return timescaledbDialect{} }
func (timescaledbEngine) Label() string        { return "TimescaleDB" }
func (timescaledbEngine) Base() Engine         { return Postgres }
func (timescaledbEngine) ColorHex() string     { return "#3fa34d" }
func (timescaledbEngine) DefaultPort() int     { return 5432 }
func (timescaledbEngine) DefaultUser() string  { return "postgres" }
func (timescaledbEngine) URLSchemes() []string { return []string{"timescaledb"} }

func (timescaledbEngine) CreateFile(context.Context, string) error { return noFileError(TimescaleDB) }

// identify keeps a TimescaleDB connection TimescaleDB on a database that
// has not created the extension, when the server has it to create: the
// extension belongs to each database, the server stays TimescaleDB's.
func (timescaledbEngine) identify(ctx context.Context, q *sql.DB) (Engine, string, error) {
	e, v, err := identifyPostgresFamily(ctx, q)
	if err != nil || e != Postgres {
		return e, v, err
	}
	var available sql.NullString
	_ = q.QueryRowContext(ctx, `SELECT default_version FROM pg_available_extensions WHERE name = 'timescaledb'`).Scan(&available)
	if available.Valid {
		return TimescaleDB, available.String, nil
	}
	return e, v, nil
}

// Search: PostgreSQL's, but in TimescaleDB's own schemas, where its
// chunks and views are.
func (timescaledbEngine) Search(ctx context.Context, d *DB, text string, definitions bool) ([]SearchHit, bool, error) {
	text = strings.ToLower(text)
	return searchWith(ctx, d, text, postgresSearch+timescaleOwnSchemasFilter("s"), []any{text, definitions})
}

// timescaleOutsideCalls are TimescaleDB's routines that refuse a
// transaction wherever a statement calls them; run_job for the jobs that
// commit as they go, as refresh and compression policies.
var timescaleOutsideCalls = []string{"REFRESH_CONTINUOUS_AGGREGATE", "MERGE_CHUNKS_CONCURRENTLY", "RUN_JOB", "REORDER_CHUNK", "MOVE_CHUNK"}

// OutsideTransaction: PostgreSQL's, a call of timescaleOutsideCalls, by a
// quoted name too, a continuous aggregate made with its data, which
// refreshes it, and an index made a transaction a chunk
// (timescaledb.transaction_per_chunk): TimescaleDB refuses them in a
// transaction.
func (t timescaledbEngine) OutsideTransaction(sql string) bool {
	var toks []sqltext.Token
	for _, tok := range sqltext.Tokenize(sql, sqltext.Postgres) {
		if tok.Kind != sqltext.Whitespace && tok.Kind != sqltext.Comment {
			toks = append(toks, tok)
		}
	}
	name := func(i int) string {
		if i >= len(toks) || !sqltext.IsName(toks[i]) && toks[i].Kind != sqltext.Keyword {
			return ""
		}
		return strings.ToUpper(sqltext.DecodeQuoted(toks[i].Text, sqltext.Postgres))
	}
	for i := range toks {
		if i+1 < len(toks) && toks[i+1].Text == "(" && slices.Contains(timescaleOutsideCalls, name(i)) {
			return true
		}
	}
	words := nameWords(sql, sqltext.Postgres)
	if wordAt(words, 0) == "CREATE" {
		as := slices.Index(words, "AS")
		n := len(words)
		noData := n >= 3 && words[n-3] == "WITH" && words[n-2] == "NO" && words[n-1] == "DATA"
		if wordAt(words, 1) == "MATERIALIZED" && as > 0 && slices.Contains(words[:as], "CONTINUOUS") && !noData {
			return true
		}
		if slices.Contains(words[:min(3, n)], "INDEX") {
			for i := range toks {
				option, value := name(i), i+1
				if option == "TIMESCALEDB" && i+2 < len(toks) && toks[i+1].Text == "." {
					option, value = option+"."+name(i+2), i+3
				}
				if option != "TIMESCALEDB.TRANSACTION_PER_CHUNK" {
					continue
				}
				if value+1 < len(toks) && toks[value].Text == "=" {
					return !falseWord(strings.ToLower(sqltext.DecodeQuoted(toks[value+1].Text, sqltext.Postgres)))
				}
				return true
			}
		}
	}
	return t.postgresEngine.OutsideTransaction(sql)
}

// falseWord reports whether PostgreSQL reads v as false, as an option's
// value: false, off, no, 0, or the start of false or no.
func falseWord(v string) bool {
	return v == "0" || v == "of" || v == "off" || v != "" && (strings.HasPrefix("false", v) || strings.HasPrefix("no", v))
}

// ParsePlan reads PostgreSQL's JSON, and a filter run as chunks
// decompress the detail of a step that has no other.
func (timescaledbEngine) ParsePlan(_ []string, rows [][]any) (*Plan, bool) {
	p, ok := parsePostgresPlan(planText(rows))
	if !ok {
		return p, ok
	}
	p.Root.Walk(func(n *PlanNode, _ int) {
		if v := n.Prop("Vectorized Filter"); n.Detail == "" && v != "" {
			n.Detail = "Vectorized Filter: " + v
		}
	})
	return p, true
}

// ActivityPages: PostgreSQL's, and the database's jobs: TimescaleDB's
// policies and those added with add_job.
func (t timescaledbEngine) ActivityPages() []ActivityPage {
	return append(t.postgresEngine.ActivityPages(), ActivityPage{
		Name: "Jobs",
		Query: `SELECT j.job_id AS job, j.application_name AS name,
  CASE WHEN j.on_name IS NULL THEN '' ELSE format('%I.%I', j.on_schema, j.on_name) END AS "on",
  j.schedule_interval::text AS every, j.scheduled, s.job_status AS status, s.last_run_status AS last_run,
  date_trunc('second', s.last_run_started_at)::text AS last_started, s.last_run_duration::text AS took,
  date_trunc('second', s.next_start)::text AS next_start, s.total_runs AS runs, s.total_failures AS failures, j.config::text AS config
FROM ` + timescaleJobs + ` LEFT JOIN timescaledb_information.job_stats s ON s.job_id = j.job_id
ORDER BY j.job_id`,
		Hint: "Jobs come from timescaledb_information.jobs and job_stats, of this database, which needs the TimescaleDB extension created. In an editor, SELECT alter_job(job, scheduled => false) pauses one and CALL run_job(job) runs it now.",
	})
}

// PlanRestore: pg_restore in one transaction, as PostgreSQL's, with
// TimescaleDB told a restore runs, between timescaledb_pre_restore(),
// which also stops the database's jobs, and timescaledb_post_restore(),
// where the database has the extension; should the latter fail, what the
// former did is undone by its parts.
func (timescaledbEngine) PlanRestore(ctx context.Context, d *DB, database, path string, _ bool) (ToolRun, error) {
	run, err := postgresTool(ctx, d, "pg_restore", database)
	if err != nil {
		return run, err
	}
	run.Argv = append(run.Argv, "--verbose", "--single-transaction", "--exit-on-error", "--", path)
	// The restore's own session restores, where the database had not
	// created the extension before.
	run.Env = append(run.Env, "PGOPTIONS=-c timescaledb.restoring=on")
	run.Before = []RunStep{{SQL: timescaleRestoreStep("timescaledb_pre_restore"),
		Needs: "Restoring into a TimescaleDB database needs a superuser: timescaledb_pre_restore() sets timescaledb.restoring on the database and stops its background workers, which TimescaleDB lets only a superuser do"}}
	run.After = []RunStep{{SQL: timescaleRestoreStep("timescaledb_post_restore")}}
	run.Undo = []RunStep{{SQL: "ALTER DATABASE " + quoteDouble(cmp.Or(database, d.Config.Database)) + " RESET timescaledb.restoring"},
		{SQL: "SELECT _timescaledb_functions.restart_background_workers()"}}
	return run, nil
}

// timescaleRestoreStep calls TimescaleDB's function fn, of the
// extension's schema, where the database has the extension.
func timescaleRestoreStep(fn string) string {
	return `DO $$DECLARE s name := (` + timescaleExtensionQuery + `); BEGIN
  IF s IS NOT NULL THEN EXECUTE format('SELECT %I.` + fn + `()', s); END IF;
END$$`
}

// BackupChoices: no Clean, which would drop the extension, refused while
// compressed chunks exist.
func (timescaledbEngine) BackupChoices() BackupChoices {
	return BackupChoices{Content: true}
}

func (timescaledbEngine) BackupNote(restore bool) string {
	return backupNoteFor(restore,
		"A .dump of pg_dump restores with pg_restore in one transaction, between timescaledb_pre_restore() and timescaledb_post_restore(), which pause the database's jobs and need a superuser; the server needs the dump's TimescaleDB version. A SQL file runs as Run SQL File runs it: put SELECT public.timescaledb_pre_restore(); before it and SELECT public.timescaledb_post_restore(); after it, naming the extension's schema.",
		"pg_dump writes it, reaching the server as the connection does; it must be installed, of the server's version or later.")
}
