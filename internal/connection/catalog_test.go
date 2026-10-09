package connection

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dgopher/internal/db"
)

// runNow runs a runner's work at once, on the test's goroutine.
type runNow struct{}

func (runNow) Post(fn func())                { fn() }
func (runNow) Background(work func() func()) { work()() }

// A schema read by names alone has no column read on its own; one read
// with its columns has every table's at once.
func TestCatalogDepth(t *testing.T) {
	file := filepath.Join(t.TempDir(), "c.sqlite")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := db.Config{Name: "lite", Engine: db.SQLite, Database: file}
	d, err := db.Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, q := range []string{`CREATE TABLE a (id INTEGER PRIMARY KEY, x TEXT)`, `CREATE TABLE b (id INTEGER PRIMARY KEY)`} {
		if _, err := d.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	cn := &Conn{Config: cfg, DB: d, Status: StatusConnected}
	cn.Reset()

	cn.Config.SetCatalog("", "main", db.CatalogNames)
	LoadObjects(runNow{}, cn, "", "main")
	if len(cn.Objects[SchemaKey{Schema: "main"}]) != 2 || len(cn.Columns) != 0 {
		t.Fatalf("names: objects %+v, columns %+v", cn.Objects, cn.Columns)
	}
	if WantColumns(runNow{}, cn, "", "main", "a") || len(cn.Columns) != 0 {
		t.Fatalf("names: columns read on their own: %+v", cn.Columns)
	}

	cn.Config.SetCatalog("", "main", db.CatalogColumns)
	cn.ForgetCatalog()
	LoadObjects(runNow{}, cn, "", "main")
	if cols := cn.Columns[ObjectKey{Schema: "main", Name: "a"}]; len(cols) != 2 || len(cn.Columns[ObjectKey{Schema: "main", Name: "b"}]) != 1 {
		t.Fatalf("columns: %+v", cn.Columns)
	}

	cn.Config.SetCatalog("", "main", db.CatalogEverything)
	cn.ForgetCatalog()
	LoadObjects(runNow{}, cn, "", "main")
	if _, ok := cn.Items[SchemaKey{Schema: "main"}]; !ok || len(cn.Columns) != 2 {
		t.Fatalf("everything: items %+v, columns %+v", cn.Items, cn.Columns)
	}

	cn.Config.SetCatalog("", "main", db.CatalogAsNeeded)
	if cn.Config.Catalog != nil {
		t.Fatalf("as needed is kept: %+v", cn.Config.Catalog)
	}
}

// held keeps a runner's work until the test runs it, as a slow read.
type held struct{ work []func() func() }

func (h *held) Post(fn func())                { fn() }
func (h *held) Background(work func() func()) { h.work = append(h.work, work) }

// A depth set while the schema is read at the one before has the schema
// read again at it.
func TestCatalogDepthChangedWhileReading(t *testing.T) {
	file := filepath.Join(t.TempDir(), "c.sqlite")
	os.WriteFile(file, nil, 0o600)
	cfg := db.Config{Name: "lite", Engine: db.SQLite, Database: file}
	d, err := db.Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.SQL.Exec(`CREATE TABLE a (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	cn := &Conn{Config: cfg, DB: d, Status: StatusConnected}
	cn.Reset()
	h := &held{}
	LoadObjects(h, cn, "", "main")
	cn.Config.SetCatalog("", "main", db.CatalogColumns)
	for len(h.work) > 0 {
		w := h.work[0]
		h.work = h.work[1:]
		w()()
	}
	if len(cn.Columns[ObjectKey{Schema: "main", Name: "a"}]) != 1 {
		t.Fatalf("columns %+v", cn.Columns)
	}
}
