package editor

import (
	"slices"
	"sort"

	"github.com/egoist/mygo/ui"
)

// span is a range of runes, a caret where it is empty.
type span struct{ start, end int }

// HasCursors reports whether the editor has carets besides its own.
func (e *Editor) HasCursors() bool { return len(e.cursors) > 0 }

// ClearCursors leaves the editor with its own caret alone.
func (e *Editor) ClearCursors() { e.cursors, e.waiting = nil, false }

// primary is the text area's selection, in order.
func (e *Editor) primary() span {
	return span{min(e.SelStart, e.SelEnd), max(e.SelStart, e.SelEnd)}
}

// AddCursor puts a caret on the line above the primary's, or below for a
// positive step, at its column, where the primary moves, keeping its
// place as a caret.
func (e *Editor) AddCursor(step int) {
	t := []rune(e.Text)
	at := e.SelEnd
	start := lineStart(t, at)
	line := lineAt(t, at, step)
	if line == start {
		return // no line there
	}
	next := min(line+at-start, lineEnd(t, line))
	e.cursors = append(e.cursors, e.primary())
	e.wordSearch = false
	e.setPrimary(span{next, next})
	e.mergeCursors()
}

// SelectNext selects the word at the caret; with a selection, the next
// time its text occurs, as the primary, the selection kept as a cursor.
// From a word, only the same word whole is found again.
func (e *Editor) SelectNext() {
	t := []rune(e.Text)
	p := e.primary()
	if p.start == p.end {
		if from, to, ok := textObject(t, p.start, false, 'w'); ok && class(t[from], false) == 1 {
			e.setPrimary(span{from, to})
			e.wordSearch = true
		}
		return
	}
	word := t[p.start:p.end]
	at, ok := e.find(t, word, p.end)
	if !ok || e.covered(at) {
		return
	}
	e.cursors = append(e.cursors, p)
	e.setPrimary(span{at, at + len(word)})
}

// SelectAll selects every time the selection's text occurs, or the word
// at the caret's, whole.
func (e *Editor) SelectAll() {
	if e.primary().start == e.primary().end {
		e.SelectNext()
	}
	p := e.primary()
	if p.start == p.end {
		return
	}
	t := []rune(e.Text)
	word := slices.Clone(t[p.start:p.end])
	e.cursors = nil
	for at := 0; ; {
		i, ok := e.find(t, word, at)
		if !ok || i < at {
			break
		}
		if i != p.start {
			e.cursors = append(e.cursors, span{i, i + len(word)})
		}
		at = i + len(word)
	}
	e.cursorText = e.Text
}

// find finds a text at or after rune at, round to the start: a word, as
// the selection began from one, only whole.
func (e *Editor) find(t, word []rune, at int) (int, bool) {
	whole := func(i int) bool {
		return !e.wordSearch || (i == 0 || class(t[i-1], false) != 1) && (i+len(word) >= len(t) || class(t[i+len(word)], false) != 1)
	}
	for _, from := range []int{at, 0} {
		for i := from; i+len(word) <= len(t); i++ {
			if slices.Equal(t[i:i+len(word)], word) && whole(i) {
				return i, true
			}
		}
	}
	return 0, false
}

// covered reports whether a cursor or the primary starts at a rune.
func (e *Editor) covered(at int) bool {
	return e.primary().start == at || slices.ContainsFunc(e.cursors, func(c span) bool { return c.start == at })
}

// setPrimary moves the text area's selection, the cursors staying with
// the text as it is.
func (e *Editor) setPrimary(s span) {
	e.PendingSel = &[2]int{s.start, s.end}
	e.SelStart, e.SelEnd = s.start, s.end
	e.cursorText, e.settingSel, e.lastSel = e.Text, &s, s
}

// cursorKeys moves every caret as the text area moves its own, for the
// keys that move it: the text area's moves the primary alone.
func (e *Editor) cursorKeys(ev ui.InputEvent) bool {
	if ev.Kind != ui.InputKeyDown || ev.Mods != 0 || len(e.cursors) == 0 {
		return false
	}
	if ev.Key == ui.KeyEscape {
		e.cursors = nil
		return true
	}
	t := []rune(e.Text)
	move := func(c span) span {
		at := c.end
		switch ev.Key {
		case ui.KeyLeft:
			if c.start != c.end {
				return span{c.start, c.start}
			}
			at = max(at-1, 0)
		case ui.KeyRight:
			if c.start != c.end {
				return span{c.end, c.end}
			}
			at = min(at+1, len(t))
		case ui.KeyUp, ui.KeyDown:
			step := 1
			if ev.Key == ui.KeyUp {
				step = -1
			}
			start := lineStart(t, at)
			line := lineAt(t, at, step)
			if line == start {
				return span{at, at}
			}
			at = min(line+at-start, lineEnd(t, line))
		case ui.KeyHome:
			at = lineStart(t, at)
		case ui.KeyEnd:
			at = lineEnd(t, at)
		default:
			return c
		}
		return span{at, at}
	}
	switch ev.Key {
	case ui.KeyLeft, ui.KeyRight, ui.KeyUp, ui.KeyDown, ui.KeyHome, ui.KeyEnd:
	default:
		return false
	}
	for i, c := range e.cursors {
		e.cursors[i] = move(c)
	}
	e.setPrimary(move(e.primary()))
	e.mergeCursors()
	return true
}

// mergeCursors drops a cursor where the primary or another is, or that
// overlaps them.
func (e *Editor) mergeCursors() {
	meet := func(a, b span) bool { return a == b || a.start < b.end && b.start < a.end }
	kept := []span{e.primary()}
	for _, c := range e.cursors {
		if !slices.ContainsFunc(kept, func(k span) bool { return meet(c, k) }) {
			kept = append(kept, c)
		}
	}
	e.cursors = kept[1:]
}

