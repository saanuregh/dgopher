package query

import (
	"strings"
	"unicode/utf8"

	"dgopher/internal/ui/editor"

	"github.com/egoist/mygo/ui"
)

// findView builds the find bar above the editor, and selects the current
// match in it.
func (q *Tab) findView(c *ui.Context, fontSize float32) {
	f := &q.find
	if c.Shortcut(ui.Cmd, ui.KeyF) {
		f.Open, f.Shown = true, -1
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
	e := &q.Editor
	e.PendingSel = &[2]int{at, at + utf8.RuneCountInString(f.Query)}
	// Bring the match's line into view.
	line := strings.Count(string([]rune(e.Text)[:at]), "\n")
	lh := fontSize * editor.LineHeight
	y := float32(line) * lh
	if y < e.Scroll.Y || y > e.Scroll.Y+q.editorH-3*lh {
		e.Scroll.Y = max(0, y-q.editorH/3)
	}
}
