package db

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/lists"
)

// oraclePlanCols and oraclePlanRows are the rows of oraclePlanSQL as
// Oracle 23ai Free wrote them for
//
//	SELECT a."id", b."id" FROM "DGOPHER"."cap_a" a, "DGOPHER"."cap_b" b
//	WHERE a."g" = 5 AND a."code" = 7 AND b."id" > 3
//
// cap_a holding 20000 rows, its code a VARCHAR2, and cap_b 50 rows with
// an index on id.
var oraclePlanCols = []string{"ID", "PARENT_ID", "DEPTH", "OPERATION", "OPTIONS", "OBJECT_OWNER", "OBJECT_NAME", "OBJECT_TYPE", "COST",
	"CARDINALITY", "BYTES", "TIME", "PARTITION_START", "PARTITION_STOP", "ACCESS_PREDICATES", "FILTER_PREDICATES", "TABLE_ROWS"}

var oraclePlanRows = [][]any{
	{"0", nil, "0", "SELECT STATEMENT", nil, nil, nil, nil, "19", "1", "16", "1", nil, nil, nil, nil, nil},
	{"1", "0", "1", "MERGE JOIN", "CARTESIAN", nil, nil, nil, "19", "1", "16", "1", nil, nil, nil, nil, nil},
	{"2", "1", "2", "TABLE ACCESS", "FULL", "DGOPHER", "cap_a", "TABLE", "18", "1", "13", "1", nil, nil, nil,
		`"A"."g"=5 AND TO_NUMBER("A"."code")=7`, "20000"},
	{"3", "1", "2", "BUFFER", "SORT", nil, nil, nil, "1", "48", "144", "1", nil, nil, nil, nil, nil},
	{"4", "3", "3", "INDEX", "RANGE SCAN", "DGOPHER", "cap_b_id", "INDEX", "1", "48", "144", "1", nil, nil, `"B"."id">3`, nil, nil},
}

// PLAN_TABLE's rows read as a tree of steps, each under its parent, with
// the planner's estimates; advice names the cartesian join, the full scan
// that keeps few rows, and the column converted to be compared.
func TestOracleParsePlan(t *testing.T) {
	p, ok := oracleEngine{}.ParsePlan(oraclePlanCols, oraclePlanRows)
	if !ok || p.Analyzed {
		t.Fatalf("plan %+v %v", p, ok)
	}
	var steps []string
	p.Root.Walk(func(n *PlanNode, depth int) {
		steps = append(steps, strings.Repeat(" ", depth)+n.Op+" "+n.Target)
	})
	want := []string{"SELECT STATEMENT ", " MERGE JOIN CARTESIAN ", "  TABLE ACCESS FULL DGOPHER.cap_a", "  BUFFER SORT ", "   INDEX RANGE SCAN DGOPHER.cap_b_id"}
	if !slices.Equal(steps, want) {
		t.Fatalf("steps %q", steps)
	}
	scan := p.Root.Children[0].Children[0]
	if scan.Cost != 18 || scan.Rows != 1 || scan.ActualRows != -1 || scan.Detail != `filter: "A"."g"=5 AND TO_NUMBER("A"."code")=7` ||
		scan.Prop("Table rows") != "20000" || scan.Prop("Object type") != "TABLE" {
		t.Fatalf("scan %+v", scan)
	}
	if ix := p.Root.Children[0].Children[1].Children[0]; ix.Detail != `access: "B"."id">3` {
		t.Fatalf("index %+v", ix)
	}
	texts := lists.Map(Advise(Oracle, p), func(a Advice) string { return a.Text })
	for _, want := range []string{"MERGE JOIN CARTESIAN pairs each row", `TO_NUMBER("A"."code") converts the column code`,
		"A full scan reads all 20000 rows of DGOPHER.cap_a to keep 1 of them: an index on (g)"} {
		if !slices.ContainsFunc(texts, func(s string) bool { return strings.Contains(s, want) }) {
			t.Errorf("no advice %q in %q", want, texts)
		}
	}
	if _, ok := (oracleEngine{}).ParsePlan([]string{"ID", "NAME"}, [][]any{{"1", "x"}}); ok {
		t.Error("other rows read as a plan")
	}
	if _, ok := (oracleEngine{}).ParsePlan(oraclePlanCols, [][]any{oraclePlanRows[0], oraclePlanRows[0]}); ok {
		t.Error("two roots read as a plan")
	}
}

// Explain Analyze's rows carry what the run took, a step's time holding
// its steps'.
func TestOracleParseAnalyzedPlan(t *testing.T) {
	cols := append(slices.Clone(oraclePlanCols), "STARTS", "ACTUAL_ROWS", "ELAPSED_MS", "BUFFER_GETS", "DISK_READS")
	rows := [][]any{
		{"0", nil, "0", "SELECT STATEMENT", nil, nil, nil, nil, "18", nil, nil, nil, nil, nil, nil, nil, nil, "1", "20", "1.25", "40", "0"},
		{"1", "0", "1", "TABLE ACCESS", "FULL", "DGOPHER", "it_plan", "TABLE", "18", "20", "160", "1", nil, nil, nil, `"g"=5`, "20000",
			"1", "20", "1.2", "40", "0"},
	}
	p, ok := oracleEngine{}.ParsePlan(cols, rows)
	if !ok || !p.Analyzed {
		t.Fatalf("plan %+v %v", p, ok)
	}
	scan := p.Root.Children[0]
	if scan.ActualRows != 20 || scan.Loops != 1 || scan.Time != 1.2 || scan.Prop("Buffer gets") != "40" {
		t.Fatalf("scan %+v", scan)
	}
}

