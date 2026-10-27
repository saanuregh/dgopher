package db

import (
	"context"
	"crypto/tls"
	"regexp"
	"strings"
)

// tidbDialect is MySQL's Dialect but for the engine it names, so that
// the rules found through it are TiDB's, and for its catalog.
type tidbDialect struct{ mysqlDialect }

func (tidbDialect) Engine() Engine { return TiDB }

// tidbCatalog reads TiDB's catalog: a sequence is an item, not a table.
var tidbCatalog = func() catalogColumns {
	c := mysqlCatalog
	c.tables = " AND TABLE_TYPE <> 'SEQUENCE'"
	return c
}()

func (tidbDialect) Objects(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return mysqlObjects(ctx, q, schema, true, tidbCatalog)
}

func (tidbDialect) ObjectNames(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return mysqlObjects(ctx, q, schema, false, tidbCatalog)
}

// Items lists sequences; TiDB has no routines, triggers or events, whose
// tables it keeps empty.
func (tidbDialect) Items(ctx context.Context, q Querier, schema string) ([]Item, error) {
	return mysqlItems(ctx, q, schema, mysqlSequenceItems)
}

func (d tidbDialect) ItemDDL(ctx context.Context, q Querier, it Item) (string, error) {
	if it.Kind == ItemSequence {
		return showCreate(ctx, q, "SHOW CREATE SEQUENCE "+QualifiedName(d, it.Schema, it.Name), "Create Sequence")
	}
	return d.mysqlDialect.ItemDDL(ctx, q, it)
}

// tidbEngine is TiDB: MySQL's rules but for its name, how it is reached
// by default, and what its server does otherwise.
type tidbEngine struct{ mysqlEngine }

var _ SQLRules = tidbEngine{}

func (tidbEngine) Engine() Engine       { return TiDB }
func (tidbEngine) dialect() Dialect     { return tidbDialect{} }
func (tidbEngine) Label() string        { return "TiDB" }
func (tidbEngine) Base() Engine         { return MySQL }
func (tidbEngine) ColorHex() string     { return "#e6007a" }
func (tidbEngine) DefaultPort() int     { return 4000 }
func (tidbEngine) DefaultUser() string  { return "root" }
func (tidbEngine) URLSchemes() []string { return []string{"tidb"} }

func (tidbEngine) CreateFile(context.Context, string) error { return noFileError(TiDB) }

// open: TiDB has no read-only session. Its transaction_read_only and
// READ ONLY transactions are refused, or with tidb_enable_noop_functions
// accepted and ignored, so a read-only connection is the app's check
// alone.
func (tidbEngine) open(_ context.Context, cfg Config, ep endpoint, tc *tls.Config, w wire) (openedPool, error) {
	sqldb, connector, err := openMySQL(cfg, ep, tc, w, "")
	return openedPool{sql: sqldb, connector: connector, tls: tc}, err
}

func (tidbEngine) ReadOnlyBeginSQL() string     { return "" }
func (tidbEngine) ServerEnforcesReadOnly() bool { return false }

// commitsImplicitlyWords: TiDB commits the open transaction on any CREATE,
// ALTER or DROP but a local temporary table's or a plan binding's, and on
// FLASHBACK and RECOVER, as well as where MySQL does.
func (tidbEngine) commitsImplicitlyWords(words []string, all func() []string) bool {
	switch wordAt(words, 0) {
	case "CREATE", "ALTER", "DROP":
		scope := wordAt(words, 1)
		if scope == "BINDING" || (scope == "GLOBAL" || scope == "SESSION") && wordAt(words, 2) == "BINDING" {
			return false
		}
		object, temporary := ddlObject(words)
		if object == "" {
			object, temporary = ddlObject(all())
		}
		return !(temporary && object == "TABLE")
	case "FLASHBACK", "RECOVER":
		return true
	}
	return commitsImplicitly(words, all)
}

