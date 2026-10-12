package app

import (
	"context"
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

// idleAfter has every connection disconnect after limit without use, as
// a server's does; SQLite, which the tests use, never does.
func idleAfter(t *testing.T, limit time.Duration) {
	t.Helper()
	was := idleConnectionLimit
	idleConnectionLimit = func(*db.Config) time.Duration { return limit }
	t.Cleanup(func() { idleConnectionLimit = was })
}

// idleApp is an app with a connection "lite" to a SQLite file whose table
// t holds two rows.
func idleApp(t *testing.T, autoConnect bool) (*App, *ui.Tester, *connection.Conn) {
	t.Helper()
	a := newTestApp(t)
	file := writeSQL(t, "idle.sqlite", "")
	setup, err := db.Open(context.Background(), db.Config{Name: "s", Engine: db.SQLite, Database: file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	setup.SQL.Exec("CREATE TABLE t (a INT)")
	setup.SQL.Exec("INSERT INTO t VALUES (1), (2)")
	setup.Close()
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file, AutoConnect: autoConnect})
	return a, ui.NewTester(a.view, 1000, 700), cn
}

// stillConnected runs frames for d, failing if cn disconnects.
func stillConnected(t *testing.T, tt *ui.Tester, cn *connection.Conn, d time.Duration, why string) {
	t.Helper()
	for deadline := time.Now().Add(d); time.Now().Before(deadline); {
		tt.Frame()
		if cn.Status != connection.StatusConnected {
			t.Fatalf("disconnected %s", why)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A connection unused past its limit disconnects; its editor stays, with
// its rows, and connects it again at the next run, not as it shows.
func TestIdleConnectionDisconnects(t *testing.T) {
	idleAfter(t, time.Second)
	a, tt, cn := idleApp(t, true)
	q := newAppEditor(t, a, tt, cn, "SELECT a FROM t")
	text := q.Editor.Text
	runAt(t, tt, q, "SELECT", query.RunStatement)
	testutil.WaitFor(t, tt, "the result", func() bool { return resultRows(tt, q, 2) })
	testutil.WaitFor(t, tt, "the idle disconnect", func() bool { return cn.Status == connection.StatusIdle })
	if a.ActiveTab() != q || q.Editor.Text != text || !resultRows(tt, q, 2) {
		t.Fatalf("the editor or its rows went: %q", tt.Texts())
	}
	if !testutil.HasTextContaining(tt, "Disconnected lite after") {
		t.Fatalf("no notice: %q", tt.Texts())
	}
	audited := false
	for _, e := range auditEvents(t, a) {
		audited = audited || e.Kind == audit.KindDisconnect && strings.Contains(e.Detail, "idle")
	}
	if !audited {
		t.Fatalf("not audited: %+v", auditEvents(t, a))
	}
	for range 10 {
		tt.Frame()
		time.Sleep(20 * time.Millisecond)
	}
	if cn.Status != connection.StatusIdle {
		t.Fatal("connected again by its editor showing")
	}
	// The next run connects it, and is refused once: the new session has
	// none of what the old one had set.
	q.Editor.Text = strings.Replace(text, "FROM t", "FROM t WHERE a = 1", 1)
	q.Run(query.RunStatement)
	testutil.WaitFor(t, tt, "the refusal", func() bool { return testutil.HasTextContaining(tt, "closed while idle") })
	if cn.Status != connection.StatusConnected || cn.ClosedIdle {
		t.Fatalf("not connected again: %v, %v", cn.Status, cn.ClosedIdle)
	}
	q.Run(query.RunStatement)
	testutil.WaitFor(t, tt, "the run on the new connection", func() bool { return !q.Running && resultRows(tt, q, 1) })
}

// A run confirmed after its connection closed connects it again.
func TestIdleConnectionConfirmedRunConnects(t *testing.T) {
	idleAfter(t, time.Second)
	a, tt, cn := idleApp(t, false)
	cn.Config.Env = db.Production
	a.trust(cn)
	q := newAppEditor(t, a, tt, cn, "INSERT INTO t VALUES (3)")
	runAt(t, tt, q, "INSERT", query.RunStatement)
	testutil.WaitFor(t, tt, "the confirmation", func() bool { return a.confirm != nil })
	testutil.WaitFor(t, tt, "the idle disconnect", func() bool { return cn.Status == connection.StatusIdle })
	a.confirm.OnConfirm()
	a.confirm = nil
	testutil.WaitFor(t, tt, "the insert", func() bool {
		return cn.Status == connection.StatusConnected && !q.Running && testutil.HasTextContaining(tt, "INSERT · 1 row affected")
	})
	q.FinishTx(false, func(error) {})
}

// An open transaction holds the connection open, however long unused.
func TestIdleConnectionWaitsForTransaction(t *testing.T) {
	idleAfter(t, time.Second)
	_, tt, q, _ := txApp(t, 0)
	stillConnected(t, tt, q.Conn, 2500*time.Millisecond, "with a transaction open")
	q.FinishTx(false, func(error) {})
	testutil.WaitFor(t, tt, "the idle disconnect", func() bool { return q.Conn.Status == connection.StatusIdle })
}

// A tab other than an editor keeps its connection, which it cannot do
// without.
func TestIdleConnectionKeptByOtherTab(t *testing.T) {
	idleAfter(t, time.Second)
	a, tt, cn := idleApp(t, false)
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "t", Kind: db.KindTable}, 0)
	testutil.WaitFor(t, tt, "the table", func() bool { return len(a.tabs) == 1 })
	stillConnected(t, tt, cn, 2500*time.Millisecond, "under an open table")
	a.closeTab(a.active)
	testutil.WaitFor(t, tt, "the idle disconnect", func() bool { return cn.Status == connection.StatusIdle })
	// Its tree in the sidebar closes, which would connect it as it draws;
	// opened again, it connects.
	node := navNode{kind: nodeConn, conn: cn.Config.ID}
	for range 10 {
		tt.Frame()
		time.Sleep(20 * time.Millisecond)
	}
	if cn.Status != connection.StatusIdle || a.nav.tree.Open.Has(node) {
		t.Fatalf("connected again by the sidebar: %v, open %v", cn.Status, a.nav.tree.Open.Has(node))
	}
	a.nav.expand(node)
	testutil.WaitFor(t, tt, "the connection", func() bool { return cn.Status == connection.StatusConnected })
}

// An open copy dialog keeps the connections it copies between.
func TestIdleConnectionKeptByDialog(t *testing.T) {
	idleAfter(t, time.Second)
	a, tt, cn := idleApp(t, false)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "the connection", func() bool { return cn.Status == connection.StatusConnected })
	a.copying = &copyDialog{open: true, from: cn}
	stillConnected(t, tt, cn, 2500*time.Millisecond, "under an open copy dialog")
	a.copying = nil
	testutil.WaitFor(t, tt, "the idle disconnect", func() bool { return cn.Status == connection.StatusIdle })
}

// The idle time counts from the last statement.
func TestIdleConnectionCountsRecentUse(t *testing.T) {
	idleAfter(t, 3*time.Second)
	a, tt, cn := idleApp(t, false)
	q := newAppEditor(t, a, tt, cn, "SELECT a FROM t")
	runAt(t, tt, q, "SELECT", query.RunStatement)
	stillConnected(t, tt, cn, 2*time.Second, "within the limit")
	runAt(t, tt, q, "SELECT", query.RunStatement)
	stillConnected(t, tt, cn, 1500*time.Millisecond, "soon after a statement")
}

// The form keeps a connection's idle limit in minutes, Never as -1, for
// servers only; changing it does not reconnect.
func TestConnFormIdleConnection(t *testing.T) {
	a := newTestApp(t)
	cn := &connection.Conn{Config: db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "h", Env: db.Production, IdleConnectionTimeout: 5400}, Project: a.projects[0]}
	a.openConnForm(cn)
	f := a.connForm
	tt := ui.NewTester(a.view, 1000, 1400)
	tt.Frame()
	if err := tt.Click("Options •"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	tt.Frame()
	if f.idleConnMin != 90 || !testutil.HasTextContaining(tt, "1 hour on Production") {
		t.Fatalf("loaded %v: %q", f.idleConnMin, tt.Texts())
	}
	for _, c := range []struct {
		min   float64
		never bool
		want  int
	}{{30, false, 1800}, {30, true, -1}, {0, false, 0}} {
		f.idleConnMin, f.idleConnNever = c.min, c.never
		cfg := f.config()
		if cfg.IdleConnectionTimeout != c.want {
			t.Errorf("%v minutes, never %v: saved %d", c.min, c.never, cfg.IdleConnectionTimeout)
		}
		if reconnectNeeded(f.loaded, cfg) {
			t.Errorf("%d asks to reconnect", cfg.IdleConnectionTimeout)
		}
	}
	a.syncEngineChoice(f, slices.Index(db.Engines(), db.SQLite))
	f.engineIdx = slices.Index(db.Engines(), db.SQLite)
	tt.Frame()
	if testutil.HasTextContaining(tt, "Idle connection") {
		t.Fatalf("a file offers to disconnect: %q", tt.Texts())
	}
}
