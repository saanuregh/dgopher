package widgets

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// FontPinch turns a touchpad's pinch into whole font sizes: a pinch comes
// as many small scales, each too small to change a rounded size, which it
// adds up.
type FontPinch struct{ size float64 }

// Zoom returns size scaled by scale, rounded and kept between lo and hi,
// and whether that differs from size.
func (p *FontPinch) Zoom(size, scale, lo, hi float32) (float32, bool) {
	if math.Round(p.size) != float64(size) {
		// The first pinch, or the size changed since in another way.
		p.size = float64(size)
	}
	p.size = min(max(p.size*float64(scale), float64(lo)), float64(hi))
	next := float32(math.Round(p.size))
	return next, next != size
}

// WheelZoom turns scrolling with ⌘ (Ctrl elsewhere) held into steps of
// font size, one for a mouse wheel's notch. Scrolling that comes as
// pixels, a touchpad's, or a wheel's as GTK reports it on Wayland, takes
// a step each wheelStep DIPs, which it adds up.
type WheelZoom struct{ rest float32 }

// wheelStep is how far scrolling by pixels goes for a step: about a
// notch, which MyGo scrolls by 50 DIPs, and GTK's on Wayland by 75.
const wheelStep = 50

// Step returns the step ev zooms text by, 1 larger and -1 smaller, and
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
