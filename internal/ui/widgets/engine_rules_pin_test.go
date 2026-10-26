package widgets

// Characterization pins: what this package's engine-dependent rules
// give today, for each engine and for an ID no build knows. Each test
// reaches its rule through one local closure; when the rule moves into a
// per-engine method, re-point that closure and leave the pinned values
// alone. A value printed as "unpinned" is one the table lacks.

import (
	"fmt"
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

// The tint of each engine's icon; grey for one without its own.
func TestPinEngineColor(t *testing.T) {
	color := func(e db.Engine) string {
		c := EngineColor(e)
		return fmt.Sprintf("#%02x%02x%02x%02x", c.R, c.G, c.B, c.A)
	}
	pinValues(t, "engine color", map[string]string{
		"postgres":   "#336791ff",
		"mysql":      "#00758fff",
		"clickhouse": "#d4a10aff",
		"sqlite":     "#0f80ccff",
		"duckdb":     "#c49a00ff",
		"redis":      "#dc382dff",
		"unknown":    "#737373ff",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			add(string(e), color(e))
		}
	})
}
