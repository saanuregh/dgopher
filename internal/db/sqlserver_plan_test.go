package db

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/lists"
)

// showplanColumn is the column SQL Server returns a plan's XML in.
const showplanColumn = "Microsoft SQL Server 2005 XML Showplan"

// showplan parses captured plans, each the row of a result, as the
// results of one explain: testdata/sqlserver_plans holds plans SQL Server
// 2025 returned for SET SHOWPLAN_XML (estimated-*) and SET STATISTICS XML
// (actual-*).
func showplan(t *testing.T, files ...string) *Plan {
	t.Helper()
	var rows [][]any
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join("testdata", "sqlserver_plans", f+".xml"))
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, []any{string(b)})
	}
	r, _ := SQL(SQLServer)
	p, ok := r.ParsePlan([]string{showplanColumn}, rows)
	if !ok || p.Root == nil {
		t.Fatalf("%v not read as a plan", files)
	}
	return p
}

func TestSQLServerExplain(t *testing.T) {
	run, ok := Explain(SQLServer, "SELECT 1", false)
	want := ExplainRun{Before: []string{"SET SHOWPLAN_XML ON"}, Statement: "SELECT 1", After: []string{"SET SHOWPLAN_XML OFF"}}
	if !ok || !reflect.DeepEqual(run, want) {
		t.Fatalf("explain %+v %v", run, ok)
	}
	run, ok = Explain(SQLServer, "SELECT 1", true)
	want = ExplainRun{Before: []string{"SET STATISTICS XML ON"}, Statement: "SELECT 1", After: []string{"SET STATISTICS XML OFF"}}
	if !ok || !reflect.DeepEqual(run, want) {
		t.Fatalf("explain analyze %+v %v", run, ok)
	}
}

func TestSQLServerParseShowplan(t *testing.T) {
	p := showplan(t, "estimated-seek-1")
	seek := p.Root
	if p.Analyzed || seek.Op != "Clustered Index Seek" || seek.Target != "dbo.plan_rows using PK__plan_row__3213E83F9A72877F" ||
		seek.Detail != "Seek Keys: [id] = CONVERT_IMPLICIT(int,[@1],0)" || seek.Cost != 0.0032831 || seek.Rows != 1 ||
		seek.ActualRows != -1 || seek.Loops != -1 || seek.Time != -1 || len(seek.Children) != 0 {
		t.Fatalf("seek %+v", seek)
	}
	if seek.Prop("EstimateIO") != "0.003125" || seek.Prop("TableCardinality") != "50000" {
		t.Fatalf("seek props %q", seek.Props)
	}

	p = showplan(t, "actual-seek-1")
	if !p.Analyzed || p.Root.ActualRows != 1 || p.Root.Loops != 1 || p.Root.Time != 0 || p.Root.Prop("Actual Logical Reads") != "3" {
		t.Fatalf("actual seek %+v", p.Root)
	}

	p = showplan(t, "estimated-scan-1")
	if p.Root.Op != "Clustered Index Scan" || p.Root.Detail != "Predicate: [pad]='pad5'" || p.Root.Prop("EstimatedRowsRead") != "50000" ||
		p.Root.Prop("Missing index") != "dbo.plan_rows (pad)" {
		t.Fatalf("scan %+v", p.Root)
	}

	p = showplan(t, "estimated-join-1")
	join := p.Root
	if join.Op != "Nested Loops (Inner Join)" || join.Detail != "Outer References: [gr].[g], [Expr1002]" || len(join.Children) != 2 {
		t.Fatalf("join %+v", join)
	}
	if outer := join.Children[0]; outer.Op != "Clustered Index Scan" || outer.Target != "dbo.plan_groups using PK__plan_gro__3BD0199423A7D85A" ||
		outer.Detail != "Predicate: [gr].[name] like 'group 1%'" || outer.Rows != 100 {
		t.Fatalf("join's outer %+v", outer)
	}
	// The inner side runs once for each outer row: its rows are those of
	// all its runs.
	if inner := join.Children[1]; inner.Op != "Index Seek" || inner.Rows != 5000 || inner.Detail != "Seek Keys: [r].[g] = [gr].[g]" {
		t.Fatalf("join's inner %+v", inner)
	}

	p = showplan(t, "estimated-aggregate-1")
	if agg := p.Root.Children[0]; p.Root.Op != "Compute Scalar" || agg.Op != "Hash Match (Aggregate)" || agg.Detail != "Hash Keys: [g]" ||
		agg.Children[0].Op != "Clustered Index Scan" {
		t.Fatalf("aggregate %+v / %+v", p.Root, agg)
	}

	p = showplan(t, "actual-sort-1")
	if sort := p.Root; !p.Analyzed || sort.Op != "Sort" || sort.Detail != "Order By: [pad] DESC" || sort.Time != 248 ||
		sort.ActualRows != 50000 || sort.Prop("Spill to tempdb") != "level 2" {
		t.Fatalf("sort %+v", sort)
	}

	p = showplan(t, "actual-lookup-1")
	if gather := p.Root; gather.Op != "Parallelism (Gather Streams)" || gather.Time != 23 || gather.ActualRows != 15000 ||
		gather.Prop("Missing index") != "dbo.plan_rows (g) INCLUDE (code, pad)" || gather.Prop("Missing index impact") != "93.0655" {
		t.Fatalf("gather %+v", gather)
	}
	// Rows and executions are those of all threads; the time, the
	// longest thread's.
	loops := p.Root.Children[0]
	if loops.Op != "Nested Loops (Inner Join)" || loops.ActualRows != 15000 || loops.Loops != 16 || loops.Time != 6 {
		t.Fatalf("nested loops %+v", loops)
	}
	lookup := loops.Children[1]
	if lookup.Op != "Key Lookup (Clustered)" || lookup.Target != "dbo.plan_rows using PK__plan_row__3213E83F9A72877F" ||
		lookup.Rows != 15000 || lookup.ActualRows != 15000 || lookup.Loops != 15000 || lookup.Prop("Output") != "[code], [pad]" {
		t.Fatalf("key lookup %+v", lookup)
	}

	p = showplan(t, "estimated-nojoin-1")
	if loops := p.Root.Children[0]; loops.Op != "Nested Loops (Inner Join)" || loops.Prop("No join predicate") != "true" {
		t.Fatalf("join without a predicate %+v", loops)
	}

	p = showplan(t, "estimated-convert-1")
	if p.Root.Op != "Index Scan" || p.Root.Prop("Plan affecting convert") != "Seek Plan: CONVERT_IMPLICIT(nvarchar(20),[code],0)=[@1]" {
		t.Fatalf("convert %+v", p.Root)
	}

	// A batch: a root for each statement with a plan, under the batch's.
	// SHOWPLAN_XML gives one document for the batch; STATISTICS XML one
	// for each statement, in results of their own.
	for _, files := range [][]string{{"estimated-batch-1"}, {"actual-batch-1", "actual-batch-2"}} {
		p = showplan(t, files...)
		batch := p.Root
		if batch.Op != "Batch" || len(batch.Children) != 2 || p.Analyzed != (len(files) == 2) {
			t.Fatalf("%v: batch %+v", files, batch)
		}
		first, second := batch.Children[0], batch.Children[1]
		if first.Op != "SELECT" || first.Detail != "SELECT COUNT(*) FROM dbo.plan_rows WHERE g = @g" || first.Children[0].Op != "Compute Scalar" ||
			second.Op != "SELECT" || second.Children[0].Op != "Clustered Index Seek" {
			t.Fatalf("%v: statements %+v / %+v", files, first, second)
		}
		if batch.Cost != first.Cost+second.Cost || first.Cost != 0.0033675 {
			t.Fatalf("%v: costs %v %v %v", files, batch.Cost, first.Cost, second.Cost)
		}
	}

	r, _ := SQL(SQLServer)
	for _, c := range []struct {
		cols []string
		rows [][]any
	}{
		{[]string{"a"}, [][]any{{"<ShowPlanXML/>"}}},
		{[]string{showplanColumn}, nil},
		{[]string{showplanColumn}, [][]any{{"not xml"}}},
		{[]string{showplanColumn}, [][]any{{`<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan"><BatchSequence/></ShowPlanXML>`}}},
	} {
		if p, ok := r.ParsePlan(c.cols, c.rows); ok {
			t.Errorf("%v %v read as a plan: %+v", c.cols, c.rows, p)
		}
	}
}

