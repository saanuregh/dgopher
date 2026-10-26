package db

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"

	"dgopher/internal/lists"
	"dgopher/internal/sqltext"
)

// engineRules is what every engine answers, Redis and an engine no build
// knows included.
type engineRules interface {
	Label() string
	// ColorHex tints the engine's icon, as "#rrggbb".
	ColorHex() string
	DefaultPort() int
	DefaultUser() string
	IsSQL() bool
	Transactions() bool
	Savepoints() bool
	TransactionalDDL() bool
	// DatabaseField is the connection form's label of the database, and
	// what it shows when empty.
	DatabaseField() (label, placeholder string)
	URLSchemes() []string
	// URLTLSParameters are a URL's TLS parameters, with the mode each of
	// their values means.
	URLTLSParameters() map[string]map[string]TLSMode
	// CloudIdentity reports whether a connection may log in with an
	// Identity.
	CloudIdentity() bool
	ClearTextPassword() bool
	DatabasesNeedOwnConnection() bool
	// DefaultSchemaName is the schema a database's tables are in unless
	// named, "" when that is the connection's database.
	DefaultSchemaName() string
	// FileExtensions are those of the engine's database files, for a file
	// engine.
	FileExtensions() []string
	// ActivityPages are the pages of the server's activity, none for an
	// engine that runs inside the app.
	ActivityPages() []ActivityPage
}

// sqlRules is what an engine that speaks SQL answers besides.
type sqlRules interface {
	engineRules
	Dialect
	driverRules
	transactionRules
	queryRules
	designRules
	accountRules
	backupRules
}

// driverRules is how an engine is opened, pinged and driven.
type driverRules interface {
	// dialect is the Dialect value DialectOf gives.
	dialect() Dialect
	// open makes the pool of a connection, the server's settings and its
	// read-only setting applied; tc is nil for no TLS.
	open(ctx context.Context, cfg Config, ep endpoint, tc *tls.Config, w wire) (openedPool, error)
	// tlsState is whether the pool's connections use TLS, as TLSState
	// says it; tc is the openedPool's.
	tlsState(ctx context.Context, sqldb *sql.DB, cfg Config, tc *tls.Config) string
	pingStatement() string
	beginStatements(opts driver.TxOptions) []string
	endStatement(commit bool) string
	pingsOnReset() bool
	execUnprepared() bool
	bytesAreBinary() bool
	// cancelQuerySQL stops the query of a connection from another, ""
	// where the driver cancels.
	cancelQuerySQL(connectionID int64) string
	pingAfterError() bool
	takesServerParameters() bool
	CopyFromStdin() bool
	// CreateFile makes an empty database at path, where there is nothing.
	CreateFile(ctx context.Context, path string) error
	VersionQuery() string
	LexDialect() sqltext.Dialect
}

// transactionRules is how an engine begins, follows and ends transactions.
type transactionRules interface {
	BeginStatement() string
	// ReadOnlyBeginSQL begins a transaction the server keeps from
	// writing, "" where it has none.
	ReadOnlyBeginSQL() string
	// followsTransactionByVerbs: a session knows whether its transaction
	// is open by following the statements it runs, where the driver cannot
	// tell; false where the driver tells, or the engine keeps none.
	followsTransactionByVerbs() bool
	// afterFailedStatement is the transaction after a statement that
	// failed with err (txByVerbs.after); beginsAny: under autocommit off,
	// any statement begins one.
	afterFailedStatement(v txByVerbs, err error, beginsAny bool, rollsBackOnTimeout func() bool) txByVerbs
	// autocommitMayBeOff: a session's autocommit can be turned off, after
	// which any statement may begin a transaction.
	autocommitMayBeOff() bool
	ExecutableComments() bool
	// commitsImplicitlyWords is CommitsImplicitly given the statement's
	// leading words, and all of them for the rules that read on.
	commitsImplicitlyWords(words []string, all func() []string) bool
	// setsAutocommitWords is SetsAutocommit given a SET statement's
	// leading words, and all of them.
	setsAutocommitWords(words []string, all func() []string) (on, ok bool)
	beginIsCompound(words []string) bool
	SavepointBeginsTransaction() bool
	// OutsideTransaction reports a statement that cannot run in a
	// transaction, which manual commit must then not open for it.
	OutsideTransaction(sql string) bool
	TablesMayIgnoreRollback() bool
	ChangedRowsWord() string
}

