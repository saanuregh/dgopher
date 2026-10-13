package editor

import (
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	code "github.com/egoist/mygo/ui/editor"
)

// VimMode is the mode of the editor's Vim key bindings.
type VimMode int

const (
	VimNormal VimMode = iota
	VimInsert
	VimVisual
	VimVisualLine
)

// Vim is the state of the Vim key bindings of an editor: the text it
// works on is the editor's, with its caret, a byte offset, the start of
// the character under the block in normal mode. Motions and edits step
// by characters.
//
// Keys come as tokens: a character typed, or a named key, as "<Esc>",
// "<CR>", "<BS>", "<C-r>", "<Left>".
type Vim struct {
	Mode   VimMode
	Text   string
	Caret  int
	Anchor int // where a visual selection started
	// Message is what the last command says, as a search not found.
	Message string

	// ReadClipboard, WriteClipboard and Save are the editor's; the
	// unnamed register is the clipboard.
	ReadClipboard  func() string
	WriteClipboard func(string)
	Save           func()

	pending []string // tokens of the command being typed
	lines   bool     // the register holds whole lines
	yanked  string   // what the register was last given, as lines tells of
	undo    []vimSnapshot
	redo    []vimSnapshot
	// last is the last change, as its tokens, replayed by '.'; change
	// gathers the change being made, an insert's typing included.
	last, change []string
	replaying    bool
	want         int // the column, in characters, j and k keep, -1 for the caret's
	search       string
	searchBack   bool
	find         [2]rune // the last f, F, t or T, and its character
	prompt       []rune  // a : or / line being typed, its first rune the kind
	typedFrom    string  // the text as typing began, for '.' to repeat it
	// halfPage is how many lines Ctrl+D and Ctrl+U move, 0 for 15.
	halfPage int
}

type vimSnapshot struct {
	text  string
	caret int
}

// NewVim returns the Vim state of an editor, in normal mode.
func NewVim() *Vim { return &Vim{want: -1} }

// Status is what the editor shows of Vim: the mode, or the command being
// typed, or the last message.
func (v *Vim) Status() string {
	switch {
	case v.prompt != nil:
		return string(v.prompt)
	case v.Mode == VimInsert:
		return "-- INSERT --"
	case v.Mode == VimVisual:
		return "-- VISUAL --"
	case v.Mode == VimVisualLine:
		return "-- VISUAL LINE --"
	case len(v.pending) > 0:
		return strings.Join(v.pending, "")
	}
	return v.Message
}

// Selection is the bytes the editor selects: the visual selection, or
// the caret alone.
func (v *Vim) Selection() (start, end int) {
	switch v.Mode {
	case VimVisual:
		lo, hi := min(v.Anchor, v.Caret), max(v.Anchor, v.Caret)
		return lo, nextChar(v.Text, hi)
	case VimVisualLine:
		lo, hi := min(v.Anchor, v.Caret), max(v.Anchor, v.Caret)
		return lineStart(v.Text, lo), min(lineEnd(v.Text, hi)+1, len(v.Text))
	}
	return v.Caret, v.Caret
}

// Feed takes a key: it reports whether Vim took it, or the editor is to,
// as typing in insert mode.
func (v *Vim) Feed(tok string) bool {
	if v.prompt != nil {
		v.promptKey(tok)
		return true
	}
	if v.Mode == VimInsert {
		return v.insertKey(tok)
	}
	v.Message = ""
	v.pending = append(v.pending, tok)
	if v.run(v.pending) {
		v.pending = nil
	}
	if v.Mode == VimInsert {
		v.typedFrom = v.Text
	}
	return true
}

// insertKey takes a key in insert mode: Esc ends it, the rest is typing,
// which the editor does but for a replay.
func (v *Vim) insertKey(tok string) bool {
	if tok == "<Esc>" || tok == "<C-[>" {
		if v.change != nil {
			v.change = append(v.change, "<Esc>")
		}
		v.endChange()
		v.Mode = VimNormal
		if v.Caret > lineStart(v.Text, v.Caret) {
			v.Caret = prevChar(v.Text, v.Caret)
		}
		return true
	}
	if !v.replaying {
		return false
	}
	v.typeKey(tok)
	return true
}

// typeKey types a key as the editor would: as '.' repeats typing, or
// keys that come with the one that began it.
func (v *Vim) typeKey(tok string) {
	switch tok {
	case "<CR>":
		v.insert("\n")
	case "<BS>":
		if v.Caret > 0 {
			from := prevChar(v.Text, v.Caret)
			v.Text = v.Text[:from] + v.Text[v.Caret:]
			v.Caret = from
		}
	default:
		if utf8.RuneCountInString(tok) == 1 {
			v.insert(tok)
		}
	}
}

// Typed tells Vim of typing the editor did in insert mode, which moved
// the caret and changed the text: kept for '.' to repeat it, as the keys
// that type it from where typing began.
func (v *Vim) Typed(text string, caret int) {
	if v.change != nil && !v.replaying {
		was := v.typedFrom
		p, s := commonEnds(was, text)
		for range utf8.RuneCountInString(was[p : len(was)-s]) {
			v.change = append(v.change, "<BS>")
		}
		for _, r := range text[p : len(text)-s] {
			if r == '\n' {
				v.change = append(v.change, "<CR>")
			} else {
				v.change = append(v.change, string(r))
			}
		}
	}
	v.Text, v.Caret = text, caret
}

