package db

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// yugabytedbDialect is PostgreSQL's Dialect but for the engine it names, so that
// the rules found through it are YugabyteDB's, and for its tables' sizes.
type yugabytedbDialect struct{ postgresDialect }

func (yugabytedbDialect) Engine() Engine { return YugabyteDB }

func (yugabytedbDialect) Objects(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return postgresObjects(ctx, q, schema, yugabyteTableSize)
}

// yugabyteTableSize: YugabyteDB's pg_total_relation_size is a table's
// files alone, its secondary indexes having their own and its primary key
// none, and NULL for a table of a colocated database, which shares its
// database's tablet.
const yugabyteTableSize = "COALESCE(pg_total_relation_size(c.oid) + (SELECT COALESCE(sum(pg_total_relation_size(i.indexrelid)), 0) " +
	"FROM pg_index i WHERE i.indrelid = c.oid AND NOT i.indisprimary), -1)"

// yugabytedbEngine is YugabyteDB: PostgreSQL's rules but for its name, how it is
// reached by default, and what its server does otherwise.
type yugabytedbEngine struct{ postgresEngine }

var _ SQLRules = yugabytedbEngine{}

func (yugabytedbEngine) Engine() Engine       { return YugabyteDB }
func (yugabytedbEngine) dialect() Dialect     { return yugabytedbDialect{} }
func (yugabytedbEngine) Label() string        { return "YugabyteDB" }
func (yugabytedbEngine) Base() Engine         { return Postgres }
func (yugabytedbEngine) ColorHex() string     { return "#ff6e42" }
func (yugabytedbEngine) DefaultPort() int     { return 5433 }
func (yugabytedbEngine) DefaultUser() string  { return "yugabyte" }
func (yugabytedbEngine) URLSchemes() []string { return []string{"yugabytedb"} }

func (yugabytedbEngine) CreateFile(context.Context, string) error { return noFileError(YugabyteDB) }

// TransactionalDDL: YugabyteDB runs a schema change in a transaction of
// its own, committed at once, whatever transaction is open, which stays
// open (yb_ddl_transaction_block_enabled, off by default).
func (yugabytedbEngine) TransactionalDDL() bool { return false }

// OutsideTransaction: PostgreSQL's, CREATE TABLEGROUP, and CREATE
// [UNIQUE] INDEX but NONCONCURRENTLY, which inside a transaction is built
// without online backfill, unsafe while the table is written to.
func (y yugabytedbEngine) OutsideTransaction(sql string) bool {
	words := nameWords(sql, LexDialect(YugabyteDB))
	if wordAt(words, 0) == "CREATE" {
		index := 1
		if wordAt(words, 1) == "UNIQUE" {
			index = 2
		}
		switch {
		case wordAt(words, 1) == "TABLEGROUP":
			return true
		case wordAt(words, index) == "INDEX" && wordAt(words, index+1) != "NONCONCURRENTLY":
			return true
		}
	}
	return y.postgresEngine.OutsideTransaction(sql)
}

// Explain: DIST adds what each step read from storage, where YugabyteDB
// runs most filters.
func (yugabytedbEngine) Explain(statement string, analyze bool) (ExplainRun, bool) {
	if analyze {
		return explainWithPrefix("EXPLAIN (ANALYZE, DIST, VERBOSE, FORMAT JSON) ", statement), true
	}
	return explainWithPrefix("EXPLAIN (VERBOSE, FORMAT JSON) ", statement), true
}

// ParsePlan reads PostgreSQL's JSON, a filter YugabyteDB runs in storage
// the detail of a step that has no other.
func (yugabytedbEngine) ParsePlan(_ []string, rows [][]any) (*Plan, bool) {
	p, ok := parsePostgresPlan(planText(rows))
	if !ok {
		return p, ok
	}
	p.Root.Walk(func(n *PlanNode, _ int) {
		for _, key := range []string{"Storage Index Filter", "Storage Filter"} {
			if v := n.Prop(key); n.Detail == "" && v != "" {
				n.Detail = key + ": " + v
			}
		}
	})
	return p, true
}

