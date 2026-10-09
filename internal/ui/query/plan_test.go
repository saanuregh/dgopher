package query

import (
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// Explain shows the plan's steps with the advice about them, as a flame
// graph too; Explain Analyze refuses an engine that explains without
// running, and a statement that writes.
func TestPlanView(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1200, 800)
	q := newEditor(t, a, tt, cn, "SELECT * FROM things WHERE kind = 'a' ORDER BY name")
	cn.DB.SQL.Exec(`CREATE TABLE things (name TEXT, kind TEXT)`)
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
