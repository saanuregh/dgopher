package db

import (
	"context"
	"database/sql"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/sqltext"
)

// Plans as TimescaleDB 2.31 printed them, by EXPLAIN (VERBOSE, FORMAT
// JSON), for tsx.metrics: a hypertable of 1-day chunks on t, compressed
// segmented by device.
const (
	// SELECT * FROM tsx.metrics WHERE t < '2026-01-03' AND device = 3
	// ORDER BY t DESC LIMIT 5: two compressed chunks read in order.
	timescaleChunkAppend = `[
  {
    "Plan": {
      "Node Type": "Limit",
      "Parallel Aware": false,
      "Async Capable": false,
      "Startup Cost": 1.63,
      "Total Cost": 2.14,
      "Plan Rows": 5,
      "Plan Width": 20,
      "Output": ["metrics.t", "metrics.device", "metrics.v"],
      "Plans": [
        {
          "Node Type": "Custom Scan",
          "Parent Relationship": "Outer",
          "Custom Plan Provider": "ChunkAppend",
          "Parallel Aware": false,
          "Async Capable": false,
          "Relation Name": "metrics",
          "Schema": "tsx",
          "Alias": "metrics",
          "Startup Cost": 1.63,
          "Total Cost": 304.60,
          "Plan Rows": 3000,
          "Plan Width": 20,
          "Output": ["metrics.t", "metrics.device", "metrics.v"],
          "Order": ["metrics.t DESC"],
          "Startup Exclusion": false,
          "Runtime Exclusion": false,
          "Plans": [
            {
              "Node Type": "Custom Scan",
              "Parent Relationship": "children",
              "Custom Plan Provider": "ColumnarScan",
              "Parallel Aware": false,
              "Async Capable": false,
              "Relation Name": "_hyper_5_17_chunk",
              "Schema": "_timescaledb_internal",
              "Alias": "_hyper_5_17_chunk",
              "Startup Cost": 1.63,
              "Total Cost": 304.60,
              "Plan Rows": 3000,
              "Plan Width": 20,
              "Output": ["_hyper_5_17_chunk.t", "_hyper_5_17_chunk.device", "_hyper_5_17_chunk.v"],
              "Plans": [
                {
                  "Node Type": "Index Scan",
                  "Parent Relationship": "child",
                  "Parallel Aware": false,
                  "Async Capable": false,
                  "Scan Direction": "Forward",
                  "Index Name": "_hyper_5_17_chunk_compressed_device__ts_meta_v2_first_t__ts_idx",
                  "Relation Name": "_hyper_5_17_chunk_compressed",
                  "Schema": "_timescaledb_internal",
                  "Alias": "_hyper_5_17_chunk_compressed",
                  "Startup Cost": 0.15,
                  "Total Cost": 4.60,
                  "Plan Rows": 3,
                  "Plan Width": 88,
                  "Output": ["_hyper_5_17_chunk_compressed._ts_meta_count", "_hyper_5_17_chunk_compressed.device", "_hyper_5_17_chunk_compressed._ts_meta_min_1", "_hyper_5_17_chunk_compressed._ts_meta_max_1", "_hyper_5_17_chunk_compressed._ts_meta_v2_first_t", "_hyper_5_17_chunk_compressed._ts_meta_v2_last_t", "_hyper_5_17_chunk_compressed.t", "_hyper_5_17_chunk_compressed.v"],
                  "Index Cond": "(_hyper_5_17_chunk_compressed.device = 3)"
                }
              ]
            },
            {
              "Node Type": "Custom Scan",
              "Parent Relationship": "children",
              "Custom Plan Provider": "ColumnarScan",
              "Parallel Aware": false,
              "Async Capable": false,
              "Relation Name": "_hyper_5_16_chunk",
              "Schema": "_timescaledb_internal",
              "Alias": "_hyper_5_16_chunk",
              "Startup Cost": 1.63,
              "Total Cost": 304.60,
              "Plan Rows": 3000,
              "Plan Width": 20,
              "Output": ["_hyper_5_16_chunk.t", "_hyper_5_16_chunk.device", "_hyper_5_16_chunk.v"],
              "Plans": [
                {
                  "Node Type": "Index Scan",
                  "Parent Relationship": "child",
                  "Parallel Aware": false,
                  "Async Capable": false,
                  "Scan Direction": "Forward",
                  "Index Name": "_hyper_5_16_chunk_compressed_device__ts_meta_v2_first_t__ts_idx",
                  "Relation Name": "_hyper_5_16_chunk_compressed",
                  "Schema": "_timescaledb_internal",
                  "Alias": "_hyper_5_16_chunk_compressed",
                  "Startup Cost": 0.15,
                  "Total Cost": 4.60,
                  "Plan Rows": 3,
                  "Plan Width": 88,
                  "Output": ["_hyper_5_16_chunk_compressed._ts_meta_count", "_hyper_5_16_chunk_compressed.device", "_hyper_5_16_chunk_compressed._ts_meta_min_1", "_hyper_5_16_chunk_compressed._ts_meta_max_1", "_hyper_5_16_chunk_compressed._ts_meta_v2_first_t", "_hyper_5_16_chunk_compressed._ts_meta_v2_last_t", "_hyper_5_16_chunk_compressed.t", "_hyper_5_16_chunk_compressed.v"],
                  "Index Cond": "(_hyper_5_16_chunk_compressed.device = 3)"
                }
              ]
            }
          ]
        }
      ]
    }
  }
]`
	// SELECT * FROM tsx.metrics WHERE t < '2026-01-02' AND v > 0.5: one
	// compressed chunk, filtered as it is decompressed.
	timescaleVectorizedFilter = `[
  {
    "Plan": {
      "Node Type": "Custom Scan",
      "Custom Plan Provider": "ColumnarScan",
      "Parallel Aware": false,
      "Async Capable": false,
      "Relation Name": "_hyper_5_16_chunk",
      "Schema": "_timescaledb_internal",
      "Alias": "_hyper_5_16_chunk",
      "Startup Cost": 10.03,
      "Total Cost": 6116.10,
      "Plan Rows": 610000,
      "Plan Width": 20,
      "Output": ["_hyper_5_16_chunk.t", "_hyper_5_16_chunk.device", "_hyper_5_16_chunk.v"],
      "Vectorized Filter": "(_hyper_5_16_chunk.v > '0.5'::double precision)",
      "Plans": [
        {
          "Node Type": "Seq Scan",
          "Parent Relationship": "child",
          "Parallel Aware": false,
          "Async Capable": false,
          "Relation Name": "_hyper_5_16_chunk_compressed",
          "Schema": "_timescaledb_internal",
          "Alias": "_hyper_5_16_chunk_compressed",
          "Startup Cost": 0.00,
          "Total Cost": 16.10,
          "Plan Rows": 610,
          "Plan Width": 88,
          "Output": ["_hyper_5_16_chunk_compressed._ts_meta_count", "_hyper_5_16_chunk_compressed.device", "_hyper_5_16_chunk_compressed._ts_meta_min_1", "_hyper_5_16_chunk_compressed._ts_meta_max_1", "_hyper_5_16_chunk_compressed._ts_meta_v2_first_t", "_hyper_5_16_chunk_compressed._ts_meta_v2_last_t", "_hyper_5_16_chunk_compressed.t", "_hyper_5_16_chunk_compressed.v"]
        }
      ]
    }
  }
]`
)

