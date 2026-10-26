package db

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"strings"

	"dgopher/internal/sqltext"
)

type sqliteDialect struct{}

func (sqliteDialect) Engine() Engine           { return SQLite }
func (sqliteDialect) Quote(s string) string    { return quoteDouble(s) }
func (sqliteDialect) Placeholder(int) string   { return "?" }
func (sqliteDialect) Editable() (bool, string) { return true, "" }

func (sqliteDialect) Databases(context.Context, Querier) ([]string, error) { return nil, nil }

func (sqliteDialect) CurrentSchema(context.Context, Querier) (string, error) { return "main", nil }

func (sqliteDialect) Schemas(ctx context.Context, q Querier) ([]string, error) {
	return queryStrings(ctx, q, `SELECT name FROM pragma_database_list ORDER BY seq`)
}

func (d sqliteDialect) Objects(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return scanAll(ctx, q, `SELECT name, type FROM `+d.Quote(schema)+`.sqlite_master
WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY name`, nil, func(scan func(...any) error) (Object, error) {
		o := Object{Schema: schema, Rows: -1, Bytes: -1, Kind: KindTable}
		var typ string
		if err := scan(&o.Name, &typ); err != nil {
			return o, err
		}
		if typ == "view" {
			o.Kind = KindView
		}
		return o, nil
	})
}

func (d sqliteDialect) Columns(ctx context.Context, q Querier, schema, table string) ([]Column, error) {
	cols, err := d.columns(ctx, q, schema, table)
	return cols[table], err
}

func (d sqliteDialect) SchemaColumns(ctx context.Context, q Querier, schema string) (map[string][]Column, error) {
	return d.columns(ctx, q, schema, "")
}

// columns reads the columns of a table of a schema, or of every table and
// view of it for "", by their table.
func (d sqliteDialect) columns(ctx context.Context, q Querier, schema, table string) (map[string][]Column, error) {
	// One table is read by its name as SQLite finds it, in any case, as
	// sqlite_master too.
	query, args := `SELECT ?, name, type, "notnull", dflt_value, pk FROM pragma_table_info(?, ?) ORDER BY cid`, []any{table, table, schema}
	if table == "" {
		query, args = `SELECT m.name, p.name, p.type, p."notnull", p.dflt_value, p.pk
FROM `+d.Quote(schema)+`.sqlite_master m JOIN pragma_table_info(m.name, ?) p
WHERE m.type IN ('table', 'view') AND m.name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY m.name, p.cid`, []any{schema}
	}
	out := map[string][]Column{}
	pks := map[string]int{}
	err := scanRows(ctx, q, query, args, func(scan func(...any) error) error {
		var t string
		var c Column
		var notNull, pk int64
		var def sql.NullString
		if err := scan(&t, &c.Name, &c.Type, &notNull, &def, &pk); err != nil {
			return err
		}
		c.Nullable = notNull == 0
		c.Default, c.HasDefault = def.String, def.Valid
		c.PrimaryKey = pk > 0
		if c.PrimaryKey {
			pks[t]++
		}
		out[t] = append(out[t], c)
		return nil
	})
	for t, cols := range out {
		for i := range cols {
			// The one INTEGER PRIMARY KEY is the rowid, numbered on its own.
			if pks[t] == 1 && cols[i].PrimaryKey && strings.EqualFold(cols[i].Type, "INTEGER") {
				cols[i].AutoIncrement = true
			}
		}
	}
	return out, err
}

func (d sqliteDialect) Indexes(ctx context.Context, q Querier, schema, table string) ([]Index, error) {
	var out []Index
	type row struct {
		name   string
		unique bool
		origin string
	}
	list, err := scanAll(ctx, q, `SELECT name, "unique", origin FROM pragma_index_list(?, ?) ORDER BY name`, []any{table, schema},
		func(scan func(...any) error) (row, error) {
			var r row
			var unique int64
			if err := scan(&r.name, &unique, &r.origin); err != nil {
				return r, err
			}
			r.unique = unique != 0
			return r, nil
		})
	if err != nil {
		return nil, err
	}
	for _, r := range list {
		cols, err := queryStrings(ctx, q, `SELECT COALESCE(name, '<expression>') FROM pragma_index_info(?, ?) ORDER BY seqno`, r.name, schema)
		if err != nil {
			return nil, err
		}
		def, _ := queryString(ctx, q, `SELECT COALESCE(sql, '') FROM `+d.Quote(schema)+`.sqlite_master WHERE type = 'index' AND name = ?`, r.name)
		out = append(out, Index{Name: r.name, Columns: cols, Unique: r.unique, Primary: r.origin == "pk", Definition: def})
	}
	return out, nil
}

