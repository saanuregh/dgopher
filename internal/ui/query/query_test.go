package query

import (
	"strings"
	"testing"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/safety"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

func TestErrorOffset(t *testing.T) {
	stmt := "SELECT *\nFROM missing"
	cases := []struct {
		info db.ErrorInfo
		want int
	}{
		{db.ErrorInfo{Position: 15}, 100 + 14},
		{db.ErrorInfo{Line: 2, Column: 6}, 100 + 9 + 5},
		{db.ErrorInfo{Line: 2, Near: "missing"}, 100 + 9 + 5},
		{db.ErrorInfo{Line: 2}, 100 + 9},
		{db.ErrorInfo{Line: 9}, -1},
		{db.ErrorInfo{}, -1},
	}
	for _, tc := range cases {
		if got := errorOffset(stmt, 100, 0, tc.info); got != tc.want {
			t.Errorf("%+v: %d, want %d", tc.info, got, tc.want)
		}
	}
	if errorOffset(stmt, -1, 0, db.ErrorInfo{Position: 3}) != -1 {
		t.Error("an offset without a start")
	}
}

// A failed statement shows its error with where it is, and Go to Error
// selects the spot in the editor.
func TestErrorPanelGoToError(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1100, 760)
	q := newEditor(t, a, tt, cn, "SELECT 1;\n\nSELECT *\nFRM nowhere;")
	at := strings.Index(q.Editor.Text, "FRM")
	testutil.SetCaret(tt, &q.Editor, at)
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the error", func() bool { return !q.Running && tt.HasText("Go to Error") })
	testutil.Snapshot(t, tt, "error-panel")
	if err := tt.Click("Go to Error"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	sel := q.Editor.Selection()
	if sel != "nowhere" {
		t.Fatalf("selected %q", sel)
	}
}

// runWith runs an editor's text as a mode, with the caret at the first
// match of at, and waits for the run.
func runWith(t *testing.T, tt *ui.Tester, q *Tab, at string, mode RunMode) {
	t.Helper()
	testutil.SetCaret(tt, &q.Editor, strings.Index(q.Editor.Text, at))
	q.Run(mode)
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running })
}

// ⌘↵ runs the statement around the caret, which blank lines delimit.
func TestRunsStatementAtCaretAcrossBlankLines(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "SELECT 1 AS one\n\nSELECT 2 AS two\nUNION ALL SELECT 3")
	runWith(t, tt, q, "UNION", RunStatement)
	if len(q.results) != 1 || q.results[0].sql != "SELECT 2 AS two\nUNION ALL SELECT 3" || len(q.results[0].view.Rows()) != 2 {
		t.Fatalf("ran %+v", q.results)
	}
	// With ';' only, the same text is one statement.
	a.Settings().SemicolonOnly = true
	q.stmtText = ""
	runWith(t, tt, q, "UNION", RunStatement)
	if len(q.results) != 1 || !strings.HasPrefix(q.results[0].sql, "SELECT 1") {
		t.Fatalf("with ';' only: %+v", q.results[0].sql)
	}
}

func TestScriptStopsOrContinues(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "SELECT 1;\nSELECT * FROM missing;\nSELECT 3;")
	runWith(t, tt, q, "SELECT 1", RunScript)
	if len(q.results) != 2 || q.results[1].err == "" || q.current() != q.results[1] {
		t.Fatalf("stop: %d results, front %v", len(q.results), q.current())
	}
	a.Settings().ContinueOnError = true
	runWith(t, tt, q, "SELECT 1", RunScript)
	if len(q.results) != 3 || q.results[2].err != "" {
		t.Fatalf("continue: %d results", len(q.results))
	}
}

// Two statements on adjacent lines run in one step, each with its own
// result, as a script and as a selection.
func TestScriptTwoSelectsTwoResults(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "select 1 as a where 1 = 1;\nselect 2 as b where 2 = 2;")
	runWith(t, tt, q, "select 1", RunScript)
	if len(q.results) != 2 || len(q.results[0].view.Rows()) != 1 || len(q.results[1].view.Rows()) != 1 {
		t.Fatalf("script: %d results", len(q.results))
	}
	q.Editor.SelStart, q.Editor.SelEnd = 0, len([]rune(q.Editor.Text))
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running })
	if len(q.results) != 2 || q.results[1].sql != "select 2 as b where 2 = 2" {
		t.Fatalf("selection: %d results", len(q.results))
	}
}

// ⌘\ keeps the results shown; a plain run keeps only those pinned.
func TestNewTabAndPinnedResults(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "SELECT 1 AS a\n\nSELECT 2 AS b")
	runWith(t, tt, q, "1 AS", RunStatement)
	runWith(t, tt, q, "2 AS", RunNewTab)
	if len(q.results) != 2 || q.current() != q.results[1] {
		t.Fatalf("new tab: %d results", len(q.results))
	}
	q.results[0].pinned = true
	runWith(t, tt, q, "2 AS", RunStatement)
	if len(q.results) != 2 || !q.results[0].pinned || q.results[1].view.Columns()[0].Name != "b" {
		t.Fatalf("pinned: %d results", len(q.results))
	}
	q.closeResult(q.results[0])
	if len(q.results) != 1 {
		t.Fatal("close result")
	}
}

