package query

import (
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// The schema an editor finds names in is chosen in its toolbar, and
// follows a USE typed in it; DuckDB switches as MySQL and ClickHouse do.
func TestSchemaSwitcher(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "CREATE SCHEMA sales;")
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the schema made", func() bool { return !q.Running && len(q.results) > 0 })
	delete(cn.Schemas, "") // read again, with the new schema
	testutil.WaitFor(t, tt, "the schemas", func() bool { _, ok := cn.Schemas[""]; return ok })
	if q.currentSchema() != "main" || !tt.HasText("main") {
		t.Fatalf("current %q, texts %q", q.currentSchema(), tt.Texts())
	}
	q.switchSchema("sales")
	testutil.WaitFor(t, tt, "the switch", func() bool { return !q.Running && q.schema == "sales" })
	if !tt.HasText("sales") {
		t.Fatalf("texts %q", tt.Texts())
	}
	last := a.Events[len(a.Events)-1]
	if last.Statement != `USE "sales"` || last.Error != "" {
		t.Fatalf("audited %+v", last)
	}
	q.Editor.Text = "USE main;"
	q.Editor.PendingSel = &[2]int{2, 2}
	tt.Frame()
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "USE typed", func() bool { return !q.Running && q.schema == "main" })
}

// A PostgreSQL editor moves to another database of its server on a
// session of its own there, never with a transaction open.
func TestDatabaseSwitcher(t *testing.T) {
	testutil.Integration(t)
	a := newFakeQueryHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "BEGIN;")
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the transaction", func() bool { return !q.Running && q.Tx != db.TxNone })
	q.switchDatabase("template1")
	if q.Database == "template1" || len(a.Errors) == 0 {
		t.Fatalf("switched with a transaction open: %q", a.Errors)
	}
	q.Editor.Text = "ROLLBACK;"
	tt.Frame()
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the rollback", func() bool { return !q.Running && q.Tx == db.TxNone })
	q.switchDatabase("template1")
	q.Editor.Text = "SELECT current_database();"
	tt.Frame()
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the database", func() bool { return !q.Running && len(q.results) > 0 && q.results[0].view != nil })
	if got := db.Display(q.results[0].view.Rows()[0][0]); q.Database != "template1" || got != "template1" {
		t.Fatalf("database %q, current_database() %q", q.Database, got)
	}
}
