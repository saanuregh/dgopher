package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// Once a project has connections, the start page leads with what there
// is to open: the connections, then the query files last worked on, the
// latest first. How to begin is a row of buttons under them, not four
// cards and a banner over them.
func TestStartPageLeadsWithWhatThereIsToOpen(t *testing.T) {
	a := newTestApp(t)
	file := writeSQL(t, "p.sqlite", "")
	addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	p := a.projects[0]
	os.MkdirAll(p.Queries, 0o755)
	for i, name := range []string{"old.sql", "new.sql"} {
		path := filepath.Join(p.Queries, name)
		os.WriteFile(path, []byte("-- connection: lite\nSELECT 1;"), 0o644)
		when := time.Now().Add(time.Duration(i-2) * time.Hour)
		os.Chtimes(path, when, when)
	}
	a.ScanQueries(p, true)
	tt := ui.NewTester(a.view, 1200, 760)
	testutil.WaitFor(t, tt, "the query files", func() bool { return len(p.Files) == 2 })
	tt.Frame()
	if tt.HasText("Dig into your databases.") || tt.HasText("Try the sample") {
		t.Errorf("a project with a connection is still shown how to begin: %q", tt.Texts())
	}
	conns, recent, begin := named(t, tt, "Open lite"), named(t, tt, "Open new.sql"), named(t, tt, "Paste a URL…")
	if !(conns.Y < recent.Y && recent.Y < begin.Y) {
		t.Fatalf("the connection at %v, the recent query at %v, Paste a URL at %v: not in that order", conns, recent, begin)
	}
	if !tt.HasText("Recent Queries") {
		t.Error("the query files have no heading")
	}
	// The start page's own rows, not the navigator's of the same files.
	latest, earlier := named(t, tt, "Open new.sql"), named(t, tt, "Open old.sql")
	if latest.Y > earlier.Y {
		t.Errorf("new.sql at %v is under old.sql at %v: the latest comes first", latest, earlier)
	}
	testutil.Snapshot(t, tt, "home")
	if err := tt.Click("Open new.sql"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the editor", func() bool {
		q, ok := a.ActiveTab().(*query.Tab)
		return ok && filepath.Base(q.Path) == "new.sql"
	})
}

func named(t *testing.T, tt *ui.Tester, name string) ui.Rect {
	t.Helper()
	info, ok := tt.Access(name)
	if !ok {
		t.Fatalf("nothing is named %q in %q", name, tt.Texts())
	}
	return info.Bounds
}

// The tab in front carries a line in the accent along its top, with one
// group as with several: its background alone is too near the others'.
func TestFrontTabCarriesALine(t *testing.T) {
	a := newTestApp(t)
	file := writeSQL(t, "p.sqlite", "")
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.view, 1200, 760)
	a.NewQueryTab(cn, "", "SELECT 1")
	a.NewQueryTab(cn, "", "SELECT 2")
	tt.Frame()
	tt.Advance(time.Second) // the tabs have come in
	tt.Frame()
	var accent ui.Color
	widgets.UseTheme(nil)
	accent = ui.LightTheme().Accent
	// A tab's item is the control of its title in the tab bar, along the
	// window's top; the navigator lists the same title lower down.
	line := func(tab string) ui.Color {
		for _, info := range tt.AccessAll() {
			if r := info.Bounds; info.Name == tab && r.Y < 10 && r.W > 0 {
				p := tt.Image().RGBAAt(int(r.X+r.W/2), 1)
				return ui.RGB(p.R, p.G, p.B)
			}
		}
		t.Fatalf("no tab is named %q", tab)
		return ui.Color{}
	}
	front := a.ActiveTab().Title()
	if got := line(front); got != accent {
		t.Errorf("the tab in front has %s along its top, want the accent %s", widgets.HexColor(got), widgets.HexColor(accent))
	}
	for _, tb := range a.everyTab() {
		if tb.Title() != front && line(tb.Title()) == accent {
			t.Errorf("%s, in the back, has the accent's line", tb.Title())
		}
	}
}
