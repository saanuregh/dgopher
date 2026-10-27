package query

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/safety"
	"dgopher/internal/testutil"
	"dgopher/internal/testutil/optionalserver"

	"github.com/egoist/mygo/ui"
)

// Explain shows the plan's steps with the advice about them, as a flame
// graph too; Explain Analyze refuses an engine that explains without
// running, and a statement that writes.
func TestPlanView(t *testing.T) {
	_, tt, cn, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1200, 800, "SELECT * FROM things WHERE kind = 'a' ORDER BY name")
	cn.DB().SQL.Exec(`CREATE TABLE things (name TEXT, kind TEXT)`)
	q.Run(RunExplain)
	testutil.WaitFor(t, tt, "the plan", func() bool { r := q.current(); return !q.Running && r != nil && r.plan != nil })
	r := q.current()
	if len(r.advice) != 2 || !tt.HasText("SCAN things") || !testutil.HasTextContaining(tt, "SCAN reads every row of things") {
		t.Fatalf("advice %+v, texts %q", r.advice, tt.Texts())
	}
	testutil.Snapshot(t, tt, "plan")
	r.planMode = planFlame
	tt.Frame()
	testutil.Snapshot(t, tt, "plan-flame")
	if err := tt.Click("SCAN things"); err != nil {
		t.Fatal(err)
	}
	if r.planSel == nil || !strings.HasPrefix(r.planSel.Op, "SCAN") {
		t.Fatalf("chosen %+v", r.planSel)
	}
	r.planMode = planRows
	tt.Frame()
	q.Run(RunExplainAnalyze)
	tt.Frame()
	if !testutil.HasTextContaining(tt, "explains a statement without running it") && !strings.Contains(q.messages[len(q.messages)-1].text, "without running") {
		t.Fatalf("messages %+v", q.messages)
	}
}

// MariaDB's Explain Analyze, ANALYZE FORMAT=JSON, reads as the statement
// it runs, a SELECT: its rows show as a plan all the same.
func TestIntegrationMariaDBExplainAnalyzePlan(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "mariadb")
	cfg := db.Config{ID: "maria", Name: "maria", Engine: db.MariaDB, Host: "127.0.0.1", Port: 13307, User: "root", Password: "dgopher", Database: "shop"}
	_, tt, _, q := openEditor(t, cfg, 1000, 700, "SELECT COUNT(*) FROM information_schema.TABLES")
	for _, mode := range []RunMode{RunExplain, RunExplainAnalyze} {
		q.Run(mode)
		testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running && q.current() != nil })
		if r := q.current(); r.err != "" || r.plan == nil || mode == RunExplainAnalyze && !r.plan.Analyzed {
			t.Fatalf("mode %d, %s: error %q, plan %+v", mode, r.sql, r.err, r.plan)
		}
	}
}

// TiDB's Explain and Explain Analyze, FORMAT='tidb_json', read as plans,
// the second as run.
func TestIntegrationTiDBExplainPlan(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "tidb")
	cfg := db.Config{ID: "tidb", Name: "tidb", Engine: db.TiDB, Host: "127.0.0.1", Port: 14000, User: "root", Password: "dgopher", Database: "shop"}
	_, tt, _, q := openEditor(t, cfg, 1000, 700, "SELECT COUNT(*) FROM information_schema.TABLES")
	for _, mode := range []RunMode{RunExplain, RunExplainAnalyze} {
		q.Run(mode)
		testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running && q.current() != nil })
		if r := q.current(); r.err != "" || r.plan == nil || (mode == RunExplainAnalyze) != r.plan.Analyzed {
			t.Fatalf("mode %d, %s: error %q, plan %+v", mode, r.sql, r.err, r.plan)
		}
	}
}

// CockroachDB's Explain and Explain Analyze, its text tree, read as
// plans, the second as run.
func TestIntegrationCockroachDBExplainPlan(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "cockroachdb")
	cfg := db.Config{ID: "crdb", Name: "crdb", Engine: db.CockroachDB, Host: "127.0.0.1", Port: 26257, User: "dgopher", Password: "dgopher", Database: "shop"}
	_, tt, _, q := openEditor(t, cfg, 1000, 700, "SELECT count(*) FROM information_schema.tables")
	for _, mode := range []RunMode{RunExplain, RunExplainAnalyze} {
		q.Run(mode)
		testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running && q.current() != nil })
		if r := q.current(); r.err != "" || r.plan == nil || (mode == RunExplainAnalyze) != r.plan.Analyzed {
			t.Fatalf("mode %d, %s: error %q, plan %+v", mode, r.sql, r.err, r.plan)
		}
	}
}

// An explain's steps run on the editor's session around its statement,
// audited as the app's own: Before, the statement, Read, whose rows are
// the result's, then After, which runs when the statement fails too. A
// temporary table, the session's own, shows them.
func TestExplainStepsRunAroundStatement(t *testing.T) {
	a := newFakeQueryHost(t)
	path := filepath.Join(t.TempDir(), "steps.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: path})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "")
	explain := &db.ExplainRun{
		Before: []string{"CREATE TEMP TABLE plan_steps (step TEXT)"},
		Read:   "SELECT group_concat(step, ',') AS steps FROM plan_steps",
		After:  []string{"DROP TABLE plan_steps"},
	}
	run := func(sql string, explain *db.ExplainRun) *result {
		t.Helper()
		q.execute(safety.Analyze(&cn.Config, []string{sql}), true, explain)
		testutil.WaitFor(t, tt, sql, func() bool { return !q.Running && q.current() != nil })
		return q.current()
	}
	if r := run("INSERT INTO plan_steps VALUES ('statement')", explain); r.err != "" || r.view == nil || len(r.view.Rows()) != 1 || db.Display(r.view.Rows()[0][0]) != "statement" {
		t.Fatalf("result: error %q, view %+v", r.err, r.view)
	}
	var steps []string
	for _, e := range a.Events {
		if e.Purpose == purposeExplain {
			steps = append(steps, e.Statement)
		}
	}
	if want := []string{explain.Before[0], explain.Read, explain.After[0]}; !slices.Equal(steps, want) {
		t.Fatalf("audited steps %q, want %q; events %+v", steps, want, a.Events)
	}
	if !slices.ContainsFunc(a.Events, func(e audit.Event) bool {
		return e.Statement == "INSERT INTO plan_steps VALUES ('statement')" && e.Purpose == ""
	}) {
		t.Fatalf("the statement not audited as the user's: %+v", a.Events)
	}
	// After ran: the table is gone, and goes again after a statement that
	// fails.
	if r := run("INSERT INTO missing VALUES (1)", explain); !strings.Contains(r.err, "missing") {
		t.Fatalf("a failing statement: %q", r.err)
	}
	if r := run("SELECT * FROM plan_steps", nil); !strings.Contains(r.err, "no such table") {
		t.Fatalf("After did not run: %q", r.err)
	}
}
