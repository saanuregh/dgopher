package dataview

import (
	"context"
	"errors"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

func TestTableEditing(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := NewFakeHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.View, 1360, 860)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	connection.LoadObjects(a, cn, "", "shop")
	testutil.WaitFor(t, tt, "objects", func() bool { return cn.Objects[connection.SchemaKey{Database: "", Schema: "shop"}] != nil })
	var customers db.Object
	for _, o := range cn.Objects[connection.SchemaKey{Database: "", Schema: "shop"}] {
		if o.Name == "customers" {
			customers = o
		}
	}
	a.OpenTable(cn, "", customers, PageData)
	tb := a.Tabs[0].(*TableTab)
	testutil.WaitFor(t, tt, "rows and columns", func() bool { return tb.view.src.Rows != nil && tb.columns != nil })
	if why := tb.view.readOnlyReason(); why != "" {
		t.Fatalf("customers not editable: %s", why)
	}
	// Change a name, add a row, delete a row.
	tb.view.grid.setValue(&tb.view.src, 0, 1, db.Typed("Renamed customer"))
	tb.view.grid.addRow(&tb.view.src)
	tb.view.grid.editing = nil
	tb.view.grid.edits.inserted[0][1] = db.Typed("Brand new")
	tb.view.grid.edits.inserted[0][2] = db.Typed("new@example.com")
	tb.view.grid.edits.deleted[5] = true
	tt.Frame()
	testutil.Snapshot(t, tt, "table-pending")
	// Row 5 has orders: deleting it fails on the foreign key, and nothing
	// is applied.
	tb.view.Review(nil)
	// It reads first which tables point at customers, then asks.
	testutil.WaitFor(t, tt, "the review of the changes", func() bool { return a.Confirm != nil })
	tt.Frame()
	testutil.Snapshot(t, tt, "table-review")
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "apply", func() bool { return !tb.view.applying })
	if len(a.Errors) == 0 || tb.view.grid.edits.count() == 0 {
		t.Fatalf("a failing change was applied: errors %q", a.Errors)
	}
	a.Errors = nil
	delete(tb.view.grid.edits.deleted, 5)
	tb.view.Review(nil)
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "apply", func() bool { return !tb.view.applying && tb.view.grid.edits.count() == 0 && !tb.view.loading })
	if len(a.Errors) > 0 {
		t.Fatalf("apply failed: %q", a.Errors)
	}
	var name string
	cn.DB.SQL.QueryRow("SELECT name FROM shop.customers WHERE id = $1", tb.view.src.Rows[0][0]).Scan(&name)
	var n int
	cn.DB.SQL.QueryRow("SELECT count(*) FROM shop.customers WHERE name = 'Brand new'").Scan(&n)
	if n != 1 {
		t.Fatalf("inserted rows %d", n)
	}
	tb.Page = PageStructure
	tt.Frame()
	testutil.Snapshot(t, tt, "table-structure")
	tb.Page = PageDDL
	tt.Frame()
	testutil.Snapshot(t, tt, "table-ddl")
}

func TestERDiagram(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := NewFakeHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.View, 1360, 760)
	OpenER(a, cn, "", "shop")
	testutil.WaitFor(t, tt, "diagram", func() bool {
		return len(a.Tabs) == 1 && !a.Tabs[0].(*ERTab).loading
	})
	e := a.Tabs[0].(*ERTab)
	if e.err != "" || len(e.tables) != 2 {
		t.Fatalf("diagram %q %d", e.err, len(e.tables))
	}
	// orders refers to customers: it stands to its right.
	orders, customers := e.byName[erKey("shop", "orders")], e.byName[erKey("shop", "customers")]
	if orders.x <= customers.x {
		t.Errorf("layout: orders at %v, customers at %v", orders.x, customers.x)
	}
	tt.Frame()
	testutil.Snapshot(t, tt, "er-diagram")
}

