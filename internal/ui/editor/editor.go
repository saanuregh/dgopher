// Package editor is the SQL editor: MyGo's code editor with SQL's
// highlighting and folding, the app's colors and keys, the current
// statement's band, run buttons, problems and Vim's keys. Offsets are
// bytes of the text, whose lines end in \n.
package editor

import (
	"slices"
	"strings"
	"unicode/utf8"

	"dgopher/internal/sqltext"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	code "github.com/egoist/mygo/ui/editor"
)

// LineHeight is the height of the editor's lines, as a multiple of the
// font's size.
const LineHeight = 1.5

// Editor edits SQL. Its owner sets the fields before View, in each
// frame.
type Editor struct {
	Dialect sqltext.Dialect
	// SplitOptions end the statements that fold, as runs end them.
	SplitOptions sqltext.SplitOptions
	// State is the code editor's; owners set OnCompletion,
	// OnAcceptCompletion and ContextMenu. View sets the options it owns:
	// Bindings, OnInput, the theme, font, gutter buttons, line backgrounds
	// and the block caret.
	State code.State
	// WantFocus gives the editor the keys, asked in each frame until it
	// has them.
	WantFocus bool
	// CurrentLines are the first and last lines, from 0, of the statement
	// a run would send, shown behind them while HasCurrent.
	CurrentLines [2]int
	HasCurrent   bool
	// RunLines are the first lines, from 0, of the text's statements, each
	// with a ▶ in the gutter that calls Run with its line; RunTip names
	// it. None while Run is nil.
	RunLines []int
	Run      func(line int)
	RunTip   string
	// Problems are the mistakes of the text as it is when View is called,
	// marked with a wavy line.
	Problems []Problem
	// Vim, when set, edits with Vim's keys.
	Vim *Vim
	// KeyHook takes input before Vim and the editor, as a snippet's Tab;
	// it reports whether it took it.
	KeyHook func(ev ui.InputEvent) bool

	// text is the buffer's text of version textOf, fileText the same
	// with the line ending of fileEnding.
	text, fileText     string
	textOf, fileTextOf uint64
	textBuf, fileBuf   *code.Buffer
	fileEnding         string
	// lang is the buffer's language, for langOf.
	lang   *code.Language
	langOf struct {
		dialect sqltext.Dialect
		split   sqltext.SplitOptions
	}
	keys     []code.Binding
	band     [1]code.LineBackground
	noLines  []int
	fontSize float32
	// shown are the problems given the editor, at version shownOf.
	shown       []Problem
	shownOf     uint64
	shownSet    bool
	onInput     func(ui.InputEvent) bool
	renderRun   func(*ui.Context, ui.Element, int)
	diagnostics []code.Diagnostic
	// vimSel is the selection Vim last gave the editor, by which one the
	// pointer or the owner moved is told from Vim's own.
	vimSel code.Selection
}

// Problem is a mistake in the text: Message says what; Fix, when set, is
// the text that replaces its own to mend it.
type Problem struct {
	Start, End int // bytes
	Message    string
	Fix        string
}

// New returns an editor of text, whose \r\n line endings it edits as \n
// and saves as they were (FileText).
func New(text string, d sqltext.Dialect) *Editor {
	e := &Editor{Dialect: d}
	e.State.Buffer = code.NewBuffer(text)
	return e
}

// buffer is the text's buffer, with SQL's language in the editor's
// dialect.
func (e *Editor) buffer() *code.Buffer {
	b := e.State.Buffer
	if b == nil {
		b = code.NewBuffer("")
		e.State.Buffer = b
	}
	if e.lang == nil || e.langOf.dialect != e.Dialect || e.langOf.split != e.SplitOptions {
		e.lang = sqlLanguage(e.Dialect, e.SplitOptions)
		e.langOf.dialect, e.langOf.split = e.Dialect, e.SplitOptions
	}
	if b.Language() != e.lang {
		b.SetLanguage(e.lang)
	}
	return b
}

// Text is the text, its lines ending in \n.
func (e *Editor) Text() string {
	b := e.buffer()
	if v := b.Version(); b != e.textBuf || v != e.textOf {
		e.text, e.textOf, e.textBuf = b.String(), v, b
	}
	return e.text
}

