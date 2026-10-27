package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// cockroachdbDialect is PostgreSQL's Dialect but for the engine it names, so that
// the rules found through it are CockroachDB's, and for its catalog, where
// pg_catalog leaves out or misreports what CockroachDB keeps elsewhere.
// The catalog reads only what CockroachDB supports reading: pg_catalog,
// information_schema and SHOW statements, as crdb_internal is closed to
// sessions by default from 26.1 (allow_unsafe_internals).
type cockroachdbDialect struct{ postgresDialect }

func (cockroachdbDialect) Engine() Engine { return CockroachDB }

func (cockroachdbDialect) Schemas(ctx context.Context, q Querier) ([]string, error) {
	return queryStrings(ctx, q, `SELECT nspname FROM pg_namespace
WHERE nspname NOT LIKE 'pg\_%' AND nspname NOT IN ('information_schema', 'crdb_internal')
ORDER BY nspname = 'public' DESC, nspname`)
}

func (cockroachdbDialect) Objects(ctx context.Context, q Querier, schema string) ([]Object, error) {
	objs, err := cockroachObjects(ctx, q, schema, true, true)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "42501" {
		// The sizes need the VIEWACTIVITY or ZONECONFIG privilege:
		// without it, none.
		return cockroachObjects(ctx, q, schema, true, false)
	}
	return objs, err
}

func (cockroachdbDialect) ObjectNames(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return cockroachObjects(ctx, q, schema, false, false)
}

// cockroachObjects reads a schema's tables and views: their estimated
// rows and comments when described, and their sizes when sized, which
// CockroachDB gives from its ranges' span statistics, the database's read
// once.
func cockroachObjects(ctx context.Context, q Querier, schema string, described, sized bool) ([]Object, error) {
	rows, size, comment, joins := "-1", "-1", "''", ""
	if described {
		rows = "CASE WHEN c.relkind IN ('r', 'm') THEN COALESCE(s.estimated_row_count, -1) ELSE -1 END"
		comment = "COALESCE(obj_description(c.oid, 'pg_class'), '')"
		joins += "\nLEFT JOIN [SHOW TABLES] s ON s.schema_name = n.nspname AND s.table_name = c.relname"
	}
	if sized {
		size = "CASE WHEN c.relkind IN ('r', 'm') THEN COALESCE(z.live_bytes, -1) ELSE -1 END"
		joins += "\nLEFT JOIN (SELECT table_name, sum((span_stats->>'live_bytes')::INT8) AS live_bytes FROM [SHOW RANGES WITH TABLES, DETAILS]" +
			"\n  WHERE schema_name = $1 GROUP BY table_name) z ON z.table_name = c.relname"
	}
	return scanAll(ctx, q, `SELECT c.relname, c.relkind::text, `+rows+`, `+size+`, `+comment+`
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace`+joins+`
WHERE n.nspname = $1 AND c.relkind IN ('r', 'v', 'm')
ORDER BY c.relname`, []any{schema}, func(scan func(...any) error) (Object, error) {
		o := Object{Schema: schema}
		var kind string
		if err := scan(&o.Name, &kind, &o.Rows, &o.Bytes, &o.Comment); err != nil {
			return o, err
		}
		o.Kind = postgresObjectKind(kind)
		return o, nil
	})
}

// cockroachColumnsFrom leaves out the columns CockroachDB hides, as the
// rowid of a table without a primary key, which SELECT * does not give.
// Joined by name: a column's attnum is not its ID once its type changed.
var cockroachColumnsFrom = columnsFrom{relations: "c.relkind IN ('r', 'v', 'm')", hidden: cockroachHiddenColumns}

const cockroachHiddenColumns = "JOIN information_schema.columns ic ON ic.table_schema = n.nspname AND ic.table_name = c.relname AND ic.column_name = a.attname AND ic.is_hidden = 'NO'\n"

func (cockroachdbDialect) Columns(ctx context.Context, q Querier, schema, table string) ([]Column, error) {
	cols, err := postgresColumns(ctx, q, schema, table, cockroachColumnsFrom)
	return cols[table], err
}

func (cockroachdbDialect) SchemaColumns(ctx context.Context, q Querier, schema string) (map[string][]Column, error) {
	return postgresColumns(ctx, q, schema, "", cockroachColumnsFrom)
}

// DDL is the table's or view's CREATE as CockroachDB writes it.
func (d cockroachdbDialect) DDL(ctx context.Context, q Querier, schema string, obj Object) (string, error) {
	ddl, err := showCreate(ctx, q, "SHOW CREATE "+QualifiedName(d, schema, obj.Name), "create_statement")
	return strings.TrimRight(ddl, "; \n") + ";\n", err
}

