package app

import (
	"slices"
	"sort"
	"strings"
	"unicode"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/panes"
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
	for _, pr := range a.projects {
		a.ScanQueries(pr, false)
	}
}

// paletteItems lists the commands, the tables of the connections and the
// query files of the projects.
func (a *App) paletteItems(tablesFirst bool) []paletteItem {
	var cmds, conns, tables, files []paletteItem
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
		paletteItem{title: "Show Latest Error", key: keymap.ShowLatestError, group: "Command", icon: widgets.IconAlert, run: a.showLatestError},
		paletteItem{title: "Notifications", detail: "every error and message of this session", key: keymap.ToggleNotifications, group: "Command", icon: widgets.IconBell, run: a.toggleNotificationsByKey},
		paletteItem{title: "Settings…", key: keymap.Settings, group: "Command", icon: widgets.IconSettings, run: a.openSettings},
		paletteItem{title: "Toggle Sidebar", key: keymap.ToggleSidebar, group: "Command", icon: widgets.IconColumns, run: func() { a.layout.SidebarHidden = !a.layout.SidebarHidden }},
		paletteItem{title: "Keyboard Shortcuts", key: keymap.ShortcutsList, group: "Command", icon: widgets.IconCode, run: func() { a.shortcutsOpen = true }},
		paletteItem{title: "New Project…", group: "Command", icon: widgets.IconFolder, run: func() { a.openNewProject(nil) }},
		paletteItem{title: "AI Chat", detail: "Claude Code, Codex or pi, beside the editor", key: keymap.ToggleChat, group: "Command", icon: widgets.IconSparkles, run: a.toggleChat},
		paletteItem{title: "Add Existing Folder…", detail: "list a project folder", key: keymap.AddFolder, group: "Command", icon: widgets.IconFolder, run: a.addExistingProject},
	)
	if active != nil {
		cmds = append(cmds, paletteItem{title: "New SQL Editor", detail: active.Config.Name, key: keymap.NewEditor, group: "Command", icon: widgets.IconCode, run: func() { a.NewQueryTab(active, "", "") }})
		if active.Config.Engine.IsSQL() {
			cmds = append(cmds, paletteItem{title: "New SQL Notebook", detail: active.Config.Name + ", cells with their own results", key: keymap.NewNotebook, group: "Command", icon: widgets.IconNotebook,
				run: func() { a.newNotebook(active) }})
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
			if usersSupported(active.Config.Engine) {
				cmds = append(cmds, paletteItem{title: "Users and Privileges", detail: active.Config.Name, group: "Command", icon: widgets.IconUsers, run: func() { a.openUsers(active) }})
			}
			if active.Config.Engine.IsSQL() {
				cmds = append(cmds, paletteItem{title: "Catalog Queries", detail: active.Config.Name + ", what the app reads on its own", group: "Command", icon: widgets.IconHistory,
					run: func() { a.openCatalogQueries(active) }})
			}
			cmds = append(cmds, paletteItem{title: "Refresh Schema", detail: active.Config.Name, group: "Command", icon: widgets.IconRefresh, run: func() { a.refresh(active) }},
				paletteItem{title: "Disconnect", detail: active.Config.Name, group: "Command", icon: widgets.IconUnplug, run: func() {
					a.askDisconnect(active)
				}})
		}
	}
	if tab := a.ActiveTab(); tab != nil {
		g := a.window.panes.Focused()
		for _, bulk := range bulkCloses(tab, a.tabs().ActiveIndex()) {
			if anyUnpinned(g, bulk.which) {
				cmds = append(cmds, paletteItem{title: bulk.command, group: "Command", icon: widgets.IconX,
					run: func() { a.closeTabs(g, bulk.which) }})
			}
		}
		w := a.window
		for _, s := range []struct {
			title, id string
			d         panes.Direction
		}{{"Split Right", keymap.SplitRight, panes.Right}, {"Split Down", keymap.SplitDown, panes.Down}, {"Split Left", keymap.SplitLeft, panes.Left}, {"Split Up", keymap.SplitUp, panes.Up}} {
			cmds = append(cmds, paletteItem{title: s.title, detail: "the tab in a new group", key: s.id, group: "Command", icon: widgets.IconColumns,
				run: func() { a.splitTab(w, g, s.d) }})
		}
		if w.panes.Len() > 1 {
			cmds = append(cmds,
				paletteItem{title: "Close Group", detail: "its tabs move to the group beside", key: keymap.ClosePane, group: "Command", icon: widgets.IconX, run: func() { a.closeGroup(w, g) }},
				paletteItem{title: "Join All Groups", detail: "every tab in this group", key: keymap.JoinPanes, group: "Command", icon: widgets.IconColumns, run: func() { a.joinGroups(w) }},
				paletteItem{title: "Focus Next Group", key: keymap.NextPane, group: "Command", icon: widgets.IconColumns, run: func() { a.focusGroup(w, w.panes.Next(g)) }})
		}
	}
	if tab := a.ActiveTab(); tab != nil {
		w, pinned := a.window, a.tabs().Pinned(tab)
		title := "Pin Tab"
		if pinned {
			title = "Unpin Tab"
		}
		cmds = append(cmds, paletteItem{title: title, group: "Command", icon: widgets.IconPin, run: func() { w.tabs().SetPinned(tab, !pinned) }})
	}
	if len(a.closedTabs) > 0 {
		cmds = append(cmds, paletteItem{title: "Reopen Closed Tab", key: keymap.ReopenTab, group: "Command", icon: widgets.IconUndo, run: a.reopenTab})
	}
	if tab := a.ActiveTab(); tab != nil && (a.window == a.main || a.window.tabCount() > 1) {
		cmds = append(cmds, paletteItem{title: "Move Tab to New Window", group: "Command", icon: widgets.IconLayers, run: func() { a.Post(func() { a.moveToWindow(tab, nil) }) }})
	}
	if q, ok := a.ActiveTab().(*query.Tab); ok {
		cmds = append(cmds, paletteItem{title: "Duplicate Editor", group: "Command", icon: widgets.IconFile, run: func() { a.Post(func() { a.duplicateQuery(q) }) }})
		title, detail := "Show as Notebook", "cells, each with its own results"
		if q.Notebook() {
			title, detail = "Show as Script", "one editor of the whole file"
		}
		cmds = append(cmds, paletteItem{title: title, detail: detail, key: keymap.ToggleNotebook, group: "Command", icon: widgets.IconNotebook, run: q.ToggleNotebook})
	}
	if a.tabs().Len() > 0 {
		cmds = append(cmds, paletteItem{title: "Close Tab", key: keymap.CloseTab, group: "Command", icon: widgets.IconX, run: a.closeActive})
	}
	for _, cn := range a.conns {
		detail := cn.Config.Engine.Label() + " · " + cn.Config.Env.Label()
		title := "Open " + cn.Config.Name
		run := func() { a.NewQueryTab(cn, "", "") }
		if cn.Config.Engine == db.Redis {
			run = func() { a.openRedis(cn) }
		}
		conns = append(conns, paletteItem{title: title, detail: detail, group: "Connection", icon: widgets.IconDatabase, run: run})
		for key, objs := range cn.Objects {
			for _, o := range objs {
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
		run: func() { a.keys = &keysEditor{open: true} }},
		paletteItem{title: "Open keymap.json", detail: "the keys you set, as Zed keeps them", key: keymap.OpenKeymap, group: "Command", icon: widgets.IconFile, run: a.openKeymap})
	for _, p := range a.projects {
		// Each item keeps its own path: the closures outlive the loop.
		names, paths := a.dashboardNames(p)
		for i, name := range names {
			path := paths[i]
			cmds = append(cmds, paletteItem{title: name, detail: p.Name, group: "Dashboard", icon: widgets.IconLayers, run: func() { a.openDashboard(p, path) }})
		}
		names, paths = a.modelNames(p)
		for i, name := range names {
			path := paths[i]
			cmds = append(cmds, paletteItem{title: name, detail: p.Name, group: "Data Model", icon: widgets.IconSchema, run: func() { a.openModel(p, path) }})
		}
		for _, rel := range p.Files {
			files = append(files, paletteItem{title: rel, detail: p.Name, group: "Query", icon: widgets.IconFile, run: func() { a.openQueryFile(p, rel, nil) }})
		}
	}
	if tablesFirst {
		return slices.Concat(tables, files, conns, snippets, cmds)
	}
	return slices.Concat(cmds, snippets, conns, tables, files)
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
			a.instant = true // a picker's choice shows at once, as a key's
			p.shown[i].run()
		}
	}
	ui.DialogBase(c, &p.open, func(backdrop, panel ui.Element) {
		widgets.PickerLook(c, backdrop, panel)
		panel.Width(600).Radius(12).Background(t.Background).Border(1, t.Border).Clip().Label("Command palette")
		ui.Row(c).Padding(10, 14).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Icon(c, widgets.IconSearch).TextColor(pal.Muted).FontSize(15)
			placeholder := "Search commands, connections and tables"
			if p.tables {
				placeholder = "Open a table, a routine or a query file"
			}
			in := ui.TextInputBase(c, &p.query).Placeholder(placeholder).AutoFocus().Grow(1).FontSize(15).Label("Search")
			if widgets.ListKeys(in, &p.index, len(p.shown), &p.list, paletteRows) {
				run(p.index)
			}
		})
		p.row = p.index
		p.list.Selected = &p.row
		widgets.ChosenRows(c, func() { a.paletteList(c, p, run) })
	})
	if !p.open {
		a.palette = nil
	}
}

// paletteList is the palette's commands, the chosen one tinted.
func (a *App) paletteList(c *ui.Context, p *paletteState, run func(int)) {
	pal := widgets.PaletteOf(c)
	ui.List(c, &p.list, len(p.shown), func(i int) {
		it := p.shown[i]
		row := ui.Row(c).Padding(7, 14).Gap(10)
		row.Children(func() {
			col := pal.Muted
			if it.icon != nil {
				ui.Icon(c, it.icon).FontSize(14).TextColor(col)
			}
			ui.Text(c, it.title).SingleLine().Shrink(1)
			ui.Text(c, it.detail).FontSize(12).TextColor(col).SingleLine().Grow(1).Shrink(1)
			if k := keymap.First(it.key); k != "" {
				widgets.Keycap(c, k)
			}
			// Commands are most of the list: only the other kinds say
			// what they are.
			if it.group != "Command" {
				ui.Text(c, it.group).FontSize(11).TextColor(col)
			}
		})
		if row.Clicked() {
			run(i)
		}
	}).MaxHeight(380).Padding(6).Children(func() {
		if len(p.shown) == 0 {
			ui.Text(c, "Nothing matches.").TextColor(pal.Muted).Padding(8)
		}
	})
}