// queryRules is how an engine explains, searches and writes the SQL of queries and grid edits.
type queryRules interface {
	// Explain is how to get a statement's plan, false when the engine
	// cannot, as with analyze where it explains without running.
	Explain(statement string, analyze bool) (ExplainRun, bool)
	ParsePlan(cols []string, rows [][]any) (*Plan, bool)
	AdvisePlanStep(n *PlanNode, add func(n *PlanNode, format string, args ...any))
	UseSchemaSQL(schema string) string
	EditorSchemaPreamble(schema string) string
	NameFolding() NameFolding
	Search(ctx context.Context, d *DB, text string, definitions bool) ([]SearchHit, bool, error)
	EnumValues(ctx context.Context, d *DB, typ string) ([]string, error)
	ParameterAsText() bool
	TextLiteral(s string) string
	BytesLiteral(b []byte) string
	BooleansAreNumbers() bool
	OffsetTimesAsUTC() bool
	CastParameter(placeholder, columnType string) string
	InsertDefaultRowSQL(table string) string
	MaxParameters() int
	ProfileBySummarize() bool
	Comparable(columnType string) bool
	StableOrderByKey() bool
	UpdateSetsDefault() bool
	InterruptAbortsTransaction() bool
	FileReadingFunctions() []string
	FileNamesInStrings() bool
	// LikeFilterSQL matches a column's text, ignoring case where the
	// engine can, against pattern, a literal whose % and _ of its own are
	// escaped with a backslash.
	LikeFilterSQL(column, pattern string, not bool) string
	// JSONPathSQL reads a JSON column, quoted, at path, keys and array
	// places; leaf reads a scalar as text rather than a part.
	JSONPathSQL(column string, path []any, leaf bool) string
	// UpsertSQL makes an insert of a row update the row of its key, or
	// keep it when updatedColumns is empty; names come quoted.
	UpsertSQL(insert string, keys, updatedColumns []string) string
}

