package db

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"

	"dgopher/internal/sqltext"

	"github.com/duckdb/duckdb-go/v2"
)

type duckdbDialect struct{}

func (duckdbDialect) Engine() Engine           { return DuckDB }
func (duckdbDialect) Quote(s string) string    { return quoteDouble(s) }
func (duckdbDialect) Placeholder(int) string   { return "?" }
func (duckdbDialect) Editable() (bool, string) { return true, "" }

func (duckdbDialect) Databases(context.Context, Querier) ([]string, error) { return nil, nil }

func (duckdbDialect) CurrentSchema(ctx context.Context, q Querier) (string, error) {
	return queryString(ctx, q, `SELECT current_schema()`)
}

func (duckdbDialect) Schemas(ctx context.Context, q Querier) ([]string, error) {
	return queryStrings(ctx, q, `SELECT schema_name FROM duckdb_schemas()
WHERE database_name = current_database() AND schema_name NOT IN ('information_schema', 'pg_catalog')
ORDER BY schema_name <> 'main', schema_name`)
}

func (duckdbDialect) Objects(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return scanAll(ctx, q, `SELECT table_name, 'table', estimated_size, COALESCE(comment, '') FROM duckdb_tables()
WHERE database_name = current_database() AND schema_name = ? AND NOT internal
UNION ALL
SELECT view_name, 'view', -1, COALESCE(comment, '') FROM duckdb_views()
WHERE database_name = current_database() AND schema_name = ? AND NOT internal
ORDER BY 1`, []any{schema, schema}, func(scan func(...any) error) (Object, error) {
		o := Object{Schema: schema, Bytes: -1}
		var kind string
		var rows sql.NullInt64
		if err := scan(&o.Name, &kind, &rows, &o.Comment); err != nil {
			return o, err
		}
		o.Rows = -1
		if rows.Valid {
			o.Rows = rows.Int64
		}
		o.Kind = KindTable
		if kind == "view" {
			o.Kind = KindView
		}
		return o, nil
	})
}

// primaryKey returns the columns of a DuckDB table's primary key.
func (duckdbDialect) primaryKey(ctx context.Context, q Querier, schema, table string) (map[string]bool, error) {
	pk := map[string]bool{}
	cols, err := queryStrings(ctx, q, `SELECT array_to_string(constraint_column_names, chr(31)) FROM duckdb_constraints()
WHERE database_name = current_database() AND schema_name = ? AND table_name = ? AND constraint_type = 'PRIMARY KEY'`, schema, table)
	if err != nil {
		return nil, err
	}
	for _, c := range cols {
		for _, name := range splitList(c) {
			pk[name] = true
		}
	}
	return pk, nil
}

func (d duckdbDialect) Columns(ctx context.Context, q Querier, schema, table string) ([]Column, error) {
	cols, err := d.columns(ctx, q, schema, table)
	return cols[table], err
}

func (d duckdbDialect) SchemaColumns(ctx context.Context, q Querier, schema string) (map[string][]Column, error) {
	return d.columns(ctx, q, schema, "")
}

// columns reads the columns of a table of a schema, or of every table and
// view of it for "", by their table.
func (d duckdbDialect) columns(ctx context.Context, q Querier, schema, table string) (map[string][]Column, error) {
	which, args := "", []any{schema}
	if table != "" {
		which, args = " AND table_name = ?", append(args, table)
	}
	pk := map[[2]string]bool{}
	err := scanRows(ctx, q, `SELECT table_name, array_to_string(constraint_column_names, chr(31)) FROM duckdb_constraints()
WHERE database_name = current_database() AND schema_name = ?`+which+` AND constraint_type = 'PRIMARY KEY'`, args,
		func(scan func(...any) error) error {
			var t, cols string
			if err := scan(&t, &cols); err != nil {
				return err
			}
			for _, c := range splitList(cols) {
				pk[[2]string{t, c}] = true
			}
			return nil
		})
	if err != nil {
		return nil, err
	}
	out := map[string][]Column{}
	err = scanRows(ctx, q, `SELECT table_name, column_name, data_type, is_nullable, column_default, COALESCE(comment, '')
FROM duckdb_columns() WHERE database_name = current_database() AND schema_name = ?`+which+`
ORDER BY table_name, column_index`, args, func(scan func(...any) error) error {
		var t string
		var c Column
		var def sql.NullString
		if err := scan(&t, &c.Name, &c.Type, &c.Nullable, &def, &c.Comment); err != nil {
			return err
		}
		c.Default, c.HasDefault = def.String, def.Valid
		c.PrimaryKey = pk[[2]string{t, c.Name}]
		c.AutoIncrement = strings.HasPrefix(c.Default, "nextval(")
		out[t] = append(out[t], c)
		return nil
	})
	return out, err
}

