package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/store"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/panes"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// A cloned .dgopher names files outside the project, or the project's own
// .git and .dgopher: none comes back as an editor.
func TestRestoreSkipsOutsidePaths(t *testing.T) {
	st, _ := store.Open(t.TempDir(), store.MemorySecrets())
	a := startApp(t, st)
	root := t.TempDir()
	projDir := filepath.Join(root, "proj")
	os.MkdirAll(filepath.Join(projDir, "queries"), 0o755)
	p, err := a.addProject(projDir)
	if err != nil {
		t.Fatal(err)
	}
	addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	a.saveProject(p)

	outside := filepath.Join(root, "x")
	os.WriteFile(outside, []byte("SELECT 1;"), 0o644)
	os.Mkdir(filepath.Join(projDir, ".git"), 0o755)
	os.WriteFile(filepath.Join(projDir, ".git", "config"), []byte("SELECT 1;"), 0o644)
	os.WriteFile(filepath.Join(projDir, ".dgopher", "x"), []byte("SELECT 1;"), 0o644)
	os.Symlink(outside, filepath.Join(projDir, "queries", "link.sql"))
	inside := filepath.Join(projDir, "queries", "ok.sql")
	os.WriteFile(inside, []byte("SELECT 2;"), 0o644)

	var editors []savedEditor
	for _, path := range []string{"../x", outside, "queries/link.sql", ".git/config", ".dgopher/x", "queries/../../x", "queries/ok.sql"} {
		editors = append(editors, savedEditor{Connection: "lite", Path: path, Unsaved: "SELECT 3;"})
	}
	if err := p.Local.SaveJSON(workspaceFile, savedWorkspace{Editors: editors, Active: -1, Dashboards: []string{"../x"}, Models: []string{outside}}); err != nil {
		t.Fatal(err)
	}
	a.main.tabs().TakeAll()
	a.restoreWorkspace(p)
	if a.main.tabs().Len() != 1 {
		var paths []string
		for _, tab := range a.main.tabs().All() {
			if q, ok := tab.(*query.Tab); ok {
				paths = append(paths, q.Path)
			}
		}
		t.Fatalf("restored %d tabs, want only %s: %v", a.main.tabs().Len(), inside, paths)
	}
	if q, ok := a.main.tabs().At(0).(*query.Tab); !ok || q.Path != inside {
		t.Fatalf("restored %v", a.main.tabs().At(0))
	}
}

// The tabs of a connection come back after a restart, in their order
// among the editors, the one in front in front and pinned as they were,
// waiting to connect; one of a connection gone since stays closed.
func TestWorkspaceRestoresConnectionTabs(t *testing.T) {
	cfgDir := t.TempDir()
	st, _ := store.Open(cfgDir, store.MemorySecrets())
	a := startApp(t, st)
	projDir := filepath.Join(t.TempDir(), "proj")
	os.MkdirAll(projDir, 0o755)
	p, err := a.addProject(projDir)
	if err != nil {
		t.Fatal(err)
	}
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", "")})
	a.saveProject(p)
	tt := ui.NewTester(a.view, 1200, 760)
	connectNow(t, a, tt, cn)
	if _, err := cn.DB.SQL.Exec("CREATE TABLE orders (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	newAppEditor(t, a, tt, cn, "SELECT 1;")
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "orders", Kind: db.KindTable, Rows: -1}, dataview.PageDDL)
	dataview.OpenER(a, cn, "", "main")
	table := a.main.tabs().At(1)
	a.main.tabs().SetPinned(table, true)
	a.main.tabs().Select(table)
	a.saveWorkspace(true)
	var w savedWorkspace
	p.Local.LoadJSON(workspaceFile, &w)
	w.Tabs = append(w.Tabs, savedTab{Kind: savedTable, Connection: "gone", Schema: "main", Name: "orders"})
	w.Order = append(w.Order, tabRef("tabs", len(w.Tabs)-1))
	p.Local.SaveJSON(workspaceFile, w)

	st2, _ := store.Open(cfgDir, store.MemorySecrets())
	b := startApp(t, st2)
	if b.main.tabs().Len() != 3 {
		t.Fatalf("restored %d tabs", b.main.tabs().Len())
	}
	first, ok := b.main.tabs().At(0).(*waitingTab)
	if !ok || first.saved.Kind != savedTable || first.saved.Name != "orders" || first.saved.Page != dataview.PageDDL || !b.main.tabs().Pinned(first) || b.ActiveTab() != first {
		t.Fatalf("first %T %+v, pinned %v", b.main.tabs().At(0), first, b.main.tabs().Pinned(b.main.tabs().At(0)))
	}
	if _, ok := b.main.tabs().At(1).(*query.Tab); !ok {
		t.Fatalf("second %T", b.main.tabs().At(1))
	}
	if d, ok := b.main.tabs().At(2).(*waitingTab); !ok || d.saved.Kind != savedDiagram {
		t.Fatalf("third %T", b.main.tabs().At(2))
	}
	if first.conn.Status != connection.StatusIdle {
		t.Fatalf("restored tab connected on its own: %v", first.conn.Status)
	}
}

