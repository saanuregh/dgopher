package app

import (
	"os"
	"path/filepath"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/store"
	"dgopher/internal/ui/query"
)

// A cloned .dgopher names files outside the project, or the project's own
// .git and .dgopher: none comes back as an editor.
func TestRestoreSkipsOutsidePaths(t *testing.T) {
	st, _ := store.Open(t.TempDir(), store.MemorySecrets())
	a := startApp(t, st)
	root := t.TempDir()
	projDir := filepath.Join(root, "proj")
	os.MkdirAll(filepath.Join(projDir, "queries"), 0o755)
	p, err := a.addProject(projDir)
	if err != nil {
		t.Fatal(err)
	}
	addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	a.saveProject(p)

	outside := filepath.Join(root, "x")
	os.WriteFile(outside, []byte("SELECT 1;"), 0o644)
	os.Mkdir(filepath.Join(projDir, ".git"), 0o755)
	os.WriteFile(filepath.Join(projDir, ".git", "config"), []byte("SELECT 1;"), 0o644)
	os.WriteFile(filepath.Join(projDir, ".dgopher", "x"), []byte("SELECT 1;"), 0o644)
	os.Symlink(outside, filepath.Join(projDir, "queries", "link.sql"))
	inside := filepath.Join(projDir, "queries", "ok.sql")
	os.WriteFile(inside, []byte("SELECT 2;"), 0o644)

	var editors []savedEditor
	for _, path := range []string{"../x", outside, "queries/link.sql", ".git/config", ".dgopher/x", "queries/../../x", "queries/ok.sql"} {
		editors = append(editors, savedEditor{Connection: "lite", Path: path, Unsaved: "SELECT 3;"})
	}
	if err := p.Local.SaveJSON(workspaceFile, savedWorkspace{Editors: editors, Active: -1, Dashboards: []string{"../x"}, Models: []string{outside}}); err != nil {
		t.Fatal(err)
	}
	a.main.tabs = nil
	a.restoreWorkspace(p)
	if len(a.main.tabs) != 1 {
		var paths []string
		for _, tab := range a.main.tabs {
			if q, ok := tab.(*query.Tab); ok {
				paths = append(paths, q.Path)
			}
		}
		t.Fatalf("restored %d tabs, want only %s: %v", len(a.main.tabs), inside, paths)
	}
	if q, ok := a.main.tabs[0].(*query.Tab); !ok || q.Path != inside {
		t.Fatalf("restored %v", a.main.tabs[0])
	}
}