func (d duckdbDialect) Indexes(ctx context.Context, q Querier, schema, table string) ([]Index, error) {
	var out []Index
	pk, err := d.primaryKey(ctx, q, schema, table)
	if err != nil {
		return nil, err
	}
	if len(pk) > 0 {
		cols, _ := d.Columns(ctx, q, schema, table)
		ix := Index{Name: "PRIMARY KEY", Primary: true, Unique: true}
		for _, c := range cols {
			if c.PrimaryKey {
				ix.Columns = append(ix.Columns, c.Name)
			}
		}
		ix.Definition = "PRIMARY KEY (" + strings.Join(ix.Columns, ", ") + ")"
		out = append(out, ix)
	}
	err = scanRows(ctx, q, `SELECT index_name, is_unique, COALESCE(sql, ''), COALESCE(expressions, '') FROM duckdb_indexes()
WHERE database_name = current_database() AND schema_name = ? AND table_name = ? ORDER BY index_name`,
		[]any{schema, table}, func(scan func(...any) error) error {
			var ix Index
			var exprs string
			if err := scan(&ix.Name, &ix.Unique, &ix.Definition, &exprs); err != nil {
				return err
			}
			ix.Columns = []string{strings.Trim(exprs, "[]")}
			out = append(out, ix)
			return nil
		})
	return out, err
}

func (duckdbDialect) ForeignKeys(ctx context.Context, q Querier, schema, table string) ([]ForeignKey, error) {
	// Its constraint_text quotes no names, so the parts are read apart.
	return scanAll(ctx, q, `SELECT constraint_name, constraint_text, array_to_string(constraint_column_names, chr(31)), referenced_table,
  array_to_string(referenced_column_names, chr(31))
FROM duckdb_constraints()
WHERE database_name = current_database() AND schema_name = ? AND table_name = ? AND constraint_type = 'FOREIGN KEY'
ORDER BY constraint_index`,
		[]any{schema, table}, func(scan func(...any) error) (ForeignKey, error) {
			fk := ForeignKey{RefSchema: schema} // DuckDB's keys point within their schema
			var cols, refCols string
			if err := scan(&fk.Name, &fk.Definition, &cols, &fk.RefTable, &refCols); err != nil {
				return fk, err
			}
			fk.Columns, fk.RefColumns = splitList(cols), splitList(refCols)
			return fk, nil
		})
}

func (duckdbDialect) DDL(ctx context.Context, q Querier, schema string, obj Object) (string, error) {
	fn, col := "duckdb_tables()", "table_name"
	if obj.Kind == KindView {
		fn, col = "duckdb_views()", "view_name"
	}
	def, err := queryString(ctx, q, fmt.Sprintf(`SELECT sql FROM %s WHERE database_name = current_database() AND schema_name = ? AND %s = ?`, fn, col), schema, obj.Name)
	if err != nil {
		return "", err
	}
	ixs, _ := queryStrings(ctx, q, `SELECT sql FROM duckdb_indexes() WHERE database_name = current_database() AND schema_name = ? AND table_name = ? AND sql IS NOT NULL`, schema, obj.Name)
	return strings.Join(append([]string{strings.TrimRight(def, ";")}, ixs...), ";\n\n") + ";\n", nil
}

