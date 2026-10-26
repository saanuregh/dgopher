package db

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"dgopher/internal/sqltext"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/proto"
)

// ServerParam is an argument the server binds itself, as a ClickHouse
// {name:Type} parameter; Value is its text, \N for NULL.
type ServerParam struct {
	Name, Value string
}

// withServerParams moves the ServerParams out of args and, on ClickHouse,
// into ctx.
func withServerParams(ctx context.Context, e Engine, args []any) (context.Context, []any, error) {
	params := clickhouse.Parameters{}
	var rest []any
	for _, a := range args {
		if p, ok := a.(ServerParam); ok {
			params[p.Name] = p.Value
		} else {
			rest = append(rest, a)
		}
	}
	if len(params) == 0 {
		return ctx, args, nil
	}
	if r, ok := SQL(e); !ok || !r.takesServerParameters() {
		return ctx, nil, fmt.Errorf("%s does not take server parameters", e)
	}
	return clickhouse.Context(ctx, clickhouse.WithParameters(params)), rest, nil
}

type clickhouseDialect struct{}

func (clickhouseDialect) Engine() Engine         { return ClickHouse }
func (clickhouseDialect) Quote(s string) string  { return quoteClickHouse(s) }
func (clickhouseDialect) Placeholder(int) string { return "?" }
func (clickhouseDialect) Editable() (bool, string) {
	return false, "ClickHouse changes rows with asynchronous mutations (ALTER TABLE … UPDATE), not row edits: use the SQL editor."
}

func (clickhouseDialect) Databases(context.Context, Querier) ([]string, error) { return nil, nil }

func (clickhouseDialect) CurrentSchema(ctx context.Context, q Querier) (string, error) {
	return queryString(ctx, q, `SELECT currentDatabase()`)
}

func (clickhouseDialect) Schemas(ctx context.Context, q Querier) ([]string, error) {
	return queryStrings(ctx, q, `SELECT name FROM system.databases
ORDER BY name IN ('system', 'INFORMATION_SCHEMA', 'information_schema'), name`)
}

func (clickhouseDialect) Objects(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return scanAll(ctx, q, `SELECT name, engine, ifNull(toInt64(total_rows), toInt64(-1)), ifNull(toInt64(total_bytes), toInt64(-1)), comment
FROM system.tables WHERE database = ? AND NOT is_temporary ORDER BY name`, []any{schema}, func(scan func(...any) error) (Object, error) {
		o := Object{Schema: schema}
		if err := scan(&o.Name, &o.Engine, &o.Rows, &o.Bytes, &o.Comment); err != nil {
			return o, err
		}
		switch o.Engine {
		case "View", "LiveView", "WindowView":
			o.Kind = KindView
		case "MaterializedView":
			o.Kind = KindMaterializedView
		case "Dictionary":
			o.Kind = KindDictionary
		default:
			o.Kind = KindTable
		}
		return o, nil
	})
}

func (clickhouseDialect) Columns(ctx context.Context, q Querier, schema, table string) ([]Column, error) {
	cols, err := clickhouseColumns(ctx, q, schema, table)
	return cols[table], err
}

func (clickhouseDialect) SchemaColumns(ctx context.Context, q Querier, schema string) (map[string][]Column, error) {
	return clickhouseColumns(ctx, q, schema, "")
}

// clickhouseColumns reads the columns of a table of a database, or of
// every table of it for "", by their table.
func clickhouseColumns(ctx context.Context, q Querier, schema, table string) (map[string][]Column, error) {
	which, args := "", []any{schema}
	if table != "" {
		which, args = " AND table = ?", append(args, table)
	}
	out := map[string][]Column{}
	err := scanRows(ctx, q, `SELECT table, name, type, default_kind, default_expression, toBool(is_in_primary_key), comment
FROM system.columns WHERE database = ?`+which+` ORDER BY table, position`, args, func(scan func(...any) error) error {
		var t, kind string
		var c Column
		if err := scan(&t, &c.Name, &c.Type, &kind, &c.Default, &c.PrimaryKey, &c.Comment); err != nil {
			return err
		}
		c.Nullable = strings.HasPrefix(c.Type, "Nullable(")
		c.HasDefault = kind != ""
		if kind != "" && kind != "DEFAULT" {
			c.Default = kind + " " + c.Default
		}
		out[t] = append(out[t], c)
		return nil
	})
	return out, err
}

