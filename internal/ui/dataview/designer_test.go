package dataview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// designHost is a host with a SQLite file holding a table of two rows.
func designHost(t *testing.T) (*FakeHost, *ui.Tester, *connection.Conn) {
	t.Helper()
	a := NewFakeHost(t)
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.View, 1300, 800)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	for _, q := range []string{`CREATE TABLE items (id INTEGER PRIMARY KEY, label TEXT, price TEXT)`,
		`INSERT INTO items (label, price) VALUES ('pen', '1.5'), ('ink', '3')`} {
		if _, err := cn.DB.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return a, tt, cn
}

// The structure of a table changes in its form, once its SQL is
// reviewed; the table's tab then shows it as it now is, its rows kept.
func TestEditStructure(t *testing.T) {
	a, tt, cn := designHost(t)
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "items", Kind: db.KindTable, Rows: -1}, PageStructure)
	tb := a.Tabs[0].(*TableTab)
	testutil.WaitFor(t, tt, "the structure", func() bool { return tb.columns != nil })
	if err := tt.Click("Edit Structure"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the form", func() bool { return tb.design != nil })
	d := tb.design
	d.now.Columns[1].Name = "name"
	d.now.Columns[2].Type = "REAL"
	if err := tt.Click("Add Column"); err != nil {
		t.Fatal(err)
	}
	d.now.Columns[3].Name = "stock"
	tt.Frame()
	testutil.Snapshot(t, tt, "table-form")
	if _, err := d.change(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tb.CloseReason(), "not been applied") {
		t.Fatalf("closing with changes: %q", tb.CloseReason())
	}
	if err := tt.Click("Review SQL…"); err != nil {
		t.Fatal(err)
	}
	if a.Confirm == nil || !strings.Contains(a.Confirm.Preview, `RENAME COLUMN "label" TO "name"`) || !strings.Contains(a.Confirm.Preview, "is made again") {
		t.Fatalf("the review: %+v", a.Confirm)
	}
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "the table again", func() bool { next, ok := a.Tabs[0].(*TableTab); return ok && next != tb && next.columns != nil })
	var name string
	var price float64
	if err := cn.DB.SQL.QueryRow(`SELECT name, price FROM items WHERE stock IS NULL ORDER BY id LIMIT 1`).Scan(&name, &price); err != nil || name != "pen" || price != 1.5 {
		t.Fatalf("rows: %q %v %v", name, price, err)
	}
	if len(a.Errors) != 0 {
		t.Fatal(a.Errors)
	}
}

// A read-only connection's table offers no form.
func TestEditStructureReadOnly(t *testing.T) {
	a, tt, cn := designHost(t)
	cn.Config.ReadOnly = true
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "items", Kind: db.KindTable, Rows: -1}, PageStructure)
	tb := a.Tabs[0].(*TableTab)
	testutil.WaitFor(t, tt, "the structure", func() bool { return tb.columns != nil && tt.HasText("The connection is read-only.") })
	tb.editStructure()
	tt.Frame()
	if tb.design != nil || tb.readingDesign {
		t.Fatal("a read-only table is edited")
	}
}

// A new table is designed in a tab of its own, which becomes the
// table's once it is made.
func TestNewTable(t *testing.T) {
	a, tt, cn := designHost(t)
	OpenNewTable(a, cn, "", "main")
	nt := a.Tabs[0].(*TableDesignTab)
	tt.Frame()
	if nt.CloseReason() != "" {
		t.Fatalf("an untouched design: %q", nt.CloseReason())
	}
	d := nt.design
	d.now.Name = "people"
	if err := tt.Click("Add Column"); err != nil {
		t.Fatal(err)
	}
	d.now.Columns[1].Name, d.now.Columns[1].Nullable = "email", false
	d.section = designIndexes
	tt.Frame()
	if err := tt.Click("Add Index"); err != nil {
		t.Fatal(err)
	}
	d.now.Indexes[0].Columns, d.now.Indexes[0].Unique = []string{"email"}, true
	tt.Frame()
	testutil.Snapshot(t, tt, "new-table")
	if nt.Title() != "people (new)" || nt.CloseReason() == "" {
		t.Fatalf("title %q, close reason %q", nt.Title(), nt.CloseReason())
	}
	if err := tt.Click("Review SQL…"); err != nil {
		t.Fatal(err)
	}
	if a.Confirm == nil || !strings.Contains(a.Confirm.Preview, `CREATE UNIQUE INDEX "main"."people_idx" ON "people" ("email")`) {
		t.Fatalf("the review: %+v", a.Confirm)
	}
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "the table's tab", func() bool { tb, ok := a.Tabs[0].(*TableTab); return ok && tb.Object.Name == "people" })
	if _, err := cn.DB.SQL.Exec(`INSERT INTO people (email) VALUES ('a@b'), ('a@b')`); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("the unique index: %v", err)
	}
}
