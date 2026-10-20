package query

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/safety"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	code "github.com/egoist/mygo/ui/editor"
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
	// The database counts characters, past the prefix the app put before;
	// the editor, bytes.
	wide := "EXPLAIN SELECT 'é日' FROM missing"
	skip := len("EXPLAIN ")
	at := strings.Index(wide, "missing") - skip
	for _, info := range []db.ErrorInfo{{Position: 26}, {Line: 1, Column: 26}, {Line: 1, Near: "missing"}} {
		if got := errorOffset(wide, 100, skip, info); got != 100+at {
			t.Errorf("%+v past characters of several bytes: %d, want %d", info, got, 100+at)
		}
	}
	// ClickHouse counts bytes.
	for _, info := range []db.ErrorInfo{{Position: 26 + 3, Bytes: true}, {Line: 1, Column: 26 + 3, Bytes: true}} {
		if got := errorOffset(wide, 100, skip, info); got != 100+at {
			t.Errorf("%+v in bytes: %d, want %d", info, got, 100+at)
		}
	}
}

// A failed statement shows its error with where it is, and Go to Error
// selects the spot in the editor.
func TestErrorPanelGoToError(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"}, 1100, 760, "SELECT 1;\n\nSELECT *\nFRM nowhere;")
	at := strings.Index(q.Editor.Text(), "FRM")
	testutil.SetCaret(tt, q.Editor, at)
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the error", func() bool { return !q.Running && tt.HasText("Go to Error") })
	testutil.Snapshot(t, tt, "error-panel")
	if err := tt.Click("Go to Error"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	sel := q.Editor.SelectedText()
	if sel != "nowhere" {
		t.Fatalf("selected %q", sel)
	}
}

// runWith runs an editor's text as a mode, with the caret at the first
// match of at, and waits for the run.
func runWith(t *testing.T, tt *ui.Tester, q *Tab, at string, mode RunMode) {
	t.Helper()
	testutil.SetCaret(tt, q.Editor, strings.Index(q.Editor.Text(), at))
	q.Run(mode)
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running })
}

// ⌘↵ runs the statement around the caret, which blank lines delimit.
func TestRunsStatementAtCaretAcrossBlankLines(t *testing.T) {
	a, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "SELECT 1 AS one\n\nSELECT 2 AS two\nUNION ALL SELECT 3")
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
	a, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "SELECT 1;\nSELECT * FROM missing;\nSELECT 3;")
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

// With errors skipped, the statements after a failed one run, and each
// shows its own outcome.
func TestScriptContinuesPastFailedWrite(t *testing.T) {
	a, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "SELECT * FROM missing;\nCREATE TABLE t (a int);\nINSERT INTO t VALUES (1);\nSELECT count(*) AS n FROM t;")
	a.Settings().ContinueOnError = true
	runWith(t, tt, q, "SELECT *", RunScript)
	if len(q.results) != 4 || q.results[0].err == "" {
		t.Fatalf("%d results", len(q.results))
	}
	for _, r := range q.results[1:] {
		if r.err != "" {
			t.Fatalf("%s: %s", r.sql, r.err)
		}
	}
	if q.results[2].affected != 1 {
		t.Fatalf("INSERT affected %d", q.results[2].affected)
	}
}

// Two statements on adjacent lines run in one step, each with its own
// result, as a script and as a selection.
func TestScriptTwoSelectsTwoResults(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "select 1 as a where 1 = 1;\nselect 2 as b where 2 = 2;")
	runWith(t, tt, q, "select 1", RunScript)
	if len(q.results) != 2 || len(q.results[0].view.Rows()) != 1 || len(q.results[1].view.Rows()) != 1 {
		t.Fatalf("script: %d results", len(q.results))
	}
	q.Editor.Select(0, len(q.Editor.Text()))
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running })
	if len(q.results) != 2 || q.results[1].sql != "select 2 as b where 2 = 2" {
		t.Fatalf("selection: %d results", len(q.results))
	}
}

