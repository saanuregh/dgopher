package db

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/user"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"dgopher/internal/lists"
	"dgopher/internal/sqltext"

	mssql "github.com/microsoft/go-mssqldb"
	_ "github.com/microsoft/go-mssqldb/integratedauth/krb5" // the krb5 authenticator a connection without a user logs in with
	"github.com/microsoft/go-mssqldb/msdsn"
)

func init() {
	// The driver hands each message of PRINT and RAISERROR to its logger,
	// with the context of the statement that got it: the logger keeps it
	// for the session that ran the statement (Session.TakeMessages).
	mssql.SetContextLogger(sqlserverMessages{})
}

// sqlserverMessages gives the messages SQL Server sends with a statement
// to the session the statement's context names.
type sqlserverMessages struct{}

func (sqlserverMessages) Log(ctx context.Context, category msdsn.Log, msg string) {
	if category != msdsn.LogMessages {
		return
	}
	if l, ok := ctx.Value(messagesKey{}).(*messageLog); ok {
		l.add(msg)
	}
}

type sqlserverDialect struct{}

func (sqlserverDialect) Engine() Engine { return SQLServer }

// Quote writes a name in brackets, a ] in it doubled, which reads the same
// whatever QUOTED_IDENTIFIER is.
func (sqlserverDialect) Quote(s string) string { return "[" + strings.ReplaceAll(s, "]", "]]") + "]" }

func (sqlserverDialect) Placeholder(n int) string { return "@p" + strconv.Itoa(n) }
func (sqlserverDialect) Editable() (bool, string) { return true, "" }

type sqlserverEngine struct{ sqlserverDialect }

var _ SQLRules = sqlserverEngine{}

func (sqlserverEngine) Label() string                   { return "SQL Server" }
func (sqlserverEngine) Base() Engine                    { return SQLServer }
func (sqlserverEngine) ColorHex() string                { return "#cc2927" }
func (sqlserverEngine) DefaultPort() int                { return 1433 }
func (sqlserverEngine) DefaultUser() string             { return "sa" }
func (sqlserverEngine) Kind() EngineKind                { return KindSQL }
func (sqlserverEngine) Transactions() bool              { return true }
func (sqlserverEngine) DatabaseField() (string, string) { return "Database", "the login's default" }
func (sqlserverEngine) URLSchemes() []string            { return []string{"sqlserver", "mssql"} }

// Savepoints is false: SAVE TRANSACTION nests one, but a failed statement
// can doom the whole transaction, as every error does under XACT_ABORT
// ON, so that rolling back to it does not keep the rest.
func (sqlserverEngine) Savepoints() bool { return false }

// TransactionalDDL: CREATE, ALTER and DROP of tables and the like run in
// the transaction, which a rollback undoes; those of a database refuse
// one (OutsideTransaction).
func (sqlserverEngine) TransactionalDDL() bool { return true }

// URLTLSParameters are none of the other schemes': encrypt and
// TrustServerCertificate are read by readURL.
func (sqlserverEngine) URLTLSParameters() map[string]map[string]TLSMode { return nil }

func (sqlserverEngine) CloudIdentities() []Identity { return []Identity{IdentityAzureSQL} }
func (sqlserverEngine) ClearTextPassword() bool     { return false }

// ServerEnforcesReadOnly is false: SQL Server has no read-only session,
// and ApplicationIntent=ReadOnly only routes to a readable secondary of an
// availability group, writing on a primary as on any other connection.
func (sqlserverEngine) ServerEnforcesReadOnly() bool     { return false }
func (sqlserverEngine) ServerReadOnlySince() string      { return "" }
func (sqlserverEngine) DatabasesNeedOwnConnection() bool { return true }
func (sqlserverEngine) DefaultSchemaName() string        { return "dbo" }
func (sqlserverEngine) FileExtensions() []string         { return nil }

func (sqlserverEngine) dialect() Dialect { return sqlserverDialect{} }

func (sqlserverEngine) open(_ context.Context, cfg Config, ep endpoint, tc *tls.Config, w wire) (openedPool, error) {
	c, err := sqlserverConnector(cfg, ep, tc)
	if err != nil {
		return openedPool{}, err
	}
	return openedPool{sql: sql.OpenDB(w.connector(c)), tls: tc}, nil
}

