package db

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/netproxy/proxytest"
	"dgopher/internal/sqltext"
	"dgopher/internal/sshtunnel"
	"dgopher/internal/sshtunnel/sshtest"

	"github.com/sijms/go-ora/v3/network"
	oratypes "github.com/sijms/go-ora/v3/types"
)

var oracleConfig = Config{Name: "oracle", Engine: Oracle, Host: "127.0.0.1", Port: 11521, User: "dgopher", Password: "dgopher", Database: "FREEPDB1"}

// oracleSystemConfig logs in as SYSTEM, the test server's administrator,
// for what needs a DBA's privileges.
var oracleSystemConfig = Config{Name: "oracle-system", Engine: Oracle, Host: "127.0.0.1", Port: 11521, User: "system", Password: "dgopher",
	Database: "FREEPDB1"}

func TestOracleURL(t *testing.T) {
	for _, c := range []struct {
		url  string
		want Config
		err  string
	}{
		{url: "oracle://scott:tiger@db.example.com:1522/ORCLPDB1",
			want: Config{Engine: Oracle, Host: "db.example.com", Port: 1522, User: "scott", Password: "tiger", Database: "ORCLPDB1"}},
		{url: "oracle://scott@db.example.com/?SID=ORCL",
			want: Config{Engine: Oracle, Host: "db.example.com", User: "scott", Database: "ORCL", Oracle: OracleConfig{SID: true}}},
		{url: "oracle://scott@adb.example.com:1522/x_high?SSL=true",
			want: Config{Engine: Oracle, Host: "adb.example.com", Port: 1522, User: "scott", Database: "x_high", TLS: TLSVerifyFull}},
		{url: "oracle://scott@adb.example.com:1522/x_high?SSL=enable&SSL%20VERIFY=false",
			want: Config{Engine: Oracle, Host: "adb.example.com", Port: 1522, User: "scott", Database: "x_high", TLS: TLSRequire}},
		{url: "oracle://scott@adb.example.com:1522/x_high?ssl=true&wallet=/home/me/wallet",
			want: Config{Engine: Oracle, Host: "adb.example.com", Port: 1522, User: "scott", Database: "x_high", TLS: TLSVerifyFull,
				Oracle: OracleConfig{Wallet: "/home/me/wallet"}}},
		{url: "oracle://scott@db.example.com/svc?DBA%20PRIVILEGE=SYSDBA", err: "DBA PRIVILEGE"},
		{url: "oracle://scott@db.example.com/svc?SSL%20VERIFY=false", err: "needs SSL"},
		{url: "oracle://scott@db.example.com/svc?SID=ORCL", err: "both a service"},
		{url: "(DESCRIPTION=(ADDRESS=(PROTOCOL=TCPS)(HOST=adb.example.com)(PORT=1522))(CONNECT_DATA=(SERVICE_NAME=x_high.adb.oraclecloud.com))(SECURITY=(SSL_SERVER_DN_MATCH=YES)))",
			want: Config{Engine: Oracle, Host: "adb.example.com", Port: 1522, Database: "x_high.adb.oraclecloud.com", TLS: TLSVerifyFull}},
		{url: " (description = (address = (protocol = tcp)(host = 10.0.0.5)(port = 1521)) (connect_data = (sid = ORCL)))",
			want: Config{Engine: Oracle, Host: "10.0.0.5", Port: 1521, Database: "ORCL", Oracle: OracleConfig{SID: true}}},
		{url: "(DESCRIPTION=(ADDRESS_LIST=(ADDRESS=(PROTOCOL=TCP)(HOST=a)(PORT=1521))(ADDRESS=(PROTOCOL=TCP)(HOST=b)(PORT=1521)))(CONNECT_DATA=(SERVICE_NAME=s)))",
			err: "2 addresses"},
	} {
		t.Run(c.url, func(t *testing.T) {
			got, err := ParseURL(c.url)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("error %v, want one saying %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got.Name = ""
			if got.Engine != c.want.Engine || got.Host != c.want.Host || got.Port != c.want.Port || got.User != c.want.User ||
				got.Password != c.want.Password || got.Database != c.want.Database || got.TLS != c.want.TLS || got.Oracle != c.want.Oracle {
				t.Fatalf("got %+v\nwant %+v", got, c.want)
			}
		})
	}
}

func TestOracleConfigValidate(t *testing.T) {
	ok := Config{Name: "o", Engine: Oracle, Host: "h", User: "u", Database: "FREEPDB1"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		change func(*Config)
		want   string
	}{
		{func(c *Config) { c.Database = "" }, "service name"},
		{func(c *Config) { c.Database, c.Oracle.SID = "", true }, "SID"},
		{func(c *Config) { c.Oracle.Wallet = "/w" }, "wallet needs TLS"},
		{func(c *Config) { c.Engine, c.Oracle.SID = Postgres, true }, "SID"},
		{func(c *Config) { c.Engine, c.TLS, c.Oracle.Wallet = Postgres, TLSVerifyFull, "/w" }, "wallet"},
	} {
		cfg := ok
		c.change(&cfg)
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want an error saying %q", cfg, err, c.want)
		}
	}
}

// The driver gets the connection's service or SID, its user and password
// in the connect string alone, with none of the settings it would read
// from the environment.
func TestOracleConnectString(t *testing.T) {
	cfg := Config{Engine: Oracle, Host: "db.example.com", Port: 1522, User: "sc@tt", Password: "p&ss:w/rd", Database: "ORCLPDB1"}
	dsn := oracleConnectString(cfg, endpoint{host: "127.0.0.1", port: 40000, serverName: "db.example.com"}, false)
	for _, want := range []string{"oracle://sc%40tt:p&ss%3Aw%2Frd@127.0.0.1:40000/ORCLPDB1", "PROGRAM=DGopher", "FAST+LOGIN=FALSE"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("%s lacks %s", dsn, want)
		}
	}
	cfg.Oracle.SID = true
	if dsn := oracleConnectString(cfg, endpoint{host: "h", port: 1521}, true); !strings.Contains(dsn, "@h:1521/?") || !strings.Contains(dsn, "SID=ORCLPDB1") || !strings.Contains(dsn, "SSL=TRUE") {
		t.Errorf("SID and TLS: %s", dsn)
	}
}

