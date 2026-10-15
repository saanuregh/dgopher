package dataview

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// statsKey is what stats over the chosen cells depend on, cheap to take
// every frame, so that they are worked out again only when it changes.
// selGen counts what the rest cannot tell cheaply: which rows are chosen
// (ui.Selection keeps no count of its changes) and edits of values. chosen,
// selRow and cells catch the choice changed without the grid's methods, as
// the table changes it itself.
type statsKey struct {
	src                         *Source
	selGen, gen, chosen, selRow int
	cells                       ui.ColumnRange
	cols, orderKey              string
	edits                       *pendingEdits
	editCount, undo, redo       int
}

type statsCache struct {
	statusKey, calcKey statsKey
	status             string
	calc               []calcStats
}

func (g *Grid) statsKey(src *Source, cols []int) statsKey {
	g.ViewOrder(src)
	var b []byte
	for _, col := range cols {
		b = strconv.AppendInt(b, int64(col), 10)
		if g.isMasked(col) {
			b = append(b, '*')
		}
		b = append(b, ',')
	}
	return statsKey{
		src: src, selGen: g.selGen, gen: g.gen, chosen: g.selection.Len(), selRow: g.SelRow,
		cells: g.List.Cells, cols: string(b), orderKey: g.orderKey,
		edits: g.edits, editCount: g.edits.count(), undo: len(g.undoStack), redo: len(g.redoStack),
	}
}

// SelectionStats sums up the chosen block of cells, when it has more than
// one: its size, then Excel's count, sum, average, minimum and maximum of
// its numbers, and its NULLs.
func (g *Grid) SelectionStats(src *Source) string {
	if g.List.DragSelecting() {
		return g.stats.status // a large block costs a frame or more: once the drag ends
	}
	cols := g.selectedCols(src)
	if key := g.statsKey(src, cols); key != g.stats.statusKey {
		g.stats.statusKey, g.stats.status = key, g.blockStats(src, cols)
	}
	return g.stats.status
}

func (g *Grid) blockStats(src *Source, cols []int) string {
	rows := g.selectedRows(src)
	if len(rows)*len(cols) < 2 {
		return ""
	}
	nulls, count := 0, 0
	sum := new(big.Float)
	var minV, maxV *big.Float
	for _, col := range cols {
		masked := g.isMasked(col)
		for _, r := range rows {
			v, _ := g.value(src, r, col)
			if v == nil || v == unset || v == db.Default {
				nulls++
				continue
			}
			if masked || !db.IsNumeric(v) {
				continue
			}
			n, ok := new(big.Float).SetString(db.Display(v))
			if !ok {
				continue
			}
			count++
			sum.Add(sum, n)
			if minV == nil || n.Cmp(minV) < 0 {
				minV = n
			}
			if maxV == nil || n.Cmp(maxV) > 0 {
				maxV = n
			}
		}
	}
	text := widgets.Count(len(rows), "row") + " × " + widgets.Count(len(cols), "column")
	if count > 0 {
		avg := new(big.Float).Quo(sum, big.NewFloat(float64(count)))
		num := func(f *big.Float) string {
			s := f.Text('f', 10)
			if strings.Contains(s, ".") {
				s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
			}
			return s
		}
		text += fmt.Sprintf(" · Count %d · Sum %s · Avg %s · Min %s · Max %s", count, num(sum), num(avg), num(minV), num(maxV))
	}
	if nulls > 0 {
		text += fmt.Sprintf(" · %d NULL", nulls)
	}
	return text
}

// cellText is a value's text as a cell shows it: its first line, cut to
// max runes.
func cellText(s string, max int) string {
	// A cell is drawn every frame: a long value is read no further than
	// the runes that can show, and the one past them.
	n := 0
	for i := range s {
		if n > max {
			s = s[:i]
			break
		}
		n++
	}
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i] + " ⏎…"
	}
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}

// formatMenu is the View/Format submenu: how every grid shows values.
func formatMenu(a Host, m *ui.Menu) {
	f := &a.Settings().ViewFormat
	m.Submenu("View/Format", func(m *ui.Menu) {
		changed := false
		toggle := func(label string, on *bool) {
			if m.Item(label).Checked(*on).Chosen() {
				*on, changed = !*on, true
			}
		}
		choose := func(label string, field *string, value string) {
			if m.Item(label).Checked(*field == value).Chosen() {
				*field, changed = value, true
			}
		}
		toggle("Raw Values, as Stored", &f.Raw)
		m.Separator()
		toggle("Group Thousands in Numbers", &f.GroupNumbers)
		toggle("Numbers in Regional Format", &f.Regional)
		toggle("Booleans as ✓ and ✗", &f.BoolTicks)
		m.Separator()
		choose("Dates as ISO 8601", &f.Dates, "")
		choose("Dates as Oct 8, 2026", &f.Dates, "short")
		choose("Dates in Regional Format", &f.Dates, "regional")
		m.Separator()
		choose("Binary as Text", &f.Binary, "")
		choose("Binary as Hex", &f.Binary, "hex")
		choose("Binary as Base64", &f.Binary, "base64")
		m.Separator()
		choose("NULL as NULL", &f.NullText, "")
		choose("NULL as [NULL]", &f.NullText, "[NULL]")
		m.Separator()
		if m.Item("Zoom In").Chosen() {
			a.Settings().GridFont, changed = min(a.Settings().GridFontSize()+1, 22), true
		}
		if m.Item("Zoom Out").Chosen() {
			a.Settings().GridFont, changed = max(a.Settings().GridFontSize()-1, 9), true
		}
		if m.Item("Actual Size").Chosen() {
			a.Settings().GridFont, changed = 0, true
		}
		if changed {
			a.SaveSettings()
		}
	})
}

