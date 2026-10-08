package app

import (
	"sort"
	"strings"
	"unicode"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// paletteItem is a command or a place of the command palette.
type paletteItem struct {
	title  string
	detail string
	group  string
	icon   *ui.SVG
	run    func()
	score  int
}

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
	cmds = append(cmds,
		paletteItem{title: "New Connection…", group: "Command", icon: widgets.IconPlus, run: func() { a.openConnForm(nil) }},
		paletteItem{title: "Query History", group: "Command", icon: widgets.IconHistory, run: func() { a.openHistory() }},
		paletteItem{title: "Audit Log", detail: "who did what, verifiable", group: "Command", icon: widgets.IconShield, run: a.openAudit},
		paletteItem{title: "Settings…", group: "Command", icon: widgets.IconSettings, run: func() { a.settingsOpen = true }},
		paletteItem{title: "Toggle Sidebar", group: "Command", icon: widgets.IconColumns, run: func() { a.sidebarHidden = !a.sidebarHidden }},
		paletteItem{title: "Keyboard Shortcuts", detail: widgets.KeyLabel("⌘/"), group: "Command", icon: widgets.IconCode, run: func() { a.shortcutsOpen = true }},
		paletteItem{title: "New Project…", group: "Command", icon: widgets.IconFolder, run: func() { a.openNewProject(nil) }},
		paletteItem{title: "Add Existing Folder…", detail: "list a project folder", group: "Command", icon: widgets.IconFolder, run: a.addExistingProject},
	)
	if active != nil {
		cmds = append(cmds, paletteItem{title: "New SQL Editor", detail: active.Config.Name, group: "Command", icon: widgets.IconCode, run: func() { a.NewQueryTab(active, "", "") }})
		if active.Status == connection.StatusConnected {
			if !active.Config.Engine.IsFile() {
				cmds = append(cmds, paletteItem{title: "Server Activity", detail: active.Config.Name, group: "Command", icon: widgets.IconClock, run: func() { a.openActivity(active) }})
			}
			cmds = append(cmds, paletteItem{title: "Refresh Schema", detail: active.Config.Name, group: "Command", icon: widgets.IconRefresh, run: func() { a.refresh(active) }},
				paletteItem{title: "Disconnect", detail: active.Config.Name, group: "Command", icon: widgets.IconUnplug, run: func() {
					a.requestDisconnect("Disconnect "+active.Config.Name+"?", []*connection.Conn{active}, func() { a.disconnect(active) })
				}})
		}
	}
	if len(a.tabs) > 0 {
		cmds = append(cmds, paletteItem{title: "Close Tab", group: "Command", icon: widgets.IconX, run: func() { a.closeTab(a.active) }})
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
	}
	sort.SliceStable(tables, func(i, j int) bool { return tables[i].title < tables[j].title })
	snippets := a.snippetItems()
	if q, ok := a.ActiveTab().(*query.Tab); ok {
		cmds = append(cmds, paletteItem{title: "Save as Snippet…", group: "Command", icon: widgets.IconSave, run: func() { a.AskSnippet(q) }})
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
		backdrop.Background(ui.RGBA(0, 0, 0, 0.25)).Justify(ui.Start).PaddingY(80)
		panel.Width(600).Radius(12).Background(t.Background).Border(1, t.Border).Clip().
			Shadow(0, 16, 48, 0, ui.RGBA(0, 0, 0, 0.3)).Label("Command palette")
		ui.Row(c).Padding(10, 14).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Icon(c, widgets.IconSearch).TextColor(pal.Muted).FontSize(15)
			placeholder := "Search commands, connections and tables"
			if p.tables {
				placeholder = "Open a table"
			}
			in := ui.TextInputBase(c, &p.query).Placeholder(placeholder).AutoFocus().Grow(1).FontSize(15).Label("Search")
			switch {
			case in.Shortcut(0, ui.KeyDown):
				p.index = min(p.index+1, len(p.shown)-1)
				p.list.ScrollIntoView(p.index)
			case in.Shortcut(0, ui.KeyUp):
				p.index = max(p.index-1, 0)
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
