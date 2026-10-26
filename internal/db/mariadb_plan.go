package db

import (
	"encoding/json"
	"fmt"
	"strings"

	"dgopher/internal/lists"
)

// mariadbBlockJoin is the step of a block nested-loop join.
const mariadbBlockJoin = "Block nested-loop join"

// mariadbSteps name the objects of MariaDB's JSON plan that are steps, by
// their key, in the order a step's own steps are shown.
var mariadbSteps = []struct{ key, op string }{
	{"union_result", "Union result"},
	{"window_functions_computation", "Window functions"},
	{"filesort", "Sort"},
	{"read_sorted_file", "Read sorted file"},
	{"temporary_table", "Temporary table"},
	{"duplicates_removal", "Duplicates removal"},
	{"table", ""},
	{"block-nl-join", mariadbBlockJoin},
	{"materialized", "Materialize"},
	{"subquery_cache", "Subquery cache"},
	{"expression_cache", "Expression cache"},
	{"query_block", "Query block"},
}

// mariadbStepOps are mariadbSteps' names by key.
var mariadbStepOps = func() map[string]string {
	ops := map[string]string{}
	for _, s := range mariadbSteps {
		ops[s.key] = s.op
	}
	return ops
}()

// mariadbStepLists are the lists of a plan whose items each hold steps.
var mariadbStepLists = []string{"nested_loop", "query_specifications", "sorts", "subqueries"}

// mariadbAccess names a table's access_type as MySQL's tree names its
// steps.
var mariadbAccess = map[string]string{
	"ALL": "Table scan", "index": "Index scan", "range": "Index range scan", "ref": "Index lookup",
	"eq_ref": "Unique index lookup", "ref_or_null": "Index lookup or null", "const": "Constant row",
	"system": "Constant row", "fulltext": "Full-text search", "index_merge": "Index merge",
	"hash_ALL": "Hash join, table scan", "hash_index": "Hash join, index scan", "hash_range": "Hash join, index range scan",
	"hash_eq_ref": "Hash join, unique index lookup", "hash_ref": "Hash join, index lookup",
}

// parseMariaDBPlan reads EXPLAIN FORMAT=JSON, or ANALYZE FORMAT=JSON,
// whose r_ members are what running the query took.
func parseMariaDBPlan(text string) (*Plan, bool) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return nil, false
	}
	block, ok := doc["query_block"].(map[string]any)
	if !ok {
		return nil, false
	}
	p := &Plan{Root: mariadbStep(newNode(), "query_block", block)}
	p.Root.Walk(func(n *PlanNode, _ int) { p.Analyzed = p.Analyzed || n.Loops >= 0 })
	return p, true
}

// mariadbStep builds the step an object of the plan is, by its key, with
// its own steps.
func mariadbStep(n *PlanNode, key string, m map[string]any) *PlanNode {
	if op, ok := mariadbStepOps[key]; ok {
		n.Op = op
	}
	n.Cost, n.Loops = jsonNumber(m["cost"]), jsonNumber(m["r_loops"])
	own := -1.0 // the time of the step alone
	switch key {
	case "query_block":
		if id := jsonNumber(m["select_id"]); id >= 0 {
			n.Op += fmt.Sprintf(" #%v", id)
		}
		if having := str(m, "having_condition"); having != "" {
			n.Detail = "Having: " + having
		}
	case "table":
		mariadbTable(n, m)
		own = sumTimes(m, "r_table_time_ms", "r_other_time_ms")
	case "filesort":
		if k := str(m, "sort_key"); k != "" {
			n.Detail = "Sort key: " + k
		}
		n.ActualRows = jsonNumber(m["r_output_rows"])
		own = jsonNumber(m["r_total_time_ms"])
	case "block-nl-join":
		if join := str(m, "join_type"); join != "" && join != "BNL" {
			n.Op += " (" + join + ")"
		}
		own = sumTimes(m, "r_unpack_time_ms", "r_other_time_ms")
	case "union_result", "read_sorted_file":
		n.Target = str(m, "table_name")
		n.ActualRows = jsonNumber(m["r_rows"])
	case "materialized":
		if jsonNumber(m["unique"]) == 1 {
			n.Op += " (unique)"
		}
	}
	if c := str(m, "attached_condition"); c != "" && n.Detail == "" {
		n.Detail = "Condition: " + c
	}
	var childKeys []string
	for _, s := range mariadbSteps {
		if child, ok := m[s.key].(map[string]any); ok {
			n.Children = append(n.Children, mariadbStep(newNode(), s.key, child))
			childKeys = append(childKeys, s.key)
		} else if items, ok := m[s.key].([]any); ok {
			// duplicates_removal holds its steps as a list.
			list := newNode()
			list.Op = s.op
			list.Children = mariadbListSteps(items)
			n.Children = append(n.Children, list)
			childKeys = append(childKeys, s.key)
		}
	}
	for _, key := range mariadbStepLists {
		if items, ok := m[key].([]any); ok {
			n.Children = append(n.Children, mariadbListSteps(items)...)
			childKeys = append(childKeys, key)
		}
	}
	n.Props = props(m, childKeys...)
	if key == "query_block" {
		n.Time = jsonNumber(m["r_total_time_ms"]) // its steps' included
		return n
	}
	timed, sum := own >= 0, max(own, 0)
	for _, c := range n.Children {
		timed, sum = timed || c.Time >= 0, sum+max(c.Time, 0)
	}
	if timed {
		n.Time = sum
	}
	return n
}

// mariadbListSteps builds the steps of a list's items, each an object
// holding a step by its key.
func mariadbListSteps(items []any) []*PlanNode {
	var out []*PlanNode
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		for _, s := range mariadbSteps {
			if child, ok := m[s.key].(map[string]any); ok {
				out = append(out, mariadbStep(newNode(), s.key, child))
			}
		}
	}
	return out
}

// mariadbTable reads a step reading a table. rows and r_rows are per loop,
// the planner's loops and those that ran.
func mariadbTable(n *PlanNode, m map[string]any) {
	if msg := str(m, "message"); msg != "" {
		n.Op = msg
		return
	}
	access := str(m, "access_type")
	n.Op = mariadbAccess[access]
	if n.Op == "" {
		n.Op = "Access " + access
	}
	n.Target = str(m, "table_name")
	if key := str(m, "key"); key != "" {
		n.Target += " using " + key
	}
	if ref, ok := m["ref"].([]any); ok {
		n.Detail = "Ref: " + strings.Join(lists.Map(ref, func(r any) string { return fmt.Sprint(r) }), ", ")
	}
	if c := str(m, "attached_condition"); c != "" {
		n.Detail = "Condition: " + c
	}
	if rows := jsonNumber(m["rows"]); rows >= 0 {
		n.Rows = rows * max(jsonNumber(m["loops"]), 1)
	}
	if rows, loops := jsonNumber(m["r_rows"]), jsonNumber(m["r_loops"]); rows >= 0 && loops >= 0 {
		n.ActualRows = rows * loops
	}
}

// sumTimes adds the times of m's keys, -1 when it has none.
func sumTimes(m map[string]any, keys ...string) float64 {
	sum, found := 0.0, false
	for _, k := range keys {
		if t := jsonNumber(m[k]); t >= 0 {
			sum, found = sum+t, true
		}
	}
	if !found {
		return -1
	}
	return sum
}
