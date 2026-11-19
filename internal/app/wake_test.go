package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// MyGo's fake backend sends no power events: these tests call
// checkConnectionsAfterWake, as the wake does.

// sqliteStore opens a pool on a fresh SQLite file: the store of the
// server connections below, which DGopher pings like any server's.
func sqliteStore(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), db.Config{Name: "store", Engine: db.SQLite, Database: writeSQL(t, "store.sqlite", "")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// deadStore is a pool whose server no longer answers: its ping fails.
func deadStore(t *testing.T) *db.DB {
	t.Helper()
	d := sqliteStore(t)
	d.SQL.Close()
	return d
}

// unreachable is a PostgreSQL connection to a port nothing listens on.
var unreachable = db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "127.0.0.1", Port: 1, User: "x", Database: "x"}

// connectedTo makes cn connected on store, as a connect would.
func connectedTo(cn *connection.Conn, store db.Store) {
	cn.Status, cn.Store, cn.HeldAt = connection.StatusConnected, store, time.Now()
}

func actionLabels(n notification) []string {
	var labels []string
	for _, act := range n.actions {
		labels = append(labels, act.Label)
	}
	return labels
}

// A server that still answers after the wake is left as it was.
func TestWakeKeepsLiveConnection(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, unreachable)
	connectedTo(cn, sqliteStore(t))
	tt := ui.NewTester(a.view, 1000, 700)
	gen, store, notes := cn.Generation, cn.Store, len(a.notes)
	a.checkConnectionsAfterWake()
	settle(t, a, tt)
	if cn.Status != connection.StatusConnected || cn.Generation != gen || cn.Store != store {
		t.Fatalf("status %v, generation %d (was %d), store replaced %v", cn.Status, cn.Generation, gen, cn.Store != store)
	}
	if len(a.notes) != notes || a.frontAlert() != nil {
		t.Fatalf("notes %+v, alert %v", a.notes[notes:], a.frontAlert())
	}
}

// A dead server without work on it is reconnected quietly through the
// normal connect: while the server stays unreachable, the failure is a notification,
// never a dialog, and the editor stays.
func TestWakeReconnectFailureOnlyNotifies(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, unreachable)
	connectedTo(cn, deadStore(t))
	tt := ui.NewTester(a.view, 1000, 700)
	q := newAppEditor(t, a, tt, cn, "SELECT 1")
	connects := countEvents(t, a, cn, audit.KindConnect)
	a.checkConnectionsAfterWake()
	testutil.WaitFor(t, tt, "the reconnect to fail", func() bool { return cn.Status == connection.StatusFailed })
	if a.frontAlert() != nil {
		t.Fatalf("an alert showed: %q", a.frontAlert().report.Title)
	}
	if got := countEvents(t, a, cn, audit.KindConnect); got != connects+1 {
		t.Fatalf("%d connect attempts audited, want 1", got-connects)
	}
	var dropped bool
	for _, e := range auditEvents(t, a) {
		dropped = dropped || e.Kind == audit.KindDisconnect && strings.Contains(e.Detail, "sleep")
	}
	if !dropped {
		t.Fatalf("no disconnect audited: %+v", auditEvents(t, a))
	}
	if n := a.notes[len(a.notes)-1]; !n.isError || n.title != "Could not connect to pg" {
		t.Fatalf("last note %+v", n)
	}
	if !a.holds(q) {
		t.Fatal("the editor closed")
	}
}

