package export

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

// The literals of a SQL export: each engine's own, standard SQL as SQLite
// reads it for any other engine, or none.
func TestPinSQLExportLiterals(t *testing.T) {
	row := []any{"it's", `a\b`, "a\x00b", []byte{0xde, 0xad}, true, int64(42), 1.5, nil}
	pinValues(t, "sql export literals", map[string]string{
		"\"postgres\"":   "INSERT INTO \"t\" (\"a\", \"b\", \"c\", \"d\", \"e\", \"f\", \"g\", \"h\") VALUES ('it''s', E'a\\\\b', ('a' || chr(0) || 'b'), E'\\\\xdead'::bytea, TRUE, 42, 1.5, NULL);\n <nil>",
		"\"mysql\"":      "INSERT INTO \"t\" (\"a\", \"b\", \"c\", \"d\", \"e\", \"f\", \"g\", \"h\") VALUES ('it''s', 'a\\\\b', 'a\\0b', X'dead', TRUE, 42, 1.5, NULL);\n <nil>",
		"\"clickhouse\"": "INSERT INTO \"t\" (\"a\", \"b\", \"c\", \"d\", \"e\", \"f\", \"g\", \"h\") VALUES ('it\\'s', 'a\\\\b', 'a\\0b', unhex('dead'), TRUE, 42, 1.5, NULL);\n <nil>",
		"\"sqlite\"":     "INSERT INTO \"t\" (\"a\", \"b\", \"c\", \"d\", \"e\", \"f\", \"g\", \"h\") VALUES ('it''s', 'a\\b', ('a' || char(0) || 'b'), X'dead', TRUE, 42, 1.5, NULL);\n <nil>",
		"\"duckdb\"":     "INSERT INTO \"t\" (\"a\", \"b\", \"c\", \"d\", \"e\", \"f\", \"g\", \"h\") VALUES ('it''s', E'a\\\\b', ('a' || chr(0) || 'b'), '\\xDE\\xAD'::BLOB, TRUE, 42, 1.5, NULL);\n <nil>",
		"\"redis\"":      "INSERT INTO \"t\" (\"a\", \"b\", \"c\", \"d\", \"e\", \"f\", \"g\", \"h\") VALUES ('it''s', 'a\\b', ('a' || char(0) || 'b'), X'dead', TRUE, 42, 1.5, NULL);\n <nil>",
		"\"unknown\"":    "INSERT INTO \"t\" (\"a\", \"b\", \"c\", \"d\", \"e\", \"f\", \"g\", \"h\") VALUES ('it''s', 'a\\b', ('a' || char(0) || 'b'), X'dead', TRUE, 42, 1.5, NULL);\n <nil>",
		"\"\"":           "INSERT INTO \"t\" (\"a\", \"b\", \"c\", \"d\", \"e\", \"f\", \"g\", \"h\") VALUES ('it''s', 'a\\b', ('a' || char(0) || 'b'), X'dead', TRUE, 42, 1.5, NULL);\n <nil>",
	}, func(add func(key, got string)) {
		for _, e := range append(pinEngines, "") {
			out, err := Text(SQL, []string{"a", "b", "c", "d", "e", "f", "g", "h"}, [][]any{row}, Options{Table: "t", Engine: e})
			add(fmt.Sprintf("%q", e), fmt.Sprintf("%s %v", out, err))
		}
	})
}
