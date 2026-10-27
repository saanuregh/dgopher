package safety

// Characterization pins: what the safety policy's engine-dependent rules
// give today, for each engine and for an ID no build knows. Each test
// reaches its rule through one local closure; when the rule moves into a
// per-engine method, re-point that closure and leave the pinned values
// alone. A value printed as "unpinned" is one the table lacks.

import (
	"fmt"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/sqltext"
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

func verdictText(v Verdict) string {
	return fmt.Sprintf("blocked=%q confirm=%v typeName=%v writes=%v reasons=%q", v.Blocked, v.Confirm, v.TypeName, v.Writes, v.Reasons)
}

// What cannot run in a transaction, which manual commit opens none for.
func TestPinOutsideTransaction(t *testing.T) {
	outside := func(e db.Engine, sql string) bool { return OutsideTransaction(e, sql) }
	stmts := []string{
		"VACUUM", "vacuum analyze t", "VACUUM FULL t", "ALTER SYSTEM SET work_mem = '1MB'", "ALTER TABLE t ADD c int",
		"REINDEX TABLE CONCURRENTLY t", "REINDEX TABLE t", "CREATE DATABASE d", "DROP TABLESPACE s",
		"CREATE UNIQUE INDEX CONCURRENTLY i ON t (a)", "DROP INDEX CONCURRENTLY i", "CREATE TABLE concurrently (a int)",
		"OPTIMIZE TABLE t", "-- note\nVACUUM", "SELECT 1", "",
	}
	pinValues(t, "outside transaction", map[string]string{
		"postgres \"VACUUM\"":                                        "true",
		"postgres \"vacuum analyze t\"":                              "true",
		"postgres \"VACUUM FULL t\"":                                 "true",
		"postgres \"ALTER SYSTEM SET work_mem = '1MB'\"":             "true",
		"postgres \"ALTER TABLE t ADD c int\"":                       "false",
		"postgres \"REINDEX TABLE CONCURRENTLY t\"":                  "true",
		"postgres \"REINDEX TABLE t\"":                               "false",
		"postgres \"CREATE DATABASE d\"":                             "true",
		"postgres \"DROP TABLESPACE s\"":                             "true",
		"postgres \"CREATE UNIQUE INDEX CONCURRENTLY i ON t (a)\"":   "true",
		"postgres \"DROP INDEX CONCURRENTLY i\"":                     "true",
		"postgres \"CREATE TABLE concurrently (a int)\"":             "false",
		"postgres \"OPTIMIZE TABLE t\"":                              "false",
		"postgres \"-- note\\nVACUUM\"":                              "true",
		"postgres \"SELECT 1\"":                                      "false",
		"postgres \"\"":                                              "false",
		"mysql \"VACUUM\"":                                           "false",
		"mysql \"vacuum analyze t\"":                                 "false",
		"mysql \"VACUUM FULL t\"":                                    "false",
		"mysql \"ALTER SYSTEM SET work_mem = '1MB'\"":                "false",
		"mysql \"ALTER TABLE t ADD c int\"":                          "false",
		"mysql \"REINDEX TABLE CONCURRENTLY t\"":                     "false",
		"mysql \"REINDEX TABLE t\"":                                  "false",
		"mysql \"CREATE DATABASE d\"":                                "false",
		"mysql \"DROP TABLESPACE s\"":                                "false",
		"mysql \"CREATE UNIQUE INDEX CONCURRENTLY i ON t (a)\"":      "false",
		"mysql \"DROP INDEX CONCURRENTLY i\"":                        "false",
		"mysql \"CREATE TABLE concurrently (a int)\"":                "false",
		"mysql \"OPTIMIZE TABLE t\"":                                 "false",
		"mysql \"-- note\\nVACUUM\"":                                 "false",
		"mysql \"SELECT 1\"":                                         "false",
		"mysql \"\"":                                                 "false",
		"clickhouse \"VACUUM\"":                                      "false",
		"clickhouse \"vacuum analyze t\"":                            "false",
		"clickhouse \"VACUUM FULL t\"":                               "false",
		"clickhouse \"ALTER SYSTEM SET work_mem = '1MB'\"":           "false",
		"clickhouse \"ALTER TABLE t ADD c int\"":                     "false",
		"clickhouse \"REINDEX TABLE CONCURRENTLY t\"":                "false",
		"clickhouse \"REINDEX TABLE t\"":                             "false",
		"clickhouse \"CREATE DATABASE d\"":                           "false",
		"clickhouse \"DROP TABLESPACE s\"":                           "false",
		"clickhouse \"CREATE UNIQUE INDEX CONCURRENTLY i ON t (a)\"": "false",
		"clickhouse \"DROP INDEX CONCURRENTLY i\"":                   "false",
		"clickhouse \"CREATE TABLE concurrently (a int)\"":           "false",
		"clickhouse \"OPTIMIZE TABLE t\"":                            "false",
		"clickhouse \"-- note\\nVACUUM\"":                            "false",
		"clickhouse \"SELECT 1\"":                                    "false",
		"clickhouse \"\"":                                            "false",
		"sqlite \"VACUUM\"":                                          "true",
		"sqlite \"vacuum analyze t\"":                                "true",
		"sqlite \"VACUUM FULL t\"":                                   "true",
		"sqlite \"ALTER SYSTEM SET work_mem = '1MB'\"":               "false",
		"sqlite \"ALTER TABLE t ADD c int\"":                         "false",
		"sqlite \"REINDEX TABLE CONCURRENTLY t\"":                    "false",
		"sqlite \"REINDEX TABLE t\"":                                 "false",
		"sqlite \"CREATE DATABASE d\"":                               "false",
		"sqlite \"DROP TABLESPACE s\"":                               "false",
		"sqlite \"CREATE UNIQUE INDEX CONCURRENTLY i ON t (a)\"":     "false",
		"sqlite \"DROP INDEX CONCURRENTLY i\"":                       "false",
		"sqlite \"CREATE TABLE concurrently (a int)\"":               "false",
		"sqlite \"OPTIMIZE TABLE t\"":                                "false",
		"sqlite \"-- note\\nVACUUM\"":                                "true",
		"sqlite \"SELECT 1\"":                                        "false",
		"sqlite \"\"":                                                "false",
		"duckdb \"VACUUM\"":                                          "true",
		"duckdb \"vacuum analyze t\"":                                "true",
		"duckdb \"VACUUM FULL t\"":                                   "true",
		"duckdb \"ALTER SYSTEM SET work_mem = '1MB'\"":               "false",
		"duckdb \"ALTER TABLE t ADD c int\"":                         "false",
		"duckdb \"REINDEX TABLE CONCURRENTLY t\"":                    "false",
		"duckdb \"REINDEX TABLE t\"":                                 "false",
		"duckdb \"CREATE DATABASE d\"":                               "false",
		"duckdb \"DROP TABLESPACE s\"":                               "false",
		"duckdb \"CREATE UNIQUE INDEX CONCURRENTLY i ON t (a)\"":     "false",
		"duckdb \"DROP INDEX CONCURRENTLY i\"":                       "false",
		"duckdb \"CREATE TABLE concurrently (a int)\"":               "false",
		"duckdb \"OPTIMIZE TABLE t\"":                                "false",
		"duckdb \"-- note\\nVACUUM\"":                                "true",
		"duckdb \"SELECT 1\"":                                        "false",
		"duckdb \"\"":                                                "false",
		"redis \"VACUUM\"":                                           "false",
		"redis \"vacuum analyze t\"":                                 "false",
		"redis \"VACUUM FULL t\"":                                    "false",
		"redis \"ALTER SYSTEM SET work_mem = '1MB'\"":                "false",
		"redis \"ALTER TABLE t ADD c int\"":                          "false",
		"redis \"REINDEX TABLE CONCURRENTLY t\"":                     "false",
		"redis \"REINDEX TABLE t\"":                                  "false",
		"redis \"CREATE DATABASE d\"":                                "false",
		"redis \"DROP TABLESPACE s\"":                                "false",
		"redis \"CREATE UNIQUE INDEX CONCURRENTLY i ON t (a)\"":      "false",
		"redis \"DROP INDEX CONCURRENTLY i\"":                        "false",
		"redis \"CREATE TABLE concurrently (a int)\"":                "false",
		"redis \"OPTIMIZE TABLE t\"":                                 "false",
		"redis \"-- note\\nVACUUM\"":                                 "false",
		"redis \"SELECT 1\"":                                         "false",
		"redis \"\"":                                                 "false",
		"unknown \"VACUUM\"":                                         "false",
		"unknown \"vacuum analyze t\"":                               "false",
		"unknown \"VACUUM FULL t\"":                                  "false",
		"unknown \"ALTER SYSTEM SET work_mem = '1MB'\"":              "false",
		"unknown \"ALTER TABLE t ADD c int\"":                        "false",
		"unknown \"REINDEX TABLE CONCURRENTLY t\"":                   "false",
		"unknown \"REINDEX TABLE t\"":                                "false",
		"unknown \"CREATE DATABASE d\"":                              "false",
		"unknown \"DROP TABLESPACE s\"":                              "false",
		"unknown \"CREATE UNIQUE INDEX CONCURRENTLY i ON t (a)\"":    "false",
		"unknown \"DROP INDEX CONCURRENTLY i\"":                      "false",
		"unknown \"CREATE TABLE concurrently (a int)\"":              "false",
		"unknown \"OPTIMIZE TABLE t\"":                               "false",
		"unknown \"-- note\\nVACUUM\"":                               "false",
		"unknown \"SELECT 1\"":                                       "false",
		"unknown \"\"":                                               "false",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			for _, s := range stmts {
				add(fmt.Sprintf("%s %q", e, s), fmt.Sprint(outside(e, s)))
			}
		}
	})
}

