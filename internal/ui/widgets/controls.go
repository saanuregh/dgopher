package widgets

import (
	"time"

	"dgopher/internal/keymap"

	"github.com/egoist/mygo"
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

// ListKeys lets the search box in steer a list of n rows: Down and Up move
// *sel by one, PageDown and PageUp by page rows when page > 0, and the list
// scrolls to it. It reports whether Enter was pressed.
func ListKeys(in ui.Element, sel *int, n int, list *ui.ListState, page int) (submitted bool) {
	step := 0
	switch {
	case in.Shortcut(0, ui.KeyDown):
		step = 1
	case in.Shortcut(0, ui.KeyUp):
		step = -1
	case page > 0 && in.Shortcut(0, ui.KeyPageDown):
		step = page
	case page > 0 && in.Shortcut(0, ui.KeyPageUp):
		step = -page
	case in.Submitted():
		return true
	}
	if step != 0 && n > 0 {
		*sel = max(0, min(*sel+step, n-1))
		list.ScrollIntoView(*sel)
	}
	return false
}

// Pill is a small tab of the results.
func Pill(c *ui.Context, label string, on bool) ui.Element {
	return IconPill(c, nil, label, on)
}

// IconPill is a Pill with an icon after its label, none for nil.
func IconPill(c *ui.Context, ic *ui.SVG, label string, on bool) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Padding(3, 10).Radius(6).Label(label)
	if on {
		b.Background(t.Surface)
	} else if b.Hovered() {
		b.Background(PaletteOf(c).Hover)
	}
	b.Children(func() {
		ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
			txt := ui.Text(c, label).FontSize(12)
			if on {
				txt.Bold()
			}
			if ic != nil {
				ui.Icon(c, ic).FontSize(11).TextColor(PaletteOf(c).Muted)
			}
		})
	})
	return b
}

// ToolFontSize is the size of the labels of a toolbar's buttons, which
// other controls there take to be as tall; toolIconSize their icons'.
const (
	ToolFontSize = 12.5
	toolIconSize = 13
)

// ToolButton is a button of a toolbar with an icon and a label.
// A button whose label changes, as Run to Stop, changes in place: the
// label and icon slide to the new ones, and the button eases to its new
// width, rather than one button popping in for another.
func ToolButton(c *ui.Context, ic *ui.SVG, label, tip string) ui.Element {
	b := ui.Button(c, "").Label(label).Tooltip(tip).Transition(morph)
	b.Children(func() {
		ui.Row(c).Gap(5).Children(func() {
			ui.Icon(c, ic).FontSize(toolIconSize).ContentTransition(ui.ContentTransition{})
			ui.Text(c, label).FontSize(ToolFontSize).ContentTransition(ui.ContentTransition{})
		})
	})
	return b
}

// MorphButton is ui.Button whose label changes in place, as Cancel to
// Close: the text slides to the new one and the button eases to its width.
func MorphButton(c *ui.Context, label string) ui.Element {
	return morphLabel(c, ui.Button(c, "").Label(label), label)
}

// MorphPrimaryButton is ui.PrimaryButton as MorphButton is ui.Button.
func MorphPrimaryButton(c *ui.Context, label string) ui.Element {
	return morphLabel(c, ui.PrimaryButton(c, "").Label(label), label)
}

func morphLabel(c *ui.Context, b ui.Element, label string) ui.Element {
	b.Transition(morph).Children(func() { ui.Text(c, label).SingleLine().ContentTransition(ui.ContentTransition{}) })
	return b
}

// DismissButton is a job dialog's first footer button, in one place:
// while the job runs it reads Cancel and stops it; otherwise it closes the
// dialog, reading Cancel before the job and Close once done. It morphs
// between them.
func DismissButton(c *ui.Context, running, done bool, stop, close func()) {
	label := "Cancel"
	if done && !running {
		label = "Close"
	}
	if !MorphButton(c.Key("dismiss"), label).Clicked() {
		return
	}
	switch {
	case !running:
		close()
	case stop != nil:
		stop()
	}
}

