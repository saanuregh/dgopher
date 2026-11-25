package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/project"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dashboard"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// bookmarkNames are the names of a project's bookmarks, in order.
func bookmarkNames(a *App, p *project.Project) []string {
	var out []string
	for _, b := range a.bookmarks(p).list {
		out = append(out, b.Name)
	}
	return out
}

// A file's bookmark is made from its menu, listed over the files, kept in
// bookmarks.json, follows the file as it moves, opens it, is renamed, and
// goes with the file deleted.
func TestFileBookmarks(t *testing.T) {
	a, tt, p, _ := folderApp(t, "lite")
	writeFiles(t, p.Queries, "orders.sql", "reports/.keep")
	scanned(t, a, tt, p, "folder reports", "file orders")
	if tt.HasText("Bookmarks") {
		t.Fatal("a Bookmarks heading without bookmarks")
	}

	chooseOn(t, tt, "orders.sql", "Add Bookmark")
	if got := bookmarkNames(a, p); !slices.Equal(got, []string{"orders"}) {
		t.Fatalf("bookmarks: %q", got)
	}
	data, err := os.ReadFile(filepath.Join(p.Dir, project.BookmarksFile))
	if err != nil || !strings.Contains(string(data), `"file": "queries/orders.sql"`) {
		t.Fatalf("not kept in the project: %s %v", data, err)
	}
	tt.Frame()
	if !tt.HasText("Bookmarks") {
		t.Fatalf("no Bookmarks heading: %q", tt.Texts())
	}

	chooseOn(t, tt, "orders.sql", "Move to Folder", "reports")
	if b := a.bookmarks(p).list; len(b) != 1 || b[0].File != "queries/reports/orders.sql" {
		t.Fatalf("the bookmark did not follow the file: %+v", b)
	}
	node := navNode{kind: nodeBookmark, projectDir: p.Dir, name: a.bookmarks(p).list[0].Target()}
	a.activate(node)
	if q, ok := a.ActiveTab().(*query.Tab); !ok || q.Path != filepath.Join(p.Queries, "reports", "orders.sql") {
		t.Fatalf("the bookmark opened %v", a.ActiveTab())
	}

	a.renameBookmark(p, node)
	if err := a.renaming.rename("Daily orders"); err != nil {
		t.Fatal(err)
	}
	a.renaming = nil
	if got := bookmarkNames(a, p); !slices.Equal(got, []string{"Daily orders"}) {
		t.Fatalf("renamed: %q", got)
	}

	// The filter finds a bookmark by its name.
	a.nav.filter = "daily"
	tt.Frame()
	tt.Frame()
	if !slices.Contains(navNames(a), "Daily orders") {
		t.Errorf("the filter hides the bookmark: %q", navNames(a))
	}
	a.nav.filter = ""
	tt.Frame()

	// Moved outside the app, the file leaves its bookmark pointing nowhere.
	os.Rename(filepath.Join(p.Queries, "reports", "orders.sql"), filepath.Join(p.Queries, "gone.sql"))
	if why := a.bookmarkMissing(p, a.bookmarks(p).list[0]); why == "" {
		t.Error("a bookmark of a file gone is not said to be")
	}
	os.Rename(filepath.Join(p.Queries, "gone.sql"), filepath.Join(p.Queries, "reports", "orders.sql"))

	// Deleted with its folder, the bookmark goes; the file's own Delete
	// does the same.
	a.closeTab(0)
	a.askDeleteFileFolder(p, queriesSection, "reports")
	a.closing.onClose()
	a.closing = nil
	if got := bookmarkNames(a, p); len(got) != 0 {
		t.Fatalf("bookmarks of deleted files stay: %q", got)
	}
	if _, err := os.Stat(filepath.Join(p.Dir, project.BookmarksFile)); !os.IsNotExist(err) {
		t.Errorf("an empty bookmarks.json stays: %v", err)
	}
}

// A table's bookmark is made by its key, and opens the table, its
// connection connected first.
func TestTableBookmark(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	a.openSample()
	testutil.WaitFor(t, tt, "sample editor", func() bool { return a.tabs().Len() == 1 })
	cn := a.tabs().At(0).(*query.Tab).Conn
	p := cn.Project
	key := connection.SchemaKey{Schema: "main"}
	testutil.WaitFor(t, tt, "objects", func() bool { return cn.Objects[key] != nil })
	orders := navNode{kind: nodeObject, conn: cn.Config.ID, schema: "main", name: "orders"}
	a.nav.expand(navNode{kind: nodeConn, conn: cn.Config.ID})
	a.nav.expand(navNode{kind: nodeFolder, conn: cn.Config.ID, schema: "main"})
	tt.Frame()
	if err := tt.Click("orders"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	tt.Frame()
	if n, ok := a.nav.panes()[0].chosen(); !ok || n != orders {
		t.Fatalf("chose %+v", n)
	}
	tt.Key(ui.Cmd|ui.Alt|ui.Shift, ui.KeyD)
	tt.Frame()
	b := a.bookmarks(p).list
	if len(b) != 1 || b[0].Object != "orders" || b[0].Schema != "main" || b[0].Kind != "table" || b[0].Connection != strings.TrimPrefix(cn.Config.ID, p.Prefix) {
		t.Fatalf("bookmark: %+v", b)
	}
	if place := a.bookmarkPlace(p, b[0]); place != "main · "+cn.Config.Name {
		t.Errorf("place %q", place)
	}

	a.disconnect(cn)
	testutil.WaitFor(t, tt, "disconnected", func() bool { return cn.Status != connection.StatusConnected })
	a.activate(navNode{kind: nodeBookmark, projectDir: p.Dir, name: b[0].Target()})
	testutil.WaitFor(t, tt, "the table's tab", func() bool {
		tb, ok := a.ActiveTab().(*dataview.TableTab)
		return ok && tb.Object.Name == "orders"
	})

	// Toggled again, it goes.
	a.toggleBookmark(orders)
	if got := bookmarkNames(a, p); len(got) != 0 {
		t.Errorf("not removed: %q", got)
	}
}

// A dashboard renamed keeps its bookmark.
func TestDashboardBookmarkFollowsRename(t *testing.T) {
	a, tt, p, _ := folderApp(t, "lite")
	path := dashboard.File(p, "Sales", "Ops")
	if _, err := (&dashboard.Dashboard{Name: "Ops", Panels: []dashboard.Panel{}}).Save(path, nil); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.toggleBookmark(dashboardsSection.fileNode(p, "Sales/ops.json"))
	a.askRenameDashboard(p, path)
	if err := a.renaming.rename("Health"); err != nil {
		t.Fatal(err)
	}
	if b := a.bookmarks(p).list; len(b) != 1 || b[0].File != "dashboards/Sales/health.json" {
		t.Fatalf("the bookmark did not follow: %+v", b)
	}
}
