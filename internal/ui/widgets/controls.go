package widgets

import (
	"github.com/egoist/mygo/ui"
)

// SearchBox is a search field with a placeholder of its own; Escape
// clears it. It is width wide, or grows for 0. It returns the text input.
func SearchBox(c *ui.Context, query *string, placeholder string, width float32) ui.Element {
	t := c.Theme()
	pal := PaletteOf(c)
	var in ui.Element
	box := ui.Row(c).Gap(6).Padding(4, 8).Radius(t.Radius).Border(1, t.Border).Background(t.Background)
	if width > 0 {
		box.Width(width)
	} else {
		box.Grow(1)
	}
	box.Children(func() {
		ui.Icon(c, IconSearch).FontSize(12).TextColor(pal.Muted)
		in = ui.TextInputBase(c, query).Placeholder(placeholder).Grow(1).FontSize(12.5).Label(placeholder)
		if *query != "" && in.Shortcut(0, ui.KeyEscape) {
			*query = ""
		}
	})
	if in.Focused() {
		box.Border(1, t.Accent)
	}
	return in
}

// Pill is a small tab of the results.
func Pill(c *ui.Context, label string, on bool) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Padding(3, 10).Radius(6).Label(label)
	if on {
		b.Background(t.Surface)
	} else if b.Hovered() {
		b.Background(PaletteOf(c).Hover)
	}
	b.Children(func() {
		txt := ui.Text(c, label).FontSize(12)
		if on {
			txt.Bold()
		}
	})
	return b
}

// ToolButton is a button of a toolbar with an icon and a label.
func ToolButton(c *ui.Context, ic *ui.SVG, label, tip string) ui.Element {
	b := ui.Button(c, "").Label(label).Tooltip(tip)
	b.Children(func() {
		ui.Row(c).Gap(5).Children(func() {
			ui.Icon(c, ic).FontSize(13)
			ui.Text(c, label).FontSize(12.5)
		})
	})
	return b
}

// IconButton is a borderless button showing an icon.
func IconButton(c *ui.Context, ic *ui.SVG, label string) ui.Element {
	b := ui.ButtonBase(c).Label(label).Tooltip(label).Padding(4).Radius(6)
	if b.Hovered() {
		b.Background(PaletteOf(c).Hover)
	}
	b.Children(func() { ui.Icon(c, ic).FontSize(14).TextColor(c.Theme().Text) })
	return b
}
