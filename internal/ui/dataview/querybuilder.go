package dataview

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// The aggregates a column of a built query takes, as its select lists
// them; noAggregate, shown for "", is none.
var aggregates = []string{noAggregate, "count", "count distinct", "sum", "avg", "min", "max"}

const noAggregate = "no aggregate"

// qbTable is a table of a built query, and how it joins the tables
// before it.
type qbTable struct {
	schema, name, alias string
	cols                []db.Column
	fks                 []db.ForeignKey
	refs                []db.Reference
	loading             bool
	err                 string
	chosen              []bool // its columns chosen, as cols
	left                int    // 1 for a LEFT JOIN, else an inner one
	on                  []qbOn
}

// qbOn is a condition of a join: a column of the table equal to one of a
// table before it, as alias.column.
type qbOn struct {
	column, other string
}

// qbColumn is a column of the query's result: a table's column, as
// alias.column, and its aggregate.
type qbColumn struct {
	label string
	agg   string
}

type qbSort struct {
	label string
	desc  int // 1 for descending
}

// qbCandidate is a table a query may join, and how.
type qbCandidate struct {
	label        string
	schema, name string
	on           []qbOn // nil for a table no key relates: its join is chosen
}

// queryBuilder builds a SELECT from tables, columns, conditions and an
// order chosen rather than typed.
type queryBuilder struct {
	open     bool
	conn     *connection.Conn
	database string
	tables   []*qbTable
	columns  []qbColumn
	conds    []builderCond
	any      int
	sorts    []qbSort
	limit    string
	distinct bool

	schemaTables []db.Object // the first table's schema's, for a join chosen by hand
	add          string      // the candidate chosen to join
	colKeys      rowKeys
	condKeys     rowKeys
	sortKeys     rowKeys
}

// OpenQueryBuilder builds a query of a table, which can then join others.
func OpenQueryBuilder(a Host, cn *connection.Conn, database string, obj db.Object) {
	b := &queryBuilder{open: true, conn: cn, database: database, limit: "100"}
	a.Dialogs().builder = b
	b.addTable(a, obj.Schema, obj.Name, nil)
	poolOf := cn.PoolFor(database)
	a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := poolOf(ctx)
		var objs []db.Object
		if err == nil {
			objs, err = d.Dialect.Objects(ctx, d.Catalog(), obj.Schema)
		}
		return func() {
			if err == nil {
				// Tables first, then views: both join.
				b.schemaTables = append(connection.SortedObjects(objs, false), connection.SortedObjects(objs, true)...)
			}
		}
	})
}

// addTable adds a table, joined by on, and reads its columns and keys.
func (b *queryBuilder) addTable(a Host, schema, name string, on []qbOn) {
	t := &qbTable{schema: schema, name: name, alias: b.aliasFor(name), loading: true, on: on}
	b.tables = append(b.tables, t)
	poolOf := b.conn.PoolFor(b.database)
	a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := poolOf(ctx)
		var cols []db.Column
		var fks []db.ForeignKey
		var refs []db.Reference
		if err == nil {
			cols, err = d.Dialect.Columns(ctx, d.Catalog(), schema, name)
		}
		if err == nil {
			fks, _ = d.Dialect.ForeignKeys(ctx, d.Catalog(), schema, name)
			refs, _ = d.Dialect.ReferencedBy(ctx, d.Catalog(), schema, name)
		}
		return func() {
			t.loading = false
			if err != nil {
				t.err = err.Error()
				return
			}
			t.cols, t.fks, t.refs, t.chosen = cols, fks, refs, make([]bool, len(cols))
			if others := b.labels(t); len(t.on) == 0 && len(b.tables) > 1 && len(cols) > 0 && len(others) > 0 {
				// Joined by hand: on its first column, to be chosen.
				t.on = []qbOn{{column: cols[0].Name, other: others[0]}}
			}
		}
	})
}

// aliasFor names a table in the query: its name, numbered when another
// is named so.
func (b *queryBuilder) aliasFor(name string) string {
	alias := name
	for n := 2; slices.ContainsFunc(b.tables, func(t *qbTable) bool { return t.alias == alias }); n++ {
		alias = name + strconv.Itoa(n)
	}
	return alias
}