// designRules is how an engine maps types and writes the DDL of the table designer.
type designRules interface {
	CommonTypes() []string
	FirstColumn() ColumnDesign
	ColumnTypeFor(duckType string) string
	CanonicalType(columnType string) string
	// ModifyColumnSQL writes the ALTER TABLE clauses, and on PostgreSQL
	// and DuckDB the COMMENT statement, changing a column: the column has
	// its new name already.
	ModifyColumnSQL(d Dialect, t TableDesign, was, now ColumnDesign) ([]string, error)
	RenameObjectSQL(obj Object, name string) (string, error)
	ScriptPreamble(functions bool) string
	// TypeKindOf is the kind of a column type, with its length or
	// precision and its scale: "" for a type of no kind.
	TypeKindOf(columnType string) (kind TypeKind, size, scale string)
	// TypeOfKind is the column type of a kind, its sizes kept where the
	// type takes them.
	TypeOfKind(kind TypeKind, size, scale string) string
	// NullableWrapper: a column holds NULL only as Nullable(type), and
	// NOT NULL is never written.
	NullableWrapper() bool
	// CreateTableSuffix is what follows the parentheses of a CREATE TABLE
	// of t, whose primary key is primaryKey.
	CreateTableSuffix(d Dialect, t TableDesign, primaryKey []string) string
	// InlineIntegerKey: a key of one column that numbers its rows is
	// written in its column, as SQLite's INTEGER PRIMARY KEY.
	InlineIntegerKey() bool
	// PrimaryKeyClause: CREATE TABLE writes a PRIMARY KEY (…) line.
	PrimaryKeyClause() bool
	// RebuildsToAlter: a table changes by being made again, and takes no
	// key added later.
	RebuildsToAlter() bool
	// DropConstraintSQL is the statement dropping a constraint or an
	// index of a table.
	DropConstraintSQL(d Dialect, kind ConstraintKind, schema, table, name string) string
	// UniqueAsConstraint: a unique constraint added to a table made is
	// added as a constraint, not as a unique index.
	UniqueAsConstraint() bool
	// TableCommentSQL is the statement setting a table's comment, which
	// "" removes where the engine keeps comments apart.
	TableCommentSQL(d Dialect, schema, table, comment string) string
	// ColumnCommentInline: comments are written in the column's
	// definition, and the table's in its CREATE TABLE.
	ColumnCommentInline() bool
	// Comments: tables and columns keep comments.
	Comments() bool
	// KeepsColumnExtra: a column's Extra is written in its definition.
	KeepsColumnExtra() bool
	// AutoIncrementSQL is what a column that numbers its rows says so
	// with, "" where it says nothing.
	AutoIncrementSQL() string
	// NumbersRows: a new column may number its rows itself.
	NumbersRows() bool
	HasForeignKeys() bool
	// ForeignKeyActions: a foreign key takes ON DELETE and ON UPDATE.
	ForeignKeyActions() bool
	// ForeignKeyNamesUniqueInSchema: two tables' foreign keys cannot
	// share a name.
	ForeignKeyNamesUniqueInSchema() bool
	SecondaryIndexes() bool
	// KeepsKeyNames: a foreign key is known by its name, not only by what
	// it points at.
	KeepsKeyNames() bool
	// NamesConstraintsItself: the engine names a table's unique
	// constraints and foreign keys from their places and columns.
	NamesConstraintsItself() bool
	// NamesTablesWithinSchema: an index and a foreign key name their
	// table without its schema, which is theirs.
	NamesTablesWithinSchema() bool
	// IgnoresVarcharLength: a varchar's length is not kept.
	IgnoresVarcharLength() bool
	// KeysOfMadeTable says why the keys, checks and unique constraints of
	// a table made cannot change, "" when they can.
	KeysOfMadeTable() string
	// FixedKeysNote is what the table designer says of the keys and checks
	// of a table made, "" when it can change them.
	FixedKeysNote() string
	// ReadDesignExtras reads into a table's design what the Dialect does
	// not read: checks, defaults and the like.
	ReadDesignExtras(ctx context.Context, d *DB, t *TableDesign) error
	// NormalizeModelTable leaves out of a table read what a data model
	// would write again.
	NormalizeModelTable(t *TableDesign)
	// TruncateToEmpty: a table's rows are all deleted by TRUNCATE, not
	// DELETE.
	TruncateToEmpty() bool
}

// accountRules is how an engine lists and manages accounts and privileges.
type accountRules interface {
	ManagesAccounts() bool
	ListAccounts(ctx context.Context, q Querier) ([]Account, error)
	AccountPrivileges(ctx context.Context, q Querier, a Account) ([]Privilege, error)
	AccountName(a Account) string
	CreateAccountSQL(n NewAccount) (string, error)
	PasswordSQL(a Account, password string) (string, error)
	DropAccountSQL(a Account) []string
	GrantSQL(privileges []string, schema, table string, a Account) ([]string, error)
	Privileges() []string
	AccountForm() AccountFormFields
}

// backupRules is how an engine backs up, restores and maintains tables.
type backupRules interface {
	BackupFormats() []BackupFormat
	PlanBackup(ctx context.Context, d *DB, o BackupOptions) (ToolRun, error)
	PlanRestore(ctx context.Context, d *DB, database, path string, clean bool) (ToolRun, error)
	BackupNote(restore bool) string
	BackupChoices() BackupChoices
	BackupScope() BackupScope
	BackupToolChecksCAOnly(d *DB) bool
	MaintenanceCommands(quotedTable string) []MaintenanceCommand
}

