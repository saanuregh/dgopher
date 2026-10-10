package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/project"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type nodeKind uint8

const (
	nodeProject     nodeKind = iota
	nodeConnections          // the connections of a project
	nodeQueries              // the query files of a project
	nodeQueryFile
	nodeDashboards // the dashboards of a project
	nodeDashboard  // named by its file's path
	nodeModels     // the data models of a project
	nodeModel      // named by its file's path
	nodeConn
	nodeDatabase
	nodeSchema
	nodeFolder // the tables, views or items of a kind of a schema, or a table's partitions
	nodeObject
	nodeColumn
	nodeItem // a routine, trigger, sequence, type and the like
	nodeInfo // loading, an error, or nothing there
)

// The folders of a schema besides those of its items, by their kind;
// partitions are a table's.
const (
	folderTables     = ""
	folderViews      = "views"
	folderPartitions = string(db.ItemPartition)
)

// navNode is an item of the navigator's tree.
type navNode struct {
	kind nodeKind
	// projectDir is the folder of a project's own nodes: the project, its
	// sections, their files, and their notes. The nodes of a connection
	// are named by its ID alone, unique across projects.
	projectDir string
	conn       string
	database   string
	schema     string
	// name is an object's, a table's for its partitions' folder, or, for
	// an item, its kind and label (itemName).
	name   string
	folder string // a folder's: folderTables, folderViews, or an item kind
}

type navState struct {
	tree ui.OutlineState[navNode]
	row  int
	// shown is the tab in front the navigator last followed; reveal is
	// its node, until the tree shows it, and opened the nodes opened to
	// show it.
	shown  widgets.Tab
	reveal *navNode
	opened map[navNode]bool
	// chose is the row chosen as the last frame ended, which a choice
	// moved from.
	chose int
	// listed are the projects shown already, whose Connections were
	// opened as they first showed.
	listed map[string]bool
}

func (n *navState) init() {
	n.row, n.chose = -1, -1
	n.tree.List.Selected = &n.row
}

// skipNotes keeps the choice off a note, as "Loading…" or "No tables",
// which nothing is done with: it goes on to the next row the way it moved,
// else back where it was; the next frame, asked at once, shows it.
func (a *App) skipNotes(c *ui.Context) {
	rows, r, was := a.nav.tree.Rows(), a.nav.row, a.nav.chose
	if r >= 0 && r < rows && a.nav.tree.Item(r).kind == nodeInfo {
		step := 1
		if r < was {
			step = -1
		}
		to := -1
		if was >= 0 && was < rows && a.nav.tree.Item(was).kind != nodeInfo {
			to = was
		}
		for i := r; i >= 0 && i < rows; i += step {
			if a.nav.tree.Item(i).kind != nodeInfo {
				to = i
				break
			}
		}
		a.nav.row = to
		if to >= 0 {
			a.nav.tree.List.ScrollIntoView(to)
		}
		c.Invalidate()
	}
	a.nav.chose = a.nav.row
}

// expand opens a node of the tree.
func (n *navState) expand(node navNode) { n.tree.Open.Add(node) }

// navRoots are the projects; one alone shows its sections, a level less
// deep, its name above the tree (sidebarBar).
func (a *App) navRoots() []navNode {
	if len(a.projects) == 1 {
		return a.projectChildren(navNode{kind: nodeProject, projectDir: a.projects[0].Dir})
	}
	roots := make([]navNode, len(a.projects))
	for i, p := range a.projects {
		roots[i] = navNode{kind: nodeProject, projectDir: p.Dir}
	}
	return roots
}

// projectChildren lists a project's sections, its connections, query
// files, dashboards and data models, and what each holds.
func (a *App) projectChildren(n navNode) []navNode {
	p := a.projectByDir(n.projectDir)
	if p == nil {
		return nil
	}
	if p.Err != "" {
		return []navNode{{kind: nodeInfo, projectDir: p.Dir, name: widgets.FirstLine(p.Err)}}
	}
	note := func(text string) []navNode { return []navNode{{kind: nodeInfo, projectDir: p.Dir, name: text}} }
	files := func(kind nodeKind, paths []string, none string) []navNode {
		if len(paths) == 0 {
			return note(none)
		}
		out := make([]navNode, len(paths))
		for i, path := range paths {
			out[i] = navNode{kind: kind, projectDir: p.Dir, name: path}
		}
		return out
	}
	switch n.kind {
	case nodeProject:
		conns := navNode{kind: nodeConnections, projectDir: p.Dir}
		if !a.nav.listed[p.Dir] {
			if a.nav.listed == nil {
				a.nav.listed = map[string]bool{}
			}
			a.nav.listed[p.Dir] = true
			a.nav.expand(conns) // what a project is opened for, first
		}
		return []navNode{conns, {kind: nodeQueries, projectDir: p.Dir}, {kind: nodeDashboards, projectDir: p.Dir}, {kind: nodeModels, projectDir: p.Dir}}
	case nodeConnections:
		var out []navNode
		for _, cn := range a.projectConns(p) {
			out = append(out, navNode{kind: nodeConn, conn: cn.Config.ID})
		}
		if out == nil {
			return note("No connections yet")
		}
		return out
	case nodeDashboards:
		_, paths := a.dashboardNames(p)
		return files(nodeDashboard, paths, "No dashboards yet")
	case nodeModels:
		_, paths := a.modelNames(p)
		return files(nodeModel, paths, "No data models yet")
	case nodeQueries:
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
			return note("No .sql files yet")
		}
		return out
	}
	return nil
}

