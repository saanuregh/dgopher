package db

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"strings"

	"dgopher/internal/lists"
	"dgopher/internal/sqltext"
)

type mysqlDialect struct{}

func (mysqlDialect) Engine() Engine           { return MySQL }
func (mysqlDialect) Quote(s string) string    { return quoteBacktick(s) }
func (mysqlDialect) Placeholder(int) string   { return "?" }
func (mysqlDialect) Editable() (bool, string) { return true, "" }

func (mysqlDialect) Databases(context.Context, Querier) ([]string, error) { return nil, nil }

func (mysqlDialect) CurrentSchema(ctx context.Context, q Querier) (string, error) {
	return queryString(ctx, q, `SELECT COALESCE(DATABASE(), '')`)
}

func (mysqlDialect) Schemas(ctx context.Context, q Querier) ([]string, error) {
	return queryStrings(ctx, q, `SELECT SCHEMA_NAME FROM information_schema.SCHEMATA
ORDER BY SCHEMA_NAME IN ('information_schema','mysql','performance_schema','sys'), SCHEMA_NAME`)
}

func (mysqlDialect) Objects(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return mysqlObjects(ctx, q, schema, true, mysqlCatalog)
}

func (mysqlDialect) ObjectNames(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return mysqlObjects(ctx, q, schema, false, mysqlCatalog)
}

// catalogColumns are the expressions by which MySQL, MariaDB and TiDB
// read their catalogs differently.
type catalogColumns struct {
	tables        string // more conditions on information_schema.TABLES
	engine        string // a table's engine, of TABLES
	columnType    string // a column's type, of COLUMNS
	columnDefault string // a column's default, NULL for none, of COLUMNS
	indexPart     string // a part of an index, of STATISTICS
	checkJoin     string // more conditions joining CHECK_CONSTRAINTS cc to TABLE_CONSTRAINTS tc
}

var mysqlCatalog = catalogColumns{engine: "ENGINE", columnType: "COLUMN_TYPE", columnDefault: "COLUMN_DEFAULT",
	indexPart: "COALESCE(COLUMN_NAME, EXPRESSION)"}

// listSeparator ends a GROUP_CONCAT whose list splitList reads: unitSep
// in a string, not 0x1f, which TiDB does not take there.
const listSeparator = "SEPARATOR '" + unitSep + "'"

// mysqlObjects reads a schema's tables and views; their statistics only
// when asked, which MySQL may read anew from every table.
func mysqlObjects(ctx context.Context, q Querier, schema string, sized bool, c catalogColumns) ([]Object, error) {
	stats := "-1, -1, '', ''"
	if sized {
		stats = "COALESCE(TABLE_ROWS, -1), COALESCE(DATA_LENGTH + INDEX_LENGTH, -1), COALESCE(" + c.engine + ", ''), COALESCE(TABLE_COMMENT, '')"
	}
	return scanAll(ctx, q, `SELECT TABLE_NAME, TABLE_TYPE, `+stats+`
FROM information_schema.TABLES WHERE TABLE_SCHEMA = ?`+c.tables+` ORDER BY TABLE_NAME`, []any{schema}, func(scan func(...any) error) (Object, error) {
		o := Object{Schema: schema}
		var typ string
		if err := scan(&o.Name, &typ, &o.Rows, &o.Bytes, &o.Engine, &o.Comment); err != nil {
			return o, err
		}
		o.Kind = KindTable
		if strings.Contains(typ, "VIEW") {
			o.Kind, o.Rows, o.Bytes = KindView, -1, -1
		}
		return o, nil
	})
}

func (mysqlDialect) Columns(ctx context.Context, q Querier, schema, table string) ([]Column, error) {
	cols, err := mysqlColumns(ctx, q, schema, table, mysqlCatalog)
	return cols[table], err
}

func (mysqlDialect) SchemaColumns(ctx context.Context, q Querier, schema string) (map[string][]Column, error) {
	return mysqlColumns(ctx, q, schema, "", mysqlCatalog)
}

// mysqlColumns reads the columns of a table of a schema, or of every
// table of it for "", by their table.
func mysqlColumns(ctx context.Context, q Querier, schema, table string, c catalogColumns) (map[string][]Column, error) {
	which, args := "", []any{schema}
	if table != "" {
		which, args = " AND TABLE_NAME = ?", append(args, table)
	}
	return scanColumns(ctx, q, `SELECT TABLE_NAME, COLUMN_NAME, `+c.columnType+`, IS_NULLABLE = 'YES', COALESCE(`+c.columnDefault+`, ''),
  `+c.columnDefault+` IS NOT NULL, COLUMN_KEY = 'PRI', EXTRA LIKE '%auto_increment%', COALESCE(COLUMN_COMMENT, '')
FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ?`+which+`
ORDER BY TABLE_NAME, ORDINAL_POSITION`, args)
}

func (d mysqlDialect) Indexes(ctx context.Context, q Querier, schema, table string) ([]Index, error) {
	return mysqlIndexes(ctx, q, d, schema, table, mysqlCatalog)
}