// FileText is the text with the line endings it was read with, to save.
func (e *Editor) FileText() string {
	text := e.Text()
	b := e.buffer()
	ending := b.LineEnding()
	if b != e.fileBuf || e.textOf != e.fileTextOf || ending != e.fileEnding {
		e.fileText, e.fileTextOf, e.fileBuf, e.fileEnding = text, e.textOf, b, ending
		if ending != "\n" {
			e.fileText = strings.ReplaceAll(text, "\n", ending)
		}
	}
	return e.fileText
}

// SetText replaces the whole text, as one change undo takes back.
func (e *Editor) SetText(s string) { e.buffer().Set(s) }

// SetFileText replaces the whole text with a file's, as Replace does, to
// be saved with the file's line endings.
func (e *Editor) SetFileText(s string) {
	e.Replace(0, e.buffer().Len(), s)
	ending := "\n"
	if strings.Contains(s, "\r\n") {
		ending = "\r\n"
	}
	e.buffer().SetLineEnding(ending)
}

// Replace replaces bytes start to end of the text with s, as one change
// undo takes back, and puts the caret after s.
func (e *Editor) Replace(start, end int, s string) {
	b := e.buffer()
	n := b.Len()
	start, end = max(0, min(start, n)), max(0, min(end, n))
	b.Edit(code.Edit{Start: start, End: end, Text: s})
	at := start + len(strings.ReplaceAll(s, "\r\n", "\n"))
	e.State.Select(at, at)
}

// Selection is the newest selection, start before end.
func (e *Editor) Selection() (start, end int) {
	e.buffer()
	s := e.State.Selection()
	return s.Start(), s.End()
}

// Caret is the newest caret.
func (e *Editor) Caret() int {
	e.buffer()
	return e.State.Cursor()
}

// Select selects bytes start to end, the caret at end: a caret alone
// when they are equal.
func (e *Editor) Select(start, end int) {
	e.buffer()
	e.State.Select(start, end)
}

// SelectedText is the text of the newest selection, "" for a caret.
func (e *Editor) SelectedText() string {
	start, end := e.Selection()
	if start == end {
		return ""
	}
	return e.buffer().Slice(start, end)
}

// HasFocus reports whether the editor had the keys in the last frame.
func (e *Editor) HasFocus() bool { return e.State.Focused() }

// Typing reports whether keys type in the editor: not in Vim's modes but
// insert, whose changes are commands'.
func (e *Editor) Typing() bool { return e.Vim == nil || e.Vim.Mode == VimInsert }

// LineCol is the line and column, from 0, of a byte offset, the column
// counted in characters, for messages: an offset past the end is at the
// end.
func (e *Editor) LineCol(at int) (line, col int) {
	b := e.buffer()
	p := b.Pos(at)
	return p.Line, utf8.RuneCountInString(b.Line(p.Line)[:p.Col])
}

// ProblemAt is the problem whose text holds a byte offset, as the caret,
// at either of its ends.
func (e *Editor) ProblemAt(at int) (Problem, bool) {
	for _, p := range e.Problems {
		if at >= p.Start && at <= p.End {
			return p, true
		}
	}
	return Problem{}, false
}

// View builds the editor, which grows in its parent, and returns it.
func (e *Editor) View(c *ui.Context, fontSize float32) ui.Element {
	pal := widgets.PaletteOf(c)
	st := &e.State
	buf := e.buffer()
	st.Font = ui.Font{Family: widgets.MonoFont, Size: fontSize}
	st.LineHeight = LineHeight
	st.Theme = themeFrom(c.Theme(), pal)
	if st.Label == "" {
		st.Label = "SQL editor"
	}
	e.syncBindings()
	if e.onInput == nil {
		e.onInput = e.input
	}
	st.OnInput = e.onInput
	st.LineBackgrounds = nil
	if e.HasCurrent {
		e.band[0] = code.LineBackground{First: e.CurrentLines[0], Last: e.CurrentLines[1], Color: pal.CurrentStatement}
		st.LineBackgrounds = e.band[:]
	}
	e.syncRunButtons(fontSize)
	e.syncProblems(buf)
	vim := e.Vim
	commands := vim != nil && vim.Mode != VimInsert
	if commands {
		e.vimSync()
	}
	st.CursorShape = code.CursorBar
	if commands {
		st.CursorShape = code.CursorBlock
	}
	st.NoInputMethod = commands // letters are Vim's commands, not text to compose
	el := code.View(c, st).Grow(1).FillWidth()
	if e.WantFocus {
		el.Focus()
		e.WantFocus = !el.Focused()
	}
	if commands && el.Focused() {
		// Vim's Control keys before the app's commands, which on Linux and
		// Windows have the same keys, and the editor's.
		for _, k := range vimCtrlKeys {
			if c.Shortcut(ui.Ctrl, k.key) {
				e.vimFeed([]string{k.token})
			}
		}
	}
	return el
}

