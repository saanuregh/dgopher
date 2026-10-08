package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/safety"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// txApp is an app with a SQLite file in manual commit, and an editor that
// has inserted a row in an open transaction.
func txApp(t *testing.T, idle int) (*App, *ui.Tester, *query.Tab, string) {
	t.Helper()
	a := newTestApp(t)
	file := filepath.Join(t.TempDir(), "tx.sqlite")
	os.WriteFile(file, nil, 0o600)
	setup, err := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	setup.SQL.Exec("CREATE TABLE t (a INT)")
	setup.Close()
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file, Commit: db.CommitManual, IdleTxTimeout: idle})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newAppEditor(t, a, tt, cn, "INSERT INTO t VALUES (1)")
	runAt(t, tt, q, "INSERT", query.RunStatement)
	if !q.OpenTx() {
		t.Fatal("no transaction")
	}
	return a, tt, q, file
}

func rowsIn(t *testing.T, file string) string {
	t.Helper()
	d, err := db.Open(context.Background(), db.Config{Name: "r", Engine: db.SQLite, Database: file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var n string
	d.SQL.QueryRow("SELECT count(*) FROM t").Scan(&n)
	return n
}

func TestCloseAsksCommitOrRollback(t *testing.T) {
	for _, commit := range []bool{true, false} {
		a, tt, q, file := txApp(t, 0)
		a.closeTab(a.active)
		if a.closing == nil || len(a.closing.txs) != 1 || a.closing.txs[0] != q {
			t.Fatalf("closing %+v", a.closing)
		}
		tt.Frame()
		texts := strings.Join(tt.Texts(), "|")
		if !strings.HasSuffix(texts, "Cancel|Roll Back|Commit") {
			t.Fatalf("the dialog's buttons: %s", texts)
		}
		// As the dialog's buttons do (the editor's bar has a Commit too).
		r := a.closing
		r.open = false
		if commit {
			a.commitThen(r.txs, r.onClose)
		} else {
			r.onClose()
		}
		testutil.WaitFor(t, tt, "the tab to close", func() bool { return len(a.tabs) == 0 })
		want := "0"
		if commit {
			want = "1"
		}
		testutil.WaitFor(t, tt, "the outcome", func() bool { return rowsIn(t, file) == want })
	}
}

func TestQuitAsksAboutOpenTransactions(t *testing.T) {
	a, tt, _, file := txApp(t, 0)
	quit := false
	if a.requestQuit(func() { quit = true }) || a.closing == nil {
		t.Fatal("quit without asking")
	}
	tt.Frame()
	testutil.Snapshot(t, tt, "quit-open-transaction")
	r := a.closing
	r.open = false
	a.commitThen(r.txs, r.onClose)
	testutil.WaitFor(t, tt, "the quit", func() bool { return quit })
	if !a.requestQuit(func() {}) {
		t.Fatal("asked again after agreeing")
	}
	if rowsIn(t, file) != "1" {
		t.Fatal("not committed")
	}
}

func TestIdleTransactionRollsBack(t *testing.T) {
	a, tt, q, file := txApp(t, 13) // warned three seconds after its last use
	testutil.WaitFor(t, tt, "the warning", func() bool { return a.idleWarn != nil })
	testutil.Snapshot(t, tt, "idle-transaction")
	a.idleWarn.deadline = a.now // as the countdown ends
	testutil.WaitFor(t, tt, "the rollback", func() bool { return !q.OpenTx() })
	if rowsIn(t, file) != "0" {
		t.Fatal("the idle transaction was not rolled back")
	}
	// Keeping it open restarts the idle time.
	runAt(t, tt, q, "INSERT", query.RunStatement)
	testutil.WaitFor(t, tt, "the warning", func() bool { return a.idleWarn != nil })
	if err := tt.Click("Keep Open"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if a.idleWarn != nil || !q.OpenTx() {
		t.Fatal("keep it open")
	}
	q.FinishTx(false, func(error) {})

	testutil.WaitFor(t, tt, "the rollback", func() bool { return !q.OpenTx() && !q.Running })
}

// A tab whose statement runs inside the transaction is not idle.
func TestBusyTransactionIsNotIdle(t *testing.T) {
	a, tt, q, _ := txApp(t, 11)
	q.Running = true // as a long statement in the transaction
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		tt.Frame()
		if a.idleWarn != nil {
			t.Fatal("warned about a transaction in use")
		}
		time.Sleep(50 * time.Millisecond)
	}
	q.Running = false
	q.FinishTx(false, func(error) {})

	testutil.WaitFor(t, tt, "the rollback", func() bool { return !q.OpenTx() && !q.Running })
}

func TestOpenTransactionsIndicator(t *testing.T) {
	a, tt, q, _ := txApp(t, 0)
	tt.Frame()
	if !testutil.HasTextContaining(tt, "1 open transaction") {
		t.Fatalf("no indicator: %q", tt.Texts())
	}
	if !strings.Contains(a.openTxTabs()[0].Title(), "query-") || a.openTxTabs()[0] != q {
		t.Fatal("indicator lists the wrong tab")
	}
	q.FinishTx(false, func(error) {})

	testutil.WaitFor(t, tt, "the indicator to go", func() bool { return !testutil.HasTextContaining(tt, "open transaction") })
}

// A confirmation asked while another is shown waits its turn.
func TestConfirmationsQueue(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	cancelled := false
	first := a.AskConfirm(cn, safety.Verdict{Reasons: []string{"one"}}, "First?", "Run", "", func() {})
	first.OnCancel = func() { cancelled = true }
	second := a.AskConfirm(cn, safety.Verdict{Reasons: []string{"two"}}, "Second?", "Run", "", func() {})
	if a.confirm != first {
		t.Fatal("the second replaced the first")
	}
	tt.Frame()
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !cancelled || a.confirm != second {
		t.Fatalf("after the first: cancelled %v, shown %v", cancelled, a.confirm == second)
	}
}

// runAt runs the editor's statement at the first place that shows at,
// and waits for the run to end.
func runAt(t *testing.T, tt *ui.Tester, q *query.Tab, at string, mode query.RunMode) {
	t.Helper()
	testutil.SetCaret(tt, &q.Editor, strings.Index(q.Editor.Text, at))
	q.Run(mode)
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running })
}