// sqlserverConnector makes the connections of a SQL Server pool. Its
// settings are the connection's alone; the password and the token never
// go into a connection string.
func sqlserverConnector(cfg Config, ep endpoint, tc *tls.Config) (*mssql.Connector, error) {
	p, err := sqlserverParams(cfg, ep, tc)
	if err != nil {
		return nil, err
	}
	var c *mssql.Connector
	if cfg.Identity != "" {
		p.User, p.Password = "", ""
		c, err = mssql.NewSecurityTokenConnector(p, func(ctx context.Context) (string, error) { return identityToken(ctx, cfg) })
		if err != nil {
			return nil, err
		}
	} else {
		c = mssql.NewConnectorConfig(p)
	}
	if addr := net.JoinHostPort(ep.host, strconv.Itoa(ep.port)); ep.host != cfg.Host || ep.port != cfg.port() {
		c.Dialer = onlyAddress{addr: addr, hint: "set the server's connection policy to Proxy"}
	}
	return c, nil
}

// sqlserverParams are the driver's settings of a connection.
func sqlserverParams(cfg Config, ep endpoint, tc *tls.Config) (msdsn.Config, error) {
	q := url.Values{}
	q.Set("app name", appName)
	q.Set("dial timeout", "15")
	switch cfg.TLS {
	case "", TLSDisable, TLSPrefer:
		// The login is encrypted, and the rest when the server asks it:
		// encrypt=disable would send the password scrambled only.
		q.Set("encrypt", "false")
	default:
		q.Set("encrypt", "true")
	}
	kerberos := cfg.User == "" && cfg.Identity == ""
	if kerberos && runtime.GOOS != "windows" {
		// Windows logs in as its user (SSPI) without being told.
		cache, err := kerberosCache()
		if err != nil {
			return msdsn.Config{}, err
		}
		q.Set("authenticator", "krb5")
		q.Set("krb5-credcachefile", cache)
	}
	u := url.URL{Scheme: "sqlserver", Host: net.JoinHostPort(ep.host, strconv.Itoa(ep.port)), RawQuery: q.Encode()}
	p, err := msdsn.Parse(u.String())
	if err != nil {
		return msdsn.Config{}, err
	}
	p.User, p.Password, p.Database = cfg.User, cfg.Password, cfg.Database
	// Ours: verified against the connection's host even through a tunnel,
	// with the CA the user chose; the driver's would name the tunnel's
	// local address.
	if tc == nil {
		// disable: the login's TLS, unverified, as prefer's.
		tc = &tls.Config{InsecureSkipVerify: true, ServerName: cfg.Host}
	}
	p.TLSConfig = tc
	p.TrustServerCertificate = tc.InsecureSkipVerify
	// Kerberos asks for a ticket to the server the connection names, not
	// to a tunnel's local port.
	p.ServerSPN = "MSSQLSvc/" + net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.port()))
	p.LogFlags = msdsn.LogMessages
	return p, nil
}

// kerberosCache is the file of the credentials cache kinit fills: that of
// KRB5CCNAME, else MIT Kerberos' default.
func kerberosCache() (string, error) {
	name := os.Getenv("KRB5CCNAME")
	if name == "" {
		u, err := user.Current()
		if err != nil {
			return "", fmt.Errorf("Kerberos: %w", err)
		}
		return "/tmp/krb5cc_" + u.Uid, nil
	}
	if kind, path, ok := strings.Cut(name, ":"); ok && !strings.Contains(kind, "/") {
		if kind != "FILE" {
			return "", fmt.Errorf("Kerberos: KRB5CCNAME names a cache of kind %s; DGopher reads a file's, as KRB5CCNAME=FILE:/tmp/krb5cc_%s", kind, "<uid>")
		}
		return path, nil
	}
	return name, nil
}