func (v *Vim) insert(s string) {
	v.Text = v.Text[:v.Caret] + s + v.Text[v.Caret:]
	v.Caret += len(s)
}

// endChange keeps the change made as the one '.' repeats.
func (v *Vim) endChange() {
	if !v.replaying && v.change != nil {
		v.last = v.change
	}
	v.change = nil
}

// snapshot keeps the text before a change, for u.
func (v *Vim) snapshot() {
	v.undo = append(v.undo, vimSnapshot{v.Text, v.Caret})
	v.redo = nil
}

// run runs a command, as typed so far; it reports whether the command is
// done, ran or not, so that the next key starts another.
func (v *Vim) run(toks []string) bool {
	count, rest := takeCount(toks)
	if len(rest) == 0 {
		return false
	}
	n := max(count, 1)
	if v.Mode == VimVisual || v.Mode == VimVisualLine {
		return v.visual(rest, n, count)
	}
	k := rest[0]
	switch k {
	case "<Esc>", "<C-[>":
		return true
	case "i", "a", "I", "A", "o", "O":
		v.startChange(toks)
		switch k {
		case "a":
			if v.Caret < lineEnd(v.Text, v.Caret) {
				v.Caret = nextChar(v.Text, v.Caret)
			}
		case "I":
			v.Caret = firstNonBlank(v.Text, v.Caret)
		case "A":
			v.Caret = lineEnd(v.Text, v.Caret)
		case "o":
			v.Caret = lineEnd(v.Text, v.Caret)
			v.insert("\n" + indentOf(v.Text, v.Caret-1))
		case "O":
			v.Caret = lineStart(v.Text, v.Caret)
			ind := indentOf(v.Text, v.Caret)
			v.insert(ind + "\n")
			v.Caret -= 1
		}
		v.Mode = VimInsert
		return true
	case "v", "V":
		v.Anchor, v.Mode = v.Caret, visualModes[k]
		return true
	case "u":
		for range n {
			v.step(&v.undo, &v.redo)
		}
		return true
	case "<C-r>":
		for range n {
			v.step(&v.redo, &v.undo)
		}
		return true
	case ".":
		if v.last != nil {
			last := v.last
			if count > 0 {
				// The count given replaces the change's own, an
				// operator's motion's too.
				_, rest := takeCount(last)
				if len(rest) > 0 && strings.Contains("dcy><", rest[0]) {
					_, after := takeCount(rest[1:])
					rest = append([]string{rest[0]}, after...)
				}
				last = append([]string{strconv.Itoa(count)}, rest...)
			}
			v.pending, v.replaying = nil, true
			for _, t := range last {
				v.Feed(t)
			}
			v.pending = nil
			v.replaying = false
		}
		return true
	case ":", "/", "?":
		v.prompt = []rune(k)
		return true
	case "n", "N":
		back := v.searchBack != (k == "N")
		for range n {
			v.findNext(back)
		}
		return true
	case "x", "X", "D", "C", "s", "S", "Y", "J", "~", "p", "P":
		return v.simple(toks, k, n)
	case "<Del>":
		return v.simple(toks, "x", n)
	case "r":
		if len(rest) < 2 {
			return false
		}
		if utf8.RuneCountInString(rest[1]) != 1 {
			return true
		}
		to, ok := forwardExactly(v.Text, v.Caret, n, lineEnd(v.Text, v.Caret))
		if !ok {
			return true
		}
		v.startChange(toks)
		v.Text = v.Text[:v.Caret] + strings.Repeat(rest[1], n) + v.Text[to:]
		v.Caret += (n - 1) * len(rest[1])
		v.endChange()
		return true
	case "d", "c", "y", ">", "<":
		return v.operator(toks, k, rest[1:], count)
	}
	to, _, _, ok, done := v.motion(rest, n, count)
	if ok {
		v.Caret = v.clampNormal(to)
	}
	return done
}

// visualModes are the modes v and V begin.
var visualModes = map[string]VimMode{"v": VimVisual, "V": VimVisualLine}

// startChange begins gathering a change's tokens, for '.', and keeps the
// text before it, for u.
func (v *Vim) startChange(toks []string) {
	v.change = slices.Clone(toks)
	v.snapshot()
}

// step undoes from one history into the other.
func (v *Vim) step(from, to *[]vimSnapshot) {
	if len(*from) == 0 {
		v.Message = "Nothing to undo"
		return
	}
	s := (*from)[len(*from)-1]
	*from = (*from)[:len(*from)-1]
	*to = append(*to, vimSnapshot{v.Text, v.Caret})
	v.Text, v.Caret = s.text, v.clampNormalIn(s.text, s.caret)
}

