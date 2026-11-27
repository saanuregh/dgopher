package app

import (
	"context"
	"errors"
	"maps"
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

	"github.com/egoist/mygo/ui"
)

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
		a.navConnMenu(m, cn, n)
	case nodeObject:
		a.navObjectMenu(m, n)
	case nodeColumn:
		a.navColumnMenu(m, n)
	case nodeItem:
		a.navItemMenu(m, n)
	case nodeSchema, nodeDatabase, nodeFolder:
		a.navSchemaMenu(m, cn, n)
	case nodeDocDatabase, nodeDocFolder, nodeDocCollection, nodeDocIndex:
		a.navDocMenu(m, cn, n)
	}
}

// navConnMenu is the menu of a connection's row.
func (a *App) navConnMenu(m *ui.Menu, cn *connection.Conn, n navNode) {
	if cn.Status == connection.StatusConnected {
		if m.Item("Disconnect").Chosen() {
			a.askDisconnect(cn)
		}
		if keymap.Item(m.Item("Refresh"), keymap.NavigatorRefresh).Chosen() {
			a.refresh(cn)
		}
		if len(cn.Schemas[""]) > 1 && m.Item("Schemas…").Chosen() {
			a.openSchemaPicker(cn, "")
		}
	} else if m.Item("Connect").Chosen() {
		a.activateConnection(cn, n)
	}
	if _, ok := a.connectStarted(cn); ok && m.Item("Cancel Connecting").Chosen() {
		a.CancelConnect(cn)
	}
	switch {
	case cn.Config.Engine.IsKeyValue():
		if m.Item("Open Key Browser").Chosen() {
			a.openRedis(cn)
		}
	case cn.Config.Engine.IsDocument():
		if keymap.Item(m.Item("Open Console"), keymap.NewEditor).Chosen() {
			a.openMongoConsole(cn, "")
		}
	default:
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
	if activitySupported(cn.Config.Engine) && m.Item("Server Activity").Chosen() {
		a.openActivity(cn)
	}
	if usersSupported(cn.Config.Engine) && m.Item("Users and Privileges").Chosen() {
		a.openUsers(cn)
	}
	if cn.Config.Engine.IsSQL() && m.Item("Catalog Queries").Disabled(cn.DB() == nil).Chosen() {
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
	a.connFolderItems(m, cn)
	m.Separator()
	if m.Item("Delete Connection…").Chosen() {
		a.askDeleteConn(cn)
	}
}

// navObjectMenu is the menu of a table's or a view's row.
func (a *App) navObjectMenu(m *ui.Menu, n navNode) {
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
	if m.Item("Show Card").Chosen() {
		a.cardWanted = objectCardKey(n)
	}
	if m.Item("View DDL").Chosen() {
		a.OpenTable(cn, n.database, obj, dataview.PageDDL)
	}
	a.bookmarkItem(m, n)
	m.Separator()
	quoted := db.QualifiedName(cn.DB().Dialect, obj.Schema, obj.Name)
	if m.Item("New SQL: SELECT").Chosen() {
		a.NewQueryTab(cn, n.database, db.LimitSQL(cn.Config.Engine, "SELECT *\nFROM "+quoted, "\n", 100)+";\n")
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
			a.NewQueryTab(cn, n.database, partitionTemplate(cn.DB().Dialect, obj))
		}
		var cmds []db.MaintenanceCommand
		if r, ok := db.SQL(cn.Config.Engine); ok {
			cmds = r.MaintenanceCommands(quoted)
		}
		if len(cmds) > 0 {
			m.Submenu("Maintenance", func(m *ui.Menu) {
				for _, mc := range cmds {
					if m.Item(mc.Label).Disabled(cn.Config.ReadOnly).Chosen() {
						a.runInEditor(cn, n.database, mc.SQL)
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
}

// navColumnMenu is the menu of a column's row.
func (a *App) navColumnMenu(m *ui.Menu, n navNode) {
	table, column, _ := strings.Cut(n.name, "\x00")
	cn, obj, ok := a.object(navNode{conn: n.conn, database: n.database, schema: n.schema, name: table})
	if !ok {
		return
	}
	if m.Item("Copy Name").Chosen() {
		a.WriteClipboard(cn.DB().Dialect.Quote(column))
	}
	if obj.Kind == db.KindTable && m.Item("Rename…").Disabled(cn.Config.ReadOnly).Chosen() {
		a.askRenameInDatabase(cn, n.database, column, func(name string) (string, error) {
			return db.RenameColumnSQL(cn.DB().Dialect, obj.Schema, obj.Name, column, name), nil
		})
	}
}

// navItemMenu is the menu of a routine's, a partition's or another item's row.
func (a *App) navItemMenu(m *ui.Menu, n navNode) {
	cn, it, ok := a.item(n)
	if !ok {
		return
	}
	if it.Kind == db.ItemPartition {
		if m.Item("Open Data").Chosen() {
			a.openItem(cn, n.database, it)
		}
		if m.Item("Detach…").Disabled(cn.Config.ReadOnly).Chosen() {
			d := cn.DB().Dialect
			a.NewQueryTab(cn, n.database, "ALTER TABLE "+db.QualifiedName(d, it.Schema, it.Table)+" DETACH PARTITION "+db.QualifiedName(d, it.Schema, it.Name)+";\n")
		}
	}
	if m.Item("Open Definition").Chosen() {
		dataview.OpenItemDefinition(a, cn, n.database, it)
	}
	if m.Item("Copy Name").Chosen() {
		a.WriteClipboard(db.QualifiedName(cn.DB().Dialect, it.Schema, it.Name))
	}
	a.bookmarkItem(m, n)
}

// navSchemaMenu is the menu of a database's, a schema's or a group's row: what
// is done to a schema, less for a database, which holds several.
func (a *App) navSchemaMenu(m *ui.Menu, cn *connection.Conn, n navNode) {
	if n.kind == nodeDatabase && len(cn.Schemas[n.database]) > 1 && m.Item("Schemas…").Chosen() {
		a.openSchemaPicker(cn, n.database)
	}
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
		if r, ok := db.SQL(cn.Config.Engine); ok && n.kind == nodeSchema {
			text = r.EditorSchemaPreamble(n.schema)
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
				a.ShowReport(widgets.ErrorReportOf("Could not list the tables of "+schema, err))
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
				a.ShowReport(widgets.ErrorReportOf("Rename failed", err))
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

// runInEditor opens an editor on a statement and runs it there, through
// the safety policy, with its result and audit as any run's.
func (a *App) runInEditor(cn *connection.Conn, database, sql string) {
	a.newQueryFile(cn, database, sql+";\n", func(q *query.Tab) { q.Run(query.RunScript) })
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
		a.ShowReport(widgets.ErrorReportOf("Could not save "+project.File, err))
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