// Items lists routines, triggers, sequences and types. Triggers come from
// information_schema: pg_trigger lists none.
func (cockroachdbDialect) Items(ctx context.Context, q Querier, schema string) ([]Item, error) {
	return scanItems(ctx, q, schema, `SELECT CASE p.prokind WHEN 'p' THEN 'procedure' ELSE 'function' END, p.proname, '',
  pg_get_function_identity_arguments(p.oid), p.oid::text
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = $1
UNION ALL
SELECT DISTINCT 'trigger', trigger_name, event_object_table, '', ''
FROM information_schema.triggers WHERE event_object_schema = $1
UNION ALL
SELECT 'sequence', c.relname, '', '', c.oid::text
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind = 'S'
UNION ALL
SELECT 'type', s.name, '', CASE t.typtype WHEN 'e' THEN 'enum' ELSE 'composite' END, t.oid::text
FROM [SHOW TYPES] s JOIN pg_namespace n ON n.nspname = s.schema
JOIN pg_type t ON t.typnamespace = n.oid AND t.typname = s.name
WHERE s.schema = $1
ORDER BY 1, 2, 4`, schema)
}

// ItemDDL writes an item's definition as CockroachDB does, a type's from
// pg_catalog as PostgreSQL's is: pg_catalog has no trigger's.
func (d cockroachdbDialect) ItemDDL(ctx context.Context, q Querier, it Item) (string, error) {
	var ddl string
	var err error
	switch it.Kind {
	case ItemFunction, ItemProcedure:
		ddl, err = queryString(ctx, q, `SELECT pg_get_functiondef($1::oid)`, it.ID)
	case ItemTrigger:
		ddl, err = showCreate(ctx, q, "SHOW CREATE TRIGGER "+d.Quote(it.Name)+" ON "+QualifiedName(d, it.Schema, it.Table), "create_statement")
	case ItemSequence:
		ddl, err = showCreate(ctx, q, "SHOW CREATE "+QualifiedName(d, it.Schema, it.Name), "create_statement")
	case ItemType:
		ddl, err = d.postgresDialect.ItemDDL(ctx, q, it)
	default:
		return "", errNoDefinition(it)
	}
	if err != nil {
		return "", err
	}
	if ddl == "" {
		return "", fmt.Errorf("CockroachDB gave no definition of the %s %s", it.Kind, it.Name)
	}
	if it.Kind == ItemFunction || it.Kind == ItemProcedure {
		return ddl, nil
	}
	return strings.TrimRight(ddl, "; \n") + ";", nil
}

// cockroachdbEngine is CockroachDB: PostgreSQL's rules but for its name, how it is
// reached by default, and what its server does otherwise.
type cockroachdbEngine struct{ postgresEngine }

var _ SQLRules = cockroachdbEngine{}

func (cockroachdbEngine) Engine() Engine       { return CockroachDB }
func (cockroachdbEngine) dialect() Dialect     { return cockroachdbDialect{} }
func (cockroachdbEngine) Label() string        { return "CockroachDB" }
func (cockroachdbEngine) Base() Engine         { return Postgres }
func (cockroachdbEngine) ColorHex() string     { return "#6933ff" }
func (cockroachdbEngine) DefaultPort() int     { return 26257 }
func (cockroachdbEngine) DefaultUser() string  { return "root" }
func (cockroachdbEngine) URLSchemes() []string { return []string{"cockroachdb", "cockroach"} }

func (cockroachdbEngine) CreateFile(context.Context, string) error { return noFileError(CockroachDB) }

// TransactionalDDL: CockroachDB commits the open transaction before a
// schema change (autocommit_before_ddl, on by default since 25.1), and
// runs the change after its own commit.
func (cockroachdbEngine) TransactionalDDL() bool { return false }

func (cockroachdbEngine) ReplacesPrimaryKeyAtOnce() bool { return true }

// commitsImplicitlyWords: CockroachDB commits the open transaction before
// the statements that change the schema, privileges or statistics.
func (cockroachdbEngine) commitsImplicitlyWords(words []string, _ func() []string) bool {
	switch wordAt(words, 0) {
	case "CREATE", "ALTER", "DROP", "TRUNCATE", "GRANT", "REVOKE", "COMMENT", "ANALYZE", "ANALYSE", "REASSIGN", "REFRESH":
		return true
	}
	return false
}