// AdvisePlanStep: a scan whose storage filter keeps few of the rows it
// read, as DIST counts them a loop, is advised an index; the rest as on
// PostgreSQL.
func (y yugabytedbEngine) AdvisePlanStep(n *PlanNode, add func(n *PlanNode, format string, args ...any)) {
	filter := n.Prop("Storage Filter")
	scanned, err := strconv.ParseFloat(n.Prop("Storage Table Rows Scanned"), 64)
	if strings.HasPrefix(n.Op, "Seq Scan") && filter != "" && err == nil && n.Loops > 0 {
		if removed := scanned*n.Loops - n.ActualRows; removed >= adviseRows && removed > 10*rowsOf(n) {
			add(n, "Seq Scan reads every row of %s to keep the few of %s: an index on (%s) may let it read only those.", n.Target, filter, columnsIn(filter))
			return
		}
	}
	y.postgresEngine.AdvisePlanStep(n, add)
}

// ActivityPages: each node keeps its own sessions, statement statistics
// and samples of waits; pg_locks lists the cluster's locks, a session by
// its transaction.
func (yugabytedbEngine) ActivityPages() []ActivityPage {
	stop, end := sessionStatement(postgresStopQuery), sessionStatement(postgresEndSession)
	// A lock held on another node has no pid here.
	ifSession := func(statement func([]any) string) func([]any) string {
		return func(row []any) string {
			if row[0] == nil {
				return ""
			}
			return statement(row)
		}
	}
	return []ActivityPage{{
		Name: "Sessions (this node)", Live: true, Stop: stop, Kill: end, Query: postgresSessionsQuery,
		Hint: "Sessions come from pg_stat_activity of the node the connection reached; each node lists its own.",
	}, {
		Name: "Locks", Live: true, Stop: ifSession(stop), Kill: ifSession(end),
		Query: `SELECT a.pid, a.usename AS "user", l.locktype AS lock_type, l.relation::regclass::text AS object,
  array_to_string(ARRAY(SELECT jsonb_array_elements_text(CASE jsonb_typeof(l.ybdetails->'keyrangedetails'->'cols')
    WHEN 'array' THEN l.ybdetails->'keyrangedetails'->'cols' END)), ', ') AS row_key,
  l.mode, l.granted,
  (SELECT string_agg(coalesce(b.pid::text, w.id), ', ') FROM jsonb_array_elements_text(CASE jsonb_typeof(l.ybdetails->'blocked_by')
    WHEN 'array' THEN l.ybdetails->'blocked_by' END) w(id) LEFT JOIN pg_stat_activity b ON b.yb_backend_xid::text = w.id) AS waits_for,
  l.ybdetails->>'node' AS node, l.ybdetails->>'transactionid' AS transaction, date_trunc('second', now() - a.xact_start)::text AS transaction_for,
  left(a.query, 300) AS query
FROM pg_locks l LEFT JOIN pg_stat_activity a ON a.yb_backend_xid::text = l.ybdetails->>'transactionid'
WHERE a.pid IS DISTINCT FROM pg_backend_pid()
ORDER BY l.granted, a.xact_start`,
		Hint:      "Locks come from pg_locks, of every node; a session on another node shows its transaction's ID, not its pid.",
		EmptyHint: "pg_locks lists the locks of transactions open for a second or more (yb_locks_min_txn_age), of 16 transactions at most (yb_locks_max_transactions).",
		NoSession: "This lock is held by a transaction on another node, which has no session on this one: connect to that node to stop it, or cancel the transaction, by the ID in its transaction column, with SELECT yb_cancel_transaction('…') in an editor.",
	}, {
		Name: "Statistics (this node)",
		// Times by either name: YugabyteDB before 2025.1 is PostgreSQL 11's,
		// whose pg_stat_statements names them total_time and max_time.
		Query: `SELECT left(query, 400) AS query, calls, round(total_ms::numeric, 1) AS total_ms, round((total_ms / nullif(calls, 0))::numeric, 2) AS mean_ms,
  round(max_ms::numeric, 1) AS max_ms, rows
FROM (SELECT query, calls, rows, coalesce(to_jsonb(s)->>'total_exec_time', to_jsonb(s)->>'total_time')::float8 AS total_ms,
    coalesce(to_jsonb(s)->>'max_exec_time', to_jsonb(s)->>'max_time')::float8 AS max_ms
  FROM pg_stat_statements s) s
ORDER BY total_ms DESC LIMIT 500`,
		Hint: "These come from the pg_stat_statements extension, of the node the connection reached; each node keeps its own. Where it is missing, run CREATE EXTENSION pg_stat_statements.",
	}, {
		Name: "Wait Events (this node)",
		Query: `SELECT wait_event_component AS component, wait_event_class AS class, wait_event, samples,
  (SELECT left(s.query, 300) FROM pg_stat_statements s WHERE s.queryid = w.query_id LIMIT 1) AS query
FROM (SELECT wait_event_component, wait_event_class, wait_event, query_id, sum(sample_weight) AS samples
  FROM yb_active_session_history WHERE sample_time > now() - interval '5 minutes'
  GROUP BY 1, 2, 3, 4) w
ORDER BY samples DESC LIMIT 200`,
		Hint:      "Wait events come from yb_active_session_history, the samples of the node the connection reached, which need the server's ysql_yb_enable_ash flag, on by default from YugabyteDB 2025.1.",
		EmptyHint: "The node samples what its sessions wait on every second (yb_ash_sampling_interval_ms); the page shows the last 5 minutes.",
	}}
}

