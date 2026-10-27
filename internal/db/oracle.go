package db

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/lists"
	"dgopher/internal/sqltext"

	go_ora "github.com/sijms/go-ora/v3"
	"github.com/sijms/go-ora/v3/configurations"
	"github.com/sijms/go-ora/v3/network"
)

type oracleDialect struct{}

func (oracleDialect) Engine() Engine { return Oracle }

// Quote writes a name in double quotes, which Oracle keeps as written.
// No Oracle name holds a double quote.
func (oracleDialect) Quote(s string) string    { return quoteDouble(s) }
func (oracleDialect) Placeholder(n int) string { return ":" + strconv.Itoa(n) }
func (oracleDialect) Editable() (bool, string) { return true, "" }

type oracleEngine struct{ oracleDialect }

var _ SQLRules = oracleEngine{}

func (oracleEngine) Label() string    { return "Oracle" }
func (oracleEngine) Base() Engine     { return Oracle }
func (oracleEngine) ColorHex() string { return "#c74634" }
func (oracleEngine) DefaultPort() int { return 1521 }

// DefaultUser is SYSTEM, the administrator every installation has; SYS
// logs in only AS SYSDBA.
func (oracleEngine) DefaultUser() string  { return "system" }
func (oracleEngine) Kind() EngineKind     { return KindSQL }
func (oracleEngine) Transactions() bool   { return true }
func (oracleEngine) URLSchemes() []string { return []string{"oracle"} }

// DatabaseField is the service the listener knows the database by, as a
// pluggable database's own (decision 11 of the more-engines plan: each
// PDB is a connection of its own).
func (oracleEngine) DatabaseField() (string, string) {
	return "Service name", "the service, as FREEPDB1"
}

// Savepoints is false: Oracle has SAVEPOINT and ROLLBACK TO SAVEPOINT,
// but no RELEASE SAVEPOINT, which grid edits nested in an open
// transaction end with.
func (oracleEngine) Savepoints() bool { return false }

// TransactionalDDL is false: a schema change commits the open
// transaction before it runs and after (OracleCommitsImplicitly).
func (oracleEngine) TransactionalDDL() bool { return false }

// URLTLSParameters are go-ora's ssl in lower case, as the other schemes'
// TLS parameters are read; readURL reads it in any case, with SSL VERIFY.
func (oracleEngine) URLTLSParameters() map[string]map[string]TLSMode {
	return map[string]map[string]TLSMode{"ssl": {"true": TLSVerifyFull, "enable": TLSVerifyFull, "enabled": TLSVerifyFull,
		"false": TLSDisable, "disable": TLSDisable, "disabled": TLSDisable}}
}

// CloudIdentities are none: OCI IAM tokens are deferred (decision 15).
func (oracleEngine) CloudIdentities() []Identity { return nil }
func (oracleEngine) ClearTextPassword() bool     { return false }

// ServerEnforcesReadOnly: from 23ai a session with READ_ONLY set refuses
// DML, DDL, LOCK, SELECT … FOR UPDATE and what autonomous transactions
// write; before, Oracle has no read-only session (ServerReadOnlySince).
func (oracleEngine) ServerEnforcesReadOnly() bool     { return true }
func (oracleEngine) ServerReadOnlySince() string      { return "23ai" }
func (oracleEngine) DatabasesNeedOwnConnection() bool { return false }

// DefaultSchemaName is "": a session's names are found in its user's
// schema (CurrentSchema).
func (oracleEngine) DefaultSchemaName() string { return "" }
func (oracleEngine) FileExtensions() []string  { return nil }

func (oracleEngine) dialect() Dialect { return oracleDialect{} }

func (oracleEngine) ConnectionFields() ConnectionFields {
	return ConnectionFields{SID: true, Wallet: true}
}

// checkConfig needs the service name or SID, which go-ora cannot do
// without, and TLS for a wallet. TLS prefer is refused: an Oracle
// listener speaks TCPS or TCP on a port of its own, so a connection
// cannot fall back from one to the other.
func (oracleEngine) checkConfig(c *Config) []string {
	var errs []string
	switch {
	case strings.TrimSpace(c.Database) != "":
	case c.Oracle.SID:
		errs = append(errs, "the instance's SID is required")
	default:
		errs = append(errs, "the service name is required, as FREEPDB1 or ORCLPDB1")
	}
	if c.TLS == TLSPrefer {
		errs = append(errs, "Oracle cannot fall back from TLS to plain text, which listen on ports of their own: choose disable, require or verify-full")
	}
	if c.Oracle.Wallet != "" && (c.TLS == "" || c.TLS == TLSDisable) {
		errs = append(errs, "a wallet needs TLS: choose require or verify-full")
	}
	return errs
}