// labels are the columns of the tables before t, as alias.column; of
// every table for nil.
func (b *queryBuilder) labels(t *qbTable) []string {
	var out []string
	for _, o := range b.tables {
		if o == t {
			break
		}
		for _, c := range o.cols {
			out = append(out, o.alias+"."+c.Name)
		}
	}
	return out
}

// candidates are the tables the query may join: those the tables chosen
// refer to by a foreign key, or that refer to them; then any of the
// first table's schema, joined as chosen.
func (b *queryBuilder) candidates() []qbCandidate {
	var out []qbCandidate
	for _, t := range b.tables {
		for _, fk := range t.fks {
			schema := fk.RefSchema
			if schema == "" {
				schema = t.schema
			}
			c := qbCandidate{label: fmt.Sprintf("%s, which %s.%s refers to", fk.RefTable, t.alias, strings.Join(fk.Columns, ", ")), schema: schema, name: fk.RefTable}
			for i, col := range fk.Columns {
				if i < len(fk.RefColumns) {
					c.on = append(c.on, qbOn{column: fk.RefColumns[i], other: t.alias + "." + col})
				}
			}
			out = append(out, c)
		}
		for _, ref := range t.refs {
			c := qbCandidate{label: fmt.Sprintf("%s, whose %s refers to %s", ref.Table, strings.Join(ref.Columns, ", "), t.alias), schema: ref.Schema, name: ref.Table}
			for i, col := range ref.Columns {
				if i < len(ref.RefColumns) {
					c.on = append(c.on, qbOn{column: col, other: t.alias + "." + ref.RefColumns[i]})
				}
			}
			out = append(out, c)
		}
	}
	for _, o := range b.schemaTables {
		out = append(out, qbCandidate{label: o.Name + ", joined as chosen", schema: o.Schema, name: o.Name})
	}
	return out
}

// find is the table and the column a label, alias.column, names; by the
// labels the tables give, as an alias or a column may hold a dot.
func (b *queryBuilder) find(label string) (*qbTable, string, bool) {
	for _, t := range b.tables {
		for _, c := range t.cols {
			if t.alias+"."+c.Name == label {
				return t, c.Name, true
			}
		}
	}
	return nil, "", false
}

// expr writes a column, alias.column, as SQL.
func (b *queryBuilder) expr(label string) (string, bool) {
	t, column, ok := b.find(label)
	if !ok || b.conn.DB == nil {
		return "", false
	}
	d := b.conn.DB.Dialect
	return d.Quote(t.alias) + "." + d.Quote(column), true
}

// qbOutput is a column of the result as SQL, with the name it gets, ""
// when it keeps the column's.
type qbOutput struct {
	expr, name string
	aggregate  bool
}

// outputs are the result's columns as SQL, each named apart: an
// aggregate as agg_column, a name two of them share with the alias of
// its table before it, and a number after should that be taken too.
func (b *queryBuilder) outputs() ([]qbOutput, error) {
	out := make([]qbOutput, len(b.columns))
	names := make([]string, len(b.columns))
	count := map[string]int{}
	for i, col := range b.columns {
		_, column, _ := b.find(col.label)
		e, ok := b.expr(col.label)
		if !ok {
			return nil, fmt.Errorf("%s is no column of the tables chosen", col.label)
		}
		names[i] = column
		switch col.agg {
		case "":
		case "count distinct":
			e, names[i] = "COUNT(DISTINCT "+e+")", "distinct_"+column
		default:
			e, names[i] = strings.ToUpper(col.agg)+"("+e+")", col.agg+"_"+column
		}
		out[i] = qbOutput{expr: e, aggregate: col.agg != ""}
		count[names[i]]++
	}
	taken := map[string]bool{}
	for i, col := range b.columns {
		name := names[i]
		if count[name] > 1 {
			t, _, _ := b.find(col.label)
			name = t.alias + "_" + name
		}
		for n, base := 2, name; taken[name]; n++ {
			name = fmt.Sprint(base, "_", n)
		}
		taken[name] = true
		if name != names[i] || out[i].aggregate {
			out[i].name = name
		}
	}
	return out, nil
}

