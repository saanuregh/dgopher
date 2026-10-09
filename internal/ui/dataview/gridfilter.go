package dataview

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/params"
	"dgopher/internal/project"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// rowCond is a filter of a column: op is "=", "<>", ">", "<", "null",
// "notnull", "contains" or "in", which the grid also matches rows with
// itself, or ">=", "<=", "notcontains", "prefix", "suffix" or "between",
// which the filter builder writes as SQL.
type rowCond struct {
	col  int
	op   string
	vals []any
}

// matches reports whether a row passes the condition, as the database
// would: NULL passes none but "null".
func (c rowCond) matches(row []any) bool {
	v := row[c.col]
	switch c.op {
	case "null":
		return v == nil
	case "notnull":
		return v != nil
	case "in":
		if v == nil {
			return slices.Contains(c.vals, nil)
		}
	}
	if v == nil {
		return false
	}
	switch c.op {
	case "=":
		return compareValues(v, c.vals[0]) == 0
	case "<>":
		return compareValues(v, c.vals[0]) != 0
	case ">":
		return compareValues(v, c.vals[0]) > 0
	case "<":
		return compareValues(v, c.vals[0]) < 0
	case "contains":
		return strings.Contains(strings.ToLower(db.Display(v)), strings.ToLower(db.Display(c.vals[0])))
	case "in":
		for _, x := range c.vals {
			if x != nil && compareValues(v, x) == 0 {
				return true
			}
		}
	}
	return false
}

// addCond filters the grid's rows on a column, besides the filters it has.
func (g *Grid) addCond(c rowCond) {
	g.conds = append(g.conds, c)
	g.orderKey = ""
}

// clearConds drops the filters of a column, or all of them for -1.
func (g *Grid) clearConds(col int) {
	kept := g.conds[:0]
	for _, c := range g.conds {
		if col >= 0 && c.col != col {
			kept = append(kept, c)
		}
	}
	g.conds = kept
	g.orderKey = ""
}

func (g *Grid) passes(row []any) bool {
	for _, c := range g.conds {
		if !c.matches(row) {
			return false
		}
	}
	return true
}

// condSQL writes a filter of a column as SQL of an engine.
func condSQL(d db.Dialect, e db.Engine, column string, c rowCond) string {
	return condExprSQL(e, d.Quote(column), c)
}

// condExprSQL writes a condition on a column already written as SQL, as
// a joined table's, alias.column.
func condExprSQL(e db.Engine, col string, c rowCond) string {
	lit := func(v any) string { return db.Literal(e, v) }
	switch c.op {
	case "null":
		return col + " IS NULL"
	case "notnull":
		return col + " IS NOT NULL"
	case "in":
		var vals []string
		null := false
		for _, v := range c.vals {
			if v == nil {
				null = true
				continue
			}
			vals = append(vals, lit(v))
		}
		in := col + " IN (" + strings.Join(vals, ", ") + ")"
		switch {
		case null && len(vals) == 0:
			return col + " IS NULL"
		case null:
			return "(" + in + " OR " + col + " IS NULL)"
		}
		return in
	case "contains":
		return likeSQL(e, col, "%", c.vals[0], "%", false)
	case "notcontains":
		return likeSQL(e, col, "%", c.vals[0], "%", true)
	case "prefix":
		return likeSQL(e, col, "", c.vals[0], "%", false)
	case "suffix":
		return likeSQL(e, col, "%", c.vals[0], "", false)
	case "between":
		return col + " BETWEEN " + lit(c.vals[0]) + " AND " + lit(c.vals[1])
	}
	return col + " " + c.op + " " + lit(c.vals[0])
}