// ExplainRun is how to get a plan on one session: Before, then Statement,
// whose rows hold the plan unless Read's do, then After, even on failure.
type ExplainRun struct {
	Before    []string
	Statement string
	// Offset is how many bytes of Statement come before the user's text.
	Offset int
	// Read is "" when Statement's own rows hold the plan.
	Read  string
	After []string
}

// ActivityPage is a page of a server's activity: what a statement lists,
// and how to stop what a row runs.
type ActivityPage struct {
	Name  string
	Query string
	// Live pages read again every 2 seconds; the others when asked, as
	// their statements weigh on the server.
	Live bool
	// Stop and Kill return the statement that cancels the query of a row,
	// and the one that ends its session, nil when the page has none. A
	// row's first column names its session.
	Stop func(row []any) string
	Kill func(row []any) string
	// Hint says what the page needs of the server, when it fails.
	Hint string
	// Metrics shows the page's one row as tiles, with rates between reads.
	Metrics bool
}

// MaintenanceCommand is a command that keeps a table in shape.
type MaintenanceCommand struct {
	Label, SQL string
}

// TypeKind is a type's kind, as a model moved to another engine is typed
// through: the common types of SQL, sizes kept, which every engine has a
// type for.
type TypeKind string

const (
	TypeKindSmallint    TypeKind = "smallint"
	TypeKindInteger     TypeKind = "integer"
	TypeKindBigint      TypeKind = "bigint"
	TypeKindDecimal     TypeKind = "decimal"
	TypeKindReal        TypeKind = "real"
	TypeKindDouble      TypeKind = "double"
	TypeKindBoolean     TypeKind = "boolean"
	TypeKindVarchar     TypeKind = "varchar"
	TypeKindChar        TypeKind = "char"
	TypeKindText        TypeKind = "text"
	TypeKindDate        TypeKind = "date"
	TypeKindTime        TypeKind = "time"
	TypeKindTimestamp   TypeKind = "timestamp"
	TypeKindTimestampTZ TypeKind = "timestamptz"
	TypeKindUUID        TypeKind = "uuid"
	TypeKindJSON        TypeKind = "json"
	TypeKindBlob        TypeKind = "blob"
)

// NameFolding is how an engine finds an unqualified name a statement
// writes.
type NameFolding int

const (
	// NamesFoldToLower: an unquoted name is found in lower case, a quoted
	// one as written.
	NamesFoldToLower NameFolding = iota
	// NamesKeepCase: a name is found as written.
	NamesKeepCase
	// NamesIgnoreCase: a name is found whatever its case, the exact one
	// first.
	NamesIgnoreCase
)

// ConstraintKind is what a dropped constraint is.
type ConstraintKind int

const (
	ConstraintForeignKey ConstraintKind = iota
	ConstraintCheck
	ConstraintIndex
	ConstraintPrimaryKey
)

// BackupChoices are what the backup dialog asks of an engine's backup.
type BackupChoices struct {
	// Content asks whether to keep the schema, the data or both.
	Content bool
	// Clean asks whether a restore drops what it makes first.
	Clean bool
	// Folder: a backup is a folder, not a file.
	Folder bool
}

// BackupScope is the navigator node an engine backs up.
type BackupScope int

const (
	BackupScopeNone BackupScope = iota
	BackupScopeDatabase
	BackupScopeSchema
)

// AccountFormFields are what the account form asks, and says, for an
// engine.
type AccountFormFields struct {
	// Host asks where an account may connect from.
	Host bool
	// CreateOptions asks whether a new user may create databases and
	// roles.
	CreateOptions bool
	// GrantTargetLabel names what a grant's privileges are on.
	GrantTargetLabel string
	// PrivilegesNote says which privileges are listed, "" for no note.
	PrivilegesNote string
}

var registeredRules = map[Engine]engineRules{
	Postgres:   postgresEngine{},
	MySQL:      mysqlEngine{},
	ClickHouse: clickhouseEngine{},
	Redis:      redisEngine{},
	SQLite:     sqliteEngine{},
	DuckDB:     duckdbEngine{},
}

