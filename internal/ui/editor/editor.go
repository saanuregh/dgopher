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
	// The line numbers of a text of gutterLines lines.
	gutterLines int
	gutter      string
	longest     int // runes of the longest line

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
	// Marks color runs over the highlighting, as the matches of a find.
	Marks []ui.TextRange
	// CurrentLines are the first and last lines, 0-based, of the
	// statement a run would send, shown behind them when hasCurrent.
	CurrentLines [2]int
	HasCurrent   bool
	// Problems are marked with a wavy line under their text.
	Problems []Problem
}

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

// lineCol is the line and column, 0-based, in runes, of a rune offset.
func lineCol(text string, at int) (line, col int) {
	i := 0
	for _, r := range text {
		if i == at {
			break
		}
		if r == '\n' {
			line, col = line+1, 0
		} else {
			col++
		}
		i++
	}
	return line, col
}

// drawProblems draws a wavy line under the first line of each problem's
// text: the font is monospaced and lines do not wrap, so a rune's column
// is where it shows.
func (e *Editor) drawProblems(p *ui.Painter, r ui.Rect, textX, lh float32, c ui.Color) {
	for _, pr := range e.Problems {
		line, col := lineCol(e.Text, pr.Start)
		endLine, endCol := lineCol(e.Text, max(pr.End, pr.Start+1))
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
	n := strings.Count(e.Text, "\n") + 1
	if n != e.gutterLines || e.gutter == "" {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			if i > 1 {
				b.WriteByte('\n')
			}
			b.WriteString(strconv.Itoa(i))
		}
		e.gutterLines, e.gutter = n, b.String()
	}
	e.longest = 0
	for line := range strings.SplitSeq(e.Text, "\n") {
		e.longest = max(e.longest, utf8.RuneCountInString(line))
	}
	return e.gutter
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
	numbers := e.lineNumbers()
	gutterW := e.charW*float32(max(4, len(strconv.Itoa(e.gutterLines)))) + 22
	ui.ScrollBoth(c).TrackScroll(&e.Scroll).Grow(1).Background(pal.EditorBg).Draw(func(p *ui.Painter, r ui.Rect) {
		// The gutter's color down the whole height, below short texts too.
		p.Fill(ui.Rect{X: r.X, Y: r.Y, W: gutterW, H: r.H}, pal.Gutter, 0)
		// The statement a run would send, across the whole width.
		if e.HasCurrent {
			first, last := e.CurrentLines[0], e.CurrentLines[1]
			top := r.Y + 8 + float32(first)*lh - e.Scroll.Y
			p.Fill(ui.Rect{X: r.X + gutterW, Y: top, W: r.W - gutterW, H: float32(last-first+1) * lh}, pal.CurrentStatement, 0)
		}
		e.drawProblems(p, r, r.X+gutterW+12, lh, c.Theme().Danger)
	}).Children(func() {
		ui.Row(c).AlignItems(ui.Stretch).MinHeightPercent(100).MinWidthPercent(100).Children(func() {
			ui.Text(c, numbers).Font(widgets.MonoFont).FontSize(fontSize).FixedLineHeight(lh).
				TextColor(pal.LineNumber).TextAlign(ui.End).Padding(8, 10, 8, 12).
				Width(gutterW).Unselectable()
			area = ui.TextAreaBase(c, &e.Text).Font(widgets.MonoFont).FontSize(fontSize).FixedLineHeight(lh).
				NoWrap().Padding(8, 12).Grow(1).MinWidth(float32(e.longest+2)*e.charW + 24).Label("SQL editor")
			area.TextRanges(overlay(e.highlight(pal), e.Marks)...)
			if e.KeyHook != nil {
				area.HandleInput(func(ev ui.InputEvent) bool {
					return ev.Kind == ui.InputKeyDown && e.KeyHook(ev.Mods, ev.Key)
				})
			}
			if e.WantFocus {
				// Asked until it holds: the frame that builds a new tab
				// may not be the one that settles the focus.
				area.Focus()
				if area.Focused() {
					e.WantFocus = false
				}
			}
			if e.PendingSel != nil {
				area.SetTextSelection(e.PendingSel[0], e.PendingSel[1])
				e.PendingSel = nil
			}
			e.SelStart, e.SelEnd = area.TextSelection()
			e.HasFocus = area.Focused()
		})
	})
	return area
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
	caret := e.SelEnd
	line, col := 0, 0
	i := 0
	for _, r := range e.Text {
		if i == caret {
			break
		}
		if r == '\n' {
			line++
			col = 0
		} else {
			col++
		}
		i++
	}
	gutter := e.charW*float32(max(4, len(strconv.Itoa(e.gutterLines)))) + 22
	return gutter + 12 + float32(col)*e.charW, 8 + float32(line+1)*fontSize*LineHeight
}

// Replace replaces runes [start, end) of the text and puts the caret
// after what was inserted.
func (e *Editor) Replace(start, end int, s string) {
	r := []rune(e.Text)
	start, end = max(0, min(start, len(r))), max(0, min(end, len(r)))
	e.Text = string(r[:start]) + s + string(r[end:])
	at := start + utf8.RuneCountInString(s)
	e.PendingSel = &[2]int{at, at}
}

// overlay puts runs over others, which it cuts where they meet: text
// ranges may not overlap.
func overlay(base, top []ui.TextRange) []ui.TextRange {
	if len(top) == 0 {
		return base
	}
	out := make([]ui.TextRange, 0, len(base)+len(top))
	for _, b := range base {
		parts := []ui.TextRange{b}
		for _, t := range top {
			var next []ui.TextRange
			for _, p := range parts {
				if t.End <= p.Start || t.Start >= p.End {
					next = append(next, p)
					continue
				}
				if p.Start < t.Start {
					left := p
					left.End = t.Start
					next = append(next, left)
				}
				if t.End < p.End {
					right := p
					right.Start = t.End
					next = append(next, right)
				}
			}
			parts = next
		}
		out = append(out, parts...)
	}
	out = append(out, top...)
	slices.SortFunc(out, func(x, y ui.TextRange) int { return x.Start - y.Start })
	return out
}

// LineOf is the 0-based line of a rune offset.
func LineOf(text string, at int) int {
	line := 0
	for i, r := range []rune(text) {
		if i >= at {
			break
		}
		if r == '\n' {
			line++
		}
	}
	return line
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
	for from := 0; ; {
		i := strings.Index(lt[from:], lq)
		if i < 0 {
			return out
		}
		at := from + i
		out = append(out, utf8.RuneCountInString(lt[:at]))
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