// A custom scan names the TimescaleDB step it is, as on PostgreSQL, and a
// filter run as chunks decompress is the step's detail.
func TestTimescaleDBParsePlan(t *testing.T) {
	r, _ := SQL(TimescaleDB)
	p, ok := r.ParsePlan(nil, [][]any{{timescaleChunkAppend}})
	if !ok {
		t.Fatal("not parsed")
	}
	var ops []string
	p.Root.Walk(func(n *PlanNode, _ int) { ops = append(ops, n.Op) })
	want := []string{"Limit", "Custom Scan (ChunkAppend)", "Custom Scan (ColumnarScan)", "Index Scan", "Custom Scan (ColumnarScan)", "Index Scan"}
	if !slices.Equal(ops, want) {
		t.Errorf("steps %q, want %q", ops, want)
	}
	p, ok = r.ParsePlan(nil, [][]any{{timescaleVectorizedFilter}})
	if !ok || p.Root.Op != "Custom Scan (ColumnarScan)" || p.Root.Detail != "Vectorized Filter: (_hyper_5_16_chunk.v > '0.5'::double precision)" ||
		p.Root.Target != "_hyper_5_16_chunk" {
		t.Errorf("%+v", p.Root)
	}
	// PostgreSQL names any extension's custom scan by its provider, as
	// Citus's; the vectorized filter is TimescaleDB's alone.
	pg, _ := SQL(Postgres)
	if p, _ := pg.ParsePlan(nil, [][]any{{timescaleChunkAppend}}); p.Root.Children[0].Op != "Custom Scan (ChunkAppend)" {
		t.Errorf("PostgreSQL's step %q", p.Root.Children[0].Op)
	}
	if p, _ := pg.ParsePlan(nil, [][]any{{timescaleVectorizedFilter}}); p.Root.Detail != "" {
		t.Errorf("PostgreSQL's detail %q", p.Root.Detail)
	}
}

