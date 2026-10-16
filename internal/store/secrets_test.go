package store

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"
)

// fakeStore wraps a store to fail or change its writes, and to count its
// calls.
type fakeStore struct {
	Secrets
	setErr      error
	readBack    string // when set, Get returns it in place of what was stored
	unavailable bool
	calls       int
}

func (f *fakeStore) Get(key string) (string, error) {
	f.calls++
	v, err := f.Secrets.Get(key)
	if err == nil && f.readBack != "" {
		return f.readBack, nil
	}
	return v, err
}

func (f *fakeStore) Set(key, value string) error {
	f.calls++
	if f.setErr != nil {
		return f.setErr
	}
	return f.Secrets.Set(key, value)
}

func (f *fakeStore) Delete(key string) error {
	f.calls++
	return f.Secrets.Delete(key)
}

func (f *fakeStore) Available() bool { return !f.unavailable }

func wantSecret(t *testing.T, sec Secrets, key, want string) {
	t.Helper()
	if v, err := sec.Get(key); err != nil || v != want {
		t.Fatalf("Get(%q): want the stored value, got a different one or %v", key, err)
	}
}

func wantNoSecret(t *testing.T, sec Secrets, key string) {
	t.Helper()
	if _, err := sec.Get(key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(%q): want ErrNotFound, got %v", key, err)
	}
}

func TestSecretsMigrateOnRead(t *testing.T) {
	current, old := MemorySecrets(), MemorySecrets()
	sec := newMigratingSecrets(current, old, false)
	testSecrets(t, sec)

	old.Set("k", "pw")
	wantSecret(t, sec, "k", "pw")
	wantSecret(t, current, "k", "pw")
	wantNoSecret(t, old, "k")
	wantSecret(t, sec, "k", "pw")

	// What the new store has wins over a stale old item.
	old.Set("k", "stale")
	wantSecret(t, sec, "k", "pw")
	wantSecret(t, old, "k", "stale")
}

func TestSecretsKeepOldWhenWriteFails(t *testing.T) {
	old := MemorySecrets()
	old.Set("k", "pw")
	sec := newMigratingSecrets(&fakeStore{Secrets: MemorySecrets(), setErr: errors.New("write failed")}, old, false)
	wantSecret(t, sec, "k", "pw")
	wantSecret(t, old, "k", "pw")

	// A write that reads back different keeps the old item too.
	sec = newMigratingSecrets(&fakeStore{Secrets: MemorySecrets(), readBack: "other"}, old, false)
	wantSecret(t, sec, "k", "pw")
	wantSecret(t, old, "k", "pw")

	// A new store that fails to read, other than for a missing key, is
	// not passed over: its item may be the newer one.
	sec = newMigratingSecrets(brokenStore{}, old, false)
	if _, err := sec.Get("k"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("want the read error, got %v", err)
	}
	wantSecret(t, old, "k", "pw")
}

type brokenStore struct{ Secrets }

func (brokenStore) Get(string) (string, error) { return "", errors.New("store locked") }
func (brokenStore) Available() bool            { return true }

func TestSecretsDeleteRemovesBoth(t *testing.T) {
	current, old := MemorySecrets(), MemorySecrets()
	sec := newMigratingSecrets(current, old, false)
	current.Set("k", "pw")
	old.Set("k", "stale")
	if err := sec.Delete("k"); err != nil {
		t.Fatal(err)
	}
	wantNoSecret(t, current, "k")
	wantNoSecret(t, old, "k")
	wantNoSecret(t, sec, "k")
	if err := sec.Delete("k"); err != nil {
		t.Fatalf("delete absent: %v", err)
	}
}

// On macOS both libraries keep a key in the same Keychain item, which
// go-keyring stores encoded: it is decoded and written back in place, and
// the old store is never touched, since deleting there deletes the item.
func TestSecretsSharedItemDecodesInPlace(t *testing.T) {
	current := MemorySecrets()
	old := &fakeStore{Secrets: MemorySecrets()}
	sec := newMigratingSecrets(current, old, true)
	current.Set("b", "go-keyring-base64:"+base64.StdEncoding.EncodeToString([]byte("pw\nü")))
	current.Set("h", "go-keyring-encoded:"+hex.EncodeToString([]byte("pw2")))
	current.Set("raw", "pw3")

	wantSecret(t, sec, "b", "pw\nü")
	wantSecret(t, current, "b", "pw\nü")
	wantSecret(t, sec, "h", "pw2")
	wantSecret(t, current, "h", "pw2")
	wantSecret(t, sec, "raw", "pw3")
	wantNoSecret(t, sec, "missing")
	if err := sec.Delete("b"); err != nil {
		t.Fatal(err)
	}
	wantNoSecret(t, current, "b")

	current.Set("bad", "go-keyring-base64:!")
	if _, err := sec.Get("bad"); err == nil {
		t.Fatal("want an error for a value that does not decode")
	}
	wantSecret(t, current, "bad", "go-keyring-base64:!")
	if old.calls != 0 {
		t.Fatalf("the old store was called %d times", old.calls)
	}
}

func TestSecretsUnavailableWithoutEitherStore(t *testing.T) {
	sec := newMigratingSecrets(&fakeStore{Secrets: MemorySecrets(), unavailable: true},
		&fakeStore{Secrets: MemorySecrets(), unavailable: true}, false)
	if sec.Available() {
		t.Fatal("want unavailable")
	}
}

// unsupportedStore is a credential store the system lacks, as MyGo's on
// Linux without libsecret.
type unsupportedStore struct{}

func (unsupportedStore) Get(string) (string, error) { return "", errors.ErrUnsupported }
func (unsupportedStore) Set(string, string) error   { return errors.ErrUnsupported }
func (unsupportedStore) Delete(string) error        { return errors.ErrUnsupported }
func (unsupportedStore) Available() bool            { return false }

// Without the new store, the old one keeps working as before: saved
// passwords are read, new ones saved, and the keychain is available.
func TestSecretsFallBackWhenUnsupported(t *testing.T) {
	old := MemorySecrets()
	old.Set("k", "pw")
	sec := newMigratingSecrets(unsupportedStore{}, old, false)
	if !sec.Available() {
		t.Fatal("not available though the old store works")
	}
	wantSecret(t, sec, "k", "pw")
	if err := sec.Set("n", "pw2"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	wantSecret(t, old, "n", "pw2")
	if err := sec.Delete("k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	wantNoSecret(t, old, "k")
}
