package dashboard

// Characterization pins: what this package's engine-dependent rules
// give today, for each engine and for an ID no build knows. Each test
// reaches its rule through one local closure; when the rule moves into a
// per-engine method, re-point that closure and leave the pinned values
// alone. A value printed as "unpinned" is one the table lacks.

import (
	"testing"

	"dgopher/internal/db"
)

var pinEngines = []db.Engine{db.Postgres, db.MySQL, db.ClickHouse, db.SQLite, db.DuckDB, db.Redis, "unknown"}

// pinValues checks the values produce gives against want, by key: each
// key given must be pinned with the same value, and each pinned key given.
func pinValues(t *testing.T, name string, want map[string]string, produce func(add func(key, got string))) {
	t.Helper()
	seen := map[string]bool{}
	produce(func(key, got string) {
		t.Helper()
		if seen[key] {
			t.Fatalf("%s: key %q given twice", name, key)
		}
		seen[key] = true
		w, ok := want[key]
		switch {
		case !ok:
			t.Errorf("unpinned %s: %q: %q,", name, key, got)
		case got != w:
			t.Errorf("%s %s changed:\n got  %q\n want %q", name, key, got, w)
		}
	})
	for key := range want {
		if !seen[key] {
			t.Errorf("%s: %q is pinned but no longer given", name, key)
		}
	}
}

// The read-only transaction a panel's statement runs in, where the engine
// has one.
func TestPinReadOnlyBegin(t *testing.T) {
	begin := func(r db.SQLRules) string { return r.ReadOnlyBeginSQL() }
	pinValues(t, "read only begin", map[string]string{
		"postgres":   "START TRANSACTION READ ONLY",
		"mysql":      "START TRANSACTION READ ONLY",
		"clickhouse": "",
		"sqlite":     "",
		"duckdb":     "",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			// Redis and an unknown engine open no session, which run needs.
			if r, ok := db.SQL(e); ok {
				add(string(e), begin(r))
			}
		}
	})
}
