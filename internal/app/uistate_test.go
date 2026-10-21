package app

import (
	"maps"
	"os"
	"path/filepath"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/state"
	"dgopher/internal/store"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
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
	a.layout.EditorPercent, a.layout.RedisConsoleHeight = 60, 300 // as dragged
	tt.Frame()

	a, _ = run()
	want := widgets.DefaultLayout()
	want.SidebarWidth, want.SidebarHidden, want.EditorPercent, want.RedisConsoleHeight = 333, true, 60, 300
	if a.layout != want {
		t.Fatalf("the next run's layout %+v, want %+v", a.layout, want)
	}
}

// An editor the next run restores takes the percentage above its results
// dragged last, as a new one does.
func TestRestoredEditorTakesSavedPercent(t *testing.T) {
	cfg, data, projDir := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "proj")
	os.MkdirAll(projDir, 0o755)
	hint := "Output" // the button in the results' bar
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
		a.Connect(q.Conn, nil)
		testutil.WaitFor(t, tt, "the results' bar", func() bool { return q.Conn.Status == connection.StatusConnected && tt.HasText(hint) })
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
	a.layout.EditorPercent += 25 // as dragged
	tt.Frame()
	a.saveWorkspace(true)

	b, tt2 := run()
	if b.tabs().Len() != 1 {
		t.Fatalf("restored %d tabs", b.tabs().Len())
	}
	// A quarter of a room some 600 high.
	if after := resultsTop(b, tt2); after < before+100 || after > before+200 {
		t.Fatalf("the restored editor's results start at %v, %v before: want about 150 lower", after, before)
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
	if got, err := (windowStates{open()}).LoadWindowStates(); len(got) != 0 || err != nil {
		t.Fatalf("nothing saved loads %v, %v", got, err)
	}
	saved := map[string]mygo.WindowState{"main": {X: 40, Y: 50, Width: 800, Height: 600, Maximized: true}}
	if err := (windowStates{open()}).SaveWindowStates(saved); err != nil {
		t.Fatal(err)
	}
	if got, err := (windowStates{open()}).LoadWindowStates(); err != nil || !maps.Equal(got, saved) {
		t.Fatalf("the next run loads %v, %v; want %v", got, err, saved)
	}
}