// Refreshing a continuous aggregate, making one with its data, merging
// chunks concurrently, reordering or moving a chunk, running a job, and
// an index made a transaction a chunk cannot run in a transaction, named
// quoted or not; neither can PostgreSQL's VACUUM. A continuous aggregate
// made WITH NO DATA, a materialized view of PostgreSQL's, and an index
// made in one transaction can (TimescaleDB 2.31).
func TestTimescaleDBOutsideTransaction(t *testing.T) {
	r, _ := SQL(TimescaleDB)
	for sql, want := range map[string]bool{
		"CALL refresh_continuous_aggregate('m', NULL, NULL)":                                                                                                             true,
		"call public.refresh_continuous_aggregate('m', '2026-01-01', '2026-02-01')":                                                                                      true,
		"CALL merge_chunks_concurrently(ARRAY['c1', 'c2']::regclass[])":                                                                                                  true,
		"CREATE MATERIALIZED VIEW m WITH (timescaledb.continuous) AS SELECT time_bucket('1 day', t) b, count(*) FROM h GROUP BY 1":                                       true,
		"CREATE MATERIALIZED VIEW m WITH (timescaledb.continuous, timescaledb.materialized_only = true) AS SELECT time_bucket('1 day', t) b FROM h GROUP BY 1 WITH DATA": true,
		"CREATE MATERIALIZED VIEW m WITH (timescaledb.continuous) AS SELECT time_bucket('1 day', t) b FROM h GROUP BY 1 WITH NO DATA;":                                   false,
		"CREATE MATERIALIZED VIEW m AS SELECT continuous FROM h":                                                                                                         false,
		"CALL run_job(1000)":                                     true,
		`CALL "public"."run_job"(3)`:                             true,
		`CALL "refresh_continuous_aggregate"('m', NULL, NULL)`:   true,
		"SELECT reorder_chunk('_timescaledb_internal.c', 'm_v')": true,
		"SELECT public.move_chunk(chunk => 'c', destination_tablespace => 'pg_default', index_destination_tablespace => 'pg_default')": true,
		"CREATE INDEX i ON m (v) WITH (timescaledb.transaction_per_chunk)":                                                             true,
		"CREATE UNIQUE INDEX i ON m (t, v) WITH (timescaledb.transaction_per_chunk = true)":                                            true,
		"CREATE INDEX i ON m (v) WITH (timescaledb.transaction_per_chunk = false)":                                                     false,
		"CREATE INDEX i ON m (v) WITH (timescaledb.transaction_per_chunk = 'off')":                                                     false,
		"CREATE INDEX i ON m (v)":                          false,
		"CALL merge_chunks(ARRAY['c1', 'c2']::regclass[])": false,
		"SELECT refresh_continuous_aggregate FROM t":       false,
		"SELECT reorder_chunk FROM t":                      false,
		"SELECT * FROM jobs WHERE name = 'run_job(1)'":     false,
		"VACUUM metrics":                                   true,
	} {
		if got := r.OutsideTransaction(sql); got != want {
			t.Errorf("%q: %v, want %v", sql, got, want)
		}
	}
	pg, _ := SQL(Postgres)
	if pg.OutsideTransaction("CALL refresh_continuous_aggregate('m', NULL, NULL)") {
		t.Error("PostgreSQL's rule changed")
	}
}

// A job's definition is the statement that makes it again, from its
// configuration as TimescaleDB 2.31 keeps it.
func TestTimescaleDBJobDefinition(t *testing.T) {
	for _, c := range []struct {
		job  timescaleJob
		want string
	}{
		{timescaleJob{"_timescaledb_functions", "policy_retention", `"tsx"."metrics"`, `{"drop_after": "365 days", "hypertable_id": 3}`, "1 day"},
			`SELECT add_retention_policy('"tsx"."metrics"', drop_after => INTERVAL '365 days', schedule_interval => INTERVAL '1 day');`},
		{timescaleJob{"_timescaledb_functions", "policy_compression", `"tsx"."metrics"`, `{"hypertable_id": 3, "compress_after": "7 days"}`, "12:00:00"},
			`SELECT add_compression_policy('"tsx"."metrics"', compress_after => INTERVAL '7 days', schedule_interval => INTERVAL '12:00:00');`},
		{timescaleJob{"_timescaledb_functions", "policy_refresh_continuous_aggregate", `"tsx"."metrics_hourly"`,
			`{"end_offset": "01:00:00", "start_offset": "3 days", "mat_hypertable_id": 4}`, "01:00:00"},
			`SELECT add_continuous_aggregate_policy('"tsx"."metrics_hourly"', start_offset => INTERVAL '3 days', end_offset => INTERVAL '01:00:00', schedule_interval => INTERVAL '01:00:00');`},
		{timescaleJob{"_timescaledb_functions", "policy_refresh_continuous_aggregate", `"tsx"."daily"`,
			`{"end_offset": null, "start_offset": 100, "buckets_per_batch": 2, "mat_hypertable_id": 9}`, "01:00:00"},
			`SELECT add_continuous_aggregate_policy('"tsx"."daily"', start_offset => 100, end_offset => NULL, buckets_per_batch => 2, schedule_interval => INTERVAL '01:00:00');`},
		{timescaleJob{"_timescaledb_functions", "policy_reorder", `"tsx"."metrics"`, `{"index_name": "metrics_t_idx", "hypertable_id": 3}`, "4 days"},
			`SELECT add_reorder_policy('"tsx"."metrics"', index_name => 'metrics_t_idx');`},
		{timescaleJob{"public", "it's job", "", `{"a": 1}`, "01:00:00"},
			`SELECT add_job('"public"."it''s job"'::regproc, INTERVAL '01:00:00', config => '{"a": 1}'::jsonb);`},
		{timescaleJob{"public", "tick", "", "", "00:05:00"},
			`SELECT add_job('"public"."tick"'::regproc, INTERVAL '00:05:00');`},
		{timescaleJob{"_timescaledb_functions", "policy_new", `"tsx"."metrics"`, `{"x": 1}`, "1 day"},
			`-- TimescaleDB runs _timescaledb_functions.policy_new on "tsx"."metrics" every 1 day, with {"x": 1}; DGopher cannot write the statement that makes it.`},
	} {
		if got := c.job.definition(); got != c.want {
			t.Errorf("%+v:\n got %s\nwant %s", c.job, got, c.want)
		}
	}
}