func TestOracleSQL(t *testing.T) {
	d := DialectOf(Oracle)
	r, _ := SQL(Oracle)
	for _, c := range []struct{ got, want string }{
		{d.Quote(`a"b`), `"a""b"`},
		{d.Placeholder(3), ":3"},
		{QuoteIfNeeded(d, "customers"), `"customers"`},
		{QuoteIfNeeded(d, "CUSTOMERS"), "CUSTOMERS"},
		{QuoteIfNeeded(d, "TABLE"), `"TABLE"`},
		{QuoteIfNeeded(DialectOf(Postgres), "customers"), "customers"},
		{r.TextLiteral("it's"), "'it''s'"},
		{r.BytesLiteral([]byte{0xde, 0xad}), "HEXTORAW('DEAD')"},
		{r.BooleanLiteral(true) + r.BooleanLiteral(false), "10"},
		{LimitSQL(Oracle, "SELECT DISTINCT a FROM t", " ", 5), "SELECT DISTINCT a FROM t FETCH FIRST 5 ROWS ONLY"},
		{r.KeyParameter(":1", "DATE"), "CAST(:1 AS DATE)"},
		{r.KeyParameter(":1", "TIMESTAMP(6)"), "CAST(:1 AS TIMESTAMP(6))"},
		{r.KeyParameter(":1", "NUMBER(10)"), ":1"},
		{r.CastParameter(":2", "NUMBER(10,2)"), "CAST(:2 AS NUMBER(10,2))"},
		{r.CastParameter(":2", "CLOB"), "TO_CLOB(:2)"},
		{r.CastParameter(":2", "BLOB"), "TO_BLOB(HEXTORAW(:2))"},
		{r.LikeFilterSQL(`"note"`, `'%a\_b%'`, false), `UPPER(TO_CHAR("note")) LIKE UPPER('%a\_b%') ESCAPE '\'`},
		{r.JSONPathSQL(`"doc"`, []any{"a b", 0}, true), `JSON_VALUE("doc", '$."a b"[0]')`},
		{r.UseSchemaSQL("HR"), "ALTER SESSION SET CURRENT_SCHEMA = HR"},
		{r.UseSchemaSQL("it"), `ALTER SESSION SET CURRENT_SCHEMA = "it"`},
		{r.UpsertSQL(Upsert{Table: `"t"`, Columns: []string{`"id"`, `"v"`}, Values: []string{"1", "'x'"}, Keys: []string{`"id"`}, UpdatedColumns: []string{`"v"`}}),
			`MERGE INTO "t" target
USING (SELECT 1 AS "id", 'x' AS "v" FROM dual) source
ON (target."id" = source."id")
WHEN MATCHED THEN UPDATE SET target."v" = source."v"
WHEN NOT MATCHED THEN INSERT ("id", "v") VALUES (source."id", source."v")`},
		{r.UpsertSQL(Upsert{Table: `"t"`, Columns: []string{`"id"`}, Values: []string{"1"}, Keys: []string{`"id"`}}),
			`MERGE INTO "t" target
USING (SELECT 1 AS "id" FROM dual) source
ON (target."id" = source."id")
WHEN NOT MATCHED THEN INSERT ("id") VALUES (source."id")`},
		{r.InsertDefaultRowSQL(`"t"`), ""},
	} {
		if c.got != c.want {
			t.Errorf("got %q\nwant %q", c.got, c.want)
		}
	}
	// A row of defaults names its first column, as Oracle has no DEFAULT
	// VALUES.
	target := &EditTarget{Dialect: d, Schema: "S", Table: "t", Columns: []Column{{Name: "id", Type: "NUMBER"}, {Name: "v", Type: "VARCHAR2(10)"}}, Key: []string{"id"}}
	stmts, err := target.Statements([]Change{{Kind: ChangeInsert, Values: map[string]any{}}})
	if err != nil || len(stmts) != 1 || stmts[0].SQL != `INSERT INTO "S"."t" ("id") VALUES (DEFAULT)` {
		t.Fatalf("insert of defaults %+v %v", stmts, err)
	}
}

// The statements Oracle refuses inside a transaction, the commits DDL
// makes, and how a session follows its transaction.
func TestOracleTransactionRules(t *testing.T) {
	r, _ := SQL(Oracle)
	for _, c := range []struct {
		sql                     string
		outside, commits, takes bool
	}{
		{sql: "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE", outside: true},
		{sql: "set transaction read only", outside: true},
		{sql: "ALTER SESSION SET ISOLATION_LEVEL = SERIALIZABLE", outside: true},
		{sql: "ALTER SESSION ENABLE PARALLEL DML", outside: true},
		{sql: "ALTER SESSION FORCE PARALLEL DML PARALLEL 4", outside: true},
		{sql: "ALTER SESSION DISABLE PARALLEL DML", outside: true},
		{sql: "ALTER SESSION SET CURRENT_SCHEMA = HR"},
		{sql: "CREATE TABLE t (a NUMBER)", commits: true},
		{sql: "-- why\nDROP TABLE t", commits: true},
		{sql: "TRUNCATE TABLE t", commits: true},
		{sql: "ALTER TABLE t ADD b NUMBER", commits: true},
		{sql: "COMMENT ON TABLE t IS 'x'", commits: true},
		{sql: "GRANT SELECT ON t TO u", commits: true},
		{sql: "UPDATE t SET a = 1"},
		{sql: "SELECT * FROM t FOR UPDATE"},
		{sql: "SELECT s.NEXTVAL FROM dual", takes: true},
		{sql: `INSERT INTO t VALUES ("S".nextval)`, takes: true},
		{sql: `SELECT "S"."NEXTVAL" FROM dual`, takes: true},
		{sql: `SELECT "S"."nextval" FROM dual`},
		{sql: "SELECT nextval FROM t"},
		{sql: "SELECT 'x.NEXTVAL' FROM dual"},
		{sql: "BEGIN NULL; END;"},
	} {
		if got := r.OutsideTransaction(c.sql); got != c.outside {
			t.Errorf("OutsideTransaction(%q) = %v", c.sql, got)
		}
		if got := CommitsImplicitly(Oracle, c.sql); got != c.commits {
			t.Errorf("CommitsImplicitly(%q) = %v", c.sql, got)
		}
		if got := r.TakesEffectAtOnce(c.sql); got != c.takes {
			t.Errorf("TakesEffectAtOnce(%q) = %v", c.sql, got)
		}
	}
	if !errors.Is(fmt.Errorf("x: %w", ErrTxMayBeCommitted), ErrTxLost) || errors.Is(ErrTxLost, ErrTxMayBeCommitted) {
		t.Error("a transaction that may have committed is lost to the session too")
	}
	if r.TransactionalDDL() || r.Savepoints() || !r.SavepointBeginsTransaction() || r.transactionStateSQL() == "" || !r.driverCommitsStatements() {
		t.Error("transaction facts")
	}
	if r.BeginStatement() != "SET TRANSACTION READ WRITE" || r.ReadOnlyBeginSQL() != "SET TRANSACTION READ ONLY" || beginStatements(Oracle, driver.TxOptions{}) != nil {
		t.Error("begin statements")
	}
	// BEGIN starts a PL/SQL block, never a transaction.
	if v := (txByVerbs{}).after(Oracle, "BEGIN NULL; END;", nil, nil); v.tx != TxNone {
		t.Errorf("a PL/SQL block followed as %v", v.tx)
	}
}