// A workspace of an older DGopher, without an order, reopens its editors
// first, the one it names in front.
func TestWorkspaceOldFormat(t *testing.T) {
	st, _ := store.Open(t.TempDir(), store.MemorySecrets())
	a := startApp(t, st)
	projDir := filepath.Join(t.TempDir(), "proj")
	os.MkdirAll(filepath.Join(projDir, "queries"), 0o755)
	p, err := a.addProject(projDir)
	if err != nil {
		t.Fatal(err)
	}
	addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	a.saveProject(p)
	var editors []savedEditor
	for _, name := range []string{"a.sql", "b.sql"} {
		os.WriteFile(filepath.Join(projDir, "queries", name), []byte("SELECT 1;"), 0o644)
		editors = append(editors, savedEditor{Connection: "lite", Path: "queries/" + name})
	}
	if err := p.Local.SaveJSON(workspaceFile, savedWorkspace{Editors: editors, Active: 1}); err != nil {
		t.Fatal(err)
	}
	a.main.tabs().TakeAll()
	a.restoreWorkspace(p)
	if a.main.tabs().Len() != 2 || a.main.tabs().At(0).Title() != "a.sql" || a.ActiveTab().Title() != "b.sql" {
		t.Fatalf("restored %d tabs, %s in front", a.main.tabs().Len(), a.ActiveTab().Title())
	}
}

// The main window's groups come back after a restart, each with its tabs
// and the one in front, the group the keys went to focused.
func TestWorkspaceRestoresGroups(t *testing.T) {
	cfgDir := t.TempDir()
	st, _ := store.Open(cfgDir, store.MemorySecrets())
	a := startApp(t, st)
	projDir := filepath.Join(t.TempDir(), "proj")
	os.MkdirAll(projDir, 0o755)
	p, err := a.addProject(projDir)
	if err != nil {
		t.Fatal(err)
	}
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", "")})
	a.saveProject(p)
	tt := ui.NewTester(a.view, 1200, 760)
	connectNow(t, a, tt, cn)
	if _, err := cn.DB.SQL.Exec("CREATE TABLE orders (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	newAppEditor(t, a, tt, cn, "SELECT 1;")
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "orders", Kind: db.KindTable, Rows: -1}, dataview.PageDDL)
	newAppEditor(t, a, tt, cn, "SELECT 2;")
	a.moveTabToward(a.main, panes.Down)
	tt.Frame()
	a.main.panes.Root().Percent = 70
	a.focusGroup(a.main, a.main.panes.Groups()[0])
	a.saveWorkspace(true)

	st2, _ := store.Open(cfgDir, store.MemorySecrets())
	b := startApp(t, st2)
	groups := b.main.panes.Groups()
	if len(groups) != 2 || b.main.panes.Root().Axis != panes.Column || b.main.panes.Root().Percent != 70 {
		t.Fatalf("groups %d", len(groups))
	}
	if groups[0].Tabs.Len() != 2 || groups[1].Tabs.Len() != 1 || b.main.panes.Focused() != groups[0] {
		t.Fatalf("tabs %d and %d", groups[0].Tabs.Len(), groups[1].Tabs.Len())
	}
	if _, ok := groups[0].Tabs.Active().(*waitingTab); !ok {
		t.Fatalf("in front of the first %T", groups[0].Tabs.Active())
	}
	if q, ok := groups[1].Tabs.Active().(*query.Tab); !ok || !strings.HasSuffix(q.Editor.Text(), "SELECT 2;") {
		t.Fatalf("in the second %T %q", groups[1].Tabs.Active(), q.Editor.Text())
	}
}
