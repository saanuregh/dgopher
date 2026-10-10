package app

import (
	"strings"
	"testing"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// serverApp is an app connected to a test server, with the statements
// setup run on its pool and cleanup run at the end.
func serverApp(t *testing.T, cfg db.Config, setup, cleanup []string) (*App, *ui.Tester, *connection.Conn) {
	t.Helper()
	a := newTestApp(t)
	cn := addConn(a, cfg)
	tt := ui.NewTester(a.view, 1200, 800)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	t.Cleanup(func() {
		for _, s := range cleanup {
			cn.DB.SQL.Exec(s)
		}
	})
	for _, s := range setup {
		if _, err := cn.DB.SQL.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return a, tt, cn
}

// fileRowCount counts the rows of a table on the server.
func fileRowCount(t *testing.T, cn *connection.Conn, table string) int {
	t.Helper()
	var n int
	if err := cn.DB.SQL.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A file whose connection is lost after its SET search_path stops at the
// refusal that follows, though it was not to stop at errors: the rest
// would run on the new connection, in the default schema.
func TestSQLFileStopsAfterReset(t *testing.T) {
	testutil.Integration(t)
	drop := []string{"DROP SCHEMA IF EXISTS zz_fix_file CASCADE", "DROP TABLE IF EXISTS public.zz_fix_file_rows"}
	a, tt, cn := serverApp(t, testutil.PGConfig(), append(drop, "CREATE SCHEMA zz_fix_file",
		"CREATE TABLE zz_fix_file.zz_fix_file_rows (id int)", "CREATE TABLE public.zz_fix_file_rows (id int)"), drop)
	path := writeSQL(t, "lost.sql", "SET search_path TO zz_fix_file;\nSELECT pg_terminate_backend(pg_backend_pid());\n"+
		"INSERT INTO zz_fix_file_rows VALUES (1);\nINSERT INTO zz_fix_file_rows VALUES (2);\nINSERT INTO zz_fix_file_rows VALUES (3);\n")
	x := runFile(t, a, tt, cn, path, func(x *sqlFileRun) { x.oneTx, x.stopOnError = false, false })
	if !strings.Contains(x.err, "connection was lost") || len(x.failures) != 2 {
		t.Fatalf("the end of the file: %q, failures %q", x.err, x.failures)
	}
	for _, table := range []string{"public.zz_fix_file_rows", "zz_fix_file.zz_fix_file_rows"} {
		if n := fileRowCount(t, cn, table); n != 0 {
			t.Errorf("%s holds %d rows", table, n)
		}
	}
}

// mysqlFileApp is an app connected to the MySQL test server, with an
// InnoDB table zz_fix_autocommit, dropped at the end.
func mysqlFileApp(t *testing.T) (*App, *ui.Tester, *connection.Conn) {
	t.Helper()
	cfg := db.Config{ID: "my", Name: "my", Engine: db.MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dgopher", Database: "shop"}
	drop := []string{"DROP TABLE IF EXISTS shop.zz_fix_autocommit"}
	return serverApp(t, cfg, append(drop, "CREATE TABLE shop.zz_fix_autocommit (id int) ENGINE=InnoDB"), drop)
}

// With MySQL's autocommit off, a file that commits what it wrote
// succeeds: autocommit off alone holds no transaction to roll back.
func TestSQLFileAutocommitOffCommitted(t *testing.T) {
	testutil.Integration(t)
	a, tt, cn := mysqlFileApp(t)
	path := writeSQL(t, "committed.sql", "set autocommit=0;\nINSERT INTO zz_fix_autocommit VALUES (1);\ncommit;\nSET @x=1;\n")
	x := runFile(t, a, tt, cn, path, nil)
	if x.err != "" || len(x.failures) != 0 {
		t.Fatalf("the end of the file: %q, failures %q", x.err, x.failures)
	}
	for _, e := range auditEvents(t, a) {
		if e.Kind == audit.KindStatement && e.Statement == "ROLLBACK" {
			t.Fatalf("a ROLLBACK was audited: %+v", e)
		}
	}
	if n := fileRowCount(t, cn, "shop.zz_fix_autocommit"); n != 1 {
		t.Fatalf("%d rows committed", n)
	}
}

// With MySQL's autocommit off, a file whose last statement left its
// transaction open is rolled back, audited and reported with the line
// that began it.
func TestSQLFileAutocommitOffLeftOpen(t *testing.T) {
	testutil.Integration(t)
	a, tt, cn := mysqlFileApp(t)
	path := writeSQL(t, "open.sql", "set autocommit=0;\nINSERT INTO zz_fix_autocommit VALUES (1);\n")
	x := runFile(t, a, tt, cn, path, nil)
	if !strings.Contains(x.err, "the transaction begun at line 2 was never committed; its changes were rolled back") {
		t.Fatalf("the end of the file: %q, failures %q", x.err, x.failures)
	}
	if e := rollbackAudited(t, a, "line 2"); e == nil || e.Error != "" {
		t.Fatalf("the rollback: %+v", e)
	}
	if n := fileRowCount(t, cn, "shop.zz_fix_autocommit"); n != 0 {
		t.Fatalf("%d rows stayed", n)
	}
}
