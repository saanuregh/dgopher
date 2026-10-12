// Package editor is the SQL editor: a text area with highlighting, line
// numbers, the current statement's band and find.
package editor

import (
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"dgopher/internal/sqltext"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// Editor edits SQL with highlighting and line numbers.
type Editor struct {
	Text    string
	Dialect sqltext.Dialect
	Scroll  ui.ScrollState

	// The highlighting of hlText.
	hlText   string
	hlRanges []ui.TextRange
	// The text has gutterLines lines; gutter numbers gutterFor lines, and
	// foldedGutter the lines that show folded.
	gutterLines, gutterFor int
	gutter, foldedGutter   string
	longest                int // runes of the longest line of longestOf
	longestOf              string

	// gutterTop is the first line, as the editor shows it, that the
	// gutter drew in the last frame.
	gutterTop int
	// The line starts of the line numbers and of the fold markers.
	numberLines, markerLines lineSlicer

	// The selection as the last frame read it, in runes.
	SelStart, SelEnd int
	// PendingSel moves the selection in the next frame.
	PendingSel *[2]int
	WantFocus  bool
	HasFocus   bool // the text area had the keyboard focus in the last frame

	charW float32 // the width of a character of the font, for the caret

	// KeyHook takes keys before the text area does, as a completion
	// popup's arrows and Enter; it reports whether it took the key.
	KeyHook func(ui.Modifiers, ui.Key) bool
	// Pasted is set as the user pastes text, for the owner to tell the
	// change from typing; the owner clears it.
	Pasted bool
	// Marks color runs over the highlighting, as the matches of a find.
	Marks []ui.TextRange
	// CurrentLines are the first and last lines, 0-based, of the
	// statement a run would send, shown behind them when hasCurrent.
	CurrentLines [2]int
	HasCurrent   bool
	// RunLines are the first lines, 0-based, of the text's statements,
	// each with a button in the gutter that calls Run with its line, shown
	// with or without the focus; RunTip names it.
	RunLines []int
	Run      func(line int)
	RunTip   string
	// Problems are marked with a wavy line under their text.
	Problems []Problem
	// Vim, when set, edits with Vim's keys.
	Vim *Vim
	// cursors are carets and selections besides the text area's own, the
	// primary: what is typed at the primary is typed at each of them
	// alike. cursorText is the text they are in; another, as the app
	// made, leaves them.
	cursors    []span
	cursorText string
	// settingSel is the primary given to the text area, which it shows a
	// frame later; lastSel is the primary the cursors go with, and waiting
	// is set while a moved primary waits for the text it typed.
	settingSel *span
	lastSel    span
	waiting    bool
	// pressed is whether the pointer was pressed on the text in the last
	// frame; wordSearch, whether the primary's selection is a word ⌘D
	// selected, whose occurrences are only the word whole.
	pressed    bool
	wordSearch bool
	// vimSel is the selection Vim last gave the text area, by which a
	// caret the pointer moved is told from Vim's own.
	vimSel [2]int
	// folds are the blocks folded, in the text; while there are any, the
	// text area shows shown, built from foldedText with builtFolds as
	// builtShown, from shownOf: another text is the app's change, another
	// shown the user's typing. blocks are the blocks of blocksOf that fold.
	folds, builtFolds                      folding
	foldedText, shown, builtShown, shownOf string
	blocks                                 []block
	blocksOf                               string
	// The line starts of the text and of shown.
	textLineCache, shownLineCache lineCache
	// The gutter's fold markers, and the blocks by their lines, of
	// markersOf folded as markersFolds.
	markers      string
	markersAt    map[int]block
	markersOf    string
	markersFolds folding
	// The colored runs foldRanges made of rangesIn, folded as rangesFolds.
	rangesIn, rangesOut []ui.TextRange
	rangesFolds         folding
	rangesMuted         ui.Color
	// vimEscape is an Esc that ends typing, taken in the frame after.
	vimEscape bool
	// viewW and viewH are the size the editor showed in, for Vim to keep
	// its caret in view.
	viewW, viewH float32
}

// Typing reports whether keys type in the editor: not in Vim's modes but
// insert, whose changes are commands'.
func (e *Editor) Typing() bool { return e.Vim == nil || e.Vim.Mode == VimInsert }

// Problem is a mistake in the text: Message says what; Fix, when set, is
// the text that replaces its own to mend it.
type Problem struct {
	Start, End int // runes
	Message    string
	Fix        string
}

// ProblemAt is the problem whose text holds a rune offset, as the caret.
func (e *Editor) ProblemAt(at int) (Problem, bool) {
	for _, p := range e.Problems {
		if at >= p.Start && at <= p.End {
			return p, true
		}
	}
	return Problem{}, false
}

// LineCol is the line and column, 0-based, in runes, of a rune offset of
// the text: an offset past the end is at the end.
func (e *Editor) LineCol(at int) (line, col int) { return e.textLines().lineCol(at) }

// drawProblems draws a wavy line under the first line of each problem's
// text: the font is monospaced and lines do not wrap, so a rune's column
// is where it shows.
func (e *Editor) drawProblems(p *ui.Painter, r ui.Rect, textX, lh float32, c ui.Color) {
	view := e.viewLines()
	for _, pr := range e.Problems {
		if len(e.folds) > 0 && e.folds.hides(pr.Start) {
			continue
		}
		line, col := view.lineCol(e.toView(pr.Start))
		endLine, endCol := view.lineCol(e.toView(max(pr.End, pr.Start+1)))
		if endLine != line {
			endCol = col + 1
		}
		x0 := textX + float32(col)*e.charW - e.Scroll.X
		x1 := textX + float32(endCol)*e.charW - e.Scroll.X
		y := r.Y + 8 + float32(line+1)*lh - lh*0.15 - e.Scroll.Y
		var path ui.Path
		path.MoveTo(x0, y)
		for x, up := x0, true; x < x1; x, up = x+2, !up {
			dy := float32(1.5)
			if up {
				dy = -1.5
			}
			path.LineTo(min(x+2, x1), y+dy)
		}
		p.StrokePath(&path, 1, c)
	}
}

const LineHeight = 1.5

// highlight returns the colored runs of the text.
func (e *Editor) highlight(pal *widgets.Palette) []ui.TextRange {
	if e.hlText == e.Text && e.hlRanges != nil {
		return e.hlRanges
	}
	e.hlText = e.Text
	e.hlRanges = e.hlRanges[:0]
	if len(e.Text) > 2_000_000 {
		return e.hlRanges // too large to color as it is typed
	}
	for _, t := range sqltext.Tokenize(e.Text, e.Dialect) {
		col, ok := pal.Syntax[t.Kind]
		if !ok {
			continue
		}
		r := ui.TextRange{Start: t.Start, End: t.End, Color: col}
		if t.Kind == sqltext.Keyword {
			r.Weight = 600
		}
		e.hlRanges = append(e.hlRanges, r)
	}
	if e.hlRanges == nil {
		e.hlRanges = []ui.TextRange{}
	}
	return e.hlRanges
}

func (e *Editor) lineNumbers() string {
	n := len(e.textLines().starts)
	e.gutterLines = n
	if len(e.folds) > 0 {
		return e.foldedGutter // built with shown
	}
	if n != e.gutterFor || e.gutter == "" {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			if i > 1 {
				b.WriteByte('\n')
			}
			b.WriteString(strconv.Itoa(i))
		}
		e.gutterFor, e.gutter = n, b.String()
	}
	if e.longestOf != e.Text {
		e.longest, e.longestOf = 0, e.Text
		for line := range strings.SplitSeq(e.Text, "\n") {
			e.longest = max(e.longest, utf8.RuneCountInString(line))
		}
	}
	return e.gutter
}

