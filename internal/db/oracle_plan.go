package db

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Oracle's plans: EXPLAIN PLAN writes the estimated plan into the
// session's PLAN_TABLE, read after it; Explain Analyze runs the
// statement, its run's statistics gathered, and reads them from
// V$SQL_PLAN_STATISTICS_ALL.

// oracleStatementID names the rows of PLAN_TABLE the app's EXPLAIN PLAN
// writes. Explain is asked for its steps apart from its statement, so it
// is the same each time: the rows read are those of the last plan of
// that name, by PLAN_ID.
const oracleStatementID = "DGOPHER"

// oracleExplainPrefix makes the plan of the statement that follows it.
const oracleExplainPrefix = "EXPLAIN PLAN SET STATEMENT_ID = '" + oracleStatementID + "' FOR "

// oracleAnalyzeMarker starts the statement Explain Analyze runs, which
// finds its cursor in V$SQL by it.
const oracleAnalyzeMarker = "/* DGopher Explain Analyze */ "

// oraclePlanColumns are the columns of a plan's step in PLAN_TABLE and
// V$SQL_PLAN alike, p, as ParsePlan reads them, with the rows of the
// table it reads by its statistics.
const oraclePlanColumns = `p.id, p.parent_id, p.depth, p.operation, p.options, p.object_owner, p.object_name, p.object_type,
  p.cost, p.cardinality, p.bytes, p.time, p.partition_start, p.partition_stop, p.access_predicates, p.filter_predicates,
  (SELECT t.num_rows FROM all_tables t WHERE t.owner = p.object_owner AND t.table_name = p.object_name) AS table_rows`

// oraclePlanSQL reads the last plan EXPLAIN PLAN wrote.
const oraclePlanSQL = `SELECT ` + oraclePlanColumns + `
FROM plan_table p
WHERE p.statement_id = '` + oracleStatementID + `'
  AND p.plan_id = (SELECT MAX(plan_id) FROM plan_table WHERE statement_id = '` + oracleStatementID + `')
ORDER BY p.id`

// oracleAnalyzedPlanSQL reads the plan of the cursor Explain Analyze ran
// last, as the user, with what its last run took: the time of a step
// holds its steps' too, as Oracle counts it.
const oracleAnalyzedPlanSQL = `SELECT ` + oraclePlanColumns + `,
  p.last_starts AS starts, p.last_output_rows AS actual_rows, ROUND(p.last_elapsed_time / 1000, 3) AS elapsed_ms,
  p.last_cr_buffer_gets + p.last_cu_buffer_gets AS buffer_gets, p.last_disk_reads AS disk_reads
FROM v$sql_plan_statistics_all p
WHERE (p.sql_id, p.child_number) = (SELECT sql_id, child_number FROM (
    SELECT s.sql_id, s.child_number FROM v$sql s
    WHERE s.sql_text LIKE '` + oracleAnalyzeMarker + `%' AND s.parsing_user_id = UID
    ORDER BY s.last_active_time DESC, s.last_load_time DESC)
  WHERE ROWNUM = 1)
ORDER BY p.id`

// oracleDiscardPlanSQL deletes the rows EXPLAIN PLAN wrote, inside the
// transaction that was open before it. A read-only session (ORA-28193),
// or a read-only transaction (ORA-01456), keeps them: they are the
// session's own, and the next plan is read by its PLAN_ID.
const oracleDiscardPlanSQL = `BEGIN
  DELETE FROM plan_table WHERE statement_id = '` + oracleStatementID + `';
EXCEPTION
  WHEN OTHERS THEN
    IF SQLCODE NOT IN (-28193, -1456) THEN
      RAISE;
    END IF;
END;`

// Explain writes the statement's plan into PLAN_TABLE with EXPLAIN PLAN,
// which runs nothing, and reads it; the transaction its rows began is
// rolled back, or, in one open before, they are deleted. The safety
// policy reads the statement as typed: EXPLAIN PLAN of a SELECT is
// allowed on a read-only connection, as 23ai's read-only session allows
// it. Analyze runs the statement, read to its end, with the session's
// statistics level ALL, then TYPICAL again, Oracle's default.
func (oracleEngine) Explain(statement string, analyze bool) (ExplainRun, bool) {
	if analyze {
		return ExplainRun{Before: []string{"ALTER SESSION SET STATISTICS_LEVEL = ALL"}, Statement: oracleAnalyzeMarker + statement,
			Offset: len(oracleAnalyzeMarker), Read: oracleAnalyzedPlanSQL, RunsWhole: true, ReviewsTyped: true,
			After: []string{"ALTER SESSION SET STATISTICS_LEVEL = TYPICAL"}}, true
	}
	return ExplainRun{Statement: oracleExplainPrefix + statement, Offset: len(oracleExplainPrefix), Read: oraclePlanSQL,
		Discard: []string{oracleDiscardPlanSQL}, ReviewsTyped: true}, true
}