// A dead server without work on it is reconnected through the normal
// connect; its editor stays, its session to be made again.
func TestWakeReconnectsDeadIdleConnection(t *testing.T) {
	testutil.Integration(t)
	a := newTestApp(t)
	cn := addConn(a, testutil.PGConfig())
	tt := ui.NewTester(a.view, 1000, 700)
	connectNow(t, a, tt, cn)
	q := newAppEditor(t, a, tt, cn, "SELECT 1")
	runAt(t, tt, q, "SELECT", query.RunStatement)
	live := cn.Store
	t.Cleanup(func() { live.Close() })
	dead := deadStore(t)
	cn.Store = dead // as a server gone while the computer slept
	gen := cn.Generation
	a.checkConnectionsAfterWake()
	testutil.WaitFor(t, tt, "the reconnect", func() bool {
		return cn.Status == connection.StatusConnected && cn.Generation != gen && cn.Store != dead
	})
	if a.frontAlert() != nil {
		t.Fatalf("an alert showed: %q", a.frontAlert().report.Title)
	}
	if !a.holds(q) || q.Conn != cn {
		t.Fatal("the editor closed")
	}
	if n := a.notes[len(a.notes)-1]; n.isError || !strings.Contains(n.title, "Reconnected "+cn.Config.Name) {
		t.Fatalf("last note %+v", n)
	}
	// The next run is refused once: the new session has none of what the
	// old one had set.
	runAt(t, tt, q, "SELECT", query.RunStatement)
	if !tt.HasText("The connection was closed and opened again: run again to use it.") {
		t.Fatal("the run after the reconnect was not refused")
	}
}

// fastWakeChecks shortens the wait before a connection holding work is
// pinged again.
func fastWakeChecks(t *testing.T, delay time.Duration) {
	t.Helper()
	old := wakeRecheckDelay
	wakeRecheckDelay = delay
	t.Cleanup(func() { wakeRecheckDelay = old })
}

// lostTxApp is txApp's editor, with a transaction open, on a connection
// taken for a server's, whose server stopped answering.
func lostTxApp(t *testing.T) (*App, *ui.Tester, *query.Tab) {
	t.Helper()
	fastWakeChecks(t, 10*time.Millisecond)
	a, tt, q, _ := txApp(t, 0)
	cn := q.Conn
	// The editor's session stays on the SQLite pool; the connection
	// counts as a server's, whose ping fails.
	cn.Config.Engine = db.Postgres
	live := cn.Store
	t.Cleanup(func() { closeStore(live) })
	cn.Store = deadStore(t)
	return a, tt, q
}

// A dead server whose editor holds a transaction is never reconnected:
// the rollback is audited, the editor stays, and one report says the
// transaction was lost, with Reconnect.
func TestWakeAsksForLostTransaction(t *testing.T) {
	a, tt, q := lostTxApp(t)
	cn := q.Conn
	notes, connects := len(a.notes), countEvents(t, a, cn, audit.KindConnect)
	a.checkConnectionsAfterWake()
	testutil.WaitFor(t, tt, "the connection to fail", func() bool { return cn.Status == connection.StatusFailed })
	if q.OpenTx() {
		t.Fatal("the editor still shows its transaction open")
	}
	var rolledBack, dropped bool
	for _, e := range auditEvents(t, a) {
		rolledBack = rolledBack || e.Kind == audit.KindStatement && e.Statement == "ROLLBACK"
		dropped = dropped || e.Kind == audit.KindDisconnect && strings.Contains(e.Detail, "sleep")
	}
	if !rolledBack || !dropped {
		t.Fatalf("audit: rollback %v, disconnect %v: %+v", rolledBack, dropped, auditEvents(t, a))
	}
	if got := countEvents(t, a, cn, audit.KindConnect); got != connects {
		t.Fatalf("reconnected %d times on its own", got-connects)
	}
	if !a.holds(q) {
		t.Fatal("the editor closed")
	}
	testutil.WaitFor(t, tt, "the report", func() bool {
		return a.frontAlert() != nil && a.frontAlert().report.Title == "Transaction lost while asleep" && tt.HasText("Reconnect")
	})
	added := a.notes[notes:]
	if len(added) != 1 || !added[0].isError {
		t.Fatalf("notes added: %+v", added)
	}
	n := added[0]
	if !strings.Contains(n.detail, "lite") || !strings.Contains(n.detail, q.Title()) {
		t.Fatalf("note detail %q", n.detail)
	}
	if !slices.Contains(actionLabels(n), "Reconnect") {
		t.Fatalf("note actions %q", actionLabels(n))
	}
	if cn.Status != connection.StatusFailed {
		t.Fatalf("status %v before Reconnect", cn.Status)
	}
}

