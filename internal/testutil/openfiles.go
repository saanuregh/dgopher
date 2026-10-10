package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Main runs a package's tests, then fails them if a file in a test's
// temporary folder is still open: Windows cannot remove such a folder, so
// the test fails there at cleanup, while Linux removes it and passes.
// Call it from TestMain as os.Exit(testutil.Main(m)).
func Main(m *testing.M) int {
	code := m.Run()
	if runtime.GOOS != "linux" {
		return code
	}
	var open []string
	// Some files close asynchronously, as a pool's connections do.
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		open = openTestFiles()
		if len(open) == 0 || time.Now().After(deadline) {
			break
		}
	}
	if len(open) == 0 {
		return code
	}
	fmt.Fprintln(os.Stderr, "files left open in tests' temporary folders:")
	for _, f := range open {
		fmt.Fprintln(os.Stderr, "\t"+f)
	}
	return 1
}

// testFolder matches t.TempDir's folder below the temporary directory,
// <TestName><digits>/.
var testFolder = regexp.MustCompile(`^Test[^/]*\d+/`)

// openTestFiles lists the regular files this process holds open inside a
// test's temporary folder, from /proc/self/fd.
func openTestFiles() []string {
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return nil
	}
	tmp := os.Getenv("GOTMPDIR") // where t.TempDir makes its folders
	if tmp == "" {
		tmp = os.TempDir()
	}
	tmp = filepath.Clean(tmp) + "/"
	var open []string
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name()))
		if err != nil {
			continue
		}
		rest, ok := strings.CutPrefix(target, tmp)
		if !ok || !testFolder.MatchString(rest) {
			continue
		}
		if fi, err := os.Stat(filepath.Join("/proc/self/fd", fd.Name())); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		open = append(open, target)
	}
	return open
}