func info(cn *connection.Conn, text string) []navNode {
	return []navNode{{kind: nodeInfo, conn: cn.Config.ID, name: text}}
}

// schemaChildren are the folders of a schema, which start to be read
// when open: that of the schema's node, or of the node it shows in.
func (a *App) schemaChildren(cn *connection.Conn, n navNode, open bool) []navNode {
	key := connection.SchemaKey{Database: n.database, Schema: n.schema}
	objs, ok := cn.Objects[key]
	if !ok {
		if e := cn.LoadErr[key]; e != "" {
			return info(cn, "Failed: "+widgets.FirstLine(e))
		}
		if open {
			connection.LoadObjects(a, cn, n.database, n.schema)
		}
		return info(cn, "Loading…")
	}
	items, itemsRead := cn.Items[key]
	if !itemsRead && open && cn.ItemsError(n.database, n.schema) == "" {
		connection.LoadItems(a, cn, n.database, n.schema)
	}
	folder := func(name string) navNode {
		return navNode{kind: nodeFolder, conn: n.conn, database: n.database, schema: n.schema, folder: name}
	}
	var out []navNode
	if connection.CountObjects(objs, false) > 0 {
		out = append(out, folder(folderTables))
	}
	if connection.CountObjects(objs, true) > 0 {
		out = append(out, folder(folderViews))
	}
	for _, kind := range db.ItemKinds() {
		if slices.ContainsFunc(items, func(it db.Item) bool { return it.Kind == kind }) {
			out = append(out, folder(string(kind)))
		}
	}
	if e := cn.ItemsError(n.database, n.schema); e != "" {
		out = append(out, info(cn, "Could not read the routines and other objects: "+widgets.FirstLine(e))...)
	}
	if e := cn.SchemaColumnsError(n.database, n.schema); e != "" {
		out = append(out, info(cn, "Could not read the columns at once, so each table's are read as needed: "+widgets.FirstLine(e))...)
	}
	if out == nil {
		return info(cn, "No tables")
	}
	return out
}