func (sqliteDialect) ForeignKeys(ctx context.Context, q Querier, schema, table string) ([]ForeignKey, error) {
	var out []ForeignKey
	byID := map[int64]int{}
	err := scanRows(ctx, q, `SELECT id, "table", "from", COALESCE("to", ''), on_delete, on_update FROM pragma_foreign_key_list(?, ?) ORDER BY id, seq`,
		[]any{table, schema}, func(scan func(...any) error) error {
			var id int64
			var ref, from, to, onDelete, onUpdate string
			if err := scan(&id, &ref, &from, &to, &onDelete, &onUpdate); err != nil {
				return err
			}
			i, ok := byID[id]
			if !ok {
				i = len(out)
				byID[id] = i
				out = append(out, ForeignKey{RefSchema: schema, RefTable: ref, OnDelete: action(onDelete), OnUpdate: action(onUpdate)})
			}
			out[i].Columns = append(out[i].Columns, from)
			out[i].RefColumns = append(out[i].RefColumns, to)
			return nil
		})
	for i := range out {
		fk := &out[i]
		fk.Name = "fk_" + strings.Join(fk.Columns, "_")
		fk.Definition = "FOREIGN KEY (" + strings.Join(fk.Columns, ", ") + ") REFERENCES " + fk.RefTable + " (" + strings.Join(fk.RefColumns, ", ") + ")" +
			actionsSQL(fk.OnDelete, fk.OnUpdate)
	}
	return out, err
}

// DDL writes a table's or view's statement and its indexes'; its
// triggers are items of their own, as on the other engines.
func (d sqliteDialect) DDL(ctx context.Context, q Querier, schema string, obj Object) (string, error) {
	defs, err := queryStrings(ctx, q, `SELECT sql FROM `+d.Quote(schema)+`.sqlite_master
WHERE tbl_name = ? AND type <> 'trigger' AND sql IS NOT NULL ORDER BY type <> 'table' AND type <> 'view', name`, obj.Name)
	if err != nil {
		return "", err
	}
	return strings.Join(defs, ";\n\n") + ";\n", nil
}

func (d sqliteDialect) ReferencedBy(ctx context.Context, q Querier, schema, table string) ([]Reference, error) {
	var out []Reference
	type key struct {
		table string
		id    int64
	}
	byKey := map[key]int{}
	err := scanRows(ctx, q, `SELECT m.name, f.id, f."from", COALESCE(f."to", '')
FROM `+d.Quote(schema)+`.sqlite_master m, pragma_foreign_key_list(m.name, ?) f
WHERE m.type = 'table' AND lower(f."table") = lower(?)
ORDER BY m.name, f.id, f.seq`, []any{schema, table}, func(scan func(...any) error) error {
		var name, from, to string
		var id int64
		if err := scan(&name, &id, &from, &to); err != nil {
			return err
		}
		k := key{name, id}
		i, ok := byKey[k]
		if !ok {
			i = len(out)
			byKey[k] = i
			out = append(out, Reference{Schema: schema, Table: name})
		}
		out[i].Columns = append(out[i].Columns, from)
		out[i].RefColumns = append(out[i].RefColumns, to)
		return nil
	})
	for i := range out {
		out[i].Name = "fk_" + strings.Join(out[i].Columns, "_")
	}
	return out, err
}