// ReadDesignExtras reads a table's checks from SHOW CREATE TABLE: TiDB's
// TABLE_CONSTRAINTS lists none, so MySQL's reading finds none, and TiDB
// 7.5 has no table naming a check's table.
func (tidbEngine) ReadDesignExtras(ctx context.Context, d *DB, t *TableDesign) error {
	if err := readMySQLDesign(ctx, d, t, mysqlDefault, tidbCatalog); err != nil {
		return err
	}
	ddl, err := showCreate(ctx, d.Catalog(), "SHOW CREATE TABLE "+QualifiedName(tidbDialect{}, t.Schema, t.Name), "Create Table")
	t.Checks = tidbChecksOf(ddl)
	return err
}

// tidbCheck is a check as TiDB's SHOW CREATE TABLE writes it, on a line
// of its own: its name, and its expression in parentheses.
var tidbCheck = regexp.MustCompile("(?m)^\\s*CONSTRAINT `((?:[^`]|``)+)` CHECK \\((.*)\\)(?: /\\*!80016 NOT ENFORCED \\*/)?,?$")

// tidbChecksOf reads the checks of a table's SHOW CREATE TABLE.
func tidbChecksOf(ddl string) []CheckDesign {
	var out []CheckDesign
	for _, m := range tidbCheck.FindAllStringSubmatch(ddl, -1) {
		out = append(out, CheckDesign{Read: true, Name: strings.ReplaceAll(m[1], "``", "`"), Expression: m[2]})
	}
	return out
}

func (tidbEngine) Search(ctx context.Context, d *DB, text string, definitions bool) ([]SearchHit, bool, error) {
	text = strings.ToLower(text)
	return searchWith(ctx, d, text, mariadbSearch, []any{text, definitions, text})
}

func (tidbEngine) Explain(statement string, analyze bool) (ExplainRun, bool) {
	if analyze {
		return explainWithPrefix("EXPLAIN ANALYZE FORMAT='tidb_json' ", statement), true
	}
	return explainWithPrefix("EXPLAIN FORMAT='tidb_json' ", statement), true
}

func (tidbEngine) ParsePlan(_ []string, rows [][]any) (*Plan, bool) {
	return parseTiDBPlan(planText(rows))
}

// AdvisePlanStep reads a step's operator without its part in a join, as
// "TableFullScan (Build)".
func (tidbEngine) AdvisePlanStep(n *PlanNode, add func(n *PlanNode, format string, args ...any)) {
	op, _, _ := strings.Cut(n.Op, " (")
	switch {
	case op == "TableFullScan" && rowsOf(n) >= adviseRows:
		add(n, "A full table scan reads every row of %s: an index on the columns of the WHERE may let TiDB read only those it keeps.", n.Target)
	case (op == "Sort" || op == "TopN") && rowsOf(n) >= adviseRows:
		add(n, "The rows are sorted after they are read: an index in the ORDER BY's order may give them sorted.")
	}
}

// tidbKillNote says when Stop and End reach a session.
const tidbKillNote = " Stop and End send KILL QUERY and KILL, which TiDB runs on whichever of its servers holds the session only with enable-global-kill on, as it is by default since TiDB 6.1; with it off they do nothing."

