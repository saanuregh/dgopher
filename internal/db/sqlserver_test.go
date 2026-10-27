package db

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"
)

// A uniqueidentifier's bytes, as SQL Server keeps them and the driver
// gives them, read as the UUID SQL Server shows: the first three groups
// are stored little-endian. The bytes are those the server sent for
// CAST('6F9619FF-8B86-D011-B42D-00C04FC964FF' AS uniqueidentifier).
func TestSQLServerUniqueIdentifier(t *testing.T) {
	raw := []byte{0xff, 0x19, 0x96, 0x6f, 0x86, 0x8b, 0x11, 0xd0, 0xb4, 0x2d, 0x00, 0xc0, 0x4f, 0xc9, 0x64, 0xff}
	if got := sqlserverValue("UNIQUEIDENTIFIER", raw); got != "6F9619FF-8B86-D011-B42D-00C04FC964FF" {
		t.Fatalf("got %v", got)
	}
	if got := sqlserverValue("VARBINARY", raw); !slices.Equal(got.([]byte), raw) {
		t.Fatalf("another type's bytes changed: %v", got)
	}
	if got := sqlserverValue("UNIQUEIDENTIFIER", nil); got != nil {
		t.Fatalf("NULL became %v", got)
	}
}

// Values as the driver gives them show as SQL Server shows them.
func TestSQLServerValues(t *testing.T) {
	tm := time.Date(1, time.January, 1, 7, 8, 9, 123456700, time.UTC)
	if got := duckValue("TIME", tm); got != "07:08:09.1234567" {
		t.Errorf("time(7): %v", got)
	}
	if got := duckValue("TIME", time.Date(1, time.January, 1, 7, 8, 9, 123456000, time.UTC)); got != "07:08:09.123456" {
		t.Errorf("a time of microseconds, as DuckDB's: %v", got)
	}
	for _, c := range []struct {
		v    any
		want string
	}{
		{"it's", "N'it''s'"},
		{"ünï", "N'ünï'"},
		{[]byte{0, 0xff}, "0x00ff"},
		{[]byte{}, "0x"},
		{true, "1"},
		{false, "0"},
		{int64(7), "7"},
	} {
		if got := Literal(SQLServer, c.v); got != c.want {
			t.Errorf("Literal(%#v) = %s, want %s", c.v, got, c.want)
		}
	}
	for _, name := range []string{"GEOGRAPHY", "GEOMETRY", "HIERARCHYID", "IMAGE", "VARBINARY", "BINARY", "UNIQUEIDENTIFIER"} {
		if !isBinaryType(name) {
			t.Errorf("%s is not read as bytes", name)
		}
	}
	if sqlserverVersion("12.0.2000.8", 5) != "12.0.2000.8 (Azure SQL Database)" || sqlserverVersion("17.0.5035.5", 3) != "17.0.5035.5" {
		t.Error("versions")
	}
}

func TestSQLServerQuoting(t *testing.T) {
	d := sqlserverDialect{}
	if got := d.Quote("a]b"); got != "[a]]b]" {
		t.Errorf("quote %s", got)
	}
	if got := QualifiedName(d, "dbo", "t"); got != "[dbo].[t]" {
		t.Errorf("qualified %s", got)
	}
	st := Statement{SQL: "UPDATE [x@p1] SET [a] = @p1 WHERE [id] = @p2", Args: []any{"it's", int64(3)}}
	if got := st.Script(SQLServer); got != "UPDATE [x@p1] SET [a] = N'it''s' WHERE [id] = 3;" {
		t.Errorf("script %s", got)
	}
}

