package params

// Characterization pins: what this package's engine-dependent rules
// give today, for each engine and for an ID no build knows. Each test
// reaches its rule through one local closure; when the rule moves into a
// per-engine method, re-point that closure and leave the pinned values
// alone. A value printed as "unpinned" is one the table lacks.

import (
	"fmt"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/safety"
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

// An auto parameter goes as text to PostgreSQL, which reads it as the
// type the statement needs; elsewhere as a number when it reads as one.
func TestPinParameterValue(t *testing.T) {
	inputs := []Input{{Value: "42", Kind: "auto"}, {Value: "4.5", Kind: "auto"}, {Value: "0123", Kind: "auto"}, {Value: "null", Kind: "auto"},
		{Value: "abc", Kind: "auto"}, {Value: "42", Kind: "text"}, {Value: "42", Kind: "number"}}
	pinValues(t, "parameter value", map[string]string{
		"postgres auto 42":     "\"42\" <nil>",
		"postgres auto 4.5":    "\"4.5\" <nil>",
		"postgres auto 0123":   "\"0123\" <nil>",
		"postgres auto null":   "<nil> <nil>",
		"postgres auto abc":    "\"abc\" <nil>",
		"postgres text 42":     "\"42\" <nil>",
		"postgres number 42":   "42 <nil>",
		"postgres bind":        "SELECT $1, $2 []interface {}{\"7\", \"x\"} \":a = '7', :b = 'x'\" <nil>",
		"mysql auto 42":        "42 <nil>",
		"mysql auto 4.5":       "4.5 <nil>",
		"mysql auto 0123":      "\"0123\" <nil>",
		"mysql auto null":      "<nil> <nil>",
		"mysql auto abc":       "\"abc\" <nil>",
		"mysql text 42":        "\"42\" <nil>",
		"mysql number 42":      "42 <nil>",
		"mysql bind":           "SELECT ?, ? []interface {}{7, \"x\"} \":a = 7, :b = 'x'\" <nil>",
		"clickhouse auto 42":   "42 <nil>",
		"clickhouse auto 4.5":  "4.5 <nil>",
		"clickhouse auto 0123": "\"0123\" <nil>",
		"clickhouse auto null": "<nil> <nil>",
		"clickhouse auto abc":  "\"abc\" <nil>",
		"clickhouse text 42":   "\"42\" <nil>",
		"clickhouse number 42": "42 <nil>",
		"clickhouse bind":      "SELECT ?, ? []interface {}{7, \"x\"} \":a = 7, :b = 'x'\" <nil>",
		"sqlite auto 42":       "42 <nil>",
		"sqlite auto 4.5":      "4.5 <nil>",
		"sqlite auto 0123":     "\"0123\" <nil>",
		"sqlite auto null":     "<nil> <nil>",
		"sqlite auto abc":      "\"abc\" <nil>",
		"sqlite text 42":       "\"42\" <nil>",
		"sqlite number 42":     "42 <nil>",
		"sqlite bind":          "SELECT ?, ? []interface {}{7, \"x\"} \":a = 7, :b = 'x'\" <nil>",
		"duckdb auto 42":       "42 <nil>",
		"duckdb auto 4.5":      "4.5 <nil>",
		"duckdb auto 0123":     "\"0123\" <nil>",
		"duckdb auto null":     "<nil> <nil>",
		"duckdb auto abc":      "\"abc\" <nil>",
		"duckdb text 42":       "\"42\" <nil>",
		"duckdb number 42":     "42 <nil>",
		"duckdb bind":          "SELECT ?, ? []interface {}{7, \"x\"} \":a = 7, :b = 'x'\" <nil>",
		"redis auto 42":        "42 <nil>",
		"redis auto 4.5":       "4.5 <nil>",
		"redis auto 0123":      "\"0123\" <nil>",
		"redis auto null":      "<nil> <nil>",
		"redis auto abc":       "\"abc\" <nil>",
		"redis text 42":        "\"42\" <nil>",
		"redis number 42":      "42 <nil>",
		"unknown auto 42":      "42 <nil>",
		"unknown auto 4.5":     "4.5 <nil>",
		"unknown auto 0123":    "\"0123\" <nil>",
		"unknown auto null":    "<nil> <nil>",
		"unknown auto abc":     "\"abc\" <nil>",
		"unknown text 42":      "\"42\" <nil>",
		"unknown number 42":    "42 <nil>",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			for _, in := range inputs {
				v, err := Value(in, e)
				add(fmt.Sprintf("%s %s %s", e, in.Kind, in.Value), fmt.Sprintf("%#v %v", v, err))
			}
			if db.DialectOf(e) != nil {
				sql, args, shown, err := Bind("SELECT :a, :b", e, safety.Dialect(e), map[string]Input{":a": {Value: "7", Kind: "auto"}, ":b": {Value: "x", Kind: "auto"}})
				add(string(e)+" bind", fmt.Sprintf("%s %#v %q %v", sql, args, shown, err))
			}
		}
	})
}
