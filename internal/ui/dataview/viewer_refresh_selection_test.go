package dataview

import (
	"context"
	"slices"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

func execSQLite(t *testing.T, file string, stmts ...string) {
	t.Helper()
	d, err := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, s := range stmts {
		if _, err := d.SQL.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
}

func chosenRows(g *Grid) []int {
	rows := slices.Collect(g.selection.All())
	slices.Sort(rows)
	return rows
}

func reloadAndWait(t *testing.T, tt *ui.Tester, v *Viewer) {
	t.Helper()
	v.reload()
	testutil.WaitFor(t, tt, "reload", func() bool { return !v.loading })
}

// Rows moved or gone between reads: the rows chosen are found by their key.
func TestRefreshKeepsSelectionByKey(t *testing.T) {
	_, tt, tb, file := openNumbers(t, 5, 100)
	v := tb.view
	g := v.grid
	for _, i := range []int{1, 2, 4} { // ids 2, 3, 5
		g.selection.Add(i)
	}
	g.ViewOrder(&v.src)
	g.SelRow = 4
	g.selCol = 1
	g.List.Cells = ui.ColumnRange{Cursor: "1", Anchor: "0"}
	execSQLite(t, file, "DELETE FROM n WHERE id IN (1, 2)")
	reloadAndWait(t, tt, v)
	if got := chosenRows(g); !slices.Equal(got, []int{0, 2}) {
		t.Fatalf("rows chosen %v, want [0 2] (ids 3 and 5)", got)
	}
	if g.SelRow != 2 {
		t.Fatalf("cursor row %d, want 2 (id 5)", g.SelRow)
	}
	if g.List.Cells.Cursor != "1" || g.List.Cells.Anchor != "0" || g.selCol != 1 {
		t.Fatalf("columns %+v selCol %d", g.List.Cells, g.selCol)
	}
}

// Without a key, the rows chosen keep their places, up to the rows read.
func TestRefreshKeepsSelectionByPosition(t *testing.T) {
	_, tt, tb, file := openSQLite(t, 100, db.Object{Schema: "main", Name: "log", Kind: db.KindTable, Rows: -1}, func(d *db.DB) {
		d.SQL.Exec("CREATE TABLE log (code TEXT, note TEXT)")
		d.SQL.Exec("INSERT INTO log VALUES ('a','x'),('b','y'),('c','z'),('d','w'),('e','v')")
	})
	v := tb.view
	g := v.grid
	g.selection.Add(1)
	g.selection.Add(4)
	g.ViewOrder(&v.src)
	g.SelRow = 1
	g.setCursorCol(1)
	execSQLite(t, file, "DELETE FROM log WHERE code IN ('a', 'b')")
	reloadAndWait(t, tt, v)
	if got := chosenRows(g); !slices.Equal(got, []int{1}) {
		t.Fatalf("rows chosen %v, want [1]", got)
	}
	if g.SelRow != 1 || g.List.Cells.Cursor != "1" || g.selCol != 1 {
		t.Fatalf("cursor row %d columns %+v", g.SelRow, g.List.Cells)
	}
}
