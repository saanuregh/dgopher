package query

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/safety"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// bigTable makes a DuckDB file holding big, a table of 10,000 rows, for
// a production editor on it.
func bigTable(t *testing.T) db.Config {
	t.Helper()
	file := filepath.Join(t.TempDir(), "big.duckdb")
	if err := db.CreateFile(context.Background(), db.DuckDB, file); err != nil {
		t.Fatal(err)
	}
	setup, err := db.Open(context.Background(), db.Config{Name: "s", Engine: db.DuckDB, Database: file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.SQL.Exec("CREATE TABLE big AS SELECT range AS id FROM range(10000)"); err != nil {
		t.Fatal(err)
	}
	setup.Close()
	return db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: file, Env: db.Production}
}

// largeRead and largeWrite scan the whole of big, whatever rows their
// filter keeps.
const (
	largeRead  = "SELECT * FROM big WHERE id % 7 = 3"
	largeWrite = "UPDATE big SET id = id WHERE id % 7 = 3"
)

// scanReason is the warning of DuckDB's scan of big's 10,000 rows past
// a limit of 1,000.
const scanReason = "It reads about 10,000 rows of big (SEQ_SCAN), more than the 1,000 a scan reads without asking (Settings)."

// runAtStart runs the editor's statement at its start, as ⌘↵ does.
func runAtStart(tt *ui.Tester, q *Tab) {
	testutil.SetCaret(tt, q.Editor, 0)
	q.Run(RunStatement)
}

// sentWithPurpose reports whether the audit log holds a statement the
// app sent for purpose.
func sentWithPurpose(a *fakeQueryHost, purpose string) bool {
	return slices.ContainsFunc(a.Events, func(e audit.Event) bool { return e.Kind == audit.KindStatement && e.Purpose == purpose })
}

// On production, a read whose plan scans more rows than the limit asks
// first, with the scan's reason and Show Plan, and without a script's
// Skip or Run All; Run then runs it.
func TestPlanCheckAsksOnLargeScan(t *testing.T) {
	a, tt, _, q := openEditor(t, bigTable(t), 1000, 700, largeRead)
	a.Settings().ScanLimit = 1000
	runAtStart(tt, q)
	testutil.WaitFor(t, tt, "the scan's question", func() bool { return a.Confirm != nil })
	r := a.Confirm
	if !slices.Equal(r.Reasons, []string{scanReason}) {
		t.Fatalf("reasons %q", r.Reasons)
	}
	if r.Title != "Run 1 statement on duck?" || r.Extra != "Show Plan" || r.OnSkip != nil || r.OnRunAll != nil || r.TypeName {
		t.Fatalf("title %q, extra %q, skip %v, run all %v, type name %v", r.Title, r.Extra, r.OnSkip != nil, r.OnRunAll != nil, r.TypeName)
	}
	if !sentWithPurpose(a, purposeScanCheck) {
		t.Fatalf("the plan's read is not audited: %+v", a.Events)
	}
	a.Confirm = nil
	r.OnConfirm()
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running })
	if len(q.results) != 1 || q.results[0].err != "" || len(q.results[0].view.Rows()) == 0 {
		t.Fatalf("results %+v", q.results)
	}
}

// Under the limit, a production read runs without asking, its plan read
// first.
func TestPlanCheckUnderLimitRunsUnasked(t *testing.T) {
	a, tt, _, q := openEditor(t, bigTable(t), 1000, 700, largeRead)
	runAtStart(tt, q)
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running && len(q.results) == 1 })
	if a.Confirm != nil {
		t.Fatalf("asked %q", a.Confirm.Reasons)
	}
	if q.results[0].err != "" || len(q.results[0].view.Rows()) == 0 {
		t.Fatalf("result %+v", q.results[0])
	}
	if !sentWithPurpose(a, purposeScanCheck) {
		t.Fatal("the plan was not read")
	}
}

// A plan that cannot be read lets the statement run, and Output says so,
// muted.
func TestPlanCheckFailureRunsWithNote(t *testing.T) {
	failing := func(context.Context, *db.Session, string, float64, func(string, time.Duration, error), ...any) ([]db.Scan, bool, error) {
		return nil, true, errors.New("EXPLAIN failed: permission denied for table big")
	}
	was := largeScans
	largeScans = failing
	t.Cleanup(func() { largeScans = was })
	a, tt, _, q := openEditor(t, bigTable(t), 1000, 700, largeRead)
	a.Settings().ScanLimit = 1000
	runAtStart(tt, q)
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running && len(q.results) == 1 })
	if a.Confirm != nil {
		t.Fatalf("asked %q", a.Confirm.Reasons)
	}
	i := slices.IndexFunc(q.messages, func(m message) bool {
		return m.text == "Ran without the scan check: EXPLAIN failed: permission denied for table big."
	})
	if i < 0 || q.messages[i].err {
		t.Fatalf("messages %+v", q.messages)
	}
}

