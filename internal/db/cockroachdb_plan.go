package db

import (
	"strconv"
	"strings"
	"time"
)

// cockroachTreeMarks are the characters CockroachDB draws a plan's tree
// with, before a step or a property.
const cockroachTreeMarks = " │├└─"

// parseCockroachPlan reads the text of CockroachDB's EXPLAIN, or EXPLAIN
// ANALYZE's: properties of the whole plan, a tree of steps, each "• op"
// with its properties on the lines below, then notes as index
// recommendations, which the root keeps.
func parseCockroachPlan(text string) (*Plan, bool) {
	var header, notes [][2]string
	var roots []*PlanNode
	type open struct {
		n      *PlanNode
		column int
	}
	var path []open
	inTree, afterTree := false, false
	for line := range strings.Lines(text) {
		line = strings.TrimRight(line, "\r\n")
		rest := strings.TrimLeft(line, cockroachTreeMarks)
		if op, ok := strings.CutPrefix(rest, "• "); ok && !afterTree {
			inTree = true
			column := len([]rune(line)) - len([]rune(rest))
			n := newNode()
			n.Op = strings.TrimSpace(op)
			for len(path) > 0 && path[len(path)-1].column >= column {
				path = path[:len(path)-1]
			}
			if len(path) == 0 {
				roots = append(roots, n)
			} else {
				parent := path[len(path)-1].n
				parent.Children = append(parent.Children, n)
			}
			path = append(path, open{n, column})
			continue
		}
		switch {
		case strings.TrimSpace(line) == "":
			// A blank line, without the tree's lines, ends the tree.
			afterTree = afterTree || inTree
		case rest == "":
		case !inTree:
			header = append(header, cockroachProp(rest))
		case afterTree:
			notes = append(notes, cockroachProp(strings.TrimSpace(rest)))
		default:
			n := path[len(path)-1].n
			n.Props = append(n.Props, cockroachProp(rest))
		}
	}
	if len(roots) == 0 {
		return nil, false
	}
	p := &Plan{Root: roots[0]}
	if len(roots) > 1 {
		p.Root = newNode()
		p.Root.Op, p.Root.Children = "Plan", roots
	}
	p.Root.Walk(func(n *PlanNode, _ int) {
		cockroachStep(n)
		p.Analyzed = p.Analyzed || n.ActualRows >= 0
	})
	p.Root.Props = append(append(p.Root.Props, header...), cockroachRecommendations(notes)...)
	return p, true
}

// cockroachProp splits a line "name: value" into its name and value; a
// line without one, as "auto commit", is a name alone.
func cockroachProp(line string) [2]string {
	name, value, _ := strings.Cut(line, ": ")
	return [2]string{strings.TrimSpace(name), strings.TrimSpace(value)}
}

// cockroachDetails are the properties that describe a step best, the
// first a step has its Detail.
var cockroachDetails = []string{"filter", "spans", "equality", "group by", "order", "set", "size"}

// cockroachStep fills a step's fields from its properties.
func cockroachStep(n *PlanNode) {
	n.Rows = cockroachCount(n.Prop("estimated row count"))
	n.ActualRows = cockroachCount(n.Prop("actual row count"))
	if d, err := time.ParseDuration(n.Prop("execution time")); err == nil {
		n.Time = float64(d) / float64(time.Millisecond)
	}
	if table := n.Prop("table"); table != "" {
		name, index, ok := strings.Cut(table, "@")
		n.Target = name
		if ok {
			n.Target += " using " + index
		}
	}
	for _, key := range cockroachDetails {
		if v := n.Prop(key); v != "" {
			n.Detail = v
			break
		}
	}
}

// cockroachCount reads a count as "20,000 (100% of the table; …)", -1
// when there is none.
func cockroachCount(s string) float64 {
	s, _, _ = strings.Cut(s, " ")
	f, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
	if s == "" || err != nil {
		return -1
	}
	return f
}

// cockroachRecommendations keeps the statements of the index
// recommendations that follow a plan, as "index recommendation" properties.
func cockroachRecommendations(notes [][2]string) [][2]string {
	var out [][2]string
	for _, p := range notes {
		if p[0] == "SQL command" || p[0] == "SQL commands" {
			out = append(out, [2]string{"index recommendation", p[1]})
		}
	}
	return out
}
