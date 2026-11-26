// Package optionalserver skips the tests of an integration server that
// compose.yaml starts only in a profile, when it is not running.
package optionalserver

import (
	"net"
	"testing"
	"time"
)

// server is where an optional server listens, and compose.yaml's
// profile and service that start it.
type server struct {
	address, profile, service string
}

// servers are the optional servers, by compose.yaml's service name, or
// the service and a listener of its own.
var servers = map[string]server{
	"mariadb":     {"127.0.0.1:13307", "compat", "mariadb"},
	"tidb":        {"127.0.0.1:14000", "compat", "tidb"},
	"cockroachdb": {"127.0.0.1:26257", "compat", "cockroachdb"},
	"yugabytedb":  {"127.0.0.1:15433", "compat", "yugabytedb"},
	"timescaledb": {"127.0.0.1:15434", "compat", "timescaledb"},
	"sqlserver":   {"127.0.0.1:11433", "enterprise", "sqlserver"},
	"oracle":      {"127.0.0.1:11521", "enterprise", "oracle"},
	// The oracle service's TCPS listeners, absent from a container made
	// before compose.yaml had them, which the command makes again.
	"oracle-tcps":      {"127.0.0.1:12484", "enterprise", "oracle"},
	"oracle-tcps-mtls": {"127.0.0.1:12485", "enterprise", "oracle"},
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
		t.Skipf("no server at %s: start it with go run ./tools/dev servers up --profile %s %s", s.address, s.profile, s.service)
	}
	c.Close()
}
