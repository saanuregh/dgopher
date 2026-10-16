package widgets

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"
)

// Modal is ui.Modal with the look of DialogLook.
func Modal(c *ui.Context, open *bool, fn func()) ui.Element {
	t := c.Theme()
	w, h := c.Size()
	return ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		panel.Padding(t.Space(5)).Gap(t.Space(3)).Radius(t.Space(2.5)).Background(t.Background).
			MaxWidth(w - t.Space(10)).MaxHeight(h - t.Space(10))
		DialogLook(c, backdrop, panel)
		fn()
	})
}

// DialogLook dims the window behind a dialog and lifts its panel, which
// comes in as comeInto has it.
func DialogLook(c *ui.Context, backdrop, panel ui.Element) {
	frosted(c, backdrop, false)
	dialogShadow(panel)
	comeInto(backdrop, panel)
}

// PickerLook is DialogLook for a picker that drops from the top, as the
// command palette: dimmed less, and there at once, as the key that opens
// it asks.
func PickerLook(c *ui.Context, backdrop, panel ui.Element) {
	frosted(c, backdrop, true)
	backdrop.Justify(ui.Start).PaddingY(80)
	dialogShadow(panel)
}

// frosted dims the window behind a dialog, or a picker, and blurs it, as
// frosted glass: what is behind stays where it was, out of focus, so the
// dialog is the one thing to read. Where the window draws on the CPU,
// which a shader reading the backdrop would cost seconds a frame, it only
// dims.
func frosted(c *ui.Context, backdrop ui.Element, picker bool) {
	col := BackdropOf(c, picker)
	blur := float32(10)
	if picker {
		blur = 6
	}
	backdrop.Draw(func(p *ui.Painter, r ui.Rect) {
		if !p.Accelerated() {
			p.Fill(r, col, 0)
			return
		}
		frost.Draw(p, r, shader.Options{BackdropBlur: blur, Uniforms: map[string]any{"Tint": col}})
	})
}

// frost is the backdrop blurred, under Tint.
var frost = shader.MustCompile(`package main

var Tint vec4

func Fragment(dst vec4, pos vec2) vec4 {
	b := backdropSample(pos)
	return b*(1-Tint.a) + vec4(Tint.rgb*Tint.a, Tint.a)
}
`)

// dialogShadow lifts a dialog off the window: a tight shadow for its edge
// and a wide one for its height, as light falls on a raised sheet.
func dialogShadow(panel ui.Element) {
	panel.Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, 0.12)).Shadow(0, 16, 40, 0, ui.RGBA(0, 0, 0, 0.32))
}

// comeInto has a dialog come in: the window dims as the panel fades in,
// grown from a touch smaller. It is quick, as a dialog often answers a
// key: it shows where the dialog came from, without keeping the hand
// waiting.
func comeInto(backdrop, panel ui.Element) {
	in := panel.Keyframes("in", []ui.Keyframe{{Value: 0}, {At: comeIn, Value: 1, Ease: ui.EaseOut}})
	s := 0.98 + 0.02*in
	backdrop.Opacity(in)
	panel.Scale(s, s)
}

const comeIn = 140 * time.Millisecond
