package widgets

import (
	"time"

	"dgopher/internal/db"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/shader"
)

// The widths of a dialog's content: its panel is wider by its sides'
// padding. DialogWide gives way to a window narrower than it.
const (
	DialogSmall  = 440
	DialogMedium = 640
	DialogLarge  = 900
	DialogWide   = 1100
)

// DialogPad is the room a dialog leaves at the sides of its content, as
// rows of its own that run edge to edge leave too, and at the least
// between its panel and the window's edges.
const DialogPad = 20

// Dialog is the app's dialog: a panel for content width wide, with one
// border, corner, shadow and fit to the window, which fn builds the parts
// of, edge to edge. A height of its own, a role and a name are fn's to
// give panel.
func Dialog(c *ui.Context, open *bool, width float32, fn func(panel ui.Element)) ui.Element {
	return ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		dialogPanel(c, panel, width)
		dialogLook(c, backdrop, panel)
		fn(panel)
	})
}

// Picker is a Dialog that drops from the top, as the command palette:
// pickerLook has it there at once, the window dimmed less.
func Picker(c *ui.Context, open *bool, fn func(panel ui.Element)) ui.Element {
	return ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		dialogPanel(c, panel, DialogMedium)
		pickerLook(c, backdrop, panel)
		fn(panel)
	})
}

// dialogPanel gives a dialog's panel its size and its look.
func dialogPanel(c *ui.Context, panel ui.Element, width float32) {
	t := c.Theme()
	w, h := c.Size()
	// The border takes its room too.
	panel.Width(min(width+2*(DialogPad+1), w-2*DialogPad)).MaxHeight(h-2*DialogPad).
		Radius(RadiusLarge).Background(t.Background).Border(1, t.Border).Clip()
}

// Modal is a Dialog whose content is one padded column.
func Modal(c *ui.Context, open *bool, width float32, fn func()) ui.Element {
	return EnvModal(c, open, nil, width, fn)
}

// EnvModal is Modal with EnvBand flush at the top of its panel, for a
// dialog that writes to or restores into cfg's connection; with cfg nil,
// no band shows and the content keeps its place.
func EnvModal(c *ui.Context, open *bool, cfg *db.Config, width float32, fn func()) ui.Element {
	t := c.Theme()
	return Dialog(c, open, width, func(ui.Element) {
		if cfg != nil {
			EnvBand(c, cfg)
		}
		ui.Column(c).Padding(DialogPad).Gap(t.Space(3)).Shrink(1).Children(fn)
	})
}

// DialogTitle is a dialog's title.
func DialogTitle(c *ui.Context, title string) ui.Element {
	return ui.Text(c, title).FontSize(TextTitle).Bold()
}

// DialogHeader is a sectioned dialog's first bar, over a line: its
// DialogTitle, and what stands beside it.
func DialogHeader(c *ui.Context, fn func()) ui.Element {
	return ui.Row(c).Padding(14, DialogPad).Gap(10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border).Children(fn)
}

// DialogBody is a sectioned dialog's padded content.
func DialogBody(c *ui.Context, fn func()) ui.Element {
	return ui.Column(c).Padding(16, DialogPad).Gap(c.Theme().Space(3)).Shrink(1).Children(fn)
}

// DialogActions is a sectioned dialog's last bar: its buttons, under a
// line.
func DialogActions(c *ui.Context, fn func()) ui.Element {
	return ui.Row(c).Padding(12, DialogPad).Gap(8).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(c.Theme().Border).Children(fn)
}

// dialogLook dims the window behind a dialog and lifts its panel, which
// comes in as comeInto has it.
func dialogLook(c *ui.Context, backdrop, panel ui.Element) {
	frosted(c, backdrop, false)
	dialogShadow(panel)
	comeInto(backdrop, panel)
}

// pickerLook is dialogLook for a picker that drops from the top, as the
// command palette: dimmed less, and there at once, as the key that opens
// it asks.
func pickerLook(c *ui.Context, backdrop, panel ui.Element) {
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