// ParsePlan reads the rows of oraclePlanSQL, or of
// oracleAnalyzedPlanSQL, a step each, in the order of their IDs, each
// under the step of its PARENT_ID.
func (oracleEngine) ParsePlan(cols []string, rows [][]any) (*Plan, bool) {
	at := map[string]int{}
	for i, c := range cols {
		at[strings.ToUpper(c)] = i
	}
	for i, want := range []string{"ID", "PARENT_ID", "DEPTH", "OPERATION", "OPTIONS", "OBJECT_OWNER", "OBJECT_NAME"} {
		if i >= len(cols) || strings.ToUpper(cols[i]) != want {
			return nil, false
		}
	}
	if len(rows) == 0 {
		return nil, false
	}
	value := func(r []any, name string) string {
		i, ok := at[name]
		if !ok || i >= len(r) || r[i] == nil {
			return ""
		}
		return strings.TrimSpace(Display(r[i]))
	}
	number := func(r []any, name string) float64 {
		if f, err := strconv.ParseFloat(value(r, name), 64); err == nil {
			return f
		}
		return -1
	}
	_, analyzed := at["ACTUAL_ROWS"]
	p := &Plan{Analyzed: analyzed}
	byID := map[string]*PlanNode{}
	for _, r := range rows {
		n := newNode()
		n.Op = strings.TrimSpace(value(r, "OPERATION") + " " + value(r, "OPTIONS"))
		if name := value(r, "OBJECT_NAME"); name != "" {
			n.Target = name
			if owner := value(r, "OBJECT_OWNER"); owner != "" {
				n.Target = owner + "." + name
			}
		}
		if access := value(r, "ACCESS_PREDICATES"); access != "" {
			n.Detail = "access: " + access
		} else if filter := value(r, "FILTER_PREDICATES"); filter != "" {
			n.Detail = "filter: " + filter
		}
		n.Cost, n.Rows = number(r, "COST"), number(r, "CARDINALITY")
		if analyzed {
			n.ActualRows, n.Loops, n.Time = number(r, "ACTUAL_ROWS"), number(r, "STARTS"), number(r, "ELAPSED_MS")
		}
		for i, c := range cols {
			if i < len(r) && r[i] != nil {
				if v := strings.TrimSpace(Display(r[i])); v != "" {
					n.Props = append(n.Props, [2]string{oraclePropName(c), v})
				}
			}
		}
		id, parent := value(r, "ID"), value(r, "PARENT_ID")
		byID[id] = n
		switch up, ok := byID[parent]; {
		case ok:
			up.Children = append(up.Children, n)
		case p.Root == nil:
			p.Root = n
		default:
			return nil, false // a second root, or a step before its parent
		}
	}
	return p, true
}

// oraclePropName writes a plan column's name as a property's: ACCESS_
// PREDICATES as "Access predicates".
func oraclePropName(column string) string {
	s := strings.ReplaceAll(strings.ToLower(column), "_", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// oracleConverted finds a column a predicate converts to compare it,
// which keeps an index on the column from being used.
var oracleConverted = regexp.MustCompile(`\b(?:TO_NUMBER|TO_BINARY_DOUBLE|TO_BINARY_FLOAT|SYS_OP_C2C)\((?:"[^"]+"\.)?"([^"]+)"\)`)

// oracleCompared finds the columns a predicate compares, written quoted.
var oracleCompared = regexp.MustCompile(`"([^"]+)"\s*(?:=|<>|!=|<=|>=|<|>|(?i:like|in|is|between)\b)`)

func (oracleEngine) AdvisePlanStep(n *PlanNode, add func(n *PlanNode, format string, args ...any)) {
	filter := n.Prop("Filter predicates")
	for _, pred := range []string{n.Prop("Access predicates"), filter} {
		for _, m := range oracleConverted.FindAllStringSubmatch(pred, -1) {
			add(n, "%s converts the column %s to compare it, which keeps an index on it from being used: compare it with a value of its own type.",
				m[0], m[1])
		}
	}
	switch {
	case n.Op == "MERGE JOIN CARTESIAN":
		add(n, "The MERGE JOIN CARTESIAN pairs each row of one input with every row of the other: check the query's join conditions.")
	case strings.HasPrefix(n.Op, "TABLE ACCESS") && strings.HasSuffix(n.Op, " FULL"):
		read := jsonNumber(n.Prop("Table rows"))
		if filter == "" || read < adviseRows || rowsOf(n) < 0 || rowsOf(n)*10 > read {
			return
		}
		var cols []string
		for _, m := range oracleCompared.FindAllStringSubmatch(filter, -1) {
			if !slices.Contains(cols, m[1]) {
				cols = append(cols, m[1])
			}
		}
		where := "the columns of its filter"
		if len(cols) > 0 {
			where = "(" + strings.Join(cols, ", ") + ")"
		}
		add(n, "A full scan reads all %s rows of %s to keep %s of them: an index on %s may let it read only those.",
			count(read), n.Target, count(rowsOf(n)), where)
	}
}
