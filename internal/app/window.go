package app

import (
	"iter"
	"slices"

	"dgopher/internal/ui/tablist"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// window is a window of the app and what it shows: its tabs, the one in
// front, and the one beside it. The main window has the sidebar too; the
// others are tabs moved out of it.
type window struct {
	native *mygo.Window // nil before it is made, as in tests
	title  string       // what the system's window was last titled
	tabs   tablist.List
	// splitW is how wide the left pane is, of a tab shown beside the one
	// in front.
	splitW float32
	// focusWant is where a key asked the keyboard focus to go: "nav",
	// "filter", "editor" or "results". The view that holds that place
	// takes it, and clears it once it has the focus.
	focusWant string
	// tabScroll is the tab bar's, whose overflow shows the list of tabs;
	// revealTab scrolls the tab in front into view once.
	tabScroll ui.ScrollState
	revealTab bool
	// inView are the tabs the window showed in its last frame: in front,
	// and beside it.
	inView [2]widgets.Tab
}

// ActiveTab is the tab in front of the window, nil for none.
func (w *window) ActiveTab() widgets.Tab { return w.tabs.Active() }

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
	now := [2]widgets.Tab{a.ActiveTab(), a.tabs.Side()}
	for _, t := range now {
		if s, ok := t.(widgets.ShownNotifier); ok && !slices.Contains(a.window.inView[:], t) {
			s.Shown()
			c.Invalidate()
		}
	}
	a.window.inView = now
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
			for _, t := range w.tabs.Tabs() {
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
			w.tabs.Select(t)
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
// window and the place it had. A window but the main one goes with its
// last tab, after the frame: the caller may put another in its place, and
// a window closing while it draws would draw no more.
func (a *App) removeTab(t widgets.Tab) (*window, int, bool) {
	for _, w := range a.windows {
		if i, ok := w.tabs.Remove(t); ok {
			if w.tabs.Len() == 0 && w != a.main {
				a.Post(func() {
					if w.tabs.Len() == 0 {
						a.closeWindow(w)
					}
				})
			}
			return w, i, true
		}
	}
	return nil, 0, false
}

// moveToWindow moves a tab to a window, in front there; nil makes a new
// window for it.
func (a *App) moveToWindow(t widgets.Tab, to *window) {
	if !slices.ContainsFunc(a.windows, func(w *window) bool { return w.tabs.Contains(t) }) {
		return // closed since the move was asked
	}
	if to == nil {
		to = a.openWindow()
	}
	from, _, ok := a.removeTab(t)
	if !ok {
		return
	}
	to.tabs.Add(t)
	a.window = to
	if to.native != nil && to != from {
		to.native.Show()
		to.native.Focus()
	}
}

// openWindow makes a window of tabs, empty until a tab moves there.
func (a *App) openWindow() *window {
	w := &window{}
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
	for _, t := range w.tabs.TakeAll() {
		a.main.tabs.Add(t)
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