// colorRule colors the rows whose column holds a value.
type colorRule struct {
	Column string `json:"column"`
	Value  string `json:"value"` // as db.Display writes it; "NULL" for NULL
	Color  string `json:"color"`
}

var rowColors = []struct{ name, hex string }{{"Yellow", "#fde68a"}, {"Green", "#bbf7d0"}, {"Blue", "#bfdbfe"}, {"Red", "#fecaca"}}

// rowColor is the color of a data row, by the first rule it matches.
func (g *Grid) rowColor(src *Source, row int) (string, bool) {
	if row >= len(src.Rows) {
		return "", false
	}
	for _, r := range g.colorRules {
		for i, c := range src.Cols {
			if c.Name == r.Column && db.Display(src.Rows[row][i]) == r.Value {
				return r.Color, true
			}
		}
	}
	return "", false
}

// colorMenu offers to color the rows that hold a cell's value.
func (g *Grid) colorMenu(m *ui.Menu, src *Source, row, col int) {
	if row >= len(src.Rows) {
		return
	}
	if col != wholeRow {
		name, value := src.Cols[col].Name, db.Display(src.Rows[row][col])
		m.Submenu("Color Rows Where "+name+" = "+cellText(value, 24), func(m *ui.Menu) {
			for _, c := range rowColors {
				if m.Item(c.name).Chosen() {
					g.colorRules = append([]colorRule{{Column: name, Value: value, Color: c.hex}}, g.colorRules...)
					if g.colorsChanged != nil {
						g.colorsChanged(g.colorRules)
					}
				}
			}
		})
	}
	if len(g.colorRules) > 0 && m.Item("Reset Row Colors").Chosen() {
		g.colorRules = nil
		if g.colorsChanged != nil {
			g.colorsChanged(nil)
		}
	}
}

// rowColorsFile keeps each table's row colors, in the project's own
// folder.
const rowColorsFile = "colors.json"

func rowColorRules(p *project.Project, key string) []colorRule {
	var all map[string][]colorRule
	if p == nil || p.Local == nil || p.Local.LoadJSON(rowColorsFile, &all) != nil {
		return nil
	}
	return all[key]
}

func saveRowColors(p *project.Project, key string, rules []colorRule) {
	if p == nil || p.Local == nil {
		return
	}
	all := map[string][]colorRule{}
	p.Local.LoadJSON(rowColorsFile, &all)
	if len(rules) == 0 {
		delete(all, key)
	} else {
		all[key] = rules
	}
	p.Local.SaveJSON(rowColorsFile, all)
}

// RenameConnection moves a project's settings of a connection's tables,
// kept by "connection/schema.table", from the ID old to new: the virtual
// keys and hidden values of its file, which is the caller's to save, and
// the recent filters, row colors and editors' parameter values of its
// own folder.
func RenameConnection(p *project.Project, old, new string) error {
	rekey := func(keys []string, move func(from, to string)) {
		for _, k := range keys {
			if rest, ok := strings.CutPrefix(k, old+"/"); ok {
				move(k, new+"/"+rest)
			}
		}
	}
	for _, m := range []map[string][]string{p.VirtualKeys, p.HiddenValues} {
		rekey(slices.Collect(maps.Keys(m)), func(from, to string) { m[to] = m[from]; delete(m, from) })
	}
	if p.Local == nil {
		return nil
	}
	defer paramsVersion.Add(1)
	var errs []error
	for _, name := range []string{filterHistoryFile, rowColorsFile, queryParamsFile} {
		var all map[string]json.RawMessage
		if err := p.Local.LoadJSON(name, &all); err != nil || all == nil {
			continue
		}
		changed := false
		rekey(slices.Collect(maps.Keys(all)), func(from, to string) { all[to] = all[from]; delete(all, from); changed = true })
		if changed {
			errs = append(errs, p.Local.SaveJSON(name, all))
		}
	}
	return errors.Join(errs...)
}
