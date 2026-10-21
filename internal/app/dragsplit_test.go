package app

import (
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/ui/panes"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// A tab dragged onto a side of the editor area splits it there, the tab
// moving into the new group.
func TestDragTabSplits(t *testing.T) {
	a, tt := tabsApp(t)
	f := testutil.Settled(t, tt, "b")
	x, y := f.X+f.W/2, f.Y+f.H/2
	tt.Press(x, y)
	const toX, toY = 1150, 400
	for i := 1; i <= 12; i++ {
		tt.Move(x+(toX-x)*float32(i)/12, y+(toY-y)*float32(i)/12)
		tt.Frame()
	}
	// Over a zone, the zones stay up.
	for range 4 {
		tt.Move(toX, toY+1)
		tt.Frame()
		if !tt.HasText("Split Left") {
			t.Fatalf("no drop zones while dragging: %q", tt.Texts())
		}
	}
	tt.Release(toX, toY)
	tt.Frame()
	tt.Frame()
	if n := a.window.panes.Len(); n != 2 {
		t.Fatalf("%d groups after the drop", n)
	}
	if tt.HasText("Split Left") {
		t.Fatal("the drop zones stayed after the drop")
	}
	right := a.window.panes.Groups()[1]
	if right.Tabs.Len() != 1 || right.Tabs.Active().Title() != "b" {
		t.Fatalf("right group holds %d tabs", right.Tabs.Len())
	}

	// Onto the other group's middle: the tab moves there.
	drag(tt, testutil.Settled(t, tt, "c"), 1000, 400)
	if right.Tabs.Len() != 2 || right.Tabs.Active().Title() != "c" {
		t.Fatalf("right group holds %d tabs", right.Tabs.Len())
	}
}

// drag drags what is at f to (toX, toY), holding it there a few frames.
func drag(tt *ui.Tester, f ui.Rect, toX, toY float32) {
	x, y := f.X+f.W/2, f.Y+f.H/2
	tt.Press(x, y)
	for i := 1; i <= 12; i++ {
		tt.Move(x+(toX-x)*float32(i)/12, y+(toY-y)*float32(i)/12)
		tt.Frame()
	}
	for range 3 {
		tt.Move(toX, toY+1)
		tt.Frame()
	}
	tt.Release(toX, toY)
	tt.Frame()
	tt.Frame()
}

// Closing a group's last tab from its tab bar takes the group away in the
// frame drawing it, whichever side the group is on.
func TestCloseLastTabOfGroup(t *testing.T) {
	for _, side := range []float32{1150, 300} {
		a, tt := tabsApp(t)
		drag(tt, testutil.Settled(t, tt, "b"), side, 400)
		if a.window.panes.Len() != 2 {
			t.Fatalf("%d groups", a.window.panes.Len())
		}
		if err := tt.Click("Close b"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		tt.Frame()
		if n := a.window.panes.Len(); n != 1 {
			t.Fatalf("%d groups after closing", n)
		}
	}
}

// In a split, each group's status bar tells of its own tab: two editors
// on two connections, side by side, show both, each under its own.
func TestEachGroupHasItsStatus(t *testing.T) {
	a := newTestApp(t)
	alpha := addConn(a, db.Config{ID: "alpha", Name: "alpha", Engine: db.SQLite, Database: ":memory:", ReadOnly: true})
	beta := addConn(a, db.Config{ID: "beta", Name: "beta", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1400, 800)
	tt.Frame()
	qa := newAppEditor(t, a, tt, alpha, "SELECT 1;")
	qb := newAppEditor(t, a, tt, beta, "SELECT 2;")
	a.splitTab(a.main, a.main.panes.Focused(), panes.Right)
	testutil.WaitFor(t, tt, "the groups", func() bool { return a.main.panes.Len() == 2 })
	for range 3 {
		tt.Frame()
	}
	left, right := a.main.panes.Groups()[0], a.main.panes.Groups()[1]
	if left.Tabs.Active() != widgets.Tab(qa) || right.Tabs.Active() != widgets.Tab(qb) {
		t.Fatal("the editors are not one in each group")
	}
	ro, ok := tt.Find("read-only")
	if !ok || ro.X > 700 {
		t.Fatalf("alpha's read-only is not under the left group: %v %v", ro, ok)
	}
	if n := countTexts(tt, "auto-commit"); n != 2 {
		t.Fatalf("%d commit modes shown, want one per group", n)
	}
}

func countTexts(tt *ui.Tester, s string) int {
	n := 0
	for _, t := range tt.Texts() {
		if t == s {
			n++
		}
	}
	return n
}

// A press on a group focuses it, even where nothing takes the keys.
func TestPressFocusesGroup(t *testing.T) {
	a, tt := tabsApp(t)
	drag(tt, testutil.Settled(t, tt, "b"), 1150, 400)
	left, right := a.main.panes.Groups()[0], a.main.panes.Groups()[1]
	a.main.panes.Focus(right)
	tt.Frame()
	// A plain tab's empty space, far from its title.
	tt.Press(400, 600)
	tt.Release(400, 600)
	tt.Frame()
	if a.main.panes.Focused() != left {
		t.Fatal("the press left the focus on the other group")
	}
}

// Focusing a group by key moves the keys there, to the right as to the
// left: the group the keys leave does not take them back.
func TestFocusGroupByKey(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1400, 800)
	tt.Frame()
	ql := newAppEditor(t, a, tt, cn, "SELECT 1;")
	qr := newAppEditor(t, a, tt, cn, "SELECT 2;")
	a.splitTab(a.main, a.main.panes.Focused(), panes.Right)
	left, right := a.main.panes.Groups()[0], a.main.panes.Groups()[1]
	for _, step := range []struct {
		to *panes.Group
		q  *query.Tab
	}{{left, ql}, {right, qr}, {left, ql}} {
		a.focusGroup(a.main, step.to)
		for range 5 {
			tt.Frame()
		}
		if a.main.panes.Focused() != step.to || !step.q.Editor.HasFocus() {
			t.Fatalf("to group %d: focused %d, its editor has the keys %v", a.main.panes.Index(step.to), a.main.panes.Index(a.main.panes.Focused()), step.q.Editor.HasFocus())
		}
	}
}

// A tab let go past the window's edge moves to a new window of its own,
// as in a browser; let go inside, over nothing taking it, it stays.
func TestDragTabOutOfWindow(t *testing.T) {
	a, tt := tabsApp(t)
	drag(tt, testutil.Settled(t, tt, "b"), 600, 690)
	if len(a.windows) != 1 || a.main.tabCount() != 4 {
		t.Fatalf("let go inside: %d windows, %d tabs in the main one", len(a.windows), a.main.tabCount())
	}
	drag(tt, testutil.Settled(t, tt, "b"), 600, 760)
	if len(a.windows) != 2 || a.main.tabCount() != 3 {
		t.Fatalf("let go below the window: %d windows, %d tabs in the main one", len(a.windows), a.main.tabCount())
	}
	if w := a.windows[1]; w.tabCount() != 1 || w.tabs().Active().Title() != "b" {
		t.Fatalf("the new window holds %d tabs", w.tabCount())
	}
}