// sql writes the query as the choices make it.
func (b *queryBuilder) sql() (string, error) {
	if b.conn.DB == nil {
		return "", errors.New(b.conn.Config.Name + " is not connected")
	}
	d, e := b.conn.DB.Dialect, b.conn.Config.Engine
	if len(b.tables) == 0 {
		return "", errors.New("choose a table")
	}
	var sb strings.Builder
	sb.WriteString("SELECT ")
	if b.distinct {
		sb.WriteString("DISTINCT ")
	}
	var outs, groups []string
	aggregated := false
	cols, err := b.outputs()
	if err != nil {
		return "", err
	}
	for _, o := range cols {
		expr := o.expr
		if o.aggregate {
			aggregated = true
		} else {
			groups = append(groups, o.expr)
		}
		if o.name != "" {
			expr += " AS " + d.Quote(o.name)
		}
		outs = append(outs, expr)
	}
	if len(outs) == 0 {
		outs = []string{"*"}
	}
	sb.WriteString(strings.Join(outs, ",\n       "))
	for i, t := range b.tables {
		table := db.QualifiedName(d, t.schema, t.name) + " AS " + d.Quote(t.alias)
		if i == 0 {
			sb.WriteString("\nFROM " + table)
			continue
		}
		if len(t.on) == 0 {
			return "", fmt.Errorf("choose how %s joins the tables before it", t.alias)
		}
		var on []string
		for _, o := range t.on {
			mine, ok1 := b.expr(t.alias + "." + o.column)
			other, ok2 := b.expr(o.other)
			if !ok1 || !ok2 {
				return "", fmt.Errorf("choose the columns %s joins on", t.alias)
			}
			on = append(on, mine+" = "+other)
		}
		join := "\nJOIN "
		if t.left == 1 {
			join = "\nLEFT JOIN "
		}
		sb.WriteString(join + table + " ON " + strings.Join(on, " AND "))
	}
	if len(b.conds) > 0 {
		where, err := condsSQL(e, b.any == 1, b.conds, b.expr)
		if err != nil {
			return "", err
		}
		sb.WriteString("\nWHERE " + where)
	}
	if aggregated && len(groups) > 0 {
		sb.WriteString("\nGROUP BY " + strings.Join(groups, ", "))
	}
	if len(b.sorts) > 0 {
		var order []string
		for _, s := range b.sorts {
			expr, err := b.sortExpr(s.label)
			if err != nil {
				return "", err
			}
			if s.desc == 1 {
				expr += " DESC"
			}
			order = append(order, expr)
		}
		sb.WriteString("\nORDER BY " + strings.Join(order, ", "))
	}
	if limit := strings.TrimSpace(b.limit); limit != "" {
		n, err := strconv.Atoi(limit)
		if err != nil || n < 1 {
			return "", errors.New("the limit is a number of rows, 1 or more")
		}
		sb.WriteString("\nLIMIT " + strconv.Itoa(n))
	}
	return sb.String(), nil
}

// sortChoices are what the rows may be sorted by: the result's columns,
// one named by its name, else every column of the tables.
func (b *queryBuilder) sortChoices() []string {
	if len(b.columns) == 0 {
		return b.labels(nil)
	}
	cols, err := b.outputs()
	if err != nil {
		return nil
	}
	out := make([]string, len(b.columns))
	for i, col := range b.columns {
		out[i] = col.label
		if cols[i].name != "" {
			out[i] = cols[i].name
		}
	}
	return out
}

// sortExpr writes what the rows are sorted by: a column named in the
// result by its name, else as alias.column.
func (b *queryBuilder) sortExpr(choice string) (string, error) {
	if b.conn.DB == nil {
		return "", errors.New(b.conn.Config.Name + " is not connected")
	}
	cols, err := b.outputs()
	if err != nil {
		return "", err
	}
	for i, col := range b.columns {
		if cols[i].name != "" && cols[i].name == choice {
			return b.conn.DB.Dialect.Quote(cols[i].name), nil
		}
		if cols[i].name == "" && col.label == choice {
			return cols[i].expr, nil
		}
	}
	if expr, ok := b.expr(choice); ok && len(b.columns) == 0 {
		return expr, nil
	}
	return "", fmt.Errorf("the rows are sorted by %s, which the query does not have", choice)
}