// ⌘\ keeps the results shown; a plain run keeps only those pinned.
func TestNewTabAndPinnedResults(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "SELECT 1 AS a\n\nSELECT 2 AS b")
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
	_, tt, _, q := openEditor(t, db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:", StatementTimeout: 1}, 1000, 700, "SELECT count(*) FROM range(10000000000) a, range(1000) b")
	start := time.Now()
	runWith(t, tt, q, "SELECT", RunStatement)
	if time.Since(start) > 5*time.Second || len(q.results) != 1 || !strings.Contains(q.results[0].err, "statement timeout") {
		t.Fatalf("after %s: %+v", time.Since(start), q.results)
	}
	// A quick statement's rows are read past the timeout.
	q.Editor.SetText("-- connection: duck\n\nSELECT * FROM range(5000)")
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
	a, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:", Env: db.Production}, 1000, 700, "SELECT :n + 1 AS v")
	testutil.SetCaret(tt, q.Editor, strings.Index(q.Editor.Text(), "SELECT"))
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
	q.Editor.SetText("-- connection: lite\n\n${stmt}")
	testutil.SetCaret(tt, q.Editor, len(q.Editor.Text()))
	q.Run(RunStatement)
	a.dialogs.params.fields[0].value = "DELETE FROM t"
	a.dialogs.params.submit()
	tt.Frame()
	if a.Confirm == nil || !a.Confirm.TypeName {
		t.Fatal("a DELETE without WHERE from a variable ran without asking")
	}
	a.Confirm = nil
	// A value that adds a statement is refused outright.
	q.Editor.SetText("-- connection: lite\n\nSELECT * FROM ${t}")
	testutil.SetCaret(tt, q.Editor, len(q.Editor.Text()))
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
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "CREATE TABLE t (a INT);\nINSERT INTO t VALUES (1);")
	runWith(t, tt, q, "CREATE", RunScript)
	q.Editor.SetText("-- connection: lite\n\nDELETE FROM t WHERE a = 1")
	start := strings.Index(q.Editor.Text(), "DELETE")
	q.Editor.Select(start, len(q.Editor.Text()))
	tt.Frame()
	tt.Frame()
	q.Run(RunExplain)
	testutil.WaitFor(t, tt, "the plan", func() bool { return !q.Running })
	if r := q.results[len(q.results)-1]; !strings.HasPrefix(r.sql, "EXPLAIN") {
		t.Fatalf("ran %q", r.sql)
	}
	q.Editor.SetText("-- connection: lite\n\nSELECT count(*) FROM t")
	testutil.SetCaret(tt, q.Editor, len(q.Editor.Text()))
	q.Run(RunStatement)
	testutil.WaitFor(t, tt, "the count", func() bool { return !q.Running })
	if got := db.Display(q.results[0].view.Rows()[0][0]); got != "1" {
		t.Fatalf("the DELETE ran: %s rows", got)
	}
}

// A production script asks before each write: Run, Skip, Run All, and a
// destructive statement asks even after Run All.
func TestProductionScriptConfirmsEachWrite(t *testing.T) {
	a, tt, _, q := openEditor(t, db.Config{ID: "prod", Name: "prod", Engine: db.SQLite, Database: ":memory:", Env: db.Production}, 1000, 700, "CREATE TABLE t (a INT);\nINSERT INTO t VALUES (1);\nINSERT INTO t VALUES (2);\nINSERT INTO t VALUES (3);\nDELETE FROM t;\nSELECT count(*) FROM t;")
	testutil.SetCaret(tt, q.Editor, strings.Index(q.Editor.Text(), "CREATE"))
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
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "select 'a' as x\n\nselect 2 as y")
	if err := tt.RightClick("SQL editor"); err != nil {
		t.Fatal(err)
	}
	items := strings.Join(tt.Menu(), "|")
	for _, want := range []string{"Run", "Cut", "Copy", "Paste", "Format", "Copy Statement", "Save as Snippet…"} {
		if !strings.Contains(items, want) {
			t.Fatalf("menu %q lacks %q", items, want)
		}
	}
	if err := tt.ChooseMenuItem("Format", "To Upper Case"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// Without a selection, the statement at the caret.
	q.Editor.SetText("-- connection: lite\n\nselect 'a' as x\n\nselect 2 as y")
	testutil.SetCaret(tt, q.Editor, strings.Index(q.Editor.Text(), "2 as"))
	q.changeCase(true)
	if !strings.HasSuffix(q.Editor.Text(), "select 'a' as x\n\nSELECT 2 AS Y") {
		t.Fatalf("upper case of the statement: %q", q.Editor.Text())
	}
	// With one, the selection's lines.
	q.Editor.Select(strings.Index(q.Editor.Text(), "select 'a'"), strings.Index(q.Editor.Text(), " as x"))
	tt.Frame()
	tt.Frame()
	q.toggleComment()
	if !strings.Contains(q.Editor.Text(), "-- select 'a' as x") || strings.Contains(q.Editor.Text(), "-- SELECT 2") {
		t.Fatalf("comment: %q", q.Editor.Text())
	}
}

