package app

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/ui/dashboard"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/panes"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// activeConn is the connection of the tab in front, else the one chosen
// in the navigator.
func (a *App) activeConn() *connection.Conn {
	if t := a.ActiveTab(); t != nil {
		return t.Connection()
	}
	if a.nav.row >= 0 && a.nav.row < a.nav.tree.Rows() {
		return a.connByID(a.nav.tree.Item(a.nav.row).conn)
	}
	if len(a.conns) == 1 {
		return a.conns[0]
	}
	return nil
}

func (a *App) WriteClipboard(s string) {
	if a.clipboard != nil {
		a.clipboard(s)
	}
}

// ReadClipboard is the clipboard's text, read outside a frame, as by a
// menu.
func (a *App) ReadClipboard() string {
	if a.readClip == nil {
		return ""
	}
	return a.readClip()
}

// view builds the window.
func (a *App) view(c *ui.Context) {
	// Tabs a key opened, closed or brought in front show so at once: a
	// key asks for no motion, which only a click's change has.
	tabs, front := a.tabs().Len(), a.ActiveTab()
	a.drain()
	a.now = c.Now()
	widgets.ApplyTheme(c, strings.TrimSpace(a.settings.UIFontFamily))
	widgets.HoverCards = hoverSettings(a.settings.HoverCards)
	if a.clipboard == nil {
		a.clipboard, a.readClip = c.WriteClipboard, c.ReadClipboard
	}
	a.shortcuts(c)
	if a.instant || a.tabs().Len() != tabs || a.ActiveTab() != front {
		c.DisableAnimations()
		a.instant = false
	}
	a.window.several = a.window.panes.Len() > 1
	root := ui.Column(c).Fill()
	defer a.dropZone(c, root)
	root.Children(func() {
		// The sidebar is the main window's; the others hold tabs alone.
		if a.layout.SidebarHidden || a.window != a.main {
			ui.Column(c).Grow(1).Children(func() { a.workspace(c) })
		} else {
			ui.Split(c, &a.layout.SidebarWidth, func() { a.sidebar(c) }, func() { a.workspace(c) }).Grow(1)
		}
		a.statusBar(c)
	})
	// A tab chosen after the sidebar was drawn, as by a click on it,
	// shows there in the next frame, asked at once.
	if a.window == a.main && !a.layout.SidebarHidden && a.ActiveTab() != a.nav.shown {
		c.Invalidate()
	}
	a.noteShown(c)
	a.titleWindow()
	a.checkIdleTransactions(c)
	a.checkIdleConnections(c)
	// The dialogs and toasts show in the window the user uses.
	if a.window == a.focusedWindow() {
		a.overlays(c)
	}
	// Every menu of the window drawn in the app's theme, over the dialogs
	// too, as a field's in one.
	widgets.ShownMenus(c)
	widgets.FinishCards(c)
	a.saveWorkspace(false)
	if a.settingsDirty && !c.Root().Dragging() {
		a.settingsDirty = false
		a.SaveSettings()
	}
	if a.layout != a.layoutSaved && !c.Root().Dragging() {
		a.saveLayout()
	}
}

// overlays draws what shows over the window: the dialogs, and a toast.
func (a *App) overlays(c *ui.Context) {
	a.dialogs(c)
	if dataview.ExportOpen(a) {
		dataview.ExportView(a, c)
	}
	if a.history != nil {
		a.historyView(c)
	}
	if a.importing != nil {
		a.importView(c)
	}
	if a.sqlFile != nil {
		a.sqlFileView(c)
	}
	if a.snippetForm != nil {
		a.snippetFormView(c)
	}
	if a.newProject != nil {
		a.newProjectView(c)
	}
	if a.renaming != nil {
		a.renameView(c)
	}
	if a.generating != nil {
		a.generateView(c)
	}
	if a.catalogQueries != nil {
		a.catalogQueriesView(c)
	}
	if a.backup != nil {
		a.backupView(c)
	}
	if a.copying != nil {
		a.copyView(c)
	}
	if a.rowCompare != nil {
		a.rowCompareView(c)
	}
	if a.filling != nil {
		a.fillView(c)
	}
	if a.addingPanel != nil {
		a.addToDashboardView(c)
	}
	if a.savingModel != nil {
		a.saveModelView(c)
	}
	if a.newModel != nil {
		a.newModelView(c)
	}
	query.DialogsView(a, c)
	dataview.DialogsView(a, c)
	if a.idleWarn != nil {
		a.idleWarningView(c)
	}
	if a.shortcutsOpen {
		a.shortcutsView(c)
	}
	if a.touring {
		a.tourView(c)
	}
	if a.keys != nil {
		a.keysView(c)
	}
	a.questions(c)
	for _, tst := range a.toasts {
		if tst.action != "" {
			c.ToastAction(tst.text, tst.action, tst.run)
		} else {
			c.Toast(tst.text)
		}
	}
	a.toasts = nil
}

// titleWindow names a window but the main one by its tab in front.
func (a *App) titleWindow() {
	w := a.window
	if w == a.main || w.native == nil {
		return
	}
	title := "DGopher"
	if t := w.ActiveTab(); t != nil {
		title = t.Title() + " — DGopher"
	}
	if title != w.title {
		w.title = title
		w.native.SetTitle(title)
	}
}

// shortcuts handles the keys of the whole window.
func (a *App) shortcuts(c *ui.Context) {
	keymap.Dispatch(c, a.window)
	a.watchKeymap(c)
	a.openKeymapNow(c)
	pressed := keymap.Pressed
	if nativeMenuBar {
		pressed = keymap.LaterPressed // the first keys are the menu bar's
	}
	for _, id := range menuCommands {
		if pressed(c, id) {
			a.menuAction(id)()
		}
	}
	if n, ok := keymap.PressedArg(c, keymap.ActivateTab); ok && a.tabs().Len() > 0 {
		a.tabs().SelectAt(n - 1)
		a.focusWant = "editor" // the keys go on in the tab chosen
	}
	if keymap.Pressed(c, keymap.LastTab) && a.tabs().Len() > 0 {
		a.tabs().SelectAt(a.tabs().Len() - 1)
		a.focusWant = "editor"
	}
	a.groupKeys(c)
	switch {
	case keymap.Pressed(c, keymap.FocusNavigator):
		a.layout.SidebarHidden = false
		a.focusWant = "nav"
	case keymap.Pressed(c, keymap.FocusFilter) && a.ActiveTab() != nil:
		a.focusWant = "filter"
	case keymap.Pressed(c, keymap.ShortcutsList):
		a.shortcutsOpen = !a.shortcutsOpen
	case keymap.Pressed(c, keymap.NextTab):
		if a.tabs().Len() > 0 {
			a.tabs().Next()
			a.focusWant = "editor"
		}
	case keymap.Pressed(c, keymap.PreviousTab):
		if a.tabs().Len() > 0 {
			a.tabs().Previous()
			a.focusWant = "editor"
		}
	}
}

