package app

import (
	"slices"

	"dgopher/internal/keymap"

	"github.com/egoist/mygo/ui"
)

// The window's zoom is the whole window's, as a browser's or VS Code's:
// everything in it larger or smaller, laid out again at that size, by its
// keys, a pinch, or the wheel with ⌘ held. The text of the editors and
// the grids alone has its sizes in the settings.

// zoomSteps are the zooms the keys step through, as a browser's.
var zoomSteps = []float32{0.5, 0.67, 0.75, 0.8, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2, 2.5, 3}

// zoom is the windows' zoom, 1 when it was never set.
func (a *App) zoom() float32 {
	if a.layout.Zoom <= 0 {
		return 1
	}
	return a.layout.Zoom
}

// stepZoom sets the zoom a step larger or smaller, or back to 1 for no
// step, and keeps it in the layout for the next run.
func (a *App) stepZoom(step int) {
	next := float32(1)
	if step != 0 {
		// The step next to the zoom now, which a layout written by hand
		// may have between two.
		at, _ := slices.BinarySearch(zoomSteps, a.zoom())
		if step < 0 || at < len(zoomSteps) && zoomSteps[at] != a.zoom() {
			at--
		}
		if step > 0 {
			at++
		}
		next = zoomSteps[min(max(at, 0), len(zoomSteps)-1)]
	}
	a.layout.Zoom = next
}

// ZoomInput zooms the window by a pinch, or by scrolling with ⌘ held, and
// reports whether ev was one: the window's own input, and that of the
// views that take theirs, which a pinch does not go past.
func (a *App) ZoomInput(ev ui.InputEvent) bool {
	step, took := a.zoomSteps.Step(ev)
	if step != 0 {
		a.stepZoom(step)
	}
	return took
}

// zoomAction is what a zoom command does, nil for another command.
func (a *App) zoomAction(id string) func() {
	switch id {
	case keymap.ZoomIn:
		return func() { a.stepZoom(1) }
	case keymap.ZoomOut:
		return func() { a.stepZoom(-1) }
	case keymap.ZoomReset:
		return func() { a.stepZoom(0) }
	}
	return nil
}