// simple runs the commands that take no motion.
func (v *Vim) simple(toks []string, k string, n int) bool {
	start, end := lineStart(v.Text, v.Caret), lineEnd(v.Text, v.Caret)
	switch k {
	case "Y":
		v.yank(lineStart(v.Text, v.Caret), min(lineEnd(v.Text, lineAt(v.Text, v.Caret, n-1))+1, len(v.Text)), true)
		return true
	case "p", "P":
		v.startChange(toks)
		v.put(k == "P", n)
		v.endChange()
		return true
	}
	v.startChange(toks)
	switch k {
	case "x":
		v.cut(v.Caret, forward(v.Text, v.Caret, n, end), false)
	case "X":
		from := back(v.Text, v.Caret, n, start)
		v.cut(from, v.Caret, false)
		v.Caret = from
	case "D", "C":
		to := end
		if n > 1 {
			to = lineEnd(v.Text, lineAt(v.Text, v.Caret, n-1))
		}
		v.cut(v.Caret, to, false)
		if k == "C" {
			v.Mode = VimInsert
			return true
		}
	case "s":
		v.cut(v.Caret, forward(v.Text, v.Caret, n, end), false)
		v.Mode = VimInsert
		return true
	case "S":
		from := firstNonBlank(v.Text, v.Caret)
		v.cut(from, lineEnd(v.Text, lineAt(v.Text, v.Caret, n-1)), false)
		v.Caret = from
		v.Mode = VimInsert
		return true
	case "J":
		for range max(n-1, 1) {
			v.join()
		}
	case "~":
		to := forward(v.Text, v.Caret, n, end)
		toggled := toggleCase(v.Text[v.Caret:to])
		v.Text = v.Text[:v.Caret] + toggled + v.Text[to:]
		v.Caret += len(toggled)
	}
	v.Caret = v.clampNormal(v.Caret)
	v.endChange()
	return true
}

// operator runs d, c, y, > or < over a motion, a text object, or doubled,
// whole lines.
func (v *Vim) operator(toks []string, op string, rest []string, outer int) bool {
	inner, rest := takeCount(rest)
	n := min(max(outer, 1)*max(inner, 1), maxCount)
	// The count a motion sees, 0 for none, as G goes to the last line.
	count := 0
	if outer > 0 || inner > 0 {
		count = n
	}
	if len(rest) == 0 {
		return false
	}
	t := v.Text
	var from, to int
	lines := false
	switch {
	case rest[0] == op:
		from, to, lines = lineStart(t, v.Caret), lineEnd(t, lineAt(t, v.Caret, n-1)), true
	case rest[0] == "i" || rest[0] == "a":
		if len(rest) < 2 {
			return false
		}
		if utf8.RuneCountInString(rest[1]) != 1 {
			return true
		}
		kind, _ := utf8.DecodeRuneInString(rest[1])
		var ok bool
		from, to, ok = textObject(t, v.Caret, rest[0] == "a", kind)
		if !ok {
			return true
		}
	case op == "c" && (rest[0] == "w" || rest[0] == "W") && v.Caret < len(t) && !unicode.IsSpace(runeAt(t, v.Caret)):
		// cw changes the word the caret is on, to its end, and the words
		// after it for a count, but not the space after.
		big := rest[0] == "W"
		cl := class(runeAt(t, v.Caret), big)
		to = v.Caret
		for next := nextChar(t, to); next < len(t) && t[next] != '\n' && class(runeAt(t, next), big) == cl; next = nextChar(t, to) {
			to = next
		}
		end := "e"
		if big {
			end = "E"
		}
		for range n - 1 {
			to = wordMotion(t, to, end)
		}
		from, to = v.Caret, nextChar(t, to)
	default:
		target, inclusive, linewise, ok, done := v.motion(rest, n, count)
		if !done {
			return false
		}
		if !ok {
			return true
		}
		if (rest[0] == "w" || rest[0] == "W") && target > lineEnd(t, v.Caret) {
			// A word motion past the line stops at its end, as d on a
			// line's last word leaves the line's newline.
			target = lineEnd(t, v.Caret)
		}
		from, to = min(v.Caret, target), max(v.Caret, target)
		// An inclusive motion takes the character it ends on, but no
		// newline, as $ on an empty line.
		if inclusive && to < lineEnd(t, to) {
			to = nextChar(t, to)
		}
		if linewise {
			from, to, lines = lineStart(t, from), lineEnd(t, to), true
		}
	}
	return v.apply(toks, op, from, to, lines)
}

// apply runs an operator over bytes [from, to), or the whole lines they
// are in when lines is set.
func (v *Vim) apply(toks []string, op string, from, to int, lines bool) bool {
	if op == "y" {
		v.yank(from, to, lines)
		v.Caret = v.clampNormal(from)
		return true
	}
	v.startChange(toks)
	switch op {
	case ">", "<":
		var starts []int
		for l := lineStart(v.Text, from); l <= to && l <= len(v.Text); {
			starts = append(starts, l)
			end := lineEnd(v.Text, l)
			if end >= len(v.Text) {
				break
			}
			l = end + 1
		}
		// From the last line up, so that each start holds.
		for _, l := range slices.Backward(starts) {
			if op == ">" {
				if lineEnd(v.Text, l) > l {
					v.Text = v.Text[:l] + vimIndent + v.Text[l:]
				}
				continue
			}
			cut := 0
			for cut < len(vimIndent) && l+cut < len(v.Text) && v.Text[l+cut] == ' ' {
				cut++
			}
			if cut == 0 && l < len(v.Text) && v.Text[l] == '\t' {
				cut = 1
			}
			v.Text = v.Text[:l] + v.Text[l+cut:]
		}
		// The first line starts where it did; from may not be in it now.
		v.Caret = firstNonBlank(v.Text, starts[0])
	case "d":
		if !lines {
			v.cut(from, to, false)
			v.Caret = v.clampNormal(from)
			break
		}
		v.yank(from, to, true)
		// The lines' own newline goes with them; the last line's, the
		// one before it.
		if to < len(v.Text) {
			to++
		} else if from > 0 {
			from--
		}
		v.Text = v.Text[:from] + v.Text[to:]
		v.Caret = firstNonBlank(v.Text, min(from, len(v.Text)))
	case "c":
		if lines {
			v.yank(from, to, true)
			from = firstNonBlank(v.Text, from)
			v.Text = v.Text[:from] + v.Text[to:]
		} else {
			v.cut(from, to, false)
		}
		v.Caret = from
		v.Mode = VimInsert
		return true
	}
	v.endChange()
	return true
}