func (clickhouseDialect) Indexes(ctx context.Context, q Querier, schema, table string) ([]Index, error) {
	var out []Index
	var sortingKey, primaryKey string
	err := scanRows(ctx, q, `SELECT sorting_key, primary_key FROM system.tables WHERE database = ? AND name = ?`,
		[]any{schema, table}, func(scan func(...any) error) error { return scan(&sortingKey, &primaryKey) })
	if err != nil {
		return nil, err
	}
	if primaryKey != "" {
		out = append(out, Index{Name: "PRIMARY KEY", Primary: true, Columns: strings.Split(primaryKey, ", "), Definition: "PRIMARY KEY (" + primaryKey + ")"})
	}
	if sortingKey != "" && sortingKey != primaryKey {
		out = append(out, Index{Name: "ORDER BY", Columns: strings.Split(sortingKey, ", "), Definition: "ORDER BY (" + sortingKey + ")"})
	}
	err = scanRows(ctx, q, `SELECT name, type_full, expr, granularity FROM system.data_skipping_indices
WHERE database = ? AND table = ? ORDER BY name`, []any{schema, table}, func(scan func(...any) error) error {
		var ix Index
		var typ, expr string
		var gran uint64
		if err := scan(&ix.Name, &typ, &expr, &gran); err != nil {
			return err
		}
		ix.Columns = []string{expr}
		ix.Definition = "INDEX " + ix.Name + " " + expr + " TYPE " + typ
		out = append(out, ix)
		return nil
	})
	if unreadableSystemTable(err) {
		return out, nil
	}
	return out, err
}

func (clickhouseDialect) ForeignKeys(context.Context, Querier, string, string) ([]ForeignKey, error) {
	return nil, nil
}

func (d clickhouseDialect) DDL(ctx context.Context, q Querier, schema string, obj Object) (string, error) {
	def, err := queryString(ctx, q, `SELECT create_table_query FROM system.tables WHERE database = ? AND name = ?`, schema, obj.Name)
	if err != nil {
		return "", err
	}
	return def + ";\n", nil
}

func (clickhouseDialect) ReferencedBy(context.Context, Querier, string, string) ([]Reference, error) {
	return nil, nil // ClickHouse has no foreign keys
}

// Items lists the projections of the database's tables. A server before
// system.projections, of version 24, or a user not granted it, lists none.
func (clickhouseDialect) Items(ctx context.Context, q Querier, schema string) ([]Item, error) {
	items, err := scanItems(ctx, q, schema, `SELECT 'projection', name, table, lower(toString(type)), '' FROM system.projections
WHERE database = ? ORDER BY table, name`, schema)
	if unreadableSystemTable(err) {
		return nil, nil
	}
	return items, err
}

// unreadableSystemTable reports whether a system table is missing
// (UNKNOWN_TABLE) or not granted to the user (ACCESS_DENIED): unlike
// system.tables and system.columns, which ClickHouse shows filtered by
// the user's grants, system.projections and system.data_skipping_indices
// need a grant of their own, which ClickHouse Cloud's users often lack.
func unreadableSystemTable(err error) bool {
	var ch *proto.Exception
	return errors.As(err, &ch) && (ch.Code == 60 || ch.Code == 497)
}

// ItemDDL writes the statement adding a projection to its table.
func (d clickhouseDialect) ItemDDL(ctx context.Context, q Querier, it Item) (string, error) {
	if it.Kind != ItemProjection {
		return "", errNoDefinition(it)
	}
	query, err := queryString(ctx, q, `SELECT query FROM system.projections WHERE database = ? AND table = ? AND name = ?`, it.Schema, it.Table, it.Name)
	if err != nil {
		return "", err
	}
	return "ALTER TABLE " + QualifiedName(d, it.Schema, it.Table) + " ADD PROJECTION " + d.Quote(it.Name) + " (" + query + ");\n", nil
}

// ObjectNames is Objects: reading those is not slow.
func (d clickhouseDialect) ObjectNames(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return d.Objects(ctx, q, schema)
}

