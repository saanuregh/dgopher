package containercli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// pathWith puts fake executables of the given names on an otherwise empty
// PATH.
func pathWith(t *testing.T, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv(Override, "")
}

// Podman is used when both engines are installed.
func TestEnginePrefersPodman(t *testing.T) {
	pathWith(t, "docker", "podman")
	if got, err := Engine(); got != "podman" || err != nil {
		t.Errorf("got %q, %v", got, err)
	}
}

// Docker is used when Podman is not installed.
func TestEngineFallsBackToDocker(t *testing.T) {
	pathWith(t, "docker")
	if got, err := Engine(); got != "docker" || err != nil {
		t.Errorf("got %q, %v", got, err)
	}
}

// DGOPHER_CONTAINER chooses the engine even when the other is preferred.
func TestEngineHonorsOverride(t *testing.T) {
	pathWith(t, "docker", "podman")
	t.Setenv(Override, "docker")
	if got, err := Engine(); got != "docker" || err != nil {
		t.Errorf("got %q, %v", got, err)
	}
}

// An override naming neither engine is an error, not a silent default.
func TestEngineRejectsUnknownOverride(t *testing.T) {
	pathWith(t, "podman")
	t.Setenv(Override, "nerdctl")
	if got, err := Engine(); err == nil {
		t.Errorf("got %q, want an error", got)
	}
}

// With neither engine installed, the error says what to install.
func TestEngineReportsNoneFound(t *testing.T) {
	pathWith(t)
	if got, err := Engine(); err == nil {
		t.Errorf("got %q, want an error", got)
	}
}
