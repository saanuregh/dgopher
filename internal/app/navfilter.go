package app

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"dgopher/internal/connection"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// The navigator's filter is a view of its two trees: navShown and
// navShownRoots give each outline the rows whose names hold the text
// typed and the rows that hold such rows, and the trees' keys, menus and
// drags work on what shows. Only what is known is searched: walking a closed
// node reads nothing (navChildren), so no connection is opened and no
// catalog read to be matched.

// navName is what a node shows as its name: "" for a section, a folder of
// a schema and a note, which have none of their own.
func (a *App) navName(n navNode) string {
	switch n.kind {
	case nodeConn:
		if cn := a.connByID(n.conn); cn != nil {
			return cn.Config.Name
		}
	case nodeConnFolder, nodeFileFolder:
		return path.Base(n.name)
	case nodeQueryFile, nodeDashboard, nodeModel:
		if p := a.projectByDir(n.projectDir); p != nil {
			s, _ := sectionOf(n)
			return a.fileName(p, s, n.name)
		}
	case nodeBookmark:
		if p := a.projectByDir(n.projectDir); p != nil {
			b, _ := a.bookmark(p, n)
			return b.Name
		}
	case nodeDatabase, nodeDocDatabase:
		return n.database
	case nodeDocCollection:
		return n.name
	case nodeSchema:
		return n.schema
	case nodeObject:
		return n.name
	case nodeItem, nodeColumn:
		_, name, _ := strings.Cut(n.name, "\x00")
		return name
	}
	return ""
}

// navMatches reports whether the filter's text is in a node's name, or in
// the name of a node above it: what matches shows all it holds, as a
// connection its tables. A node says what is above it in its own fields.
func (a *App) navMatches(n navNode) bool {
	has := func(s string) bool { return s != "" && strings.Contains(strings.ToLower(s), a.nav.needle) }
	if n.conn == "" {
		if n.kind == nodeConnFolder || n.kind == nodeFileFolder {
			return has(n.name) // its own name and its folders'
		}
		return has(a.navName(n))
	}
	cn := a.connByID(n.conn)
	if cn == nil {
		return false
	}
	if has(cn.Config.Name) || has(cn.Config.Folder) || has(n.database) || has(n.schema) {
		return true
	}
	switch n.kind {
	case nodeObject, nodeDocCollection:
		return has(n.name)
	case nodeItem:
		return has(a.navName(n))
	case nodeColumn, nodeFolder:
		// A table's columns and partitions show with it; a column is not
		// matched by its own name, which is read only as its table opens.
		table, _, _ := strings.Cut(n.name, "\x00")
		return has(table)
	}
	return false
}

// navHolds reports whether a node under n matches, among those known:
// down to the tables and the files, not into a table's columns.
func (a *App) navHolds(n navNode) bool {
	switch n.kind {
	case nodeObject, nodeItem, nodeColumn, nodeInfo, nodeQueryFile, nodeDashboard, nodeModel, nodeBookmark, nodeDocCollection, nodeDocIndex:
		return false
	}
	// Asked of a node by each row above it: answered once a frame.
	if held, ok := a.nav.holds[n]; ok {
		return held
	}
	held := false
	for _, ch := range a.navChildren(n) {
		if ch.kind != nodeInfo && (a.navMatches(ch) || a.navHolds(ch)) {
			held = true
			break
		}
	}
	a.nav.holds[n] = held
	return held
}

// navKeep leaves, of nodes, those the filter shows.
func (a *App) navKeep(nodes []navNode) []navNode {
	out := make([]navNode, 0, len(nodes))
	for _, n := range nodes {
		if n.kind != nodeInfo && (a.navMatches(n) || a.navHolds(n)) {
			out = append(out, n)
		}
	}
	return out
}

// navShownRoots are a pane's roots under the filter: the database tree's,
// or those of the projects' files.
func (a *App) navShownRoots(files bool) []navNode {
	if a.nav.needle == "" {
		return a.paneRoots(files)
	}
	return a.navKeep(a.paneRoots(files))
}

// navShown are a node's children under the filter: all of them under a
// node that matches, else those that match or hold a match.
func (a *App) navShown(n navNode) []navNode {
	kids := a.navChildren(n)
	if a.nav.needle == "" || a.navMatches(n) {
		return kids
	}
	return a.navKeep(kids)
}

