// Package redisserver says where the Redis integration tests' servers
// are: Redis's, or with DGOPHER_REDIS=valkey Valkey's, so that one suite
// tests both (compose.yaml).
package redisserver

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"

	"dgopher/internal/testutil/optionalserver"
)

// Host is where every test server listens.
const Host = "127.0.0.1"

// Servers are the test servers of one engine of Redis's family. The
// cluster's nodes and the master take the password "dgopher", the
// sentinel "sentinelpw".
type Servers struct {
	// Engine is the db.Engine the tests' connections are set to.
	Engine string
	// Port is the standalone server's.
	Port int
	// ModulesPort is the server with JSON and search, and on Redis 8
	// time series, vector sets and arrays.
	ModulesPort int
	// ClusterPorts are the cluster's three masters.
	ClusterPorts []int
	// MasterPort is the master the sentinel watches, as "mymaster".
	MasterPort int
	// SentinelPort is the sentinel's.
	SentinelPort int
}

var redis = Servers{Engine: "redis", Port: 16379, ModulesPort: 16385, ClusterPorts: []int{17000, 17001, 17002}, MasterPort: 16380, SentinelPort: 26379}

// valkey's standalone server is valkey-bundle's, with its modules: the
// integration tests take turns (serverlock), so they share it.
var valkey = Servers{Engine: "valkey", Port: 16386, ModulesPort: 16386, ClusterPorts: []int{17100, 17101, 17102}, MasterPort: 16390, SentinelPort: 26390}

// Get returns the servers DGOPHER_REDIS names, Redis's when unset, and
// skips the test when Valkey's standalone server is not running.
func Get(t testing.TB) Servers {
	t.Helper()
	s, err := pick(os.Getenv("DGOPHER_REDIS"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Valkey() {
		optionalserver.SkipUnless(t, "valkey")
	}
	return s
}

func pick(name string) (Servers, error) {
	switch name {
	case "", "redis":
		return redis, nil
	case "valkey":
		return valkey, nil
	}
	return Servers{}, fmt.Errorf("DGOPHER_REDIS=%q: use redis or valkey", name)
}

// Valkey reports whether the servers are Valkey's.
func (s Servers) Valkey() bool { return s.Engine == "valkey" }

// Address is host:port of the port given.
func Address(port int) string { return net.JoinHostPort(Host, strconv.Itoa(port)) }

// RequireCluster skips the test when the cluster is optional and not
// running.
func (s Servers) RequireCluster(t testing.TB) {
	t.Helper()
	optionalserver.SkipUnlessRunningAt(t, Address(s.ClusterPorts[0]))
}

// RequireSentinel skips the test when the sentinel is optional and not
// running.
func (s Servers) RequireSentinel(t testing.TB) {
	t.Helper()
	optionalserver.SkipUnlessRunningAt(t, Address(s.SentinelPort))
}

// SkipOnValkey skips a test of a feature Valkey lacks, saying which.
func (s Servers) SkipOnValkey(t testing.TB, lacks string) {
	t.Helper()
	if s.Valkey() {
		t.Skipf("Valkey has no %s", lacks)
	}
}
