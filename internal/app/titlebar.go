package app

import (
	"runtime"

	"dgopher/internal/keymap"
	"dgopher/internal/project"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/ui"
)

// The window's title bar is the top row of the sidebar and that of the
// workspace, under the window controls the system keeps
// (mygo.TitleBarHidden): the same on every system, and every part of it
// does something.

// titleBarHeight is the height of the title bar's rows, which the window
// controls fill on Windows and are centered in on Linux.
const titleBarHeight = 36

// barHeight is the height of the rows the title bar is made of, never
// less than the room the window controls take.
func barHeight(c *ui.Context) float32 { return max(c.TitleBar().Height, titleBarHeight) }

// sidebarBar is the sidebar's top row: the project, when it is the only
// one, adding a project or a connection, and hiding the sidebar.
func (a *App) sidebarBar(c *ui.Context) {
	bar := c.TitleBar()
	ui.Row(c).Height(barHeight(c)).Padding(0, 8, 0, bar.Left+12).Gap(2).DragWindow().Children(func() {
		if len(a.projects) == 1 {
			a.projectHeader(c, a.projects[0])
		} else {
			// What the tree under it holds, as the sections in it are
			// named: the row is the projects', and so is its New….
			ui.Text(c, "Projects").FontSize(12).FontWeight(600).TextColor(widgets.PaletteOf(c).Muted).
				SingleLine().Grow(1).Shrink(1)
		}
		widgets.MenuOn(c, widgets.IconButton(c, widgets.IconPlus, "New…"), a.addMenu)
		if widgets.IconButton(c, widgets.IconSidebar, "Hide Sidebar").Clicked() {
			a.layout.SidebarHidden = true
		}
	})
}

// workspaceBar is the workspace's top row: the tabs, the search of the
// command palette and the app's menu.
func (a *App) workspaceBar(c *ui.Context) {
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	bar := c.TitleBar()
	hidden := a.window != a.main || a.layout.SidebarHidden
	left := float32(0)
	if hidden {
		left = bar.Left + 8 // the sidebar holds the controls on the left
	}
	right := bar.Right + 8
	if drawsWindowButtons {
		right = 0 // the buttons go to the edge
	}
	ui.Row(c).Height(barHeight(c)).Padding(0, right, 0, left).Gap(2).
		Background(pal.Sidebar).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).DragWindow().Children(func() {
		if hidden && a.window == a.main {
			if widgets.IconButton(c, widgets.IconSidebar, "Show Sidebar").Clicked() {
				a.layout.SidebarHidden = false
			}
		}
		if !a.several() {
			a.tabBar(c, a.window.panes.Focused()) // else each group has its own
			ui.Spacer(c)
		} else {
			// What the keys go to, as each group's bar has the tabs.
			ui.Spacer(c)
			if t := a.ActiveTab(); t != nil {
				title := t.Title()
				if cn := t.Connection(); cn != nil {
					title += " — " + cn.Config.Name
				}
				ui.Text(c, title).FontSize(12.5).TextColor(pal.Muted).SingleLine().Shrink(1)
			}
			ui.Spacer(c)
		}
		// The window's own state: keys of two in a row, and transactions
		// open anywhere.
		if k := keymap.Pending(); k != "" {
			widgets.Keycap(c.Key("pending-key"), k+" …").Tooltip("Waiting for the second key; Esc forgets it")
		}
		a.txIndicator(c)
		search := widgets.IconButton(c, widgets.IconSearch, "Search")
		search.Tooltip("Search commands, tables and files (" + keymap.First(keymap.Palette) + ")")
		if search.Clicked() {
			a.openPalette(false)
		}
		widgets.MenuOn(c, widgets.IconButton(c, widgets.IconMenu, "Menu"), a.titleMenu)
		if drawsWindowButtons {
			ui.Box(c).Width(6)
			a.windowButtons(c)
		}
	})
}

// drawsWindowButtons is whether the app draws the window's minimize,
// maximize and close buttons itself (mygo.WindowOptions.HideWindowButtons):
// on Windows, in its theme's colors, as Zed does. macOS keeps its traffic
// lights, and Linux the desktop's buttons, which follow its button layout.
var drawsWindowButtons = runtime.GOOS == "windows"