// Which batches SQL Server refuses inside a transaction, as the server
// answered each inside BEGIN TRAN (errors 226, 574, 3021, 6115, 8920);
// and which take effect at once, as UPDATE STATISTICS, whose statistics
// a rollback kept.
func TestSQLServerOutsideTransaction(t *testing.T) {
	r := sqlserverEngine{}
	outside := []string{"CREATE DATABASE x", "ALTER DATABASE shop SET RECOVERY FULL", "DROP DATABASE x", "BACKUP DATABASE shop TO DISK = 'x'",
		"RESTORE DATABASE x FROM DISK = 'y'", "RECONFIGURE", "KILL 55", "CREATE FULLTEXT CATALOG c", "DBCC SHRINKDATABASE(shop)",
		"CREATE ENDPOINT ep AS TCP (LISTENER_PORT = 7022) FOR DATABASE_MIRRORING (ROLE = ALL)", "USE master; CREATE DATABASE x",
		"backup log shop to disk = 'x'"}
	inside := []string{"SELECT 1", "UPDATE t SET [kill] = 1", "SELECT 'CREATE DATABASE x'", "-- BACKUP DATABASE x\nSELECT 1",
		"ALTER DATABASE SCOPED CONFIGURATION SET MAXDOP = 1", "DBCC CHECKDB", "KILL QUERY NOTIFICATION SUBSCRIPTION ALL",
		"CREATE PROCEDURE p AS BACKUP DATABASE shop TO DISK = 'x'", "CREATE TABLE backup_log (a int)", "UPDATE STATISTICS t"}
	for _, q := range outside {
		if !r.OutsideTransaction(q) {
			t.Errorf("%q runs in a transaction", q)
		}
	}
	for _, q := range inside {
		if r.OutsideTransaction(q) {
			t.Errorf("%q is outside a transaction", q)
		}
	}
	for q, want := range map[string]bool{
		"UPDATE STATISTICS dbo.t": true, "EXEC sp_updatestats": true, "EXEC sys.sp_updatestats": true, "sp_updatestats": true,
		"UPDATE t SET statistics = 1": false, "SELECT sp_updatestats FROM t": false, "CREATE PROCEDURE p AS UPDATE STATISTICS t": false,
	} {
		if got := r.TakesEffectAtOnce(q); got != want {
			t.Errorf("TakesEffectAtOnce(%q) = %v, want %v", q, got, want)
		}
	}
}

// SET IMPLICIT_TRANSACTIONS and ANSI_DEFAULTS turn autocommit off when ON;
// BEGIN begins a transaction only as BEGIN TRAN[SACTION].
func TestSQLServerAutocommitAndBegin(t *testing.T) {
	for q, want := range map[string][2]bool{
		"SET IMPLICIT_TRANSACTIONS ON":          {false, true},
		"SET IMPLICIT_TRANSACTIONS OFF":         {true, true},
		"SET NOCOUNT, IMPLICIT_TRANSACTIONS ON": {false, true},
		"SET ANSI_DEFAULTS ON":                  {false, true},
		"SET NOCOUNT ON":                        {false, false},
		"SET @x = 1":                            {false, false},
		// Only a batch that begins with SET: the server's setting is
		// read after each run anyway (Session.AutocommitOff).
		"SELECT 1; SET IMPLICIT_TRANSACTIONS ON":              {false, false},
		"SET IMPLICIT_TRANSACTIONS ON; SET ANSI_DEFAULTS OFF": {true, true},
	} {
		on, ok := SetsAutocommit(SQLServer, q)
		if [2]bool{on, ok} != want {
			t.Errorf("SetsAutocommit(%q) = %v %v, want %v", q, on, ok, want)
		}
	}
	r := sqlserverEngine{}
	for q, compound := range map[string]bool{"BEGIN TRAN": false, "BEGIN TRANSACTION t": false, "BEGIN DISTRIBUTED TRANSACTION": false,
		"BEGIN TRY SELECT 1 END TRY BEGIN CATCH END CATCH": true, "BEGIN SELECT 1 END": true} {
		if got := r.beginIsCompound(leadingWords(SQLServer, q)); got != compound {
			t.Errorf("beginIsCompound(%q) = %v", q, got)
		}
	}
}

// SQL Server's errors are described with their number, line and the
// errors before them; deadlock victims and snapshot conflicts say the
// transaction can be run again.
func TestSQLServerErrors(t *testing.T) {
	first := mssql.Error{Number: 547, Message: "The INSERT statement conflicted with the FOREIGN KEY constraint.", LineNo: 2}
	err := mssql.Error{Number: 102, Class: 15, Message: "Incorrect syntax near 'FRM'.", LineNo: 3}
	err.All = []mssql.Error{first, err}
	info := DescribeError(err)
	if info.Code != "102" || info.Line != 3 || info.Near != "FRM" || !strings.Contains(info.Detail, "FOREIGN KEY") {
		t.Errorf("described %+v", info)
	}
	inProc := DescribeError(mssql.Error{Number: 8134, Message: "Divide by zero error encountered.", LineNo: 4, ProcName: "dbo.p"})
	if inProc.Line != 0 || !strings.Contains(inProc.Detail, "dbo.p, line 4") {
		t.Errorf("in a procedure %+v", inProc)
	}
	for _, n := range []int32{1205, 3960} {
		if conflictHint(mssql.Error{Number: n}) == "" {
			t.Errorf("%d has no hint", n)
		}
	}
	if conflictHint(mssql.Error{Number: 2627}) != "" {
		t.Error("a duplicate key reads as a conflict")
	}
	for n, want := range map[int32]ProblemKind{18456: ProblemAuth, 4060: ProblemUnknownDatabase} {
		if got := ClassifyConnectError(mssql.Error{Number: n, Message: "login error"}); got != want {
			t.Errorf("%d: %v", n, got)
		}
	}
	if ClassifyConnectError(errors.New("TLS Handshake failed: x509: certificate signed by unknown authority")) != ProblemTLS {
		t.Error("TLS failure")
	}
	if !endsConnection(mssql.Error{Number: 596, Class: 21}) || endsConnection(mssql.Error{Number: 8134, Class: 16}) {
		t.Error("fatal severities")
	}
}