// likeSQL matches a column's text, ignoring case, against a value with
// wildcards before and after it: the value's own % and _ are text,
// escaped with a backslash, the escape of PostgreSQL, MySQL and
// ClickHouse, and named for SQLite and DuckDB, which have none.
func likeSQL(e db.Engine, col, before string, value any, after string, not bool) string {
	escaped := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(db.Display(value))
	pattern := db.Literal(e, before+escaped+after)
	like, ilike := " LIKE ", " ILIKE "
	if not {
		like, ilike = " NOT LIKE ", " NOT ILIKE "
	}
	switch e {
	case db.Postgres:
		return "CAST(" + col + " AS TEXT)" + ilike + pattern
	case db.DuckDB:
		return "CAST(" + col + " AS VARCHAR)" + ilike + pattern + " ESCAPE '\\'"
	case db.ClickHouse:
		return "toString(" + col + ")" + ilike + pattern
	case db.MySQL:
		return "CAST(" + col + " AS CHAR)" + like + pattern
	}
	return "CAST(" + col + " AS TEXT)" + like + pattern + " ESCAPE '\\'"
}

// filterMenu is the Filter submenu of a cell: its value, or the values
// of the rows chosen, against the column.
func (g *Grid) filterMenu(m *ui.Menu, a Host, src *Source, row, col int, apply func(rowCond), clear func(col int)) {
	v, _ := g.value(src, row, col)
	if v == unset || v == db.Default {
		v = nil
	}
	if typed, ok := v.(db.Typed); ok {
		v = string(typed)
	}
	name := src.Cols[col].Name
	shown := cellText(db.Display(v), 40)
	m.Submenu("Filter", func(m *ui.Menu) {
		if v == nil {
			if m.Item(name + " IS NULL").Chosen() {
				apply(rowCond{col: col, op: "null"})
			}
		} else {
			for _, op := range []string{"=", "<>", ">", "<"} {
				if m.Item(fmt.Sprintf("%s %s %s", name, op, shown)).Chosen() {
					apply(rowCond{col: col, op: op, vals: []any{v}})
				}
			}
			if m.Item(fmt.Sprintf("%s contains %s", name, shown)).Chosen() {
				apply(rowCond{col: col, op: "contains", vals: []any{v}})
			}
		}
		if m.Item(name + " IS NOT NULL").Chosen() {
			apply(rowCond{col: col, op: "notnull"})
		}
		ops := []string{"=", "<>", ">", "<", "contains"}
		m.Submenu("By Entered Value", func(m *ui.Menu) {
			for _, op := range ops {
				if m.Item(fmt.Sprintf("%s %s …", name, op)).Chosen() {
					a.Dialogs().filterPrompt = &filterPrompt{open: true, label: name + " " + op, apply: func(s string) {
						apply(rowCond{col: col, op: op, vals: []any{typedFilterValue(s)}})
					}}
				}
			}
		})
		clip := a.ReadClipboard()
		m.Submenu("By Clipboard Value", func(m *ui.Menu) {
			for _, op := range ops {
				if m.Item(fmt.Sprintf("%s %s %s", name, op, cellText(clip, 30))).Disabled(clip == "").Chosen() {
					apply(rowCond{col: col, op: op, vals: []any{typedFilterValue(clip)}})
				}
			}
		})
		if keymap.Item(m.Item("Distinct Values…"), keymap.DistinctValues).Chosen() {
			openDistinct(a, g, src, col, apply)
		}
		if rows := g.selectedRows(src); len(rows) > 1 {
			var vals []any
			for _, r := range rows {
				if x, _ := g.value(src, r, col); x != nil && x != unset && x != db.Default {
					vals = append(vals, x)
				}
			}
			if len(vals) > 0 && m.Item(fmt.Sprintf("%s IN (%d values chosen)", name, len(vals))).Chosen() {
				apply(rowCond{col: col, op: "in", vals: vals})
			}
		}
		m.Separator()
		if m.Item("Remove the Filters of " + name).Chosen() {
			clear(col)
		}
		if m.Item("Clear All Filters").Chosen() {
			clear(-1)
		}
	})
}

// orderMenu is the Order submenu of a column.
func (g *Grid) orderMenu(m *ui.Menu, col int) {
	id := colID(col)
	m.Submenu("Order", func(m *ui.Menu) {
		if m.Item("Ascending").Chosen() {
			g.sort = ui.SortOrder{Column: id}
		}
		if m.Item("Descending").Chosen() {
			g.sort = ui.SortOrder{Column: id, Descending: true}
		}
		if g.sort.Column != "" && g.sort.Column != id {
			if m.Item("Then by This, Ascending").Chosen() {
				g.sort.Then = append(withoutSortKey(g.sort.Then, id), ui.SortKey{Column: id})
			}
			if m.Item("Then by This, Descending").Chosen() {
				g.sort.Then = append(withoutSortKey(g.sort.Then, id), ui.SortKey{Column: id, Descending: true})
			}
		}
		if m.Item("No Order").Disabled(g.sort.Column == "").Chosen() {
			g.sort = ui.SortOrder{}
		}
	})
}

