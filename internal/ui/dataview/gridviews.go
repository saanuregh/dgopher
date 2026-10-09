package dataview

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"dgopher/internal/keymap"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// The grid's presentations, as DBeaver's: rows in a grid, one row as a
// record of fields, or rows as plain text.
const (
	viewGrid = iota
	viewRecord
	viewText
)

// presentationBar switches between the presentations, at the grid's left.
func (g *Grid) presentationBar(c *ui.Context) {
	th := c.Theme()
	ui.Column(c).Width(30).Padding(4, 3).Gap(2).BorderWidth(0, 1, 0, 0).BorderColor(th.Border).Children(func() {
		for _, p := range []struct {
			mode  int
			icon  *ui.SVG
			label string
		}{{viewGrid, widgets.IconColumns, "Grid"}, {viewRecord, widgets.IconFile, "Record: the chosen row as fields (Tab)"}, {viewText, widgets.IconCode, keymap.Hint("Text: the rows as plain text", keymap.NextPresentation)}} {
			b := widgets.IconButton(c, p.icon, p.label)
			if g.mode == p.mode {
				b.Background(th.Accent.Alpha(0.15))
			}
			if b.Clicked() {
				g.mode = p.mode
			}
		}
	})
}

// recordView shows the chosen row as a list of its fields, editable as the
// grid is.
func (g *Grid) recordView(c *ui.Context, a Host, src *Source, order []int) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	if g.SelRow < 0 && len(order) > 0 {
		g.SelRow = 0
	}
	if g.SelRow < 0 || g.SelRow >= len(order) {
		ui.Text(c, "No row to show.").TextColor(pal.Muted).Padding(12)
		return
	}
	data := order[g.SelRow]
	var fields []int
	for i := range src.Cols {
		if !g.hidden[i] {
			fields = append(fields, i)
		}
	}
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(4, 8).Gap(6).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			if widgets.IconButton(c, widgets.IconPrev, "Previous row").Disabled(g.SelRow == 0).Clicked() {
				g.SelRow--
			}
			ui.Text(c, "Row "+strconv.Itoa(g.SelRow+1)+" of "+strconv.Itoa(len(order))).FontSize(12).TextColor(pal.Muted)
			if widgets.IconButton(c, widgets.IconNext, "Next row").Disabled(g.SelRow >= len(order)-1).Clicked() {
				g.SelRow++
			}
		})
		g.recList.Selected = &g.recRow
		cols := []ui.TableColumn{{Title: "Field", ID: "field", Width: 200}, {Title: "Value", ID: "value"}}
		list := ui.Table(c, &g.recList, cols, len(fields), func(r, col int) {
			field := fields[r]
			if col == 0 {
				ui.Text(c, src.Cols[field].Name).FontSize(12.5).Bold().SingleLine()
				return
			}
			g.cell(c, a, src, g.SelRow, data, field)
		}).Grow(1).Label("Record")
		if g.recRow >= 0 && g.recRow < len(fields) {
			g.selCol = fields[g.recRow]
		}
		if list.Valid() && g.recList.Shortcut(c, 0, ui.KeyTab) {
			g.mode = viewGrid
		}
		if g.edits != nil && g.readOnly == "" && (g.recList.Shortcut(c, 0, ui.KeyF2) || list.Submitted()) && g.recRow >= 0 {
			g.startEdit(src, data, fields[g.recRow])
		}
	})
}

// textView shows the rows as aligned plain text, as psql prints them.
func (g *Grid) textView(c *ui.Context, a Host, src *Source, order []int) {
	pal := widgets.PaletteOf(c)
	key := g.orderKey + "/" + strconv.Itoa(len(order))
	if key != g.textKey {
		g.textKey, g.text = key, g.plainText(a, src, order, 5000)
	}
	ui.ScrollBoth(c).Grow(1).Background(pal.EditorBg).Children(func() {
		ui.Text(c, g.text).Font(widgets.MonoFont).FontSize(12).Padding(10, 12).Selectable().NoWrap()
	})
}

// plainText writes up to limit rows as an aligned text table.
func (g *Grid) plainText(a Host, src *Source, order []int, limit int) string {
	var cols []int
	for i := range src.Cols {
		if !g.hidden[i] {
			cols = append(cols, i)
		}
	}
	rows := order[:min(len(order), limit)]
	cells := make([][]string, len(rows))
	widths := make([]int, len(cols))
	for j, col := range cols {
		widths[j] = utf8.RuneCountInString(src.Cols[col].Name)
	}
	for i, r := range rows {
		cells[i] = make([]string, len(cols))
		for j, col := range cols {
			v, _ := g.value(src, r, col)
			if v == unset {
				v = nil
			}
			s := cellText(a.Settings().ViewFormat.Format(v), 60)
			cells[i][j] = s
			widths[j] = max(widths[j], utf8.RuneCountInString(s))
		}
	}
	var b strings.Builder
	line := func(vals []string) {
		for j, v := range vals {
			if j > 0 {
				b.WriteString(" | ")
			}
			pad := widths[j] - utf8.RuneCountInString(v)
			if g.numericColumn(src, cols[j]) {
				b.WriteString(strings.Repeat(" ", pad) + v)
			} else {
				b.WriteString(v + strings.Repeat(" ", pad))
			}
		}
		b.WriteString("\n")
	}
	names := make([]string, len(cols))
	for j, col := range cols {
		names[j] = src.Cols[col].Name
	}
	line(names)
	for j := range cols {
		if j > 0 {
			b.WriteString("-+-")
		}
		b.WriteString(strings.Repeat("-", widths[j]))
	}
	b.WriteString("\n")
	for _, row := range cells {
		line(row)
	}
	if len(order) > limit {
		b.WriteString("… " + strconv.Itoa(len(order)-limit) + " more rows\n")
	}
	return b.String()
}