func (a *App) workspace(c *ui.Context) {
	t := c.Theme()
	space := ui.Column(c).Fill().Background(t.Background)
	a.tourPart(tourWorkspace, space)
	space.Children(func() {
		a.workspaceBar(c)
		if a.window.tabCount() == 0 {
			ui.Column(c).Grow(1).Shrink(1).MinHeight(0).Children(func() { a.welcome(c) })
			// The connection chosen in the navigator, as connecting it
			// shows, with Cancel.
			if cn := a.activeConn(); cn != nil {
				a.statusOf(c, nil, []connPlaces{{conn: cn, databases: []string{""}}})
			}
			return
		}
		a.paneNode(c, a.window.panes.Root())
		if a.several() != (a.window.panes.Len() > 1) {
			c.Invalidate() // split or joined as the frame was drawn
		}
		if a.focusWant == "editor" {
			a.focusWant = "" // a tab without an editor keeps the focus where it is
		}
	})
}

// paneNode draws a node of the window's groups: a group, or a split of
// two, which the user resizes.
func (a *App) paneNode(c *ui.Context, n *panes.Node) {
	if n.Leaf() {
		a.groupView(c, n.Group)
		return
	}
	split := ui.SplitPercent
	if n.Axis == panes.Column {
		split = ui.SplitVerticalPercent
	}
	split(c.Key(fmt.Sprintf("split-%p", n)), &n.Percent, func() { a.paneNode(c, n.First) }, func() { a.paneNode(c, n.Then) }).Grow(1)
}

// several reports whether the window shows more than one group, each
// then with a tab bar of its own, as of the frame's start.
func (a *App) several() bool { return a.window.several }

// groupView draws a group: its tab bar when the window has several, the
// tab in front, and where a dragged tab may go. The focus going into it,
// or a press on it, makes it the focused group.
func (a *App) groupView(c *ui.Context, g *panes.Group) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	w := a.window
	col := ui.Column(c.Key(fmt.Sprintf("group-%d", g.ID))).Grow(1).Shrink(1).MinWidth(0).MinHeight(0)
	col.Children(func() {
		if a.several() {
			bar := ui.Row(c).Height(barHeight(c)).Background(pal.Sidebar).BorderWidth(0, 0, 1, 0).BorderColor(th.Border)
			bar.Children(func() {
				a.tabBar(c, g)
				ui.Spacer(c)
				if widgets.IconButton(c, widgets.IconColumns, "Split Right").Tooltip(keymap.Hint("Split right", keymap.SplitRight)).Clicked() {
					a.Post(func() { a.splitTab(w, g, panes.Right) })
				}
				if widgets.IconButton(c, widgets.IconX, "Close Group").Tooltip(keymap.Hint("Close the group: its tabs move beside", keymap.ClosePane)).Clicked() {
					a.Post(func() { a.closeGroup(w, g) })
				}
			})
			// Dropped on the bar past the tabs: last in the group.
			if d, ok := ui.Drop[tabDrag](bar); ok {
				a.Post(func() { a.moveTabToGroup(w, d.tab, g, g.Tabs.Len()) })
			}
		}
		content := ui.Column(c).Grow(1).Shrink(1).MinHeight(0)
		content.Children(func() {
			if t := g.Tabs.Active(); t != nil {
				a.pane(c, t)
			} else {
				a.Post(func() { a.dropEmptyGroup(w, g) }) // as a restored group no tab came back to
				ui.Column(c).Grow(1).Center().Children(func() {
					ui.Text(c, "No tabs in this group").TextColor(pal.Muted)
				})
			}
			dragged, over := ui.DragOver[tabDrag](content)
			if over || g.Dropping != nil {
				t := cmp.Or[widgets.Tab](dragged.tab, g.Dropping)
				if g.Dropping = cmp.Or(a.dropZones(c, w, g, t), dragged.tab); g.Dropping == nil {
					c.Invalidate() // the zones go
				}
			}
		})
	})
	// The keys coming into it, or a press anywhere on it, as on a chart or
	// empty space that takes no keys.
	keys := col.FocusWithin()
	if (keys && !g.HadKeys || col.PressedInside()) && w.panes.Focused() != g {
		w.panes.Focus(g)
		c.Invalidate()
	}
	g.HadKeys = keys
}

// dropZones show, while a tab is dragged over a group, where it may go:
// a side of the group, splitting it there, or its middle, joining it. It
// returns the tab dragged over the zones, nil once it is over none.
func (a *App) dropZones(c *ui.Context, w *window, g *panes.Group, t widgets.Tab) widgets.Tab {
	th := c.Theme()
	var over widgets.Tab
	zone := func(label string, place func(ui.Element) ui.Element, then func(widgets.Tab)) {
		b := place(ui.Box(c.Key("drop-"+label)).Absolute()).Radius(8).Border(1, th.Accent).Background(th.Accent.Alpha(0.10)).Center().Label(label)
		if d, ok := ui.DragOver[tabDrag](b); ok {
			b.Background(th.Accent.Alpha(0.28))
			over = d.tab
		}
		b.Children(func() { ui.Text(c, label).FontSize(12).TextColor(th.Accent) })
		if d, ok := ui.Drop[tabDrag](b); ok {
			a.Post(func() { then(d.tab) })
		}
	}
	split := func(d panes.Direction) func(widgets.Tab) {
		return func(t widgets.Tab) {
			ng := w.panes.Split(g, d)
			a.moveTabToGroup(w, t, ng, 0)
		}
	}
	zone("Split Left", func(e ui.Element) ui.Element { return e.Left(8).Top(56).Bottom(56).Width(96) }, split(panes.Left))
	zone("Split Right", func(e ui.Element) ui.Element { return e.Right(8).Top(56).Bottom(56).Width(96) }, split(panes.Right))
	zone("Split Up", func(e ui.Element) ui.Element { return e.Top(8).Left(112).Right(112).Height(44) }, split(panes.Up))
	zone("Split Down", func(e ui.Element) ui.Element { return e.Bottom(8).Left(112).Right(112).Height(44) }, split(panes.Down))
	if !g.Tabs.Contains(t) {
		zone("Move Here", func(e ui.Element) ui.Element { return e.Top(64).Bottom(64).Left(112).Right(112) }, func(t widgets.Tab) { a.moveTabToGroup(w, t, g, g.Tabs.Len()) })
	}
	return over
}