func (oracleEngine) open(_ context.Context, cfg Config, ep endpoint, tc *tls.Config, w wire) (openedPool, error) {
	c, err := newOracleConnector(cfg, ep, tc, w)
	if err != nil {
		return openedPool{}, err
	}
	return openedPool{sql: sql.OpenDB(w.connector(c)), tls: c.tls}, nil
}

// oracleConnector makes the connections of an Oracle pool, each from a
// connector of its own, as go-ora writes into the TLS configuration it is
// given; and sets each up as the connection's settings say.
type oracleConnector struct {
	driver *go_ora.OracleDriver
	// dsn holds the password: it never goes into an error or a log.
	dsn      string
	tls      *tls.Config
	dialer   configurations.DialerContext
	readOnly bool
	w        wire
}

func newOracleConnector(cfg Config, ep endpoint, tc *tls.Config, w wire) (*oracleConnector, error) {
	if tc != nil && cfg.Oracle.Wallet != "" {
		var err error
		if tc, err = withOracleWallet(tc, cfg.Oracle.Wallet); err != nil {
			return nil, err
		}
	}
	if tc != nil {
		tc = oracleVerifiedAs(tc, ep.serverName)
	}
	c := &oracleConnector{driver: newOracleDriver(), dsn: oracleConnectString(cfg, ep, tc != nil), tls: tc, readOnly: cfg.ReadOnly, w: w}
	if _, err := go_ora.ParseConfig(c.dsn); err != nil {
		// Its error may quote the connect string, the password with it.
		return nil, errors.New("the connection's settings make no Oracle connect string: check its host, port, user and service name")
	}
	if addr := net.JoinHostPort(ep.host, strconv.Itoa(ep.port)); ep.host != cfg.Host || ep.port != cfg.port() {
		c.dialer = onlyAddress{addr: addr, hint: "connect to the instance's own listener, or to a service its listener does not redirect"}
	}
	return c, nil
}

// oracleConnectString is the go-ora URL of a connection: the service or
// SID, the user and the password, and the program name the server shows;
// TLS is the connector's, whose configuration the URL leaves to it.
func oracleConnectString(cfg Config, ep endpoint, tls bool) string {
	q := url.Values{}
	service := cfg.Database
	if cfg.Oracle.SID {
		q.Set("SID", cfg.Database)
		service = ""
	}
	if tls {
		q.Set("SSL", "TRUE")
	}
	q.Set("PROGRAM", appName)
	q.Set("CONNECT TIMEOUT", "15")
	// go-ora v3.0.1 logs in again from what its first login kept of the
	// server, process-wide, and fails with EOF once that connection's pool
	// closed.
	q.Set("FAST LOGIN", "FALSE")
	u := url.URL{Scheme: "oracle", User: url.UserPassword(cfg.User, cfg.Password), Host: net.JoinHostPort(ep.host, strconv.Itoa(ep.port)),
		Path: "/" + service, RawQuery: q.Encode()}
	return u.String()
}

// oracleVerifiedAs returns tc, checking the server's certificate against
// name itself where tc verifies: go-ora sets ServerName to the address it
// dials, a tunnel's local one too.
func oracleVerifiedAs(tc *tls.Config, name string) *tls.Config {
	tc = tc.Clone()
	if tc.InsecureSkipVerify {
		return tc
	}
	roots := tc.RootCAs
	tc.InsecureSkipVerify = true
	tc.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("the server sent no certificate")
		}
		opts := x509.VerifyOptions{DNSName: name, Roots: roots, Intermediates: x509.NewCertPool()}
		for _, c := range cs.PeerCertificates[1:] {
			opts.Intermediates.AddCert(c)
		}
		_, err := cs.PeerCertificates[0].Verify(opts)
		return err
	}
	return tc
}

