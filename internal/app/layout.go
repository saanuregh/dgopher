package app

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/ui/dashboard"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/modelview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/redis"
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
		if a.sidebarHidden || a.window != a.main {
			ui.Column(c).Grow(1).Children(func() { a.workspace(c) })
		} else if ui.Split(c, &a.settings.SidebarWidth, func() { a.sidebar(c) }, func() { a.workspace(c) }).Grow(1).Changed() {
			a.settingsDirty = true
		}
		a.statusBar(c)
	})
	a.titleWindow()
	a.checkIdleTransactions(c)
	// The dialogs and toasts show in the window the user uses.
	if a.window == a.focusedWindow() {
		a.overlays(c)
	}
	a.saveWorkspace(false)
	if a.settingsDirty && !c.Root().Dragging() {
		a.settingsDirty = false
		a.SaveSettings()
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
	if a.auditView != nil {
		a.auditViewer(c)
	}
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
		if c.Shortcut(ui.Cmd, k) && len(a.tabs) > 0 {
			a.active = min(i, len(a.tabs)-1) // ⌘9 is the last, as in browsers
			if i == 8 {
				a.active = len(a.tabs) - 1
			}
			a.focusWant = "editor" // the keys go on in the tab chosen
		}
	}
	switch {
	case keymap.Pressed(c, keymap.FocusNavigator):
		a.sidebarHidden = false
		a.focusWant = "nav"
	case keymap.Pressed(c, keymap.FocusFilter) && a.ActiveTab() != nil:
		a.focusWant = "filter"
	case keymap.Pressed(c, keymap.ShortcutsList):
		a.shortcutsOpen = !a.shortcutsOpen
	case keymap.Pressed(c, keymap.NextTab):
		if len(a.tabs) > 0 {
			a.active = (a.active + 1) % len(a.tabs)
			a.focusWant = "editor"
		}
	case keymap.Pressed(c, keymap.PreviousTab):
		if len(a.tabs) > 0 {
			a.active = (a.active + len(a.tabs) - 1) % len(a.tabs)
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
		if len(a.tabs) == 0 {
			a.welcome(c)
			return
		}
		at, side := a.ActiveTab(), a.sideTab()
		if side == nil {
			a.pane(c, at, false)
		} else {
			left, right := at, side
			if a.sideLeft {
				left, right = side, at
			}
			if a.splitW == 0 {
				a.splitW = 560
			}
			ui.Split(c, &a.splitW, func() { a.pane(c, left, true) }, func() { a.pane(c, right, true) }).Grow(1)
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
	if split && t == a.side && col.FocusWithin() {
		a.bringSide()
	}
}

// sideTab is the tab shown beside the active one, nil for none, as one
// closed or brought in front ends the split.
func (a *App) sideTab() widgets.Tab {
	if a.side != nil && (a.side == a.ActiveTab() || !slices.Contains(a.tabs, a.side)) {
		a.side = nil
	}
	return a.side
}

// openToSide shows a tab beside the active one, right of it.
func (a *App) openToSide(t widgets.Tab) {
	if t != a.ActiveTab() {
		a.side, a.sideLeft = t, false
	}
}

// bringSide makes the side tab the active one, which stays where it is:
// the one in front before goes to its side.
func (a *App) bringSide() {
	i := slices.Index(a.tabs, a.side)
	if i < 0 {
		return
	}
	a.side, a.active, a.sideLeft = a.ActiveTab(), i, !a.sideLeft
}

func (a *App) tabBar(c *ui.Context) {
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	closed := -1
	height := barHeight(c)
	ui.ScrollHorizontal(c).Shrink(1).Children(func() {
		ui.Row(c).AlignItems(ui.Stretch).Children(func() {
			for i, tb := range a.tabs {
				i, tb := i, tb
				on := i == a.active
				cn := tb.Connection()
				item := ui.ButtonBase(c.Key(tabKey(tb))).Padding(0, 6, 0, 12).Height(height).MaxWidth(260).Label(tb.Title())
				switch {
				case on:
					item.Background(t.Background)
				case tb == a.side:
					item.Background(t.Background.Alpha(0.6)) // shown beside the one in front
				case item.Hovered():
					item.Background(pal.Hover)
				}
				hovered := item.Hovered()
				item.BorderWidth(0, 1, 0, 0).BorderColor(t.Border)
				item.Children(func() {
					ui.Row(c).Gap(7).Grow(1).Children(func() {
						if cn != nil {
							ui.Box(c).Size(7, 7).Radius(4).Background(widgets.EnvColor(&cn.Config)).Tooltip(cn.Config.Env.Label())
						}
						ic := widgets.IconCode
						switch tb.(type) {
						case *dataview.TableTab:
							ic = widgets.IconTable
						case *redis.Tab:
							ic = widgets.IconKey
						case *dataview.ERTab:
							ic = widgets.IconSchema
						case *activityTab:
							ic = widgets.IconClock
						case *usersTab:
							ic = widgets.IconUsers
						case *dashboard.Tab:
							ic = widgets.IconLayers
						case *modelview.Tab:
							ic = widgets.IconSchema
						}
						ui.Icon(c, ic).FontSize(12).TextColor(pal.Muted)
						txt := ui.Text(c, tb.Title()).SingleLine().Shrink(1).FontSize(12.5)
						if on {
							txt.Bold()
						}
						if tb.CloseReason() != "" {
							ui.Box(c).Size(6, 6).Radius(3).Background(t.Warning).Tooltip("Unsaved changes or an open transaction")
						}
						x := ui.ButtonBase(c).Padding(3).Radius(4).Label("Close " + tb.Title())
						if !on && !hovered {
							x.Opacity(0)
						}
						if x.Hovered() {
							x.Background(pal.Hover)
						}
						x.Children(func() { ui.Icon(c, widgets.IconX).FontSize(11) })
						if x.Clicked() {
							closed = i
						}
					})
				})
				if item.Clicked() {
					if tb == a.side {
						a.bringSide()
					} else {
						a.active = i
					}
				}
				item.ContextMenu(func(m *ui.Menu) {
					if q, ok := tb.(*query.Tab); ok && q.Path != "" && m.Item("Rename…").Chosen() {
						a.askRename(q.Conn.Project, q.Path)
					}
					if m.Item("Move to New Window").Disabled(a.window != a.main && len(a.tabs) == 1).Chosen() {
						a.Post(func() { a.moveToWindow(tb, nil) })
					}
					if a.window != a.main && m.Item("Move to Main Window").Chosen() {
						a.Post(func() { a.moveToWindow(tb, a.main) })
					}
					if m.Item("Open to the Side").Disabled(on || tb == a.side).Chosen() {
						a.openToSide(tb)
					}
					if a.side != nil && m.Item("Close the Side").Chosen() {
						a.side = nil
					}
					m.Separator()
					if keymap.Item(m.Item("Close"), keymap.CloseTab).Chosen() {
						closed = i
					}
					if m.Item("Close Others").Chosen() {
						a.closeOthers(a.window, tb)
					}
				})
			}
		})
	})
	if closed >= 0 {
		a.closeTab(closed)
	}
}

// closeOthers closes a window's tabs but one, each once the user agrees
// to what closing it loses.
func (a *App) closeOthers(w *window, keep widgets.Tab) {
	a.Post(func() {
		for _, t := range slices.Backward(slices.Clone(w.tabs)) {
			if t != keep {
				a.closeTabOf(t)
			}
		}
	})
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
							cn := cn
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
	cn := a.activeConn()
	ui.Row(c).Height(26).Padding(0, 10).Gap(12).Background(pal.Sidebar).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		a.txIndicator(c)
		if cn == nil {
			ui.Text(c, "No connection").FontSize(12).TextColor(pal.Muted)
			ui.Spacer(c)
			return
		}
		ui.Row(c).Gap(5).Padding(1, 8).Radius(4).Background(widgets.EnvironmentColor(cn.Config.Env)).Children(func() {
			ui.Text(c, cn.Config.Env.Label()).FontSize(11).Bold().TextColor(ui.RGB(255, 255, 255))
		})
		ui.Text(c, cn.Config.Name).FontSize(12).Bold()
		status := "disconnected"
		switch cn.Status {
		case connection.StatusConnected:
			status = cn.Config.Engine.Label()
			if cn.Version != "" {
				status += " " + widgets.FirstLine(cn.Version)
			}
		case connection.StatusConnecting:
			status = "connecting…"
		case connection.StatusFailed:
			status = "failed"
		}
		ui.Text(c, status).FontSize(12).TextColor(pal.Muted).SingleLine().Shrink(1)
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
		switch tb := a.ActiveTab().(type) {
		case *query.Tab:
			switch tb.Tx {
			case db.TxOpen:
				ui.Text(c, "● transaction open").FontSize(12).TextColor(t.Warning)
			case db.TxFailed:
				ui.Text(c, "● transaction failed").FontSize(12).TextColor(t.Danger)
			}
			if tb.Running {
				ui.Text(c, "running "+widgets.FormatDuration(time.Since(tb.StartedAt).Round(100*time.Millisecond))).FontSize(12).TextColor(pal.Muted)
			}
		}
		mode := "auto-commit"
		if cn.Config.ManualCommit() {
			mode = "manual commit"
		}
		if cn.Config.Engine.IsSQL() && cn.Config.Engine != db.ClickHouse {
			ui.Text(c, mode).FontSize(12).TextColor(pal.Muted)
		}
	})
}

// tabKey identifies a tab among the elements of the frame.
func tabKey(t widgets.Tab) string { return fmt.Sprintf("tab-%p", t) }

// pendingToast is a toast asked for outside a frame, shown in the next.
type pendingToast struct {
	text   string
	action string
	run    func()
}