// pane shows a tab, under the color of its connection's environment; of
// several groups, the focused one's is marked.
func (a *App) pane(c *ui.Context, t widgets.Tab) {
	th := c.Theme()
	// Keyed by a string: a pointer as the key loses the focus within.
	ui.Column(c.Key(tabKey(t))).Grow(1).Shrink(1).MinHeight(0).Children(func() {
		if cn := t.Connection(); cn != nil {
			// The environment's color all along the top of the work: the
			// first thing to see before typing into production.
			ui.Box(c).FillWidth().Height(3).Background(widgets.SafetyColor(&cn.Config))
		}
		if a.several() {
			band := th.Border
			if t == a.ActiveTab() {
				band = th.Accent
			}
			ui.Box(c).FillWidth().Height(2).Background(band)
		}
		ui.Column(c).Grow(1).Shrink(1).MinHeight(0).Children(func() { t.View(c) })
		a.tabStatus(c, t)
	})
}

// chooseTab brings the tab at i of a group in front, as a click on it
// does, and gives the group the window's keys.
func (a *App) chooseTab(g *panes.Group, i int) {
	a.window.panes.Focus(g)
	g.Tabs.SelectAt(i)
}

// moveTabToGroup moves a tab of the window to a group of it, at i, in
// front there, and focuses that group; its old group goes when left
// empty.
func (a *App) moveTabToGroup(w *window, t widgets.Tab, to *panes.Group, i int) {
	from := w.panes.GroupOf(t)
	if from == nil || w.panes.Index(to) < 0 {
		return
	}
	if from == to {
		to.Tabs.Move(t, i)
		w.panes.Focus(to)
		return
	}
	pinned := from.Tabs.Pinned(t)
	from.Tabs.Remove(t)
	to.Tabs.Insert(t, i, pinned)
	w.panes.Focus(to)
	if from.Tabs.Len() == 0 {
		a.dropEmptyGroup(w, from)
	}
	w.focusWant = "editor"
}

// splitTab moves the tab in front of a group to a new group beside it in
// direction d; with no other tab to leave behind, a group of one tab is
// split with a copy of a SQL editor, else the split is refused.
func (a *App) splitTab(w *window, g *panes.Group, d panes.Direction) {
	t := g.Tabs.Active()
	if t == nil {
		return
	}
	if g.Tabs.Len() == 1 {
		q, ok := t.(*query.Tab)
		if !ok {
			a.Toast("One tab cannot be split: open another to split the group", "", nil)
			return
		}
		ng := w.panes.Split(g, d)
		w.panes.Focus(ng)
		a.window = w
		a.duplicateQuery(q) // in front of the new group
		return
	}
	ng := w.panes.Split(g, d)
	a.moveTabToGroup(w, t, ng, 0)
}

// moveTabToward moves the tab in front of the focused group to the group
// beside it in direction d, made when there is none.
func (a *App) moveTabToward(w *window, d panes.Direction) {
	g := w.panes.Focused()
	t := g.Tabs.Active()
	if t == nil {
		return
	}
	if to := w.panes.Neighbor(g, d); to != nil {
		a.moveTabToGroup(w, t, to, to.Tabs.Len())
		return
	}
	if g.Tabs.Len() == 1 {
		return // the group would be left empty beside itself
	}
	a.moveTabToGroup(w, t, w.panes.Split(g, d), 0)
}

// focusGroup gives the keys to a group, and the focus to its tab.
func (a *App) focusGroup(w *window, g *panes.Group) {
	if g == nil {
		return
	}
	w.panes.Focus(g)
	w.focusWant = "editor"
}

// closeGroup takes a group away, its tabs going to the group beside it,
// unclosed: nothing in them is lost.
func (a *App) closeGroup(w *window, g *panes.Group) {
	if w.panes.Len() < 2 || w.panes.Index(g) < 0 {
		return
	}
	tabs := g.Tabs.Tabs()
	pinned := make([]bool, len(tabs))
	for i, t := range tabs {
		pinned[i] = g.Tabs.Pinned(t)
	}
	to := w.panes.Beside(g) // the one beside, whichever has the keys
	g.Tabs.TakeAll()
	w.panes.Remove(g)
	for i, t := range tabs {
		to.Tabs.Insert(t, to.Tabs.Len(), pinned[i])
	}
	w.panes.Focus(to)
}

// joinGroups puts every group's tabs in the focused one.
func (a *App) joinGroups(w *window) {
	for w.panes.Len() > 1 {
		for _, g := range w.panes.Groups() {
			if g != w.panes.Focused() {
				a.closeGroup(w, g)
				break
			}
		}
	}
}