// withOracleWallet adds to tc the certificates of the wallet in dir: those
// it trusts, besides the system's or the chosen CA's, and the client
// certificate its private key goes with, unless a certificate file gives
// one. The wallet is read from its auto-login file, cwallet.sso; a wallet
// that only a password opens (ewallet.p12) is not read.
func withOracleWallet(tc *tls.Config, dir string) (*tls.Config, error) {
	w, err := configurations.NewWallet(filepath.Join(ExpandPath(dir), "cwallet.sso"))
	if err != nil {
		return nil, fmt.Errorf("reading the wallet's cwallet.sso in %s: %w", dir, err)
	}
	tc = tc.Clone()
	roots := tc.RootCAs
	if roots == nil {
		if roots, err = x509.SystemCertPool(); err != nil {
			roots = x509.NewCertPool()
		}
	}
	for _, der := range w.Certificates {
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("reading the wallet's certificates: %w", err)
		}
		roots.AddCert(cert)
		if len(tc.Certificates) > 0 {
			continue
		}
		for _, k := range w.PrivateKeys {
			key, err := x509.ParsePKCS1PrivateKey(k)
			if err == nil && key.PublicKey.Equal(cert.PublicKey) {
				tc.Certificates = []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}}
			}
		}
	}
	tc.RootCAs = roots
	return tc, nil
}

// oracleSessionSettings make the text of times and numbers the same on
// every connection, whatever the server's territory: what an edit types
// reads back, and the grid shows ISO dates.
const oracleSessionSettings = "ALTER SESSION SET NLS_DATE_FORMAT = 'YYYY-MM-DD HH24:MI:SS' NLS_TIMESTAMP_FORMAT = 'YYYY-MM-DD HH24:MI:SS.FF' " +
	"NLS_TIMESTAMP_TZ_FORMAT = 'YYYY-MM-DD HH24:MI:SS.FF TZH:TZM' NLS_NUMERIC_CHARACTERS = '.,'"

// oracleReadOnlySQL makes the session refuse writes, from 23ai.
const oracleReadOnlySQL = "ALTER SESSION SET READ_ONLY = TRUE"

func (c *oracleConnector) Connect(ctx context.Context) (driver.Conn, error) {
	inner, err := c.driver.OpenConnector(c.dsn)
	if err != nil {
		return nil, err
	}
	oc := inner.(*go_ora.OracleConnector)
	if c.tls != nil {
		oc.WithTLSConfig(c.tls.Clone())
	}
	if c.dialer != nil {
		oc.Dialer(c.dialer)
	}
	dc, err := oc.Connect(ctx)
	if err != nil {
		return nil, err
	}
	conn := dc.(*go_ora.Connection)
	setup := []string{oracleSessionSettings}
	if c.readOnly {
		setup = append(setup, oracleReadOnlySQL)
	}
	for _, text := range setup {
		start := time.Now()
		_, err := conn.ExecContext(ctx, text, nil)
		c.w.record(text, 0, start, err)
		if text == oracleReadOnlySQL {
			// Before 23ai, which answers ORA-02248, an invalid option of
			// ALTER SESSION, the app's check alone refuses writes.
			if oracleErrorIs(err, 2248) {
				err = nil
			}
		}
		if err != nil {
			conn.Close()
			return nil, err
		}
	}
	return oracleConn{conn}, nil
}

func (c *oracleConnector) Driver() driver.Driver { return c.driver }

// oracleErrorIs reports an error the server answered with ORA-code.
func oracleErrorIs(err error, code int) bool {
	var oe *network.OracleError
	return errors.As(err, &oe) && oe.ErrCode == code
}

// readURL reads what an oracle:// URL, as go-ora writes it, says besides
// host, port and user: the path is the service; SID, SSL, SSL VERIFY and
// WALLET are parameters. Any other parameter is refused, rather than
// dropped, as DBA PRIVILEGE, which would log in otherwise.
func (oracleEngine) readURL(cfg *Config, u *url.URL) error {
	cfg.Database = strings.Trim(u.Path, "/")
	on := func(v string) bool {
		return oracleEngine{}.URLTLSParameters()["ssl"][strings.ToLower(v)] == TLSVerifyFull
	}
	ssl, verify := cfg.TLS == TLSVerifyFull, ""
	for key, values := range u.Query() {
		value := values[len(values)-1]
		switch strings.ToUpper(key) {
		case "SID":
			if cfg.Database != "" {
				return fmt.Errorf("the URL names both a service, %s, and a SID, %s", cfg.Database, value)
			}
			cfg.Database, cfg.Oracle.SID = value, true
		case "SSL":
			if key != "ssl" {
				ssl = on(value)
			}
		case "SSL VERIFY":
			verify = value
		case "WALLET":
			cfg.Oracle.Wallet = value
		default:
			return fmt.Errorf("the URL's %s is not supported: DGopher connects with host, port, service or SID, user, password, SSL and WALLET", key)
		}
	}
	switch {
	case ssl && verify != "" && !on(verify):
		cfg.TLS = TLSRequire
	case ssl:
		cfg.TLS = TLSVerifyFull
	case verify != "":
		return errors.New("the URL's SSL VERIFY needs SSL=TRUE")
	}
	return nil
}

