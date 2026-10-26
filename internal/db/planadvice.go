package db

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Advice is what a plan suggests to make its query faster, about a step.
type Advice struct {
	Node *PlanNode
	Text string
}

// adviseRows is how many rows a step must read for advice about reading
// them to be worth it.
const adviseRows = 1000

// rowsOf is how many rows a step gives: what it did when it ran, else
// what the planner thinks.
func rowsOf(n *PlanNode) float64 {
	if n.ActualRows >= 0 {
		return n.ActualRows
	}
	return n.Rows
}

var filterColumns = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*(?:=|<>|!=|<=|>=|<|>|~~|LIKE|IN|IS)`)

// columnsIn are the columns a condition compares, as an index would hold.
func columnsIn(cond string) string {
	var cols []string
	for _, m := range filterColumns.FindAllStringSubmatch(cond, -1) {
		if name := m[1]; !strings.EqualFold(name, "AND") && !strings.EqualFold(name, "OR") && !strings.EqualFold(name, "NOT") {
			cols = append(cols, name)
		}
	}
	return strings.Join(cols, ", ")
}

// Advise reads a plan for what would make its query faster.
func Advise(e Engine, p *Plan) []Advice {
	if p == nil || p.Root == nil {
		return nil
	}
	var out []Advice
	add := func(n *PlanNode, format string, args ...any) {
		out = append(out, Advice{Node: n, Text: fmt.Sprintf(format, args...)})
	}
	r, ok := SQL(e)
	p.Root.Walk(func(n *PlanNode, _ int) {
		if ok {
			r.AdvisePlanStep(n, add)
		}
		// An estimate far from what ran misleads the planner's choices.
		if n.ActualRows >= adviseRows && n.Rows >= 0 && (n.ActualRows > 10*max(n.Rows, 1) || n.Rows > 10*n.ActualRows) && n.Target != "" {
			add(n, "The planner expected %s rows of %s and found %s: fresher statistics (ANALYZE) may give it a better plan.",
				count(n.Rows), n.Target, count(n.ActualRows))
		}
	})
	return out
}

func count(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
