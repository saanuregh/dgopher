package editor

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"dgopher/internal/sqltext"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	code "github.com/egoist/mygo/ui/editor"
)

// AreaLanguage is what a CodeArea highlights its text as.
type AreaLanguage uint8

const (
	LanguagePlain AreaLanguage = iota
	LanguageSQL                // in the area's Dialect
	LanguageJSON
	// LanguageDetected is JSON when the text set from outside is JSON,
	// else plain.
	LanguageDetected
)

// CodeArea shows a string in a code editor, without the SQL editor's
// run buttons, keys or Vim. The owner keeps it across frames and sets its
// options before View, in each frame.
type CodeArea struct {
	// Label names the area for assistive technology.
	Label    string
	Language AreaLanguage
	Dialect  sqltext.Dialect
	// FontSize is 12.5 when 0.
	FontSize      float32
	ReadOnly      bool
	NoLineNumbers bool
	NoFolding     bool
	// NoBorder leaves out the border of a text field, for an area that
	// fills a page.
	NoBorder bool
	// Disabled takes no focus and shows the text muted; it is read only.
	Disabled  bool
	AutoFocus bool

	state code.State
	// held is the owner's string as the buffer of version heldOf holds
	// it, heldLF the buffer's text then, with \n line endings.
	held   string
	heldLF string
	heldOf uint64
	bound  bool
	// detected is the language of a LanguageDetected area, found as the
	// text came from outside.
	detected *code.Language
	sql      *code.Language
	sqlOf    sqltext.Dialect
}

// View shows *s, which it sets to the text the user edits; it reports
// whether it did. A *s the owner changed replaces the text, with no step
// to undo.
func (a *CodeArea) View(c *ui.Context, s *string) (ui.Element, bool) {
	st := &a.state
	if st.Buffer == nil {
		st.Buffer = code.NewBuffer("")
	}
	b := st.Buffer
	changed := false
	switch {
	case !a.bound || *s != a.held:
		a.reset(*s)
	case b.Version() != a.heldOf:
		now := b.String()
		text := withEndings(a.held, a.heldLF, now, b.LineEnding())
		*s, a.held, a.heldLF, a.heldOf, changed = text, text, now, b.Version(), true
	}
	if l := a.language(); b.Language() != l {
		b.SetLanguage(l)
	}

	pal := widgets.PaletteOf(c)
	size := a.FontSize
	if size == 0 {
		size = 12.5
	}
	st.Font = ui.Font{Family: widgets.MonoFont, Size: size}
	st.LineHeight = LineHeight
	st.Theme = themeFrom(c.Theme(), pal)
	if a.Disabled {
		st.Theme.Text = pal.Muted
		for k := range st.Theme.Tokens {
			st.Theme.Tokens[k].Color = pal.Muted
		}
	}
	st.Label = a.Label
	st.ReadOnly = a.ReadOnly || a.Disabled
	st.LineNumbers = code.LineNumbersOn
	if a.NoLineNumbers {
		st.LineNumbers = code.LineNumbersOff
	}
	st.NoFolding = a.NoFolding
	// A value is data: typed as it is, and Tab and Escape are the
	// window's, as in a text field.
	st.NoAutoClose = true
	st.Bindings = dataKeys
	if st.OnInput == nil {
		st.OnInput = a.input
	}
	if a.NoBorder {
		return a.editor(c), changed
	}
	// The editor paints all of its box: the border goes around it.
	th := c.Theme()
	box := ui.Column(c).Border(1, th.Border).Radius(th.Radius).Clip()
	box.Children(func() { a.editor(c).Grow(1) })
	return box, changed
}

// editor builds the code editor of the area's state.
func (a *CodeArea) editor(c *ui.Context) ui.Element {
	el := code.View(c, &a.state)
	if a.Disabled {
		el.Disabled(true)
	} else if a.AutoFocus {
		el.AutoFocus()
	}
	return el
}

// reset makes the buffer hold s, with no step to undo, the caret kept in
// the text.
func (a *CodeArea) reset(s string) {
	st := &a.state
	b := st.Buffer
	caret := 0
	if a.bound {
		caret = st.Cursor()
	}
	b.Reset(s)
	text := b.String()
	a.held, a.heldLF, a.heldOf, a.bound = s, text, b.Version(), true
	if a.Language == LanguageDetected {
		a.detected = nil
		if json.Valid([]byte(strings.TrimSpace(s))) {
			a.detected = code.LanguageNamed("JSON")
		}
	}
	caret = min(caret, len(text))
	for caret > 0 && caret < len(text) && !utf8.RuneStart(text[caret]) {
		caret--
	}
	st.Select(caret, caret)
}

// dataKeys leave Tab and Escape to the window, to move on from the area
// and close its dialog.
var dataKeys = []code.Binding{
	{Key: ui.KeyTab, Command: code.NoCommand},
	{Mods: ui.Shift, Key: ui.KeyTab, Command: code.NoCommand},
	{Key: ui.KeyEscape, Command: code.NoCommand},
}

// input types Enter as a line break alone, without the indentation the
// editor would carry over.
func (a *CodeArea) input(ev ui.InputEvent) bool {
	if ev.Kind != ui.InputKeyDown || ev.Key != ui.KeyEnter || ev.Mods != 0 || a.ReadOnly || a.Disabled {
		return false
	}
	a.state.Insert("\n")
	return true
}

// withEndings is now, a buffer's text with \n line endings, as the owner's
// string: the run an edit changed since was, which the owner held as
// held, takes ending, and the rest keeps held's bytes, mixed endings too.
func withEndings(held, was, now, ending string) string {
	if ending == "\n" && len(held) == len(was) {
		return now // held has no \r\n to keep
	}
	p, s := commonEnds(was, now)
	middle := now[p : len(now)-s]
	if ending != "\n" {
		middle = strings.ReplaceAll(middle, "\n", ending)
	}
	return held[:rawOffset(held, p)] + middle + held[rawOffset(held, len(was)-s):]
}

// rawOffset is the offset in held of offset lf in held with its \r\n
// line endings as \n.
func rawOffset(held string, lf int) int {
	i := 0
	for j := 0; j < lf && i < len(held); j++ {
		if held[i] == '\r' && i+1 < len(held) && held[i+1] == '\n' {
			i++
		}
		i++
	}
	return i
}

// language is the buffer's language, nil for plain text.
func (a *CodeArea) language() *code.Language {
	switch a.Language {
	case LanguageSQL:
		if a.sql == nil || a.sqlOf != a.Dialect {
			a.sql, a.sqlOf = sqlLanguage(a.Dialect, sqltext.SplitOptions{}), a.Dialect
		}
		return a.sql
	case LanguageJSON:
		return code.LanguageNamed("JSON")
	case LanguageDetected:
		return a.detected
	}
	return nil
}

// Focused reports whether the area had the keys in the last frame.
func (a *CodeArea) Focused() bool { return a.state.Focused() }
