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
