package dataview

import (
	"context"
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
	if tb.tx != db.TxOpen {
		t.Fatalf("tx %v after the first apply", tb.tx)
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