// The restore runs pg_restore in one transaction, with TimescaleDB told a
// restore runs, between timescaledb_pre_restore() and
// timescaledb_post_restore() where the extension is; Clean is not
// offered, as dropping the extension fails while compressed chunks exist.
func TestTimescaleDBBackup(t *testing.T) {
	d := &DB{Config: Config{Name: "ts", Engine: TimescaleDB, Host: "127.0.0.1", Port: 5432, User: "postgres", Database: "shop"}}
	r, _ := SQL(TimescaleDB)
	run, err := r.PlanRestore(context.Background(), d, "shop", "/tmp/x.dump", false)
	if err != nil || run.Argv[0] != "pg_restore" || !slices.Contains(run.Argv, "--single-transaction") || slices.Contains(run.Argv, "--clean") ||
		run.Argv[len(run.Argv)-1] != "/tmp/x.dump" {
		t.Fatalf("restore %q %v", run.Argv, err)
	}
	if !slices.Contains(run.Env, "PGOPTIONS=-c timescaledb.restoring=on") {
		t.Errorf("env %q", run.Env)
	}
	if len(run.Before) != 1 || !strings.Contains(run.Before[0].SQL, "timescaledb_pre_restore()") || !strings.Contains(run.Before[0].Needs, "superuser") ||
		len(run.After) != 1 || !strings.Contains(run.After[0].SQL, "timescaledb_post_restore()") {
		t.Errorf("before %+v, after %+v", run.Before, run.After)
	}
	if len(run.Undo) != 2 || run.Undo[0].SQL != `ALTER DATABASE "shop" RESET timescaledb.restoring` ||
		run.Undo[1].SQL != "SELECT _timescaledb_functions.restart_background_workers()" {
		t.Errorf("undo %+v", run.Undo)
	}
	if shown := run.Shown(); !strings.HasPrefix(shown, run.Before[0].SQL) || !strings.HasSuffix(shown, run.After[0].SQL) || !strings.Contains(shown, "pg_restore") {
		t.Errorf("shown %q", shown)
	}
	if r.BackupChoices() != (BackupChoices{Content: true}) || !strings.Contains(r.BackupNote(true), "timescaledb_post_restore") {
		t.Errorf("choices %+v, note %q", r.BackupChoices(), r.BackupNote(true))
	}
	pg, _ := SQL(Postgres)
	run, _ = pg.PlanRestore(context.Background(), d, "shop", "/tmp/x.dump", false)
	if len(run.Before)+len(run.After)+len(run.Undo) != 0 || slices.ContainsFunc(run.Env, func(e string) bool { return strings.HasPrefix(e, "PGOPTIONS=") }) {
		t.Errorf("PostgreSQL's restore %+v", run)
	}
}

// The pages are PostgreSQL's, and TimescaleDB's jobs, read only.
func TestTimescaleDBActivityPages(t *testing.T) {
	r, _ := SQL(TimescaleDB)
	pg, _ := SQL(Postgres)
	pages, pgPages := r.ActivityPages(), pg.ActivityPages()
	if len(pages) != len(pgPages)+1 {
		t.Fatalf("%d pages", len(pages))
	}
	for i, p := range pgPages {
		if pages[i].Name != p.Name || pages[i].Query != p.Query {
			t.Errorf("page %d: %s", i, pages[i].Name)
		}
	}
	jobs := pages[len(pages)-1]
	if jobs.Name != "Jobs" || !strings.Contains(jobs.Query, "timescaledb_information.job_stats") || jobs.Stop != nil || jobs.Kill != nil || jobs.Hint == "" {
		t.Errorf("%+v", jobs)
	}
}

// The app's own reads of TimescaleDB's catalog and activity classify as
// reads.
func TestTimescaleDBOwnReadsClassifyAsReads(t *testing.T) {
	r, _ := SQL(TimescaleDB)
	reads := []string{timescaleObjectsQuery("public"), timescaleItemsQuery, timescaleJobQuery, timescaleDimensionsQuery,
		timescaleCompressionQuery, timescaleCompressionQueryBefore214, timescaleAggregateQuery, timescaleExtensionQuery, postgresSearch + timescaleOwnSchemasFilter("s")}
	for _, page := range r.ActivityPages() {
		reads = append(reads, page.Query)
	}
	for _, sql := range reads {
		if a := sqltext.Classify(sql, LexDialect(TimescaleDB)); a.Class != sqltext.Read || a.Dangerous {
			t.Errorf("%+v:\n%s", a, sql)
		}
	}
}

