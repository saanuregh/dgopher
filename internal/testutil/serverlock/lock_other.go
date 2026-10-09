//go:build !unix

package serverlock

import "testing"

// Lock does not lock here: run the integration tests with go test -p 1
// so that one package at a time uses the servers.
func Lock(t testing.TB) {}
