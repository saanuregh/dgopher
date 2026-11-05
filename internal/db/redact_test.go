package db

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"dgopher/internal/secretcmd"
	"dgopher/internal/sshtunnel"
)

// A command's failure keeps its type through redaction, for the audit log
// to leave out what it printed, whatever secret that held.
func TestRedactKeepsCommandError(t *testing.T) {
	cfg := Config{Proxy: ProxyConfig{Password: "zzproxy-pass"}}
	err := redact(fmt.Errorf("AWS IAM (RDS, Aurora): %w",
		&secretcmd.CommandError{Command: "aws", Status: 255, Stderr: "An error occurred: pin=4711 for proxy zzproxy-pass"}), cfg)
	if strings.Contains(err.Error(), "zzproxy-pass") {
		t.Fatalf("the secret is in %q", err)
	}
	var ce *secretcmd.CommandError
	if !errors.As(err, &ce) || strings.Contains(ce.Stderr, "zzproxy-pass") {
		t.Fatalf("the command's failure: %#v", ce)
	}
	if got := secretcmd.AuditText(err); got != "AWS IAM (RDS, Aurora): aws failed (exit 255)" {
		t.Fatalf("audited as %q", got)
	}
}

// An unknown SSH host keeps its type, for the app to ask about its key.
func TestRedactKeepsHostKeyError(t *testing.T) {
	cfg := Config{SSH: SSHConfig{Password: "db1"}}
	err := redact(fmt.Errorf("ssh: %w", &sshtunnel.HostKeyError{Host: "db1.example.test:22", Fingerprint: "SHA256:zz"}), cfg)
	if strings.Contains(err.Error(), "db1") {
		t.Fatalf("the secret is in %q", err)
	}
	var hk *sshtunnel.HostKeyError
	if !errors.As(err, &hk) || hk.Fingerprint != "SHA256:zz" {
		t.Fatalf("the host key error: %#v", hk)
	}
}

// A password shorter than three characters is hidden where it stands as a
// word of its own, not inside other words.
func TestRedactShortPassword(t *testing.T) {
	cfg := Config{Password: "q9"}
	got := redact(errors.New(`connect: auth "q9" rejected; q9x and aq9 stay`), cfg).Error()
	if want := `connect: auth "•••" rejected; q9x and aq9 stay`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A cloud identity's token, which the connection logs in with in place of
// a password, is hidden as the password is.
func TestRedactIdentityToken(t *testing.T) {
	cfg := Config{Engine: Postgres, Host: "db.example.test", User: "app", Identity: IdentityAWS, IdentityRegion: "eu-west-1"}
	key := strings.Join(IdentityCommand(&cfg), "\x00")
	tokens.Lock()
	tokens.byCommand[key] = token{value: "zz-iam-token-77", made: time.Now()}
	tokens.Unlock()
	t.Cleanup(func() {
		tokens.Lock()
		delete(tokens.byCommand, key)
		tokens.Unlock()
	})
	err := redact(errors.New("login failed with password zz-iam-token-77"), cfg)
	if strings.Contains(err.Error(), "zz-iam-token-77") {
		t.Fatalf("the token is in %q", err)
	}
}
