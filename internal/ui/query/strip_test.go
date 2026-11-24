package query

import (
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// dockAndFilter are where the dock's Output button and the grid's WHERE
// box show.
func dockAndFilter(t *testing.T, tt *ui.Tester) (output, filter ui.Rect) {
	t.Helper()
	tt.Frame()
	o, okO := tt.Access("Output")
	f, okF := tt.Access("Filter")
	if !okO || !okF {
		t.Fatalf("Output shows %v, the WHERE box %v", okO, okF)
	}
	return o.Bounds, f.Bounds
}

// One result takes no row for its tab: the tab starts its grid's toolbar,
// and the dock's buttons end it.
func TestOneResultSharesTheGridToolbar(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "SELECT 42 AS answer")
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the result", func() bool { return !q.Running && len(q.results) == 1 })
	output, filter := dockAndFilter(t, tt)
	if mid := output.Y + output.H/2; mid < filter.Y || mid > filter.Y+filter.H {
		t.Errorf("Output at %v is not in the row of the WHERE box at %v", output, filter)
	}
	if output.X < filter.X+filter.W {
		t.Errorf("Output at %v does not end the toolbar the WHERE box at %v is in", output, filter)
	}
}

// Several results keep their row of tabs, over the grid's toolbar.
func TestTwoResultsKeepTheirStrip(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "select 1 as a where 1 = 1;\nselect 2 as b where 2 = 2;")
	runWith(t, tt, q, "select 1", RunScript)
	output, filter := dockAndFilter(t, tt)
	if output.Y+output.H > filter.Y {
		t.Errorf("Output at %v is not in a row over the WHERE box at %v", output, filter)
	}
}

// A narrow pane, as one of a split, keeps the row of tabs for its one
// result: sharing the toolbar would leave no room for the WHERE box.
func TestNarrowPaneKeepsTheStrip(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 700, 700, "SELECT 42 AS answer")
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the result", func() bool { return !q.Running && len(q.results) == 1 })
	tt.Frame()
	output, filter := dockAndFilter(t, tt)
	if output.Y+output.H > filter.Y {
		t.Errorf("Output at %v is not in a row over the WHERE box at %v", output, filter)
	}
}