// readURL reads what a sqlserver:// URL says besides host, port and
// user: the database is a parameter, as are encrypt and
// TrustServerCertificate, whose names the driver reads in any case.
func (sqlserverEngine) readURL(cfg *Config, u *url.URL) error {
	if path := strings.Trim(u.Path, "/"); path != "" {
		return fmt.Errorf("the URL names the instance %q: DGopher connects to an instance by its port, as host:port", path)
	}
	cfg.Database = ""
	var encrypt, trust string
	for key, values := range u.Query() {
		value := values[len(values)-1]
		switch strings.ToLower(key) {
		case "database", "initial catalog":
			cfg.Database = value
		case "encrypt":
			encrypt = strings.ToLower(value)
		case "trustservercertificate", "trust server certificate":
			trust = strings.ToLower(value)
		}
	}
	// The driver's booleans: ParseBool's, and yes and no.
	trusted, err := urlBool("TrustServerCertificate", strings.NewReplacer("yes", "true", "no", "false").Replace(trust))
	if err != nil {
		return err
	}
	switch encrypt {
	case "":
		// The driver encrypts the login only, unverified unless told to
		// verify.
		cfg.TLS = TLSPrefer
		if trust != "" && !trusted {
			cfg.TLS = TLSVerifyFull
		}
	case "disable":
		cfg.TLS = TLSDisable
	case "false", "optional", "no", "0", "f":
		cfg.TLS = TLSPrefer
	case "true", "mandatory", "yes", "1", "t":
		cfg.TLS = TLSVerifyFull
		if trust != "" && trusted {
			cfg.TLS = TLSRequire
		}
	case "strict":
		return errors.New("encrypt=strict (TDS 8) is not supported: use encrypt=true, which DGopher verifies")
	default:
		return fmt.Errorf("unknown encrypt=%s in the URL", encrypt)
	}
	return nil
}

// tlsState is what the server says of the session's connection, as
// prefer may encrypt the login alone; without VIEW SERVER STATE, it
// follows from the mode, which the driver refuses to connect otherwise,
// and is unknown for prefer.
func (sqlserverEngine) tlsState(ctx context.Context, sqldb *sql.DB, cfg Config, _ *tls.Config) string {
	var on string
	err := sqldb.QueryRowContext(ctx, "SELECT encrypt_option FROM sys.dm_exec_connections WHERE session_id = @@SPID AND parent_connection_id IS NULL").Scan(&on)
	switch {
	case err == nil:
		return tlsWord(strings.EqualFold(on, "TRUE"))
	case cfg.TLS == "" || cfg.TLS == TLSDisable:
		return tlsWord(false)
	case cfg.TLS == TLSRequire || cfg.TLS == TLSVerifyFull:
		return tlsWord(true)
	}
	return ""
}

// pingStatement is what the driver's ping runs.
func (sqlserverEngine) pingStatement() string { return "select 1;" }