// oracleDescriptorConfig reads a connect descriptor, as tnsnames.ora and
// Oracle's tools write them, (DESCRIPTION=(ADDRESS=…)(CONNECT_DATA=…)):
// its one address, the service or SID, and TCPS as verified TLS.
func oracleDescriptorConfig(s string) (Config, error) {
	var info configurations.DatabaseInfo
	if err := info.UpdateDatabaseInfo(s); err != nil {
		return Config{}, fmt.Errorf("reading the connect descriptor: %w", err)
	}
	if len(info.Servers) != 1 {
		return Config{}, fmt.Errorf("the connect descriptor names %d addresses: DGopher connects to one, as host and port", len(info.Servers))
	}
	srv := info.Servers[0]
	cfg := Config{Engine: Oracle, Host: srv.Addr, Port: srv.Port, Database: info.ServiceName}
	if info.SID != "" {
		if cfg.Database != "" {
			return Config{}, errors.New("the connect descriptor names both a SERVICE_NAME and a SID")
		}
		cfg.Database, cfg.Oracle.SID = info.SID, true
	}
	if strings.EqualFold(srv.Protocol, "tcps") {
		cfg.TLS = TLSVerifyFull
	}
	cfg.Name = cfg.Host + "/" + cfg.Database
	return cfg, nil
}

// tlsState is what the server says of the session's connection.
func (oracleEngine) tlsState(ctx context.Context, sqldb *sql.DB, _ Config, _ *tls.Config) string {
	var protocol string
	if err := sqldb.QueryRowContext(ctx, "SELECT SYS_CONTEXT('USERENV', 'NETWORK_PROTOCOL') FROM dual").Scan(&protocol); err != nil {
		return ""
	}
	return tlsWord(strings.EqualFold(protocol, "tcps"))
}

// pingStatement stands for go-ora's ping, a protocol message.
func (oracleEngine) pingStatement() string { return "-- ping" }

// beginStatements are none: go-ora's BeginTx sends nothing, and only
// stops committing the statements that follow.
func (oracleEngine) beginStatements(driver.TxOptions) []string { return nil }

// endStatement stands for go-ora's commit and rollback calls.
func (oracleEngine) endStatement(commit bool) string {
	if commit {
		return "COMMIT"
	}
	return "ROLLBACK"
}

func (oracleEngine) pingsOnReset() bool   { return false }
func (oracleEngine) execUnprepared() bool { return false }
func (oracleEngine) bytesAreBinary() bool { return false }

// cancelQuerySQL is "": go-ora breaks the statement its context ends,
// which keeps the connection and its transaction.
func (oracleEngine) cancelQuerySQL(int64) string { return "" }

// pingAfterError is false: the transaction is read after every statement
// (transactionStateSQL).
func (oracleEngine) pingAfterError() bool                     { return false }
func (oracleEngine) takesServerParameters() bool              { return false }
func (oracleEngine) CopyFromStdin() bool                      { return false }
func (oracleEngine) CreateFile(context.Context, string) error { return noFileError(Oracle) }

func (oracleEngine) VersionQuery() string {
	return "SELECT version_full FROM product_component_version WHERE ROWNUM = 1"
}

func (oracleEngine) LexDialect() sqltext.Dialect { return sqltext.Oracle }
func (oracleEngine) readsAllResults() bool       { return false }
func (oracleEngine) sessionDatabaseSQL() string  { return "" }

// sessionStartSQL lets the session's PL/SQL print with DBMS_OUTPUT, which
// messagesSQL reads; NULL is no limit to what it keeps.
func (oracleEngine) sessionStartSQL() []string {
	return []string{"BEGIN DBMS_OUTPUT.ENABLE(NULL); END;"}
}

func (oracleEngine) messagesSQL() string { return oracleMessagesSQL }

