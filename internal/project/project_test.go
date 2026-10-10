package project

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dgopher/internal/db"
)

// Save writes connections without secrets and without the app's ID
// prefix, Load reads them back, and a file changed by someone else is not
// overwritten.
func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p, _, err := Load(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg := db.Config{ID: p.Prefix + "pg", Name: "Shop", Engine: db.Postgres, Host: "localhost", Password: "hunter2"}
	cfg.SSH.Password, cfg.SSH.KeyPassphrase = "ssh-secret", "key-secret"
	if err := p.Save([]db.Config{cfg}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, File))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"hunter2", "ssh-secret", "key-secret", p.Prefix} {
		if strings.Contains(string(data), secret) {
			t.Errorf("%s holds %q:\n%s", File, secret, data)
		}
	}
	_, pc, err := Load(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(pc.Connections) != 1 || pc.Connections[0].ID != "pg" || pc.Connections[0].Host != "localhost" {
		t.Fatalf("read back %+v", pc.Connections)
	}
	if err := os.WriteFile(filepath.Join(dir, File), append(data, ' '), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.Save([]db.Config{cfg}); !errors.Is(err, errChanged) {
		t.Errorf("saving over a changed file: %v", err)
	}
}

func TestHeaderConnection(t *testing.T) {
	for text, want := range map[string]string{
		"-- connection: billing-prod\n\nSELECT 1": "billing-prod",
		"\n\n  --connection:local\n":              "local",
		"SELECT 1\n\n\n\n\n-- connection: late":   "",
		"-- connection:\nSELECT 1":                "",
	} {
		if got := HeaderConnection(text); got != want {
			t.Errorf("HeaderConnection(%q) = %q, want %q", text, got, want)
		}
	}
}

// HeaderAt finds the connection being typed in a header line, and only
// there.
func TestHeaderAt(t *testing.T) {
	text := "-- connection: bil\nSELECT 'é' -- connection: x"
	if start, typed, ok := HeaderAt(text, 18); !ok || start != 15 || typed != "bil" {
		t.Errorf("in the header: %d %q %v", start, typed, ok)
	}
	if _, typed, ok := HeaderAt("-- connection: ", 15); !ok || typed != "" {
		t.Errorf("after the colon: %q %v", typed, ok)
	}
	if _, _, ok := HeaderAt(text, 16); ok {
		t.Error("the caret inside the name completes it")
	}
	if _, _, ok := HeaderAt(text, len([]rune(text))); ok {
		t.Error("a comment after a statement is a header")
	}
	if _, _, ok := HeaderAt("\n\n\n\n\n-- connection: b", 22); ok {
		t.Error("a header below the fifth line")
	}
}

// MoveJSON moves a shared file to its new name as it is written there,
// unless it changed since it was read or the name is taken.
func TestMoveJSON(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")
	was, err := SaveJSON(from, nil, map[string]string{"name": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MoveJSON(from, to, []byte("{}\n"), map[string]string{"name": "b"}); !errors.Is(err, ErrChanged) {
		t.Fatalf("moved a file changed since: %v", err)
	}
	if err := os.WriteFile(to, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if data, err := MoveJSON(from, to, was, map[string]string{"name": "b"}); data != nil || err == nil {
		t.Fatalf("moved onto a file there: %v", err)
	}
	os.Remove(to)
	data, err := MoveJSON(from, to, was, map[string]string{"name": "b"})
	if err != nil || !strings.Contains(string(data), `"b"`) {
		t.Fatalf("move: %v %s", err, data)
	}
	if _, err := os.Stat(from); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the old file stayed")
	}
	if disk, _ := os.ReadFile(to); string(disk) != string(data) {
		t.Fatalf("the new file holds %s", disk)
	}
}

// A committed .dgopher/.gitignore that ignores nothing is put back to
// ignoring everything; a .dgopher that is a symlink is refused.
func TestGitignoreRewritten(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := Load(dir, true); err != nil {
		t.Fatal(err)
	}
	ignore := filepath.Join(dir, LocalDir, ".gitignore")
	os.WriteFile(ignore, nil, 0o644)
	p, _, err := Load(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	if b, _ := os.ReadFile(ignore); string(b) != "*\n" {
		t.Fatalf(".gitignore %q", b)
	}

	linked := t.TempDir()
	if _, _, err := Load(linked, true); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(linked, LocalDir))
	target := t.TempDir()
	os.Symlink(target, filepath.Join(linked, LocalDir))
	if p, _, err := Load(linked, false); err == nil {
		p.Close()
		t.Fatal("a symlinked .dgopher was opened")
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Fatalf("wrote through the symlink: %v", entries)
	}
}

func TestContains(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "p")
	os.MkdirAll(filepath.Join(dir, "q"), 0o755)
	os.Symlink(root, filepath.Join(dir, "out"))
	os.Symlink(filepath.Join(dir, "q"), filepath.Join(dir, "in"))
	for path, want := range map[string]bool{
		"q/a.sql": true, filepath.Join(dir, "q", "new.sql"): true, "in/a.sql": true, "a/b/c.sql": true,
		"../x": false, "/etc/passwd": false, "out/x": false, "q/../../x": false, "~/x": false, "": false,
		".git/config": false, ".dgopher/x": false, "q/.git": true, ".GIT/config": false,
	} {
		if got := Contains(dir, path); got != want {
			t.Errorf("Contains(%q) = %v", path, got)
		}
	}
}
