package app

import (
	"sort"
	"strings"
	"unicode"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// paletteItem is a command or a place of the command palette.
type paletteItem struct {
	title  string
	detail string
	// key is the keymap command whose keys the row shows, "" for none.
	key   string
	group string
	icon  *ui.SVG
	run   func()
	score int
}

// paletteRows is about the rows the palette shows at once, which Page Up
// and Page Down move by.
const paletteRows = 10

type paletteState struct {
	open   bool
	query  string
	items  []paletteItem
	shown  []paletteItem
	shownQ string
	index  int
	row    int
	list   ui.ListState
	tables bool // opened with ⌘P: tables first
}

func (a *App) openPalette(tables bool) {
	p := &paletteState{open: true, tables: tables, shownQ: "\x00"}
	p.items = a.paletteItems(tables)
	a.palette = p
}

// paletteItems lists the commands and the tables of the connections.
func (a *App) paletteItems(tablesFirst bool) []paletteItem {
	var cmds, conns, tables []paletteItem
	active := a.activeConn()
	// The tab's own commands first: they are the ones at hand.
	if t, ok := a.ActiveTab().(widgets.Commander); ok {
		for _, cmd := range t.Commands() {
			cmds = append(cmds, paletteItem{title: cmd.Title, detail: cmd.Detail, key: cmd.Key, group: "Command", icon: cmd.Icon, run: cmd.Run})
		}
	}
	cmds = append(cmds,
		paletteItem{title: "New Connection…", key: keymap.NewConnection, group: "Command", icon: widgets.IconPlus, run: func() { a.openConnForm(nil) }},
		paletteItem{title: "Query History", key: keymap.History, group: "Command", icon: widgets.IconHistory, run: func() { a.openHistory() }},
		paletteItem{title: "Audit Log", detail: "who did what, verifiable", key: keymap.AuditLog, group: "Command", icon: widgets.IconShield, run: a.openAudit},
		paletteItem{title: "Settings…", key: keymap.Settings, group: "Command", icon: widgets.IconSettings, run: a.openSettings},
		paletteItem{title: "Toggle Sidebar", key: keymap.ToggleSidebar, group: "Command", icon: widgets.IconColumns, run: func() { a.sidebarHidden = !a.sidebarHidden }},
		paletteItem{title: "Keyboard Shortcuts", key: keymap.ShortcutsList, group: "Command", icon: widgets.IconCode, run: func() { a.shortcutsOpen = true }},
		paletteItem{title: "New Project…", group: "Command", icon: widgets.IconFolder, run: func() { a.openNewProject(nil) }},
		paletteItem{title: "Add Existing Folder…", detail: "list a project folder", key: keymap.AddFolder, group: "Command", icon: widgets.IconFolder, run: a.addExistingProject},
	)
	if active != nil {
		cmds = append(cmds, paletteItem{title: "New SQL Editor", detail: active.Config.Name, key: keymap.NewEditor, group: "Command", icon: widgets.IconCode, run: func() { a.NewQueryTab(active, "", "") }})
		if active.Config.Engine.IsSQL() {
			cmds = append(cmds, paletteItem{title: "Open SQL Script…", detail: active.Config.Name, key: keymap.OpenScript, group: "Command", icon: widgets.IconFile,
				run: func() { a.openSQLFile(active) }})
			cmds = append(cmds, paletteItem{title: "Run SQL File…", detail: active.Config.Name + ", without opening it", group: "Command", icon: widgets.IconFile,
				run: func() { a.openSQLFileRun(active, "") }})
			cmds = append(cmds, paletteItem{title: "Search Objects…", detail: active.Config.Name + ", by name or definition", group: "Command", icon: widgets.IconSearch,
				run: func() { a.openSearch(active, "") }})
		}
		if active.Status == connection.StatusConnected {
			if !active.Config.Engine.IsFile() {
				cmds = append(cmds, paletteItem{title: "Server Activity", detail: active.Config.Name, group: "Command", icon: widgets.IconClock, run: func() { a.openActivity(active) }})
			}
			if db.UsersSupported(active.Config.Engine) {
				cmds = append(cmds, paletteItem{title: "Users and Privileges", detail: active.Config.Name, group: "Command", icon: widgets.IconUsers, run: func() { a.openUsers(active) }})
			}
			if active.Config.Engine.IsSQL() {
				cmds = append(cmds, paletteItem{title: "Catalog Queries", detail: active.Config.Name + ", what the app reads on its own", group: "Command", icon: widgets.IconHistory,
					run: func() { a.openCatalogQueries(active) }})
			}
			cmds = append(cmds, paletteItem{title: "Refresh Schema", detail: active.Config.Name, group: "Command", icon: widgets.IconRefresh, run: func() { a.refresh(active) }},
				paletteItem{title: "Disconnect", detail: active.Config.Name, group: "Command", icon: widgets.IconUnplug, run: func() {
					a.requestDisconnect("Disconnect "+active.Config.Name+"?", []*connection.Conn{active}, func() { a.disconnect(active) })
				}})
		}
	}
	if tab := a.ActiveTab(); tab != nil && len(a.tabs) > 1 {
		w := a.window
		cmds = append(cmds, paletteItem{title: "Close Other Tabs", group: "Command", icon: widgets.IconX, run: func() { a.closeOthers(w, tab) }})
	}
	if tab := a.ActiveTab(); tab != nil && (a.window == a.main || len(a.tabs) > 1) {
		cmds = append(cmds, paletteItem{title: "Move Tab to New Window", group: "Command", icon: widgets.IconLayers, run: func() { a.Post(func() { a.moveToWindow(tab, nil) }) }})
	}
	if len(a.tabs) > 0 {
		cmds = append(cmds, paletteItem{title: "Close Tab", key: keymap.CloseTab, group: "Command", icon: widgets.IconX, run: func() { a.closeTab(a.active) }})
	}
	for _, cn := range a.conns {
		cn := cn
		detail := cn.Config.Engine.Label() + " · " + cn.Config.Env.Label()
		title := "Open " + cn.Config.Name
		run := func() { a.NewQueryTab(cn, "", "") }
		if cn.Config.Engine == db.Redis {
			run = func() { a.openRedis(cn) }
		}
		conns = append(conns, paletteItem{title: title, detail: detail, group: "Connection", icon: widgets.IconDatabase, run: run})
		for key, objs := range cn.Objects {
			for _, o := range objs {
				o, key := o, key
				detail := cn.Config.Name + " · " + o.Schema
				ic := widgets.IconTable
				if o.Kind != db.KindTable {
					ic = widgets.IconView
				}
				tables = append(tables, paletteItem{title: o.Name, detail: detail, group: "Table", icon: ic, run: func() { a.OpenTable(cn, key.Database, o, dataview.PageData) }})
			}
		}
		for key, items := range cn.Items {
			for _, it := range items {
				if it.Kind != db.ItemFunction && it.Kind != db.ItemProcedure {
					continue
				}
				group := "Function"
				if it.Kind == db.ItemProcedure {
					group = "Procedure"
				}
				tables = append(tables, paletteItem{title: it.Label(), detail: cn.Config.Name + " · " + it.Schema, group: group, icon: itemIcon(it.Kind),
					run: func() { dataview.OpenItemDefinition(a, cn, key.Database, it) }})
			}
		}
	}
	sort.SliceStable(tables, func(i, j int) bool { return tables[i].title < tables[j].title })
	snippets := a.snippetItems()
	if q, ok := a.ActiveTab().(*query.Tab); ok {
		cmds = append(cmds, paletteItem{title: "Save as Snippet…", group: "Command", icon: widgets.IconSave, run: func() { a.AskSnippet(q) }},
			paletteItem{title: "Add to Dashboard…", detail: "the statement at the caret, as a panel", group: "Command", icon: widgets.IconLayers, run: func() { a.openAddToDashboard(q) }})
	}
	cmds = append(cmds, paletteItem{title: "Take the Tour", detail: "a minute on how DGopher works", group: "Command", icon: widgets.IconWand, run: a.startTour})
	cmds = append(cmds, paletteItem{title: "Customize Keyboard Shortcuts…", detail: "change the keys of the commands", group: "Command", icon: widgets.IconSettings,
		run: func() { a.keys = &keysEditor{open: true} }})
	for _, p := range a.projects {
		names, paths := a.dashboardNames(p)
		for i, name := range names {
			cmds = append(cmds, paletteItem{title: name, detail: p.Name, group: "Dashboard", icon: widgets.IconLayers, run: func() { a.openDashboard(p, paths[i]) }})
		}
		names, paths = a.modelNames(p)
		for i, name := range names {
			cmds = append(cmds, paletteItem{title: name, detail: p.Name, group: "Data Model", icon: widgets.IconSchema, run: func() { a.openModel(p, paths[i]) }})
		}
	}
	if tablesFirst {
		return append(append(append(tables, conns...), snippets...), cmds...)
	}
	return append(append(append(cmds, snippets...), conns...), tables...)
}

// fuzzyScore scores how well a query matches a text, -1 for not at all:
// the letters of the query must come in order; matches at the start of
// words and runs of letters score higher.
func fuzzyScore(query, text string) int {
	if query == "" {
		return 0
	}
	q := []rune(strings.ToLower(query))
	t := []rune(strings.ToLower(text))
	score, qi, run := 0, 0, 0
	for i := 0; i < len(t) && qi < len(q); i++ {
		if t[i] != q[qi] {
			run = 0
			continue
		}
		s := 1
		if i == 0 || !unicode.IsLetter(t[i-1]) && !unicode.IsDigit(t[i-1]) {
			s += 8
		}
		run++
		s += run * 2
		score += s
		qi++
	}
	if qi < len(q) {
		return -1
	}
	if strings.HasPrefix(string(t), string(q)) {
		score += 20
	}
	return score - len(t)/8
}

func (a *App) paletteView(c *ui.Context) {
	p := a.palette
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	if p.query != p.shownQ {
		p.shownQ = p.query
		p.shown = p.shown[:0]
		for _, it := range p.items {
			s := fuzzyScore(p.query, it.title+" "+it.detail)
			if s >= 0 {
				it.score = s
				p.shown = append(p.shown, it)
			}
		}
		if p.query != "" {
			sort.SliceStable(p.shown, func(i, j int) bool { return p.shown[i].score > p.shown[j].score })
		}
		if len(p.shown) > 200 {
			p.shown = p.shown[:200]
		}
		p.index = 0
	}
	run := func(i int) {
		if i >= 0 && i < len(p.shown) {
			p.open = false
			p.shown[i].run()
		}
	}
	ui.DialogBase(c, &p.open, func(backdrop, panel ui.Element) {
		backdrop.Background(widgets.PickerBackdrop).Justify(ui.Start).PaddingY(80)
		panel.Width(600).Radius(12).Background(t.Background).Border(1, t.Border).Clip().
			Shadow(0, 16, 48, 0, ui.RGBA(0, 0, 0, 0.3)).Label("Command palette")
		ui.Row(c).Padding(10, 14).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Icon(c, widgets.IconSearch).TextColor(pal.Muted).FontSize(15)
			placeholder := "Search commands, connections and tables"
			if p.tables {
				placeholder = "Open a table or a routine"
			}
			in := ui.TextInputBase(c, &p.query).Placeholder(placeholder).AutoFocus().Grow(1).FontSize(15).Label("Search")
			switch {
			case in.Shortcut(0, ui.KeyDown):
				p.index = min(p.index+1, len(p.shown)-1)
				p.list.ScrollIntoView(p.index)
			case in.Shortcut(0, ui.KeyUp):
				p.index = max(p.index-1, 0)
				p.list.ScrollIntoView(p.index)
			case in.Shortcut(0, ui.KeyPageDown):
				p.index = min(p.index+paletteRows, len(p.shown)-1)
				p.list.ScrollIntoView(p.index)
			case in.Shortcut(0, ui.KeyPageUp):
				p.index = max(p.index-paletteRows, 0)
				p.list.ScrollIntoView(p.index)
			case in.Submitted():
				run(p.index)
			}
		})
		p.row = p.index
		p.list.Selected = &p.row
		ui.List(c, &p.list, len(p.shown), func(i int) {
			it := p.shown[i]
			row := ui.Row(c).Padding(7, 14).Gap(10)
			on := i == p.index
			if on {
				row.Background(t.Accent)
			}
			row.Children(func() {
				col := pal.Muted
				if on {
					col = t.AccentText
				}
				if it.icon != nil {
					ui.Icon(c, it.icon).FontSize(14).TextColor(col)
				}
				title := ui.Text(c, it.title).SingleLine().Shrink(1)
				if on {
					title.TextColor(t.AccentText)
				}
				ui.Text(c, it.detail).FontSize(12).TextColor(col).SingleLine().Grow(1).Shrink(1)
				if k := keymap.First(it.key); k != "" {
					ui.Text(c, k).FontSize(11.5).TextColor(col)
				}
				ui.Text(c, it.group).FontSize(11).TextColor(col)
			})
			if row.Clicked() {
				run(i)
			}
		}).MaxHeight(380).Children(func() {
			if len(p.shown) == 0 {
				ui.Text(c, "Nothing matches.").TextColor(pal.Muted).Padding(14)
			}
		})
	})
	if !p.open {
		a.palette = nil
	}
}
