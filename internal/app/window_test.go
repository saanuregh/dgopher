package app

import (
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// A tab moved to a new window shows there alone, without the sidebar;
// dialogs show in the window used; a tab of another window comes forward
// there; closing the window brings its tabs back, as does its last tab
// leaving it; a connection that goes closes its tabs in every window.
func TestWindows(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	main := ui.NewTester(a.windowView(a.main), 1000, 700)
	a.NewQueryTab(cn, "", "SELECT 1")
	a.NewQueryTab(cn, "", "SELECT 2")
	testutil.WaitFor(t, main, "the editors", func() bool { return a.main.tabs.Len() == 2 })
	first, second := a.main.tabs.At(0), a.main.tabs.At(1)

	a.moveToWindow(second, nil)
	if len(a.windows) != 2 || a.main.tabs.Len() != 1 {
		t.Fatalf("windows %d, main's tabs %d", len(a.windows), a.main.tabs.Len())
	}
	w := a.windows[1]
	other := ui.NewTester(a.windowView(w), 800, 600)
	a.lastFocused = w
	other.Frame()
	if other.HasText("Projects") || !other.HasText(second.Title()) {
		t.Fatalf("the other window shows %q", other.Texts())
	}

	a.openSettings()
	other.Frame()
	main.Frame()
	if !other.HasText("Settings") || main.HasText("Customize Keyboard Shortcuts…") {
		t.Fatal("the settings show in a window not used")
	}
	a.settingsOpen = false

	a.lastFocused = a.main
	if !a.ActivateTab(func(t widgets.Tab) bool { return t == second }) || a.window != w || w.ActiveTab() != second {
		t.Fatal("the other window's tab did not come forward there")
	}

	a.closeWindow(w)
	if len(a.windows) != 1 || a.main.tabs.Len() != 2 {
		t.Fatalf("after closing: windows %d, main's tabs %d", len(a.windows), a.main.tabs.Len())
	}
	a.moveToWindow(first, nil)
	a.moveToWindow(first, a.main)
	main.Frame()
	if len(a.windows) != 1 || a.main.tabs.Len() != 2 {
		t.Fatal("a window kept without tabs")
	}

	// A window's only tab put in its place stays there, the window too.
	a.moveToWindow(first, nil)
	w = a.windows[1]
	a.SwitchConnection(first.(*query.Tab), "lite")
	main.Frame()
	if len(a.windows) != 2 || w.tabs.Len() != 1 || w.tabs.At(0) == first {
		t.Fatalf("after switching: windows %d, its tabs %d", len(a.windows), w.tabs.Len())
	}
	first = w.tabs.At(0)

	// Brought forward while another window draws, a tab stays in its
	// window, and the window drawn stays the one drawn.
	a.drawing, a.window = a.main, a.main
	if !a.ActivateTab(func(t widgets.Tab) bool { return t == first }) || a.window != a.main {
		t.Fatal("bringing a tab forward moved the window drawn")
	}
	a.drawing = nil

	a.closeConnection(cn)
	main.Frame()
	if a.main.tabs.Len() != 0 || len(a.windows) != 1 {
		t.Fatalf("after the connection went: main's tabs %d, windows %d", a.main.tabs.Len(), len(a.windows))
	}
}
