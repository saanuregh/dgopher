package query

import (
	"fmt"
	"unicode/utf8"

	"dgopher/internal/keymap"
	"dgopher/internal/ui/editor"

	"github.com/egoist/mygo/ui"
)

// findView builds the find bar above the editor, with its replace row,
// and selects the current match in the editor.
func (q *Tab) findView(c *ui.Context, fontSize float32) {
	f := &q.find
	switch {
	case q.pressed(c, keymap.Replace):
		f.Open, f.Replacing, f.Shown = true, true, -1
	case q.pressed(c, keymap.Find):
		f.Open, f.Replacing, f.Shown = true, false, -1
	}
	if !f.Open {
		q.Editor.Marks = nil
		return
	}
	if f.Query != f.ForQuery || q.Editor.Text != f.ForText {
		f.Matches = editor.FindAll(q.Editor.Text, f.Query)
		f.ForQuery, f.ForText = f.Query, q.Editor.Text
		f.Current, f.Shown = 0, -1
	}
	ui.FindBar(c, &f.Open, &f.Query, len(f.Matches), &f.Current).Padding(4, 8)
	if f.Open && f.Replacing {
		q.replaceRow(c)
	}
	th := c.Theme()
	n := utf8.RuneCountInString(f.Query)
	q.Editor.Marks = q.Editor.Marks[:0]
	for i, at := range f.Matches {
		mark := ui.TextRange{Start: at, End: at + n, Color: th.Accent, Weight: 700}
		if i == f.Current {
			mark.Color, mark.Weight = th.Danger, 800
		}
		q.Editor.Marks = append(q.Editor.Marks, mark)
	}
	if !f.Open || len(f.Matches) == 0 || f.Current == f.Shown || f.Current >= len(f.Matches) {
		return
	}
	f.Shown = f.Current
	at := f.Matches[f.Current]
	q.Editor.PendingSel = &[2]int{at, at + n}
	q.reveal(at, fontSize)
}

// reveal scrolls the editor to the line of a rune offset, unless it
// shows already.
func (q *Tab) reveal(at int, fontSize float32) {
	e := &q.Editor
	line := e.ViewLine(min(at, utf8.RuneCountInString(e.Text)))
	lh := fontSize * editor.LineHeight
	y := float32(line) * lh
	if y < e.Scroll.Y || y > e.Scroll.Y+q.editorH-3*lh {
		e.Scroll.Y = max(0, y-q.editorH/3)
	}
}

// replaceRow builds the row under the find bar that replaces its
// matches: Enter in its field replaces the current one.
func (q *Tab) replaceRow(c *ui.Context) {
	f := &q.find
	th := c.Theme()
	none := len(f.Matches) == 0
	ui.Row(c).Gap(th.Space(2)).Padding(4, 8).Background(th.Surface).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
		in := ui.TextInput(c, &f.Replacement).Placeholder("Replace with").Label("Replace with").Grow(1).MaxWidth(th.Space(90))
		switch {
		case in.Submitted() && !none:
			q.replaceCurrent()
		case in.Shortcut(0, ui.KeyEscape):
			f.Open = false
		}
		if ui.Button(c, "Replace").Disabled(none).Tooltip("Replace the current match, then show the next").Clicked() {
			q.replaceCurrent()
		}
		if ui.Button(c, "Replace All").Disabled(none).Clicked() {
			q.replaceAll()
		}
	})
}

// replaceCurrent replaces the current match, then shows the next one
// after what it inserted, so that a replacement holding the query is not
// found again.
func (q *Tab) replaceCurrent() {
	f := &q.find
	if f.Current >= len(f.Matches) {
		return
	}
	at := f.Matches[f.Current]
	q.Editor.Replace(at, at+utf8.RuneCountInString(f.Query), f.Replacement)
	f.Matches = editor.FindAll(q.Editor.Text, f.Query)
	f.ForQuery, f.ForText = f.Query, q.Editor.Text
	after := at + utf8.RuneCountInString(f.Replacement)
	f.Current, f.Shown = 0, -1
	for i, m := range f.Matches {
		if m >= after {
			f.Current = i
			break
		}
	}
}

// replaceAll replaces every match. The editor's undo would also take
// back the typing before it, so a toast offers to undo the replacing
// alone, while the text is still as it left it.
func (q *Tab) replaceAll() {
	f := &q.find
	n := len(f.Matches)
	if n == 0 {
		return
	}
	before := q.Editor.Text
	after := editor.ReplaceAt(before, f.Matches, utf8.RuneCountInString(f.Query), f.Replacement)
	q.Editor.Text = after
	what := "matches"
	if n == 1 {
		what = "match"
	}
	q.a.Toast(fmt.Sprintf("Replaced %d %s", n, what), "Undo", func() {
		if q.Editor.Text == after {
			q.Editor.Text = before
		}
	})
}
