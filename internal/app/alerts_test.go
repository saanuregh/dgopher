package app

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"
)

// An error raised while another is shown waits behind it: the first says
// it is one of two, and OK brings the second.
func TestSecondErrorWaitsForFirst(t *testing.T) {
	a := newBareApp(t)
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.ShowError("First problem", "the first detail")
	a.ShowError("Second problem", "the second detail")
	tt.Frame()
	if !tt.HasText("First problem (1 of 2)") || !tt.HasText("the first detail") || testutil.HasTextContaining(tt, "Second problem") {
		t.Fatalf("first alert: texts %q", tt.Texts())
	}
	tt.Click("OK")
	testutil.WaitFor(t, tt, "the second alert", func() bool { return tt.HasText("Second problem") })
	if !tt.HasText("the second detail") || testutil.HasTextContaining(tt, "First problem") || testutil.HasTextContaining(tt, "(1 of") {
		t.Fatalf("second alert: texts %q", tt.Texts())
	}
	tt.Click("OK")
	testutil.WaitFor(t, tt, "no alert", func() bool { return !testutil.HasTextContaining(tt, "Second problem") })
}

// The same error raised again while it waits is not queued twice, so a
// burst of one failure needs one OK. One raised again while it is shown,
// as by its own Retry failing alike, is queued: the user sees the answer.
func TestRepeatedErrorQueuedOnce(t *testing.T) {
	a := newBareApp(t)
	a.ShowError("Other problem", "other detail")
	a.ShowError("Same problem", "same detail")
	a.ShowError("Same problem", "same detail")
	if len(a.alerts) != 2 {
		t.Fatalf("queued %d alerts, want 2", len(a.alerts))
	}
	a.ShowError("Other problem", "other detail")
	if len(a.alerts) != 3 {
		t.Fatalf("a repeat of the shown alert: queued %d alerts, want 3", len(a.alerts))
	}
}

// A connection that fails to connect, asked for where nothing else shows
// the failure, raises an alert that offers to edit the connection or to
// try again; Retry connects again, and the failure's kind is kept.
func TestConnectFailureOffersRetryAndEdit(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: filepath.Join(t.TempDir(), "no", "x.sqlite")})
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "the alert", func() bool { return a.frontAlert() != nil })
	tt.Frame()
	al := a.frontAlert()
	var labels []string
	for _, act := range al.report.Actions {
		labels = append(labels, act.Label)
	}
	if al.report.Title != "Could not connect to lite" || al.report.Detail != cn.Err || !slices.Equal(labels, []string{"Edit Connection", "Retry"}) {
		t.Fatalf("the alert: %+v, actions %q", al.report, labels)
	}
	if cn.ErrKind != db.ClassifyConnectError(errors.New(cn.Err)) || !tt.HasText("Retry") || !tt.HasText("Edit Connection") {
		t.Fatalf("kind %v; texts %q", cn.ErrKind, tt.Texts())
	}
	if err := tt.Click("Retry"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the alert to close", func() bool { return a.frontAlert() != al })
	if cn.Status != connection.StatusConnecting && cn.Status != connection.StatusFailed {
		t.Fatalf("after Retry: status %v", cn.Status)
	}
	// Only a connect that ran again fails again, with its own alert.
	testutil.WaitFor(t, tt, "the alert again", func() bool { return a.frontAlert() != nil })
	if cn.Status != connection.StatusFailed {
		t.Fatalf("after the retry failed: status %v", cn.Status)
	}
	tt.Frame()
	if err := tt.Click("Edit Connection"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the connection's form", func() bool { return a.frontAlert() == nil && a.connForm != nil })
	if a.connForm.editing != cn {
		t.Fatalf("the form edits %+v", a.connForm.editing)
	}
}

// A refused connection is kept as refused, and its alert says so in
// plain words.
func TestConnectFailureKeepsItsKind(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "127.0.0.1", Port: 1, User: "x", Database: "x"})
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "the alert", func() bool { return a.frontAlert() != nil })
	if cn.ErrKind != db.ProblemRefused || a.frontAlert().report.Hint != db.ProblemRefused.Hint() {
		t.Fatalf("kind %v, alert %+v", cn.ErrKind, a.frontAlert().report)
	}
	tt.Frame()
	if !tt.HasText(db.ProblemRefused.Hint()) {
		t.Fatalf("texts %q", tt.Texts())
	}
}

