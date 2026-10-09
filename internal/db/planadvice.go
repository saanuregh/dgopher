package db

import (
	"encoding/json"
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
	p.Root.Walk(func(n *PlanNode, _ int) {
		switch e {
		case Postgres:
			advisePostgres(n, add)
		case MySQL:
			adviseMySQL(n, add)
		case SQLite:
			adviseSQLite(n, add)
		case ClickHouse:
			adviseClickHouse(n, add)
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

func advisePostgres(n *PlanNode, add func(*PlanNode, string, ...any)) {
	switch {
	case strings.HasPrefix(n.Op, "Seq Scan"):
		// Only a plan that ran tells how few rows the filter keeps.
		removed, _ := strconv.ParseFloat(n.Prop("Rows Removed by Filter"), 64)
		if filter := n.Prop("Filter"); filter != "" && removed >= adviseRows && removed > 10*rowsOf(n) {
			add(n, "Seq Scan reads every row of %s to keep the few of %s: an index on (%s) may let it read only those.", n.Target, filter, columnsIn(filter))
		}
	case n.Op == "Sort" && n.Prop("Sort Space Type") == "Disk":
		add(n, "The sort spills to disk (%s kB): more work_mem, or an index in the sort's order, avoids it.", n.Prop("Sort Space Used"))
	case n.Op == "Hash":
		if batches, _ := strconv.Atoi(n.Prop("Hash Batches")); batches > 1 {
			add(n, "The hash is built in %d batches on disk: more work_mem would keep it in memory.", batches)
		}
	case strings.HasPrefix(n.Op, "Nested Loop") && len(n.Children) == 2:
		inner := n.Children[1]
		if strings.HasPrefix(inner.Op, "Seq Scan") && inner.Loops >= adviseRows {
			add(inner, "The nested loop reads all of %s %s times: an index on its join columns may let each loop find its rows.", inner.Target, count(inner.Loops))
		}
	}
}

func adviseMySQL(n *PlanNode, add func(*PlanNode, string, ...any)) {
	switch {
	case strings.HasPrefix(n.Op, "Table scan") && rowsOf(n) >= adviseRows:
		add(n, "A table scan reads every row of %s: an index on the columns of the WHERE may let it read only those it keeps.", n.Target)
	case strings.HasPrefix(n.Op, "Temporary table") && rowsOf(n) >= adviseRows:
		add(n, "A temporary table holds the rows to group: an index on the GROUP BY columns may let MySQL read them in order instead.")
	case strings.HasPrefix(n.Op, "Sort") && rowsOf(n) >= adviseRows:
		add(n, "The rows are sorted after they are read: an index in the ORDER BY's order may give them sorted.")
	}
}

func adviseSQLite(n *PlanNode, add func(*PlanNode, string, ...any)) {
	switch {
	case strings.HasPrefix(n.Op, "SCAN ") && !strings.Contains(n.Op, "USING"):
		add(n, "SCAN reads every row of %s: an index on the columns of the WHERE may let SQLite SEARCH it instead.", n.Target)
	case strings.HasPrefix(n.Op, "USE TEMP B-TREE FOR"):
		add(n, "SQLite builds a temporary index to %s: an index in that order may save it.", strings.ToLower(strings.TrimPrefix(n.Op, "USE TEMP B-TREE FOR ")))
	}
}

func adviseClickHouse(n *PlanNode, add func(*PlanNode, string, ...any)) {
	if !strings.HasPrefix(n.Op, "ReadFromMergeTree") {
		return
	}
	var indexes []map[string]any
	if json.Unmarshal([]byte(n.Prop("Indexes")), &indexes) != nil {
		return
	}
	for _, ix := range indexes {
		if str(ix, "Type") != "PrimaryKey" {
			continue
		}
		initial, selected := jsonNumber(ix["Initial Granules"]), jsonNumber(ix["Selected Granules"])
		if initial > 1 && selected == initial {
			add(n, "The primary key narrows nothing: all %s granules of %s are read. A WHERE on the sorting key's first columns would skip most.", count(initial), n.Target)
		}
	}
}
