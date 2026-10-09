package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/params"
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
	if !strings.Contains(tab.err, ErrChanged.Error()) {
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
