package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/store"
	"dgopher/internal/testutil"
)

// lockedSecrets is a keychain whose reads fail, as when it is locked or
// the user denies access, while what it keeps stays there.
type lockedSecrets struct {
	store.Secrets
	mu      sync.Mutex
	getErr  error
	deleted []string
	set     []string
}

func (s *lockedSecrets) Get(key string) (string, error) {
	s.mu.Lock()
	err := s.getErr
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	return s.Secrets.Get(key)
}

func (s *lockedSecrets) Set(key, value string) error {
	s.mu.Lock()
	s.set = append(s.set, key)
	s.mu.Unlock()
	return s.Secrets.Set(key, value)
}

func (s *lockedSecrets) Delete(key string) error {
	s.mu.Lock()
	s.deleted = append(s.deleted, key)
	s.mu.Unlock()
	return s.Secrets.Delete(key)
}

func (s *lockedSecrets) lock(err error) {
	s.mu.Lock()
	s.getErr = err
	s.mu.Unlock()
}

// stored reads what the keychain really keeps, past the failing reads.
func (s *lockedSecrets) stored(key string) (string, error) { return s.Secrets.Get(key) }

func (s *lockedSecrets) wasDeleted(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.deleted, key)
}

const keptPassword = "s3cret-kept-pw"

// newLockedKeychainApp returns an app with one project whose keychain
// keeps cn's password and then fails every read.
func newLockedKeychainApp(t *testing.T, cfg db.Config) (*App, *lockedSecrets, *connection.Conn) {
	t.Helper()
	sec := &lockedSecrets{Secrets: store.MemorySecrets()}
	st, err := store.Open(t.TempDir(), sec)
	if err != nil {
		t.Fatal(err)
	}
	a := startApp(t, st)
	dir := filepath.Join(t.TempDir(), "proj")
	os.MkdirAll(dir, 0o755)
	if _, err := a.addProject(dir); err != nil {
		t.Fatal(err)
	}
	cn := addConn(a, cfg)
	a.saveProject(cn.Project)
	if err := sec.Secrets.Set(secretKey(&cn.Config, "password"), keptPassword); err != nil {
		t.Fatal(err)
	}
	sec.lock(errors.New("the user denied access"))
	return a, sec, cn
}

var lockedPostgres = db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "127.0.0.1", Port: 1, User: "x", Database: "x", Env: db.Development}

// A password the keychain cannot give fails the connect with the reason,
// before any server is reached, and offers to retry or edit.
func TestKeychainReadFailureStopsConnect(t *testing.T) {
	a, _, cn := newLockedKeychainApp(t, lockedPostgres)
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "the alert", func() bool { return a.frontAlert() != nil })
	if cn.Status != connection.StatusFailed || cn.ErrKind != db.ProblemKeychain {
		t.Fatalf("status %v, kind %v, err %q", cn.Status, cn.ErrKind, cn.Err)
	}
	if !strings.Contains(cn.Err, "keychain") || strings.Contains(cn.Err, keptPassword) {
		t.Fatalf("err %q", cn.Err)
	}
	var labels []string
	for _, act := range a.frontAlert().report.Actions {
		labels = append(labels, act.Label)
	}
	if !slices.Contains(labels, "Retry") || !slices.Contains(labels, "Edit Connection") {
		t.Fatalf("actions %q", labels)
	}
	tt.Frame()
	for _, s := range tt.Texts() {
		if strings.Contains(s, keptPassword) {
			t.Fatalf("the password is shown: %q", s)
		}
	}

	// Connected inline, the failure shows where the view shows it.
	other := addConn(a, db.Config{ID: "pg2", Name: "pg2", Engine: db.Postgres, Host: "127.0.0.1", Port: 1, User: "y", Database: "y"})
	alerts := len(a.alerts)
	a.ConnectInline(other, nil)
	testutil.WaitFor(t, tt, "the inline failure", func() bool { return other.Status == connection.StatusFailed })
	if other.ErrKind != db.ProblemKeychain || len(a.alerts) != alerts {
		t.Fatalf("inline: kind %v, alerts %d then %d", other.ErrKind, alerts, len(a.alerts))
	}
}

// Saving the form of a connection whose password the keychain could not
// read keeps that password, and the form says why the field is empty.
func TestFormKeepsSecretItCouldNotRead(t *testing.T) {
	a, sec, cn := newLockedKeychainApp(t, lockedPostgres)
	key := secretKey(&cn.Config, "password")
	tt := ui.NewTester(a.view, 1000, 900)
	a.openConnForm(cn)
	tt.Frame()
	if !testutil.HasTextContaining(tt, "Couldn't read the saved password from the keychain: the user denied access") {
		t.Fatalf("no note: %q", tt.Texts())
	}
	f := a.connForm
	a.saveConnForm(f, false)
	testutil.WaitFor(t, tt, "the form saved", func() bool { return !f.open })
	if sec.wasDeleted(key) {
		t.Fatal("the unread password was deleted")
	}
	if pw, err := sec.stored(key); pw != keptPassword || err != nil {
		t.Fatalf("stored %q, %v", pw, err)
	}
}

