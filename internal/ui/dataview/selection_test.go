package dataview

import (
	"slices"
	"strings"
	"testing"

	"dgopher/internal/db"

	"github.com/egoist/mygo/ui"
)

// selectionGrid is an editable grid of five rows, ids 11 to 15 and names
// aa to ee, shown in a tester.
func selectionGrid(t *testing.T) (*ui.Tester, *Grid, *Source) {
	t.Helper()
	a := NewFakeHost(t)
	src := &Source{
		Cols: []db.ColumnInfo{{Name: "id", Type: "INTEGER"}, {Name: "name", Type: "TEXT"}, {Name: "note", Type: "TEXT"}},
		Rows: [][]any{{int64(11), "aa", "n1"}, {int64(12), "bb", "n2"}, {int64(13), "cc", "n3"}, {int64(14), "dd", "n4"}, {int64(15), "ee", "n5"}},
	}
	g := NewGrid()
	g.edits = newPendingEdits()
	tt := ui.NewTester(func(c *ui.Context) {
		g.View(c, a, src)
		if a.dialogs.valueEdit != nil {
			valueEditorView(a, c)
		}
	}, 800, 300)
	tt.Frame()
	return tt, g, src
}

func chosenData(g *Grid) []int {
	var out []int
	for k := range g.selection.All() {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func wantChosen(t *testing.T, g *Grid, what string, rows []int, col int) {
	t.Helper()
	if got := chosenData(g); !slices.Equal(got, rows) || g.selCol != col {
		t.Fatalf("%s: rows %v, column %d; want %v, %d", what, got, g.selCol, rows, col)
	}
}

func press(t *testing.T, tt *ui.Tester, text string) (float32, float32) {
	t.Helper()
	r, ok := tt.Find(text)
	if !ok {
		t.Fatalf("no %q", text)
	}
	return r.X + r.W/2, r.Y + r.H/2
}

func TestShiftClickExtends(t *testing.T) {
	tt, g, _ := selectionGrid(t)
	if err := tt.Click("bb"); err != nil {
		t.Fatal(err)
	}
	wantChosen(t, g, "click bb", []int{1}, 1)
	if err := tt.ClickWith(ui.Shift, "dd"); err != nil {
		t.Fatal(err)
	}
	wantChosen(t, g, "Shift-click dd", []int{1, 2, 3}, 1)
}

func TestCmdClickToggles(t *testing.T) {
	tt, g, _ := selectionGrid(t)
	if err := tt.Click("aa"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ClickWith(ui.Cmd, "cc"); err != nil {
		t.Fatal(err)
	}
	wantChosen(t, g, "Cmd-click cc", []int{0, 2}, 1)
	if err := tt.ClickWith(ui.Cmd, "aa"); err != nil {
		t.Fatal(err)
	}
	wantChosen(t, g, "Cmd-click aa again", []int{2}, 1)
}

func TestShiftArrowFromClickedRow(t *testing.T) {
	tt, g, _ := selectionGrid(t)
	if err := tt.Click("bb"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyDown)
	if err := tt.Click("aa"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Shift, ui.KeyDown)
	wantChosen(t, g, "click aa, Shift+Down", []int{0, 1}, 1)
	tt.Key(0, ui.KeyRight)
	wantChosen(t, g, "Right", []int{1}, 2)
}

func TestRightClickDeletesClickedRow(t *testing.T) {
	tt, g, _ := selectionGrid(t)
	if err := tt.Click("bb"); err != nil {
		t.Fatal(err)
	}
	if err := tt.RightClick("ee"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Edit", "Delete Row"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !g.edits.deleted[4] || g.edits.deleted[1] {
		t.Fatalf("right-click ee, Delete Row: deleted %v, want row 4 only", g.edits.deleted)
	}
}

func TestDragOnRowNumbersSelectsRows(t *testing.T) {
	tt, g, _ := selectionGrid(t)
	x, y := press(t, tt, "2")
	_, y4 := press(t, tt, "4")
	tt.Press(x, y)
	tt.Move(x, y+4)
	tt.Move(x, y4)
	tt.Release(x, y4)
	if got := chosenData(g); !slices.Equal(got, []int{1, 2, 3}) || !g.List.Cells.All {
		t.Fatalf("drag on row numbers 2 to 4: rows %v, all columns %v", got, g.List.Cells.All)
	}
}

func TestDragOverCellsSelectsBlock(t *testing.T) {
	tt, g, src := selectionGrid(t)
	x, y := press(t, tt, "aa")
	x2, y2 := press(t, tt, "n3")
	tt.Press(x, y)
	tt.Move(x+4, y+4)
	tt.Move(x2, y2)
	tt.Release(x2, y2)
	if got := chosenData(g); !slices.Equal(got, []int{0, 1, 2}) || !slices.Equal(g.selectedCols(src), []int{1, 2}) {
		t.Fatalf("drag aa to n3: rows %v, columns %v", got, g.selectedCols(src))
	}
}

func TestDoubleClickEdits(t *testing.T) {
	tt, g, _ := selectionGrid(t)
	x, y := press(t, tt, "cc")
	tt.ClickAt(x, y)
	tt.ClickAt(x, y)
	tt.Frame()
	if g.editing == nil || g.editing.row != 2 || g.editing.col != 1 {
		t.Fatalf("double-click cc: editing %+v", g.editing)
	}
}

func TestSelectSubmenu(t *testing.T) {
	tt, g, src := selectionGrid(t)
	if err := tt.Click("bb"); err != nil {
		t.Fatal(err)
	}
	if err := tt.RightClick("bb"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Select", "Select Column"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := chosenData(g); len(got) != 5 || !slices.Equal(g.selectedCols(src), []int{1}) {
		t.Fatalf("Select Column: rows %v, columns %v", got, g.selectedCols(src))
	}
	if err := tt.Click("cc"); err != nil {
		t.Fatal(err)
	}
	if err := tt.RightClick("cc"); err != nil {
		t.Fatal(err)
	}
	tt.ChooseMenuItem("Select", "Select Row")
	tt.Frame()
	if got := chosenData(g); !slices.Equal(got, []int{2}) || !slices.Equal(g.selectedCols(src), []int{0, 1, 2}) {
		t.Fatalf("Select Row: rows %v, columns %v", got, g.selectedCols(src))
	}
	if err := tt.RightClick("aa"); err != nil {
		t.Fatal(err)
	}
	tt.ChooseMenuItem("Select", "Select All")
	tt.Frame()
	if got := chosenData(g); len(got) != 5 || len(g.selectedCols(src)) != 3 {
		t.Fatalf("Select All: rows %v, columns %v", got, g.selectedCols(src))
	}
}

func TestSelectColumnKey(t *testing.T) {
	tt, g, src := selectionGrid(t)
	if err := tt.Click("n2"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeySpace)
	if got := chosenData(g); len(got) != 5 || !slices.Equal(g.selectedCols(src), []int{2}) {
		t.Fatalf("Cmd+Shift+Space: rows %v, columns %v", got, g.selectedCols(src))
	}
	if err := tt.Click("bb"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Alt|ui.Shift, ui.KeySpace)
	if got := chosenData(g); !slices.Equal(got, []int{1}) || len(g.selectedCols(src)) != 3 {
		t.Fatalf("Alt+Shift+Space: rows %v, columns %v", got, g.selectedCols(src))
	}
}

func TestSelectMatchingChangedInvert(t *testing.T) {
	tt, g, src := selectionGrid(t)
	src.Rows[3][2] = "n1" // dd's note as aa's
	tt.Frame()
	if err := tt.RightClick("n2"); err != nil {
		t.Fatal(err)
	}
	tt.CloseMenu()
	if err := tt.Click("aa"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyRight) // the note of aa: n1
	if err := tt.RightClick("n1"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Select", "Select Matching Rows"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := chosenData(g); !slices.Equal(got, []int{0, 3}) {
		t.Fatalf("Select Matching Rows on n1: %v", got)
	}
	g.setValue(src, 4, 1, "zz")
	if err := tt.RightClick("aa"); err != nil {
		t.Fatal(err)
	}
	tt.ChooseMenuItem("Select", "Select Changed Rows")
	tt.Frame()
	if got := chosenData(g); !slices.Equal(got, []int{4}) {
		t.Fatalf("Select Changed Rows: %v", got)
	}
	if err := tt.RightClick("zz"); err != nil {
		t.Fatal(err)
	}
	tt.ChooseMenuItem("Select", "Invert Selection")
	tt.Frame()
	if got := chosenData(g); !slices.Equal(got, []int{0, 1, 2, 3}) {
		t.Fatalf("Invert Selection: %v", got)
	}
}

func TestEscCollapsesThenReverts(t *testing.T) {
	tt, g, src := selectionGrid(t)
	g.setValue(src, 1, 1, "changed")
	tt.Frame()
	if err := tt.Click("changed"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ClickWith(ui.Shift, "n4"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyEscape)
	if got := chosenData(g); !slices.Equal(got, []int{3}) || len(g.selectedCols(src)) != 1 || !g.changed(src, 1, 1) {
		t.Fatalf("Esc on a block: rows %v, columns %v, change kept %v", got, g.selectedCols(src), g.changed(src, 1, 1))
	}
	if err := tt.Click("changed"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyEscape)
	if g.changed(src, 1, 1) {
		t.Fatal("Esc on one changed cell did not revert it")
	}
}

func TestShiftLastRowExtends(t *testing.T) {
	tt, g, _ := selectionGrid(t)
	if err := tt.Click("bb"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyDown)
	if got := chosenData(g); !slices.Equal(got, []int{1, 2, 3, 4}) {
		t.Fatalf("Cmd+Shift+Down from bb: %v", got)
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyUp)
	if got := chosenData(g); !slices.Equal(got, []int{0, 1}) {
		t.Fatalf("then Cmd+Shift+Up: %v", got)
	}
}

// Editing a cell leaves it the only one chosen, as in DBeaver: from F2 at
// the cursor, and from Edit Cell on a cell inside the block.
func TestEditCollapsesToCell(t *testing.T) {
	tt, g, src := selectionGrid(t)
	if err := tt.Click("bb"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ClickWith(ui.Shift, "n4"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyF2)
	if g.editing == nil || chosenData(g) == nil || !slices.Equal(chosenData(g), []int{3}) || !slices.Equal(g.selectedCols(src), []int{2}) || g.SelRow != 3 {
		t.Fatalf("F2 on a block: editing %+v, rows %v, columns %v, cursor row %d", g.editing, chosenData(g), g.selectedCols(src), g.SelRow)
	}
	tt.Key(0, ui.KeyEscape)
	if err := tt.Click("bb"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ClickWith(ui.Shift, "n4"); err != nil {
		t.Fatal(err)
	}
	if err := tt.RightClick("cc"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Edit", "Edit Cell"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if g.editing == nil || !slices.Equal(chosenData(g), []int{2}) || !slices.Equal(g.selectedCols(src), []int{1}) || g.SelRow != 2 {
		t.Fatalf("Edit Cell inside a block: editing %+v, rows %v, columns %v, cursor row %d", g.editing, chosenData(g), g.selectedCols(src), g.SelRow)
	}
}

// The cells of the block, and the row numbers of whole rows chosen, are
// the ones on the selection color, whose muted texts change color for it:
// the table reports them (column 0 is the row numbers, data column n is
// table column n+1).
func TestChosenCellsKnown(t *testing.T) {
	tt, g, _ := selectionGrid(t)
	if err := tt.Click("bb"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ClickWith(ui.Shift, "n4"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		row, col int
		want     bool
	}{{2, 2, true}, {3, 3, true}, {2, 1, false}, {4, 2, false}, {2, 0, false}} {
		if got := g.List.CellSelected(c.row, c.col); got != c.want {
			t.Errorf("block bb..n4: cell (%d, %d) chosen %v", c.row, c.col, got)
		}
	}
	if err := tt.Click("2"); err != nil {
		t.Fatal(err)
	}
	if !g.List.CellSelected(1, 0) || !g.List.CellSelected(1, 1) || g.List.CellSelected(2, 0) {
		t.Error("the row number of row 2 did not choose its whole row")
	}
}

// A double-click on a cell of a grid that cannot be edited shows its value
// rather than editing it.
func TestDoubleClickReadOnlyShowsValue(t *testing.T) {
	tt, g, _ := selectionGrid(t)
	g.edits = nil
	tt.Frame()
	x, y := press(t, tt, "n3")
	tt.ClickAt(x, y)
	tt.ClickAt(x, y)
	tt.Frame()
	if g.editing != nil || !g.ShowValue || g.selCol != 2 || !slices.Equal(chosenData(g), []int{2}) {
		t.Fatalf("double-click n3, read-only: editing %+v, value shown %v, column %d, rows %v", g.editing, g.ShowValue, g.selCol, chosenData(g))
	}
}

// Opening a cell's menu never reads the clipboard, which can block on
// another program (Wayland); "By Clipboard Value" reads it once chosen.
func TestCellMenuReadsClipboardOnlyWhenChosen(t *testing.T) {
	a := NewFakeHost(t)
	src := &Source{
		Cols: []db.ColumnInfo{{Name: "id", Type: "INTEGER"}, {Name: "name", Type: "TEXT"}},
		Rows: [][]any{{int64(1), "aa"}, {int64(2), "bb"}, {int64(3), "cc"}},
	}
	g := NewGrid()
	tt := ui.NewTester(func(c *ui.Context) { g.View(c, a, src) }, 800, 300)
	tt.Frame()
	a.Clipboard = "cc"
	if err := tt.RightClick("bb"); err != nil {
		t.Fatal(err)
	}
	if a.ClipboardReads != 0 {
		t.Fatalf("opening the menu read the clipboard %d times", a.ClipboardReads)
	}
	if err := tt.ChooseMenuItem("Filter", "By Clipboard Value", "name = clipboard value"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(g.conds) != 1 || g.conds[0].op != "=" || len(g.conds[0].vals) != 1 || g.conds[0].vals[0] != "cc" {
		t.Fatalf("name = clipboard value: conditions %+v", g.conds)
	}
}

// A row number has a menu for whole rows: it chooses its row with every
// column, and leaves out what acts on one column.
func TestRowNumberMenu(t *testing.T) {
	tt, g, src := selectionGrid(t)
	if err := tt.RightClick("2"); err != nil {
		t.Fatal(err)
	}
	items := tt.Menu()
	if items == nil {
		t.Fatal("no menu on a row number")
	}
	for _, want := range []string{"Copy", "Copy with Header", "Select", "Edit"} {
		if !slices.Contains(items, want) {
			t.Errorf("row number menu lacks %q: %q", want, items)
		}
	}
	for _, not := range []string{"Edit Value…", "Filter"} {
		if slices.Contains(items, not) {
			t.Errorf("row number menu has the column item %q", not)
		}
	}
	tt.CloseMenu()
	if got := chosenData(g); !slices.Equal(got, []int{1}) || len(g.selectedCols(src)) != 3 {
		t.Fatalf("right-click row number 2: rows %v, columns %v", got, g.selectedCols(src))
	}

	// Anywhere in the row number's cell, not only on its digits.
	r, _ := tt.Find("4")
	tt.RightClickAt(r.X+r.W+6, r.Y+r.H/2)
	if tt.Menu() == nil {
		t.Fatal("no menu beside the digits of a row number")
	}
	tt.CloseMenu()
	if got := chosenData(g); !slices.Equal(got, []int{3}) {
		t.Fatalf("right-click beside row number 4: rows %v", got)
	}
}

// While a drag chooses cells, the status bar's sums and the Calc panel keep
// what they showed; they follow the new block once the drag ends.
func TestStatsWaitForDragEnd(t *testing.T) {
	tt, g, src := selectionGrid(t)
	if err := tt.Click("11"); err != nil {
		t.Fatal(err)
	}
	before, calcBefore := g.SelectionStats(src), len(g.calc(src))
	x, y := press(t, tt, "11")
	_, y13 := press(t, tt, "13")
	tt.Press(x, y)
	tt.Move(x, y+6)
	tt.Move(x, y13)
	if got := chosenData(g); !slices.Equal(got, []int{0, 1, 2}) {
		t.Fatalf("dragged 11 to 13: rows %v", got)
	}
	if got := g.SelectionStats(src); got != before {
		t.Fatalf("during the drag the sums changed to %q", got)
	}
	if got := len(g.calc(src)); got != calcBefore {
		t.Fatalf("during the drag the Calc panel changed: %d columns", got)
	}
	tt.Release(x, y13)
	if got := g.SelectionStats(src); !strings.Contains(got, "Sum 36") {
		t.Fatalf("after the drag: %q, want Sum 36 (11+12+13)", got)
	}
}