// navChildren returns a node's children, starting to read them when the
// node is open and they are not known yet.
func (a *App) navChildren(n navNode) []navNode {
	switch n.kind {
	case nodeProject, nodeConnections, nodeQueries, nodeDashboards, nodeModels:
		return a.projectChildren(n)
	case nodeQueryFile, nodeDashboard, nodeModel, nodeInfo:
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
		return a.schemaChildren(cn, n, open)
	case nodeFolder:
		var out []navNode
		key := connection.SchemaKey{Database: n.database, Schema: n.schema}
		switch n.folder {
		case folderTables, folderViews:
			for _, o := range connection.SortedObjects(cn.Objects[key], n.folder == folderViews) {
				out = append(out, navNode{kind: nodeObject, conn: n.conn, database: n.database, schema: n.schema, name: o.Name})
			}
		default:
			for _, it := range cn.Items[key] {
				if string(it.Kind) != n.folder || n.folder == folderPartitions && it.Table != n.name {
					continue
				}
				out = append(out, navNode{kind: nodeItem, conn: n.conn, database: n.database, schema: n.schema, name: itemName(it)})
			}
		}
		if out == nil {
			return info(cn, "None")
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
		var out []navNode
		if _, obj, ok := a.object(n); ok && obj.Partitioning != "" {
			out = append(out, navNode{kind: nodeFolder, conn: n.conn, database: n.database, schema: n.schema, name: n.name, folder: folderPartitions})
		}
		for _, c := range cols {
			out = append(out, navNode{kind: nodeColumn, conn: n.conn, database: n.database, schema: n.schema, name: n.name + "\x00" + c.Name})
		}
		return out
	}
	return nil
}

// itemName names an item's node: by its kind and label, unique in its
// schema, as overloaded functions' labels hold their arguments.
func itemName(it db.Item) string { return string(it.Kind) + "\x00" + it.Label() }

// item returns the item a node names.
func (a *App) item(n navNode) (*connection.Conn, db.Item, bool) {
	cn := a.connByID(n.conn)
	if cn == nil {
		return nil, db.Item{}, false
	}
	for _, it := range cn.Items[connection.SchemaKey{Database: n.database, Schema: n.schema}] {
		if k := string(it.Kind); len(n.name) <= len(k) || n.name[len(k)] != 0 || n.name[:len(k)] != k {
			continue
		}
		if itemName(it) == n.name {
			return cn, it, true
		}
	}
	return cn, db.Item{}, false
}

// itemIcon is the icon of an item's kind.
func itemIcon(k db.ItemKind) *ui.SVG {
	switch k {
	case db.ItemFunction, db.ItemProcedure:
		return widgets.IconCode
	case db.ItemTrigger:
		return widgets.IconPlay
	case db.ItemSequence, db.ItemProjection:
		return widgets.IconLayers
	case db.ItemType:
		return widgets.IconSchema
	case db.ItemExtension:
		return widgets.IconPlug
	case db.ItemEvent:
		return widgets.IconClock
	}
	return widgets.IconTable
}

// openItem opens an item: a partition's rows, else its definition, in an
// editor where it can be changed and run.
func (a *App) openItem(cn *connection.Conn, database string, it db.Item) {
	if it.Kind == db.ItemPartition {
		a.OpenTable(cn, database, db.Object{Schema: it.Schema, Name: it.Name, Kind: db.KindTable, Rows: -1}, dataview.PageData)
		return
	}
	dataview.OpenItemDefinition(a, cn, database, it)
}

// schemaNodes are the schemas of a connection's database; a database of
// one schema shows its folders instead, the schema named on its row
// (soleSchema), a level less deep.
func (a *App) schemaNodes(cn *connection.Conn, database string, open bool) []navNode {
	schemas, ok := cn.Schemas[database]
	if !ok {
		if e := cn.SchemasError(database); e != "" {
			return info(cn, "Failed: "+widgets.FirstLine(e))
		}
		if open {
			a.loadSchemas(cn, database)
		}
		return info(cn, "Loading…")
	}
	if s, ok := soleSchema(cn, database); ok {
		return a.schemaChildren(cn, navNode{kind: nodeSchema, conn: cn.Config.ID, database: database, schema: s}, open)
	}
	out := make([]navNode, len(schemas))
	for i, s := range schemas {
		out[i] = navNode{kind: nodeSchema, conn: cn.Config.ID, database: database, schema: s}
	}
	return out
}

// soleSchema is the schema of a database that has one alone, which shows
// in its node.
func soleSchema(cn *connection.Conn, database string) (string, bool) {
	if schemas := cn.Schemas[database]; len(schemas) == 1 {
		return schemas[0], true
	}
	return "", false
}

// schemaOf is the node of the schema a connection or a database shows in
// place of its sole one.
func schemaOf(cn *connection.Conn, n navNode) (navNode, bool) {
	if n.kind == nodeConn && len(cn.Databases) > 1 {
		return navNode{}, false // its databases show
	}
	s, ok := soleSchema(cn, n.database)
	return navNode{kind: nodeSchema, conn: cn.Config.ID, database: n.database, schema: s}, ok
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
		a.followTab()
		a.openToReveal()
		tree := ui.Outline(c, &a.nav.tree, a.navRoots(), a.navChildren, func(n navNode) {
			a.navRow(c, n)
		}).Grow(1).Label("Navigator")
		a.chooseRevealed(c)
		a.skipNotes(c)
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
		if keymap.ListPressed(c, &a.nav.tree.List, keymap.NavigatorRefresh) && a.nav.row >= 0 && a.nav.row < a.nav.tree.Rows() {
			if cn := a.connByID(a.nav.tree.Item(a.nav.row).conn); cn != nil {
				a.refresh(cn)
			}
		}
		a.nodeKeys(c)
		// On a table: a new editor with its rows, the quickest look.
		if keymap.ListPressed(c, &a.nav.tree.List, keymap.NavigatorSelect) && a.nav.row >= 0 && a.nav.row < a.nav.tree.Rows() {
			if n := a.nav.tree.Item(a.nav.row); n.kind == nodeObject {
				if cn, obj, ok := a.object(n); ok {
					a.NewQueryTab(cn, n.database, "SELECT *\nFROM "+db.QualifiedName(cn.DB.Dialect, obj.Schema, obj.Name)+"\nLIMIT 100;\n")
				}
			}
		}
		_ = t
	})
}

