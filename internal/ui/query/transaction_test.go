package query

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"

	"github.com/egoist/mygo/ui"
)

// mysqlConfig is the MySQL test server's, on its shop database.
func mysqlConfig() db.Config {
	return db.Config{ID: "my", Name: "my", Engine: db.MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dgopher", Database: "shop"}
}

// ranTimes counts the runs of a statement the host recorded.
func ranTimes(a *fakeQueryHost, sql string) int {
	n := 0
	for _, e := range a.Events {
		if e.Kind == audit.KindStatement && e.Statement == sql {
			n++
		}
	}
	return n
}

// sessionLabels reads the labels of table u on the editor's own session,
// which sees its open transaction.
func sessionLabels(t *testing.T, q *Tab) string {
	t.Helper()
	c, err := q.sess.Query(context.Background(), "SELECT label FROM u ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	rows, err := c.Fetch(100)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, db.Display(r[0]))
	}
	return strings.Join(out, ",")
}

// sessionValue reads one value on the editor's own session.
func sessionValue(t *testing.T, q *Tab, sql string) string {
	t.Helper()
	c, err := q.sess.Query(context.Background(), sql)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	rows, err := c.Fetch(1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%s: %v %v", sql, rows, err)
	}
	return fmt.Sprint(rows[0][0])
}

// The rows of a statement that does more than read, as one calling
// nextval or locking its rows FOR UPDATE, are not edited, which says why:
// applying changes reads the rows again, which would run the statement
// again unreviewed. Only Refresh runs it again, once confirmed.
func TestNonReadResultIsReadOnly(t *testing.T) {
	check := func(t *testing.T, a *fakeQueryHost, tt *ui.Tester, q *Tab, sql, cell string) {
		t.Helper()
		q.Editor.Text = sql
		runWith(t, tt, q, "SELECT", RunStatement)
		r := q.results[len(q.results)-1]
		if r.err != "" || r.view == nil {
			t.Fatalf("%s: error %q", sql, r.err)
		}
		testutil.WaitFor(t, tt, "the read-only reason", func() bool { return testutil.HasTextContaining(tt, "does more than read") })
		editCell(t, tt, cell, "edited")
		if n := r.view.PendingCount(); n != 0 {
			t.Fatalf("%d changes to the rows of %s", n, sql)
		}
		tt.Key(ui.Cmd, ui.KeyS)
		tt.Frame()
		if a.Confirm != nil || q.Busy() || ranTimes(a, sql) != 1 {
			t.Fatalf("%s ran again: %d runs, confirm %+v", sql, ranTimes(a, sql), a.Confirm)
		}
		if err := tt.Click("Refresh"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if a.Confirm == nil || ranTimes(a, sql) != 1 {
			t.Fatalf("Refresh ran %s without asking: %d runs", sql, ranTimes(a, sql))
		}
		a.Confirm.Open, a.Confirm = false, nil
	}

	t.Run("nextval", func(t *testing.T) {
		a := newFakeQueryHost(t)
		cn := a.AddConn(db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
		tt := ui.NewTester(a.view, 1200, 760)
		q := newEditor(t, a, tt, cn, "")
		testutil.WaitFor(t, tt, "the schemas", func() bool { return cn.DefaultSchema != "" })
		for _, s := range []string{"CREATE SEQUENCE seq", "CREATE TABLE n (id INTEGER PRIMARY KEY, label TEXT)", "INSERT INTO n VALUES (11, 'row 1'), (12, 'row 2')"} {
			if _, err := cn.DB.SQL.Exec(s); err != nil {
				t.Fatal(err)
			}
		}
		check(t, a, tt, q, "SELECT nextval('seq') AS v, * FROM n", "row 2")
	})

	t.Run("for update", func(t *testing.T) {
		testutil.Integration(t)
		testutil.SeedPostgres(t)
		a := newFakeQueryHost(t)
		cn := a.AddConn(testutil.PGConfig())
		tt := ui.NewTester(a.view, 1200, 760)
		q := newEditor(t, a, tt, cn, "")
		testutil.WaitFor(t, tt, "the schemas", func() bool { return cn.DefaultSchema != "" })
		for _, s := range []string{"DROP TABLE IF EXISTS shop.rv_lock", "CREATE TABLE shop.rv_lock (id INT PRIMARY KEY, v TEXT)",
			"INSERT INTO shop.rv_lock VALUES (1, 'alpha'), (2, 'beta')"} {
			if _, err := cn.DB.SQL.Exec(s); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() { q.sess.Close() })
		check(t, a, tt, q, "SELECT * FROM shop.rv_lock FOR UPDATE", "beta")
	})
}

// Changes applied inside the user's transaction go behind a savepoint: a
// failed batch rolls back to it, which leaves the transaction open with
// what ran before, as the editor still shows; Roll Back then undoes it.
func TestFailedApplyKeepsUserTransaction(t *testing.T) {
	try := func(t *testing.T, a *fakeQueryHost, tt *ui.Tester, q *Tab, committed func() string) {
		t.Helper()
		runWith(t, tt, q, "BEGIN", RunScript)
		if q.Tx != db.TxOpen {
			t.Fatalf("tx %v after BEGIN; messages %+v", q.Tx, q.messages)
		}
		waitEditable(t, tt)
		editCell(t, tt, "one", "uno")
		editCell(t, tt, "two", "three") // taken by row 3: the second UPDATE fails
		tt.Key(ui.Cmd, ui.KeyS)
		testutil.WaitFor(t, tt, "the review", func() bool { return a.Confirm != nil })
		a.Confirm.OnConfirm()
		a.Confirm = nil
		testutil.WaitFor(t, tt, "the apply", func() bool { return !q.Busy() })
		if len(a.Errors) == 0 {
			t.Fatal("the failed batch reported nothing")
		}
		if q.Tx != db.TxOpen || q.sess.Tx() != db.TxOpen || !testutil.HasTextContaining(tt, "Transaction open") {
			t.Fatalf("after the failed batch: tx %v, session tx %v, texts %q", q.Tx, q.sess.Tx(), tt.Texts())
		}
		if got := sessionLabels(t, q); got != "one,two,three,mine" {
			t.Fatalf("the editor's transaction holds %q", got)
		}
		if got := committed(); got != "0" {
			t.Fatalf("%s rows of the editor's transaction were committed", got)
		}
		q.FinishTx(false, func(error) {})
		testutil.WaitFor(t, tt, "the rollback", func() bool { return !q.Busy() && q.Tx == db.TxNone })
		if got := sessionLabels(t, q); got != "one,two,three" {
			t.Fatalf("after Roll Back: %q", got)
		}
	}
	const text = "BEGIN;\n\nINSERT INTO u VALUES (4, 'mine');\n\nSELECT * FROM u ORDER BY id;"

	t.Run("sqlite", func(t *testing.T) {
		a := newFakeQueryHost(t)
		file := filepath.Join(t.TempDir(), "u.sqlite")
		os.WriteFile(file, nil, 0o600)
		d, err := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: file}, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range []string{"CREATE TABLE u (id INTEGER PRIMARY KEY, label TEXT UNIQUE)", "INSERT INTO u VALUES (1, 'one'), (2, 'two'), (3, 'three')"} {
			if _, err := d.SQL.Exec(s); err != nil {
				t.Fatal(err)
			}
		}
		d.Close()
		cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
		tt := ui.NewTester(a.view, 1200, 760)
		q := newEditor(t, a, tt, cn, text)
		testutil.WaitFor(t, tt, "the schemas", func() bool { return cn.DefaultSchema != "" })
		try(t, a, tt, q, func() string { return sqliteValue(t, file, "SELECT count(*) FROM u WHERE label = 'mine'") })
	})

	t.Run("mysql", func(t *testing.T) {
		testutil.Integration(t)
		cfg := mysqlConfig()
		d, err := db.Open(context.Background(), cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() }) // after the DROP below: cleanups run last first
		for _, s := range []string{"DROP TABLE IF EXISTS shop.u", "CREATE TABLE shop.u (id INT PRIMARY KEY, label VARCHAR(20) UNIQUE) ENGINE=InnoDB",
			"INSERT INTO shop.u VALUES (1, 'one'), (2, 'two'), (3, 'three')"} {
			if _, err := d.SQL.Exec(s); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() { d.SQL.Exec("DROP TABLE IF EXISTS shop.u") })
		a := newFakeQueryHost(t)
		cn := a.AddConn(cfg)
		tt := ui.NewTester(a.view, 1200, 760)
		q := newEditor(t, a, tt, cn, text)
		t.Cleanup(func() { q.sess.Close() })
		testutil.WaitFor(t, tt, "the schemas", func() bool { return cn.DefaultSchema != "" })
		try(t, a, tt, q, func() string {
			var n string
			d.SQL.QueryRow("SELECT count(*) FROM shop.u WHERE label = 'mine'").Scan(&n)
			return n
		})
	})
}

// After the editor's connection is lost, the run that would go to a new
// connection is refused, saying so, and the switcher shows the schema the
// new connection finds names in, not the one the lost connection had.
func TestSchemaReReadAfterReset(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	for _, c := range []struct {
		cfg                db.Config
		set, before, after string
		kill               func(t *testing.T, q *Tab) string
	}{
		{testutil.PGConfig(), "SET search_path TO shop", "shop", "public", func(t *testing.T, q *Tab) string {
			return "SELECT pg_terminate_backend(" + sessionValue(t, q, "SELECT pg_backend_pid()") + ")"
		}},
		{mysqlConfig(), "USE information_schema", "information_schema", "shop", func(t *testing.T, q *Tab) string {
			return "KILL " + sessionValue(t, q, "SELECT CONNECTION_ID()")
		}},
	} {
		t.Run(string(c.cfg.Engine), func(t *testing.T) {
			a := newFakeQueryHost(t)
			cn := a.AddConn(c.cfg)
			tt := ui.NewTester(a.view, 1200, 760)
			q := newEditor(t, a, tt, cn, c.set+";\n\nSELECT 1 AS one;")
			t.Cleanup(func() { q.sess.Close() })
			testutil.WaitFor(t, tt, "the schemas", func() bool { return cn.DefaultSchema != "" })
			runWith(t, tt, q, c.set, RunStatement)
			if q.schema != c.before {
				t.Fatalf("schema %q after %s", q.schema, c.set)
			}
			if _, err := cn.DB.SQL.Exec(c.kill(t, q)); err != nil {
				t.Fatal(err)
			}
			time.Sleep(300 * time.Millisecond)
			refused := false
			for range 3 {
				runWith(t, tt, q, "SELECT 1", RunStatement)
				if r := q.results[len(q.results)-1]; strings.Contains(r.err, "made again") {
					refused = true
					break
				}
			}
			if !refused {
				t.Fatalf("no run said the connection was made again: %+v", q.messages)
			}
			testutil.WaitFor(t, tt, "the new connection's schema", func() bool { return q.schema == c.after })
			if !slices.Contains(tt.Texts(), c.after) {
				t.Fatalf("the switcher does not show %s: %q", c.after, tt.Texts())
			}

			// A result read again on the session is refused the same way,
			// and the switcher follows too.
			runWith(t, tt, q, c.set, RunStatement)
			runWith(t, tt, q, "SELECT 1", RunStatement)
			if q.schema != c.before || q.results[0].view == nil {
				t.Fatalf("schema %q, results %+v", q.schema, q.results)
			}
			if _, err := cn.DB.SQL.Exec(c.kill(t, q)); err != nil {
				t.Fatal(err)
			}
			time.Sleep(300 * time.Millisecond)
			refused = false
			for range 3 {
				if err := tt.Click("Refresh"); err != nil {
					t.Fatal(err)
				}
				testutil.WaitFor(t, tt, "the read", func() bool { return !q.Busy() })
				if testutil.HasTextContaining(tt, "made again") {
					refused = true
					break
				}
			}
			if !refused {
				t.Fatalf("no read said the connection was made again: %q", tt.Texts())
			}
			testutil.WaitFor(t, tt, "the new connection's schema", func() bool { return q.schema == c.after })
		})
	}
}

// With a transaction open, a MySQL statement that commits it implicitly,
// as CREATE INDEX does, asks first, saying Roll Back will not undo what
// ran before it.
func TestEndsTransactionAsksInEditor(t *testing.T) {
	testutil.Integration(t)
	cfg := mysqlConfig()
	d, err := db.Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() }) // after the DROP below: cleanups run last first
	for _, s := range []string{"DROP TABLE IF EXISTS shop.rv_implicit", "CREATE TABLE shop.rv_implicit (id INT PRIMARY KEY, v INT) ENGINE=InnoDB"} {
		if _, err := d.SQL.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { d.SQL.Exec("DROP TABLE IF EXISTS shop.rv_implicit") })
	a := newFakeQueryHost(t)
	cn := a.AddConn(cfg)
	tt := ui.NewTester(a.view, 1200, 760)
	q := newEditor(t, a, tt, cn, "CREATE INDEX rv_implicit_v ON rv_implicit (v);\n\nBEGIN;\n\nINSERT INTO rv_implicit VALUES (1, 1);")
	t.Cleanup(func() { q.sess.Close() })
	runWith(t, tt, q, "CREATE", RunStatement)
	if a.Confirm != nil || len(q.results) != 1 || q.results[0].err != "" {
		t.Fatalf("without a transaction: confirm %+v, results %+v", a.Confirm, q.results)
	}
	runWith(t, tt, q, "BEGIN", RunStatement)
	runWith(t, tt, q, "INSERT", RunStatement)
	if q.Tx != db.TxOpen {
		t.Fatalf("tx %v; messages %+v", q.Tx, q.messages)
	}
	testutil.SetCaret(tt, &q.Editor, strings.Index(q.Editor.Text, "CREATE"))
	q.Run(RunStatement)
	tt.Frame()
	if a.Confirm == nil || !slices.ContainsFunc(a.Confirm.Reasons, func(r string) bool { return strings.Contains(r, "commits the open transaction") }) {
		t.Fatalf("no warning: %+v", a.Confirm)
	}
	a.Confirm.Open, a.Confirm = false, nil
	tt.Frame()
	if q.Running || q.Tx != db.TxOpen {
		t.Fatalf("ran without agreeing: running %v, tx %v", q.Running, q.Tx)
	}
}