// A catalog that could not be read shows the error's first line in the
// sidebar, the whole on hover; it raises the error with the whole text and
// Refresh as it shows, and again on a click; Refresh reads the catalog
// again.
func TestSidebarLoadErrorShowsFullTextAndCard(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.activate(navNode{kind: nodeConn, conn: cn.Config.ID})
	testutil.WaitFor(t, tt, "the tables", func() bool { return len(cn.Objects) > 0 })
	var key connection.SchemaKey
	for k := range cn.Objects {
		key = k
	}
	const full = "permission denied for schema main\nDETAIL: the role cannot read it"
	delete(cn.Objects, key)
	cn.LoadErr[key] = full
	tt.Frame()
	a.alerts = nil
	tt.Frame()
	row := "Failed: permission denied for schema main"
	r, ok := tt.Find(row)
	if !ok {
		t.Fatalf("no %q in %q", row, tt.Texts())
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(2 * time.Second)
	tt.Frame()
	if !testutil.HasTextContaining(tt, "DETAIL: the role cannot read it") {
		t.Fatalf("no tooltip with the whole error: %q", tt.Texts())
	}
	if err := tt.Click(row); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the alert", func() bool { return a.frontAlert() != nil })
	rep := a.frontAlert().report
	if rep.Title != "Could not read the tables" || rep.Detail != full || len(rep.Actions) != 1 || rep.Actions[0].Label != "Refresh" {
		t.Fatalf("the alert: %+v", rep)
	}
	tt.Frame()
	if err := tt.Click("Refresh"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the tables again", func() bool { return cn.LoadErr[key] == "" && len(cn.Objects) > 0 })
}

// A failed connection's tab says why once, in words, in the editor's
// toolbar; the status bar does not repeat it.
func TestFailedTabSaysWhyOnce(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "127.0.0.1", Port: 1, User: "x", Database: "x"})
	cn.Status, cn.Err, cn.ErrKind = connection.StatusFailed, "dial tcp 127.0.0.1:1: connect: connection refused", db.ProblemRefused
	a.tabs().Add(query.New(a, cn, "", "q.sql", ""))
	tt := ui.NewTester(a.windowView(a.main), 1400, 760)
	tt.Frame()
	hint := 0
	for _, s := range tt.Texts() {
		if strings.Contains(s, db.ProblemRefused.Hint()) {
			hint++
		}
	}
	if hint != 1 || slices.Contains(tt.Texts(), "failed") || slices.Contains(tt.Texts(), "disconnected") {
		t.Fatalf("the hint %d times; texts %q", hint, tt.Texts())
	}
	testutil.Snapshot(t, tt, "failed-tab")
}

// A connect asked for by a view that shows its failure itself raises no
// alert, but the failure is noted once, with its hint.
func TestInlineConnectFailureIsNoted(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "127.0.0.1", Port: 1, User: "x", Database: "x"})
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	before := len(a.notes)
	a.ConnectInline(cn, nil)
	testutil.WaitFor(t, tt, "the failure", func() bool { return cn.Status == connection.StatusFailed })
	tt.Frame()
	added := a.notes[before:]
	if a.frontAlert() != nil || len(added) != 1 {
		t.Fatalf("alert %v, notes %+v", a.frontAlert(), added)
	}
	n := added[0]
	if !n.isError || n.title != "Could not connect to pg" || n.hint != db.ProblemRefused.Hint() || n.detail != cn.Err || len(n.actions) != 2 {
		t.Fatalf("note %+v", n)
	}
}

// A notification's actions act only on a connection that still exists:
// Retry on a deleted one connects nothing.
func TestNotedRetryIgnoresDeletedConnection(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "127.0.0.1", Port: 1, User: "x", Database: "x"})
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.ConnectInline(cn, nil)
	testutil.WaitFor(t, tt, "the failure", func() bool { return cn.Status == connection.StatusFailed })
	n := a.notes[len(a.notes)-1]
	a.conns = slices.DeleteFunc(a.conns, func(x *connection.Conn) bool { return x == cn })
	for _, act := range n.actions {
		if act.Label == "Retry" {
			act.Run()
		}
	}
	if cn.Status != connection.StatusFailed {
		t.Fatalf("the deleted connection was connected again: status %v", cn.Status)
	}
}

// A toast's action is not kept in the notifications, where it could run
// again long after, as an Undo applied twice.
func TestToastActionNotKeptInNotifications(t *testing.T) {
	a := newBareApp(t)
	a.Toast("Removed the panel", "Undo", func() {})
	if n := a.notes[len(a.notes)-1]; len(n.actions) != 0 {
		t.Fatalf("note %+v", n)
	}
}

// A connect asked for that first asks for the password still raises its
// failure once the password is given: the prompt does not make it quiet.
func TestConnectAfterPasswordPromptAlerts(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "127.0.0.1", Port: 1, User: "x", Database: "x", AskPassword: true})
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "the password prompt", func() bool { return a.prompt != nil })
	a.prompt.onSubmit("wrong")
	testutil.WaitFor(t, tt, "the failure", func() bool { return cn.Status == connection.StatusFailed })
	tt.Frame()
	if al := a.frontAlert(); al == nil || al.report.Title != "Could not connect to pg" {
		t.Fatalf("no alert after the prompt: %v", al)
	}
}
