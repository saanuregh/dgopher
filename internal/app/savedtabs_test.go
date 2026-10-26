package app

import (
	"path/filepath"
	"testing"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// A connection's tab is kept by what it shows, and opens again from it;
// a kind its engine lacks is refused.
func TestSavedTabOfEachKind(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", "")})
	tt := ui.NewTester(a.view, 1200, 760)
	connectNow(t, a, tt, cn)
	if _, err := cn.DB().SQL.Exec("CREATE TABLE orders (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []savedTab{
		{Kind: savedTable, Connection: cn.Config.ID, Schema: "main", Name: "orders", ObjectKind: string(db.KindTable), Page: dataview.PageDDL},
		{Kind: savedDiagram, Connection: cn.Config.ID, Schema: "main"},
		{Kind: savedSearch, Connection: cn.Config.ID, Database: "main"},
	} {
		if !a.openSaved(cn, s) {
			t.Fatalf("%s refused", s.Kind)
		}
		tt.Frame()
		got, ok := savedTabOf(a.ActiveTab())
		if !ok || got != s {
			t.Fatalf("%s kept as %+v, %v", s.Kind, got, ok)
		}
	}
	if a.tabs().Len() != 3 {
		t.Fatalf("tabs %d", a.tabs().Len())
	}
	for _, kind := range []string{savedKeys, savedActivity, savedUsers} {
		if a.openSaved(cn, savedTab{Kind: kind, Connection: cn.Config.ID}) {
			t.Fatalf("%s opened on SQLite", kind)
		}
	}
	if _, ok := savedTabOf(&plainTab{"x"}); ok {
		t.Fatal("a plain tab is kept")
	}
}

// A restored table waits unconnected: Connect, or ↵, opens it in its place,
// pinned as it was; one set to auto-connect connects as it comes into
// view; opening the same thing meanwhile takes the waiting tab's place
// rather than adding another.
func TestWaitingTabConnectsWhenShown(t *testing.T) {
	a := newTestApp(t)
	path := writeSQL(t, "x.sqlite", "")
	seed := addConn(a, db.Config{ID: "seed", Name: "seed", Engine: db.SQLite, Database: path})
	tt := ui.NewTester(a.view, 1200, 760)
	connectNow(t, a, tt, seed)
	if _, err := seed.DB().SQL.Exec("CREATE TABLE orders (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: path})
	auto := addConn(a, db.Config{ID: "auto", Name: "auto", Engine: db.SQLite, Database: path, AutoConnect: true})
	table := func(cn *connection.Conn) savedTab {
		return savedTab{Kind: savedTable, Connection: cn.Config.ID, Schema: "main", Name: "orders", ObjectKind: string(db.KindTable)}
	}
	first := &waitingTab{a: a, conn: cn, saved: table(cn)}
	second := &waitingTab{a: a, conn: auto, saved: table(auto)}
	diagram := &waitingTab{a: a, conn: cn, saved: savedTab{Kind: savedDiagram, Connection: cn.Config.ID, Schema: "main"}}
	a.main.tabs().Append(first)
	a.main.tabs().Append(second)
	a.main.tabs().Append(diagram)
	a.main.tabs().SetPinned(first, true)
	a.main.tabs().Select(first)
	for range 3 {
		tt.Frame()
	}
	if cn.Status != connection.StatusIdle || !tt.HasText("Not connected.") || !tt.HasText("main.orders will open once lite is connected.") {
		t.Fatalf("status %v, texts %q", cn.Status, tt.Texts())
	}
	testutil.Snapshot(t, tt, "waiting-tab")
	if !tt.HasText(widgets.KeyLabel("↵")) {
		t.Fatalf("no key on the button: %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEnter)
	testutil.WaitFor(t, tt, "the table", func() bool { _, ok := a.main.tabs().At(0).(*dataview.TableTab); return ok })
	if a.main.tabs().Len() != 3 || !a.main.tabs().Pinned(a.main.tabs().At(0)) || a.ActiveTab() != a.main.tabs().At(0) {
		t.Fatalf("tabs %d, pinned %v", a.main.tabs().Len(), a.main.tabs().Pinned(a.main.tabs().At(0)))
	}
	a.main.tabs().Select(second)
	testutil.WaitFor(t, tt, "the auto-connected table", func() bool { _, ok := a.main.tabs().At(1).(*dataview.TableTab); return ok })
	dataview.OpenER(a, cn, "", "main")
	tt.Frame()
	if _, ok := a.main.tabs().At(2).(*dataview.ERTab); !ok || a.main.tabs().Len() != 3 || a.ActiveTab() != a.main.tabs().At(2) {
		t.Fatalf("the diagram did not take the waiting tab's place: %d tabs, %T", a.main.tabs().Len(), a.main.tabs().At(2))
	}
}

// A waiting tab follows its connection's new ID; one whose tab is open
// already gives way to it; one its connection no longer has says so;
// closed, it reopens waiting.
func TestWaitingTabEdges(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", "")})
	tt := ui.NewTester(a.view, 1200, 760)
	orders := func() savedTab {
		return savedTab{Kind: savedTable, Connection: cn.Config.ID, Schema: "main", Name: "orders", ObjectKind: string(db.KindTable)}
	}
	renamed := &waitingTab{a: a, conn: cn, saved: orders()}
	a.main.tabs().Append(&plainTab{"front"})
	a.main.tabs().Append(renamed)
	cn.Config.ID += "2"
	a.trust(cn)
	connectNow(t, a, tt, cn)
	if _, err := cn.DB().SQL.Exec("CREATE TABLE orders (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	a.OpenTable(cn, "", db.Object{Schema: "main", Name: "orders", Kind: db.KindTable, Rows: -1}, dataview.PageData)
	tt.Frame()
	if _, ok := a.main.tabs().At(1).(*dataview.TableTab); !ok || a.main.tabs().Len() != 2 {
		t.Fatalf("after a change of ID, %d tabs, second %T", a.main.tabs().Len(), a.main.tabs().At(1))
	}
	if s, _ := savedTabOf(&waitingTab{a: a, conn: cn, saved: savedTab{Connection: "old"}}); s.Connection != cn.Config.ID {
		t.Fatalf("kept as %q", s.Connection)
	}

	again := &waitingTab{a: a, conn: cn, saved: orders()}
	a.main.tabs().Add(again)
	testutil.WaitFor(t, tt, "the waiting tab to give way", func() bool { return !a.holds(again) })
	if a.main.tabs().Len() != 2 {
		t.Fatalf("tabs %d", a.main.tabs().Len())
	}

	users := &waitingTab{a: a, conn: cn, saved: savedTab{Kind: savedUsers, Connection: cn.Config.ID}}
	a.main.tabs().Add(users)
	for range 3 {
		tt.Frame()
	}
	if !a.holds(users) || !tt.HasText("lite has no lite · users now.") {
		t.Fatalf("texts %q", tt.Texts())
	}

	a.rememberClosed(users, a.main, a.main.panes.Focused(), 2)
	a.removeTab(users)
	a.reopenTab()
	if w, ok := a.ActiveTab().(*waitingTab); !ok || w.saved.Kind != savedUsers {
		t.Fatalf("reopened %T", a.ActiveTab())
	}
}

// Brought in front by a click on its tab, a waiting tab connects on ↵:
// the tab's own button, focused by the click, does not keep it.
func TestWaitingTabEnterAfterClick(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", "")})
	tt := ui.NewTester(a.view, 1200, 760)
	a.main.tabs().Append(&plainTab{"front"})
	a.main.tabs().Append(&waitingTab{a: a, conn: cn, saved: savedTab{Kind: savedSearch, Connection: cn.Config.ID}})
	tt.Frame()
	tt.Advance(time.Second) // the tabs grown in
	if err := tt.Click("lite · search"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
}

// A waiting tab has one button: Connect while it waits, kept as it starts
// connecting but disabled, Cancel once connecting has lasted, Retry once
// it failed; it keeps its place and its height through the three.
func TestWaitingTabOneButton(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, silentPostgres(t))
	tt := ui.NewTester(a.view, 1200, 760)
	a.main.tabs().Append(&waitingTab{a: a, conn: cn, saved: savedTab{Kind: savedSearch, Connection: cn.Config.ID}})
	tt.Frame()
	connect, ok := tt.Find("Connect")
	if !ok {
		t.Fatalf("no Connect button: %q", tt.Texts())
	}
	if err := tt.Click("Connect"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "connecting", func() bool { return a.connecting[cn] != nil })
	if r, ok := tt.Find("Connect"); !ok || r.X+r.W != connect.X+connect.W || r.Y != connect.Y {
		t.Fatalf("while connecting briefly: Connect found %v at %v, was at %v", ok, r, connect)
	}
	if _, ok := tt.Find("Cancel"); ok {
		t.Fatal("Cancel shown before connecting lasted")
	}
	a.connecting[cn].started = time.Now().Add(-3 * time.Second)
	a.busySince = time.Now().Add(-3 * time.Second)
	testutil.WaitFor(t, tt, "Cancel", func() bool { _, ok := tt.Find("Cancel"); return ok })
	testutil.WaitFor(t, tt, "the label to have changed", func() bool { _, ok := tt.Find("Connect"); return !ok })
	if r := testutil.Settled(t, tt, "Cancel"); r.X < connect.X || r.X+r.W > connect.X+connect.W || r.Y < connect.Y || r.Y+r.H > connect.Y+connect.H {
		t.Fatalf("Cancel at %v, not where Connect was, %v", r, connect)
	}
	// ↵, which began the connect, does not cancel it.
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if cn.Status != connection.StatusConnecting {
		t.Fatalf("↵ ended the connect: %v %q", cn.Status, cn.Err)
	}
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "Retry", func() bool { _, ok := tt.Find("Retry"); return ok })
	testutil.WaitFor(t, tt, "the label to have changed", func() bool { _, ok := tt.Find("Cancel"); return !ok })
	if r := testutil.Settled(t, tt, "Retry"); r.X+r.W != connect.X+connect.W || r.Y != connect.Y {
		t.Fatalf("Retry at %v, Connect was at %v", r, connect)
	}
}

// Disconnecting keeps the connection's tabs where they were: an editor as
// it is, a table waiting for the connection, which does not connect again
// on its own though set to; connecting opens the table in its place.
func TestDisconnectKeepsTabs(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", ""), AutoConnect: true})
	tt := ui.NewTester(a.view, 1200, 760)
	connectNow(t, a, tt, cn)
	if _, err := cn.DB().SQL.Exec("CREATE TABLE orders (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	q := query.New(a, cn, "", "query", "SELECT 1")
	a.AddTab(q)
	orders := savedTab{Kind: savedTable, Connection: cn.Config.ID, Schema: "main", Name: "orders", ObjectKind: string(db.KindTable)}
	if !a.openSaved(cn, orders) {
		t.Fatal("the table did not open")
	}
	tt.Frame()

	a.askDisconnect(cn)
	for range 3 {
		tt.Frame()
	}
	if a.tabs().Len() != 2 || a.tabs().At(0) != q {
		t.Fatalf("after disconnecting: %d tabs, the editor kept %v", a.tabs().Len(), a.tabs().At(0) == q)
	}
	wait, ok := a.tabs().At(1).(*waitingTab)
	if !ok || a.ActiveTab() != wait || !wait.saved.same(orders) {
		t.Fatalf("the table's place holds %T, in front %v", a.tabs().At(1), a.ActiveTab() == a.tabs().At(1))
	}
	if cn.Status != connection.StatusIdle {
		t.Fatalf("connected again on its own: %v", cn.Status)
	}

	connectNow(t, a, tt, cn)
	testutil.WaitFor(t, tt, "the table", func() bool {
		s, ok := savedTabOf(a.tabs().At(1))
		_, waiting := a.tabs().At(1).(*waitingTab)
		return ok && !waiting && s.same(orders)
	})
}

// A waiting tab whose connection fails says why in its bar; the user
// asked, so the failure is also raised.
func TestWaitingTabFailureSaysWhyAndAlerts(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: filepath.Join(t.TempDir(), "no", "x.sqlite")})
	tt := ui.NewTester(a.view, 1200, 760)
	a.main.tabs().Append(&waitingTab{a: a, conn: cn, saved: savedTab{Kind: savedSearch, Connection: cn.Config.ID}})
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	testutil.WaitFor(t, tt, "the failure", func() bool { return cn.Status == connection.StatusFailed })
	tt.Frame()
	testutil.WaitFor(t, tt, "the alert", func() bool { return a.frontAlert() != nil })
	if !tt.HasText("Not connected: ") {
		t.Fatalf("texts %q", tt.Texts())
	}
}

// Connect clicked on a waiting tab raises the failure as an alert.
func TestWaitingTabConnectClickAlerts(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "127.0.0.1", Port: 1, User: "x", Database: "x"})
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	w := &waitingTab{a: a, conn: cn, saved: savedTab{Kind: savedTable, Connection: cn.Config.ID, Schema: "public", Name: "orders", ObjectKind: string(db.KindTable)}}
	a.main.tabs().Add(w)
	tt.Frame()
	if err := tt.Click("Connect"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the alert", func() bool { return a.frontAlert() != nil })
}

// A waiting tab connecting by itself fails inline: no alert, one error note.
func TestWaitingTabAutoConnectDoesNotAlert(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "127.0.0.1", Port: 1, User: "x", Database: "x", AutoConnect: true})
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	notesBefore := len(a.notes)
	w := &waitingTab{a: a, conn: cn, saved: savedTab{Kind: savedTable, Connection: cn.Config.ID, Schema: "public", Name: "orders", ObjectKind: string(db.KindTable)}}
	a.main.tabs().Add(w)
	testutil.WaitFor(t, tt, "the failure", func() bool { return cn.Status == connection.StatusFailed })
	for range 3 {
		tt.Frame()
	}
	added := 0
	for _, n := range a.notes[notesBefore:] {
		if n.isError {
			added++
		}
	}
	if a.frontAlert() != nil || added != 1 {
		t.Fatalf("alert %v, error notes %d", a.frontAlert(), added)
	}
}