// OutsideTransaction: CockroachDB refuses a cluster setting, BACKUP,
// RESTORE, IMPORT, EXPORT and a changefeed in a transaction.
func (cockroachdbEngine) OutsideTransaction(sql string) bool {
	words := nameWords(sql, LexDialect(CockroachDB))
	switch wordAt(words, 0) {
	case "SET", "RESET":
		return wordAt(words, 1) == "CLUSTER"
	case "BACKUP", "RESTORE", "IMPORT", "EXPORT":
		return true
	case "CREATE":
		return wordAt(words, 1) == "CHANGEFEED"
	}
	return false
}

func (cockroachdbEngine) Explain(statement string, analyze bool) (ExplainRun, bool) {
	if analyze {
		return explainWithPrefix("EXPLAIN ANALYZE (VERBOSE) ", statement), true
	}
	return explainWithPrefix("EXPLAIN (VERBOSE) ", statement), true
}

func (cockroachdbEngine) ParsePlan(_ []string, rows [][]any) (*Plan, bool) {
	return parseCockroachPlan(planText(rows))
}

func (cockroachdbEngine) AdvisePlanStep(n *PlanNode, add func(n *PlanNode, format string, args ...any)) {
	if index := n.Prop("index recommendation"); index != "" {
		add(n, "CockroachDB recommends an index: %s", index)
	}
	if n.Op == "scan" && n.Prop("spans") == "FULL SCAN" && rowsOf(n) >= adviseRows {
		table, _, _ := strings.Cut(n.Target, " using ")
		add(n, "A full scan reads every row of %s: an index on the columns of the WHERE may let CockroachDB read only those it keeps.", table)
	}
}

func (cockroachdbEngine) Search(ctx context.Context, d *DB, text string, definitions bool) ([]SearchHit, bool, error) {
	text = strings.ToLower(text)
	return searchWith(ctx, d, text, cockroachSearch, []any{text, definitions})
}

// cockroachSessionStatement returns the statement acting on the session a
// row's first column names, its ID quoted.
func cockroachSessionStatement(format string) func(row []any) string {
	return func(r []any) string { return fmt.Sprintf(format, standardLiteral(Display(r[0]))) }
}

const (
	cockroachStopQuery  = "CANCEL QUERIES (SELECT query_id FROM [SHOW CLUSTER QUERIES] WHERE session_id = %s)"
	cockroachEndSession = "CANCEL SESSION %s"
)

// ActivityPages: CockroachDB keeps no pg_stat_activity, pg_locks or
// pg_stat_statements of its own; the crdb_internal tables it supports
// reading list what every node of the cluster runs.
func (cockroachdbEngine) ActivityPages() []ActivityPage {
	stop, end := cockroachSessionStatement(cockroachStopQuery), cockroachSessionStatement(cockroachEndSession)
	return []ActivityPage{{
		Name: "Sessions", Live: true, Stop: stop, Kill: end,
		Query: `SELECT session_id, user_name AS "user", application_name AS application, client_address AS client, node_id AS node, status,
  (now() - active_query_start)::STRING AS running_for, left(active_queries, 400) AS query
FROM crdb_internal.cluster_sessions WHERE session_id <> current_setting('session_id') AND status <> 'CLOSED'
ORDER BY active_queries = '', active_query_start`,
		Hint: "Sessions come from crdb_internal.cluster_sessions, of every node; without the VIEWACTIVITY privilege the user sees only its own.",
	}, {
		Name: "Locks", Stop: stop, Kill: end,
		Query: `SELECT t.session_id, concat_ws('.', l.schema_name, l.table_name) AS object, l.index_name, l.lock_key_pretty AS row_key,
  l.lock_strength AS mode, l.granted,
  CASE WHEN NOT l.granted THEN (SELECT string_agg(h.session_id, ', ') FROM crdb_internal.cluster_locks g
    JOIN crdb_internal.cluster_transactions h ON h.id = g.txn_id WHERE g.lock_key = l.lock_key AND g.granted) END AS waits_for,
  l.duration::STRING AS held_for, left(q.query, 300) AS query
FROM crdb_internal.cluster_locks l JOIN crdb_internal.cluster_transactions t ON t.id = l.txn_id
LEFT JOIN crdb_internal.cluster_queries q ON q.session_id = t.session_id
WHERE t.session_id <> current_setting('session_id')
ORDER BY l.granted, l.duration DESC`,
		Hint: "Locks come from crdb_internal.cluster_locks, which reads every range's locks, so the page reads it only when asked; the user needs the VIEWACTIVITY privilege.",
	}, {
		Name: "Statistics",
		Query: `SELECT left(query, 400) AS query, database, sum(cnt)::INT8 AS calls, round(sum(cnt * mean) * 1000, 1) AS total_ms,
  round(sum(cnt * mean) * 1000 / sum(cnt), 2) AS mean_ms, round(max(max) * 1000, 1) AS max_ms, round(sum(cnt * rows_read)) AS rows_read
FROM (SELECT metadata->>'query' AS query, metadata->>'db' AS database, (statistics->'statistics'->>'cnt')::FLOAT8 AS cnt,
    (statistics->'statistics'->'svcLat'->>'mean')::FLOAT8 AS mean, (statistics->'statistics'->'latencyInfo'->>'max')::FLOAT8 AS max,
    (statistics->'statistics'->'rowsRead'->>'mean')::FLOAT8 AS rows_read
  FROM crdb_internal.statement_statistics WHERE app_name NOT LIKE '$ internal%') s
GROUP BY query, database ORDER BY total_ms DESC LIMIT 500`,
		Hint:      "Statistics come from crdb_internal.statement_statistics, of every node.",
		EmptyHint: "CockroachDB keeps statements' statistics by the hour, and flushes them from each node's memory every 10 minutes (sql.stats.flush.interval); crdb_internal.statement_statistics shows both.",
	}, {
		Name: "Jobs",
		Query: `SELECT job_id, job_type AS type, status, user_name AS "user", created, round(fraction_completed * 100) AS percent_done,
  left(description, 400) AS description, error
FROM [SHOW JOBS] ORDER BY status IN ('running', 'paused', 'pending') DESC, created DESC LIMIT 500`,
		Hint: "Jobs come from SHOW JOBS: those running, and those of the last 12 hours (information_schema.crdb_jobs keeps older ones); schema changes, backups, imports and changefeeds among them. The user needs the VIEWJOB privilege to see others' jobs. CANCEL JOB and PAUSE JOB, run in an editor, act on one.",
	}}
}

