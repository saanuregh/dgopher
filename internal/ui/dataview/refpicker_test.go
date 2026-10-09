package dataview

import (
	"slices"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

// A foreign key's cell takes its value from a row of the table it points
// at, found by its key or its text.
func TestRefPicker(t *testing.T) {
	a, tt, cn := designHost(t)
	for _, q := range []string{`CREATE TABLE owners (id INTEGER PRIMARY KEY, name TEXT, secret_token TEXT)`,
		`INSERT INTO owners (name, secret_token) VALUES ('Alice', 'x1'), ('Bob', 'x2'), ('Alina', 'x3')`,
		`CREATE TABLE pets (id INTEGER PRIMARY KEY, owner INTEGER REFERENCES owners (id))`, `INSERT INTO pets (owner) VALUES (1)`} {
		if _, err := cn.DB.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "pets", Kind: db.KindTable, Rows: -1}, PageData)
	v := a.Tabs[0].(*TableTab).view
	testutil.WaitFor(t, tt, "the rows", func() bool { return len(v.src.Rows) == 1 && len(v.fks) == 1 })
	owner := slices.IndexFunc(v.src.Cols, func(c db.ColumnInfo) bool { return c.Name == "owner" })
	v.grid.addRow(&v.src)
	v.grid.editing = nil
	fk, ok := v.refOf(owner)
	if !ok {
		t.Fatal("no foreign key")
	}
	v.openRefPicker(1, owner, fk)
	p := a.Dialogs().refPicker
	testutil.WaitFor(t, tt, "the owners", func() bool { return !p.loading && len(p.rows) == 3 })
	if !p.masked[2] || tt.HasText("x1") {
		t.Fatalf("the token shows: %v", p.masked)
	}
	testutil.Snapshot(t, tt, "ref-picker")
	p.search = "ali"
	p.query()
	testutil.WaitFor(t, tt, "the search", func() bool { return !p.loading && len(p.rows) == 2 })
	p.choose(1)
	if got := v.grid.edits.inserted[0][owner]; got != db.Typed("3") || a.Dialogs().refPicker != nil && p.open {
		t.Fatalf("the cell holds %v", got)
	}
}
