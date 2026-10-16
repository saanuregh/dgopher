package widgets

import (
	"maps"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/sqltext"

	"github.com/egoist/mygo/ui"
)

// HoverSettings are what the user chose of hover cards: none at all, how
// long the pointer rests before one opens, and the surfaces without them.
type HoverSettings struct {
	Off   bool
	Delay time.Duration
	// Hidden holds the surfaces (CardEditor, …) whose cards are off.
	Hidden map[string]bool
}

// The surfaces a hover card shows on, which settings turn off one by one.
const (
	CardEditor  = "editor"
	CardSidebar = "sidebar"
	CardGrid    = "grid" // asked for only (CardOptions.OnRequest): no setting
	CardDiagram = "diagram"
)

// HoverCards are the settings every card follows; the app sets them from
// its settings.
var HoverCards = HoverSettings{Delay: DefaultHoverDelay}

// DefaultHoverDelay is how long the pointer rests on a thing before its
// card opens, as long as a tooltip's.
const DefaultHoverDelay = 600 * time.Millisecond

// Shows reports whether cards show on a surface.
func (h HoverSettings) Shows(surface string) bool { return !h.Off && !h.Hidden[surface] }

const (
	// cardLeaveGrace is how long a card stays once the pointer left its
	// thing and the card, for the pointer to cross the gap between them.
	cardLeaveGrace = 300 * time.Millisecond
	// cardWarm is how soon after a card goes the next one opens at once,
	// as the pointer moves down a list.
	cardWarm = time.Second
	// copiedFor is how long a copy button says the text was copied.
	copiedFor = 1200 * time.Millisecond
)

// CardPlace is where a card opens by the thing it is about.
type CardPlace uint8

const (
	// PlaceAbove opens the card above, or below where there is no room.
	PlaceAbove CardPlace = iota
	// PlaceBelow opens it below, or above where there is no room.
	PlaceBelow
	// PlaceRight opens it at the right, level with the thing's top.
	PlaceRight
)

// hoverState is a window's cards: the one open, the one pinned, the
// thing the pointer rests on, and what each card keeps while it shows.
type hoverState struct {
	open      string // the key of the card open, "" for none
	leftAt    time.Time
	warmUntil time.Time
	// dismissed is the card a press or Escape closed, until the pointer
	// leaves its thing.
	dismissed string
	// rest is the thing the pointer rests on, since restSince, where it was
	// then: a scroll moving another thing under the pointer starts a rest.
	rest      string
	restSince time.Time
	restAt    ui.Rect
	// restSeen and openSeen are set as the frame builds the thing rested
	// on and that of the open card; FinishCards drops those not built.
	restSeen, openSeen bool
	// over is set while the pointer is in the open card; asked, while the
	// card open was asked for (ShowCard) rather than rested on, which
	// keeps it until Escape or a press outside it.
	over, asked bool
	// fresh is set until the card asked for is first built: the press
	// that asked for it, as an Alt+click, closes nothing.
	fresh    bool
	pinned   *pinnedCard
	copied   map[string]time.Time // by card key and label
	expanded map[string]bool      // the cards whose "+ N more" was clicked
	filters  map[string]*string   // the column filters typed, by card
	pages    map[string]int       // the page of a card's pages (overloads)
	extra    map[string]any       // what a card's content keeps (Keep)
	// content is the last card CardContent built, whose state goes as
	// another is built.
	content string
}

// pinnedCard is a card the user pinned: it stays where it was, in front,
// until closed, built by the function that built it.
type pinnedCard struct {
	key   string
	build func(c *ui.Context, card *Card)
	wide  bool
	at    ui.Point
	drag  ui.Point
}

func hoverOf(c *ui.Context) *hoverState {
	return ui.Local(c.Root(), "hover cards", func() hoverState {
		return hoverState{copied: map[string]time.Time{}, expanded: map[string]bool{}, filters: map[string]*string{},
			pages: map[string]int{}, extra: map[string]any{}}
	})
}

// close closes the open card; warm, the next one opens at once.
func (st *hoverState) close(now time.Time, warm bool) {
	if st.open != "" {
		if warm {
			st.warmUntil = now.Add(cardWarm)
		}
		st.forget(st.open)
	}
	st.open, st.leftAt, st.over, st.asked = "", time.Time{}, false, false
}