// groupKeys handles the keys of the window's groups.
func (a *App) groupKeys(c *ui.Context) {
	w := a.window
	g := w.panes.Focused()
	pressed := func(id string) bool { return keymap.Pressed(c, id) }
	directions := []struct {
		d                  panes.Direction
		split, move, focus string
	}{
		{panes.Right, keymap.SplitRight, keymap.MoveTabRight, keymap.FocusPaneRight},
		{panes.Left, keymap.SplitLeft, keymap.MoveTabLeft, keymap.FocusPaneLeft},
		{panes.Down, keymap.SplitDown, keymap.MoveTabDown, keymap.FocusPaneDown},
		{panes.Up, keymap.SplitUp, keymap.MoveTabUp, keymap.FocusPaneUp},
	}
	for _, x := range directions {
		if pressed(x.split) {
			a.splitTab(w, g, x.d)
		}
	}
	for _, x := range directions {
		if pressed(x.move) {
			a.moveTabToward(w, x.d)
		}
	}
	for _, x := range directions {
		if pressed(x.focus) {
			a.focusGroup(w, w.panes.Neighbor(g, x.d))
		}
	}
	if n, ok := keymap.PressedArg(c, keymap.ActivatePane); ok {
		if groups := w.panes.Groups(); n >= 1 && n <= len(groups) {
			a.focusGroup(w, groups[n-1])
		}
	}
	if pressed(keymap.NextPane) {
		a.focusGroup(w, w.panes.Next(g))
	}
	if pressed(keymap.ClosePane) {
		a.closeGroup(w, g)
	}
	if pressed(keymap.JoinPanes) {
		a.joinGroups(w)
	}
}

func (a *App) tabBar(c *ui.Context, g *panes.Group) {
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	w := a.window
	l := &g.Tabs
	var closed widgets.Tab
	var dropped *tabDrag // and where to, after the tabs are drawn
	droppedAt := 0
	height := barHeight(c)
	focusedGroup := w.panes.Focused() == g
	// A tab names its connection under its title only where another
	// shares the title, as two query-1.sql: its dot and tooltip say it
	// otherwise, and the status bar for the one in front.
	titles := map[string]int{}
	for _, tb := range l.All() {
		titles[tb.Title()]++
	}
	ui.ScrollHorizontal(c).TrackScroll(&g.TabScroll).Shrink(1).Children(func() {
		ui.Row(c).AlignItems(ui.Stretch).Children(func() {
			for i, tb := range l.All() {
				on := i == l.ActiveIndex()
				pinned := l.Pinned(tb)
				groups := byConnection(tabPlaces(tb))
				item := ui.ButtonBase(c.Key(tabKey(tb))).Padding(0, 6, 0, 12).Height(height).MaxWidth(260).Label(tb.Title()).
					Drag(tabDrag{tb})
				if at, ok := item.DragMissed(); ok && outsideWindow(c, at) {
					a.Post(func() { a.tearOut(w, tb, at) })
				}
				if len(groups) > 0 {
					item.Tooltip(placesTooltip(groups))
				}
				switch {
				case on:
					item.Background(t.Background)
				case item.Hovered():
					item.Background(pal.Hover)
				}
				if on && g.RevealTab {
					item.ScrollIntoView()
					g.RevealTab = false
				}
				hovered := item.Hovered()
				item.Transition(tabMotion)
				// A short line between two tabs in the back; the one in front
				// stands out on its own.
				if next := i + 1; !on && next < l.Len() && next != l.ActiveIndex() {
					item.DrawOver(func(p *ui.Painter, r ui.Rect) {
						p.Fill(ui.Rect{X: r.X + r.W - 1, Y: r.Y + r.H/2 - 9, W: 1, H: 18}, t.Border, 0)
					})
				}
				if on && focusedGroup && a.several() {
					// The tab the keys go to, of several groups.
					item.DrawOver(func(p *ui.Painter, r ui.Rect) {
						p.Fill(ui.Rect{X: r.X, Y: r.Y, W: r.W, H: 2}, t.Accent, 0)
					})
				}
				// A tab dropped on another takes its place, which the side it
				// comes from shows; from another group, it goes before it.
				dragged, over := ui.DragOver[tabDrag](item)
				over = over && dragged.tab != tb && (!l.Contains(dragged.tab) || l.Pinned(dragged.tab) == pinned)
				if d, ok := ui.Drop[tabDrag](item); ok {
					dropped, droppedAt = &d, i
				}
				item.HandleInput(func(ev ui.InputEvent) bool { return a.closeOnMiddlePress(w, tb, ev) })
				item.Children(func() {
					if over {
						bar := ui.Box(c).Absolute().Top(0).Bottom(0).Width(2).Background(t.Accent)
						if at := l.Index(dragged.tab); at < 0 || at > i {
							bar.Left(0)
						} else {
							bar.Right(0)
						}
					}
					ui.Row(c).Gap(7).Grow(1).Children(func() {
						if len(groups) > 0 {
							tabDots(c, groups)
						}
						ic := widgets.IconCode
						if i, ok := tb.(widgets.Iconed); ok {
							ic = i.Icon()
						}
						ui.Icon(c, ic).FontSize(12).TextColor(pal.Muted)
						ui.Column(c).Shrink(1).Children(func() {
							// The one in front in the text's color, the others
							// muted: the weight stays, so no tab moves as
							// another comes in front.
							txt := ui.Text(c, tb.Title()).SingleLine().FontSize(12.5).FontWeight(500)
							if !on && !hovered {
								txt.TextColor(t.Text.Alpha(0.7))
							}
							if sub := placesSubtitle(groups); sub != "" && titles[tb.Title()] > 1 {
								// Under the title, and fainter until connected,
								// as the sidebar's.
								col := pal.Muted
								if !anyConnected(groups) {
									col = col.Alpha(0.6)
								}
								ui.Text(c, sub).SingleLine().FontSize(10.5).TextColor(col)
							}
						})
						var r string
						if m, ok := tb.(widgets.Marked); ok {
							r = m.Mark()
						} else {
							r = tb.CloseReason()
						}
						if r != "" {
							ui.Box(c).Size(6, 6).Radius(3).Background(t.Warning).Tooltip(r)
						}
						if pinned {
							pin := ui.ButtonBase(c).Padding(3).Radius(4).Label("Unpin " + tb.Title()).Tooltip("Pinned: click to unpin")
							if pin.Hovered() {
								pin.Background(pal.Hover)
							}
							pin.Children(func() {
								ui.Icon(c, widgets.IconPin).FontSize(11).TextColor(pal.Muted).ContentTransition(ui.ContentTransition{})
							})
							if pin.Clicked() {
								a.Post(func() { l.SetPinned(tb, false) }) // after the tabs are drawn
							}
							return
						}
						x := ui.ButtonBase(c).Padding(3).Radius(4).Label("Close " + tb.Title())
						// A file not saved yet shows a dot where the button
						// is, the button itself once pointed at.
						unsaved := false
						if u, ok := tb.(interface{ Unsaved() bool }); ok && u.Unsaved() && !hovered {
							unsaved = true
							x.Tooltip("Not saved yet")
						}
						if !on && !hovered && !unsaved {
							x.Opacity(0)
						}
						if x.Hovered() {
							x.Background(pal.Hover)
						}
						x.Children(func() {
							if unsaved {
								ui.Box(c).Size(8, 8).Margin(1.5).Radius(4).Background(t.Text)
							} else {
								ui.Icon(c, widgets.IconX).FontSize(11).ContentTransition(ui.ContentTransition{})
							}
						})
						if x.Clicked() {
							closed = tb
						}
					})
				})
				if item.Clicked() {
					a.chooseTab(g, i)
				}
				item.ContextMenu(func(m *ui.Menu) { a.tabMenu(m, w, g, tb, i, on, &closed) })
			}
		})
	})
	if g.TabScroll.MaxX > 0 {
		widgets.MenuOn(c, widgets.IconButton(c, widgets.IconDropdown, "Open Tabs"), func(m *widgets.Menu) {
			for i, tb := range l.All() {
				label := tb.Title()
				if sub := tabSubtitle(tb); sub != "" {
					label += " — " + sub
				}
				if m.Item(label).Checked(i == l.ActiveIndex()).Chosen() {
					a.chooseTab(g, i)
					g.RevealTab = true
				}
			}
		})
	}
	if dropped != nil {
		d, at := *dropped, droppedAt
		a.Post(func() { a.moveTabToGroup(w, d.tab, g, at) })
	}
	if closed != nil {
		a.closeTabOf(closed)
	}
}