// nodeKeys handles the keys of the node chosen in the navigator, as its
// menu's items: a table's, or a project file's.
func (a *App) nodeKeys(c *ui.Context) {
	l := &a.nav.tree.List
	if a.nav.row < 0 || a.nav.row >= a.nav.tree.Rows() {
		return
	}
	n := a.nav.tree.Item(a.nav.row)
	if p := a.projectByDir(n.projectDir); p != nil {
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
	quoted := db.QualifiedName(cn.DB.Dialect, obj.Schema, obj.Name)
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
	_, err := db.RenameObjectSQL(cn.DB.Dialect, obj, obj.Name)
	return err == nil
}

// renameObject asks for an object's new name, and renames it.
func (a *App) renameObject(cn *connection.Conn, database string, obj db.Object) {
	a.askRenameInDatabase(cn, database, obj.Name, func(name string) (string, error) {
		return db.RenameObjectSQL(cn.DB.Dialect, obj, name)
	})
}

// activate does what a double click on a node does.
func (a *App) activate(n navNode) {
	switch n.kind {
	case nodeProject, nodeConnections, nodeQueries, nodeDashboards, nodeModels:
		if a.nav.tree.Open.Has(n) {
			a.nav.tree.Open.Remove(n)
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
	case nodeItem:
		if cn, it, ok := a.item(n); ok {
			a.openItem(cn, n.database, it)
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
			name := ui.Text(c, cn.Config.Name).SingleLine().Shrink(1)
			if sn, ok := schemaOf(cn, n); ok {
				ui.Text(c, sn.schema).FontSize(12).TextColor(pal.Muted).SingleLine().Grow(1).Shrink(1).Tooltip("Its schema")
			} else {
				name.Grow(1)
			}
			switch cn.Status {
			case connection.StatusConnecting:
				ui.Spinner(c).Size(12, 12)
			case connection.StatusConnected:
				tip := "Connected " + cn.Version
				if s := tlsStateOf(cn.DB, cn.KV); s != "" {
					tip += ", " + s
				}
				ui.Box(c).Size(6, 6).Radius(3).Background(t.Success).Tooltip(tip)
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
			ui.Text(c, name).SingleLine().Shrink(1)
			if sn, ok := schemaOf(cn, n); ok {
				ui.Text(c, sn.schema).FontSize(12).TextColor(pal.Muted).SingleLine().Shrink(1).Tooltip("Its schema")
			}
		case nodeSchema:
			ui.Icon(c, widgets.IconSchema).TextColor(pal.Muted).FontSize(13)
			ui.Text(c, n.schema).SingleLine()
		case nodeFolder:
			key := connection.SchemaKey{Database: n.database, Schema: n.schema}
			var label string
			var count int
			switch n.folder {
			case folderTables, folderViews:
				label, count = "Tables", connection.CountObjects(cn.Objects[key], n.folder == folderViews)
				if n.folder == folderViews {
					label = "Views"
				}
			default:
				label = db.ItemKind(n.folder).Plural()
				for _, it := range cn.Items[key] {
					if string(it.Kind) == n.folder && (n.folder != folderPartitions || it.Table == n.name) {
						count++
					}
				}
			}
			ui.Text(c, label).SingleLine()
			ui.Text(c, fmt.Sprint(count)).FontSize(11).TextColor(pal.Muted)
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
				ui.Icon(c, widgets.IconKey).TextColor(c.Theme().Warning).FontSize(12)
			} else {
				ui.Icon(c, widgets.IconColumns).TextColor(pal.Muted).FontSize(12)
			}
			ui.Text(c, col).SingleLine()
			ui.Text(c, column.Type).FontSize(11).TextColor(pal.Muted).SingleLine().Shrink(1)
		case nodeItem:
			_, it, _ := a.item(n)
			ui.Icon(c, itemIcon(it.Kind)).TextColor(pal.Muted).FontSize(12)
			label := it.Label()
			if it.Kind == db.ItemPartition {
				label = it.Name
			}
			ui.Text(c, label).SingleLine().Grow(1).Shrink(1).Tooltip(label)
			if it.Kind != db.ItemFunction && it.Kind != db.ItemProcedure && it.Detail != "" {
				ui.Text(c, it.Detail).FontSize(11).TextColor(pal.Muted).SingleLine().Shrink(1)
			}
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
			if keymap.Item(m.Item("Refresh"), keymap.NavigatorRefresh).Chosen() {
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
			if keymap.Item(m.Item("New SQL Editor"), keymap.NewEditor).Chosen() {
				a.NewQueryTab(cn, "", "")
			}
			if m.Item("Run SQL File…").Chosen() {
				a.openSQLFileRun(cn, "")
			}
			if m.Item("Search Objects…").Chosen() {
				a.openSearch(cn, "")
			}
		}
		if !cn.Config.Engine.IsFile() && m.Item("Server Activity").Chosen() {
			a.openActivity(cn)
		}
		if db.UsersSupported(cn.Config.Engine) && m.Item("Users and Privileges").Chosen() {
			a.openUsers(cn)
		}
		if cn.Config.Engine.IsSQL() && m.Item("Catalog Queries").Disabled(cn.DB == nil).Chosen() {
			a.openCatalogQueries(cn)
		}
		if backupSupported(cn.Config.Engine) {
			m.Separator()
			if m.Item("Back Up…").Chosen() {
				a.Connect(cn, func() { a.openBackup(cn, "", false) })
			}
			if m.Item("Restore…").Disabled(cn.Config.ReadOnly).Chosen() {
				a.Connect(cn, func() { a.openBackup(cn, "", true) })
			}
		}
		if sn, ok := schemaOf(cn, n); ok {
			m.Separator()
			m.Submenu("Schema "+sn.schema, func(m *ui.Menu) { a.navMenu(m, sn) })
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
		if keymap.Item(m.Item("View Structure"), keymap.NavigatorStructure).Chosen() {
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
		if m.Item("Build a Query…").Chosen() {
			dataview.OpenQueryBuilder(a, cn, n.database, obj)
		}
		if keymap.Item(m.Item("Copy Name"), keymap.NavigatorCopyName).Chosen() {
			a.WriteClipboard(quoted)
		}
		if renamable(cn, obj) && keymap.Item(m.Item("Rename…"), keymap.NavigatorRename).Disabled(cn.Config.ReadOnly).Chosen() {
			a.renameObject(cn, n.database, obj)
		}
		if m.Item("Export Data…").Chosen() {
			dataview.OpenExport(a, dataview.ExportSource{Conn: cn, Database: n.database, Schema: obj.Schema, Name: obj.Name, SQL: "SELECT * FROM " + quoted})
		}
		if obj.Kind == db.KindTable && m.Item("Copy to Another Database…").Chosen() {
			a.openCopy(cn, n.database, obj.Schema, []string{obj.Name})
		}
		if obj.Kind == db.KindTable && m.Item("Compare Rows with…").Chosen() {
			a.openRowCompare(cn, n.database, obj)
		}
		if obj.Kind == db.KindTable {
			if m.Item("Import Data…").Disabled(cn.Config.ReadOnly).Chosen() {
				a.openImport(cn, n.database, obj.Schema, &obj)
			}
			if m.Item("Generate Test Data…").Disabled(cn.Config.ReadOnly).Chosen() {
				a.openFill(cn, n.database, obj)
			}
			if obj.Partitioning != "" && m.Item("New Partition…").Disabled(cn.Config.ReadOnly).Chosen() {
				a.NewQueryTab(cn, n.database, partitionTemplate(cn.DB.Dialect, obj))
			}
			if cmds := maintenanceCommands(cn.Config.Engine, quoted); len(cmds) > 0 {
				m.Submenu("Maintenance", func(m *ui.Menu) {
					for _, mc := range cmds {
						if m.Item(mc.label).Disabled(cn.Config.ReadOnly).Chosen() {
							a.runInEditor(cn, n.database, mc.sql)
						}
					}
				})
			}
			m.Separator()
			if m.Item("Truncate…").Chosen() {
				a.NewQueryTab(cn, n.database, "TRUNCATE TABLE "+quoted+";\n")
			}
			if keymap.Item(m.Item("Drop…"), keymap.NavigatorDrop).Chosen() {
				a.NewQueryTab(cn, n.database, "DROP TABLE "+quoted+";\n")
			}
		}
	case nodeColumn:
		table, column, _ := strings.Cut(n.name, "\x00")
		cn, obj, ok := a.object(navNode{conn: n.conn, database: n.database, schema: n.schema, name: table})
		if !ok {
			return
		}
		if m.Item("Copy Name").Chosen() {
			a.WriteClipboard(cn.DB.Dialect.Quote(column))
		}
		if obj.Kind == db.KindTable && m.Item("Rename…").Disabled(cn.Config.ReadOnly).Chosen() {
			a.askRenameInDatabase(cn, n.database, column, func(name string) (string, error) {
				return db.RenameColumnSQL(cn.DB.Dialect, obj.Schema, obj.Name, column, name), nil
			})
		}
	case nodeItem:
		cn, it, ok := a.item(n)
		if !ok {
			return
		}
		if it.Kind == db.ItemPartition {
			if m.Item("Open Data").Chosen() {
				a.openItem(cn, n.database, it)
			}
			if m.Item("Detach…").Disabled(cn.Config.ReadOnly).Chosen() {
				d := cn.DB.Dialect
				a.NewQueryTab(cn, n.database, "ALTER TABLE "+db.QualifiedName(d, it.Schema, it.Table)+" DETACH PARTITION "+db.QualifiedName(d, it.Schema, it.Name)+";\n")
			}
		}
		if m.Item("Open Definition").Chosen() {
			dataview.OpenItemDefinition(a, cn, n.database, it)
		}
		if m.Item("Copy Name").Chosen() {
			a.WriteClipboard(db.QualifiedName(cn.DB.Dialect, it.Schema, it.Name))
		}
	case nodeSchema, nodeDatabase, nodeFolder:
		if n.kind != nodeDatabase && m.Item("New Table…").Disabled(cn.Config.ReadOnly).Chosen() {
			dataview.OpenNewTable(a, cn, n.database, n.schema)
		}
		if n.kind != nodeDatabase && m.Item("View ER Diagram").Chosen() {
			dataview.OpenER(a, cn, n.database, n.schema)
		}
		if n.kind != nodeDatabase && m.Item("Import File as New Table…").Disabled(cn.Config.ReadOnly).Chosen() {
			a.openImport(cn, n.database, n.schema, nil)
		}
		if n.kind != nodeDatabase && m.Item("Export Tables…").Chosen() {
			a.openExportTables(cn, n.database, n.schema)
		}
		if n.kind != nodeDatabase && m.Item("Copy Tables to Another Database…").Chosen() {
			a.openCopy(cn, n.database, n.schema, nil)
		}
		if n.kind != nodeDatabase && m.Item("Generate SQL Script…").Chosen() {
			a.openGenerate(cn, n.database, n.schema, generateScript)
		}
		if n.kind != nodeDatabase && m.Item("Generate Documentation…").Chosen() {
			a.openGenerate(cn, n.database, n.schema, generateDocs)
		}
		if n.kind != nodeDatabase && m.Item("Save as Data Model…").Chosen() {
			a.openSaveModel(cn, n.database, n.schema)
		}
		if n.kind == nodeDatabase && m.Item("Run SQL File…").Chosen() {
			a.openSQLFileRun(cn, n.database)
		}
		// A PostgreSQL database, or a MySQL schema, which is its database.
		if database, ok := backupTarget(cn, n); ok {
			if m.Item("Back Up…").Chosen() {
				a.openBackup(cn, database, false)
			}
			if m.Item("Restore…").Disabled(cn.Config.ReadOnly).Chosen() {
				a.openBackup(cn, database, true)
			}
		}
		if m.Item("Search Objects…").Chosen() {
			a.openSearch(cn, n.database)
		}
		if n.kind == nodeSchema && cn.Config.Engine.IsSQL() {
			m.Submenu("Read Catalog", func(m *ui.Menu) {
				now := cn.Config.CatalogOf(n.database, n.schema)
				for _, d := range db.CatalogDepths {
					if m.Item(catalogLabel(d)).Checked(d == now).Chosen() && d != now {
						a.setCatalogDepth(cn, n.database, n.schema, d)
					}
				}
			})
		}
		if m.Item("New SQL Editor").Chosen() {
			text := ""
			if n.kind == nodeSchema && cn.Config.Engine == db.Postgres {
				text = "SET search_path TO " + cn.DB.Dialect.Quote(n.schema) + ";\n\n"
			}
			a.NewQueryTab(cn, n.database, text)
		}
		if sn, ok := schemaOf(cn, n); ok && n.kind == nodeDatabase {
			m.Submenu("Schema "+sn.schema, func(m *ui.Menu) { a.navMenu(m, sn) })
		}
		if m.Item("Refresh").Chosen() {
			if n.kind == nodeDatabase {
				delete(cn.Schemas, n.database)
			} else {
				delete(cn.Objects, connection.SchemaKey{Database: n.database, Schema: n.schema})
				delete(cn.Items, connection.SchemaKey{Database: n.database, Schema: n.schema})
			}
		}
	}
}

// openExportTables opens the export of a schema's tables and views,
// the tables chosen.
func (a *App) openExportTables(cn *connection.Conn, database, schema string) {
	poolOf := cn.PoolFor(database)
	a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := poolOf(ctx)
		var objs []db.Object
		if err == nil {
			objs, err = d.Dialect.Objects(ctx, d.Catalog(), schema)
		}
		return func() {
			if err != nil {
				a.ShowError("Could not list the tables of "+schema, err.Error())
				return
			}
			if len(objs) == 0 {
				a.ShowError("Nothing to export", schema+" has no tables or views.")
				return
			}
			srcs := make([]dataview.ExportSource, len(objs))
			chosen := make([]bool, len(objs))
			for i, o := range objs {
				srcs[i] = dataview.ExportSource{Conn: cn, Database: database, Schema: o.Schema, Name: o.Name,
					SQL: "SELECT * FROM " + db.QualifiedName(d.Dialect, o.Schema, o.Name)}
				chosen[i] = o.Kind == db.KindTable
			}
			dataview.OpenExportTables(a, "Export tables of "+schema, srcs, chosen)
		}
	})
}

// askRenameInDatabase asks for a new name of a table, view or column
// called current, and renames it by the statement rename writes, which
// runs as the safety policy says. The navigator then reads the catalog
// again.
func (a *App) askRenameInDatabase(cn *connection.Conn, database, current string, rename func(name string) (string, error)) {
	a.renaming = &renameForm{open: true, title: "Rename " + current, name: current, rename: func(name string) error {
		name = strings.TrimSpace(name)
		if name == "" {
			return errors.New("A name may not be empty.")
		}
		if name == current {
			return nil
		}
		sql, err := rename(name)
		if err != nil {
			return err
		}
		a.applyStatements(cn, database, "Rename "+current, []string{sql}, "", func(err error) {
			cn.ForgetCatalog()
			if err != nil {
				a.ShowError("Rename failed", err.Error())
			}
		})
		return nil
	}}
}

// partitionTemplate is the statement making a new partition of a
// partitioned table, its bounds left to fill in as the table's strategy
// writes them.
func partitionTemplate(d db.Dialect, obj db.Object) string {
	bounds := "FOR VALUES FROM ('…') TO ('…')"
	switch strategy, _, _ := strings.Cut(obj.Partitioning, " "); strings.ToUpper(strategy) {
	case "LIST":
		bounds = "FOR VALUES IN ('…')"
	case "HASH":
		bounds = "FOR VALUES WITH (MODULUS 4, REMAINDER 0)"
	}
	return sqltext.LineComment("A new partition of "+obj.Name+", partitioned by "+obj.Partitioning+": name it and set its bounds, then run it.") + "\n" +
		"CREATE TABLE " + db.QualifiedName(d, obj.Schema, obj.Name+"_new") + "\n  PARTITION OF " + db.QualifiedName(d, obj.Schema, obj.Name) + "\n  " + bounds + ";\n"
}

// maintenance is a command that keeps a table in shape.
type maintenance struct {
	label, sql string
}

// maintenanceCommands are the commands of an engine that keep a table,
// named as quoted, in shape; a label says what the command locks.
func maintenanceCommands(e db.Engine, quoted string) []maintenance {
	switch e {
	case db.Postgres:
		return []maintenance{
			{"Vacuum", "VACUUM " + quoted},
			{"Vacuum and Analyze", "VACUUM ANALYZE " + quoted},
			{"Analyze", "ANALYZE " + quoted},
			{"Reindex (locks writes)", "REINDEX TABLE " + quoted},
			{"Vacuum Full (locks reads and writes)", "VACUUM FULL " + quoted},
		}
	case db.MySQL:
		return []maintenance{
			{"Analyze", "ANALYZE TABLE " + quoted},
			{"Optimize (rebuilds the table)", "OPTIMIZE TABLE " + quoted},
			{"Check", "CHECK TABLE " + quoted},
		}
	case db.SQLite:
		return []maintenance{
			{"Analyze", "ANALYZE " + quoted},
			{"Reindex", "REINDEX " + quoted},
		}
	case db.ClickHouse:
		return []maintenance{
			{"Optimize", "OPTIMIZE TABLE " + quoted},
			{"Optimize Final (merges every part)", "OPTIMIZE TABLE " + quoted + " FINAL"},
		}
	}
	return nil
}

// runInEditor opens an editor on a statement and runs it there, through
// the safety policy, with its result and audit as any run's.
func (a *App) runInEditor(cn *connection.Conn, database, sql string) {
	a.newQueryFile(cn, database, sql+";\n", func(q *query.Tab) { q.Run(query.RunScript) })
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
			a.projectName(c, p)
		case nodeConnections, nodeQueries, nodeDashboards, nodeModels:
			ic, label, count := a.section(p, n.kind)
			ui.Icon(c, ic).TextColor(pal.Muted).FontSize(13)
			ui.Text(c, label).SingleLine().Grow(1)
			ui.Text(c, fmt.Sprint(count)).FontSize(11).TextColor(pal.Muted)
			// Another made from the row, shown as the row is pointed at; a
			// section that cannot have one keeps the room, for the counts
			// to line up.
			var newLabel string
			var run func()
			if p.Err == "" {
				newLabel, run = a.sectionNew(p, n.kind)
			}
			add := widgets.IconButton(c, widgets.IconPlus, newLabel).Padding(1).Disabled(run == nil)
			if run == nil || !row.Hovered() {
				add.Opacity(0)
			}
			if run != nil && add.Clicked() {
				run()
			}
		case nodeQueryFile:
			ui.Icon(c, widgets.IconFile).TextColor(pal.Muted).FontSize(12)
			ui.Text(c, n.name).SingleLine().Shrink(1).Tooltip(filepath.Join(p.Queries, filepath.FromSlash(n.name)))
		case nodeDashboard, nodeModel:
			ic, names, paths := widgets.IconLayers, []string(nil), []string(nil)
			if n.kind == nodeDashboard {
				names, paths = a.dashboardNames(p)
			} else {
				ic = widgets.IconSchema
				names, paths = a.modelNames(p)
			}
			name := nameOf(names, paths, n.name)
			ui.Icon(c, ic).TextColor(pal.Muted).FontSize(12)
			ui.Text(c, name).SingleLine().Shrink(1).Tooltip(n.name)
		case nodeInfo:
			ui.Text(c, n.name).FontSize(12).TextColor(pal.Muted).Italic().SingleLine()
		}
	})
	row.ContextMenu(func(m *ui.Menu) { a.navMenu(m, n) })
}