// rulesOf returns an engine's rules; unknownEngine's for an ID no build
// knows.
func rulesOf(e Engine) engineRules {
	if r, ok := registeredRules[e]; ok {
		return r
	}
	return unknownEngine{id: e}
}

// sqlRulesOf returns an engine's SQL rules, false for Redis and an
// unknown engine.
func sqlRulesOf(e Engine) (sqlRules, bool) {
	r, ok := rulesOf(e).(sqlRules)
	return r, ok
}

// rulesOfDialect returns the SQL rules of a Dialect's engine: every
// Dialect is a SQL engine's.
func rulesOfDialect(d Dialect) sqlRules {
	r, _ := sqlRulesOf(d.Engine())
	return r
}

var _ engineRules = unknownEngine{}

// unknownEngine answers for an ID no build knows, as one read from a
// project file of a later version: SQL that holds transactions, but with
// no Dialect.
type unknownEngine struct{ id Engine }

func (u unknownEngine) Label() string                 { return string(u.id) }
func (unknownEngine) ColorHex() string                { return "#737373" }
func (unknownEngine) DefaultPort() int                { return 0 }
func (unknownEngine) DefaultUser() string             { return "" }
func (unknownEngine) IsSQL() bool                     { return true }
func (unknownEngine) Transactions() bool              { return true }
func (unknownEngine) Savepoints() bool                { return false }
func (unknownEngine) TransactionalDDL() bool          { return false }
func (unknownEngine) DatabaseField() (string, string) { return "Database", "" }
func (unknownEngine) URLSchemes() []string            { return nil }
func (unknownEngine) URLTLSParameters() map[string]map[string]TLSMode {
	return nil
}
func (unknownEngine) CloudIdentity() bool              { return false }
func (unknownEngine) ClearTextPassword() bool          { return false }
func (unknownEngine) DatabasesNeedOwnConnection() bool { return false }

// The backup dialog's notes; an engine without one of its own shows
// mysqldump's.
const (
	mysqldumpBackupNote = "mysqldump writes it, in one transaction, with routines, triggers and events; it must be installed."
	sqlFileRestoreNote  = "A SQL file, as mysqldump writes, runs as Run SQL File runs it."
)

// backupNoteFor is the restore note when restoring, else the backup note.
func backupNoteFor(restore bool, restoreNote, backupNote string) string {
	if restore {
		return restoreNote
	}
	return backupNote
}

// sessionStatement returns the statement acting on the session a row's
// first column names.
func sessionStatement(format string) func(row []any) string {
	return func(r []any) string { return fmt.Sprintf(format, Display(r[0])) }
}

func (unknownEngine) ActivityPages() []ActivityPage { return nil }

func (unknownEngine) DefaultSchemaName() string { return "" }

func (unknownEngine) FileExtensions() []string { return nil }

// likeOperator is LIKE, or ILIKE ignoring case, with NOT for not.
func likeOperator(ignoreCase, not bool) string {
	op := "LIKE"
	if ignoreCase {
		op = "ILIKE"
	}
	if not {
		op = "NOT " + op
	}
	return " " + op + " "
}

// likeTextFilterSQL is the LIKE filter of standard SQL, with the
// backslash named as its escape.
func likeTextFilterSQL(column, pattern string, not bool) string {
	return "CAST(" + column + " AS TEXT)" + likeOperator(false, not) + pattern + ` ESCAPE '\'`
}