// tabMotion has a tab grow in as it opens and close up as it goes, its
// neighbours sliding over. Its colors change at once: tabs change places
// in front often, by keys as much as clicks.
var tabMotion = ui.ElementTransition{
	Position: true,
	Size:     true,
	Enter:    &ui.Motion{Collapse: true},
	Exit:     &ui.Motion{Collapse: true},
	Duration: 150 * time.Millisecond,
}

// tabDots are a tab's connections, problems first: a dot each, up to
// maxChips, then how many more. A failure says why inside the tab.
func tabDots(c *ui.Context, groups []connPlaces) {
	ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
		for _, g := range groups[:min(maxChips, len(groups))] {
			widgets.ConnDot(c, g.conn)
		}
		if n := len(groups) - maxChips; n > 0 {
			ui.Text(c, fmt.Sprintf("+%d", n)).FontSize(10).TextColor(widgets.PaletteOf(c).Muted)
		}
	})
}

// middleButton is the pointer's middle button, as input events number it.
const middleButton = 2

// closeOnMiddlePress closes a tab of w pressed with the middle button,
// but a pinned one, and reports whether it took the press.
func (a *App) closeOnMiddlePress(w *window, tb widgets.Tab, ev ui.InputEvent) bool {
	if ev.Kind != ui.InputPointerDown || ev.Button != middleButton || w.pinned(tb) {
		return false
	}
	a.Post(func() { a.closeTabOf(tb) })
	return true
}

// tabDrag is a tab dragged along a tab bar, to another group's, or to a
// side of a group.
type tabDrag struct{ tab widgets.Tab }

// tabMenu is a tab's menu; Close sets closed to the tab.
func (a *App) tabMenu(m *ui.Menu, w *window, g *panes.Group, tb widgets.Tab, i int, on bool, closed *widgets.Tab) {
	if q, ok := tb.(*query.Tab); ok && q.Path != "" && m.Item("Rename…").Chosen() {
		a.askRename(q.Conn.Project, q.Path)
	}
	if q, ok := tb.(*query.Tab); ok && m.Item("Duplicate").Chosen() {
		a.Post(func() { a.duplicateQuery(q) })
	}
	pinned := g.Tabs.Pinned(tb)
	pin := "Pin"
	if pinned {
		pin = "Unpin"
	}
	if m.Item(pin).Chosen() {
		a.Post(func() { g.Tabs.SetPinned(tb, !pinned) }) // after the tabs are drawn
	}
	if m.Item("Move to New Window").Disabled(a.window != a.main && w.tabCount() == 1).Chosen() {
		a.Post(func() { a.moveToWindow(tb, nil) })
	}
	if a.window != a.main && m.Item("Move to Main Window").Chosen() {
		a.Post(func() { a.moveToWindow(tb, a.main) })
	}
	m.Separator()
	_, isQuery := tb.(*query.Tab)
	for _, s := range []struct {
		label, id string
		d         panes.Direction
	}{{"Split Right", keymap.SplitRight, panes.Right}, {"Split Down", keymap.SplitDown, panes.Down}} {
		if keymap.Item(m.Item(s.label), s.id).Disabled(g.Tabs.Len() == 1 && !isQuery).Chosen() {
			a.Post(func() {
				g.Tabs.Select(tb)
				a.splitTab(w, g, s.d)
			})
		}
	}
	if groups := w.panes.Groups(); len(groups) > 1 {
		m.Submenu("Move to Group", func(m *ui.Menu) {
			for n, to := range groups {
				label := fmt.Sprintf("Group %d", n+1)
				if front := to.Tabs.Active(); front != nil {
					label += " — " + front.Title()
				}
				if m.Item(label).Disabled(to == g).Chosen() {
					a.Post(func() { a.moveTabToGroup(w, tb, to, to.Tabs.Len()) })
				}
			}
		})
		if keymap.Item(m.Item("Close Group"), keymap.ClosePane).Chosen() {
			a.Post(func() { a.closeGroup(w, g) })
		}
	}
	m.Separator()
	if keymap.Item(m.Item("Close"), keymap.CloseTab).Chosen() {
		*closed = tb
	}
	for _, bulk := range bulkCloses(tb, i) {
		if m.Item(bulk.label).Disabled(!anyUnpinned(g, bulk.which)).Chosen() {
			a.closeTabs(g, bulk.which)
		}
	}
	m.Separator()
	if keymap.Item(m.Item("Reopen Closed Tab"), keymap.ReopenTab).Disabled(len(a.closedTabs) == 0).Chosen() {
		a.Post(a.reopenTab)
	}
}