// windowButtons draws the window's buttons at the end of its title bar,
// as Windows 11's: as tall as the bar, the close button red under the
// pointer, and the maximize button the one that shows the snap layouts.
func (a *App) windowButtons(c *ui.Context) {
	w := a.window
	maximize, maximizeLabel := windowGlyphMaximize, "Maximize"
	if w.maximized {
		maximize, maximizeLabel = windowGlyphRestore, "Restore"
	}
	if windowButton(c, windowGlyphMinimize, "Minimize", false).Clicked() && w.native != nil {
		w.native.Minimize()
	}
	if windowButton(c, maximize, maximizeLabel, false).MaximizeButton().Clicked() && w.native != nil {
		w.native.ToggleMaximize()
	}
	// As the system's close button: the window's OnClose asks first about
	// what closing would lose.
	if windowButton(c, windowGlyphClose, "Close", true).Clicked() && w.native != nil {
		w.native.Close()
	}
}

// windowGlyph is the symbol of a window button.
type windowGlyph int

const (
	windowGlyphMinimize windowGlyph = iota
	windowGlyphMaximize
	windowGlyphRestore
	windowGlyphClose
)

// windowCloseRed is the background of Windows 11's close button under the
// pointer, whatever the theme.
var windowCloseRed = ui.Hex("#c42b1c")

// windowButton is one of the window's buttons: 46 DIPs wide, its glyph
// drawn in lines 10 DIPs across, as Windows draws its own.
func windowButton(c *ui.Context, glyph windowGlyph, label string, closes bool) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Width(46).Height(barHeight(c)).Label(label).Tooltip(label)
	ink := t.Text
	switch {
	case closes && b.Hovered():
		b.Background(windowCloseRed)
		ink = ui.RGB(255, 255, 255)
	case b.Hovered():
		b.Background(t.Text.Alpha(0.08))
	}
	b.DrawOver(func(p *ui.Painter, r ui.Rect) {
		const size, stroke = 10, 1
		x, y := r.X+(r.W-size)/2, r.Y+(r.H-size)/2
		switch glyph {
		case windowGlyphMinimize:
			p.Line(x, y+size/2, x+size, y+size/2, stroke, ink)
		case windowGlyphMaximize:
			p.Stroke(ui.Rect{X: x, Y: y, W: size, H: size}, ink, 1, stroke)
		case windowGlyphRestore:
			// A window in front of another, behind it to the top right.
			p.Stroke(ui.Rect{X: x, Y: y + 2, W: size - 2, H: size - 2}, ink, 1, stroke)
			p.Line(x+2, y, x+size, y, stroke, ink)
			p.Line(x+size, y, x+size, y+size-2, stroke, ink)
		case windowGlyphClose:
			// Line draws only across or down: the diagonals are a path.
			var cross ui.Path
			cross.MoveTo(x, y).LineTo(x+size, y+size).MoveTo(x+size, y).LineTo(x, y+size)
			p.StrokePath(&cross, stroke, ink)
		}
	})
	return b
}

// titleMenu is the title bar's menu: the app's menus, then its settings,
// updates and quitting, which macOS's menu bar has in the app's menu.
func (a *App) titleMenu(m *widgets.Menu) {
	for _, g := range a.appMenus() {
		m.Submenu(g.label, func(m *widgets.Menu) {
			for _, e := range g.items {
				if e.label == "" {
					m.Separator()
				} else if e.openRecent {
					m.Submenu(e.label, a.recentMenu)
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
	if m.Item("Check for Updates…").Chosen() {
		updater.CheckForUpdates()
	}
	if keymap.Item(m.Item("Quit"), keymap.Quit).Chosen() {
		a.menuAction(keymap.Quit)()
	}
}

// projectHeader names the only project, as its node would, with its menu.
func (a *App) projectHeader(c *ui.Context, p *project.Project) {
	head := ui.Row(c).Gap(6).Grow(1).Shrink(1)
	head.Children(func() { a.projectName(c, p) })
	head.ContextMenu(func(m *ui.Menu) { a.navMenu(m, navNode{kind: nodeProject, projectDir: p.Dir}) })
}

// projectName shows a project's folder and name, muted with why when it
// cannot be read.
func (a *App) projectName(c *ui.Context, p *project.Project) {
	pal := widgets.PaletteOf(c)
	ui.Icon(c, widgets.IconFolder).TextColor(pal.Muted).FontSize(14)
	name := ui.Text(c, a.projectLabel(p)).Bold().SingleLine().Grow(1).Shrink(1).Tooltip(p.Dir)
	if p.Err != "" {
		name.TextColor(pal.Muted)
		ui.Icon(c, widgets.IconAlert).TextColor(c.Theme().Danger).FontSize(12).Tooltip(p.Err)
	}
}
