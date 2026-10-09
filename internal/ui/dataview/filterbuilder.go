package dataview

import (
	"errors"
	"slices"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// builderOp is an operator of the filter builder: its label, the
// condition's op, and how many values it takes.
type builderOp struct {
	label, op string
	values    int
}

var builderOps = []builderOp{
	{"=", "=", 1}, {"≠", "<>", 1}, {">", ">", 1}, {"≥", ">=", 1}, {"<", "<", 1}, {"≤", "<=", 1},
	{"contains", "contains", 1}, {"does not contain", "notcontains", 1}, {"starts with", "prefix", 1}, {"ends with", "suffix", 1},
	{"is one of", "in", 1}, {"is between", "between", 2}, {"is NULL", "null", 0}, {"is not NULL", "notnull", 0},
}

func builderOpLabels() []string {
	out := make([]string, len(builderOps))
	for i, o := range builderOps {
		out[i] = o.label
	}
	return out
}

// filterBuilder builds the filter of the rows from conditions chosen
// rather than typed: a column, an operator and a value each.
type filterBuilder struct {
	// any joins the conditions with OR, else with AND.
	any   int
	conds []builderCond
	keys  rowKeys
}

type builderCond struct {
	column, op string // labels the selects show
	value, to  string
}

func newFilterBuilder(firstColumn string) *filterBuilder {
	b := &filterBuilder{}
	b.add(firstColumn)
	return b
}

func (b *filterBuilder) add(column string) {
	b.conds = append(b.conds, builderCond{column: column, op: builderOps[0].label})
	b.keys.add()
}

// where writes the conditions as the filter of the rows.
func (b *filterBuilder) where(v *Viewer) (string, error) {
	var parts []string
	for _, bc := range b.conds {
		col := slices.IndexFunc(v.src.Cols, func(c db.ColumnInfo) bool { return c.Name == bc.column })
		op := builderOps[slices.IndexFunc(builderOps, func(o builderOp) bool { return o.label == bc.op })]
		if col < 0 {
			return "", errors.New("choose a column")
		}
		c := rowCond{col: col, op: op.op}
		switch {
		case op.op == "in":
			for _, s := range strings.Split(bc.value, ",") {
				if s = strings.TrimSpace(s); s != "" {
					c.vals = append(c.vals, typedFilterValue(s))
				}
			}
		case op.values == 2:
			c.vals = []any{typedFilterValue(strings.TrimSpace(bc.value)), typedFilterValue(strings.TrimSpace(bc.to))}
		case op.values == 1:
			c.vals = []any{typedFilterValue(strings.TrimSpace(bc.value))}
		}
		if op.values > 0 && (strings.TrimSpace(bc.value) == "" || op.values == 2 && strings.TrimSpace(bc.to) == "") {
			return "", errors.New(bc.column + " " + bc.op + " needs a value")
		}
		sql := condSQL(v.dialect(), v.source.Conn.Config.Engine, v.src.Cols[col].Name, c)
		if b.any == 1 && len(b.conds) > 1 {
			sql = "(" + sql + ")"
		}
		parts = append(parts, sql)
	}
	join := " AND "
	if b.any == 1 {
		join = " OR "
	}
	return strings.Join(parts, join), nil
}

// builderView shows the conditions under the filter bar, and applies
// them as the filter of the rows.
func (v *Viewer) builderView(c *ui.Context) {
	b := v.builder
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	columns := make([]string, len(v.src.Cols))
	for i, col := range v.src.Cols {
		columns[i] = col.Name
	}
	ops := builderOpLabels()
	where, err := b.where(v)
	ui.Column(c).Padding(8, 12).Gap(6).Background(th.Surface).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
		remove := -1
		for i := range b.conds {
			bc := &b.conds[i]
			op := builderOps[slices.IndexFunc(builderOps, func(o builderOp) bool { return o.label == bc.op })]
			ui.Row(c.Key(b.keys.key("cond-", i))).Gap(8).AlignItems(ui.Center).Children(func() {
				if i == 0 {
					ui.Text(c, "Where").FontSize(12).TextColor(pal.Muted).Width(56)
				} else if b.any == 1 {
					ui.Text(c, "or").FontSize(12).TextColor(pal.Muted).Width(56)
				} else {
					ui.Text(c, "and").FontSize(12).TextColor(pal.Muted).Width(56)
				}
				ui.Select(c, &bc.column, columns).Label("Column").Width(200)
				ui.Select(c, &bc.op, ops).Label("Operator").Width(150)
				if op.values > 0 {
					placeholder := "value"
					if op.op == "in" {
						placeholder = "a, b, c"
					}
					ui.TextInput(c, &bc.value).Placeholder(placeholder).Font(widgets.MonoFont).Width(220).Label("Value")
				}
				if op.values == 2 {
					ui.Text(c, "and").FontSize(12).TextColor(pal.Muted)
					ui.TextInput(c, &bc.to).Placeholder("value").Font(widgets.MonoFont).Width(220).Label("Second value")
				}
				ui.Spacer(c)
				if widgets.IconButton(c, widgets.IconX, "Remove the condition").Disabled(len(b.conds) == 1).Clicked() {
					remove = i
				}
			})
		}
		if remove >= 0 {
			b.conds = slices.Delete(b.conds, remove, remove+1)
			b.keys.remove(remove)
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if widgets.ToolButton(c, widgets.IconPlus, "Condition", "Add a condition").Clicked() {
				b.add(columns[0])
			}
			if len(b.conds) > 1 {
				ui.Segmented(c, &b.any, "All", "Any").Label("Rows meeting")
			}
			if err != nil {
				ui.Text(c, capitalize(err.Error())+".").FontSize(12).TextColor(pal.Muted)
			}
			ui.Spacer(c)
			if ui.Button(c, "Close").Clicked() {
				v.builder = nil
			}
			if ui.PrimaryButton(c, "Apply").Disabled(err != nil).Tooltip(where).Clicked() {
				v.setFilter(func() {
					v.where, v.typedWhere, v.menuFilters = where, where, nil
					v.whereIn, v.count = where, -1
				})
			}
		})
	})
}