// A database that has not created the extension reads as PostgreSQL's:
// the extension is looked for first, and nothing of TimescaleDB's is read.
func TestTimescaleDBWithoutExtension(t *testing.T) {
	q := &queryFailing{errs: []error{errors.New("lost")}}
	if _, err := (timescaledbDialect{}).Objects(context.Background(), q, "public"); err == nil || len(q.asked) != 1 {
		t.Errorf("error %v after %q", err, q.asked)
	}
}

// timescaleSetup makes schema it_ts: a hypertable of four 1-day chunks on
// t, two compressed, segmented by device, with retention and compression
// policies (whose first run may compress the others), a continuous
// aggregate of it with a refresh policy, a plain table, and a job of a
// procedure of its own.
var timescaleSetup = []string{
	"DROP SCHEMA IF EXISTS it_ts CASCADE",
	"CREATE SCHEMA it_ts",
	"CREATE TABLE it_ts.metrics (t timestamptz NOT NULL, device int, v double precision)",
	"SELECT create_hypertable('it_ts.metrics', by_range('t', INTERVAL '1 day'))",
	"INSERT INTO it_ts.metrics SELECT timestamptz '2026-01-01' + i * INTERVAL '1 minute', i % 10, i FROM generate_series(1, 5000) i",
	"ALTER TABLE it_ts.metrics SET (timescaledb.compress, timescaledb.compress_segmentby = 'device', timescaledb.compress_orderby = 't DESC')",
	"SELECT compress_chunk(c) FROM show_chunks('it_ts.metrics', older_than => timestamptz '2026-01-03') c",
	"SELECT add_compression_policy('it_ts.metrics', INTERVAL '7 days')",
	"SELECT add_retention_policy('it_ts.metrics', INTERVAL '3650 days')",
	"CREATE MATERIALIZED VIEW it_ts.hourly WITH (timescaledb.continuous) AS SELECT time_bucket('1 hour', t) AS bucket, device, avg(v) FROM it_ts.metrics GROUP BY 1, 2 WITH NO DATA",
	"SELECT add_continuous_aggregate_policy('it_ts.hourly', start_offset => INTERVAL '3 days', end_offset => INTERVAL '1 hour', schedule_interval => INTERVAL '1 hour')",
	"CREATE TABLE it_ts.plain (id int PRIMARY KEY)",
	"CREATE PROCEDURE it_ts.tick(job_id int, config jsonb) LANGUAGE plpgsql AS $$BEGIN END$$",
	`SELECT add_job('it_ts.tick', INTERVAL '1 hour', config => '{"a": 1}')`,
	"ANALYZE it_ts.metrics",
}

