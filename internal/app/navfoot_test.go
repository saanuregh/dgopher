package app

import (
	"testing"
	"time"

	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// The sidebar's foot shows the memory the app uses, as the system counts
// it, with Go's part on hover.
func TestSidebarShowsMemoryUsed(t *testing.T) {
	a, tt, _ := filterApp(t)
	tt.Frame()
	if !a.nav.memory.ok {
		t.Skip("this system cannot be asked")
	}
	foot, ok := tt.Access("Memory used")
	// Megabytes, or gigabytes when the tests run together in one process.
	if !ok || !testutil.HasTextContaining(tt, " MB") && !testutil.HasTextContaining(tt, " GB") {
		t.Fatalf("the sidebar shows no memory: %q", tt.Texts())
	}
	// At the sidebar's foot, under the tree.
	tree, _ := tt.Access("Navigator")
	if foot.Bounds.Y < tree.Bounds.Y+tree.Bounds.H-1 || foot.Bounds.Y+foot.Bounds.H < 760-12 {
		t.Errorf("the memory is at %v, the tree at %v: not at the sidebar's foot", foot.Bounds, tree.Bounds)
	}
	if m := a.nav.memory; m.used < 1<<20 || m.inGo == 0 || m.most < m.used {
		t.Errorf("the memory read is %d bytes, the most %d, of which Go's %d", m.used, m.most, m.inGo)
	}
}

// A click on the memory shows what is known of it: the most it has been,
// Go's part and the rest, the rows each tab holds, the connections open,
// and a button that frees what is not needed.
func TestMemoryDetails(t *testing.T) {
	a, tt, cn := filterApp(t)
	tt.Frame()
	if !a.nav.memory.ok {
		t.Skip("this system cannot be asked")
	}
	for _, rows := range []string{"37", "120"} {
		q := newAppEditor(t, a, tt, cn, "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < "+rows+") SELECT i FROM n;\n")
		q.Run(query.RunStatement)
		testutil.WaitFor(t, tt, "the "+rows+" rows", func() bool { return resultRows(tt, q, -1) })
	}
	// The tab holding the most first.
	if held := a.rowsHeld(); len(held) != 2 || held[0].rows != 120 || held[1].rows != 37 {
		t.Fatalf("the tabs hold %+v, want 120 rows then 37", held)
	}
	if err := tt.Click("Memory used"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, want := range []string{"Highest this run", "Go's", "Outside Go: databases, drawing", "Rows held in results", "157 in 2 tabs", "Open connections", "Free Memory"} {
		if _, ok := tt.Find(want); !ok {
			t.Errorf("the details do not show %q: %q", want, tt.Texts())
		}
	}
	read := a.nav.memory.at
	tt.Advance(time.Second)
	if err := tt.Click("Free Memory"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.nav.memory.at.After(read) || !a.nav.memory.shown {
		t.Errorf("Free Memory read the memory again: %v, and left the details open: %v", a.nav.memory.at.After(read), a.nav.memory.shown)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.nav.memory.shown {
		t.Error("Esc left the details open")
	}
}
