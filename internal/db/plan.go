package db

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"dgopher/internal/lists"
)

// Plan is how a server runs a query, as its EXPLAIN tells.
type Plan struct {
	Root *PlanNode
	// Analyzed is set when the query ran and the steps carry what it
	// took, as with EXPLAIN ANALYZE.
	Analyzed bool
}

// PlanNode is a step of a plan, with the steps whose rows it takes.
type PlanNode struct {
	Op     string // what it does, as Seq Scan or Hash Join
	Target string // the table, or index, it reads
	Detail string // its condition, key or other description
	// Cost and Rows are the planner's estimates, ActualRows, Time (in
	// milliseconds, its steps' included) and Loops what running it took;
	// -1 where the engine tells nothing.
	Cost, Rows, ActualRows, Time, Loops float64
	// Props is everything the engine tells of the step.
	Props    [][2]string
	Children []*PlanNode
}

func newNode() *PlanNode { return &PlanNode{Cost: -1, Rows: -1, ActualRows: -1, Time: -1, Loops: -1} }

// childNodes builds a node of each object in the list under key of m, in order.
func childNodes(m map[string]any, key string, build func(map[string]any) *PlanNode) []*PlanNode {
	var out []*PlanNode
	kids, _ := m[key].([]any)
	for _, k := range kids {
		if km, ok := k.(map[string]any); ok {
			out = append(out, build(km))
		}
	}
	return out
}

// Walk calls fn for the node and every node below it, depth first.
func (n *PlanNode) Walk(fn func(*PlanNode, int)) { n.walk(fn, 0) }

func (n *PlanNode) walk(fn func(*PlanNode, int), depth int) {
	fn(n, depth)
	for _, c := range n.Children {
		c.walk(fn, depth+1)
	}
}

// Prop is the value the engine gives a property of the step, "" when it
// gives none.
func (n *PlanNode) Prop(name string) string {
	for _, p := range n.Props {
		if p[0] == name {
			return p[1]
		}
	}
	return ""
}

// explainWithPrefix is the ExplainRun of a statement after an EXPLAIN prefix.
func explainWithPrefix(prefix, statement string) ExplainRun {
	return ExplainRun{Statement: prefix + statement, Offset: len(prefix)}
}

// planText is the last column of an EXPLAIN's rows, a line each.
func planText(rows [][]any) string {
	var lines []string
	for _, r := range rows {
		if len(r) > 0 {
			lines = append(lines, Display(r[len(r)-1]))
		}
	}
	return strings.Join(lines, "\n")
}

// jsonNumber is a JSON value as a number, -1 when it is none.
func jsonNumber(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			return f
		}
	}
	return -1
}

// props lists the scalar properties of a JSON object, sorted, leaving
// out the keys of its children.
func props(m map[string]any, children ...string) [][2]string {
	var out [][2]string
	for k, v := range m {
		if slices.Contains(children, k) {
			continue
		}
		switch x := v.(type) {
		case map[string]any, []any:
			if b, err := json.Marshal(x); err == nil && len(b) < 2000 {
				out = append(out, [2]string{k, string(b)})
			}
		case nil:
		default:
			out = append(out, [2]string{k, fmt.Sprint(x)})
		}
	}
	slices.SortFunc(out, func(a, b [2]string) int { return strings.Compare(a[0], b[0]) })
	return out
}