// MySQL runs what an executable comment holds: the policy reads it.
func TestPinPolicyText(t *testing.T) {
	stmts := []string{"/*!50001 DROP TABLE t */", "/*M! DELETE FROM t */", "/* DROP TABLE t */ SELECT 1"}
	pinValues(t, "policy text", map[string]string{
		"postgres \"/*!50001 DROP TABLE t */\"":      "/*!50001 DROP TABLE t */ | blocked=\"c is read-only: This statement statements are not allowed. Edit the connection to allow writes.\" confirm=false typeName=false writes=true reasons=[]",
		"postgres \"/*M! DELETE FROM t */\"":         "/*M! DELETE FROM t */ | blocked=\"c is read-only: This statement statements are not allowed. Edit the connection to allow writes.\" confirm=false typeName=false writes=true reasons=[]",
		"postgres \"/* DROP TABLE t */ SELECT 1\"":   "/* DROP TABLE t */ SELECT 1 | blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"mysql \"/*!50001 DROP TABLE t */\"":         "  DROP TABLE t   | blocked=\"c is read-only: DROP statements are not allowed. Edit the connection to allow writes.\" confirm=false typeName=false writes=true reasons=[]",
		"mysql \"/*M! DELETE FROM t */\"":            "  DELETE FROM t   | blocked=\"c is read-only: DELETE statements are not allowed. Edit the connection to allow writes.\" confirm=false typeName=false writes=true reasons=[]",
		"mysql \"/* DROP TABLE t */ SELECT 1\"":      "/* DROP TABLE t */ SELECT 1 | blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse \"/*!50001 DROP TABLE t */\"":    "/*!50001 DROP TABLE t */ | blocked=\"c is read-only: This statement statements are not allowed. Edit the connection to allow writes.\" confirm=false typeName=false writes=true reasons=[]",
		"clickhouse \"/*M! DELETE FROM t */\"":       "/*M! DELETE FROM t */ | blocked=\"c is read-only: This statement statements are not allowed. Edit the connection to allow writes.\" confirm=false typeName=false writes=true reasons=[]",
		"clickhouse \"/* DROP TABLE t */ SELECT 1\"": "/* DROP TABLE t */ SELECT 1 | blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite \"/*!50001 DROP TABLE t */\"":        "/*!50001 DROP TABLE t */ | blocked=\"c is read-only: This statement statements are not allowed. Edit the connection to allow writes.\" confirm=false typeName=false writes=true reasons=[]",
		"sqlite \"/*M! DELETE FROM t */\"":           "/*M! DELETE FROM t */ | blocked=\"c is read-only: This statement statements are not allowed. Edit the connection to allow writes.\" confirm=false typeName=false writes=true reasons=[]",
		"sqlite \"/* DROP TABLE t */ SELECT 1\"":     "/* DROP TABLE t */ SELECT 1 | blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb \"/*!50001 DROP TABLE t */\"":        "/*!50001 DROP TABLE t */ | blocked=\"c is read-only: This statement statements are not allowed. Edit the connection to allow writes.\" confirm=false typeName=false writes=true reasons=[]",
		"duckdb \"/*M! DELETE FROM t */\"":           "/*M! DELETE FROM t */ | blocked=\"c is read-only: This statement statements are not allowed. Edit the connection to allow writes.\" confirm=false typeName=false writes=true reasons=[]",
		"duckdb \"/* DROP TABLE t */ SELECT 1\"":     "/* DROP TABLE t */ SELECT 1 | blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis \"/*!50001 DROP TABLE t */\"":         "/*!50001 DROP TABLE t */ | blocked=\"c is read-only: This statement statements are not allowed. Edit the connection to allow writes.\" confirm=false typeName=false writes=true reasons=[]",
		"redis \"/*M! DELETE FROM t */\"":            "/*M! DELETE FROM t */ | blocked=\"c is read-only: This statement statements are not allowed. Edit the connection to allow writes.\" confirm=false typeName=false writes=true reasons=[]",
		"redis \"/* DROP TABLE t */ SELECT 1\"":      "/* DROP TABLE t */ SELECT 1 | blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown \"/*!50001 DROP TABLE t */\"":       "/*!50001 DROP TABLE t */ | blocked=\"c is read-only: This statement statements are not allowed. Edit the connection to allow writes.\" confirm=false typeName=false writes=true reasons=[]",
		"unknown \"/*M! DELETE FROM t */\"":          "/*M! DELETE FROM t */ | blocked=\"c is read-only: This statement statements are not allowed. Edit the connection to allow writes.\" confirm=false typeName=false writes=true reasons=[]",
		"unknown \"/* DROP TABLE t */ SELECT 1\"":    "/* DROP TABLE t */ SELECT 1 | blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			cfg := &db.Config{Name: "c", Engine: e, Env: db.Development, ReadOnly: true}
			for _, s := range stmts {
				add(fmt.Sprintf("%s %q", e, s), PolicyText(e, s)+" | "+verdictText(ReviewSQL(cfg, Analyze(cfg, []string{s}))))
			}
		}
	})
}