// clickhouseFileReaders are ClickHouse's functions that read the server's
// files or fetch URLs; callers only read them.
var clickhouseFileReaders = strings.Fields("file url urlcluster s3 s3cluster hdfs hdfscluster remote remotesecure cluster clusterallreplicas mysql " +
	"postgresql jdbc odbc mongodb redis azureblobstorage azureblobstoragecluster gcs executable input")

type clickhouseEngine struct{ clickhouseDialect }

var _ SQLRules = clickhouseEngine{}

func (clickhouseEngine) Label() string                   { return "ClickHouse" }
func (clickhouseEngine) ColorHex() string                { return "#d4a10a" }
func (clickhouseEngine) DefaultPort() int                { return 9000 }
func (clickhouseEngine) DefaultUser() string             { return "default" }
func (clickhouseEngine) IsSQL() bool                     { return true }
func (clickhouseEngine) Transactions() bool              { return false }
func (clickhouseEngine) Savepoints() bool                { return false }
func (clickhouseEngine) TransactionalDDL() bool          { return false }
func (clickhouseEngine) DatabaseField() (string, string) { return "Database", "" }
func (clickhouseEngine) URLSchemes() []string            { return []string{"clickhouse", "ch"} }

func (clickhouseEngine) URLTLSParameters() map[string]map[string]TLSMode {
	return clickhouseURLTLS
}

func (clickhouseEngine) CloudIdentity() bool              { return false }
func (clickhouseEngine) ClearTextPassword() bool          { return false }
func (clickhouseEngine) DatabasesNeedOwnConnection() bool { return false }
func (clickhouseEngine) DefaultSchemaName() string        { return "default" }
func (clickhouseEngine) FileExtensions() []string         { return nil }

func (clickhouseEngine) ActivityPages() []ActivityPage {
	return []ActivityPage{{
		Name: "Sessions", Live: true,
		Stop: func(r []any) string {
			return "KILL QUERY WHERE query_id = " + Literal(ClickHouse, r[0]) + " ASYNC"
		},
		Query: `SELECT query_id, user, round(elapsed, 1) AS seconds, read_rows, formatReadableSize(memory_usage) AS memory, left(query, 400) AS query
FROM system.processes WHERE query_id != queryID() ORDER BY elapsed DESC`,
	}, {
		Name: "Statistics",
		Query: `SELECT any(left(query, 400)) AS query, count() AS calls, round(sum(query_duration_ms)) AS total_ms, round(avg(query_duration_ms), 1) AS mean_ms,
  max(query_duration_ms) AS max_ms, sum(read_rows) AS read_rows, formatReadableSize(sum(read_bytes)) AS read, formatReadableSize(max(memory_usage)) AS peak_memory
FROM system.query_log WHERE type = 'QueryFinish' AND event_time > now() - INTERVAL 1 DAY
GROUP BY normalized_query_hash ORDER BY total_ms DESC LIMIT 500`,
		Hint: "Statistics come from system.query_log, which the server keeps when log_queries is on.",
	}, {
		Name: "Metrics", Live: true, Metrics: true,
		Query: `SELECT uptime() AS uptime,
  (SELECT value FROM system.metrics WHERE metric = 'Query') AS queries,
  (SELECT sum(value) FROM system.metrics WHERE metric IN ('TCPConnection', 'HTTPConnection', 'MySQLConnection', 'PostgreSQLConnection')) AS connections,
  (SELECT value FROM system.metrics WHERE metric = 'MemoryTracking') AS memory,
  (SELECT value FROM system.metrics WHERE metric = 'Merge') AS merges,
  (SELECT count() FROM system.parts WHERE active) AS parts,
  (SELECT value FROM system.asynchronous_metrics WHERE metric = 'LoadAverage1') AS load,
  (SELECT value FROM system.events WHERE event = 'Query') AS queries_run,
  (SELECT value FROM system.events WHERE event = 'SelectedRows') AS rows_read,
  (SELECT value FROM system.events WHERE event = 'InsertedRows') AS rows_inserted`,
	}}
}

func (clickhouseEngine) dialect() Dialect { return clickhouseDialect{} }