// oracleMessagesSQL reads the lines DBMS_OUTPUT kept, maxMessages of them
// and the count of the others, in a query, which the driver never commits
// as it does a PL/SQL block.
var oracleMessagesSQL = `WITH FUNCTION dgopher_output RETURN CLOB IS
  line VARCHAR2(32767);
  status INTEGER;
  shown PLS_INTEGER := 0;
  more PLS_INTEGER := 0;
  text CLOB;
BEGIN
  LOOP
    DBMS_OUTPUT.GET_LINE(line, status);
    EXIT WHEN status <> 0;
    IF shown < ` + strconv.Itoa(maxMessages) + ` THEN
      text := text || line || CHR(31);
      shown := shown + 1;
    ELSE
      more := more + 1;
    END IF;
  END LOOP;
  IF more > 0 THEN
    text := text || CHR(30) || more;
  END IF;
  RETURN text;
END;
SELECT dgopher_output FROM dual`

// identify reads the server's version; Oracle has no flavors, and
// Autonomous Database's service only names it.
func (oracleEngine) identify(ctx context.Context, q *sql.DB) (Engine, string, error) {
	var version string
	var cloud sql.NullString
	err := q.QueryRowContext(ctx, `SELECT (SELECT version_full FROM product_component_version WHERE ROWNUM = 1), SYS_CONTEXT('USERENV', 'CLOUD_SERVICE') FROM dual`).
		Scan(&version, &cloud)
	if err != nil {
		return "", "", err
	}
	if cloud.String != "" {
		version += " (Autonomous Database, " + cloud.String + ")"
	}
	return Oracle, version, nil
}

// BeginStatement begins a transaction the server holds at once, which a
// session's transactionStateSQL then reads as open: Oracle has no BEGIN.
func (oracleEngine) BeginStatement() string   { return "SET TRANSACTION READ WRITE" }
func (oracleEngine) ReadOnlyBeginSQL() string { return "SET TRANSACTION READ ONLY" }

// followsTransactionByVerbs is true, for when transactionStateSQL cannot
// be read.
func (oracleEngine) followsTransactionByVerbs() bool { return true }

// afterFailedStatement keeps the transaction: a failed DDL statement may
// have committed it or not, which the server's state tells.
func (oracleEngine) afterFailedStatement(v txByVerbs, _ error, _ bool, _ func() bool) txByVerbs {
	return v
}

func (oracleEngine) autocommitMayBeOff() bool { return false }
func (oracleEngine) ExecutableComments() bool { return false }

func (oracleEngine) commitsImplicitlyWords(words []string, _ func() []string) bool {
	return sqltext.OracleCommitsImplicitly(words)
}

func (oracleEngine) setsAutocommitWords([]string, func() []string) (on, ok bool) { return false, false }

// beginIsCompound: BEGIN starts a PL/SQL block, never a transaction.
func (oracleEngine) beginIsCompound([]string) bool { return true }

// SavepointBeginsTransaction: a SAVEPOINT outside a transaction begins
// one (verified on 23ai).
func (oracleEngine) SavepointBeginsTransaction() bool { return true }

// OutsideTransaction reports a statement Oracle runs only as the first of
// a transaction, or outside one: SET TRANSACTION, ALTER SESSION SET
// ISOLATION_LEVEL, and ALTER SESSION ENABLE, DISABLE or FORCE PARALLEL DML
// (ORA-12841).
func (oracleEngine) OutsideTransaction(sql string) bool {
	words := nameWords(sql, sqltext.Oracle)
	switch {
	case wordAt(words, 0) == "SET" && wordAt(words, 1) == "TRANSACTION":
		return true
	case wordAt(words, 0) != "ALTER" || wordAt(words, 1) != "SESSION":
		return false
	case wordAt(words, 2) == "SET":
		return wordAt(words, 3) == "ISOLATION_LEVEL"
	}
	switch wordAt(words, 2) {
	case "ENABLE", "DISABLE", "FORCE":
		return wordAt(words, 3) == "PARALLEL" && wordAt(words, 4) == "DML"
	}
	return false
}

