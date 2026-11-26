package modelview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/datamodel"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"

	"github.com/egoist/mygo/ui"
)

// Comparing with a database and applying the migration each end in a
// notice of the tab; a comparison that fails says so.
func TestModelCompareNotifies(t *testing.T) {
	h := dataview.NewFakeHost(t)
	file := filepath.Join(t.TempDir(), "shop.sqlite")
	os.WriteFile(file, nil, 0o600)
	cn := h.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(h.View, 1280, 800)
	h.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	if _, err := cn.DB().SQL.Exec(`CREATE TABLE customers (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	tables, _, err := datamodel.Build(context.Background(), cn.DB(), "main", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := &datamodel.Model{Name: "Shop", Engine: db.SQLite, Tables: tables, Source: &datamodel.Source{Connection: "lite", Schema: "main"}}
	path := datamodel.File(h.Project, "", m.Name)
	if _, err := m.Save(path, nil); err != nil {
		t.Fatal(err)
	}
	tab, err := Open(h, h.Project, path)
	if err != nil {
		t.Fatal(err)
	}
	h.AddTab(tab)
	if _, err := cn.DB().SQL.Exec(`ALTER TABLE customers ADD COLUMN email TEXT`); err != nil {
		t.Fatal(err)
	}
	f := newCompareForm(tab)
	tab.comparing = f
	tab.runCompare(f)
	testutil.WaitFor(t, tt, "the comparison", func() bool { return tab.result != nil })
	tab.result.drop = true
	tab.applyMigration()
	h.Confirm.OnConfirm()
	h.Confirm.Open = false
	r := tab.result
	testutil.WaitFor(t, tt, "the migration", func() bool { return tab.result != r && !tab.result.applying })

	f = newCompareForm(tab)
	f.schema = "nosuch"
	tab.comparing = f
	tab.runCompare(f)
	testutil.WaitFor(t, tt, "the failure", func() bool { return !f.running && f.err != "" })

	var titles []string
	for _, n := range h.Notified {
		titles = append(titles, n.Title)
		if n.Tab != tab || n.Started.IsZero() || !strings.Contains(n.Body, "Shop") {
			t.Errorf("notice %+v", n)
		}
	}
	if want := []string{"Compare finished", "Migration applied", "Compare failed"}; strings.Join(titles, "|") != strings.Join(want, "|") {
		t.Fatalf("notices %q, want %q", titles, want)
	}
}