// Opening an editor on a short line of SQL does not offer completions as
// if it had just been typed.
func TestOpeningDoesNotComplete(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "SELECT 1 AS v")
	tt.Frame()
	if q.Editor.State.CompletionsOpen() {
		t.Fatalf("completions open on opening: %d items", len(q.ac.items))
	}
	// Typed a key at a time, as a word is.
	tt.Type(" ")
	tt.Type("FR")
	tt.Frame()
	if !q.Editor.State.CompletionsOpen() {
		t.Fatal("typing offers no completion")
	}
}

// A paste does not offer completions, however short; typing after it
// does.
func TestPasteDoesNotComplete(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "SELECT 1 AS v")
	tt.Frame()
	tt.SetClipboard(" FR")
	tt.Key(ui.Cmd, ui.KeyV)
	tt.Frame()
	if q.Editor.Text() != "SELECT 1 AS v FR" {
		t.Fatalf("after the paste: %q", q.Editor.Text())
	}
	if q.Editor.State.CompletionsOpen() {
		t.Fatalf("a paste offers completions: %d items", len(q.ac.items))
	}
	tt.Type("O")
	tt.Frame()
	if !q.Editor.State.CompletionsOpen() {
		t.Fatal("typing after a paste offers no completion")
	}
}

// The completion popup shows over the window rather than inside the
// editor: past the editor's bottom, over the results, it takes a click.
func TestCompletionPopupPastEditor(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "SELECT 1")
	q.editorPercent = 10
	tt.Frame()
	tt.Type(" ")
	tt.Type("C")
	tt.Frame()
	if !q.Editor.State.CompletionsOpen() || len(q.ac.items) < 5 {
		t.Fatalf("completions: open %v, %d items", q.Editor.State.CompletionsOpen(), len(q.ac.items))
	}
	ed, ok := tt.Find("SQL editor")
	if !ok {
		t.Fatal("no editor")
	}
	editorBottom := float32(ed.Y + ed.H)
	list, ok := tt.Find("Completions")
	if !ok {
		t.Fatal("no completions shown")
	}
	label := ""
	for _, it := range q.ac.items {
		// One in sight in the list, past the editor's bottom.
		if r, ok := tt.Find(it.label); ok && float32(r.Y) > editorBottom && r.Y+r.H <= list.Y+list.H {
			label = it.label
			break
		}
	}
	if label == "" {
		t.Fatalf("no suggestion past the editor's bottom at %v", editorBottom)
	}
	testutil.Snapshot(t, tt, "completion-past-editor")
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !strings.Contains(q.Editor.Text(), label) {
		t.Fatalf("a click on %q past the editor's bottom: text %q, popup open %v", label, q.Editor.Text(), q.Editor.State.CompletionsOpen())
	}
}

// A table completed in a query's FROM or JOIN takes a short alias, apart
// from the statement's others; elsewhere, or with one after it, none.
func TestCompletingTableAddsAlias(t *testing.T) {
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	setup, _ := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: file}, nil)
	setup.SQL.Exec("CREATE TABLE reflex_order (id INTEGER)")
	setup.Close()
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file}, 1000, 700, "")
	complete := func(before, after string) string {
		t.Helper()
		q.Editor.SetText(before + after)
		testutil.SetCaret(tt, q.Editor, len(before))
		testutil.WaitFor(t, tt, "the table's suggestion", func() bool {
			q.suggest(q.Editor.Caret(), true)
			return offers(q, "reflex_order")
		})
		tt.Key(0, ui.KeyEnter)
		tt.Frame()
		return q.Editor.Text()
	}
	for _, c := range []struct{ before, after, want string }{
		{"SELECT * FROM refl", "", "SELECT * FROM reflex_order ro"},
		{"SELECT * FROM reflex_order ro JOIN refl", "", "SELECT * FROM reflex_order ro JOIN reflex_order ro2"},
		{"SELECT * FROM refl", "\nWHERE ro.id = 1", "SELECT * FROM reflex_order ro\nWHERE ro.id = 1"},
		{"SELECT * FROM refl", " r WHERE r.id = 1", "SELECT * FROM reflex_order r WHERE r.id = 1"},
		{"INSERT INTO refl", "", "INSERT INTO reflex_order"},
		{"DELETE FROM refl", "", "DELETE FROM reflex_order"},
	} {
		if got := complete(c.before, c.after); got != c.want {
			t.Errorf("completing %q|%q: %q, want %q", c.before, c.after, got, c.want)
		}
	}
}