// bulkClose is a command closing several tabs of a group: those which
// says, of those not pinned.
type bulkClose struct {
	label, command string // in a tab's menu, in the palette
	which          func(i int, t widgets.Tab) bool
}

// bulkCloses are the commands closing others than tab tb, at i, those to
// its right, or all.
func bulkCloses(tb widgets.Tab, i int) []bulkClose {
	return []bulkClose{
		{"Close Others", "Close Other Tabs", func(_ int, t widgets.Tab) bool { return t != tb }},
		{"Close to the Right", "Close Tabs to the Right", func(j int, _ widgets.Tab) bool { return j > i }},
		{"Close All", "Close All Tabs", func(int, widgets.Tab) bool { return true }},
	}
}

// anyUnpinned reports whether a tab of a group not pinned is one which
// says.
func anyUnpinned(g *panes.Group, which func(i int, t widgets.Tab) bool) bool {
	for j, t := range g.Tabs.All() {
		if !g.Tabs.Pinned(t) && which(j, t) {
			return true
		}
	}
	return false
}

// closeTabs closes the tabs of a group which says, but the pinned ones,
// from the right.
func (a *App) closeTabs(g *panes.Group, which func(i int, t widgets.Tab) bool) {
	a.Post(func() {
		var tabs []widgets.Tab
		for j, t := range slices.Backward(g.Tabs.Tabs()) {
			if !g.Tabs.Pinned(t) && which(j, t) {
				tabs = append(tabs, t)
			}
		}
		a.closeTogether(tabs)
	})
}

// closeTogether closes tabs, the first first, once the user agrees to what
// closing them loses, asked once for them all, and at once when none
// loses anything; one closed meanwhile is skipped. Reopen Closed Tab opens
// them again, the last first.
func (a *App) closeTogether(tabs []widgets.Tab) {
	if len(tabs) == 0 {
		return
	}
	var lost []string
	var txs []txHolder
	for _, t := range tabs {
		if r := t.CloseReason(); r != "" {
			lost = append(lost, t.Title()+": "+r)
		}
		if h, ok := t.(txHolder); ok && h.OpenTx() {
			txs = append(txs, h)
		}
	}
	end := func() {
		for _, t := range tabs {
			if w, g, at, ok := a.removeTab(t); ok {
				t.Close()
				a.rememberClosed(t, w, g, at)
			}
		}
	}
	if len(lost) == 0 {
		end()
		return
	}
	title, reason := "Close "+widgets.Count(len(tabs), "tab")+"?", strings.Join(lost, "\n\n")
	if len(tabs) == 1 {
		title, reason = "Close "+tabs[0].Title()+"?", tabs[0].CloseReason()
	}
	a.closing = &closeRequest{open: true, title: title, reason: reason, onClose: end, txs: txs}
}

func (a *App) welcome(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	t := c.Theme()
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(48, 48).Gap(28).MaxWidth(860).Children(func() {
			ui.Row(c).Gap(16).Children(func() {
				ui.Box(c).Size(56, 56).Radius(14).Center().
					LinearGradient(ui.LinearGradient{From: ui.Hex("#2563eb"), To: ui.Hex("#7c3aed"), Angle: 135}).
					Children(func() { ui.Icon(c, widgets.IconDatabase).FontSize(28).TextColor(ui.RGB(255, 255, 255)) })
				ui.Column(c).Gap(4).Children(func() {
					ui.Text(c, "DGopher").FontSize(28).Bold()
					ui.Text(c, "Dig into your databases.").FontSize(16)
					ui.Text(c, "PostgreSQL, MySQL, ClickHouse, SQLite, DuckDB and Redis: fast, keyboard-first, careful with production.").TextColor(pal.Muted)
				})
			})
			ui.Grid(c).Columns(4).GapX(12).Children(func() {
				card := func(ic *ui.SVG, title, detail, key string, onClick func()) {
					b := ui.ButtonBase(c).Padding(14).Radius(10).Border(1, t.Border).Label(title).AlignItems(ui.Start) // tops in line, whatever their text
					if b.Hovered() {
						b.Background(pal.Hover).Border(1, t.Accent)
					}
					b.Children(func() {
						ui.Column(c).Gap(6).Grow(1).AlignItems(ui.Stretch).Children(func() {
							// The key at the far end of the icon's row, where
							// it takes no room from the title.
							ui.Row(c).Height(22).AlignItems(ui.Center).Children(func() { // one height, keycap or not
								ui.Icon(c, ic).FontSize(18).TextColor(t.Accent)
								ui.Spacer(c)
								if k := keymap.First(key); k != "" {
									widgets.Keycap(c, k)
								}
							})
							ui.Text(c, title).Bold()
							ui.Text(c, detail).FontSize(12).TextColor(pal.Muted)
						})
					})
					if b.Clicked() {
						onClick()
					}
				}
				card(widgets.IconPlus, "New connection", "A server or a file", keymap.NewConnection, func() { a.openConnForm(nil) })
				card(widgets.IconPlug, "Paste a URL", "postgres://, mysql://, redis://…", "", func() {
					a.openConnForm(nil)
					if a.connForm != nil {
						a.connForm.urlFocus = true
					}
				})
				card(widgets.IconLayers, "Try the sample", "A small shop in SQLite, to explore", "", a.openSample)
				card(widgets.IconFolder, "Add a project folder", "A Git repository's connections and .sql files", keymap.AddFolder, a.addExistingProject)
			})
			if !a.settings.TourDone {
				ui.Row(c).Gap(10).AlignItems(ui.Center).Padding(10, 14).Radius(10).Background(t.Accent.Alpha(0.10)).Children(func() {
					ui.Icon(c, widgets.IconWand).TextColor(t.Accent).FontSize(16)
					ui.Text(c, "New here? A one-minute tour shows how DGopher works.").Grow(1).Shrink(1)
					if ui.PrimaryButton(c, "Take the Tour").Clicked() {
						a.startTour()
					}
				})
			}
			if len(a.conns) > 0 {
				ui.Column(c).Gap(8).Children(func() {
					ui.Text(c, "Connections").Bold().TextColor(pal.Muted).FontSize(12)
					ui.Grid(c).Columns(2).GapX(10).GapY(8).Children(func() {
						for _, cn := range a.conns {
							row := ui.ButtonBase(c.Key("welcome-"+cn.Config.ID)).Padding(10, 14).Radius(8).Border(1, t.Border).Label("Open " + cn.Config.Name)
							if row.Hovered() {
								row.Background(pal.Hover)
							}
							row.Children(func() {
								ui.Row(c).Gap(10).Grow(1).Children(func() {
									ui.Box(c).Size(4, 32).Radius(2).Background(widgets.EnvColor(&cn.Config))
									ui.Icon(c, widgets.IconDatabase).TextColor(widgets.EngineColor(cn.Config.Engine)).FontSize(18)
									ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
										ui.Text(c, cn.Config.Name).Bold().SingleLine()
										ui.Text(c, connSummary(&cn.Config)).FontSize(12).TextColor(pal.Muted).SingleLine()
									})
									if cn.Config.ReadOnly {
										ui.Icon(c, widgets.IconLock).FontSize(12).TextColor(pal.Muted)
									}
								})
							})
							if row.Clicked() {
								a.NewQueryTab(cn, "", "") // the key browser for a key-value engine
							}
						}
					})
				})
			}
			ui.Row(c).Gap(16).Wrap().Children(func() {
				tip := func(keys, what string) {
					if keys == "" {
						return // its key taken away
					}
					ui.Row(c).Gap(6).Children(func() {
						widgets.Keycap(c, keys)
						ui.Text(c, what).FontSize(12.5).TextColor(pal.Muted)
					})
				}
				tip(keymap.First(keymap.Palette), "every command")
				tip(keymap.First(keymap.OpenTable), "open a table")
				tip(keymap.First(keymap.Run), "run")
				if ui.Link(c, keymap.Hint("All shortcuts", keymap.ShortcutsList), "").FontSize(12.5).Clicked() {
					a.shortcutsOpen = true
				}
			})
		})
	})
}

