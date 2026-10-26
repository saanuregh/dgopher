package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// mismatchedConn is the PostgreSQL test server set as CockroachDB, its
// password in the keychain as a saved connection's: a mismatch either
// way shows the flow.
func mismatchedConn(t *testing.T, a *App) *connection.Conn {
	t.Helper()
	cfg := testutil.PGConfig()
	cfg.Engine = db.CockroachDB
	cn := addConn(a, cfg)
	if err := a.saveSecrets(&cn.Config, true, nil); err != nil {
		t.Fatal(err)
	}
	cn.Config.Password = ""
	a.saveProject(cn.Project)
	return cn
}

// hasMismatchNote reports whether the notifications say that cn's server
// is PostgreSQL, though it is set to CockroachDB.
func hasMismatchNote(a *App, cn *connection.Conn) bool {
	return slices.ContainsFunc(a.notes, func(n notification) bool {
		return strings.HasPrefix(n.title, cn.Config.Name+": this server is PostgreSQL ") && strings.HasSuffix(n.title, ", but the connection is set to CockroachDB.")
	})
}

// A server of the connection's family that is another engine than the
// connection says is told on connect, with a switch that changes nothing
// until clicked; clicked, it saves the engine as an edit of the
// connection does, and connects again.
func TestConnectMismatchOffersSwitch(t *testing.T) {
	testutil.Integration(t)
	a := newTestApp(t)
	cn := mismatchedConn(t, a)
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	// Test Connection says it too, and switches the form alone.
	a.openConnForm(cn)
	f := a.connForm
	a.testConnection(f)
	testutil.WaitFor(t, tt, "the test", func() bool { return !f.testing })
	if !strings.HasPrefix(f.testResult, "Connected to PostgreSQL ") || !strings.HasSuffix(f.testResult, ", but the connection is set to CockroachDB.") {
		t.Fatalf("test result %q", f.testResult)
	}
	if err := tt.Click("Use PostgreSQL"); err != nil {
		t.Fatal(err)
	}
	if f.config().Engine != db.Postgres || cn.Config.Engine != db.CockroachDB {
		t.Fatalf("the form is set to %q, the connection to %q", f.config().Engine, cn.Config.Engine)
	}
	f.open = false
	tt.Frame()
	connectNow(t, a, tt, cn)
	testutil.WaitFor(t, tt, "the notice", func() bool { return tt.HasText("Use PostgreSQL") })
	if !hasMismatchNote(a, cn) {
		t.Fatalf("no notification of the mismatch: %+v", a.notes)
	}
	if cn.Config.Engine != db.CockroachDB {
		t.Fatalf("the engine changed unasked: %q", cn.Config.Engine)
	}
	if err := tt.Click("Use PostgreSQL"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the switch", func() bool {
		return cn.Config.Engine == db.Postgres && cn.Status == connection.StatusConnected
	})
	if a.connForm != nil && a.connForm.open {
		t.Fatalf("the form stays open: %q", a.connForm.err)
	}
	saved, err := os.ReadFile(filepath.Join(cn.Project.Dir, project.File))
	if err != nil || !strings.Contains(string(saved), `"engine": "postgres"`) {
		t.Fatalf("the project file does not say postgres (%v):\n%s", err, saved)
	}
	tt.Frame()
	if tt.HasText("Use PostgreSQL") {
		t.Fatal("the notice shows again after the switch")
	}
}

// A notice left to close asks again on the next connect; one dismissed
// with Don't Ask Again does not, and the project's state keeps that.
func TestConnectMismatchDismissRemembered(t *testing.T) {
	testutil.Integration(t)
	a := newTestApp(t)
	cn := mismatchedConn(t, a)
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	connectNow(t, a, tt, cn)
	testutil.WaitFor(t, tt, "the notice", func() bool { return tt.HasText("Use PostgreSQL") })
	// A notice that closes on its own asks again on the next connect.
	tt.Advance(time.Minute)
	tt.Frame()
	if tt.HasText("Use PostgreSQL") {
		t.Fatal("the notice does not close")
	}
	a.disconnect(cn)
	connectNow(t, a, tt, cn)
	testutil.WaitFor(t, tt, "the notice again", func() bool { return tt.HasText("Use PostgreSQL") })
	// Don't Ask Again, from the notification, is kept.
	i := slices.IndexFunc(a.notes, func(n notification) bool { return strings.Contains(n.title, ": this server is") })
	if i < 0 {
		t.Fatal("no notification")
	}
	j := slices.IndexFunc(a.notes[i].actions, func(x widgets.ErrorAction) bool { return x.Label == "Don't Ask Again" })
	if j < 0 {
		t.Fatalf("actions %v", a.notes[i].actions)
	}
	a.notes[i].actions[j].Run()
	// The notice from this connect closes, to see that none follows.
	tt.Advance(time.Minute)
	tt.Frame()
	a.disconnect(cn)
	notes := len(a.notes)
	connectNow(t, a, tt, cn)
	for range 5 {
		tt.Frame()
	}
	if tt.HasText("Use PostgreSQL") || slices.ContainsFunc(a.notes[notes:], func(n notification) bool { return strings.Contains(n.title, ": this server is") }) {
		t.Fatal("the dismissed notice shows again")
	}
	if cn.Config.Engine != db.CockroachDB {
		t.Fatalf("the engine changed: %q", cn.Config.Engine)
	}
	// Kept in the project's state, for the next start.
	if v := dismissedVersion(t, cn); !strings.HasPrefix(v, "17.") {
		t.Fatalf("dismissed for version %q", v)
	}
}