// mysqlIndexes reads the indexes of a table, written in d's quotes.
func mysqlIndexes(ctx context.Context, q Querier, d Dialect, schema, table string, c catalogColumns) ([]Index, error) {
	return scanAll(ctx, q, `SELECT INDEX_NAME, MIN(NON_UNIQUE) = 0,
  GROUP_CONCAT(`+c.indexPart+` ORDER BY SEQ_IN_INDEX `+listSeparator+`), MIN(INDEX_TYPE)
FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
GROUP BY INDEX_NAME ORDER BY INDEX_NAME = 'PRIMARY' DESC, INDEX_NAME`, []any{schema, table}, func(scan func(...any) error) (Index, error) {
		var ix Index
		var cols, typ string
		if err := scan(&ix.Name, &ix.Unique, &cols, &typ); err != nil {
			return ix, err
		}
		ix.Columns = splitList(cols)
		ix.Primary = ix.Name == "PRIMARY"
		kind := "INDEX"
		if ix.Primary {
			kind = "PRIMARY KEY"
		} else if ix.Unique {
			kind = "UNIQUE INDEX " + d.Quote(ix.Name)
		} else {
			kind += " " + d.Quote(ix.Name)
		}
		ix.Definition = fmt.Sprintf("%s (%s) USING %s", kind, QuoteNames(d, ix.Columns), typ)
		return ix, nil
	})
}

func (mysqlDialect) ForeignKeys(ctx context.Context, q Querier, schema, table string) ([]ForeignKey, error) {
	return mysqlForeignKeys(ctx, q, schema, table, action)
}

// mysqlForeignKeys reads a table's foreign keys, actionOf writing each
// action as a design keeps it: "" for the server's default.
func mysqlForeignKeys(ctx context.Context, q Querier, schema, table string, actionOf func(string) string) ([]ForeignKey, error) {
	return scanAll(ctx, q, `SELECT k.CONSTRAINT_NAME, k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, rc.DELETE_RULE, rc.UPDATE_RULE,
  GROUP_CONCAT(k.COLUMN_NAME ORDER BY k.ORDINAL_POSITION `+listSeparator+`),
  GROUP_CONCAT(k.REFERENCED_COLUMN_NAME ORDER BY k.ORDINAL_POSITION `+listSeparator+`)
FROM information_schema.KEY_COLUMN_USAGE k
JOIN information_schema.REFERENTIAL_CONSTRAINTS rc
  ON rc.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA AND rc.CONSTRAINT_NAME = k.CONSTRAINT_NAME AND rc.TABLE_NAME = k.TABLE_NAME
WHERE k.TABLE_SCHEMA = ? AND k.TABLE_NAME = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL
GROUP BY k.CONSTRAINT_NAME, k.REFERENCED_TABLE_SCHEMA, k.REFERENCED_TABLE_NAME, rc.DELETE_RULE, rc.UPDATE_RULE
ORDER BY k.CONSTRAINT_NAME`, []any{schema, table}, func(scan func(...any) error) (ForeignKey, error) {
		var fk ForeignKey
		var cols, refCols, onDelete, onUpdate string
		if err := scan(&fk.Name, &fk.RefSchema, &fk.RefTable, &onDelete, &onUpdate, &cols, &refCols); err != nil {
			return fk, err
		}
		fk.Columns, fk.RefColumns = splitList(cols), splitList(refCols)
		fk.OnDelete, fk.OnUpdate = actionOf(onDelete), actionOf(onUpdate)
		fk.Definition = fmt.Sprintf("FOREIGN KEY (%s) REFERENCES %s.%s (%s)",
			strings.Join(fk.Columns, ", "), fk.RefSchema, fk.RefTable, strings.Join(fk.RefColumns, ", ")) + actionsSQL(fk.OnDelete, fk.OnUpdate)
		return fk, nil
	})
}

func (d mysqlDialect) DDL(ctx context.Context, q Querier, schema string, obj Object) (string, error) {
	if obj.Kind == KindView {
		return showCreate(ctx, q, "SHOW CREATE VIEW "+QualifiedName(d, schema, obj.Name), "Create View")
	}
	return showCreate(ctx, q, "SHOW CREATE TABLE "+QualifiedName(d, schema, obj.Name), "Create Table")
}

// showCreate runs a SHOW CREATE statement and returns the definition in
// its column of that name.
func showCreate(ctx context.Context, q Querier, stmt, column string) (string, error) {
	rows, err := q.QueryContext(ctx, stmt)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	at := slices.Index(cols, column)
	if at < 0 {
		return "", fmt.Errorf("%s gave no %s", stmt, column)
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("%s gave no definition", stmt)
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return "", err
	}
	if vals[at] == nil {
		return "", fmt.Errorf("%s gave no definition: the user may lack the privilege to read it", stmt)
	}
	return Display(vals[at]) + ";\n", nil
}

// Items lists routines, triggers and events.
func (mysqlDialect) Items(ctx context.Context, q Querier, schema string) ([]Item, error) {
	return mysqlItems(ctx, q, schema)
}

// mysqlSequenceItems selects the sequences of a schema, which MariaDB and
// TiDB keep as tables, as items for mysqlItems.
const mysqlSequenceItems = `SELECT 'sequence', TABLE_NAME, '', '', ''
FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'SEQUENCE'`

