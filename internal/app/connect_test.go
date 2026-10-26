package app

import (
	"net"
	"sync"
	"testing"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// silentPostgres is a PostgreSQL connection to a server of the test that
// accepts and never answers, so that connecting to it waits.
func silentPostgres(t *testing.T) db.Config {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	return db.Config{ID: "silent", Name: "silent", Engine: db.Postgres, Host: "127.0.0.1", Port: l.Addr().(*net.TCPAddr).Port,
		User: "postgres", Password: "secret", Database: "postgres"}
}

func TestDisconnectWhileConnectingDropsWaiters(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, silentPostgres(t))
	tt := ui.NewTester(a.view, 1000, 700)
	ran := false
	a.Connect(cn, func() { ran = true })
	if cn.Status != connection.StatusConnecting {
		t.Fatalf("status %v, want connecting", cn.Status)
	}
	a.disconnect(cn) // as the connection form does when its changes need a reconnect
	if len(cn.Waiters) != 0 {
		t.Fatalf("the disconnect kept %d waiters of the connect it ended", len(cn.Waiters))
	}
	cn.Config = db.Config{ID: cn.Config.ID, Name: "silent", Engine: db.SQLite, Database: writeSQL(t, "later.sqlite", "")}
	a.trust(cn)
	connectNow(t, a, tt, cn)
	if ran {
		t.Fatal("what waited for the ended connect ran on a later one")
	}
}

// slowConnect starts connecting cn, whose server never answers, the
// attempt and the app's work dated back for Cancel and the activity bar to
// show.
func slowConnect(t *testing.T, a *App, tt *ui.Tester, cn *connection.Conn) {
	t.Helper()
	a.Connect(cn, func() { t.Error("what waited for a cancelled connect ran") })
	testutil.WaitFor(t, tt, "connecting", func() bool { return a.connecting[cn] != nil })
	tt.Frame()
	a.connecting[cn].started = time.Now().Add(-3 * time.Second)
	a.busySince = time.Now().Add(-3 * time.Second)
	tt.Frame()
	tt.Advance(time.Second) // Cancel come in
}

// cancelledConnect checks that cn's connect ended as cancelled, audited,
// and that what it opened late was not taken.
func cancelledConnect(t *testing.T, a *App, tt *ui.Tester, cn *connection.Conn) {
	t.Helper()
	const why = "you cancelled connecting"
	if cn.Status != connection.StatusFailed || cn.Err != why || cn.Waiters != nil || a.connecting[cn] != nil {
		t.Fatalf("after Cancel: status %v, error %q, %d waiters, attempt kept %v", cn.Status, cn.Err, len(cn.Waiters), a.connecting[cn] != nil)
	}
	audited := false
	for _, e := range auditEvents(t, a) {
		audited = audited || e.Kind == audit.KindConnect && e.Error == why
	}
	if !audited {
		t.Fatalf("the cancel is not audited: %+v", auditEvents(t, a))
	}
	time.Sleep(300 * time.Millisecond) // the stopped attempt's result arrives
	tt.Frame()
	tt.Frame()
	if cn.Status != connection.StatusFailed || cn.Err != why || cn.DB() != nil {
		t.Fatalf("a late result was taken: status %v, error %q", cn.Status, cn.Err)
	}
}

func TestConnectCancel(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, silentPostgres(t))
	tt := ui.NewTester(a.view, 1200, 800)
	slowConnect(t, a, tt, cn)
	if !tt.HasText("Working") || !tt.HasText("connecting…") {
		t.Fatalf("the status bar shows no work: %q", tt.Texts())
	}
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	cancelledConnect(t, a, tt, cn)
}

func TestConnectCancelFromNavigator(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, silentPostgres(t))
	tt := ui.NewTester(a.view, 1200, 800)
	slowConnect(t, a, tt, cn)
	if err := tt.RightClick("silent"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Cancel Connecting"); err != nil {
		t.Fatalf("%v; menu %q", err, tt.Menu())
	}
	tt.Frame()
	cancelledConnect(t, a, tt, cn)
}

// An editor open on the connection says so once its connect is cancelled.
func TestConnectCancelFromEditor(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, silentPostgres(t))
	tt := ui.NewTester(a.view, 1200, 800)
	slowConnect(t, a, tt, cn)
	// As an editor restored with the workspace, open before its connection.
	a.AddTab(query.New(a, cn, "", "query", "select 1"))
	tt.Frame()
	tt.Advance(time.Second) // the tab and its Cancel come in
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	cancelledConnect(t, a, tt, cn)
	if _, ok := tt.Find("Connection failed: you cancelled connecting"); !ok {
		t.Fatalf("the editor does not say the connect was cancelled: %q", tt.Texts())
	}
}

// The editor's Connect button turns into Disconnect once connected, in
// place, and back: disconnecting from there keeps the editor.
func TestEditorConnectTurnsToDisconnect(t *testing.T) {
	a := newTestApp(t)
	// DuckDB, whose editor has a schema menu once connected.
	cn := addConn(a, db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1100, 400)
	q := query.New(a, cn, "", "query", "SELECT 1;")
	a.AddTab(q)
	tt.Frame()
	bar, _ := tt.Find("Editor")
	run, _ := tt.Find("Run")
	if err := tt.Click("Connect"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "Disconnect", func() bool { _, ok := tt.Find("Disconnect"); return ok })
	// The schema's menu comes, the toolbar as tall.
	testutil.WaitFor(t, tt, "the schema menu", func() bool { return hasExact(tt, "main") })
	if now, _ := tt.Find("Editor"); now.H != bar.H {
		t.Errorf("connected, the toolbar is %v high, %v before", now.H, bar.H)
	}
	if now, _ := tt.Find("Run"); now.X != run.X {
		t.Errorf("connected, Run moved from %v to %v", run.X, now.X)
	}
	if err := tt.Click("Disconnect"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if cn.Status != connection.StatusIdle || a.tabs().Len() != 1 || a.tabs().At(0) != q || !hasExact(tt, "Connect") {
		t.Fatalf("after Disconnect: %v, %d tabs, %q", cn.Status, a.tabs().Len(), tt.Texts())
	}
}
