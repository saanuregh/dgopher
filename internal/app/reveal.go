package app

import (
	"path/filepath"

	"dgopher/internal/connection"
	"dgopher/internal/project"
	"dgopher/internal/ui/dashboard"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/modelview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// followTab has the navigator show what the tab in front shows, as that
// tab changes.
func (a *App) followTab() {
	t := a.ActiveTab()
	if t == a.nav.shown {
		return
	}
	a.nav.shown, a.nav.reveal, a.nav.opened = t, nil, nil
	if n, ok := a.tabNode(t); ok {
		a.nav.reveal, a.nav.opened = &n, map[navNode]bool{}
	}
}

// tabNode is the node of the navigator a tab shows: its query file, table,
// dashboard or data model, else its connection.
func (a *App) tabNode(t widgets.Tab) (navNode, bool) {
	switch t := t.(type) {
	case nil:
		return navNode{}, false
	case *query.Tab:
		for _, p := range a.projects {
			if rel, err := filepath.Rel(p.Queries, t.Path); t.Path != "" && err == nil && filepath.IsLocal(rel) {
				return navNode{kind: nodeQueryFile, projectDir: p.Dir, name: filepath.ToSlash(rel)}, true
			}
		}
	case *dataview.TableTab:
		return navNode{kind: nodeObject, conn: t.Conn.Config.ID, database: t.Database, schema: t.Object.Schema, name: t.Object.Name}, true
	case *dashboard.Tab:
		return navNode{kind: nodeDashboard, projectDir: t.Project.Dir, name: t.Path}, true
	case *modelview.Tab:
		return navNode{kind: nodeModel, projectDir: t.Project.Dir, name: t.Path}, true
	}
	if cn := t.Connection(); cn != nil {
		return navNode{kind: nodeConn, conn: cn.Config.ID}, true
	}
	return navNode{}, false
}

// openToReveal opens the nodes above the node to reveal, before the tree
// is drawn, for it to show in the same frame: each once, so that one the
// user closes stays closed, and the node is no longer looked for. It is
// given up too once the tree cannot hold it, nothing above it loading any
// more.
func (a *App) openToReveal() {
	target := a.nav.reveal
	if target == nil {
		return
	}
	dir, folder := target.projectDir, ""
	if cn := a.connByID(target.conn); cn != nil {
		dir, folder = cn.Project.Dir, cn.Config.Folder
	}
	waiting := false
	var walk func(n navNode) bool
	walk = func(n navNode) bool {
		if cn := a.connByID(n.conn); n.kind == nodeConn && (cn == nil || cn.Status != connection.StatusConnected) {
			return false // opened, it would connect
		}
		if !a.nav.opened[n] {
			a.nav.opened[n] = true
			a.nav.expand(n)
		}
		if !a.nav.isOpen(n) {
			return false // closed by the user
		}
		for _, ch := range a.navChildren(n) {
			switch {
			case ch == *target:
				return true
			case ch.kind == nodeInfo:
				waiting = true // loading, or failed: it may come
			case leadsTo(ch, *target, dir, folder) && walk(ch):
				return true
			}
		}
		return false
	}
	// Its pane's roots: one project's are its first rows, which may be
	// the node itself.
	for _, root := range a.paneRoots(inFilesPane(*target)) {
		if root == *target || leadsTo(root, *target, dir, folder) && walk(root) {
			return
		}
	}
	if !waiting {
		a.nav.reveal = nil
	}
}

// chooseRevealed chooses the node to reveal once the tree drawn holds it,
// which shows chosen in the next frame, asked at once.
func (a *App) chooseRevealed(c *ui.Context) {
	target := a.nav.reveal
	if target == nil {
		return
	}
	pane := a.nav.paneOf(*target)
	for i := range pane.tree.Rows() {
		if pane.tree.Item(i) == *target {
			*pane.row = i
			pane.tree.List.ScrollIntoView(i)
			// Its pane is the one worked in, unless the other has the keys.
			a.nav.inFiles = inFilesPane(*target)
			a.nav.reveal = nil
			c.Invalidate()
			return
		}
	}
}

// leadsTo reports whether a node may hold target, of the project in
// folder dir, and of a connection in the navigator's folder folder.
func leadsTo(n, target navNode, dir, folder string) bool {
	same := n.conn == target.conn && n.database == target.database
	switch n.kind {
	case nodeProject:
		return n.projectDir == dir
	case nodeConnections:
		return n.projectDir == dir && target.conn != ""
	case nodeConnFolder:
		return n.projectDir == dir && target.conn != "" && project.UnderFolder(folder, n.name)
	case nodeQueries:
		return n.projectDir == dir && target.kind == nodeQueryFile
	case nodeDashboards:
		return n.projectDir == dir && target.kind == nodeDashboard
	case nodeModels:
		return n.projectDir == dir && target.kind == nodeModel
	case nodeConn:
		return n.conn == target.conn && target.kind != nodeConn
	case nodeDatabase:
		return same
	case nodeSchema:
		return same && n.schema == target.schema
	case nodeFolder:
		return same && n.schema == target.schema && (n.folder == folderTables || n.folder == folderViews)
	}
	return false
}