// vimIndent is what > adds before a line, and < takes off.
const vimIndent = "  "

// visual runs a key of visual mode: a motion moves the caret, an operator
// acts on the selection.
func (v *Vim) visual(rest []string, n, count int) bool {
	k := rest[0]
	from, to := v.Selection()
	lines := v.Mode == VimVisualLine
	switch k {
	case "<Esc>", "<C-[>":
		v.Mode = VimNormal
		return true
	case "v", "V":
		mode := visualModes[k]
		if v.Mode == mode {
			v.Mode = VimNormal
		} else {
			v.Mode = mode
		}
		return true
	case "o":
		v.Anchor, v.Caret = v.Caret, v.Anchor
		return true
	case "d", "x", "c", "s", "y", ">", "<", "~", "J":
		v.Mode = VimNormal
		switch k {
		case "~", "J":
			v.snapshot()
			if k == "~" {
				v.Text = v.Text[:from] + toggleCase(v.Text[from:to]) + v.Text[to:]
				v.Caret = from
			} else {
				v.Caret = from
				for range max(strings.Count(v.Text[from:to], "\n"), 1) {
					v.join()
				}
			}
			return true
		case "x", "s":
			k = map[string]string{"x": "d", "s": "c"}[k]
		}
		if lines && to > from && v.Text[to-1] == '\n' {
			to--
		}
		// A visual change is not one '.' repeats: the last stays.
		last := v.last
		v.apply([]string{k}, k, from, to, lines)
		v.last, v.change = last, nil
		return true
	}
	target, _, _, ok, done := v.motion(rest, n, count)
	if ok {
		v.Caret = v.clampNormal(target)
	}
	return done
}

// motion reads a motion: where it goes, whether it takes the character
// it ends on, whether it goes by lines; ok is false for one going
// nowhere, done false for one not typed whole yet.
func (v *Vim) motion(toks []string, n, count int) (to int, inclusive, linewise, ok, done bool) {
	t, c := v.Text, v.Caret
	k := toks[0]
	keepWant := false
	defer func() {
		if ok && !keepWant {
			v.want = -1
		}
	}()
	switch k {
	case "h", "<Left>", "<BS>":
		return back(t, c, n, lineStart(t, c)), false, false, true, true
	case "l", "<Right>", " ":
		// Onto the line's last character from past it, too.
		last := lastChar(t, c)
		return min(forward(t, c, n, last), last), false, false, true, true
	case "j", "k", "<Down>", "<Up>", "<C-d>", "<C-u>", "<CR>", "+", "-":
		step := n
		if k == "<C-d>" || k == "<C-u>" {
			half := v.halfPage
			if half <= 0 {
				half = 15
			}
			step = min(half*n, maxCount)
		}
		if k == "k" || k == "<Up>" || k == "<C-u>" || k == "-" {
			step = -step
		}
		line := lineAt(t, c, step)
		if line == lineStart(t, c) {
			// No line there: the motion fails, as dj on the last line.
			return 0, false, false, false, true
		}
		if k == "<CR>" || k == "+" || k == "-" {
			return firstNonBlank(t, line), false, true, true, true
		}
		if v.want < 0 {
			v.want = utf8.RuneCountInString(t[lineStart(t, c):c])
		}
		keepWant = true
		return forward(t, line, v.want, lastChar(t, line)), false, true, true, true
	case "w", "W", "b", "B", "e", "E":
		for range n {
			c = wordMotion(t, c, k)
		}
		return c, k == "e" || k == "E", false, true, true
	case "0", "<Home>":
		return lineStart(t, c), false, false, true, true
	case "^":
		return firstNonBlank(t, c), false, false, true, true
	case "$", "<End>":
		return lastChar(t, lineAt(t, c, n-1)), true, false, true, true
	case "G":
		line := len(t)
		if count > 0 {
			line = lineAt(t, 0, count-1)
		}
		return firstNonBlank(t, lineStart(t, line)), false, true, true, true
	case "g":
		if len(toks) < 2 {
			return 0, false, false, false, false
		}
		if toks[1] != "g" {
			return 0, false, false, false, true
		}
		return firstNonBlank(t, lineAt(t, 0, max(count, 1)-1)), false, true, true, true
	case "f", "F", "t", "T":
		if len(toks) < 2 {
			return 0, false, false, false, false
		}
		if utf8.RuneCountInString(toks[1]) != 1 {
			return 0, false, false, false, true // as Esc, which cancels
		}
		ch, _ := utf8.DecodeRuneInString(toks[1])
		v.find = [2]rune{rune(k[0]), ch}
		to, ok := findChar(t, c, v.find, n)
		return to, ok && (k == "f" || k == "t"), false, ok, true
	case ";", ",":
		if v.find[0] == 0 {
			return 0, false, false, false, true
		}
		find := v.find
		if k == "," {
			find[0] = map[rune]rune{'f': 'F', 'F': 'f', 't': 'T', 'T': 't'}[find[0]]
		}
		to, ok := findChar(t, c, find, n)
		return to, ok && (find[0] == 'f' || find[0] == 't'), false, ok, true
	case "%":
		to, ok := matchPair(t, c)
		return to, true, false, ok, true
	case "{", "}":
		for range n {
			c = paragraph(t, c, k == "}")
		}
		return c, false, false, true, true
	}
	return 0, false, false, false, true
}

