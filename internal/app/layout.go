package app

import (
	"fmt"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/ui/dataview"
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
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	if a.clipboard == nil {
		a.clipboard, a.readClip = c.WriteClipboard, c.ReadClipboard
	}
	a.shortcuts(c)
	root := ui.Column(c).Fill()
	defer a.dropZone(c, root)
	root.Children(func() {
		if a.sidebarHidden {
			ui.Column(c).Grow(1).Children(func() { a.workspace(c) })
		} else if ui.Split(c, &a.settings.SidebarWidth, func() { a.sidebar(c) }, func() { a.workspace(c) }).Grow(1).Changed() {
			a.settingsDirty = true
		}
		a.statusBar(c)
	})
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
	query.DialogsView(a, c)
	dataview.DialogsView(a, c)
	a.checkIdleTransactions(c)
	if a.idleWarn != nil {
		a.idleWarningView(c)
	}
	if a.shortcutsOpen {
		a.shortcutsView(c)
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
	a.saveWorkspace(false)
	if a.settingsDirty && !c.Root().Dragging() {
		a.settingsDirty = false
		a.SaveSettings()
	}
	_, _ = t, pal
}

// shortcuts handles the keys of the whole window.
func (a *App) shortcuts(c *ui.Context) {
	// The commands of the menu bar take their keys there (main.go).
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
	case c.Shortcut(ui.Cmd, ui.Key0):
		a.sidebarHidden = false
		a.focusWant = "nav"
	case c.Shortcut(ui.Cmd, ui.KeyL):
		a.focusWant = "filter"
	case c.Shortcut(ui.Cmd, ui.KeySlash):
		a.shortcutsOpen = !a.shortcutsOpen
	case c.Shortcut(ui.Ctrl, ui.KeyTab):
		if len(a.tabs) > 0 {
			a.active = (a.active + 1) % len(a.tabs)
			a.focusWant = "editor"
		}
	case c.Shortcut(ui.Ctrl|ui.Shift, ui.KeyTab):
		if len(a.tabs) > 0 {
			a.active = (a.active + len(a.tabs) - 1) % len(a.tabs)
			a.focusWant = "editor"
		}
	}
}

func (a *App) workspace(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Background(t.Background).Children(func() {
		if len(a.tabs) == 0 {
			a.welcome(c)
			return
		}
		a.tabBar(c)
		at := a.ActiveTab()
		if cn := at.Connection(); cn != nil {
			// The environment's color all along the top of the work: the
			// first thing to see before typing into production.
			ui.Box(c).FillWidth().Height(3).Background(widgets.EnvColor(&cn.Config))
		}
		// Keyed by a string: a pointer as the key loses the focus within.
		ui.Column(c.Key(tabKey(at))).Grow(1).Children(func() { at.View(c) })
		if a.focusWant == "editor" {
			a.focusWant = "" // a tab without an editor keeps the focus where it is
		}
	})
}

func (a *App) tabBar(c *ui.Context) {
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	closed := -1
	ui.ScrollHorizontal(c).FillWidth().Background(pal.Sidebar).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Row(c).AlignItems(ui.Stretch).Children(func() {
			for i, tb := range a.tabs {
				i, tb := i, tb
				on := i == a.active
				cn := tb.Connection()
				item := ui.ButtonBase(c.Key(tabKey(tb))).Padding(0, 6, 0, 12).Height(34).MaxWidth(260).Label(tb.Title())
				if on {
					item.Background(t.Background)
				} else if item.Hovered() {
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
					a.active = i
				}
				item.ContextMenu(func(m *ui.Menu) {
					if q, ok := tb.(*query.Tab); ok && q.Path != "" && m.Item("Rename…").Chosen() {
						a.askRename(q.Conn.Project, q.Path)
					}
					if m.Item("Close").Shortcut(ui.Cmd, ui.KeyW).Chosen() {
						closed = i
					}
					if m.Item("Close Others").Chosen() {
						a.Post(func() {
							for j := len(a.tabs) - 1; j >= 0; j-- {
								if a.tabs[j] != tb {
									a.closeTab(j)
								}
							}
						})
					}
				})
			}
		})
	})
	if closed >= 0 {
		a.closeTab(closed)
	}
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
				card(widgets.IconPlus, "New connection", widgets.KeyLabel("A server or a file (⌘N)"), func() { a.openConnForm(nil) })
				card(widgets.IconPlug, "Paste a URL", "postgres://, mysql://, redis://…", func() {
					a.openConnForm(nil)
					if a.connForm != nil {
						a.connForm.urlFocus = true
					}
				})
				card(widgets.IconLayers, "Try the sample", "A small shop in SQLite, to explore", a.openSample)
				card(widgets.IconFolder, "Add a project folder", widgets.KeyLabel("A Git repository's connections and .sql files (⌘⇧O)"), a.addExistingProject)
			})
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
					ui.Row(c).Gap(6).Children(func() {
						ui.Text(c, widgets.KeyLabel(keys)).Font(widgets.MonoFont).FontSize(11.5).Padding(2, 6).Radius(4).Background(pal.Hover)
						ui.Text(c, what).FontSize(12.5).TextColor(pal.Muted)
					})
				}
				tip("⌘K", "every command")
				tip("⌘P", "open a table")
				tip("⌘↵", "run")
				if ui.Link(c, widgets.KeyLabel("All shortcuts (⌘/)"), "").FontSize(12.5).Clicked() {
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
		col := widgets.EnvColor(&cn.Config)
		ui.Row(c).Gap(5).Padding(1, 8).Radius(4).Background(col).Children(func() {
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