// mysqlItems lists routines, triggers and events, and the items more
// selects: each a SELECT of kind, name, table, detail and ID, taking the
// schema once.
func mysqlItems(ctx context.Context, q Querier, schema string, more ...string) ([]Item, error) {
	query := `SELECT LOWER(ROUTINE_TYPE) AS kind, ROUTINE_NAME AS name, '' AS tbl, '' AS detail, '' AS id
FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = ?
UNION ALL
SELECT 'trigger', TRIGGER_NAME, EVENT_OBJECT_TABLE, CONCAT(ACTION_TIMING, ' ', EVENT_MANIPULATION), ''
FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA = ?
UNION ALL
SELECT 'event', EVENT_NAME, '', STATUS, '' FROM information_schema.EVENTS WHERE EVENT_SCHEMA = ?`
	args := []any{schema, schema, schema}
	for _, m := range more {
		query += "\nUNION ALL\n" + m
		args = append(args, schema)
	}
	return scanItems(ctx, q, schema, query+"\nORDER BY kind, name", args...)
}

// ItemDDL writes an item's definition as SHOW CREATE gives it.
func (d mysqlDialect) ItemDDL(ctx context.Context, q Querier, it Item) (string, error) {
	name := QualifiedName(d, it.Schema, it.Name)
	switch it.Kind {
	case ItemFunction:
		return showCreate(ctx, q, "SHOW CREATE FUNCTION "+name, "Create Function")
	case ItemProcedure:
		return showCreate(ctx, q, "SHOW CREATE PROCEDURE "+name, "Create Procedure")
	case ItemTrigger:
		return showCreate(ctx, q, "SHOW CREATE TRIGGER "+name, "SQL Original Statement")
	case ItemEvent:
		return showCreate(ctx, q, "SHOW CREATE EVENT "+name, "Create Event")
	}
	return "", errNoDefinition(it)
}

func (mysqlDialect) ReferencedBy(ctx context.Context, q Querier, schema, table string) ([]Reference, error) {
	return scanAll(ctx, q, `SELECT CONSTRAINT_NAME, TABLE_SCHEMA, TABLE_NAME,
  GROUP_CONCAT(COLUMN_NAME ORDER BY ORDINAL_POSITION `+listSeparator+`),
  GROUP_CONCAT(REFERENCED_COLUMN_NAME ORDER BY ORDINAL_POSITION `+listSeparator+`)
FROM information_schema.KEY_COLUMN_USAGE
WHERE REFERENCED_TABLE_SCHEMA = ? AND REFERENCED_TABLE_NAME = ?
GROUP BY CONSTRAINT_NAME, TABLE_SCHEMA, TABLE_NAME
ORDER BY TABLE_SCHEMA, TABLE_NAME, CONSTRAINT_NAME`, []any{schema, table}, func(scan func(...any) error) (Reference, error) {
		var r Reference
		var cols, refCols string
		if err := scan(&r.Name, &r.Schema, &r.Table, &cols, &refCols); err != nil {
			return r, err
		}
		r.Columns, r.RefColumns = splitList(cols), splitList(refCols)
		return r, nil
	})
}

const (
	mysqlStopQuery  = "KILL QUERY %s"
	mysqlEndSession = "KILL %s"
)

// mysqlFileReaders are MySQL's functions that read the server's
// files or fetch URLs; callers only read them.
var mysqlFileReaders = strings.Fields("load_file")

type mysqlEngine struct{ mysqlDialect }

var _ SQLRules = mysqlEngine{}

func (mysqlEngine) Label() string                   { return "MySQL" }
func (mysqlEngine) Base() Engine                    { return MySQL }
func (mysqlEngine) ColorHex() string                { return "#00758f" }
func (mysqlEngine) DefaultPort() int                { return 3306 }
func (mysqlEngine) DefaultUser() string             { return "root" }
func (mysqlEngine) Kind() EngineKind                { return KindSQL }
func (mysqlEngine) Transactions() bool              { return true }
func (mysqlEngine) Savepoints() bool                { return true }
func (mysqlEngine) TransactionalDDL() bool          { return false }
func (mysqlEngine) DatabaseField() (string, string) { return "Database", "" }
func (mysqlEngine) URLSchemes() []string            { return []string{"mysql"} }

func (mysqlEngine) URLTLSParameters() map[string]map[string]TLSMode {
	return mysqlURLTLS
}

func (mysqlEngine) CloudIdentities() []Identity      { return tokenIdentities }
func (mysqlEngine) ClearTextPassword() bool          { return true }
func (mysqlEngine) ServerEnforcesReadOnly() bool     { return true }
func (mysqlEngine) ServerReadOnlySince() string      { return "" }
func (mysqlEngine) DatabasesNeedOwnConnection() bool { return false }
func (mysqlEngine) DefaultSchemaName() string        { return "" }
func (mysqlEngine) FileExtensions() []string         { return nil }