func withoutSortKey(keys []ui.SortKey, id string) []ui.SortKey {
	var out []ui.SortKey
	for _, k := range keys {
		if k.Column != id {
			out = append(out, k)
		}
	}
	return out
}

// sortKeys is an order as columns and directions, the first one first.
func sortKeys(o ui.SortOrder, ncols int) []ui.SortKey {
	var out []ui.SortKey
	for _, k := range append([]ui.SortKey{{Column: o.Column, Descending: o.Descending}}, o.Then...) {
		if col, err := strconv.Atoi(k.Column); err == nil && col >= 0 && col < ncols {
			out = append(out, k)
		}
	}
	return out
}

// typedFilterValue is a value typed for a filter: a number when it reads
// as one, else text.
func typedFilterValue(s string) any {
	if n, ok := params.NumberOf(s); ok {
		return n
	}
	return s
}

// filterSuggestions are what the filter bar offers: the recent filters
// when it is empty, else the columns whose names start with the word
// being typed.
func filterSuggestions(text string, cols []db.ColumnInfo, history []string) []string {
	if strings.TrimSpace(text) == "" {
		return history
	}
	word := lastWord(text)
	if word == "" {
		return nil
	}
	var out []string
	for _, c := range cols {
		if strings.HasPrefix(strings.ToLower(c.Name), strings.ToLower(word)) && !strings.EqualFold(c.Name, word) {
			out = append(out, c.Name)
		}
	}
	return out
}

// lastWord is the name being typed at the end of a text.
func lastWord(text string) string {
	i := len(text)
	for i > 0 {
		r := text[i-1]
		if r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			i--
			continue
		}
		break
	}
	return text[i:]
}

// completeFilter puts a chosen suggestion in the filter: a column name in
// place of the word being typed, else a whole recent filter.
func completeFilter(text, choice string) string {
	if strings.TrimSpace(text) == "" {
		return choice
	}
	return text[:len(text)-len(lastWord(text))] + choice
}

// filterHistoryFile keeps each table's recent filters, in the project's
// own folder.
const filterHistoryFile = "filters.json"

func filterHistory(p *project.Project, key string) []string {
	var all map[string][]string
	if p.Local == nil || p.Local.LoadJSON(filterHistoryFile, &all) != nil {
		return nil
	}
	return all[key]
}

// rememberFilter puts a filter first in a table's recent filters.
func rememberFilter(p *project.Project, key, filter string) {
	filter = strings.TrimSpace(filter)
	if p.Local == nil || filter == "" {
		return
	}
	all := map[string][]string{}
	p.Local.LoadJSON(filterHistoryFile, &all)
	h := slices.DeleteFunc(all[key], func(f string) bool { return f == filter })
	h = append([]string{filter}, h...)
	all[key] = h[:min(len(h), 20)]
	p.Local.SaveJSON(filterHistoryFile, all)
}

// filterPrompt asks the value of a filter.
type filterPrompt struct {
	open  bool
	label string
	value string
	apply func(string)
}

func filterPromptView(a Host, c *ui.Context) {
	f := a.Dialogs().filterPrompt
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(420).Gap(10).Children(func() {
			ui.Text(c, "Filter: "+f.label).FontSize(15).Bold()
			submit := ui.TextInput(c, &f.value).AutoFocus().Font(widgets.MonoFont).Label("Value").Submitted()
			ui.Text(c, "A number when it reads as one, else text.").FontSize(12).TextColor(widgets.PaletteOf(c).Muted)
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					f.open = false
				}
				if ui.PrimaryButton(c, "Filter").Clicked() || submit {
					f.open = false
					f.apply(f.value)
				}
			})
		})
	})
	if !f.open && a.Dialogs().filterPrompt == f {
		a.Dialogs().filterPrompt = nil
	}
}

