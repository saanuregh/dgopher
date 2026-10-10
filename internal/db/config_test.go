package db

import (
	"context"
	"strings"
	"testing"
)

func TestValidatePasswordSources(t *testing.T) {
	base := Config{Name: "x", Engine: Postgres, Host: "h"}
	cases := []struct {
		name string
		edit func(*Config)
		bad  bool
	}{
		{"keychain", func(*Config) {}, false},
		{"env", func(c *Config) { c.PasswordEnv = "DGOPHER_PW" }, false},
		{"command", func(c *Config) { c.PasswordCommand = "op read op://a/b/c" }, false},
		{"ask", func(c *Config) { c.AskPassword = true }, false},
		{"env and command", func(c *Config) { c.PasswordEnv, c.PasswordCommand = "DGOPHER_PW", "op read x" }, true},
		{"ask and command", func(c *Config) { c.AskPassword, c.PasswordCommand = true, "op read x" }, true},
		{"ask and env", func(c *Config) { c.AskPassword, c.PasswordEnv = true, "DGOPHER_PW" }, true},
	}
	for _, tc := range cases {
		c := base
		tc.edit(&c)
		err := c.Validate()
		if (err != nil) != tc.bad {
			t.Errorf("%s: %v", tc.name, err)
		}
		if err != nil && !strings.Contains(err.Error(), "one way") {
			t.Errorf("%s: unclear error %v", tc.name, err)
		}
	}
}

func TestValidColor(t *testing.T) {
	for s, want := range map[string]bool{"#16a34a": true, "#ABCDEF": true, "": false, "16a34a": false, "#16a34": false, "#16a34g": false, "red": false} {
		if got := ValidColor(s); got != want {
			t.Errorf("ValidColor(%q) = %v", s, got)
		}
	}
}

func TestJumps(t *testing.T) {
	s := SSHConfig{User: "deploy", Jump: " ops@jump1.example.com , jump2:2222,[::1]:22"}
	hops, err := s.Jumps()
	if err != nil || len(hops) != 3 || hops[0].User != "ops" || hops[0].Host != "jump1.example.com" ||
		hops[1].User != "deploy" || hops[1].Port != 2222 || hops[2].Host != "::1" {
		t.Fatalf("%+v %v", hops, err)
	}
	for _, bad := range []string{"jump:99999", "@host", "a b"} {
		if _, err := (&SSHConfig{User: "u", Jump: bad}).Jumps(); err == nil {
			t.Errorf("%q read", bad)
		}
	}
}

func TestIdentityCommand(t *testing.T) {
	cfg := Config{Engine: MySQL, Host: "db.example.com; rm -rf /", Port: 3307, User: "app user", Identity: IdentityAWS, IdentityProfile: "prod"}
	got := IdentityCommand(&cfg)
	want := []string{"aws", "rds", "generate-db-auth-token", "--hostname", "db.example.com; rm -rf /", "--port", "3307", "--username", "app user", "--profile", "prod"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", got)
	}
	if cmd := IdentityCommand(&Config{Identity: IdentityAzure}); cmd[0] != "az" {
		t.Fatalf("%q", cmd)
	}
}

// A cloud identity's token and a clear-text password go only to a server
// whose certificate and name are verified: unverified TLS hands them to
// whoever sits in the middle.
func TestIdentityNeedsVerifyFull(t *testing.T) {
	for _, mode := range TLSModes() {
		for _, cfg := range []Config{
			{Name: "x", Engine: MySQL, Host: "db.example.com", User: "app", Identity: IdentityAWS, TLS: mode},
			{Name: "x", Engine: Postgres, Host: "db.example.com", User: "app", Identity: IdentityGCP, TLS: mode},
			{Name: "x", Engine: MySQL, Host: "db.example.com", User: "app", ClearTextPassword: true, TLS: mode},
		} {
			err := cfg.Validate()
			if mode == TLSVerifyFull {
				if err != nil {
					t.Errorf("%s %s under verify-full: %v", cfg.Engine, cfg.Identity, err)
				}
				continue
			}
			if err == nil || !strings.Contains(err.Error(), "verify-full") || !strings.Contains(err.Error(), "CA") {
				t.Errorf("%s %s under %q: %v", cfg.Engine, cfg.Identity, mode, err)
			}
		}
	}
}

// The values an AWS identity puts on the aws command line are plain
// names: no spaces, quotes, shell or batch characters, nor a leading dash
// read as an option.
func TestAWSIdentityValuesAllowlisted(t *testing.T) {
	base := Config{Name: "x", Engine: MySQL, Host: "orders.abc123.eu-west-1.rds.amazonaws.com", Port: 3306, User: "app_user.1@corp-x",
		Identity: IdentityAWS, IdentityRegion: "eu-west-1", IdentityProfile: "prod_2.a-b", TLS: TLSVerifyFull}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*Config){
		"host with a command":  func(c *Config) { c.Host = "db.example.com; rm -rf /" },
		"host with a batch &":  func(c *Config) { c.Host = "db&calc" },
		"host as an option":    func(c *Config) { c.Host = "-db" },
		"user with a space":    func(c *Config) { c.User = "app user" },
		"user with a quote":    func(c *Config) { c.User = `app"` },
		"user with a percent":  func(c *Config) { c.User = "%PATH%" },
		"region in capitals":   func(c *Config) { c.IdentityRegion = "EU-WEST-1" },
		"region with a caret":  func(c *Config) { c.IdentityRegion = "eu^west" },
		"profile with a pipe":  func(c *Config) { c.IdentityProfile = "a|b" },
		"profile as an option": func(c *Config) { c.IdentityProfile = "--debug" },
		"region as an option":  func(c *Config) { c.IdentityRegion = "-x" },
	} {
		c := base
		edit(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: taken", name)
		}
	}
	// Other identities run commands without the connection's values.
	gcp := base
	gcp.Identity, gcp.Engine, gcp.User = IdentityGCP, Postgres, "app user@corp.iam"
	if err := gcp.Validate(); err != nil {
		t.Errorf("Google Cloud: %v", err)
	}
}

// Savepoints says which engines nest a savepoint in an open transaction:
// DuckDB's grammar has none, and ClickHouse holds no transactions.
func TestEngineSavepoints(t *testing.T) {
	for e, want := range map[Engine]bool{Postgres: true, MySQL: true, SQLite: true, DuckDB: false, ClickHouse: false, Redis: false} {
		if got := e.Savepoints(); got != want {
			t.Errorf("%s: Savepoints() = %v", e, got)
		}
	}
	ctx := context.Background()
	for _, e := range []Engine{SQLite, DuckDB} {
		d, err := Open(ctx, Config{Name: "s", Engine: e, Database: ":memory:"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		conn, err := d.SQL.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
			t.Fatal(err)
		}
		_, err = conn.ExecContext(ctx, "SAVEPOINT p")
		if (err == nil) != e.Savepoints() {
			t.Errorf("%s: SAVEPOINT in a transaction: %v", e, err)
		}
		conn.ExecContext(ctx, "ROLLBACK")
		conn.Close()
		d.Close()
	}
}