// On a connection every session shares (DuckDB), a tab's statements run
// inside the transaction another tab began: its bar says whose, its
// Commit refuses, and it holds no transaction of its own, so closing or
// quitting leaves the transaction to its owner. The owner's end clears it.
func TestSharedTransactionInsideTab(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1200, 760)
	owner := newEditor(t, a, tt, cn, "CREATE TABLE t (a INT);\n\nBEGIN;\n\nINSERT INTO t VALUES (1);")
	runWith(t, tt, owner, "CREATE", RunScript)
	if owner.Tx != db.TxOpen {
		t.Fatalf("the owner's tx %v; messages %+v", owner.Tx, owner.messages)
	}
	other := newEditor(t, a, tt, cn, "SELECT count(*) AS n FROM t")
	runWith(t, tt, other, "SELECT", RunStatement)
	if other.OpenTx() || other.InsideTx != db.TxOpen || other.CloseReason() != "" {
		t.Fatalf("the other tab: own %v, inside %v, close reason %q", other.Tx, other.InsideTx, other.CloseReason())
	}
	if !testutil.HasTextContaining(tt, "Inside another session's transaction") {
		t.Fatalf("no bar: %q", tt.Texts())
	}
	other.SetTxOwner("the owner") // as the app names it
	tt.Frame()
	if !testutil.HasTextContaining(tt, "Inside the owner's transaction") {
		t.Fatalf("the bar does not name the owner: %q", tt.Texts())
	}
	if err := tt.Click("Commit"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.Errors) != 1 || !strings.Contains(a.Errors[0], "the owner's") || other.Running {
		t.Fatalf("errors %q, running %v", a.Errors, other.Running)
	}
	ended := false
	other.FinishTx(false, func(err error) { ended = err == nil })
	tt.Frame()
	if !ended || other.Running || owner.sess.Tx() != db.TxOpen {
		t.Fatalf("the other tab's FinishTx: ended %v, running %v, session tx %v", ended, other.Running, owner.sess.Tx())
	}
	owner.FinishTx(true, func(error) {})
	testutil.WaitFor(t, tt, "the owner's commit", func() bool { return owner.Tx == db.TxNone && !owner.Busy() })
	other.SetTxOwner("") // as the app does once no tab holds it
	tt.Frame()
	if other.InsideTx != db.TxNone || testutil.HasTextContaining(tt, "Inside ") {
		t.Fatalf("the bar stays: %q", tt.Texts())
	}
	var n int
	if err := cn.DB.SQL.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil || n != 1 {
		t.Fatalf("committed rows %d, %v", n, err)
	}
}

