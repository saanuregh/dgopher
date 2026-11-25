package app

import (
	"testing"
	"time"

	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// The navigator's rows are as dense as DBeaver's, about 24 high, and a
// query file takes one of them: its connection shows on its line.
func TestNavigatorRowsAreCompact(t *testing.T) {
	a := newTestApp(t)
	filesOfEveryKind(t, a)
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

// A row that never opens, as a query file, starts under its heading's
// name, with no room kept for an arrow it will never have: the room goes
// to the name.
func TestNavigatorRowsStartWithoutAGutter(t *testing.T) {
	a, tt, cn := filterApp(t)
	a.nav.expand(navNode{kind: nodeConn, conn: cn.Config.ID})
	a.nav.expand(navNode{kind: nodeFolder, conn: cn.Config.ID, schema: cn.DefaultSchema})
	tt.Frame()
	tt.Frame()
	heading, okH := tt.Find("Queries")
	file, okF := tt.Find("cleanup.sql")
	if !okH || !okF {
		t.Fatalf("the heading shows %v, the file %v: %q", okH, okF, tt.Texts())
	}
	// Its connection's dot is under the heading's first letter, its name
	// a dot's width after.
	if file.X > heading.X+16 {
		t.Errorf("the file's name starts at %v, the heading's at %v: more than a dot's room to its right", file.X, heading.X)
	}
	if file.X > 40 {
		t.Errorf("the file's name starts %v px into the sidebar, want 40 at most", file.X)
	}
	if table, _ := tt.Find("order_items"); table.X > 70 {
		t.Errorf("a table's name starts %v px into the sidebar, want 70 at most", table.X)
	}
}

// A heading's count keeps its place as the pointer comes over the row:
// the button that makes another fades in beside it, in room it had all
// along, where it used to take the count's place.
func TestHeadingCountKeepsItsPlace(t *testing.T) {
	_, tt, _ := filterApp(t)
	tt.Frame()
	heading, _ := tt.Find("Queries")
	count := func() ui.Rect {
		// The count of the two query files, on the heading's line.
		for _, n := range tt.AccessAll() {
			if n.Name == "2" && n.Bounds.Y >= heading.Y-4 && n.Bounds.Y <= heading.Y+heading.H {
				return n.Bounds
			}
		}
		r, _ := tt.Find("2")
		return r
	}
	before := count()
	tt.Move(heading.X+40, heading.Y+heading.H/2)
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	if after := count(); after != before || before.W == 0 {
		t.Errorf("the count moved from %v to %v as the pointer came over its row", before, after)
	}
	if _, ok := tt.Find("New SQL Editor"); !ok {
		t.Errorf("the row pointed at offers no New SQL Editor: %q", tt.Texts())
	}
}
