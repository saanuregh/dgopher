package widgets_test

import (
	"testing"

	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// A pinch's small scales add up to whole sizes, kept within the bounds,
// and a size changed another way starts the pinch over from it.
func TestFontPinch(t *testing.T) {
	var p widgets.FontPinch
	size := float32(13)
	changes := 0
	for range 10 {
		if next, ok := p.Zoom(size, 1.02, 9, 24); ok {
			size = next
			changes++
		}
	}
	// 13 × 1.02¹⁰ is 15.85.
	if size != 16 || changes != 3 {
		t.Fatalf("pinched out to %v in %d changes, want 16 in 3", size, changes)
	}
	if next, _ := p.Zoom(size, 10, 9, 24); next != 24 {
		t.Fatalf("pinched past the largest to %v", next)
	}
	if next, ok := p.Zoom(12, 1.01, 9, 24); ok || next != 12 {
		t.Fatalf("a size set another way pinched to %v, %v; want 12 kept", next, ok)
	}
	if next, ok := p.Zoom(12.5, 1.01, 9, 22); !ok || next != 13 {
		t.Fatalf("the grid's default pinched to %v, %v; want 13", next, ok)
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
