package dataview

import (
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

// A restored table reads its rows in the sort it kept, a column gone
// since left out, and chooses its cell, in view, past the rows that fit;
// a row past those read chooses the last.
func TestRestoredTableSortAndCursorInView(t *testing.T) {
	a, tt, _, _ := openNumbers(t, 100, 100)
	cn := a.Tabs[0].(*TableTab).Conn
	obj := db.Object{Schema: "main", Name: "n", Kind: db.KindTable, Rows: -1}
	tb := NewTableTabWith(a, cn, "", obj, PageData, TableState{
		Sort: []SortKey{{Column: "gone"}, {Column: "id", Desc: true}}, Row: 80, Column: "label"})
	a.AddTab(tb)
	testutil.WaitFor(t, tt, "rows", func() bool { return tb.view.src.Rows != nil && !tb.view.loading })
	if ran := lastStatement(a); !strings.Contains(ran, `ORDER BY "id" DESC`) || strings.Contains(ran, "gone") {
		t.Fatalf("ran %q", ran)
	}
	tt.Frame()
	tt.Frame()
	g := tb.view.grid
	if g.SelRow != 80 || g.selCol != 1 || !tt.HasText("row 20") || tt.HasText("row 100") {
		t.Fatalf("row %d, column %d, texts %q", g.SelRow, g.selCol, tt.Texts())
	}
	st := tb.State()
	if len(st.Sort) != 1 || st.Sort[0] != (SortKey{Column: "id", Desc: true}) || st.Row != 80 || st.Column != "label" {
		t.Fatalf("state %+v", st)
	}

	far := NewTableTabWith(a, cn, "", obj, PageData, TableState{Row: 500, Column: "gone"})
	a.AddTab(far)
	testutil.WaitFor(t, tt, "rows", func() bool { return far.view.src.Rows != nil && !far.view.loading })
	if far.view.grid.SelRow != 99 || far.view.grid.selCol != 0 {
		t.Fatalf("row %d, column %d", far.view.grid.SelRow, far.view.grid.selCol)
	}
}

// A table opened with no state reads as before: in the key's order, no
// row chosen.
func TestTableWithoutRestoredStateUnchanged(t *testing.T) {
	a, tt, tb, _ := openNumbers(t, 3, 100)
	tt.Frame()
	if ran := lastStatement(a); !strings.HasSuffix(ran, `ORDER BY "id"`) {
		t.Fatalf("ran %q", ran)
	}
	if st := tb.State(); st.Filter != "" || st.Sort != nil || st.Column != "" || st.Row != 0 {
		t.Fatalf("state %+v", st)
	}
}

// A negative row, as a hand-edited workspace.json may hold, chooses the
// first row.
func TestRestoredNegativeRowChoosesFirst(t *testing.T) {
	a, tt, _, _ := openNumbers(t, 10, 100)
	cn := a.Tabs[0].(*TableTab).Conn
	obj := db.Object{Schema: "main", Name: "n", Kind: db.KindTable, Rows: -1}
	tb := NewTableTabWith(a, cn, "", obj, PageData, TableState{Row: -5, Column: "label"})
	a.AddTab(tb)
	testutil.WaitFor(t, tt, "rows", func() bool { return tb.view.src.Rows != nil && !tb.view.loading })
	tt.Frame()
	if g := tb.view.grid; g.SelRow != 0 || g.selCol != 1 {
		t.Fatalf("row %d, column %d", g.SelRow, g.selCol)
	}
}
