package sqltext

import (
	"strings"
	"testing"
)

// A statement's rows can be edited when it reads one table, column by
// column; anything else says why not.
func TestSingleTable(t *testing.T) {
	for _, c := range []struct {
		sql, schema, name string
	}{
		{"SELECT * FROM t", "", "t"},
		{"select a, b from s.t where x > 1 order by a limit 5;", "s", "t"},
		{`SELECT "id", "Name" FROM "Shop"."Orders" o WHERE o.total > 10`, "Shop", "Orders"},
		{"SELECT id, lower(name) FROM t", "", "t"},
		{"-- top\nSELECT id FROM t /* x */", "", "t"},
		{"SELECT * FROM t WHERE total > (SELECT max(total) / 2 FROM t)", "", "t"},
	} {
		ref, why := SingleTable(c.sql, Postgres)
		if why != "" || ref.Schema != c.schema || ref.Name != c.name {
			t.Errorf("%q: %+v, %q", c.sql, ref, why)
		}
	}
	for _, c := range []struct{ sql, why string }{
		{"SELECT * FROM a JOIN b ON a.id = b.a_id", "join"},
		{"SELECT * FROM a, b", "join"},
		{"SELECT country, count(*) FROM t GROUP BY country", "group"},
		{"SELECT count(*) FROM t", "aggregate"},
		{"SELECT DISTINCT country FROM t", "distinct"},
		{"SELECT id FROM a UNION SELECT id FROM b", "union"},
		{"SELECT id, row_number() OVER (ORDER BY id) FROM t", "window"},
		{"SELECT * FROM (SELECT * FROM t) x", "subquery"},
		{"WITH x AS (SELECT 1) SELECT * FROM x", "with"},
		{"SELECT 1", "no table"},
		{"INSERT INTO t (a) VALUES (1) RETURNING id", "not a select"},
		{"SELECT 1 FROM t; SELECT 2 FROM t", "one statement"},
		{"SELECT * FROM a STRAIGHT_JOIN b ON a.id = b.a_id", "join"},
		{"SELECT * FROM a CROSS APPLY f(a.id)", "join"},
		{"SELECT * FROM a OUTER APPLY f(a.id) x", "join"},
		{"SELECT * FROM generate_series(1, 10)", "table function"},
		{"SELECT * FROM s.fn(1) AS f", "table function"},
	} {
		if _, why := SingleTable(c.sql, Postgres); !strings.Contains(strings.ToLower(why), c.why) {
			t.Errorf("%q: reason %q, want one with %q", c.sql, why, c.why)
		}
	}
}

// A table's name keeps its case when quoted: the database folds the rest.
func TestSingleTableQuoted(t *testing.T) {
	ref, _ := SingleTable(`SELECT * FROM "Shop".orders`, Postgres)
	if !ref.SchemaQuoted || ref.Quoted {
		t.Errorf("%+v", ref)
	}
	ref, _ = SingleTable(`SELECT * FROM shop."Orders"`, Postgres)
	if ref.SchemaQuoted || !ref.Quoted || ref.Name != "Orders" {
		t.Errorf("%+v", ref)
	}
}

// A SELECT's list tells which items read a column as it is, and which
// compute, rename or expand.
func TestSelectItems(t *testing.T) {
	for _, c := range []struct {
		sql  string
		want []SelectItem
	}{
		{"SELECT * FROM t", []SelectItem{{Star: true}}},
		{"SELECT t.*, id FROM t", []SelectItem{{Star: true}, {Name: "id", Column: "id"}}},
		{`SELECT o.id, s.o."Label", label AS l, id + 1 AS id, upper(label), (SELECT 1) x, label lbl, id AS ID FROM s.o`, []SelectItem{
			{Name: "id", Column: "id"}, {Name: "Label", Column: "Label"}, {Name: "l", Column: "label"}, {Name: "id"},
			{}, {Name: "x"}, {Name: "lbl", Column: "label"}, {Name: "ID", Column: "id"}}},
		{"SELECT id::text, f(a, b) AS c FROM t WHERE x IN (1, 2)", []SelectItem{{}, {Name: "c"}}},
		{"SELECT ALL id FROM t;", []SelectItem{{Name: "id", Column: "id"}}},
	} {
		got, ok := SelectItems(c.sql, Postgres)
		if !ok || len(got) != len(c.want) {
			t.Errorf("%q: %+v, %v", c.sql, got, ok)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q item %d: %+v, want %+v", c.sql, i, got[i], c.want[i])
			}
		}
	}
	if _, ok := SelectItems("INSERT INTO t VALUES (1)", Postgres); ok {
		t.Error("an INSERT has a select list")
	}
}

func TestChangedTable(t *testing.T) {
	for _, c := range []struct {
		sql, table string
		ok         bool
	}{
		{"UPDATE shop.t SET a = 1 WHERE id = 2", "shop.t", true},
		{"update `t` set a = 1", "t", true},
		{"DELETE FROM t WHERE id = 1", "t", true},
		{"UPDATE LOW_PRIORITY t SET a = 1", "t", true},
		{"UPDATE a JOIN b ON a.id = b.id SET a.x = 1", "", false},
		{"DELETE a FROM a JOIN b ON a.id = b.id", "", false},
		{"DELETE FROM a USING a, b WHERE a.id = b.id", "", false},
		{"SELECT 1", "", false},
	} {
		ref, ok := ChangedTable(c.sql, MySQL)
		name := ref.Name
		if ref.Schema != "" {
			name = ref.Schema + "." + name
		}
		if ok != c.ok || ok && name != c.table {
			t.Errorf("%s: %q %v", c.sql, name, ok)
		}
	}
}

func TestUpserts(t *testing.T) {
	for sql, want := range map[string]bool{
		"INSERT INTO t VALUES (1) ON CONFLICT (id) DO UPDATE SET a = 1":  true,
		"insert into t values (1) on conflict do nothing":                false,
		"INSERT INTO t VALUES (1) ON DUPLICATE KEY UPDATE a = 1":         true,
		"INSERT OR REPLACE INTO t VALUES (1)":                            true,
		"INSERT INTO t SELECT * FROM u JOIN v ON u.id = v.id":            false,
		"INSERT INTO t VALUES (1)":                                       false,
		"INSERT INTO t SELECT * FROM a JOIN b ON duplicate = 1":          false,
		"UPDATE t SET a = 1 WHERE b IN (SELECT 1 ON CONFLICT DO UPDATE)": false,
	} {
		if got := Upserts(sql, Postgres); got != want {
			t.Errorf("%s: %v", sql, got)
		}
	}
}

func TestChangedTableInserts(t *testing.T) {
	for sql, want := range map[string]string{
		"INSERT INTO shop.t (a) VALUES (1) ON DUPLICATE KEY UPDATE a = 1": "t",
		"REPLACE LOW_PRIORITY INTO t VALUES (1)":                          "t",
		"INSERT IGNORE t VALUES (1)":                                      "t",
	} {
		if ref, ok := ChangedTable(sql, MySQL); !ok || ref.Name != want {
			t.Errorf("%s: %+v %v", sql, ref, ok)
		}
	}
}