// viewLineOf is the line, as the editor shows it, of a line of the text.
func (e *Editor) viewLineOf(line int, folded bool) int {
	if !folded {
		return line
	}
	starts := e.textLines().starts
	return e.ViewLine(starts[max(0, min(line, len(starts)-1))])
}

// markersWidth is the width of the gutter's column of fold markers.
func (e *Editor) markersWidth() float32 { return e.charW + 8 }

// gutterWidth is the width of the gutter: its line numbers, and the fold
// markers.
func (e *Editor) gutterWidth() float32 {
	return e.charW*float32(max(4, len(strconv.Itoa(e.gutterLines)))) + 22 + e.markersWidth()
}

// View builds the editor, which grows in its parent; it returns the text
// area.
func (e *Editor) View(c *ui.Context, fontSize float32) ui.Element {
	pal := widgets.PaletteOf(c)
	lh := fontSize * LineHeight
	if e.charW == 0 {
		w, _ := c.MeasureText(0, ui.Span{Text: strings.Repeat("0", 20), Font: widgets.MonoFont, Size: fontSize})
		e.charW = w / 20
	}
	var area ui.Element
	folded := e.syncFolds()
	// The gutter's lines in sight alone, as a long text's would be laid
	// out each frame: from top, which its top padding puts in place.
	top, bottom := e.gutterRange(lh)
	e.gutterTop = top
	numbers := e.numberLines.lines(e.lineNumbers(), top, bottom)
	markers, foldAt := e.foldMarkers()
	markers = e.markerLines.lines(markers, top, bottom)
	markersW := e.markersWidth()
	gutterW := e.gutterWidth()
	textX := gutterW + 12
	// Vim's modes but insert take the keys in the container, which takes
	// the text typed as keys, where the text area would type it.
	vim := e.Vim
	commands := vim != nil && vim.Mode != VimInsert
	switch {
	case vim != nil:
		e.cursors = nil
	case e.Text != e.cursorText && e.waiting && len(e.cursors) > 0:
		// The text the text area typed as its caret moved, which it puts
		// in the bound text between frames: typed at the cursors too.
		e.replicate(e.cursorText, e.lastSel)
		e.waiting = false
	case e.Text != e.cursorText:
		e.cursors = nil // the app's change, which they are not in
	}
	// The text before the text area builds, which puts there what it
	// typed: the change it makes is typed at the cursors too.
	was := e.Text
	box := ui.ScrollBoth(c).TrackScroll(&e.Scroll).Grow(1).Background(pal.EditorBg).Draw(func(p *ui.Painter, r ui.Rect) {
		e.viewW, e.viewH = r.W, r.H
		// The gutter's color down the whole height, below short texts too.
		p.Fill(ui.Rect{X: r.X, Y: r.Y, W: gutterW, H: r.H}, pal.Gutter, 0)
		// The statement a run would send, across the whole width.
		if e.HasCurrent {
			first, last := e.viewLineOf(e.CurrentLines[0], folded), e.viewLineOf(e.CurrentLines[1], folded)
			top := r.Y + 8 + float32(first)*lh - e.Scroll.Y
			p.Fill(ui.Rect{X: r.X + gutterW, Y: top, W: r.W - gutterW, H: float32(last-first+1) * lh}, pal.CurrentStatement, 0)
		}
		e.drawProblems(p, r, r.X+textX, lh, c.Theme().Danger)
		if commands && e.HasFocus {
			e.drawVim(p, r.X+textX-e.Scroll.X, r.Y+8-e.Scroll.Y, lh, c.Theme().Accent)
		}
		if len(e.cursors) > 0 {
			e.drawCursors(p, r.X+textX-e.Scroll.X, r.Y+8-e.Scroll.Y, lh, c.Theme().Text, c.Theme().Selection)
		}
	})
	if vim != nil {
		box.Focusable().Label("SQL editor, Vim keys").HandleInput(func(ev ui.InputEvent) bool { return commands && e.vimInput(ev, lh) })
		if commands {
			line, col := e.LineCol(vim.Caret)
			box.TextCaret(ui.Rect{X: textX + float32(col)*e.charW - e.Scroll.X, Y: 8 + float32(line)*lh - e.Scroll.Y, W: e.charW, H: lh})
			if box.Focused() {
				// Vim's Control keys before the app's commands, which on
				// Linux and Windows have the same keys.
				for key, tok := range vimCtrlKeys {
					if c.Shortcut(ui.Ctrl, key) {
						e.vimFeed([]string{tok}, lh)
					}
				}
			}
		}
	}
	box.Children(func() {
		ui.Row(c).AlignItems(ui.Stretch).MinHeightPercent(100).MinWidthPercent(100).Children(func() {
			ui.Text(c, numbers).Font(widgets.MonoFont).FontSize(fontSize).FixedLineHeight(lh).
				TextColor(pal.LineNumber).TextAlign(ui.End).Padding(8+float32(top)*lh, 4, 8, 12).
				Width(gutterW - markersW).Unselectable()
			// A block's marker folds it, or opens it, as it is clicked.
			marks := ui.Text(c, markers).Font(widgets.MonoFont).FontSize(fontSize).FixedLineHeight(lh).
				TextColor(pal.LineNumber).Padding(8+float32(top)*lh, 6, 8, 2).Width(markersW).Unselectable()
			if marks.Clicked() {
				if _, y, ok := marks.PointerPosition(); ok {
					if b, ok := foldAt[int((y-8)/lh)]; ok {
						e.toggleFold(b)
					}
				}
			}
			bound := &e.Text
			if folded {
				bound = &e.shown
			}
			area = ui.TextAreaBase(c, bound).Font(widgets.MonoFont).FontSize(fontSize).FixedLineHeight(lh).
				NoWrap().Padding(8, 12).Grow(1).MinWidth(float32(e.longest+2)*e.charW + 24).Label("SQL editor")
			area.TextRanges(e.foldRanges(overlay(e.highlight(pal), e.Marks), pal.Muted)...)
			area.OnPaste(func(text string) bool {
				e.Pasted = e.Pasted || text != ""
				return false
			})
			area.HandleInput(func(ev ui.InputEvent) bool {
				if ev.Kind != ui.InputKeyDown {
					return false
				}
				if e.KeyHook != nil && e.KeyHook(ev.Mods, ev.Key) || e.cursorKeys(ev) {
					return true
				}
				// Typing ends at Esc, the text area's one key Vim takes: once
				// the text area has typed what came before it, in the frame.
				if vim != nil && vim.Mode == VimInsert && (ev.Mods == 0 && ev.Key == ui.KeyEscape || ev.Mods == ui.Ctrl && ev.Key == ui.KeyBracketLeft) {
					e.vimEscape = true
					return true
				}
				return false
			})
			if e.vimEscape && vim != nil {
				e.vimEscape = false
				_, caret := area.TextSelection()
				vim.Typed([]rune(e.Text), caret)
				e.vimFeed([]string{"<Esc>"}, lh)
			}
			focus := area
			if commands {
				focus = box
			}
			if e.WantFocus {
				// Asked until it holds: the frame that builds a new tab
				// may not be the one that settles the focus.
				focus.Focus()
				if focus.Focused() {
					e.WantFocus = false
				}
			}
			if e.PendingSel != nil {
				area.SetTextSelection(e.toView(e.PendingSel[0]), e.toView(e.PendingSel[1]))
				e.PendingSel = nil
			} else if commands {
				e.vimSync(area.TextSelection())
			}
			switch {
			case commands && area.Focused():
				// A click in the text puts the caret there; the keys stay
				// Vim's.
				box.Focus()
			case vim != nil && vim.Mode == VimInsert && box.Focused():
				area.Focus()
			}
			e.SelStart, e.SelEnd = area.TextSelection()
			if folded {
				// Each end rounded outward, the selection whichever way.
				lo, hi := min(e.SelStart, e.SelEnd), max(e.SelStart, e.SelEnd)
				lo, hi = e.folds.toText(lo, false), e.folds.toText(hi, hi > lo)
				if e.SelStart <= e.SelEnd {
					e.SelStart, e.SelEnd = lo, hi
				} else {
					e.SelStart, e.SelEnd = hi, lo
				}
			}
			e.HasFocus = area.Focused() || vim != nil && box.Focused()
			if vim == nil {
				e.followCursors(c, area, was)
			}
		})
		if e.Run != nil {
			e.runButtons(c, folded, lh, fontSize)
		}
	})
	return area
}