// yugabyteBuiltInRoles are the roles YugabyteDB makes, left out as
// PostgreSQL's pg_ roles are.
const yugabyteBuiltInRoles = "('yb_db_admin', 'yb_extension', 'yb_fdw')"

func (yugabytedbEngine) ListAccounts(ctx context.Context, q Querier) ([]Account, error) {
	return listPostgresAccounts(ctx, q, "r.rolname !~ '^pg_' AND r.rolname NOT IN "+yugabyteBuiltInRoles)
}

// PlanBackup: ysql_dump, YugabyteDB's pg_dump, writes what pg_dump leaves
// out, as range-sharded keys and colocation; pg_dump where it is missing,
// or, under verify-full without a CA file, too old to check the
// certificate against the system's authorities. A pg_dump 17 or later
// archive is asked about first.
func (yugabytedbEngine) PlanBackup(ctx context.Context, d *DB, o BackupOptions) (ToolRun, error) {
	tool := "pg_dump"
	if _, err := exec.LookPath("ysql_dump"); err == nil {
		tool = "ysql_dump"
	}
	if tool == "ysql_dump" && d.Config.TLS == TLSVerifyFull && d.Config.CAFile == "" {
		if major, ok := pgToolVersion(ctx, tool); ok && major < pgSystemRoots {
			if pgMajor, ok := pgToolVersion(ctx, "pg_dump"); !ok || pgMajor < pgSystemRoots {
				return ToolRun{}, fmt.Errorf("ysql_dump %d cannot check the server's certificate against the system's certificate authorities, as verify-full without a CA file needs, and no pg_dump %d or later is installed to back up instead: set the connection's CA file", major, pgSystemRoots)
			}
			tool = "pg_dump"
		}
	}
	run, err := planPostgresDump(ctx, d, o, tool)
	if err != nil || tool != "pg_dump" || o.Format != BackupArchive {
		return run, err
	}
	if major, ok := pgToolVersion(ctx, tool); ok && major >= 17 {
		run.Warning = fmt.Sprintf("pg_dump %d writes this backup, not ysql_dump: its archive restores only into PostgreSQL, not into YugabyteDB.", major)
	}
	return run, nil
}

// PlanRestore: pg_restore, a statement at a time: YugabyteDB aborts one
// transaction holding the schema changes and the data of a database.
func (yugabytedbEngine) PlanRestore(ctx context.Context, d *DB, database, path string, _ bool) (ToolRun, error) {
	run, err := postgresTool(ctx, d, "pg_restore", database)
	if err != nil {
		return run, err
	}
	run.Argv = append(run.Argv, "--verbose", "--exit-on-error", "--", path)
	return run, nil
}

// BackupChoices: no Clean, which the restore dialog describes as one
// transaction, and a restore that keeps what it restored before it
// stopped.
func (yugabytedbEngine) BackupChoices() BackupChoices {
	return BackupChoices{Content: true, RestoreKeepsPart: true}
}

func (yugabytedbEngine) BackupNote(restore bool) string {
	return backupNoteFor(restore,
		"A .dump restores with pg_restore, a statement at a time, stopping at the first error; a SQL file runs as Run SQL File runs it. pg_restore 16 or earlier, or YugabyteDB's own, must be installed: later ones set what YugabyteDB does not know.",
		"ysql_dump, or pg_dump where it is missing, writes it, reaching the server as the connection does. Only ysql_dump keeps range-sharded keys and colocation, and a pg_dump 17 or later archive restores into PostgreSQL alone.")
}

// MaintenanceCommands: YugabyteDB collects garbage itself, and has no
// REINDEX of a table; ANALYZE refreshes its statistics.
func (yugabytedbEngine) MaintenanceCommands(quotedTable string) []MaintenanceCommand {
	return []MaintenanceCommand{{"Analyze", "ANALYZE " + quotedTable}}
}