func TestOracleEngineFacts(t *testing.T) {
	e := Oracle
	if !slices.Contains(Engines(), e) || !e.IsSQL() || LexDialect(e) != sqltext.Oracle || !e.ServerEnforcesReadOnly() {
		t.Fatal("engine facts")
	}
	r, _ := SQL(e)
	if f := r.ConnectionFields(); !f.SID || !f.Wallet {
		t.Errorf("connection fields %+v", f)
	}
	if f, _ := SQL(Postgres); f.ConnectionFields() != (ConnectionFields{}) {
		t.Error("PostgreSQL asks for an Oracle field")
	}
	for _, analyze := range []bool{false, true} {
		if _, ok := Explain(e, "SELECT 1 FROM dual", analyze); !ok {
			t.Errorf("Oracle does not explain, analyze %v", analyze)
		}
	}
}

// intervalBytes writes an interval as Oracle sends it: its fields
// offset by 2^31 or 60.
func intervalBytes(days, hours, minutes, seconds int, nanos int32) []byte {
	b := make([]byte, 11)
	binary.BigEndian.PutUint32(b, uint32(int64(days)+0x80000000))
	b[4], b[5], b[6] = byte(hours+60), byte(minutes+60), byte(seconds+60)
	binary.BigEndian.PutUint32(b[7:], uint32(int64(nanos)+0x80000000))
	return b
}

func TestOracleIntervalText(t *testing.T) {
	ym := make([]byte, 5)
	binary.BigEndian.PutUint32(ym, uint32(1+0x80000000))
	ym[4] = 2 + 60
	negYM := make([]byte, 5)
	binary.BigEndian.PutUint32(negYM, uint32(int64(-1)+0x80000000))
	negYM[4] = byte(-2 + 60)
	for _, c := range []struct {
		typ  uint16
		b    []byte
		want string
	}{
		{oratypes.INTERVALYM_DTY, ym, "+01-02"},
		{oratypes.INTERVALYM_DTY, negYM, "-01-02"},
		{oratypes.INTERVALDS_DTY, intervalBytes(3, 4, 5, 6, 789000000), "+03 04:05:06.789000000"},
		{oratypes.INTERVALDS_DTY, intervalBytes(-3, -4, -5, -6, 0), "-03 04:05:06.000000000"},
		{oratypes.INTERVALDS_DTY, intervalBytes(0, 0, 0, 0, -500000000), "-00 00:00:00.500000000"},
		{oratypes.INTERVALDS_DTY, intervalBytes(123, 0, 0, 1, 0), "+123 00:00:01.000000000"},
	} {
		got, err := oracleIntervalText(c.typ, c.b)
		if err != nil || got != c.want {
			t.Errorf("%v: %q %v, want %q", c.b, got, err, c.want)
		}
	}
	if _, err := oracleIntervalText(oratypes.INTERVALDS_DTY, []byte{1, 2}); err == nil {
		t.Error("a short interval read")
	}
}

func TestOracleColumnTypeName(t *testing.T) {
	for _, c := range []struct {
		col  oracleColumn
		want string
	}{
		{oracleColumn{dataType: oratypes.NCHAR, form: 1}, "VARCHAR2"},
		{oracleColumn{dataType: oratypes.NCHAR, form: 2}, "NVARCHAR2"},
		{oracleColumn{dataType: oratypes.CHAR, form: 2}, "NCHAR"},
		{oracleColumn{dataType: oratypes.NUMBER}, "NUMBER"},
		{oracleColumn{dataType: oratypes.DATE}, "DATE"},
		{oracleColumn{dataType: oratypes.TimeStampDTY}, "TIMESTAMP"},
		{oracleColumn{dataType: oratypes.TimeStampTZ_DTY}, "TIMESTAMP WITH TIME ZONE"},
		{oracleColumn{dataType: oratypes.TimeStampLTZ_DTY}, "TIMESTAMP WITH LOCAL TIME ZONE"},
		{oracleColumn{dataType: oratypes.INTERVALYM_DTY}, "INTERVAL YEAR TO MONTH"},
		{oracleColumn{dataType: oratypes.RAW}, "RAW"},
		{oracleColumn{dataType: oratypes.LongRaw}, "BLOB"},
		{oracleColumn{dataType: oratypes.LongVarChar, form: 1}, "CLOB"},
		{oracleColumn{dataType: oratypes.VECTOR, json: true}, "VECTOR"},
		{oracleColumn{dataType: oratypes.OCIClobLocator, form: 2}, "NCLOB"},
		{oracleColumn{dataType: oratypes.OCIBlobLocator}, "BLOB"},
		{oracleColumn{dataType: oratypes.IBDOUBLE}, "BINARY_DOUBLE"},
		{oracleColumn{dataType: oratypes.BOOLEAN}, "BOOLEAN"},
		{oracleColumn{dataType: oratypes.OCIBlobLocator, json: true}, "JSON"},
		{oracleColumn{dataType: oratypes.ROWID}, "ROWID"},
		{oracleColumn{dataType: 999}, ""},
	} {
		if got := c.col.typeName(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.col, got, c.want)
		}
	}
	// Raw bytes stay bytes, valid text or not.
	if !isBinaryType("RAW") || isBinaryType("VARCHAR2") {
		t.Error("RAW read as text")
	}
}

func TestOracleValue(t *testing.T) {
	for _, c := range []struct {
		typ  string
		in   any
		want any
	}{
		{"VECTOR", []float32{1, 2.5}, "[1,2.5]"},
		{"VECTOR", []uint8{1, 2}, "[1,2]"},
		{"VECTOR", []float64{-1}, "[-1]"},
		{"NUMBER", "12345678901234567890.123456789", "12345678901234567890.123456789"},
		{"RAW", []byte{1}, []byte{1}},
		{"", []uint8{1, 2}, []uint8{1, 2}},
	} {
		got := oracleValue(c.typ, c.in)
		if s, ok := c.want.(string); ok && got != s {
			t.Errorf("%s %v: %#v, want %q", c.typ, c.in, got, s)
		}
		if b, ok := c.want.([]byte); ok && !slices.Equal(got.([]byte), b) {
			t.Errorf("%s %v: %#v", c.typ, c.in, got)
		}
	}
}

func TestOracleDescribeError(t *testing.T) {
	err := error(&network.OracleError{ErrCode: 942, ErrMsg: "ORA-00942: table or view \"X\".\"Y\" does not exist"})
	info := DescribeError(err)
	if info.Code != "ORA-00942" || info.Message != `table or view "X"."Y" does not exist` {
		t.Errorf("%+v", info)
	}
	for _, code := range []int{60, 8177} {
		if hint := DescribeError(&network.OracleError{ErrCode: code, ErrMsg: "x"}).Hint; !strings.Contains(hint, "never runs it again") {
			t.Errorf("ORA-%05d hint %q", code, hint)
		}
	}
	if !endsConnection(&network.OracleError{ErrCode: 3113}) || endsConnection(&network.OracleError{ErrCode: 942}) {
		t.Error("lost connection errors")
	}
	if !oracleErrorIs(fmt.Errorf("x: %w", &network.OracleError{ErrCode: 2248}), 2248) || oracleErrorIs(&network.OracleError{ErrCode: 1031}, 2248) ||
		oracleErrorIs(errors.New("ORA-02248"), 2248) {
		t.Error("ORA codes")
	}
}

