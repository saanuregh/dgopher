package db

import (
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
