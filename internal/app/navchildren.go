package app

import (
	"fmt"
	"slices"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// projectChildren lists a project's sections, its connections, query
// files, dashboards and data models, and what each holds.
func (a *App) projectChildren(n navNode) []navNode {
	p := a.projectByDir(n.projectDir)
	if p == nil {
		return nil
	}
	if p.Err != "" {
		if inFilesPane(n) {
			return nil // said once, in the database tree
		}
		return a.errorNote(navNode{projectDir: p.Dir, name: widgets.FirstLine(p.Err)}, navError{title: "Could not read the project", full: p.Err})
	}
	switch n.kind {
	case nodeProject:
		if !n.files {
			// Among several projects, each has the heading of its
			// connections under its row, as it has those of its files in
			// the other pane: the same count and button as with one.
			conns := navNode{kind: nodeConnections, projectDir: p.Dir}
			a.openHeading(conns)
			return []navNode{conns}
		}
		queries := navNode{kind: nodeQueries, projectDir: p.Dir}
		a.openHeading(queries) // what the pane is opened for, first
		var out []navNode
		// Bookmarks have their heading over the files, once there is one.
		if len(a.bookmarks(p).list) > 0 {
			bookmarks := navNode{kind: nodeBookmarks, projectDir: p.Dir}
			a.openHeading(bookmarks)
			out = append(out, bookmarks)
		}
		// Dashboards and data models have a heading once there is one, or
		// a folder for them: a new project starts with Queries alone over
		// its connections. The menu of any heading, and the sidebar's +,
		// make the first.
		out = append(out, queries)
		for _, s := range []fileSection{dashboardsSection, modelsSection} {
			if l := a.sectionFiles(p, s); len(l.Files)+len(l.Folders) > 0 {
				out = append(out, s.folderNode(p, ""))
			}
		}
		return out
	case nodeBookmarks:
		return a.bookmarkChildren(p)
	case nodeConnections:
		return a.connFolderChildren(p, "")
	case nodeConnFolder:
		return a.connFolderChildren(p, n.name)
	case nodeFileFolder:
		s, _ := sectionOf(n)
		return a.fileFolderChildren(p, s, n.name)
	case nodeQueries:
		if a.nav.isOpen(n) {
			a.ScanQueries(p, false)
		}
		out := a.fileFolderChildren(p, queriesSection, "")
		if p.ScanErr != "" {
			out = append(out, a.errorNote(navNode{projectDir: p.Dir, name: widgets.FirstLine(p.ScanErr)}, navError{title: "Could not list the .sql files", full: p.ScanErr})...)
		}
		if out == nil {
			return []navNode{{kind: nodeInfo, projectDir: p.Dir, name: "No .sql files yet"}}
		}
		return out
	case nodeDashboards, nodeModels:
		s, _ := sectionOf(n)
		if out := a.fileFolderChildren(p, s, ""); out != nil {
			return out
		}
		return []navNode{{kind: nodeInfo, projectDir: p.Dir, name: "No " + s.plural + " yet"}}
	}
	return nil
}

// info is a note under a node of a connection, as that it is loading.
func info(under navNode, text string) []navNode {
	return []navNode{{kind: nodeInfo, conn: under.conn, name: text, under: noteUnder(under)}}
}

// noteUnder names the node a note is under: a note is a row as any
// other, and two rows of one name would be one to the tree.
func noteUnder(n navNode) string {
	return fmt.Sprintf("%d\x00%d\x00%s\x00%s\x00%s\x00%s", n.kind, n.section, n.database, n.schema, n.folder, n.name)
}

// schemaChildren are the folders of a schema, which start to be read
// when open: that of the schema's node, or of the node it shows in.
func (a *App) schemaChildren(cn *connection.Conn, n navNode, open bool) []navNode {
	key := connection.SchemaKey{Database: n.database, Schema: n.schema}
	objs, ok := cn.Objects[key]
	if !ok {
		if e := cn.LoadErr[key]; e != "" {
			return a.catalogError(cn, n, "the tables", "Failed: "+widgets.FirstLine(e), e)
		}
		if open {
			connection.LoadObjects(a, cn, n.database, n.schema)
		}
		return info(n, "Loading…")
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
		out = append(out, a.catalogError(cn, n, "the routines and other objects", "Could not read the routines and other objects: "+widgets.FirstLine(e), e)...)
	}
	if e := cn.SchemaColumnsError(n.database, n.schema); e != "" {
		out = append(out, a.catalogError(cn, n, "the columns at once", "Could not read the columns at once, so each table's are read as needed: "+widgets.FirstLine(e), e)...)
	}
	if out == nil {
		return info(n, "No tables")
	}
	return out
}

// navChildren returns a node's children, starting to read them when the
// node is open and they are not known yet.
func (a *App) navChildren(n navNode) []navNode {
	switch n.kind {
	case nodeProject, nodeConnections, nodeConnFolder, nodeQueries, nodeDashboards, nodeModels, nodeFileFolder, nodeBookmarks:
		return a.projectChildren(n)
	case nodeQueryFile, nodeDashboard, nodeModel, nodeBookmark, nodeInfo:
		return nil
	}
	cn := a.connByID(n.conn)
	if cn == nil {
		return nil
	}
	open := a.nav.isOpen(n)
	switch n.kind {
	case nodeConn:
		if !cn.Config.Engine.IsSQL() && !cn.Config.Engine.IsDocument() {
			return nil
		}
		opened := open && !a.nav.openConns[n.conn]
		if open {
			a.nav.openConnsNow[n.conn] = true
		}
		switch cn.Status {
		case connection.StatusConnecting:
			return info(n, "Connecting…")
		case connection.StatusFailed:
			return nil
		case connection.StatusIdle:
			switch {
			case opened:
				a.Connect(cn, nil)
				return info(n, "Connecting…")
			case open:
				return info(n, "Not connected")
			}
			return info(n, "")
		}
		if cn.Config.Engine.IsDocument() {
			return a.docDatabaseNodes(cn, n, open)
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
	case nodeDocDatabase, nodeDocFolder, nodeDocCollection:
		return a.docChildren(cn, n, open)
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
			return info(n, "None")
		}
		return out
	case nodeObject:
		key := connection.ObjectKey{Database: n.database, Schema: n.schema, Name: n.name}
		cols, ok := cn.Columns[key]
		if !ok {
			if open {
				connection.LoadColumns(a, cn, n.database, n.schema, n.name, nil)
			}
			return info(n, "Loading…")
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
	case db.ItemFunction, db.ItemProcedure, db.ItemPackage:
		return widgets.IconCode
	case db.ItemTrigger:
		return widgets.IconPlay
	case db.ItemSequence, db.ItemProjection:
		return widgets.IconLayers
	case db.ItemType:
		return widgets.IconSchema
	case db.ItemExtension:
		return widgets.IconPlug
	case db.ItemEvent, db.ItemJob:
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
	if _, ok := cn.Schemas[database]; !ok {
		of := navNode{kind: nodeDatabase, conn: cn.Config.ID, database: database}
		if e := cn.SchemasError(database); e != "" {
			return a.catalogError(cn, of, "the schemas", "Failed: "+widgets.FirstLine(e), e)
		}
		if open {
			a.loadSchemas(cn, database)
		}
		return info(of, "Loading…")
	}
	if s, ok := soleSchema(cn, database); ok {
		return a.schemaChildren(cn, navNode{kind: nodeSchema, conn: cn.Config.ID, database: database, schema: s}, open)
	}
	// Those chosen to show, all of them until some are.
	shown := a.shownSchemas(cn, database)
	out := make([]navNode, len(shown))
	for i, s := range shown {
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