// A production UPDATE that scans past the limit asks once, inside the
// run: the policy's reason and the scan's in one dialog, with the rows it
// changes counted.
func TestProductionSingleStatementAsksOnceInRun(t *testing.T) {
	a, tt, _, q := openEditor(t, bigTable(t), 1000, 700, largeWrite)
	a.Settings().ScanLimit = 1000
	runAtStart(tt, q)
	testutil.WaitFor(t, tt, "the question", func() bool { return a.Confirm != nil })
	r := a.Confirm
	if !slices.Contains(r.Reasons, "UPDATE on production") || !slices.Contains(r.Reasons, scanReason) {
		t.Fatalf("reasons %q", r.Reasons)
	}
	if want := []string{"It changes 1,429 rows of big, counted just now."}; !slices.Equal(r.Notes, want) {
		t.Fatalf("notes %q, want %q", r.Notes, want)
	}
	if r.Title != "Run 1 statement on duck?" || r.Extra != "Show Plan" || r.OnSkip != nil || r.OnRunAll != nil {
		t.Fatalf("title %q, extra %q, skip %v, run all %v", r.Title, r.Extra, r.OnSkip != nil, r.OnRunAll != nil)
	}
	if !sentWithPurpose(a, purposeCount) {
		t.Fatal("the count is not audited")
	}
	a.Confirm = nil
	r.OnConfirm()
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running })
	if a.Confirm != nil {
		t.Fatalf("asked again: %q", a.Confirm.Reasons)
	}
	if len(q.results) != 1 || q.results[0].err != "" || q.results[0].affected != 1429 {
		t.Fatalf("results %+v", q.results)
	}
}

// A production UPDATE without a large scan asks as before, with the
// count of its rows and Preview Changes; Cancel runs nothing and says
// nothing of a stop.
func TestCountNoteInConfirm(t *testing.T) {
	a, tt, _, q := openEditor(t, bigTable(t), 1000, 700, "UPDATE big SET id = id + 1 WHERE id < 10")
	runAtStart(tt, q)
	testutil.WaitFor(t, tt, "the question", func() bool { return a.Confirm != nil })
	r := a.Confirm
	if !slices.Equal(r.Reasons, []string{"UPDATE on production"}) {
		t.Fatalf("reasons %q", r.Reasons)
	}
	if want := []string{"It changes 10 rows of big, counted just now."}; !slices.Equal(r.Notes, want) {
		t.Fatalf("notes %q, want %q", r.Notes, want)
	}
	if r.Extra != "Preview Changes" || r.OnSkip != nil || r.OnRunAll != nil {
		t.Fatalf("extra %q, skip %v, run all %v", r.Extra, r.OnSkip != nil, r.OnRunAll != nil)
	}
	if r.Preview != "UPDATE big SET id = id + 1 WHERE id < 10;" {
		t.Fatalf("preview %q", r.Preview)
	}
	a.Confirm = nil
	r.OnCancel()
	testutil.WaitFor(t, tt, "the run's end", func() bool { return !q.Running })
	if len(q.results) != 0 || slices.ContainsFunc(q.messages, func(m message) bool { return strings.HasPrefix(m.text, "Stopped") }) {
		t.Fatalf("results %d, messages %+v", len(q.results), q.messages)
	}
	q.Editor.SetText("SELECT count(*) FROM big WHERE id < 10")
	runAtStart(tt, q)
	testutil.WaitFor(t, tt, "the count", func() bool { return !q.Running && len(q.results) == 1 })
	if got := db.Display(q.results[0].view.Rows()[0][0]); got != "10" {
		t.Fatalf("the UPDATE ran: %s rows under 10", got)
	}
}

// Show Plan stops the run and explains the statement instead: a plan
// shows, and the statement never ran.
func TestShowPlanOpensExplain(t *testing.T) {
	const stmt = largeRead
	a, tt, _, q := openEditor(t, bigTable(t), 1000, 700, stmt)
	a.Settings().ScanLimit = 1000
	runAtStart(tt, q)
	testutil.WaitFor(t, tt, "the scan's question", func() bool { return a.Confirm != nil })
	r := a.Confirm
	a.Confirm = nil
	r.Open, r.Answered = false, true
	r.OnExtra()
	testutil.WaitFor(t, tt, "the plan", func() bool {
		return !q.Running && slices.ContainsFunc(q.results, func(r *result) bool { return r.plan != nil })
	})
	if a.Confirm != nil {
		t.Fatalf("asked again: %q", a.Confirm.Reasons)
	}
	if slices.ContainsFunc(a.Events, func(e audit.Event) bool {
		return e.Kind == audit.KindStatement && e.Statement == stmt && e.Purpose == ""
	}) {
		t.Fatal("the statement ran")
	}
	if slices.ContainsFunc(q.messages, func(m message) bool { return strings.HasPrefix(m.text, "Stopped") }) {
		t.Fatalf("messages %+v", q.messages)
	}
}

