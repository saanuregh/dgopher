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
	a.drain()
	a.now = c.Now()
	widgets.ApplyTheme(c, strings.TrimSpace(a.settings.UIFontFamily))
	if a.clipboard == nil {
		a.clipboard, a.readClip = c.WriteClipboard, c.ReadClipboard
	}
	a.shortcuts(c)
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
	if tst := a.toast; tst != nil {
		a.toast = nil
		if tst.action != "" {
			c.ToastAction(tst.text, tst.action, tst.run)
		} else {
			c.Toast(tst.text)
		}
	}
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
	pressed := keymap.Pressed
	if nativeMenuBar {
		pressed = keymap.LaterPressed // the first keys are the menu bar's
	}
	for _, id := range menuCommands {
		if pressed(c, id) {
			a.menuAction(id)()
		}
	}
	for i, k := range []ui.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6, ui.Key7, ui.Key8, ui.Key9} {
		if c.Shortcut(ui.Cmd, k) && a.tabs.Len() > 0 {
			at := i
			if i == 8 {
				at = a.tabs.Len() - 1 // ⌘9 is the last, as in browsers
			}
			a.tabs.SelectAt(at)
			a.focusWant = "editor" // the keys go on in the tab chosen
		}
	}
	switch {
	case keymap.Pressed(c, keymap.FocusNavigator):
		a.layout.SidebarHidden = false
		a.focusWant = "nav"
	case keymap.Pressed(c, keymap.FocusFilter) && a.ActiveTab() != nil:
		a.focusWant = "filter"
	case keymap.Pressed(c, keymap.ShortcutsList):
		a.shortcutsOpen = !a.shortcutsOpen
	case keymap.Pressed(c, keymap.NextTab):
		if a.tabs.Len() > 0 {
			a.tabs.Next()
			a.focusWant = "editor"
		}
	case keymap.Pressed(c, keymap.PreviousTab):
		if a.tabs.Len() > 0 {
			a.tabs.Previous()
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
		if a.tabs.Len() == 0 {
			a.welcome(c)
			return
		}
		at, side := a.ActiveTab(), a.tabs.Side()
		if side == nil {
			a.pane(c, at, false)
		} else {
			left, right := at, side
			if a.tabs.SideLeft() {
				left, right = side, at
			}
			if a.splitW == 0 {
				a.splitW = a.layout.SideWidth
			}
			if ui.Split(c, &a.splitW, func() { a.pane(c, left, true) }, func() { a.pane(c, right, true) }).Grow(1).Changed() {
				a.layout.SideWidth = a.splitW // where the next split starts
			}
		}
		if a.focusWant == "editor" {
			a.focusWant = "" // a tab without an editor keeps the focus where it is
		}
	})
}

// pane shows a tab, under the color of its connection's environment; of
// two side by side, the one in front is marked, and the other comes in
// front as the focus goes into it.
func (a *App) pane(c *ui.Context, t widgets.Tab, split bool) {
	th := c.Theme()
	// Keyed by a string: a pointer as the key loses the focus within.
	col := ui.Column(c.Key(tabKey(t))).Grow(1).Children(func() {
		if cn := t.Connection(); cn != nil {
			// The environment's color all along the top of the work: the
			// first thing to see before typing into production.
			ui.Box(c).FillWidth().Height(3).Background(widgets.SafetyColor(&cn.Config))
		}
		if split {
			band := th.Border
			if t == a.ActiveTab() {
				band = th.Accent
			}
			ui.Box(c).FillWidth().Height(2).Background(band)
		}
		ui.Column(c).Grow(1).Children(func() { t.View(c) })
	})
	if split && t == a.tabs.Side() && col.FocusWithin() {
		a.tabs.BringSide()
	}
}

// chooseTab brings the tab at i in front, as a click on it does: the tab
// shown beside the one in front changes places with it.
func (a *App) chooseTab(i int) {
	if a.tabs.At(i) == a.tabs.Side() {
		a.tabs.BringSide()
	} else {
		a.tabs.SelectAt(i)
	}
}

