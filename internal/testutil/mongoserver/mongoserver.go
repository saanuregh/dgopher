// Package mongoserver says where the MongoDB integration tests' servers
// are (compose.yaml): a one-member replica set, a standalone server and
// FerretDB. Each URI can be overridden by an environment variable.
package mongoserver

import (
	"os"
	"testing"

	"dgopher/internal/testutil"
	"dgopher/internal/testutil/optionalserver"
)

// The replica set's member is named localhost:27017, its address inside
// the container, so a client that discovered the set from the host would
// dial the wrong port: directConnection=true keeps it on the published one.
const (
	primaryURI    = "mongodb://127.0.0.1:27018/?directConnection=true"
	standaloneURI = "mongodb://127.0.0.1:27019/?directConnection=true"
	ferretDBURI   = "mongodb://dgopher:dgopher@127.0.0.1:27020/?directConnection=true"
)

// Primary returns the URI of the replica set rs0's primary, which has
// transactions and change streams, or DGOPHER_MONGO_URL.
func Primary(t *testing.T) string {
	t.Helper()
	testutil.Integration(t)
	return uri("DGOPHER_MONGO_URL", primaryURI)
}

// Standalone returns the URI of the standalone server, or
// DGOPHER_MONGO_STANDALONE_URL, and skips the test when the default
// server is not running.
func Standalone(t *testing.T) string {
	t.Helper()
	testutil.Integration(t)
	return optional(t, "DGOPHER_MONGO_STANDALONE_URL", standaloneURI, "mongo-standalone")
}

// FerretDB returns the URI of FerretDB, as user dgopher, or
// DGOPHER_FERRETDB_URL, and skips the test when the default server is not
// running.
func FerretDB(t *testing.T) string {
	t.Helper()
	testutil.Integration(t)
	return optional(t, "DGOPHER_FERRETDB_URL", ferretDBURI, "ferretdb")
}

func optional(t *testing.T, env, fallback, service string) string {
	t.Helper()
	if v := os.Getenv(env); v != "" {
		return v
	}
	optionalserver.SkipUnless(t, service)
	return fallback
}

func uri(env, fallback string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return fallback
}