// Items lists the schema's triggers, SQLite's only objects besides tables,
// views and indexes.
func (d sqliteDialect) Items(ctx context.Context, q Querier, schema string) ([]Item, error) {
	return scanItems(ctx, q, schema, `SELECT 'trigger', name, tbl_name, '', '' FROM `+d.Quote(schema)+`.sqlite_master
WHERE type = 'trigger' ORDER BY name`)
}

// ItemDDL writes a trigger's definition as SQLite keeps it.
func (d sqliteDialect) ItemDDL(ctx context.Context, q Querier, it Item) (string, error) {
	if it.Kind != ItemTrigger {
		return "", errNoDefinition(it)
	}
	def, err := queryString(ctx, q, `SELECT sql FROM `+d.Quote(it.Schema)+`.sqlite_master WHERE type = 'trigger' AND name = ?`, it.Name)
	return def + ";\n", err
}

// ObjectNames is Objects: reading those is not slow.
func (d sqliteDialect) ObjectNames(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return d.Objects(ctx, q, schema)
}

// sqliteFileReaders are SQLite's functions that read the server's
// files or fetch URLs; callers only read them.
var sqliteFileReaders = strings.Fields("readfile fsdir load_extension")

type sqliteEngine struct{ sqliteDialect }

var _ sqlRules = sqliteEngine{}

func (sqliteEngine) Label() string                   { return "SQLite" }
func (sqliteEngine) ColorHex() string                { return "#0f80cc" }
func (sqliteEngine) DefaultPort() int                { return 0 }
func (sqliteEngine) DefaultUser() string             { return "" }
func (sqliteEngine) IsSQL() bool                     { return true }
func (sqliteEngine) Transactions() bool              { return true }
func (sqliteEngine) Savepoints() bool                { return true }
func (sqliteEngine) TransactionalDDL() bool          { return true }
func (sqliteEngine) DatabaseField() (string, string) { return "Database", "" }
func (sqliteEngine) URLSchemes() []string            { return []string{"sqlite", "sqlite3", "file"} }

func (sqliteEngine) URLTLSParameters() map[string]map[string]TLSMode {
	return nil
}

func (sqliteEngine) CloudIdentity() bool              { return false }
func (sqliteEngine) ClearTextPassword() bool          { return false }
func (sqliteEngine) DatabasesNeedOwnConnection() bool { return false }
func (sqliteEngine) DefaultSchemaName() string        { return "main" }
func (sqliteEngine) FileExtensions() []string         { return []string{"sqlite", "sqlite3", "db", "db3"} }
func (sqliteEngine) ActivityPages() []ActivityPage    { return nil }
func (sqliteEngine) dialect() Dialect                 { return sqliteDialect{} }

func (sqliteEngine) open(_ context.Context, cfg Config, _ endpoint, tc *tls.Config, w wire) (openedPool, error) {
	sqldb, err := openSQLite(cfg, w)
	return openedPool{sql: sqldb, single: isMemory(cfg.Database), tls: tc}, err
}

func (sqliteEngine) tlsState(context.Context, *sql.DB, Config, *tls.Config) string { return "" }
func (sqliteEngine) pingStatement() string                                         { return "select 1" }
func (sqliteEngine) beginStatements(driver.TxOptions) []string                     { return []string{"begin"} }
func (sqliteEngine) endStatement(commit bool) string                               { return lowerEndStatement(commit) }
func (sqliteEngine) pingsOnReset() bool                                            { return false }
func (sqliteEngine) execUnprepared() bool                                          { return false }
func (sqliteEngine) bytesAreBinary() bool                                          { return true }
func (sqliteEngine) cancelQuerySQL(int64) string                                   { return "" }
func (sqliteEngine) pingAfterError() bool                                          { return false }
func (sqliteEngine) takesServerParameters() bool                                   { return false }
func (sqliteEngine) CopyFromStdin() bool                                           { return false }

