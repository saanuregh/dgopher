//go:build unix

// Package serverlock serializes the tests of every package that use the
// integration servers.
package serverlock

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Lock holds a file lock shared by the test processes until the test
// ends, so that one test at a time uses the integration servers.
func Lock(t testing.TB) {
	f, err := os.OpenFile(filepath.Join(os.TempDir(), "dgopher-integration.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
}