// clampNormal keeps the caret on a character of its line, as normal mode
// does: not past the line's last.
func (v *Vim) clampNormal(c int) int { return v.clampNormalIn(v.Text, c) }

func (v *Vim) clampNormalIn(t string, c int) int {
	c = max(0, min(c, len(t)))
	for c > 0 && c < len(t) && !utf8.RuneStart(t[c]) {
		c--
	}
	if end := lineEnd(t, c); c >= end && end > lineStart(t, c) {
		return prevChar(t, end)
	}
	return c
}

// cut takes bytes [from, to) out into the register.
func (v *Vim) cut(from, to int, lines bool) {
	if from >= to {
		return
	}
	v.yank(from, to, lines)
	v.Text = v.Text[:from] + v.Text[to:]
}

func (v *Vim) yank(from, to int, lines bool) {
	s := v.Text[from:to]
	if lines && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	v.lines, v.yanked = lines, s
	if v.WriteClipboard != nil {
		v.WriteClipboard(s)
	}
}

// put puts the register after the caret, or before it, n times: lines go
// below or above the caret's line.
func (v *Vim) put(before bool, n int) {
	if v.ReadClipboard == nil {
		return
	}
	clip := v.ReadClipboard()
	s := strings.Repeat(clip, n)
	if s == "" {
		return
	}
	// Lines only as Vim yanked them: the clipboard may hold another's
	// text since.
	lines := v.lines && clip == v.yanked
	switch {
	case lines && before:
		v.Caret = lineStart(v.Text, v.Caret)
	case lines:
		end := lineEnd(v.Text, v.Caret)
		if end == len(v.Text) {
			v.Caret = end
			v.insert("\n" + strings.TrimSuffix(s, "\n"))
			v.Caret = firstNonBlank(v.Text, end+1)
			return
		}
		v.Caret = end + 1
	default:
		if !before && v.Caret < lineEnd(v.Text, v.Caret) {
			v.Caret = nextChar(v.Text, v.Caret)
		}
		v.insert(s)
		v.Caret = v.clampNormal(prevChar(v.Text, v.Caret))
		return
	}
	at := v.Caret
	v.insert(s)
	v.Caret = firstNonBlank(v.Text, at)
}

// join joins the caret's line and the next with one space.
func (v *Vim) join() {
	end := lineEnd(v.Text, v.Caret)
	if end >= len(v.Text) {
		return
	}
	next := end + 1
	for next < len(v.Text) && (v.Text[next] == ' ' || v.Text[next] == '\t') {
		next++
	}
	sep := " "
	if end == lineStart(v.Text, end) || next < len(v.Text) && v.Text[next] == ')' {
		sep = ""
	}
	v.Text = v.Text[:end] + sep + v.Text[next:]
	v.Caret = end
}

// promptKey takes a key of a : or / line.
func (v *Vim) promptKey(tok string) {
	switch tok {
	case "<Esc>", "<C-[>":
		v.prompt = nil
	case "<BS>":
		if len(v.prompt) <= 1 {
			v.prompt = nil
		} else {
			v.prompt = v.prompt[:len(v.prompt)-1]
		}
	case "<CR>":
		kind, line := v.prompt[0], strings.TrimSpace(string(v.prompt[1:]))
		v.prompt = nil
		if kind == ':' {
			v.command(line)
			return
		}
		if line != "" {
			v.search, v.searchBack = line, kind == '?'
		}
		v.findNext(v.searchBack)
	default:
		if utf8.RuneCountInString(tok) == 1 {
			v.prompt = append(v.prompt, []rune(tok)...)
		}
	}
}

// command runs a : line: w saves, a number goes to its line.
func (v *Vim) command(line string) {
	switch {
	case line == "w":
		if v.Save != nil {
			v.Save()
		}
	case line == "":
	default:
		n, err := strconv.Atoi(line)
		if err != nil {
			v.Message = "Not a command here: " + line
			return
		}
		v.Caret = firstNonBlank(v.Text, lineAt(v.Text, 0, max(n, 1)-1))
	}
}

// findNext goes to the next match of the search, or the one before.
func (v *Vim) findNext(back bool) {
	if v.search == "" {
		v.Message = "No search yet"
		return
	}
	text, s := v.Text, v.search
	var i int
	if back {
		if i = strings.LastIndex(text[:v.Caret], s); i < 0 {
			i = strings.LastIndex(text, s)
		}
	} else if at := nextChar(text, v.Caret); strings.Contains(text[at:], s) {
		i = at + strings.Index(text[at:], s)
	} else {
		i = strings.Index(text, s)
	}
	if i < 0 {
		v.Message = "Not found: " + s
		return
	}
	v.Caret = i
}

