package dashboard

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/params"
	"dgopher/internal/project"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"

	"github.com/egoist/mygo/ui"
)

// A dashboard's panels run their reads, with the parameters they share,
// as charts, tables and values; a panel that writes, or holds two
// statements, is refused, and nothing it holds runs.
func TestDashboardPanels(t *testing.T) {
	h := dataview.NewFakeHost(t)
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	cn := h.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(h.View, 1280, 800)
	h.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	for _, q := range []string{`CREATE TABLE sales (region TEXT, amount INTEGER)`, `INSERT INTO sales VALUES ('north', 5), ('south', 7), ('north', 3)`} {
		if _, err := cn.DB.SQL.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	d := &Dashboard{Name: "Sales", Refresh: 60, Panels: []Panel{
		{Title: "By region", Connection: "lite", SQL: "SELECT region, sum(amount) AS total FROM sales GROUP BY region ORDER BY region", View: ViewChart, Width: 2},
		{Title: "Big", Connection: "lite", SQL: "SELECT count(*) AS big FROM sales WHERE amount >= :min", View: ViewValue, Width: 1},
		{Title: "Purge", Connection: "lite", SQL: "DELETE FROM sales", View: ViewTable, Width: 1},
		{Title: "Two", Connection: "lite", SQL: "SELECT 1; SELECT 2", View: ViewTable, Width: 1},
		{Title: "Gone", Connection: "nowhere", SQL: "SELECT 1", View: ViewTable, Width: 1},
	}}
	path := File(h.Project, d.Name)
	if _, err := d.Save(path, nil); err != nil {
		t.Fatal(err)
	}
	written, _ := os.ReadFile(path)
	tab, err := Open(h, h.Project, path)
	if err != nil {
		t.Fatal(err)
	}
	tab.values = map[string]params.Input{":min": {Value: "5", Kind: "number"}}
	tab.syncFields()
	h.AddTab(tab)
	testutil.WaitFor(t, tt, "the panels", func() bool {
		return tab.panels[0].src != nil && tab.panels[1].src != nil && !tab.panels[0].running
	})
	if got := len(tab.panels[0].src.Rows); got != 2 {
		t.Fatalf("by region: %d rows", got)
	}
	if v := tab.panels[1].src.Rows[0][0]; db.Display(v) != "2" {
		t.Fatalf("big sales at 5 or more: %v", v)
	}
	for i, want := range map[int]string{2: "reads only", 3: "one statement", 4: "no connection"} {
		if !strings.Contains(tab.panels[i].err, want) {
			t.Errorf("panel %q: %q, want %q", tab.d.Panels[i].Title, tab.panels[i].err, want)
		}
	}
	var n int
	if cn.DB.SQL.QueryRow(`SELECT count(*) FROM sales`).Scan(&n); n != 3 {
		t.Fatalf("%d sales left", n)
	}
	testutil.Snapshot(t, tt, "dashboard-panels")

	// Shown, a dashboard leaves its file as it was.
	tt.Frame()
	if now, _ := os.ReadFile(path); string(now) != string(written) {
		t.Fatalf("opening the dashboard wrote its file:\n%s", now)
	}

	// A parameter applied runs the panels again, and is the user's own:
	// kept apart from the file.
	tab.fields[0].value = "3"
	tab.applyParams()
	testutil.WaitFor(t, tt, "the panel again", func() bool { return !tab.panels[1].running && db.Display(tab.panels[1].src.Rows[0][0]) == "3" })
	if now, _ := os.ReadFile(path); strings.Contains(string(now), `"3"`) {
		t.Fatalf("a parameter's value went into the file:\n%s", now)
	}
	if again, _ := Open(h, h.Project, path); again.values[":min"].Value != "3" {
		t.Fatalf("the parameter's value was not kept: %+v", again.values)
	}

	// Removed while its row is drawn, a panel goes once it is.
	tab.editing = true
	tt.Frame()
	testutil.Settled(t, tt, "Remove")
	panels := len(tab.d.Panels)
	if err := tt.Click("Remove"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(tab.d.Panels) != panels-1 || tab.d.Panels[0].Title != "Big" {
		t.Fatalf("after removing the first panel: %d panels, first %q", len(tab.d.Panels), tab.d.Panels[0].Title)
	}
	if saved, _ := Load(path); len(saved.Panels) != panels-1 {
		t.Fatal("the removal was not saved")
	}

	// A file changed on disk, as by a pull, is not written over.
	os.WriteFile(path, []byte(`{"name": "Sales", "panels": []}`+"\n"), 0o644)
	tab.d.Panels[0].Width = 3
	tab.save()
	if !strings.Contains(tab.err, project.ErrChanged.Error()) {
		t.Fatalf("a save over a changed file: %q", tab.err)
	}
	tab.Reload()
	if len(tab.d.Panels) != 0 || tab.err != "" {
		t.Fatalf("reloaded: %d panels, %q", len(tab.d.Panels), tab.err)
	}
}

// A panel whose connection fails says why, and connects again when asked.
func TestDashboardConnectionFails(t *testing.T) {
	h := dataview.NewFakeHost(t)
	cn := h.AddConn(db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "127.0.0.1", Port: 1, User: "x", Database: "x"})
	tt := ui.NewTester(h.View, 1000, 600)
	d := &Dashboard{Name: "Down", Panels: []Panel{{Title: "One", Connection: "pg", SQL: "SELECT 1", View: ViewValue, Width: 1}}}
	path := File(h.Project, d.Name)
	if _, err := d.Save(path, nil); err != nil {
		t.Fatal(err)
	}
	tab, err := Open(h, h.Project, path)
	if err != nil {
		t.Fatal(err)
	}
	h.AddTab(tab)
	testutil.WaitFor(t, tt, "the failure", func() bool { return cn.Status == connection.StatusFailed })
	tt.Frame()
	if _, failed := tab.connState(tab.d.Panels[0]); !strings.Contains(failed, "Could not connect to pg") {
		t.Fatalf("the panel says %q", failed)
	}
	tab.refreshAll(false) // automatic: leaves the failed connection
	if cn.Status != connection.StatusFailed {
		t.Fatal("an automatic refresh connected again")
	}
	tab.refreshAll(true)
	if cn.Status != connection.StatusConnecting {
		t.Fatalf("a refresh asked for did not connect again: %v", cn.Status)
	}
}

// A file edited by hand is refused when it holds what the app cannot show.
func TestLoadRefuses(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"no name":  `{"panels": []}`,
		"refresh":  `{"name": "x", "refreshSeconds": 7, "panels": []}`,
		"no query": `{"name": "x", "panels": [{"title": "a", "connection": "c", "sql": " ", "view": "table"}]}`,
		"bad view": `{"name": "x", "panels": [{"title": "a", "connection": "c", "sql": "SELECT 1", "view": "pie"}]}`,
		"not json": `{`,
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".json")
		os.WriteFile(path, []byte(body), 0o644)
		if _, err := Load(path); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

// A dashboard shows where its panels run, once when that is one place; a
// panel's query editor moves it to another connection of the project,
// saved with the query.
func TestPanelConnection(t *testing.T) {
	h := dataview.NewFakeHost(t)
	dir := t.TempDir()
	lite := h.AddConn(db.Config{ID: "lite", Name: "Lite DB", Engine: db.SQLite, Database: filepath.Join(dir, "a.sqlite")})
	other := h.AddConn(db.Config{ID: "other", Name: "Other DB", Engine: db.SQLite, Database: filepath.Join(dir, "b.sqlite")})
	for _, cn := range []*connection.Conn{lite, other} {
		os.WriteFile(cn.Config.Database, nil, 0o600)
	}
	tt := ui.NewTester(h.View, 1280, 800)
	d := &Dashboard{Name: "D", Panels: []Panel{{Title: "P", Connection: "lite", SQL: "SELECT 1 AS one", View: ViewTable, Width: Columns}}}
	path := File(h.Project, d.Name)
	if _, err := d.Save(path, nil); err != nil {
		t.Fatal(err)
	}
	tab, err := Open(h, h.Project, path)
	if err != nil {
		t.Fatal(err)
	}
	h.AddTab(tab)
	testutil.WaitFor(t, tt, "the panel", func() bool { return tab.panels[0].src != nil })
	if !tt.HasText("Lite DB") {
		t.Fatalf("no connection shown: %q", tt.Texts())
	}
	tab.editing = true
	tt.Frame()
	testutil.Settled(t, tt, "Remove")
	if err := tt.Click("SQL"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	s := tab.panels[0]
	if s.connID != lite.Config.ID {
		t.Fatalf("editing on %q", s.connID)
	}
	s.connID = other.Config.ID // as the select chooses it
	tt.Frame()
	testutil.Snapshot(t, tt, "dashboard-panel-connection")
	if err := tt.Click("Run and Keep"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the run on the other", func() bool { return other.Status == connection.StatusConnected && s.src != nil && !s.running })
	saved, _ := os.ReadFile(path)
	if tab.d.Panels[0].Connection != "other" || !strings.Contains(string(saved), `"connection": "other"`) {
		t.Fatalf("panel %+v, file %s", tab.d.Panels[0], saved)
	}
}

// Two connections of one name are told apart by their IDs: the panel runs
// on the one chosen.
func TestPanelSameNameConnections(t *testing.T) {
	h := dataview.NewFakeHost(t)
	dir := t.TempDir()
	first := h.AddConn(db.Config{ID: "first", Name: "DB", Engine: db.SQLite, Database: filepath.Join(dir, "a.sqlite")})
	second := h.AddConn(db.Config{ID: "second", Name: "DB", Engine: db.SQLite, Database: filepath.Join(dir, "b.sqlite")})
	for _, cn := range []*connection.Conn{first, second} {
		os.WriteFile(cn.Config.Database, nil, 0o600)
	}
	tt := ui.NewTester(h.View, 1280, 800)
	d := &Dashboard{Name: "D", Panels: []Panel{{Title: "P", Connection: "first", SQL: "SELECT 1 AS one", View: ViewTable, Width: Columns}}}
	path := File(h.Project, d.Name)
	if _, err := d.Save(path, nil); err != nil {
		t.Fatal(err)
	}
	tab, err := Open(h, h.Project, path)
	if err != nil {
		t.Fatal(err)
	}
	h.AddTab(tab)
	tab.editing = true
	tt.Frame()
	testutil.Settled(t, tt, "Remove")
	if err := tt.Click("SQL"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("DB (first)"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	tt.Frame()
	if err := tt.Click("DB (second)"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	tt.Frame()
	s := tab.panels[0]
	if err := tt.Click("Run and Keep"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the run on the second", func() bool { return second.Status == connection.StatusConnected && s.src != nil && !s.running })
	if tab.d.Panels[0].Connection != "second" {
		t.Fatalf("panel %+v", tab.d.Panels[0])
	}
}

// On PostgreSQL, the panel's query editor chooses its database too.
func TestPanelDatabase(t *testing.T) {
	testutil.Integration(t)
	h := dataview.NewFakeHost(t)
	cfg := testutil.PGConfig()
	cn := h.AddConn(cfg)
	tt := ui.NewTester(h.View, 1280, 800)
	h.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	// Another database, gone after: other tests find one alone.
	cn.DB.SQL.Exec("DROP DATABASE IF EXISTS it_panel WITH (FORCE)")
	if _, err := cn.DB.SQL.Exec("CREATE DATABASE it_panel"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		d, err := db.Open(context.Background(), cfg, nil)
		if err == nil {
			d.SQL.Exec("DROP DATABASE IF EXISTS it_panel WITH (FORCE)")
			d.Close()
		}
	})
	// Read again, with it.
	testutil.WaitFor(t, tt, "the first read", func() bool { return len(cn.Loading) == 0 })
	cn.Reset()
	connection.LoadSchemas(h, cn, "", nil)
	testutil.WaitFor(t, tt, "the databases", func() bool { return slices.Contains(cn.Databases, "it_panel") })
	other := "it_panel"
	id := strings.TrimPrefix(cn.Config.ID, h.Project.Prefix)
	d := &Dashboard{Name: "D", Panels: []Panel{{Title: "P", Connection: id, SQL: "SELECT current_database() AS db", View: ViewTable, Width: Columns}}}
	path := File(h.Project, d.Name)
	d.Save(path, nil)
	tab, err := Open(h, h.Project, path)
	if err != nil {
		t.Fatal(err)
	}
	h.AddTab(tab)
	tab.editing = true
	tt.Frame()
	testutil.Settled(t, tt, "Remove")
	if err := tt.Click("SQL"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("Panel database"); !ok {
		t.Fatalf("no database to choose: %q", tt.Texts())
	}
	s := tab.panels[0]
	s.database = other // as the select chooses it
	if err := tt.Click("Run and Keep"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the run", func() bool { return s.src != nil && !s.running })
	if tab.d.Panels[0].Database != other || db.Display(s.src.Rows[0][0]) != other {
		t.Fatalf("panel %+v, ran in %v", tab.d.Panels[0], s.src.Rows[0][0])
	}
}

// A panel one column wide shows all its tools in edit mode, wrapped
// under its title, and each takes a click.
func TestNarrowPanelTools(t *testing.T) {
	h := dataview.NewFakeHost(t)
	file := filepath.Join(t.TempDir(), "n.sqlite")
	os.WriteFile(file, nil, 0o600)
	h.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(h.View, 1280, 800)
	d := &Dashboard{Name: "N", Panels: []Panel{{Title: "P", Connection: "lite", SQL: "SELECT 1 AS one", View: ViewTable, Width: 1}}}
	path := File(h.Project, d.Name)
	d.Save(path, nil)
	tab, err := Open(h, h.Project, path)
	if err != nil {
		t.Fatal(err)
	}
	h.AddTab(tab)
	tab.editing = true
	tt.Frame()
	testutil.Settled(t, tt, "Remove")
	tt.Frame()
	testutil.Snapshot(t, tt, "dashboard-narrow-panel-edit")
	panel, _ := tt.Find("P")
	for _, tool := range []string{"SQL", "Editor", "Remove"} {
		r, ok := tt.Find(tool)
		if !ok || r.X+r.W > 1280/Columns+20 {
			t.Fatalf("%s at %+v, past the panel at %+v", tool, r, panel)
		}
	}
	if err := tt.Click("SQL"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tab.panels[0].editSQL {
		t.Fatal("the SQL button took no click")
	}
	testutil.Snapshot(t, tt, "dashboard-narrow-panel-sql")
	if err := tt.Click("Run and Keep"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tab.panels[0].editSQL {
		t.Fatal("Run and Keep took no click")
	}
}

// Add Panel opens a panel's query editor on the project's connection; the
// panel is kept once its query runs, and Cancel leaves none.
func TestAddPanel(t *testing.T) {
	h := dataview.NewFakeHost(t)
	file := filepath.Join(t.TempDir(), "a.sqlite")
	os.WriteFile(file, nil, 0o600)
	cn := h.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(h.View, 1280, 800)
	d := &Dashboard{Name: "A", Panels: []Panel{}}
	path := File(h.Project, d.Name)
	d.Save(path, nil)
	tab, err := Open(h, h.Project, path)
	if err != nil {
		t.Fatal(err)
	}
	h.AddTab(tab)
	tt.Frame()
	add := func() *panelState {
		t.Helper()
		if err := tt.Click("Add Panel"); err != nil {
			t.Fatalf("%v: %q", err, tt.Texts())
		}
		tt.Frame()
		if len(tab.panels) != 1 || !tab.panels[0].editSQL || tab.panels[0].connID != cn.Config.ID || !tab.editing {
			t.Fatalf("added: %d panels, editing %v", len(tab.panels), tab.editing)
		}
		return tab.panels[0]
	}
	added := add()
	tab.refreshAll(true) // as a refresh while it is written
	tt.Frame()
	if added.running || added.err != "" {
		t.Fatalf("ran with no query: %q", added.err)
	}
	tab.save() // as another change saves the dashboard
	if saved, _ := os.ReadFile(path); strings.Contains(string(saved), `"title"`) {
		t.Fatalf("saved before its query ran: %s", saved)
	}
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(tab.d.Panels) != 0 {
		t.Fatalf("Cancel left %d panels", len(tab.d.Panels))
	}
	s := add()
	s.sql = "SELECT 1 AS one"
	tt.Frame()
	testutil.Snapshot(t, tt, "dashboard-add-panel")
	if err := tt.Click("Run and Keep"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the panel's rows", func() bool { return s.src != nil && !s.running })
	if saved, _ := os.ReadFile(path); !strings.Contains(string(saved), "SELECT 1 AS one") {
		t.Fatalf("not saved: %s", saved)
	}
}

// Counts and sums read grouped; what names a thing, as an ID or a year,
// does not.
func TestGroupable(t *testing.T) {
	for col, want := range map[string]bool{"revenue": true, "units": true, "paid": true, "id": false, "customer_id": false, "Order Year": false, "zip": false, "": true} {
		if got := groupable(col); got != want {
			t.Errorf("groupable(%q) = %v", col, got)
		}
	}
}
