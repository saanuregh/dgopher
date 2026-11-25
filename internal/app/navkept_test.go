package app

import (
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/datamodel"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/state"
	"dgopher/internal/store"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dashboard"

	"github.com/egoist/mygo/ui"
)

// filesOfEveryKind gives the first project a dashboard and a data model:
// their headings show only once there is one.
func filesOfEveryKind(t *testing.T, a *App) {
	t.Helper()
	p := a.projects[0]
	if _, err := (&dashboard.Dashboard{Name: "Sales"}).Save(dashboard.File(p, "Sales"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := (&datamodel.Model{Name: "Shop", Engine: db.SQLite}).Save(datamodel.File(p, "Shop"), nil); err != nil {
		t.Fatal(err)
	}
}

// A new project has one heading over its connections, Queries: those of
// its dashboards and data models come with the first of each, which the
// sidebar's + makes.
func TestEmptySectionsHaveNoHeading(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()
	shows := func(text string) bool { _, ok := tt.Find(text); return ok }
	if !shows("Queries") || shows("Dashboards") || shows("Data Models") {
		t.Fatalf("a new project's headings are in %q, want Queries alone", tt.Texts())
	}
	if err := tt.Click("New…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !shows("New Dashboard…") || !shows("New Data Model…") {
		t.Errorf("the sidebar's + offers %q, not a dashboard and a data model", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	filesOfEveryKind(t, a)
	testutil.WaitFor(t, tt, "the headings", func() bool { return shows("Dashboards") && shows("Data Models") })
}

// A heading left closed is closed in the next run, and one left open
// opens as it first shows.
func TestClosedHeadingsAreRemembered(t *testing.T) {
	st, err := store.Open(t.TempDir(), store.MemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	run := func() (*App, *ui.Tester) {
		db, err := state.OpenUI(data)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		a := startApp(t, st)
		a.useUIState(db)
		return a, ui.NewTester(a.view, 1000, 700)
	}
	a, tt := run()
	if _, err := a.addProject(newProjectDir(t, "proj")); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	conns := navNode{kind: nodeConnections, projectDir: a.projects[0].Dir}
	queries := navNode{kind: nodeQueries, projectDir: a.projects[0].Dir}
	if !a.nav.isOpen(conns) || !a.nav.isOpen(queries) {
		t.Fatal("the headings do not open as they first show")
	}
	a.nav.collapse(conns)
	tt.Frame()
	tt.Frame()
	if !strings.Contains(a.layout.NavClosed, "connections:") || strings.Contains(a.layout.NavClosed, "queries:") {
		t.Fatalf("with Connections closed, the layout keeps %q", a.layout.NavClosed)
	}
	b, bt := run()
	bt.Frame()
	bt.Frame()
	if len(b.projects) != 1 {
		t.Fatalf("the next run lists %d projects", len(b.projects))
	}
	if b.nav.isOpen(conns) || !b.nav.isOpen(queries) {
		t.Errorf("in the next run Connections is open: %v, Queries: %v, and the layout kept %q", b.nav.isOpen(conns), b.nav.isOpen(queries), b.layout.NavClosed)
	}
	// Opened again, it is open in the run after.
	b.nav.expand(conns)
	bt.Frame()
	bt.Frame()
	if c, ct := run(); true {
		ct.Frame()
		ct.Frame()
		if !c.nav.isOpen(conns) {
			t.Errorf("opened again, Connections is closed in the run after: the layout kept %q", c.layout.NavClosed)
		}
	}
}

// With several projects, each has the heading of its connections under
// its row in the database tree, with their count, as with one project.
func TestEachProjectHasItsConnectionsHeading(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.addProject(newProjectDir(t, "other")); err != nil {
		t.Fatal(err)
	}
	addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	for _, p := range a.projects {
		a.nav.expand(navNode{kind: nodeProject, projectDir: p.Dir})
	}
	tt.Frame()
	tt.Frame()
	var kinds []nodeKind
	for i := range a.nav.tree.Rows() {
		kinds = append(kinds, a.nav.tree.Item(i).kind)
	}
	// A project, its heading and its connection; the other, its heading
	// and the note that it has none.
	want := []nodeKind{nodeProject, nodeConnections, nodeConn, nodeProject, nodeConnections, nodeInfo}
	if len(kinds) != len(want) {
		t.Fatalf("the tree's rows are %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("the tree's rows are %v, want %v", kinds, want)
		}
	}
	// A heading does not indent: the connection starts where it does.
	heading, _ := tt.Find("Connections")
	if conn, ok := tt.Find("lite"); !ok || conn.X-heading.X > navArrow+26+8 {
		t.Errorf("the connection's name starts %v right of its heading's, more than its badge", conn.X-heading.X)
	}
}

// The new editor's picker lists the databases read of a server the last
// time it was open, while it is not.
func TestPickerListsTheDatabasesKnownOfAClosedServer(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "db.invalid", Port: 5432, Database: "postgres"})
	addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	// As read while it was open.
	cn.Status, cn.Databases = connection.StatusConnected, []string{"audit", "postgres", "reports"}
	a.keepKnownDatabases()
	cn.Status, cn.Databases = connection.StatusIdle, nil
	tt := ui.NewTester(a.view, 1200, 760)
	tt.Frame()
	a.menuAction(keymap.NewEditor)()
	tt.Frame()
	if a.palette == nil {
		t.Fatal("no picker")
	}
	var details []string
	for _, it := range a.palette.shown {
		if it.conn == cn {
			details = append(details, it.detail)
		}
	}
	if got := strings.Join(details, ", "); got != "audit · not connected, postgres · not connected, reports · not connected" {
		t.Errorf("the picker lists pg's databases as %q", got)
	}
}

// A server whose databases were never read has a row in the picker that
// connects it to read them, and leaves the picker open, which lists them
// as they come.
func TestPickerConnectsAServerToListItsDatabases(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "db.invalid", Port: 5432, Database: "postgres"})
	addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1200, 760)
	tt.Frame()
	a.menuAction(keymap.NewEditor)()
	tt.Frame()
	rows := func() []string {
		var out []string
		for _, it := range a.palette.shown {
			if it.conn == cn {
				out = append(out, it.detail)
			}
		}
		return out
	}
	if got := strings.Join(rows(), ", "); got != "postgres · not connected, connect to list its databases" {
		t.Fatalf("the picker's rows of pg are %q", got)
	}
	// The row that connects leaves the picker open.
	for i, it := range a.palette.shown {
		if it.stay {
			a.palette.index = i
		}
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if a.palette == nil || cn.Status == connection.StatusIdle {
		t.Fatalf("↵ on the row that connects left the picker open: %v, and pg %v", a.palette != nil, cn.Status)
	}
	// As its databases are read, the picker, still open, lists them, and
	// the row that connects is gone.
	cn.Status, cn.Databases = connection.StatusConnected, []string{"audit", "postgres", "reports"}
	tt.Frame()
	tt.Frame()
	got := rows()
	if len(got) != 3 || !strings.HasPrefix(got[0], "audit") || !strings.HasPrefix(got[1], "postgres") || !strings.HasPrefix(got[2], "reports") {
		t.Errorf("once its databases are read, the picker's rows of pg are %q", got)
	}
}

// Two notes of one text under two nodes are two rows to the tree.
func TestNotesAreToldApartByWhatTheyAreUnder(t *testing.T) {
	a := navNode{kind: nodeDatabase, conn: "c", database: "one"}
	b := navNode{kind: nodeDatabase, conn: "c", database: "two"}
	if info(a, "Loading…")[0] == info(b, "Loading…")[0] {
		t.Error("Loading… under two databases is one node")
	}
	if info(a, "Loading…")[0] != info(a, "Loading…")[0] {
		t.Error("a note is another node each time it is made: its row would lose its place")
	}
}

// Deleting a connection forgets what was kept by its ID: a new connection
// given the ID does not take the old one's tables' settings, recent
// tables, databases, nor the agreement to where it connects.
func TestDeletedConnectionsStateIsForgotten(t *testing.T) {
	a := newTestApp(t)
	p := a.projects[0]
	cn := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "db.invalid", Port: 5432, Database: "postgres"})
	other := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	a.saveProject(p)
	cn.Status, cn.Databases = connection.StatusConnected, []string{"audit", "postgres", "reports"}
	a.keepKnownDatabases()
	cn.Status, cn.Databases = connection.StatusIdle, nil
	a.recordRecentTable(cn, "", db.Object{Schema: "public", Name: "orders"})
	a.recordRecentTable(other, "", db.Object{Schema: "main", Name: "kept"})
	p.VirtualKeys = map[string][]string{"pg/public.orders": {"id"}, "lite/main.kept": {"id"}}
	if len(loadKnownDatabases(p)["pg"]) != 3 || len(loadRecentTables(p)) != 2 || a.settings.TrustedDetails[trustKey(cn)].Rows == nil {
		t.Fatalf("not kept to begin with: databases %q, recent %d, trusted %v", loadKnownDatabases(p)["pg"], len(loadRecentTables(p)), a.settings.TrustedDetails[trustKey(cn)].Rows != nil)
	}
	key := trustKey(cn)
	tt := ui.NewTester(a.view, 1000, 700)
	a.askDeleteConn(cn)
	tt.Frame()
	if err := tt.Click("Delete"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.connByID(cn.Config.ID) != nil {
		t.Fatal("the connection is still there")
	}
	if got, kept := loadKnownDatabases(p)["pg"]; kept {
		t.Errorf("its databases are still kept: %q", got)
	}
	if recent := loadRecentTables(p); len(recent) != 1 || !strings.HasPrefix(recent[0].Key, "lite/") {
		t.Errorf("the recent tables are %+v, want the other connection's alone", recent)
	}
	if _, kept := p.VirtualKeys["pg/public.orders"]; kept || len(p.VirtualKeys["lite/main.kept"]) != 1 {
		t.Errorf("the tables' settings are %v, want the other connection's alone", p.VirtualKeys)
	}
	if _, kept := a.settings.TrustedDetails[key]; kept {
		t.Error("the agreement to where it connects is still kept")
	}
	if _, kept := a.settings.TrustedDetails[trustKey(other)]; !kept {
		t.Error("the other connection's agreement went with it")
	}
}
