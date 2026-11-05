package redis

import (
	"strings"
	"testing"
)

// A command MONITOR told cut at its length limit shows its name only:
// its arguments may hold a secret redaction can no longer find.
func TestCutMonitorLineHidesArguments(t *testing.T) {
	for _, c := range []struct{ in, out string }{
		{`"CONFIG" "SET" "requirepass" "hunter2`, `CONFIG … (cut: arguments hidden)`},
		{`"AUTH" "s3cret`, `AUTH … (cut: arguments hidden)`},
		{`"GET" "k"`, `GET k`},
	} {
		got := monitorText(c.in)
		if got != c.out || strings.Contains(got, "hunter2") {
			t.Errorf("%s: shown %q, want %q", c.in, got, c.out)
		}
	}
}
