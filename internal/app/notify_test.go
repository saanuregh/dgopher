package app

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/settings"
	"dgopher/internal/store"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"

	"github.com/egoist/mygo/ui"
)

// An older DGopher saved 0 seconds for never: it starts with long work
// off and the time back at 10 seconds. The alerts chosen survive a
// restart, and one this version does not know reads as both.
func TestNotifySettingsMigrate(t *testing.T) {
	st, err := store.Open(t.TempDir(), store.MemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveJSON("settings.json", map[string]any{"notifyAfterSeconds": 0}); err != nil {
		t.Fatal(err)
	}
	a := startApp(t, st)
	if a.settings.NotifyLongWork != settings.AlertOff || a.settings.NotifyAfter != 10 {
		t.Fatalf("never became %q after %d seconds", a.settings.NotifyLongWork, a.settings.NotifyAfter)
	}
	if a.settings.NotifyAtRisk != settings.AlertBoth {
		t.Fatalf("at risk %q", a.settings.NotifyAtRisk)
	}

	a.settings.NotifyLongWork, a.settings.NotifyAtRisk, a.settings.NotifyAfter = settings.AlertSound, settings.AlertNotification, 30
	a.SaveSettings()
	b := startApp(t, st)
	if b.settings.NotifyLongWork != settings.AlertSound || b.settings.NotifyAtRisk != settings.AlertNotification || b.settings.NotifyAfter != 30 {
		t.Fatalf("restarted with %q, %q, %d", b.settings.NotifyLongWork, b.settings.NotifyAtRisk, b.settings.NotifyAfter)
	}

	fresh := startApp(t, mustStore(t))
	if fresh.settings.NotifyLongWork != settings.AlertBoth || fresh.settings.NotifyAtRisk != settings.AlertBoth || fresh.settings.NotifyAfter != 10 {
		t.Fatalf("first start %q, %q, %d", fresh.settings.NotifyLongWork, fresh.settings.NotifyAtRisk, fresh.settings.NotifyAfter)
	}

	for _, c := range []struct {
		a                  settings.Alert
		on, desktop, sound bool
	}{
		{settings.AlertOff, false, false, false},
		{settings.AlertNotification, true, true, false},
		{settings.AlertSound, true, false, true},
		{settings.AlertBoth, true, true, true},
		{"later", true, true, true},
	} {
		if c.a.On() != c.on || c.a.Desktop() != c.desktop || c.a.Sound() != c.sound {
			t.Errorf("%q: on %v, desktop %v, sound %v", c.a, c.a.On(), c.a.Desktop(), c.a.Sound())
		}
	}
}

func mustStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir(), store.MemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// A tab's work tells of its end unless its own window has the focus, and
// app-wide work unless any window has it; quick work is silent, work at
// risk is not. The settings choose a notification, a sound, both or
// nothing, and a click brings the tab forward in its own window.
func TestNotify(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	a.NewQueryTab(cn, "", "SELECT 1")
	a.NewQueryTab(cn, "", "SELECT 2")
	testutil.WaitFor(t, tt, "the editors", func() bool { return a.main.tabs().Len() == 2 })
	first, second := a.main.tabs().At(0), a.main.tabs().At(1)
	a.moveToWindow(second, nil)
	other := a.windows[1]
	focused := map[*window]bool{a.main: true}
	a.windowFocused = func(w *window) bool { return focused[w] }
	var got []alerting
	a.deliverNotice = func(x alerting) { got = append(got, x) }
	long := time.Now().Add(-time.Minute)

	a.Notify(dataview.Notice{Started: long, Title: "Query finished", Body: "second", Tab: second})
	a.Notify(dataview.Notice{Started: long, Title: "Query finished", Body: "first", Tab: first})
	a.Notify(dataview.Notice{Started: long, Title: "Backed up", Body: "app-wide"})
	if len(got) != 1 || got[0].Body != "second" || got[0].window != other || !got[0].desktop || !got[0].sound {
		t.Fatalf("with the main window focused: %+v", got)
	}

	focused[a.main] = false
	a.Notify(dataview.Notice{Started: time.Now(), Title: "Query finished", Body: "quick", Tab: first})
	a.Notify(dataview.Notice{AtRisk: true, Title: "Lost lite", ID: "route:lite"})
	if len(got) != 2 || got[1].Title != "Lost lite" || got[1].ID != "route:lite" || got[1].window != a.main {
		t.Fatalf("quick work and work at risk: %+v", got[1:])
	}

	for _, c := range []struct {
		alert          settings.Alert
		desktop, sound bool
	}{
		{settings.AlertNotification, true, false},
		{settings.AlertSound, false, true},
	} {
		a.settings.NotifyLongWork = c.alert
		a.Notify(dataview.Notice{Started: long, Title: "Query finished", Tab: first})
		if x := got[len(got)-1]; len(got) != 3 || x.desktop != c.desktop || x.sound != c.sound {
			t.Fatalf("%q: %+v", c.alert, got[2:])
		}
		got = got[:2]
	}
	a.settings.NotifyLongWork, a.settings.NotifyAtRisk = settings.AlertOff, settings.AlertOff
	a.Notify(dataview.Notice{Started: long, Title: "Query finished", Tab: first})
	a.Notify(dataview.Notice{AtRisk: true, Title: "Lost lite"})
	if len(got) != 2 {
		t.Fatalf("off: %+v", got[2:])
	}

	a.settings.NotifyLongWork = settings.AlertBoth
	shown := false
	a.Notify(dataview.Notice{Started: long, Title: "Query finished", Tab: second, Show: func() { shown = true }})
	a.lastFocused, a.window = a.main, a.main
	got[2].onClick()
	tt.Frame()
	if !shown || a.window != other || other.ActiveTab() != second {
		t.Fatalf("the click: shown %v, window is the other %v", shown, a.window == other)
	}
}

// recordAlerts has a's notices recorded as the system would tell them,
// however quick the work, with no window in front.
func recordAlerts(a *App) *[]alerting {
	got := &[]alerting{}
	a.settings.NotifyAfter = 0
	a.windowFocused = func(*window) bool { return false }
	a.deliverNotice = func(x alerting) { *got = append(*got, x) }
	return got
}

func titles(got []alerting) []string {
	var ts []string
	for _, x := range got {
		ts = append(ts, x.Title)
	}
	return ts
}

// An idle transaction tells at once that it is about to roll back, and
// then that it did, in the same notification; long work's setting does
// not silence it.
func TestIdleTxNotifiesAtRisk(t *testing.T) {
	a, tt, q, _ := txApp(t, 13)
	got := recordAlerts(a) // long work tells too, but not of the rollback the app made
	testutil.WaitFor(t, tt, "the warning", func() bool { return a.idleWarn != nil })
	if len(*got) != 1 || (*got)[0].Title != "Transaction rolls back soon" || (*got)[0].ID == "" || !strings.Contains((*got)[0].Body, q.Title()) {
		t.Fatalf("warned %+v", *got)
	}
	a.idleWarn.deadline = a.now
	testutil.WaitFor(t, tt, "the rollback", func() bool { return !q.OpenTx() && len(*got) >= 2 })
	tt.Frame()
	if x := (*got)[1]; len(*got) != 2 || x.Title != "Rolled back idle transaction" || x.ID != (*got)[0].ID {
		t.Fatalf("rolled back %q", titles(*got))
	}
}

// A tunnel lost under a connection tells at once.
func TestRouteLostNotifiesAtRisk(t *testing.T) {
	a, tt, q, _ := txApp(t, 0)
	got := recordAlerts(a)
	cn := q.Conn
	done := make(chan struct{})
	a.watchRoute(cn, cn.Generation, done, func() error { return errors.New("keepalive timed out") })
	close(done)
	testutil.WaitFor(t, tt, "the notice", func() bool { return len(*got) == 1 })
	if x := (*got)[0]; x.Title != "Lost lite" || !strings.Contains(x.Body, q.Title()) {
		t.Fatalf("notified %+v", x)
	}
}

// Work lost while the computer slept tells at once.
func TestLostAfterWakeNotifiesAtRisk(t *testing.T) {
	a, tt, q := lostTxApp(t)
	got := recordAlerts(a)
	a.checkConnectionsAfterWake()
	testutil.WaitFor(t, tt, "the notice", func() bool { return len(*got) == 1 })
	if x := (*got)[0]; x.Title != "Transaction lost while asleep" || !strings.Contains(x.Body, q.Title()) {
		t.Fatalf("notified %+v", x)
	}
}

// A connect that fails tells of it, as long work.
func TestConnectFailureNotifies(t *testing.T) {
	a := newTestApp(t)
	got := recordAlerts(a)
	cn := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "127.0.0.1", Port: 1, User: "x", Database: "x"})
	tt := ui.NewTester(a.view, 1000, 700)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "the failure", func() bool { return cn.Status == connection.StatusFailed })
	if slices.Index(titles(*got), "Could not connect pg") < 0 {
		t.Fatalf("notified %q", titles(*got))
	}
}