func (mysqlEngine) ActivityPages() []ActivityPage {
	return []ActivityPage{mysqlSessionsPage(), {
		Name: "Locks", Live: true, Stop: sessionStatement(mysqlStopQuery), Kill: sessionStatement(mysqlEndSession),
		Query: `SELECT t.PROCESSLIST_ID AS id, t.PROCESSLIST_USER AS user, CONCAT_WS('.', l.OBJECT_SCHEMA, l.OBJECT_NAME) AS object,
  l.INDEX_NAME AS index_name, l.LOCK_TYPE AS lock_type, l.LOCK_MODE AS mode, l.LOCK_STATUS AS status,
  (SELECT GROUP_CONCAT(bt.PROCESSLIST_ID) FROM performance_schema.data_lock_waits w
   JOIN performance_schema.threads bt ON bt.THREAD_ID = w.BLOCKING_THREAD_ID
   WHERE w.REQUESTING_ENGINE_LOCK_ID = l.ENGINE_LOCK_ID) AS waits_for,
  LEFT(t.PROCESSLIST_INFO, 300) AS query
FROM performance_schema.data_locks l JOIN performance_schema.threads t ON t.THREAD_ID = l.THREAD_ID
WHERE t.PROCESSLIST_ID <> CONNECTION_ID()
UNION ALL
SELECT t.PROCESSLIST_ID, t.PROCESSLIST_USER, CONCAT_WS('.', m.OBJECT_SCHEMA, m.OBJECT_NAME), NULL, CONCAT('METADATA ', m.OBJECT_TYPE),
  m.LOCK_TYPE, m.LOCK_STATUS, NULL, LEFT(t.PROCESSLIST_INFO, 300)
FROM performance_schema.metadata_locks m JOIN performance_schema.threads t ON t.THREAD_ID = m.OWNER_THREAD_ID
WHERE t.PROCESSLIST_ID IS NOT NULL AND t.PROCESSLIST_ID <> CONNECTION_ID() AND m.OBJECT_TYPE IN ('TABLE', 'SCHEMA')
ORDER BY status = 'GRANTED', id`,
		Hint: "Locks come from performance_schema, which the server must have on, and the user may read.",
	}, mysqlStatisticsPage("Statistics come from performance_schema, which the server must have on, and the user may read.")}
}

// mysqlSessionsPage lists the server's sessions, as MySQL and MariaDB
// show them.
func mysqlSessionsPage() ActivityPage {
	return ActivityPage{
		Name: "Sessions", Live: true, Stop: sessionStatement(mysqlStopQuery), Kill: sessionStatement(mysqlEndSession),
		Query: `SELECT ID AS id, USER AS user, HOST AS host, DB AS db, COMMAND AS command, TIME AS seconds, STATE AS state, LEFT(INFO, 400) AS query
FROM information_schema.PROCESSLIST WHERE ID <> CONNECTION_ID() ORDER BY COMMAND = 'Sleep', TIME DESC`,
	}
}

// mysqlStatisticsPage lists statements by the time they took, from
// performance_schema, with what the page needs of the server.
func mysqlStatisticsPage(hint string) ActivityPage {
	return ActivityPage{
		Name: "Statistics",
		Query: `SELECT LEFT(DIGEST_TEXT, 400) AS query, SCHEMA_NAME AS db, COUNT_STAR AS calls, ROUND(SUM_TIMER_WAIT / 1e9, 1) AS total_ms,
  ROUND(AVG_TIMER_WAIT / 1e9, 2) AS mean_ms, ROUND(MAX_TIMER_WAIT / 1e9, 1) AS max_ms, SUM_ROWS_SENT AS rows_sent, SUM_ROWS_EXAMINED AS rows_examined
FROM performance_schema.events_statements_summary_by_digest WHERE DIGEST_TEXT IS NOT NULL
ORDER BY SUM_TIMER_WAIT DESC LIMIT 500`,
		Hint: hint,
	}
}

func (mysqlEngine) dialect() Dialect { return mysqlDialect{} }

func (mysqlEngine) open(_ context.Context, cfg Config, ep endpoint, tc *tls.Config, w wire) (openedPool, error) {
	sqldb, connector, err := openMySQL(cfg, ep, tc, w, "transaction_read_only")
	return openedPool{sql: sqldb, connector: connector, tls: tc}, err
}

func (mysqlEngine) tlsState(ctx context.Context, sqldb *sql.DB, cfg Config, tc *tls.Config) string {
	if cfg.TLS == TLSPrefer {
		return negotiatedMySQLTLS(ctx, sqldb)
	}
	// Without prefer's fallback to plain text, the connection uses TLS
	// exactly when it was asked for.
	return tlsWord(tc != nil)
}

func (mysqlEngine) pingStatement() string { return "-- ping" }

func (mysqlEngine) beginStatements(opts driver.TxOptions) []string {
	var out []string
	if level := sql.IsolationLevel(opts.Isolation); level != sql.LevelDefault {
		out = append(out, "SET TRANSACTION ISOLATION LEVEL "+map[sql.IsolationLevel]string{
			sql.LevelRepeatableRead:  "REPEATABLE READ",
			sql.LevelReadCommitted:   "READ COMMITTED",
			sql.LevelReadUncommitted: "READ UNCOMMITTED",
			sql.LevelSerializable:    "SERIALIZABLE",
		}[level])
	}
	if opts.ReadOnly {
		return append(out, "START TRANSACTION READ ONLY")
	}
	return append(out, "START TRANSACTION")
}

func (mysqlEngine) endStatement(commit bool) string {
	if commit {
		return "COMMIT"
	}
	return "ROLLBACK"
}