func connSummary(cfg *db.Config) string {
	if cfg.Engine.IsFile() {
		return cfg.Engine.Label() + " · " + cfg.Database
	}
	s := cfg.Engine.Label() + " · "
	if cfg.User != "" {
		s += cfg.User + "@"
	}
	s += cfg.Host
	if cfg.Port > 0 {
		s += fmt.Sprintf(":%d", cfg.Port)
	}
	if cfg.Database != "" {
		s += "/" + cfg.Database
	}
	switch cfg.Redis.Mode {
	case db.RedisCluster:
		s += " (cluster)"
	case db.RedisSentinel:
		s += " (sentinel master " + cfg.Redis.Master + ")"
	}
	if cfg.SSH.Enabled {
		s += " via " + cfg.SSH.Host
	}
	return s
}

// statusBar is the window's edge under the work: the thin line of the
// app's work going on. What a tab runs on is in its group's own status
// bar (tabStatus).
func (a *App) statusBar(c *ui.Context) {
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	// Its top edge is the app's one sign of work going on: a thin bar,
	// moving, once work has lasted a moment, in place of the line drawn
	// there otherwise, so that nothing moves.
	busy := a.busy()
	switch {
	case !busy:
		a.busySince = time.Time{}
	case a.busySince.IsZero():
		a.busySince = time.Now()
	}
	// Once up, it stays a moment: work ending just after it showed would
	// otherwise flash it.
	show := busy && widgets.Lasted(c, a.busySince)
	switch {
	case show:
		if a.busyShown.IsZero() {
			a.busyShown = time.Now()
		}
	case !a.busyShown.IsZero():
		switch left := widgets.MinShown - time.Since(a.busyShown); {
		case busy:
			show = true // work began again while it was up: it stays
		case left > 0:
			show = true
			c.After(left)
		default:
			a.busyShown = time.Time{}
		}
	}
	if show {
		widgets.ActivityBar(c)
	} else {
		ui.Box(c).Height(2).Background(pal.Sidebar).BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
	}
}

// tabStatus is a tab's own status bar, under it, in its group: where it
// runs, how, and its transaction; each group's describes its own tab, as
// a split shows several.
func (a *App) tabStatus(c *ui.Context, tab widgets.Tab) {
	a.statusOf(c, tab, byConnection(tabPlaces(tab)))
}

// statusOf is a status bar of the connections of groups, and the
// transaction of tab, nil for none.
func (a *App) statusOf(c *ui.Context, tab widgets.Tab, groups []connPlaces) {
	pal := widgets.PaletteOf(c)
	th := c.Theme()
	ui.Row(c.Key("tab-status")).Height(24).Padding(0, 10).Gap(10).AlignItems(ui.Center).Clip().
		Background(pal.Sidebar).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
		a.connectionStatus(c, tab, groups)
	})
}

