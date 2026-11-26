package redis

import (
	"slices"
	"testing"

	"dgopher/internal/db"
)

// Valkey's Bloom filter module names its type bloomfltr, Redis's
// MBbloom--: both are a Bloom filter, offered once, and the filter scans
// for the engine's.
func TestBloomFilterTypeByEngine(t *testing.T) {
	for _, typ := range []string{"MBbloom--", "bloomfltr"} {
		if got := typeLabel(typ); got != "Bloom filter" {
			t.Errorf("%s is called %q", typ, got)
		}
	}
	for engine, want := range map[db.Engine]string{db.Redis: "MBbloom--", db.Valkey: "bloomfltr"} {
		if got := typeOf("Bloom filter", engine); got != want {
			t.Errorf("%s: scans for %q, want %q", engine, got, want)
		}
	}
	if got := typeOf("hash", db.Valkey); got != "hash" {
		t.Errorf("Valkey's hash: %q", got)
	}
}

// The type filter offers the core types always, and a module's type only
// on a server whose command table has the module, each once.
func TestTypeChoicesFollowServer(t *testing.T) {
	core := []string{allTypes, "string", "hash", "list", "set", "sorted set", "stream"}
	has := func(cmds ...string) func(string) bool {
		return func(c string) bool { return slices.Contains(cmds, c) }
	}
	for _, c := range []struct {
		name string
		has  func(string) bool
		want []string
	}{
		{"no modules", has(), core},
		{"redis 8", has("JSON.GET", "VCARD", "ARLEN", "TS.INFO", "BF.EXISTS"),
			append(slices.Clone(core), "JSON", "vector set", "array", "time series", "Bloom filter")},
		{"valkey-bundle", has("JSON.GET", "BF.EXISTS", "FT.SEARCH"),
			append(slices.Clone(core), "JSON", "Bloom filter")},
	} {
		if got := typeChoices(c.has); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