// ShowCard opens the card of key at once, as a menu's Show Card or an
// Alt+click asks: it shows by its thing, built with HoverCard as for a
// rest, and stays until Escape, a press outside it, or another card.
func ShowCard(c *ui.Context, key string) {
	st := hoverOf(c)
	if st.open != key {
		st.close(c.Now(), false)
	}
	st.open, st.asked, st.dismissed, st.leftAt, st.fresh = key, true, "", time.Time{}, true
	c.Invalidate()
}

// forget drops what a card kept, unless it still shows, pinned.
func (st *hoverState) forget(key string) {
	if st.pinned != nil && st.pinned.key == key {
		return
	}
	prefix := key + "\x00"
	maps.DeleteFunc(st.copied, func(k string, _ time.Time) bool { return strings.HasPrefix(k, prefix) })
	maps.DeleteFunc(st.extra, func(k string, _ any) bool { return strings.HasPrefix(k, prefix) })
	delete(st.expanded, key)
	delete(st.filters, key)
	delete(st.pages, key)
}

// CardOptions are where a card opens and how.
type CardOptions struct {
	Place CardPlace
	// Wide is for content that needs room, as a JSON document.
	Wide bool
	// Edge, with PlaceRight, is a window's x the card opens past: past the
	// sidebar a row is in, so as not to cover it.
	Edge float32
	// OnRequest opens the card only when ShowCard asks, never as the
	// pointer rests, as a grid's, whose cells the pointer crosses all the
	// time; the settings, of cards on hover, do not apply to it.
	OnRequest bool
	// Hovered, when set, says whether the pointer is on the thing, for a
	// thing that is no element, as a line a canvas draws: the card opens
	// by anchor, a box where the pointer came on it.
	Hovered *bool
}

// HoverCard gives anchor a card that build fills, opened once the pointer
// rests on anchor (HoverCards.Delay), or at once while another card just
// showed, as the pointer moves down a list. The pointer may go into the
// card, to click its buttons or select its text; it closes a moment after
// the pointer left both, on a press on anchor, or on Escape. key tells
// the card from the others of the window, as "table:shop.orders". A
// thing that may have a card calls it in each frame the pointer is on it
// and while its card shows; the root calls FinishCards after.
func HoverCard(c *ui.Context, anchor ui.Element, surface, key string, o CardOptions, build func(c *ui.Context, card *Card)) {
	if !o.OnRequest && !HoverCards.Shows(surface) {
		return
	}
	st := hoverOf(c)
	now := c.Now()
	hovered := anchor.Hovered()
	if o.Hovered != nil {
		hovered = *o.Hovered
	}
	// A press on the thing closes its card, and keeps it closed while the
	// pointer stays; one in the card, which MyGo counts as on the thing
	// the card is attached to, does not, as the card tells below.
	pressed := anchor.PressedInside() && !st.asked
	if pressed && st.open != key {
		st.dismissed = key
	}
	bounds := anchor.Bounds()
	switch {
	case hovered:
		if st.rest != key || st.restAt != bounds {
			st.rest, st.restSince, st.restAt = key, now, bounds
		}
		st.restSeen = true
	case st.dismissed == key:
		st.dismissed = ""
	}
	pinned := st.pinned != nil && st.pinned.key == key
	if hovered && !o.OnRequest && st.dismissed != key && st.open != key && !pinned {
		if wait := HoverCards.Delay - now.Sub(st.restSince); wait <= 0 || now.Before(st.warmUntil) {
			st.open, st.leftAt = key, time.Time{}
		} else {
			c.After(wait)
		}
	}
	if st.open != key {
		return
	}
	st.openSeen = true
	at, gap := o.Place, cardGap(c)
	if at == PlaceRight {
		gap = max(gap, o.Edge+cardGap(c)-(bounds.X+bounds.W))
		// No room at the right: below, as a menu opens.
		if w, _ := c.Size(); bounds.X+bounds.W+gap+cardWidth(o.Wide) > w {
			at, gap = PlaceBelow, cardGap(c)
		}
	}
	over := false
	ui.Overlay(c, func() {
		panel := ui.Scroll(c)
		switch at {
		case PlaceBelow:
			panel.AttachTo(anchor, ui.AnchorBottomStart, ui.AnchorTopStart).Margin(cardGap(c), 0, 0, 0)
		case PlaceRight:
			panel.AttachTo(anchor, ui.AnchorTopEnd, ui.AnchorTopStart).Margin(0, 0, 0, gap)
		default:
			panel.AttachTo(anchor, ui.AnchorTopStart, ui.AnchorBottomStart).Margin(0, 0, cardGap(c), 0)
		}
		CardPanel(c, panel, o.Wide)
		panel.MaxHeight(cardMaxHeight)
		card := &Card{st: st, key: key, build: build, wide: o.Wide}
		panel.Children(func() { build(c, card) })
		over = panel.Hovered()
		inCard := panel.PressedInside()
		defer func() { st.fresh = false }()
		if panel.OverlayShortcut(0, ui.KeyEscape) || st.asked && !st.fresh && panel.PressedOutside() || pressed && !inCard {
			st.dismissed = key
			st.close(now, true)
		}
	})
	if st.open != key {
		c.Invalidate()
		return
	}
	st.over = over
	st.warmUntil = now.Add(cardWarm)
	switch {
	case hovered || over || st.asked:
		st.leftAt = time.Time{}
	case st.leftAt.IsZero():
		st.leftAt = now
		c.After(cardLeaveGrace)
	case now.Sub(st.leftAt) >= cardLeaveGrace:
		st.close(now, true)
		c.Invalidate()
	default:
		c.After(cardLeaveGrace - now.Sub(st.leftAt))
	}
}