// ListAccounts leaves out node, the user CockroachDB's nodes run as.
func (cockroachdbEngine) ListAccounts(ctx context.Context, q Querier) ([]Account, error) {
	return listPostgresAccounts(ctx, q, "r.rolname !~ '^pg_' AND r.rolname <> 'node'")
}

// cockroachGrantObjects are the objects SHOW GRANTS lists whose
// privileges an account's page shows, with the word a REVOKE names them by.
var cockroachGrantObjects = map[string]string{"database": "DATABASE", "schema": "SCHEMA", "table": "TABLE", "sequence": "SEQUENCE"}

// AccountPrivileges reads SHOW GRANTS: CockroachDB keeps no ACLs in
// pg_catalog. System privileges come from SHOW SYSTEM GRANTS.
func (cockroachdbEngine) AccountPrivileges(ctx context.Context, q Querier, a Account) ([]Privilege, error) {
	d := cockroachdbDialect{}
	account := d.Quote(a.Name)
	out, err := scanAll(ctx, q, `SELECT object_type, CASE object_type WHEN 'database' THEN quote_ident(database_name) WHEN 'schema' THEN quote_ident(schema_name)
    ELSE quote_ident(schema_name) || '.' || quote_ident(object_name) END,
  string_agg(privilege_type, ', ' ORDER BY privilege_type)
FROM [SHOW GRANTS FOR `+account+`] WHERE grantee = $1 AND object_type IN ('database', 'schema', 'table', 'sequence')
GROUP BY 1, 2 ORDER BY object_type = 'table' OR object_type = 'sequence', 1, 2`, []any{a.Name}, func(scan func(...any) error) (Privilege, error) {
		var kind, name, privs string
		if err := scan(&kind, &name, &privs); err != nil {
			return Privilege{}, err
		}
		return Privilege{Text: privs + " on " + kind + " " + name,
			Revoke: "REVOKE " + privs + " ON " + cockroachGrantObjects[kind] + " " + name + " FROM " + account}, nil
	})
	if err != nil {
		return nil, err
	}
	system, err := queryStrings(ctx, q, `SELECT privilege_type FROM [SHOW SYSTEM GRANTS FOR `+account+`] WHERE grantee = $1 ORDER BY 1`, a.Name)
	if err != nil {
		return nil, err
	}
	for _, p := range system {
		out = append(out, Privilege{Text: p + " on the cluster", Revoke: "REVOKE SYSTEM " + p + " FROM " + account})
	}
	return append(out, roleMemberships(d, a)...), nil
}