// connectionStatus is the status bar's account of the connection of the
// tab in front, or of the several it runs on.
func (a *App) connectionStatus(c *ui.Context, tab widgets.Tab, groups []connPlaces) {
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	switch len(groups) {
	case 0:
		ui.Text(c, "No connection").FontSize(12).TextColor(pal.Muted)
		ui.Spacer(c)
		return
	case 1:
	default:
		a.connChips(c, tab, groups)
		ui.Spacer(c)
		return
	}
	g := groups[0]
	cn := g.conn
	connBadge(c, cn)
	if w := g.where(); w != "" {
		ui.Text(c, "· "+w).FontSize(12).TextColor(pal.Muted).SingleLine()
	}
	status := "disconnected"
	if !a.Online() && !cn.Config.Engine.IsFile() {
		status = "offline"
	}
	switch cn.Status {
	case connection.StatusConnected:
		status = cn.Config.Engine.Label()
		if cn.Version != "" {
			status += " " + widgets.FirstLine(cn.Version)
		}
		if s := tlsStateOf(cn.Store); s != "" {
			status += ", " + s
		}
	case connection.StatusConnecting:
		status = "connecting…"
	case connection.StatusFailed:
		status = "" // the tab above says why, once
	}
	if status != "" {
		// The first to give way as the group narrows.
		ui.Text(c, status).FontSize(12).TextColor(pal.Muted).SingleLine().Shrink(1).MinWidth(0).ContentTransition(ui.ContentTransition{})
	}
	if cn.Config.ReadOnly {
		ui.Row(c).Gap(4).Children(func() {
			ui.Icon(c, widgets.IconLock).FontSize(11)
			ui.Text(c, "read-only").FontSize(12)
		})
	}
	if cn.Config.SSH.Enabled {
		ui.Text(c, "SSH "+cn.Config.SSH.Host).FontSize(12).TextColor(pal.Muted)
	}
	ui.Spacer(c)
	// After the spacer, Cancel moves nothing as it comes and goes.
	if since, ok := a.connectStarted(cn); ok && widgets.Lasted(c, since) && widgets.Appear(widgets.StatusButton(c.Key("cancel-connect"), "Cancel")).Tooltip("Stop connecting").Clicked() {
		a.CancelConnect(cn)
	}
	switch tb := tab.(type) {
	case *query.Tab:
		switch tb.Tx {
		case db.TxOpen:
			widgets.Appear(ui.Text(c.Key("transaction-state"), "● transaction open").FontSize(12).TextColor(t.Warning).ContentTransition(ui.ContentTransition{}))
		case db.TxFailed:
			widgets.Appear(ui.Text(c.Key("transaction-state"), "● transaction failed").FontSize(12).TextColor(t.Danger).ContentTransition(ui.ContentTransition{}))
		}
	}
	if cn.Config.Engine.Transactions() {
		if cn.Config.ManualCommit() {
			ui.Text(c, "manual commit").FontSize(12).TextColor(pal.Muted).Tooltip("Writes open a transaction that you commit or roll back")
		} else {
			ui.Text(c, "auto-commit").FontSize(12).TextColor(pal.Muted)
		}
	}
}

// maxChips is how many connections the status bar shows of a tab running
// on several, problems first; a button lists the others.
const maxChips = 3

// connChips shows each connection of a tab running on several: its state,
// its environment when staging or production, its name and databases,
// which its menu acts on.
func (a *App) connChips(c *ui.Context, tab widgets.Tab, groups []connPlaces) {
	pal := widgets.PaletteOf(c)
	shown := groups[:min(maxChips, len(groups))]
	for _, g := range shown {
		chip := ui.ButtonBase(c).Padding(1, 4).Radius(4).Label(g.label()).Tooltip(placesTooltip([]connPlaces{g}))
		if chip.Hovered() {
			chip.Background(pal.Hover)
		}
		chip.Children(func() {
			ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
				// Staging and production keep their word, which the safety
				// model leans on; the default environment needs none.
				widgets.ConnState(c, g.conn)
				if env := g.conn.Config.Env; env == db.Staging || env == db.Production {
					envBadge(c, g.conn)
				}
				connName(c, g.conn)
				if w := g.where(); w != "" {
					ui.Text(c, "· "+w).FontSize(12).TextColor(pal.Muted).SingleLine()
				}
			})
		})
		widgets.MenuOn(c, chip, func(m *widgets.Menu) { a.connMenu(m, tab, g) })
	}
	if rest := groups[len(shown):]; len(rest) > 0 {
		label := fmt.Sprintf("+%d more", len(rest))
		more := ui.ButtonBase(c).Padding(1, 6).Radius(4).Label(label).Tooltip(placesTooltip(rest))
		more.Children(func() { ui.Text(c, label).FontSize(12).TextColor(pal.Muted) })
		widgets.MenuOn(c, more, func(m *widgets.Menu) {
			for _, g := range rest {
				m.Submenu(g.label()+" — "+connStateText(g.conn), func(m *widgets.Menu) { a.connMenu(m, tab, g) })
			}
		})
	}
}

// connMenu is a status bar chip's menu: the connection's databases the tab
// runs on, with the dashboard's panels on each, then what connects or
// disconnects it.
func (a *App) connMenu(m *widgets.Menu, tab widgets.Tab, g connPlaces) {
	cn := g.conn
	d, _ := tab.(*dashboard.Tab) // the status bar's own tab, whichever group has the keys
	for _, database := range g.databases {
		label := cmp.Or(database, cn.Config.Database, cn.Config.Name)
		if d != nil {
			label += " — " + strings.Join(d.PanelsOn(connection.Place{Conn: cn, Database: database}), ", ")
		}
		m.Item(label).Disabled(true)
	}
	m.Separator()
	switch cn.Status {
	case connection.StatusConnected:
		if m.Item("Disconnect").Chosen() {
			a.askDisconnect(cn)
		}
	case connection.StatusFailed:
		if m.Item("Retry").Chosen() {
			a.Connect(cn, nil)
		}
		if m.Item("Show Error").Chosen() {
			a.ShowReport(a.connectionReport(cn, nil))
		}
	case connection.StatusIdle:
		if m.Item("Connect").Chosen() {
			a.Connect(cn, nil)
		}
	}
}

// connBadge shows a connection's environment and name.
func connBadge(c *ui.Context, cn *connection.Conn) {
	envBadge(c, cn)
	connName(c, cn)
}

// envBadge names a connection's environment in its color.
func envBadge(c *ui.Context, cn *connection.Conn) {
	ui.Row(c).Gap(5).Padding(1, 8).Radius(4).Background(widgets.LabelFill(widgets.EnvironmentColor(cn.Config.Env))).Children(func() {
		ui.Text(c, cn.Config.Env.Label()).FontSize(11).Bold().TextColor(ui.RGB(255, 255, 255))
	})
}

// connName is a connection's name as the status bar shows it.
func connName(c *ui.Context, cn *connection.Conn) {
	ui.Text(c, cn.Config.Name).FontSize(12).Bold()
}

// tabKey identifies a tab among the elements of the frame.
func tabKey(t widgets.Tab) string { return fmt.Sprintf("tab-%p", t) }

// pendingToast is a toast asked for outside a frame, shown in the next.
type pendingToast struct {
	text   string
	action string
	run    func()
}

// tlsStateOf is whether an open connection uses TLS, as the server
// negotiated it: "TLS", "no TLS", or "" for a file.
func tlsStateOf(s db.Store) string {
	if s == nil {
		return ""
	}
	return s.TLSState()
}
