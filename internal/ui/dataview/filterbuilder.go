package dataview

import (
	"errors"
	"slices"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/lists"
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
	return lists.Map(builderOps, func(o builderOp) string { return o.label })
}

// filterBuilder builds the filter of the rows from conditions chosen
// rather than typed: a column, an operator and a value each.
type filterBuilder struct {
	// any joins the conditions with OR, else with AND.
	any   int
	conds []builderCond
	keys  rowKeys
	// from is the filter of the rows it was filled from.
	from string
}

type builderCond struct {
	column, op string // labels the selects show
	value, to  string
	// sql is the condition as applied, sent as it is while the column,
	// operator and values are still those shown: values made text again
	// may not mean what they did. Without a column, it is a filter typed,
	// which shows as its SQL.
	sql   string
	shown [4]string
}

// applied reports whether the condition is sent as it was applied.
func (bc builderCond) applied() bool {
	return bc.sql != "" && (bc.column == "" || bc.shown == [4]string{bc.column, bc.op, bc.value, bc.to})
}

// builtFilter is what the filter builder last applied: the filter it
// wrote and its conditions.
type builtFilter struct {
	where string
	conds []builderCond
	any   int
}

// newFilterBuilder shows the filter of the rows as conditions: those the
// builder applied, or the filter typed as its SQL, then those of the
// Filter menu.
func (v *Viewer) newFilterBuilder() *filterBuilder {
	b := &filterBuilder{from: v.where}
	typed := strings.TrimSpace(v.typedWhere)
	switch {
	case typed == "":
	// Conditions joined with OR stay one when the menu's join them.
	case typed == v.built.where && (v.built.any == 0 || len(v.menuFilters) == 0):
		b.any = v.built.any
		for _, bc := range v.built.conds {
			appendRow(&b.conds, &b.keys, bc)
		}
	default:
		appendRow(&b.conds, &b.keys, builderCond{sql: typed})
	}
	for _, f := range v.menuFilters {
		op := slices.IndexFunc(builderOps, func(o builderOp) bool { return o.op == f.cond.op })
		if op < 0 {
			appendRow(&b.conds, &b.keys, builderCond{sql: f.sql})
			continue
		}
		bc := builderCond{column: v.src.Cols[f.col].Name, op: builderOps[op].label, sql: f.sql}
		vals := lists.Map(f.cond.vals, func(x any) string { return db.Display(x) })
		if f.cond.op == "in" {
			bc.value = strings.Join(vals, ", ")
		} else if len(vals) > 0 {
			bc.value = vals[0]
		}
		bc.shown = [4]string{bc.column, bc.op, bc.value, bc.to}
		appendRow(&b.conds, &b.keys, bc)
	}
	if len(b.conds) == 0 {
		b.add(v.src.Cols[0].Name)
	}
	return b
}

func (b *filterBuilder) add(column string) {
	appendRow(&b.conds, &b.keys, builderCond{column: column, op: builderOps[0].label})
}

// where writes the conditions as the filter of the rows.
func (b *filterBuilder) where(v *Viewer) (string, error) {
	d := v.dialect()
	return condsSQL(v.source.Conn.Config.Engine, b.any == 1, b.conds, func(column string) (string, bool) {
		ok := slices.ContainsFunc(v.src.Cols, func(c db.ColumnInfo) bool { return c.Name == column })
		return d.Quote(column), ok
	})
}

// condsSQL writes conditions chosen, each a column, an operator and its
// values, as SQL, all of them or one at least; expr writes a
// column as SQL, false for one not known. orJoined joins them with OR.
func condsSQL(e db.Engine, orJoined bool, conds []builderCond, expr func(column string) (string, bool)) (string, error) {
	var parts []string
	for _, bc := range conds {
		if bc.applied() {
			sql := bc.sql
			if len(conds) > 1 {
				sql = "(" + sql + ")"
			}
			parts = append(parts, sql)
			continue
		}
		col, ok := expr(bc.column)
		op := builderOps[slices.IndexFunc(builderOps, func(o builderOp) bool { return o.label == bc.op })]
		if !ok {
			return "", errors.New("choose a column")
		}
		c := rowCond{op: op.op}
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
			return "", errors.New("enter a value for " + bc.column)
		}
		sql := condExprSQL(e, col, c)
		if orJoined && len(conds) > 1 {
			sql = "(" + sql + ")"
		}
		parts = append(parts, sql)
	}
	join := " AND "
	if orJoined {
		join = " OR "
	}
	return strings.Join(parts, join), nil
}