// A ping that failed waiting for a pool whose every connection is taken,
// one by the editor's transaction, says nothing of the server: nothing
// happens, and the transaction stays open.
func TestWakeFullPoolIsInconclusive(t *testing.T) {
	old := wakePingTimeout
	wakePingTimeout = 300 * time.Millisecond
	t.Cleanup(func() { wakePingTimeout = old })
	a, tt, q, _ := txApp(t, 0)
	cn := q.Conn
	cn.Config.Engine = db.Postgres // checked as a server's
	cn.DB().SQL.SetMaxOpenConns(1) // the editor's session holds the one connection
	gen, notes := cn.Generation, len(a.notes)
	a.checkConnectionsAfterWake()
	settle(t, a, tt)
	if cn.Status != connection.StatusConnected || cn.Generation != gen || !q.OpenTx() {
		t.Fatalf("status %v, generation %d (was %d), tx %v", cn.Status, cn.Generation, gen, q.OpenTx())
	}
	if len(a.notes) != notes || a.frontAlert() != nil {
		t.Fatalf("notes %+v, alert %v", a.notes[notes:], a.frontAlert())
	}
}

// A connection holding work is given up only when a second ping, a while
// after the first, fails too.
func TestWakePingsTwiceBeforeLosingWork(t *testing.T) {
	a, tt, q := lostTxApp(t)
	fastWakeChecks(t, 600*time.Millisecond)
	cn := q.Conn
	start := time.Now()
	a.checkConnectionsAfterWake()
	testutil.WaitFor(t, tt, "a while", func() bool { return time.Since(start) > 200*time.Millisecond })
	if cn.Status != connection.StatusConnected || !q.OpenTx() {
		t.Fatalf("given up on the first ping: status %v, tx %v", cn.Status, q.OpenTx())
	}
	testutil.WaitFor(t, tt, "the report", func() bool {
		return a.frontAlert() != nil && a.frontAlert().report.Title == "Transaction lost while asleep"
	})
	if cn.Status != connection.StatusFailed || q.OpenTx() {
		t.Fatalf("status %v, tx %v", cn.Status, q.OpenTx())
	}
}

// A job running on a dead server is never stopped by a quiet reconnect:
// the connection fails, and one report says what was stopped.
func TestWakeReportsRunningJob(t *testing.T) {
	fastWakeChecks(t, 10*time.Millisecond)
	a := newTestApp(t)
	cn := addConn(a, unreachable)
	connectedTo(cn, deadStore(t))
	tt := ui.NewTester(a.view, 1000, 700)
	cancelled := make(chan struct{})
	finish := a.StartJob(cn, "Waiting: stopping it loses nothing.", func() { close(cancelled) })
	t.Cleanup(finish)
	connects := countEvents(t, a, cn, audit.KindConnect)
	a.checkConnectionsAfterWake()
	testutil.WaitFor(t, tt, "the report", func() bool {
		return a.frontAlert() != nil && a.frontAlert().report.Title == "Work lost while asleep"
	})
	if r := a.frontAlert().report; !strings.Contains(r.Detail, "Waiting: stopping it loses nothing.") {
		t.Fatalf("report detail %q", r.Detail)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the job was not stopped")
	}
	if cn.Status != connection.StatusFailed {
		t.Fatalf("status %v", cn.Status)
	}
	if got := countEvents(t, a, cn, audit.KindConnect); got != connects {
		t.Fatalf("reconnected %d times on its own", got-connects)
	}
}