// A cloud identity of SQL Server is Entra's token for Azure SQL alone,
// which its own command asks for.
func TestSQLServerIdentity(t *testing.T) {
	base := Config{Name: "n", Engine: SQLServer, Host: "srv.database.windows.net", TLS: TLSVerifyFull}
	sql := base
	sql.Identity = IdentityAzureSQL
	if err := sql.Validate(); err != nil {
		t.Fatal(err)
	}
	pg := base
	pg.Identity = IdentityAzure
	if err := pg.Validate(); err == nil || !strings.Contains(err.Error(), "does not log in to SQL Server") {
		t.Fatalf("Azure's PostgreSQL token: %v", err)
	}
	if argv := IdentityCommand(&sql); !slices.Contains(argv, "https://database.windows.net/") {
		t.Fatalf("command %q", argv)
	}
	postgres := Config{Name: "n", Engine: Postgres, Host: "h", TLS: TLSVerifyFull, Identity: IdentityAzureSQL}
	if err := postgres.Validate(); err == nil {
		t.Fatal("PostgreSQL took Azure SQL's token")
	}
}

func TestSQLServerKerberosCache(t *testing.T) {
	for name, want := range map[string]string{"FILE:/tmp/k": "/tmp/k", "/tmp/plain": "/tmp/plain"} {
		t.Setenv("KRB5CCNAME", name)
		if got, err := kerberosCache(); err != nil || got != want {
			t.Errorf("%s: %q %v", name, got, err)
		}
	}
	t.Setenv("KRB5CCNAME", "KCM:1000")
	if _, err := kerberosCache(); err == nil {
		t.Error("a KCM cache was taken as a file")
	}
}

// A tunnel's or a proxy's local port is the only address dialled: Azure
// SQL's redirect elsewhere would leave the tunnel.
// disable still encrypts the login, unverified, as the driver's encrypt=false
// does: encrypt=disable would send the password scrambled only.
func TestSQLServerTLSModes(t *testing.T) {
	ep := endpoint{host: "db.example.com", port: 1433}
	for mode, want := range map[TLSMode]msdsn.Encryption{"": msdsn.EncryptionOff, TLSDisable: msdsn.EncryptionOff,
		TLSPrefer: msdsn.EncryptionOff, TLSRequire: msdsn.EncryptionRequired, TLSVerifyFull: msdsn.EncryptionRequired} {
		cfg := Config{Engine: SQLServer, Host: ep.host, Port: ep.port, User: "sa", TLS: mode}
		tc, err := serverTLS(&cfg, ep.host)
		if err != nil {
			t.Fatal(err)
		}
		p, err := sqlserverParams(cfg, ep, tc)
		if err != nil {
			t.Fatalf("%q: %v", mode, err)
		}
		if p.Encryption != want {
			t.Errorf("%q: encryption %d, want %d", mode, p.Encryption, want)
		}
		if p.TLSConfig == nil {
			t.Errorf("%q: no TLS settings for the login", mode)
			continue
		}
		if verify := mode == TLSVerifyFull; p.TLSConfig.InsecureSkipVerify == verify || p.TrustServerCertificate == verify {
			t.Errorf("%q: InsecureSkipVerify %v, TrustServerCertificate %v", mode, p.TLSConfig.InsecureSkipVerify, p.TrustServerCertificate)
		}
	}
}

func TestSQLServerDialsOnlyTheRoute(t *testing.T) {
	_, err := onlyAddress{addr: "127.0.0.1:1", hint: "set the server's connection policy to Proxy"}.DialContext(context.Background(), "tcp", "worker.example.com:11000")
	if err == nil || !strings.Contains(err.Error(), "Proxy") {
		t.Fatalf("dialled elsewhere: %v", err)
	}
}