// beginStatements stand for the driver's request beginning a
// transaction, of an isolation level when one is asked.
func (sqlserverEngine) beginStatements(opts driver.TxOptions) []string {
	var out []string
	switch sql.IsolationLevel(opts.Isolation) {
	case sql.LevelReadUncommitted:
		out = append(out, "SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED")
	case sql.LevelReadCommitted:
		out = append(out, "SET TRANSACTION ISOLATION LEVEL READ COMMITTED")
	case sql.LevelRepeatableRead:
		out = append(out, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ")
	case sql.LevelSnapshot:
		out = append(out, "SET TRANSACTION ISOLATION LEVEL SNAPSHOT")
	case sql.LevelSerializable:
		out = append(out, "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE")
	}
	return append(out, "BEGIN TRANSACTION")
}

func (sqlserverEngine) endStatement(commit bool) string {
	if commit {
		return "COMMIT"
	}
	return "ROLLBACK"
}

func (sqlserverEngine) pingsOnReset() bool   { return false }
func (sqlserverEngine) execUnprepared() bool { return false }
func (sqlserverEngine) bytesAreBinary() bool { return false }

// cancelQuerySQL is "": the driver stops a statement whose context ends
// with an attention, which keeps the connection and its transaction.
func (sqlserverEngine) cancelQuerySQL(int64) string { return "" }

// pingAfterError is false: the driver follows the transaction from what
// the server sends, errors' answers too.
func (sqlserverEngine) pingAfterError() bool                     { return false }
func (sqlserverEngine) takesServerParameters() bool              { return false }
func (sqlserverEngine) CopyFromStdin() bool                      { return false }
func (sqlserverEngine) CreateFile(context.Context, string) error { return noFileError(SQLServer) }

func (sqlserverEngine) VersionQuery() string {
	return "SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128))"
}

func (sqlserverEngine) LexDialect() sqltext.Dialect            { return sqltext.TSQL }
func (sqlserverEngine) readsAllResults() bool                  { return true }
func (sqlserverEngine) sessionDatabaseSQL() string             { return "SELECT DB_NAME()" }
func (sqlserverEngine) ConnectionFields() ConnectionFields     { return ConnectionFields{} }
func (sqlserverEngine) sessionStartSQL() []string              { return nil }
func (sqlserverEngine) messagesSQL() string                    { return "" }
func (sqlserverEngine) transactionStateAndMessagesSQL() string { return "" }

// identify reads the server's version; SQL Server has no flavors, and
// Azure SQL's edition only names it.
func (sqlserverEngine) identify(ctx context.Context, q *sql.DB) (Engine, string, error) {
	var version string
	var edition int
	err := q.QueryRowContext(ctx, `SELECT CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(128)), CAST(SERVERPROPERTY('EngineEdition') AS int)`).
		Scan(&version, &edition)
	if err != nil {
		return "", "", err
	}
	return SQLServer, sqlserverVersion(version, edition), nil
}

// sqlserverVersion is a server's version, Azure's editions named
// (SERVERPROPERTY('EngineEdition')).
func sqlserverVersion(version string, edition int) string {
	switch edition {
	case 5:
		return version + " (Azure SQL Database)"
	case 8:
		return version + " (Azure SQL Managed Instance)"
	case 9:
		return version + " (Azure SQL Edge)"
	case 11:
		return version + " (Azure Synapse serverless SQL pool)"
	case 12:
		return version + " (Microsoft Fabric SQL database)"
	}
	return version
}

func (sqlserverEngine) BeginStatement() string   { return "BEGIN TRANSACTION" }
func (sqlserverEngine) ReadOnlyBeginSQL() string { return "" }

// followsTransactionByVerbs is false: the driver keeps the transaction
// the server says begins and ends (readTxState), however a batch did it.
func (sqlserverEngine) followsTransactionByVerbs() bool { return false }

func (sqlserverEngine) afterFailedStatement(v txByVerbs, _ error, _ bool, _ func() bool) txByVerbs {
	return v
}

// autocommitMayBeOff: SET IMPLICIT_TRANSACTIONS ON, and ANSI_DEFAULTS ON,
// make the next statement that reads or writes begin a transaction.
func (sqlserverEngine) autocommitMayBeOff() bool                              { return true }
func (sqlserverEngine) ExecutableComments() bool                              { return false }
func (sqlserverEngine) commitsImplicitlyWords([]string, func() []string) bool { return false }

// setsAutocommitWords reads SET IMPLICIT_TRANSACTIONS and SET ANSI_DEFAULTS,
// which turn autocommit off when ON: the last of a batch counts.
func (sqlserverEngine) setsAutocommitWords(_ []string, all func() []string) (on, ok bool) {
	words := all()
	for i := 0; i < len(words); i++ {
		if words[i] != "SET" {
			continue
		}
		sets := false
		j := i + 1
		for ; j < len(words) && words[j] != "ON" && words[j] != "OFF"; j++ {
			switch words[j] {
			case "IMPLICIT_TRANSACTIONS", "ANSI_DEFAULTS":
				sets = true
			case ",":
			default:
				if !strings.HasPrefix(words[j], "ANSI_") && !isTSQLOption(words[j]) {
					j = len(words) // another SET, as of a variable
				}
			}
		}
		if sets && j < len(words) {
			on, ok = words[j] == "OFF", true
		}
	}
	return on, ok
}

// isTSQLOption reports a SET option that is ON or OFF, which a SET may
// list with others.
func isTSQLOption(w string) bool {
	switch w {
	case "NOCOUNT", "XACT_ABORT", "ARITHABORT", "ARITHIGNORE", "CONCAT_NULL_YIELDS_NULL", "CURSOR_CLOSE_ON_COMMIT",
		"FMTONLY", "FORCEPLAN", "NOEXEC", "NUMERIC_ROUNDABORT", "PARSEONLY", "QUOTED_IDENTIFIER", "REMOTE_PROC_TRANSACTIONS",
		"SHOWPLAN_ALL", "SHOWPLAN_TEXT", "SHOWPLAN_XML", "IMPLICIT_TRANSACTIONS", "ANSI_DEFAULTS":
		return true
	}
	return false
}

// beginIsCompound: BEGIN … END, BEGIN TRY, BEGIN CATCH, BEGIN DIALOG and
// BEGIN CONVERSATION TIMER begin no transaction.
func (sqlserverEngine) beginIsCompound(words []string) bool {
	switch wordAt(words, 1) {
	case "TRAN", "TRANSACTION", "DISTRIBUTED":
		return false
	}
	return true
}

func (sqlserverEngine) SavepointBeginsTransaction() bool { return false }

// sqlserverOutside are the statements SQL Server refuses inside a
// transaction, by their first two words, or the first alone: CREATE, ALTER
// and DROP of a database, a full-text catalog or index and an endpoint,
// BACKUP, RESTORE, RECONFIGURE, KILL and the shrinking DBCCs.
var sqlserverOutside = map[string][]string{
	"CREATE":      {"DATABASE", "FULLTEXT", "ENDPOINT"},
	"ALTER":       {"DATABASE", "FULLTEXT", "ENDPOINT"},
	"DROP":        {"DATABASE", "FULLTEXT", "ENDPOINT"},
	"BACKUP":      {"DATABASE", "LOG"},
	"RESTORE":     {"DATABASE", "LOG", "HEADERONLY", "FILELISTONLY", "VERIFYONLY", "LABELONLY", "REWINDONLY"},
	"DBCC":        {"SHRINKDATABASE", "SHRINKFILE"},
	"RECONFIGURE": nil,
	"KILL":        nil,
}

// OutsideTransaction reports a batch holding a statement SQL Server
// refuses inside a transaction (sqlserverOutside), found by its words
// wherever the batch has them: they are reserved, so unquoted they name
// nothing. ALTER DATABASE SCOPED CONFIGURATION, and the audit
// specifications and encryption key of a database, run in one; so does
// a routine's or a trigger's CREATE, whose body runs later.
func (sqlserverEngine) OutsideTransaction(sql string) bool {
	words := tsqlKeywords(sql)
	if routineDefinition(words) {
		return false
	}
	for i, w := range words {
		next, ok := sqlserverOutside[w]
		if !ok {
			continue
		}
		if next == nil {
			if w == "KILL" && (wordAt(words, i+1) == "QUERY" || wordAt(words, i+1) == "STATS") {
				continue
			}
			return true
		}
		if !slices.Contains(next, wordAt(words, i+1)) {
			continue
		}
		if wordAt(words, i+1) == "DATABASE" {
			switch wordAt(words, i+2) {
			case "SCOPED", "AUDIT", "ENCRYPTION":
				continue
			}
		}
		return true
	}
	return false
}

// TakesEffectAtOnce: UPDATE STATISTICS, and sp_updatestats running it,
// keep what they did when the transaction they ran in rolls back.
func (sqlserverEngine) TakesEffectAtOnce(sql string) bool {
	words := tsqlKeywords(sql)
	if routineDefinition(words) {
		return false
	}
	for i, w := range words {
		switch {
		case w == "UPDATE" && wordAt(words, i+1) == "STATISTICS":
			return true
		case w == "SP_UPDATESTATS":
			// Called, by its name alone or in sys: not a column so named.
			if i == 0 || slices.Contains([]string{"EXEC", "EXECUTE", "SYS"}, words[i-1]) {
				return true
			}
		}
	}
	return false
}

// routineDefinition reports a batch that makes or changes a procedure, a
// function or a trigger, whose statements run when it is called.
func routineDefinition(words []string) bool {
	i := 1
	switch wordAt(words, 0) {
	case "CREATE":
		if wordAt(words, 1) == "OR" && wordAt(words, 2) == "ALTER" {
			i = 3
		}
	case "ALTER":
	default:
		return false
	}
	switch wordAt(words, i) {
	case "PROCEDURE", "PROC", "FUNCTION", "TRIGGER":
		return true
	}
	return false
}

// tsqlKeywords are the keywords and names of a batch, in upper case,
// outside its strings, comments and quoted names.
func tsqlKeywords(sql string) []string { return nameWords(sql, sqltext.TSQL) }

func (sqlserverEngine) RollbackMayKeepChanges() bool { return false }

// RowCountWord: an UPDATE counts the rows it matched, set to their own
// values too.
func (sqlserverEngine) RowCountWord() string { return "matched" }

func (sqlserverEngine) ownRowCountSQL() string { return "SELECT @@ROWCOUNT" }

// implicitTransactionsSQL reads IMPLICIT_TRANSACTIONS, bit 2 of @@OPTIONS,
// which the server's user options may set at login.
func (sqlserverEngine) implicitTransactionsSQL() string { return "SELECT (@@OPTIONS & 2) / 2" }
func (sqlserverEngine) transactionStateSQL() string     { return "" }
func (sqlserverEngine) driverCommitsStatements() bool   { return false }

// Explain: under SET SHOWPLAN_XML the session returns each statement's
// estimated plan instead of running it; under SET STATISTICS XML it runs
// it and returns its plan as it ran after its rows. Each SET must stand
// alone in its batch (error 1067).
func (sqlserverEngine) Explain(statement string, analyze bool) (ExplainRun, bool) {
	set := "SET SHOWPLAN_XML"
	if analyze {
		set = "SET STATISTICS XML"
	}
	return ExplainRun{Before: []string{set + " ON"}, Statement: statement, After: []string{set + " OFF"}}, true
}

// UseSchemaSQL is "": a SQL Server session finds names in its user's
// default schema, which the session cannot change.
func (sqlserverEngine) UseSchemaSQL(string) string         { return "" }
func (sqlserverEngine) EditorSchemaPreamble(string) string { return "" }

// NameFolding: names are found as the database's collation compares
// them, without case unless it is a case-sensitive one.
func (sqlserverEngine) NameFolding() NameFolding { return NamesIgnoreCase }

func (sqlserverEngine) Search(ctx context.Context, d *DB, text string, definitions bool) ([]SearchHit, bool, error) {
	return sqlserverSearch(ctx, d, strings.ToLower(text), definitions)
}

func (sqlserverEngine) EnumValues(context.Context, *DB, string) ([]string, error) { return nil, nil }

func (sqlserverEngine) ParameterAsText() bool { return true }

// TextLiteral is an N'…' string, which keeps every character whatever the
// database's code page.
func (sqlserverEngine) TextLiteral(s string) string {
	return "N'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func (sqlserverEngine) BytesLiteral(b []byte) string { return "0x" + hex.EncodeToString(b) }

// BooleanLiteral: T-SQL has no TRUE; a bit is 1 or 0.
func (sqlserverEngine) BooleanLiteral(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func (sqlserverEngine) BooleansAreNumbers() bool { return true }
func (sqlserverEngine) OffsetTimesAsUTC() bool   { return false }

func (sqlserverEngine) CastParameter(placeholder, columnType string) string {
	return "CAST(" + placeholder + " AS " + columnType + ")"
}

// KeyParameter converts a datetime or smalldatetime key: the driver sends
// a time as datetimeoffset, which a datetime's thirds of a millisecond,
// .123 kept as .1233333, do not equal.
func (sqlserverEngine) KeyParameter(placeholder, columnType string) string {
	switch typeName(columnType) {
	case "datetime", "smalldatetime":
		return "CAST(" + placeholder + " AS " + columnType + ")"
	}
	return placeholder
}

func (sqlserverEngine) InsertDefaultRowSQL(table string) string {
	return "INSERT INTO " + table + " DEFAULT VALUES"
}

// MaxParameters is the most parameters a statement may have: a request
// takes 2100, two of them sp_executesql's own, which the driver sends.
func (sqlserverEngine) MaxParameters() int               { return 2098 }
func (sqlserverEngine) ProfilesInOneQuery() bool         { return false }
func (sqlserverEngine) CountSQL(column, _ string) string { return "count(" + column + ")" }
func (sqlserverEngine) NullSQL() string                  { return "NULL" }

func (sqlserverEngine) Comparable(columnType string) bool {
	switch typeName(columnType) {
	case "xml", "text", "ntext", "image", "geography", "geometry", "bit":
		return false
	}
	return true
}

func (sqlserverEngine) StableOrderByKey() bool           { return true }
func (sqlserverEngine) UpdateSetsDefault() bool          { return true }
func (sqlserverEngine) InterruptAbortsTransaction() bool { return false }

// FileReadingFunctions are SQL Server's that read the server's files or
// reach other servers.
func (sqlserverEngine) FileReadingFunctions() []string {
	return []string{"openrowset", "opendatasource", "openquery", "xp_cmdshell", "xp_dirtree", "xp_fileexist"}
}

func (sqlserverEngine) FileNamesInStrings() bool { return false }

// LikeFilterSQL escapes the pattern's [ too, which SQL Server's LIKE reads
// as the start of a set of characters; without one, ] and ^ are text. A
// [ in the literal is the pattern's: T-SQL quotes a string with ' alone.
func (sqlserverEngine) LikeFilterSQL(column, pattern string, not bool) string {
	return "CAST(" + column + " AS nvarchar(max))" + likeOperator(false, not) + strings.ReplaceAll(pattern, "[", `\[`) + ` ESCAPE '\'`
}

func (sqlserverEngine) JSONPathSQL(column string, path []any, leaf bool) string {
	var b strings.Builder
	b.WriteString("$")
	for _, p := range path {
		switch x := p.(type) {
		case int:
			b.WriteString("[" + strconv.Itoa(x) + "]")
		case string:
			b.WriteString(`."` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(x) + `"`)
		}
	}
	fn := "JSON_QUERY"
	if leaf {
		fn = "JSON_VALUE"
	}
	return fn + "(" + column + ", " + standardLiteral(b.String()) + ")"
}

// UpsertSQL writes the row as a MERGE on its key: HOLDLOCK keeps two from
// inserting one key.
func (sqlserverEngine) UpsertSQL(u Upsert) string {
	list := strings.Join(u.Columns, ", ")
	on := lists.Map(u.Keys, func(k string) string { return "target." + k + " = source." + k })
	merge := "MERGE INTO " + u.Table + " WITH (HOLDLOCK) AS target\nUSING (VALUES (" + strings.Join(u.Values, ", ") + ")) AS source (" + list + ")\n" +
		"ON " + strings.Join(on, " AND ") + "\n"
	if len(u.UpdatedColumns) > 0 {
		sets := lists.Map(u.UpdatedColumns, func(c string) string { return c + " = source." + c })
		merge += "WHEN MATCHED THEN UPDATE SET " + strings.Join(sets, ", ") + "\n"
	}
	sources := lists.Map(u.Columns, func(c string) string { return "source." + c })
	return merge + "WHEN NOT MATCHED THEN INSERT (" + list + ") VALUES (" + strings.Join(sources, ", ") + ")"
}

// RowLimit: T-SQL limits rows with TOP, which needs no ORDER BY, where
// OFFSET … FETCH would.
func (sqlserverEngine) RowLimit(n int) (string, string) {
	return "TOP (" + strconv.Itoa(n) + ") ", ""
}

// ShownColumnSQL reads geography and geometry as their WKT, with Z and M,
// and hierarchyid as its /1/2/ path: SQL Server converts the text back
// when an edit writes it.
func (sqlserverEngine) ShownColumnSQL(column, columnType string) string {
	switch typeName(columnType) {
	case "geography", "geometry", "hierarchyid":
		return column + ".ToString()"
	}
	return ""
}
