package app

import (
	"os"
	"path/filepath"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/datamodel"
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
	a := startApp(t, st)
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
	b := startApp(t, st)
	for _, tab := range b.tabs {
		if d, ok := tab.(*dashboard.Tab); ok && d.Path == path {
			return
		}
	}
	t.Fatal("the dashboard did not open again")
}

// Dashboards and data models are renamed, their files following their
// names, and deleted from the navigator, an open one's tab closing.
func TestRenameAndDeleteProjectFiles(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	p := a.projects[0]
	path := dashboard.File(p, "Sales")
	if _, err := (&dashboard.Dashboard{Name: "Sales", Panels: []dashboard.Panel{}}).Save(path, nil); err != nil {
		t.Fatal(err)
	}
	a.openDashboard(p, path)
	tt.Frame()

	a.askRenameDashboard(p, path)
	if err := a.renaming.rename("Revenue"); err != nil {
		t.Fatal(err)
	}
	a.renaming = nil
	renamed := dashboard.File(p, "Revenue")
	d, ok := a.ActiveTab().(*dashboard.Tab)
	if saved, err := dashboard.Load(renamed); !ok || d.Path != renamed || err != nil || saved.Name != "Revenue" {
		t.Fatalf("the tab is on %s, the file %+v %v", d.Path, saved, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the old file stayed")
	}

	// From the navigator's menu, which the tab in front opened to its row.
	testutil.WaitFor(t, tt, "the dashboard's row", func() bool { return tt.HasText("Revenue") })
	if err := tt.RightClick("Revenue"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Delete…"); err != nil {
		t.Fatalf("%v; menu %q", err, tt.Menu())
	}
	tt.Frame()
	if err := tt.Click("Delete"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, err := os.Stat(renamed); !os.IsNotExist(err) || len(a.tabs) != 0 {
		t.Fatalf("the file stayed (%v), or its tab: %d", err, len(a.tabs))
	}

	model := datamodel.File(p, "Shop")
	if _, err := (&datamodel.Model{Name: "Shop", Engine: db.Postgres}).Save(model, nil); err != nil {
		t.Fatal(err)
	}
	a.askRenameModel(p, model)
	if err := a.renaming.rename("Store"); err != nil {
		t.Fatal(err)
	}
	if m, err := datamodel.Load(datamodel.File(p, "Store")); err != nil || m.Name != "Store" {
		t.Fatalf("model %+v %v", m, err)
	}
}