// input takes the editor's input before it: the owner's hook first, then
// Vim's.
func (e *Editor) input(ev ui.InputEvent) bool {
	if e.KeyHook != nil && e.KeyHook(ev) {
		return true
	}
	return e.Vim != nil && e.vimInput(ev)
}

// syncRunButtons puts a ▶ on the statements' lines, while there is Run.
func (e *Editor) syncRunButtons(fontSize float32) {
	st := &e.State
	if e.Run == nil {
		st.GutterButtons, st.RenderGutterButton = nil, nil
		return
	}
	e.fontSize = fontSize
	if e.renderRun == nil {
		e.renderRun = e.runButton
	}
	st.RenderGutterButton = e.renderRun
	// The gutter keeps room for them, none or some, while the list is set.
	if st.GutterButtons = e.RunLines; st.GutterButtons == nil {
		if e.noLines == nil {
			e.noLines = []int{}
		}
		st.GutterButtons = e.noLines
	}
}

// runButton fills a run button, which runs its statement and gives the
// editor back the keys.
func (e *Editor) runButton(c *ui.Context, button ui.Element, line int) {
	button.Center().Label(e.RunTip).Tooltip(e.RunTip).Cursor(ui.CursorPointer)
	button.Children(func() {
		ui.Icon(c, widgets.IconPlay).FontSize(e.fontSize * 0.85).TextColor(c.Theme().Success)
	})
	if button.Clicked() && e.Run != nil {
		e.Run(line)
		e.WantFocus = true
	}
}

// syncProblems gives the editor the problems as diagnostics, again as the
// text or they change: they are of the text as it is.
func (e *Editor) syncProblems(buf *code.Buffer) {
	v := buf.Version()
	if e.shownSet && v == e.shownOf && slices.Equal(e.Problems, e.shown) {
		return
	}
	e.shown, e.shownOf, e.shownSet = slices.Clone(e.Problems), v, true
	text := e.Text()
	e.diagnostics = e.diagnostics[:0]
	for _, p := range e.Problems {
		start := max(0, min(p.Start, len(text)))
		end := max(start, min(p.End, len(text)))
		if end == start && start < len(text) {
			// At least a character wide, to show.
			_, n := utf8.DecodeRuneInString(text[start:])
			end = start + n
		}
		e.diagnostics = append(e.diagnostics, code.Diagnostic{Start: start, End: end, Severity: code.SeverityError, Message: p.Message})
	}
	e.State.SetDiagnostics(v, e.diagnostics)
}

// ChangeTo makes the text now with one edit of the run that differs, as
// one change undo takes back: carets outside the run stay where they are.
func (e *Editor) ChangeTo(now string) { e.edit(e.Text(), now) }

// edit makes the text from was, the text as it is, to now, as one change
// undo takes back: of the bytes between the ends they share.
func (e *Editor) edit(was, now string) {
	p, s := commonEnds(was, now)
	e.buffer().Edit(code.Edit{Start: p, End: len(was) - s, Text: now[p : len(now)-s]})
}

// commonEnds are the lengths of the starts and ends two texts share,
// apart, at boundaries of characters.
func commonEnds(a, b string) (prefix, suffix int) {
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	for prefix > 0 && (prefix < len(a) && !utf8.RuneStart(a[prefix]) || prefix < len(b) && !utf8.RuneStart(b[prefix])) {
		prefix--
	}
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	for suffix > 0 && !utf8.RuneStart(a[len(a)-suffix]) {
		suffix--
	}
	return prefix, suffix
}
