package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/zalando/go-keyring"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "cfg"), MemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestJSONRoundTripAndAtomic(t *testing.T) {
	s := openTest(t)
	type conn struct {
		Name string
		Port int
	}
	var missing []conn
	if err := s.LoadJSON("connections.json", &missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	in := []conn{{"a", 5432}, {"b", 3306}}
	for i := 0; i < 2; i++ {
		if err := s.SaveJSON("connections.json", in); err != nil {
			t.Fatal(err)
		}
	}
	var out []conn
	if err := s.LoadJSON("connections.json", &out); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(out) != fmt.Sprint(in) {
		t.Fatalf("got %v", out)
	}
	entries, _ := os.ReadDir(s.Dir())
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(s.Dir(), "connections.json"))
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("file mode %v", fi.Mode().Perm())
		}
	}
}

func TestDirPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix permissions")
	}
	dir := filepath.Join(t.TempDir(), "cfg")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.Chmod(dir, 0o755)
	if _, err := Open(dir, MemorySecrets()); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(dir)
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", fi.Mode().Perm())
	}
	s := openTest(t)
	fi, _ = os.Stat(s.Dir())
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("new dir mode %v", fi.Mode().Perm())
	}
}

func TestNameValidation(t *testing.T) {
	s := openTest(t)
	for _, n := range []string{"../x", "a/b", "..", "", `a\b`} {
		if err := s.SaveJSON(n, 1); err == nil {
			t.Errorf("SaveJSON(%q) accepted", n)
		}
		var v any
		if err := s.LoadJSON(n, &v); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("LoadJSON(%q) = %v", n, err)
		}
	}
}

func TestDefaultDirOverride(t *testing.T) {
	t.Setenv("DGOPHER_CONFIG_DIR", "/tmp/x")
	if d, _ := DefaultDir(); d != "/tmp/x" {
		t.Fatal(d)
	}
}

func testSecrets(t *testing.T, sec Secrets) {
	t.Helper()
	if !sec.Available() {
		t.Fatal("unavailable")
	}
	if _, err := sec.Get("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := sec.Set("k", "v"); err != nil {
		t.Fatal(err)
	}
	if v, err := sec.Get("k"); err != nil || v != "v" {
		t.Fatal(v, err)
	}
	if err := sec.Delete("k"); err != nil {
		t.Fatal(err)
	}
	if err := sec.Delete("k"); err != nil {
		t.Fatalf("delete absent: %v", err)
	}
	if _, err := sec.Get("k"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestMemorySecrets(t *testing.T) {
	sec := MemorySecrets()
	testSecrets(t, sec)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); sec.Set(fmt.Sprint(i), "x"); sec.Get(fmt.Sprint(i)) }(i)
	}
	wg.Wait()
}

func TestKeyringSecretsMock(t *testing.T) {
	keyring.MockInit()
	testSecrets(t, KeyringSecrets("dgopher-test"))
}

func TestKeyringSecretsUnavailable(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret service"))
	defer keyring.MockInit()
	if KeyringSecrets("dgopher-test").Available() {
		t.Fatal("want unavailable")
	}
}

// The data folder is the one the variables name, else the system's.
func TestDefaultDataDir(t *testing.T) {
	t.Setenv("DGOPHER_DATA_DIR", "/data")
	t.Setenv("DGOPHER_CONFIG_DIR", "/config")
	if d, _ := DefaultDataDir(); d != "/data" {
		t.Fatalf("with DGOPHER_DATA_DIR: %s", d)
	}
	t.Setenv("DGOPHER_DATA_DIR", "")
	if d, _ := DefaultDataDir(); d != "/config" {
		t.Fatalf("with DGOPHER_CONFIG_DIR alone: %s", d)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LOCALAPPDATA", `C:\Users\me\AppData\Local`)
	t.Setenv("XDG_DATA_HOME", "relative/ignored")
	for goos, want := range map[string]string{
		"linux":   filepath.Join(home, ".local", "share"),
		"darwin":  filepath.Join(home, "Library", "Application Support"),
		"windows": `C:\Users\me\AppData\Local`,
	} {
		if d, err := userDataDir(goos); err != nil || d != want {
			t.Errorf("%s: %s, %v; want %s", goos, d, err, want)
		}
	}
	t.Setenv("XDG_DATA_HOME", "/xdg")
	if d, _ := userDataDir("linux"); d != "/xdg" {
		t.Errorf("with XDG_DATA_HOME: %s", d)
	}
}