// gutterMargin is how far, in DIPs, the gutter draws beyond the view, as
// the view's height and scrolling are the last frame's.
const gutterMargin = 1000

// gutterRange is the first and the last line, as the editor shows it,
// that the gutter draws: those in sight, and a margin.
func (e *Editor) gutterRange(lh float32) (top, bottom int) {
	top = max(0, int((e.Scroll.Y-8-gutterMargin)/lh))
	bottom = int((e.Scroll.Y+max(e.viewH, gutterMargin)+gutterMargin)/lh) + 1
	return top, bottom
}

// lineSlicer cuts lines out of a text, with its line starts kept until
// the text changes.
type lineSlicer struct {
	of     string
	starts []int
}

// lines are a text's lines from top to bottom, both counted from 0.
func (s *lineSlicer) lines(text string, top, bottom int) string {
	if text != s.of || s.starts == nil {
		s.of, s.starts = text, []int{0}
		for i := range len(text) {
			if text[i] == '\n' {
				s.starts = append(s.starts, i+1)
			}
		}
	}
	if top >= len(s.starts) {
		return ""
	}
	end := len(text)
	if bottom+1 < len(s.starts) {
		end = s.starts[bottom+1] - 1
	}
	return text[s.starts[top]:end]
}

// runButtons draws the statements' run buttons, on their first lines in
// the gutter's margin, before the line numbers: those in sight alone, as
// a long script has many. A statement inside a fold has none.
func (e *Editor) runButtons(c *ui.Context, folded bool, lh, fontSize float32) {
	top := int((e.Scroll.Y - 8) / lh)
	bottom := int((e.Scroll.Y+e.viewH)/lh) + 1
	shown := -1
	for _, line := range e.RunLines {
		view := e.viewLineOf(line, folded)
		if view < top || view == shown || folded && e.textLineOf(view) != line {
			continue
		}
		if view > bottom {
			break
		}
		shown = view
		run := ui.Box(c.Key(line)).Absolute().Left(2).Top(8+float32(view)*lh).Size(14, lh).Center().
			Label(e.RunTip).Tooltip(e.RunTip).Cursor(ui.CursorPointer)
		run.Children(func() { ui.Icon(c, widgets.IconPlay).FontSize(fontSize * 0.85).TextColor(c.Theme().Success) })
		if run.Clicked() {
			e.Run(line)
			e.WantFocus = true
		}
	}
}