func (sqliteEngine) CreateFile(_ context.Context, path string) error {
	// An empty file is an empty SQLite database.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

func (sqliteEngine) VersionQuery() string            { return "SELECT sqlite_version()" }
func (sqliteEngine) LexDialect() sqltext.Dialect     { return sqltext.SQLite }
func (sqliteEngine) BeginStatement() string          { return "BEGIN" }
func (sqliteEngine) ReadOnlyBeginSQL() string        { return "" }
func (sqliteEngine) followsTransactionByVerbs() bool { return true }

func (sqliteEngine) afterFailedStatement(v txByVerbs, _ error, _ bool, _ func() bool) txByVerbs {
	return v
}

func (sqliteEngine) autocommitMayBeOff() bool                                    { return false }
func (sqliteEngine) ExecutableComments() bool                                    { return false }
func (sqliteEngine) commitsImplicitlyWords([]string, func() []string) bool       { return false }
func (sqliteEngine) setsAutocommitWords([]string, func() []string) (on, ok bool) { return false, false }
func (sqliteEngine) beginIsCompound([]string) bool                               { return false }
func (sqliteEngine) SavepointBeginsTransaction() bool                            { return true }

func (sqliteEngine) OutsideTransaction(sql string) bool {
	words := nameWords(sql, sqltext.SQLite)
	return len(words) > 0 && words[0] == "VACUUM"
}

func (sqliteEngine) TablesMayIgnoreRollback() bool { return false }
func (sqliteEngine) ChangedRowsWord() string       { return "changed" }

// SQLite and ClickHouse explain without running: analyze cannot.
func (sqliteEngine) Explain(statement string, analyze bool) (ExplainRun, bool) {
	if analyze {
		return explainWithPrefix("", statement), false
	}
	return explainWithPrefix("EXPLAIN QUERY PLAN ", statement), true
}

func (sqliteEngine) ParsePlan(cols []string, rows [][]any) (*Plan, bool) {
	return parseSQLitePlan(cols, rows)
}

func (sqliteEngine) AdvisePlanStep(n *PlanNode, add func(n *PlanNode, format string, args ...any)) {
	switch {
	case strings.HasPrefix(n.Op, "SCAN ") && !strings.Contains(n.Op, "USING"):
		add(n, "SCAN reads every row of %s: an index on the columns of the WHERE may let SQLite SEARCH it instead.", n.Target)
	case strings.HasPrefix(n.Op, "USE TEMP B-TREE FOR"):
		add(n, "SQLite builds a temporary index to %s: an index in that order may save it.", strings.ToLower(strings.TrimPrefix(n.Op, "USE TEMP B-TREE FOR ")))
	}
}

func (sqliteEngine) UseSchemaSQL(schema string) string {
	return "" // a session has no schema to change
}

func (sqliteEngine) EditorSchemaPreamble(string) string { return "" }
func (sqliteEngine) NameFolding() NameFolding           { return NamesIgnoreCase }

func (sqliteEngine) Search(ctx context.Context, d *DB, text string, definitions bool) ([]SearchHit, bool, error) {
	text = strings.ToLower(text)
	schemas, err := d.Dialect.Schemas(ctx, d.Catalog())
	if err != nil {
		return nil, false, err
	}
	parts := make([]string, len(schemas))
	var args []any
	for i, s := range schemas {
		parts[i] = strings.ReplaceAll(sqliteSearch, "SCHEMA", d.Dialect.Quote(s))
		args = append(args, s, s, s)
	}
	query := `SELECT * FROM (` + strings.Join(parts, "\nUNION ALL\n") + `)
WHERE instr(lower(name), ?) > 0 OR (? AND instr(lower(def), ?) > 0)`
	args = append(args, text, definitions, text)
	return searchWith(ctx, d, text, query, args)
}

func (sqliteEngine) EnumValues(context.Context, *DB, string) ([]string, error) { return nil, nil }
func (sqliteEngine) ParameterAsText() bool                                     { return false }
func (sqliteEngine) TextLiteral(s string) string                               { return textLiteral(s, "char(0)", standardLiteral) }
func (sqliteEngine) BytesLiteral(b []byte) string                              { return hexLiteral(b) }
func (sqliteEngine) BooleansAreNumbers() bool                                  { return true }
func (sqliteEngine) OffsetTimesAsUTC() bool                                    { return false }
func (sqliteEngine) CastParameter(placeholder, _ string) string                { return placeholder }

func (sqliteEngine) InsertDefaultRowSQL(table string) string {
	return "INSERT INTO " + table + " DEFAULT VALUES"
}

func (sqliteEngine) MaxParameters() int               { return 32766 }
func (sqliteEngine) ProfileBySummarize() bool         { return false }
func (sqliteEngine) Comparable(string) bool           { return true }
func (sqliteEngine) StableOrderByKey() bool           { return true }
func (sqliteEngine) UpdateSetsDefault() bool          { return false }
func (sqliteEngine) InterruptAbortsTransaction() bool { return false }
func (sqliteEngine) FileReadingFunctions() []string   { return sqliteFileReaders }
func (sqliteEngine) FileNamesInStrings() bool         { return false }

// SQLite has no escape of its own for LIKE: the backslash is named.
func (sqliteEngine) LikeFilterSQL(column, pattern string, not bool) string {
	return likeTextFilterSQL(column, pattern, not)
}

func (sqliteEngine) JSONPathSQL(column string, path []any, leaf bool) string {
	return jsonPathSQL(column, path, leaf, false)
}

func (sqliteEngine) UpsertSQL(insert string, keys, updatedColumns []string) string {
	return upsertOnConflictSQL(insert, keys, updatedColumns)
}

func (sqliteEngine) CommonTypes() []string {
	return []string{"INTEGER", "REAL", "TEXT", "BLOB", "NUMERIC"}
}

func (sqliteEngine) FirstColumn() ColumnDesign {
	return ColumnDesign{Name: "id", PrimaryKey: true, Type: "INTEGER"} // the rowid, numbered on its own
}

func (sqliteEngine) ColumnTypeFor(duckType string) string { return columnTypesFor(duckType).sqlite }

func (sqliteEngine) CanonicalType(typ string) string {
	t := strings.TrimSpace(typ)
	if m := sizedDecimal.FindStringSubmatch(t); m != nil {
		return "DECIMAL(" + m[1] + "," + m[2] + ")"
	}
	return sqliteCanonical(typeName(t))
}

func (sqliteEngine) ModifyColumnSQL(d Dialect, t TableDesign, was, now ColumnDesign) ([]string, error) {
	return alterColumnSQL(d, t, was, now, true)
}

func (sqliteEngine) RenameObjectSQL(obj Object, name string) (string, error) {
	if obj.Kind != KindTable {
		return "", errors.New("SQLite cannot rename a view: drop it and create it again under the new name")
	}
	return alterRenameSQL(sqliteDialect{}, obj, name), nil
}

func (sqliteEngine) ScriptPreamble(bool) string { return "" }

func (sqliteEngine) TypeKindOf(typ string) (TypeKind, string, string) {
	return commonTypeKind(typ, false, true)
}

func (sqliteEngine) TypeOfKind(kind TypeKind, size, scale string) string {
	return typesOfKind(kind, size, scale).sqlite
}

func (sqliteEngine) NullableWrapper() bool                                   { return false }
func (sqliteEngine) CreateTableSuffix(Dialect, TableDesign, []string) string { return "" }
func (sqliteEngine) InlineIntegerKey() bool                                  { return true }
func (sqliteEngine) PrimaryKeyClause() bool                                  { return true }
func (sqliteEngine) RebuildsToAlter() bool                                   { return true }

func (sqliteEngine) DropConstraintSQL(d Dialect, kind ConstraintKind, schema, table, name string) string {
	return dropConstraintSQL(d, kind, schema, table, name)
}

func (sqliteEngine) UniqueAsConstraint() bool { return false }

func (sqliteEngine) TableCommentSQL(d Dialect, schema, table, comment string) string {
	return commentOnTableSQL(d, schema, table, comment)
}

func (sqliteEngine) ColumnCommentInline() bool           { return false }
func (sqliteEngine) Comments() bool                      { return false }
func (sqliteEngine) KeepsColumnExtra() bool              { return false }
func (sqliteEngine) AutoIncrementSQL() string            { return "" }
func (sqliteEngine) NumbersRows() bool                   { return true }
func (sqliteEngine) HasForeignKeys() bool                { return true }
func (sqliteEngine) ForeignKeyActions() bool             { return true }
func (sqliteEngine) ForeignKeyNamesUniqueInSchema() bool { return false }
func (sqliteEngine) SecondaryIndexes() bool              { return true }
func (sqliteEngine) KeepsKeyNames() bool                 { return false }
func (sqliteEngine) NamesConstraintsItself() bool        { return true }
func (sqliteEngine) NamesTablesWithinSchema() bool       { return true }
func (sqliteEngine) IgnoresVarcharLength() bool          { return false }

// KeysOfMadeTable: SQLite makes the table again to change them.
func (sqliteEngine) KeysOfMadeTable() string { return "" }

func (sqliteEngine) FixedKeysNote() string { return "" }

func (sqliteEngine) ReadDesignExtras(ctx context.Context, d *DB, t *TableDesign) error {
	return readSQLiteDesign(ctx, d, t)
}

func (sqliteEngine) NormalizeModelTable(*TableDesign) {}
func (sqliteEngine) TruncateToEmpty() bool            { return false }
func (sqliteEngine) ManagesAccounts() bool            { return false }

func (sqliteEngine) ListAccounts(context.Context, Querier) ([]Account, error) {
	return nil, noAccountsError(SQLite)
}

func (sqliteEngine) AccountPrivileges(context.Context, Querier, Account) ([]Privilege, error) {
	return nil, noAccountsError(SQLite)
}

func (sqliteEngine) AccountName(a Account) string { return sqliteDialect{}.Quote(a.Name) }

func (sqliteEngine) CreateAccountSQL(n NewAccount) (string, error) {
	if n.Role {
		return createRoleSQL(sqliteDialect{}.Quote(n.Name)), nil
	}
	return "", noAccountsError(SQLite)
}

func (sqliteEngine) PasswordSQL(Account, string) (string, error) { return "", noAccountsError(SQLite) }

func (sqliteEngine) DropAccountSQL(a Account) []string {
	return dropUserSQL(sqliteDialect{}.Quote(a.Name), a)
}

func (sqliteEngine) GrantSQL(privileges []string, schema, _ string, _ Account) ([]string, error) {
	if err := grantError(privileges, schema); err != nil {
		return nil, err
	}
	return nil, noAccountsError(SQLite)
}

func (sqliteEngine) Privileges() []string { return standardPrivileges() }

func (sqliteEngine) AccountForm() AccountFormFields {
	return AccountFormFields{GrantTargetLabel: "Database"}
}

func (sqliteEngine) BackupFormats() []BackupFormat { return []BackupFormat{BackupCopy} }

func (sqliteEngine) PlanBackup(_ context.Context, d *DB, o BackupOptions) (ToolRun, error) {
	if err := backupOptionsError(d.Config.Engine, o); err != nil {
		return ToolRun{}, err
	}
	return ToolRun{Statement: "VACUUM INTO " + Literal(SQLite, o.Path)}, nil
}

func (sqliteEngine) PlanRestore(_ context.Context, d *DB, _, _ string, _ bool) (ToolRun, error) {
	return ToolRun{}, restoreWithRunSQLFileError(d.Config.Engine)
}

func (sqliteEngine) BackupNote(restore bool) string {
	return backupNoteFor(restore,
		"A SQL file runs as Run SQL File runs it; a copy of the database opens as a connection of its own.",
		"VACUUM INTO writes a copy of the database as it is, while it stays in use: open the copy as a connection to read it.")
}

func (sqliteEngine) BackupChoices() BackupChoices    { return BackupChoices{} }
func (sqliteEngine) BackupScope() BackupScope        { return BackupScopeNone }
func (sqliteEngine) BackupToolChecksCAOnly(*DB) bool { return false }

func (sqliteEngine) MaintenanceCommands(quotedTable string) []MaintenanceCommand {
	return []MaintenanceCommand{
		{"Analyze", "ANALYZE " + quotedTable},
		{"Reindex", "REINDEX " + quotedTable},
	}
}