// pruneSorts drops the orders by what the result no longer has, as a
// column taken out, or renamed by its aggregate.
func (b *queryBuilder) pruneSorts(choices []string) {
	if slices.ContainsFunc(b.tables, func(t *qbTable) bool { return t.loading }) {
		return
	}
	for i := len(b.sorts) - 1; i >= 0; i-- {
		if !slices.Contains(choices, b.sorts[i].label) {
			b.sorts = slices.Delete(b.sorts, i, i+1)
			b.sortKeys.remove(i)
		}
	}
}

// setChosen adds a table's column to the result, or takes it out.
func (b *queryBuilder) setChosen(label string, on bool) {
	i := slices.IndexFunc(b.columns, func(c qbColumn) bool { return c.label == label })
	switch {
	case on && i < 0:
		b.columns = append(b.columns, qbColumn{label: label})
		b.colKeys.add()
	case !on && i >= 0:
		b.columns = slices.Delete(b.columns, i, i+1)
		b.colKeys.remove(i)
	}
}

// addCond adds a condition on the rows.
func (b *queryBuilder) addCond(c builderCond) {
	b.conds = append(b.conds, c)
	b.condKeys.add()
}

// addSort sorts the rows by one more column.
func (b *queryBuilder) addSort(s qbSort) {
	b.sorts = append(b.sorts, s)
	b.sortKeys.add()
}

// removeTable takes a table out, and what refers to it: the columns,
// conditions and joins of its alias, and the tables joined only to it.
func (b *queryBuilder) removeTable(t *qbTable) {
	i := slices.Index(b.tables, t)
	if i <= 0 {
		return
	}
	// Found by their labels while t is still among the tables.
	mine := func(label string) bool {
		owner, _, ok := b.find(label)
		return ok && owner == t
	}
	for j := len(b.columns) - 1; j >= 0; j-- {
		if mine(b.columns[j].label) {
			b.columns = slices.Delete(b.columns, j, j+1)
			b.colKeys.remove(j)
		}
	}
	for j := len(b.conds) - 1; j >= 0; j-- {
		if mine(b.conds[j].column) {
			b.conds = slices.Delete(b.conds, j, j+1)
			b.condKeys.remove(j)
		}
	}
	for j := len(b.sorts) - 1; j >= 0; j-- {
		if mine(b.sorts[j].label) {
			b.sorts = slices.Delete(b.sorts, j, j+1)
			b.sortKeys.remove(j)
		}
	}
	later := slices.Clone(b.tables[i+1:])
	for _, o := range later {
		o.on = slices.DeleteFunc(o.on, func(on qbOn) bool { return mine(on.other) })
	}
	b.tables = slices.Delete(b.tables, i, i+1)
	for _, o := range later {
		if len(o.on) == 0 {
			b.removeTable(o)
		}
	}
}

func queryBuilderView(a Host, c *ui.Context) {
	b := a.Dialogs().builder
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	_, winH := c.Size()
	sql, err := b.sql()
	section := func(title string) {
		ui.Text(c, title).FontSize(12).Bold().TextColor(pal.Muted).Padding(6, 0, 0, 0)
	}
	ui.Modal(c, &b.open, func() {
		// A height of its own, for its sections to scroll in.
		ui.Column(c).Width(1000).Height(min(winH-60, 860)).Gap(8).Children(func() {
			ui.Text(c, "Build a Query · "+b.conn.Config.Name).FontSize(15).Bold()
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Column(c).Gap(6).Padding(0, 0, 8, 0).Children(func() {
					section("Tables")
					b.tablesView(a, c)
					section("Columns")
					b.columnsView(c)
					section("Rows where")
					b.condsView(c)
					section("Order and limit")
					b.orderView(c)
				})
			})
			ui.Scroll(c).MaxHeight(180).Radius(6).Background(pal.EditorBg).Border(1, th.Border).Children(func() {
				if err != nil {
					ui.Text(c, widgets.Capitalize(err.Error())+".").FontSize(12.5).TextColor(pal.Muted).Padding(8, 10)
					return
				}
				ui.Text(c, sql).Font(widgets.MonoFont).FontSize(12).Padding(8, 10).Selectable()
			})
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					b.open = false
				}
				if ui.Button(c, "Copy SQL").Disabled(err != nil).Clicked() {
					a.WriteClipboard(sql)
				}
				if widgets.Activated(c, ui.PrimaryButton(c, "Open in Editor").Disabled(err != nil)) {
					a.NewQueryTab(b.conn, b.database, sql+";\n")
					b.open = false
				}
			})
		})
	})
	if !b.open {
		a.Dialogs().builder = nil
	}
}