func TestStatementTimeout(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:", StatementTimeout: 1})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "SELECT count(*) FROM range(10000000000) a, range(1000) b")
	start := time.Now()
	runWith(t, tt, q, "SELECT", RunStatement)
	if time.Since(start) > 5*time.Second || len(q.results) != 1 || !strings.Contains(q.results[0].err, "statement timeout") {
		t.Fatalf("after %s: %+v", time.Since(start), q.results)
	}
	// A quick statement's rows are read past the timeout.
	q.Editor.Text = "-- connection: duck\n\nSELECT * FROM range(5000)"
	runWith(t, tt, q, "SELECT", RunStatement)
	time.Sleep(1200 * time.Millisecond)
	q.results[0].view.FetchAll()
	testutil.WaitFor(t, tt, "all rows", func() bool { return q.results[0].view.Done() })
	if len(q.results[0].view.Rows()) != 5000 || q.results[0].err != "" {
		t.Fatalf("rows %d, err %q", len(q.results[0].view.Rows()), q.results[0].err)
	}
}

// Values ask before a run, are bound, and a ${var} that hides a write is
// reviewed as one.
func TestParametersBoundAndReviewed(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:", Env: db.Production})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "SELECT :n + 1 AS v")
	testutil.SetCaret(tt, &q.Editor, strings.Index(q.Editor.Text, "SELECT"))
	q.Run(RunStatement)
	if a.dialogs.params == nil || len(a.dialogs.params.fields) != 1 {
		t.Fatalf("no parameter dialog: %+v", a.dialogs.params)
	}
	tt.Frame()
	testutil.Snapshot(t, tt, "parameters")
	a.dialogs.params.fields[0].value = "41"
	a.dialogs.params.submit()
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running && len(q.results) == 1 })
	if got := db.Display(q.results[0].view.Rows()[0][0]); got != "42" {
		t.Fatalf("got %s", got)
	}
	// The value is remembered, and a ${var} is reviewed after substitution.
	q.Editor.Text = "-- connection: lite\n\n${stmt}"
	testutil.SetCaret(tt, &q.Editor, len([]rune(q.Editor.Text)))
	q.Run(RunStatement)
	a.dialogs.params.fields[0].value = "DELETE FROM t"
	a.dialogs.params.submit()
	tt.Frame()
	if a.Confirm == nil || !a.Confirm.TypeName {
		t.Fatal("a DELETE without WHERE from a variable ran without asking")
	}
	a.Confirm = nil
	// A value that adds a statement is refused outright.
	q.Editor.Text = "-- connection: lite\n\nSELECT * FROM ${t}"
	testutil.SetCaret(tt, &q.Editor, len([]rune(q.Editor.Text)))
	q.Run(RunStatement)
	a.dialogs.params.fields[0].value = "x; DROP TABLE y"
	a.dialogs.params.submit()
	tt.Frame()
	if q.Running || a.Confirm != nil || len(a.Errors) == 0 || !strings.Contains(strings.Join(a.Errors, "\n"), "adds a statement") {
		t.Fatalf("a second statement from a variable: running %v, errors %q", q.Running, a.Errors)
	}
}

// Explain on a selection explains it, never runs it.
func TestExplainSelectionNeverRuns(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "CREATE TABLE t (a INT);\nINSERT INTO t VALUES (1);")
	runWith(t, tt, q, "CREATE", RunScript)
	q.Editor.Text = "-- connection: lite\n\nDELETE FROM t WHERE a = 1"
	start := strings.Index(q.Editor.Text, "DELETE")
	q.Editor.PendingSel = &[2]int{start, len([]rune(q.Editor.Text))}
	tt.Frame()
	tt.Frame()
	q.Run(RunExplain)
	testutil.WaitFor(t, tt, "the plan", func() bool { return !q.Running })
	if r := q.results[len(q.results)-1]; !strings.HasPrefix(r.sql, "EXPLAIN") {
		t.Fatalf("ran %q", r.sql)
	}
	q.Editor.Text = "-- connection: lite\n\nSELECT count(*) FROM t"
	testutil.SetCaret(tt, &q.Editor, len([]rune(q.Editor.Text)))
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the count", func() bool { return !q.Running })
	if got := db.Display(q.results[0].view.Rows()[0][0]); got != "1" {
		t.Fatalf("the DELETE ran: %s rows", got)
	}
}