// Through a tunnel go-ora names the local address as the server: a
// verified connection still checks the certificate against the host the
// connection names.
func TestOracleTLSVerifiesTheHost(t *testing.T) {
	ca, caKey, _, _ := certificate(t, "ca", nil, nil)
	server, serverKey, _, _ := certificate(t, "db.example.com", ca, caKey)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{server.Raw}, PrivateKey: serverKey}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { c.(*tls.Conn).Handshake(); c.Close() }()
		}
	}()
	handshake := func(tc *tls.Config) error {
		tc = tc.Clone()
		tc.ServerName = "127.0.0.1" // as go-ora sets it
		c, err := tls.Dial("tcp", ln.Addr().String(), tc)
		if err == nil {
			c.Close()
		}
		return err
	}
	if err := handshake(oracleVerifiedAs(&tls.Config{RootCAs: pool}, "db.example.com")); err != nil {
		t.Fatalf("the right host: %v", err)
	}
	if err := handshake(oracleVerifiedAs(&tls.Config{RootCAs: pool}, "other.example.com")); err == nil {
		t.Fatal("a certificate of another host passed")
	}
	if err := handshake(oracleVerifiedAs(&tls.Config{}, "db.example.com")); err == nil {
		t.Fatal("a certificate of an unknown CA passed")
	}
	if err := handshake(oracleVerifiedAs(&tls.Config{InsecureSkipVerify: true}, "other.example.com")); err != nil {
		t.Fatalf("require checks nothing: %v", err)
	}
}

// The integration tests below run against the compose service oracle.

func openOracle(t *testing.T, cfg Config) *DB {
	t.Helper()
	integration(t)
	skipUnlessRunning(t, cfg)
	return openOracleAgain(t, cfg)
}

