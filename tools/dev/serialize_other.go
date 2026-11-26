//go:build !unix

package main

// serializedServers says whether internal/testutil/serverlock makes the
// integration tests of separate packages take turns on this OS; here it
// does not, so they run one package at a time.
const serializedServers = false