// The truth table of EndsRunTransaction's autocommit-off rule
// (safety.go: off := autocommitOff && cfg.Engine == db.MySQL): a write the
// server holds under autocommit off, then a statement committing it.
func TestPinAutocommitOffTruthTable(t *testing.T) {
	runs := map[string][]string{
		"write then DDL":              {"INSERT INTO t VALUES (1)", "CREATE TABLE u (a INT)"},
		"set off, write, then DDL":    {"SET autocommit = 0", "INSERT INTO t VALUES (1)", "CREATE TABLE u (a INT)"},
		"set on, write, then DDL":     {"SET autocommit = 1", "INSERT INTO t VALUES (1)", "CREATE TABLE u (a INT)"},
		"begin, write, then DDL":      {"BEGIN", "INSERT INTO t VALUES (1)", "CREATE TABLE u (a INT)"},
		"write, commit, then DDL":     {"INSERT INTO t VALUES (1)", "COMMIT", "CREATE TABLE u (a INT)"},
		"vacuum then DDL":             {"VACUUM", "CREATE TABLE u (a INT)"},
		"executable comment commits":  {"INSERT INTO t VALUES (1)", "/*!50001 CREATE ALGORITHM=UNDEFINED */ /*!50001 VIEW v AS SELECT 1 */"},
		"temporary table keeps":       {"INSERT INTO t VALUES (1)", "CREATE TEMPORARY TABLE u (a INT)"},
		"start transaction then drop": {"START TRANSACTION", "UPDATE t SET a = 1 WHERE b = 2", "DROP TABLE u"},
	}
	names := []string{"write then DDL", "set off, write, then DDL", "set on, write, then DDL", "begin, write, then DDL",
		"write, commit, then DDL", "vacuum then DDL", "executable comment commits", "temporary table keeps", "start transaction then drop"}
	pinValues(t, "autocommit off", map[string]string{
		"postgres write then DDL auto":                          "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres write then DDL autocommit off":                "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres write then DDL manual":                        "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres set off, write, then DDL auto":                "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres set off, write, then DDL autocommit off":      "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres set off, write, then DDL manual":              "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres set on, write, then DDL auto":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres set on, write, then DDL autocommit off":       "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres set on, write, then DDL manual":               "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres begin, write, then DDL auto":                  "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres begin, write, then DDL autocommit off":        "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres begin, write, then DDL manual":                "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres write, commit, then DDL auto":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres write, commit, then DDL autocommit off":       "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres write, commit, then DDL manual":               "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres vacuum then DDL auto":                         "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres vacuum then DDL autocommit off":               "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres vacuum then DDL manual":                       "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres executable comment commits auto":              "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres executable comment commits autocommit off":    "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres executable comment commits manual":            "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres temporary table keeps auto":                   "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres temporary table keeps autocommit off":         "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres temporary table keeps manual":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres start transaction then drop auto":             "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres start transaction then drop autocommit off":   "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"postgres start transaction then drop manual":           "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"mysql write then DDL auto":                             "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"mysql write then DDL autocommit off":                   "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"CREATE commits the writes before it, which autocommit off holds in a transaction: rolling back after it will not undo them\"]",
		"mysql write then DDL manual":                           "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"CREATE commits the writes before it, which manual commit holds in a transaction: Roll Back will not undo them\"]",
		"mysql set off, write, then DDL auto":                   "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"CREATE commits the writes before it, which autocommit off holds in a transaction: rolling back after it will not undo them\"]",
		"mysql set off, write, then DDL autocommit off":         "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"CREATE commits the writes before it, which autocommit off holds in a transaction: rolling back after it will not undo them\"]",
		"mysql set off, write, then DDL manual":                 "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"CREATE commits the writes before it, which manual commit holds in a transaction: Roll Back will not undo them\"]",
		"mysql set on, write, then DDL auto":                    "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"mysql set on, write, then DDL autocommit off":          "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"mysql set on, write, then DDL manual":                  "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"CREATE commits the writes before it, which manual commit holds in a transaction: Roll Back will not undo them\"]",
		"mysql begin, write, then DDL auto":                     "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"CREATE commits the writes since BEGIN: rolling back after it will not undo them\"]",
		"mysql begin, write, then DDL autocommit off":           "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"CREATE commits the writes since BEGIN: rolling back after it will not undo them\"]",
		"mysql begin, write, then DDL manual":                   "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"CREATE commits the writes before it, which manual commit holds in a transaction: Roll Back will not undo them\"]",
		"mysql write, commit, then DDL auto":                    "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"mysql write, commit, then DDL autocommit off":          "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"mysql write, commit, then DDL manual":                  "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"mysql vacuum then DDL auto":                            "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"mysql vacuum then DDL autocommit off":                  "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"CREATE commits the writes before it, which autocommit off holds in a transaction: rolling back after it will not undo them\"]",
		"mysql vacuum then DDL manual":                          "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"CREATE commits the writes before it, which manual commit holds in a transaction: Roll Back will not undo them\"]",
		"mysql executable comment commits auto":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"mysql executable comment commits autocommit off":       "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"CREATE commits the writes before it, which autocommit off holds in a transaction: rolling back after it will not undo them\"]",
		"mysql executable comment commits manual":               "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"CREATE commits the writes before it, which manual commit holds in a transaction: Roll Back will not undo them\"]",
		"mysql temporary table keeps auto":                      "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"mysql temporary table keeps autocommit off":            "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"CREATE takes effect at once: Roll Back will not undo it\"]",
		"mysql temporary table keeps manual":                    "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"CREATE takes effect at once: Roll Back will not undo it\"]",
		"mysql start transaction then drop auto":                "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"DROP commits the writes since START TRANSACTION: rolling back after it will not undo them\"]",
		"mysql start transaction then drop autocommit off":      "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"DROP commits the writes since START TRANSACTION: rolling back after it will not undo them\"]",
		"mysql start transaction then drop manual":              "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"DROP commits the writes before it, which manual commit holds in a transaction: Roll Back will not undo them\"]",
		"clickhouse write then DDL auto":                        "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse write then DDL autocommit off":              "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse write then DDL manual":                      "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse set off, write, then DDL auto":              "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse set off, write, then DDL autocommit off":    "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse set off, write, then DDL manual":            "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse set on, write, then DDL auto":               "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse set on, write, then DDL autocommit off":     "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse set on, write, then DDL manual":             "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse begin, write, then DDL auto":                "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse begin, write, then DDL autocommit off":      "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse begin, write, then DDL manual":              "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse write, commit, then DDL auto":               "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse write, commit, then DDL autocommit off":     "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse write, commit, then DDL manual":             "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse vacuum then DDL auto":                       "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse vacuum then DDL autocommit off":             "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse vacuum then DDL manual":                     "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse executable comment commits auto":            "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse executable comment commits autocommit off":  "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse executable comment commits manual":          "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse temporary table keeps auto":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse temporary table keeps autocommit off":       "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse temporary table keeps manual":               "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse start transaction then drop auto":           "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse start transaction then drop autocommit off": "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"clickhouse start transaction then drop manual":         "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite write then DDL auto":                            "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite write then DDL autocommit off":                  "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite write then DDL manual":                          "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite set off, write, then DDL auto":                  "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite set off, write, then DDL autocommit off":        "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite set off, write, then DDL manual":                "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite set on, write, then DDL auto":                   "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite set on, write, then DDL autocommit off":         "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite set on, write, then DDL manual":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite begin, write, then DDL auto":                    "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite begin, write, then DDL autocommit off":          "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite begin, write, then DDL manual":                  "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite write, commit, then DDL auto":                   "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite write, commit, then DDL autocommit off":         "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite write, commit, then DDL manual":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite vacuum then DDL auto":                           "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite vacuum then DDL autocommit off":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite vacuum then DDL manual":                         "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite executable comment commits auto":                "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite executable comment commits autocommit off":      "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite executable comment commits manual":              "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite temporary table keeps auto":                     "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite temporary table keeps autocommit off":           "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite temporary table keeps manual":                   "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite start transaction then drop auto":               "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite start transaction then drop autocommit off":     "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"sqlite start transaction then drop manual":             "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb write then DDL auto":                            "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb write then DDL autocommit off":                  "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb write then DDL manual":                          "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb set off, write, then DDL auto":                  "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb set off, write, then DDL autocommit off":        "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb set off, write, then DDL manual":                "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb set on, write, then DDL auto":                   "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb set on, write, then DDL autocommit off":         "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb set on, write, then DDL manual":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb begin, write, then DDL auto":                    "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb begin, write, then DDL autocommit off":          "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb begin, write, then DDL manual":                  "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb write, commit, then DDL auto":                   "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb write, commit, then DDL autocommit off":         "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb write, commit, then DDL manual":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb vacuum then DDL auto":                           "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb vacuum then DDL autocommit off":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb vacuum then DDL manual":                         "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb executable comment commits auto":                "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb executable comment commits autocommit off":      "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb executable comment commits manual":              "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb temporary table keeps auto":                     "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb temporary table keeps autocommit off":           "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb temporary table keeps manual":                   "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb start transaction then drop auto":               "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb start transaction then drop autocommit off":     "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"duckdb start transaction then drop manual":             "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis write then DDL auto":                             "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis write then DDL autocommit off":                   "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis write then DDL manual":                           "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis set off, write, then DDL auto":                   "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis set off, write, then DDL autocommit off":         "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis set off, write, then DDL manual":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis set on, write, then DDL auto":                    "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis set on, write, then DDL autocommit off":          "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis set on, write, then DDL manual":                  "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis begin, write, then DDL auto":                     "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis begin, write, then DDL autocommit off":           "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis begin, write, then DDL manual":                   "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis write, commit, then DDL auto":                    "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis write, commit, then DDL autocommit off":          "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis write, commit, then DDL manual":                  "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis vacuum then DDL auto":                            "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis vacuum then DDL autocommit off":                  "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis vacuum then DDL manual":                          "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis executable comment commits auto":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis executable comment commits autocommit off":       "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis executable comment commits manual":               "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis temporary table keeps auto":                      "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis temporary table keeps autocommit off":            "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis temporary table keeps manual":                    "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis start transaction then drop auto":                "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis start transaction then drop autocommit off":      "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"redis start transaction then drop manual":              "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown write then DDL auto":                           "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown write then DDL autocommit off":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown write then DDL manual":                         "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown set off, write, then DDL auto":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown set off, write, then DDL autocommit off":       "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown set off, write, then DDL manual":               "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown set on, write, then DDL auto":                  "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown set on, write, then DDL autocommit off":        "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown set on, write, then DDL manual":                "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown begin, write, then DDL auto":                   "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown begin, write, then DDL autocommit off":         "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown begin, write, then DDL manual":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown write, commit, then DDL auto":                  "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown write, commit, then DDL autocommit off":        "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown write, commit, then DDL manual":                "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown vacuum then DDL auto":                          "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown vacuum then DDL autocommit off":                "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown vacuum then DDL manual":                        "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown executable comment commits auto":               "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown executable comment commits autocommit off":     "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown executable comment commits manual":             "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown temporary table keeps auto":                    "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown temporary table keeps autocommit off":          "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown temporary table keeps manual":                  "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown start transaction then drop auto":              "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown start transaction then drop autocommit off":    "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
		"unknown start transaction then drop manual":            "blocked=\"\" confirm=false typeName=false writes=false reasons=[]",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			cfg := &db.Config{Name: "c", Engine: e}
			for _, n := range names {
				for _, mode := range []struct {
					name                  string
					manual, autocommitOff bool
				}{{"auto", false, false}, {"autocommit off", false, true}, {"manual", true, false}} {
					var v Verdict
					v.EndsRunTransaction(cfg, Analyze(cfg, runs[n]), mode.manual, mode.autocommitOff)
					add(fmt.Sprintf("%s %s %s", e, n, mode.name), verdictText(v))
				}
			}
		}
	})
}

