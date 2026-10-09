package app

import (
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
	if err := tt.ChooseMenuItem("Help", "Keyboard Shortcuts"); err != nil {
		t.Fatalf("%v; menu %q", err, tt.Menu())
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
