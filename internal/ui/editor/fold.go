package editor

import (
	"slices"
	"sort"
	"strconv"
	"strings"

	"dgopher/internal/sqltext"

	"github.com/egoist/mygo/ui"
)

// foldMark stands in the editor for the lines of a folded block.
const foldMark = " ⋯"

// block is a part of the text that folds: runes [from, to), from the end
// of its first line to the end of its last, which folding hides.
type block struct{ from, to int }

// blocks are the parts of a text that fold, in order: statements, groups
// in parentheses and block comments that take more than one line.
func blocks(text string, d sqltext.Dialect) []block {
	t := []rune(text)
	var out []block
	add := func(start, end int) {
		end = min(end, len(t))
		first := lineEnd(t, start)
		last := lineEnd(t, max(end-1, start))
		if first < last {
			out = append(out, block{first, last})
		}
	}
	for _, st := range sqltext.SplitWith(text, d, sqltext.SplitOptions{}) {
		add(st.Start, st.End)
	}
	var open []int
	for _, tok := range sqltext.Tokenize(text, d) {
		switch {
		case tok.Kind == sqltext.Comment:
			add(tok.Start, tok.End)
		case tok.Kind == sqltext.Punct && tok.Start < len(t) && t[tok.Start] == '(':
			open = append(open, tok.Start)
		case tok.Kind == sqltext.Punct && tok.Start < len(t) && t[tok.Start] == ')' && len(open) > 0:
			add(open[len(open)-1], tok.End)
			open = open[:len(open)-1]
		}
	}
	slices.SortFunc(out, func(a, b block) int {
		if a.from != b.from {
			return a.from - b.from
		}
		return b.to - a.to // the outer first
	})
	return slices.Compact(out)
}

// folding maps between the text and what the editor shows of it, the
// folded blocks' lines hidden: folds in order, none inside another.
type folding []block

// add folds a block, the folds inside it taken in.
func (f folding) add(b block) folding {
	f = slices.DeleteFunc(f, func(o block) bool { return o.from >= b.from && o.to <= b.to })
	if slices.ContainsFunc(f, func(o block) bool { return o.from <= b.from && o.to >= b.to }) {
		return f // inside one folded already
	}
	f = append(f, b)
	slices.SortFunc(f, func(a, b block) int { return a.from - b.from })
	return f
}

// shown is the text as the editor shows it.
func (f folding) shown(t []rune) []rune {
	if len(f) == 0 {
		return t
	}
	var out []rune
	at := 0
	for _, b := range f {
		out = append(out, t[at:b.from]...)
		out = append(out, []rune(foldMark)...)
		at = b.to
	}
	return append(out, t[at:]...)
}

// toShown is where a rune of the text shows: one hidden shows at its
// fold's mark.
func (f folding) toShown(at int) int {
	shift := 0
	mark := len([]rune(foldMark))
	for _, b := range f {
		switch {
		case at <= b.from:
			return at - shift
		case at < b.to:
			return b.from - shift
		}
		shift += b.to - b.from - mark
	}
	return at - shift
}

// toText is the rune of the text a place shown is: on a fold's mark, the
// end of its block, past it, or its start, before it.
func (f folding) toText(at int, after bool) int {
	shift := 0
	mark := len([]rune(foldMark))
	for _, b := range f {
		from := b.from - shift
		switch {
		case at <= from:
			return at + shift
		case at < from+mark:
			if after {
				return b.to
			}
			return b.from
		}
		shift += b.to - b.from - mark
	}
	return at + shift
}

// hides reports whether a rune of the text is folded away.
func (f folding) hides(at int) bool {
	return slices.ContainsFunc(f, func(b block) bool { return at > b.from && at < b.to })
}

// edited is the folds after the text changed in runes [from, to), which
// now hold n runes: folds after it move, and one it touches opens.
func (f folding) edited(from, to, n int) folding {
	var out folding
	for _, b := range f {
		switch {
		case b.to <= from:
			out = append(out, b)
		case b.from >= to:
			out = append(out, block{b.from + n - (to - from), b.to + n - (to - from)})
		}
	}
	return out
}

// Folded reports whether blocks of the editor are folded.
func (e *Editor) Folded() bool { return len(e.folds) > 0 }

// foldable are the blocks of the text that fold, read again as it
// changes.
func (e *Editor) foldable() []block {
	if e.blocksOf != e.Text || e.blocks == nil {
		e.blocksOf, e.blocks = e.Text, blocks(e.Text, e.Dialect)
	}
	return e.blocks
}

// blockAt is the innermost block holding a rune, or starting on its
// line; ok is false for none.
func (e *Editor) blockAt(at int) (block, bool) {
	t := []rune(e.Text)
	var found block
	ok := false
	for _, b := range e.foldable() {
		if lineStart(t, b.from) <= at && at <= b.to && (!ok || b.to-b.from < found.to-found.from) {
			found, ok = b, true
		}
	}
	return found, ok
}

