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
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

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

// On a connection every session shares (DuckDB), the transaction is the
// tab's that began it: another tab's statements run inside it, which its
// bar says, naming the owner, and its Commit refuses; the indicator, the
// quit prompt and the idle warning name the owner alone, and closing
// another tab leaves the transaction alone.
func TestSharedTransactionOwner(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:", IdleTxTimeout: 13})
	tt := ui.NewTester(a.view, 1000, 700)
	owner := newAppEditor(t, a, tt, cn, "CREATE TABLE t (a INT);\n\nBEGIN;\n\nINSERT INTO t VALUES (1);\n\nINSERT INTO t VALUES (2);")
	runAt(t, tt, owner, "CREATE", query.RunStatement)
	runAt(t, tt, owner, "BEGIN", query.RunStatement)
	runAt(t, tt, owner, "INSERT INTO t VALUES (1)", query.RunStatement)
	if !owner.OpenTx() {
		t.Fatal("no transaction")
	}
	inside := "Inside " + owner.Title() + "'s transaction"
	other := newAppEditor(t, a, tt, cn, "SELECT count(*) AS n FROM t")
	runAt(t, tt, other, "SELECT", query.RunStatement)
	if other.OpenTx() {
		t.Fatal("the other editor holds the owner's transaction as its own")
	}
	testutil.WaitFor(t, tt, "the other editor's bar", func() bool { return testutil.HasTextContaining(tt, inside) })
	if err := tt.Click("Commit"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.alert == nil || !strings.Contains(a.alert.message, owner.Title()) || other.Running {
		t.Fatalf("the other editor's Commit: alert %+v, running %v", a.alert, other.Running)
	}
	a.alert = nil

	// A table tab reads inside it too.
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "t", Kind: db.KindTable, Rows: -1}, dataview.PageData)
	table := a.ActiveTab().(*dataview.TableTab)
	tt.Frame()
	testutil.WaitFor(t, tt, "the table tab's bar", func() bool { return !table.Busy() && testutil.HasTextContaining(tt, inside) })
	if table.OpenTx() {
		t.Fatal("the table tab holds the owner's transaction as its own")
	}
	if err := tt.Click("Roll Back"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.alert == nil || !strings.Contains(a.alert.message, owner.Title()) || !owner.OpenTx() {
		t.Fatalf("the table tab's Roll Back: alert %+v, owner's tx %v", a.alert, owner.OpenTx())
	}
	a.alert = nil

	if txs := a.openTxTabs(); len(txs) != 1 || txs[0] != owner {
		t.Fatalf("open transactions %d", len(txs))
	}
	if a.requestQuit(func() {}) || a.closing == nil || len(a.closing.txs) != 1 || a.closing.txs[0] != owner ||
		strings.Contains(a.closing.reason, other.Title()) {
		t.Fatalf("the quit prompt: %+v", a.closing)
	}
	a.closing = nil
	a.endTab(other, "Close?", func(*window, int) {})
	if a.closing != nil {
		t.Fatalf("closing the other editor asks: %q", a.closing.reason)
	}

	// The owner used last: warned about first, alone.
	runAt(t, tt, owner, "INSERT INTO t VALUES (2)", query.RunStatement)
	testutil.WaitFor(t, tt, "the warning", func() bool { return a.idleWarn != nil })
	if a.idleWarn.tab != owner || !testutil.HasTextContaining(tt, "The transaction of "+owner.Title()+" on duck") {
		t.Fatalf("the warning names %s: %q", a.idleWarn.tab.Title(), tt.Texts())
	}
	a.idleWarn.deadline = a.now
	testutil.WaitFor(t, tt, "the rollback", func() bool { return !owner.OpenTx() })
	a.ActivateTab(func(t widgets.Tab) bool { return t == table })
	testutil.WaitFor(t, tt, "the table tab's bar to go", func() bool { return !testutil.HasTextContaining(tt, "Inside ") })
	var n int
	if err := cn.DB.SQL.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil || n != 0 {
		t.Fatalf("after the rollback: %d rows, %v", n, err)
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

// On a connection every session shares, the owner notices every frame what
// another session did to its transaction: a COMMIT clears its bar, the
// status bar's count, the close and quit prompts and the idle warning; a
// statement of a tab inside it that fails it shows the owner it failed.
func TestOwnerSeesOtherTabCommit(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:", IdleTxTimeout: 13})
	tt := ui.NewTester(a.view, 1000, 700)
	owner := newAppEditor(t, a, tt, cn, "CREATE TABLE t (a INT PRIMARY KEY);\n\nBEGIN;\n\nINSERT INTO t VALUES (1);")
	runAt(t, tt, owner, "CREATE", query.RunScript)
	if !owner.OpenTx() || owner.CloseReason() == "" {
		t.Fatalf("no transaction: %v, %q", owner.OpenTx(), owner.CloseReason())
	}
	testutil.WaitFor(t, tt, "the warning", func() bool { return a.idleWarn != nil })
	if !testutil.HasTextContaining(tt, "1 open transaction") || !testutil.HasTextContaining(tt, "Transaction open") {
		t.Fatalf("no indicator or bar: %q", tt.Texts())
	}

	// Another session, as a tab that typed it before it was refused.
	ctx := context.Background()
	other, err := cn.DB.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	if _, err := other.Exec(ctx, "COMMIT"); err != nil {
		t.Fatal(err)
	}
	// Within frames, long before the warning's countdown would end it.
	for range 3 {
		tt.Frame()
	}
	if owner.OpenTx() {
		t.Fatalf("the owner did not notice the commit: %q", tt.Texts())
	}
	if a.idleWarn != nil || owner.CloseReason() != "" || len(a.openTxTabs()) != 0 {
		t.Fatalf("after the commit: warning %v, close reason %q, open %d", a.idleWarn != nil, owner.CloseReason(), len(a.openTxTabs()))
	}
	if testutil.HasTextContaining(tt, "open transaction") || testutil.HasTextContaining(tt, "Transaction open") {
		t.Fatalf("the indicator or the bar stays: %q", tt.Texts())
	}
	if !a.requestQuit(func() {}) || a.closing != nil {
		t.Fatalf("quitting asks: %+v", a.closing)
	}

	// A tab inside the owner's transaction fails it.
	runAt(t, tt, owner, "BEGIN", query.RunStatement)
	inside := newAppEditor(t, a, tt, cn, "INSERT INTO t VALUES (1)")
	runAt(t, tt, inside, "INSERT", query.RunStatement)
	a.ActivateTab(func(t widgets.Tab) bool { return t == owner })
	testutil.WaitFor(t, tt, "the owner to show it failed", func() bool {
		return owner.Tx == db.TxFailed && testutil.HasTextContaining(tt, "The transaction failed")
	})
	owner.FinishTx(false, func(error) {})
	testutil.WaitFor(t, tt, "the rollback", func() bool { return !owner.OpenTx() && !owner.Busy() })
}

// Work in a tab inside the owner's transaction, on a connection every
// session shares, uses the transaction: no idle warning while it goes on,
// one once it stops, and the rollback takes its rows too.
func TestSharedIdleCountsInsideWork(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:", IdleTxTimeout: 11}) // warned a second after the last use
	tt := ui.NewTester(a.view, 1000, 700)
	owner := newAppEditor(t, a, tt, cn, "CREATE TABLE t (a INT);\n\nBEGIN;\n\nINSERT INTO t VALUES (1);")
	runAt(t, tt, owner, "CREATE", query.RunScript)
	if !owner.OpenTx() {
		t.Fatal("no transaction")
	}
	inside := newAppEditor(t, a, tt, cn, "INSERT INTO t VALUES (2)")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runAt(t, tt, inside, "INSERT", query.RunStatement)
		for range 8 {
			tt.Frame()
			if a.idleWarn != nil {
				t.Fatal("warned about a transaction another tab works in")
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	testutil.WaitFor(t, tt, "the warning", func() bool { return a.idleWarn != nil })
	if a.idleWarn.tab != owner {
		t.Fatalf("the warning names %s", a.idleWarn.tab.Title())
	}
	a.idleWarn.deadline = a.now
	testutil.WaitFor(t, tt, "the rollback", func() bool { return !owner.OpenTx() && !owner.Busy() })
	var n int
	if err := cn.DB.SQL.QueryRow("SELECT count(*) FROM t").Scan(&n); err != nil || n != 0 {
		t.Fatalf("after the rollback: %d rows, %v", n, err)
	}
}