// A parameter's value goes with the plan's read: the scan is checked, and
// asks, with nothing said of a check skipped.
func TestPlanCheckTakesParameters(t *testing.T) {
	a, tt, _, q := openEditor(t, bigTable(t), 1000, 700, "SELECT * FROM big WHERE id % 7 = :n")
	a.Settings().ScanLimit = 1000
	runAtStart(tt, q)
	if a.dialogs.params == nil || len(a.dialogs.params.fields) != 1 {
		t.Fatalf("no parameter dialog: %+v", a.dialogs.params)
	}
	a.dialogs.params.fields[0].value = "3"
	a.dialogs.params.submit()
	testutil.WaitFor(t, tt, "the scan's question", func() bool { return a.Confirm != nil })
	if !slices.Equal(a.Confirm.Reasons, []string{scanReason}) {
		t.Fatalf("reasons %q", a.Confirm.Reasons)
	}
	if !strings.HasSuffix(a.Confirm.Preview, "\n-- :n = 3") {
		t.Fatalf("preview %q", a.Confirm.Preview)
	}
	if slices.ContainsFunc(q.messages, func(m message) bool { return strings.HasPrefix(m.text, "Ran without the scan check") }) {
		t.Fatalf("messages %+v", q.messages)
	}
	r := a.Confirm
	a.Confirm = nil
	r.OnConfirm()
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running })
	if len(q.results) != 1 || q.results[0].err != "" || len(q.results[0].view.Rows()) == 0 {
		t.Fatalf("results %+v", q.results)
	}
}

// Cancel at a production question leaves the editor as it was before the
// run: its results kept and shown, undimmed, with nothing noted, numbered
// or notified.
func TestProductionCancelKeepsResults(t *testing.T) {
	a, tt, _, q := openEditor(t, bigTable(t), 1000, 700, "SELECT * FROM big WHERE id < 5")
	runAtStart(tt, q)
	testutil.WaitFor(t, tt, "the read", func() bool { return !q.Running && len(q.results) == 1 })
	results, shown, messages := slices.Clone(q.results), q.resultIdx, len(q.messages)
	runs, notified := q.runsShown, len(a.Notified)
	q.Editor.SetText("UPDATE big SET id = id + 1 WHERE id < 10")
	runAtStart(tt, q)
	testutil.WaitFor(t, tt, "the question", func() bool { return a.Confirm != nil })
	if !slices.Equal(q.results, results) || q.stale != nil {
		t.Fatalf("while asking: results %d, stale %d", len(q.results), len(q.stale))
	}
	r := a.Confirm
	a.Confirm = nil
	r.OnCancel()
	testutil.WaitFor(t, tt, "the cancel", func() bool { return !q.Running })
	tt.Frame()
	if !slices.Equal(q.results, results) || q.resultIdx != shown || q.stale != nil {
		t.Fatalf("results %d (shown %d), stale %d", len(q.results), q.resultIdx, len(q.stale))
	}
	if len(q.messages) != messages || q.runsShown != runs || len(a.Notified) != notified {
		t.Fatalf("messages %+v, runs shown %d, notified %q", q.messages[messages:], q.runsShown-runs, a.Notified)
	}
	if got := len(q.results[0].view.Rows()); got != 5 {
		t.Fatalf("the read's result shows %d rows", got)
	}
}

// The plan check reads the plans of what reads or changes rows only: a
// SHOW, a CALL or a VACUUM has none to read, and is not explained.
func TestExplainableStatements(t *testing.T) {
	cfg := db.Config{Engine: db.Postgres}
	for sql, want := range map[string]bool{
		"SELECT 1": true, "WITH x AS (SELECT 1) SELECT * FROM x": true, "UPDATE t SET a = 1": true,
		"DELETE FROM t": true, "INSERT INTO t SELECT * FROM u": true,
		"SHOW search_path": false, "CALL cleanup()": false, "VACUUM t": false, "CREATE TABLE t (a int)": false,
	} {
		if got := explainable(safety.Analyze(&cfg, []string{sql})[0]); got != want {
			t.Errorf("%s: explainable %v, want %v", sql, got, want)
		}
	}
}