// jsonPathSQL reads a JSON column at a JSON path, $.a[0].b, with -> and
// ->>; backslashEscapes doubles the backslashes of the path's string.
func jsonPathSQL(column string, path []any, leaf, backslashEscapes bool) string {
	var b strings.Builder
	b.WriteString("$")
	for _, p := range path {
		switch x := p.(type) {
		case int:
			b.WriteString("[" + strconv.Itoa(x) + "]")
		case string:
			if plainJSONKey(x) {
				b.WriteString("." + x)
			} else {
				b.WriteString(`."` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(x) + `"`)
			}
		}
	}
	op := "->"
	if leaf {
		op = "->>"
	}
	text := b.String()
	if backslashEscapes {
		text = strings.ReplaceAll(text, `\`, `\\`)
	}
	return column + op + standardLiteral(text)
}

// plainJSONKey reports whether a key goes in a JSON path unquoted.
func plainJSONKey(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// upsertOnConflictSQL is the upsert of ON CONFLICT … EXCLUDED.
func upsertOnConflictSQL(insert string, keys, updatedColumns []string) string {
	conflict := insert + "\nON CONFLICT (" + strings.Join(keys, ", ") + ")"
	if len(updatedColumns) == 0 {
		return conflict + " DO NOTHING"
	}
	sets := lists.Map(updatedColumns, func(c string) string { return c + " = EXCLUDED." + c })
	return conflict + " DO UPDATE SET " + strings.Join(sets, ", ")
}

// The methods and functions below give an engine's rules to the packages
// outside db; an engine without SQL rules answers what the switches they
// replaced did.

// Label is the engine's display name.
func (e Engine) Label() string { return rulesOf(e).Label() }

// DefaultPort is the port the engine listens on unless configured otherwise.
func (e Engine) DefaultPort() int { return rulesOf(e).DefaultPort() }

// DefaultUser is the user most installations of the engine start with.
func (e Engine) DefaultUser() string { return rulesOf(e).DefaultUser() }

// IsSQL reports whether the engine speaks SQL.
func (e Engine) IsSQL() bool { return rulesOf(e).IsSQL() }

// IsFile reports whether the engine's databases are files on this
// computer, opened by path rather than reached over the network.
func (e Engine) IsFile() bool { return len(rulesOf(e).FileExtensions()) > 0 }

// TransactionalDDL reports whether the engine's schema changes can run in
// a transaction, all or none, as PostgreSQL's, SQLite's and DuckDB's can.
func (e Engine) TransactionalDDL() bool { return rulesOf(e).TransactionalDDL() }

// Transactions reports whether a client can hold a transaction of the
// engine's open: ClickHouse and Redis have none.
func (e Engine) Transactions() bool { return rulesOf(e).Transactions() }

// Savepoints reports whether the engine nests a savepoint in an open
// transaction, which a failed statement can roll back to alone: DuckDB's
// grammar has none, and ClickHouse holds no transactions.
func (e Engine) Savepoints() bool { return rulesOf(e).Savepoints() }

// ColorHex tints the engine's icon, as "#rrggbb".
func (e Engine) ColorHex() string { return rulesOf(e).ColorHex() }

// DatabaseField is the connection form's label of the database, and what
// it shows when empty.
func (e Engine) DatabaseField() (label, placeholder string) { return rulesOf(e).DatabaseField() }

// CloudIdentity reports whether a connection may log in with an Identity.
func (e Engine) CloudIdentity() bool { return rulesOf(e).CloudIdentity() }

// ClearTextPassword reports whether a connection may send its password as
// clear text.
func (e Engine) ClearTextPassword() bool { return rulesOf(e).ClearTextPassword() }

// DatabasesNeedOwnConnection reports whether each database of a server
// is reached by a connection of its own.
func (e Engine) DatabasesNeedOwnConnection() bool { return rulesOf(e).DatabasesNeedOwnConnection() }

// DefaultSchemaName is the schema a database's tables are in unless
// named, "" when that is the connection's database.
func (e Engine) DefaultSchemaName() string { return rulesOf(e).DefaultSchemaName() }

// FileExtensions are those of a file engine's database files.
func (e Engine) FileExtensions() []string { return rulesOf(e).FileExtensions() }

// ActivityPages are the pages of the server's activity, none for an
// engine that runs inside the app.
func (e Engine) ActivityPages() []ActivityPage { return rulesOf(e).ActivityPages() }

// Explain is how to get a statement's plan, false when the engine cannot:
// an engine without SQL rules is sent EXPLAIN, and explains without
// running.
func Explain(e Engine, statement string, analyze bool) (ExplainRun, bool) {
	if r, ok := sqlRulesOf(e); ok {
		return r.Explain(statement, analyze)
	}
	if analyze {
		return explainWithPrefix("", statement), false
	}
	return explainWithPrefix("EXPLAIN ", statement), true
}

// NameFoldingOf is how the engine finds an unqualified name.
func NameFoldingOf(e Engine) NameFolding {
	if r, ok := sqlRulesOf(e); ok {
		return r.NameFolding()
	}
	return NamesIgnoreCase
}

// TablesMayIgnoreRollback reports whether a table may keep no
// transactions, as MySQL's MyISAM tables.
func TablesMayIgnoreRollback(e Engine) bool {
	r, ok := sqlRulesOf(e)
	return ok && r.TablesMayIgnoreRollback()
}

// ChangedRowsWord is how the engine counts the rows a change makes.
func ChangedRowsWord(e Engine) string {
	if r, ok := sqlRulesOf(e); ok {
		return r.ChangedRowsWord()
	}
	return "changed"
}

// UseSchemaSQL makes a session find unqualified names in a schema, ""
// for an engine whose sessions have no schema to change.
func UseSchemaSQL(e Engine, schema string) string {
	if r, ok := sqlRulesOf(e); ok {
		return r.UseSchemaSQL(schema)
	}
	return ""
}

// EditorSchemaPreamble is what a new editor on a schema starts with, ""
// for none.
func EditorSchemaPreamble(e Engine, schema string) string {
	if r, ok := sqlRulesOf(e); ok {
		return r.EditorSchemaPreamble(schema)
	}
	return ""
}

// ReadOnlyBeginSQL starts a transaction in which the server refuses
// writes, "" where the engine has none.
func ReadOnlyBeginSQL(e Engine) string {
	if r, ok := sqlRulesOf(e); ok {
		return r.ReadOnlyBeginSQL()
	}
	return ""
}

// ParameterAsText reports whether a parameter typed in goes to the server
// as text, for the server to read as the type it needs.
func ParameterAsText(e Engine) bool {
	r, ok := sqlRulesOf(e)
	return ok && r.ParameterAsText()
}

// ScriptPreamble is what a schema's script starts with; functions says it
// makes functions.
func ScriptPreamble(e Engine, functions bool) string {
	if r, ok := sqlRulesOf(e); ok {
		return r.ScriptPreamble(functions)
	}
	return ""
}

// FileReadingFunctions are the engine's functions that read the server's
// files or fetch URLs.
func FileReadingFunctions(e Engine) []string {
	if r, ok := sqlRulesOf(e); ok {
		return r.FileReadingFunctions()
	}
	return nil
}

// FileNamesInStrings reports whether the engine reads a file named by a
// string or a quoted name wherever a table goes, as DuckDB.
func FileNamesInStrings(e Engine) bool {
	r, ok := sqlRulesOf(e)
	return ok && r.FileNamesInStrings()
}

// LikeFilterSQL matches a column's text against pattern, a literal whose
// own % and _ are escaped with a backslash.
func LikeFilterSQL(e Engine, column, pattern string, not bool) string {
	if r, ok := sqlRulesOf(e); ok {
		return r.LikeFilterSQL(column, pattern, not)
	}
	return likeTextFilterSQL(column, pattern, not)
}

// JSONPathSQL reads a JSON column, quoted, at path, keys and array places;
// leaf reads a scalar as text rather than a part of the document.
func JSONPathSQL(e Engine, column string, path []any, leaf bool) string {
	if r, ok := sqlRulesOf(e); ok {
		return r.JSONPathSQL(column, path, leaf)
	}
	return jsonPathSQL(column, path, leaf, false)
}

// UpsertSQL makes an insert of a row update the row of its key, or keep
// it when updatedColumns is empty; names come quoted.
func UpsertSQL(e Engine, insert string, keys, updatedColumns []string) string {
	if r, ok := sqlRulesOf(e); ok {
		return r.UpsertSQL(insert, keys, updatedColumns)
	}
	return upsertOnConflictSQL(insert, keys, updatedColumns)
}

// ProfileBySummarize reports whether the engine profiles a table's
// columns itself, as DuckDB's SUMMARIZE.
func ProfileBySummarize(e Engine) bool {
	r, ok := sqlRulesOf(e)
	return ok && r.ProfileBySummarize()
}

// Comparable reports whether the server can compare a column type's
// values, for their minimum, maximum and distinct count.
func Comparable(e Engine, columnType string) bool {
	r, ok := sqlRulesOf(e)
	return !ok || r.Comparable(columnType)
}

// FirstColumn is the key column a new table starts with: one numbering
// its rows where the engine does.
func FirstColumn(e Engine) ColumnDesign {
	if r, ok := sqlRulesOf(e); ok {
		return r.FirstColumn()
	}
	return ColumnDesign{Name: "id", PrimaryKey: true}
}

// FixedKeysNote is what the table designer says of the keys and checks of
// a table made, "" when it can change them.
func FixedKeysNote(e Engine) string {
	if r, ok := sqlRulesOf(e); ok {
		return r.FixedKeysNote()
	}
	return ""
}

// UpdateSetsDefault reports whether an UPDATE may set a column to DEFAULT.
func UpdateSetsDefault(e Engine) bool {
	r, ok := sqlRulesOf(e)
	return !ok || r.UpdateSetsDefault()
}

// InterruptAbortsTransaction reports whether stopping a statement aborts
// the transaction open on its connection.
func InterruptAbortsTransaction(e Engine) bool {
	r, ok := sqlRulesOf(e)
	return ok && r.InterruptAbortsTransaction()
}

// StableOrderByKey reports whether a table's rows are read ordered by its
// key, for pages and edits to find the same rows.
func StableOrderByKey(e Engine) bool {
	r, ok := sqlRulesOf(e)
	return !ok || r.StableOrderByKey()
}

// BooleansAreNumbers reports whether the engine's booleans are 1 and 0.
func BooleansAreNumbers(e Engine) bool {
	r, ok := sqlRulesOf(e)
	return ok && r.BooleansAreNumbers()
}

// AccountForm is what the account form asks, and says, for the engine.
func AccountForm(e Engine) AccountFormFields {
	if r, ok := sqlRulesOf(e); ok {
		return r.AccountForm()
	}
	return AccountFormFields{GrantTargetLabel: "Database"}
}

// BackupNote says how the engine's databases are backed up, or restored.
func BackupNote(e Engine, restore bool) string {
	if r, ok := sqlRulesOf(e); ok {
		return r.BackupNote(restore)
	}
	return backupNoteFor(restore, sqlFileRestoreNote, mysqldumpBackupNote)
}

// BackupChoicesOf are what the backup dialog asks of the engine's backup.
func BackupChoicesOf(e Engine) BackupChoices {
	if r, ok := sqlRulesOf(e); ok {
		return r.BackupChoices()
	}
	return BackupChoices{}
}

// BackupScopeOf is the navigator node the engine backs up.
func BackupScopeOf(e Engine) BackupScope {
	if r, ok := sqlRulesOf(e); ok {
		return r.BackupScope()
	}
	return BackupScopeNone
}

// MaintenanceCommands are the commands that keep a table, named as
// quotedTable, in shape; a label says what the command locks.
func MaintenanceCommands(e Engine, quotedTable string) []MaintenanceCommand {
	if r, ok := sqlRulesOf(e); ok {
		return r.MaintenanceCommands(quotedTable)
	}
	return nil
}

// TruncateToEmpty reports whether a table's rows are all deleted by
// TRUNCATE, not DELETE.
func TruncateToEmpty(e Engine) bool {
	r, ok := sqlRulesOf(e)
	return ok && r.TruncateToEmpty()
}

// SchemaChangesCommit reports whether a schema change cannot be part of a
// transaction, so that a table is made before one opens.
func SchemaChangesCommit(e Engine) bool {
	_, ok := sqlRulesOf(e)
	return ok && !e.TransactionalDDL()
}
