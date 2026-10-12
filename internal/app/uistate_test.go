package app

import (
	"os"
	"path/filepath"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/state"
	"dgopher/internal/store"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"
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
		a := startApp(t, st)
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

// An editor the next run restores takes the height above its results
// dragged last, as a new one does.
func TestRestoredEditorTakesSavedHeight(t *testing.T) {
	cfg, data, projDir := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "proj")
	os.MkdirAll(projDir, 0o755)
	hint := keymap.Hint("Run a statement", keymap.Run) + ", or " + keymap.Hint("the whole script", keymap.RunScript) + "."
	run := func() (*App, *ui.Tester) {
		st, err := store.Open(cfg, store.MemorySecrets())
		if err != nil {
			t.Fatal(err)
		}
		uiDB, err := state.OpenUI(data)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { uiDB.Close() })
		a := newApp(st, uiDB)
		t.Cleanup(a.shutdown)
		return a, ui.NewTester(a.view, 1000, 700)
	}
	// resultsTop is where the results under the editor start, once it
	// connects.
	resultsTop := func(a *App, tt *ui.Tester) float32 {
		t.Helper()
		q := a.ActiveTab().(*query.Tab)
		q.Conn.Config.AutoConnect = true
		testutil.WaitFor(t, tt, "the results' hint", func() bool { return tt.HasText(hint) })
		r, _ := tt.Find(hint)
		return float32(r.Y)
	}

	a, tt := run()
	p, err := a.addProject(projDir)
	if err != nil {
		t.Fatal(err)
	}
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	a.saveProject(p)
	newAppEditor(t, a, tt, cn, "SELECT 1;")
	before := resultsTop(a, tt)
	a.layout.EditorHeight += 150 // as dragged
	tt.Frame()
	a.saveWorkspace(true)

	b, tt2 := run()
	if len(b.tabs) != 1 {
		t.Fatalf("restored %d tabs", len(b.tabs))
	}
	if after := resultsTop(b, tt2); after < before+149 || after > before+151 {
		t.Fatalf("the restored editor's results start at %v, %v before: want 150 lower", after, before)
	}
}

// MyGo's window states are kept in the UI state file: nothing saved loads
// as none, and what is saved, the next run loads back.
func TestWindowStateInUIState(t *testing.T) {
	data := t.TempDir()
	open := func() *state.DB {
		uiDB, err := state.OpenUI(data)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { uiDB.Close() })
		return uiDB
	}
	load, save := windowStateStore(open())
	if got, err := load(); got != nil || err != nil {
		t.Fatalf("nothing saved loads %q, %v", got, err)
	}
	saved := `{"main":{"x":40,"y":50,"width":800,"height":600,"maximized":true}}`
	if err := save([]byte(saved)); err != nil {
		t.Fatal(err)
	}
	load, _ = windowStateStore(open())
	if got, err := load(); err != nil || string(got) != saved {
		t.Fatalf("the next run loads %q, %v; want %q", got, err, saved)
	}
}
