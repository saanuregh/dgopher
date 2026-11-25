package query

import (
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// dockAndFilter are where the dock's button that hides the results and
// the grid's WHERE box show.
func dockAndFilter(t *testing.T, tt *ui.Tester) (dock, filter ui.Rect) {
	t.Helper()
	tt.Frame()
	o, okO := tt.Access("Hide Results")
	f, okF := tt.Access("Filter")
	if !okO || !okF {
		t.Fatalf("Hide Results shows %v, the WHERE box %v", okO, okF)
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
		t.Errorf("the dock's buttons at %v are not in the row of the WHERE box at %v", output, filter)
	}
	if output.X < filter.X+filter.W {
		t.Errorf("the dock's buttons at %v do not end the toolbar the WHERE box at %v is in", output, filter)
	}
}

// Several results keep their row of tabs, over the grid's toolbar.
func TestTwoResultsKeepTheirStrip(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "select 1 as a where 1 = 1;\nselect 2 as b where 2 = 2;")
	runWith(t, tt, q, "select 1", RunScript)
	output, filter := dockAndFilter(t, tt)
	if output.Y+output.H > filter.Y {
		t.Errorf("the dock's buttons at %v are not in a row over the WHERE box at %v", output, filter)
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
		t.Errorf("the dock's buttons at %v are not in a row over the WHERE box at %v", output, filter)
	}
}

// Output is a tab after the results', not a button at the far end: one
// more view of the same pane. Beside one result it follows that result's
// tab; with several it ends their row, by name.
func TestOutputFollowsTheResultTabs(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "select 1 as a where 1 = 1;\nselect 2 as b where 2 = 2;")
	runWith(t, tt, q, "select 1", RunStatement)
	tt.Frame()
	output, filter := named(t, tt, "Output"), named(t, tt, "Filter")
	if output.X > filter.X {
		t.Errorf("beside one result, Output at %v is not before the WHERE box at %v", output, filter)
	}
	runWith(t, tt, q, "select 1", RunScript)
	tt.Frame()
	output = named(t, tt, "Output")
	if !tt.HasText("Output") || output.Y+output.H > named(t, tt, "Filter").Y {
		t.Errorf("with two results, Output at %v is not a named tab of their row: %q", output, tt.Texts())
	}
	if err := tt.Click("Output"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if q.current() != nil {
		t.Error("Output did not come in front")
	}
}

// The results answer to the keys: the next and the one before, around
// past the Output, and every one is in a list by name, where the row of
// tabs has no room for some.
func TestResultsStepByKey(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "select 1 as a where 1 = 1;\nselect 2 as b where 2 = 2;")
	runWith(t, tt, q, "select 1", RunScript)
	q.resultIdx = 0
	tt.Frame()
	for _, want := range []int{1, 2, 0} { // the second, the Output, the first again
		tt.Key(ui.Cmd|ui.Alt, ui.KeyPageDown)
		tt.Frame()
		if q.resultIdx != want {
			t.Fatalf("after the key, result %d is in front, want %d", q.resultIdx, want)
		}
	}
	tt.Key(ui.Cmd|ui.Alt, ui.KeyPageUp)
	tt.Frame()
	if q.current() != nil {
		t.Errorf("the key back from the first result shows %d, not the Output", q.resultIdx)
	}
	if _, ok := tt.Access("All Results"); !ok {
		t.Error("several results have no list of them all")
	}
}

// F8 puts the caret where the statement failed, as Go to Error does.
func TestGoToErrorByKey(t *testing.T) {
	text := "select 1;\nselect * from missing_table;"
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, text)
	runWith(t, tt, q, "select *", RunStatement)
	tt.Frame()
	testutil.SetCaret(tt, q.Editor, 0)
	tt.Key(0, ui.KeyF8)
	tt.Frame()
	if at := q.Editor.Caret(); at < len("select 1;\n") {
		t.Errorf("the caret is at %d, not in the statement that failed", at)
	}
}
