package app

import (
	"iter"
	"slices"

	"dgopher/internal/keymap"
	"dgopher/internal/ui/panes"
	"dgopher/internal/ui/tablist"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// window is a window of the app and what it shows: its groups of tabs,
// one of them focused. The main window has the sidebar too; the others
// are tabs moved out of it.
type window struct {
	native *mygo.Window // nil before it is made, as in tests
	title  string       // what the system's window was last titled
	// panes are its groups of tabs, as a tree of splits.
	panes *panes.Layout
	// focusWant is where a key asked the keyboard focus to go: "nav",
	// "filter", "editor" or "results". The view that holds that place
	// takes it, and clears it once it has the focus.
	focusWant string
	// several is whether the frame shows more than one group, decided as
	// it begins: a menu's command splitting the window mid-frame shows in
	// the next, so that the title bar and the groups agree on who draws
	// which tab bar.
	several bool
}

func newWindow() *window { return &window{panes: panes.New()} }

// tabs are the tabs of the focused group, which keys and commands act
// on.
func (w *window) tabs() *tablist.List { return &w.panes.Focused().Tabs }

// ActiveTab is the tab in front of the focused group, nil for none.
func (w *window) ActiveTab() widgets.Tab { return w.tabs().Active() }

// allTabs are the tabs of every group of the window.
func (w *window) allTabs() []widgets.Tab { return w.panes.Tabs() }

// holds reports whether a group of the window holds a tab.
func (w *window) holds(t widgets.Tab) bool { return w.panes.GroupOf(t) != nil }

// pinned reports whether a tab of the window is pinned in its group.
func (w *window) pinned(t widgets.Tab) bool {
	g := w.panes.GroupOf(t)
	return g != nil && g.Tabs.Pinned(t)
}

// tabCount counts the tabs of every group.
func (w *window) tabCount() int { return len(w.allTabs()) }

// windowView draws a window. What was posted runs first with the focused
// window current, so that a tab it opens shows where the user is.
func (a *App) windowView(w *window) func(*ui.Context) {
	return func(c *ui.Context) {
		a.window = a.focusedWindow()
		a.drain()
		a.window, a.drawing = w, w
		a.view(c)
		a.drawing = nil
	}
}

// noteShown tells the tabs that came into view since the window's last
// frame that they did (widgets.ShownNotifier), and draws again after.
func (a *App) noteShown(c *ui.Context) {
	for _, g := range a.window.panes.Groups() {
		t := g.Tabs.Active()
		if s, ok := t.(widgets.ShownNotifier); ok && g.InView != t {
			s.Shown()
			c.Invalidate()
		}
		g.InView = t
	}
}

// focusedWindow is the window the user last used.
func (a *App) focusedWindow() *window {
	if a.lastFocused != nil && slices.Contains(a.windows, a.lastFocused) {
		return a.lastFocused
	}
	return a.main
}

// everyTab yields the tabs of every window, with their window.
func (a *App) everyTab() iter.Seq2[*window, widgets.Tab] {
	return func(yield func(*window, widgets.Tab) bool) {
		for _, w := range slices.Clone(a.windows) {
			for _, t := range w.allTabs() {
				if !yield(w, t) {
					return
				}
			}
		}
	}
}

// findTab brings forward the first tab of any window match accepts, in
// its window, which comes to the front: it reports the tab, and whether
// there was one.
func (a *App) findTab(match func(widgets.Tab) bool) (widgets.Tab, bool) {
	for w, t := range a.everyTab() {
		if match(t) {
			g := w.panes.GroupOf(t)
			g.Tabs.Select(t)
			w.panes.Focus(g)
			if a.drawing == nil {
				a.window = w
			}
			if w.native != nil && w != a.focusedWindow() {
				w.native.Show()
				w.native.Focus()
			}
			return t, true
		}
	}
	return nil, false
}

// removeTab takes a tab out of whichever window holds it, and reports the
// window, the group and the place it had there. A group but the last goes
// with its last tab, and a window but the main one with its last tab,
// after the frame: the caller may put another in its place, and a window
// closing while it draws would draw no more.
func (a *App) removeTab(t widgets.Tab) (*window, *panes.Group, int, bool) {
	for _, w := range a.windows {
		g := w.panes.GroupOf(t)
		if g == nil {
			continue
		}
		i, _ := g.Tabs.Remove(t)
		if g.Tabs.Len() == 0 {
			a.Post(func() { a.dropEmptyGroup(w, g) })
		}
		return w, g, i, true
	}
	return nil, nil, 0, false
}

// dropEmptyGroup takes away a group left without tabs, and a window but
// the main one left without any.
func (a *App) dropEmptyGroup(w *window, g *panes.Group) {
	if g.Tabs.Len() > 0 || !slices.Contains(a.windows, w) {
		return
	}
	if w.panes.Len() > 1 {
		w.panes.Remove(g)
		return
	}
	if w != a.main && w.tabCount() == 0 {
		a.closeWindow(w)
	}
}

// moveToWindow moves a tab to a window, in front there; nil makes a new
// window for it.
func (a *App) moveToWindow(t widgets.Tab, to *window) {
	if !slices.ContainsFunc(a.windows, func(w *window) bool { return w.holds(t) }) {
		return // closed since the move was asked
	}
	if to == nil {
		to = a.openWindow()
	}
	from, _, _, ok := a.removeTab(t)
	if !ok {
		return
	}
	to.tabs().Add(t)
	a.window = to
	if to.native != nil && to != from {
		to.native.Show()
		to.native.Focus()
	}
}

// tearOut moves a tab let go at, past the edge of the window from, to a
// new window under the pointer, as in a browser. The only tab of a window
// other than the main one stays, as Move to New Window leaves it.
func (a *App) tearOut(from *window, t widgets.Tab, at ui.Point) {
	if from != a.main && from.tabCount() == 1 {
		return
	}
	a.moveToWindow(t, nil)
	to := a.window
	if to == from || !to.holds(t) || from.native == nil || to.native == nil {
		return // closed since, or no system windows, as in tests
	}
	// The pointer over the new window's tab bar, where it let go. Where
	// the system places windows itself, as Wayland does, it stays put.
	b := from.native.ContentBounds()
	to.native.SetPosition(b.X+int(at.X)-tearOutGrab, b.Y+int(at.Y)-titleBarHeight/2)
}

// tearOutGrab is how far right of a new window's left edge a tab torn out
// of another lands under the pointer.
const tearOutGrab = 120

// outsideWindow reports whether at, in the window's coordinates, is past
// its edge.
func outsideWindow(c *ui.Context, at ui.Point) bool {
	w, h := c.Size()
	return at.X < 0 || at.Y < 0 || at.X >= w || at.Y >= h
}

// openWindow makes a window of tabs, empty until a tab moves there.
func (a *App) openWindow() *window {
	w := newWindow()
	a.windows = append(a.windows, w)
	if a.makeWindow != nil {
		a.makeWindow(w)
	}
	return w
}

// closeWindow takes a window away. Its tabs go back to the main window,
// unclosed: nothing in them is lost, as an open transaction.
func (a *App) closeWindow(w *window) {
	if w == a.main || !slices.Contains(a.windows, w) {
		return
	}
	a.windows = slices.DeleteFunc(a.windows, func(o *window) bool { return o == w })
	keymap.Forget(w)
	for _, g := range w.panes.Groups() {
		for _, t := range g.Tabs.TakeAll() {
			a.main.tabs().Add(t)
		}
	}
	if a.window == w {
		a.window = a.main
	}
	if w.native != nil {
		native := w.native
		w.native = nil
		native.Close()
	}
}

// anyFocused reports whether a window of the app has the keyboard focus.
func (a *App) anyFocused() bool {
	return slices.ContainsFunc(a.windows, func(w *window) bool { return w.native != nil && w.native.IsFocused() })
}

// invalidate has every window drawn again.
func (a *App) invalidate() {
	for _, w := range a.windows {
		if w.native != nil {
			w.native.Invalidate()
		}
	}
}