// Fold folds the innermost block the caret is in.
func (e *Editor) Fold() {
	if b, ok := e.blockAt(e.SelEnd); ok {
		e.startFolding()
		e.folds = e.folds.add(b)
	}
}

// Unfold opens the fold the caret is in, or on the first line of.
func (e *Editor) Unfold() {
	t := []rune(e.Text)
	e.folds = slices.DeleteFunc(e.folds, func(b block) bool { return lineStart(t, b.from) <= e.SelEnd && e.SelEnd <= b.to })
}

// FoldAll folds every block, the outermost.
func (e *Editor) FoldAll() {
	e.startFolding()
	for _, b := range e.foldable() {
		e.folds = e.folds.add(b)
	}
}

// UnfoldAll opens every fold.
func (e *Editor) UnfoldAll() { e.folds = nil }

// startFolding leaves the carets but the editor's own, which folding does
// not keep.
func (e *Editor) startFolding() {
	e.ClearCursors()
	e.foldedText = e.Text
}

// syncFolds brings the folds up to date as the editor is built: what the
// user typed in what shows goes into the text; Vim, several carets and a
// text the app changed open the folds; a selection set inside a fold
// opens it. As the folds change, the selection is set again where it is
// in the text, which the text area, given another text, would not keep.
// It reports whether the editor shows folded.
func (e *Editor) syncFolds() bool {
	if len(e.builtFolds) > 0 && e.shown != e.builtShown && e.Text == e.foldedText {
		e.typedFolded()
	}
	if e.Vim != nil || len(e.cursors) > 0 || e.Text != e.foldedText {
		e.folds = nil
	}
	if p := e.PendingSel; p != nil {
		e.folds = slices.DeleteFunc(e.folds, func(b block) bool {
			return p[0] > b.from && p[0] < b.to || p[1] > b.from && p[1] < b.to
		})
	}
	changed := !slices.Equal(e.folds, e.builtFolds)
	if changed && e.PendingSel == nil {
		e.PendingSel = &[2]int{e.SelStart, e.SelEnd}
	}
	if changed {
		e.builtFolds = slices.Clone(e.folds)
	}
	if len(e.folds) == 0 {
		return false
	}
	// Built again only as the text or the folds change: it runs each frame.
	if changed || e.Text != e.shownOf || e.shown != e.builtShown {
		e.shown = string(e.folds.shown([]rune(e.Text)))
		e.builtShown, e.shownOf = e.shown, e.Text
		e.foldedGutter = e.foldedNumbers()
	}
	return true
}

// foldedNumbers are the gutter's numbers while folded: the text's numbers
// of the lines that show.
func (e *Editor) foldedNumbers() string {
	var b strings.Builder
	text := e.textLines().starts
	for i, at := range e.viewLines().starts {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strconv.Itoa(lineIndex(text, e.folds.toText(at, false)) + 1))
	}
	return b.String()
}

// typedFolded puts in the text what the text area typed in what it shows
// folded, which it writes there between frames: typed over a fold's mark,
// it takes the fold's lines with it. The caret goes after what was typed.
func (e *Editor) typedFolded() {
	before, after := []rune(e.builtShown), []rune(e.shown)
	p := 0
	for p < len(before) && p < len(after) && before[p] == after[p] {
		p++
	}
	s := 0
	for s < len(before)-p && s < len(after)-p && before[len(before)-1-s] == after[len(after)-1-s] {
		s++
	}
	from := e.builtFolds.toText(p, false)
	to := from
	if end := len(before) - s; end > p {
		to = e.builtFolds.toText(end, true)
	}
	ins := after[p : len(after)-s]
	t := []rune(e.Text)
	e.Text = string(slices.Concat(t[:from], ins, t[to:]))
	e.builtFolds = e.builtFolds.edited(from, to, len(ins))
	e.folds = e.folds.edited(from, to, len(ins))
	e.foldedText = e.Text
	e.SelStart, e.SelEnd = from+len(ins), from+len(ins)
}

// lineStarts are the runes the lines of a text start at.
func lineStarts(t []rune) []int {
	out := []int{0}
	for i, r := range t {
		if r == '\n' {
			out = append(out, i+1)
		}
	}
	return out
}

// lineCache holds the line starts of a text, read again as it changes.
type lineCache struct {
	text   string
	starts []int
	runes  int
}

// of is the cache for a text.
func (c *lineCache) of(text string) *lineCache {
	if c.starts == nil || c.text != text {
		t := []rune(text)
		c.text, c.starts, c.runes = text, lineStarts(t), len(t)
	}
	return c
}