func (clickhouseEngine) open(ctx context.Context, cfg Config, ep endpoint, tc *tls.Config, w wire) (openedPool, error) {
	if cfg.TLS == TLSPrefer && !speaksTLS(ctx, ep, tc) {
		tc = nil
	}
	return openedPool{sql: openClickHouse(cfg, ep, tc, w), tls: tc}, nil
}

func (clickhouseEngine) tlsState(_ context.Context, _ *sql.DB, _ Config, tc *tls.Config) string {
	return tlsWord(tc != nil)
}

func (clickhouseEngine) pingStatement() string                     { return "-- ping" }
func (clickhouseEngine) beginStatements(driver.TxOptions) []string { return nil }
func (clickhouseEngine) endStatement(bool) string                  { return "" }
func (clickhouseEngine) pingsOnReset() bool                        { return false }
func (clickhouseEngine) execUnprepared() bool                      { return false }
func (clickhouseEngine) bytesAreBinary() bool                      { return false }
func (clickhouseEngine) cancelQuerySQL(int64) string               { return "" }
func (clickhouseEngine) pingAfterError() bool                      { return false }
func (clickhouseEngine) takesServerParameters() bool               { return true }
func (clickhouseEngine) CopyFromStdin() bool                       { return false }
func (clickhouseEngine) CreateFile(context.Context, string) error  { return noFileError(ClickHouse) }
func (clickhouseEngine) VersionQuery() string                      { return "SELECT version()" }
func (clickhouseEngine) LexDialect() sqltext.Dialect               { return sqltext.ClickHouse }
func (clickhouseEngine) BeginStatement() string                    { return "BEGIN" }
func (clickhouseEngine) ReadOnlyBeginSQL() string                  { return "" }
func (clickhouseEngine) followsTransactionByVerbs() bool           { return false }

func (clickhouseEngine) afterFailedStatement(v txByVerbs, _ error, _ bool, _ func() bool) txByVerbs {
	return v
}

func (clickhouseEngine) autocommitMayBeOff() bool                              { return false }
func (clickhouseEngine) ExecutableComments() bool                              { return false }
func (clickhouseEngine) commitsImplicitlyWords([]string, func() []string) bool { return false }

func (clickhouseEngine) setsAutocommitWords([]string, func() []string) (on, ok bool) {
	return false, false
}

func (clickhouseEngine) beginIsCompound([]string) bool    { return false }
func (clickhouseEngine) SavepointBeginsTransaction() bool { return false }
func (clickhouseEngine) OutsideTransaction(string) bool   { return false }
func (clickhouseEngine) RollbackMayKeepChanges() bool     { return false }
func (clickhouseEngine) RowCountWord() string             { return "changed" }

func (clickhouseEngine) Explain(statement string, analyze bool) (ExplainRun, bool) {
	if analyze {
		return explainWithPrefix("", statement), false
	}
	return explainWithPrefix("EXPLAIN json = 1, description = 1, indexes = 1 ", statement), true
}

func (clickhouseEngine) ParsePlan(_ []string, rows [][]any) (*Plan, bool) {
	return parseClickHousePlan(planText(rows))
}

func (clickhouseEngine) AdvisePlanStep(n *PlanNode, add func(n *PlanNode, format string, args ...any)) {
	if !strings.HasPrefix(n.Op, "ReadFromMergeTree") {
		return
	}
	var indexes []map[string]any
	if json.Unmarshal([]byte(n.Prop("Indexes")), &indexes) != nil {
		return
	}
	for _, ix := range indexes {
		if str(ix, "Type") != "PrimaryKey" {
			continue
		}
		initial, selected := jsonNumber(ix["Initial Granules"]), jsonNumber(ix["Selected Granules"])
		if initial > 1 && selected == initial {
			add(n, "The primary key narrows nothing: all %s granules of %s are read. A WHERE on the sorting key's first columns would skip most.", count(initial), n.Target)
		}
	}
}

func (clickhouseEngine) UseSchemaSQL(schema string) string {
	return "USE " + clickhouseDialect{}.Quote(schema)
}

func (clickhouseEngine) EditorSchemaPreamble(string) string { return "" }
func (clickhouseEngine) NameFolding() NameFolding           { return NamesKeepCase }

func (clickhouseEngine) Search(ctx context.Context, d *DB, text string, definitions bool) ([]SearchHit, bool, error) {
	text = strings.ToLower(text)
	return searchWith(ctx, d, text, clickhouseSearch, []any{text, definitions, text})
}

