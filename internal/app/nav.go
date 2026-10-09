package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type nodeKind uint8

const (
	nodeProject nodeKind = iota
	nodeQueries          // the query files of a project
	nodeQueryFile
	nodeConn
	nodeDatabase
	nodeSchema
	nodeFolder // the tables or the views of a schema
	nodeObject
	nodeColumn
	nodeInfo // loading, an error, or nothing there
)

// navNode is an item of the navigator's tree.
type navNode struct {
	kind nodeKind
	// projectDir is the folder of a project's own nodes: the project, its
	// Queries folder and files, and their notes. The nodes of a
	// connection are named by its ID alone, unique across projects.
	projectDir string
	conn       string
	database   string
	schema     string
	name       string
	views      bool // a folder of views rather than tables
}

type navState struct {
	tree   ui.OutlineState[navNode]
	row    int
	filter string
}

func (n *navState) init() {
	n.row = -1
	n.tree.List.Selected = &n.row
}

// expand opens a node of the tree.
func (n *navState) expand(node navNode) { n.tree.Open.Add(node) }

func (a *App) navRoots() []navNode {
	roots := make([]navNode, len(a.projects))
	for i, p := range a.projects {
		roots[i] = navNode{kind: nodeProject, projectDir: p.Dir}
	}
	return roots
}

// projectChildren lists a project's connections, then its queries.
func (a *App) projectChildren(n navNode) []navNode {
	p := a.projectByDir(n.projectDir)
	if p == nil {
		return nil
	}
	if p.Err != "" {
		return []navNode{{kind: nodeInfo, projectDir: p.Dir, name: widgets.FirstLine(p.Err)}}
	}
	if n.kind == nodeQueries {
		if a.nav.tree.Open.Has(n) {
			a.ScanQueries(p, false)
		}
		var out []navNode
		for _, f := range p.Files {
			out = append(out, navNode{kind: nodeQueryFile, projectDir: p.Dir, name: f})
		}
		if p.ScanErr != "" {
			out = append(out, navNode{kind: nodeInfo, projectDir: p.Dir, name: widgets.FirstLine(p.ScanErr)})
		}
		if out == nil {
			return []navNode{{kind: nodeInfo, projectDir: p.Dir, name: "No .sql files yet"}}
		}
		return out
	}
	var out []navNode
	for _, cn := range a.projectConns(p) {
		out = append(out, navNode{kind: nodeConn, conn: cn.Config.ID})
	}
	if out == nil {
		out = append(out, navNode{kind: nodeInfo, projectDir: p.Dir, name: "No connections yet"})
	}
	return append(out, navNode{kind: nodeQueries, projectDir: p.Dir})
}

func info(cn *connection.Conn, text string) []navNode {
	return []navNode{{kind: nodeInfo, conn: cn.Config.ID, name: text}}
}

// navChildren returns a node's children, starting to read them when the
// node is open and they are not known yet.
func (a *App) navChildren(n navNode) []navNode {
	switch n.kind {
	case nodeProject, nodeQueries:
		return a.projectChildren(n)
	case nodeQueryFile, nodeInfo:
		return nil
	}
	cn := a.connByID(n.conn)
	if cn == nil {
		return nil
	}
	open := a.nav.tree.Open.Has(n)
	switch n.kind {
	case nodeConn:
		if cn.Config.Engine == db.Redis {
			return nil
		}
		switch cn.Status {
		case connection.StatusConnecting:
			return info(cn, "Connecting…")
		case connection.StatusFailed:
			return info(cn, "Failed: "+widgets.FirstLine(cn.Err))
		case connection.StatusIdle:
			if open {
				a.Connect(cn, nil)
				return info(cn, "Connecting…")
			}
			return info(cn, "")
		}
		if len(cn.Databases) > 1 {
			out := make([]navNode, len(cn.Databases))
			for i, d := range cn.Databases {
				out[i] = navNode{kind: nodeDatabase, conn: n.conn, database: d}
				if d == cn.Config.Database {
					out[i].database = ""
				}
			}
			return out
		}
		return a.schemaNodes(cn, "", open)
	case nodeDatabase:
		return a.schemaNodes(cn, n.database, open)
	case nodeSchema:
		objs, ok := cn.Objects[connection.SchemaKey{Database: n.database, Schema: n.schema}]
		if !ok {
			if e := cn.LoadErr[connection.SchemaKey{Database: n.database, Schema: n.schema}]; e != "" {
				return info(cn, "Failed: "+widgets.FirstLine(e))
			}
			if open {
				connection.LoadObjects(a, cn, n.database, n.schema)
			}
			return info(cn, "Loading…")
		}
		var out []navNode
		if len(connection.SortedObjects(objs, false)) > 0 {
			out = append(out, navNode{kind: nodeFolder, conn: n.conn, database: n.database, schema: n.schema})
		}
		if len(connection.SortedObjects(objs, true)) > 0 {
			out = append(out, navNode{kind: nodeFolder, conn: n.conn, database: n.database, schema: n.schema, views: true})
		}
		if out == nil {
			return info(cn, "No tables")
		}
		return out
	case nodeFolder:
		var out []navNode
		filter := strings.ToLower(a.nav.filter)
		for _, o := range connection.SortedObjects(cn.Objects[connection.SchemaKey{Database: n.database, Schema: n.schema}], n.views) {
			if filter == "" || strings.Contains(strings.ToLower(o.Name), filter) {
				out = append(out, navNode{kind: nodeObject, conn: n.conn, database: n.database, schema: n.schema, name: o.Name})
			}
		}
		if out == nil {
			return info(cn, "No match")
		}
		return out
	case nodeObject:
		key := connection.ObjectKey{Database: n.database, Schema: n.schema, Name: n.name}
		cols, ok := cn.Columns[key]
		if !ok {
			if open {
				connection.LoadColumns(a, cn, n.database, n.schema, n.name, nil)
			}
			return info(cn, "Loading…")
		}
		out := make([]navNode, len(cols))
		for i, c := range cols {
			out[i] = navNode{kind: nodeColumn, conn: n.conn, database: n.database, schema: n.schema, name: n.name + "\x00" + c.Name}
		}
		return out
	}
	return nil
}