func (mysqlEngine) pingsOnReset() bool   { return false }
func (mysqlEngine) execUnprepared() bool { return false }
func (mysqlEngine) bytesAreBinary() bool { return false }

func (mysqlEngine) cancelQuerySQL(connectionID int64) string {
	return fmt.Sprintf("KILL QUERY %d", connectionID)
}

func (mysqlEngine) pingAfterError() bool                     { return true }
func (mysqlEngine) takesServerParameters() bool              { return false }
func (mysqlEngine) CopyFromStdin() bool                      { return false }
func (mysqlEngine) CreateFile(context.Context, string) error { return noFileError(MySQL) }
func (mysqlEngine) VersionQuery() string                     { return "SELECT VERSION()" }
func (mysqlEngine) LexDialect() sqltext.Dialect              { return sqltext.MySQL }
func (mysqlEngine) BeginStatement() string                   { return "START TRANSACTION" }
func (mysqlEngine) ReadOnlyBeginSQL() string                 { return "START TRANSACTION READ ONLY" }
func (mysqlEngine) followsTransactionByVerbs() bool          { return true }

func (mysqlEngine) identify(ctx context.Context, q *sql.DB) (Engine, string, error) {
	return identifyMySQLFamily(ctx, q)
}

// The server rolls back the whole transaction of a deadlock's victim, and
// of a lock wait timeout when told to. Under autocommit off a failed
// statement begins a transaction too.
func (mysqlEngine) afterFailedStatement(v txByVerbs, err error, beginsAny bool, rollsBackOnTimeout func() bool) txByVerbs {
	if n := mysqlErrorNumber(err); n == 1213 || n == 1205 && rollsBackOnTimeout != nil && rollsBackOnTimeout() {
		v.tx = TxNone
	} else if beginsAny {
		v.tx = TxOpen
	}
	return v
}

func (mysqlEngine) autocommitMayBeOff() bool { return true }
func (mysqlEngine) ExecutableComments() bool { return true }

func (mysqlEngine) commitsImplicitlyWords(words []string, all func() []string) bool {
	return commitsImplicitly(words, all)
}

func (mysqlEngine) setsAutocommitWords(_ []string, all func() []string) (on, ok bool) {
	return setsAutocommit(all())
}

// beginIsCompound: MariaDB's BEGIN NOT ATOMIC … END is a compound
// statement, not a transaction.
func (mysqlEngine) beginIsCompound(words []string) bool { return wordAt(words, 1) == "NOT" }

func (mysqlEngine) SavepointBeginsTransaction() bool { return false }
func (mysqlEngine) OutsideTransaction(string) bool   { return false }
func (mysqlEngine) RollbackMayKeepChanges() bool     { return true }
func (mysqlEngine) RowCountWord() string             { return "matched" }
func (mysqlEngine) implicitTransactionsSQL() string  { return "" }
func (mysqlEngine) transactionStateSQL() string      { return "" }
func (mysqlEngine) driverCommitsStatements() bool    { return false }
func (mysqlEngine) ownRowCountSQL() string           { return "" }

func (mysqlEngine) Explain(statement string, analyze bool) (ExplainRun, bool) {
	if analyze {
		return explainWithPrefix("EXPLAIN ANALYZE ", statement), true
	}
	return explainWithPrefix("EXPLAIN FORMAT=TREE ", statement), true
}

func (mysqlEngine) ParsePlan(_ []string, rows [][]any) (*Plan, bool) {
	return parseMySQLTree(planText(rows))
}

func (mysqlEngine) AdvisePlanStep(n *PlanNode, add func(n *PlanNode, format string, args ...any)) {
	adviseMySQLFamilyStep("MySQL", n, add)
}

// adviseMySQLFamilyStep advises on a step of MySQL's plan, or one named
// as MySQL names it, server naming the server.
func adviseMySQLFamilyStep(server string, n *PlanNode, add func(n *PlanNode, format string, args ...any)) {
	switch {
	case strings.HasPrefix(n.Op, "Table scan") && rowsOf(n) >= adviseRows:
		add(n, "A table scan reads every row of %s: an index on the columns of the WHERE may let it read only those it keeps.", n.Target)
	case strings.HasPrefix(n.Op, "Temporary table") && rowsOf(n) >= adviseRows:
		add(n, "A temporary table holds the rows to group: an index on the GROUP BY columns may let %s read them in order instead.", server)
	case strings.HasPrefix(n.Op, "Sort") && rowsOf(n) >= adviseRows:
		add(n, "The rows are sorted after they are read: an index in the ORDER BY's order may give them sorted.")
	}
}

func (mysqlEngine) UseSchemaSQL(schema string) string {
	return "USE " + mysqlDialect{}.Quote(schema)
}

func (mysqlEngine) EditorSchemaPreamble(string) string { return "" }
func (mysqlEngine) NameFolding() NameFolding           { return NamesIgnoreCase }

func (mysqlEngine) Search(ctx context.Context, d *DB, text string, definitions bool) ([]SearchHit, bool, error) {
	text = strings.ToLower(text)
	return searchWith(ctx, d, text, mysqlSearch, []any{text, definitions, text})
}

