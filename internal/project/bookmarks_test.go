package project

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Bookmarks round-trip through bookmarks.json, which a project without
// any does not have, and a file changed since it was read is not
// overwritten.
func TestBookmarksFile(t *testing.T) {
	p := &Project{Dir: t.TempDir()}
	path := filepath.Join(p.Dir, BookmarksFile)
	list, disk, err := LoadBookmarks(p)
	if list != nil || disk != nil || err != nil {
		t.Fatalf("without a file: %v %q %v", list, disk, err)
	}
	want := []Bookmark{
		{Name: "orders", Connection: "pg", Schema: "public", Object: "orders", Kind: "table"},
		{Name: "daily", File: "queries/reports/daily.sql"},
	}
	disk, err = SaveBookmarks(p, want, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, read, err := LoadBookmarks(p)
	if err != nil || !slices.Equal(got, want) || string(read) != string(disk) {
		t.Fatalf("read back %v, %v", got, err)
	}
	if _, err := SaveBookmarks(p, want[:1], []byte("stale")); err == nil {
		t.Error("a file changed since it was read was overwritten")
	}
	if _, err := SaveBookmarks(p, nil, disk); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("no bookmarks left the file: %v", err)
	}
	for _, bad := range []string{
		`{"bookmarks": [{"name": "", "file": "a.sql"}]}`,
		`{"bookmarks": [{"name": "x"}]}`,
		`{"bookmarks": [{"name": "x", "file": "a.sql", "object": "t", "connection": "pg"}]}`,
		`{"bookmarks": [{"name": "x", "object": "t"}]}`,
	} {
		os.WriteFile(path, []byte(bad), 0o644)
		if _, _, err := LoadBookmarks(p); err == nil {
			t.Errorf("read %s", bad)
		}
	}
}

// Targets tell bookmarks apart by what they link to, not their names.
func TestBookmarkTarget(t *testing.T) {
	a := Bookmark{Name: "a", Connection: "pg", Schema: "s", Object: "t"}
	b := Bookmark{Name: "b", Connection: "pg", Schema: "s", Object: "t"}
	f := Bookmark{Name: "a", Connection: "pg", Schema: "s", Object: "t", Item: "function"}
	if a.Target() != b.Target() || a.Target() == f.Target() {
		t.Errorf("targets: %q %q %q", a.Target(), b.Target(), f.Target())
	}
}