func (a *App) schemaNodes(cn *connection.Conn, database string, open bool) []navNode {
	schemas, ok := cn.Schemas[database]
	if !ok {
		key := connection.SchemaKey{Database: database, Schema: "\x00schemas"}
		if e := cn.LoadErr[key]; e != "" {
			return info(cn, "Failed: "+widgets.FirstLine(e))
		}
		if open {
			a.loadSchemas(cn, database)
		}
		return info(cn, "Loading…")
	}
	out := make([]navNode, len(schemas))
	for i, s := range schemas {
		out[i] = navNode{kind: nodeSchema, conn: cn.Config.ID, database: database, schema: s}
	}
	return out
}

// object returns the schema object a node names.
func (a *App) object(n navNode) (*connection.Conn, db.Object, bool) {
	cn := a.connByID(n.conn)
	if cn == nil {
		return nil, db.Object{}, false
	}
	for _, o := range cn.Objects[connection.SchemaKey{Database: n.database, Schema: n.schema}] {
		if o.Name == n.name {
			return cn, o, true
		}
	}
	return cn, db.Object{}, false
}

// sidebar builds the navigator.
func (a *App) sidebar(c *ui.Context) {
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Column(c).FillHeight().Width(a.settings.SidebarWidth).Background(pal.Sidebar).Children(func() {
		ui.Row(c).Padding(8, 8, 6, 12).Gap(6).Children(func() {
			ui.Text(c, "Projects").FontSize(12).Bold().TextColor(pal.Muted).Grow(1)
			widgets.IconButton(c, widgets.IconPlus, "New…").Menu(a.addMenu)
		})
		ui.Row(c).Padding(0, 8, 6, 8).Children(func() {
			in := widgets.SearchBox(c, &a.nav.filter, "Filter tables", 0)
			if a.focusWant == "filter" && a.ActiveTab() == nil {
				if in.Focus().Focused() {
					a.focusWant = ""
				}
			}
		})
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
		tree := ui.Outline(c, &a.nav.tree, a.navRoots(), a.navChildren, func(n navNode) {
			a.navRow(c, n)
		}).Grow(1).Label("Navigator")
		if tree.Submitted() && a.nav.row >= 0 && a.nav.row < a.nav.tree.Rows() {
			a.activate(a.nav.tree.Item(a.nav.row))
		}
		if a.focusWant == "nav" {
			if a.nav.row < 0 && a.nav.tree.Rows() > 0 {
				a.nav.row = 0
			}
			if a.nav.tree.List.Focus(c) {
				a.focusWant = ""
			}
		}
		// ⌘↵ on a table: a new editor with its rows, the quickest look.
		if a.nav.tree.List.Shortcut(c, ui.Cmd, ui.KeyEnter) && a.nav.row >= 0 && a.nav.row < a.nav.tree.Rows() {
			if n := a.nav.tree.Item(a.nav.row); n.kind == nodeObject {
				if cn, obj, ok := a.object(n); ok {
					a.NewQueryTab(cn, n.database, "SELECT *\nFROM "+db.QualifiedName(cn.DB.Dialect, obj.Schema, obj.Name)+"\nLIMIT 100;\n")
				}
			}
		}
		_ = t
	})
}