func (duckdbDialect) ReferencedBy(ctx context.Context, q Querier, schema, table string) ([]Reference, error) {
	return scanAll(ctx, q, `SELECT constraint_name, schema_name, table_name, array_to_string(constraint_column_names, chr(31)),
  array_to_string(referenced_column_names, chr(31))
FROM duckdb_constraints()
WHERE database_name = current_database() AND constraint_type = 'FOREIGN KEY' AND schema_name = ? AND referenced_table = ?
ORDER BY table_name`, []any{schema, table}, func(scan func(...any) error) (Reference, error) {
		var r Reference
		var cols, refCols string
		if err := scan(&r.Name, &r.Schema, &r.Table, &cols, &refCols); err != nil {
			return r, err
		}
		r.Columns, r.RefColumns = splitList(cols), splitList(refCols)
		return r, nil
	})
}

// Items lists the schema's macros, sequences and enum types.
func (duckdbDialect) Items(ctx context.Context, q Querier, schema string) ([]Item, error) {
	return scanItems(ctx, q, schema, `SELECT 'function', function_name, '', array_to_string(parameters, ', '), function_type
FROM duckdb_functions() WHERE database_name = current_database() AND schema_name = ? AND NOT internal
  AND function_type IN ('macro', 'table_macro')
UNION ALL
SELECT 'sequence', sequence_name, '', '', '' FROM duckdb_sequences()
WHERE database_name = current_database() AND schema_name = ? AND NOT temporary
UNION ALL
SELECT 'type', type_name, '', lower(logical_type), '' FROM duckdb_types()
WHERE database_name = current_database() AND schema_name = ? AND NOT internal
ORDER BY 1, 2`, schema, schema, schema)
}

// ItemDDL writes an item's definition from DuckDB's catalog: a macro's,
// with its parameters, a sequence's as DuckDB writes it, an enum's labels.
func (d duckdbDialect) ItemDDL(ctx context.Context, q Querier, it Item) (string, error) {
	name := QualifiedName(d, it.Schema, it.Name)
	switch it.Kind {
	case ItemFunction:
		def, err := queryString(ctx, q, `SELECT macro_definition FROM duckdb_functions()
WHERE database_name = current_database() AND schema_name = ? AND function_name = ? AND function_type = ? LIMIT 1`, it.Schema, it.Name, it.ID)
		if err != nil {
			return "", err
		}
		as := "AS "
		if it.ID == "table_macro" {
			as = "AS TABLE "
		}
		return "CREATE MACRO " + name + "(" + it.Detail + ") " + as + def + ";\n", nil
	case ItemSequence:
		def, err := queryString(ctx, q, `SELECT sql FROM duckdb_sequences()
WHERE database_name = current_database() AND schema_name = ? AND sequence_name = ?`, it.Schema, it.Name)
		return def + "\n", err
	case ItemType:
		labels, err := queryString(ctx, q, `SELECT array_to_string(list_transform(labels, x -> '''' || replace(x, '''', '''''') || ''''), ', ')
FROM duckdb_types() WHERE database_name = current_database() AND schema_name = ? AND type_name = ?`, it.Schema, it.Name)
		if err != nil {
			return "", err
		}
		if labels == "" {
			return "", errNoDefinition(it)
		}
		return "CREATE TYPE " + name + " AS ENUM (" + labels + ");\n", nil
	}
	return "", errNoDefinition(it)
}

// ObjectNames is Objects: reading those is not slow.
func (d duckdbDialect) ObjectNames(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return d.Objects(ctx, q, schema)
}

