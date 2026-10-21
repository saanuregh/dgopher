package dataview

import (
	"strings"
	"testing"

	"dgopher/internal/db"

	"github.com/egoist/mygo/ui"
)

func statsGrid(t TB, src *Source) (*ui.Tester, *Grid) {
	a := NewFakeHost(t)
	g := NewGrid()
	g.edits = newPendingEdits()
	tt := ui.NewTester(func(c *ui.Context) { g.View(c, a, src) }, 800, 300)
	tt.Frame()
	return tt, g
}

func chooseStatsBlock(tt *ui.Tester, g *Grid, rows []int, from, to string) {
	g.selection.Clear()
	for _, r := range rows {
		g.selection.Add(r)
	}
	g.List.Cells = ui.ColumnRange{Cursor: to, Anchor: from}
	tt.Frame()
}

func TestStatsOverBlock(t *testing.T) {
	src := &Source{
		Cols: []db.ColumnInfo{{Name: "n", Type: "INTEGER"}, {Name: "m", Type: "INTEGER"}, {Name: "s", Type: "TEXT"}},
		Rows: [][]any{{int64(1), int64(10), "a"}, {int64(2), nil, "b"}, {int64(3), int64(30), nil}},
	}
	tt, g := statsGrid(t, src)
	chooseStatsBlock(tt, g, []int{0, 1, 2}, "0", "1")
	if got := g.SelectionStats(src); got != "3 rows × 2 columns · Count 5 · Sum 46 · Avg 9.2 · Min 1 · Max 30 · 1 NULL" {
		t.Fatalf("numbers: %q", got)
	}
	chooseStatsBlock(tt, g, []int{0, 1, 2}, "0", "2")
	if got := g.SelectionStats(src); got != "3 rows × 3 columns · Count 5 · Sum 46 · Avg 9.2 · Min 1 · Max 30 · 2 NULL" {
		t.Fatalf("mixed: %q", got)
	}
	chooseStatsBlock(tt, g, []int{0, 1, 2}, "2", "2")
	if got := g.SelectionStats(src); got != "3 rows × 1 column · 1 NULL" {
		t.Fatalf("text: %q", got)
	}
	chooseStatsBlock(tt, g, []int{0}, "0", "1")
	if got := g.SelectionStats(src); got != "1 row × 2 columns · Count 2 · Sum 11 · Avg 5.5 · Min 1 · Max 10" {
		t.Fatalf("one row: %q", got)
	}
	chooseStatsBlock(tt, g, nil, "0", "0")
	g.SelRow = 0
	if got := g.SelectionStats(src); got != "" {
		t.Fatalf("one cell: %q", got)
	}
	g.masked = []bool{false, true}
	chooseStatsBlock(tt, g, []int{0, 1, 2}, "0", "1")
	if got := g.SelectionStats(src); got != "3 rows × 2 columns · Count 3 · Sum 6 · Avg 2 · Min 1 · Max 3 · 1 NULL" {
		t.Fatalf("masked: %q", got)
	}
	g.masked = nil
	tt.Frame()
	g.checkpoint()
	g.setValue(src, 0, 0, int64(100))
	if got := g.SelectionStats(src); got != "3 rows × 2 columns · Count 5 · Sum 145 · Avg 29 · Min 2 · Max 100 · 1 NULL" {
		t.Fatalf("after an edit: %q", got)
	}
}

func TestCalcPerColumn(t *testing.T) {
	src := &Source{
		Cols: []db.ColumnInfo{{Name: "n", Type: "INTEGER"}, {Name: "s", Type: "TEXT"}},
		Rows: [][]any{{int64(4), "x"}, {int64(1), "x"}, {nil, "y"}},
	}
	tt, g := statsGrid(t, src)
	chooseStatsBlock(tt, g, []int{0, 1, 2}, "0", "1")
	got := g.calc(src)
	if len(got) != 2 || got[0].col != 0 || got[1].col != 1 {
		t.Fatalf("columns: %+v", got)
	}
	if n := got[0]; n.count != 2 || n.nulls != 1 || !n.numeric || n.sum != 5 || n.min != 1 || n.max != 4 {
		t.Fatalf("n: %+v", n)
	}
	if s := got[1]; s.count != 3 || s.numeric || s.distinct != 2 || s.mode != "x" || s.modeCount != 2 {
		t.Fatalf("s: %+v", s)
	}
}

func BenchmarkStatusFrameAllSelected(b *testing.B) {
	src := &Source{Cols: []db.ColumnInfo{{Name: "n", Type: "INTEGER"}, {Name: "m", Type: "INTEGER"}}}
	for i := range 100_000 {
		src.Rows = append(src.Rows, []any{int64(i), int64(i % 7)})
	}
	tt, g := statsGrid(b, src)
	for i := range src.Rows {
		g.selection.Add(i)
	}
	g.List.Cells.All = true
	tt.Frame()
	if g.SelectionStats(src) == "" {
		b.Fatal("no stats")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		g.SelectionStats(src)
	}
}

// The status follows a selection the grid changes itself, with as many
// rows as before, and a cell edited again.
func TestStatsFollowGridChanges(t *testing.T) {
	tt, g, src := selectionGrid(t)
	if err := tt.Click("11"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ClickWith(ui.Shift, "12"); err != nil {
		t.Fatal(err)
	}
	before := g.SelectionStats(src)
	order := g.ViewOrder(src)
	g.selectWhere(order, func(r int) bool { return r == 3 || r == 4 })
	if after := g.SelectionStats(src); after == before || !strings.Contains(after, "Sum 29") {
		t.Fatalf("rows 3 and 4 (14+15) chosen by the grid: status %q, was %q", after, before)
	}
	g.setValue(src, 3, 0, int64(100))
	first := g.SelectionStats(src)
	g.setValue(src, 3, 0, int64(200))
	if second := g.SelectionStats(src); second == first || !strings.Contains(second, "Sum 215") {
		t.Fatalf("cell edited again: status %q, was %q", second, first)
	}
}

// Record view leaves the choice as it is from frame to frame, so the stats
// it caches stay cached.
func TestRecordViewKeepsChoice(t *testing.T) {
	tt, g, _ := selectionGrid(t)
	if err := tt.Click("aa"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ClickWith(ui.Shift, "n2"); err != nil {
		t.Fatal(err)
	}
	g.mode = viewRecord
	tt.Frame()
	gen, cells := g.selGen, g.List.Cells
	tt.Frame()
	tt.Frame()
	if g.selGen != gen || g.List.Cells != cells {
		t.Fatalf("record view frames: selGen %d → %d, cells %+v → %+v", gen, g.selGen, cells, g.List.Cells)
	}
}
