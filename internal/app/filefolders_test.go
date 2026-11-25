package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/datamodel"
	"dgopher/internal/project"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dashboard"

	"github.com/egoist/mygo/ui"
)

// fileLabels are the files pane's rows as their kinds and names, in order.
func fileLabels(a *App) []string {
	var out []string
	for i := range a.nav.files.tree.Rows() {
		n := a.nav.files.tree.Item(i)
		switch {
		case n.kind == nodeFileFolder:
			out = append(out, "folder "+n.name)
		case n.kind == nodeQueryFile || n.kind == nodeDashboard || n.kind == nodeModel:
			out = append(out, "file "+a.navName(n))
		}
	}
	return out
}

// writeFiles writes empty files at paths relative to dir, with the
// folders they are in.
func writeFiles(t *testing.T, dir string, paths ...string) {
	t.Helper()
	for _, rel := range paths {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("SELECT 1;\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// scanned scans a project's query files and waits for the files pane to
// show labels.
func scanned(t *testing.T, a *App, tt *ui.Tester, p *project.Project, labels ...string) {
	t.Helper()
	a.ScanQueries(p, true)
	testutil.WaitFor(t, tt, "the files pane listing "+strings.Join(labels, ", "), func() bool {
		return slices.Equal(fileLabels(a), labels)
	})
}

// Query files in folders show as a tree: folders first, then files, each
// by name in any case, and a file by its own name.
func TestQueryFoldersShowAsATree(t *testing.T) {
	a, tt, p, _ := folderApp(t, "lite")
	writeFiles(t, p.Queries, "b.sql", "A.sql", "reports/daily.sql", "reports/2026/q1.sql", "archive/.keep", ".hidden/x.sql")
	a.expandFileFolder(p, queriesSection, "reports")
	scanned(t, a, tt, p, "folder archive", "folder reports", "folder reports/2026", "file daily", "file A", "file b")
	a.nav.expand(queriesSection.folderNode(p, "archive"))
	tt.Frame()
	if !tt.HasText("Empty folder") {
		t.Errorf("an empty folder says nothing: %q", tt.Texts())
	}
	a.toggleBookmark(queriesSection.fileNode(p, "reports/daily.sql"))
	tt.Frame()
	testutil.Snapshot(t, tt, "navigator-file-folders")
}

// Folders of queries are made, filled, renamed and deleted from the
// navigator's menus, on disk; the editors open on their files follow.
func TestQueryFolders(t *testing.T) {
	a, tt, p, conns := folderApp(t, "lite")
	writeFiles(t, p.Queries, "orders.sql", "reports/daily.sql")
	scanned(t, a, tt, p, "folder reports", "file orders")

	chooseOn(t, tt, "Queries", "New Folder…")
	for _, bad := range []string{" ", "a/b", "Reports", ".hidden"} {
		if err := a.renaming.rename(bad); err == nil {
			t.Errorf("folder %q was made", bad)
		}
	}
	if err := a.renaming.rename("Archive"); err != nil {
		t.Fatal(err)
	}
	a.renaming = nil
	scanned(t, a, tt, p, "folder Archive", "folder reports", "file orders")

	// Moved from its menu, a file's editor follows it.
	a.openQueryFile(p, "orders.sql", nil)
	tt.Frame()
	chooseOn(t, tt, "orders.sql", "Move to Folder", "Archive")
	moved := filepath.Join(p.Queries, "Archive", "orders.sql")
	if _, err := os.Stat(moved); err != nil {
		t.Fatalf("not moved: %v", err)
	}
	q := a.tabOf(moved)
	if q == nil {
		t.Fatalf("the editor did not follow its file")
	}
	scanned(t, a, tt, p, "folder Archive", "file orders", "folder reports")

	// Dragged onto a folder, a folder goes into it with what it holds,
	// but never into itself.
	dragTo(t, tt, "reports", "Archive")
	if _, err := os.Stat(filepath.Join(p.Queries, "Archive", "reports", "daily.sql")); err != nil {
		t.Fatalf("the folder was not dragged in: %v", err)
	}
	scanned(t, a, tt, p, "folder Archive", "folder Archive/reports", "file daily", "file orders")
	dragTo(t, tt, "Archive", "reports")
	if _, err := os.Stat(filepath.Join(p.Queries, "Archive")); err != nil {
		t.Fatalf("moved into itself: %v", err)
	}

	// Renamed, it takes what it holds, and the editor, along.
	a.askRenameFileFolder(p, queriesSection, "Archive")
	if err := a.renaming.rename("Old"); err != nil {
		t.Fatal(err)
	}
	a.renaming = nil
	if want := filepath.Join(p.Queries, "Old", "orders.sql"); a.tabOf(want) != q {
		t.Fatalf("the editor did not follow the folder to %s", want)
	}

	// A new editor from a folder's menu is made in it.
	scanned(t, a, tt, p, "folder Old", "folder Old/reports", "file daily", "file orders")
	chooseOn(t, tt, "Old", "New SQL Editor")
	if _, err := os.Stat(filepath.Join(p.Queries, "Old", "query-1.sql")); err != nil {
		t.Fatalf("the new editor's file is not in the folder: %v", err)
	}
	if a.ActiveTab().Connection() != conns[0] {
		t.Errorf("the new editor is not on the project's connection")
	}

	// Deleted, it goes with all it holds, once agreed; its tabs close.
	a.askDeleteFileFolder(p, queriesSection, "Old")
	if a.closing == nil || !strings.Contains(a.closing.reason, "3 queries") {
		t.Fatalf("no question, or not how many go: %+v", a.closing)
	}
	a.closing.onClose()
	a.closing = nil
	if _, err := os.Stat(filepath.Join(p.Queries, "Old")); !os.IsNotExist(err) {
		t.Fatalf("the folder is still there: %v", err)
	}
	if a.tabOf(filepath.Join(p.Queries, "Old", "orders.sql")) != nil {
		t.Error("an editor of a deleted file stays open")
	}
}

// Dashboards and data models in folders show as the queries do; a
// dashboard renamed stays in its folder, and its name is unique across
// them.
func TestDashboardAndModelFolders(t *testing.T) {
	a, tt, p, _ := folderApp(t, "lite")
	for _, d := range []struct{ folder, name string }{{"Sales/EU", "Revenue"}, {"", "Ops"}} {
		if _, err := (&dashboard.Dashboard{Name: d.name, Panels: []dashboard.Panel{}}).Save(dashboard.File(p, d.folder, d.name), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(p.Dir, datamodel.Folder, "Drafts"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.nav.collapse(queriesSection.folderNode(p, ""))
	a.expandFileFolder(p, dashboardsSection, "Sales/EU")
	a.nav.expand(modelsSection.folderNode(p, ""))
	tt.Frame()
	if got := fileLabels(a); !slices.Equal(got, []string{"folder Sales", "folder Sales/EU", "file Revenue", "file Ops", "folder Drafts"}) {
		t.Fatalf("dashboards and models: %q", got)
	}
	if !tt.HasText("Data Models") {
		t.Errorf("a folder of models alone has no heading: %q", tt.Texts())
	}

	path := dashboard.File(p, "Sales/EU", "Revenue")
	a.askRenameDashboard(p, path)
	if err := a.renaming.rename("Income"); err != nil {
		t.Fatal(err)
	}
	a.renaming = nil
	if _, err := os.Stat(dashboard.File(p, "Sales/EU", "Income")); err != nil {
		t.Fatalf("renamed out of its folder: %v", err)
	}
	a.newDashboard(p, "")
	if err := a.renaming.rename("Income"); err == nil {
		t.Error("a second Income, in another folder, was made")
	}
	a.askRenameDashboard(p, dashboard.File(p, "", "Ops"))
	if err := a.renaming.rename("Income"); err == nil {
		t.Error("a dashboard was renamed to the name of one in another folder")
	}
	a.newDashboard(p, "")
	if err := a.renaming.rename("Costs"); err != nil {
		t.Fatal(err)
	}
	a.renaming = nil
	tt.Frame()
	if names, _ := a.dashboardNames(p); !slices.Equal(slices.Sorted(slices.Values(names)), []string{"Costs", "Income", "Ops"}) {
		t.Errorf("dashboards listed: %q", names)
	}

	// Moved into another folder, an open dashboard follows its file.
	a.openDashboard(p, dashboard.File(p, "", "Ops"))
	tt.Frame()
	if err := a.moveOnDisk(p, dashboardsSection, "ops.json", "Sales/ops.json"); err != nil {
		t.Fatal(err)
	}
	if a.tabOf(dashboard.File(p, "Sales", "Ops")) == nil {
		t.Error("the dashboard's tab did not follow its file")
	}
}

// A section sorted by when its files last changed lists the last first,
// its folders still first, by name; the order is kept per section.
func TestSortByModified(t *testing.T) {
	a, tt, p, _ := folderApp(t, "lite")
	writeFiles(t, p.Queries, "alpha.sql", "beta.sql", "zeta/x.sql")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(p.Queries, "beta.sql"), old, old); err != nil {
		t.Fatal(err)
	}
	scanned(t, a, tt, p, "folder zeta", "file alpha", "file beta")
	os.Chtimes(filepath.Join(p.Queries, "alpha.sql"), old.Add(-time.Hour), old.Add(-time.Hour))
	chooseOn(t, tt, "Queries", "Sort By", "Last Modified")
	scanned(t, a, tt, p, "folder zeta", "file beta", "file alpha")
	if a.layout.NavByModified != "queries" || a.sortedByModified(dashboardsSection) {
		t.Errorf("kept as %q", a.layout.NavByModified)
	}
	chooseOn(t, tt, "Queries", "Sort By", "Name")
	scanned(t, a, tt, p, "folder zeta", "file alpha", "file beta")
}

// A queries folder that is the project's own does not offer the folders
// of the dashboards and the models as folders of queries.
func TestQueriesFolderOfTheProject(t *testing.T) {
	a, tt, p, _ := folderApp(t, "lite")
	p.Queries = p.Dir
	writeFiles(t, p.Dir, "top.sql", "reports/daily.sql")
	if _, err := (&dashboard.Dashboard{Name: "Ops", Panels: []dashboard.Panel{}}).Save(dashboard.File(p, "", "Ops"), nil); err != nil {
		t.Fatal(err)
	}
	scanned(t, a, tt, p, "folder queries", "folder reports", "file top")
}