// HoverCardOpen reports the key of the window's open card, "" for none.
func HoverCardOpen(c *ui.Context) string { return hoverOf(c).open }

// PointerInCard reports whether the pointer is in the window's open card,
// as it was in the last frame: what lies under the card is not pointed at.
func PointerInCard(c *ui.Context) bool {
	st := hoverOf(c)
	return st.open != "" && st.over
}

func cardGap(c *ui.Context) float32 { return c.Theme().Space(1) }

// CardWidth is the width of a card, and of a wide one; CardMaxHeight the
// most a card takes before it scrolls.
const (
	CardWidth     = 360
	WideCardWidth = 520
	cardMaxHeight = 380
)

func cardWidth(wide bool) float32 {
	if wide {
		return WideCardWidth
	}
	return CardWidth
}

// CardContent builds a card's content into the element being built,
// where something else decides when it shows, as the editor's hover;
// close closes it, as the card's buttons do.
func CardContent(c *ui.Context, key string, close func(), build func(c *ui.Context, card *Card)) {
	st := hoverOf(c)
	if st.content != key {
		st.forget(st.content)
		st.content = key
	}
	card := &Card{st: st, key: key, build: build, onClose: close}
	build(c, card)
}

// CardPanel gives a panel the look of a card, that of the app's menus.
func CardPanel(c *ui.Context, panel ui.Element, wide bool) {
	t := c.Theme()
	panel.Width(cardWidth(wide)).Radius(t.Radius+2).Background(t.Background).Border(1, t.Border).
		Shadow(0, 8, 28, 0, ui.RGBA(0, 0, 0, 0.22)).Role(ui.RoleDialog).FontSize(ToolFontSize).
		Transition(cardMotion)
}

// CardScroll is the scroll a card's content goes in where its panel is
// no scroll of its own, as the editor's hover.
func CardScroll(c *ui.Context) ui.Element { return ui.Scroll(c).MaxHeight(cardMaxHeight) }

// cardMotion fades a card in, quickly, and at once under reduced motion,
// which MyGo's transitions follow.
var cardMotion = ui.ElementTransition{Enter: &ui.Motion{Opacity: 0}, Duration: 120 * time.Millisecond}

