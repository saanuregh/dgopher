// Package optionalserver skips the tests of an integration server that
// compose.yaml starts only in a profile, when it is not running.
package optionalserver

import (
	"net"
	"testing"
	"time"
)

// server is where an optional server listens, and the command that
// starts it.
type server struct {
	address string
	start   string
}

// servers are the optional servers, by compose.yaml's service name.
var servers = map[string]server{
	"mariadb": {"127.0.0.1:13307", "docker compose --profile compat up -d --wait mariadb"},
	"tidb":    {"127.0.0.1:14000", "docker compose --profile compat up -d --wait tidb"},
}

// SkipUnless skips the test unless the optional server service is
// running, naming the command that starts it.
func SkipUnless(t testing.TB, service string) {
	t.Helper()
	s, ok := servers[service]
	if !ok {
		t.Fatalf("no optional server %q", service)
	}
	skipUnlessListening(t, s)
}

// SkipUnlessRunningAt skips the test when address is an optional server's
// that is not running; any other address it leaves to the test.
func SkipUnlessRunningAt(t testing.TB, address string) {
	t.Helper()
	for _, s := range servers {
		if s.address == address {
			skipUnlessListening(t, s)
		}
	}
}

// skipUnlessListening skips the test unless something accepts a TCP
// connection at the server's address.
func skipUnlessListening(t testing.TB, s server) {
	t.Helper()
	c, err := net.DialTimeout("tcp", s.address, time.Second)
	if err != nil {
		t.Skipf("no server at %s: start it with %s", s.address, s.start)
	}
	c.Close()
}
