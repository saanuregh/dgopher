//go:build !unix

package testutil

import "testing"

// lockServers does not lock here: run the integration tests with
// go test -p 1 so that one package at a time uses the servers.
func lockServers(t *testing.T) {}