// takeCount splits a command's count off, 0 for none.
func takeCount(toks []string) (int, []string) {
	n, i := 0, 0
	for ; i < len(toks); i++ {
		t := toks[i]
		if len(t) != 1 || t[0] < '0' || t[0] > '9' || t == "0" && i == 0 {
			break
		}
		n = min(n*10+int(t[0]-'0'), maxCount)
	}
	return n, toks[i:]
}

// maxCount is the largest count a command takes, a larger one meaning
// as much: no text needs more, and a command repeated past it would hold
// the window.
const maxCount = 10000

// nextChar is the start of the character after the one at c, or the
// text's end.
func nextChar(t string, c int) int {
	if c >= len(t) {
		return len(t)
	}
	_, n := utf8.DecodeRuneInString(t[c:])
	return c + n
}

// prevChar is the start of the character before c, or 0.
func prevChar(t string, c int) int {
	if c <= 0 {
		return 0
	}
	_, n := utf8.DecodeLastRuneInString(t[:c])
	return c - n
}

// runeAt is the character at c.
func runeAt(t string, c int) rune {
	r, _ := utf8.DecodeRuneInString(t[c:])
	return r
}

// forward is n characters after c, stopping at limit.
func forward(t string, c, n, limit int) int {
	for ; n > 0 && c < limit; n-- {
		c = nextChar(t, c)
	}
	return c
}

// forwardExactly is n characters after c, which fail to fit before
// limit.
func forwardExactly(t string, c, n, limit int) (int, bool) {
	for ; n > 0; n-- {
		if c >= limit {
			return c, false
		}
		c = nextChar(t, c)
	}
	return c, true
}

// back is n characters before c, stopping at limit.
func back(t string, c, n, limit int) int {
	for ; n > 0 && c > limit; n-- {
		c = prevChar(t, c)
	}
	return c
}

// lastChar is the start of the last character of the line holding c, or
// the line's start for an empty one.
func lastChar(t string, c int) int {
	return max(prevChar(t, lineEnd(t, c)), lineStart(t, c))
}

// lineStart is the start of the line holding byte c.
func lineStart(t string, c int) int {
	c = max(0, min(c, len(t)))
	if i := strings.LastIndexByte(t[:c], '\n'); i >= 0 {
		return i + 1
	}
	return 0
}

// lineEnd is the newline ending the line holding byte c, or the text's
// end.
func lineEnd(t string, c int) int {
	c = max(0, min(c, len(t)))
	if i := strings.IndexByte(t[c:], '\n'); i >= 0 {
		return c + i
	}
	return len(t)
}

// lineAt is the start of the line n lines below the one holding c, above
// for a negative n, within the text.
func lineAt(t string, c, n int) int {
	c = lineStart(t, c)
	for ; n > 0; n-- {
		end := lineEnd(t, c)
		if end >= len(t) {
			break
		}
		c = end + 1
	}
	for ; n < 0 && c > 0; n++ {
		c = lineStart(t, c-1)
	}
	return c
}

func firstNonBlank(t string, c int) int {
	c = lineStart(t, c)
	for c < len(t) && (t[c] == ' ' || t[c] == '\t') {
		c++
	}
	return c
}

func indentOf(t string, c int) string {
	start := lineStart(t, max(c, 0))
	return t[start:firstNonBlank(t, start)]
}

// class is a character's kind for word motions: 0 space, 1 a word's, 2
// other.
func class(r rune, big bool) int {
	switch {
	case unicode.IsSpace(r):
		return 0
	case big || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
		return 1
	}
	return 2
}

// wordMotion is where w, W, b, B, e or E go from c.
func wordMotion(t string, c int, k string) int {
	big := k == "W" || k == "B" || k == "E"
	at := func(i int) int { return class(runeAt(t, i), big) }
	switch k {
	case "w", "W":
		if c >= len(t) {
			return c
		}
		cl := at(c)
		for c < len(t) && at(c) == cl && cl != 0 {
			c = nextChar(t, c)
		}
		for c < len(t) && at(c) == 0 {
			if t[c] == '\n' && c+1 < len(t) && t[c+1] == '\n' {
				return c + 1 // an empty line is a word
			}
			c = nextChar(t, c)
		}
		return min(c, len(t))
	case "e", "E":
		if nextChar(t, c) >= len(t) {
			return c
		}
		c = nextChar(t, c)
		for nextChar(t, c) < len(t) && at(c) == 0 {
			c = nextChar(t, c)
		}
		cl := at(c)
		for next := nextChar(t, c); next < len(t) && at(next) == cl; next = nextChar(t, c) {
			c = next
		}
		return c
	}
	if c == 0 {
		return 0
	}
	c = prevChar(t, c)
	for c > 0 && at(c) == 0 {
		c = prevChar(t, c)
	}
	cl := at(c)
	for c > 0 && at(prevChar(t, c)) == cl {
		c = prevChar(t, c)
	}
	return c
}