// A production script asks before each write: Run, Skip, Run All, and a
// destructive statement asks even after Run All.
func TestProductionScriptConfirmsEachWrite(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "prod", Name: "prod", Engine: db.SQLite, Database: ":memory:", Env: db.Production})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "CREATE TABLE t (a INT);\nINSERT INTO t VALUES (1);\nINSERT INTO t VALUES (2);\nINSERT INTO t VALUES (3);\nDELETE FROM t;\nSELECT count(*) FROM t;")
	testutil.SetCaret(tt, &q.Editor, strings.Index(q.Editor.Text, "CREATE"))
	q.Run(RunScript)
	answer := func(want string, choose func(r *widgets.ConfirmRequest)) {
		t.Helper()
		testutil.WaitFor(t, tt, want, func() bool { return a.Confirm != nil && strings.Contains(a.Confirm.Preview, want) })
		r := a.Confirm
		r.Open, r.Answered = false, true
		a.Confirm = nil
		choose(r)
	}
	answer("CREATE", func(r *widgets.ConfirmRequest) { r.OnConfirm() })
	answer("VALUES (1)", func(r *widgets.ConfirmRequest) { r.OnSkip() })
	answer("VALUES (2)", func(r *widgets.ConfirmRequest) {
		if r.OnRunAll == nil {
			t.Fatal("no Run All")
		}
		r.OnRunAll()
	})
	// VALUES (3) runs without asking; the DELETE asks, by name, with no Run All.
	answer("DELETE", func(r *widgets.ConfirmRequest) {
		if !r.TypeName || r.OnRunAll != nil {
			t.Fatalf("a destructive statement after Run All: typeName %v, run all %v", r.TypeName, r.OnRunAll != nil)
		}
		r.OnSkip()
	})
	testutil.WaitFor(t, tt, "the end", func() bool { return !q.Running })
	last := q.results[len(q.results)-1]
	if got := db.Display(last.view.Rows()[0][0]); got != "2" {
		t.Fatalf("rows left %s (want 2: the skipped insert and the skipped delete did not run)", got)
	}
	q.endTx(false)
	// The rollback is audited into the project, which must be done before
	// its directory is removed.
	testutil.WaitFor(t, tt, "the rollback", func() bool { return !q.Running })
}

// The editor's menu acts on the statement at the caret, or the selection.
func TestEditorMenuActsOnSelection(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "select 'a' as x\n\nselect 2 as y")
	if err := tt.RightClick("SQL editor"); err != nil {
		t.Fatal(err)
	}
	items := strings.Join(tt.Menu(), "|")
	for _, want := range []string{"Execute", "Cut", "Copy", "Paste", "Format", "Copy Statement", "Save as Snippet…"} {
		if !strings.Contains(items, want) {
			t.Fatalf("menu %q lacks %q", items, want)
		}
	}
	if err := tt.ChooseMenuItem("Format", "To Upper Case"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// Without a selection, the statement at the caret.
	q.Editor.Text = "-- connection: lite\n\nselect 'a' as x\n\nselect 2 as y"
	testutil.SetCaret(tt, &q.Editor, strings.Index(q.Editor.Text, "2 as"))
	q.changeCase(true)
	if !strings.HasSuffix(q.Editor.Text, "select 'a' as x\n\nSELECT 2 AS Y") {
		t.Fatalf("upper case of the statement: %q", q.Editor.Text)
	}
	// With one, the selection's lines.
	q.Editor.PendingSel = &[2]int{strings.Index(q.Editor.Text, "select 'a'"), strings.Index(q.Editor.Text, " as x")}
	tt.Frame()
	tt.Frame()
	q.toggleComment()
	if !strings.Contains(q.Editor.Text, "-- select 'a' as x") || strings.Contains(q.Editor.Text, "-- SELECT 2") {
		t.Fatalf("comment: %q", q.Editor.Text)
	}
}

// Opening an editor on a short line of SQL does not offer completions as
// if it had just been typed.
func TestOpeningDoesNotComplete(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "SELECT 1 AS v")
	tt.Frame()
	if q.ac.open {
		t.Fatalf("completions open on opening: %d items", len(q.ac.items))
	}
	tt.Type(" FR")
	tt.Frame()
	if !q.ac.open {
		t.Fatal("typing offers no completion")
	}
}

// A SELECT that writes shows its rows, unless it writes them INTO a table
// or file: that one returns none, and runs for its count of rows.
func TestWantsRowsForWritingSelects(t *testing.T) {
	cases := []struct {
		engine db.Engine
		sql    string
		want   bool
	}{
		{db.Postgres, "SELECT pg_terminate_backend(1)", true},
		{db.Postgres, "SELECT nextval('s')", true},
		{db.Postgres, "SELECT * INTO copy FROM t", false},
		{db.Postgres, "SELECT 1 INTO OUTFILE '/tmp/x'", false},
		{db.Postgres, "SELECT 'into' AS word, setval('s', 1)", true},
		{db.Postgres, "INSERT INTO t VALUES (1)", false},
		{db.Postgres, "INSERT INTO t VALUES (1) RETURNING id", true},
		// The statement is lexed in its engine's dialect, so INTO inside a
		// string is not a keyword.
		{db.MySQL, `SELECT SLEEP(1), 'a\' into'`, true},
		{db.Postgres, "SELECT nextval('s'), $$ into $$", true},
	}
	for _, c := range cases {
		cfg := &db.Config{Engine: c.engine}
		s := safety.Analyze(cfg, []string{c.sql})[0]
		if got := wantsRows(s, safety.Dialect(c.engine)); got != c.want {
			t.Errorf("%s: wantsRows %v, want %v (%+v)", c.sql, got, c.want, s.Analysis)
		}
	}
}