func (clickhouseEngine) EnumValues(_ context.Context, _ *DB, typ string) ([]string, error) {
	if m := clickhouseEnum.FindStringSubmatch(clickhouseInnerType(strings.TrimSpace(typ))); m != nil {
		return quotedValues(m[2], true), nil
	}
	return nil, nil
}

func (clickhouseEngine) ParameterAsText() bool { return false }

func (clickhouseEngine) TextLiteral(s string) string {
	return "'" + clickHouseEscaper.Replace(s) + "'"
}

func (clickhouseEngine) BytesLiteral(b []byte) string {
	return "unhex('" + hex.EncodeToString(b) + "')"
}

func (clickhouseEngine) BooleansAreNumbers() bool                   { return false }
func (clickhouseEngine) OffsetTimesAsUTC() bool                     { return true }
func (clickhouseEngine) CastParameter(placeholder, _ string) string { return placeholder }

func (clickhouseEngine) InsertDefaultRowSQL(table string) string {
	return "INSERT INTO " + table + " DEFAULT VALUES"
}

func (clickhouseEngine) MaxParameters() int       { return 65535 }
func (clickhouseEngine) ProfilesInOneQuery() bool { return false }

func (clickhouseEngine) Comparable(columnType string) bool {
	t := strings.ToLower(columnType)
	return !slices.ContainsFunc([]string{"map(", "object(", "json", "variant(", "dynamic", "nested("}, func(p string) bool { return strings.HasPrefix(t, p) })
}

func (clickhouseEngine) StableOrderByKey() bool           { return false }
func (clickhouseEngine) UpdateSetsDefault() bool          { return true }
func (clickhouseEngine) InterruptAbortsTransaction() bool { return false }
func (clickhouseEngine) FileReadingFunctions() []string   { return clickhouseFileReaders }
func (clickhouseEngine) FileNamesInStrings() bool         { return false }

func (clickhouseEngine) LikeFilterSQL(column, pattern string, not bool) string {
	return "toString(" + column + ")" + likeOperator(true, not) + pattern
}

func (clickhouseEngine) JSONPathSQL(column string, path []any, leaf bool) string {
	fn := "JSONExtractRaw"
	if leaf {
		fn = "JSONExtractString"
	}
	parts := []string{column}
	for _, p := range path {
		switch x := p.(type) {
		case int:
			parts = append(parts, strconv.Itoa(x+1)) // ClickHouse counts from 1
		case string:
			parts = append(parts, standardLiteral(x))
		}
	}
	return fn + "(" + strings.Join(parts, ", ") + ")"
}

func (clickhouseEngine) UpsertSQL(insert string, _, _ []string) string {
	return "-- ClickHouse has no upsert: a ReplacingMergeTree keeps the last row of a key.\n" + insert
}

func (clickhouseEngine) CommonTypes() []string {
	return []string{"Int32", "Int64", "UInt32", "UInt64", "Float64", "Decimal(12,2)", "Bool", "String", "LowCardinality(String)",
		"Date", "Date32", "DateTime", "DateTime64(3)", "UUID"}
}

func (clickhouseEngine) FirstColumn() ColumnDesign {
	return ColumnDesign{Name: "id", PrimaryKey: true, Type: "UInt64"}
}

func (clickhouseEngine) ColumnTypeFor(duckType string) string {
	return columnTypesFor(duckType).clickhouse
}

func (clickhouseEngine) CanonicalType(typ string) string {
	return clickhouseCanonical(strings.TrimSpace(typ))
}

func (clickhouseEngine) ModifyColumnSQL(d Dialect, _ TableDesign, was, now ColumnDesign) ([]string, error) {
	if err := columnChangeError(was, now); err != nil {
		return nil, err
	}
	name := d.Quote(now.Name)
	var out []string
	defaulted := now.Default == was.Default
	if now.Type != was.Type || now.Nullable != was.Nullable {
		typ := now.Type
		if now.Nullable {
			typ = "Nullable(" + typ + ")"
		}
		modify := "MODIFY COLUMN " + name + " " + typ
		if !defaulted && now.Default != "" {
			// The rows' NULLs take the default, when the column stops
			// holding them.
			modify, defaulted = modify+" DEFAULT "+now.Default, true
		}
		out = append(out, modify)
	}
	if !defaulted {
		if now.Default == "" {
			out = append(out, "MODIFY COLUMN "+name+" REMOVE DEFAULT")
		} else {
			out = append(out, "MODIFY COLUMN "+name+" DEFAULT "+now.Default)
		}
	}
	if now.Comment != was.Comment {
		out = append(out, "COMMENT COLUMN "+name+" "+Literal(d.Engine(), now.Comment))
	}
	return out, nil
}