// TimescaleDB's own schemas are hidden; a hypertable lists as a table
// marked so, with its chunks' size and rows, and a continuous aggregate as
// a materialized view; its chunks and jobs are items. The definitions of
// the hypertable, the aggregate and the jobs make them again, as they were.
func TestIntegrationTimescaleDBCatalog(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d := open(t, fixtureOf(t, TimescaleDB))
	mustExec(t, d, timescaleSetup...)
	defer d.SQL.ExecContext(ctx, "DROP SCHEMA IF EXISTS it_ts CASCADE")
	schemas, err := d.Dialect.Schemas(ctx, d.SQL)
	if err != nil || !slices.Contains(schemas, "it_ts") || !slices.Contains(schemas, "public") ||
		slices.ContainsFunc(schemas, func(s string) bool { return strings.Contains(s, "timescaledb") }) {
		t.Fatalf("schemas %q: %v", schemas, err)
	}
	objs, err := d.Dialect.Objects(ctx, d.SQL, "it_ts")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Object{}
	for _, o := range objs {
		byName[o.Name] = o
	}
	var size int64
	if err := d.SQL.QueryRowContext(ctx, "SELECT hypertable_size('it_ts.metrics')").Scan(&size); err != nil {
		t.Fatal(err)
	}
	if m := byName["metrics"]; m.Kind != KindTable || m.Engine != "hypertable, compressed" || m.Bytes != size || m.Rows < 4000 {
		t.Errorf("hypertable %+v, size %d", m, size)
	}
	if h := byName["hourly"]; h.Kind != KindMaterializedView || h.Engine != "continuous aggregate" || h.Bytes <= 0 {
		t.Errorf("continuous aggregate %+v", h)
	}
	if p := byName["plain"]; p.Kind != KindTable || p.Engine != "" {
		t.Errorf("plain table %+v", p)
	}
	if len(objs) != 3 {
		t.Errorf("objects %+v", objs)
	}
	items, err := d.Dialect.Items(ctx, d.SQL, "it_ts")
	if err != nil {
		t.Fatal(err)
	}
	var chunks, compressed int
	var jobs []string
	for _, it := range items {
		switch it.Kind {
		case ItemChunk:
			chunks++
			if it.Table != "metrics" || !strings.HasPrefix(it.Detail, "t from 2026-01-") {
				t.Errorf("chunk %+v", it)
			}
			if strings.HasSuffix(it.Detail, ", compressed") {
				compressed++
			}
		case ItemJob:
			jobs = append(jobs, it.Label())
		}
	}
	if chunks != 4 || compressed < 2 || len(jobs) != 4 || !slices.ContainsFunc(jobs, func(j string) bool { return strings.HasSuffix(j, " on hourly") }) {
		t.Errorf("%d chunks, %d compressed, jobs %q", chunks, compressed, jobs)
	}

	// The definitions, run in a schema made again, make the same objects.
	read := func(q string) string {
		t.Helper()
		var out string
		if err := d.SQL.QueryRowContext(ctx, q).Scan(&out); err != nil {
			t.Fatal(q, err)
		}
		return out
	}
	const facts = `SELECT (SELECT string_agg(format('%s %s %s %s', column_name, dimension_type, time_interval, num_partitions), '; ' ORDER BY dimension_number)
    FROM timescaledb_information.dimensions WHERE hypertable_schema = 'it_ts')
  || ' | ' || (SELECT format('%s %s', segmentby, orderby) FROM timescaledb_information.hypertable_compression_settings WHERE hypertable = 'it_ts.metrics'::regclass)
  || ' | ' || (SELECT format('%s %s', materialized_only, view_definition) FROM timescaledb_information.continuous_aggregates WHERE view_schema = 'it_ts')
  || ' | ' || (SELECT string_agg(format('%s %s %s', proc_name, schedule_interval, config - 'hypertable_id' - 'mat_hypertable_id'), '; ' ORDER BY proc_name)
    FROM timescaledb_information.jobs WHERE coalesce(hypertable_schema, proc_schema) = 'it_ts')`
	before := read(facts)
	var definitions []string
	for _, o := range []Object{byName["metrics"], byName["hourly"]} {
		ddl, err := d.Dialect.DDL(ctx, d.SQL, "it_ts", o)
		if err != nil {
			t.Fatal(err)
		}
		definitions = append(definitions, ddl)
	}
	// The procedure before the job running it.
	slices.SortStableFunc(items, func(a, b Item) int { return strings.Compare(string(b.Kind), string(a.Kind)) })
	for _, it := range items {
		if it.Kind == ItemProcedure || it.Kind == ItemJob {
			ddl, err := d.Dialect.ItemDDL(ctx, d.SQL, it)
			if err != nil {
				t.Fatal(err)
			}
			definitions = append(definitions, ddl)
		}
	}
	if !strings.Contains(definitions[0], "SELECT create_hypertable(") || !strings.Contains(definitions[1], "timescaledb.continuous") {
		t.Errorf("definitions:\n%s", strings.Join(definitions, "\n"))
	}
	mustExec(t, d, "DROP SCHEMA it_ts CASCADE", "CREATE SCHEMA it_ts")
	for _, def := range definitions {
		for _, st := range sqltext.SplitWith(def, sqltext.Postgres, sqltext.SplitOptions{}) {
			mustExec(t, d, st.Text)
		}
	}
	if after := read(facts); after != before {
		t.Errorf("made again:\n%s\nwas:\n%s\ndefinitions:\n%s", after, before, strings.Join(definitions, "\n"))
	}
}

// A schema's chunks list the newest timescaleChunkItems, and an item
// saying how many more there are, whose definition says where they are.
func TestIntegrationTimescaleDBChunksCapped(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d := open(t, fixtureOf(t, TimescaleDB))
	mustExec(t, d, timescaleSetup...)
	defer d.SQL.ExecContext(ctx, "DROP SCHEMA IF EXISTS it_ts CASCADE")
	old := timescaleChunkItems
	timescaleChunkItems = 2
	t.Cleanup(func() { timescaleChunkItems = old })
	items, err := d.Dialect.Items(ctx, d.SQL, "it_ts")
	if err != nil {
		t.Fatal(err)
	}
	var chunks []Item
	for _, it := range items {
		if it.Kind == ItemChunk {
			chunks = append(chunks, it)
		}
	}
	if len(chunks) != 3 || chunks[0].Label() != "2 older chunks not listed" || !strings.HasPrefix(chunks[1].Detail, "t from 2026-01-03") ||
		!strings.HasPrefix(chunks[2].Detail, "t from 2026-01-04") {
		t.Fatalf("chunks %+v", chunks)
	}
	if ddl, err := d.Dialect.ItemDDL(ctx, d.SQL, chunks[0]); err != nil || !strings.Contains(ddl, "timescaledb_information.chunks") {
		t.Errorf("%q %v", ddl, err)
	}
}

// timescaleCatalogFailing passes queries to q but those of TimescaleDB's
// own catalog, which fail.
type timescaleCatalogFailing struct{ q Querier }