// lineCol is the line and column, 0-based, of a rune, as lineCol has
// them: a rune outside the text is at its end.
func (c *lineCache) lineCol(at int) (int, int) {
	if at < 0 || at > c.runes {
		at = c.runes
	}
	line := lineIndex(c.starts, at)
	return line, at - c.starts[line]
}

// textLines are the lines of the text.
func (e *Editor) textLines() *lineCache { return e.textLineCache.of(e.Text) }

// viewLines are the lines of what the editor shows.
func (e *Editor) viewLines() *lineCache {
	if len(e.folds) == 0 {
		return e.textLines()
	}
	return e.shownLineCache.of(e.shown)
}

// lineIndex is the line, 0-based, of lines starting at starts that a rune
// is on.
func lineIndex(starts []int, at int) int {
	return max(sort.SearchInts(starts, at+1)-1, 0)
}

// toView is where a rune of the text is in what the editor shows.
func (e *Editor) toView(at int) int {
	if len(e.folds) == 0 {
		return at
	}
	return e.folds.toShown(at)
}

// viewText is what the editor shows: the text, folded.
func (e *Editor) viewText() string {
	if len(e.folds) == 0 {
		return e.Text
	}
	return e.shown
}

// ViewLine is the line, 0-based, of what the editor shows that a rune of
// the text is on.
func (e *Editor) ViewLine(at int) int {
	line, _ := e.viewLines().lineCol(e.toView(at))
	return line
}

// textLineOf is the line of the text that a line the editor shows starts
// on.
func (e *Editor) textLineOf(view int) int {
	starts := e.viewLines().starts
	line, _ := e.textLines().lineCol(e.folds.toText(starts[max(0, min(view, len(starts)-1))], false))
	return line
}

// foldRanges are the text's colored runs as they show folded: those in a
// fold left out, and the marks muted.
func (e *Editor) foldRanges(ranges []ui.TextRange, muted ui.Color) []ui.TextRange {
	if len(e.folds) == 0 {
		return ranges
	}
	// The same runs, folds and color as the last frame's give its runs.
	if e.rangesOut != nil && e.rangesMuted == muted && slices.Equal(e.rangesFolds, e.folds) && slices.Equal(e.rangesIn, ranges) {
		return e.rangesOut
	}
	out := []ui.TextRange{}
	for _, r := range ranges {
		if e.folds.hides(r.Start) && e.folds.hides(max(r.End-1, r.Start)) {
			continue
		}
		r.Start, r.End = e.folds.toShown(r.Start), e.folds.toShown(r.End)
		if r.End > r.Start {
			out = append(out, r)
		}
	}
	mark := len([]rune(foldMark))
	for _, b := range e.folds {
		at := e.folds.toShown(b.from)
		out = append(out, ui.TextRange{Start: at, End: at + mark, Color: muted})
	}
	slices.SortFunc(out, func(a, b ui.TextRange) int { return a.Start - b.Start })
	// CompactFunc passes a run, then the one before it: a run that starts
	// inside the one before goes.
	out = slices.CompactFunc(out, func(run, before ui.TextRange) bool { return run.Start < before.End })
	// A copy of the runs: the highlighting's are rewritten in place.
	e.rangesIn, e.rangesFolds, e.rangesMuted, e.rangesOut = slices.Clone(ranges), slices.Clone(e.folds), muted, out
	return out
}

// foldMarkers are the gutter's markers, a line each of what shows: ▾ on a
// block's first line, ▸ on a folded one's; and the blocks by those lines.
// They are kept until the text or the folds change.
func (e *Editor) foldMarkers() (string, map[int]block) {
	if e.markersAt != nil && e.markersOf == e.Text && slices.Equal(e.markersFolds, e.folds) {
		return e.markers, e.markersAt
	}
	text, view := e.textLines().starts, e.viewLines().starts
	at := map[int]block{}
	for _, b := range e.foldable() {
		line := lineIndex(view, e.toView(text[lineIndex(text, b.from)]))
		if o, ok := at[line]; !ok || b.to-b.from > o.to-o.from {
			at[line] = b // the outermost
		}
	}
	var sb strings.Builder
	for i := range view {
		if i > 0 {
			sb.WriteByte('\n')
		}
		if b, ok := at[i]; ok {
			if slices.Contains(e.folds, b) {
				sb.WriteString("▸")
			} else {
				sb.WriteString("▾")
			}
		}
	}
	e.markersOf, e.markersFolds, e.markers, e.markersAt = e.Text, slices.Clone(e.folds), sb.String(), at
	return e.markers, at
}

// toggleFold folds the block on a line of what shows, or opens it.
func (e *Editor) toggleFold(b block) {
	if i := slices.Index(e.folds, b); i >= 0 {
		e.folds = slices.Delete(e.folds, i, i+1)
		return
	}
	e.startFolding()
	e.folds = e.folds.add(b)
}
