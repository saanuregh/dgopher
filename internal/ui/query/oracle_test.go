package query

import (
	"slices"
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/testutil/optionalserver"
)

var oracleEditorConfig = db.Config{ID: "ora", Name: "ora", Engine: db.Oracle, Host: "127.0.0.1", Port: 11521, User: "dgopher", Password: "dgopher", Database: "FREEPDB1"}

// Oracle finds an unquoted name in upper case, a quoted one as written.
func TestOracleMatchName(t *testing.T) {
	names := []string{"ORDERS", "Orders", "orders", "LINES"}
	for _, c := range []struct {
		name   string
		quoted bool
		want   int
	}{{"orders", false, 0}, {"Orders", false, 0}, {"orders", true, 2}, {"Orders", true, 1}, {"lines", false, 3}, {"lines", true, -1}} {
		if got := matchName(names, c.name, c.quoted, db.Oracle); got != c.want {
			t.Errorf("%s quoted=%v: %d, want %d", c.name, c.quoted, got, c.want)
		}
	}
}

// Oracle explains, and measures a statement that reads by running it.
func TestOracleExplains(t *testing.T) {
	h := newFakeQueryHost(t)
	cn := h.AddConn(db.Config{ID: "x", Name: "x", Engine: db.Oracle, Host: "h"})
	q := New(h, cn, "", "query", "SELECT 1 FROM dual")
	for _, mode := range []RunMode{RunExplain, RunExplainAnalyze} {
		if why := q.explainRefusal(mode); why != "" {
			t.Fatalf("mode %d refused: %q", mode, why)
		}
	}
}

// Explain gets a plan on a read-only connection, through EXPLAIN PLAN,
// which a read-only session allows, and leaves no transaction open;
// Explain of an UPDATE is refused there, as the UPDATE is.
func TestIntegrationOracleExplainReadOnly(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "oracle")
	cfg := oracleEditorConfig
	cfg.ReadOnly = true
	_, tt, _, q := openEditor(t, cfg, 1000, 700, "SELECT * FROM dual WHERE dummy = 'X'")
	q.Run(RunExplain)
	testutil.WaitFor(t, tt, "the plan", func() bool { return !q.Running && q.current() != nil })
	if r := q.current(); r.err != "" || r.plan == nil || r.plan.Root.Op != "SELECT STATEMENT" || q.OpenTx() {
		t.Fatalf("explain: error %q, plan %+v, transaction open %v", r.err, r.plan, q.OpenTx())
	}
	q.Editor.SetText("UPDATE dual SET dummy = 'Y'")
	q.Run(RunExplain)
	tt.Frame()
	if q.Running || !strings.Contains(q.messages[len(q.messages)-1].text, "read-only") {
		t.Fatalf("explain of an UPDATE on a read-only connection: running %v, messages %+v", q.Running, q.messages)
	}
}

// Explain Analyze runs the statement to its end, as SYSTEM, which reads
// the V$ views its run's statistics are in.
func TestIntegrationOracleExplainAnalyze(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "oracle")
	cfg := oracleEditorConfig
	cfg.User = "system"
	_, tt, _, q := openEditor(t, cfg, 1000, 700, "SELECT LEVEL FROM dual CONNECT BY LEVEL <= 3000")
	q.Run(RunExplainAnalyze)
	testutil.WaitFor(t, tt, "the plan", func() bool { return !q.Running && q.current() != nil })
	if r := q.current(); r.err != "" || r.plan == nil || !r.plan.Analyzed || r.plan.Root.ActualRows != 3000 {
		t.Fatalf("explain analyze: error %q, plan %+v", r.err, r.plan)
	}
}

// A bind named by a number, as Oracle's :1, is asked for as a name is,
// and runs with its value, as text and as a number.
func TestIntegrationOracleNumberedBinds(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "oracle")
	a, tt, _, q := openEditor(t, oracleEditorConfig, 1000, 700, "SELECT :1 || 'x' AS a, :2 + 1 AS b FROM dual")
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the question", func() bool { return a.dialogs.params != nil })
	f := a.dialogs.params
	if len(f.fields) != 2 || f.fields[0].key != ":1" || f.fields[1].key != ":2" {
		t.Fatalf("asked %+v", f.fields)
	}
	f.fields[0].value, f.fields[1].value = "ab", "41"
	f.submit()
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running && q.current() != nil })
	r := q.current()
	if r.err != "" || r.view == nil || len(r.view.Rows()) != 1 || db.Display(r.view.Rows()[0][0]) != "abx" || db.Display(r.view.Rows()[0][1]) != "42" {
		t.Fatalf("error %q, rows %v", r.err, r.view)
	}
}

// What DBMS_OUTPUT prints shows in the editor's messages.
func TestIntegrationOracleMessages(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "oracle")
	_, tt, _, q := openEditor(t, oracleEditorConfig, 1000, 700, "BEGIN DBMS_OUTPUT.PUT_LINE('hello from Oracle'); END;")
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running && len(q.results) > 0 })
	if !slices.ContainsFunc(q.messages, func(m message) bool { return m.text == "hello from Oracle" }) {
		t.Fatalf("messages %+v", q.messages)
	}
}