// duckdbFileReaders are DuckDB's functions that read the server's
// files or fetch URLs; callers only read them.
var duckdbFileReaders = strings.Fields("read_text read_blob read_csv read_csv_auto read_json read_json_auto read_json_objects read_json_objects_auto " +
	"read_ndjson read_ndjson_auto read_ndjson_objects read_parquet parquet_scan parquet_metadata parquet_schema " +
	"parquet_file_metadata parquet_kv_metadata glob sniff_csv read_xlsx st_read iceberg_scan delta_scan read_avro http_get " +
	"sqlite_scan sqlite_attach postgres_scan postgres_scan_pushdown postgres_query postgres_attach mysql_scan mysql_query")

type duckdbEngine struct{ duckdbDialect }

var _ SQLRules = duckdbEngine{}

func (duckdbEngine) Label() string                   { return "DuckDB" }
func (duckdbEngine) Base() Engine                    { return DuckDB }
func (duckdbEngine) ColorHex() string                { return "#c49a00" }
func (duckdbEngine) DefaultPort() int                { return 0 }
func (duckdbEngine) DefaultUser() string             { return "" }
func (duckdbEngine) Kind() EngineKind                { return KindSQL }
func (duckdbEngine) Transactions() bool              { return true }
func (duckdbEngine) Savepoints() bool                { return false }
func (duckdbEngine) TransactionalDDL() bool          { return true }
func (duckdbEngine) DatabaseField() (string, string) { return "Database", "" }
func (duckdbEngine) URLSchemes() []string            { return []string{"duckdb"} }

func (duckdbEngine) URLTLSParameters() map[string]map[string]TLSMode {
	return nil
}

func (duckdbEngine) CloudIdentity() bool              { return false }
func (duckdbEngine) ClearTextPassword() bool          { return false }
func (duckdbEngine) DatabasesNeedOwnConnection() bool { return false }
func (duckdbEngine) DefaultSchemaName() string        { return "main" }
func (duckdbEngine) FileExtensions() []string         { return []string{"duckdb", "ddb", "db"} }
func (duckdbEngine) ActivityPages() []ActivityPage    { return nil }
func (duckdbEngine) dialect() Dialect                 { return duckdbDialect{} }

func (duckdbEngine) open(_ context.Context, cfg Config, _ endpoint, tc *tls.Config, w wire) (openedPool, error) {
	sqldb, err := openDuckDB(cfg, w)
	return openedPool{sql: sqldb, single: true, tls: tc}, err
}

func (duckdbEngine) tlsState(context.Context, *sql.DB, Config, *tls.Config) string { return "" }
func (duckdbEngine) pingStatement() string                                         { return "-- ping" }
func (duckdbEngine) beginStatements(driver.TxOptions) []string                     { return []string{"BEGIN TRANSACTION"} }

func (duckdbEngine) endStatement(commit bool) string {
	if commit {
		return "COMMIT TRANSACTION"
	}
	return "ROLLBACK"
}

func (duckdbEngine) pingsOnReset() bool          { return false }
func (duckdbEngine) execUnprepared() bool        { return false }
func (duckdbEngine) bytesAreBinary() bool        { return false }
func (duckdbEngine) cancelQuerySQL(int64) string { return "" }
func (duckdbEngine) pingAfterError() bool        { return false }
func (duckdbEngine) takesServerParameters() bool { return false }
func (duckdbEngine) CopyFromStdin() bool         { return false }

func (duckdbEngine) CreateFile(ctx context.Context, path string) error {
	// An empty file is not a DuckDB database: DuckDB writes its header as
	// it opens a path that does not exist.
	if strings.Contains(path, "?") {
		return fmt.Errorf("%s: DuckDB cannot open a path that contains '?'", path)
	}
	sqldb, err := sql.Open("duckdb", path)
	if err != nil {
		return err
	}
	return errors.Join(sqldb.PingContext(ctx), sqldb.Close())
}

func (duckdbEngine) VersionQuery() string            { return "SELECT version()" }
func (duckdbEngine) LexDialect() sqltext.Dialect     { return sqltext.Postgres }
func (duckdbEngine) BeginStatement() string          { return "BEGIN" }
func (duckdbEngine) ReadOnlyBeginSQL() string        { return "" }
func (duckdbEngine) followsTransactionByVerbs() bool { return true }