func (f timescaleCatalogFailing) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, "timescaledb_information") {
		return nil, errors.New("TimescaleDB's catalog is broken")
	}
	return f.q.QueryContext(ctx, query, args...)
}

// When TimescaleDB's own catalog cannot be read, the schema still lists
// as PostgreSQL's: its objects unmarked, its items without chunks and
// jobs, and a hypertable's definition PostgreSQL's, saying what it lacks.
func TestIntegrationTimescaleDBCatalogFailing(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d := open(t, fixtureOf(t, TimescaleDB))
	mustExec(t, d, timescaleSetup...)
	defer d.SQL.ExecContext(ctx, "DROP SCHEMA IF EXISTS it_ts CASCADE")
	q := timescaleCatalogFailing{d.SQL}
	objs, err := d.Dialect.Objects(ctx, q, "it_ts")
	if err != nil || len(objs) != 3 || slices.ContainsFunc(objs, func(o Object) bool { return o.Engine != "" }) {
		t.Fatalf("objects %+v: %v", objs, err)
	}
	items, err := d.Dialect.Items(ctx, q, "it_ts")
	if err != nil || len(items) == 0 || slices.ContainsFunc(items, func(it Item) bool { return it.Kind == ItemChunk || it.Kind == ItemJob }) {
		t.Errorf("items %+v: %v", items, err)
	}
	for _, o := range objs {
		if o.Name != "metrics" && o.Name != "hourly" {
			continue
		}
		ddl, err := d.Dialect.DDL(ctx, q, "it_ts", o)
		if err != nil || strings.Contains(ddl, "create_hypertable") || !strings.Contains(ddl, "TimescaleDB's catalog is broken") {
			t.Errorf("%s: %q %v", o.Name, ddl, err)
		}
	}
}

// Search finds a hypertable, not its chunks in TimescaleDB's own schemas,
// nor TimescaleDB's views.
func TestIntegrationTimescaleDBSearch(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d := open(t, fixtureOf(t, TimescaleDB))
	mustExec(t, d, timescaleSetup...)
	defer d.SQL.ExecContext(ctx, "DROP SCHEMA IF EXISTS it_ts CASCADE")
	for text, want := range map[string]bool{"metrics": true, "_hyper_": false, "hypertable": false, "_materialized_hypertable": false} {
		hits, _, err := Search(ctx, d, text, false)
		if err != nil {
			t.Fatal(err)
		}
		inside := slices.ContainsFunc(hits, func(h SearchHit) bool {
			return strings.Contains(h.Schema, "timescaledb")
		})
		found := slices.ContainsFunc(hits, func(h SearchHit) bool { return h.Schema == "it_ts" })
		if inside || found != want {
			t.Errorf("%q: %+v", text, hits)
		}
	}
}

// A database that has the extension to create, not created, still
// identifies as TimescaleDB, by the version it would create, and reads
// as PostgreSQL's; one that created it identifies by its version, set as
// PostgreSQL or as TimescaleDB.
func TestIntegrationTimescaleDBIdentify(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d := open(t, fixtureOf(t, TimescaleDB))
	var installed string
	if err := d.SQL.QueryRowContext(ctx, "SELECT extversion FROM pg_extension WHERE extname = 'timescaledb'").Scan(&installed); err != nil {
		t.Fatal(err)
	}
	for _, e := range []Engine{TimescaleDB, Postgres} {
		cfg := timescaledbConfig
		cfg.Engine = e
		other, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, v, err := other.Identify(ctx)
		other.Close()
		if err != nil || got != TimescaleDB || v != installed {
			t.Errorf("set as %s: %s %q %v", e, got, v, err)
		}
	}
	d.SQL.ExecContext(ctx, "DROP DATABASE IF EXISTS it_ts_none")
	mustExec(t, d, "CREATE DATABASE it_ts_none TEMPLATE template0")
	defer d.SQL.ExecContext(ctx, "DROP DATABASE IF EXISTS it_ts_none")
	none, err := d.Database(ctx, "it_ts_none")
	if err != nil {
		t.Fatal(err)
	}
	defer none.Close()
	if got, v, err := none.Identify(ctx); err != nil || got != TimescaleDB || v == "" {
		t.Errorf("without the extension: %s %q %v", got, v, err)
	}
	mustExec(t, none, "CREATE TABLE plain (id int PRIMARY KEY)")
	objs, err := none.Dialect.Objects(ctx, none.SQL, "public")
	if err != nil || len(objs) != 1 || objs[0].Engine != "" {
		t.Errorf("objects %+v: %v", objs, err)
	}
	if items, err := none.Dialect.Items(ctx, none.SQL, "public"); err != nil || len(items) != 0 {
		t.Errorf("items %+v: %v", items, err)
	}
	if ddl, err := none.Dialect.DDL(ctx, none.SQL, "public", objs[0]); err != nil || strings.Contains(ddl, "hypertable") {
		t.Errorf("DDL %q: %v", ddl, err)
	}
	if hits, _, err := Search(ctx, none, "plain", false); err != nil || len(hits) == 0 {
		t.Errorf("search %+v: %v", hits, err)
	}
}