// FinishCards ends the window's cards for the frame: a card whose thing
// was not built, as a cell that started an edit, closes, and a rest on a
// thing not built ends; the pinned card shows where the user left it.
// The root view calls it once a frame, after its overlays.
func FinishCards(c *ui.Context) {
	st := hoverOf(c)
	if !st.restSeen {
		st.rest = ""
	}
	if st.open != "" && !st.openSeen {
		st.close(c.Now(), false)
	}
	st.restSeen, st.openSeen = false, false
	p := st.pinned
	if p == nil {
		return
	}
	ui.Overlay(c, func() {
		panel := ui.Scroll(c).Absolute().Left(p.at.X).Top(p.at.Y).MaxHeight(cardMaxHeight)
		CardPanel(c, panel, p.wide)
		panel.Draggable(&p.drag, ui.DraggableOptions{})
		card := &Card{st: st, key: p.key, build: p.build, wide: p.wide, pinned: true}
		panel.Children(func() { p.build(c, card) })
	})
}

// Tone is the color of a card's icon, by what the card is about.
type Tone uint8

const (
	ToneTable Tone = iota
	ToneColumn
	ToneFunction
	ToneParam
	ToneValue
)

func toneColor(c *ui.Context, tone Tone) ui.Color {
	pal := PaletteOf(c)
	switch tone {
	case ToneColumn:
		return pal.Syntax[sqltext.QuotedIdent]
	case ToneFunction:
		return pal.Syntax[sqltext.Keyword]
	case ToneParam:
		return pal.Syntax[sqltext.Param]
	case ToneValue:
		return pal.Syntax[sqltext.Number]
	}
	return c.Theme().Accent
}

// Card is a card being built: its parts go down it in the order called,
// as the header, then facts, a comment, columns and actions.
type Card struct {
	st     *hoverState
	key    string
	build  func(c *ui.Context, card *Card)
	wide   bool
	pinned bool
	// onClose closes what shows the card, when not HoverCard.
	onClose func()
}

// Pinned reports whether the card is pinned, which keeps it open.
func (k *Card) Pinned() bool { return k.pinned }

// Keep returns what the card's content keeps from frame to frame while
// the card shows, init making it the first time.
func Keep[T any](k *Card, name string, init func() T) *T {
	id := k.key + "\x00" + name
	if v, ok := k.st.extra[id].(*T); ok {
		return v
	}
	v := init()
	k.st.extra[id] = &v
	return &v
}

const cardPadX, cardIndent = 12, 30

// Header names what the card is about: an icon in its tone, the name,
// and where it is, muted, as "· shop.main"; and the button pinning it,
// or closing it once pinned.
func (k *Card) Header(c *ui.Context, icon *ui.SVG, tone Tone, name, where string) {
	t := c.Theme()
	col := toneColor(c, tone)
	ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(10, cardPadX, 4, cardPadX).Children(func() {
		ui.Box(c).Size(20, 20).Radius(5).Background(col.Alpha(0.14)).AlignItems(ui.Center).Justify(ui.Center).Shrink(0).Children(func() {
			ui.Icon(c, icon).FontSize(12).TextColor(col)
		})
		ui.Text(c, name).FontWeight(600).FontSize(t.FontSize).SingleLine().Shrink(1)
		if where != "" {
			ui.Text(c, where).TextColor(PaletteOf(c).Muted).SingleLine().Shrink(1)
		}
		ui.Box(c).Grow(1)
		if k.pinned {
			if IconButton(c, IconX, "Close the card").Clicked() {
				k.st.pinned = nil
				k.st.forget(k.key)
			}
		} else if IconButton(c, IconPin, "Pin the card, to keep it open").Clicked() {
			k.pin(c)
		}
	})
}

// pin keeps the card open where it shows.
func (k *Card) pin(c *ui.Context) {
	at := ui.Point{X: 80, Y: 80}
	if w, h := c.Size(); w > 0 && h > 0 {
		at = ui.Point{X: w - cardWidth(k.wide) - 24, Y: 64}
	}
	k.st.pinned = &pinnedCard{key: k.key, build: k.build, wide: k.wide, at: at}
	k.close(c)
}

// close closes the card, unless pinned.
func (k *Card) close(c *ui.Context) {
	if k.pinned {
		return
	}
	if k.onClose != nil {
		k.onClose()
		return
	}
	k.st.close(c.Now(), true)
}

// Mono adds a line in the monospaced font, as a function's signature.
func (k *Card) Mono(c *ui.Context, s string) {
	ui.Text(c, s).Font(MonoFont).FontSize(12.5).Padding(2, cardPadX, 8, cardIndent+cardPadX-12).Selectable()
}