// Reading rows to compare tells of its end.
func TestRowCompareReadNotifies(t *testing.T) {
	a := newTestApp(t)
	got := recordAlerts(a)
	lite := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", "")})
	duck := addConn(a, db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1200, 800)
	for _, cn := range []*connection.Conn{lite, duck} {
		connectNow(t, a, tt, cn)
	}
	mustExec(t, lite.DB(), `CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)`, `INSERT INTO items VALUES (1, 'pen')`)
	mustExec(t, duck.DB(), `CREATE TABLE items (id INTEGER PRIMARY KEY, name VARCHAR)`, `INSERT INTO items VALUES (1, 'ink')`)
	a.openRowCompare(lite, "", db.Object{Schema: "main", Name: "items", Kind: db.KindTable})
	x := a.rowCompare
	x.target.label = targetLabel(duck)
	testutil.WaitFor(t, tt, "the table chosen", func() bool { return x.table == "items" })
	a.startCompare(x)
	testutil.WaitFor(t, tt, "the comparison", func() bool { return !x.running })
	if !slices.Contains(titles(*got), "Compare finished") {
		t.Fatalf("notified %q", titles(*got))
	}
	mustExec(t, duck.DB(), `DROP TABLE items`)
	a.startCompare(x)
	testutil.WaitFor(t, tt, "the comparison", func() bool { return !x.running })
	if x.err == "" || !slices.Contains(titles(*got), "Compare failed") {
		t.Fatalf("error %q, notified %q", x.err, titles(*got))
	}
}