// findChar is where f, F, t or T of a character goes, n times, within the
// line: on it, or for t and T next to it.
func findChar(t string, c int, find [2]rune, n int) (int, bool) {
	start, end := lineStart(t, c), lineEnd(t, c)
	kind, ch := find[0], find[1]
	at := c
	for range n {
		i := at
		for {
			if kind == 'f' || kind == 't' {
				if i = nextChar(t, i); i >= end {
					return 0, false
				}
			} else {
				if i <= start {
					return 0, false
				}
				i = prevChar(t, i)
			}
			if runeAt(t, i) == ch {
				break
			}
		}
		at = i
	}
	switch kind {
	case 't':
		at = prevChar(t, at)
	case 'T':
		at = nextChar(t, at)
	}
	return at, true
}

var pairs = map[byte]byte{'(': ')', '[': ']', '{': '}', ')': '(', ']': '[', '}': '{'}

// matchPair is the bracket matching the first one at or after c on its
// line, as %.
func matchPair(t string, c int) (int, bool) {
	end := lineEnd(t, c)
	for c < end && pairs[t[c]] == 0 {
		c++
	}
	if c >= end {
		return 0, false
	}
	open, close := t[c], pairs[t[c]]
	step := 1
	if strings.IndexByte(")]}", open) >= 0 {
		step = -1
	}
	depth := 0
	for i := c; i >= 0 && i < len(t); i += step {
		switch t[i] {
		case open:
			depth++
		case close:
			if depth--; depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// paragraph is where } or { goes: past the paragraph the caret is in,
// or the one after the empty lines it is on, to the empty line after it,
// or before it.
func paragraph(t string, c int, down bool) int {
	empty := func(l int) bool { return lineEnd(t, l) == l }
	l := lineStart(t, c)
	if down {
		next := func(l int) int { return min(lineEnd(t, l)+1, len(t)) }
		for l < len(t) && empty(l) {
			l = next(l)
		}
		for l < len(t) && !empty(l) {
			l = next(l)
		}
		return l
	}
	prev := func(l int) int { return lineStart(t, l-1) }
	for l > 0 && empty(l) {
		l = prev(l)
	}
	for l > 0 && !empty(prev(l)) {
		l = prev(l)
	}
	if l > 0 {
		return prev(l)
	}
	return 0
}

// textObject is the bytes of iw, aw, i", a", i(, a( and the like around
// c.
func textObject(t string, c int, around bool, kind rune) (int, int, bool) {
	if c >= len(t) {
		return 0, 0, false
	}
	switch kind {
	case 'w', 'W':
		big := kind == 'W'
		cl := class(runeAt(t, c), big)
		from, to := c, c
		for from > 0 {
			p := prevChar(t, from)
			if t[p] == '\n' || class(runeAt(t, p), big) != cl {
				break
			}
			from = p
		}
		for to < len(t) && t[to] != '\n' && class(runeAt(t, to), big) == cl {
			to = nextChar(t, to)
		}
		if around {
			for to < len(t) && t[to] == ' ' {
				to++
			}
		}
		return from, to, true
	case '"', '\'', '`':
		start, end := lineStart(t, c), lineEnd(t, c)
		var quotes []int
		for i := start; i < end; i++ {
			if rune(t[i]) == kind {
				quotes = append(quotes, i)
			}
		}
		for i := 0; i+1 < len(quotes); i += 2 {
			if c <= quotes[i+1] {
				if around {
					return quotes[i], quotes[i+1] + 1, true
				}
				return quotes[i] + 1, quotes[i+1], true
			}
		}
		return 0, 0, false
	}
	open := map[rune]byte{'(': '(', ')': '(', 'b': '(', '[': '[', ']': '[', '{': '{', '}': '{', 'B': '{'}[kind]
	if open == 0 {
		return 0, 0, false
	}
	close := pairs[open]
	// The open bracket before c whose pair holds c: one closed between
	// them is skipped, but c's own.
	depth, from := 0, -1
	for i := c; i >= 0 && from < 0; i-- {
		switch {
		case t[i] == close && i != c:
			depth++
		case t[i] == open && depth == 0:
			from = i
		case t[i] == open:
			depth--
		}
	}
	if from < 0 {
		return 0, 0, false
	}
	to, ok := matchPair(t, from)
	if !ok {
		return 0, 0, false
	}
	if around {
		return from, to + 1, true
	}
	return from + 1, to, true
}

// toggleCase swaps the case of each character of s, which can change its
// length.
func toggleCase(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsUpper(r) {
			return unicode.ToLower(r)
		}
		return unicode.ToUpper(r)
	}, s)
}

// vimKeys are the named keys Vim takes, as tokens.
var vimKeys = map[ui.Key]string{
	ui.KeyEscape: "<Esc>", ui.KeyEnter: "<CR>", ui.KeyBackspace: "<BS>", ui.KeyDelete: "<Del>", ui.KeyLeft: "<Left>",
	ui.KeyRight: "<Right>", ui.KeyUp: "<Up>", ui.KeyDown: "<Down>", ui.KeyHome: "<Home>", ui.KeyEnd: "<End>",
}

// vimCtrlKey is a Control key Vim takes, and its token.
type vimCtrlKey struct {
	key   ui.Key
	token string
}

// vimCtrlKeys are the Control keys Vim takes.
var vimCtrlKeys = []vimCtrlKey{{ui.KeyR, "<C-r>"}, {ui.KeyD, "<C-d>"}, {ui.KeyU, "<C-u>"}, {ui.KeyBracketLeft, "<C-[>"}}

// vimEditKeys are the Vim keys of the Edit menu's commands.
var vimEditKeys = map[string][]string{
	"undo": {"u"}, "redo": {"<C-r>"}, "paste": {"P"}, "copy": {"y"}, "cut": {"d"}, "selectAll": {"<Esc>", "g", "g", "V", "G"},
}

// editCommands are the Edit menu's commands the editor binds keys to.
var editCommands = map[code.Command]string{
	code.Undo: "undo", code.Redo: "redo", code.Paste: "paste", code.Copy: "copy", code.Cut: "cut", code.SelectAll: "selectAll",
}

// vimInput gives Vim an input of the editor, and reports whether Vim took
// it. In insert mode the editor types, and Esc goes back to normal mode;
// in the others Vim takes the text typed and the keys it knows, and what
// would change the text behind its back. Other keys, as the app's
// commands and the pointer, go on.
func (e *Editor) vimInput(ev ui.InputEvent) bool {
	v := e.Vim
	if v.Mode == VimInsert {
		if ev.Kind != ui.InputKeyDown || !(ev.Mods == 0 && ev.Key == ui.KeyEscape || ev.Mods == ui.Ctrl && ev.Key == ui.KeyBracketLeft) {
			return false
		}
		if e.State.CompletionsOpen() {
			e.State.ShowCompletions(-1, nil)
		}
		v.Typed(e.Text(), e.Caret())
		e.vimFeed([]string{"<Esc>"})
		return true
	}
	var toks []string
	switch ev.Kind {
	case ui.InputText:
		for _, r := range ev.Text {
			toks = append(toks, string(r))
		}
	case ui.InputKeyDown:
		if tok := vimKeys[ev.Key]; tok != "" && ev.Mods == 0 {
			toks = []string{tok}
			break
		}
		if ev.Mods == ui.Ctrl {
			if i := slices.IndexFunc(vimCtrlKeys, func(k vimCtrlKey) bool { return k.key == ev.Key }); i >= 0 {
				toks = []string{vimCtrlKeys[i].token}
				break
			}
		}
		cmd := e.boundCommand(code.Binding{Mods: ev.Mods, Key: ev.Key})
		if name, ok := editCommands[cmd]; ok {
			return e.vimCommand(name)
		}
		return editsText(cmd)
	case ui.InputCommand:
		return e.vimCommand(ev.Text)
	default:
		return false
	}
	e.vimFeed(toks)
	return true
}

// vimCommand runs an Edit menu's command as Vim's keys, and reports
// whether Vim took it: copy and cut select in visual mode alone, and cut
// and delete outside it change nothing.
func (e *Editor) vimCommand(name string) bool {
	visual := e.Vim.Mode == VimVisual || e.Vim.Mode == VimVisualLine
	toks := vimEditKeys[name]
	if toks == nil || (name == "copy" || name == "cut") && !visual {
		return name == "cut" || name == "delete"
	}
	e.vimFeed(toks)
	return true
}

// vimFeed gives Vim keys, on the editor's text as it is, and gives the
// editor what Vim made of it: its change as one, and its caret or
// selection, shown.
func (e *Editor) vimFeed(toks []string) {
	v := e.Vim
	text := e.Text()
	if v.Mode != VimInsert {
		e.vimSync()
	}
	if v.Text != text {
		v.Text = text
		v.Caret = v.clampNormal(v.Caret)
	}
	v.halfPage = 0
	if first, last := e.State.VisibleLines(); last > first {
		v.halfPage = (last - first + 1) / 2
	}
	for _, t := range toks {
		if v.Mode == VimInsert && v.prompt == nil && t != "<Esc>" && t != "<C-[>" {
			// Keys that came with the one beginning typing are typed.
			v.typeKey(t)
			continue
		}
		v.Feed(t)
	}
	if v.Text != text {
		e.edit(text, v.Text)
	}
	e.showVim()
}

// showVim gives the editor Vim's caret, or its visual selection, the
// caret at its head.
func (e *Editor) showVim() {
	v := e.Vim
	sel := code.Selection{Anchor: v.Caret, Head: v.Caret}
	if v.Mode == VimVisual || v.Mode == VimVisualLine {
		start, end := v.Selection()
		sel = code.Selection{Anchor: start, Head: end}
		if v.Caret < v.Anchor {
			sel = code.Selection{Anchor: end, Head: start}
		}
	}
	e.State.SetSelections(sel)
	e.vimSel = e.State.Selection()
}

// vimSync takes a selection the editor has that Vim did not give it, as
// the pointer's or the owner's: a caret moves Vim's, a range is a visual
// selection.
func (e *Editor) vimSync() {
	sel := e.State.Selection()
	if sel == e.vimSel {
		return
	}
	v := e.Vim
	v.Text = e.Text()
	start, end := sel.Start(), sel.End()
	if start == end {
		v.Mode, v.Caret = VimNormal, v.clampNormal(end)
	} else {
		v.Mode, v.Anchor, v.Caret = VimVisual, start, max(prevChar(v.Text, end), start)
	}
	e.showVim()
}