// Meta adds a muted line of facts, joined by dots; empty ones are left
// out.
func (k *Card) Meta(c *ui.Context, facts ...string) {
	var shown []string
	for _, f := range facts {
		if f != "" {
			shown = append(shown, f)
		}
	}
	if len(shown) == 0 {
		return
	}
	ui.Text(c, strings.Join(shown, " · ")).TextColor(PaletteOf(c).Muted).Padding(0, cardPadX, 8, cardIndent+cardPadX-12)
}

// Comment adds what a comment of the database says of the thing.
func (k *Card) Comment(c *ui.Context, s string) {
	if s = strings.TrimSpace(s); s == "" {
		return
	}
	ui.Text(c, s).Padding(0, cardPadX, 10, cardIndent+cardPadX-12).Selectable()
}

// Note adds a sentence about the card's state, as why it shows less.
func (k *Card) Note(c *ui.Context, s string) {
	ui.Text(c, s).TextColor(PaletteOf(c).Muted).Italic().Padding(0, cardPadX, 10, cardIndent+cardPadX-12)
}

// CardPill is a short fact shown as a rounded label.
type CardPill struct {
	Text string
	// Mono writes it in the monospaced font, as a type.
	Mono bool
	Tone PillTone
	// Open, when set, makes it a link that runs Open.
	Open func()
}

// PillTone is the color of a pill.
type PillTone uint8

const (
	PillPlain PillTone = iota
	PillKey
	PillLink
)

// Pills adds a row of pills.
func (k *Card) Pills(c *ui.Context, pills ...CardPill) { k.LabeledPills(c, "", pills...) }

// LabeledPills adds a row of pills after a muted label, as "Joins".
func (k *Card) LabeledPills(c *ui.Context, label string, pills ...CardPill) {
	t := c.Theme()
	pal := PaletteOf(c)
	ui.Row(c).Wrap().Gap(5).AlignItems(ui.Center).Padding(0, cardPadX, 10, cardIndent+cardPadX-12).Children(func() {
		if label != "" {
			ui.Text(c, label).FontSize(11.5).TextColor(pal.Muted).Margin(0, 3, 0, 0)
		}
		for i, p := range pills {
			ink, line := pal.Muted, t.Border
			switch p.Tone {
			case PillKey:
				ink, line = t.Warning, t.Warning.Alpha(0.35)
			case PillLink:
				ink, line = t.Accent, t.Accent.Alpha(0.35)
			}
			var el ui.Element
			if p.Open != nil {
				el = ui.ButtonBase(c.Key(i)).Label(p.Text).Cursor(ui.CursorPointer)
				if el.Clicked() {
					p.Open()
				}
			} else {
				el = ui.Box(c.Key(i))
			}
			el.Padding(1, 7).Radius(9).Border(1, line)
			if p.Mono {
				el.Background(t.Text.Alpha(0.05))
				ink = t.Text
			}
			el.Children(func() {
				txt := ui.Text(c, p.Text).FontSize(11).TextColor(ink).SingleLine()
				if p.Mono {
					txt.Font(MonoFont)
				}
			})
		}
	})
}

// Section adds a part below a line, which fn builds.
func (k *Card) Section(c *ui.Context, fn func()) {
	t := c.Theme()
	ui.Column(c).Gap(3).Padding(8, cardPadX).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(fn)
}

// Placeholder adds bars where content is being read, as it will lie,
// so the card does not jump when it comes.
func (k *Card) Placeholder(c *ui.Context, lines int) {
	k.Section(c, func() {
		for i := range lines {
			w := float32(220 - 50*(i%3))
			ui.Box(c.Key(i)).Height(9).Width(w).Radius(4).Background(c.Theme().Text.Alpha(0.08)).Margin(3, 0)
		}
	})
}

// CardColumn is a column as a card lists it.
type CardColumn struct {
	Name, Type string
	Key        bool
	// Ref is the table a foreign key of the column points at, "" for none.
	Ref string
	// Used marks a column the statement being written uses.
	Used bool
}

// ColumnsOptions are how a card lists columns.
type ColumnsOptions struct {
	// Insert, when set, makes each column a button that runs it.
	Insert func(name string)
}

// cardColumns is how many columns a card lists before "+ N more".
const cardColumns = 12

