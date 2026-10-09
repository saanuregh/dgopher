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
// leaving it; disconnecting closes the tabs of every window.
func TestWindows(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	main := ui.NewTester(a.windowView(a.main), 1000, 700)
	a.NewQueryTab(cn, "", "SELECT 1")
	a.NewQueryTab(cn, "", "SELECT 2")
	testutil.WaitFor(t, main, "the editors", func() bool { return len(a.main.tabs) == 2 })
	first, second := a.main.tabs[0], a.main.tabs[1]

	a.moveToWindow(second, nil)
	if len(a.windows) != 2 || len(a.main.tabs) != 1 {
		t.Fatalf("windows %d, main's tabs %d", len(a.windows), len(a.main.tabs))
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
	if len(a.windows) != 1 || len(a.main.tabs) != 2 {
		t.Fatalf("after closing: windows %d, main's tabs %d", len(a.windows), len(a.main.tabs))
	}
	a.moveToWindow(first, nil)
	a.moveToWindow(first, a.main)
	main.Frame()
	if len(a.windows) != 1 || len(a.main.tabs) != 2 {
		t.Fatal("a window kept without tabs")
	}

	// A window's only tab put in its place stays there, the window too.
	a.moveToWindow(first, nil)
	w = a.windows[1]
	a.SwitchConnection(first.(*query.Tab), "lite")
	main.Frame()
	if len(a.windows) != 2 || len(w.tabs) != 1 || w.tabs[0] == first {
		t.Fatalf("after switching: windows %d, its tabs %d", len(a.windows), len(w.tabs))
	}
	first = w.tabs[0]

	// Brought forward while another window draws, a tab stays in its
	// window, and the window drawn stays the one drawn.
	a.drawing, a.window = a.main, a.main
	if !a.ActivateTab(func(t widgets.Tab) bool { return t == first }) || a.window != a.main {
		t.Fatal("bringing a tab forward moved the window drawn")
	}
	a.drawing = nil

	a.disconnect(cn)
	main.Frame()
	if len(a.main.tabs) != 0 || len(a.windows) != 1 {
		t.Fatalf("after disconnecting: main's tabs %d, windows %d", len(a.main.tabs), len(a.windows))
	}
}
