package dataview

import (
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// openLabels opens a table of three rows, one of them with a NULL label,
// in a window that takes keys of two in a row, as the app's does.
func openLabels(t *testing.T) (*FakeHost, *ui.Tester, *TableTab) {
	t.Helper()
	a, _, tb, _ := openSQLite(t, 0, db.Object{Schema: "main", Name: "n", Kind: db.KindTable, Rows: -1}, func(d *db.DB) {
		d.SQL.Exec("CREATE TABLE n (id INTEGER PRIMARY KEY, label TEXT)")
		d.SQL.Exec("INSERT INTO n VALUES (1, 'apple'), (2, 'pear'), (3, NULL)")
	})
	tt := ui.NewTester(func(c *ui.Context) {
		keymap.Dispatch(c, a)
		a.View(c)
	}, 1200, 700)
	tt.Frame()
	return a, tt, tb
}

// filterByKey puts the cursor on a cell of the label column and presses
// Cmd+K then the second key, waiting for the rows the filter reads.
func filterByKey(t *testing.T, tt *ui.Tester, tb *TableTab, row int, mods ui.Modifiers, key ui.Key) []int64 {
	t.Helper()
	v := tb.view
	v.clearFilters(-1)
	testutil.WaitFor(t, tt, "the rows unfiltered", func() bool { return !v.loading && len(v.src.Rows) == 3 })
	if err := tt.Click("apple"); err != nil {
		t.Fatal(err)
	}
	v.grid.SelRow, v.grid.selCol = row, 1
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Key(mods, key)
	testutil.WaitFor(t, tt, "the filtered rows", func() bool { return !v.loading && v.where != "" })
	var ids []int64
	for _, r := range v.src.Rows {
		ids = append(ids, r[0].(int64))
	}
	return ids
}

func TestFilterByCellKey(t *testing.T) {
	_, tt, tb := openLabels(t)
	v := tb.view
	cases := []struct {
		name  string
		row   int
		mods  ui.Modifiers
		key   ui.Key
		where string
		ids   []int64
	}{
		{"equals", 0, ui.Cmd, ui.KeyEqual, "= 'apple'", []int64{1}},
		{"not equals", 0, ui.Cmd | ui.Shift, ui.Key1, "<> 'apple'", []int64{2}},
		{"equals NULL", 2, ui.Cmd, ui.KeyEqual, "IS NULL", []int64{3}},
		{"not equals NULL", 2, ui.Cmd | ui.Shift, ui.Key1, "IS NOT NULL", []int64{1, 2}},
	}
	for _, c := range cases {
		ids := filterByKey(t, tt, tb, c.row, c.mods, c.key)
		if !strings.Contains(v.where, c.where) || len(ids) != len(c.ids) || (len(ids) > 0 && ids[0] != c.ids[0]) {
			t.Fatalf("%s: where %q, rows %v; want %q, %v", c.name, v.where, ids, c.where, c.ids)
		}
	}
}

func TestExportKey(t *testing.T) {
	a, tt, _ := openLabels(t)
	if err := tt.Click("apple"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Key(ui.Cmd, ui.KeyX)
	tt.Frame()
	if !ExportOpen(a) {
		t.Fatal("Cmd+K Cmd+X did not open the export dialog")
	}
}

// Cmd+= alone still zooms a focused grid: the grid leaves it only while
// Cmd+K waits for a second key.
func TestGridZoomKeyWithoutChord(t *testing.T) {
	a, tt, _ := openLabels(t)
	if err := tt.Click("apple"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	before := a.Settings().GridFontSize()
	tt.Key(ui.Cmd, ui.KeyEqual)
	testutil.WaitFor(t, tt, "the grid zoomed", func() bool { return a.Settings().GridFontSize() > before })
}
