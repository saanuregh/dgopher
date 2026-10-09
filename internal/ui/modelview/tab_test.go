package modelview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/datamodel"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"

	"github.com/egoist/mygo/ui"
)

// A model built from a schema shows its tables, writes its DDL, and
// compares with the schema once it changes; the migration, confirmed,
// makes the schema like the model again, and an update makes the model
// like the schema.
func TestModelTab(t *testing.T) {
	h := dataview.NewFakeHost(t)
	file := filepath.Join(t.TempDir(), "shop.sqlite")
	os.WriteFile(file, nil, 0o600)
	cn := h.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(h.View, 1280, 800)
	h.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	exec := func(stmts ...string) {
		t.Helper()
		for _, q := range stmts {
			if _, err := cn.DB.SQL.Exec(q); err != nil {
				t.Fatal(q, err)
			}
		}
	}
	exec(`CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER REFERENCES customers (id), total NUMERIC)`,
		`INSERT INTO customers VALUES (1, 'Ada')`, `INSERT INTO orders VALUES (1, 1, 10)`)
	tables, _, err := datamodel.Build(context.Background(), cn.DB, "main", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := &datamodel.Model{Name: "Shop", Engine: db.SQLite, Tables: tables, Source: &datamodel.Source{Connection: "lite", Schema: "main"}}
	path := datamodel.File(h.Project, m.Name)
	if _, err := m.Save(path, nil); err != nil {
		t.Fatal(err)
	}
	tab, err := Open(h, h.Project, path)
	if err != nil {
		t.Fatal(err)
	}
	h.AddTab(tab)
	tt.Frame()
	testutil.Snapshot(t, tt, "model-tab")

	tab.generating = newGenerateForm(tab)
	tt.Frame()
	if !strings.Contains(tab.generating.text, `CREATE TABLE "main"."orders"`) {
		t.Fatalf("the script:\n%s", tab.generating.text)
	}
	tab.generating.engine = db.Postgres.Label()
	tt.Frame()
	if !strings.Contains(tab.generating.text, "PostgreSQL") || !strings.Contains(tab.generating.text, "bigint") {
		t.Fatalf("the script for PostgreSQL:\n%s", tab.generating.text)
	}
	tab.generating = nil

	// The schema changes: a column is added, and one table dropped.
	exec(`ALTER TABLE customers ADD COLUMN email TEXT`)
	f := newCompareForm(tab)
	if f.conn != "lite" || f.schema != "main" {
		t.Fatalf("compare with %q %q", f.conn, f.schema)
	}
	tab.comparing = f
	tab.runCompare(f)
	testutil.WaitFor(t, tt, "the comparison", func() bool { return tab.result != nil })
	if d := tab.result.diffs[0]; d.Name != "customers" || d.State != datamodel.Changed {
		t.Fatalf("diffs %+v", tab.result.diffs)
	}
	testutil.Snapshot(t, tt, "model-compare")

	tab.result.drop = true
	tab.applyMigration()
	if h.Confirm == nil || !strings.Contains(h.Confirm.Preview, "customers") {
		t.Fatalf("not asked: %+v", h.Confirm)
	}
	h.Confirm.OnConfirm()
	h.Confirm.Open = false
	r := tab.result
	testutil.WaitFor(t, tt, "the migration", func() bool { return tab.result != r && !tab.result.applying })
	for _, d := range tab.result.diffs {
		if d.State != datamodel.Same {
			t.Fatalf("after the migration: %+v", tab.result.diffs)
		}
	}
	var name string
	if err := cn.DB.SQL.QueryRow(`SELECT name FROM customers`).Scan(&name); err != nil || name != "Ada" {
		t.Fatalf("the rows of customers: %q %v", name, err)
	}

	// Designed in the model: a table added, changed, and dropped.
	tab.result = nil
	tab.edit(-1)
	if err := tab.saveTable(db.TableDesign{Schema: "main", Name: "items", Columns: []db.ColumnDesign{dataview.FirstColumn(db.SQLite)}}); err != nil {
		t.Fatal(err)
	}
	if err := tab.saveTable(db.TableDesign{Schema: "main", Name: "orders", Columns: []db.ColumnDesign{dataview.FirstColumn(db.SQLite)}}); err == nil {
		t.Fatal("two tables of one name")
	}
	tab.edit(tab.table)
	items := tab.m.Tables[tab.table]
	items.Columns = append(items.Columns, db.ColumnDesign{Name: "qty", Type: "INTEGER", Nullable: true})
	if err := tab.saveTable(items); err != nil || tab.form != nil {
		t.Fatal(err)
	}
	if saved, _ := datamodel.Load(path); len(saved.Tables) != 3 {
		t.Fatalf("saved %+v", saved.Tables)
	} else if it, _ := saved.Table("main", "items"); len(it.Columns) != 2 {
		t.Fatalf("items %+v", it)
	}
	tab.confirmDrop = true
	testutil.WaitFor(t, tt, "the question", func() bool { return testutil.HasTextContaining(tt, "from the model?") })
	if err := tt.Click("Drop"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tab.m.Table("main", "items"); ok || tab.confirmDrop {
		t.Fatalf("items not dropped: %+v", tab.m.Tables)
	}

	exec(`ALTER TABLE customers ADD COLUMN phone TEXT`)
	tab.update()
	testutil.WaitFor(t, tt, "the update", func() bool { return !tab.updating })
	saved, err := datamodel.Load(path)
	if err != nil || tab.err != "" {
		t.Fatal(err, tab.err)
	}
	if c, _ := saved.Table("main", "customers"); len(c.Columns) != 3 || c.Columns[2].Name != "phone" {
		t.Fatalf("customers after the update: %+v", c.Columns)
	}
}