// builderView shows the conditions under the filter bar, and applies
// them as the filter of the rows. Its controls are as compact as the
// toolbar's above it.
func (v *Viewer) builderView(c *ui.Context) {
	if v.builder.from != v.where {
		v.builder = v.newFilterBuilder() // the filter changed elsewhere
	}
	b := v.builder
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	columns := lists.Map(v.src.Cols, func(col db.ColumnInfo) string { return col.Name })
	ops := builderOpLabels()
	where, err := b.where(v)
	apply := func() {
		v.built = builtFilter{where: where, conds: slices.Clone(b.conds), any: b.any}
		v.setFilter(func() {
			v.where, v.typedWhere, v.menuFilters = where, where, nil
			v.whereIn, v.count = where, -1
		})
	}
	// The connectors' column: as wide as + Condition below them, so the
	// conditions and the preview start in line.
	const connectorWidth = 96
	compact := *th
	compact.Spacing, compact.FontSize = 2.5, 12.5
	c.SetTheme(&compact)
	defer c.SetTheme(th)
	ui.Column(c).Padding(6, 8).Gap(4).Background(th.Surface).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
		remove := -1
		submitted := false
		for i := range b.conds {
			bc := &b.conds[i]
			if bc.column == "" {
				// A filter typed: its SQL, which only its removal changes.
				ui.Row(c.Key(b.keys.key("cond-", i))).Gap(6).AlignItems(ui.Center).Children(func() {
					builderConnector(c, b, i, pal, compact.Space(3.5)+1, connectorWidth)
					ui.Text(c, bc.sql).Font(widgets.MonoFont).FontSize(12.5).SingleLine().Shrink(1).Grow(1).
						Padding(compact.Space(1.5), compact.Space(2.5)).Radius(compact.Radius).Border(1, th.Border).Selectable().
						Tooltip("Typed in the filter bar: as SQL, it is kept as typed")
					if widgets.IconButton(c, widgets.IconX, "Remove the condition").Clicked() {
						remove = i
					}
				})
				continue
			}
			op := builderOps[slices.IndexFunc(builderOps, func(o builderOp) bool { return o.label == bc.op })]
			ui.Row(c.Key(b.keys.key("cond-", i))).Gap(6).AlignItems(ui.Center).Children(func() {
				builderConnector(c, b, i, pal, compact.Space(3.5)+1, connectorWidth)
				ui.Select(c, &bc.column, columns).Label("Column").Width(180)
				ui.Select(c, &bc.op, ops).Label("Operator").Width(130)
				if op.values > 0 {
					placeholder := "value"
					if op.op == "in" {
						placeholder = "a, b, c"
					}
					submitted = ui.TextInput(c, &bc.value).Placeholder(placeholder).Font(widgets.MonoFont).FontSize(12.5).Grow(1).Label("Value").Submitted() || submitted
				}
				if op.values == 2 {
					ui.Text(c, "and").TextColor(pal.Muted)
					submitted = ui.TextInput(c, &bc.to).Placeholder("value").Font(widgets.MonoFont).FontSize(12.5).Grow(1).Label("Second value").Submitted() || submitted
				}
				if (len(b.conds) > 1 || bc.sql != "") && widgets.IconButton(c, widgets.IconX, "Remove the condition").Clicked() {
					remove = i
				}
			})
		}
		if remove >= 0 {
			deleteRow(&b.conds, &b.keys, remove)
			if len(b.conds) == 0 {
				b.add(columns[0])
			}
		}
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			if widgets.ToolButton(c, widgets.IconPlus, "Condition", "Add a condition").Width(connectorWidth).Clicked() {
				column := columns[0]
				if last := b.conds[len(b.conds)-1].column; last != "" {
					column = last
				}
				b.add(column)
			}
			if err != nil {
				ui.Text(c, widgets.Sentence(err.Error())).TextColor(pal.Muted).SingleLine().Shrink(1)
			} else {
				ui.Text(c, "WHERE "+where).Font(widgets.MonoFont).TextColor(pal.Muted).SingleLine().Shrink(1).Tooltip(where).Selectable()
			}
			ui.Spacer(c)
			if ui.Button(c, "Close").Tooltip("Close the conditions; the filter applied stays").Clicked() {
				v.builder = nil
			}
			if ui.PrimaryButton(c, "Apply").Disabled(err != nil).Clicked() || submitted && err == nil {
				apply()
			}
		})
	})
}

// builderConnector shows how the condition i joins those before it: "Where" for
// the first, the choice of "and" or "or" for the second, which joins them
// all, and that choice again after. Its words line up with the text of a
// select, inset by inset.
func builderConnector(c *ui.Context, b *filterBuilder, i int, pal *widgets.Palette, inset, width float32) {
	joins := []string{"and", "or"}
	ui.Row(c).Width(width).Shrink(0).Children(func() {
		switch i {
		case 0:
			ui.Text(c, "Where").TextColor(pal.Muted).Padding(0, inset)
		case 1:
			join := joins[b.any]
			if ui.Select(c, &join, joins).Label("Join the conditions with").Grow(1).Changed() {
				b.any = slices.Index(joins, join)
			}
		default:
			ui.Text(c, joins[b.any]).TextColor(pal.Muted).Padding(0, inset)
		}
	})
}
