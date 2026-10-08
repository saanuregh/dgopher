//go:build unix

package testutil

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// lockServers holds a file lock shared by the test processes until the
// test ends.
func lockServers(t *testing.T) {
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
