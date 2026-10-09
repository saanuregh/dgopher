package dataview

import (
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/export"
	"dgopher/internal/settings"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// pasteOptions say how pasted text is read into the grid.
type pasteOptions struct {
	// Delimiter separates cells; 0 reads a tab when the text has one,
	// else a comma.
	Delimiter rune
	// Header takes the first row as column names, matched by name.
	Header bool
	// NullText is the text of a NULL cell; "" has none.
	NullText string
	// Insert adds the rows as new ones, rather than writing over the
	// cells from the chosen one down.
	Insert bool
}

// parseClipboard splits pasted text into rows of cells, as a spreadsheet
// or another grid copies them.
func parseClipboard(text string, opt pasteOptions) ([][]string, error) {
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if text == "" {
		return nil, errors.New("the clipboard holds no text")
	}
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = opt.Delimiter
	if r.Comma == 0 {
		r.Comma = ','
		if strings.Contains(text, "\t") {
			r.Comma = '\t'
		}
	}
	r.FieldsPerRecord, r.LazyQuotes = -1, true
	return r.ReadAll()
}

// paste writes rows of text into the grid as pending edits, from the
// chosen cell down and right, adding rows past the end, or as new rows.
// It returns how many cells it set.
func (g *Grid) paste(src *Source, rows [][]string, opt pasteOptions) (int, error) {
	if g.edits == nil {
		return 0, errors.New("these rows are the result of a query: open the table to change its rows")
	}
	if g.readOnly != "" {
		return 0, errors.New(g.readOnly)
	}
	g.checkpoint()
	// cols[i] is the grid column of pasted column i, -1 for none.
	var cols []int
	if opt.Header && len(rows) > 0 {
		for _, name := range rows[0] {
			at := -1
			for i, c := range src.Cols {
				if strings.EqualFold(c.Name, strings.TrimSpace(name)) {
					at = i
				}
			}
			cols = append(cols, at)
		}
		rows = rows[1:]
	}
	value := func(s string) any {
		if opt.NullText != "" && s == opt.NullText {
			return nil
		}
		return db.Typed(s)
	}
	start := g.SelRow
	if start < 0 {
		start = 0
	}
	// Cells go to the columns shown, from the chosen one: a hidden column
	// takes none.
	var shown []int
	for i := range src.Cols {
		if !g.hidden[i] {
			shown = append(shown, i)
		}
	}
	firstCol := max(slices.Index(shown, g.selCol), 0)
	if opt.Insert || cols != nil {
		firstCol = 0
	}
	order := g.ViewOrder(src)
	n := 0
	for i, cells := range rows {
		var data int
		if !opt.Insert && start+i < len(order) {
			data = order[start+i]
		} else {
			row := make([]any, len(src.Cols))
			for c := range row {
				row[c] = unset
			}
			g.edits.inserted = append(g.edits.inserted, row)
			data = len(src.Rows) + len(g.edits.inserted) - 1
		}
		for j, cell := range cells {
			col := -1
			if firstCol+j < len(shown) {
				col = shown[firstCol+j]
			}
			if cols != nil {
				if j >= len(cols) {
					continue
				}
				col = cols[j]
			}
			if col < 0 || col >= len(src.Cols) || g.readOnlyCols[col] != "" {
				continue
			}
			if data < len(src.Rows) && g.edits.deleted[data] {
				continue
			}
			g.setValue(src, data, col, value(cell))
			n++
		}
	}
	g.orderKey = ""
	return n, nil
}

// copyAdvanced writes the chosen rows as delimited text.
func (g *Grid) copyAdvanced(src *Source, opt settings.CopyOptions) string {
	names := make([]string, 0, len(src.Cols)+1)
	if opt.RowNumbers {
		names = append(names, "#")
	}
	for _, col := range src.Cols {
		names = append(names, col.Name)
	}
	var data [][]any
	for _, r := range g.selectedRows(src) {
		vals := make([]any, 0, len(names))
		if opt.RowNumbers {
			vals = append(vals, strconv.Itoa(r+1))
		}
		for col := range src.Cols {
			v, _ := g.value(src, r, col)
			if v == unset || v == db.Default {
				v = nil
			}
			if typed, ok := v.(db.Typed); ok {
				v = string(typed)
			}
			vals = append(vals, v)
		}
		data = append(data, vals)
	}
	delim := opt.Delimiter
	if delim == 0 {
		delim = '\t'
	}
	out, err := export.Text(export.CSV, names, data, export.Options{Header: opt.Header, NullText: opt.NullText, Delimiter: delim, QuoteAlways: opt.QuoteAll})
	if err != nil {
		return ""
	}
	return out
}

var delimiterLabels = []string{"Tab", "Comma", "Semicolon", "Pipe"}

var delimiterRunes = []rune{'\t', ',', ';', '|'}

func delimiterIndex(r rune) int {
	for i, d := range delimiterRunes {
		if d == r {
			return i
		}
	}
	return 0
}

// clipForm is the Advanced Copy or Advanced Paste dialog of a grid.
type clipForm struct {
	open      bool
	paste     bool
	g         *Grid
	src       *Source
	delim     int
	copy      settings.CopyOptions
	pasteOpts pasteOptions
}

func openAdvancedCopy(a Host, g *Grid, src *Source) {
	f := &clipForm{open: true, g: g, src: src, copy: a.Settings().AdvancedCopy}
	if f.copy.Delimiter == 0 {
		f.copy = settings.CopyOptions{Delimiter: '\t', Header: true, NullText: "NULL"}
	}
	f.delim = delimiterIndex(f.copy.Delimiter)
	a.Dialogs().clip = f
}

func openAdvancedPaste(a Host, g *Grid, src *Source) {
	a.Dialogs().clip = &clipForm{open: true, paste: true, g: g, src: src, pasteOpts: pasteOptions{NullText: "NULL"}}
}

// pasteInto pastes the clipboard into a grid, saying how it went.
func pasteInto(a Host, g *Grid, src *Source, text string, opt pasteOptions) {
	rows, err := parseClipboard(text, opt)
	if err == nil {
		var n int
		n, err = g.paste(src, rows, opt)
		if err == nil {
			a.Toast(fmt.Sprintf("Pasted %d cells: review them before applying", n), "", nil)
		}
	}
	if err != nil {
		a.ShowError("Could not paste", err.Error())
	}
}

func clipView(a Host, c *ui.Context) {
	f := a.Dialogs().clip
	pal := widgets.PaletteOf(c)
	title := "Advanced Copy"
	if f.paste {
		title = "Advanced Paste"
	}
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(460).Gap(12).Children(func() {
			ui.Text(c, title).FontSize(15).Bold()
			ui.Form(c, func() {
				ui.Field(c, "Delimiter", func() {
					ui.Segmented(c, &f.delim, delimiterLabels...).Label("Delimiter").AutoFocus()
				})
				if f.paste {
					ui.Field(c, "", func() { ui.Checkbox(c, &f.pasteOpts.Header, "The first row names the columns") })
					ui.Field(c, "", func() { ui.Checkbox(c, &f.pasteOpts.Insert, "Add as new rows") })
					ui.Field(c, "NULL text", func() { ui.TextInput(c, &f.pasteOpts.NullText).Font(widgets.MonoFont) }).
						Description("A cell with exactly this text is NULL.")
					return
				}
				ui.Field(c, "", func() { ui.Checkbox(c, &f.copy.Header, "Column names first") })
				ui.Field(c, "", func() { ui.Checkbox(c, &f.copy.RowNumbers, "Row numbers") })
				ui.Field(c, "", func() { ui.Checkbox(c, &f.copy.QuoteAll, "Quote every value") })
				ui.Field(c, "NULL text", func() { ui.TextInput(c, &f.copy.NullText).Font(widgets.MonoFont) })
			})
			if f.paste {
				ui.Text(c, "Pasted cells are pending changes: review them as SQL before they are applied.").FontSize(12).TextColor(pal.Muted)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					f.open = false
				}
				label := "Copy"
				if f.paste {
					label = "Paste"
				}
				if widgets.Activated(c, ui.PrimaryButton(c, label)) {
					f.open = false
					if f.paste {
						f.pasteOpts.Delimiter = delimiterRunes[f.delim]
						pasteInto(a, f.g, f.src, c.ReadClipboard(), f.pasteOpts)
						return
					}
					f.copy.Delimiter = delimiterRunes[f.delim]
					a.Settings().AdvancedCopy = f.copy
					a.SaveSettings()
					a.WriteClipboard(f.g.copyAdvanced(f.src, f.copy))
					c.Toast("Copied")
				}
			})
		})
	})
	if !f.open && a.Dialogs().clip == f {
		a.Dialogs().clip = nil
	}
}