// ActivityPages: TiDB keeps no performance_schema; its cluster tables list
// what every TiDB server of the cluster runs.
func (tidbEngine) ActivityPages() []ActivityPage {
	return []ActivityPage{{
		Name: "Sessions", Live: true, Stop: sessionStatement(mysqlStopQuery), Kill: sessionStatement(mysqlEndSession),
		Query: `SELECT ID AS id, USER AS user, HOST AS host, DB AS db, COMMAND AS command, TIME AS seconds, STATE AS state,
  INSTANCE AS server, LEFT(INFO, 400) AS query
FROM information_schema.CLUSTER_PROCESSLIST WHERE ID <> CONNECTION_ID() ORDER BY COMMAND = 'Sleep', TIME DESC`,
		Hint: "Sessions come from information_schema.CLUSTER_PROCESSLIST, of every TiDB server; without the PROCESS privilege the user sees only its own." + tidbKillNote,
	}, {
		Name: "Locks", Live: true, Stop: sessionStatement(mysqlStopQuery), Kill: sessionStatement(mysqlEndSession),
		Query: `SELECT t.SESSION_ID AS id, t.USER AS user,
  CONCAT_WS('.', JSON_UNQUOTE(JSON_EXTRACT(w.KEY_INFO, '$.db_name')), JSON_UNQUOTE(JSON_EXTRACT(w.KEY_INFO, '$.table_name'))) AS object,
  JSON_UNQUOTE(JSON_EXTRACT(w.KEY_INFO, '$.index_name')) AS index_name,
  COALESCE(JSON_UNQUOTE(JSON_EXTRACT(w.KEY_INFO, '$.handle_value')), JSON_UNQUOTE(JSON_EXTRACT(w.KEY_INFO, '$.index_values'))) AS row_key,
  IF(t.ID = w.TRX_ID, 'WAITING', 'GRANTED') AS status, IF(t.ID = w.TRX_ID, h.SESSION_ID, NULL) AS waits_for,
  LEFT(IF(t.ID = w.TRX_ID, COALESCE(w.SQL_DIGEST_TEXT, t.CURRENT_SQL_DIGEST_TEXT), t.CURRENT_SQL_DIGEST_TEXT), 300) AS query
FROM information_schema.DATA_LOCK_WAITS w
JOIN information_schema.CLUSTER_TIDB_TRX t ON t.ID IN (w.TRX_ID, w.CURRENT_HOLDING_TRX_ID)
LEFT JOIN information_schema.CLUSTER_TIDB_TRX h ON h.ID = w.CURRENT_HOLDING_TRX_ID
WHERE t.SESSION_ID <> CONNECTION_ID()
ORDER BY status = 'GRANTED', id`,
		Hint: "Locks come from information_schema.DATA_LOCK_WAITS, which lists only the row locks a transaction waits for, with the one holding each, and which the user needs the PROCESS privilege to read." + tidbKillNote,
	}, {
		Name: "Statistics",
		Query: `SELECT LEFT(DIGEST_TEXT, 400) AS query, SCHEMA_NAME AS db, SUM(EXEC_COUNT) AS calls, ROUND(SUM(SUM_LATENCY) / 1e6, 1) AS total_ms,
  ROUND(SUM(SUM_LATENCY) / SUM(EXEC_COUNT) / 1e6, 2) AS mean_ms, ROUND(MAX(MAX_LATENCY) / 1e6, 1) AS max_ms,
  ROUND(SUM(AVG_RESULT_ROWS * EXEC_COUNT)) AS rows_sent, ROUND(SUM(AVG_PROCESSED_KEYS * EXEC_COUNT)) AS keys_read
FROM information_schema.CLUSTER_STATEMENTS_SUMMARY WHERE DIGEST_TEXT <> ''
GROUP BY DIGEST, DIGEST_TEXT, SCHEMA_NAME ORDER BY SUM(SUM_LATENCY) DESC LIMIT 500`,
		Hint:      "Statistics come from information_schema.CLUSTER_STATEMENTS_SUMMARY, of every TiDB server, which the user needs the PROCESS privilege to read.",
		EmptyHint: "TiDB sums statements up over a window (tidb_stmt_summary_refresh_interval), starting each empty, and only with tidb_enable_stmt_summary on.",
	}}
}

// A TiDB backup keeps the schema only: TiDB commits mysqldump's snapshot
// at its UNLOCK TABLES, and a dump without one would mix moments.
func (tidbEngine) PlanBackup(ctx context.Context, d *DB, o BackupOptions) (ToolRun, error) {
	o.Content = BackupSchemaOnly
	return planMySQLDump(ctx, d, o, "mysqldump", mysqlClientFlags, false)
}

func (tidbEngine) BackupChoices() BackupChoices { return BackupChoices{} }

func (tidbEngine) BackupNote(restore bool) string {
	return backupNoteFor(restore, sqlFileRestoreNote,
		"mysqldump writes the schema: TiDB cannot give it a consistent snapshot of the data, which Export or Copy Tables can take instead; it must be installed.")
}