// A quiet reconnect runs no password command, which may raise a system
// prompt: the connection is left failed, with a notification.
func TestWakePasswordCommandNotifies(t *testing.T) {
	a := newTestApp(t)
	ran := filepath.Join(t.TempDir(), "ran")
	cfg := unreachable
	cfg.PasswordCommand = "touch " + ran
	cn := addConn(a, cfg)
	connectedTo(cn, deadStore(t))
	tt := ui.NewTester(a.view, 1000, 700)
	a.checkConnectionsAfterWake()
	testutil.WaitFor(t, tt, "the connection to fail", func() bool { return cn.Status == connection.StatusFailed })
	settle(t, a, tt)
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("the password command ran")
	}
	n := a.notes[len(a.notes)-1]
	if n.title != "Reconnect pg: its password command may ask you" || !slices.Contains(actionLabels(n), "Reconnect") {
		t.Fatalf("last note %q, actions %q", n.title, actionLabels(n))
	}
}

// The network coming back checks the connections as the wake does, and
// says so.
func TestBackOnlineAsksForLostTransaction(t *testing.T) {
	a, tt, q := lostTxApp(t)
	a.setOnline(false)
	a.setOnline(true)
	testutil.WaitFor(t, tt, "the report", func() bool {
		return a.frontAlert() != nil && a.frontAlert().report.Title == "Transaction lost while offline"
	})
	if q.Conn.Status != connection.StatusFailed {
		t.Fatalf("status %v", q.Conn.Status)
	}
}

// A quiet reconnect that would ask for a password asks nothing: the
// connection is left failed, with a notification whose Reconnect asks.
func TestWakeNeedsPasswordNotifies(t *testing.T) {
	a := newTestApp(t)
	cfg := unreachable
	cfg.AskPassword = true
	cn := addConn(a, cfg)
	connectedTo(cn, deadStore(t))
	tt := ui.NewTester(a.view, 1000, 700)
	a.checkConnectionsAfterWake()
	testutil.WaitFor(t, tt, "the connection to fail", func() bool { return cn.Status == connection.StatusFailed })
	settle(t, a, tt)
	if a.prompt != nil || len(a.promptQueue) > 0 || a.frontAlert() != nil {
		t.Fatalf("prompt %v, queue %d, alert %v", a.prompt != nil, len(a.promptQueue), a.frontAlert())
	}
	n := a.notes[len(a.notes)-1]
	if n.title != "Reconnect pg: a password is needed" || !slices.Contains(actionLabels(n), "Reconnect") {
		t.Fatalf("last note %q, actions %q", n.title, actionLabels(n))
	}
	n.actions[slices.Index(actionLabels(n), "Reconnect")].Run()
	testutil.WaitFor(t, tt, "the password prompt", func() bool { return a.prompt != nil })
}

// A quiet reconnect that would ask whether the destination is trusted
// asks nothing either.
func TestWakeNeedsTrustNotifies(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, unreachable)
	connectedTo(cn, deadStore(t))
	a.settings.TrustedShared, a.settings.TrustedDetails = nil, nil
	tt := ui.NewTester(a.view, 1000, 700)
	a.checkConnectionsAfterWake()
	testutil.WaitFor(t, tt, "the connection to fail", func() bool { return cn.Status == connection.StatusFailed })
	settle(t, a, tt)
	if a.confirm != nil || a.frontAlert() != nil {
		t.Fatalf("confirm %v, alert %v", a.confirm != nil, a.frontAlert())
	}
	n := a.notes[len(a.notes)-1]
	if !strings.HasPrefix(n.title, "Reconnect pg: ") || !slices.Contains(actionLabels(n), "Reconnect") {
		t.Fatalf("last note %q, actions %q", n.title, actionLabels(n))
	}
}

// The idle limits count from the wake, not from before the sleep.
func TestWakeResetsIdleClocks(t *testing.T) {
	a, tt, q, _ := txApp(t, 60)
	cn := q.Conn
	slept := a.now.Add(-time.Hour)
	q.Times().Used, cn.HeldAt = slept, slept
	a.checkConnectionsAfterWake()
	tt.Frame()
	if !q.Times().Used.Equal(a.now) || !cn.HeldAt.Equal(a.now) {
		t.Fatalf("transaction used %v, connection held %v, now %v", q.Times().Used, cn.HeldAt, a.now)
	}
	if a.idleWarn != nil {
		t.Fatal("the idle warning showed for the sleep")
	}
	if !q.OpenTx() || cn.Status != connection.StatusConnected {
		t.Fatalf("tx %v, status %v", q.OpenTx(), cn.Status)
	}
}

