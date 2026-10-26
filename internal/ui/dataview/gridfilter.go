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
// "notnull", "contains", "in" or "notin", which the grid also matches rows with
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
	case "notin":
		if v == nil {
			return !slices.Contains(c.vals, nil)
		}
		return !slices.ContainsFunc(c.vals, func(x any) bool { return x != nil && compareValues(v, x) == 0 })
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
	g.conds = slices.DeleteFunc(g.conds, func(c rowCond) bool { return col < 0 || c.col == col })
	g.orderKey = ""
}

func (g *Grid) passes(row []any) bool {
	return !slices.ContainsFunc(g.conds, func(c rowCond) bool { return !c.matches(row) })
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
	case "in", "notin":
		var vals []string
		null := false
		for _, v := range c.vals {
			if v == nil {
				null = true
				continue
			}
			vals = append(vals, lit(v))
		}
		list := " (" + strings.Join(vals, ", ") + ")"
		if c.op == "notin" {
			// A row with NULL stays unless NULL is one of the values left out.
			switch {
			case null && len(vals) == 0:
				return col + " IS NOT NULL"
			case null:
				return col + " NOT IN" + list
			}
			return "(" + col + " NOT IN" + list + " OR " + col + " IS NULL)"
		}
		switch {
		case null && len(vals) == 0:
			return col + " IS NULL"
		case null:
			return "(" + col + " IN" + list + " OR " + col + " IS NULL)"
		}
		return col + " IN" + list
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

// likeSQL matches a column's text, ignoring case where the engine can,
// against a value with wildcards before and after it: the value's own %
// and _ are text, escaped with a backslash, the escape of PostgreSQL,
// MySQL and ClickHouse, and named for SQLite and DuckDB, which have none.
func likeSQL(e db.Engine, col, before string, value any, after string, not bool) string {
	escaped := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(db.Display(value))
	return db.LikeFilterSQL(e, col, db.Literal(e, before+escaped+after), not)
}

// filterMenu is the Filter submenu of a cell: its value, or the values
// of the rows chosen, against the column.
func (g *Grid) filterMenu(m *ui.Menu, a Host, src *Source, row, col int, apply func(rowCond), clear func(col int)) {
	v := g.plainValue(src, row, col)
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
		// The clipboard is read once an item is chosen: reading it as the
		// menu builds can block on the program that owns it (Wayland).
		m.Submenu("By Clipboard Value", func(m *ui.Menu) {
			for _, op := range ops {
				if m.Item(fmt.Sprintf("%s %s clipboard value", name, op)).Chosen() {
					if clip := a.ReadClipboard(); clip != "" {
						apply(rowCond{col: col, op: op, vals: []any{typedFilterValue(clip)}})
					}
				}
			}
		})
		if keymap.Item(m.Item("Distinct Values…"), keymap.DistinctValues).Chosen() {
			openDistinct(a, g, src, col, apply)
		}
		if rows := g.selectedRows(src); len(rows) > 1 {
			vals := g.chosenValues(src, rows, col)
			if len(vals) > 0 && m.Item(fmt.Sprintf("%s IN (%s chosen)", name, widgets.Count(len(vals), "value"))).Chosen() {
				apply(rowCond{col: col, op: "in", vals: vals})
			}
			if len(vals) > 0 && m.Item(fmt.Sprintf("%s NOT IN (%s chosen)", name, widgets.Count(len(vals), "value"))).Chosen() {
				apply(rowCond{col: col, op: "notin", vals: vals})
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
			g.sort = []ui.SortOrder{{Column: id}}
		}
		if m.Item("Descending").Chosen() {
			g.sort = []ui.SortOrder{{Column: id, Descending: true}}
		}
		if len(g.sort) > 0 && g.sort[0].Column != id {
			if m.Item("Then by This, Ascending").Chosen() {
				g.sort = append(withoutSort(g.sort, id), ui.SortOrder{Column: id})
			}
			if m.Item("Then by This, Descending").Chosen() {
				g.sort = append(withoutSort(g.sort, id), ui.SortOrder{Column: id, Descending: true})
			}
		}
		if m.Item("No Order").Disabled(len(g.sort) == 0).Chosen() {
			g.sort = nil
		}
	})
}

// withoutSort is a new order of order's columns but id: the grid's order
// changes to a new slice, as the table's SortBy does.
func withoutSort(order []ui.SortOrder, id string) []ui.SortOrder {
	return slices.DeleteFunc(slices.Clone(order), func(o ui.SortOrder) bool { return o.Column == id })
}

// sortKeys is the columns of an order that the rows have, the first first.
func sortKeys(order []ui.SortOrder, ncols int) []ui.SortOrder {
	var out []ui.SortOrder
	for _, k := range order {
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
	widgets.Modal(c, &f.open, func() {
		ui.Column(c).Width(420).Gap(10).Children(func() {
			ui.Text(c, "Filter: "+f.label).FontSize(15).Bold()
			submit := ui.TextInput(c, &f.value).AutoFocus().Font(widgets.MonoFont).Label("Value").Submitted()
			ui.Text(c, "A number when it reads as one, else text.").FontSize(12).TextColor(widgets.PaletteOf(c).Muted)
			if widgets.DialogFooter(c, &f.open, "Filter", submit) {
				f.open = false
				f.apply(f.value)
			}
		})
	})
	dropClosed(&a.Dialogs().filterPrompt, f, f.open)
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
	dismiss widgets.DismissGuard
}

// maxDistinct is how many values the distinct-values filter lists.
const maxDistinct = 1000

func openDistinct(a Host, g *Grid, src *Source, col int, apply func(rowCond)) {
	f := &distinctForm{open: true, name: src.Cols[col].Name, apply: apply, col: col, loading: true}
	a.Dialogs().distinct = f
	done := func(vals []distinctValue, err error) {
		f.loading, f.values = false, vals
		f.dismiss.Used() // the values read are the dialog's; none can be ticked before
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
	dlg := widgets.Modal(c, &f.open, func() {
		ui.Column(c).Width(520).Height(520).Gap(10).Children(func() {
			ui.Text(c, "Filter "+f.name+" by its values").FontSize(15).Bold()
			ui.Row(c).Children(func() { widgets.SearchBox(c, &f.search, "Find a value", 0).AutoFocus() })
			switch {
			case f.loading:
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
				if widgets.Activated(c, ui.PrimaryButton(c, "Filter").Disabled(len(chosen) == 0)) {
					f.open = false
					f.apply(rowCond{col: f.col, op: "in", vals: chosen})
				}
			})
		})
	})
	checked := make([]bool, len(f.values))
	for i, v := range f.values {
		checked[i] = v.checked
	}
	f.dismiss.Keep(c, dlg, &f.open, checked, false)
	dropClosed(&a.Dialogs().distinct, f, f.open)
}

// filterFuncs are how the grid filters: on the server for a table, else
// on the rows read.
func (g *Grid) filterFuncs() (func(rowCond), func(int)) {
	if g.filterSQL != nil {
		return g.filterSQL, g.clearSQL
	}
	return g.addCond, g.clearConds
}

// chosenValues are the values of rows in col, each once in the order of
// the rows, NULL too: a value is the same as another of its type and text.
func (g *Grid) chosenValues(src *Source, rows []int, col int) []any {
	seen := map[string]bool{}
	var vals []any
	for _, r := range rows {
		x, _ := g.value(src, r, col)
		if x == unset || x == db.Default {
			continue
		}
		if k := fmt.Sprintf("%T\x00%s", x, db.Display(x)); !seen[k] {
			seen[k] = true
			vals = append(vals, x)
		}
	}
	return vals
}
