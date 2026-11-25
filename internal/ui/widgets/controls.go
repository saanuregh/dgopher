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
		ui.Icon(c, IconSearch).FontSize(IconSmall).TextColor(pal.Muted)
		in = ui.TextInputBase(c, query).Placeholder(placeholder).Grow(1).FontSize(TextBody).Label(placeholder)
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
	b := ui.ButtonBase(c).Padding(3, 10).Radius(RadiusMedium).Label(label)
	if on {
		b.Background(t.Surface)
	} else if b.Hovered() {
		b.Background(PaletteOf(c).Hover)
	}
	b.Children(func() {
		ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
			// The weight stays as another comes in front, so no pill moves.
			txt := ui.Text(c, label).FontSize(TextSmall).FontWeight(500)
			if !on {
				txt.TextColor(PaletteOf(c).Muted)
			}
			if ic != nil {
				ui.Icon(c, ic).FontSize(IconSmall).TextColor(PaletteOf(c).Muted)
			}
		})
	})
	return b
}

// ToolButton is a button of a toolbar with an icon and a label.
// A button whose label changes, as Run to Stop, changes in place: the
// label and icon slide to the new ones, and the button eases to its new
// width, rather than one button popping in for another.
func ToolButton(c *ui.Context, ic *ui.SVG, label, tip string) ui.Element {
	return toolButton(c, ic, label, "", tip, nil)
}

// LeadToolButton is the ToolButton of the toolbar's main action, as Run:
// its icon in tint, its label a little heavier, the first the eye finds.
func LeadToolButton(c *ui.Context, ic *ui.SVG, label, tip string, tint ui.Color) ui.Element {
	return toolButton(c, ic, label, "", tip, &tint)
}

// TimedToolButton is LeadToolButton with how long its work has run since
// since after its label, as a clock: Stop, of a run. The time is not part
// of its name, which a screen reader would else hear change each second.
func TimedToolButton(c *ui.Context, ic *ui.SVG, label, tip string, tint ui.Color, since time.Time) ui.Element {
	ran := time.Since(since)
	c.After(time.Second - ran%time.Second)
	return toolButton(c, ic, label, Clock(ran), tip, &tint)
}