// The network coming back does not restart the idle limits: the
// computer ran meanwhile, and the transaction held its locks.
func TestBackOnlineKeepsIdleClocks(t *testing.T) {
	a, tt, q, _ := txApp(t, 3600)
	slept := a.now.Add(-time.Minute)
	q.Times().Used = slept
	a.setOnline(false)
	a.setOnline(true)
	tt.Frame()
	if !q.Times().Used.Equal(slept) {
		t.Fatalf("transaction used %v, want %v", q.Times().Used, slept)
	}
}

// Local files lose nothing to a sleep: they are not pinged, even when
// their pool no longer answers.
func TestWakeSkipsFileEngines(t *testing.T) {
	a, tt, cn, _ := liteApp(t, db.Development)
	live := cn.Store
	t.Cleanup(func() { closeStore(live) })
	cn.Store = deadStore(t)
	gen, notes := cn.Generation, len(a.notes)
	a.checkConnectionsAfterWake()
	settle(t, a, tt)
	if cn.Status != connection.StatusConnected || cn.Generation != gen {
		t.Fatalf("status %v, generation %d (was %d)", cn.Status, cn.Generation, gen)
	}
	if len(a.notes) != notes || a.frontAlert() != nil {
		t.Fatalf("notes %+v", a.notes[notes:])
	}
}

// On a real server, a transaction open when the server stops answering
// is reported lost, its rollback audited, and nothing reconnects.
func TestIntegrationWakeAsksForLostTransaction(t *testing.T) {
	testutil.Integration(t)
	fastWakeChecks(t, 10*time.Millisecond)
	a := newTestApp(t)
	cn := addConn(a, testutil.PGConfig())
	tt := ui.NewTester(a.view, 1000, 700)
	connectNow(t, a, tt, cn)
	q := newAppEditor(t, a, tt, cn, "BEGIN")
	runAt(t, tt, q, "BEGIN", query.RunStatement)
	if !q.OpenTx() {
		t.Fatal("no transaction")
	}
	live := cn.Store
	t.Cleanup(func() { live.Close() })
	cn.Store = deadStore(t)
	connects := countEvents(t, a, cn, audit.KindConnect)
	a.checkConnectionsAfterWake()
	testutil.WaitFor(t, tt, "the report", func() bool {
		return a.frontAlert() != nil && a.frontAlert().report.Title == "Transaction lost while asleep"
	})
	if cn.Status != connection.StatusFailed || q.OpenTx() || !a.holds(q) {
		t.Fatalf("status %v, tx %v, editor kept %v", cn.Status, q.OpenTx(), a.holds(q))
	}
	if rollbackAudited(t, a, "") == nil {
		t.Fatalf("no rollback audited: %+v", auditEvents(t, a))
	}
	if got := countEvents(t, a, cn, audit.KindConnect); got != connects {
		t.Fatalf("reconnected %d times on its own", got-connects)
	}
}

// A Redis server that answers its PING after the wake is left as it was.
func TestIntegrationWakeKeepsLiveRedis(t *testing.T) {
	testutil.Integration(t)
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "rd", Name: "rd", Engine: db.Redis, Host: "127.0.0.1", Port: 16379})
	tt := ui.NewTester(a.view, 1000, 700)
	connectNow(t, a, tt, cn)
	gen, notes := cn.Generation, len(a.notes)
	a.checkConnectionsAfterWake()
	settle(t, a, tt)
	if cn.Status != connection.StatusConnected || cn.Generation != gen || len(a.notes) != notes {
		t.Fatalf("status %v, generation %d (was %d), notes %+v", cn.Status, cn.Generation, gen, a.notes[notes:])
	}
}