// oracleExplain gets a statement's plan on a session as the editor does.
func oracleExplain(t *testing.T, s *Session, statement string, analyze bool) (*Plan, error) {
	t.Helper()
	ctx := context.Background()
	run, _ := Explain(Oracle, statement, analyze)
	var cols []string
	var rows [][]any
	err := s.RunExplain(ctx, run, func(string, time.Duration, error) {}, func(ctx context.Context) error {
		cur, err := explainStatement(ctx, s, run)
		if err != nil {
			return err
		}
		defer cur.Close()
		cols = lists.Map(cur.Columns, func(c ColumnInfo) string { return c.Name })
		rows, err = cur.Fetch(MaxRows)
		return err
	})
	if err != nil {
		return nil, err
	}
	p, _ := PlanResults(Oracle, [][]string{cols}, [][][]any{rows})
	if p == nil {
		t.Fatalf("no plan in %q %v", cols, rows)
	}
	return p, nil
}

// EXPLAIN PLAN writes PLAN_TABLE, which begins a transaction: one the
// explain began is rolled back, and in one open before only its rows go,
// the transaction's own changes kept. A read-only session explains too,
// and keeps no transaction.
func TestOracleExplainLeavesTransactions(t *testing.T) {
	d := openOracle(t, oracleConfig)
	ctx := context.Background()
	mustExec(t, d, `DROP TABLE IF EXISTS "ex_t"`, `CREATE TABLE "ex_t" ("id" NUMBER PRIMARY KEY)`)
	t.Cleanup(func() { mustExec(t, d, `DROP TABLE IF EXISTS "ex_t"`) })
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	planRows := func() string {
		t.Helper()
		row, _ := queryOne(t, s, "SELECT COUNT(*) FROM plan_table WHERE statement_id = '"+oracleStatementID+"'")
		return Display(row[0])
	}
	for i := range 2 {
		p, err := oracleExplain(t, s, `SELECT * FROM "ex_t" WHERE "id" = 1`, false)
		if err != nil || !strings.HasPrefix(p.Root.Op, "SELECT STATEMENT") || len(p.Root.Children) == 0 {
			t.Fatalf("explain %d: %+v %v", i, p, err)
		}
		if s.Tx() != TxNone || planRows() != "0" {
			t.Fatalf("explain %d left %v, %s plan rows", i, s.Tx(), planRows())
		}
	}
	if err := s.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(ctx, `INSERT INTO "ex_t" VALUES (2)`); err != nil || s.Tx() != TxOpen {
		t.Fatalf("insert: %v %v", err, s.Tx())
	}
	if _, err := oracleExplain(t, s, `UPDATE "ex_t" SET "id" = 3`, false); err != nil {
		t.Fatal(err)
	}
	if row, _ := queryOne(t, s, `SELECT COUNT(*) FROM "ex_t" WHERE "id" = 2`); s.Tx() != TxOpen || Display(row[0]) != "1" || planRows() != "0" {
		t.Fatalf("the open transaction: %v, %v of its row, %s plan rows", s.Tx(), row, planRows())
	}
	if err := s.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	cfg := oracleConfig
	cfg.ReadOnly = true
	ro, err := openOracleAgain(t, cfg).Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if p, err := oracleExplain(t, ro, `SELECT * FROM "ex_t"`, false); err != nil || p.Root == nil || ro.Tx() != TxNone {
		t.Fatalf("read-only: %+v %v %v", p, err, ro.Tx())
	}
}

// Explain Analyze runs the statement and reads what it took from V$
// views, which a user without SELECT_CATALOG_ROLE is told it needs; its
// session's statistics level is set back.
func TestOracleExplainAnalyze(t *testing.T) {
	d := openOracle(t, oracleSystemConfig)
	ctx := context.Background()
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := oracleExplain(t, s, "SELECT LEVEL FROM dual CONNECT BY LEVEL <= 3000", true)
	if err != nil || !p.Analyzed || p.Root.ActualRows != 3000 {
		t.Fatalf("analyzed %+v %v", p, err)
	}
	if row, _ := queryOne(t, s, "SELECT value FROM v$parameter WHERE name = 'statistics_level'"); Display(row[0]) != "TYPICAL" {
		t.Fatalf("statistics level %v", row[0])
	}
	user, err := openOracleAgain(t, oracleConfig).Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer user.Close()
	_, err = oracleExplain(t, user, "SELECT 1 FROM dual", true)
	if err == nil || !strings.Contains(DescribeError(err).Hint, "SELECT_CATALOG_ROLE") {
		t.Fatalf("without the catalog's privilege: %v %+v", err, DescribeError(err))
	}
}
