package widgets

// The sizes of text: every text in the app has one of them.
const (
	TextMeta    = 11 // counts, times, badges, types
	TextSmall   = 12 // secondary text, labels, status, help
	TextBody    = 13 // what is read and pressed: toolbars, rows, code in boxes
	TextTitle   = 15 // a dialog's or a card's title
	TextHeading = 18
	TextDisplay = 28
)

// The sizes of icons.
const (
	IconSmall   = 12
	IconMedium  = 14
	IconLarge   = 16
	IconDisplay = 28
)

// The radii of corners: a badge's or a chip's, a control's (the toolkit's
// own, ui.Theme's Radius), and a card's, a dialog's or a popup's; and
// that of what is round whatever its size, as a dot, a round button or a
// pill.
const (
	RadiusSmall  = 4
	RadiusMedium = 6
	RadiusLarge  = 10
	RadiusRound  = 999
)
