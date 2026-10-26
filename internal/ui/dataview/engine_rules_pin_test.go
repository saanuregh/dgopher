package dataview

// Characterization pins: what the data view's engine-dependent rules give
// today, for each engine and for an ID no build knows. Each test reaches
// its rule through one local closure; when a switch moves into a
// per-engine method (LikeFilterSQL, UpsertSQL, JSONPathSQL, Comparable,
// FirstColumn and the like), re-point that closure and leave the pinned
// values alone. A value printed as "unpinned" is one the table lacks.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

var pinEngines = []db.Engine{db.Postgres, db.MySQL, db.ClickHouse, db.SQLite, db.DuckDB, db.Redis, "unknown"}

// sqlDialectEngines are the engines with a Dialect: what quotes names
// cannot be reached for Redis or an unknown engine.
var sqlDialectEngines = []db.Engine{db.Postgres, db.MySQL, db.ClickHouse, db.SQLite, db.DuckDB}

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

// The grid's filters as SQL: a LIKE of each engine's, and its literals.
func TestPinFilterSQL(t *testing.T) {
	filter := func(e db.Engine, col string, c rowCond) string { return condExprSQL(e, col, c) }
	conds := []struct {
		name string
		cond rowCond
	}{
		{"contains", rowCond{op: "contains", vals: []any{`50%_off\`}}},
		{"not contains", rowCond{op: "notcontains", vals: []any{"it's"}}},
		{"prefix", rowCond{op: "prefix", vals: []any{"a"}}},
		{"suffix", rowCond{op: "suffix", vals: []any{"z"}}},
		{"equals", rowCond{op: "=", vals: []any{`a\b`}}},
		{"in", rowCond{op: "in", vals: []any{"x", []byte{1}, nil}}},
		{"between", rowCond{op: "between", vals: []any{int64(1), int64(9)}}},
	}
	pinValues(t, "filter sql", map[string]string{
		"postgres contains":       "CAST(col AS TEXT) ILIKE E'%50\\\\%\\\\_off\\\\\\\\%'",
		"postgres not contains":   "CAST(col AS TEXT) NOT ILIKE '%it''s%'",
		"postgres prefix":         "CAST(col AS TEXT) ILIKE 'a%'",
		"postgres suffix":         "CAST(col AS TEXT) ILIKE '%z'",
		"postgres equals":         "col = E'a\\\\b'",
		"postgres in":             "(col IN ('x', E'\\\\x01'::bytea) OR col IS NULL)",
		"postgres between":        "col BETWEEN 1 AND 9",
		"mysql contains":          "CAST(col AS CHAR) LIKE '%50\\\\%\\\\_off\\\\\\\\%'",
		"mysql not contains":      "CAST(col AS CHAR) NOT LIKE '%it''s%'",
		"mysql prefix":            "CAST(col AS CHAR) LIKE 'a%'",
		"mysql suffix":            "CAST(col AS CHAR) LIKE '%z'",
		"mysql equals":            "col = 'a\\\\b'",
		"mysql in":                "(col IN ('x', X'01') OR col IS NULL)",
		"mysql between":           "col BETWEEN 1 AND 9",
		"clickhouse contains":     "toString(col) ILIKE '%50\\\\%\\\\_off\\\\\\\\%'",
		"clickhouse not contains": "toString(col) NOT ILIKE '%it\\'s%'",
		"clickhouse prefix":       "toString(col) ILIKE 'a%'",
		"clickhouse suffix":       "toString(col) ILIKE '%z'",
		"clickhouse equals":       "col = 'a\\\\b'",
		"clickhouse in":           "(col IN ('x', unhex('01')) OR col IS NULL)",
		"clickhouse between":      "col BETWEEN 1 AND 9",
		"sqlite contains":         "CAST(col AS TEXT) LIKE '%50\\%\\_off\\\\%' ESCAPE '\\'",
		"sqlite not contains":     "CAST(col AS TEXT) NOT LIKE '%it''s%' ESCAPE '\\'",
		"sqlite prefix":           "CAST(col AS TEXT) LIKE 'a%' ESCAPE '\\'",
		"sqlite suffix":           "CAST(col AS TEXT) LIKE '%z' ESCAPE '\\'",
		"sqlite equals":           "col = 'a\\b'",
		"sqlite in":               "(col IN ('x', X'01') OR col IS NULL)",
		"sqlite between":          "col BETWEEN 1 AND 9",
		"duckdb contains":         "CAST(col AS VARCHAR) ILIKE E'%50\\\\%\\\\_off\\\\\\\\%' ESCAPE '\\'",
		"duckdb not contains":     "CAST(col AS VARCHAR) NOT ILIKE '%it''s%' ESCAPE '\\'",
		"duckdb prefix":           "CAST(col AS VARCHAR) ILIKE 'a%' ESCAPE '\\'",
		"duckdb suffix":           "CAST(col AS VARCHAR) ILIKE '%z' ESCAPE '\\'",
		"duckdb equals":           "col = E'a\\\\b'",
		"duckdb in":               "(col IN ('x', '\\x01'::BLOB) OR col IS NULL)",
		"duckdb between":          "col BETWEEN 1 AND 9",
		"redis equals":            "col = 'a\\b'",
		"redis in":                "(col IN ('x', X'01') OR col IS NULL)",
		"redis between":           "col BETWEEN 1 AND 9",
		"unknown equals":          "col = 'a\\b'",
		"unknown in":              "(col IN ('x', X'01') OR col IS NULL)",
		"unknown between":         "col BETWEEN 1 AND 9",
	}, func(add func(key, got string)) {
		likeOps := []string{"contains", "notcontains", "prefix", "suffix"}
		for _, e := range pinEngines {
			_, isSQL := db.SQL(e)
			for _, c := range conds {
				// A LIKE filter is written only with the engine's Dialect,
				// which Redis and an unknown engine do not have.
				if !isSQL && slices.Contains(likeOps, c.cond.op) {
					continue
				}
				add(string(e)+" "+c.name, filter(e, "col", c.cond))
			}
		}
	})
}

// The statements Generate SQL writes, an upsert of each engine's.
func TestPinGeneratedSQL(t *testing.T) {
	cols := []db.ColumnInfo{{Name: "id", Type: "integer"}, {Name: "name", Type: "text"}, {Name: "blob", Type: "bytea"}}
	src := &Source{Cols: cols, Rows: [][]any{{int64(1), "it's", []byte{0xca, 0xfe}}}}
	pinValues(t, "generated sql", map[string]string{
		"postgres upsert":                "INSERT INTO \"s\".\"t\" (\"id\", \"name\", \"blob\")\nVALUES (:id, :name, :blob)\nON CONFLICT (\"id\") DO UPDATE SET \"name\" = EXCLUDED.\"name\", \"blob\" = EXCLUDED.\"blob\";",
		"postgres upsert of keys only":   "INSERT INTO \"s\".\"t\" (\"id\", \"name\", \"blob\")\nVALUES (:id, :name, :blob)\nON CONFLICT (\"id\", \"name\", \"blob\") DO NOTHING;",
		"postgres upsert without key":    "-- No unique key: the database cannot tell an existing row from a new one.\nINSERT INTO \"s\".\"t\" (\"id\", \"name\", \"blob\")\nVALUES (:id, :name, :blob);",
		"postgres rows select":           "SELECT * FROM \"s\".\"t\" WHERE \"id\" IN (1);",
		"postgres rows insert":           "INSERT INTO \"s\".\"t\" (\"id\", \"name\", \"blob\") VALUES (1, 'it''s', E'\\\\xcafe'::bytea);",
		"postgres rows update":           "UPDATE \"s\".\"t\" SET \"name\" = 'it''s', \"blob\" = E'\\\\xcafe'::bytea WHERE \"id\" = 1;",
		"postgres rows delete":           "DELETE FROM \"s\".\"t\" WHERE \"id\" = 1;",
		"mysql upsert":                   "INSERT INTO `s`.`t` (`id`, `name`, `blob`)\nVALUES (:id, :name, :blob)\nON DUPLICATE KEY UPDATE `name` = VALUES(`name`), `blob` = VALUES(`blob`);",
		"mysql upsert of keys only":      "INSERT IGNORE INTO `s`.`t` (`id`, `name`, `blob`)\nVALUES (:id, :name, :blob);",
		"mysql upsert without key":       "-- No unique key: the database cannot tell an existing row from a new one.\nINSERT INTO `s`.`t` (`id`, `name`, `blob`)\nVALUES (:id, :name, :blob);",
		"mysql rows select":              "SELECT * FROM `s`.`t` WHERE `id` IN (1);",
		"mysql rows insert":              "INSERT INTO `s`.`t` (`id`, `name`, `blob`) VALUES (1, 'it''s', X'cafe');",
		"mysql rows update":              "UPDATE `s`.`t` SET `name` = 'it''s', `blob` = X'cafe' WHERE `id` = 1;",
		"mysql rows delete":              "DELETE FROM `s`.`t` WHERE `id` = 1;",
		"clickhouse upsert":              "-- ClickHouse has no upsert: a ReplacingMergeTree keeps the last row of a key.\nINSERT INTO `s`.`t` (`id`, `name`, `blob`)\nVALUES (:id, :name, :blob);",
		"clickhouse upsert of keys only": "-- ClickHouse has no upsert: a ReplacingMergeTree keeps the last row of a key.\nINSERT INTO `s`.`t` (`id`, `name`, `blob`)\nVALUES (:id, :name, :blob);",
		"clickhouse upsert without key":  "-- No unique key: the database cannot tell an existing row from a new one.\nINSERT INTO `s`.`t` (`id`, `name`, `blob`)\nVALUES (:id, :name, :blob);",
		"clickhouse rows select":         "SELECT * FROM `s`.`t` WHERE `id` IN (1);",
		"clickhouse rows insert":         "INSERT INTO `s`.`t` (`id`, `name`, `blob`) VALUES (1, 'it\\'s', unhex('cafe'));",
		"clickhouse rows update":         "UPDATE `s`.`t` SET `name` = 'it\\'s', `blob` = unhex('cafe') WHERE `id` = 1;",
		"clickhouse rows delete":         "DELETE FROM `s`.`t` WHERE `id` = 1;",
		"sqlite upsert":                  "INSERT INTO \"s\".\"t\" (\"id\", \"name\", \"blob\")\nVALUES (:id, :name, :blob)\nON CONFLICT (\"id\") DO UPDATE SET \"name\" = EXCLUDED.\"name\", \"blob\" = EXCLUDED.\"blob\";",
		"sqlite upsert of keys only":     "INSERT INTO \"s\".\"t\" (\"id\", \"name\", \"blob\")\nVALUES (:id, :name, :blob)\nON CONFLICT (\"id\", \"name\", \"blob\") DO NOTHING;",
		"sqlite upsert without key":      "-- No unique key: the database cannot tell an existing row from a new one.\nINSERT INTO \"s\".\"t\" (\"id\", \"name\", \"blob\")\nVALUES (:id, :name, :blob);",
		"sqlite rows select":             "SELECT * FROM \"s\".\"t\" WHERE \"id\" IN (1);",
		"sqlite rows insert":             "INSERT INTO \"s\".\"t\" (\"id\", \"name\", \"blob\") VALUES (1, 'it''s', X'cafe');",
		"sqlite rows update":             "UPDATE \"s\".\"t\" SET \"name\" = 'it''s', \"blob\" = X'cafe' WHERE \"id\" = 1;",
		"sqlite rows delete":             "DELETE FROM \"s\".\"t\" WHERE \"id\" = 1;",
		"duckdb upsert":                  "INSERT INTO \"s\".\"t\" (\"id\", \"name\", \"blob\")\nVALUES (:id, :name, :blob)\nON CONFLICT (\"id\") DO UPDATE SET \"name\" = EXCLUDED.\"name\", \"blob\" = EXCLUDED.\"blob\";",
		"duckdb upsert of keys only":     "INSERT INTO \"s\".\"t\" (\"id\", \"name\", \"blob\")\nVALUES (:id, :name, :blob)\nON CONFLICT (\"id\", \"name\", \"blob\") DO NOTHING;",
		"duckdb upsert without key":      "-- No unique key: the database cannot tell an existing row from a new one.\nINSERT INTO \"s\".\"t\" (\"id\", \"name\", \"blob\")\nVALUES (:id, :name, :blob);",
		"duckdb rows select":             "SELECT * FROM \"s\".\"t\" WHERE \"id\" IN (1);",
		"duckdb rows insert":             "INSERT INTO \"s\".\"t\" (\"id\", \"name\", \"blob\") VALUES (1, 'it''s', '\\xCA\\xFE'::BLOB);",
		"duckdb rows update":             "UPDATE \"s\".\"t\" SET \"name\" = 'it''s', \"blob\" = '\\xCA\\xFE'::BLOB WHERE \"id\" = 1;",
		"duckdb rows delete":             "DELETE FROM \"s\".\"t\" WHERE \"id\" = 1;",
	}, func(add func(key, got string)) {
		for _, e := range sqlDialectEngines {
			keyed := sqlGen{dialect: db.DialectOf(e), engine: e, schema: "s", table: "t", key: []string{"id"}}
			allKey := keyed
			allKey.key = []string{"id", "name", "blob"}
			unkeyed := keyed
			unkeyed.key = nil
			add(string(e)+" upsert", keyed.template("upsert", cols, nil))
			add(string(e)+" upsert of keys only", allKey.template("upsert", cols, nil))
			add(string(e)+" upsert without key", unkeyed.template("upsert", cols, nil))
			for _, kind := range []string{"select", "insert", "update", "delete"} {
				add(string(e)+" rows "+kind, keyed.generate(kind, src, []int{0}))
			}
		}
	})
}

// The expression reading a JSON column at a path.
func TestPinJSONPath(t *testing.T) {
	jsonPath := func(e db.Engine, col string, path []any, leaf bool) string { return JSONPath(e, col, path, leaf) }
	paths := [][]any{{"a"}, {"a", 0, "b"}, {"it's", `back\slash`, "has space"}}
	pinValues(t, "json path", map[string]string{
		"postgres [a] leaf=false":                            "doc->'a'",
		"postgres [a] leaf=true":                             "doc->>'a'",
		"postgres [a 0 b] leaf=false":                        "doc->'a'->0->'b'",
		"postgres [a 0 b] leaf=true":                         "doc->'a'->0->>'b'",
		"postgres [it's back\\slash has space] leaf=false":   "doc->'it''s'->'back\\slash'->'has space'",
		"postgres [it's back\\slash has space] leaf=true":    "doc->'it''s'->'back\\slash'->>'has space'",
		"mysql [a] leaf=false":                               "doc->'$.a'",
		"mysql [a] leaf=true":                                "doc->>'$.a'",
		"mysql [a 0 b] leaf=false":                           "doc->'$.a[0].b'",
		"mysql [a 0 b] leaf=true":                            "doc->>'$.a[0].b'",
		"mysql [it's back\\slash has space] leaf=false":      "doc->'$.\"it''s\".\"back\\\\\\\\slash\".\"has space\"'",
		"mysql [it's back\\slash has space] leaf=true":       "doc->>'$.\"it''s\".\"back\\\\\\\\slash\".\"has space\"'",
		"clickhouse [a] leaf=false":                          "JSONExtractRaw(doc, 'a')",
		"clickhouse [a] leaf=true":                           "JSONExtractString(doc, 'a')",
		"clickhouse [a 0 b] leaf=false":                      "JSONExtractRaw(doc, 'a', 1, 'b')",
		"clickhouse [a 0 b] leaf=true":                       "JSONExtractString(doc, 'a', 1, 'b')",
		"clickhouse [it's back\\slash has space] leaf=false": "JSONExtractRaw(doc, 'it''s', 'back\\slash', 'has space')",
		"clickhouse [it's back\\slash has space] leaf=true":  "JSONExtractString(doc, 'it''s', 'back\\slash', 'has space')",
		"sqlite [a] leaf=false":                              "doc->'$.a'",
		"sqlite [a] leaf=true":                               "doc->>'$.a'",
		"sqlite [a 0 b] leaf=false":                          "doc->'$.a[0].b'",
		"sqlite [a 0 b] leaf=true":                           "doc->>'$.a[0].b'",
		"sqlite [it's back\\slash has space] leaf=false":     "doc->'$.\"it''s\".\"back\\\\slash\".\"has space\"'",
		"sqlite [it's back\\slash has space] leaf=true":      "doc->>'$.\"it''s\".\"back\\\\slash\".\"has space\"'",
		"duckdb [a] leaf=false":                              "doc->'$.a'",
		"duckdb [a] leaf=true":                               "doc->>'$.a'",
		"duckdb [a 0 b] leaf=false":                          "doc->'$.a[0].b'",
		"duckdb [a 0 b] leaf=true":                           "doc->>'$.a[0].b'",
		"duckdb [it's back\\slash has space] leaf=false":     "doc->'$.\"it''s\".\"back\\\\slash\".\"has space\"'",
		"duckdb [it's back\\slash has space] leaf=true":      "doc->>'$.\"it''s\".\"back\\\\slash\".\"has space\"'",
		"redis [a] leaf=false":                               "doc->'$.a'",
		"redis [a] leaf=true":                                "doc->>'$.a'",
		"redis [a 0 b] leaf=false":                           "doc->'$.a[0].b'",
		"redis [a 0 b] leaf=true":                            "doc->>'$.a[0].b'",
		"redis [it's back\\slash has space] leaf=false":      "doc->'$.\"it''s\".\"back\\\\slash\".\"has space\"'",
		"redis [it's back\\slash has space] leaf=true":       "doc->>'$.\"it''s\".\"back\\\\slash\".\"has space\"'",
		"unknown [a] leaf=false":                             "doc->'$.a'",
		"unknown [a] leaf=true":                              "doc->>'$.a'",
		"unknown [a 0 b] leaf=false":                         "doc->'$.a[0].b'",
		"unknown [a 0 b] leaf=true":                          "doc->>'$.a[0].b'",
		"unknown [it's back\\slash has space] leaf=false":    "doc->'$.\"it''s\".\"back\\\\slash\".\"has space\"'",
		"unknown [it's back\\slash has space] leaf=true":     "doc->>'$.\"it''s\".\"back\\\\slash\".\"has space\"'",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			for _, p := range paths {
				for _, leaf := range []bool{false, true} {
					add(fmt.Sprintf("%s %v leaf=%v", e, p, leaf), jsonPath(e, "doc", p, leaf))
				}
			}
		}
	})
}

// Which columns the server compares for a profile, and how it profiles.
func TestPinProfile(t *testing.T) {
	isComparable := func(r db.SQLRules, typ string) bool { return r.Comparable(typ) }
	types := []string{"json", "jsonb", "point", "Map(String, UInt8)", "JSON", "Nested(a Int8)", "Variant(String)", "text", "integer"}
	cols := []db.ColumnInfo{{Name: "n", Type: "integer"}, {Name: "doc", Type: "json"}}
	pinValues(t, "profile", map[string]string{
		"postgres comparable":        "json=false jsonb=true point=false Map(String, UInt8)=true JSON=false Nested(a Int8)=true Variant(String)=true text=true integer=true",
		"postgres server profiles":   "[{name:n typ:integer col:0 count:10 nulls:1 distinct:3 min:1 max:5 avg:2.5 median: bins:[] top:[]} {name:doc typ:json col:0 count:10 nulls:2 distinct:-1 min: max: avg: median: bins:[] top:[]}] <nil>",
		"mysql comparable":           "json=true jsonb=true point=true Map(String, UInt8)=true JSON=true Nested(a Int8)=true Variant(String)=true text=true integer=true",
		"mysql server profiles":      "[{name:n typ:integer col:0 count:10 nulls:1 distinct:3 min:1 max:5 avg:2.5 median: bins:[] top:[]} {name:doc typ:json col:0 count:10 nulls:2 distinct:-1 min: max: avg: median: bins:[] top:[]}] <nil>",
		"clickhouse comparable":      "json=false jsonb=false point=true Map(String, UInt8)=false JSON=false Nested(a Int8)=false Variant(String)=false text=true integer=true",
		"clickhouse server profiles": "[{name:n typ:integer col:0 count:10 nulls:1 distinct:3 min:1 max:5 avg:2.5 median: bins:[] top:[]} {name:doc typ:json col:0 count:10 nulls:2 distinct:-1 min: max: avg: median: bins:[] top:[]}] <nil>",
		"sqlite comparable":          "json=true jsonb=true point=true Map(String, UInt8)=true JSON=true Nested(a Int8)=true Variant(String)=true text=true integer=true",
		"sqlite server profiles":     "[{name:n typ:integer col:0 count:10 nulls:1 distinct:3 min:1 max:5 avg:2.5 median: bins:[] top:[]} {name:doc typ:json col:0 count:10 nulls:2 distinct:-1 min: max: avg: median: bins:[] top:[]}] <nil>",
		"duckdb comparable":          "json=true jsonb=true point=true Map(String, UInt8)=true JSON=true Nested(a Int8)=true Variant(String)=true text=true integer=true",
		"duckdb server profiles":     "[{name:n typ:INTEGER col:0 count:10 nulls:1 distinct:3 min:1 max:5 avg:2.5 median:2 bins:[] top:[]}] <nil>",
		"redis server profiles":      "[{name:n typ:integer col:0 count:10 nulls:1 distinct:3 min:1 max:5 avg:2.5 median: bins:[] top:[]} {name:doc typ:json col:0 count:10 nulls:2 distinct:-1 min: max: avg: median: bins:[] top:[]}] <nil>",
		"unknown server profiles":    "[{name:n typ:integer col:0 count:10 nulls:1 distinct:3 min:1 max:5 avg:2.5 median: bins:[] top:[]} {name:doc typ:json col:0 count:10 nulls:2 distinct:-1 min: max: avg: median: bins:[] top:[]}] <nil>",
		"postgres query":             "SELECT count(*), count(\"n\"), count(DISTINCT \"n\"), min(\"n\"), max(\"n\"), avg(\"n\"), count(\"doc\"), NULL, NULL, NULL, NULL FROM \"t\" WHERE n > 1",
		"mysql query":                "SELECT count(*), count(`n`), count(DISTINCT `n`), min(`n`), max(`n`), avg(`n`), count(`doc`), count(DISTINCT `doc`), min(`doc`), max(`doc`), NULL FROM \"t\" WHERE n > 1",
		"clickhouse query":           "SELECT count(*), count(`n`), count(DISTINCT `n`), min(`n`), max(`n`), avg(`n`), count(`doc`), NULL, NULL, NULL, NULL FROM \"t\" WHERE n > 1",
		"sqlite query":               "SELECT count(*), count(\"n\"), count(DISTINCT \"n\"), min(\"n\"), max(\"n\"), avg(\"n\"), count(\"doc\"), count(DISTINCT \"doc\"), min(\"doc\"), max(\"doc\"), NULL FROM \"t\" WHERE n > 1",
		"duckdb query":               "SUMMARIZE SELECT * FROM \"t\" WHERE n > 1",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			// Redis and an unknown engine have no Dialect, which
			// profileQuery reads its engine from.
			if r, ok := db.SQL(e); ok {
				var lines []string
				for _, typ := range types {
					lines = append(lines, fmt.Sprintf("%s=%v", typ, isComparable(r, typ)))
				}
				add(string(e)+" comparable", strings.Join(lines, " "))
			}
			rows := [][]any{{int64(10), int64(9), int64(3), int64(1), int64(5), "2.5", int64(8), nil, nil, nil, nil}}
			names := []string{"count"}
			if e == db.DuckDB {
				names = []string{"column_name", "column_type", "min", "max", "approx_unique", "avg", "q50", "count", "null_percentage"}
				rows = [][]any{{"n", "INTEGER", "1", "5", int64(3), "2.5", "2", int64(10), "10.0"}}
			}
			ps, err := serverProfiles(e, cols, names, rows, db.Display)
			add(string(e)+" server profiles", fmt.Sprintf("%+v %v", ps, err))
		}
		for _, e := range sqlDialectEngines {
			add(string(e)+" query", profileQuery(db.DialectOf(e), cols, `"t"`, " n > 1 "))
		}
	})
}

// The key column a new table starts with.
func TestPinFirstColumn(t *testing.T) {
	first := func(e db.Engine) db.ColumnDesign { return FirstColumn(e) }
	pinValues(t, "first column", map[string]string{
		"postgres":   "{Was: Name:id Type:bigint Nullable:false Default: PrimaryKey:true AutoIncrement:true Comment: Extra: Generated:false}",
		"mysql":      "{Was: Name:id Type:bigint Nullable:false Default: PrimaryKey:true AutoIncrement:true Comment: Extra: Generated:false}",
		"clickhouse": "{Was: Name:id Type:UInt64 Nullable:false Default: PrimaryKey:true AutoIncrement:false Comment: Extra: Generated:false}",
		"sqlite":     "{Was: Name:id Type:INTEGER Nullable:false Default: PrimaryKey:true AutoIncrement:false Comment: Extra: Generated:false}",
		"duckdb":     "{Was: Name:id Type:BIGINT Nullable:false Default: PrimaryKey:true AutoIncrement:false Comment: Extra: Generated:false}",
		"redis":      "{Was: Name:id Type: Nullable:false Default: PrimaryKey:true AutoIncrement:false Comment: Extra: Generated:false}",
		"unknown":    "{Was: Name:id Type: Nullable:false Default: PrimaryKey:true AutoIncrement:false Comment: Extra: Generated:false}",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			add(string(e), fmt.Sprintf("%+v", first(e)))
		}
	})
}

// What the table form shows, and what it cannot change, by engine.
func TestPinTableForm(t *testing.T) {
	labels := []string{"Columns", "Indexes", "Foreign Keys", "Checks", "Auto", "Comment", "Name", "Type"}
	pinValues(t, "table form", map[string]string{
		"postgres fixed":   "",
		"mysql fixed":      "",
		"clickhouse fixed": "A ClickHouse table's sorting key is fixed when it is made.",
		"sqlite fixed":     "",
		"duckdb fixed":     "DuckDB cannot change the keys or checks of a table it made.",
		"redis fixed":      "",
		"unknown fixed":    "",
		"postgres shows":   "Columns=true Indexes=true Foreign Keys=true Checks=true Auto=true Comment=true Name=true Type=true",
		"mysql shows":      "Columns=true Indexes=true Foreign Keys=true Checks=true Auto=true Comment=true Name=true Type=true",
		"clickhouse shows": "Columns=false Indexes=false Foreign Keys=false Checks=false Auto=false Comment=true Name=true Type=true",
		"sqlite shows":     "Columns=true Indexes=true Foreign Keys=true Checks=true Auto=true Comment=false Name=true Type=true",
		"duckdb shows":     "Columns=true Indexes=true Foreign Keys=true Checks=true Auto=false Comment=true Name=true Type=true",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			made := &designer{engine: e, was: &db.TableDesign{}}
			add(string(e)+" fixed", made.fixed())
		}
		// Redis and an unknown engine have no dialect to write a table in:
		// no table form opens on them.
		for _, e := range sqlDialectEngines {
			a := NewFakeHost(t)
			start := db.TableDesign{Schema: "s", Name: "t", Columns: []db.ColumnDesign{FirstColumn(e)}}
			f := NewModelTableForm(a, e, start, func() []string { return nil }, func(string) []string { return nil }, func(db.TableDesign) error { return nil })
			tt := ui.NewTester(f.View, 1200, 700)
			tt.Frame()
			var shown []string
			for _, l := range labels {
				shown = append(shown, fmt.Sprintf("%s=%v", l, tt.HasText(l)))
			}
			add(string(e)+" shows", strings.Join(shown, " "))
		}
	})
}

// The grid's and the diagram's engine rules: an UPDATE to DEFAULT, the
// order of a table's rows, true and false typed in, foreign keys drawn.
func TestPinViewerRules(t *testing.T) {
	pinValues(t, "viewer rules", map[string]string{
		"postgres":          "noDefaultUpdate=false countStop=\"\" diagram=\"\" true=true false=false tinyint=1",
		"mysql":             "noDefaultUpdate=false countStop=\"\" diagram=\"\" true=1 false=0 tinyint=1",
		"clickhouse":        "noDefaultUpdate=false countStop=\"\" diagram=\"ClickHouse has no foreign keys.\" true=true false=false tinyint=1",
		"sqlite":            "noDefaultUpdate=true countStop=\"\" diagram=\"\" true=1 false=0 tinyint=1",
		"duckdb":            "noDefaultUpdate=false countStop=\"\" diagram=\"\" true=true false=false tinyint=1",
		"redis":             "noDefaultUpdate=false countStop=\"\" diagram=\"\" true=true false=false tinyint=1",
		"unknown":           "noDefaultUpdate=false countStop=\"\" diagram=\"\" true=true false=false tinyint=1",
		"postgres select":   "SELECT * FROM t WHERE v > 1 ORDER BY \"id\"",
		"mysql select":      "SELECT * FROM t WHERE v > 1 ORDER BY `id`",
		"clickhouse select": "SELECT * FROM t WHERE v > 1",
		"sqlite select":     "SELECT * FROM t WHERE v > 1 ORDER BY \"id\"",
		"duckdb select":     "SELECT * FROM t WHERE v > 1 ORDER BY \"id\"",
	}, func(add func(key, got string)) {
		a := NewFakeHost(t)
		for _, e := range pinEngines {
			cn := a.AddConn(db.Config{ID: "c-" + string(e), Name: "c", Engine: e, Host: "h"})
			v := NewViewer(a, ViewerSource{Conn: cn, Statement: "SELECT 1", Reads: true})
			er := &ERTab{conn: cn}
			add(string(e), fmt.Sprintf("noDefaultUpdate=%v countStop=%q diagram=%q true=%s false=%s tinyint=%s",
				v.grid.noDefaultUpdate, v.countStopRefusal(), er.editable(),
				truthText(e, "boolean", true), truthText(e, "boolean", false), truthText(e, "tinyint(1)", true)))
		}
		for _, e := range sqlDialectEngines {
			cn := a.AddConn(db.Config{ID: "t-" + string(e), Name: "c", Engine: e, Host: "h"})
			v := &Viewer{a: a, source: ViewerSource{Conn: cn, Statement: "SELECT * FROM t", Table: &db.Object{Schema: "s", Name: "t"}, Reads: true},
				grid: NewGrid(), columns: []db.Column{{Name: "id", PrimaryKey: true}, {Name: "v"}}}
			v.where = "v > 1"
			add(string(e)+" select", v.selectSQL())
		}
	})
}

// Stopping a count on DuckDB's one connection would abort the
// transaction open there.
func TestPinCountStopOnSharedConnection(t *testing.T) {
	ctx := context.Background()
	pinValues(t, "count stop", map[string]string{
		"duckdb": "idle=\"\" in a transaction=\"Stopping the count would abort the transaction open on duckdb.\"",
		"sqlite": "idle=\"\" in a transaction=\"\"",
	}, func(add func(key, got string)) {
		for _, e := range []db.Engine{db.DuckDB, db.SQLite} {
			a := NewFakeHost(t)
			cn := a.AddConn(db.Config{ID: string(e), Name: string(e), Engine: e, Database: ":memory:"})
			tt := ui.NewTester(a.View, 800, 600)
			a.Connect(cn, nil)
			testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
			s, err := cn.DB.Session(ctx)
			if err != nil {
				t.Fatal(err)
			}
			v := &Viewer{a: a, source: ViewerSource{Conn: cn}}
			before := v.countStopRefusal()
			if _, err := s.Exec(ctx, "BEGIN"); err != nil {
				t.Fatal(err)
			}
			add(string(e), fmt.Sprintf("idle=%q in a transaction=%q", before, v.countStopRefusal()))
			s.Exec(ctx, "ROLLBACK")
			s.Close()
		}
	})
}
