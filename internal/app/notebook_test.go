package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/store"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// New Notebook's keys open a notebook of one cell in a new query file,
// which comes back as a notebook after a restart.
func TestNewNotebook(t *testing.T) {
	cfgDir := t.TempDir()
	st, _ := store.Open(cfgDir, store.MemorySecrets())
	a := startApp(t, st)
	projDir := filepath.Join(t.TempDir(), "proj")
	os.MkdirAll(projDir, 0o755)
	p, err := a.addProject(projDir)
	if err != nil {
		t.Fatal(err)
	}
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", "")})
	a.saveProject(p)
	tt := ui.NewTester(a.view, 1200, 760)
	connectNow(t, a, tt, cn)
	a.nav.row = -1 // none chosen: the only connection is the one
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyT)
	testutil.WaitFor(t, tt, "the notebook", func() bool {
		q, ok := a.ActiveTab().(*query.Tab)
		return ok && q.Notebook()
	})
	q := a.ActiveTab().(*query.Tab)
	disk, _ := os.ReadFile(q.Path)
	if !strings.Contains(string(disk), "-- connection: lite") || !query.HasCells(string(disk)) {
		t.Fatalf("file %q", disk)
	}
	a.saveWorkspace(true)

	st2, _ := store.Open(cfgDir, store.MemorySecrets())
	b := startApp(t, st2)
	if r, ok := b.main.tabs().Active().(*query.Tab); !ok || !r.Notebook() {
		t.Fatalf("restored %T", b.main.tabs().Active())
	}
}

// A file opens as a script, cells or not, unless its header names the
// notebook view.
func TestCellsOpenAsScript(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	path := filepath.Join(a.projects[0].Dir, "cells.sql")
	text := "-- %%\nSELECT 1;\n\n-- %%\nSELECT 2;\n"
	os.WriteFile(path, []byte(text), 0o644)
	q := a.openFileTab(cn, "", path, text)
	if q.Notebook() {
		t.Fatal("a file with cells opened as a notebook")
	}
	// Its header naming the notebook view, it opens as one; back to the
	// script, the line goes.
	text = "-- connection: lite\n-- view: notebook\n\n" + text
	q = a.openFileTab(cn, "", path, text)
	if !q.Notebook() || q.FileText() != text {
		t.Fatalf("notebook %v, text %q", q.Notebook(), q.FileText())
	}
	q.ToggleNotebook()
	if q.Notebook() || q.FileText() != "-- connection: lite\n\n-- %%\nSELECT 1;\n\n-- %%\nSELECT 2;\n" {
		t.Fatalf("script text %q", q.FileText())
	}
}
