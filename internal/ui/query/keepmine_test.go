package query

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dgopher/internal/db"

	"github.com/egoist/mygo/ui"
)

// Keep Mine writes a file inside the project at once, and asks first, with
// the full path, for one outside it.
func TestKeepMineOutsideAsks(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "SELECT 1;")
	conflicted := func(path string) {
		os.WriteFile(path, []byte("theirs"), 0o644)
		q.Path, q.Saved, q.Editor.Text, q.DiskConflict = path, "old", "mine", "changed on disk"
	}

	inside := filepath.Join(cn.Project.Dir, "in.sql")
	conflicted(inside)
	q.keepMine()
	if b, _ := os.ReadFile(inside); string(b) != "mine" || a.Confirm != nil {
		t.Fatalf("inside: %q, asked %v", b, a.Confirm)
	}

	outside := filepath.Join(t.TempDir(), "out.sql")
	conflicted(outside)
	q.keepMine()
	if b, _ := os.ReadFile(outside); string(b) != "theirs" {
		t.Fatalf("written before asking: %q", b)
	}
	if a.Confirm == nil || a.Confirm.Preview != outside {
		t.Fatalf("asked %+v", a.Confirm)
	}
	a.Confirm.OnConfirm()
	if b, _ := os.ReadFile(outside); string(b) != "mine" || q.DiskConflict != "" {
		t.Fatalf("outside after yes: %q, %q", b, q.DiskConflict)
	}
}

// A file of the project that is a symlink to a missing file outside it,
// as a clone may hold, counts as outside: Keep Mine asks, naming where
// the link goes, before writing through it.
func TestKeepMineDanglingSymlinkAsks(t *testing.T) {
	a := newFakeQueryHost(t)
	cn := a.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := newEditor(t, a, tt, cn, "SELECT 1;")
	planted := filepath.Join(t.TempDir(), "planted.txt")
	link := filepath.Join(cn.Project.Dir, "notes.sql")
	if err := os.Symlink(planted, link); err != nil {
		t.Fatal(err)
	}
	q.Path, q.Saved, q.Editor.Text, q.DiskConflict = link, "", "mine", "was deleted on disk"
	q.keepMine()
	if _, err := os.Lstat(planted); err == nil {
		t.Fatal("written through the link before asking")
	}
	if a.Confirm == nil || !strings.Contains(strings.Join(a.Confirm.Reasons, " "), planted) {
		t.Fatalf("asked %+v", a.Confirm)
	}
}