// TakesEffectAtOnce reports a statement calling a sequence's NEXTVAL,
// whose number a rollback does not give back. Quoted, only "NEXTVAL" is
// it: the server refuses "nextval".
func (oracleEngine) TakesEffectAtOnce(sql string) bool {
	toks := sqltext.Tokenize(sql, sqltext.Oracle)
	prev := ""
	for _, t := range toks {
		switch t.Kind {
		case sqltext.Whitespace, sqltext.Comment:
			continue
		case sqltext.Keyword, sqltext.Identifier:
			if prev == "." && strings.EqualFold(t.Text, "NEXTVAL") {
				return true
			}
		case sqltext.QuotedIdent:
			if prev == "." && sqltext.DecodeQuoted(t.Text, sqltext.Oracle) == "NEXTVAL" {
				return true
			}
		}
		prev = t.Text
	}
	return false
}

func (oracleEngine) RollbackMayKeepChanges() bool { return false }

// RowCountWord: an UPDATE counts the rows it matched, set to their own
// values too.
func (oracleEngine) RowCountWord() string { return "matched" }

// ownRowCountSQL is "": go-ora's count is the statement's own, not that
// of the rows its triggers changed (verified on 23ai).
func (oracleEngine) ownRowCountSQL() string          { return "" }
func (oracleEngine) implicitTransactionsSQL() string { return "" }

// transactionStateSQL reads the id of the session's transaction, NULL
// when none is open: the driver keeps none, and a failed DDL statement,
// a PL/SQL block or a SELECT … FOR UPDATE may have begun or ended one.
func (oracleEngine) transactionStateSQL() string {
	return "SELECT DBMS_TRANSACTION.LOCAL_TRANSACTION_ID FROM dual"
}

// driverCommitsStatements: go-ora commits each DML statement and PL/SQL
// block it runs unless a transaction of its own is open.
func (oracleEngine) driverCommitsStatements() bool { return true }

// UseSchemaSQL makes the session find unqualified names in a schema,
// which Oracle calls its current schema; the user stays who it is.
func (oracleEngine) UseSchemaSQL(schema string) string {
	if schema == "" {
		return "ALTER SESSION SET CURRENT_SCHEMA = "
	}
	return "ALTER SESSION SET CURRENT_SCHEMA = " + QuoteIfNeeded(oracleDialect{}, schema)
}

func (oracleEngine) EditorSchemaPreamble(schema string) string {
	return oracleEngine{}.UseSchemaSQL(schema) + ";\n\n"
}

func (oracleEngine) NameFolding() NameFolding { return NamesFoldToUpper }

func (oracleEngine) Search(ctx context.Context, d *DB, text string, definitions bool) ([]SearchHit, bool, error) {
	return oracleSearch(ctx, d, strings.ToLower(text), definitions)
}

func (oracleEngine) EnumValues(context.Context, *DB, string) ([]string, error) { return nil, nil }
func (oracleEngine) ParameterAsText() bool                                     { return true }

// TextLiteral is a '…' string: Oracle has no escapes but the doubled
// quote. An empty string is NULL.
func (oracleEngine) TextLiteral(s string) string { return standardLiteral(s) }

func (oracleEngine) BytesLiteral(b []byte) string {
	return "HEXTORAW('" + strings.ToUpper(hex.EncodeToString(b)) + "')"
}