// A connection lost in the middle of an apply takes the batch's
// transaction with it: no undo runs, which is no second failure, and the
// switcher shows where the new connection finds names.
func TestApplyLostConnectionResetsSchema(t *testing.T) {
	testutil.Integration(t)
	cfg := mysqlConfig()
	d, err := db.Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() }) // after the DROP below: cleanups run last first
	for _, s := range []string{"DROP TABLE IF EXISTS shop.rv_lost", "CREATE TABLE shop.rv_lost (id INT PRIMARY KEY, v VARCHAR(20)) ENGINE=InnoDB",
		"INSERT INTO shop.rv_lost VALUES (1, 'alpha'), (2, 'beta')"} {
		if _, err := d.SQL.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { d.SQL.Exec("DROP TABLE IF EXISTS shop.rv_lost") })
	a := newFakeQueryHost(t)
	cn := a.AddConn(cfg)
	tt := ui.NewTester(a.view, 1200, 760)
	q := newEditor(t, a, tt, cn, "USE information_schema;\n\nSELECT * FROM shop.rv_lost ORDER BY id;")
	t.Cleanup(func() { q.sess.Close() })
	testutil.WaitFor(t, tt, "the schemas", func() bool { return cn.DefaultSchema != "" })
	runWith(t, tt, q, "USE", RunScript)
	if q.schema != "information_schema" {
		t.Fatalf("schema %q", q.schema)
	}
	waitEditable(t, tt)
	kill := "KILL " + sessionValue(t, q, "SELECT CONNECTION_ID()")
	editCell(t, tt, "alpha", "one")
	editCell(t, tt, "beta", "two")
	var once sync.Once
	a.afterRun = func(kind string) {
		if kind == audit.KindEdit {
			once.Do(func() {
				d.SQL.Exec(kill)
				time.Sleep(300 * time.Millisecond)
			})
		}
	}
	tt.Key(ui.Cmd, ui.KeyS)
	testutil.WaitFor(t, tt, "the review", func() bool { return a.Confirm != nil })
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "the apply", func() bool { return !q.Busy() })
	if len(a.Errors) != 1 || strings.Contains(a.Errors[0], "rollback failed too") || !strings.Contains(a.Errors[0], "lost") {
		t.Fatalf("errors %q", a.Errors)
	}
	testutil.WaitFor(t, tt, "the new connection's schema", func() bool { return q.schema == "shop" })
	var v string
	d.SQL.QueryRow("SELECT v FROM shop.rv_lost WHERE id = 1").Scan(&v)
	if v != "alpha" {
		t.Fatalf("row 1 holds %q", v)
	}
}