// filterColumnsFrom is how many columns a card lists before it offers to
// filter them.
const filterColumnsFrom = 30

// orderColumns puts keys first, then foreign keys, then the others, each
// group in the table's order.
func orderColumns(cols []CardColumn) []CardColumn {
	out := make([]CardColumn, 0, len(cols))
	for pass := range 3 {
		for _, col := range cols {
			group := 2
			switch {
			case col.Key:
				group = 0
			case col.Ref != "":
				group = 1
			}
			if group == pass {
				out = append(out, col)
			}
		}
	}
	return out
}

// Columns adds a table's columns, keys and foreign keys first; past
// o.Max, "+ N more" shows the rest, and a long list can be filtered.
func (k *Card) Columns(c *ui.Context, cols []CardColumn, o ColumnsOptions) {
	t := c.Theme()
	pal := PaletteOf(c)
	limit := cardColumns
	cols = orderColumns(cols)
	k.Section(c, func() {
		if len(cols) >= filterColumnsFrom && k.st.expanded[k.key] {
			f := k.st.filters[k.key]
			if f == nil {
				f = new(string)
				k.st.filters[k.key] = f
			}
			ui.SearchField(c, f).Placeholder("Filter columns").FontSize(12).Margin(0, 0, 4, 0)
			if *f != "" {
				var kept []CardColumn
				for _, col := range cols {
					if strings.Contains(strings.ToLower(col.Name), strings.ToLower(*f)) {
						kept = append(kept, col)
					}
				}
				cols = kept
			}
		}
		shown := cols
		if !k.st.expanded[k.key] && len(cols) > limit {
			shown = cols[:limit]
		}
		for i, col := range shown {
			// A column the card inserts is a button, else a plain row.
			var row ui.Element
			if o.Insert != nil {
				row = ui.ButtonBase(c.Key(insertKey{i})).Label("Insert " + col.Name).Tooltip("Insert " + col.Name + " at the caret").
					Cursor(ui.CursorPointer).Justify(ui.Start)
				if row.Hovered() {
					row.Background(pal.Hover)
				}
				if row.Clicked() {
					o.Insert(col.Name)
				}
			} else {
				row = ui.Row(c.Key(col.Name))
			}
			row.Gap(8).AlignItems(ui.Center).Padding(1, 4).Margin(0, -4).Radius(4)
			row.Children(func() {
				mark := ui.Box(c).Width(12).Shrink(0).AlignItems(ui.Center)
				mark.Children(func() {
					switch {
					case col.Key:
						ui.Icon(c, IconKey).FontSize(10).TextColor(t.Warning)
					case col.Used:
						ui.Box(c).Size(5, 5).Radius(3).Background(t.Accent)
					}
				})
				name := ui.Text(c, col.Name).SingleLine().Shrink(1)
				if col.Used {
					name.FontWeight(600)
				}
				if col.Ref != "" {
					ui.Text(c, "→ "+col.Ref).FontSize(11).TextColor(t.Accent).SingleLine().Shrink(1)
				}
				ui.Box(c).Grow(1)
				ui.Text(c, col.Type).Font(MonoFont).FontSize(11.5).TextColor(pal.Muted).SingleLine().Shrink(0)
			})
		}
		if more := len(cols) - len(shown); more > 0 {
			if ui.ButtonBase(c.Key("more")).Label("Show every column").Cursor(ui.CursorPointer).Children(func() {
				ui.Text(c, "+ "+Count(more, "more column")).FontSize(11.5).TextColor(pal.Muted)
			}).Clicked() {
				k.st.expanded[k.key] = true
			}
		}
	})
}

// Rows adds labels and values, the labels right-aligned in a column, as
// a record shows; a value Masked shows dots.
func (k *Card) Rows(c *ui.Context, rows [][2]string) {
	pal := PaletteOf(c)
	width := float32(0)
	for _, r := range rows {
		w, _ := c.MeasureText(0, ui.Span{Text: r[0], Size: 12})
		width = max(width, w)
	}
	width = min(width+2, 140)
	k.Section(c, func() {
		for i, r := range rows {
			ui.Row(c.Key(i)).Gap(12).Children(func() {
				ui.Text(c, r[0]).FontSize(12).TextColor(pal.Muted).Width(width).TextAlign(ui.End).SingleLine().Shrink(0)
				ui.Text(c, r[1]).Font(MonoFont).FontSize(11.5).SingleLine().Shrink(1).Selectable()
			})
		}
	})
}

