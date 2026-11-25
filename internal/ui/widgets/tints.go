package widgets

import "github.com/egoist/mygo/ui"

// The strengths of a tint: how much of a status color or of the accent
// fills behind what it marks. Every tinted fill in the app has one of
// them, so that a notice reads as strongly here as there.
const (
	tintSubtle = 0.08 // set apart, no more: a suggestion, a row pointed at
	tintNotice = 0.12 // says something: a notice's bar, an error's box, a chosen row
	tintStrong = 0.20 // must be seen: a chip in the title bar, where a drop lands
)

// TintSubtle is col as the fill of what is only set apart.
func TintSubtle(col ui.Color) ui.Color { return col.Alpha(tintSubtle) }

// TintNotice is col as the fill of what says something: a notice, an
// error, a choice.
func TintNotice(col ui.Color) ui.Color { return col.Alpha(tintNotice) }

// TintStrong is col as the fill of what must be seen.
func TintStrong(col ui.Color) ui.Color { return col.Alpha(tintStrong) }

// NoticeBar is a bar across a view that says something of it, as that a
// file changed on disk or a transaction failed: an alert in tone, then
// what fn builds, its text and its buttons, on a tint of tone. Every such
// bar is this one, as tall and as strong wherever it shows.
func NoticeBar(c *ui.Context, tone ui.Color, fn func()) ui.Element {
	return ui.Row(c).Padding(PadBarY, PadBarX).Gap(10).AlignItems(ui.Center).Background(TintNotice(tone)).Children(func() {
		ui.Icon(c, IconAlert).TextColor(tone).FontSize(IconMedium)
		fn()
	})
}

// RowButton is a row of a list that is pressed, as an account, a channel
// or a recent file: as tall and as padded in every list, lit when
// pointed at, tinted with the accent while on, the one chosen.
func RowButton(c *ui.Context, label string, on bool) ui.Element {
	b := ui.ButtonBase(c).Padding(5, 8).Radius(RadiusMedium).Label(label).Justify(ui.Start)
	switch {
	case on:
		b.Background(TintNotice(c.Theme().Accent))
	case b.Hovered():
		b.Background(PaletteOf(c).Hover)
	}
	return b
}

// SmallIconButton is an icon button of a tight place, as a tab's close
// button: IconButton with a smaller icon and less room around it.
func SmallIconButton(c *ui.Context, ic *ui.SVG, label string) ui.Element {
	b := ui.ButtonBase(c).Padding(3).Radius(RadiusSmall).Label(label)
	if b.Hovered() {
		b.Background(PaletteOf(c).Hover)
	}
	b.Children(func() {
		ui.Icon(c, ic).FontSize(IconSmall).TextColor(PaletteOf(c).Muted).ContentTransition(ui.ContentTransition{})
	})
	return b
}
