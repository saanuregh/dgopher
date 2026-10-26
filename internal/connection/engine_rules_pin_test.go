package connection

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

// Only PostgreSQL's databases each need a connection, which a tab
// switches between.
func TestPinSwitchesDatabase(t *testing.T) {
	pinValues(t, "switches database", map[string]string{
		"postgres 1 databases":   "false",
		"postgres 2 databases":   "true",
		"mysql 1 databases":      "false",
		"mysql 2 databases":      "false",
		"clickhouse 1 databases": "false",
		"clickhouse 2 databases": "false",
		"sqlite 1 databases":     "false",
		"sqlite 2 databases":     "false",
		"duckdb 1 databases":     "false",
		"duckdb 2 databases":     "false",
		"redis 1 databases":      "false",
		"redis 2 databases":      "false",
		"unknown 1 databases":    "false",
		"unknown 2 databases":    "false",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			for _, n := range []int{1, 2} {
				cn := &Conn{Config: db.Config{Engine: e}, Databases: make([]string, n)}
				add(fmt.Sprintf("%s %d databases", e, n), fmt.Sprint(cn.SwitchesDatabase()))
			}
		}
	})
}