// The large-change hold reads a MySQL table's engine before its BEGIN: on
// a connection made again, that read is refused, and so the change is,
// rather than run where the new connection finds names.
func TestLargeChangeHoldKeepsRefusal(t *testing.T) {
	testutil.Integration(t)
	cfg := mysqlConfig()
	cfg.Env = db.Staging // auto-commit, changes held
	d, err := db.Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() }) // after the DROP below: cleanups run last first
	for _, s := range []string{"DROP DATABASE IF EXISTS rvguard", "CREATE DATABASE rvguard",
		"CREATE TABLE rvguard.rv_guard (id INT PRIMARY KEY, v VARCHAR(20)) ENGINE=InnoDB", "INSERT INTO rvguard.rv_guard VALUES (1, 'kept')",
		"DROP TABLE IF EXISTS shop.rv_guard", "CREATE TABLE shop.rv_guard (id INT PRIMARY KEY, v VARCHAR(20)) ENGINE=InnoDB", "INSERT INTO shop.rv_guard VALUES (1, 'kept')"} {
		if _, err := d.SQL.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		d.SQL.Exec("DROP DATABASE IF EXISTS rvguard")
		d.SQL.Exec("DROP TABLE IF EXISTS shop.rv_guard")
	})
	a := newFakeQueryHost(t)
	cn := a.AddConn(cfg)
	tt := ui.NewTester(a.view, 1200, 760)
	q := newEditor(t, a, tt, cn, "USE rvguard;\n\nUPDATE rv_guard SET v = 'changed' WHERE id = 1;\n\nSELECT 1;")
	t.Cleanup(func() { q.sess.Close() })
	runWith(t, tt, q, "USE", RunStatement)
	if _, err := d.SQL.Exec("KILL " + sessionValue(t, q, "SELECT CONNECTION_ID()")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	runWith(t, tt, q, "SELECT 1", RunStatement)
	if r := q.results[len(q.results)-1]; r.err == "" || strings.Contains(r.err, "made again") {
		t.Fatalf("the first statement after the loss: %q", r.err)
	}
	runWith(t, tt, q, "UPDATE", RunStatement)
	if r := q.results[len(q.results)-1]; !strings.Contains(r.err, "made again") {
		t.Fatalf("the change after the loss: error %q", r.err)
	}
	for _, table := range []string{"rvguard.rv_guard", "shop.rv_guard"} {
		var v string
		d.SQL.QueryRow("SELECT v FROM " + table + " WHERE id = 1").Scan(&v)
		if v != "kept" {
			t.Errorf("%s holds %q", table, v)
		}
	}
}

