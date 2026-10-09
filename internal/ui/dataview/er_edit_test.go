package dataview

import (
	"os"
	"path/filepath"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// A column dragged onto a table refers to it by a foreign key, once
// confirmed; the diagram reads the schema again, its tables where they
// stood. A table's menu drops the key, then the table.
func TestEREditsSchema(t *testing.T) {
	file := filepath.Join(t.TempDir(), "shop.sqlite")
	os.WriteFile(file, nil, 0o600)
	a := NewFakeHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.View, 1360, 760)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	for _, q := range []string{
		`CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER, total REAL)`,
		`INSERT INTO customers VALUES (1, 'Ada')`, `INSERT INTO orders VALUES (1, 1, 9.5)`,
	} {
		if _, err := cn.DB.SQL.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	OpenER(a, cn, "", "main")
	testutil.WaitFor(t, tt, "the diagram", func() bool { return len(a.Tabs) == 1 && !a.Tabs[0].(*ERTab).loading })
	e := a.Tabs[0].(*ERTab)
	orders, customers := e.byName[erKey("main", "orders")], e.byName[erKey("main", "customers")]
	customers.x, customers.y = 500, 300 // moved by hand: a reload keeps it there

	// customer_id dragged onto customers' box.
	tt.Frame()
	from, ok := tt.Find("customer_id")
	if !ok {
		t.Fatalf("no customer_id in %q", tt.Texts())
	}
	to, ok := tt.Find("customers")
	if !ok {
		t.Fatal("no customers")
	}
	tt.Press(from.X+5, from.Y+from.H/2)
	for i := 1; i <= 10; i++ {
		x := from.X + 5 + (to.X+20-from.X-5)*float32(i)/10
		y := from.Y + from.H/2 + (to.Y+to.H/2-from.Y-from.H/2)*float32(i)/10
		tt.Move(x, y)
	}
	tt.Release(to.X+20, to.Y+to.H/2)
	testutil.WaitFor(t, tt, "the confirmation", func() bool { return a.Confirm != nil })
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "the foreign key", func() bool {
		o := e.byName[erKey("main", "orders")]
		return !e.loading && o != nil && len(o.fks) == 1
	})
	if c := e.byName[erKey("main", "customers")]; c.x != 500 || c.y != 300 {
		t.Fatalf("customers moved to %v, %v on reload", c.x, c.y)
	}
	var n int
	if cn.DB.SQL.QueryRow(`SELECT count(*) FROM orders`).Scan(&n); n != 1 {
		t.Fatalf("the rows of orders: %d", n)
	}

	orders = e.byName[erKey("main", "orders")]
	e.dropForeignKey(orders, orders.fks[0])
	testutil.WaitFor(t, tt, "the confirmation", func() bool { return a.Confirm != nil })
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "the key dropped", func() bool {
		o := e.byName[erKey("main", "orders")]
		return !e.loading && o != nil && len(o.fks) == 0
	})
	e.dropTable(e.byName[erKey("main", "orders")])
	testutil.WaitFor(t, tt, "the confirmation", func() bool { return a.Confirm != nil })
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "the table dropped", func() bool { return !e.loading && len(e.tables) == 1 })
}