// Accepting completes at every caret with the word before it: a
// function with its parentheses, the caret between, unless they follow;
// a table in FROM with an alias; each as one step of undo.
func TestCompletionAcceptsAliasAndParens(t *testing.T) {
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	setup, _ := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: file}, nil)
	setup.SQL.Exec("CREATE TABLE reflex_order (id INTEGER)")
	setup.Close()
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file}, 1000, 700, "")
	e := q.Editor
	// complete types text, | marking the carets, asks at the newest, and
	// accepts label.
	complete := func(text, label string) string {
		t.Helper()
		parts := strings.Split(text, "|")
		e.SetText(strings.Join(parts, ""))
		var sels []code.Selection
		at := 0
		for _, p := range parts[:len(parts)-1] {
			at += len(p)
			sels = append(sels, code.Selection{Anchor: at, Head: at})
		}
		e.State.SetSelections(sels...)
		tt.Frame()
		testutil.WaitFor(t, tt, label, func() bool {
			q.suggest(e.Caret(), true)
			return offers(q, label)
		})
		tt.Key(0, ui.KeyEnter)
		tt.Frame()
		return e.Text()
	}
	for _, c := range []struct{ text, label, want string }{
		{"SELECT coales|", "coalesce", "SELECT coalesce()"},
		{"SELECT coales|(a, 1)", "coalesce", "SELECT coalesce(a, 1)"},
		{"SELECT 'é', coales|, coales|", "coalesce", "SELECT 'é', coalesce(), coalesce()"},
		{"SELECT * FROM refl| JOIN refl|", "reflex_order", "SELECT * FROM reflex_order ro JOIN reflex_order ro"},
	} {
		if got := complete(c.text, c.label); got != c.want {
			t.Errorf("completing %q: %q, want %q", c.text, got, c.want)
		}
	}
	if sels := e.State.Selections(); len(sels) != 2 || sels[1].Head != len(e.Text()) {
		t.Errorf("carets after the aliases: %+v", sels)
	}
	complete("SELECT 'é', coales|, coales|", "coalesce")
	want := []int{len("SELECT 'é', coalesce("), len("SELECT 'é', coalesce(), coalesce(")}
	if sels := e.State.Selections(); len(sels) != 2 || sels[0].Head != want[0] || sels[1].Head != want[1] {
		t.Errorf("carets in the parentheses: %+v, want at %v", sels, want)
	}
	e.State.Run(code.Undo)
	if e.Text() != "SELECT 'é', coales, coales" {
		t.Errorf("undo left %q", e.Text())
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

// The audit log keeps what was sent, apart from its parameters' values,
// and what was typed when binding changed it; Explain sends what the
// history keeps, EXPLAIN and all.
func TestEditorAuditTypedAndSent(t *testing.T) {
	a, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "SELECT :n + 1 AS v")
	last := func() audit.Event {
		for i := len(a.Events) - 1; i >= 0; i-- {
			if e := a.Events[i]; e.Kind == audit.KindStatement {
				return e
			}
		}
		t.Fatal("no statement recorded")
		return audit.Event{}
	}
	run := func(text, value string, mode RunMode) audit.Event {
		t.Helper()
		q.Editor.SetText("-- connection: lite\n\n" + text)
		testutil.SetCaret(tt, q.Editor, len(q.Editor.Text()))
		n := len(a.Events)
		q.Run(mode)
		if value != "" {
			a.dialogs.params.fields[0].value = value
			a.dialogs.params.submit()
		}
		testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running && len(a.Events) > n })
		return last()
	}
	if e := run("SELECT :n + 1 AS v", "41", RunStatement); e.Statement != "SELECT ? + 1 AS v" || e.Typed != "SELECT :n + 1 AS v" || e.Params != ":n = 41" || e.Origin != audit.OriginTyped {
		t.Errorf("bound: %+v", e)
	}
	if e := run("SELECT ${c} AS v", "2", RunStatement); e.Statement != "SELECT 2 AS v" || e.Typed != "SELECT ${c} AS v" {
		t.Errorf("substituted: %+v", e)
	}
	if e := run("SELECT 3 AS v", "", RunExplain); !strings.HasPrefix(e.Statement, "EXPLAIN") || e.Typed != "" {
		t.Errorf("explained: %+v", e)
	}
}

