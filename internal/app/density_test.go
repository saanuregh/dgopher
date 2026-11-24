package app

import (
	"testing"

	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// An editor and its one result, in a window of 1280 by 800, show fifteen
// rows of the result under the editor: the bars around them leave room.
func TestSampleShowsFifteenResultRows(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	if err := tt.Click("Try the sample"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "sample editor", func() bool { return a.tabs().Len() == 1 })
	q := a.tabs().At(0).(*query.Tab)
	testutil.SetCaret(tt, q.Editor, 80)
	q.Run(query.RunStatement)
	testutil.WaitFor(t, tt, "the sample query's 8 rows", func() bool { return resultRows(tt, q, 8) })
	tt.Frame()
	grid, ok := tt.Access("Results")
	if !ok {
		t.Fatal("no grid of results")
	}
	const header = 21
	row := dataview.RowHeight(a.settings.GridFontSize())
	if rows := (grid.Bounds.H - header) / row; rows < 14.5 {
		t.Errorf("the grid, %v high, shows %.1f rows of %v, want 15", grid.Bounds.H, rows, row)
	}
}
