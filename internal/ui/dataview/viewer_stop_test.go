package dataview

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// Stop ends Fetch All between pages: the rows read stay, more remain, and
// Fetch All goes on from there.
func TestFetchAllStop(t *testing.T) {
	a, tt, v := openSlowView(t)
	a.Settings().PageSize = 100
	v.FetchAll()
	v.fetchStarted = time.Now().Add(-time.Second)
	tt.Frame()
	if err := tt.Click("Stop"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	testutil.WaitFor(t, tt, "the page on its way", func() bool { return !v.loading })
	tt.Frame()
	read := len(v.src.Rows)
	if v.fetchingAll || v.done || read >= db.MaxRows || v.cursor == nil {
		t.Fatalf("fetching %v, done %v, %d rows", v.fetchingAll, v.done, read)
	}
	if err := tt.Click("Fetch All"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "more rows", func() bool { return len(v.src.Rows) > read })
	v.stopFetchAll()
}

// openSlowView opens a view of millions of rows made as they are read,
// whose count takes seconds.
func openSlowView(t *testing.T) (*FakeHost, *ui.Tester, *Viewer) {
	t.Helper()
	a, tt, tb, _ := openSQLite(t, 0, db.Object{Schema: "main", Name: "slow", Kind: db.KindView, Rows: -1}, func(d *db.DB) {
		if _, err := d.SQL.Exec("CREATE VIEW slow AS WITH RECURSIVE g(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM g WHERE x < 50000000) SELECT x FROM g"); err != nil {
			t.Fatal(err)
		}
	})
	return a, tt, tb.view
}

// Stop ends a count on the server: no count, and no error to report.
func TestCountStop(t *testing.T) {
	a, tt, v := openSlowView(t)
	v.countRows()
	v.countStarted = time.Now().Add(-time.Second)
	tt.Frame()
	if err := tt.Click("Stop"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	start := time.Now()
	testutil.WaitFor(t, tt, "the count to stop", func() bool { return !v.counting })
	if time.Since(start) > 3*time.Second || v.count >= 0 || len(a.Errors) != 0 {
		t.Fatalf("after %v: count %d, errors %q", time.Since(start), v.count, a.Errors)
	}
	if !tt.HasText("Count") {
		t.Fatal("Count is not offered again")
	}
}

// On DuckDB's one connection, stopping a count would abort the
// transaction open on it: Stop says so instead.
func TestCountStopRefusedInSharedTx(t *testing.T) {
	a := NewFakeHost(t)
	cn := a.AddConn(db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:", Commit: db.CommitManual})
	tt := ui.NewTester(a.View, 1200, 700)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	for _, s := range []string{"CREATE TABLE n (id INTEGER PRIMARY KEY, label TEXT)", "INSERT INTO n VALUES (1, 'one')"} {
		if _, err := cn.DB.SQL.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	tb := NewTableTab(a, cn, "", db.Object{Schema: "main", Name: "n", Kind: db.KindTable, Rows: -1}, PageData)
	a.AddTab(tb)
	testutil.WaitFor(t, tt, "rows", func() bool { return tb.view.src.Rows != nil && !tb.view.loading && tb.sess != nil })
	if why := tb.view.countStopRefusal(); why != "" {
		t.Fatalf("refused with no transaction: %q", why)
	}
	tb.view.grid.setValue(&tb.view.src, 0, 1, db.Typed("uno"))
	stmts, shown, err := tb.view.changeStatements()
	if err != nil {
		t.Fatal(err)
	}
	tb.view.apply(stmts, shown) // opens the tab's transaction (manual commit)
	testutil.WaitFor(t, tt, "the apply", func() bool { return !tb.view.applying && !tb.view.loading })
	if why := tb.view.countStopRefusal(); why == "" {
		t.Fatal("Stop offered with a transaction open on DuckDB's one connection")
	}
}

// applyAt sets the apply's test step for the test's length.
func applyAt(t *testing.T, step func(stage string, i int)) {
	t.Helper()
	applyStep = step
	t.Cleanup(func() { applyStep = nil })
}

// editLabels changes the labels of the first rows, and applies.
func editLabels(t *testing.T, v *Viewer, labels ...string) {
	t.Helper()
	for i, l := range labels {
		v.grid.setValue(&v.src, i, 1, db.Typed(l))
	}
	stmts, shown, err := v.changeStatements()
	if err != nil {
		t.Fatal(err)
	}
	v.apply(stmts, shown)
}

// An owner closing as the last change went, before COMMIT: the changes
// roll back, rather than stay in a transaction left open while the
// apply says they were not applied.
func TestApplyCancelBeforeCommitRollsBack(t *testing.T) {
	a, tt, tb, _ := openNumbers(t, 3, 100)
	v := tb.view
	applyAt(t, func(stage string, _ int) {
		if stage == "finish" {
			v.applyCancel()
		}
	})
	editLabels(t, v, "edited")
	testutil.WaitFor(t, tt, "the apply", func() bool { return !v.applying })
	if tb.sess.Tx() != db.TxNone {
		t.Fatalf("a transaction stays open: %v; errors %q", tb.sess.Tx(), a.Errors)
	}
	if len(a.Errors) != 1 || !strings.Contains(a.Errors[0], "not applied") {
		t.Fatalf("errors %q", a.Errors)
	}
}

// Stop takes effect before the next change: what ran rolls back, the
// changes stay pending, and a transaction the user had open stays, with
// what it held.
func TestApplyStopBetweenStatements(t *testing.T) {
	a := NewFakeHost(t)
	file := filepath.Join(t.TempDir(), "n.sqlite")
	os.WriteFile(file, nil, 0o600)
	setup, _ := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: file}, nil)
	setup.SQL.Exec("CREATE TABLE n (id INTEGER PRIMARY KEY, label TEXT)")
	setup.SQL.Exec("INSERT INTO n VALUES (1, 'one'), (2, 'two'), (3, 'three')")
	setup.Close()
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file, Commit: db.CommitManual})
	tt := ui.NewTester(a.View, 1200, 700)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	tb := NewTableTab(a, cn, "", db.Object{Schema: "main", Name: "n", Kind: db.KindTable, Rows: -1}, PageData)
	a.AddTab(tb)
	v := tb.view
	testutil.WaitFor(t, tt, "rows", func() bool { return v.src.Rows != nil && !v.loading && tb.columns != nil })
	editLabels(t, v, "uno") // opens the user's transaction (manual commit)
	testutil.WaitFor(t, tt, "the first apply", func() bool { return !v.applying && !v.loading && v.grid.edits.count() == 0 })
	if tb.Tx != db.TxOpen {
		t.Fatalf("tx %v, errors %q", tb.Tx, a.Errors)
	}
	applyAt(t, func(stage string, i int) {
		if stage == "statement" && i == 1 {
			v.stopApply()
		}
	})
	editLabels(t, v, "uno", "dos", "tres")
	testutil.WaitFor(t, tt, "the apply", func() bool { return !v.applying })
	if tb.Tx != db.TxOpen || len(a.Errors) != 0 || v.grid.edits.count() != 2 {
		t.Fatalf("tx %v, errors %q, %d pending", tb.Tx, a.Errors, v.grid.edits.count())
	}
	if len(a.Toasts) == 0 || !strings.Contains(a.Toasts[len(a.Toasts)-1], "none of the 2 changes were applied") {
		t.Fatalf("toasts %q", a.Toasts)
	}
	c, err := tb.sess.Query(context.Background(), "SELECT label FROM n ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := c.Fetch(10)
	c.Close()
	if got := fmt.Sprint(rows); got != "[[uno] [two] [three]]" {
		t.Fatalf("in the user's transaction: %s", got)
	}
	if !slices.ContainsFunc(a.JobTitles, func(s string) bool { return strings.Contains(s, "Applying 2 changes") }) {
		t.Fatalf("jobs %q", a.JobTitles)
	}
}

// On PostgreSQL, in the apply's own transaction, Stop between changes
// leaves the table as it was and no transaction open.
func TestApplyStopRollsBackOnPostgres(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := NewFakeHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.View, 1200, 700)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	a.OpenTable(cn, "", db.Object{Schema: "shop", Name: "customers", Kind: db.KindTable, Rows: -1}, PageData)
	tb := a.Tabs[0].(*TableTab)
	v := tb.view
	testutil.WaitFor(t, tt, "rows", func() bool { return v.src.Rows != nil && !v.loading && tb.columns != nil })
	applyAt(t, func(stage string, i int) {
		if stage == "statement" && i == 1 {
			v.stopApply()
		}
	})
	editLabels(t, v, "stopped 1", "stopped 2")
	testutil.WaitFor(t, tt, "the apply", func() bool { return !v.applying })
	if tb.sess.Tx() != db.TxNone || len(a.Errors) != 0 {
		t.Fatalf("tx %v, errors %q", tb.sess.Tx(), a.Errors)
	}
	d, err := db.Open(context.Background(), testutil.PGConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var n int
	d.SQL.QueryRow("SELECT count(*) FROM shop.customers WHERE name LIKE 'stopped%'").Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows changed after Stop", n)
	}
}