func toolButton(c *ui.Context, ic *ui.SVG, label, note, tip string, tint *ui.Color) ui.Element {
	b := ui.Button(c, "").Label(label).Tooltip(tip).Transition(morph)
	b.Children(func() {
		ui.Row(c).Gap(5).Children(func() {
			icon := ui.Icon(c, ic).FontSize(IconMedium).ContentTransition(ui.ContentTransition{})
			txt := ui.Text(c, label).FontSize(TextBody).ContentTransition(ui.ContentTransition{})
			if tint != nil {
				icon.TextColor(*tint)
				txt.FontWeight(600)
			}
			if note != "" {
				ui.Text(c, note).FontSize(TextSmall).FontFeatures("tnum").TextColor(PaletteOf(c).Muted)
			}
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
	return e.Transition(ui.ElementTransition{Enter: barAppearing, Exit: barAppearing, Duration: morph.Duration})
}

// barAppearing is appearing without the scale, which would zoom a bar as
// wide as the window.
var barAppearing = &ui.Motion{Opacity: 0, Collapse: true}

// appearing is how what Appear has comes and goes.
var appearing = &ui.Motion{Opacity: 0, Scale: 0.9, Collapse: true}

// IconButton is a borderless button showing an icon, muted until pointed
// at, so that the chrome stays behind the work.
func IconButton(c *ui.Context, ic *ui.SVG, label string) ui.Element {
	return iconButton(c, ic, label, false)
}

// ToggleIconButton is an IconButton that shows whether what it switches
// is on.
func ToggleIconButton(c *ui.Context, ic *ui.SVG, label string, on bool) ui.Element {
	return iconButton(c, ic, label, on)
}

func iconButton(c *ui.Context, ic *ui.SVG, label string, on bool) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Label(label).Tooltip(label).Padding(4).Radius(RadiusMedium)
	col := PaletteOf(c).Muted
	switch {
	case on:
		b.Background(t.Accent.Alpha(0.15))
		col = t.Accent
	case b.Hovered():
		b.Background(PaletteOf(c).Hover)
		col = t.Text
	}
	b.Children(func() { ui.Icon(c, ic).FontSize(IconMedium).TextColor(col) })
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
	b.Children(func() { ui.Text(c, label).FontSize(TextSmall).ContentTransition(ui.ContentTransition{}) })
	return b
}

// StatusPrimaryButton is StatusButton filled with the accent: a status
// bar's main action.
func StatusPrimaryButton(c *ui.Context, label string) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Label(label).Padding(1, 8).Radius(t.Radius).Border(1, t.Accent).Background(t.Accent)
	if b.Hovered() {
		b.Background(t.AccentHover).Border(1, t.AccentHover)
	}
	b.Children(func() { ui.Text(c, label).FontSize(TextSmall).FontWeight(500).TextColor(t.AccentText) })
	return b
}

// DangerButton is the main button of what destroys data or trusts a
// server: red, its label bold; unavailable while why is not "", as
// Unavailable has it, and its clicks then for the caller to ignore.
func DangerButton(c *ui.Context, label, why string) ui.Element {
	b := ui.ButtonBase(c).Padding(6, 14).Radius(c.Theme().Radius).Label(label).Background(c.Theme().Danger)
	b.Children(func() { ui.Text(c, label).TextColor(OnDanger(c.Theme())).Bold() })
	return Unavailable(b, why)
}

// Unavailable marks b unavailable while why is not "", with why as its
// tooltip. Unlike Disabled, it keeps b focusable, so the keyboard reaches
// the tooltip, and assistive technology reads b as unavailable
// (AccessDisabled, aria-disabled); it dims as Disabled does. b still
// reports its clicks, which the caller ignores while why is not "".
func Unavailable(b ui.Element, why string) ui.Element {
	if why != "" {
		b.AccessDisabled(true).Opacity(0.5).Tooltip(why)
	}
	return b
}

// When is why while cond holds, and "" otherwise: a reason a control is
// unavailable, for Unavailable and Clicked. Several are tried in turn
// with cmp.Or, the first that holds given.
func When(cond bool, why string) string {
	if cond {
		return why
	}
	return ""
}

// WhyNot is what keeps something from being done, as a reason a control
// is unavailable: the first line of err, "" for none.
func WhyNot(err error) string {
	if err == nil {
		return ""
	}
	return FirstLine(err.Error())
}

// Clicked reports a click on b while it is available: why says why it
// is not, as Unavailable shows it, and is "" while it is.
func Clicked(b ui.Element, why string) bool {
	return Unavailable(b, why).Clicked() && why == ""
}

// ActivatedUnless is Activated of a dialog's main button that is
// unavailable while why is not "", with why as its tooltip.
func ActivatedUnless(c *ui.Context, b ui.Element, why string) bool {
	return Activated(c, Unavailable(b, why)) && why == ""
}

// BusyButton is a button that shows its work going on in place: a
// spinner and busy, as "Testing…", while working, over the room its
// label takes, so that what is beside it keeps its place. Its name stays
// its label.
func BusyButton(c *ui.Context, label, busy string, working bool) ui.Element {
	b := ui.Button(c, "").Label(label)
	b.Children(func() {
		ui.Row(c).Gap(6).Invisible().Children(func() {
			ui.Box(c).Size(IconMedium, IconMedium)
			ui.Text(c, label)
		})
		ui.Row(c).Absolute().Left(0).Right(0).Top(0).Bottom(0).Gap(6).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			if !working {
				ui.Text(c, label)
				return
			}
			// A spinner takes more than its font size: in an icon's room.
			ui.Box(c).Size(IconMedium, IconMedium).Center().Children(func() { ui.Spinner(c).FontSize(TextMeta) })
			ui.Text(c, busy)
		})
	})
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

// Keycap shows a shortcut as the keys it is pressed with, as a key's cap.
func Keycap(c *ui.Context, keys string) ui.Element {
	pal := PaletteOf(c)
	return ui.Text(c, KeyLabel(keys)).Font(MonoFont).FontSize(TextMeta).TextColor(pal.Muted).SingleLine().Shrink(0).
		Padding(1, 5).Radius(RadiusSmall).Background(pal.Hover).Border(1, c.Theme().Border)
}

// Bar is the row of a view's tools, as the editor's along its top: tools
// without a face until pointed at, text the size of their labels, and a
// line under it, so that every view's bar is as tall and reads alike.
// Titles and notes in it take its size unless they set their own.
func Bar(c *ui.Context, label string, fn func()) ui.Element {
	return ui.Toolbar(c, fn).Label(label).Padding(4, 8).FontSize(TextBody).
		BorderWidth(0, 0, 1, 0).BorderColor(c.Theme().Border)
}

// BarTitle is the start of a Bar: what the view shows, its icon, name and
// notes, apart from one another as words are, where the bar's tools sit
// closer.
func BarTitle(c *ui.Context, fn func()) ui.Element {
	return ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(1).MinWidth(0).Children(fn)
}