func (duckdbEngine) identify(context.Context, *sql.DB) (Engine, string, error) {
	return DuckDB, "", nil
}

// Any error but a parse error aborts DuckDB's transaction.
func (duckdbEngine) afterFailedStatement(v txByVerbs, err error, _ bool, _ func() bool) txByVerbs {
	if v.tx == TxOpen {
		var de *duckdb.Error
		if !errors.As(err, &de) || de.Type != duckdb.ErrorTypeParser {
			v.tx = TxFailed
		}
	}
	return v
}

func (duckdbEngine) autocommitMayBeOff() bool                                    { return false }
func (duckdbEngine) ExecutableComments() bool                                    { return false }
func (duckdbEngine) commitsImplicitlyWords([]string, func() []string) bool       { return false }
func (duckdbEngine) setsAutocommitWords([]string, func() []string) (on, ok bool) { return false, false }
func (duckdbEngine) beginIsCompound([]string) bool                               { return false }
func (duckdbEngine) SavepointBeginsTransaction() bool                            { return false }

func (duckdbEngine) OutsideTransaction(sql string) bool {
	words := nameWords(sql, sqltext.Postgres)
	return len(words) > 0 && words[0] == "VACUUM"
}

func (duckdbEngine) RollbackMayKeepChanges() bool { return false }
func (duckdbEngine) RowCountWord() string         { return "changed" }

func (duckdbEngine) Explain(statement string, analyze bool) (ExplainRun, bool) {
	if analyze {
		return explainWithPrefix("EXPLAIN (ANALYZE, FORMAT JSON) ", statement), true
	}
	return explainWithPrefix("EXPLAIN (FORMAT JSON) ", statement), true
}

func (duckdbEngine) ParsePlan(_ []string, rows [][]any) (*Plan, bool) {
	return parseDuckDBPlan(planText(rows))
}

func (duckdbEngine) AdvisePlanStep(*PlanNode, func(n *PlanNode, format string, args ...any)) {}

func (duckdbEngine) UseSchemaSQL(schema string) string {
	return "USE " + duckdbDialect{}.Quote(schema)
}

func (duckdbEngine) EditorSchemaPreamble(string) string { return "" }
func (duckdbEngine) NameFolding() NameFolding           { return NamesIgnoreCase }

func (duckdbEngine) Search(ctx context.Context, d *DB, text string, definitions bool) ([]SearchHit, bool, error) {
	text = strings.ToLower(text)
	return searchWith(ctx, d, text, duckdbSearch, []any{text, definitions})
}

func (duckdbEngine) EnumValues(_ context.Context, _ *DB, typ string) ([]string, error) {
	return enumListValues(typ, false), nil
}

func (duckdbEngine) ParameterAsText() bool { return false }

func (duckdbEngine) TextLiteral(s string) string {
	return textLiteral(s, "chr(0)", escapeStringLiteral)
}

func (duckdbEngine) BytesLiteral(b []byte) string {
	const digits = "0123456789ABCDEF"
	var s strings.Builder
	s.Grow(len("'") + 4*len(b) + len("'::BLOB"))
	s.WriteByte('\'')
	for _, c := range b {
		s.WriteString(`\x`)
		s.WriteByte(digits[c>>4])
		s.WriteByte(digits[c&0xf])
	}
	s.WriteString("'::BLOB")
	return s.String()
}

func (duckdbEngine) BooleansAreNumbers() bool { return false }
func (duckdbEngine) OffsetTimesAsUTC() bool   { return false }

func (duckdbEngine) CastParameter(placeholder, columnType string) string {
	return "CAST(" + placeholder + " AS " + columnType + ")"
}

func (duckdbEngine) InsertDefaultRowSQL(table string) string {
	return "INSERT INTO " + table + " DEFAULT VALUES"
}