// On a connection every session shares (DuckDB), a tab inside another
// tab's transaction is refused a typed COMMIT, END, ROLLBACK or ABORT, as
// its buttons are: the whole run, naming the owner, with nothing of it
// run. A tab that has run nothing since the transaction began is refused
// too.
func TestInsideTabRefusesCommit(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1200, 760)
	owner := newEditor(t, a, tt, cn, "CREATE TABLE t (a INT);\n\nBEGIN;\n\nINSERT INTO t VALUES (1);")
	runWith(t, tt, owner, "CREATE", RunScript)
	if owner.Tx != db.TxOpen {
		t.Fatalf("the owner's tx %v; messages %+v", owner.Tx, owner.messages)
	}
	fresh := newEditor(t, a, tt, cn, "COMMIT")
	other := newEditor(t, a, tt, cn, "SELECT count(*) AS n FROM t")
	runWith(t, tt, other, "SELECT", RunStatement)
	other.SetTxOwner("the owner") // as the app names it
	// A BEGIN there would fail, and on DuckDB abort the owner's transaction.
	for _, end := range []string{"COMMIT", "commit work", "COMMIT TRANSACTION", "END", "END TRANSACTION", "ROLLBACK", "ROLLBACK TRANSACTION", "ABORT", "BEGIN", "BEGIN TRANSACTION", "START TRANSACTION"} {
		a.Errors = nil
		other.Editor.Text = "INSERT INTO t VALUES (2);\n\n" + end + ";"
		runWith(t, tt, other, "INSERT", RunScript)
		if len(a.Errors) != 1 || !strings.Contains(a.Errors[0], "the owner's") || !strings.Contains(a.Errors[0], "commit or roll it back there") {
			t.Fatalf("%s: errors %q", end, a.Errors)
		}
	}
	if got := sessionValue(t, owner, "SELECT count(*) FROM t"); got != "1" || owner.sess.Tx() != db.TxOpen || !owner.sess.OwnsTx() {
		t.Fatalf("the owner's transaction: %s rows, tx %v, owned %v", got, owner.sess.Tx(), owner.sess.OwnsTx())
	}
	a.Errors = nil
	runWith(t, tt, fresh, "COMMIT", RunStatement)
	if len(a.Errors) != 1 || !strings.Contains(a.Errors[0], "another session's") || owner.sess.Tx() != db.TxOpen {
		t.Fatalf("the tab that ran nothing: errors %q, the owner's tx %v", a.Errors, owner.sess.Tx())
	}
	owner.FinishTx(false, func(error) {})
	testutil.WaitFor(t, tt, "the owner's rollback", func() bool { return owner.Tx == db.TxNone && !owner.Busy() })
	if n := sessionValue(t, owner, "SELECT count(*) FROM t"); n != "0" {
		t.Fatalf("%s rows after the rollback", n)
	}
}

