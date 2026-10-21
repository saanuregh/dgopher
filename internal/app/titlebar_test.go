package app

import (
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// The title bar is the sidebar's and the tabs' top rows: they hide and
// show the sidebar, open the palette and hold the app's menus; without the
// system's menu bar, the window takes the menus' first keys.
func TestTitleBar(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	a.AddTab(query.New(a, cn, "", "q", "SELECT 1;"))
	tt.Frame()
	testutil.Snapshot(t, tt, "title-bar")

	if err := tt.Click("Menu"); err != nil {
		t.Fatal(err)
	}
	if err := testutil.ChooseMenu(tt, "Help", "Keyboard Shortcuts"); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	if !a.shortcutsOpen {
		t.Fatal("Keyboard Shortcuts did not open the list")
	}
	tt.Key(0, ui.KeyEscape)

	if err := tt.Click("Search"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.palette == nil || a.palette.tables {
		t.Fatalf("the search opened %+v", a.palette)
	}
	tt.Key(0, ui.KeyEscape)
	if a.palette != nil {
		t.Fatal("Escape left the palette open")
	}

	if err := tt.Click("Hide Sidebar"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Show Sidebar"); err != nil || a.layout.SidebarHidden {
		t.Fatalf("the sidebar did not come back: %v", err)
	}
	tt.Frame()

	if !nativeMenuBar {
		tt.Key(ui.Cmd, ui.KeyB)
		tt.Frame()
		if !a.layout.SidebarHidden {
			t.Fatal("the first key of Toggle Sidebar did nothing")
		}
	}
}

// ⌘/ in the editor comments out the line, and leaves the list of keys,
// which F1 opens, shut.
func TestCmdSlashCommentsInEditor(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := query.New(a, cn, "", "q", "SELECT 1;")
	a.AddTab(q)
	q.Editor.WantFocus = true
	tt.Frame()
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeySlash)
	tt.Frame()
	if a.shortcutsOpen || q.Editor.Text() != "-- SELECT 1;" {
		t.Fatalf("⌘/: list of keys open %v, text %q", a.shortcutsOpen, q.Editor.Text())
	}
}

// ⌘⌥/ in the editor puts the line in a block comment.
func TestCmdAltSlashBlockCommentsInEditor(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := query.New(a, cn, "", "q", "SELECT 1;")
	a.AddTab(q)
	q.Editor.WantFocus = true
	tt.Frame()
	tt.Frame()
	tt.Key(ui.Cmd|ui.Alt, ui.KeySlash)
	tt.Frame()
	if text := q.Editor.Text(); !strings.HasPrefix(text, "/*") || !strings.Contains(text, "*/") {
		t.Fatalf("⌘⌥/: text %q", text)
	}
}

// Where the app draws the window's buttons, as on Windows, they end the
// tabs' row at the window's right edge, maximize turning to restore as
// the window maximizes; elsewhere the system's stay.
func TestWindowButtons(t *testing.T) {
	own := drawsWindowButtons // true on Windows
	t.Cleanup(func() { drawsWindowButtons = own })
	drawsWindowButtons = false
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()
	if tt.HasText("Minimize") {
		t.Fatal("window buttons drawn where the system draws them")
	}
	drawsWindowButtons = true
	tt.Frame()
	var right float32
	for _, label := range []string{"Minimize", "Maximize", "Close"} {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("no %s button; texts %q", label, tt.Texts())
		}
		if r.X < right || r.Y != 0 {
			t.Fatalf("%s at %v, after %v", label, r, right)
		}
		right = r.X + r.W
	}
	if right != 1000 {
		t.Fatalf("the buttons end at %v", right)
	}
	testutil.Snapshot(t, tt, "window-buttons")
	a.main.maximized = true
	tt.Frame()
	if !tt.HasText("Restore") || tt.HasText("Maximize") {
		t.Fatalf("maximized: texts %q", tt.Texts())
	}
	closeAt, _ := tt.Find("Close")
	tt.Move(closeAt.X+closeAt.W/2, closeAt.Y+closeAt.H/2)
	tt.Frame()
	testutil.Snapshot(t, tt, "window-buttons-maximized")
}