// openOracleAgain opens another pool in a test that holds the servers'
// lock already, which a second integration(t) would wait for forever.
func openOracleAgain(t *testing.T, cfg Config) *DB {
	t.Helper()
	d, err := Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// queryOne runs a query on a session and returns its first row.
func queryOne(t *testing.T, s *Session, q string) ([]any, []ColumnInfo) {
	t.Helper()
	c, err := s.Query(context.Background(), q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer c.Close()
	rows, err := c.Fetch(1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%s: %v %v", q, rows, err)
	}
	return rows[0], c.Columns
}

// NUMBER keeps every digit: no float conversion.
func TestOracleNumberPrecision(t *testing.T) {
	d := openOracle(t, oracleConfig)
	s, err := d.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	row, cols := queryOne(t, s, "SELECT 12345678901234567890.123456789 a, -0.000000000000000000000000000001 b, 1e125 c, CAST(7 AS NUMBER(10)) d FROM dual")
	for i, want := range []string{"12345678901234567890.123456789", "-0.000000000000000000000000000001", "1" + strings.Repeat("0", 125), "7"} {
		if Display(row[i]) != want || cols[i].Type != "NUMBER" {
			t.Errorf("%s: %#v (%s), want %s", cols[i].Name, row[i], cols[i].Type, want)
		}
	}
}

// Each of Oracle's types reads as the app shows it.
func TestOracleValues(t *testing.T) {
	d := openOracle(t, oracleConfig)
	ctx := context.Background()
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, c := range []struct {
		query, typ string
		want       any
	}{
		{"SELECT DATE '2024-02-03' FROM dual", "DATE", "2024-02-03"},
		{"SELECT TO_DATE('2024-02-03 04:05:06', 'YYYY-MM-DD HH24:MI:SS') FROM dual", "DATE", "2024-02-03 04:05:06"},
		{"SELECT TIMESTAMP '2024-02-03 04:05:06.123456' FROM dual", "TIMESTAMP", "2024-02-03 04:05:06.123456"},
		{"SELECT TIMESTAMP '2024-02-03 04:05:06.5 +05:30' FROM dual", "TIMESTAMP WITH TIME ZONE", "2024-02-03 04:05:06.5 +05:30"},
		{"SELECT INTERVAL '1-2' YEAR TO MONTH FROM dual", "INTERVAL YEAR TO MONTH", "+01-02"},
		{"SELECT INTERVAL '3 04:05:06.789' DAY TO SECOND FROM dual", "INTERVAL DAY TO SECOND", "+03 04:05:06.789000000"},
		{"SELECT HEXTORAW('41') FROM dual", "RAW", []byte("A")},
		{"SELECT TO_CLOB('clob text') FROM dual", "CLOB", "clob text"},
		{"SELECT TO_NCLOB('nclob') FROM dual", "NCLOB", "nclob"},
		{"SELECT TO_BLOB(HEXTORAW('0102')) FROM dual", "BLOB", []byte{1, 2}},
		{"SELECT CAST(1.5 AS BINARY_FLOAT) FROM dual", "BINARY_FLOAT", "1.5"},
		{"SELECT CAST(2.5 AS BINARY_DOUBLE) FROM dual", "BINARY_DOUBLE", "2.5"},
		{"SELECT TRUE FROM dual", "BOOLEAN", "true"},
		{"SELECT JSON('{\"a\":1}') FROM dual", "JSON", `{"a":1}`},
		{"SELECT VECTOR('[1,2,3]', 3, FLOAT32) FROM dual", "VECTOR", "[1,2,3]"},
		{"SELECT CAST(N'ünï' AS NVARCHAR2(5)) FROM dual", "NVARCHAR2", "ünï"},
		{"SELECT N'ünï' FROM dual", "NCHAR", "ünï"},
		{"SELECT CAST('x' AS CHAR(3)) FROM dual", "CHAR", "x  "},
		// '' is NULL in Oracle: shown as it is, not altered.
		{"SELECT CAST('' AS VARCHAR2(5)) FROM dual", "VARCHAR2", nil},
	} {
		row, cols := queryOne(t, s, c.query)
		if cols[0].Type != c.typ {
			t.Errorf("%s: type %q, want %q", c.query, cols[0].Type, c.typ)
		}
		switch w := c.want.(type) {
		case nil:
			if row[0] != nil {
				t.Errorf("%s: %#v, want NULL", c.query, row[0])
			}
		case []byte:
			if b, ok := row[0].([]byte); !ok || !slices.Equal(b, w) {
				t.Errorf("%s: %#v, want bytes %v", c.query, row[0], w)
			}
		case string:
			if Display(row[0]) != w {
				t.Errorf("%s: %q (%#v), want %q", c.query, Display(row[0]), row[0], w)
			}
		}
	}
	// ROWID reads as its text; XMLType, which the driver cannot read, fails
	// with a hint to serialize it.
	row, cols := queryOne(t, s, "SELECT ROWID FROM dual")
	if cols[0].Type != "ROWID" || len(Display(row[0])) != 18 {
		t.Errorf("rowid %q %#v", cols[0].Type, row[0])
	}
	if _, err := s.Query(ctx, "SELECT XMLTYPE('<a>1</a>') FROM dual"); err == nil || !strings.Contains(DescribeError(err).Hint, "XMLSERIALIZE") {
		t.Errorf("XMLType: %v %+v", err, DescribeError(err))
	}
}

// go-ora commits each DML statement and PL/SQL block it runs while no
// transaction is open, and none while one is; the session reads the
// server's transaction after every statement, failed ones too.
func TestOracleTransactions(t *testing.T) {
	d := openOracle(t, oracleConfig)
	ctx := context.Background()
	mustExec(t, d, `DROP TABLE IF EXISTS "tx_t"`, `CREATE TABLE "tx_t" ("id" NUMBER PRIMARY KEY, "v" VARCHAR2(20))`, `INSERT INTO "tx_t" VALUES (1, 'a')`,
		`CREATE OR REPLACE PROCEDURE tx_touch AS BEGIN UPDATE "tx_t" SET "v" = 'touched' WHERE "id" = 1; END;`)
	t.Cleanup(func() { mustExec(t, d, `DROP TABLE IF EXISTS "tx_t"`, `DROP PROCEDURE IF EXISTS tx_touch`) })
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	count := func(where string) int {
		var n int
		if err := d.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM "tx_t" WHERE `+where).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, st := range []struct {
		sql   string
		want  TxState
		fails bool
	}{
		{sql: `INSERT INTO "tx_t" VALUES (2, 'auto')`, want: TxNone},
		{sql: `BEGIN UPDATE "tx_t" SET "v" = 'block' WHERE "id" = 2; END;`, want: TxNone},
		{sql: `SELECT * FROM "tx_t" FOR UPDATE`, want: TxOpen},
		{sql: `UPDATE "tx_t" SET "v" = 'held' WHERE "id" = 2`, want: TxOpen},
		{sql: `BEGIN UPDATE "tx_t" SET "v" = 'held too' WHERE "id" = 2; END;`, want: TxOpen},
		{sql: "ROLLBACK", want: TxNone},
		{sql: "CALL tx_touch()", want: TxOpen},
		{sql: "ROLLBACK", want: TxNone},
		{sql: `<<l>> BEGIN UPDATE "tx_t" SET "v" = 'label' WHERE "id" = 1; END;`, want: TxOpen},
		{sql: "ROLLBACK", want: TxNone},
		{sql: `LOCK TABLE "tx_t" IN EXCLUSIVE MODE`, want: TxOpen},
		{sql: "COMMIT", want: TxNone},
		{sql: "/* why */ SET TRANSACTION READ WRITE", want: TxOpen},
		{sql: "SAVEPOINT s", want: TxOpen},
		{sql: `DELETE FROM "tx_t" WHERE "id" = 2`, want: TxOpen},
		{sql: "ROLLBACK TO SAVEPOINT s", want: TxOpen},
		{sql: "SELEC 1", want: TxOpen, fails: true},
		{sql: `INSERT INTO "tx_t" VALUES (3, 'committed')`, want: TxOpen},
		// A DDL statement commits, even one that fails as a CREATE of a
		// name in use does.
		{sql: `CREATE TABLE "tx_t" (a NUMBER)`, want: TxNone, fails: true},
		{sql: `BEGIN UPDATE "tx_t" SET "v" = 'x' WHERE "id" = 3; COMMIT; END;`, want: TxNone},
	} {
		if _, err := s.Exec(ctx, st.sql); (err != nil) != st.fails {
			t.Fatalf("%s: %v", st.sql, err)
		}
		if got := s.Tx(); got != st.want {
			t.Fatalf("after %q: %v, want %v", st.sql, got, st.want)
		}
	}
	if count(`"id" = 2 AND "v" = 'block'`) != 1 || count(`"id" = 3`) != 1 || count(`"v" = 'touched' OR "v" = 'label'`) != 0 {
		t.Fatal("what committed and rolled back differs from the transactions shown")
	}
	// Begin opens a transaction the server holds, Rollback ends it through
	// the driver; and Commit commits.
	if err := s.Begin(ctx); err != nil || s.Tx() != TxOpen {
		t.Fatalf("begin: %v %v", err, s.Tx())
	}
	if _, err := s.Exec(ctx, `DELETE FROM "tx_t"`); err != nil {
		t.Fatal(err)
	}
	if err := s.Rollback(ctx); err != nil || s.Tx() != TxNone || count("1 = 1") != 3 {
		t.Fatalf("rollback: %v %v %d", err, s.Tx(), count("1 = 1"))
	}
	if err := s.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(ctx, `DELETE FROM "tx_t" WHERE "id" = 3`); err != nil {
		t.Fatal(err)
	}
	if count(`"id" = 3`) != 1 {
		t.Fatal("the delete committed before Commit")
	}
	if err := s.Commit(ctx); err != nil || s.Tx() != TxNone || count(`"id" = 3`) != 0 {
		t.Fatalf("commit: %v %v", err, s.Tx())
	}
	// After a transaction ends, statements commit on their own again.
	if _, err := s.Exec(ctx, `UPDATE "tx_t" SET "v" = 'after' WHERE "id" = 1`); err != nil || s.Tx() != TxNone || count(`"v" = 'after'`) != 1 {
		t.Fatalf("after the transaction: %v %v", err, s.Tx())
	}
}

// A read-only connection to Oracle 23ai refuses writes on the server; the
// app's Rollback still ends a read-only transaction, which ROLLBACK
// cannot under READ_ONLY.
func TestOracleReadOnlySession(t *testing.T) {
	writable := openOracle(t, oracleConfig)
	mustExec(t, writable, `DROP TABLE IF EXISTS "ro_t"`, `CREATE TABLE "ro_t" ("id" NUMBER PRIMARY KEY)`, `INSERT INTO "ro_t" VALUES (1)`)
	t.Cleanup(func() { mustExec(t, writable, `DROP TABLE IF EXISTS "ro_t"`) })
	cfg := oracleConfig
	cfg.ReadOnly = true
	d := openOracleAgain(t, cfg)
	ctx := context.Background()
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, q := range []string{`DELETE FROM "ro_t"`, "CREATE TABLE ro_other (a NUMBER)", `LOCK TABLE "ro_t" IN EXCLUSIVE MODE`, `SELECT * FROM "ro_t" FOR UPDATE`} {
		if _, err := s.Exec(ctx, q); err == nil || !strings.Contains(err.Error(), "ORA-28193") {
			t.Errorf("%s on a read-only connection: %v", q, err)
		}
	}
	r, _ := SQL(Oracle)
	if _, err := s.Exec(ctx, r.ReadOnlyBeginSQL()); err != nil || s.Tx() != TxOpen {
		t.Fatalf("read-only transaction: %v %v", err, s.Tx())
	}
	if err := s.Rollback(ctx); err != nil || s.Tx() != TxNone {
		t.Fatalf("rollback of a read-only transaction: %v %v", err, s.Tx())
	}
}

// What DBMS_OUTPUT prints comes back as the session's messages.
func TestOracleMessages(t *testing.T) {
	d := openOracle(t, oracleConfig)
	ctx := context.Background()
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Exec(ctx, "BEGIN DBMS_OUTPUT.PUT_LINE('hello'); DBMS_OUTPUT.PUT_LINE(NULL); DBMS_OUTPUT.PUT_LINE('it''s'); END;"); err != nil {
		t.Fatal(err)
	}
	if got := s.TakeMessages(); !slices.Equal(got, []string{"hello", "", "it's"}) {
		t.Fatalf("messages %q", got)
	}
	if _, err := s.Exec(ctx, "BEGIN FOR i IN 1..1005 LOOP DBMS_OUTPUT.PUT_LINE(i); END LOOP; END;"); err != nil {
		t.Fatal(err)
	}
	if got := s.TakeMessages(); len(got) != maxMessages+1 || got[maxMessages] != "5 more messages not shown." {
		t.Fatalf("%d messages, last %q", len(got), got[len(got)-1])
	}
	if s.Tx() != TxNone {
		t.Fatalf("reading the messages left %v", s.Tx())
	}
}

// After a statement the user ran, one query reads both the transaction and
// what DBMS_OUTPUT printed, failed statements too; after the app's own, the
// transaction alone.
func TestOracleOneReadAfterStatement(t *testing.T) {
	d := openOracle(t, oracleConfig)
	ctx := context.Background()
	mustExec(t, d, `DROP TABLE IF EXISTS "rt_t"`, `CREATE TABLE "rt_t" ("id" NUMBER PRIMARY KEY, "v" VARCHAR2(10))`, `INSERT INTO "rt_t" VALUES (1, 'a')`)
	t.Cleanup(func() { mustExec(t, d, `DROP TABLE IF EXISTS "rt_t"`) })
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, _ := SQL(Oracle)
	sent := captureSends(t, oracleConfig.Name)
	for _, c := range []struct {
		name     string
		run      func() error
		fails    bool
		userRan  bool
		want     TxState
		messages []string
	}{
		{name: "exec", run: func() error { _, err := s.Exec(ctx, `UPDATE "rt_t" SET "v" = 'b' WHERE "id" = 1`); return err }, userRan: true, want: TxNone},
		{name: "select for update", run: func() error { _, err := s.Exec(ctx, `SELECT * FROM "rt_t" FOR UPDATE`); return err }, userRan: true, want: TxOpen},
		{name: "printing in a transaction", run: func() error {
			_, err := s.Exec(ctx, `BEGIN UPDATE "rt_t" SET "v" = 'c' WHERE "id" = 1; DBMS_OUTPUT.PUT_LINE('done'); END;`)
			return err
		}, userRan: true, want: TxOpen, messages: []string{"done"}},
		{name: "failed", run: func() error { _, err := s.Exec(ctx, "SELEC 1"); return err }, fails: true, userRan: true, want: TxOpen},
		{name: "failed DDL commits", run: func() error { _, err := s.Exec(ctx, `CREATE TABLE "rt_t" (a NUMBER)`); return err }, fails: true, userRan: true, want: TxNone},
		{name: "cursor", run: func() error {
			c, err := s.Query(ctx, `SELECT * FROM "rt_t"`)
			if err == nil {
				c.Close()
			}
			return err
		}, userRan: true, want: TxNone},
		{name: "app's change", run: func() error { _, err := s.ExecChange(ctx, `UPDATE "rt_t" SET "v" = 'd' WHERE "id" = 1`); return err }, want: TxNone},
	} {
		// Run a plain SELECT first, so that the driver's own bookkeeping
		// after the previous case is not counted in this one.
		if _, err := s.Exec(ctx, "SELECT 1 FROM dual"); err != nil {
			t.Fatal(err)
		}
		sent.take()
		err := c.run()
		if (err != nil) != c.fails {
			t.Fatalf("%s: %v", c.name, err)
		}
		list := sent.take()
		if len(list) != 2 {
			t.Fatalf("%s: sent %d statements, want the statement and one read: %v", c.name, len(list), list)
		}
		read := list[1].Text
		if c.userRan && (!strings.Contains(read, "DBMS_OUTPUT.GET_LINE") || !strings.Contains(read, "LOCAL_TRANSACTION_ID")) {
			t.Errorf("%s: read %q, want the transaction and the messages", c.name, read)
		}
		if !c.userRan && read != r.transactionStateSQL() {
			t.Errorf("%s: read %q, want the transaction alone", c.name, read)
		}
		if s.Tx() != c.want {
			t.Errorf("%s: %v, want %v", c.name, s.Tx(), c.want)
		}
		if got := s.TakeMessages(); !slices.Equal(got, c.messages) {
			t.Errorf("%s: messages %q, want %q", c.name, got, c.messages)
		}
	}
}

// A cancelled statement keeps the session and its transaction.
func TestOracleCancelKeepsTransaction(t *testing.T) {
	d := openOracle(t, oracleConfig)
	bg := context.Background()
	s, err := d.Session(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Begin(bg); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(bg, 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	c, err := s.Query(ctx, "SELECT COUNT(*) FROM all_objects a, all_objects b, all_objects c")
	if err == nil {
		_, err = c.Fetch(1)
	}
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("cancel: %v after %v", err, time.Since(start))
	}
	if s.Tx() != TxOpen {
		t.Fatalf("after the cancel: %v, want the transaction open", s.Tx())
	}
	if row, _ := queryOne(t, s, "SELECT 1 FROM dual"); Display(row[0]) != "1" {
		t.Fatal(row)
	}
	if err := s.Rollback(bg); err != nil {
		t.Fatal(err)
	}
}

// A connection the session drops with a transaction open, as after an
// explain's After step failed, loses the transaction's changes: go-ora's
// close logs off, which would commit them, so they are rolled back first.
func TestOracleDropRollsBack(t *testing.T) {
	d := openOracle(t, oracleConfig)
	ctx := context.Background()
	mustExec(t, d, `DROP TABLE IF EXISTS "drop_t"`, `CREATE TABLE "drop_t" ("id" NUMBER PRIMARY KEY)`)
	t.Cleanup(func() { mustExec(t, d, `DROP TABLE IF EXISTS "drop_t"`) })
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(ctx, `INSERT INTO "drop_t" VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	err = s.RunExplain(ctx, ExplainRun{After: []string{"SELEC 1"}}, func(string, time.Duration, error) {}, func(context.Context) error { return nil })
	if err == nil || errors.Is(err, ErrTxMayBeCommitted) {
		t.Fatalf("the failed After step: %v", err)
	}
	var n int
	if err := d.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM "drop_t"`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("after the drop: %d rows, %v", n, err)
	}
	if _, err := s.Exec(ctx, "SELECT 1 FROM dual"); !errors.Is(err, ErrSessionReset) {
		t.Fatalf("the next statement: %v", err)
	}
}

// A key of a DATE and a TIMESTAMP column finds its row: the driver sends
// a time as a timestamp of its own type.
func TestOracleEditsByTimeKey(t *testing.T) {
	d := openOracle(t, oracleConfig)
	ctx := context.Background()
	mustExec(t, d, `DROP TABLE IF EXISTS "tk"`,
		`CREATE TABLE "tk" ("d" DATE, "ts" TIMESTAMP(9), "v" VARCHAR2(10), PRIMARY KEY ("d", "ts"))`,
		`INSERT INTO "tk" VALUES (TO_DATE('2024-02-03 04:05:06', 'YYYY-MM-DD HH24:MI:SS'), TIMESTAMP '2024-02-03 04:05:06.123456789', 'a')`)
	t.Cleanup(func() { mustExec(t, d, `DROP TABLE IF EXISTS "tk"`) })
	cols, err := d.Dialect.Columns(ctx, d.SQL, "DGOPHER", "tk")
	if err != nil {
		t.Fatal(err)
	}
	key, _ := KeyColumns(cols)
	s, _ := d.Session(ctx)
	defer s.Close()
	row, _ := queryOne(t, s, `SELECT "d", "ts" FROM "tk"`)
	target := &EditTarget{Dialect: d.Dialect, Schema: "DGOPHER", Table: "tk", Columns: cols, Key: key}
	stmts, err := target.Statements([]Change{{Kind: ChangeUpdate, Key: row, Values: map[string]any{"v": Typed("b")}}})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.ExecChange(ctx, stmts[0].SQL, stmts[0].Args...); err != nil || n != 1 {
		t.Fatalf("%s: %d %v", stmts[0].SQL, n, err)
	}
}

// A connection goes through a SOCKS5 or an HTTP proxy, or an SSH tunnel,
// to Oracle's listener, which sends it on to no other address.
func TestOracleThroughRoutes(t *testing.T) {
	integration(t)
	skipUnlessRunning(t, oracleConfig)
	ctx := context.Background()
	socks := proxytest.SOCKS5(t, "proxy-user", "proxy-pass")
	web := proxytest.HTTP(t, "web-user", "web-pass")
	s := sshtest.Start(t, "secret", nil)
	known := filepath.Join(t.TempDir(), "known_hosts")
	if err := sshtunnel.Trust(known, s.Addr, s.HostKey.PublicKey()); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Config){
		"socks5": func(c *Config) {
			c.Proxy = ProxyConfig{Kind: socks.Kind, Host: socks.Host, Port: socks.Port, User: socks.User, Password: socks.Password}
		},
		"http": func(c *Config) {
			c.Proxy = ProxyConfig{Kind: web.Kind, Host: web.Host, Port: web.Port, User: web.User, Password: web.Password}
		},
		"ssh": func(c *Config) {
			c.SSH = SSHConfig{Enabled: true, Host: s.Host, Port: s.Port, User: "tester", Password: "secret"}
		},
	} {
		cfg := oracleConfig
		change(&cfg)
		d, err := Open(ctx, cfg, []string{known})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var user string
		err = d.SQL.QueryRowContext(ctx, "SELECT USER FROM dual").Scan(&user)
		d.Close()
		if err != nil || d.route == nil || user != "DGOPHER" {
			t.Fatalf("%s: %q %v, route %v", name, user, err, d.route)
		}
	}
}

// The server's identity is Oracle, its version, and its TLS none.
func TestOracleIdentify(t *testing.T) {
	d := openOracle(t, oracleConfig)
	e, version, err := d.Identify(context.Background())
	if err != nil || e != Oracle || !strings.HasPrefix(version, "23.") {
		t.Fatalf("identified %q %q %v", e, version, err)
	}
	if d.TLSState() != "no TLS" {
		t.Errorf("TLS state %q", d.TLSState())
	}
}

// A MERGE upsert inserts a row, then updates it by its key.
func TestOracleUpsert(t *testing.T) {
	d := openOracle(t, oracleConfig)
	mustExec(t, d, `DROP TABLE IF EXISTS "up_t"`, `CREATE TABLE "up_t" ("id" NUMBER PRIMARY KEY, "v" VARCHAR2(10))`)
	t.Cleanup(func() { mustExec(t, d, `DROP TABLE IF EXISTS "up_t"`) })
	r, _ := SQL(Oracle)
	for _, v := range []string{"'a'", "'b'"} {
		mustExec(t, d, r.UpsertSQL(Upsert{Table: `"up_t"`, Columns: []string{`"id"`, `"v"`}, Values: []string{"1", v}, Keys: []string{`"id"`}, UpdatedColumns: []string{`"v"`}}))
	}
	var v string
	if err := d.SQL.QueryRow(`SELECT "v" FROM "up_t" WHERE "id" = 1`).Scan(&v); err != nil || v != "b" {
		t.Fatalf("after two upserts %q %v", v, err)
	}
}

// A deadlock rolls back Oracle's statement alone, and a serializable
// transaction's clash its statement too: each says it may run again, and
// each transaction stays open for the user to end.
func TestOracleConflicts(t *testing.T) {
	d := openOracle(t, oracleConfig)
	ctx := context.Background()
	mustExec(t, d, `DROP TABLE IF EXISTS "cf_t"`, `CREATE TABLE "cf_t" ("id" NUMBER PRIMARY KEY, "v" NUMBER)`,
		`INSERT INTO "cf_t" VALUES (1, 0)`, `INSERT INTO "cf_t" VALUES (2, 0)`)
	t.Cleanup(func() { mustExec(t, d, `DROP TABLE IF EXISTS "cf_t"`) })
	a, _ := d.Session(ctx)
	defer a.Close()
	b, _ := d.Session(ctx)
	defer b.Close()
	for _, st := range []struct {
		s   *Session
		sql string
	}{{a, `UPDATE "cf_t" SET "v" = 1 WHERE "id" = 1`}, {b, "SET TRANSACTION READ WRITE"}, {b, `UPDATE "cf_t" SET "v" = 2 WHERE "id" = 2`}} {
		if _, err := st.s.Exec(ctx, st.sql); err != nil {
			t.Fatal(st.sql, err)
		}
	}
	if err := a.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	a.Exec(ctx, `UPDATE "cf_t" SET "v" = 1 WHERE "id" = 1`)
	errs := make(chan error, 2)
	go func() { _, err := a.Exec(ctx, `UPDATE "cf_t" SET "v" = 1 WHERE "id" = 2`); errs <- err }()
	time.Sleep(300 * time.Millisecond)
	go func() { _, err := b.Exec(ctx, `UPDATE "cf_t" SET "v" = 2 WHERE "id" = 1`); errs <- err }()
	var deadlock error
	for range 2 {
		if err := <-errs; err != nil {
			deadlock = err
			// The victim's statement alone rolled back: the other waits
			// on its locks until it ends.
			if a.Tx() == TxOpen {
				a.Rollback(ctx)
			}
			if b.Tx() == TxOpen {
				b.Rollback(ctx)
			}
		}
	}
	if info := DescribeError(deadlock); info.Code != "ORA-00060" || !strings.Contains(info.Hint, "never runs it again") {
		t.Fatalf("deadlock described as %+v", info)
	}

	for _, st := range []struct {
		s   *Session
		sql string
	}{{a, "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE"}, {a, `SELECT * FROM "cf_t"`}, {b, `UPDATE "cf_t" SET "v" = 3 WHERE "id" = 1`}} {
		if _, err := st.s.Exec(ctx, st.sql); err != nil {
			t.Fatal(st.sql, err)
		}
	}
	_, err := a.Exec(ctx, `UPDATE "cf_t" SET "v" = 4 WHERE "id" = 1`)
	if info := DescribeError(err); info.Code != "ORA-08177" || !strings.Contains(info.Hint, "never runs it again") {
		t.Fatalf("serialization failure described as %+v", info)
	}
	if a.Tx() != TxOpen {
		t.Fatalf("after ORA-08177: %v, want the transaction open", a.Tx())
	}
	a.Rollback(ctx)
}

// The catalog reads DDL through DBMS_METADATA, and lists the schema's
// items, which their DDL makes again.
func TestOracleCatalog(t *testing.T) {
	d := openOracle(t, oracleConfig)
	ctx := context.Background()
	mustExec(t, d, `DROP MATERIALIZED VIEW IF EXISTS "cat_mv"`, `DROP TABLE IF EXISTS "cat_t"`,
		`CREATE TABLE "cat_t" ("id" NUMBER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY, "v" VARCHAR2(20) DEFAULT 'it''s' NOT NULL, "at" DATE DEFAULT SYSDATE)`,
		`COMMENT ON TABLE "cat_t" IS 'a table'`, `COMMENT ON COLUMN "cat_t"."v" IS 'a value'`,
		`INSERT INTO "cat_t" ("v") VALUES ('x')`,
		`CREATE MATERIALIZED VIEW "cat_mv" AS SELECT "id" FROM "cat_t"`)
	t.Cleanup(func() { mustExec(t, d, `DROP MATERIALIZED VIEW IF EXISTS "cat_mv"`, `DROP TABLE IF EXISTS "cat_t"`) })
	objs, err := d.Dialect.Objects(ctx, d.SQL, "DGOPHER")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]Object{}
	for _, o := range objs {
		kinds[o.Name] = o
	}
	if o := kinds["cat_t"]; o.Kind != KindTable || o.Comment != "a table" || o.Bytes <= 0 {
		t.Errorf("table %+v", o)
	}
	if o := kinds["cat_mv"]; o.Kind != KindMaterializedView {
		t.Errorf("materialized view %+v", o)
	}
	cols, err := d.Dialect.Columns(ctx, d.SQL, "DGOPHER", "cat_t")
	if err != nil || len(cols) != 3 {
		t.Fatal(cols, err)
	}
	if c := cols[0]; !c.AutoIncrement || !c.PrimaryKey || c.Type != "NUMBER" {
		t.Errorf("identity %+v", c)
	}
	if c := cols[1]; c.Default != "'it''s'" || !c.HasDefault || c.Nullable || c.Comment != "a value" || c.Type != "VARCHAR2(20)" {
		t.Errorf("default %+v", c)
	}
	ddl, err := d.Dialect.DDL(ctx, d.SQL, "DGOPHER", Object{Name: "cat_mv", Kind: KindMaterializedView})
	if err != nil || !strings.Contains(ddl, "MATERIALIZED VIEW") {
		t.Errorf("materialized view DDL %q %v", ddl, err)
	}
	if schemas, err := d.Dialect.Schemas(ctx, d.SQL); err != nil || !slices.Contains(schemas, "DGOPHER") || slices.Contains(schemas, "SYS") {
		t.Errorf("schemas %v %v", schemas, err)
	}
	if schema, err := d.Dialect.CurrentSchema(ctx, d.SQL); err != nil || schema != "DGOPHER" {
		t.Errorf("current schema %q %v", schema, err)
	}
}

// go-ora v3.0.1 reads no rows of a result with a column of no length, as
// a NULL or an empty string selected as it is, and fails reading one with
// other columns: both fail, with a hint to give the column a type, rather
// than show no rows. The failure may come from the query or from the
// fetch, which may return rows beside its error; either is accepted when
// it carries the hint. The right rows with no error are accepted too, as
// the driver sometimes reads such a result. A NULL of a type reads.
func TestOracleColumnOfNoLength(t *testing.T) {
	d := openOracle(t, oracleConfig)
	ctx := context.Background()
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, q := range []string{"SELECT NULL a FROM dual", "SELECT '' FROM dual CONNECT BY LEVEL <= 3", "SELECT 1 b, NULL a FROM dual",
		"SELECT NULL a, 'x' b FROM dual", "SELECT NULL a, TO_CLOB('x') b FROM dual", "SELECT CASE WHEN 1 = 0 THEN 0 END a FROM dual",
		"SELECT CASE WHEN 1 = 0 THEN HEXTORAW('00') END a FROM dual", "SELECT CASE WHEN 1 = 0 THEN TRUE END a FROM dual",
		"SELECT CASE WHEN 1 = 0 THEN 1.5d END a FROM dual"} {
		want := 1
		if strings.Contains(q, "CONNECT BY") {
			want = 3
		}
		c, err := s.Query(ctx, q)
		if err == nil {
			var rows [][]any
			rows, err = c.Fetch(10)
			c.Close()
			if err == nil {
				if len(rows) != want {
					t.Errorf("%s: %d rows, want %d", q, len(rows), want)
				}
				continue
			}
		}
		if hint := DescribeError(err).Hint; !strings.Contains(hint, "CAST(NULL AS VARCHAR2(1))") {
			t.Errorf("%s: %v, hint %q", q, err, hint)
		}
	}
	if row, _ := queryOne(t, s, "SELECT CAST(NULL AS VARCHAR2(1)) a, 1 b FROM dual"); row[0] != nil || Display(row[1]) != "1" {
		t.Fatalf("a NULL of a type: %v", row)
	}
	// A result of no rows, and one whose LOB the driver reads after, read
	// as they are.
	for q, want := range map[string]int{"SELECT 1 FROM dual WHERE 1 = 0": 0, "SELECT TO_CLOB('x') FROM dual CONNECT BY LEVEL <= 3": 3} {
		c, err := s.Query(ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		rows, err := c.Fetch(10)
		c.Close()
		if err != nil || len(rows) != want {
			t.Errorf("%s: %d rows, %v", q, len(rows), err)
		}
	}
}