// A run that began with no transaction open stops before a statement that
// would end or begin one, once another session began one meanwhile on the
// connection every session shares: the refusal before the run is checked
// again before each such statement.
func TestInsideTxBegunMidRunStopsRun(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1200, 760)
	q := newEditor(t, a, tt, cn, "")
	testutil.WaitFor(t, tt, "the schemas", func() bool { return len(cn.Loading) == 0 })
	ctx := context.Background()
	other, err := cn.DB.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	if _, err := other.Exec(ctx, "CREATE TABLE t (a INT)"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ end, says string }{
		{"ROLLBACK", "not with ROLLBACK here"},
		{"BEGIN", "BEGIN here cannot begin another"},
	} {
		a.Errors, a.Events = nil, nil
		// The first SELECT holds the one connection long enough for the
		// other session's BEGIN to wait for it, and run before the second.
		q.Editor.Text = "SELECT sum(x) FROM range(1000000000) r(x);\n\nSELECT 1;\n\n" + c.end + ";"
		testutil.SetCaret(tt, &q.Editor, 0)
		q.Run(RunScript)
		for _, s := range []string{"BEGIN", "INSERT INTO t VALUES (1)"} {
			if _, err := other.Exec(ctx, s); err != nil {
				t.Fatal(err)
			}
		}
		testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running })
		if ranTimes(a, c.end) != 0 || len(a.Errors) != 1 || !strings.Contains(a.Errors[0], c.says) {
			t.Fatalf("%s: ran %d times, errors %q; messages %+v", c.end, ranTimes(a, c.end), a.Errors, q.messages)
		}
		if !slices.ContainsFunc(a.Events, func(e audit.Event) bool { return e.Kind == audit.KindBlocked && strings.Contains(e.Statement, c.end) }) {
			t.Fatalf("%s: not audited as refused: %+v", c.end, a.Events)
		}
		if got := sessionValue(t, q, "SELECT count(*) FROM t"); got != "1" || other.Tx() != db.TxOpen || !other.OwnsTx() || q.InsideTx != db.TxOpen {
			t.Fatalf("%s: the other session's transaction: %s rows, tx %v, owned %v; the tab inside %v", c.end, got, other.Tx(), other.OwnsTx(), q.InsideTx)
		}
		if _, err := other.Exec(ctx, "ROLLBACK"); err != nil {
			t.Fatal(err)
		}
		if got := sessionValue(t, q, "SELECT count(*) FROM t"); got != "0" {
			t.Fatalf("%s: %s rows after the other session's rollback", c.end, got)
		}
		tt.Frame()
	}
}

// Work that stopped on an internal error before its end read the
// transaction leaves it unread: the reset reads it again, so that one the
// run opened still shows and closing still asks, and a stopped Commit
// tells what waited for it that it failed.
func TestStoppedWorkReadsTxAgain(t *testing.T) {
	file := filepath.Join(t.TempDir(), "t.sqlite")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file, Commit: db.CommitManual})
	tt := ui.NewTester(a.view, 1200, 760)
	q := newEditor(t, a, tt, cn, "CREATE TABLE t (a INT);\n\nINSERT INTO t VALUES (1);")
	runWith(t, tt, q, "CREATE", RunScript)
	if q.Tx != db.TxOpen {
		t.Fatalf("tx %v; messages %+v", q.Tx, q.messages)
	}
	// As a run that stopped on a panic leaves the tab.
	q.Running = true
	q.setTx(db.TxNone, db.TxNone)
	q.runStopped()
	testutil.WaitFor(t, tt, "the transaction read again", func() bool { return q.Tx == db.TxOpen })
	if q.Running || q.CloseReason() == "" {
		t.Fatalf("after the stopped run: running %v, close reason %q", q.Running, q.CloseReason())
	}
	var told error
	q.Running = true
	q.Times().Then = func(err error) { told = err }
	q.setTx(db.TxNone, db.TxNone)
	q.endTxStopped()
	if !errors.Is(told, dataview.ErrTxEndStopped) || q.Times().Then != nil || q.Running {
		t.Fatalf("after the stopped Commit: told %v, then kept %v, running %v", told, q.Times().Then != nil, q.Running)
	}
	testutil.WaitFor(t, tt, "the transaction read again", func() bool { return q.Tx == db.TxOpen })
	q.endTx(false)
	testutil.WaitFor(t, tt, "the rollback", func() bool { return !q.Running && q.Tx == db.TxNone })
}