// copyText writes the chosen rows in a "Copy as" format.
func (g *Grid) copyText(a Host, src *Source, format string) string {
	if format == "text" {
		return g.plainText(a, src, g.selectedRows(src), maxCopyRows)
	}
	return g.copySelection(src, export.Format(format), true)
}

// maxCopyRows is how many rows a plain-text copy writes.
const maxCopyRows = 100000

// openWith opens the rows read in the system's app for CSV files, as a
// spreadsheet, through a file of the user's own.
func (g *Grid) openWith(a Host, src *Source) {
	names := make([]string, len(src.Cols))
	for i, c := range src.Cols {
		names[i] = c.Name
	}
	rows := make([][]any, 0, len(src.Rows))
	for r := range src.Rows {
		vals := make([]any, len(src.Cols))
		for col := range src.Cols {
			v, _ := g.value(src, r, col)
			if v == unset || v == db.Default {
				v = nil
			}
			if typed, ok := v.(db.Typed); ok {
				v = string(typed)
			}
			vals[col] = v
		}
		rows = append(rows, vals)
	}
	f, err := os.CreateTemp("", "dgopher-*.csv")
	if err == nil {
		f.Close()
		ecols := make([]export.Column, len(src.Cols))
		for i, c := range src.Cols {
			ecols[i] = export.Column{Name: c.Name, DatabaseType: c.Type}
		}
		var w export.RowWriter
		if w, err = export.NewFileWriter(f.Name(), export.CSV, ecols, export.Options{Header: true}); err == nil {
			for _, r := range rows {
				if err = w.Write(r); err != nil {
					break
				}
			}
			if cerr := w.Close(); err == nil {
				err = cerr
			}
		}
	}
	if err != nil {
		a.ShowError("Could not open the rows", err.Error())
		return
	}
	if g.Exported != nil {
		g.Exported("opened as CSV in the system's app: "+f.Name(), len(rows))
	}
	mygo.Shell.OpenPath(f.Name())
}