func (duckdbEngine) MaxParameters() int               { return 65535 }
func (duckdbEngine) ProfilesInOneQuery() bool         { return true }
func (duckdbEngine) Comparable(string) bool           { return true }
func (duckdbEngine) StableOrderByKey() bool           { return true }
func (duckdbEngine) UpdateSetsDefault() bool          { return true }
func (duckdbEngine) InterruptAbortsTransaction() bool { return true }
func (duckdbEngine) FileReadingFunctions() []string   { return duckdbFileReaders }
func (duckdbEngine) FileNamesInStrings() bool         { return true }

func (duckdbEngine) LikeFilterSQL(column, pattern string, not bool) string {
	return "CAST(" + column + " AS VARCHAR)" + likeOperator(true, not) + pattern + ` ESCAPE '\'`
}

func (duckdbEngine) JSONPathSQL(column string, path []any, leaf bool) string {
	return jsonPathSQL(column, path, leaf, false)
}

func (duckdbEngine) UpsertSQL(insert string, keys, updatedColumns []string) string {
	return upsertOnConflictSQL(insert, keys, updatedColumns)
}

func (duckdbEngine) CommonTypes() []string {
	return []string{"INTEGER", "BIGINT", "SMALLINT", "DECIMAL(12,2)", "DOUBLE", "BOOLEAN", "VARCHAR", "DATE", "TIMESTAMP",
		"TIMESTAMPTZ", "TIME", "INTERVAL", "UUID", "JSON", "BLOB"}
}

func (duckdbEngine) FirstColumn() ColumnDesign {
	return ColumnDesign{Name: "id", PrimaryKey: true, Type: "BIGINT"}
}

func (duckdbEngine) ColumnTypeFor(duckType string) string { return columnTypesFor(duckType).duckdb }
func (duckdbEngine) CanonicalType(typ string) string      { return commonCanonical(strings.TrimSpace(typ)) }

func (duckdbEngine) ModifyColumnSQL(d Dialect, t TableDesign, was, now ColumnDesign) ([]string, error) {
	return alterColumnSQL(d, t, was, now, true)
}

func (duckdbEngine) RenameObjectSQL(obj Object, name string) (string, error) {
	return alterRenameSQL(duckdbDialect{}, obj, name), nil
}

func (duckdbEngine) ScriptPreamble(bool) string { return "" }

func (duckdbEngine) TypeKindOf(typ string) (TypeKind, string, string) {
	return commonTypeKind(typ, false, false)
}

func (duckdbEngine) TypeOfKind(kind TypeKind, size, scale string) string {
	return typesOfKind(kind, size, scale).duckdb
}

func (duckdbEngine) NullableWrapper() bool                                   { return false }
func (duckdbEngine) CreateTableSuffix(Dialect, TableDesign, []string) string { return "" }
func (duckdbEngine) InlineIntegerKey() bool                                  { return false }
func (duckdbEngine) PrimaryKeyClause() bool                                  { return true }
func (duckdbEngine) RebuildsToAlter() bool                                   { return false }

func (duckdbEngine) DropConstraintSQL(d Dialect, kind ConstraintKind, schema, table, name string) string {
	return dropConstraintSQL(d, kind, schema, table, name)
}

func (duckdbEngine) UniqueAsConstraint() bool { return false }

func (duckdbEngine) TableCommentSQL(d Dialect, schema, table, comment string) string {
	return commentOnTableSQL(d, schema, table, comment)
}

