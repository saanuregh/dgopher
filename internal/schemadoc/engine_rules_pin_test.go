package schemadoc

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

// A script with functions starts as pg_dump's does on PostgreSQL.
func TestPinScriptPreamble(t *testing.T) {
	pinValues(t, "script preamble", map[string]string{
		"postgres with a function":   "SET check_function_bodies = false;\n\nCREATE FUNCTION f() RETURNS int AS 'SELECT 1';\n\nCREATE TABLE t (a int);\n",
		"postgres tables only":       "CREATE TABLE t (a int);\n",
		"mysql with a function":      "CREATE FUNCTION f() RETURNS int AS 'SELECT 1';\n\nCREATE TABLE t (a int);\n",
		"mysql tables only":          "CREATE TABLE t (a int);\n",
		"clickhouse with a function": "CREATE FUNCTION f() RETURNS int AS 'SELECT 1';\n\nCREATE TABLE t (a int);\n",
		"clickhouse tables only":     "CREATE TABLE t (a int);\n",
		"sqlite with a function":     "CREATE FUNCTION f() RETURNS int AS 'SELECT 1';\n\nCREATE TABLE t (a int);\n",
		"sqlite tables only":         "CREATE TABLE t (a int);\n",
		"duckdb with a function":     "CREATE FUNCTION f() RETURNS int AS 'SELECT 1';\n\nCREATE TABLE t (a int);\n",
		"duckdb tables only":         "CREATE TABLE t (a int);\n",
		"redis with a function":      "CREATE FUNCTION f() RETURNS int AS 'SELECT 1';\n\nCREATE TABLE t (a int);\n",
		"redis tables only":          "CREATE TABLE t (a int);\n",
		"unknown with a function":    "CREATE FUNCTION f() RETURNS int AS 'SELECT 1';\n\nCREATE TABLE t (a int);\n",
		"unknown tables only":        "CREATE TABLE t (a int);\n",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			s := &Schema{Engine: e,
				Tables: []Table{{Object: db.Object{Name: "t", Kind: db.KindTable}, Definition: "CREATE TABLE t (a int)"}},
				Items:  []Item{{Item: db.Item{Kind: db.ItemFunction, Name: "f"}, Definition: "CREATE FUNCTION f() RETURNS int AS 'SELECT 1'"}}}
			add(string(e)+" with a function", Script(s))
			s.Items = nil
			add(string(e)+" tables only", Script(s))
		}
	})
}
