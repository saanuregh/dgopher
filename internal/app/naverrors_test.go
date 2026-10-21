package app

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

// failCatalog connects an in-memory SQLite and makes its tables fail to
// read with full, returning the schema's key.
func failCatalog(t *testing.T, a *App, tt *ui.Tester, cn *connection.Conn, full string) connection.SchemaKey {
	t.Helper()
	testutil.WaitFor(t, tt, "the tables", func() bool { return len(cn.Objects) > 0 })
	var key connection.SchemaKey
	for k := range cn.Objects {
		key = k
	}
	delete(cn.Objects, key)
	cn.LoadErr[key] = full
	return key
}

// countAlerts counts the alerts queued with title.
func countAlerts(a *App, title string) int {
	n := 0
	for _, al := range a.alerts {
		if al.report.Title == title {
			n++
		}
	}
	return n
}

// A catalog error raises its dialog the first time its row shows, once
// however many frames show it; after Refresh, a new failure raises again.
func TestCatalogErrorRaisesDialogOnce(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.activate(navNode{kind: nodeConn, conn: cn.Config.ID})
	key := failCatalog(t, a, tt, cn, "permission denied for schema main")
	for range 4 {
		tt.Frame()
	}
	if got := countAlerts(a, "Could not read the tables"); got != 1 {
		t.Fatalf("raised %d times, want 1", got)
	}
	if rep := a.frontAlert().report; rep.Detail != "permission denied for schema main" || len(rep.Actions) != 1 || rep.Actions[0].Label != "Refresh" {
		t.Fatalf("the alert: %+v", rep)
	}
	a.alerts = nil
	for range 4 {
		tt.Frame()
	}
	if got := countAlerts(a, "Could not read the tables"); got != 0 {
		t.Fatalf("raised again %d times while the error stayed", got)
	}
	a.refresh(cn)
	testutil.WaitFor(t, tt, "the tables again", func() bool { return cn.LoadErr[key] == "" && len(cn.Objects) > 0 })
	failCatalog(t, a, tt, cn, "permission denied for schema main")
	for range 3 {
		tt.Frame()
	}
	if got := countAlerts(a, "Could not read the tables"); got != 1 {
		t.Fatalf("after Refresh, a new failure raised %d times, want 1", got)
	}
}

// An error note takes the keyboard's choice; Enter on it raises its dialog.
func TestErrorRowTakesKeyboardSelection(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.activate(navNode{kind: nodeConn, conn: cn.Config.ID})
	failCatalog(t, a, tt, cn, "permission denied for schema main")
	tt.Frame()
	a.alerts = nil
	tt.Frame()
	at := -1
	for i := range a.nav.tree.Rows() {
		if _, ok := a.nav.errors[a.nav.tree.Item(i)]; ok {
			at = i
		}
	}
	if at < 1 {
		t.Fatalf("no error row in the tree")
	}
	testutil.Snapshot(t, tt, "sidebar-catalog-error")
	a.nav.row, a.nav.chose = at-1, at-1
	a.focusWant = "nav"
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if a.nav.row != at {
		t.Fatalf("down chose row %d, want the error's %d", a.nav.row, at)
	}
	tt.Key(0, ui.KeyEnter)
	testutil.WaitFor(t, tt, "the alert", func() bool { return a.frontAlert() != nil })
	if rep := a.frontAlert().report; rep.Title != "Could not read the tables" {
		t.Fatalf("the alert: %+v", rep)
	}
}

// A connection that failed inline raises nothing; Enter or a click on its
// row then raises its error instead of connecting again.
func TestFailedConnectionRowOpensErrorDialog(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "127.0.0.1", Port: 1, User: "x", Database: "x"})
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.ConnectInline(cn, nil)
	testutil.WaitFor(t, tt, "the failure", func() bool { return cn.Status == connection.StatusFailed })
	tt.Frame()
	tt.Frame()
	if a.frontAlert() != nil {
		t.Fatalf("an inline failure raised %+v", a.frontAlert().report)
	}
	node := navNode{kind: nodeConn, conn: cn.Config.ID}
	at := -1
	for i := range a.nav.tree.Rows() {
		if a.nav.tree.Item(i) == node {
			at = i
		}
	}
	if at < 0 {
		t.Fatalf("no row of the connection")
	}
	a.nav.row, a.nav.chose = at, at
	a.focusWant = "nav"
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	testutil.WaitFor(t, tt, "the alert", func() bool { return a.frontAlert() != nil })
	check := func(how string) {
		t.Helper()
		rep := a.frontAlert().report
		var labels []string
		for _, act := range rep.Actions {
			labels = append(labels, act.Label)
		}
		if rep.Title != "Could not connect to pg" || rep.Hint != db.ProblemRefused.Hint() || rep.Detail != cn.Err || !slices.Equal(labels, []string{"Edit Connection", "Retry"}) || cn.Status != connection.StatusFailed {
			t.Fatalf("%s: the alert %+v, status %v", how, rep, cn.Status)
		}
	}
	check("Enter")
	a.alerts = nil
	tt.Frame()
	if err := tt.Click("pg"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the alert", func() bool { return a.frontAlert() != nil })
	check("click")
	if got := countAlerts(a, "Could not connect to pg"); got != 1 {
		t.Fatalf("a click raised %d alerts", got)
	}
}

// A failed connection shows no note under its row: the ⚠ on it says it.
func TestFailedConnectionHasNoHintRow(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "127.0.0.1", Port: 1, User: "x", Database: "x"})
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.nav.expand(navNode{kind: nodeConn, conn: cn.Config.ID})
	a.ConnectInline(cn, nil)
	testutil.WaitFor(t, tt, "the failure", func() bool { return cn.Status == connection.StatusFailed })
	tt.Frame()
	if slices.Contains(tt.Texts(), db.ProblemRefused.Hint()) {
		t.Fatalf("a hint row in %q", tt.Texts())
	}
	for i := range a.nav.tree.Rows() {
		if n := a.nav.tree.Item(i); n.kind == nodeInfo && n.conn == cn.Config.ID {
			t.Fatalf("a note under the failed connection: %+v", n)
		}
	}
	testutil.Snapshot(t, tt, "sidebar-failed-connection")
}