// The Jobs page lists the database's jobs, with how their runs went.
func TestIntegrationTimescaleDBActivity(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d := open(t, fixtureOf(t, TimescaleDB))
	mustExec(t, d, timescaleSetup...)
	defer d.SQL.ExecContext(ctx, "DROP SCHEMA IF EXISTS it_ts CASCADE")
	r, _ := SQL(TimescaleDB)
	pages := r.ActivityPages()
	jobs := pages[len(pages)-1]
	rows, err := d.SQL.QueryContext(ctx, jobs.Query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var on []string
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		on = append(on, Display(values[2]))
	}
	if !slices.Contains(on, "it_ts.metrics") || !slices.Contains(on, "it_ts.hourly") || cols[2] != "on" {
		t.Errorf("jobs on %q, columns %q", on, cols)
	}
}

// An archive of a database with a hypertable, written by pg_dump,
// restores with pg_restore into a database that has not created the
// extension, and into one that has; after it, TimescaleDB is no longer
// told a restore runs.
func TestIntegrationTimescaleDBBackup(t *testing.T) {
	integration(t)
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	ctx := context.Background()
	d := open(t, fixtureOf(t, TimescaleDB))
	r, _ := SQL(TimescaleDB)
	for _, q := range []string{"DROP DATABASE IF EXISTS it_ts_from", "DROP DATABASE IF EXISTS it_ts_into", "DROP DATABASE IF EXISTS it_ts_bare"} {
		d.SQL.ExecContext(ctx, q)
	}
	mustExec(t, d, "CREATE DATABASE it_ts_from", "CREATE DATABASE it_ts_into", "CREATE DATABASE it_ts_bare TEMPLATE template0")
	defer func() {
		for _, q := range []string{"DROP DATABASE IF EXISTS it_ts_from", "DROP DATABASE IF EXISTS it_ts_into", "DROP DATABASE IF EXISTS it_ts_bare"} {
			d.SQL.ExecContext(ctx, q)
		}
	}()
	from, err := d.Database(ctx, "it_ts_from")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, from, timescaleSetup...)
	from.Close()
	path := filepath.Join(t.TempDir(), "it.dump")
	run, err := r.PlanBackup(ctx, d, BackupOptions{Format: BackupArchive, Content: BackupAll, Database: "it_ts_from", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := RunTool(ctx, run, func(string) {}); err != nil {
		t.Fatalf("%q: %v", run.Argv, err)
	}
	for _, database := range []string{"it_ts_into", "it_ts_bare"} {
		into, err := d.Database(ctx, database)
		if err != nil {
			t.Fatal(err)
		}
		defer into.Close()
		run, err := r.PlanRestore(ctx, d, database, path, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := RunPlanned(ctx, run, func(context.Context) (*DB, error) { return into, nil }, func(string) {}); err != nil {
			t.Fatalf("%s: %v", database, err)
		}
		var rows, chunks int
		var restoring string
		err = into.SQL.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM it_ts.metrics), (SELECT count(*) FROM timescaledb_information.chunks),
  coalesce((SELECT array_to_string(setconfig, ',') FROM pg_db_role_setting WHERE setdatabase = (SELECT oid FROM pg_database WHERE datname = current_database()) AND setrole = 0), '')`).
			Scan(&rows, &chunks, &restoring)
		if err != nil || rows != 5000 || chunks != 4 || strings.Contains(restoring, "timescaledb.restoring") {
			t.Errorf("%s: %d rows, %d chunks, settings %q: %v", database, rows, chunks, restoring, err)
		}
	}
}

// A restore by a user who is not a superuser stops at
// timescaledb_pre_restore(), saying it needs one, and runs neither the
// tool nor timescaledb_post_restore(): the refused call changed nothing.
func TestIntegrationTimescaleDBRestoreNeedsSuperuser(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d := open(t, fixtureOf(t, TimescaleDB))
	d.SQL.ExecContext(ctx, "DROP ROLE IF EXISTS it_ts_plain")
	mustExec(t, d, "CREATE ROLE it_ts_plain LOGIN PASSWORD 'plain'")
	defer d.SQL.ExecContext(ctx, "DROP ROLE IF EXISTS it_ts_plain")
	cfg := timescaledbConfig
	cfg.User, cfg.Password = "it_ts_plain", "plain"
	plain, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	r, _ := SQL(TimescaleDB)
	run, err := r.PlanRestore(ctx, plain, "", filepath.Join(t.TempDir(), "x.dump"), false)
	if err != nil {
		t.Fatal(err)
	}
	run.Argv = []string{"dgopher-no-such-tool"}
	err = RunPlanned(ctx, run, func(context.Context) (*DB, error) { return plain, nil }, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "needs a superuser") || strings.Contains(err.Error(), "dgopher-no-such-tool") ||
		strings.Contains(err.Error(), "restart_background_workers") {
		t.Errorf("%v", err)
	}
}
