package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// No test reaches MyGo's recent documents: they wait for the running app's
// main thread, and keep the user's list.
func init() {
	addRecentDocument = func(string) error { return nil }
	recentDocuments = func() []string { return nil }
	clearRecentDocuments = func() error { return nil }
}

// fakeRecent stands in for MyGo's recent documents, which need a running
// app, and returns the list they hold.
func fakeRecent(t *testing.T) *[]string {
	t.Helper()
	var paths []string
	add, list, clear := addRecentDocument, recentDocuments, clearRecentDocuments
	t.Cleanup(func() { addRecentDocument, recentDocuments, clearRecentDocuments = add, list, clear })
	addRecentDocument = func(p string) error {
		paths = slices.Insert(slices.DeleteFunc(paths, func(q string) bool { return q == p }), 0, p)
		return nil
	}
	recentDocuments = func() []string { return slices.Clone(paths) }
	clearRecentDocuments = func() error { paths = nil; return nil }
	return &paths
}

func TestOpeningFilesRecordsRecent(t *testing.T) {
	a := newTestApp(t)
	recent := fakeRecent(t)
	dir := t.TempDir()
	lite := filepath.Join(dir, "a.sqlite")
	duck := filepath.Join(dir, "b.duckdb")
	csv := filepath.Join(dir, "c.csv")
	for _, p := range []string{lite, csv} {
		os.WriteFile(p, nil, 0o600)
	}
	a.openDropped([]string{lite, duck, csv, filepath.Join(dir, "missing.sql")})
	if want := []string{duck, lite}; !slices.Equal(*recent, want) {
		t.Fatalf("recent %q, want %q", *recent, want)
	}
	// Opening one again moves it first, without adding a connection.
	n := len(a.conns)
	a.openDropped([]string{lite})
	if len(a.conns) != n || (*recent)[0] != lite {
		t.Fatalf("reopening: %d connections (was %d), recent %q", len(a.conns), n, *recent)
	}
}

func TestOpenRecentMenu(t *testing.T) {
	a := newTestApp(t)
	recent := fakeRecent(t)
	lite := filepath.Join(t.TempDir(), "a.sqlite")
	os.WriteFile(lite, nil, 0o600)
	*recent = []string{lite}

	file := a.appMenus()[0]
	at := slices.IndexFunc(file.items, func(e menuEntry) bool { return e.label == "Open SQL Script…" })
	if at < 0 || at+1 >= len(file.items) || !file.items[at+1].openRecent {
		t.Fatalf("Open Recent does not follow Open SQL Script…: %+v", file.items)
	}

	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()
	if err := tt.Click("Menu"); err != nil {
		t.Fatal(err)
	}
	if err := testutil.ChooseMenu(tt, "File", "Open Recent", "a.sqlite"); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	if !slices.ContainsFunc(a.conns, func(cn *connection.Conn) bool { return cn.Config.Engine == db.SQLite && cn.Config.Database == lite }) {
		t.Fatal("choosing a.sqlite opened no connection to it")
	}

	if err := tt.Click("Menu"); err != nil {
		t.Fatal(err)
	}
	if err := testutil.ChooseMenu(tt, "File", "Open Recent", "Clear Recent"); err != nil {
		t.Fatalf("%v; texts %q", err, tt.Texts())
	}
	tt.Frame()
	if len(*recent) != 0 {
		t.Fatalf("Clear Recent left %q", *recent)
	}
}