// Generating a script tells of its end.
func TestGenerateNotifies(t *testing.T) {
	a, tt, cn, _ := liteApp(t, "")
	got := recordAlerts(a)
	mustExec(t, cn.DB(), `CREATE TABLE orders (id INTEGER PRIMARY KEY)`)
	a.openGenerate(cn, "", "main", generateScript)
	g := a.generating
	testutil.WaitFor(t, tt, "the objects", func() bool { return !g.loading })
	a.generate(g)
	testutil.WaitFor(t, tt, "the script", func() bool { return !g.running })
	if !slices.Contains(titles(*got), "Script generated") {
		t.Fatalf("notified %q", titles(*got))
	}
}

// A restore that fails says so, not that a backup failed.
func TestRestoreFailureTitle(t *testing.T) {
	a := newTestApp(t)
	got := recordAlerts(a)
	t.Setenv("TMPDIR", t.TempDir())
	cn := addConn(a, db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1200, 800)
	connectNow(t, a, tt, cn)
	a.openBackup(cn, "", true)
	b := a.backup
	b.path = t.TempDir() // an empty folder: no export to restore
	a.startBackup(b)
	testutil.WaitFor(t, tt, "the end", func() bool {
		if a.confirm != nil {
			a.confirm.OnConfirm()
			a.confirm = nil
		}
		return !b.running && (b.err != "" || b.done)
	})
	if b.done || !slices.Contains(titles(*got), "Restore failed") {
		t.Fatalf("done %v, error %q, notified %q", b.done, b.err, titles(*got))
	}
}
