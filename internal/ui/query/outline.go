package query

import (
	"fmt"
	"strings"

	"dgopher/internal/sqltext"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// outlineEntry is a statement of the editor's text, as the outline lists
// it: its line, its first words, and the comment above it.
type outlineEntry struct {
	start   int // rune offset
	line    int // 1-based
	text    string
	comment string
}

// outlineDialog lists the statements of an editor, to go to one.
type outlineDialog struct {
	open    bool
	q       *Tab
	filter  string
	entries []outlineEntry
	sel     int
	list    ui.ListState
}

// outline lists the statements of a text: each by its first line of SQL,
// with the comment lines right above it.
func outline(text string, d sqltext.Dialect, o sqltext.SplitOptions) []outlineEntry {
	runes := []rune(text)
	var out []outlineEntry
	prevEnd := 0
	for _, st := range sqltext.SplitWith(text, d, o) {
		code := strings.Join(strings.Fields(st.Text), " ")
		above := string(runes[min(prevEnd, st.Start):st.Start])
		prevEnd = st.End
		if code == "" {
			continue
		}
		out = append(out, outlineEntry{start: st.Start, line: strings.Count(string(runes[:st.Start]), "\n") + 1,
			text: widgets.OneLine(code, 120), comment: widgets.OneLine(commentAbove(above), 80)})
	}
	return out
}

// commentAbove is the text of the -- comment lines that end a gap
// between statements, those right above the next one.
func commentAbove(gap string) string {
	lines := strings.Split(strings.TrimRight(gap, " \t\r\n"), "\n")
	var comment []string
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "--") {
			break
		}
		comment = append([]string{strings.TrimSpace(strings.TrimPrefix(line, "--"))}, comment...)
	}
	return strings.Join(comment, " ")
}

// openOutline lists the editor's statements, the one at the caret chosen.
func (q *Tab) openOutline() {
	d := &outlineDialog{open: true, q: q, entries: outline(q.Editor.Text, q.Editor.Dialect, SplitOptions(q.a.Settings()))}
	for i, e := range d.entries {
		if e.start <= q.Editor.SelEnd {
			d.sel = i
		}
	}
	d.list.Selected = &d.sel
	q.a.QueryDialogs().outline = d
}

// shown are the entries the filter keeps.
func (d *outlineDialog) shown() []outlineEntry {
	f := strings.ToLower(strings.TrimSpace(d.filter))
	if f == "" {
		return d.entries
	}
	var out []outlineEntry
	for _, e := range d.entries {
		if strings.Contains(strings.ToLower(e.text+" "+e.comment), f) {
			out = append(out, e)
		}
	}
	return out
}

func outlineView(a Host, c *ui.Context) {
	d := a.QueryDialogs().outline
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	shown := d.shown()
	d.sel = min(d.sel, max(0, len(shown)-1))
	jump := func(i int) {
		if i < 0 || i >= len(shown) {
			return
		}
		q, at := d.q, shown[i].start
		d.open = false
		q.Editor.PendingSel = &[2]int{at, at}
		q.Editor.WantFocus = true
		q.reveal(at, a.Settings().EditorFont)
	}
	ui.DialogBase(c, &d.open, func(backdrop, panel ui.Element) {
		backdrop.Background(widgets.PickerBackdrop).Justify(ui.Start).PaddingY(80)
		panel.Width(640).Radius(12).Background(th.Background).Border(1, th.Border).Clip().Label("Go to statement")
		ui.Row(c).Padding(10, 14).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Icon(c, widgets.IconSearch).TextColor(pal.Muted).FontSize(15)
			in := ui.TextInputBase(c, &d.filter).Placeholder(fmt.Sprintf("Go to one of %d statements", len(d.entries))).
				AutoFocus().Grow(1).FontSize(15).Label("Filter")
			switch {
			case in.Shortcut(0, ui.KeyDown):
				d.sel = min(d.sel+1, len(shown)-1)
				d.list.ScrollIntoView(d.sel)
			case in.Shortcut(0, ui.KeyUp):
				d.sel = max(d.sel-1, 0)
				d.list.ScrollIntoView(d.sel)
			case in.Submitted():
				jump(d.sel)
			}
		})
		list := ui.List(c, &d.list, len(shown), func(i int) {
			e := shown[i]
			row := ui.Row(c).Padding(6, 14).Gap(10)
			if i == d.sel {
				row.Background(th.Accent.Alpha(0.15))
			}
			muted := widgets.RowColor(c, pal.Muted, i == d.sel)
			row.Children(func() {
				ui.Text(c, fmt.Sprint(e.line)).Font(widgets.MonoFont).FontSize(11.5).TextColor(muted).Width(40).TextAlign(ui.End)
				ui.Column(c).Gap(1).Grow(1).Shrink(1).Children(func() {
					ui.Text(c, e.text).Font(widgets.MonoFont).FontSize(12.5).SingleLine()
					if e.comment != "" {
						ui.Text(c, e.comment).FontSize(11.5).TextColor(muted).SingleLine()
					}
				})
			})
		}).MaxHeight(420).Children(func() {
			if len(shown) == 0 {
				ui.Text(c, "No statement matches.").TextColor(pal.Muted).Padding(14)
			}
		})
		if list.Submitted() {
			jump(d.sel)
		}
	})
	if !d.open {
		a.QueryDialogs().outline = nil
	}
}
