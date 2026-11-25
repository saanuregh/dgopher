package app

import (
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// sidebar builds the navigator.
func (a *App) sidebar(c *ui.Context) {
	a.nav.notesRead = false
	pal := widgets.PaletteOf(c)
	side := ui.Column(c).FillHeight().Width(a.layout.SidebarWidth).Background(pal.Sidebar)
	a.tourPart(tourSidebar, side)
	side.Children(func() {
		a.sidebarBar(c)
		if len(a.projects) == 0 {
			ui.Column(c).Padding(16).Gap(10).Children(func() {
				ui.Text(c, "No projects yet. A project is a folder for Git: its connections, without passwords, and its .sql files.").TextColor(pal.Muted)
				if ui.PrimaryButton(c, "New Project…").Clicked() {
					a.openNewProject(nil)
				}
				if ui.Button(c, "Add Existing Folder…").Clicked() {
					a.addExistingProject()
				}
			})
			ui.Spacer(c)
			return
		}
		a.navFilterBox(c)
		a.nav.openConns, a.nav.openConnsNow = a.nav.openConnsNow, map[string]bool{}
		clear(a.nav.errors)
		a.navFilterSync()
		a.followTab()
		a.openToReveal()
		roots, files := a.navShownRoots(false), a.navShownRoots(true)
		// Under a filter a pane without a match does not show, nor the
		// line between the two: the other has the sidebar.
		noTree, noFiles := a.nav.needle != "" && len(roots) == 0, a.nav.needle != "" && len(files) == 0
		panes := a.nav.panes()
		var trees [2]ui.Element
		pane := func(i int, roots []navNode, label string) {
			// The pane worked in has the tint on its chosen row, the
			// other a faint one: of two rows chosen, one is the current.
			rows := widgets.ChosenRows
			if (i == 1) != a.nav.inFiles {
				rows = widgets.ChosenRowsElsewhere
			}
			rows(c, func() {
				// Rows as dense as DBeaver's navigator: the theme's
				// spacing sizes them.
				chosen := c.Theme()
				dense := *chosen
				dense.Spacing = navSpacing
				c.SetTheme(&dense)
				defer c.SetTheme(chosen)
				trees[i] = ui.Outline(c, panes[i].tree, roots, a.navShown, func(n navNode) {
					a.navRow(c, n)
				}).Fill().PaddingX(6).Label(label)
			})
		}
		if noTree || noFiles {
			if noTree && noFiles {
				a.navNoMatch(c)
			}
			ui.Column(c).Grow(1).Children(func() {
				pane(1, files, "Project files")
				pane(0, roots, "Navigator")
			})
			trees[0].Hidden(noTree)
			trees[1].Hidden(noFiles)
		} else {
			a.navSplit(c, func() { pane(1, files, "Project files") }, func() { pane(0, roots, "Navigator") })
			// As much room under the line between the two as over it.
			trees[0].Padding(navTreeTop, 6, 0, 6)
		}
		a.keepClosedHeadings()
		a.keepKnownDatabases()
		a.nav.hasKeys = trees[0].FocusWithin() || trees[1].FocusWithin()
		if in := trees[1].FocusWithin(); (in || trees[0].FocusWithin()) && in != a.nav.inFiles {
			a.nav.inFiles = in
			c.Invalidate() // the panes' tints are of the pane worked in
		}
		a.navPickMatch()
		a.forgetGoneErrors()
		a.chooseRevealed(c)
		for i, pn := range panes {
			a.skipNotes(c, pn)
			if n, ok := pn.chosen(); ok && trees[i].Submitted() {
				a.activate(n)
			}
			a.nodeKeys(c, pn)
		}
		for i, want := range [2]string{"nav", "nav-files"} {
			if a.focusWant != want {
				continue
			}
			if *panes[i].row < 0 && panes[i].tree.Rows() > 0 {
				*panes[i].row = 0
			}
			if panes[i].tree.List.Focus(c) {
				a.focusWant = ""
			}
		}
		if n, ok := panes[0].chosen(); ok && keymap.ListPressed(c, &a.nav.tree.List, keymap.NavigatorRefresh) {
			if cn := a.connByID(n.conn); cn != nil {
				a.refresh(cn)
			}
		}
		// On a table: a new editor with its rows, the quickest look.
		if n, ok := panes[0].chosen(); ok && n.kind == nodeObject && keymap.ListPressed(c, &a.nav.tree.List, keymap.NavigatorSelect) {
			if cn, obj, ok := a.object(n); ok {
				c.DisableAnimations() // by a key: the tab is there at once
				a.NewQueryTab(cn, n.database, db.LimitSQL(cn.Config.Engine, "SELECT *\nFROM "+db.QualifiedName(cn.DB().Dialect, obj.Schema, obj.Name), "\n", 100)+";\n")
			}
		}
	})
}

// navSplit lays the navigator's two panes out: the projects' files above,
// the databases below, each scrolling on its own. The files come first
// because the tree is the pane that grows, by a row with every table
// opened: under it the files would move with each, and sit at the
// sidebar's foot under a long one; above it they keep their place, and
// the tree grows into the room below. The files are as tall as their
// rows, so that the tree follows them at once, up to the height dragged:
// until one is, up to what the tree does not need, two fifths of the
// sidebar at least.
func (a *App) navSplit(c *ui.Context, files, tree func()) {
	// The rows are counted as the panes build: as last drawn here, and
	// drawn again when a count turns out another.
	fileRows, treeRows := a.nav.files.tree.Rows(), a.nav.tree.Rows()
	paneH := func(rows int) float32 { return float32(max(rows, 1))*navRowHeight + navPaneChrome }
	need, treeNeed := paneH(fileRows), paneH(treeRows)+navTreeTop
	limit := a.layout.NavFilesHeight
	if a.nav.sideH > 0 {
		if limit == 0 {
			limit = max(0.4*a.nav.sideH, a.nav.sideH-treeNeed)
		}
		// The tree keeps its least height, or its rows when they are fewer.
		limit = min(limit, a.nav.sideH-min(navTreeMinHeight, treeNeed))
	}
	above := need
	if limit > 0 {
		above = min(need, limit)
	}
	split := ui.SplitVertical(c, &above, files, tree).
		Grow(1).Draw(func(_ *ui.Painter, r ui.Rect) { a.nav.sideH = r.H })
	if a.nav.files.tree.Rows() != fileRows || a.nav.tree.Rows() != treeRows {
		c.Invalidate()
	}
	if split.Changed() {
		a.layout.NavFilesHeight = max(navFilesMinHeight, above)
	}
}

// The heights of the navigator's panes: the least a drag leaves each, a
// row of the navigator at navSpacing, the room a pane has under its last
// row, and the room the tree has over its first, under the line between
// the two.
const (
	navFilesMinHeight = 56
	navTreeMinHeight  = 120
	navRowHeight      = 24
	navPaneChrome     = 6
	navTreeTop        = 5
)

// nodeKeys handles the keys of the node chosen in the navigator, as its
// menu's items: a table's, a connection's, or a project file's.
func (a *App) nodeKeys(c *ui.Context, pane navPane) {
	l := &pane.tree.List
	n, ok := pane.chosen()
	if !ok {
		return
	}
	if n.kind == nodeConn {
		// As DBeaver: F4 edits a connection, Delete deletes it once agreed.
		if cn := a.connByID(n.conn); cn != nil {
			switch {
			case keymap.ListPressed(c, l, keymap.NavigatorStructure):
				a.openConnForm(cn)
			case keymap.ListPressed(c, l, keymap.NavigatorDrop):
				a.askDeleteConn(cn)
			}
		}
		return
	}
	if p := a.projectByDir(n.projectDir); p != nil {
		if n.kind == nodeConnFolder {
			a.connFolderKeys(c, l, p, n)
			return
		}
		if rename, del := a.fileActions(p, n); rename != nil {
			switch {
			case keymap.ListPressed(c, l, keymap.NavigatorRename):
				rename()
			case keymap.ListPressed(c, l, keymap.NavigatorDrop):
				del()
			}
		}
		return
	}
	if n.kind != nodeObject {
		return
	}
	cn, obj, ok := a.object(n)
	if !ok {
		return
	}
	quoted := db.QualifiedName(cn.DB().Dialect, obj.Schema, obj.Name)
	switch {
	case keymap.ListPressed(c, l, keymap.NavigatorCopyName):
		a.WriteClipboard(quoted)
	case keymap.ListPressed(c, l, keymap.NavigatorRename) && renamable(cn, obj) && !cn.Config.ReadOnly:
		a.renameObject(cn, n.database, obj)
	case keymap.ListPressed(c, l, keymap.NavigatorStructure):
		a.OpenTable(cn, n.database, obj, dataview.PageStructure)
	case keymap.ListPressed(c, l, keymap.NavigatorDrop) && obj.Kind == db.KindTable:
		a.NewQueryTab(cn, n.database, "DROP TABLE "+quoted+";\n")
	}
}

// renamable reports whether the app can rename an object on its engine.
func renamable(cn *connection.Conn, obj db.Object) bool {
	_, err := db.RenameObjectSQL(cn.DB().Dialect, obj, obj.Name)
	return err == nil
}

// renameObject asks for an object's new name, and renames it.
func (a *App) renameObject(cn *connection.Conn, database string, obj db.Object) {
	a.askRenameInDatabase(cn, database, obj.Name, func(name string) (string, error) {
		return db.RenameObjectSQL(cn.DB().Dialect, obj, name)
	})
}

// activate does what a double click on a node does.
func (a *App) activate(n navNode) {
	if e, ok := a.nav.errors[n]; ok {
		a.showNavError(e)
		return
	}
	if cn := a.failedConn(n); cn != nil {
		a.showConnError(cn)
		return
	}
	switch n.kind {
	case nodeProject, nodeConnections, nodeConnFolder, nodeQueries, nodeDashboards, nodeModels:
		if a.nav.isOpen(n) {
			a.nav.collapse(n)
		} else {
			a.nav.expand(n)
		}
		return
	case nodeQueryFile, nodeDashboard, nodeModel:
		p := a.projectByDir(n.projectDir)
		switch {
		case p == nil:
		case n.kind == nodeQueryFile:
			a.openQueryFile(p, n.name, nil)
		case n.kind == nodeDashboard:
			a.openDashboard(p, n.name)
		default:
			a.openModel(p, n.name)
		}
		return
	}
	cn := a.connByID(n.conn)
	if cn == nil {
		return
	}
	switch n.kind {
	case nodeConn:
		a.activateConnection(cn, n)
	case nodeObject:
		if cn, obj, ok := a.object(n); ok {
			a.OpenTable(cn, n.database, obj, dataview.PageData)
		}
	case nodeItem:
		if cn, it, ok := a.item(n); ok {
			a.openItem(cn, n.database, it)
		}
	case nodeDatabase, nodeSchema, nodeFolder:
		if a.nav.isOpen(n) {
			a.nav.collapse(n)
		} else {
			a.nav.expand(n)
		}
	}
}

// activateConnection opens a connection's node: the key browser for a
// key-value engine, which has no tree of schemas, and otherwise connects
// and expands the node.
func (a *App) activateConnection(cn *connection.Conn, n navNode) {
	if cn.Config.Engine.IsKeyValue() {
		a.openRedis(cn)
		return
	}
	a.connectNode(cn, n)
}

// connectNode connects cn, opening its node n and then where it starts.
func (a *App) connectNode(cn *connection.Conn, n navNode) {
	a.nav.expand(n)
	a.Connect(cn, func() { a.expandDefaults(cn) })
}

// expandDefaults opens the database and schema the connection starts in.
func (a *App) expandDefaults(cn *connection.Conn) {
	a.nav.expand(navNode{kind: nodeConn, conn: cn.Config.ID})
	if cn.DefaultSchema == "" && cn.Schemas[""] == nil {
		cn.ExpandWhenLoaded = true // once the schemas arrive
		return
	}
	if cn.DefaultSchema != "" {
		a.nav.expand(navNode{kind: nodeSchema, conn: cn.Config.ID, schema: cn.DefaultSchema})
		a.nav.expand(navNode{kind: nodeFolder, conn: cn.Config.ID, schema: cn.DefaultSchema})
	}
}
