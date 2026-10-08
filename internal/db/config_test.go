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