func (clickhouseEngine) RenameObjectSQL(obj Object, name string) (string, error) {
	return renameTableSQL(clickhouseDialect{}, obj, name), nil
}

func (clickhouseEngine) ScriptPreamble(bool) string { return "" }

func (clickhouseEngine) TypeKindOf(typ string) (TypeKind, string, string) {
	return clickhouseKind(strings.TrimSpace(typ))
}

func (clickhouseEngine) TypeOfKind(kind TypeKind, size, scale string) string {
	return typesOfKind(kind, size, scale).clickhouse
}

func (clickhouseEngine) NullableWrapper() bool { return true }

func (clickhouseEngine) CreateTableSuffix(d Dialect, t TableDesign, primaryKey []string) string {
	order := "tuple()"
	if len(primaryKey) > 0 {
		order = "(" + QuoteNames(d, primaryKey) + ")"
	}
	suffix := " ENGINE = MergeTree ORDER BY " + order
	if t.Comment != "" {
		suffix += " COMMENT " + Literal(d.Engine(), t.Comment)
	}
	return suffix
}

func (clickhouseEngine) InlineIntegerKey() bool { return false }
func (clickhouseEngine) PrimaryKeyClause() bool { return false }
func (clickhouseEngine) RebuildsToAlter() bool  { return false }

func (clickhouseEngine) DropConstraintSQL(d Dialect, kind ConstraintKind, schema, table, name string) string {
	return dropConstraintSQL(d, kind, schema, table, name)
}

func (clickhouseEngine) UniqueAsConstraint() bool { return false }

func (clickhouseEngine) TableCommentSQL(d Dialect, schema, table, comment string) string {
	return "ALTER TABLE " + QualifiedName(d, schema, table) + " MODIFY COMMENT " + Literal(d.Engine(), comment)
}

func (clickhouseEngine) ColumnCommentInline() bool           { return true }
func (clickhouseEngine) HasComments() bool                   { return true }
func (clickhouseEngine) KeepsColumnExtra() bool              { return false }
func (clickhouseEngine) AutoIncrementSQL() string            { return "" }
func (clickhouseEngine) HasAutoNumbering() bool              { return false }
func (clickhouseEngine) HasForeignKeys() bool                { return false }
func (clickhouseEngine) HasForeignKeyActions() bool          { return true }
func (clickhouseEngine) ForeignKeyNamesUniqueInSchema() bool { return false }
func (clickhouseEngine) HasSecondaryIndexes() bool           { return false }
func (clickhouseEngine) KeepsKeyNames() bool                 { return true }
func (clickhouseEngine) NamesConstraintsItself() bool        { return false }
func (clickhouseEngine) NamesTablesWithinSchema() bool       { return false }
func (clickhouseEngine) IgnoresVarcharLength() bool          { return false }

func (clickhouseEngine) KeysOfMadeTable() string {
	return "ClickHouse cannot change a table's key, indexes or constraints here: its sorting key is fixed when the table is made"
}

func (clickhouseEngine) FixedKeysNote() string {
	return "A ClickHouse table's sorting key is fixed when it is made."
}

func (clickhouseEngine) ReadDesignExtras(context.Context, *DB, *TableDesign) error { return nil }

func (clickhouseEngine) NormalizeModelTable(t *TableDesign) {
	// Its sorting key is the primary key's columns, written from them.
	t.Indexes = slices.DeleteFunc(t.Indexes, func(ix IndexDesign) bool { return ix.Name == "ORDER BY" })
	t.PrimaryKeyName = ""
}

func (clickhouseEngine) TruncateToEmpty() bool { return true }
func (clickhouseEngine) ManagesAccounts() bool { return true }