// Pages adds "‹ 2 of 3 ›" to step through n pages, as a function's
// overloads, and returns the page shown, from 0.
func (k *Card) Pages(c *ui.Context, n int) int {
	page := min(k.st.pages[k.key], n-1)
	if n < 2 {
		return 0
	}
	pal := PaletteOf(c)
	ui.Row(c).Gap(4).AlignItems(ui.Center).Padding(0, cardPadX, 6, cardIndent+cardPadX-12).Children(func() {
		if IconButton(c, IconPrev, "The one before").Disabled(page == 0).Clicked() {
			page--
		}
		ui.Text(c, strconv.Itoa(page+1)+" of "+strconv.Itoa(n)).FontSize(11.5).TextColor(pal.Muted)
		if IconButton(c, IconNext, "The next one").Disabled(page == n-1).Clicked() {
			page++
		}
	})
	k.st.pages[k.key] = page
	return page
}

// Actions adds the card's buttons, in a row below a line.
func (k *Card) Actions(c *ui.Context, fn func(a *CardActions)) {
	t := c.Theme()
	ui.Row(c).Wrap().Gap(2).Padding(5, 6).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		fn(&CardActions{k: k})
	})
}

// CardActions adds a card's buttons.
type CardActions struct {
	k *Card
	n int
}

// Button adds a button with the keys that do the same, "" for none, and
// reports whether it was clicked; a click closes the card, unless pinned.
func (a *CardActions) Button(c *ui.Context, label, keys string) bool {
	if !a.button(c, label, keys) {
		return false
	}
	a.k.close(c)
	return true
}

// AnchoredButton is Button for an action on the place the card is of, as
// a range of the text or a row of a grid, which may hold something else
// by the time a pinned card is clicked: a pinned card leaves it out.
func (a *CardActions) AnchoredButton(c *ui.Context, label string) bool {
	return !a.k.pinned && a.Button(c, label, "")
}

// KeepOpen adds a button that changes the card rather than leaving it,
// as one reading more into it: a click keeps the card open.
func (a *CardActions) KeepOpen(c *ui.Context, label string) bool { return a.button(c, label, "") }

func (a *CardActions) button(c *ui.Context, label, keys string) bool {
	b := a.actionButton(c, label)
	b.Gap(6).Children(func() {
		ui.Text(c, label).FontSize(12)
		if keys != "" {
			Keycap(c, keys)
		}
	})
	return b.Clicked()
}

// actionButton is an action's button, without its content.
func (a *CardActions) actionButton(c *ui.Context, label string) ui.Element {
	a.n++
	b := ui.ButtonBase(c.Key(actionKey{a.n})).Label(label).Padding(4, 8).Radius(5).Cursor(ui.CursorPointer)
	if b.Hovered() {
		b.Background(PaletteOf(c).Hover)
	}
	return b
}

// Copy adds a button copying text, which says "Copied" for a moment.
func (a *CardActions) Copy(c *ui.Context, label, text string) {
	a.CopyFunc(c, label, func() string { return text })
}

// CopyFunc is Copy for text made only as it is copied, as a long value
// indented.
func (a *CardActions) CopyFunc(c *ui.Context, label string, text func() string) {
	id := a.k.key + "\x00" + label
	at, ok := a.k.st.copied[id]
	copied := ok && c.Now().Sub(at) < copiedFor
	b := a.actionButton(c, label)
	b.Gap(5).Children(func() {
		if copied {
			ui.Icon(c, IconCheck).FontSize(11).TextColor(c.Theme().Success)
			ui.Text(c, "Copied").FontSize(12)
		} else {
			ui.Text(c, label).FontSize(12)
		}
	})
	if copied {
		c.After(copiedFor - c.Now().Sub(at))
	}
	if b.Clicked() {
		c.WriteClipboard(text())
		a.k.st.copied[id] = c.Now()
		c.Announce("Copied")
	}
}

// insertKey and actionKey key a card's column and action buttons.
type insertKey struct{ i int }
type actionKey struct{ i int }