func (mysqlEngine) EnumValues(_ context.Context, _ *DB, typ string) ([]string, error) {
	return enumListValues(typ, true), nil
}

func (mysqlEngine) ParameterAsText() bool                     { return false }
func (mysqlEngine) TextLiteral(s string) string               { return "'" + mysqlEscaper.Replace(s) + "'" }
func (mysqlEngine) BytesLiteral(b []byte) string              { return hexLiteral(b) }
func (mysqlEngine) BooleansAreNumbers() bool                  { return true }
func (mysqlEngine) OffsetTimesAsUTC() bool                    { return false }
func (mysqlEngine) KeyParameter(placeholder, _ string) string { return placeholder }

func (mysqlEngine) CastParameter(placeholder, _ string) string { return placeholder }

func (mysqlEngine) InsertDefaultRowSQL(table string) string {
	return "INSERT INTO " + table + " () VALUES ()"
}

func (mysqlEngine) MaxParameters() int               { return 65535 }
func (mysqlEngine) ProfilesInOneQuery() bool         { return false }
func (mysqlEngine) CountSQL(column, _ string) string { return "count(" + column + ")" }
func (mysqlEngine) NullSQL() string                  { return "NULL" }
func (mysqlEngine) Comparable(string) bool           { return true }
func (mysqlEngine) StableOrderByKey() bool           { return true }
func (mysqlEngine) UpdateSetsDefault() bool          { return true }
func (mysqlEngine) InterruptAbortsTransaction() bool { return false }
func (mysqlEngine) FileReadingFunctions() []string   { return mysqlFileReaders }
func (mysqlEngine) FileNamesInStrings() bool         { return false }

func (mysqlEngine) LikeFilterSQL(column, pattern string, not bool) string {
	return "CAST(" + column + " AS CHAR)" + likeOperator(false, not) + pattern
}

// MySQL reads a backslash in a string as an escape.
func (mysqlEngine) JSONPathSQL(column string, path []any, leaf bool) string {
	return jsonPathSQL(column, path, leaf, true)
}

func (mysqlEngine) UpsertSQL(u Upsert) string {
	insert := u.InsertSQL()
	if len(u.UpdatedColumns) == 0 {
		return "INSERT IGNORE" + strings.TrimPrefix(insert, "INSERT")
	}
	sets := lists.Map(u.UpdatedColumns, func(c string) string { return c + " = VALUES(" + c + ")" })
	return insert + "\nON DUPLICATE KEY UPDATE " + strings.Join(sets, ", ")
}

func (mysqlEngine) RowLimit(n int) (string, string)      { return limitAtEnd(n) }
func (mysqlEngine) ShownColumnSQL(string, string) string { return "" }

func (mysqlEngine) CommonTypes() []string {
	return []string{"int", "bigint", "smallint", "tinyint(1)", "decimal(12,2)", "float", "double", "varchar(255)", "text",
		"date", "datetime", "timestamp", "time", "json", "blob"}
}

func (mysqlEngine) FirstColumn() ColumnDesign {
	return ColumnDesign{Name: "id", PrimaryKey: true, Type: "bigint", AutoIncrement: true}
}

func (mysqlEngine) ColumnTypeFor(duckType string) string { return columnTypesFor(duckType).mysql }

func (mysqlEngine) CanonicalType(typ string) string {
	t := strings.TrimSpace(typ)
	if strings.ToLower(t) == "tinyint(1)" {
		return "BOOLEAN"
	}
	return commonCanonical(t)
}

func (mysqlEngine) ModifyColumnSQL(d Dialect, _ TableDesign, was, now ColumnDesign) ([]string, error) {
	if err := columnChangeError(was, now); err != nil {
		return nil, err
	}
	name := d.Quote(now.Name)
	if now.Type == was.Type && now.Nullable == was.Nullable && now.Comment == was.Comment {
		if now.Default == "" {
			return []string{"ALTER COLUMN " + name + " DROP DEFAULT"}, nil
		}
		return []string{"ALTER COLUMN " + name + " SET DEFAULT " + now.Default}, nil
	}
	// MODIFY writes the whole column again, what MySQL keeps besides
	// included.
	return []string{"MODIFY COLUMN " + columnSQL(d, now, false)}, nil
}

func (mysqlEngine) RenameObjectSQL(obj Object, name string) (string, error) {
	return renameTableSQL(mysqlDialect{}, obj, name), nil
}

func (mysqlEngine) RenameColumnSQL(d Dialect, schema, table, column, name string) string {
	return renameColumnSQL(d, schema, table, column, name)
}

func (mysqlEngine) AddColumnSQL(d Dialect, schema, table, definition string) string {
	return addColumnSQL(d, schema, table, definition)
}

func (mysqlEngine) DropColumnSQL(d Dialect, schema, table string, c ColumnDesign) []string {
	return dropColumnSQL(d, schema, table, c)
}

func (mysqlEngine) ColumnCommentSQL(d Dialect, schema, table, column, _, comment string) string {
	return columnCommentSQL(d, schema, table, column, comment)
}

func (mysqlEngine) ScriptPreamble(bool) string { return "" }

func (mysqlEngine) TypeKindOf(typ string) (TypeKind, string, string) {
	return commonTypeKind(typ, true, false)
}

