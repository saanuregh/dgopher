package db

import (
	"cmp"
	"encoding/xml"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"dgopher/internal/sqltext"
)

// sqlserverPlanColumn is the column of the results SHOWPLAN_XML and
// STATISTICS XML return a plan in.
const sqlserverPlanColumn = "Microsoft SQL Server 2005 XML Showplan"

// showplanNode is an element of a SHOWPLAN XML document, read whole.
type showplanNode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr     `xml:",any,attr"`
	Children []showplanNode `xml:",any"`
}

func (n *showplanNode) attr(name string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (n *showplanNode) number(name string) float64 {
	if f, err := strconv.ParseFloat(n.attr(name), 64); err == nil {
		return f
	}
	return -1
}

// child is n's first child element named name, nil for none.
func (n *showplanNode) child(name string) *showplanNode {
	for i := range n.Children {
		if n.Children[i].XMLName.Local == name {
			return &n.Children[i]
		}
	}
	return nil
}

// within is the first element named name below n, outside the steps that
// feed n, nil for none.
func (n *showplanNode) within(name string) *showplanNode {
	for i := range n.Children {
		c := &n.Children[i]
		if c.XMLName.Local == name {
			return c
		}
		if c.XMLName.Local == "RelOp" {
			continue
		}
		if found := c.within(name); found != nil {
			return found
		}
	}
	return nil
}

// steps are the RelOps that feed n, the first found below it on each path.
func (n *showplanNode) steps() []*showplanNode {
	var out []*showplanNode
	for i := range n.Children {
		if c := &n.Children[i]; c.XMLName.Local == "RelOp" {
			out = append(out, c)
		} else {
			out = append(out, c.steps()...)
		}
	}
	return out
}

// statements are the statements of a document that have a plan, in
// order, those inside IF and WHILE included.
func (n *showplanNode) statements() []*showplanNode {
	var out []*showplanNode
	for i := range n.Children {
		c := &n.Children[i]
		switch {
		case c.XMLName.Local == "QueryPlan":
		case strings.HasPrefix(c.XMLName.Local, "Stmt") && c.child("QueryPlan") != nil:
			out = append(out, c)
		default:
			out = append(out, c.statements()...)
		}
	}
	return out
}

// ParsePlan reads the results of SET SHOWPLAN_XML, an XML document for
// the batch, or SET STATISTICS XML, one for each statement, in a column
// of that name: a root for each statement with a plan, under one for the
// batch when there are several.
func (sqlserverEngine) ParsePlan(cols []string, rows [][]any) (*Plan, bool) {
	if len(cols) != 1 || cols[0] != sqlserverPlanColumn || len(rows) == 0 {
		return nil, false
	}
	p := &Plan{}
	var roots []*PlanNode
	for _, r := range rows {
		var doc showplanNode
		if len(r) != 1 || xml.Unmarshal([]byte(Display(r[0])), &doc) != nil || doc.XMLName.Local != "ShowPlanXML" {
			return nil, false
		}
		for _, stmt := range doc.statements() {
			if n := sqlserverStatement(p, stmt); n != nil {
				roots = append(roots, n)
			}
		}
	}
	switch len(roots) {
	case 0:
		return nil, false
	case 1:
		p.Root = roots[0].Children[0]
		return p, true
	}
	batch := newNode()
	batch.Op, batch.Children = "Batch", roots
	batch.Cost, batch.Time = 0, 0
	for _, r := range roots {
		batch.Cost += r.Cost
		batch.Time += max(r.Time, 0)
	}
	if !p.Analyzed {
		batch.Time = -1
	}
	p.Root = batch
	return p, true
}

// sqlserverStatement is the node of a statement, its plan's first step
// below it; nil for a statement without one.
func sqlserverStatement(p *Plan, stmt *showplanNode) *PlanNode {
	plan := stmt.child("QueryPlan")
	top := plan.child("RelOp")
	if top == nil {
		return nil
	}
	root := sqlserverStep(p, top)
	if w := plan.child("Warnings"); w != nil {
		root.Props = append(root.Props, sqlserverWarnings(w)...)
	}
	if missing := plan.child("MissingIndexes"); missing != nil {
		root.Props = append(root.Props, sqlserverMissingIndexes(missing)...)
	}
	n := newNode()
	n.Op = stmt.attr("StatementType")
	n.Detail = strings.Join(strings.Fields(stmt.attr("StatementText")), " ")
	n.Cost, n.Rows = stmt.number("StatementSubTreeCost"), stmt.number("StatementEstRows")
	if stats := plan.child("QueryTimeStats"); stats != nil {
		p.Analyzed = true
		n.Time = stats.number("ElapsedTime")
		if root.Time < 0 {
			root.Time = n.Time
		}
	}
	n.Children = []*PlanNode{root}
	return n
}

// sqlserverStep reads a RelOp, a step of the plan, and the steps below it.
func sqlserverStep(p *Plan, op *showplanNode) *PlanNode {
	n := newNode()
	physical, logical := op.attr("PhysicalOp"), op.attr("LogicalOp")
	n.Op = physical
	if scan := op.within("IndexScan"); scan != nil && scan.attr("Lookup") == "1" {
		// Shown by SQL Server's tools as a Key Lookup.
		n.Op = "Key Lookup"
		if physical == "Clustered Index Seek" {
			n.Op += " (Clustered)"
		}
	} else if logical != "" && logical != physical {
		n.Op += " (" + logical + ")"
	}
	if obj := op.within("Object"); obj != nil && obj.attr("Table") != "" {
		n.Target = sqlserverName(obj.attr("Schema"), obj.attr("Table"))
		if index := obj.attr("Index"); index != "" {
			n.Target += " using " + sqltext.DecodeQuoted(index, sqltext.TSQL)
		}
	}
	for _, a := range op.Attrs {
		n.Props = append(n.Props, [2]string{a.Name.Local, a.Value})
	}
	slices.SortFunc(n.Props, func(a, b [2]string) int { return strings.Compare(a[0], b[0]) })
	add := func(key, value string) {
		if value != "" {
			n.Props = append(n.Props, [2]string{key, value})
		}
	}
	seek := sqlserverSeekKeys(op.within("SeekPredicates"))
	predicate := ""
	if pred := op.within("Predicate"); pred != nil {
		predicate = sqlserverScalar(pred)
	}
	add("Seek Keys", seek)
	add("Predicate", predicate)
	if out := op.child("OutputList"); out != nil {
		add("Output", sqlserverColumns(out))
	}
	n.Detail = sqlserverDetail(op, seek, predicate)
	executions := 1 + max(op.number("EstimateRebinds"), 0) + max(op.number("EstimateRewinds"), 0)
	if rows := op.number("EstimateRows"); rows >= 0 {
		// Rows of one run of the step: those of all its runs compare with
		// what it read when it ran.
		n.Rows = rows * executions
	}
	n.Cost = op.number("EstimatedTotalSubtreeCost")
	if rt := op.child("RunTimeInformation"); rt != nil {
		p.Analyzed = true
		sqlserverRunTime(n, rt, add)
	}
	if w := op.child("Warnings"); w != nil {
		n.Props = append(n.Props, sqlserverWarnings(w)...)
	}
	for _, s := range op.steps() {
		n.Children = append(n.Children, sqlserverStep(p, s))
	}
	return n
}

// sqlserverRunTime reads what each thread of a step did as it ran: rows
// and runs are those of all threads, the time the longest's, in
// milliseconds, its steps' included.
func sqlserverRunTime(n *PlanNode, rt *showplanNode, add func(key, value string)) {
	var rows, runs, reads, read float64
	hasReads, hasRead := false, false
	for i := range rt.Children {
		c := &rt.Children[i]
		if c.XMLName.Local != "RunTimeCountersPerThread" {
			continue
		}
		rows += max(c.number("ActualRows"), 0)
		runs += max(c.number("ActualExecutions"), 0)
		if ms := c.number("ActualElapsedms"); ms > n.Time {
			n.Time = ms
		}
		if v := c.number("ActualLogicalReads"); v >= 0 {
			reads, hasReads = reads+v, true
		}
		if v := c.number("ActualRowsRead"); v >= 0 {
			read, hasRead = read+v, true
		}
	}
	n.ActualRows, n.Loops = rows, runs
	if hasReads {
		add("Actual Logical Reads", count(reads))
	}
	if hasRead {
		add("Actual Rows Read", count(read))
	}
}

// sqlserverDetail is what a step does: its seek keys, else its predicate,
// else what it joins, sorts or groups on.
func sqlserverDetail(op *showplanNode, seek, predicate string) string {
	switch {
	case seek != "":
		return "Seek Keys: " + seek
	case predicate != "":
		return "Predicate: " + predicate
	}
	for _, residual := range []string{"ProbeResidual", "Residual"} {
		if r := op.within(residual); r != nil {
			return "Residual: " + sqlserverScalar(r)
		}
	}
	if build := op.within("HashKeysBuild"); build != nil {
		keys := sqlserverColumns(build)
		if probe := op.within("HashKeysProbe"); probe != nil {
			keys += " = " + sqlserverColumns(probe)
		}
		return "Hash Keys: " + keys
	}
	if order := op.within("OrderBy"); order != nil {
		var cols []string
		for i := range order.Children {
			c := &order.Children[i]
			if ref := c.child("ColumnReference"); ref != nil {
				dir := " ASC"
				if c.attr("Ascending") == "0" {
					dir = " DESC"
				}
				cols = append(cols, sqlserverColumn(ref)+dir)
			}
		}
		return "Order By: " + strings.Join(cols, ", ")
	}
	for _, key := range []string{"GroupBy", "OuterReferences"} {
		if cols := op.within(key); cols != nil {
			label := map[string]string{"GroupBy": "Group By", "OuterReferences": "Outer References"}[key]
			return label + ": " + sqlserverColumns(cols)
		}
	}
	return ""
}

// sqlserverSeekKeys is what a seek looks for, as "[id] = 5"; alternatives
// joined by OR.
func sqlserverSeekKeys(preds *showplanNode) string {
	if preds == nil {
		return ""
	}
	ops := map[string]string{"EQ": "=", "GT": ">", "GE": ">=", "LT": "<", "LE": "<=", "IS": "IS", "IS NOT": "IS NOT", "ISNOT": "IS NOT"}
	var alternatives []string
	for i := range preds.Children {
		keys := preds.Children[i].within("SeekKeys")
		if keys == nil {
			continue
		}
		var parts []string
		for j := range keys.Children {
			k := &keys.Children[j]
			cols, exprs := k.child("RangeColumns"), k.child("RangeExpressions")
			if cols == nil || exprs == nil {
				continue
			}
			op := cmp.Or(ops[k.attr("ScanType")], k.attr("ScanType"))
			for c := 0; c < len(cols.Children) && c < len(exprs.Children); c++ {
				parts = append(parts, sqlserverColumn(&cols.Children[c])+" "+op+" "+sqlserverScalar(&exprs.Children[c]))
			}
		}
		if len(parts) > 0 {
			alternatives = append(alternatives, strings.Join(parts, " AND "))
		}
	}
	return strings.Join(alternatives, " OR ")
}

// sqlserverWarnings reads the warnings of a step, or of a statement's
// plan, as properties the advice reads.
func sqlserverWarnings(w *showplanNode) [][2]string {
	var out [][2]string
	if v := w.attr("NoJoinPredicate"); v == "1" || v == "true" {
		out = append(out, [2]string{"No join predicate", "true"})
	}
	for i := range w.Children {
		c := &w.Children[i]
		switch c.XMLName.Local {
		case "SpillToTempDb":
			out = append(out, [2]string{"Spill to tempdb", "level " + c.attr("SpillLevel")})
		case "PlanAffectingConvert":
			out = append(out, [2]string{"Plan affecting convert", c.attr("ConvertIssue") + ": " + sqlserverExpression(c.attr("Expression"))})
		default:
			out = append(out, [2]string{"Warning", c.XMLName.Local})
		}
	}
	return out
}

// sqlserverMissingIndexes reads the indexes SQL Server found missing as
// it planned, each with the share of the query's cost it estimates the
// index saves.
func sqlserverMissingIndexes(m *showplanNode) [][2]string {
	var out [][2]string
	for i := range m.Children {
		group := &m.Children[i]
		for j := range group.Children {
			idx := &group.Children[j]
			if idx.XMLName.Local != "MissingIndex" {
				continue
			}
			var keys, include []string
			for k := range idx.Children {
				g := &idx.Children[k]
				for c := range g.Children {
					name := sqltext.DecodeQuoted(g.Children[c].attr("Name"), sqltext.TSQL)
					if g.attr("Usage") == "INCLUDE" {
						include = append(include, name)
					} else {
						keys = append(keys, name)
					}
				}
			}
			text := sqlserverName(idx.attr("Schema"), idx.attr("Table")) + " (" + strings.Join(keys, ", ") + ")"
			if len(include) > 0 {
				text += " INCLUDE (" + strings.Join(include, ", ") + ")"
			}
			out = append(out, [2]string{"Missing index", text}, [2]string{"Missing index impact", group.attr("Impact")})
		}
	}
	return out
}

// sqlserverColumns are the columns an element lists, as "[a], [b]".
func sqlserverColumns(n *showplanNode) string {
	var cols []string
	for i := range n.Children {
		if c := &n.Children[i]; c.XMLName.Local == "ColumnReference" {
			cols = append(cols, sqlserverColumn(c))
		}
	}
	return strings.Join(cols, ", ")
}

// sqlserverColumn is a column as a plan's expressions name it: by the
// alias of its table when the query gave one.
func sqlserverColumn(ref *showplanNode) string {
	col := "[" + ref.attr("Column") + "]"
	if alias := ref.attr("Alias"); alias != "" {
		return alias + "." + col
	}
	return col
}

// sqlserverScalar is the text of the expression an element holds.
func sqlserverScalar(n *showplanNode) string {
	if n.XMLName.Local == "ScalarOperator" {
		return sqlserverExpression(n.attr("ScalarString"))
	}
	if s := n.within("ScalarOperator"); s != nil {
		return sqlserverExpression(s.attr("ScalarString"))
	}
	return ""
}

// qualifiedColumn is a column named with its database, schema and table,
// and the alias of its table when it has one.
var qualifiedColumn = regexp.MustCompile(`(?:\[[^\]]*\]\.)+\[[^\]]*\] as (\[[^\]]*\]\.\[[^\]]*\])|(?:\[[^\]]*\]\.)+(\[[^\]]*\])`)

// sqlserverExpression is an expression of a plan with its columns named
// as the query names them: [a] or, with its table's alias, [t].[a].
func sqlserverExpression(s string) string { return qualifiedColumn.ReplaceAllString(s, "$1$2") }

// sqlserverName is schema.table, without the brackets of either.
func sqlserverName(schema, table string) string {
	if schema == "" {
		return sqltext.DecodeQuoted(table, sqltext.TSQL)
	}
	return sqltext.DecodeQuoted(schema, sqltext.TSQL) + "." + sqltext.DecodeQuoted(table, sqltext.TSQL)
}

// sqlserverFilterColumns are the columns a predicate compares.
var sqlserverFilterColumns = regexp.MustCompile(`\[([^\]@]+)\]\s*(?:=|<>|!=|<=|>=|<|>|(?i:like|in|is|between)\b)`)

func (sqlserverEngine) AdvisePlanStep(n *PlanNode, add func(n *PlanNode, format string, args ...any)) {
	for i, p := range n.Props {
		switch p[0] {
		case "Missing index":
			impact := ""
			if i+1 < len(n.Props) && n.Props[i+1][0] == "Missing index impact" {
				impact = n.Props[i+1][1]
			}
			if f, err := strconv.ParseFloat(impact, 64); err == nil {
				add(n, "SQL Server suggests an index on %s, which it estimates makes the query %s%% cheaper.", p[1], strconv.FormatFloat(f, 'f', 1, 64))
			} else {
				add(n, "SQL Server suggests an index on %s.", p[1])
			}
		case "Spill to tempdb":
			add(n, "The %s spills to tempdb (%s): its memory grant was too small; fresher statistics, or an index in the order it needs, may avoid it.", n.Op, p[1])
		case "No join predicate":
			add(n, "The %s has no join predicate: each row of one input meets every row of the other. Check the query's join conditions.", n.Op)
		case "Plan affecting convert":
			issue, expr, _ := strings.Cut(p[1], ": ")
			add(n, "An implicit conversion, %s, affects the plan's %s: compare the column with a value of its own type.", expr, issue)
		}
	}
	table, _, _ := strings.Cut(n.Target, " using ")
	switch {
	case n.Op == "Table Scan" || n.Op == "Clustered Index Scan":
		read := jsonNumber(cmp.Or(n.Prop("Actual Rows Read"), n.Prop("EstimatedRowsRead")))
		predicate := n.Prop("Predicate")
		if predicate == "" || read < adviseRows || rowsOf(n)*10 > read {
			return
		}
		var cols []string
		for _, m := range sqlserverFilterColumns.FindAllStringSubmatch(predicate, -1) {
			if !strings.HasPrefix(m[1], "Expr") && !slices.Contains(cols, m[1]) {
				cols = append(cols, m[1])
			}
		}
		where := "the columns of its predicate"
		if len(cols) > 0 {
			where = "(" + strings.Join(cols, ", ") + ")"
		}
		add(n, "A %s reads %s rows of %s to keep %s of them: an index on %s may let it read only those.", n.Op, count(read), table, count(rowsOf(n)), where)
	case (strings.HasPrefix(n.Op, "Key Lookup") || n.Op == "RID Lookup") && rowsOf(n) >= adviseRows:
		name, _, _ := strings.Cut(n.Op, " (")
		cols := cmp.Or(n.Prop("Output"), "the columns it reads")
		add(n, "A %s reads %s rows of %s one at a time: an index that holds %s too, as INCLUDE columns, avoids it.", name, count(rowsOf(n)), table, cols)
	}
}