// A new password typed over one the keychain could not read replaces it.
func TestFormReplacesUnreadSecretWhenTyped(t *testing.T) {
	a, sec, cn := newLockedKeychainApp(t, lockedPostgres)
	key := secretKey(&cn.Config, "password")
	tt := ui.NewTester(a.view, 1000, 900)
	a.openConnForm(cn)
	tt.Frame()
	f := a.connForm
	f.cfg.Password = "typed-new-pw"
	a.saveConnForm(f, false)
	testutil.WaitFor(t, tt, "the form saved", func() bool { return !f.open })
	if pw, err := sec.stored(key); pw != "typed-new-pw" || err != nil {
		t.Fatalf("stored %q, %v", pw, err)
	}
}

// Duplicating a connection whose password the keychain could not read
// leaves the original's password, and says the copy has none.
func TestDuplicateKeepsOriginalSecret(t *testing.T) {
	a, sec, cn := newLockedKeychainApp(t, lockedPostgres)
	key := secretKey(&cn.Config, "password")
	a.duplicateConn(cn)
	if len(a.conns) != 2 {
		t.Fatalf("%d connections", len(a.conns))
	}
	if sec.wasDeleted(key) {
		t.Fatal("the original's password was deleted")
	}
	if pw, err := sec.stored(key); pw != keptPassword || err != nil {
		t.Fatalf("stored %q, %v", pw, err)
	}
	if !slices.ContainsFunc(a.toasts, func(p pendingToast) bool {
		return strings.Contains(p.text, "Couldn't copy a saved password") && !strings.Contains(p.text, keptPassword)
	}) {
		t.Fatalf("toasts %+v", a.toasts)
	}
}

// A locked keychain stops only a connection whose password it keeps: a
// file opens, and one asking for its password asks.
func TestKeychainReadFailureIgnoredWhenPasswordIsNotKept(t *testing.T) {
	file := filepath.Join(t.TempDir(), "x.sqlite")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lite := db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file}
	a, _, cn := newLockedKeychainApp(t, lite)
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "the file to open", func() bool { return cn.Status != connection.StatusConnecting })
	if cn.Status != connection.StatusConnected {
		t.Fatalf("the file: status %v, err %q", cn.Status, cn.Err)
	}

	ask := lockedPostgres
	ask.ID, ask.Name, ask.AskPassword = "ask", "ask", true
	asked := addConn(a, ask)
	a.Connect(asked, nil)
	testutil.WaitFor(t, tt, "the password prompt", func() bool { return a.prompt != nil || asked.Status == connection.StatusFailed })
	if a.prompt == nil {
		t.Fatalf("asking: status %v, err %q", asked.Status, asked.Err)
	}
}

// Moving a connection to another host keeps the password the keychain
// could not read: it was never moved to the new key, so the old one stays.
func TestFormKeepsUnreadSecretWhenDestinationChanges(t *testing.T) {
	a, sec, cn := newLockedKeychainApp(t, lockedPostgres)
	key := secretKey(&cn.Config, "password")
	tt := ui.NewTester(a.view, 1000, 900)
	a.openConnForm(cn)
	tt.Frame()
	f := a.connForm
	f.cfg.Host = "127.0.0.2"
	a.saveConnForm(f, false)
	testutil.WaitFor(t, tt, "the form saved", func() bool { return !f.open })
	if cn.Config.Host != "127.0.0.2" {
		t.Fatalf("host %q: the test did not change the destination", cn.Config.Host)
	}
	if pw, err := sec.stored(key); pw != keptPassword || err != nil {
		t.Fatalf("the unread password at the old key: %q, %v", pw, err)
	}
}

// A locked keychain does not stop a connection whose SSH tunnel logs in
// without a secret from it: what it could not read is left to the server.
func TestKeychainReadFailureOfOtherSecretsDoesNotStopConnect(t *testing.T) {
	cfg := lockedPostgres
	cfg.PasswordEnv = "DGOPHER_TEST_UNSET_PASSWORD"
	cfg.SSH = db.SSHConfig{Enabled: true, Host: "127.0.0.1", Port: 1, User: "u"}
	a, _, cn := newLockedKeychainApp(t, cfg)
	tt := ui.NewTester(a.windowView(a.main), 1200, 760)
	a.ConnectInline(cn, nil)
	testutil.WaitFor(t, tt, "the connect to end", func() bool { return cn.Status == connection.StatusFailed })
	if cn.ErrKind == db.ProblemKeychain {
		t.Fatalf("stopped by the keychain: %q", cn.Err)
	}
}