// Selection returns the selected text, "" when nothing is selected.
func (e *Editor) Selection() string {
	start, end := min(e.SelStart, e.SelEnd), max(e.SelStart, e.SelEnd)
	if start == end {
		return ""
	}
	r := []rune(e.Text)
	if end > len(r) {
		return ""
	}
	return string(r[start:end])
}

// CaretXY is where the caret is in the editor's content, before its
// scrolling, in DIPs from the content's top left.
func (e *Editor) CaretXY(fontSize float32) (x, y float32) {
	line, col := e.viewLines().lineCol(e.toView(e.SelEnd))
	return e.gutterWidth() + 12 + float32(col)*e.charW, 8 + float32(line+1)*fontSize*LineHeight
}

// Replace replaces runes [start, end) of the text and puts the caret
// after what was inserted.
func (e *Editor) Replace(start, end int, s string) {
	r := []rune(e.Text)
	start, end = max(0, min(start, len(r))), max(0, min(end, len(r)))
	e.Text = string(r[:start]) + s + string(r[end:])
	n := utf8.RuneCountInString(s)
	if len(e.folds) > 0 {
		// The folds it does not touch stay folded.
		e.folds = e.folds.edited(start, end, n)
		e.builtFolds = e.builtFolds.edited(start, end, n)
		e.foldedText = e.Text
	}
	e.PendingSel = &[2]int{start + n, start + n}
}

