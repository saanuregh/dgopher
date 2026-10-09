package app

import (
	"os"
	"path/filepath"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/store"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dashboard"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// An editor's statement becomes a panel of a new dashboard, kept in the
// project's file, then of the same dashboard; the dashboard opens again
// with the workspace.
func TestAddToDashboard(t *testing.T) {
	st, _ := store.Open(t.TempDir(), store.MemorySecrets())
	a := newApp(st)
	if _, err := a.addProject(newProjectDir(t, "p")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	lite := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	a.saveProject(a.projects[0])
	tt := ui.NewTester(a.view, 1280, 800)
	a.Connect(lite, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return lite.Status == connection.StatusConnected })
	lite.DB.SQL.Exec(`CREATE TABLE sales (region TEXT, amount INTEGER)`)

	add := func(sql, title string) *addToDashboard {
		t.Helper()
		a.NewQueryTab(lite, "", sql)
		testutil.WaitFor(t, tt, "the editor", func() bool { _, ok := a.ActiveTab().(*query.Tab); return ok })
		a.openAddToDashboard(a.ActiveTab().(*query.Tab))
		x := a.addingPanel
		x.title = title
		return x
	}
	x := add("SELECT region, sum(amount) AS total FROM sales GROUP BY region;", "By region")
	if x.target != newDashboardChoice {
		t.Fatalf("with no dashboard, the dialog offers %q", x.target)
	}
	a.addPanel(x)
	if x.err != "Name the new dashboard." {
		t.Fatalf("a new dashboard without a name: %q", x.err)
	}
	x.newName = "Sales"
	a.addPanel(x)
	if x.err != "" {
		t.Fatal(x.err)
	}
	path := filepath.Join(a.projects[0].Dir, dashboard.Folder, "sales.json")
	if d, ok := a.ActiveTab().(*dashboard.Tab); !ok || d.Path != path {
		t.Fatalf("active tab %T", a.ActiveTab())
	}
	x = add("SELECT count(*) AS n FROM sales", "Count")
	if x.target != "Sales" {
		t.Fatalf("the dialog offers %q first", x.target)
	}
	x.view = 2
	a.addPanel(x)
	saved, err := dashboard.Load(path)
	if err != nil || len(saved.Panels) != 2 || saved.Panels[1].View != dashboard.ViewValue || saved.Panels[1].Connection != "lite" {
		t.Fatalf("saved %+v %v", saved, err)
	}
	testutil.Snapshot(t, tt, "dashboard")

	a.saveWorkspace(true) // as on quit
	b := newApp(st)
	for _, tab := range b.tabs {
		if d, ok := tab.(*dashboard.Tab); ok && d.Path == path {
			return
		}
	}
	t.Fatal("the dashboard did not open again")
}