// replicate types at every cursor what the text area typed at the
// primary, from was, as the primary was then, to the text it holds now.
func (e *Editor) replicate(was string, primary span) {
	before, after := []rune(was), []rune(e.Text)
	// The change, as near the primary as it reads: typing "a" after an
	// "a" changes the text at the caret, not after the next one.
	p := 0
	for p < primary.start && p < len(before) && p < len(after) && before[p] == after[p] {
		p++
	}
	s := 0
	for s < len(before)-max(p, primary.end) && s < len(after)-p && before[len(before)-1-s] == after[len(after)-1-s] {
		s++
	}
	e.atCursors(before, primary, span{p, len(before) - s}, after[p:len(after)-s])
}

// TypeAtCarets replaces runes [start, end) of the text, about the primary
// caret, with s, and the like about every other caret, as completion
// types: without others, as Replace.
func (e *Editor) TypeAtCarets(start, end int, s string) {
	if len(e.cursors) == 0 {
		e.Replace(start, end, s)
		return
	}
	e.atCursors([]rune(e.Text), e.primary(), span{start, end}, []rune(s))
}

// atCursors makes a change of the text before, the primary's runes ch
// replaced by ins, and the like about each cursor, from their ranges.
// A cursor whose change would run off the text keeps its place; one
// whose change meets another's goes.
func (e *Editor) atCursors(before []rune, primary, ch span, ins []rune) {
	d0, d1 := ch.start-primary.start, ch.end-primary.end
	type edit struct {
		span
		primary bool
	}
	edits := []edit{{ch, true}}
	var kept []span
	for _, c := range e.cursors {
		if from, to := c.start+d0, c.end+d1; from >= 0 && to <= len(before) && from <= to {
			edits = append(edits, edit{span{from, to}, false})
		} else {
			kept = append(kept, c)
		}
	}
	slices.SortStableFunc(edits, func(a, b edit) int { return a.start - b.start })
	var out []rune
	var carets, applied []span // applied: the edits made, as they were in before
	at, primaryAt := 0, -1
	for _, ed := range edits {
		if len(applied) > 0 && ed.start < at || len(applied) > 0 && ed.start == applied[len(applied)-1].start {
			if ed.primary {
				primaryAt = len(out) // swallowed: its caret is the change's
			}
			continue
		}
		out = append(append(out, before[at:ed.start]...), ins...)
		if ed.primary {
			primaryAt = len(out)
		} else {
			carets = append(carets, span{len(out), len(out)})
		}
		applied = append(applied, ed.span)
		at = ed.end
	}
	out = append(out, before[at:]...)
	// A cursor kept moves by the changes made before it.
	for _, c := range kept {
		shift := 0
		for _, a := range applied {
			if a.end <= c.start {
				shift += len(ins) - (a.end - a.start)
			}
		}
		carets = append(carets, span{c.start + shift, c.end + shift})
	}
	e.Text = string(out)
	e.cursors = carets
	e.setPrimary(span{primaryAt, primaryAt})
	e.mergeCursors()
}

// drawCursors draws the cursors besides the primary: their selections,
// and their carets, with the text's top left at x, y.
func (e *Editor) drawCursors(p *ui.Painter, x, y, lh float32, caret, selection ui.Color) {
	t := []rune(e.Text)
	starts := []int{0} // of each line
	for i, r := range t {
		if r == '\n' {
			starts = append(starts, i+1)
		}
	}
	at := func(r int) (float32, float32) {
		line := max(sort.SearchInts(starts, r+1)-1, 0)
		return x + float32(r-starts[line])*e.charW, y + float32(line)*lh
	}
	for _, c := range e.cursors {
		for l := lineStart(t, c.start); c.end > c.start; {
			from, to := max(l, c.start), min(lineEnd(t, l), c.end)
			px, py := at(from)
			p.Fill(ui.Rect{X: px, Y: py, W: max(float32(to-from)*e.charW, 2), H: lh}, selection, 0)
			next := lineEnd(t, l) + 1
			if next >= c.end || next > len(t) {
				break
			}
			l = next
		}
		cx, cy := at(min(c.end, len(t)))
		p.Fill(ui.Rect{X: cx, Y: cy, W: 1.5, H: lh}, caret, 0)
	}
}

// followCursors keeps the cursors with what the text area did in the
// frame. The text it types shows in the bound text a frame after its
// caret moves: a caret moved without the text is waited on for a frame,
// and the text typed at the cursors as it comes (View). Typing is typed at each cursor; the primary moved by the
// pointer with Alt adds a cursor where it was, and moved otherwise
// leaves them.
func (e *Editor) followCursors(c *ui.Context, area ui.Element, was string) {
	pressed := area.Pressed() && !e.pressed // the pointer's press, not its drag
	e.pressed = area.Pressed()
	if s := e.settingSel; s != nil {
		// The text area shows a selection it is given a frame later: until
		// then it reads the one before, which the cursors did not leave.
		if e.primary() != *s && e.Text == was {
			e.SelStart, e.SelEnd = s.start, s.end
			return
		}
		e.settingSel = nil
	}
	waiting := false
	switch {
	case e.Text != was:
		if len(e.cursors) > 0 {
			e.replicate(was, e.lastSel)
		}
	case e.primary() == e.lastSel:
	case pressed && c.Modifiers()&ui.Alt != 0:
		e.cursors = append(e.cursors, e.lastSel)
		e.wordSearch = false
		e.mergeCursors()
	case len(e.cursors) > 0 && !e.waiting:
		waiting = true
	default:
		e.cursors, e.wordSearch = nil, false
	}
	e.waiting = waiting
	if !waiting {
		e.lastSel = e.primary()
	}
	e.cursorText = e.Text
}
