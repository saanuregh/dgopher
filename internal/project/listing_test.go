package project

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// List lists the files of an extension, in any case, and the folders, at
// any depth and sorted, without dot folders; a missing root holds none.
func TestList(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"b.sql", "A.SQL", "notes.txt", "a-b/x.sql", "a/y.sql", "a/deep/z.sql", ".git/hook.sql", "empty/"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if strings.HasSuffix(rel, "/") {
			os.MkdirAll(path, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, nil, 0o644)
	}
	l, err := List(root, ".sql")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"A.SQL", "a-b/x.sql", "a/deep/z.sql", "a/y.sql", "b.sql"}; !slices.Equal(l.Files, want) {
		t.Errorf("files %q, want %q", l.Files, want)
	}
	if want := []string{"a", "a-b", "a/deep", "empty"}; !slices.Equal(l.Folders, want) {
		t.Errorf("folders %q, want %q", l.Folders, want)
	}
	if l.Modified["a/y.sql"].IsZero() {
		t.Error("no modification time")
	}
	if l, err := List(filepath.Join(root, "gone"), ".sql"); err != nil || l.Files != nil {
		t.Errorf("a missing root: %v, %v", l, err)
	}
}