// dismissedVersion is the version of the server the notice was dismissed
// for, as the project's state keeps it.
func dismissedVersion(t *testing.T, cn *connection.Conn) string {
	t.Helper()
	var kept map[string]string
	if err := cn.Project.Local.LoadJSON(identityDismissedFile, &kept); err != nil {
		t.Fatal(err)
	}
	engine, version, _ := strings.Cut(kept[localConnID(cn)], " ")
	if engine != string(db.Postgres) || version == "" {
		t.Fatalf("kept %q", kept)
	}
	return version
}

// The new-connection form lists every engine by name, and Neon and
// Supabase as PostgreSQL filled in for them, not as engines.
func TestNewConnectionListsPresets(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.windowView(a.main), 1200, 900)
	a.openConnForm(nil)
	tt.Frame()
	if err := tt.Click("PostgreSQL"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, label := range append(engineLabels(), "Neon (PostgreSQL)", "Supabase (PostgreSQL)") {
		if !tt.HasText(label) {
			t.Errorf("the engine list has no %q: %q", label, tt.Texts())
		}
	}
	if err := tt.Click("Supabase (PostgreSQL)"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	cfg := a.connForm.config()
	if cfg.Engine != db.Postgres || cfg.TLS != db.TLSVerifyFull || cfg.User != "postgres" || cfg.Database != "postgres" {
		t.Fatalf("Supabase filled %+v", cfg)
	}
	if got := a.connForm.hostPlaceholder(); got != "*.supabase.co" {
		t.Errorf("host placeholder %q", got)
	}
	if err := tt.Click("Supabase (PostgreSQL)"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Neon (PostgreSQL)"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if cfg := a.connForm.config(); cfg.Engine != db.Postgres || cfg.TLS != db.TLSVerifyFull || cfg.Database != "neondb" {
		t.Fatalf("Neon filled %+v", cfg)
	}
	if got := a.connForm.hostPlaceholder(); got != "*.neon.tech" {
		t.Errorf("host placeholder %q", got)
	}
}

// postgresServer is the identity of a PostgreSQL server.
var postgresServer = identifiedServer{engine: db.Postgres, version: "17.2"}

// cockroachConn is a connection set to CockroachDB, where nothing listens.
func cockroachConn(a *App, id string) *connection.Conn {
	cn := addConn(a, db.Config{ID: id, Name: id, Engine: db.CockroachDB, Host: "127.0.0.1", Port: 1, User: "x", Database: "x", Env: db.Development})
	a.saveProject(cn.Project)
	return cn
}

// noticeAction is the action labeled label of the notification about
// the connection named name.
func noticeAction(t *testing.T, a *App, name, label string) func() {
	t.Helper()
	for _, n := range a.notes {
		if !strings.HasPrefix(n.title, name+": ") {
			continue
		}
		if j := slices.IndexFunc(n.actions, func(x widgets.ErrorAction) bool { return x.Label == label }); j >= 0 {
			return n.actions[j].Run
		}
	}
	t.Fatalf("no %q for %s in %+v", label, name, a.notes)
	return nil
}

// Each mismatched connection has a notice of its own, naming it, whose
// switch changes that connection alone.
func TestIdentityNoticePerConnection(t *testing.T) {
	a := newTestApp(t)
	one, two := cockroachConn(a, "one"), cockroachConn(a, "two")
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.offerIdentifiedEngine(one, postgresServer)
	a.offerIdentifiedEngine(two, postgresServer)
	tt.Frame()
	for _, name := range []string{"one", "two"} {
		if text := name + ": this server is PostgreSQL 17.2, but the connection is set to CockroachDB."; !tt.HasText(text) {
			t.Fatalf("no notice %q: %q", text, tt.Texts())
		}
	}
	noticeAction(t, a, "two", "Use PostgreSQL")()
	if one.Config.Engine != db.CockroachDB || two.Config.Engine != db.Postgres {
		t.Fatalf("one is %q, two %q", one.Config.Engine, two.Config.Engine)
	}
	noticeAction(t, a, "one", "Use PostgreSQL")()
	if one.Config.Engine != db.Postgres {
		t.Fatalf("one is %q", one.Config.Engine)
	}
}

// A switch to a connection whose password the keychain could not read
// leaves the form open, to type it, rather than saving without it.
func TestIdentitySwitchAsksUnreadPassword(t *testing.T) {
	cfg := lockedPostgres
	cfg.Engine = db.CockroachDB
	a, _, cn := newLockedKeychainApp(t, cfg)
	a.offerIdentifiedEngine(cn, postgresServer)
	noticeAction(t, a, "pg", "Use PostgreSQL")()
	f := a.connForm
	if f == nil || !f.open || !strings.Contains(f.err, "password") || f.engine != db.Postgres.Label() {
		t.Fatalf("form %+v", f)
	}
	if cn.Config.Engine != db.CockroachDB {
		t.Fatalf("saved as %q", cn.Config.Engine)
	}
}

// A switch while a connection form is open leaves that form as it is.
func TestIdentitySwitchKeepsOpenForm(t *testing.T) {
	a := newTestApp(t)
	cn := cockroachConn(a, "crdb")
	a.offerIdentifiedEngine(cn, postgresServer)
	a.openConnForm(nil)
	f := a.connForm
	f.cfg.Name = "being typed"
	noticeAction(t, a, "crdb", "Use PostgreSQL")()
	if a.connForm != f || !f.open || f.cfg.Name != "being typed" || cn.Config.Engine != db.CockroachDB {
		t.Fatalf("the form was replaced, or the connection is %q", cn.Config.Engine)
	}
	if !slices.ContainsFunc(a.toasts, func(p pendingToast) bool { return strings.Contains(p.text, "connection form") }) {
		t.Fatalf("not told why: %+v", a.toasts)
	}
}

// A switch offered for a server the connection no longer goes to, or
// for an engine since changed, does nothing.
func TestIdentitySwitchStale(t *testing.T) {
	a := newTestApp(t)
	moved, changed := cockroachConn(a, "moved"), cockroachConn(a, "changed")
	a.offerIdentifiedEngine(moved, postgresServer)
	a.offerIdentifiedEngine(changed, postgresServer)
	moved.Config.Port = 2
	changed.Config.Engine = db.YugabyteDB
	noticeAction(t, a, "moved", "Use PostgreSQL")()
	noticeAction(t, a, "changed", "Use PostgreSQL")()
	if moved.Config.Engine != db.CockroachDB || changed.Config.Engine != db.YugabyteDB || (a.connForm != nil && a.connForm.open) {
		t.Fatalf("switched: %q, %q", moved.Config.Engine, changed.Config.Engine)
	}
}

// A dismissal follows its connection to a new ID.
func TestIdentityDismissalRenamed(t *testing.T) {
	a := newTestApp(t)
	cn := cockroachConn(a, "crdb")
	a.dismissIdentity(cn, postgresServer)
	if err := a.renameConn(cn, "renamed"); err != nil {
		t.Fatal(err)
	}
	if !a.identityDismissed(cn, postgresServer) {
		t.Fatal("the dismissal was lost")
	}
}

// The version shown of a flavor's server is its own, as identified; of
// another server, what it says.
func TestShownVersion(t *testing.T) {
	cockroach := identifiedServer{engine: db.CockroachDB, version: "25.2.1"}
	for _, c := range []struct {
		server identifiedServer
		set    db.Engine
		want   string
	}{
		{cockroach, db.CockroachDB, "25.2.1"},
		{cockroach, db.Postgres, "13.0.0"},
		{identifiedServer{engine: db.CockroachDB}, db.CockroachDB, "13.0.0"},
		{postgresServer, db.Postgres, "13.0.0"},
		{identifiedServer{}, db.Postgres, "13.0.0"},
	} {
		if got := c.server.shownVersion(c.set, "13.0.0"); got != c.want {
			t.Errorf("%+v set to %q: %q, want %q", c.server, c.set, got, c.want)
		}
	}
}