func str(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func parsePostgresPlan(text string) (*Plan, bool) {
	var doc []map[string]any
	if err := json.Unmarshal([]byte(text), &doc); err != nil || len(doc) == 0 {
		return nil, false
	}
	root, ok := doc[0]["Plan"].(map[string]any)
	if !ok {
		return nil, false
	}
	p := &Plan{}
	var build func(m map[string]any) *PlanNode
	build = func(m map[string]any) *PlanNode {
		n := newNode()
		n.Op = str(m, "Node Type")
		if j := str(m, "Join Type"); j != "" && j != "Inner" {
			n.Op += " (" + j + ")"
		}
		n.Target = str(m, "Relation Name")
		if idx := str(m, "Index Name"); idx != "" {
			n.Target = strings.TrimSpace(n.Target + " using " + idx)
		}
		for _, key := range []string{"Index Cond", "Hash Cond", "Merge Cond", "Join Filter", "Filter", "Recheck Cond"} {
			if v := str(m, key); v != "" {
				n.Detail = key + ": " + v
				break
			}
		}
		if n.Detail == "" {
			for _, key := range []string{"Sort Key", "Group Key"} {
				if keys, ok := m[key].([]any); ok {
					parts := lists.Map(keys, func(k any) string { return fmt.Sprint(k) })
					n.Detail = key + ": " + strings.Join(parts, ", ")
					break
				}
			}
		}
		n.Cost, n.Rows = jsonNumber(m["Total Cost"]), jsonNumber(m["Plan Rows"])
		if loops := jsonNumber(m["Actual Loops"]); loops >= 0 {
			p.Analyzed = true
			n.Loops = loops
			// Times and rows are those of one loop.
			n.Time = jsonNumber(m["Actual Total Time"]) * loops
			n.ActualRows = jsonNumber(m["Actual Rows"]) * loops
		}
		n.Props = props(m, "Plans")
		n.Children = childNodes(m, "Plans", build)
		return n
	}
	p.Root = build(root)
	return p, true
}

var (
	mysqlCost   = regexp.MustCompile(`\(cost=([\d.e+]+)(?:\.\.[\d.e+]+)? rows=([\d.e+]+)\)`)
	mysqlActual = regexp.MustCompile(`\(actual time=([\d.e+]+)\.\.([\d.e+]+) rows=([\d.e+]+) loops=([\d.e+]+)\)`)
	mysqlTarget = regexp.MustCompile(`\bon (\S+)(?: using (\S+))?`)
	// sqliteTarget is the table a SQLite step reads.
	sqliteTarget = regexp.MustCompile(`^(?:SCAN|SEARCH) (\S+)`)
)

// parseMySQLTree reads EXPLAIN FORMAT=TREE, or EXPLAIN ANALYZE: a step a
// line, "-> what (cost=… rows=…) (actual time=…)", four spaces deeper
// than the step it feeds.
func parseMySQLTree(text string) (*Plan, bool) {
	if !strings.HasPrefix(strings.TrimSpace(text), "->") {
		return nil, false
	}
	p := &Plan{}
	var stack []*PlanNode // by depth
	var depths []int
	for line := range strings.Lines(text) {
		trimmed := strings.TrimLeft(line, " ")
		if !strings.HasPrefix(trimmed, "-> ") {
			continue
		}
		depth := len(line) - len(trimmed)
		body := strings.TrimSpace(strings.TrimPrefix(trimmed, "-> "))
		n := newNode()
		if m := mysqlActual.FindStringSubmatch(body); m != nil {
			p.Analyzed = true
			last, _ := strconv.ParseFloat(m[2], 64)
			rows, _ := strconv.ParseFloat(m[3], 64)
			loops, _ := strconv.ParseFloat(m[4], 64)
			n.Time, n.ActualRows, n.Loops = last*loops, rows*loops, loops
			body = strings.TrimSpace(strings.Replace(body, m[0], "", 1))
		}
		if m := mysqlCost.FindStringSubmatch(body); m != nil {
			n.Cost, _ = strconv.ParseFloat(m[1], 64)
			n.Rows, _ = strconv.ParseFloat(m[2], 64)
			body = strings.TrimSpace(strings.Replace(body, m[0], "", 1))
		}
		n.Op, n.Detail, _ = strings.Cut(body, ": ")
		if m := mysqlTarget.FindStringSubmatch(body); m != nil {
			n.Target = strings.Trim(m[1], "`")
			if m[2] != "" {
				n.Target += " using " + strings.Trim(m[2], "`")
			}
		}
		n.Props = [][2]string{{"Step", body}}
		for len(depths) > 0 && depths[len(depths)-1] >= depth {
			stack, depths = stack[:len(stack)-1], depths[:len(depths)-1]
		}
		if len(stack) == 0 {
			if p.Root != nil {
				return nil, false // two roots: not a plan's tree
			}
			p.Root = n
		} else {
			parent := stack[len(stack)-1]
			parent.Children = append(parent.Children, n)
		}
		stack, depths = append(stack, n), append(depths, depth)
	}
	return p, p.Root != nil
}

// parseSQLitePlan reads EXPLAIN QUERY PLAN: rows of id, parent, notused
// and detail.
func parseSQLitePlan(cols []string, rows [][]any) (*Plan, bool) {
	if len(cols) != 4 || cols[0] != "id" || cols[1] != "parent" || cols[3] != "detail" {
		return nil, false
	}
	root := newNode()
	root.Op = "QUERY PLAN"
	byID := map[int64]*PlanNode{0: root}
	for _, r := range rows {
		id, _ := strconv.ParseInt(Display(r[0]), 10, 64)
		parent, _ := strconv.ParseInt(Display(r[1]), 10, 64)
		n := newNode()
		n.Op = Display(r[3])
		if m := sqliteTarget.FindStringSubmatch(n.Op); m != nil {
			n.Target = m[1]
		}
		n.Props = [][2]string{{"detail", n.Op}}
		byID[id] = n
		if p, ok := byID[parent]; ok {
			p.Children = append(p.Children, n)
		} else {
			root.Children = append(root.Children, n)
		}
	}
	return &Plan{Root: root}, true
}

// parseDuckDBPlan reads EXPLAIN (FORMAT JSON), a list of operators, or
// EXPLAIN (ANALYZE, FORMAT JSON), the query's profile around them.
func parseDuckDBPlan(text string) (*Plan, bool) {
	text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "physical_plan"))
	text = strings.TrimSpace(strings.TrimPrefix(text, "analyzed_plan"))
	var doc any
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return nil, false
	}
	p := &Plan{}
	var build func(m map[string]any) *PlanNode
	build = func(m map[string]any) *PlanNode {
		n := newNode()
		n.Op = str(m, "name")
		if n.Op == "" {
			n.Op = str(m, "operator_name")
		}
		if n.Op == "" {
			n.Op = str(m, "operator_type")
		}
		if extra, ok := m["extra_info"].(map[string]any); ok {
			n.Target = str(extra, "Table")
			for _, key := range []string{"Filters", "Conditions", "Groups", "Aggregates", "Projections"} {
				if v := extra[key]; v != nil {
					n.Detail = key + ": " + strings.Join(strings.Fields(fmt.Sprint(v)), " ")
					break
				}
			}
			n.Rows = jsonNumber(strings.TrimPrefix(str(extra, "Estimated Cardinality"), "~"))
		}
		if t := jsonNumber(m["operator_timing"]); t >= 0 {
			p.Analyzed = true
			n.Time = t * 1000
			n.ActualRows = jsonNumber(m["operator_cardinality"])
		}
		n.Props = props(m, "children")
		n.Children = childNodes(m, "children", build)
		return n
	}
	switch doc := doc.(type) {
	case []any:
		if len(doc) == 0 {
			return nil, false
		}
		m, ok := doc[0].(map[string]any)
		if !ok {
			return nil, false
		}
		p.Root = build(m)
	case map[string]any:
		p.Root = build(doc)
		if p.Root.Op == "" {
			p.Root.Op = "QUERY"
		}
		if latency := jsonNumber(doc["latency"]); latency >= 0 {
			p.Analyzed, p.Root.Time = true, latency*1000
		}
	default:
		return nil, false
	}
	// DuckDB times each operator alone: a step's time is its own and its
	// steps'. The query's latency, at the root, holds them all already.
	if p.Analyzed {
		var inclusive func(n *PlanNode) float64
		inclusive = func(n *PlanNode) float64 {
			sum := 0.0
			if n != p.Root {
				sum = max(n.Time, 0)
			}
			for _, c := range n.Children {
				sum += inclusive(c)
			}
			if n == p.Root {
				n.Time = max(n.Time, sum)
			} else {
				n.Time = sum
			}
			return n.Time
		}
		inclusive(p.Root)
	}
	return p, true
}

// parseClickHousePlan reads EXPLAIN json = 1: a list holding the plan,
// whose steps are Node Type and Description, with their Plans.
func parseClickHousePlan(text string) (*Plan, bool) {
	var doc []map[string]any
	if err := json.Unmarshal([]byte(text), &doc); err != nil || len(doc) == 0 {
		return nil, false
	}
	root, ok := doc[0]["Plan"].(map[string]any)
	if !ok {
		return nil, false
	}
	var build func(m map[string]any) *PlanNode
	build = func(m map[string]any) *PlanNode {
		n := newNode()
		n.Op = str(m, "Node Type")
		n.Detail = str(m, "Description")
		if strings.HasPrefix(n.Op, "ReadFrom") {
			n.Target = n.Detail
		}
		n.Props = props(m, "Plans")
		n.Children = childNodes(m, "Plans", build)
		return n
	}
	return &Plan{Root: build(root)}, true
}