// distinctValue is a value of a column and how many rows hold it.
type distinctValue struct {
	v       any
	count   int64
	checked bool
}

// distinctForm lists a column's values to filter on by ticking them.
type distinctForm struct {
	open    bool
	name    string
	values  []distinctValue
	loading bool
	err     string
	search  string
	list    ui.ListState
	apply   func(rowCond)
	col     int
}

// maxDistinct is how many values the distinct-values filter lists.
const maxDistinct = 1000

func openDistinct(a Host, g *Grid, src *Source, col int, apply func(rowCond)) {
	f := &distinctForm{open: true, name: src.Cols[col].Name, apply: apply, col: col, loading: true}
	a.Dialogs().distinct = f
	done := func(vals []distinctValue, err error) {
		f.loading, f.values = false, vals
		if err != nil {
			f.err = err.Error()
		}
	}
	if g.distinctOf != nil {
		g.distinctOf(col, done)
		return
	}
	var vals []distinctValue
	at := map[string]int{}
	for _, row := range src.Rows {
		key := fmt.Sprintf("%T:%v", row[col], db.Display(row[col]))
		if i, ok := at[key]; ok {
			vals[i].count++
			continue
		}
		if len(vals) == maxDistinct {
			continue
		}
		at[key] = len(vals)
		vals = append(vals, distinctValue{v: row[col], count: 1})
	}
	slices.SortStableFunc(vals, func(x, y distinctValue) int { return int(y.count - x.count) })
	done(vals, nil)
}

func distinctView(a Host, c *ui.Context) {
	f := a.Dialogs().distinct
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	var shown []int
	q := strings.ToLower(f.search)
	for i, v := range f.values {
		if q == "" || strings.Contains(strings.ToLower(db.Display(v.v)), q) {
			shown = append(shown, i)
		}
	}
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(520).Height(520).Gap(10).Children(func() {
			ui.Text(c, "Filter "+f.name+" by its values").FontSize(15).Bold()
			ui.Row(c).Children(func() { widgets.SearchBox(c, &f.search, "Find a value", 0).AutoFocus() })
			switch {
			case f.loading:
				ui.Row(c).Grow(1).Center().Children(func() { ui.Spinner(c) })
			case f.err != "":
				ui.Text(c, f.err).TextColor(th.Danger).Selectable()
			default:
				ui.List(c, &f.list, len(shown), func(i int) {
					v := &f.values[shown[i]]
					ui.Row(c).Padding(3, 8).Gap(8).Children(func() {
						ui.Checkbox(c, &v.checked, "")
						text := ui.Text(c, cellText(a.Settings().ViewFormat.Format(v.v), 80)).Grow(1).Shrink(1).SingleLine()
						if v.v == nil {
							text.TextColor(pal.Null).Italic()
						}
						ui.Text(c, fmt.Sprint(v.count)).FontSize(12).TextColor(pal.Muted)
					})
				}).Grow(1).Border(1, th.Border).Radius(6)
				if len(f.values) == maxDistinct {
					ui.Text(c, fmt.Sprintf("The %d most frequent values.", maxDistinct)).FontSize(12).TextColor(pal.Muted)
				}
			}
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, "All").Clicked() {
					for _, i := range shown {
						f.values[i].checked = true
					}
				}
				if ui.Button(c, "None").Clicked() {
					for i := range f.values {
						f.values[i].checked = false
					}
				}
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					f.open = false
				}
				var chosen []any
				for _, v := range f.values {
					if v.checked {
						chosen = append(chosen, v.v)
					}
				}
				if ui.PrimaryButton(c, "Filter").Disabled(len(chosen) == 0).Clicked() {
					f.open = false
					f.apply(rowCond{col: f.col, op: "in", vals: chosen})
				}
			})
		})
	})
	if !f.open && a.Dialogs().distinct == f {
		a.Dialogs().distinct = nil
	}
}

// filterFuncs are how the grid filters: on the server for a table, else
// on the rows read.
func (g *Grid) filterFuncs() (func(rowCond), func(int)) {
	if g.filterSQL != nil {
		return g.filterSQL, g.clearSQL
	}
	return g.addCond, g.clearConds
}