// overlay puts runs over others, which it cuts where they meet: text
// ranges may not overlap. Sorted by start, each list is walked once: the
// marks of a find are many, and drawn every frame.
func overlay(base, top []ui.TextRange) []ui.TextRange {
	if len(top) == 0 {
		return base
	}
	byStart := func(x, y ui.TextRange) int { return x.Start - y.Start }
	if !slices.IsSortedFunc(base, byStart) {
		base = slices.SortedFunc(slices.Values(base), byStart)
	}
	if !slices.IsSortedFunc(top, byStart) {
		top = slices.SortedFunc(slices.Values(top), byStart)
	}
	out := make([]ui.TextRange, 0, len(base)+len(top))
	j := 0
	for _, b := range base {
		// Those ending before b end before every run after it too.
		for j < len(top) && top[j].End <= b.Start {
			j++
		}
		for k := j; k < len(top) && top[k].Start < b.End && b.Start < b.End; k++ {
			if t := top[k]; b.Start < t.Start {
				left := b
				left.End = t.Start
				out = append(out, left)
			}
			b.Start = max(b.Start, top[k].End)
		}
		if b.Start < b.End {
			out = append(out, b)
		}
	}
	out = append(out, top...)
	slices.SortFunc(out, byStart)
	return out
}

// Find is the find bar of a SQL editor, and its replace row.
type Find struct {
	Open        bool
	Query       string
	Replacing   bool // the replace row shows under the find bar
	Replacement string
	Current     int
	Matches     []int // rune offsets of the matches, for the query and text below
	ForQuery    string
	ForText     string
	Shown       int // the match last selected, -1 for none
}

// FindAll returns the rune offsets where the query appears, ignoring case.
func FindAll(text, query string) []int {
	if query == "" {
		return nil
	}
	lt, lq := strings.ToLower(text), strings.ToLower(query)
	var out []int
	runes := 0 // in lt[:from], counted once through the text
	for from := 0; ; {
		i := strings.Index(lt[from:], lq)
		if i < 0 {
			return out
		}
		at := from + i
		runes += utf8.RuneCountInString(lt[from:at])
		out = append(out, runes)
		runes += utf8.RuneCountInString(lq)
		from = at + len(lq)
	}
}

// ReplaceAt replaces the runes [at, at+n) of the text, for each of the
// rune offsets matches, in order and apart, with s.
func ReplaceAt(text string, matches []int, n int, s string) string {
	r := []rune(text)
	var b strings.Builder
	b.Grow(len(text))
	from := 0
	for _, at := range matches {
		b.WriteString(string(r[from:at]))
		b.WriteString(s)
		from = at + n
	}
	b.WriteString(string(r[from:]))
	return b.String()
}