// BooleanLiteral is 1 or 0, which a BOOLEAN column of 23ai takes too, and
// a NUMBER(1) of earlier versions.
func (oracleEngine) BooleanLiteral(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func (oracleEngine) BooleansAreNumbers() bool { return true }
func (oracleEngine) OffsetTimesAsUTC() bool   { return false }

// CastParameter converts typed text to a column's type: a LOB by its
// function, as CAST takes none, and bytes from their hex digits.
func (oracleEngine) CastParameter(placeholder, columnType string) string {
	switch typeName(columnType) {
	case "clob":
		return "TO_CLOB(" + placeholder + ")"
	case "nclob":
		return "TO_NCLOB(" + placeholder + ")"
	case "blob":
		return "TO_BLOB(HEXTORAW(" + placeholder + "))"
	case "xmltype":
		return "XMLTYPE(" + placeholder + ")"
	}
	return "CAST(" + placeholder + " AS " + columnType + ")"
}

// KeyParameter converts a DATE or TIMESTAMP key: go-ora sends a time as a
// timestamp of its own, which a DATE, or a TIMESTAMP of another
// precision, would be compared with in its type rather than theirs.
func (oracleEngine) KeyParameter(placeholder, columnType string) string {
	switch typeName(columnType) {
	case "date", "timestamp":
		return "CAST(" + placeholder + " AS " + columnType + ")"
	}
	return placeholder
}

// InsertDefaultRowSQL is "": Oracle has no DEFAULT VALUES, so the row is
// inserted with its first column's default (EditTarget.Statements).
func (oracleEngine) InsertDefaultRowSQL(string) string { return "" }

// MaxParameters: a statement takes 65535 binds; one more fails with
// ORA-03120 (verified on 23ai).
func (oracleEngine) MaxParameters() int       { return 65535 }
func (oracleEngine) ProfilesInOneQuery() bool { return false }

// CountSQL counts the values of a type Oracle cannot compare, as a LOB, a
// LONG or an XMLType, by whether each is NULL: count of one fails
// (ORA-22849, ORA-00997).
func (oracleEngine) CountSQL(column, columnType string) string {
	if !(oracleEngine{}).Comparable(columnType) {
		return "count(CASE WHEN " + column + " IS NOT NULL THEN 1 END)"
	}
	return "count(" + column + ")"
}

// NullSQL has a type, as go-ora reads no rows of a result with a NULL of
// none (oracleNoLengthError).
func (oracleEngine) NullSQL() string { return "CAST(NULL AS VARCHAR2(1))" }

func (oracleEngine) Comparable(columnType string) bool {
	switch typeName(columnType) {
	case "clob", "nclob", "blob", "bfile", "long", "long raw", "xmltype", "json", "vector":
		return false
	}
	return true
}

func (oracleEngine) StableOrderByKey() bool           { return true }
func (oracleEngine) UpdateSetsDefault() bool          { return true }
func (oracleEngine) InterruptAbortsTransaction() bool { return false }

// FileReadingFunctions are Oracle's that read the server's files or reach
// other servers.
func (oracleEngine) FileReadingFunctions() []string {
	return []string{"utl_file", "bfilename", "utl_http", "utl_tcp", "utl_smtp", "httpuritype", "dbms_cloud", "dbms_lob"}
}

func (oracleEngine) FileNamesInStrings() bool { return false }

// LikeFilterSQL compares the column's text without case, as Oracle's LIKE
// keeps it.
func (oracleEngine) LikeFilterSQL(column, pattern string, not bool) string {
	return "UPPER(TO_CHAR(" + column + "))" + likeOperator(false, not) + "UPPER(" + pattern + `) ESCAPE '\'`
}

// JSONPathSQL reads with JSON_QUERY, or JSON_VALUE for a scalar, its keys
// quoted in the path.
func (oracleEngine) JSONPathSQL(column string, path []any, leaf bool) string {
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

// UpsertSQL writes the row as a MERGE on its key, from a row of DUAL.
func (oracleEngine) UpsertSQL(u Upsert) string {
	values := make([]string, len(u.Columns))
	for i, c := range u.Columns {
		values[i] = u.Values[i] + " AS " + c
	}
	on := lists.Map(u.Keys, func(k string) string { return "target." + k + " = source." + k })
	merge := "MERGE INTO " + u.Table + " target\nUSING (SELECT " + strings.Join(values, ", ") + " FROM dual) source\nON (" +
		strings.Join(on, " AND ") + ")\n"
	if len(u.UpdatedColumns) > 0 {
		sets := lists.Map(u.UpdatedColumns, func(c string) string { return "target." + c + " = source." + c })
		merge += "WHEN MATCHED THEN UPDATE SET " + strings.Join(sets, ", ") + "\n"
	}
	sources := lists.Map(u.Columns, func(c string) string { return "source." + c })
	return merge + "WHEN NOT MATCHED THEN INSERT (" + strings.Join(u.Columns, ", ") + ") VALUES (" + strings.Join(sources, ", ") + ")"
}

// RowLimit: FETCH FIRST, from 12c.
func (oracleEngine) RowLimit(n int) (string, string) {
	return "", "FETCH FIRST " + strconv.Itoa(n) + " ROWS ONLY"
}

// ShownColumnSQL reads XMLType as its text, which the driver cannot read,
// and JSON as its text as written, the driver giving its keys sorted.
func (oracleEngine) ShownColumnSQL(column, columnType string) string {
	switch typeName(columnType) {
	case "xmltype":
		return "XMLSERIALIZE(CONTENT " + column + " AS CLOB)"
	case "json":
		return "JSON_SERIALIZE(" + column + " RETURNING CLOB)"
	}
	return ""
}
