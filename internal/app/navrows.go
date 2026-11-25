package app

import (
	"cmp"
	"strings"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/redact"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// fileConnectionShrink is how much sooner than a query file's name the
// name of its connection gives way where the row is narrow: the file's
// name is what the row is chosen by.
const fileConnectionShrink = 1000

func (a *App) navRow(c *ui.Context, n navNode) {
	a.navStart(c, n)
	if n.projectDir != "" {
		a.projectRow(c, n)
		return
	}
	cn := a.connByID(n.conn)
	if cn == nil {
		return
	}
	row := ui.Row(c).Gap(6).Grow(1).Shrink(1).MinWidth(0).Height(navRowHeight).AlignItems(ui.Center).Padding(0, navEdge, 0, 0)
	quiet := a.quietHeading(c, n)
	if quiet != ui.Transparent {
		row.Background(quiet)
	}
	row.Children(func() {
		switch n.kind {
		case nodeConn:
			a.navConnRow(c, row, cn, n)
		case nodeDatabase:
			a.navDatabaseRow(c, row, cn, n)
		case nodeSchema:
			a.navSchemaRow(c, n)
		case nodeFolder:
			a.navGroupRow(c, cn, n, quiet)
		case nodeObject:
			a.navObjectRow(c, n)
		case nodeColumn:
			a.navColumnRow(c, cn, n)
		case nodeItem:
			a.navItemRow(c, n)
		case nodeInfo:
			a.navNoteRow(c, row, n)
		}
	})
	row.ContextMenu(func(m *ui.Menu) { a.navMenu(m, n) })
	if n.kind == nodeObject {
		a.objectCard(c, row, cn, n)
	}
}

// navConnRow is a connection's row: its badge, its name, heavier as a
// heading's, its one schema, and how it stands.
func (a *App) navConnRow(c *ui.Context, row ui.Element, cn *connection.Conn, n navNode) {
	t := c.Theme()
	muted := widgets.PaletteOf(c).Muted
	// One mark before the name: the engine's badge, gray until
	// connected as the explorers of DataGrip and VS Code have it,
	// with the connection's dot on its corner.
	connected := cn.Status == connection.StatusConnected
	tip := cn.Config.Engine.Label() + ", not connected"
	if connected {
		tip = cn.Config.Engine.Label() + " " + cn.Version
		if s := tlsStateOf(cn.Store); s != "" {
			tip += ", " + s
		}
	}
	widgets.ConnBadge(c, cn, widgets.PaletteOf(c).Sidebar).Tooltip(tip)
	// A heading too: its databases start where it does, and its
	// badge and heavier name set it apart from them.
	name := a.navNameText(c, cn.Config.Name).FontWeight(600).SingleLine().Shrink(1)
	if !connected {
		name.TextColor(muted)
	}
	if sn, ok := schemaOf(cn, n); ok {
		ui.Text(c, sn.schema).FontSize(widgets.TextSmall).TextColor(muted).SingleLine().Grow(1).Shrink(1).Tooltip("Its schema")
	} else {
		name.Grow(1)
	}
	a.schemaCountButton(c, cn, "")
	switch cn.Status {
	case connection.StatusFailed:
		ui.Icon(c, widgets.IconAlert).TextColor(t.Danger).FontSize(widgets.IconSmall).Tooltip(redact.Secrets(cn.Err))
		if row.Clicked() {
			a.showConnError(cn)
		}
	}
	if cn.Config.ReadOnly {
		ui.Icon(c, widgets.IconLock).TextColor(muted).FontSize(widgets.IconSmall).Tooltip("Read-only")
	}
}

// navDatabaseRow is a database's row, with the button that opens an
// editor on it.
func (a *App) navDatabaseRow(c *ui.Context, row ui.Element, cn *connection.Conn, n navNode) {
	muted := widgets.PaletteOf(c).Muted
	name := cmp.Or(n.database, cn.Config.Database)
	ui.Icon(c, widgets.IconDatabase).TextColor(muted).FontSize(widgets.IconMedium)
	a.navNameText(c, name).SingleLine().Shrink(1)
	if sn, ok := schemaOf(cn, n); ok {
		ui.Text(c, sn.schema).FontSize(widgets.TextSmall).TextColor(muted).SingleLine().Shrink(1).Tooltip("Its schema")
	}
	ui.Spacer(c)
	// An editor on it, for the pointer: its menu has the same.
	at, ok := a.nav.paneOf(n).chosen()
	if rowAdd(c, row, ok && at == n, "New SQL Editor on "+name) {
		a.NewQueryTab(cn, n.database, "")
	}
	a.schemaCountButton(c, cn, n.database)
}

// navSchemaRow is a schema's row.
func (a *App) navSchemaRow(c *ui.Context, n navNode) {
	muted := widgets.PaletteOf(c).Muted
	ui.Icon(c, widgets.IconSchema).TextColor(muted).FontSize(widgets.IconMedium)
	a.navNameText(c, n.schema).SingleLine()
}

// navGroupRow is the row of a group of a schema's objects, as Tables: a
// heading with their count.
func (a *App) navGroupRow(c *ui.Context, cn *connection.Conn, n navNode, quiet ui.Color) {
	t := c.Theme()
	muted := widgets.PaletteOf(c).Muted
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
	// A heading over its rows, as the files' pane has them: its
	// small capitals stand in for the level it does not indent.
	// Chosen, it brightens, as theirs do (quietHeading).
	name := navHeadingText(c, label, false)
	if quiet != ui.Transparent {
		name.TextColor(t.Text)
	}
	ui.Text(c, a.navCount(n, count)).FontSize(widgets.TextMeta).FontFeatures("tnum").TextColor(muted)
}

// navObjectRow is a table's or a view's row, with its rows' count.
func (a *App) navObjectRow(c *ui.Context, n navNode) {
	muted := widgets.PaletteOf(c).Muted
	_, obj, _ := a.object(n)
	ic := widgets.IconTable
	if obj.Kind == db.KindView || obj.Kind == db.KindMaterializedView {
		ic = widgets.IconView
	}
	ui.Icon(c, ic).TextColor(muted).FontSize(widgets.IconMedium)
	a.navNameText(c, n.name).SingleLine().Grow(1).Shrink(1)
	if obj.Rows >= 0 && obj.Kind == db.KindTable {
		ui.Text(c, widgets.HumanCount(obj.Rows)).FontSize(widgets.TextMeta).TextColor(muted)
	}
}

// navColumnRow is a column's row: its name, its key's mark and its type.
func (a *App) navColumnRow(c *ui.Context, cn *connection.Conn, n navNode) {
	t := c.Theme()
	muted := widgets.PaletteOf(c).Muted
	table, col, _ := strings.Cut(n.name, "\x00")
	var column db.Column
	for _, cc := range cn.Columns[connection.ObjectKey{Database: n.database, Schema: n.schema, Name: table}] {
		if cc.Name == col {
			column = cc
		}
	}
	// The names read down one line and the types down another, at
	// the row's right edge in the grid's own font; a key's mark is
	// before its type, so that it takes no room from the names.
	ui.Text(c, col).SingleLine().Shrink(1)
	ui.Spacer(c)
	if column.PrimaryKey {
		ui.Icon(c, widgets.IconKey).TextColor(t.Warning).FontSize(widgets.IconSmall).Tooltip("Primary key")
	}
	// The standard's long names by their short ones, which leave
	// the column's name its room; the whole type on hover.
	ui.Text(c, db.ShortType(column.Type)).Font(widgets.MonoFont).FontSize(widgets.TextMeta).TextColor(muted).SingleLine().Shrink(1).Tooltip(column.Type)
}

// navItemRow is the row of a routine, a partition or another item.
func (a *App) navItemRow(c *ui.Context, n navNode) {
	muted := widgets.PaletteOf(c).Muted
	_, it, _ := a.item(n)
	ui.Icon(c, itemIcon(it.Kind)).TextColor(muted).FontSize(widgets.IconSmall)
	label := it.Label()
	if it.Kind == db.ItemPartition {
		label = it.Name
	}
	a.navNameText(c, label).SingleLine().Grow(1).Shrink(1).Tooltip(label)
	if it.Kind != db.ItemFunction && it.Kind != db.ItemProcedure && it.Detail != "" {
		ui.Text(c, it.Detail).FontSize(widgets.TextMeta).TextColor(muted).SingleLine().Shrink(1)
	}
}

// navNoteRow is a note's row: its text, or the error it stands for.
func (a *App) navNoteRow(c *ui.Context, row ui.Element, n navNode) {
	muted := widgets.PaletteOf(c).Muted
	if e, ok := a.nav.errors[n]; ok {
		a.errorNoteText(c, row, n, e)
		return
	}
	ui.Text(c, n.name).FontSize(widgets.TextSmall).TextColor(muted).Italic().SingleLine()
}

// objectCard gives a table's or a view's row its card, at the sidebar's
// right, level with the row.
func (a *App) objectCard(c *ui.Context, row ui.Element, cn *connection.Conn, n navNode) {
	key := objectCardKey(n)
	// Alt+click shows the card at once; the click goes on to the row.
	if row.PressedInside() && c.Modifiers()&ui.Alt != 0 {
		a.cardWanted = key
	}
	if a.cardWanted == key {
		a.cardWanted = ""
		widgets.ShowCard(c, key)
	}
	widgets.HoverCard(c, row, widgets.CardSidebar, key, widgets.CardOptions{Place: widgets.PlaceRight, Edge: a.layout.SidebarWidth}, func(c *ui.Context, card *widgets.Card) {
		_, obj, ok := a.object(n)
		if !ok {
			obj = db.Object{Name: n.name, Kind: db.KindTable, Rows: -1, Bytes: -1}
		}
		if obj.Schema == "" {
			obj.Schema = n.schema
		}
		dataview.TableCard(c, card, a, cn, n.database, obj, dataview.TableCardOptions{NewQuery: true})
	})
}

// objectCardKey is the key of a table's or a view's card in the sidebar.
func objectCardKey(n navNode) string {
	return "sidebar:" + n.conn + "\x00" + n.database + "\x00" + n.schema + "\x00" + n.name
}

// projectRow shows a project, its Queries folder, a query file, or a note.
func (a *App) projectRow(c *ui.Context, n navNode) {
	p := a.projectByDir(n.projectDir)
	if p == nil {
		return
	}
	muted := widgets.PaletteOf(c).Muted
	row := ui.Row(c).Gap(6).Grow(1).Shrink(1).MinWidth(0).Height(navRowHeight).AlignItems(ui.Center).Padding(0, navEdge, 0, 0)
	if quiet := a.quietHeading(c, n); quiet != ui.Transparent {
		row.Background(quiet)
	}
	row.Children(func() {
		switch n.kind {
		case nodeProject:
			a.projectName(c, p)
		case nodeConnections, nodeQueries, nodeDashboards, nodeModels, nodeBookmarks:
			_, label, count := a.section(p, n.kind)
			at, ok := a.nav.paneOf(n).chosen()
			chosen := ok && at == n
			// A heading over its items: small capitals, quieter than they
			// are, and no icon, which the items' own would only repeat.
			// Chosen, it brightens: the list's own tint, made for an item,
			// would shout on a heading (quietHeading).
			name := navHeadingText(c, label, true)
			if chosen {
				name.TextColor(c.Theme().Text)
			}
			var newLabel string
			var run func()
			if p.Err == "" {
				newLabel, run = a.sectionNew(p, n.kind)
			}
			if run == nil {
				ui.Box(c).Size(widgets.IconMedium+2, widgets.IconMedium+2).Shrink(0) // its room, kept
			} else if rowAdd(c, row, chosen, newLabel) {
				run()
			}
			ui.Text(c, a.navCount(n, count)).FontSize(widgets.TextMeta).FontFeatures("tnum").TextColor(muted).MinWidth(14).TextAlign(ui.End)
		case nodeConnFolder:
			a.connFolderRow(c, p, n)
		case nodeQueryFile:
			// The connection the file's header names: its color before
			// the file, as a connection's row has its own, and its name
			// after it, which gives way first where the row is narrow.
			// An ID no connection of the project has shows as it is, in
			// the warning color, and none shows nothing.
			id := p.FileConnections[n.name].ID
			var runsOn *connection.Conn
			if id != "" {
				runsOn = a.connByID(p.Prefix + id)
			}
			a.fileMark(c, n, func() {
				dot := ui.Box(c).Size(6, 6).Radius(3).Shrink(0)
				if runsOn != nil {
					dot.Background(widgets.EnvColor(&runsOn.Config))
				}
			})
			// Its name without ".sql", which every file of the list ends
			// in, and no icon, which every row would share: the room goes
			// to the name and the connection.
			// Named whole to a screen reader, and to what looks it up.
			a.navNameText(c, a.fileName(p, queriesSection, n.name)).Label(n.name).SingleLine().Shrink(1).Tooltip(a.filePath(p, n))
			ui.Spacer(c)
			switch {
			case id == "":
			case runsOn == nil:
				ui.Text(c, id).SingleLine().Shrink(fileConnectionShrink).FontSize(widgets.TextMeta).TextColor(c.Theme().Warning).Tooltip("No connection of " + p.Name + " has this ID")
			default:
				ui.Text(c, runsOn.Config.Name).SingleLine().Shrink(fileConnectionShrink).FontSize(widgets.TextMeta).TextColor(muted).Tooltip("Runs on " + runsOn.Config.Name)
			}
		case nodeDashboard, nodeModel:
			s, _ := sectionOf(n)
			a.fileMark(c, n, nil)
			a.navNameText(c, a.fileName(p, s, n.name)).SingleLine().Shrink(1).Tooltip(a.filePath(p, n))
		case nodeFileFolder:
			a.fileFolderRow(c, p, n)
		case nodeBookmark:
			a.bookmarkRow(c, p, n)
		case nodeInfo:
			if e, ok := a.nav.errors[n]; ok {
				a.errorNoteText(c, row, n, e)
				break
			}
			ui.Text(c, n.name).FontSize(widgets.TextSmall).TextColor(muted).Italic().SingleLine()
		}
	})
	// A folder takes what is dragged onto it; a project's row in the
	// database tree, or a section's heading, for its top level.
	switch s, ok := sectionOf(n); {
	case n.kind == nodeConnFolder || n.kind == nodeProject && !n.files:
		a.folderDropTarget(c, row, p, n)
	case ok && (n.kind == nodeFileFolder || n.kind == s.heading):
		a.fileDropTarget(c, row, p, n)
	}
	row.ContextMenu(func(m *ui.Menu) { a.navMenu(m, n) })
}

// fileMark draws what a file's row has before its name, mark, which may
// be nil: among folders, in the room of their icons, for the names to
// line up.
func (a *App) fileMark(c *ui.Context, n navNode, mark func()) {
	if !a.besideFolders(n) {
		if mark != nil {
			mark()
		}
		return
	}
	room := ui.Row(c).Width(widgets.IconMedium).Shrink(0).Center()
	if mark != nil {
		room.Children(mark)
	}
}

// section is how a project's section shows: its icon, name and count.
func (a *App) section(p *project.Project, kind nodeKind) (*ui.SVG, string, int) {
	switch kind {
	case nodeConnections:
		return widgets.IconPlug, "Connections", len(a.projectConns(p))
	case nodeDashboards:
		return widgets.IconLayers, "Dashboards", len(a.sectionFiles(p, dashboardsSection).Files)
	case nodeModels:
		return widgets.IconSchema, "Data Models", len(a.sectionFiles(p, modelsSection).Files)
	case nodeBookmarks:
		return widgets.IconPin, "Bookmarks", len(a.bookmarks(p).list)
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
		if a.queryConn(p) == nil {
			return "", nil
		}
		return "New SQL Editor", a.newEditor
	case nodeDashboards, nodeModels:
		s, _ := sectionOf(navNode{kind: kind})
		return a.newFileIn(p, s, "")
	}
	return "", nil
}

// queryConn is the connection a new editor of a project opens on: the
// one worked with, when it is the project's and runs SQL, else its first
// that does; nil for none.
func (a *App) queryConn(p *project.Project) *connection.Conn {
	if cn := a.activeConn(); cn != nil && cn.Project == p && cn.Config.Engine.IsSQL() {
		return cn
	}
	return a.firstSQLConn(p)
}
