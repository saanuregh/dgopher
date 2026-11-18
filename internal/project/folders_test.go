package project

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/db"
)

func TestFolderHelpers(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"Prod", "Prod", true},
		{"  EU west ", "EU west", true},
		{"", "", false},
		{"   ", "", false},
		{".", "", false},
		{"..", "", false},
		{"a/b", "", false},
		{`a\b`, "", false},
	} {
		got, err := CleanFolderName(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("CleanFolderName(%q) = %q, %v", tc.in, got, err)
		}
	}
	for path, want := range map[string][2]string{"": {"", ""}, "A": {"", "A"}, "A/B/C": {"A/B", "C"}} {
		if p, b := FolderParent(path), FolderBase(path); p != want[0] || b != want[1] {
			t.Errorf("%q: parent %q, base %q", path, p, b)
		}
	}
	if got := MergeFolders([]string{"B", "A/x"}, "", "C/D/E", "A"); !slices.Equal(got, []string{"A", "A/x", "B", "C", "C/D", "C/D/E"}) {
		t.Errorf("MergeFolders: %q", got)
	}
	for _, tc := range []struct {
		path, from, to, want string
		ok                   bool
	}{
		{"A", "A", "B", "B", true},
		{"A/x", "A", "B/A", "B/A/x", true},
		{"AB", "A", "B", "", false},
		{"C", "A", "B", "", false},
		{"A/x", "A", "", "x", true},
	} {
		if got, ok := MovedFolder(tc.path, tc.from, tc.to); got != tc.want || ok != tc.ok {
			t.Errorf("MovedFolder(%q, %q, %q) = %q, %v", tc.path, tc.from, tc.to, got, ok)
		}
	}
	for _, tc := range []struct {
		path, folder string
		want         bool
	}{{"A", "A", true}, {"A/b", "A", true}, {"AB", "A", false}, {"", "A", false}, {"A", "", true}} {
		if got := UnderFolder(tc.path, tc.folder); got != tc.want {
			t.Errorf("UnderFolder(%q, %q) = %v", tc.path, tc.folder, got)
		}
	}
}

// A project without folders writes the file it wrote before folders; one
// with them keeps an empty folder and a connection's folder, and the
// parents of both.
func TestFoldersRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p, _, err := Load(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	cfg := db.Config{ID: p.Prefix + "pg", Name: "Shop", Engine: db.Postgres, Host: "localhost"}
	if err := p.Save([]db.Config{cfg}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, File))
	if strings.Contains(string(data), "folder") {
		t.Fatalf("a project without folders names them:\n%s", data)
	}

	p.Folders = []string{"Empty"}
	cfg.Folder = "Prod/EU"
	if err := p.Save([]db.Config{cfg}); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(dir, File))
	for _, want := range []string{`"folder": "Prod/EU"`, `"folders": {`, `"Empty": {}`, `"Prod": {}`, `"Prod/EU": {}`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the file lacks %s:\n%s", want, data)
		}
	}
	again, pc, err := Load(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if !slices.Equal(again.Folders, []string{"Empty", "Prod", "Prod/EU"}) || pc.Connections[0].Folder != "Prod/EU" {
		t.Fatalf("read back folders %q, connection %+v", again.Folders, pc.Connections[0])
	}

	// A folder named only on a connection, as by hand, is listed.
	os.WriteFile(filepath.Join(dir, File), []byte(`{"connections": [{"id": "x", "name": "X", "engine": "sqlite", "folder": "Hand/Made"}]}`), 0o644)
	hand, _, err := Load(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer hand.Close()
	if !slices.Equal(hand.Folders, []string{"Hand", "Hand/Made"}) {
		t.Fatalf("hand-written folders %q", hand.Folders)
	}

	// Paths written loosely by hand read as the folders they mean.
	os.WriteFile(filepath.Join(dir, File), []byte(`{"folders": {"Prod/": {}, " /Prod ": {}, "Prod / EU": {}, "/": {}},
		"connections": [{"id": "x", "name": "X", "engine": "sqlite", "folder": "Prod //EU/"}]}`), 0o644)
	loose, pc, err := Load(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer loose.Close()
	if !slices.Equal(loose.Folders, []string{"Prod", "Prod/EU"}) || pc.Connections[0].Folder != "Prod/EU" {
		t.Fatalf("loose folders %q, connection in %q", loose.Folders, pc.Connections[0].Folder)
	}
}
