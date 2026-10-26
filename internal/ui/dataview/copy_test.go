package dataview

import (
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/export"
	"dgopher/internal/settings"

	"github.com/egoist/mygo/ui"
)

// chooseBlock chooses rows from to to of the order and the columns from
// one to another, as a drag would.
func chooseBlock(tt *ui.Tester, g *Grid, rows []int, from, to int) {
	g.selection.Clear()
	for _, r := range rows {
		g.selection.Add(r)
	}
	g.SelRow, g.selCol = rows[len(rows)-1], to
	g.List.Cells = ui.ColumnRange{Anchor: colID(from), Cursor: colID(to)}
	tt.Frame()
}

func TestCopyBlock(t *testing.T) {
	tt, g, src := selectionGrid(t)
	chooseBlock(tt, g, []int{0, 1}, 1, 2)
	if got := g.copySelection(src, export.TSV, false); got != "aa\tn1\nbb\tn2" {
		t.Fatalf("copy block %q", got)
	}
	if got := g.copySelection(src, export.TSV, true); got != "name\tnote\naa\tn1\nbb\tn2" {
		t.Fatalf("copy block with header %q", got)
	}
	chooseBlock(tt, g, []int{2}, 1, 1)
	if got := g.copySelection(src, export.TSV, false); got != "cc" {
		t.Fatalf("copy one cell %q", got)
	}
	chooseBlock(tt, g, []int{2}, 0, 1)
	if got := g.copySelection(src, export.TSV, false); got != "13\tcc" {
		t.Fatalf("copy one row of two cells %q", got)
	}
}

func TestCopyRowsVisibleColumnsOnly(t *testing.T) {
	tt, g, src := selectionGrid(t)
	g.hidden = map[int]bool{1: true}
	tt.Frame()
	g.selection.Add(0)
	g.selection.Add(1)
	g.List.Cells = ui.ColumnRange{All: true}
	tt.Frame()
	if got := g.copySelection(src, export.TSV, true); got != "id\tnote\n11\tn1\n12\tn2" {
		t.Fatalf("copy rows %q", got)
	}
	if got := g.copyText(NewFakeHost(t), src, "text"); got[:9] != "id | note" {
		t.Fatalf("plain text %q", got)
	}
}

func TestCopyAsInList(t *testing.T) {
	tt, g, src := selectionGrid(t)
	src.Rows[2][1] = "aa"
	src.Rows[1][1] = nil
	cn := &connection.Conn{Config: db.Config{Engine: db.Postgres}, Store: &db.DB{Dialect: db.DialectOf(db.Postgres)}}
	g.SQLOpts = SQLOptionsFor(cn, "", "t")
	chooseBlock(tt, g, []int{0, 1, 2}, 1, 1)
	if got, ok := g.copyInList(src); !ok || got != `("name" IN ('aa') OR "name" IS NULL)` {
		t.Fatalf("IN list %q %v", got, ok)
	}
	chooseBlock(tt, g, []int{0, 2}, 1, 1)
	if got, _ := g.copyInList(src); got != `"name" IN ('aa')` {
		t.Fatalf("IN list %q", got)
	}
	chooseBlock(tt, g, []int{0}, 0, 1)
	if _, ok := g.copyInList(src); ok {
		t.Fatal("IN list of two columns")
	}
}

func TestCopyAsValuesList(t *testing.T) {
	tt, g, src := selectionGrid(t)
	src.Rows[1][2] = nil
	chooseBlock(tt, g, []int{0, 1}, 0, 2)
	if got := g.copyValuesList(src); got != "11, 'aa', 'n1', 12, 'bb', NULL" {
		t.Fatalf("values list %q", got)
	}
}

func TestAdvancedCopyBlock(t *testing.T) {
	tt, g, src := selectionGrid(t)
	chooseBlock(tt, g, []int{0, 1}, 2, 1)
	got := g.copyAdvanced(src, settings.CopyOptions{Delimiter: ',', Header: true, RowNumbers: true, NullText: "NULL"})
	if want := "#,name,note\n1,aa,n1\n2,bb,n2\n"; got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// chooseWholeRows shows the grid once with every column chosen, as a
// click on the row numbers does.
func chooseWholeRows(t *testing.T, g *Grid, src *Source) {
	a := NewFakeHost(t)
	g.List.Cells = ui.ColumnRange{All: true}
	ui.NewTester(func(c *ui.Context) { g.View(c, a, src) }, 800, 300).Frame()
}