// navFilterSync takes up the text typed, before the trees are built: the
// nodes above each match are opened, so that it shows, from the trees as
// open as they were before the filter; cleared, they are that again, and
// the row chosen meanwhile is brought into view.
func (a *App) navFilterSync() {
	if a.nav.holds == nil {
		a.nav.holds = map[navNode]bool{}
	}
	clear(a.nav.holds) // what a node holds is asked again each frame
	needle := strings.ToLower(strings.TrimSpace(a.nav.filter))
	if needle == a.nav.needle {
		return
	}
	panes := a.nav.panes()
	if a.nav.needle == "" {
		a.nav.openBefore = nil
		for _, pane := range panes {
			a.nav.openBefore = slices.AppendSeq(a.nav.openBefore, pane.tree.Open.All())
		}
	}
	// The row chosen under the filter, in the pane that had the keys.
	var chosen *navNode
	if n, ok := panes[b2i(a.nav.inFiles)].chosen(); ok && n.kind != nodeInfo {
		chosen = &n
	}
	a.nav.needle = needle
	for _, pane := range panes {
		pane.tree.Open.Clear()
	}
	for _, n := range a.nav.openBefore {
		a.nav.expand(n)
	}
	if needle == "" {
		a.nav.openBefore = nil
		if chosen != nil {
			a.nav.revealNode(chosen)
		}
		return
	}
	var down func(n navNode)
	down = func(n navNode) {
		if !a.navHolds(n) {
			return
		}
		a.nav.expand(n)
		for _, ch := range a.navChildren(n) {
			down(ch)
		}
	}
	for _, files := range []bool{false, true} {
		for _, root := range a.paneRoots(files) {
			down(root)
		}
	}
	a.nav.revealNode(nil)
	a.nav.pickMatch = true
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// navPickMatch chooses the first row that matches by its own name, once
// the trees show the filter's rows, the files' before the database
// tree's, as they show: ↵ in the box then opens it.
func (a *App) navPickMatch() {
	if !a.nav.pickMatch {
		return
	}
	a.nav.pickMatch = false
	panes := a.nav.panes()
	for _, i := range [2]int{1, 0} {
		pane := panes[i]
		for r := range pane.tree.Rows() {
			n := pane.tree.Item(r)
			if n.kind != nodeColumn && strings.Contains(strings.ToLower(a.navName(n)), a.nav.needle) {
				*pane.row = r
				pane.tree.List.ScrollIntoView(r)
				a.nav.inFiles = i == 1
				return
			}
		}
	}
}

// navNameText is a row's name, with the letters the filter matched
// underlined in the warning's color: why the row shows reads at a glance.
// A line and not a background, which is as tall as the row's text and
// reads as heavy as the chosen row's tint.
func (a *App) navNameText(c *ui.Context, name string) ui.Element {
	needle := a.nav.needle
	low := strings.ToLower(name)
	at := -1
	// A name whose lower case is another length cannot be cut by it.
	if needle != "" && len(low) == len(name) {
		at = strings.Index(low, needle)
	}
	if at < 0 {
		return ui.Text(c, name)
	}
	end := at + len(needle)
	return ui.RichText(c,
		ui.Span{Text: name[:at]},
		ui.Span{Text: name[at:end], Underline: true, DecorationColor: c.Theme().Warning, DecorationThickness: 2, UnderlineOffset: 3},
		ui.Span{Text: name[end:]},
	).Label(name)
}

// navCount is the count beside a heading of n things: nothing for none,
// and under a filter that leaves fewer, how many of them show.
func (a *App) navCount(heading navNode, n int) string {
	if n == 0 {
		return ""
	}
	if a.nav.needle != "" {
		if shown := a.navShownUnder(heading); shown < n {
			return fmt.Sprintf("%d/%d", shown, n)
		}
	}
	return fmt.Sprint(n)
}

// navShownUnder counts what the filter shows under a heading, through the
// folders of connections, which a section's count does not count.
func (a *App) navShownUnder(heading navNode) int {
	shown := 0
	for _, ch := range a.navShown(heading) {
		switch ch.kind {
		case nodeInfo:
		case nodeConnFolder:
			shown += a.navShownUnder(ch)
		default:
			shown++
		}
	}
	return shown
}

// navFilterBox is the navigator's filter, under its bar: ↵ opens the row
// chosen, ↓ goes on to its pane, Esc clears it.
func (a *App) navFilterBox(c *ui.Context) {
	ui.Row(c).Padding(0, 8, 6, 8).Children(func() {
		in := widgets.SearchBox(c, &a.nav.filter, "Filter by name", 0).Label("Filter the navigator").
			Tooltip("Show the connections, tables, files, dashboards and models whose names hold what you type")
		// The match chosen is in the pane navPickMatch found it in.
		pane := a.nav.panes()[b2i(a.nav.inFiles)]
		switch n, ok := pane.chosen(); {
		case ok && in.Submitted():
			a.activate(n)
		case in.Shortcut(0, ui.KeyDown):
			// To the pane of the match chosen; with nothing typed, to
			// the one under the box, the files'.
			a.focusWant = [2]string{"nav", "nav-files"}[b2i(a.nav.inFiles || a.nav.needle == "")]
		}
		if a.focusWant == "nav-filter" && in.Focus().Focused() {
			a.focusWant = ""
		}
	})
}

// navNoMatch says that the filter leaves nothing, what it did not search,
// and the way out: in place of the two panes, which have no rows to show.
func (a *App) navNoMatch(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	ui.Column(c).Padding(28, 16, 16, 16).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, widgets.IconSearch).FontSize(widgets.IconLarge).TextColor(pal.Muted).Margin(0, 0, 4, 0)
		ui.Text(c, "No matches for “"+strings.TrimSpace(a.nav.filter)+"”").TextAlign(ui.Center)
		ui.Text(c, a.navUnsearched()).FontSize(widgets.TextSmall).TextColor(pal.Muted).TextAlign(ui.Center)
		ui.Row(c).Gap(8).Margin(6, 0, 0, 0).Children(func() {
			if ui.Button(c, "Clear Filter").Clicked() {
				a.nav.filter = ""
			}
			widgets.Keycap(c, "Esc")
		})
	})
}

// navUnsearched says which tables the filter could not match: those of the
// connections not opened, whose names are not known.
func (a *App) navUnsearched() string {
	closed := 0
	for _, cn := range a.conns {
		if cn.Status != connection.StatusConnected {
			closed++
		}
	}
	switch {
	case closed == 0:
		return "Tables not listed yet were not searched."
	case closed == len(a.conns):
		return "No connection is open, so no tables were searched."
	}
	return fmt.Sprintf("%s not open: %s tables were not searched.", widgets.Count(closed, "connection"), map[bool]string{true: "its", false: "their"}[closed == 1])
}
