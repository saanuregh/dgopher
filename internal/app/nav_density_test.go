package app

import (
	"testing"

	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// The navigator's rows are as dense as DBeaver's, about 24 high, and a
// query file takes one of them: its connection shows on its line.
func TestNavigatorRowsAreCompact(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	if err := tt.Click("Try the sample"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "sample editor", func() bool { return a.tabs().Len() == 1 })
	q := a.tabs().At(0).(*query.Tab)
	testutil.WaitFor(t, tt, "the tables", func() bool { return tt.HasText("order_items") && tt.HasText("query-1.sql") })
	top := func(text string) float32 {
		t.Helper()
		r, ok := tt.Find(text)
		if !ok {
			t.Fatalf("%q does not show: %q", text, tt.Texts())
		}
		return r.Y + r.H/2
	}
	tt.Frame()
	if pitch := top("order_items") - top("customers"); pitch < 23 || pitch > 25 {
		t.Errorf("table rows are %v apart, want 24", pitch)
	}
	// The row under the one query file is the next section's.
	if pitch := top("Dashboards") - top("query-1.sql"); pitch > 27 {
		t.Errorf("the query file's row is %v high: its connection is not on its line", pitch)
	}
	// The row reads as the file and its connection, on one line.
	row, ok := tt.Access("query-1.sql " + q.Conn.Config.Name)
	if !ok || row.Bounds.H < 23 || row.Bounds.H > 25 {
		t.Errorf("the query file's row with its connection %s: found %v, %v high, want 24", q.Conn.Config.Name, ok, row.Bounds.H)
	}
}
