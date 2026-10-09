package dataview

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// A query built of a table, a table it refers to joined by its key, a
// column summed, a condition, an order and a limit runs as written.
func TestQueryBuilder(t *testing.T) {
	file := filepath.Join(t.TempDir(), "shop.sqlite")
	os.WriteFile(file, nil, 0o600)
	a := NewFakeHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.View, 1300, 900)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	for _, q := range []string{
		`CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER REFERENCES customers (id), total REAL)`,
		`CREATE TABLE items (id INTEGER PRIMARY KEY, order_id INTEGER REFERENCES orders (id))`,
		`INSERT INTO customers VALUES (1, 'Ada'), (2, 'Linus')`,
		`INSERT INTO orders VALUES (1, 1, 10), (2, 1, 20), (3, 2, 3)`,
	} {
		if _, err := cn.DB.SQL.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	OpenQueryBuilder(a, cn, "", db.Object{Schema: "main", Name: "orders", Kind: db.KindTable})
	b := a.Dialogs().builder
	testutil.WaitFor(t, tt, "the table", func() bool { return !b.tables[0].loading && len(b.schemaTables) > 0 })
	cands := b.candidates()
	i := slices.IndexFunc(cands, func(c qbCandidate) bool { return c.name == "customers" && c.on != nil })
	if i < 0 || !slices.ContainsFunc(cands, func(c qbCandidate) bool { return c.name == "items" && c.on != nil }) {
		t.Fatalf("candidates %+v", cands)
	}
	b.addTable(a, cands[i].schema, cands[i].name, cands[i].on)
	testutil.WaitFor(t, tt, "the join", func() bool { return !b.tables[1].loading })
	b.setChosen("customers.name", true)
	b.setChosen("orders.total", true)
	b.columns[1].agg = "sum"
	b.addCond(builderCond{column: "orders.total", op: ">", value: "5"})
	b.addSort(qbSort{label: "sum_total", desc: 1})
	b.limit = "10"
	sql, err := b.sql()
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT "customers"."name",
       SUM("orders"."total") AS "sum_total"
FROM "main"."orders" AS "orders"
JOIN "main"."customers" AS "customers" ON "customers"."id" = "orders"."customer_id"
WHERE "orders"."total" > 5
GROUP BY "customers"."name"
ORDER BY "sum_total" DESC
LIMIT 10`
	if sql != want {
		t.Fatalf("built\n%s\nwant\n%s", sql, want)
	}
	var name string
	var total float64
	if err := cn.DB.SQL.QueryRow(sql).Scan(&name, &total); err != nil || name != "Ada" || total != 30 {
		t.Fatalf("ran as %q %v: %v", name, total, err)
	}
	tt.Frame()
	testutil.Snapshot(t, tt, "query-builder")

	// The joined table goes with its column and its condition.
	b.addCond(builderCond{column: "customers.name", op: "=", value: "Ada"})
	b.removeTable(b.tables[1])
	if len(b.tables) != 1 || len(b.columns) != 1 || len(b.conds) != 1 {
		t.Fatalf("after removing customers: %d tables, columns %+v, conditions %+v", len(b.tables), b.columns, b.conds)
	}
	if sql, err := b.sql(); err != nil || strings.Contains(sql, "customers") {
		t.Fatalf("without customers: %s %v", sql, err)
	}

	// A sort by a column taken out of the result goes with it.
	b.addSort(qbSort{label: "orders.id"})
	b.setChosen("orders.id", true)
	tt.Frame()
	if len(b.sorts) != 2 {
		t.Fatalf("sorts %+v", b.sorts)
	}
	b.setChosen("orders.id", false)
	b.pruneSorts(b.sortChoices())
	if len(b.sorts) != 1 || b.sorts[0].label != "sum_total" {
		t.Fatalf("sorts after taking orders.id out: %+v", b.sorts)
	}

	// Columns of one name are named apart, with their tables' aliases.
	b.addTable(a, "main", "orders", nil)
	testutil.WaitFor(t, tt, "orders again", func() bool { return !b.tables[1].loading })
	b.columns = nil
	b.colKeys = rowKeys{}
	b.conds, b.condKeys = nil, rowKeys{}
	b.setChosen("orders.id", true)
	b.setChosen("orders2.id", true)
	b.setChosen("orders.total", true)
	b.setChosen("orders2.total", true)
	b.columns[2].agg, b.columns[3].agg = "sum", "sum"
	b.pruneSorts(b.sortChoices())
	if len(b.sorts) != 0 {
		t.Fatalf("sorts by sum_total, renamed: %+v", b.sorts)
	}
	if got := b.sortChoices(); !slices.Equal(got, []string{"orders_id", "orders2_id", "orders_sum_total", "orders2_sum_total"}) {
		t.Fatalf("named %q", got)
	}
	b.addSort(qbSort{label: "orders2_sum_total"})
	sql, err = b.sql()
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := cn.DB.SQL.Query(sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	} else {
		rows.Close()
	}

	// Without the connection nothing is written, and nothing breaks.
	pool := cn.DB
	cn.DB = nil
	if got := b.sortChoices(); got != nil {
		t.Fatalf("disconnected, sorted by %q", got)
	}
	if _, err := b.sql(); err == nil {
		t.Fatal("built a query disconnected")
	}
	cn.DB = pool
}

// A table whose name holds a dot is found by its columns, and goes alone.
func TestQueryBuilderDottedName(t *testing.T) {
	file := filepath.Join(t.TempDir(), "dots.sqlite")
	os.WriteFile(file, nil, 0o600)
	a := NewFakeHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.View, 1300, 900)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	for _, q := range []string{`CREATE TABLE "a.b" (id INTEGER PRIMARY KEY, n INTEGER)`, `CREATE TABLE a (id INTEGER PRIMARY KEY)`} {
		if _, err := cn.DB.SQL.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	OpenQueryBuilder(a, cn, "", db.Object{Schema: "main", Name: "a", Kind: db.KindTable})
	b := a.Dialogs().builder
	testutil.WaitFor(t, tt, "the table", func() bool { return !b.tables[0].loading })
	b.addTable(a, "main", "a.b", nil)
	testutil.WaitFor(t, tt, "the dotted table", func() bool { return !b.tables[1].loading })
	b.setChosen("a.id", true)
	b.setChosen("a.b.n", true)
	sql, err := b.sql()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, `"a.b"."n"`) {
		t.Fatalf("built\n%s", sql)
	}
	if _, err := cn.DB.SQL.Exec(sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	b.removeTable(b.tables[1])
	if len(b.columns) != 1 || b.columns[0].label != "a.id" {
		t.Fatalf("after removing a.b, columns %+v", b.columns)
	}
}
