package widgets

import "github.com/egoist/mygo/ui"

// ZoomSteps turns what zooms a window by gesture, a touchpad's pinch and
// scrolling with ⌘ (Ctrl elsewhere) held, into steps of zoom. A pinch
// comes as many small scales, which it multiplies until they make a step.
type ZoomSteps struct {
	wheel WheelZoom
	pinch float64
}

// pinchStep is how far a pinch goes for a step of zoom.
const pinchStep = 1.1

// Step returns the step ev zooms by, 1 larger and -1 smaller, and whether
// ev is a gesture that zooms, even by less than a step.
func (z *ZoomSteps) Step(ev ui.InputEvent) (step int, took bool) {
	if ev.Kind != ui.InputZoom {
		return z.wheel.Step(ev)
	}
	if z.pinch == 0 {
		z.pinch = 1
	}
	z.pinch *= float64(ev.Scale)
	switch {
	case z.pinch >= pinchStep:
		step = 1
	case z.pinch <= 1/pinchStep:
		step = -1
	default:
		return 0, true
	}
	z.pinch = 1
	return step, true
}

// WheelZoom turns scrolling with ⌘ (Ctrl elsewhere) held into steps of
// zoom, one for a mouse wheel's notch. Scrolling that comes as
// pixels, a touchpad's, or a wheel's as GTK reports it on Wayland, takes
// a step each wheelStep DIPs, which it adds up.
type WheelZoom struct{ rest float32 }

// wheelStep is how far scrolling by pixels goes for a step: about a
// notch, which MyGo scrolls by 50 DIPs, and GTK's on Wayland by 75.
const wheelStep = 50

// Step returns the step ev zooms by, 1 larger and -1 smaller, and
// whether ev is scrolling with ⌘ held, which zooms rather than scrolls,
// even by less than a step.
func (w *WheelZoom) Step(ev ui.InputEvent) (int, bool) {
	if ev.Kind != ui.InputScroll || ev.Mods != ui.Cmd {
		return 0, false
	}
	if ev.Momentum {
		return 0, true // a touchpad's glide after the fingers left
	}
	if !ev.Precise {
		w.rest = -ev.DY // a notch: a step of its own
	} else if w.rest -= ev.DY; w.rest > -wheelStep && w.rest < wheelStep {
		return 0, true
	}
	step := 0
	switch {
	case w.rest > 0:
		step = 1
	case w.rest < 0:
		step = -1
	}
	w.rest = 0
	return step, true
}