// An owner that has not noticed another session end its transaction, or
// begin the one open now, sends nothing on Commit or Roll Back: it says
// so, takes the transaction as it is, and audits no COMMIT or ROLLBACK.
func TestStaleOwnerCannotEnd(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1200, 760)
	owner := newEditor(t, a, tt, cn, "CREATE TABLE t (a INT);\n\nBEGIN;\n\nINSERT INTO t VALUES (1);")
	runWith(t, tt, owner, "CREATE", RunScript)
	if owner.Tx != db.TxOpen {
		t.Fatalf("the owner's tx %v; messages %+v", owner.Tx, owner.messages)
	}
	// Another session, as a tab that typed them before it was refused,
	// commits the transaction and begins its own.
	ctx := context.Background()
	other, err := cn.DB.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	for _, s := range []string{"COMMIT", "BEGIN", "INSERT INTO t VALUES (7)"} {
		if _, err := other.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	ended := func(commit bool, note string) {
		t.Helper()
		before := len(a.Events)
		owner.endOpenTx(commit)
		testutil.WaitFor(t, tt, "the owner's end", func() bool { return !owner.Busy() })
		for _, e := range a.Events[before:] {
			if e.Statement == "COMMIT" || e.Statement == "ROLLBACK" {
				t.Fatalf("audited %s, which was never sent: %+v", e.Statement, e)
			}
		}
		if m := owner.messages[len(owner.messages)-1]; !m.err || !strings.Contains(m.text, note) {
			t.Fatalf("the owner's note: %+v", m)
		}
	}
	ended(false, "another tab's")
	if owner.Tx != db.TxNone || owner.InsideTx != db.TxOpen || other.Tx() != db.TxOpen || !other.OwnsTx() {
		t.Fatalf("after the stale Roll Back: own %v, inside %v; the other's tx %v, owned %v", owner.Tx, owner.InsideTx, other.Tx(), other.OwnsTx())
	}
	if _, err := other.Exec(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	if n := sessionValue(t, owner, "SELECT count(*) FROM t"); n != "2" {
		t.Fatalf("%s rows: the other session's row 7 was not kept", n)
	}

	runWith(t, tt, owner, "BEGIN", RunStatement)
	if owner.Tx != db.TxOpen {
		t.Fatalf("the owner's tx %v after BEGIN; messages %+v", owner.Tx, owner.messages)
	}
	if _, err := other.Exec(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	ended(true, "already committed or rolled back elsewhere")
	if owner.Tx != db.TxNone || owner.InsideTx != db.TxNone {
		t.Fatalf("after the stale Commit: own %v, inside %v", owner.Tx, owner.InsideTx)
	}
}

// mysqlTable makes a table of the MySQL test server's shop database for a
// test, dropped once it ends, and returns the server's pool, which sees
// only what is committed.
func mysqlTable(t *testing.T, cfg db.Config, name string) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"DROP TABLE IF EXISTS shop." + name, "CREATE TABLE shop." + name + " (id INT PRIMARY KEY, v INT) ENGINE=InnoDB"} {
		if _, err := d.SQL.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		d.SQL.Exec("DROP TABLE IF EXISTS shop." + name)
		d.Close()
	})
	return d
}

// committedIDs lists the ids of a shop table that another connection sees.
func committedIDs(t *testing.T, d *db.DB, table string) string {
	t.Helper()
	rows, err := d.SQL.Query("SELECT id FROM shop." + table + " ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return strings.Join(ids, ",")
}

// Under manual commit every write runs in a transaction. A MySQL statement
// that commits the writes before it implicitly, as CREATE INDEX does, asks
// first; after it, the app opens a transaction again before the next
// write, and says so: Roll Back then undoes only what came after it.
func TestManualCommitReopensAfterImplicitCommit(t *testing.T) {
	testutil.Integration(t)
	cfg := mysqlConfig()
	cfg.Commit = db.CommitManual
	d := mysqlTable(t, cfg, "rv_reopen")
	a := newFakeQueryHost(t)
	cn := a.AddConn(cfg)
	tt := ui.NewTester(a.view, 1200, 760)
	q := newEditor(t, a, tt, cn, "INSERT INTO rv_reopen VALUES (1, 1);\nCREATE INDEX rv_reopen_v ON rv_reopen (v);\nINSERT INTO rv_reopen VALUES (2, 2);")
	t.Cleanup(func() { q.sess.Close() })
	runWith(t, tt, q, "INSERT", RunScript)
	if a.Confirm == nil || !slices.ContainsFunc(a.Confirm.Reasons, func(r string) bool { return strings.Contains(r, "CREATE commits the writes before it") }) {
		t.Fatalf("no warning: %+v", a.Confirm)
	}
	a.Confirm.Open = false
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running })
	for _, r := range q.results {
		if r.err != "" {
			t.Fatalf("%s: %s", r.sql, r.err)
		}
	}
	if q.Tx != db.TxOpen || ranTimes(a, "BEGIN -- manual commit") != 2 {
		t.Fatalf("tx %v, %d BEGINs; messages %+v", q.Tx, ranTimes(a, "BEGIN -- manual commit"), q.messages)
	}
	if !slices.ContainsFunc(q.messages, func(m message) bool {
		return strings.Contains(m.text, "CREATE committed the transaction") && strings.Contains(m.text, "A new one opens before the next write")
	}) {
		t.Fatalf("no note of the implicit commit: %+v", q.messages)
	}
	if got := committedIDs(t, d, "rv_reopen"); got != "1" {
		t.Fatalf("another connection sees %q: row 2 must wait for Commit", got)
	}
	q.endTx(false)
	testutil.WaitFor(t, tt, "the rollback", func() bool { return !q.Running })
	if got := committedIDs(t, d, "rv_reopen"); q.Tx != db.TxNone || got != "1" {
		t.Fatalf("after Roll Back: tx %v, rows %q", q.Tx, got)
	}
}

