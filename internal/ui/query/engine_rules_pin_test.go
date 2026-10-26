package query

// Characterization pins: what the SQL editor's engine-dependent rules give
// today, for each engine and for an ID no build knows. Each test reaches
// its rule through one local closure; when a switch moves into a
// per-engine method (Explain, UseSchemaSQL, NameFolding, ChangedRowsWord),
// re-point that closure and leave the pinned values alone. A value printed
// as "unpinned" is one the table lacks.

import (
	"fmt"
	"strings"
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

// What Explain and Explain Analyze send, and why Explain Analyze is
// refused, by engine.
func TestPinExplainRun(t *testing.T) {
	pinValues(t, "explain run", map[string]string{
		"postgres SELECT 1":        "explain=[\"EXPLAIN (VERBOSE, FORMAT JSON) SELECT 1\"] analyze=[\"EXPLAIN (ANALYZE, BUFFERS, VERBOSE, FORMAT JSON) SELECT 1\"] refusal=\"\"",
		"postgres DELETE FROM t":   "explain=[\"EXPLAIN (VERBOSE, FORMAT JSON) DELETE FROM t\"] analyze=[\"EXPLAIN (ANALYZE, BUFFERS, VERBOSE, FORMAT JSON) DELETE FROM t\"] refusal=\"Explain Analyze runs the statement: only one that reads is explained so.\"",
		"mysql SELECT 1":           "explain=[\"EXPLAIN FORMAT=TREE SELECT 1\"] analyze=[\"EXPLAIN ANALYZE SELECT 1\"] refusal=\"\"",
		"mysql DELETE FROM t":      "explain=[\"EXPLAIN FORMAT=TREE DELETE FROM t\"] analyze=[\"EXPLAIN ANALYZE DELETE FROM t\"] refusal=\"Explain Analyze runs the statement: only one that reads is explained so.\"",
		"clickhouse SELECT 1":      "explain=[\"EXPLAIN json = 1, description = 1, indexes = 1 SELECT 1\"] analyze=[\"SELECT 1\"] refusal=\"ClickHouse explains a statement without running it: Explain shows its plan.\"",
		"clickhouse DELETE FROM t": "explain=[\"EXPLAIN json = 1, description = 1, indexes = 1 DELETE FROM t\"] analyze=[\"DELETE FROM t\"] refusal=\"ClickHouse explains a statement without running it: Explain shows its plan.\"",
		"sqlite SELECT 1":          "explain=[\"EXPLAIN QUERY PLAN SELECT 1\"] analyze=[\"SELECT 1\"] refusal=\"SQLite explains a statement without running it: Explain shows its plan.\"",
		"sqlite DELETE FROM t":     "explain=[\"EXPLAIN QUERY PLAN DELETE FROM t\"] analyze=[\"DELETE FROM t\"] refusal=\"SQLite explains a statement without running it: Explain shows its plan.\"",
		"duckdb SELECT 1":          "explain=[\"EXPLAIN (FORMAT JSON) SELECT 1\"] analyze=[\"EXPLAIN (ANALYZE, FORMAT JSON) SELECT 1\"] refusal=\"\"",
		"duckdb DELETE FROM t":     "explain=[\"EXPLAIN (FORMAT JSON) DELETE FROM t\"] analyze=[\"EXPLAIN (ANALYZE, FORMAT JSON) DELETE FROM t\"] refusal=\"Explain Analyze runs the statement: only one that reads is explained so.\"",
		"redis SELECT 1":           "explain=[\"EXPLAIN SELECT 1\"] analyze=[\"SELECT 1\"] refusal=\"Redis explains a statement without running it: Explain shows its plan.\"",
		"redis DELETE FROM t":      "explain=[\"EXPLAIN DELETE FROM t\"] analyze=[\"DELETE FROM t\"] refusal=\"Redis explains a statement without running it: Explain shows its plan.\"",
		"unknown SELECT 1":         "explain=[\"EXPLAIN SELECT 1\"] analyze=[\"SELECT 1\"] refusal=\"unknown explains a statement without running it: Explain shows its plan.\"",
		"unknown DELETE FROM t":    "explain=[\"EXPLAIN DELETE FROM t\"] analyze=[\"DELETE FROM t\"] refusal=\"unknown explains a statement without running it: Explain shows its plan.\"",
	}, func(add func(key, got string)) {
		h := newFakeQueryHost(t)
		for _, e := range pinEngines {
			for _, text := range []string{"SELECT 1", "DELETE FROM t"} {
				cn := h.AddConn(db.Config{ID: "x-" + string(e) + text[:3], Name: "x", Engine: e, Host: "h"})
				q := New(h, cn, "", "query", text)
				explain, _ := q.statements(RunExplain)
				analyze, _ := q.statements(RunExplainAnalyze)
				add(fmt.Sprintf("%s %s", e, text), fmt.Sprintf("explain=%q analyze=%q refusal=%q", explain, analyze, q.analyzeRefusal()))
			}
		}
	})
}

// The statement switching a session's schema, the case names are found
// in, and the word for the rows a change made.
func TestPinEditorRules(t *testing.T) {
	useSchema := func(e db.Engine, schema string) string { return db.UseSchemaSQL(e, schema) }
	match := func(names []string, name string, quoted bool, e db.Engine) int {
		return matchName(names, name, quoted, e)
	}
	names := []string{"Orders", "orders", "Lines"}
	finds := []struct {
		name   string
		quoted bool
	}{{"orders", false}, {"ORDERS", false}, {"Orders", true}, {"ORDERS", true}, {"lines", false}, {"LINES", true}, {"none", false}}
	pinValues(t, "editor rules", map[string]string{
		"postgres use schema":     "SET search_path TO \"Sales\"",
		"postgres match name":     "orders quoted=false: 1; ORDERS quoted=false: 1; Orders quoted=true: 0; ORDERS quoted=true: -1; lines quoted=false: -1; LINES quoted=true: -1; none quoted=false: -1",
		"postgres changed word":   "changed",
		"mysql use schema":        "USE `Sales`",
		"mysql match name":        "orders quoted=false: 1; ORDERS quoted=false: -1; Orders quoted=true: 0; ORDERS quoted=true: -1; lines quoted=false: 2; LINES quoted=true: 2; none quoted=false: -1",
		"mysql changed word":      "matched",
		"clickhouse use schema":   "USE `Sales`",
		"clickhouse match name":   "orders quoted=false: 1; ORDERS quoted=false: -1; Orders quoted=true: 0; ORDERS quoted=true: -1; lines quoted=false: -1; LINES quoted=true: -1; none quoted=false: -1",
		"clickhouse changed word": "changed",
		"sqlite use schema":       "",
		"sqlite match name":       "orders quoted=false: 1; ORDERS quoted=false: -1; Orders quoted=true: 0; ORDERS quoted=true: -1; lines quoted=false: 2; LINES quoted=true: 2; none quoted=false: -1",
		"sqlite changed word":     "changed",
		"duckdb use schema":       "USE \"Sales\"",
		"duckdb match name":       "orders quoted=false: 1; ORDERS quoted=false: -1; Orders quoted=true: 0; ORDERS quoted=true: -1; lines quoted=false: 2; LINES quoted=true: 2; none quoted=false: -1",
		"duckdb changed word":     "changed",
		"redis use schema":        "",
		"redis match name":        "orders quoted=false: 1; ORDERS quoted=false: -1; Orders quoted=true: 0; ORDERS quoted=true: -1; lines quoted=false: 2; LINES quoted=true: 2; none quoted=false: -1",
		"redis changed word":      "changed",
		"unknown use schema":      "",
		"unknown match name":      "orders quoted=false: 1; ORDERS quoted=false: -1; Orders quoted=true: 0; ORDERS quoted=true: -1; lines quoted=false: 2; LINES quoted=true: 2; none quoted=false: -1",
		"unknown changed word":    "changed",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			add(string(e)+" use schema", useSchema(e, "Sales"))
			var found []string
			for _, f := range finds {
				found = append(found, fmt.Sprintf("%s quoted=%v: %d", f.name, f.quoted, match(names, f.name, f.quoted, e)))
			}
			add(string(e)+" match name", strings.Join(found, "; "))
			add(string(e)+" changed word", changedWord(e))
		}
	})
}