func (b *queryBuilder) tablesView(a Host, c *ui.Context) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	var drop *qbTable
	for i, t := range b.tables {
		ui.Column(c.Key(fmt.Sprintf("qb-table-%p", t))).Gap(4).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, t.alias).Font(widgets.MonoFont).Bold()
				ui.Text(c, t.schema+"."+t.name).FontSize(12).TextColor(pal.Muted)
				if t.loading {
					ui.Spinner(c).Size(12, 12)
				}
				if t.err != "" {
					ui.Text(c, t.err).FontSize(12).TextColor(th.Danger).Shrink(1)
				}
				if i == 0 {
					return
				}
				ui.Segmented(c, &t.left, "Join", "Left Join").Label("Join of " + t.alias)
				ui.Spacer(c)
				if widgets.IconButton(c, widgets.IconX, "Remove "+t.alias).Clicked() {
					drop = t
				}
			})
			if i == 0 {
				return
			}
			names := make([]string, len(t.cols))
			for k, col := range t.cols {
				names[k] = col.Name
			}
			others := b.labels(t)
			remove := -1
			for k := range t.on {
				on := &t.on[k]
				ui.Row(c.Key(fmt.Sprint("on-", k))).Gap(6).AlignItems(ui.Center).PaddingX(24).Children(func() {
					if k == 0 {
						ui.Text(c, "on").FontSize(12).TextColor(pal.Muted).Width(28)
					} else {
						ui.Text(c, "and").FontSize(12).TextColor(pal.Muted).Width(28)
					}
					ui.Text(c, t.alias+".").Font(widgets.MonoFont).FontSize(12)
					ui.Select(c, &on.column, names).Width(180).Label("Column of " + t.alias)
					ui.Text(c, "=").TextColor(pal.Muted)
					ui.Select(c, &on.other, others).Width(240).Label("Column it equals")
					if widgets.IconButton(c, widgets.IconX, "Remove the condition").Disabled(len(t.on) == 1).Clicked() {
						remove = k
					}
				})
			}
			if remove >= 0 {
				t.on = slices.Delete(t.on, remove, remove+1)
			}
			if len(names) > 0 && len(others) > 0 && ui.Link(c, "and on another column", "").FontSize(12).Clicked() {
				t.on = append(t.on, qbOn{column: names[0], other: others[0]})
			}
		})
	}
	if drop != nil {
		b.removeTable(drop)
	}
	cands := b.candidates()
	labels := make([]string, len(cands))
	for i, cand := range cands {
		labels[i] = cand.label
	}
	if len(labels) == 0 {
		return
	}
	if !slices.Contains(labels, b.add) {
		b.add = labels[0]
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Select(c, &b.add, labels).Width(520).Label("Table to join").AutoFocus()
		if ui.Button(c, "Join").Clicked() {
			cand := cands[slices.Index(labels, b.add)]
			b.addTable(a, cand.schema, cand.name, cand.on)
		}
	})
}

