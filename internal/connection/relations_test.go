package connection

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dgopher/internal/db"
)

// A table's relations are its foreign keys and those pointing at it,
// read once and kept until the catalog is forgotten.
func TestLoadRelationsCaches(t *testing.T) {
	file := filepath.Join(t.TempDir(), "r.sqlite")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := db.Config{Name: "lite", Engine: db.SQLite, Database: file}
	d, err := db.Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, q := range []string{
		`CREATE TABLE customers (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER REFERENCES customers(id))`,
		`CREATE TABLE refunds (id INTEGER PRIMARY KEY, order_id INTEGER REFERENCES orders(id))`,
	} {
		if _, err := d.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	cn := &Conn{Config: cfg, Store: d, Status: StatusConnected}
	cn.Reset()
	var got Relations
	LoadRelations(runNow{}, cn, "", "main", "orders", func(r Relations) { got = r })
	if len(got.FKs) != 1 || got.FKs[0].RefTable != "customers" || len(got.Refs) != 1 || got.Refs[0].Table != "refunds" {
		t.Fatalf("relations %+v", got)
	}
	calls := 0
	LoadRelations(held2{}, cn, "", "main", "orders", func(Relations) { calls++ })
	if calls != 1 {
		t.Fatal("cached relations not given at once")
	}
	cn.ForgetCatalog()
	if _, ok := cn.Relations[ObjectKey{Schema: "main", Name: "orders"}]; ok {
		t.Fatal("forgetting the catalog keeps relations")
	}
}

// held2 runs nothing, as a runner whose work never comes back.
type held2 struct{}

func (held2) Post(func())              {}
func (held2) Background(func() func()) {}

// counting counts the work a runner was given, running it.
type counting struct{ n int }

func (r *counting) Post(fn func())                { fn() }
func (r *counting) Background(work func() func()) { r.n++; work()() }

// A read that failed is not tried again until the catalog is forgotten,
// as a Refresh does: a card asking each frame would else read in a loop.
func TestLoadRelationsNoRetryAfterError(t *testing.T) {
	cn := &Conn{Config: db.Config{Name: "gone", Engine: db.SQLite}}
	cn.Reset() // no pool: every read fails
	r := &counting{}
	LoadRelations(r, cn, "", "main", "orders", nil)
	LoadRelations(r, cn, "", "main", "orders", nil)
	if r.n != 1 {
		t.Fatalf("%d reads after a failure", r.n)
	}
	if cn.RelationsError("", "main", "orders") == "" {
		t.Fatal("no error kept")
	}
	cn.ForgetCatalog()
	LoadRelations(r, cn, "", "main", "orders", nil)
	if r.n != 2 {
		t.Fatalf("%d reads after forgetting the catalog", r.n)
	}
}
