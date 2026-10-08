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