func (duckdbEngine) ColumnCommentInline() bool           { return false }
func (duckdbEngine) HasComments() bool                   { return true }
func (duckdbEngine) KeepsColumnExtra() bool              { return false }
func (duckdbEngine) AutoIncrementSQL() string            { return "" }
func (duckdbEngine) HasAutoNumbering() bool              { return false }
func (duckdbEngine) HasForeignKeys() bool                { return true }
func (duckdbEngine) HasForeignKeyActions() bool          { return false }
func (duckdbEngine) ForeignKeyNamesUniqueInSchema() bool { return false }
func (duckdbEngine) HasSecondaryIndexes() bool           { return true }
func (duckdbEngine) KeepsKeyNames() bool                 { return false }
func (duckdbEngine) NamesConstraintsItself() bool        { return false }
func (duckdbEngine) NamesTablesWithinSchema() bool       { return false }
func (duckdbEngine) IgnoresVarcharLength() bool          { return true }

func (duckdbEngine) KeysOfMadeTable() string {
	return "DuckDB cannot change the keys or checks of a table it made: make the table again"
}

func (duckdbEngine) FixedKeysNote() string {
	return "DuckDB cannot change the keys or checks of a table it made."
}

func (duckdbEngine) ReadDesignExtras(ctx context.Context, d *DB, t *TableDesign) error {
	return readDuckDBChecks(ctx, d, t)
}

func (duckdbEngine) NormalizeModelTable(*TableDesign) {}
func (duckdbEngine) TruncateToEmpty() bool            { return false }
func (duckdbEngine) ManagesAccounts() bool            { return false }

func (duckdbEngine) ListAccounts(context.Context, Querier) ([]Account, error) {
	return nil, noAccountsError(DuckDB)
}

func (duckdbEngine) AccountPrivileges(context.Context, Querier, Account) ([]Privilege, error) {
	return nil, noAccountsError(DuckDB)
}

func (duckdbEngine) AccountName(a Account) string { return duckdbDialect{}.Quote(a.Name) }

func (duckdbEngine) CreateAccountSQL(n NewAccount) (string, error) {
	if n.Role {
		return createRoleSQL(duckdbDialect{}.Quote(n.Name)), nil
	}
	return "", noAccountsError(DuckDB)
}

func (duckdbEngine) PasswordSQL(Account, string) (string, error) { return "", noAccountsError(DuckDB) }

func (duckdbEngine) DropAccountSQL(a Account) []string {
	return dropUserSQL(duckdbDialect{}.Quote(a.Name), a)
}

func (duckdbEngine) GrantSQL(privileges []string, schema, _ string, _ Account) ([]string, error) {
	if err := grantError(privileges, schema); err != nil {
		return nil, err
	}
	return nil, noAccountsError(DuckDB)
}

func (duckdbEngine) Privileges() []string { return standardPrivileges() }

func (duckdbEngine) AccountForm() AccountFormFields {
	return AccountFormFields{GrantTargetLabel: "Database"}
}

func (duckdbEngine) BackupFormats() []BackupFormat { return []BackupFormat{BackupParquet} }

func (duckdbEngine) PlanBackup(_ context.Context, d *DB, o BackupOptions) (ToolRun, error) {
	if err := backupOptionsError(d.Config.Engine, o); err != nil {
		return ToolRun{}, err
	}
	return ToolRun{Statement: "EXPORT DATABASE " + Literal(DuckDB, o.Path) + " (FORMAT parquet)"}, nil
}

func (duckdbEngine) PlanRestore(ctx context.Context, d *DB, _, path string, _ bool) (ToolRun, error) {
	return planDuckDBRestore(ctx, d, path)
}

func (duckdbEngine) BackupNote(restore bool) string {
	return backupNoteFor(restore,
		"IMPORT DATABASE reads the folder in a DuckDB of its own, which reaches no file outside it; what it makes is then copied into the database, which must not have its tables.",
		"EXPORT DATABASE writes the schema and each table's rows, in Parquet, in a new folder.")
}

func (duckdbEngine) BackupChoices() BackupChoices    { return BackupChoices{Folder: true} }
func (duckdbEngine) BackupScope() BackupScope        { return BackupScopeNone }
func (duckdbEngine) BackupToolChecksCAOnly(*DB) bool { return false }

func (duckdbEngine) MaintenanceCommands(quotedTable string) []MaintenanceCommand {
	return nil
}
