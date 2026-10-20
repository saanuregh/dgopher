package project

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
	defer p.Close()
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
	again, pc, err := Load(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	again.Close()
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
	if _, _, ok := HeaderAt(text, len(text)); ok {
		t.Error("a comment after a statement is a header")
	}
	if _, _, ok := HeaderAt("\n\n\n\n\n-- connection: b", 22); ok {
		t.Error("a header below the fifth line")
	}
	// Offsets count bytes, past names of several bytes and bytes that are
	// not UTF-8.
	for _, name := range []string{"é日", "a\xffb"} {
		head := "-- é\n-- connection: " + name
		if start, typed, ok := HeaderAt(head+"\nSELECT 1", len(head)); !ok || start != len(head)-len(name) || typed != name {
			t.Errorf("%q: %d %q %v, want %d", name, start, typed, ok, len(head)-len(name))
		}
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
	created, _, err := Load(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	created.Close()
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
	first, _, err := Load(linked, true)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
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
	cases := map[string]bool{
		"q/a.sql": true, filepath.Join(dir, "q", "new.sql"): true, "in/a.sql": true, "a/b/c.sql": true,
		"../x": false, filepath.Join(root, "x"): false, "out/x": false, "q/../../x": false, "~/x": false, "": false,
		".git/config": false, ".dgopher/x": false, "q/.git": true, ".GIT/config": false,
	}
	if runtime.GOOS != "windows" { // not absolute there: a file in the project's volume
		cases["/etc/passwd"] = false
	}
	for path, want := range cases {
		if got := Contains(dir, path); got != want {
			t.Errorf("Contains(%q) = %v", path, got)
		}
	}
}

// The header names the view a file opens in, after its connection's line,
// and only the script's view leaves no line.
func TestHeaderView(t *testing.T) {
	for _, c := range []struct{ in, notebook string }{
		{"-- connection: lite\n\nSELECT 1;\n", "-- connection: lite\n-- view: notebook\n\nSELECT 1;\n"},
		{"SELECT 1;\n", "-- view: notebook\n\nSELECT 1;\n"},
		{"-- connection: lite\n-- view: script\n\nSELECT 1;\n", "-- connection: lite\n-- view: notebook\n\nSELECT 1;\n"},
		{"", "-- view: notebook\n\n"},
	} {
		got := SetHeaderView(c.in, true)
		if got != c.notebook || HeaderView(got) != NotebookView {
			t.Fatalf("%q: %q", c.in, got)
		}
		if back := SetHeaderView(got, false); HeaderView(back) != "" || strings.Contains(back, "view:") {
			t.Fatalf("back from %q: %q", got, back)
		}
		if rest := got[HeaderEnd(got):]; strings.Contains(rest, "--") {
			t.Fatalf("%q: the header ends before %q", got, rest)
		}
	}
	if back := SetHeaderView("-- view: notebook\n\nSELECT 1;\n", false); back != "SELECT 1;\n" {
		t.Fatalf("back: %q", back)
	}
	// A comment of the user's that only starts like it stays as it is.
	user := "-- connection: lite\n-- view: monthly_revenue\n\nSELECT 1;\n"
	if got := SetHeaderView(user, true); got != "-- connection: lite\n-- view: notebook\n-- view: monthly_revenue\n\nSELECT 1;\n" || SetHeaderView(got, false) != user {
		t.Fatalf("the user's comment changed: %q", got)
	}
	if HeaderView("SELECT 1;\n-- view: notebook\n") != NotebookView {
		t.Fatal("a view line among the first lines is the header's")
	}
}