// The statement at the caret, and the one a ▶ runs with the caret put at
// its start, are found past characters of several bytes.
func TestRunStatementAtCaretMultibyte(t *testing.T) {
	text := "SELECT '日本' AS a;\n\nSELECT 'é' AS b;\n\nSELECT '🎉' AS c;"
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, text)
	last := func() string {
		testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running && len(q.results) > 0 })
		return q.results[len(q.results)-1].sql
	}
	testutil.SetCaret(tt, q.Editor, strings.Index(text, "é"))
	q.Run(RunStatement)
	if got := last(); got != "SELECT 'é' AS b" {
		t.Fatalf("ran %q", got)
	}
	if first, last := q.Editor.CurrentLines[0], q.Editor.CurrentLines[1]; first != 2 || last != 2 {
		t.Fatalf("the band on lines %d-%d", first, last)
	}
	q.runLine(4)
	if got := last(); got != "SELECT '🎉' AS c" {
		t.Fatalf("the ▶ of line 4 ran %q", got)
	}
	if q.Editor.Caret() != strings.Index(text, "SELECT '🎉'") {
		t.Fatalf("caret at %d", q.Editor.Caret())
	}
}

// A file with \r\n line endings opens unchanged, and saves with them:
// the editor's own \n lines are not a change, nor a conflict with the
// file on disk.
func TestSaveKeepsCRLF(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	path := filepath.Join(t.TempDir(), "crlf.sql")
	disk := "-- connection: lite\r\n\r\nSELECT 1;\r\n"
	if err := os.WriteFile(path, []byte(disk), 0o644); err != nil {
		t.Fatal(err)
	}
	q := New(a, cn, "", "crlf.sql", disk)
	q.Path, q.Saved = path, disk
	a.AddTab(q)
	tt.Frame()
	if q.Editor.Text() != strings.ReplaceAll(disk, "\r\n", "\n") || q.Unsaved() {
		t.Fatalf("opened as %q, unsaved %v", q.Editor.Text(), q.Unsaved())
	}
	q.Editor.Replace(len(q.Editor.Text()), len(q.Editor.Text()), "SELECT 2;\n")
	q.Flush(false)
	want := disk + "SELECT 2;\r\n"
	if got, _ := os.ReadFile(path); string(got) != want || q.DiskConflict != "" || q.Saved != want || q.Unsaved() {
		t.Fatalf("saved %q, conflict %q, unsaved %v", got, q.DiskConflict, q.Unsaved())
	}
}

// The completion key, as set in the settings, asks for completions with
// nothing typed.
func TestCompleteKey(t *testing.T) {
	defer keymap.Use(nil)
	for _, key := range []string{"", "alt-j"} {
		if key != "" {
			overrides, errs := keymap.Parse([]byte(`[{"context": "Editor", "bindings": {"ctrl-space": null, "` + key + `": "editor::ShowCompletions"}}]`))
			if errs != nil {
				t.Fatal(errs)
			}
			keymap.Use(overrides)
		}
		_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "SELECT 1 AS v ")
		testutil.WaitFor(t, tt, "the editor's focus", func() bool { return q.Editor.HasFocus() })
		ch, _ := keymap.Primary(keymap.Complete)
		tt.Key(ch.Mods, ch.Key)
		tt.Frame()
		if !q.Editor.State.CompletionsOpen() {
			t.Fatalf("%s: no completions", keymap.First(keymap.Complete))
		}
	}
}