// morph eases a button to its new width as its label changes.
var morph = ui.ElementTransition{Size: true, Duration: 160 * time.Millisecond}

// Appear has an element that comes and goes, as a Stop shown while work
// lasts, fade and grow in and fade out, its room opening and closing
// rather than pushing its neighbours at once; while there, it eases to a
// new width as its content changes, as a morphing button does. For one as
// wide as what holds it, as a bar, AppearBar.
func Appear(e ui.Element) ui.Element {
	return e.Transition(ui.ElementTransition{Size: true, Enter: appearing, Exit: appearing, Duration: morph.Duration})
}

// AppearBar is Appear for an element as wide as what holds it, as a bar:
// it does not ease its width, which would trail behind its parent's as a
// panel opens or a splitter moves.
func AppearBar(e ui.Element) ui.Element {
	return e.Transition(ui.ElementTransition{Enter: appearing, Exit: appearing, Duration: morph.Duration})
}

// appearing is how what Appear has comes and goes.
var appearing = &ui.Motion{Opacity: 0, Scale: 0.9, Collapse: true}

// IconButton is a borderless button showing an icon.
func IconButton(c *ui.Context, ic *ui.SVG, label string) ui.Element {
	b := ui.ButtonBase(c).Label(label).Tooltip(label).Padding(4).Radius(6)
	if b.Hovered() {
		b.Background(PaletteOf(c).Hover)
	}
	b.Children(func() { ui.Icon(c, ic).FontSize(14).TextColor(c.Theme().Text) })
	return b
}

// ToggleIconButton is an IconButton that shows whether what it switches
// is on.
func ToggleIconButton(c *ui.Context, ic *ui.SVG, label string, on bool) ui.Element {
	b := IconButton(c, ic, label)
	if on {
		b.Background(c.Theme().Accent.Alpha(0.15))
	}
	return b
}

// StatusButton is a small bordered button of a status bar, about as tall
// as its text.
func StatusButton(c *ui.Context, label string) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Label(label).Padding(1, 8).Radius(t.Radius).Border(1, t.Border).Transition(morph)
	if b.Hovered() {
		b.Background(PaletteOf(c).Hover)
	}
	b.Children(func() { ui.Text(c, label).FontSize(12).ContentTransition(ui.ContentTransition{}) })
	return b
}

// DangerButton is the main button of what destroys data or trusts a
// server: red, its label bold.
func DangerButton(c *ui.Context, label string, disabled bool) ui.Element {
	b := ui.ButtonBase(c).Padding(6, 14).Radius(c.Theme().Radius).Disabled(disabled).Label(label).Background(c.Theme().Danger)
	if disabled {
		b.Opacity(0.5)
	}
	b.Children(func() { ui.Text(c, label).TextColor(ui.RGB(255, 255, 255)).Bold() })
	return b
}

// Activated reports whether a dialog's main button was clicked, or the
// key that does what a dialog is for was pressed in it, the button
// enabled.
func Activated(c *ui.Context, b ui.Element) bool {
	return b.Clicked() || !b.IsDisabled() && keymap.Pressed(c, keymap.ConfirmDialog)
}

// DialogFooter draws a dialog's Cancel and main buttons at its end;
// Cancel sets *open false. It reports whether the main button was
// activated, or submit is set.
func DialogFooter(c *ui.Context, open *bool, action string, submit bool) bool {
	activated := false
	ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
		if ui.Button(c, "Cancel").Clicked() {
			*open = false
		}
		activated = Activated(c, ui.PrimaryButton(c, action)) || submit
	})
	return activated
}

// ChoosePath asks for one path with the system's open dialog, off the UI
// goroutine, and posts then with it when one is chosen.
func ChoosePath(post func(func()), opts mygo.OpenDialogOptions, then func(path string)) {
	go func() {
		paths, err := mygo.Dialog.Open(opts)
		if err != nil || len(paths) == 0 {
			return
		}
		post(func() { then(paths[0]) })
	}()
}