func (mysqlEngine) TypeOfKind(kind TypeKind, size, scale string) string {
	return typesOfKind(kind, size, scale).mysql
}

func (mysqlEngine) NullableWrapper() bool { return false }

func (mysqlEngine) CreateTableSuffix(d Dialect, t TableDesign, _ []string) string {
	if t.Comment != "" {
		return " COMMENT = " + Literal(d.Engine(), t.Comment)
	}
	return ""
}

func (mysqlEngine) InlineIntegerKey() bool { return false }
func (mysqlEngine) PrimaryKeyClause() bool { return true }
func (mysqlEngine) RebuildsToAlter() bool  { return false }

func (mysqlEngine) DropConstraintSQL(d Dialect, kind ConstraintKind, schema, table, name string) string {
	alter := "ALTER TABLE " + QualifiedName(d, schema, table)
	switch kind {
	case ConstraintForeignKey:
		return alter + " DROP FOREIGN KEY " + d.Quote(name)
	case ConstraintCheck:
		return alter + " DROP CHECK " + d.Quote(name)
	case ConstraintIndex:
		return "DROP INDEX " + d.Quote(name) + " ON " + QualifiedName(d, schema, table)
	}
	return alter + " DROP PRIMARY KEY"
}

func (mysqlEngine) UniqueAsConstraint() bool       { return true }
func (mysqlEngine) ReplacesPrimaryKeyAtOnce() bool { return false }

func (mysqlEngine) TableCommentSQL(d Dialect, schema, table, comment string) string {
	return "ALTER TABLE " + QualifiedName(d, schema, table) + " COMMENT = " + Literal(d.Engine(), comment)
}

func (mysqlEngine) ColumnCommentInline() bool           { return true }
func (mysqlEngine) HasComments() bool                   { return true }
func (mysqlEngine) KeepsColumnExtra() bool              { return true }
func (mysqlEngine) NotNullLast() bool                   { return false }
func (mysqlEngine) AutoIncrementSQL() string            { return "AUTO_INCREMENT" }
func (mysqlEngine) HasAutoNumbering() bool              { return true }
func (mysqlEngine) HasForeignKeys() bool                { return true }
func (mysqlEngine) HasForeignKeyActions() bool          { return true }
func (mysqlEngine) ForeignKeyNamesUniqueInSchema() bool { return true }
func (mysqlEngine) HasSecondaryIndexes() bool           { return true }
func (mysqlEngine) KeepsKeyNames() bool                 { return true }
func (mysqlEngine) NamesConstraintsItself() bool        { return false }
func (mysqlEngine) NamesTablesWithinSchema() bool       { return false }
func (mysqlEngine) IgnoresVarcharLength() bool          { return false }
func (mysqlEngine) KeysOfMadeTable() string             { return "" }
func (mysqlEngine) FixedKeysNote() string               { return "" }

func (mysqlEngine) ReadDesignExtras(ctx context.Context, d *DB, t *TableDesign) error {
	return readMySQLDesign(ctx, d, t, mysqlDefault, mysqlCatalog)
}

func (mysqlEngine) NormalizeModelTable(t *TableDesign) {
	// MySQL makes an index for a key, which it names as the key: the key
	// makes it again.
	t.Indexes = slices.DeleteFunc(t.Indexes, func(ix IndexDesign) bool {
		return slices.ContainsFunc(t.ForeignKeys, func(fk ForeignKeyDesign) bool {
			return fk.Name == ix.Name && slices.Equal(fk.Columns, ix.Columns[:min(len(fk.Columns), len(ix.Columns))])
		})
	})
}

func (mysqlEngine) TruncateToEmpty() bool { return false }
func (mysqlEngine) ManagesAccounts() bool { return true }

