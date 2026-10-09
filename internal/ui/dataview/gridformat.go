package dataview

import (
	"fmt"
	"math/big"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// SelectionStats sums up the chosen column over the chosen rows, when
// there are several: DBeaver's Calc in one line.
func (g *Grid) SelectionStats(src *Source) string {
	rows := g.selectedRows(src)
	if len(rows) < 2 || g.selCol >= len(src.Cols) {
		return ""
	}
	nulls, count := 0, 0
	sum := new(big.Float)
	var minV, maxV *big.Float
	for _, r := range rows {
		v, _ := g.value(src, r, g.selCol)
		if v == nil || v == unset || v == db.Default {
			nulls++
			continue
		}
		n, ok := new(big.Float).SetString(db.Display(v))
		if !ok || !db.IsNumeric(v) {
			count = -1
			continue
		}
		if count >= 0 {
			count++
		}
		sum.Add(sum, n)
		if minV == nil || n.Cmp(minV) < 0 {
			minV = n
		}
		if maxV == nil || n.Cmp(maxV) > 0 {
			maxV = n
		}
	}
	nullNote := ""
	if nulls > 0 {
		nullNote = fmt.Sprintf(" · %d NULL", nulls)
	}
	if count <= 0 {
		return widgets.Count(len(rows), "row") + nullNote
	}
	avg := new(big.Float).Quo(sum, big.NewFloat(float64(count)))
	num := func(f *big.Float) string {
		s := f.Text('f', 10)
		if strings.Contains(s, ".") {
			s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
		}
		return s
	}
	return fmt.Sprintf("%d values · sum %s · avg %s · min %s · max %s%s", count, num(sum), num(avg), num(minV), num(maxV), nullNote)
}

// cellText is a value's text as a cell shows it: its first line, cut to
// max runes.
func cellText(s string, max int) string {
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
		toggle("Booleans as ✓ and ✗", &f.BoolTicks)
		m.Separator()
		choose("Dates as ISO 8601", &f.Dates, "")
		choose("Dates as Oct 8, 2026", &f.Dates, "short")
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