func TestForeignKeyNavigation(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := NewFakeHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.View, 1360, 760)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	a.OpenTable(cn, "", db.Object{Schema: "shop", Name: "orders", Kind: db.KindTable, Rows: -1}, PageData)
	orders := a.Tabs[0].(*TableTab)
	testutil.WaitFor(t, tt, "orders", func() bool { return len(orders.view.src.Rows) > 0 && orders.fks != nil })
	// The first order's customer, a cell of its own text.
	customer := db.Display(orders.view.src.Rows[0][1])
	if err := tt.RightClick(customer); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Navigate", "Go to Referenced Row in customers"); err != nil {
		t.Fatalf("%v; menu %q", err, tt.Menu())
	}
	testutil.WaitFor(t, tt, "customers", func() bool {
		ct, ok := a.ActiveTab().(*TableTab)
		return ok && ct.Object.Name == "customers" && len(ct.view.src.Rows) == 1
	})
	ct := a.ActiveTab().(*TableTab)
	if got := db.Display(ct.view.src.Rows[0][0]); got != customer {
		t.Fatalf("opened customer %s, want %s (where %q)", got, customer, ct.view.where)
	}
	testutil.Snapshot(t, tt, "fk-navigation")
}

// Changes applied inside the user's transaction that fail leave nothing
// in it.
func TestApplyInsideOpenTransaction(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := NewFakeHost(t)
	cfg := testutil.PGConfig()
	cfg.Env = db.Production
	cn := a.AddConn(cfg)
	tt := ui.NewTester(a.View, 1360, 760)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	a.OpenTable(cn, "", db.Object{Schema: "shop", Name: "customers", Kind: db.KindTable, Rows: -1}, PageData)
	tb := a.Tabs[0].(*TableTab)
	testutil.WaitFor(t, tt, "rows", func() bool { return len(tb.view.src.Rows) > 0 && tb.sess != nil })
	// The first apply opens the transaction (manual commit), the second
	// runs inside it and fails on the unique email.
	tb.view.grid.setValue(&tb.view.src, 0, 1, db.Typed("Changed once"))
	stmts, _ := tb.view.changes()
	tb.view.apply(stmts)
	testutil.WaitFor(t, tt, "first apply", func() bool { return !tb.view.applying && !tb.view.loading })
	if tb.Tx != db.TxOpen {
		t.Fatalf("tx %v after the first apply", tb.Tx)
	}
	tb.view.grid.setValue(&tb.view.src, 1, 1, db.Typed("Changed twice"))
	tb.view.grid.setValue(&tb.view.src, 2, 2, db.Typed(db.Display(tb.view.src.Rows[3][2]))) // a duplicate email
	stmts, _ = tb.view.changes()
	tb.view.apply(stmts)
	testutil.WaitFor(t, tt, "second apply", func() bool { return !tb.view.applying })
	if len(a.Errors) == 0 {
		t.Fatal("a failing apply reported no error")
	}
	a.Errors = nil
	// Still in the transaction: the first change is there, not the second.
	var n int
	err := tb.sess.DB().SQL.QueryRow("SELECT 1").Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	c, err := tb.sess.Query(context.Background(), "SELECT name FROM shop.customers WHERE name IN ('Changed once', 'Changed twice') ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := c.Fetch(10)
	if len(rows) != 1 || db.Display(rows[0][0]) != "Changed once" || tb.sess.Tx() != db.TxOpen {
		t.Fatalf("inside the transaction: %v, tx %v", rows, tb.sess.Tx())
	}
	tb.endTx(false)
	testutil.WaitFor(t, tt, "rollback", func() bool { return tb.sess.Tx() == db.TxNone })
}

// A table whose rows cannot be read leaves no wish to focus its grid,
// which another tab's grid would take later.
func TestFailedTableDropsFocusWant(t *testing.T) {
	a := NewFakeHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.View, 1000, 700)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "missing", Kind: db.KindTable, Rows: -1}, PageData)
	tb := a.Tabs[0].(*TableTab)
	testutil.WaitFor(t, tt, "the error", func() bool { return tb.view.err != "" })
	*a.FocusWant() = "editor"
	tt.Frame()
	tt.Frame()
	if want := *a.FocusWant(); want != "" {
		t.Fatalf("focus still wanted: %q", want)
	}
}

