package app

import (
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dashboard"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// The navigator chooses what the tab in front shows, opening the nodes
// above it, and a dashboard's status bar names its panels' connections.
func TestNavigatorFollowsTab(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	a.openSample()
	testutil.WaitFor(t, tt, "sample editor", func() bool { return len(a.tabs) == 1 })
	cn := a.tabs[0].(*query.Tab).Conn
	chosen := func() navNode {
		if a.nav.row < 0 || a.nav.row >= a.nav.tree.Rows() {
			return navNode{}
		}
		return a.nav.tree.Item(a.nav.row)
	}
	file := navNode{kind: nodeQueryFile, projectDir: cn.Project.Dir, name: "query-1.sql"}
	testutil.WaitFor(t, tt, "the editor's file chosen", func() bool { return chosen() == file })

	key := connection.SchemaKey{Database: "", Schema: "main"}
	testutil.WaitFor(t, tt, "objects", func() bool { return cn.Objects[key] != nil })
	for _, o := range cn.Objects[key] {
		if o.Name == "orders" {
			a.OpenTable(cn, "", o, dataview.PageData)
		}
	}
	orders := navNode{kind: nodeObject, conn: cn.Config.ID, schema: "main", name: "orders"}
	testutil.WaitFor(t, tt, "the table chosen", func() bool { return chosen() == orders })

	// Back to the editor, its file is chosen again.
	tt.Key(ui.Cmd, ui.Key1)
	testutil.WaitFor(t, tt, "the file chosen again", func() bool { return chosen() == file })

	p := cn.Project
	d := &dashboard.Dashboard{Name: "Sales", Panels: []dashboard.Panel{
		{Title: "Orders", Connection: strings.TrimPrefix(cn.Config.ID, p.Prefix), SQL: "SELECT count(*) FROM orders", View: dashboard.ViewValue, Width: 1},
	}}
	path := dashboard.File(p, d.Name)
	if _, err := d.Save(path, nil); err != nil {
		t.Fatal(err)
	}
	a.openDashboard(p, path)
	testutil.WaitFor(t, tt, "the dashboard chosen", func() bool {
		return chosen() == navNode{kind: nodeDashboard, projectDir: p.Dir, name: path}
	})
	if cn.Config.Env != db.Development || tt.HasText("No connection") || !tt.HasText(cn.Config.Env.Label()) {
		t.Fatalf("the status bar does not name the panels' connection: %q", tt.Texts())
	}
}