func (a *App) tabBar(c *ui.Context) {
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	w := a.window
	closed := -1
	var dropped *tabDrag // and where to, after the tabs are drawn
	droppedAt := 0
	height := barHeight(c)
	ui.ScrollHorizontal(c).TrackScroll(&w.tabScroll).Shrink(1).Children(func() {
		ui.Row(c).AlignItems(ui.Stretch).Children(func() {
			for i, tb := range a.tabs.All() {
				on := i == a.tabs.ActiveIndex()
				pinned := w.tabs.Pinned(tb)
				groups := byConnection(tabPlaces(tb))
				item := ui.ButtonBase(c.Key(tabKey(tb))).Padding(0, 6, 0, 12).Height(height).MaxWidth(260).Label(tb.Title()).
					Drag(tabDrag{tb})
				if len(groups) > 0 {
					item.Tooltip(placesTooltip(groups))
				}
				switch {
				case on:
					item.Background(t.Background)
				case tb == a.tabs.Side():
					item.Background(t.Background.Alpha(0.6)) // shown beside the one in front
				case item.Hovered():
					item.Background(pal.Hover)
				}
				if on && w.revealTab {
					item.ScrollIntoView()
					w.revealTab = false
				}
				hovered := item.Hovered()
				item.BorderWidth(0, 1, 0, 0).BorderColor(t.Border)
				// A tab dropped on another takes its place, which the side it
				// comes from shows.
				dragged, over := ui.DragOver[tabDrag](item)
				over = over && dragged.tab != tb && w.tabs.Pinned(dragged.tab) == pinned
				if d, ok := ui.Drop[tabDrag](item); ok {
					dropped, droppedAt = &d, i
				}
				item.HandleInput(func(ev ui.InputEvent) bool { return a.closeOnMiddlePress(w, tb, ev) })
				item.Children(func() {
					if over {
						bar := ui.Box(c).Absolute().Top(0).Bottom(0).Width(2).Background(t.Accent)
						if a.tabs.Index(dragged.tab) > i {
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
							txt := ui.Text(c, tb.Title()).SingleLine().FontSize(12.5)
							if on {
								txt.Bold()
							}
							if sub := tabSubtitle(tb); sub != "" {
								ui.Text(c, sub).SingleLine().FontSize(10.5).TextColor(pal.Muted)
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
								a.Post(func() { w.tabs.SetPinned(tb, false) }) // after the tabs are drawn
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
							closed = i
						}
					})
				})
				if item.Clicked() {
					a.chooseTab(i)
				}
				item.ContextMenu(func(m *ui.Menu) { a.tabMenu(m, w, tb, i, on, &closed) })
			}
		})
	})
	if w.tabScroll.MaxX > 0 {
		widgets.IconButton(c, widgets.IconDropdown, "Open Tabs").Menu(func(m *ui.Menu) {
			for i, tb := range a.tabs.All() {
				label := tb.Title()
				if sub := tabSubtitle(tb); sub != "" {
					label += " — " + sub
				}
				if m.Item(label).Checked(i == a.tabs.ActiveIndex()).Chosen() {
					a.chooseTab(i)
					w.revealTab = true
				}
			}
		})
	}
	if dropped != nil {
		w.tabs.Move(dropped.tab, droppedAt)
	}
	if closed >= 0 {
		a.closeTab(closed)
	}
}

// tabDots are a tab's connections, problems first: a dot each, up to
// maxChips, then how many more, and a warning when the first failed.
func tabDots(c *ui.Context, groups []connPlaces) {
	ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
		for _, g := range groups[:min(maxChips, len(groups))] {
			widgets.ConnDot(c, g.conn)
		}
		if n := len(groups) - maxChips; n > 0 {
			ui.Text(c, fmt.Sprintf("+%d", n)).FontSize(10).TextColor(widgets.PaletteOf(c).Muted)
		}
		if groups[0].conn.Status == connection.StatusFailed {
			ui.Icon(c, widgets.IconAlert).FontSize(10).TextColor(c.Theme().Danger)
		}
	})
}

// middleButton is the pointer's middle button, as input events number it.
const middleButton = 2

// closeOnMiddlePress closes a tab of w pressed with the middle button,
// but a pinned one, and reports whether it took the press.
func (a *App) closeOnMiddlePress(w *window, tb widgets.Tab, ev ui.InputEvent) bool {
	if ev.Kind != ui.InputPointerDown || ev.Button != middleButton || w.tabs.Pinned(tb) {
		return false
	}
	a.Post(func() { a.closeTabOf(tb) })
	return true
}

// tabDrag is a tab dragged along its window's tab bar.
type tabDrag struct{ tab widgets.Tab }