// section is how a project's section shows: its icon, name and count.
func (a *App) section(p *project.Project, kind nodeKind) (*ui.SVG, string, int) {
	switch kind {
	case nodeConnections:
		return widgets.IconPlug, "Connections", len(a.projectConns(p))
	case nodeDashboards:
		names, _ := a.dashboardNames(p)
		return widgets.IconLayers, "Dashboards", len(names)
	case nodeModels:
		names, _ := a.modelNames(p)
		return widgets.IconSchema, "Data Models", len(names)
	}
	return widgets.IconCode, "Queries", len(p.Files)
}

// sectionNew is what makes another of a section's, by its label: "" when
// the project cannot have one, as an editor without a SQL connection.
func (a *App) sectionNew(p *project.Project, kind nodeKind) (string, func()) {
	switch kind {
	case nodeConnections:
		return "New Connection…", func() {
			a.selectProject(p)
			a.openConnForm(nil)
			if f := a.connForm; f != nil {
				f.project, f.projectSel = p, a.projectLabel(p)
			}
		}
	case nodeQueries:
		cn := a.activeConn()
		if cn == nil || cn.Project != p || !cn.Config.Engine.IsSQL() {
			cn = a.firstSQLConn(p)
		}
		if cn == nil {
			return "", nil
		}
		return "New SQL Editor", func() { a.NewQueryTab(cn, "", "") }
	case nodeDashboards:
		return "New Dashboard…", func() { a.newDashboard(p) }
	case nodeModels:
		return "New Data Model…", func() {
			a.newModel = &newModelForm{open: true, project: p, engine: db.Postgres.Label()}
		}
	}
	return "", nil
}