// DuckDB has no savepoints: an apply inside an open transaction is
// refused before anything is sent, saying why, whether the tab holds the
// transaction or runs inside another tab's, which it names. The
// transaction and the changes stay as they were.
func TestDuckDBApplyInsideTxRefused(t *testing.T) {
	a := NewFakeHost(t)
	cn := a.AddConn(db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:", Commit: db.CommitManual})
	tt := ui.NewTester(a.View, 1200, 700)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	for _, s := range []string{"CREATE TABLE n (id INTEGER PRIMARY KEY, label TEXT)", "INSERT INTO n VALUES (1, 'one'), (2, 'two')"} {
		if _, err := cn.DB.SQL.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	obj := db.Object{Schema: "main", Name: "n", Kind: db.KindTable, Rows: -1}
	open := func() *TableTab {
		tb := NewTableTab(a, cn, "", obj, PageData)
		a.AddTab(tb)
		testutil.WaitFor(t, tt, "rows", func() bool { return tb.view.src.Rows != nil && tb.columns != nil && !tb.view.loading && tb.sess != nil })
		return tb
	}
	apply := func(tb *TableTab, row int, label string) {
		t.Helper()
		tb.view.grid.setValue(&tb.view.src, row, 1, db.Typed(label))
		stmts, err := tb.view.changes()
		if err != nil {
			t.Fatal(err)
		}
		tb.view.apply(stmts)
		testutil.WaitFor(t, tt, "the apply", func() bool { return !tb.view.applying && !tb.view.loading })
	}
	refused := func(tb *TableTab, want ...string) {
		t.Helper()
		if len(a.Errors) != 1 || strings.Contains(a.Errors[0], "Parser Error") || strings.Contains(a.Errors[0], "SAVEPOINT") {
			t.Fatalf("errors %q", a.Errors)
		}
		for _, w := range want {
			if !strings.Contains(a.Errors[0], w) {
				t.Fatalf("the refusal does not say %q: %q", w, a.Errors[0])
			}
		}
		if tb.view.grid.edits.count() != 1 {
			t.Fatalf("%d changes pending after the refusal", tb.view.grid.edits.count())
		}
		a.Errors = nil
	}
	owner := open()
	apply(owner, 0, "uno") // opens the tab's transaction (manual commit)
	if owner.Tx != db.TxOpen || len(a.Errors) != 0 {
		t.Fatalf("the first apply: tx %v, errors %q", owner.Tx, a.Errors)
	}
	apply(owner, 1, "dos")
	refused(owner, "DuckDB has no savepoints", "commit or roll back this tab's transaction first")

	inside := open()
	if inside.InsideTx != db.TxOpen {
		t.Fatalf("the second tab is not inside the transaction: own %v, inside %v", inside.Tx, inside.InsideTx)
	}
	inside.SetTxOwner("the owner") // as the app names it
	apply(inside, 1, "dos")
	refused(inside, "DuckDB has no savepoints", "the owner's")
	if owner.sess.Tx() != db.TxOpen || !owner.sess.OwnsTx() {
		t.Fatalf("the owner's transaction: %v, owned %v", owner.sess.Tx(), owner.sess.OwnsTx())
	}
	var label string
	if err := cn.DB.SQL.QueryRow("SELECT label FROM n WHERE id = 2").Scan(&label); err != nil || label != "two" {
		t.Fatalf("row 2 holds %q, %v", label, err)
	}
	owner.endTx(false)
	testutil.WaitFor(t, tt, "the rollback", func() bool { return owner.sess.Tx() == db.TxNone && !owner.Busy() })
}

// A Commit or Roll Back that stopped on an internal error tells what
// waited for it that it failed, and reads the transaction again, which
// stays open: closing and quitting still ask.
func TestStoppedEndTxReadsTxAgain(t *testing.T) {
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
	testutil.WaitFor(t, tt, "rows", func() bool { return tb.view.src.Rows != nil && tb.columns != nil && !tb.view.loading && tb.sess != nil })
	tb.view.grid.setValue(&tb.view.src, 0, 1, db.Typed("uno"))
	stmts, err := tb.view.changes()
	if err != nil {
		t.Fatal(err)
	}
	tb.view.apply(stmts) // opens the tab's transaction (manual commit)
	testutil.WaitFor(t, tt, "the apply", func() bool { return !tb.view.applying && !tb.view.loading })
	if tb.Tx != db.TxOpen {
		t.Fatalf("tx %v, errors %q", tb.Tx, a.Errors)
	}
	// As a Commit that stopped on a panic leaves the tab.
	var told error
	tb.ending = true
	tb.Times().Then = func(err error) { told = err }
	tb.setTx(db.TxNone, db.TxNone)
	tb.endTxStopped()
	if !errors.Is(told, ErrTxEndStopped) || tb.Times().Then != nil || tb.ending {
		t.Fatalf("told %v, then kept %v, ending %v", told, tb.Times().Then != nil, tb.ending)
	}
	testutil.WaitFor(t, tt, "the transaction read again", func() bool { return tb.Tx == db.TxOpen })
	tb.endTx(false)
	testutil.WaitFor(t, tt, "the rollback", func() bool { return tb.Tx == db.TxNone && !tb.Busy() })
}