// tabMenu is a tab's menu; Close sets closed to the tab's place.
func (a *App) tabMenu(m *ui.Menu, w *window, tb widgets.Tab, i int, on bool, closed *int) {
	if q, ok := tb.(*query.Tab); ok && q.Path != "" && m.Item("Rename…").Chosen() {
		a.askRename(q.Conn.Project, q.Path)
	}
	if q, ok := tb.(*query.Tab); ok && m.Item("Duplicate").Chosen() {
		a.Post(func() { a.duplicateQuery(q) })
	}
	pinned := w.tabs.Pinned(tb)
	pin := "Pin"
	if pinned {
		pin = "Unpin"
	}
	if m.Item(pin).Chosen() {
		a.Post(func() { w.tabs.SetPinned(tb, !pinned) }) // after the tabs are drawn
	}
	if m.Item("Move to New Window").Disabled(a.window != a.main && a.tabs.Len() == 1).Chosen() {
		a.Post(func() { a.moveToWindow(tb, nil) })
	}
	if a.window != a.main && m.Item("Move to Main Window").Chosen() {
		a.Post(func() { a.moveToWindow(tb, a.main) })
	}
	if m.Item("Open to the Side").Disabled(on || tb == a.tabs.Side()).Chosen() {
		a.tabs.SetSide(tb, false)
	}
	if a.tabs.Side() != nil && m.Item("Close the Side").Chosen() {
		a.tabs.SetSide(nil, false)
	}
	m.Separator()
	if keymap.Item(m.Item("Close"), keymap.CloseTab).Chosen() {
		*closed = i
	}
	for _, bulk := range bulkCloses(tb, i) {
		if m.Item(bulk.label).Disabled(!w.anyUnpinned(bulk.which)).Chosen() {
			a.closeTabs(w, bulk.which)
		}
	}
	m.Separator()
	if keymap.Item(m.Item("Reopen Closed Tab"), keymap.ReopenTab).Disabled(len(a.closedTabs) == 0).Chosen() {
		a.Post(a.reopenTab)
	}
}

// bulkClose is a command closing several tabs: those which says, of
// those not pinned.
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

// anyUnpinned reports whether a tab not pinned is one which says.
func (w *window) anyUnpinned(which func(i int, t widgets.Tab) bool) bool {
	for j, t := range w.tabs.All() {
		if !w.tabs.Pinned(t) && which(j, t) {
			return true
		}
	}
	return false
}

