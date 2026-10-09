package app

import (
	"testing"

	"dgopher/internal/state"
	"dgopher/internal/store"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// The layout is kept in the UI state file as it changes, and read back by
// the next run; a sidebar width the settings kept comes over once.
func TestLayoutPersists(t *testing.T) {
	cfg, data := t.TempDir(), t.TempDir()
	st, err := store.Open(cfg, store.MemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveJSON("settings.json", map[string]any{"theme": "dark", "sidebarWidth": 333}); err != nil {
		t.Fatal(err)
	}
	run := func() (*App, *ui.Tester) {
		db, err := state.OpenUI(data)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		a := newApp(st)
		a.useUIState(db)
		return a, ui.NewTester(a.view, 1000, 600)
	}

	a, tt := run()
	if a.layout.SidebarWidth != 333 {
		t.Fatalf("the settings' sidebar width did not come over: %v", a.layout.SidebarWidth)
	}
	tt.Frame()
	if err := tt.Click("Hide Sidebar"); err != nil {
		t.Fatal(err)
	}
	a.layout.EditorHeight, a.layout.RedisConsoleHeight = 410, 300 // as dragged
	tt.Frame()

	a, _ = run()
	want := widgets.DefaultLayout()
	want.SidebarWidth, want.SidebarHidden, want.EditorHeight, want.RedisConsoleHeight = 333, true, 410, 300
	if a.layout != want {
		t.Fatalf("the next run's layout %+v, want %+v", a.layout, want)
	}
}
