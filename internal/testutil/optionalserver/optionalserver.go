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

// servers are the optional servers, by compose.yaml's service name, or
// the service and a listener of its own.
var servers = map[string]server{
	"mariadb":     {"127.0.0.1:13307", "docker compose --profile compat up -d --wait mariadb"},
	"tidb":        {"127.0.0.1:14000", "docker compose --profile compat up -d --wait tidb"},
	"cockroachdb": {"127.0.0.1:26257", "docker compose --profile compat up -d --wait cockroachdb"},
	"yugabytedb":  {"127.0.0.1:15433", "docker compose --profile compat up -d --wait yugabytedb"},
	"timescaledb": {"127.0.0.1:15434", "docker compose --profile compat up -d --wait timescaledb"},
	"sqlserver":   {"127.0.0.1:11433", "docker compose --profile enterprise up -d --wait sqlserver"},
	"oracle":      {"127.0.0.1:11521", "docker compose --profile enterprise up -d --wait oracle"},
	// The oracle service's TCPS listeners, absent from a container made
	// before compose.yaml had them, which the command makes again.
	"oracle-tcps":      {"127.0.0.1:12484", "docker compose --profile enterprise up -d --wait oracle"},
	"oracle-tcps-mtls": {"127.0.0.1:12485", "docker compose --profile enterprise up -d --wait oracle"},
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
