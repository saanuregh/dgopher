package query

import (
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// The results dock hides with ⌘J down to its bar of results, shows again
// for ⌘J, a run or the focus, and fills the tab while maximized; the
// state is the layout's, which every editor shares.
func TestResultsDockHidesAndMaximizes(t *testing.T) {
	a, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "SELECT 42 AS answer")
	run := func() {
		t.Helper()
		q.Run(RunStatement)
		testutil.WaitFor(t, tt, "the result", func() bool { return !q.Running && len(q.results) > 0 })
		tt.Frame()
	}
	run()
	shown, ok := tt.Find("answer")
	if !ok {
		t.Fatal("the result does not show")
	}

	tt.Key(ui.Cmd, ui.KeyJ)
	tt.Frame()
	if _, shows := tt.Find("answer"); !(a.Layout().Results == widgets.ResultsHidden) || shows {
		t.Fatalf("⌘J: hidden %v, the result shows %v", (a.Layout().Results == widgets.ResultsHidden), shows)
	}
	if _, ok := tt.Find("Result 1"); !ok {
		t.Error("hidden, the dock's bar of results is gone")
	}
	tt.Key(ui.Cmd, ui.KeyJ)
	tt.Frame()
	if _, shows := tt.Find("answer"); (a.Layout().Results == widgets.ResultsHidden) || !shows {
		t.Fatalf("⌘J again: hidden %v", (a.Layout().Results == widgets.ResultsHidden))
	}

	tt.Key(ui.Cmd, ui.KeyJ)
	run()
	if a.Layout().Results == widgets.ResultsHidden {
		t.Error("a run left the results hidden")
	}

	tt.Key(ui.Cmd, ui.KeyJ)
	tt.Frame()
	tt.Key(ui.Cmd|ui.Shift, ui.KeyJ)
	tt.Frame()
	if a.Layout().Results == widgets.ResultsHidden {
		t.Error("⌘⇧J left the results hidden")
	}

	if err := tt.Click("Maximize Results"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if r, ok := tt.Find("answer"); !(a.Layout().Results == widgets.ResultsMaximized) || !ok || r.Y >= shown.Y-100 {
		t.Fatalf("maximized %v: the result at %v, %v before", (a.Layout().Results == widgets.ResultsMaximized), r.Y, shown.Y)
	}
	if err := tt.Click("Restore Results"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if r, _ := tt.Find("answer"); (a.Layout().Results == widgets.ResultsMaximized) || r.Y != shown.Y {
		t.Errorf("restored: maximized %v, the result at %v, want %v", (a.Layout().Results == widgets.ResultsMaximized), r.Y, shown.Y)
	}
	if err := tt.Click("Hide Results"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Show Results"); err != nil {
		t.Fatal(err)
	}
	if a.Layout().Results == widgets.ResultsHidden {
		t.Error("Show Results left them hidden")
	}

	// Output is a button at the right, which shows the editor's log, and
	// shows the results hidden.
	tt.Key(ui.Cmd, ui.KeyJ)
	tt.Frame()
	if err := tt.Click("Output"); err != nil {
		t.Fatal(err)
	}
	if q.resultIdx != len(q.results) || (a.Layout().Results == widgets.ResultsHidden) {
		t.Errorf("Output: shows %d of %d results, hidden %v", q.resultIdx, len(q.results), (a.Layout().Results == widgets.ResultsHidden))
	}
}