func TestSQLServerPlanAdvice(t *testing.T) {
	advice := func(files ...string) string {
		return strings.Join(lists.Map(Advise(SQLServer, showplan(t, files...)), func(a Advice) string { return a.Text }), "\n")
	}
	for file, wants := range map[string][]string{
		"estimated-scan-1": {
			"SQL Server suggests an index on dbo.plan_rows (pad), which it estimates makes the query 99.7% cheaper.",
			"A Clustered Index Scan reads 50000 rows of dbo.plan_rows to keep 1 of them: an index on (pad) may let it read only those.",
		},
		"estimated-lookup-1": {
			"SQL Server suggests an index on dbo.plan_rows (g) INCLUDE (code, pad), which it estimates makes the query 93.1% cheaper.",
			"A Key Lookup reads 15000 rows of dbo.plan_rows one at a time: an index that holds [code], [pad] too, as INCLUDE columns, avoids it.",
		},
		"actual-sort-1": {
			"The Sort spills to tempdb (level 2): its memory grant was too small; fresher statistics, or an index in the order it needs, may avoid it.",
		},
		"estimated-nojoin-1": {
			"The Nested Loops (Inner Join) has no join predicate: each row of one input meets every row of the other. Check the query's join conditions.",
		},
		"estimated-convert-1": {
			"An implicit conversion, CONVERT_IMPLICIT(nvarchar(20),[code],0)=[@1], affects the plan's Seek Plan: compare the column with a value of its own type.",
		},
	} {
		got := advice(file)
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("%s: advice %q, want %q", file, got, want)
			}
		}
	}
	for _, file := range []string{"estimated-seek-1", "actual-seek-1", "estimated-sort-1"} {
		if got := advice(file); got != "" {
			t.Errorf("%s: advice %q", file, got)
		}
	}
}

// Explain Analyze of a batch returns each statement's rows, then its
// plan: the plans are read together, the rows left.
func TestSQLServerPlanResults(t *testing.T) {
	read := func(f string) []any {
		b, err := os.ReadFile(filepath.Join("testdata", "sqlserver_plans", f+".xml"))
		if err != nil {
			t.Fatal(err)
		}
		return []any{string(b)}
	}
	names := [][]string{{"a"}, {showplanColumn}, {"a"}, {showplanColumn}}
	rows := [][][]any{{{1}}, {read("actual-batch-1")}, {{2}}, {read("actual-batch-2")}}
	plan, at := PlanResults(SQLServer, names, rows)
	if plan == nil || !slices.Equal(at, []int{1, 3}) || plan.Root.Op != "Batch" {
		t.Fatalf("plan %+v at %v", plan, at)
	}
	rows[3] = [][]any{{"not a plan"}}
	if plan, at := PlanResults(SQLServer, names, rows); plan == nil || !slices.Equal(at, []int{1}) {
		t.Fatalf("with a result that is no plan: %+v at %v", plan, at)
	}
}
