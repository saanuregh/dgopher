package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A .dgopher that is a junction is refused, as a symbolic link is: Go
// reports a junction as irregular, not as a symlink.
func TestJunctionDgopherRefused(t *testing.T) {
	dir := t.TempDir()
	first, _, err := Load(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	local := filepath.Join(dir, LocalDir)
	if err := os.RemoveAll(local); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", local, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v: %s", err, out)
	}
	if p, _, err := Load(dir, false); err == nil {
		p.Close()
		t.Fatal("a .dgopher junction was opened")
	}
	if entries, _ := os.ReadDir(target); len(entries) != 0 {
		t.Fatalf("wrote through the junction: %v", entries)
	}
}