// activate does what a double click on a node does.
func (a *App) activate(n navNode) {
	switch n.kind {
	case nodeProject, nodeQueries:
		if a.nav.tree.Open.Has(n) {
			a.nav.tree.Open.Remove(n)
		} else {
			a.nav.expand(n)
		}
		return
	case nodeQueryFile:
		if p := a.projectByDir(n.projectDir); p != nil {
			a.openQueryFile(p, n.name)
		}
		return
	}
	cn := a.connByID(n.conn)
	if cn == nil {
		return
	}
	switch n.kind {
	case nodeConn:
		if cn.Config.Engine == db.Redis {
			a.openRedis(cn)
			return
		}
		a.nav.expand(n)
		a.Connect(cn, func() { a.expandDefaults(cn) })
	case nodeObject:
		if cn, obj, ok := a.object(n); ok {
			a.OpenTable(cn, n.database, obj, dataview.PageData)
		}
	case nodeDatabase, nodeSchema, nodeFolder:
		if a.nav.tree.Open.Has(n) {
			a.nav.tree.Open.Remove(n)
		} else {
			a.nav.expand(n)
		}
	}
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

func (a *App) navRow(c *ui.Context, n navNode) {
	pal := widgets.PaletteOf(c)
	t := c.Theme()
	if n.projectDir != "" {
		a.projectRow(c, n)
		return
	}
	cn := a.connByID(n.conn)
	if cn == nil {
		return
	}
	row := ui.Row(c).Gap(6).Grow(1).PaddingY(1)
	row.Children(func() {
		switch n.kind {
		case nodeConn:
			ui.Box(c).Size(8, 8).Radius(4).Background(widgets.EnvColor(&cn.Config))
			ui.Icon(c, widgets.IconDatabase).TextColor(widgets.EngineColor(cn.Config.Engine)).FontSize(14)
			ui.Text(c, cn.Config.Name).SingleLine().Grow(1).Shrink(1)
			switch cn.Status {
			case connection.StatusConnecting:
				ui.Spinner(c).Size(12, 12)
			case connection.StatusConnected:
				ui.Box(c).Size(6, 6).Radius(3).Background(t.Success).Tooltip("Connected " + cn.Version)
			case connection.StatusFailed:
				ui.Icon(c, widgets.IconAlert).TextColor(t.Danger).FontSize(12).Tooltip(cn.Err)
			}
			if cn.Config.ReadOnly {
				ui.Icon(c, widgets.IconLock).TextColor(pal.Muted).FontSize(12).Tooltip("Read-only")
			}
		case nodeDatabase:
			name := n.database
			if name == "" {
				name = cn.Config.Database
			}
			ui.Icon(c, widgets.IconDatabase).TextColor(pal.Muted).FontSize(13)
			ui.Text(c, name).SingleLine()
		case nodeSchema:
			ui.Icon(c, widgets.IconSchema).TextColor(pal.Muted).FontSize(13)
			ui.Text(c, n.schema).SingleLine()
		case nodeFolder:
			objs := connection.SortedObjects(cn.Objects[connection.SchemaKey{Database: n.database, Schema: n.schema}], n.views)
			label := "Tables"
			if n.views {
				label = "Views"
			}
			ui.Text(c, label).SingleLine()
			ui.Text(c, fmt.Sprint(len(objs))).FontSize(11).TextColor(pal.Muted)
		case nodeObject:
			_, obj, _ := a.object(n)
			ic := widgets.IconTable
			if obj.Kind == db.KindView || obj.Kind == db.KindMaterializedView {
				ic = widgets.IconView
			}
			ui.Icon(c, ic).TextColor(pal.Muted).FontSize(13)
			ui.Text(c, n.name).SingleLine().Grow(1).Shrink(1)
			if obj.Rows >= 0 && obj.Kind == db.KindTable {
				ui.Text(c, widgets.HumanCount(obj.Rows)).FontSize(11).TextColor(pal.Muted)
			}
		case nodeColumn:
			table, col, _ := strings.Cut(n.name, "\x00")
			var column db.Column
			for _, cc := range cn.Columns[connection.ObjectKey{Database: n.database, Schema: n.schema, Name: table}] {
				if cc.Name == col {
					column = cc
				}
			}
			if column.PrimaryKey {
				ui.Icon(c, widgets.IconKey).TextColor(ui.Hex("#d97706")).FontSize(12)
			} else {
				ui.Icon(c, widgets.IconColumns).TextColor(pal.Muted).FontSize(12)
			}
			ui.Text(c, col).SingleLine()
			ui.Text(c, column.Type).FontSize(11).TextColor(pal.Muted).SingleLine().Shrink(1)
		case nodeInfo:
			ui.Text(c, n.name).FontSize(12).TextColor(pal.Muted).Italic().SingleLine()
		}
	})
	row.ContextMenu(func(m *ui.Menu) { a.navMenu(m, n) })
}

// navMenu is the context menu of a node.
func (a *App) navMenu(m *ui.Menu, n navNode) {
	if n.projectDir != "" {
		a.projectMenu(m, n)
		return
	}
	cn := a.connByID(n.conn)
	if cn == nil {
		return
	}
	switch n.kind {
	case nodeConn:
		if cn.Status == connection.StatusConnected {
			if m.Item("Disconnect").Chosen() {
				a.requestDisconnect("Disconnect "+cn.Config.Name+"?", []*connection.Conn{cn}, func() { a.disconnect(cn) })
			}
			if m.Item("Refresh").Shortcut(ui.Cmd, ui.KeyR).Chosen() {
				a.refresh(cn)
			}
		} else if m.Item("Connect").Chosen() {
			a.activate(n)
		}
		if cn.Config.Engine == db.Redis {
			if m.Item("Open Key Browser").Chosen() {
				a.openRedis(cn)
			}
		} else {
			if m.Item("New SQL Editor").Shortcut(ui.Cmd, ui.KeyT).Chosen() {
				a.NewQueryTab(cn, "", "")
			}
			if m.Item("Run SQL File…").Chosen() {
				a.openSQLFileRun(cn, "")
			}
		}
		if !cn.Config.Engine.IsFile() && m.Item("Server Activity").Chosen() {
			a.openActivity(cn)
		}
		m.Separator()
		if m.Item("Edit Connection…").Chosen() {
			a.openConnForm(cn)
		}
		if m.Item("Duplicate").Chosen() {
			a.duplicateConn(cn)
		}
		m.Separator()
		if m.Item("Delete Connection…").Chosen() {
			a.askDeleteConn(cn)
		}
	case nodeObject:
		cn, obj, ok := a.object(n)
		if !ok {
			return
		}
		if m.Item("Open Data").Chosen() {
			a.OpenTable(cn, n.database, obj, dataview.PageData)
		}
		if m.Item("View Structure").Chosen() {
			a.OpenTable(cn, n.database, obj, dataview.PageStructure)
		}
		if m.Item("View DDL").Chosen() {
			a.OpenTable(cn, n.database, obj, dataview.PageDDL)
		}
		m.Separator()
		quoted := db.QualifiedName(cn.DB.Dialect, obj.Schema, obj.Name)
		if m.Item("New SQL: SELECT").Chosen() {
			a.NewQueryTab(cn, n.database, "SELECT *\nFROM "+quoted+"\nLIMIT 100;\n")
		}
		if m.Item("Copy Name").Chosen() {
			a.WriteClipboard(quoted)
		}
		if m.Item("Export Data…").Chosen() {
			dataview.OpenExport(a, dataview.ExportSource{Conn: cn, Database: n.database, Name: obj.Name, SQL: "SELECT * FROM " + quoted})
		}
		if obj.Kind == db.KindTable {
			if m.Item("Import Data…").Disabled(cn.Config.ReadOnly).Chosen() {
				a.openImport(cn, n.database, obj.Schema, &obj)
			}
			m.Separator()
			if m.Item("Truncate…").Chosen() {
				a.NewQueryTab(cn, n.database, "TRUNCATE TABLE "+quoted+";\n")
			}
			if m.Item("Drop…").Chosen() {
				a.NewQueryTab(cn, n.database, "DROP TABLE "+quoted+";\n")
			}
		}
	case nodeSchema, nodeDatabase, nodeFolder:
		if n.kind != nodeDatabase && m.Item("View ER Diagram").Chosen() {
			dataview.OpenER(a, cn, n.database, n.schema)
		}
		if n.kind != nodeDatabase && m.Item("Import File as New Table…").Disabled(cn.Config.ReadOnly).Chosen() {
			a.openImport(cn, n.database, n.schema, nil)
		}
		if n.kind == nodeDatabase && m.Item("Run SQL File…").Chosen() {
			a.openSQLFileRun(cn, n.database)
		}
		if m.Item("New SQL Editor").Chosen() {
			text := ""
			if n.kind == nodeSchema && cn.Config.Engine == db.Postgres {
				text = "SET search_path TO " + cn.DB.Dialect.Quote(n.schema) + ";\n\n"
			}
			a.NewQueryTab(cn, n.database, text)
		}
		if m.Item("Refresh").Chosen() {
			if n.kind == nodeDatabase {
				delete(cn.Schemas, n.database)
			} else {
				delete(cn.Objects, connection.SchemaKey{Database: n.database, Schema: n.schema})
			}
		}
	}
}

// projectRow shows a project, its Queries folder, a query file, or a note.
func (a *App) projectRow(c *ui.Context, n navNode) {
	pal := widgets.PaletteOf(c)
	p := a.projectByDir(n.projectDir)
	if p == nil {
		return
	}
	row := ui.Row(c).Gap(6).Grow(1).PaddingY(1)
	row.Children(func() {
		switch n.kind {
		case nodeProject:
			col := c.Theme().Accent
			if p.Err != "" {
				col = pal.Muted
			}
			ui.Icon(c, widgets.IconFolder).TextColor(col).FontSize(14)
			name := ui.Text(c, a.projectLabel(p)).Bold().SingleLine().Grow(1).Shrink(1).Tooltip(p.Dir)
			if p.Err != "" {
				name.TextColor(pal.Muted)
				ui.Icon(c, widgets.IconAlert).TextColor(c.Theme().Danger).FontSize(12).Tooltip(p.Err)
			}
		case nodeQueries:
			ui.Icon(c, widgets.IconCode).TextColor(pal.Muted).FontSize(13)
			ui.Text(c, "Queries").SingleLine().Grow(1)
			ui.Text(c, fmt.Sprint(len(p.Files))).FontSize(11).TextColor(pal.Muted)
		case nodeQueryFile:
			ui.Icon(c, widgets.IconFile).TextColor(pal.Muted).FontSize(12)
			ui.Text(c, n.name).SingleLine().Shrink(1).Tooltip(filepath.Join(p.Queries, filepath.FromSlash(n.name)))
		case nodeInfo:
			ui.Text(c, n.name).FontSize(12).TextColor(pal.Muted).Italic().SingleLine()
		}
	})
	row.ContextMenu(func(m *ui.Menu) { a.navMenu(m, n) })
}

// projectMenu is the context menu of a project's own nodes.
func (a *App) projectMenu(m *ui.Menu, n navNode) {
	p := a.projectByDir(n.projectDir)
	if p == nil {
		return
	}
	if p.Err == "" {
		if m.Item("New Connection…").Chosen() {
			a.selectProject(p)
			a.openConnForm(nil)
			if f := a.connForm; f != nil {
				f.project, f.projectSel = p, a.projectLabel(p)
			}
		}
		if conns := a.projectConns(p); len(conns) > 0 && m.Item("New Query").Chosen() {
			cn := a.activeConn()
			if cn == nil || cn.Project != p || !cn.Config.Engine.IsSQL() {
				cn = nil
				for _, c := range conns {
					if c.Config.Engine.IsSQL() {
						cn = c
						break
					}
				}
			}
			if cn != nil {
				a.NewQueryTab(cn, "", "")
			}
		}
		if n.kind == nodeQueryFile {
			if m.Item("Rename…").Chosen() {
				a.askRename(p, filepath.Join(p.Queries, filepath.FromSlash(n.name)))
			}
		}
		m.Separator()
		if m.Item("Query History").Chosen() {
			a.openHistoryOf(p)
		}
		if m.Item("Audit Log").Chosen() {
			a.openAuditOf(p)
		}
	}
	if m.Item("Show in Files").Chosen() {
		mygo.Shell.OpenPath(p.Dir)
	}
	if n.kind == nodeProject {
		m.Separator()
		if m.Item("Reload").Chosen() {
			a.reloadProject(p)
		}
		if m.Item("Remove from Sidebar…").Chosen() {
			a.removeProject(p)
		}
	}
}

// addMenu lists what the sidebar's + makes.
func (a *App) addMenu(m *ui.Menu) {
	if len(a.usableProjects()) > 0 && m.Item("New Connection…").Chosen() {
		a.openConnForm(nil)
	}
	if m.Item("New Project…").Chosen() {
		a.openNewProject(nil)
	}
	if m.Item("Add Existing Folder…").Chosen() {
		a.addExistingProject()
	}
}

// selectProject opens a project in the sidebar and chooses it.
func (a *App) selectProject(p *project.Project) {
	n := navNode{kind: nodeProject, projectDir: p.Dir}
	a.nav.expand(n)
	for i := 0; i < a.nav.tree.Rows(); i++ {
		if a.nav.tree.Item(i) == n {
			a.nav.row = i
			return
		}
	}
}
