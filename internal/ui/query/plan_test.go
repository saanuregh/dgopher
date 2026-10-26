package query

import (
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/testutil/optionalserver"
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