func sqlserverSession(t *testing.T) (*DB, *Session) {
	t.Helper()
	d := open(t, fixtureOf(t, SQLServer))
	s, err := d.Session(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return d, s
}

// The transaction is the server's, however a batch began or ended it:
// nested BEGIN TRAN counts as one, IMPLICIT_TRANSACTIONS begins one at
// a read, XACT_ABORT rolls it back on an error; schema changes roll back.
func TestIntegrationSQLServerTransactions(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d, s := sqlserverSession(t)
	steps := []struct {
		sql   string
		want  TxState
		fails bool
	}{
		{sql: "BEGIN TRAN", want: TxOpen},
		{sql: "BEGIN TRAN", want: TxOpen},
		{sql: "COMMIT", want: TxOpen}, // the inner one only
		{sql: "COMMIT", want: TxNone},
		{sql: "SELECT 1; BEGIN TRAN; SELECT 2", want: TxOpen},
		{sql: "CREATE TABLE it.rolled_back (a int)", want: TxOpen},
		{sql: "CREATE DATABASE it_never", want: TxOpen, fails: true}, // 226, the transaction kept
		{sql: "ROLLBACK", want: TxNone},
		{sql: "SET IMPLICIT_TRANSACTIONS ON", want: TxNone},
		{sql: "SELECT 1", want: TxNone},
		{sql: "SELECT * FROM it.customers", want: TxOpen},
		{sql: "ROLLBACK", want: TxNone},
		{sql: "SET IMPLICIT_TRANSACTIONS OFF", want: TxNone},
		{sql: "SET XACT_ABORT ON; BEGIN TRAN", want: TxOpen},
		{sql: "SELECT 1/0", want: TxNone, fails: true},
		{sql: "SET XACT_ABORT OFF; BEGIN TRAN", want: TxOpen},
		{sql: "SELECT 1/0", want: TxOpen, fails: true},
		{sql: "ROLLBACK", want: TxNone},
	}
	for _, st := range steps {
		if _, err := s.Exec(ctx, st.sql); (err != nil) != st.fails {
			t.Fatalf("%s: %v", st.sql, err)
		}
		if got := s.Tx(); got != st.want {
			t.Fatalf("after %q: %v, want %v", st.sql, got, st.want)
		}
		if st.sql == "SET IMPLICIT_TRANSACTIONS ON" && !s.AutocommitOff() {
			t.Fatal("IMPLICIT_TRANSACTIONS ON reads as autocommit")
		}
	}
	if s.AutocommitOff() {
		t.Fatal("autocommit off after IMPLICIT_TRANSACTIONS OFF")
	}
	var table *string
	d.SQL.QueryRow("SELECT OBJECT_NAME(OBJECT_ID('it.rolled_back'))").Scan(&table)
	if table != nil {
		t.Fatal("the rollback kept the table: SQL Server's DDL is not transactional")
	}
}

// Under IMPLICIT_TRANSACTIONS ON the app's BEGIN TRANSACTION nests in an
// implicit one: its Commit commits both, so applied edits are kept. A
// transaction the user nested is left open, with an error.
func TestIntegrationSQLServerCommitUnderImplicitTransactions(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d, s := sqlserverSession(t)
	for _, q := range []string{"DROP TABLE IF EXISTS dbo.it_implicit", "CREATE TABLE dbo.it_implicit (id INT PRIMARY KEY)"} {
		if _, err := d.SQL.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		s.Close() // first: its transaction would hold the DROP
		d.SQL.ExecContext(context.Background(), "DROP TABLE IF EXISTS dbo.it_implicit")
	})
	count := func() int {
		t.Helper()
		var n int
		if err := d.SQL.QueryRowContext(ctx, "SELECT count(*) FROM dbo.it_implicit WITH (READPAST)").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if _, err := s.Exec(ctx, "SET IMPLICIT_TRANSACTIONS ON"); err != nil {
		t.Fatal(err)
	}
	if err := s.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(ctx, "INSERT INTO dbo.it_implicit VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(ctx); err != nil || s.Tx() != TxNone || count() != 1 {
		t.Fatalf("commit: %v, tx %v, rows %d", err, s.Tx(), count())
	}
	err := s.ApplyEdits(ctx, []Statement{{SQL: "INSERT INTO dbo.it_implicit VALUES (2)", Want: 1}}, func(int, int64, time.Duration, error) {})
	if err != nil || s.Tx() != TxNone || count() != 2 {
		t.Fatalf("ApplyEdits: %v, tx %v, rows %d", err, s.Tx(), count())
	}
	if _, err := s.Exec(ctx, "SET IMPLICIT_TRANSACTIONS OFF; BEGIN TRAN"); err != nil {
		t.Fatal(err)
	}
	if err := s.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(ctx, "INSERT INTO dbo.it_implicit VALUES (3)"); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(ctx); err == nil || s.Tx() != TxOpen {
		t.Fatalf("a commit inside the user's transaction: %v, tx %v", err, s.Tx())
	}
	if err := s.Rollback(ctx); err != nil || count() != 2 {
		t.Fatalf("rollback: %v, rows %d", err, count())
	}
}

// A cancelled statement keeps the connection and its transaction; a
// killed session loses it, which the commit says.
func TestIntegrationSQLServerCancelAndKill(t *testing.T) {
	integration(t)
	bg := context.Background()
	d, s := sqlserverSession(t)
	if err := s.Begin(bg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(bg, "UPDATE it.customers SET name = 'kept' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(bg)
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	if _, err := s.Exec(ctx, "WAITFOR DELAY '00:00:10'"); err == nil {
		t.Fatal("the cancelled statement ran to its end")
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("the cancel took %v", el)
	}
	if s.Tx() != TxOpen {
		t.Fatalf("after the cancel: %v, want the transaction open", s.Tx())
	}
	c, err := s.Query(bg, "SELECT name FROM it.customers WHERE id = 1")
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := c.Fetch(1)
	if len(rows) != 1 || rows[0][0] != "kept" {
		t.Fatalf("the transaction's change: %v", rows)
	}
	c, err = s.Query(bg, "SELECT @@SPID")
	if err != nil {
		t.Fatal(err)
	}
	rows, _ = c.Fetch(1)
	c.Close()
	if _, err := d.SQL.Exec("KILL " + Display(rows[0][0])); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(bg); !errors.Is(err, ErrTxLost) {
		t.Fatalf("commit after KILL: %v, want ErrTxLost", err)
	}
	var name string
	d.SQL.QueryRow("SELECT name FROM it.customers WHERE id = 1").Scan(&name)
	if name == "kept" {
		t.Fatal("the killed transaction committed")
	}
}

// Every type shows as SQL Server shows it, a uniqueidentifier as its
// UUID and money and decimal exactly; spatial and hierarchyid values are
// bytes, not text.
func TestIntegrationSQLServerValues(t *testing.T) {
	integration(t)
	ctx := context.Background()
	_, s := sqlserverSession(t)
	c, err := s.Query(ctx, `SELECT CAST('6F9619FF-8B86-D011-B42D-00C04FC964FF' AS uniqueidentifier) AS u,
 CAST('2024-05-06 07:08:09.1234567 +05:30' AS datetimeoffset) AS dto,
 CAST('2024-05-06 07:08:09.123456' AS datetime2) AS dt2,
 CAST('2024-05-06 07:08:00' AS smalldatetime) AS sdt,
 CAST('07:08:09.1234567' AS time) AS tm,
 CAST(1234.5678 AS money) AS m, CAST(-12.34 AS smallmoney) AS sm,
 CAST(12345678901234567890.123456789 AS decimal(38,9)) AS dec,
 CAST(1 AS bit) AS b, CAST(N'héllo 🙂' AS nvarchar(max)) AS nv,
 CAST(0x0102FF AS varbinary(max)) AS vb, CAST(42 AS sql_variant) AS sv,
 CAST('<a>1</a>' AS xml) AS x, geography::Point(47.65, -122.34, 4326) AS geo,
 hierarchyid::Parse('/1/') AS h, @@DBTS AS rv`)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.Fetch(1)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	got := map[string]string{}
	for i, col := range c.Columns {
		got[col.Name] = Display(rows[0][i])
	}
	want := map[string]string{
		"u": "6F9619FF-8B86-D011-B42D-00C04FC964FF", "dto": "2024-05-06 07:08:09.123456 +05:30", "dt2": "2024-05-06 07:08:09.123456",
		"sdt": "2024-05-06 07:08:00", "tm": "07:08:09.1234567", "m": "1234.5678", "sm": "-12.3400",
		"dec": "12345678901234567890.123456789", "b": "true", "nv": "héllo 🙂", "vb": `\x0102ff`, "sv": "42", "x": "<a>1</a>",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: %q, want %q", k, got[k], w)
		}
	}
	for _, k := range []string{"geo", "h", "rv"} {
		if _, ok := rows[0][slices.IndexFunc(c.Columns, func(col ColumnInfo) bool { return col.Name == k })].([]byte); !ok {
			t.Errorf("%s: %T, want bytes", k, rows[0][slices.IndexFunc(c.Columns, func(col ColumnInfo) bool { return col.Name == k })])
		}
	}
}

// Literals read back as the values they write.
func TestIntegrationSQLServerLiterals(t *testing.T) {
	integration(t)
	ctx := context.Background()
	_, s := sqlserverSession(t)
	text := "it's \\ \"q\" ünï 🙂 \n new line"
	c, err := s.Query(ctx, "SELECT "+Literal(SQLServer, text)+", "+Literal(SQLServer, []byte{0, 1, 0xff})+", "+Literal(SQLServer, true))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.Fetch(1)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0][0] != text || !slices.Equal(rows[0][1].([]byte), []byte{0, 1, 0xff}) || Display(rows[0][2]) != "1" {
		t.Fatalf("read back %#v", rows[0])
	}
}

// PRINT and RAISERROR of a low severity reach the session's messages;
// the results of a batch are all read, and an error after the first is
// the statement's.
func TestIntegrationSQLServerMessagesAndResults(t *testing.T) {
	integration(t)
	ctx := context.Background()
	_, s := sqlserverSession(t)
	if _, err := s.Exec(ctx, "PRINT 'hello'; RAISERROR('careful', 10, 1)"); err != nil {
		t.Fatal(err)
	}
	if got := s.TakeMessages(); !slices.Equal(got, []string{"hello", "careful"}) {
		t.Fatalf("messages %q", got)
	}
	if got := s.TakeMessages(); got != nil {
		t.Fatalf("messages taken twice: %q", got)
	}
	c, err := s.Query(ctx, "SELECT 1 AS a; PRINT 'between'; SELECT 2 AS b, 3 AS c UNION ALL SELECT 4, 5")
	if err != nil {
		t.Fatal(err)
	}
	more, err := c.ReadAll(MaxRows)
	if err != nil || len(more) != 1 {
		t.Fatalf("results %d %v", len(more), err)
	}
	first, _ := c.Fetch(10)
	second, _ := more[0].Fetch(10)
	if len(first) != 1 || len(second) != 2 || len(more[0].Columns) != 2 || more[0].Columns[1].Name != "c" || !more[0].Done() {
		t.Fatalf("first %v, second %v %+v", first, second, more[0].Columns)
	}
	if got := s.TakeMessages(); !slices.Equal(got, []string{"between"}) {
		t.Fatalf("messages %q", got)
	}
	c, err = s.Query(ctx, "SELECT 1 AS a; SELECT 2 AS b; SELECT 1/0 AS c")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ReadAll(MaxRows); err == nil {
		t.Fatal("the batch's error was lost")
	}
	// Read a page at a time, as a table's rows, the error still comes.
	c, err = s.Query(ctx, "SELECT 1 AS a; SELECT 1/0 AS c")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Fetch(100); err == nil {
		t.Fatal("the error after the first result was lost")
	}
	if c, err := s.Query(ctx, "SELECT 1"); err != nil {
		t.Fatalf("after: %v", err)
	} else {
		c.Close()
	}
}

// A deadlock's victim is rolled back, and its error says it may be run
// again.
func TestIntegrationSQLServerDeadlock(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d, a := sqlserverSession(t)
	b, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, st := range []struct {
		s   *Session
		sql string
	}{{a, "BEGIN TRAN"}, {a, "UPDATE it.customers SET name = 'a' WHERE id = 1"}, {b, "BEGIN TRAN"}, {b, "UPDATE it.customers SET name = 'b' WHERE id = 2"}} {
		if _, err := st.s.Exec(ctx, st.sql); err != nil {
			t.Fatal(st.sql, err)
		}
	}
	errs := make(chan error, 2)
	go func() { _, err := a.Exec(ctx, "UPDATE it.customers SET name = 'a' WHERE id = 2"); errs <- err }()
	time.Sleep(300 * time.Millisecond)
	go func() { _, err := b.Exec(ctx, "UPDATE it.customers SET name = 'b' WHERE id = 1"); errs <- err }()
	var victim error
	for range 2 {
		if err := <-errs; err != nil {
			victim = err
		}
	}
	var me mssql.Error
	if !errors.As(victim, &me) || me.Number != 1205 || DescribeError(victim).Hint == "" {
		t.Fatalf("victim %v", victim)
	}
	if states := []TxState{a.Tx(), b.Tx()}; !slices.Contains(states, TxNone) || !slices.Contains(states, TxOpen) {
		t.Fatalf("a %v, b %v: want the victim's rolled back", states[0], states[1])
	}
	a.Rollback(ctx)
	b.Rollback(ctx)
}

// The server is SQL Server, of its version; the connection names its
// databases, and opens another's pool; TLS is the server's answer.
func TestIntegrationSQLServerConnection(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d, _ := sqlserverSession(t)
	e, v, err := d.Identify(ctx)
	if err != nil || e != SQLServer || !strings.Contains(v, ".") {
		t.Fatalf("identify %s %q %v", e, v, err)
	}
	dbs, err := d.Dialect.Databases(ctx, d.SQL)
	if err != nil || !slices.Contains(dbs, "shop") || slices.Contains(dbs, "tempdb") {
		t.Fatalf("databases %v %v", dbs, err)
	}
	master, err := d.Database(ctx, "master")
	if err != nil {
		t.Fatal(err)
	}
	if name, err := queryString(ctx, master.SQL, "SELECT DB_NAME()"); err != nil || name != "master" {
		t.Fatalf("master's pool is in %q %v", name, err)
	}
	if d.TLSState() != "no TLS" {
		t.Errorf("TLS state %q with TLS disabled", d.TLSState())
	}
	for mode, state := range map[TLSMode]string{TLSRequire: "TLS", TLSPrefer: "no TLS"} {
		cfg := sqlserverConfig
		cfg.TLS = mode
		o, err := Open(ctx, cfg, nil)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if o.TLSState() != state {
			t.Errorf("%s: %q, want %q", mode, o.TLSState(), state)
		}
		o.Close()
	}
	cfg := sqlserverConfig
	cfg.TLS = TLSVerifyFull
	if _, err := Open(ctx, cfg, nil); err == nil || ClassifyConnectError(err) != ProblemTLS {
		t.Fatalf("a self-signed certificate verified: %v", err)
	}
	cfg = sqlserverConfig
	cfg.Password = "wrong-password"
	if _, err := Open(ctx, cfg, nil); err == nil || ClassifyConnectError(err) != ProblemAuth || strings.Contains(err.Error(), cfg.Password) {
		t.Fatalf("a wrong password: %v", err)
	}
}

// The driver's fields mssqlTxState reads are still there: a rename in an
// upgrade would make every session look outside a transaction.
func TestSQLServerTxStateFields(t *testing.T) {
	conn := reflect.TypeFor[mssql.Conn]()
	if conn.PkgPath() != mssqlPackage || conn.Name() != "Conn" {
		t.Fatalf("driver connection %s.%s", conn.PkgPath(), conn.Name())
	}
	sess, ok := conn.FieldByName("sess")
	if !ok || sess.Type.Kind() != reflect.Pointer {
		t.Fatalf("no sess: %v", sess.Type)
	}
	if id, ok := sess.Type.Elem().FieldByName("tranid"); !ok || id.Type.Kind() != reflect.Uint64 {
		t.Fatalf("no tranid: %v", id.Type)
	}
	if state, ok := mssqlTxState(reflect.ValueOf(mssql.Conn{})); ok || state != TxNone {
		t.Fatalf("a connection without a session: %v %v", state, ok)
	}
}

// A session explained on returns to running statements: SHOWPLAN_XML,
// under which SQL Server runs nothing, and STATISTICS XML are turned off
// after the plan, after a statement cancelled as it ran too, and a write
// that follows is made.
func TestIntegrationSQLServerExplainLeavesSessionRunning(t *testing.T) {
	integration(t)
	bg := context.Background()
	d, s := sqlserverSession(t)
	mustExec(t, d, "DROP TABLE IF EXISTS it.explained", "CREATE TABLE it.explained (n INT)")
	defer d.SQL.Exec("DROP TABLE IF EXISTS it.explained")
	const slow = "SELECT COUNT_BIG(*) FROM sys.all_objects a CROSS JOIN sys.all_objects b CROSS JOIN sys.all_objects c"
	for i, c := range []struct {
		analyze, cancel bool
	}{{false, false}, {true, false}, {false, true}, {true, true}} {
		run, ok := Explain(SQLServer, slow, c.analyze)
		if !ok {
			t.Fatal("no explain")
		}
		ctx, cancel := context.WithCancel(bg)
		var sent []string
		results := 0
		start := time.Now()
		err := s.RunExplain(ctx, run, func(sql string, _ time.Duration, err error) {
			if err != nil {
				t.Errorf("%+v: %s: %v", c, sql, err)
			}
			sent = append(sent, sql)
		}, func(ctx context.Context) error {
			if c.cancel {
				// Cancelled as it runs, or, when SHOWPLAN only compiles
				// it, as it ends.
				defer cancel()
				time.AfterFunc(300*time.Millisecond, cancel)
			}
			cur, err := s.Query(ctx, run.Statement)
			if err != nil {
				return err
			}
			more, err := cur.ReadAll(MaxRows)
			results = 1 + len(more)
			return err
		})
		cancel()
		if c.cancel && c.analyze && (err == nil || time.Since(start) > 5*time.Second) {
			t.Fatalf("%+v: the cancel: %v after %v", c, err, time.Since(start))
		}
		if !c.cancel && (err != nil || results < 1) {
			t.Fatalf("%+v: %v, %d results", c, err, results)
		}
		if want := append(append([]string{}, run.Before...), run.After...); !slices.Equal(sent, want) {
			t.Fatalf("%+v: steps %q, want %q", c, sent, want)
		}
		if _, err := s.Exec(bg, fmt.Sprintf("INSERT INTO it.explained VALUES (%d)", i)); err != nil {
			t.Fatalf("%+v: the write after: %v", c, err)
		}
		var n int
		d.SQL.QueryRow("SELECT COUNT(*) FROM it.explained").Scan(&n)
		if n != i+1 {
			t.Fatalf("%+v: %d rows written, want %d: the session still explains", c, n, i+1)
		}
		cur, err := s.Query(bg, "SELECT 1 AS a")
		if err != nil {
			t.Fatal(err)
		}
		if more, err := cur.ReadAll(MaxRows); err != nil || len(more) != 0 || cur.Columns[0].Name != "a" {
			t.Fatalf("%+v: a SELECT after: %v, %d more results, columns %v", c, err, len(more), cur.Columns)
		}
	}
}

// A change is counted by its own rows: not the rows of the triggers it
// fires, which the driver adds to them, nor none under SET NOCOUNT ON.
func TestIntegrationSQLServerEditCounts(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d, s := sqlserverSession(t)
	for _, q := range []string{"DROP TABLE IF EXISTS dbo.it_counted_log", "DROP TABLE IF EXISTS dbo.it_counted",
		"CREATE TABLE dbo.it_counted (id int PRIMARY KEY, v int)", "CREATE TABLE dbo.it_counted_log (id int, v int)",
		"INSERT INTO dbo.it_counted VALUES (1, 1), (2, 2)",
		"CREATE TRIGGER dbo.it_counted_logged ON dbo.it_counted AFTER UPDATE AS INSERT INTO dbo.it_counted_log SELECT id, v FROM inserted UNION ALL SELECT id, v FROM inserted"} {
		if _, err := d.SQL.ExecContext(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	t.Cleanup(func() {
		d.SQL.ExecContext(context.Background(), "DROP TABLE IF EXISTS dbo.it_counted")
		d.SQL.ExecContext(context.Background(), "DROP TABLE IF EXISTS dbo.it_counted_log")
	})
	edit := []Statement{{SQL: "UPDATE dbo.it_counted SET v = 3 WHERE id = @p1", Args: []any{1}, Want: 1}}
	var counted []int64
	each := func(_ int, n int64, _ time.Duration, _ error) { counted = append(counted, n) }
	if err := s.ApplyEdits(ctx, edit, each); err != nil {
		t.Fatalf("with a trigger: %v (counted %v)", err, counted)
	}
	if _, err := s.Exec(ctx, "SET NOCOUNT ON"); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyEdits(ctx, edit, each); err != nil {
		t.Fatalf("under NOCOUNT: %v (counted %v)", err, counted)
	}
	edit[0].Args = []any{9}
	if err := s.ApplyEdits(ctx, edit, each); err == nil {
		t.Fatalf("no row's change applied (counted %v)", counted)
	}
	if !slices.Equal(counted, []int64{1, 1, 0}) {
		t.Fatalf("counted %v", counted)
	}
}
