package app

import (
	"dgopher/internal/keymap"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// The window's title bar is the top row of the sidebar and that of the
// workspace, under the window controls the system keeps
// (mygo.TitleBarHidden): the same on every system, and every part of it
// does something.

// barHeight is the height of the rows the title bar is made of.
func barHeight(c *ui.Context) float32 { return max(c.TitleBar().Height, 36) }

// sidebarBar is the sidebar's top row: adding a project or a connection,
// and hiding the sidebar.
func (a *App) sidebarBar(c *ui.Context) {
	bar := c.TitleBar()
	ui.Row(c).Height(barHeight(c)).Padding(0, 8, 0, bar.Left+8).Gap(2).DragWindow().Children(func() {
		ui.Spacer(c)
		widgets.IconButton(c, widgets.IconPlus, "New…").Menu(a.addMenu)
		if widgets.IconButton(c, widgets.IconSidebar, "Hide Sidebar").Clicked() {
			a.sidebarHidden = true
		}
	})
}

// workspaceBar is the workspace's top row: the tabs, the search of the
// command palette and the app's menu.
func (a *App) workspaceBar(c *ui.Context) {
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	bar := c.TitleBar()
	hidden := a.window != a.main || a.sidebarHidden
	left := float32(0)
	if hidden {
		left = bar.Left + 8 // the sidebar holds the controls on the left
	}
	ui.Row(c).Height(barHeight(c)).Padding(0, bar.Right+8, 0, left).Gap(2).
		Background(pal.Sidebar).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).DragWindow().Children(func() {
		if hidden && a.window == a.main {
			if widgets.IconButton(c, widgets.IconSidebar, "Show Sidebar").Clicked() {
				a.sidebarHidden = false
			}
		}
		a.tabBar(c)
		ui.Spacer(c)
		search := widgets.IconButton(c, widgets.IconSearch, "Search")
		search.Tooltip("Search commands, tables and files (" + keymap.First(keymap.Palette) + ")")
		if search.Clicked() {
			a.openPalette(false)
		}
		widgets.IconButton(c, widgets.IconMenu, "Menu").Menu(a.titleMenu)
	})
}

// titleMenu is the title bar's menu: the app's menus, then its settings
// and quitting, which macOS's menu bar has in the app's menu.
func (a *App) titleMenu(m *ui.Menu) {
	for _, g := range a.appMenus() {
		m.Submenu(g.label, func(m *ui.Menu) {
			for _, e := range g.items {
				if e.label == "" {
					m.Separator()
				} else if keymap.Item(m.Item(e.label), e.id).Chosen() {
					e.run()
				}
			}
		})
	}
	m.Separator()
	if keymap.Item(m.Item("Settings…"), keymap.Settings).Chosen() {
		a.openSettings()
	}
	if keymap.Item(m.Item("Quit"), keymap.Quit).Chosen() {
		a.menuAction(keymap.Quit)()
	}
}