// closeTabs closes the tabs of a window which says, but the pinned ones,
// from the right.
func (a *App) closeTabs(w *window, which func(i int, t widgets.Tab) bool) {
	a.Post(func() {
		var tabs []widgets.Tab
		for j, t := range slices.Backward(w.tabs.Tabs()) {
			if !w.tabs.Pinned(t) && which(j, t) {
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
			if _, at, ok := a.removeTab(t); ok {
				t.Close()
				a.rememberClosed(t, at)
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
				card := func(ic *ui.SVG, title, detail string, onClick func()) {
					b := ui.ButtonBase(c).Padding(14).Radius(10).Border(1, t.Border).Label(title)
					if b.Hovered() {
						b.Background(pal.Hover).Border(1, t.Accent)
					}
					b.Children(func() {
						ui.Column(c).Gap(6).AlignItems(ui.Start).Children(func() {
							ui.Icon(c, ic).FontSize(18).TextColor(t.Accent)
							ui.Text(c, title).Bold()
							ui.Text(c, detail).FontSize(12).TextColor(pal.Muted)
						})
					})
					if b.Clicked() {
						onClick()
					}
				}
				card(widgets.IconPlus, "New connection", keymap.Hint("A server or a file", keymap.NewConnection), func() { a.openConnForm(nil) })
				card(widgets.IconPlug, "Paste a URL", "postgres://, mysql://, redis://…", func() {
					a.openConnForm(nil)
					if a.connForm != nil {
						a.connForm.urlFocus = true
					}
				})
				card(widgets.IconLayers, "Try the sample", "A small shop in SQLite, to explore", a.openSample)
				card(widgets.IconFolder, "Add a project folder", keymap.Hint("A Git repository's connections and .sql files", keymap.AddFolder), a.addExistingProject)
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
								if cn.Config.Engine == db.Redis {
									a.openRedis(cn)
								} else {
									a.NewQueryTab(cn, "", "")
								}
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
						ui.Text(c, keys).Font(widgets.MonoFont).FontSize(11.5).Padding(2, 6).Radius(4).Background(pal.Hover)
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

func (a *App) statusBar(c *ui.Context) {
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	var groups []connPlaces
	if tab := a.ActiveTab(); tab != nil {
		groups = byConnection(tabPlaces(tab))
	} else if cn := a.activeConn(); cn != nil {
		groups = []connPlaces{{conn: cn, databases: []string{""}}}
	}
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
	if busy && widgets.Lasted(c, a.busySince) {
		widgets.ActivityBar(c)
	} else {
		ui.Box(c).Height(2).Background(pal.Sidebar).BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
	}
	ui.Row(c).Height(26).Padding(0, 10).Gap(12).Background(pal.Sidebar).Children(func() {
		a.connectionStatus(c, groups)
	})
}

// connectionStatus is the status bar's account of the connection of the
// tab in front, or of the several it runs on.
func (a *App) connectionStatus(c *ui.Context, groups []connPlaces) {
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	a.txIndicator(c)
	switch len(groups) {
	case 0:
		ui.Text(c, "No connection").FontSize(12).TextColor(pal.Muted)
		ui.Spacer(c)
		return
	case 1:
	default:
		a.connChips(c, groups)
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
	switch cn.Status {
	case connection.StatusConnected:
		status = cn.Config.Engine.Label()
		if cn.Version != "" {
			status += " " + widgets.FirstLine(cn.Version)
		}
		if s := tlsStateOf(cn.DB, cn.KV); s != "" {
			status += ", " + s
		}
	case connection.StatusConnecting:
		status = "connecting…"
	case connection.StatusFailed:
		status = "failed"
	}
	ui.Text(c, status).FontSize(12).TextColor(pal.Muted).SingleLine().Shrink(1).ContentTransition(ui.ContentTransition{})
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
	switch tb := a.ActiveTab().(type) {
	case *query.Tab:
		switch tb.Tx {
		case db.TxOpen:
			widgets.Appear(ui.Text(c.Key("transaction-state"), "● transaction open").FontSize(12).TextColor(t.Warning).ContentTransition(ui.ContentTransition{}))
		case db.TxFailed:
			widgets.Appear(ui.Text(c.Key("transaction-state"), "● transaction failed").FontSize(12).TextColor(t.Danger).ContentTransition(ui.ContentTransition{}))
		}
	}
	mode := "auto-commit"
	if cn.Config.ManualCommit() {
		mode = "manual commit"
	}
	if cn.Config.Engine.IsSQL() && cn.Config.Engine != db.ClickHouse {
		ui.Text(c, mode).FontSize(12).TextColor(pal.Muted)
	}
}

// maxChips is how many connections the status bar shows of a tab running
// on several, problems first; a button lists the others.
const maxChips = 3

// connChips shows each connection of a tab running on several: its state,
// its environment when staging or production, its name and databases,
// which its menu acts on.
func (a *App) connChips(c *ui.Context, groups []connPlaces) {
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
		chip.Menu(func(m *ui.Menu) { a.connMenu(m, g) })
	}
	if rest := groups[len(shown):]; len(rest) > 0 {
		label := fmt.Sprintf("+%d more", len(rest))
		more := ui.ButtonBase(c).Padding(1, 6).Radius(4).Label(label).Tooltip(placesTooltip(rest))
		more.Children(func() { ui.Text(c, label).FontSize(12).TextColor(pal.Muted) })
		more.Menu(func(m *ui.Menu) {
			for _, g := range rest {
				m.Submenu(g.label()+" — "+connStateText(g.conn), func(m *ui.Menu) { a.connMenu(m, g) })
			}
		})
	}
}

// connMenu is a status bar chip's menu: the connection's databases the tab
// runs on, with the dashboard's panels on each, then what connects or
// disconnects it.
func (a *App) connMenu(m *ui.Menu, g connPlaces) {
	cn := g.conn
	d, _ := a.ActiveTab().(*dashboard.Tab)
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
	ui.Row(c).Gap(5).Padding(1, 8).Radius(4).Background(widgets.EnvironmentColor(cn.Config.Env)).Children(func() {
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
func tlsStateOf(sqldb *db.DB, kv *db.KV) string {
	switch {
	case sqldb != nil:
		return sqldb.TLSState()
	case kv != nil:
		return kv.TLSState()
	}
	return ""
}