// A statement run in an open transaction that commits it.
func TestPinEndsTransaction(t *testing.T) {
	stmts := []string{"CREATE TABLE u (a INT)", "TRUNCATE t", "COMMIT", "SELECT 1", "SET autocommit = 1"}
	pinValues(t, "ends transaction", map[string]string{
		"postgres \"CREATE TABLE u (a INT)\"":   "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=true",
		"postgres \"TRUNCATE t\"":               "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=true",
		"postgres \"COMMIT\"":                   "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=true controls=true writesInTransaction=false",
		"postgres \"SELECT 1\"":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=false",
		"postgres \"SET autocommit = 1\"":       "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=false",
		"mysql \"CREATE TABLE u (a INT)\"":      "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"CREATE commits the open transaction: Roll Back will not undo what ran before it\"] commitsOrRollsBack=false controls=false writesInTransaction=true",
		"mysql \"TRUNCATE t\"":                  "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"TRUNCATE commits the open transaction: Roll Back will not undo what ran before it\"] commitsOrRollsBack=false controls=false writesInTransaction=true",
		"mysql \"COMMIT\"":                      "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=true controls=true writesInTransaction=false",
		"mysql \"SELECT 1\"":                    "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=false",
		"mysql \"SET autocommit = 1\"":          "blocked=\"\" confirm=true typeName=false writes=false reasons=[\"SET commits the open transaction: Roll Back will not undo what ran before it\"] commitsOrRollsBack=false controls=false writesInTransaction=false",
		"clickhouse \"CREATE TABLE u (a INT)\"": "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=true",
		"clickhouse \"TRUNCATE t\"":             "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=true",
		"clickhouse \"COMMIT\"":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=true controls=true writesInTransaction=false",
		"clickhouse \"SELECT 1\"":               "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=false",
		"clickhouse \"SET autocommit = 1\"":     "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=false",
		"sqlite \"CREATE TABLE u (a INT)\"":     "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=true",
		"sqlite \"TRUNCATE t\"":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=true",
		"sqlite \"COMMIT\"":                     "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=true controls=true writesInTransaction=false",
		"sqlite \"SELECT 1\"":                   "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=false",
		"sqlite \"SET autocommit = 1\"":         "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=false",
		"duckdb \"CREATE TABLE u (a INT)\"":     "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=true",
		"duckdb \"TRUNCATE t\"":                 "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=true",
		"duckdb \"COMMIT\"":                     "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=true controls=true writesInTransaction=false",
		"duckdb \"SELECT 1\"":                   "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=false",
		"duckdb \"SET autocommit = 1\"":         "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=false",
		"redis \"CREATE TABLE u (a INT)\"":      "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=true",
		"redis \"TRUNCATE t\"":                  "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=true",
		"redis \"COMMIT\"":                      "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=true controls=true writesInTransaction=false",
		"redis \"SELECT 1\"":                    "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=false",
		"redis \"SET autocommit = 1\"":          "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=false",
		"unknown \"CREATE TABLE u (a INT)\"":    "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=true",
		"unknown \"TRUNCATE t\"":                "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=true",
		"unknown \"COMMIT\"":                    "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=true controls=true writesInTransaction=false",
		"unknown \"SELECT 1\"":                  "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=false",
		"unknown \"SET autocommit = 1\"":        "blocked=\"\" confirm=false typeName=false writes=false reasons=[] commitsOrRollsBack=false controls=false writesInTransaction=false",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			cfg := &db.Config{Name: "c", Engine: e}
			for _, s := range stmts {
				var v Verdict
				st := Analyze(cfg, []string{s})
				v.EndsTransaction(cfg, st, true)
				add(fmt.Sprintf("%s %q", e, s), fmt.Sprintf("%s commitsOrRollsBack=%v controls=%v writesInTransaction=%v",
					verdictText(v), CommitsOrRollsBack(e, st[0]), ControlsTransaction(e, st[0]), WritesInTransaction(e, st[0])))
			}
		}
	})
}

