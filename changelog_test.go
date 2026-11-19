package main

import (
	"encoding/json"
	"os"
	"testing"

	"dgopher/internal/changelog"
)

// The embedded changelog has notes for the version mygo.json builds.
func TestChangelogHasBuildVersion(t *testing.T) {
	raw, err := os.ReadFile("mygo.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct{ Version string }
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if changelog.Section(changelogText, cfg.Version) == "" {
		t.Fatalf("CHANGELOG.md has no section for %q", cfg.Version)
	}
}
