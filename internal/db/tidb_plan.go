package db

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	// tidbStepID splits a step's id, as TableReader_38(Build), into its
	// operator and its part in a join.
	tidbStepID = regexp.MustCompile(`^(.+?)_\d+(?:\((\w+)\))?$`)
	// tidbTime and tidbLoops read a root step's executeInfo; a step of a
	// cop task tells only its tasks' times, inside tikv_task:{…}.
	tidbTime  = regexp.MustCompile(`(?:^|, )time:((?:[0-9.]+[a-zµ]+)+)`)
	tidbLoops = regexp.MustCompile(`(?:^|, )loops:(\d+)`)
)

// parseTiDBPlan reads EXPLAIN FORMAT='tidb_json', or EXPLAIN ANALYZE's,
// whose actRows and executeInfo are what running the query took.
func parseTiDBPlan(text string) (*Plan, bool) {
	var steps []map[string]any
	if err := json.Unmarshal([]byte(text), &steps); err != nil || len(steps) == 0 {
		return nil, false
	}
	var roots []*PlanNode
	for _, m := range steps {
		if str(m, "id") == "" {
			return nil, false
		}
		roots = append(roots, tidbStep(m))
	}
	p := &Plan{Root: roots[0]}
	if len(roots) > 1 {
		p.Root = newNode()
		p.Root.Op, p.Root.Children = "Plan", roots
	}
	p.Root.Walk(func(n *PlanNode, _ int) { p.Analyzed = p.Analyzed || n.ActualRows >= 0 })
	return p, true
}

// tidbStep builds a step of the plan, with its own steps.
func tidbStep(m map[string]any) *PlanNode {
	n := newNode()
	n.Op = str(m, "id")
	if parts := tidbStepID.FindStringSubmatch(n.Op); parts != nil {
		n.Op = parts[1]
		if parts[2] != "" {
			n.Op += " (" + parts[2] + ")"
		}
	}
	n.Target = tidbTarget(str(m, "accessObject"))
	if info := str(m, "operatorInfo"); info != "N/A" {
		n.Detail = info
	}
	n.Rows, n.ActualRows = jsonNumber(m["estRows"]), jsonNumber(m["actRows"])
	info := str(m, "executeInfo")
	if t := tidbTime.FindStringSubmatch(info); t != nil {
		if d, err := time.ParseDuration(t[1]); err == nil {
			n.Time = float64(d) / float64(time.Millisecond)
		}
	}
	if l := tidbLoops.FindStringSubmatch(info); l != nil {
		n.Loops, _ = strconv.ParseFloat(l[1], 64)
	}
	n.Props = props(m, "subOperators")
	n.Children = childNodes(m, "subOperators", tidbStep)
	return n
}

// tidbTarget names what a step reads, from its accessObject, as
// "table:t, index:i(a, b)": the table, using the index.
func tidbTarget(access string) string {
	table, ok := strings.CutPrefix(access, "table:")
	if !ok {
		return access
	}
	table, _, _ = strings.Cut(table, ", ")
	if _, index, ok := strings.Cut(access, "index:"); ok {
		return table + " using " + index
	}
	return table
}