func (cockroachdbEngine) AccountForm() AccountFormFields {
	return AccountFormFields{CreateOptions: true, GrantTargetLabel: "Schema",
		PrivilegesNote: "Those on the database and its schemas, tables and sequences, and on the cluster."}
}

func (cockroachdbEngine) BackupFormats() []BackupFormat { return []BackupFormat{BackupSQL} }

// PlanBackup: pg_dump writes a CockroachDB database's schema, from
// CockroachDB 26.3 with pg_dump 18, under pg_dump_compatibility, which
// CockroachDB sets itself only for a client named pg_dump.
func (cockroachdbEngine) PlanBackup(ctx context.Context, d *DB, o BackupOptions) (ToolRun, error) {
	o.Content = BackupSchemaOnly
	run, err := planPgDump(ctx, d, o)
	if err == nil {
		run.Env = append(run.Env, "PGOPTIONS=-c pg_dump_compatibility=cockroachdb")
	}
	return run, err
}

// PlanRestore: a schema dump is a SQL file, which Run SQL File runs.
func (cockroachdbEngine) PlanRestore(_ context.Context, d *DB, _, _ string, _ bool) (ToolRun, error) {
	return ToolRun{}, restoreWithRunSQLFileError(d.Config.Engine)
}

func (cockroachdbEngine) BackupChoices() BackupChoices { return BackupChoices{} }

func (cockroachdbEngine) BackupNote(restore bool) string {
	return backupNoteFor(restore,
		"A SQL file, as pg_dump writes, runs as Run SQL File runs it.",
		"pg_dump writes the schema, from CockroachDB 26.3 with pg_dump 18 or later; Export or Copy Tables take the data, and CockroachDB's own BACKUP the whole database.")
}

// cockroachFileReaders are CockroachDB's functions that read the server's
// files: crdb_internal.read_file.
var cockroachFileReaders = []string{"read_file"}

func (cockroachdbEngine) FileReadingFunctions() []string { return cockroachFileReaders }

// MaintenanceCommands: CockroachDB collects garbage and compacts on its
// own; ANALYZE refreshes a table's statistics.
func (cockroachdbEngine) MaintenanceCommands(quotedTable string) []MaintenanceCommand {
	return []MaintenanceCommand{{"Analyze", "ANALYZE " + quotedTable}}
}

// cockroachSearch is postgresSearch for CockroachDB's catalog: no
// partitions, foreign tables or extensions, hidden columns left out,
// triggers from information_schema, types from SHOW TYPES.
const cockroachSearch = `SELECT s, kind, name, tbl, tkind, detail, id, CASE WHEN $2 THEN def ELSE '' END FROM (
SELECT n.nspname AS s, CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized view' ELSE 'table' END AS kind, c.relname AS name,
  '' AS tbl, '' AS tkind, '' AS detail, c.oid::text AS id,
  CASE WHEN c.relkind IN ('v', 'm') THEN pg_get_viewdef(c.oid) ELSE '' END AS def
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'v', 'm')
UNION ALL
SELECT n.nspname, 'column', a.attname, c.relname,
  CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized view' ELSE 'table' END,
  format_type(a.atttypid, a.atttypmod), '', ''
FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
` + cockroachHiddenColumns + `WHERE c.relkind IN ('r', 'v', 'm') AND a.attnum > 0 AND NOT a.attisdropped
UNION ALL
SELECT n.nspname, CASE p.prokind WHEN 'p' THEN 'procedure' ELSE 'function' END, p.proname, '', '',
  pg_get_function_identity_arguments(p.oid), p.oid::text, COALESCE(p.prosrc, '')
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
UNION ALL
SELECT DISTINCT event_object_schema, 'trigger', trigger_name, event_object_table, '', '', '', ''
FROM information_schema.triggers
UNION ALL
SELECT n.nspname, 'sequence', c.relname, '', '', '', c.oid::text, ''
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'S'
UNION ALL
SELECT s.schema, 'type', s.name, '', '', CASE t.typtype WHEN 'e' THEN 'enum' ELSE 'composite' END, t.oid::text, ''
FROM [SHOW TYPES] s JOIN pg_namespace n ON n.nspname = s.schema
JOIN pg_type t ON t.typnamespace = n.oid AND t.typname = s.name
) found
WHERE s NOT LIKE 'pg\_%' AND s NOT IN ('information_schema', 'crdb_internal')
  AND (strpos(lower(name), $1) > 0 OR ($2 AND strpos(lower(def), $1) > 0))`