// projectMenu is the context menu of a project's own nodes.
func (a *App) projectMenu(m *ui.Menu, n navNode) {
	p := a.projectByDir(n.projectDir)
	if p == nil {
		return
	}
	if p.Err == "" {
		// The sidebar lists what is there: the menu makes more.
		for _, kind := range []nodeKind{nodeConnections, nodeDashboards, nodeModels, nodeQueries} {
			if label, run := a.sectionNew(p, kind); label != "" && m.Item(label).Chosen() {
				run()
			}
		}
		if rename, del := a.fileActions(p, n); rename != nil {
			if keymap.Item(m.Item("Rename…"), keymap.NavigatorRename).Chosen() {
				rename()
			}
			if keymap.Item(m.Item("Delete…"), keymap.NavigatorDrop).Chosen() {
				del()
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

// catalogLabel names how much of a schema's catalog is read, as the
// navigator's menu offers it.
func catalogLabel(d db.CatalogDepth) string {
	switch d {
	case db.CatalogNames:
		return "Names Only"
	case db.CatalogColumns:
		return "Names and Columns"
	case db.CatalogEverything:
		return "Everything"
	}
	return "As Needed"
}

// setCatalogDepth sets how much of a schema's catalog a connection reads,
// keeps it in the project's file, and reads the schema again so.
func (a *App) setCatalogDepth(cn *connection.Conn, database, schema string, d db.CatalogDepth) {
	if err := cn.Project.Writable(); err != nil {
		a.ShowError("Could not save "+project.File, err.Error())
		return
	}
	cn.Config.SetCatalog(database, schema, d)
	a.saveProject(cn.Project)
	key := connection.SchemaKey{Database: database, Schema: schema}
	delete(cn.Objects, key)
	delete(cn.Items, key)
	maps.DeleteFunc(cn.Columns, func(k connection.ObjectKey, _ []db.Column) bool { return k.Database == database && k.Schema == schema })
	connection.LoadObjects(a, cn, database, schema)
}