func (mysqlEngine) ListAccounts(ctx context.Context, q Querier) ([]Account, error) {
	members := map[string][]string{}
	err := scanRows(ctx, q, `SELECT TO_USER, TO_HOST, FROM_USER, FROM_HOST FROM mysql.role_edges`, nil, func(scan func(...any) error) error {
		var toUser, toHost, fromUser, fromHost string
		if err := scan(&toUser, &toHost, &fromUser, &fromHost); err != nil {
			return err
		}
		key := toUser + "@" + toHost
		members[key] = append(members[key], fromUser+"@"+fromHost)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return scanAll(ctx, q, `SELECT User, Host, account_locked, password_expired, plugin, authentication_string = '' FROM mysql.user ORDER BY User, Host`, nil,
		func(scan func(...any) error) (Account, error) {
			var a Account
			var locked, expired, plugin string
			var noPassword bool
			if err := scan(&a.Name, &a.Host, &locked, &expired, &plugin, &noPassword); err != nil {
				return a, err
			}
			// A role of MySQL is an account locked and without a password.
			a.Role = locked == "Y" && noPassword
			if locked == "Y" {
				a.Attributes = append(a.Attributes, "locked")
			}
			if expired == "Y" {
				a.Attributes = append(a.Attributes, "password expired")
			}
			a.Attributes = append(a.Attributes, "logs in with "+plugin)
			a.MemberOf = members[a.Name+"@"+a.Host]
			return a, nil
		})
}

func (mysqlEngine) AccountPrivileges(ctx context.Context, q Querier, a Account) ([]Privilege, error) {
	return showGrants(ctx, q, mysqlAccountName(a))
}

func (mysqlEngine) AccountName(a Account) string { return mysqlAccountName(a) }

func (mysqlEngine) CreateAccountSQL(n NewAccount) (string, error) {
	return mysqlCreateAccountSQL(MySQL, mysqlAccountName, n), nil
}

// mysqlCreateAccountSQL creates a user or a role of MySQL, or an engine
// built on it, which name and engine e's literals write.
func mysqlCreateAccountSQL(e Engine, name func(Account) string, n NewAccount) string {
	if n.Role {
		return createRoleSQL(name(n.Account))
	}
	return "CREATE USER " + name(n.Account) + " IDENTIFIED BY " + Literal(e, n.Password)
}

func (mysqlEngine) PasswordSQL(a Account, password string) (string, error) {
	return mysqlPasswordSQL(MySQL, mysqlAccountName, a, password), nil
}

// mysqlPasswordSQL changes an account's password as mysqlCreateAccountSQL
// writes it.
func mysqlPasswordSQL(e Engine, name func(Account) string, a Account, password string) string {
	return "ALTER USER " + name(a) + " IDENTIFIED BY " + Literal(e, password)
}

func (mysqlEngine) DropAccountSQL(a Account) []string { return dropUserSQL(mysqlAccountName(a), a) }

func (mysqlEngine) GrantSQL(privileges []string, schema, table string, a Account) ([]string, error) {
	return grantOnDatabaseSQL(mysqlDialect{}, privileges, schema, table, mysqlAccountName(a))
}

func (mysqlEngine) GrantRoleSQL(d Dialect, role, a Account) string { return grantRoleSQL(d, role, a) }

func (mysqlEngine) Privileges() []string { return standardPrivileges() }

func (mysqlEngine) AccountForm() AccountFormFields {
	return AccountFormFields{Host: true, GrantTargetLabel: "Database"}
}

func (mysqlEngine) BackupFormats() []BackupFormat { return []BackupFormat{BackupSQL} }

func (mysqlEngine) PlanBackup(ctx context.Context, d *DB, o BackupOptions) (ToolRun, error) {
	return planMySQLDump(ctx, d, o, "mysqldump", mysqlClientFlags, true)
}

// planMySQLDump plans the backup of a schema by mysqldump, or a tool that
// takes its options, flags writing how the tool connects; snapshot reads
// the data in one transaction, as one moment's.
func planMySQLDump(ctx context.Context, d *DB, o BackupOptions, tool string, flags func(*DB) []string, snapshot bool) (ToolRun, error) {
	if err := backupOptionsError(d.Config.Engine, o); err != nil {
		return ToolRun{}, err
	}
	run, err := mysqlTool(ctx, d, tool, flags)
	if err != nil {
		return run, err
	}
	database := o.Database
	if database == "" {
		database = d.Config.Database
	}
	if database == "" {
		return ToolRun{}, errors.New("choose the database to back up")
	}
	if snapshot {
		run.Argv = append(run.Argv, "--single-transaction")
	}
	run.Argv = append(run.Argv, "--routines", "--triggers", "--events", "--verbose", "--result-file="+o.Path)
	run.Argv = append(run.Argv, contentFlag(o.Content, "--no-data", "--no-create-info")...)
	// After --, a schema named as an option, as -Ar/tmp/x.sql, is a
	// name.
	run.Argv = append(run.Argv, "--databases", "--", database)
	return run, nil
}

func (mysqlEngine) PlanRestore(_ context.Context, d *DB, _, _ string, _ bool) (ToolRun, error) {
	return ToolRun{}, restoreWithRunSQLFileError(d.Config.Engine)
}

func (mysqlEngine) BackupNote(restore bool) string {
	return backupNoteFor(restore, sqlFileRestoreNote, mysqldumpBackupNote)
}

func (mysqlEngine) BackupChoices() BackupChoices { return BackupChoices{Content: true} }
func (mysqlEngine) BackupScope() BackupScope     { return BackupScopeSchema }

func (mysqlEngine) BackupToolChecksCAOnly(d *DB) bool {
	host, _ := d.Address()
	return d.Config.TLS == TLSVerifyFull && host != d.Config.Host
}

func (mysqlEngine) MaintenanceCommands(quotedTable string) []MaintenanceCommand {
	return []MaintenanceCommand{
		{"Analyze", "ANALYZE TABLE " + quotedTable},
		{"Optimize (rebuilds the table)", "OPTIMIZE TABLE " + quotedTable},
		{"Check", "CHECK TABLE " + quotedTable},
	}
}

func (mysqlEngine) readsAllResults() bool              { return false }
func (mysqlEngine) sessionDatabaseSQL() string         { return "" }
func (mysqlEngine) ConnectionFields() ConnectionFields { return ConnectionFields{} }
func (mysqlEngine) sessionStartSQL() []string          { return nil }
func (mysqlEngine) messagesSQL() string                { return "" }
func (mysqlEngine) TakesEffectAtOnce(string) bool      { return false }
func (mysqlEngine) BooleanLiteral(b bool) string       { return booleanWord(b) }