func (clickhouseEngine) ListAccounts(ctx context.Context, q Querier) ([]Account, error) {
	members := map[string][]string{}
	err := scanRows(ctx, q, `SELECT coalesce(user_name, role_name), granted_role_name FROM system.role_grants`, nil, func(scan func(...any) error) error {
		var who, role string
		if err := scan(&who, &role); err != nil {
			return err
		}
		members[who] = append(members[who], role)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return scanAll(ctx, q, `SELECT name, 0, toString(auth_type) FROM system.users UNION ALL SELECT name, 1, '' FROM system.roles ORDER BY 1`, nil,
		func(scan func(...any) error) (Account, error) {
			var a Account
			var role uint8
			var auth string
			if err := scan(&a.Name, &role, &auth); err != nil {
				return a, err
			}
			a.Role = role == 1
			if auth != "" {
				a.Attributes = append(a.Attributes, "logs in with "+strings.Trim(auth, "[]'"))
			}
			a.MemberOf = members[a.Name]
			return a, nil
		})
}

func (clickhouseEngine) AccountPrivileges(ctx context.Context, q Querier, a Account) ([]Privilege, error) {
	return showGrants(ctx, q, clickhouseDialect{}.Quote(a.Name))
}

func (clickhouseEngine) AccountName(a Account) string { return clickhouseDialect{}.Quote(a.Name) }

func (clickhouseEngine) CreateAccountSQL(n NewAccount) (string, error) {
	name := clickhouseDialect{}.Quote(n.Name)
	switch {
	case n.Role:
		return createRoleSQL(name), nil
	case n.Password == "":
		return "CREATE USER " + name + " IDENTIFIED WITH no_password", nil
	}
	return "CREATE USER " + name + " IDENTIFIED WITH sha256_password BY " + Literal(ClickHouse, n.Password), nil
}

func (clickhouseEngine) PasswordSQL(a Account, password string) (string, error) {
	return "ALTER USER " + clickhouseDialect{}.Quote(a.Name) + " IDENTIFIED WITH sha256_password BY " + Literal(ClickHouse, password), nil
}

func (clickhouseEngine) DropAccountSQL(a Account) []string {
	return dropUserSQL(clickhouseDialect{}.Quote(a.Name), a)
}

func (clickhouseEngine) GrantSQL(privileges []string, schema, table string, a Account) ([]string, error) {
	return grantOnDatabaseSQL(clickhouseDialect{}, privileges, schema, table, clickhouseDialect{}.Quote(a.Name))
}

func (clickhouseEngine) Privileges() []string { return []string{"SELECT", "INSERT", "ALTER", "ALL"} }

func (clickhouseEngine) AccountForm() AccountFormFields {
	return AccountFormFields{GrantTargetLabel: "Database"}
}

func (clickhouseEngine) BackupFormats() []BackupFormat { return nil }

func (clickhouseEngine) PlanBackup(_ context.Context, d *DB, o BackupOptions) (ToolRun, error) {
	if err := backupOptionsError(d.Config.Engine, o); err != nil {
		return ToolRun{}, err
	}
	return ToolRun{}, fmt.Errorf("%s backs up on its server: use BACKUP there", d.Config.Engine.Label())
}

func (clickhouseEngine) PlanRestore(_ context.Context, d *DB, _, _ string, _ bool) (ToolRun, error) {
	return ToolRun{}, restoreWithRunSQLFileError(d.Config.Engine)
}

func (clickhouseEngine) BackupNote(restore bool) string {
	return backupNoteFor(restore, sqlFileRestoreNote, mysqldumpBackupNote)
}

func (clickhouseEngine) BackupChoices() BackupChoices    { return BackupChoices{} }
func (clickhouseEngine) BackupScope() BackupScope        { return BackupScopeNone }
func (clickhouseEngine) BackupToolChecksCAOnly(*DB) bool { return false }

func (clickhouseEngine) MaintenanceCommands(quotedTable string) []MaintenanceCommand {
	return []MaintenanceCommand{
		{"Optimize", "OPTIMIZE TABLE " + quotedTable},
		{"Optimize Final (merges every part)", "OPTIMIZE TABLE " + quotedTable + " FINAL"},
	}
}