func (b *queryBuilder) columnsView(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	for _, t := range b.tables {
		ui.Row(c.Key(fmt.Sprintf("qb-cols-%p", t))).Gap(10).Wrap().AlignItems(ui.Center).Children(func() {
			ui.Text(c, t.alias).Font(widgets.MonoFont).FontSize(12).TextColor(pal.Muted).Width(120)
			for k, col := range t.cols {
				label := t.alias + "." + col.Name
				if ui.Checkbox(c, &t.chosen[k], col.Name).Changed() {
					b.setChosen(label, t.chosen[k])
				}
			}
		})
	}
	if len(b.columns) == 0 {
		ui.Text(c, "None chosen: every column, *.").FontSize(12).TextColor(pal.Muted)
		return
	}
	remove := -1
	for i := range b.columns {
		col := &b.columns[i]
		ui.Row(c.Key(b.colKeys.key("qb-col-", i))).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, col.label).Font(widgets.MonoFont).FontSize(12.5).Width(260).SingleLine()
			agg := cmp.Or(col.agg, noAggregate)
			if ui.Select(c, &agg, aggregates).Width(160).Label("Aggregate of " + col.label).Changed() {
				col.agg = agg
				if agg == noAggregate {
					col.agg = ""
				}
			}
			if widgets.IconButton(c, widgets.IconX, "Remove "+col.label).Clicked() {
				remove = i
			}
		})
	}
	if remove >= 0 {
		label := b.columns[remove].label
		b.setChosen(label, false)
		for _, t := range b.tables {
			for k, col := range t.cols {
				if t.alias+"."+col.Name == label {
					t.chosen[k] = false
				}
			}
		}
	}
	if slices.ContainsFunc(b.columns, func(c qbColumn) bool { return c.agg != "" }) {
		ui.Text(c, "With an aggregate, the rows are grouped by the other columns.").FontSize(12).TextColor(pal.Muted)
	}
}

func (b *queryBuilder) condsView(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	columns := b.labels(nil)
	ops := builderOpLabels()
	remove := -1
	for i := range b.conds {
		bc := &b.conds[i]
		op := builderOps[slices.IndexFunc(builderOps, func(o builderOp) bool { return o.label == bc.op })]
		ui.Row(c.Key(b.condKeys.key("qb-cond-", i))).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Select(c, &bc.column, columns).Width(260).Label("Column")
			ui.Select(c, &bc.op, ops).Width(150).Label("Operator")
			if op.values > 0 {
				placeholder := "value"
				if op.op == "in" {
					placeholder = "a, b, c"
				}
				ui.TextInput(c, &bc.value).Placeholder(placeholder).Font(widgets.MonoFont).Width(200).Label("Value")
			}
			if op.values == 2 {
				ui.Text(c, "and").FontSize(12).TextColor(pal.Muted)
				ui.TextInput(c, &bc.to).Placeholder("value").Font(widgets.MonoFont).Width(200).Label("Second value")
			}
			if widgets.IconButton(c, widgets.IconX, "Remove the condition").Clicked() {
				remove = i
			}
		})
	}
	if remove >= 0 {
		b.conds = slices.Delete(b.conds, remove, remove+1)
		b.condKeys.remove(remove)
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		if len(columns) > 0 && widgets.ToolButton(c, widgets.IconPlus, "Condition", "Add a condition").Clicked() {
			b.addCond(builderCond{column: columns[0], op: builderOps[0].label})
		}
		if len(b.conds) > 1 {
			ui.Segmented(c, &b.any, "All", "Any").Label("Rows meeting")
		}
	})
}

func (b *queryBuilder) orderView(c *ui.Context) {
	choices := b.sortChoices()
	b.pruneSorts(choices)
	remove := -1
	for i := range b.sorts {
		s := &b.sorts[i]
		ui.Row(c.Key(b.sortKeys.key("qb-sort-", i))).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Select(c, &s.label, choices).Width(260).Label("Sorted by")
			ui.Segmented(c, &s.desc, "Ascending", "Descending").Label("Direction")
			if widgets.IconButton(c, widgets.IconX, "Remove the order").Clicked() {
				remove = i
			}
		})
	}
	if remove >= 0 {
		b.sorts = slices.Delete(b.sorts, remove, remove+1)
		b.sortKeys.remove(remove)
	}
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		if len(choices) > 0 && widgets.ToolButton(c, widgets.IconPlus, "Order", "Sort the rows by a column").Clicked() {
			b.addSort(qbSort{label: choices[0]})
		}
		ui.Text(c, "Limit").FontSize(12).TextColor(widgets.PaletteOf(c).Muted)
		ui.TextInput(c, &b.limit).Width(90).Placeholder("none").Label("Limit")
		ui.Checkbox(c, &b.distinct, "Distinct rows")
	})
}