// Under auto-commit too, a typed BEGIN holds the writes after it: a MySQL
// statement that commits them implicitly, as CREATE INDEX does, asks
// first, for the ROLLBACK after it would not undo them.
func TestTypedBeginAsksImplicitCommit(t *testing.T) {
	testutil.Integration(t)
	cfg := mysqlConfig() // development, auto-commit
	d := mysqlTable(t, cfg, "rv_begun")
	a := newFakeQueryHost(t)
	cn := a.AddConn(cfg)
	tt := ui.NewTester(a.view, 1200, 760)
	q := newEditor(t, a, tt, cn, "BEGIN;\nINSERT INTO rv_begun VALUES (1, 1);\nCREATE INDEX rv_begun_v ON rv_begun (v);\nROLLBACK;")
	t.Cleanup(func() {
		if q.sess != nil {
			q.sess.Close()
		}
	})
	runWith(t, tt, q, "BEGIN", RunScript)
	if a.Confirm == nil || !slices.ContainsFunc(a.Confirm.Reasons, func(r string) bool { return strings.Contains(r, "CREATE commits the writes since BEGIN") }) {
		t.Fatalf("no warning: %+v; messages %+v", a.Confirm, q.messages)
	}
	a.Confirm.Open = false
	a.Confirm = nil
	tt.Frame()
	if q.Running || ranTimes(a, "BEGIN") != 0 || committedIDs(t, d, "rv_begun") != "" {
		t.Fatalf("ran without an answer: running %v, BEGIN ran %d times", q.Running, ranTimes(a, "BEGIN"))
	}
}

// On production a script asks before each write, and Run All answers for
// the plain writes after it; not for a statement that commits the open
// transaction, which asks still, saying so.
func TestProductionRunAllAsksImplicitCommit(t *testing.T) {
	testutil.Integration(t)
	cfg := mysqlConfig()
	cfg.Env = db.Production // manual commit
	d := mysqlTable(t, cfg, "rv_runall")
	a := newFakeQueryHost(t)
	cn := a.AddConn(cfg)
	tt := ui.NewTester(a.view, 1200, 760)
	q := newEditor(t, a, tt, cn, "INSERT INTO rv_runall VALUES (1, 1);\nINSERT INTO rv_runall VALUES (2, 2);\nCREATE INDEX rv_runall_v ON rv_runall (v);\nINSERT INTO rv_runall VALUES (3, 3);")
	t.Cleanup(func() { q.sess.Close() })
	testutil.SetCaret(tt, &q.Editor, 0)
	q.Run(RunScript)
	testutil.WaitFor(t, tt, "the first question", func() bool { return a.Confirm != nil && strings.Contains(a.Confirm.Preview, "(1, 1)") })
	r := a.Confirm
	r.Open, r.Answered, a.Confirm = false, true, nil
	if r.OnRunAll == nil {
		t.Fatal("no Run All")
	}
	r.OnRunAll()
	testutil.WaitFor(t, tt, "the CREATE's question or the end", func() bool { return a.Confirm != nil || !q.Running })
	r = a.Confirm
	if r == nil || !strings.Contains(r.Preview, "CREATE INDEX") || !slices.ContainsFunc(r.Reasons, func(s string) bool { return strings.Contains(s, "commits the open transaction") }) {
		t.Fatalf("the CREATE did not ask after Run All: %+v; messages %+v", r, q.messages)
	}
	r.Open, r.Answered, a.Confirm = false, true, nil
	r.OnConfirm()
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running })
	if a.Confirm != nil || q.Tx != db.TxOpen {
		t.Fatalf("the INSERT after it: asked %+v, tx %v", a.Confirm, q.Tx)
	}
	if got := committedIDs(t, d, "rv_runall"); got != "1,2" {
		t.Fatalf("another connection sees %q", got)
	}
	q.endTx(false)
	testutil.WaitFor(t, tt, "the rollback", func() bool { return !q.Running })
	if got := committedIDs(t, d, "rv_runall"); got != "1,2" {
		t.Fatalf("after Roll Back: %q", got)
	}
}

// MySQL's autocommit off holds nothing open until a statement reads or
// writes a table: the bar says autocommit is off, and closing asks
// nothing; once a table is read, the transaction is open, and shown.
func TestAutocommitOffNote(t *testing.T) {
	testutil.Integration(t)
	cfg := mysqlConfig()
	mysqlTable(t, cfg, "rv_acoff")
	a := newFakeQueryHost(t)
	cn := a.AddConn(cfg)
	tt := ui.NewTester(a.view, 1200, 760)
	q := newEditor(t, a, tt, cn, "SET autocommit = 0;\nCOMMIT;\nSELECT 1;")
	t.Cleanup(func() { q.sess.Close() })
	runWith(t, tt, q, "SET", RunScript)
	tt.Frame()
	if q.Tx != db.TxNone || q.CloseReason() != "" || testutil.HasTextContaining(tt, "Transaction open") || !testutil.HasTextContaining(tt, "Autocommit is off") {
		t.Fatalf("tx %v, close reason %q; texts %q", q.Tx, q.CloseReason(), tt.Texts())
	}
	q.Editor.Text = "SELECT * FROM rv_acoff"
	runWith(t, tt, q, "SELECT", RunStatement)
	tt.Frame()
	if q.Tx != db.TxOpen || !testutil.HasTextContaining(tt, "Transaction open") || testutil.HasTextContaining(tt, "Autocommit is off") {
		t.Fatalf("after reading a table: tx %v; texts %q", q.Tx, tt.Texts())
	}
	q.endTx(true)
	testutil.WaitFor(t, tt, "the commit", func() bool { return !q.Running })
	tt.Frame()
	if q.Tx != db.TxNone || !testutil.HasTextContaining(tt, "Autocommit is off") {
		t.Fatalf("after the commit: tx %v; texts %q", q.Tx, tt.Texts())
	}
}