// The lexer dialect each engine's statements are read in, by what it
// makes of text only some dialects read alike.
func TestPinStatementDialect(t *testing.T) {
	stmts := []string{"SELECT 1 # x\nDELETE FROM t", "SELECT [a] FROM t; DROP TABLE t", "SELECT $$; DROP TABLE t; $$"}
	pinValues(t, "statement dialect", map[string]string{
		"postgres \"SELECT 1 # x\\nDELETE FROM t\"":      "dialect=1 class=0 verb=\"SELECT\" dangerous=false statements=1",
		"postgres \"SELECT [a] FROM t; DROP TABLE t\"":   "dialect=1 class=1 verb=\"SELECT\" dangerous=true statements=2",
		"postgres \"SELECT $$; DROP TABLE t; $$\"":       "dialect=1 class=0 verb=\"SELECT\" dangerous=false statements=1",
		"mysql \"SELECT 1 # x\\nDELETE FROM t\"":         "dialect=2 class=0 verb=\"SELECT\" dangerous=false statements=1",
		"mysql \"SELECT [a] FROM t; DROP TABLE t\"":      "dialect=2 class=1 verb=\"SELECT\" dangerous=true statements=2",
		"mysql \"SELECT $$; DROP TABLE t; $$\"":          "dialect=2 class=1 verb=\"SELECT\" dangerous=true statements=3",
		"clickhouse \"SELECT 1 # x\\nDELETE FROM t\"":    "dialect=3 class=0 verb=\"SELECT\" dangerous=false statements=1",
		"clickhouse \"SELECT [a] FROM t; DROP TABLE t\"": "dialect=3 class=1 verb=\"SELECT\" dangerous=true statements=2",
		"clickhouse \"SELECT $$; DROP TABLE t; $$\"":     "dialect=3 class=0 verb=\"SELECT\" dangerous=false statements=1",
		"sqlite \"SELECT 1 # x\\nDELETE FROM t\"":        "dialect=4 class=0 verb=\"SELECT\" dangerous=false statements=1",
		"sqlite \"SELECT [a] FROM t; DROP TABLE t\"":     "dialect=4 class=1 verb=\"SELECT\" dangerous=true statements=2",
		"sqlite \"SELECT $$; DROP TABLE t; $$\"":         "dialect=4 class=1 verb=\"SELECT\" dangerous=true statements=3",
		"duckdb \"SELECT 1 # x\\nDELETE FROM t\"":        "dialect=1 class=0 verb=\"SELECT\" dangerous=false statements=1",
		"duckdb \"SELECT [a] FROM t; DROP TABLE t\"":     "dialect=1 class=1 verb=\"SELECT\" dangerous=true statements=2",
		"duckdb \"SELECT $$; DROP TABLE t; $$\"":         "dialect=1 class=0 verb=\"SELECT\" dangerous=false statements=1",
		"redis \"SELECT 1 # x\\nDELETE FROM t\"":         "dialect=0 class=0 verb=\"SELECT\" dangerous=false statements=1",
		"redis \"SELECT [a] FROM t; DROP TABLE t\"":      "dialect=0 class=1 verb=\"SELECT\" dangerous=true statements=2",
		"redis \"SELECT $$; DROP TABLE t; $$\"":          "dialect=0 class=1 verb=\"SELECT\" dangerous=true statements=3",
		"unknown \"SELECT 1 # x\\nDELETE FROM t\"":       "dialect=0 class=0 verb=\"SELECT\" dangerous=false statements=1",
		"unknown \"SELECT [a] FROM t; DROP TABLE t\"":    "dialect=0 class=1 verb=\"SELECT\" dangerous=true statements=2",
		"unknown \"SELECT $$; DROP TABLE t; $$\"":        "dialect=0 class=1 verb=\"SELECT\" dangerous=true statements=3",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			cfg := &db.Config{Name: "c", Engine: e}
			for _, s := range stmts {
				st := Analyze(cfg, []string{s})
				add(fmt.Sprintf("%s %q", e, s), fmt.Sprintf("dialect=%v class=%v verb=%q dangerous=%v", Dialect(e), st[0].Analysis.Class,
					st[0].Analysis.Verb, st[0].Analysis.Dangerous)+fmt.Sprintf(" statements=%d", len(sqltext.SplitWith(s, Dialect(e), sqltext.SplitOptions{}))))
			}
		}
	})
}
