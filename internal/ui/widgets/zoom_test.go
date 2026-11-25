package widgets_test

import (
	"testing"

	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// A pinch's small scales add up to steps, out larger and in smaller, and
// what is left of a step is kept for the next pinch.
func TestZoomStepsOfAPinch(t *testing.T) {
	var z widgets.ZoomSteps
	pinch := func(scale float32) int {
		t.Helper()
		step, took := z.Step(ui.InputEvent{Kind: ui.InputZoom, Scale: scale})
		if !took {
			t.Fatalf("a pinch of %v is not a gesture that zooms", scale)
		}
		return step
	}
	steps := 0
	for range 10 {
		steps += pinch(1.02)
	}
	// 1.02 five times is past 1.1: a step each five.
	if steps != 2 {
		t.Errorf("ten pinches of 1.02 made %d steps, want 2", steps)
	}
	if got := pinch(0.5); got != -1 {
		t.Errorf("a pinch in by half made the step %d, want -1", got)
	}
	if step, took := z.Step(ui.InputEvent{Kind: ui.InputKeyDown}); took || step != 0 {
		t.Errorf("a key is a gesture that zooms: %d, %v", step, took)
	}
}

// Scrolling with ⌘ held zooms, up larger: a notch a step, scrolling by
// pixels a step each 50 DIPs, as GTK's notches of 75 on Wayland; without
// ⌘, or with another modifier too, it scrolls.
func TestWheelZoom(t *testing.T) {
	scroll := func(dy float32, mods ui.Modifiers, precise bool) ui.InputEvent {
		return ui.InputEvent{Kind: ui.InputScroll, DY: dy, Mods: mods, Precise: precise}
	}
	type result struct {
		step int
		ok   bool
	}
	for _, tc := range []struct {
		name string
		evs  []ui.InputEvent
		want []result
	}{
		{"notches", []ui.InputEvent{scroll(-50, ui.Cmd, false), scroll(50, ui.Cmd, false)}, []result{{1, true}, {-1, true}}},
		{"Wayland's notches", []ui.InputEvent{scroll(-75, ui.Cmd, true), scroll(-75, ui.Cmd, true), scroll(75, ui.Cmd, true)}, []result{{1, true}, {1, true}, {-1, true}}},
		{"a touchpad", []ui.InputEvent{scroll(-20, ui.Cmd, true), scroll(-20, ui.Cmd, true), scroll(-20, ui.Cmd, true)}, []result{{0, true}, {0, true}, {1, true}}},
		{"no ⌘", []ui.InputEvent{scroll(-50, 0, false)}, []result{{0, false}}},
		{"⌘⇧", []ui.InputEvent{scroll(-50, ui.Cmd|ui.Shift, false)}, []result{{0, false}}},
		{"a key", []ui.InputEvent{{Kind: ui.InputKeyDown, Mods: ui.Cmd}}, []result{{0, false}}},
	} {
		var w widgets.WheelZoom
		for i, ev := range tc.evs {
			if step, ok := w.Step(ev); (result{step, ok}) != tc.want[i] {
				t.Errorf("%s, event %d: %d, %v; want %v", tc.name, i, step, ok, tc.want[i])
			}
		}
	}
}
