package chat

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

// What the chat's reads may not call: each engine's functions reading
// files or URLs, and DuckDB's file names wherever a table goes.
func TestPinFileReaders(t *testing.T) {
	reads := func(e db.Engine, sql string) string { return fileReader(e, sql) }
	stmts := []string{
		"SELECT pg_read_file('/etc/passwd')", "SELECT load_file('/etc/passwd')", "SELECT readfile('x')",
		"SELECT * FROM read_csv('x.csv')", "SELECT * FROM 'x.parquet'", "SELECT * FROM \"data.csv\"",
		"SELECT * FROM url('http://x', CSV)", "SELECT * FROM file('x')", "SELECT * FROM s3('x')", "SELECT 'x.csv'",
		"SELECT lo_get(1)", "SELECT * FROM t",
	}
	pinValues(t, "file readers", map[string]string{
		"postgres SELECT pg_read_file('/etc/passwd')":   "pg_read_file",
		"postgres SELECT load_file('/etc/passwd')":      "",
		"postgres SELECT readfile('x')":                 "",
		"postgres SELECT * FROM read_csv('x.csv')":      "",
		"postgres SELECT * FROM 'x.parquet'":            "",
		"postgres SELECT * FROM \"data.csv\"":           "",
		"postgres SELECT * FROM url('http://x', CSV)":   "",
		"postgres SELECT * FROM file('x')":              "",
		"postgres SELECT * FROM s3('x')":                "",
		"postgres SELECT 'x.csv'":                       "",
		"postgres SELECT lo_get(1)":                     "lo_get",
		"postgres SELECT * FROM t":                      "",
		"mysql SELECT pg_read_file('/etc/passwd')":      "",
		"mysql SELECT load_file('/etc/passwd')":         "load_file",
		"mysql SELECT readfile('x')":                    "",
		"mysql SELECT * FROM read_csv('x.csv')":         "",
		"mysql SELECT * FROM 'x.parquet'":               "",
		"mysql SELECT * FROM \"data.csv\"":              "",
		"mysql SELECT * FROM url('http://x', CSV)":      "",
		"mysql SELECT * FROM file('x')":                 "",
		"mysql SELECT * FROM s3('x')":                   "",
		"mysql SELECT 'x.csv'":                          "",
		"mysql SELECT lo_get(1)":                        "",
		"mysql SELECT * FROM t":                         "",
		"clickhouse SELECT pg_read_file('/etc/passwd')": "",
		"clickhouse SELECT load_file('/etc/passwd')":    "",
		"clickhouse SELECT readfile('x')":               "",
		"clickhouse SELECT * FROM read_csv('x.csv')":    "",
		"clickhouse SELECT * FROM 'x.parquet'":          "",
		"clickhouse SELECT * FROM \"data.csv\"":         "",
		"clickhouse SELECT * FROM url('http://x', CSV)": "url",
		"clickhouse SELECT * FROM file('x')":            "file",
		"clickhouse SELECT * FROM s3('x')":              "s3",
		"clickhouse SELECT 'x.csv'":                     "",
		"clickhouse SELECT lo_get(1)":                   "",
		"clickhouse SELECT * FROM t":                    "",
		"sqlite SELECT pg_read_file('/etc/passwd')":     "",
		"sqlite SELECT load_file('/etc/passwd')":        "",
		"sqlite SELECT readfile('x')":                   "readfile",
		"sqlite SELECT * FROM read_csv('x.csv')":        "",
		"sqlite SELECT * FROM 'x.parquet'":              "",
		"sqlite SELECT * FROM \"data.csv\"":             "",
		"sqlite SELECT * FROM url('http://x', CSV)":     "",
		"sqlite SELECT * FROM file('x')":                "",
		"sqlite SELECT * FROM s3('x')":                  "",
		"sqlite SELECT 'x.csv'":                         "",
		"sqlite SELECT lo_get(1)":                       "",
		"sqlite SELECT * FROM t":                        "",
		"duckdb SELECT pg_read_file('/etc/passwd')":     "a file or URL named as '/etc/passwd'",
		"duckdb SELECT load_file('/etc/passwd')":        "a file or URL named as '/etc/passwd'",
		"duckdb SELECT readfile('x')":                   "",
		"duckdb SELECT * FROM read_csv('x.csv')":        "read_csv",
		"duckdb SELECT * FROM 'x.parquet'":              "a file or URL named as 'x.parquet'",
		"duckdb SELECT * FROM \"data.csv\"":             "a file or URL named as \"data.csv\"",
		"duckdb SELECT * FROM url('http://x', CSV)":     "a file or URL named as 'http://x'",
		"duckdb SELECT * FROM file('x')":                "",
		"duckdb SELECT * FROM s3('x')":                  "",
		"duckdb SELECT 'x.csv'":                         "a file or URL named as 'x.csv'",
		"duckdb SELECT lo_get(1)":                       "",
		"duckdb SELECT * FROM t":                        "",
		"redis SELECT pg_read_file('/etc/passwd')":      "",
		"redis SELECT load_file('/etc/passwd')":         "",
		"redis SELECT readfile('x')":                    "",
		"redis SELECT * FROM read_csv('x.csv')":         "",
		"redis SELECT * FROM 'x.parquet'":               "",
		"redis SELECT * FROM \"data.csv\"":              "",
		"redis SELECT * FROM url('http://x', CSV)":      "",
		"redis SELECT * FROM file('x')":                 "",
		"redis SELECT * FROM s3('x')":                   "",
		"redis SELECT 'x.csv'":                          "",
		"redis SELECT lo_get(1)":                        "",
		"redis SELECT * FROM t":                         "",
		"unknown SELECT pg_read_file('/etc/passwd')":    "",
		"unknown SELECT load_file('/etc/passwd')":       "",
		"unknown SELECT readfile('x')":                  "",
		"unknown SELECT * FROM read_csv('x.csv')":       "",
		"unknown SELECT * FROM 'x.parquet'":             "",
		"unknown SELECT * FROM \"data.csv\"":            "",
		"unknown SELECT * FROM url('http://x', CSV)":    "",
		"unknown SELECT * FROM file('x')":               "",
		"unknown SELECT * FROM s3('x')":                 "",
		"unknown SELECT 'x.csv'":                        "",
		"unknown SELECT lo_get(1)":                      "",
		"unknown SELECT * FROM t":                       "",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			for _, s := range stmts {
				add(fmt.Sprintf("%s %s", e, s), reads(e, s))
			}
		}
	})
}
