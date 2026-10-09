package app

import (
	"iter"
	"slices"

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
	tabs   []widgets.Tab
	active int
	// side is the tab shown beside the active one, nil for none; it shows
	// left of it when sideLeft is set, and the left pane is splitW wide.
	side     widgets.Tab
	sideLeft bool
	splitW   float32
	// focusWant is where a key asked the keyboard focus to go: "nav",
	// "filter", "editor" or "results". The view that holds that place
	// takes it, and clears it once it has the focus.
	focusWant string
}

// ActiveTab is the tab in front of the window, nil for none.
func (w *window) ActiveTab() widgets.Tab {
	if w.active >= 0 && w.active < len(w.tabs) {
		return w.tabs[w.active]
	}
	return nil
}

// remove takes a tab out of the window, and reports the place it had.
func (w *window) remove(t widgets.Tab) (int, bool) {
	i := slices.Index(w.tabs, t)
	if i < 0 {
		return 0, false
	}
	w.tabs = slices.Delete(w.tabs, i, i+1)
	if w.active >= len(w.tabs) || w.active > i {
		w.active--
	}
	w.active = max(0, min(w.active, len(w.tabs)-1))
	if w.side == t {
		w.side = nil
	}
	return i, true
}

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
			for _, t := range slices.Clone(w.tabs) {
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
			w.active = slices.Index(w.tabs, t)
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
		if i, ok := w.remove(t); ok {
			if len(w.tabs) == 0 && w != a.main {
				a.Post(func() {
					if len(w.tabs) == 0 {
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
	if !slices.ContainsFunc(a.windows, func(w *window) bool { return slices.Contains(w.tabs, t) }) {
		return // closed since the move was asked
	}
	if to == nil {
		to = a.openWindow()
	}
	from, _, ok := a.removeTab(t)
	if !ok {
		return
	}
	to.tabs = append(to.tabs, t)
	to.active = len(to.tabs) - 1
	a.window = to
	if to.native != nil && to != from {
		to.native.Show()
		to.native.Focus()
	}
}

// openWindow makes a window of tabs, empty until a tab moves there.
func (a *App) openWindow() *window {
	w := &window{active: -1}
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
	if len(w.tabs) > 0 {
		a.main.tabs = append(a.main.tabs, w.tabs...)
		a.main.active = len(a.main.tabs) - 1
		w.tabs = nil
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
