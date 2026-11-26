package sqltext

import (
	"slices"
	"strings"
	"testing"
)

// An UPDATE or DELETE of one table is rewritten into a SELECT of the rows
// it would change and a COUNT of them, its own text kept; any other form
// says why it cannot be.
func TestPreviewWrite(t *testing.T) {
	newName := func(col string) string { return `"` + col + ` → new"` }
	for _, c := range []struct {
		d           Dialect
		sql         string
		verb        string
		changed     []string
		rows, count string
	}{
		{Postgres, "UPDATE t SET a = 1, b = lower(b) WHERE id = 2",
			"UPDATE", []string{"a", "b"},
			`SELECT 1 AS "a → new", lower(b) AS "b → new", t.* FROM t WHERE id = 2`,
			`SELECT COUNT(*) FROM t WHERE id = 2`},
		{Postgres, `update "Shop"."Orders" AS o set "Total" = o."Total" /* x */ * 2 where o.id in (select id from x);`,
			"UPDATE", []string{"Total"},
			`SELECT o."Total" /* x */ * 2 AS "Total → new", o.* FROM "Shop"."Orders" AS o WHERE o.id in (select id from x)`,
			`SELECT COUNT(*) FROM "Shop"."Orders" AS o WHERE o.id in (select id from x)`},
		{Postgres, "UPDATE ONLY s.t SET a = DEFAULT RETURNING *",
			"UPDATE", []string{"a"},
			`SELECT '(DEFAULT)' AS "a → new", s.t.* FROM ONLY s.t`,
			`SELECT COUNT(*) FROM ONLY s.t`},
		{Postgres, "UPDATE t SET a = $1 WHERE id = $2",
			"UPDATE", []string{"a"},
			`SELECT $1 AS "a → new", t.* FROM t WHERE id = $2`,
			`SELECT COUNT(*) FROM (SELECT $1 AS "a → new", t.* FROM t WHERE id = $2) x`},
		{Postgres, "UPDATE t SET a = ? WHERE id = ?",
			"UPDATE", []string{"a"},
			`SELECT ? AS "a → new", t.* FROM t WHERE id = ?`,
			`SELECT COUNT(*) FROM (SELECT ? AS "a → new", t.* FROM t WHERE id = ?) x`},
		{MySQL, "UPDATE t SET a = 1, b = c + 1 WHERE id = 2",
			"UPDATE", []string{"a", "b"},
			`SELECT 1 AS "a → new", c + 1 AS "b → new", t.* FROM t WHERE id = 2`,
			`SELECT COUNT(*) FROM t WHERE id = 2`},
		{Postgres, "DELETE FROM t WHERE a = 1 RETURNING id",
			"DELETE", nil, `SELECT t.* FROM t WHERE a = 1`, `SELECT COUNT(*) FROM t WHERE a = 1`},
		{MySQL, "UPDATE LOW_PRIORITY IGNORE `t` SET `a` = 1 WHERE b = 2 ORDER BY id LIMIT 10",
			"UPDATE", []string{"a"},
			"SELECT 1 AS \"a → new\", `t`.* FROM `t` WHERE b = 2 ORDER BY id LIMIT 10",
			"SELECT COUNT(*) FROM (SELECT 1 AS n FROM `t` WHERE b = 2 ORDER BY id LIMIT 10) x"},
		{MySQL, "DELETE LOW_PRIORITY QUICK IGNORE FROM t WHERE b = 2 LIMIT 5",
			"DELETE", nil, "SELECT t.* FROM t WHERE b = 2 LIMIT 5",
			"SELECT COUNT(*) FROM (SELECT 1 AS n FROM t WHERE b = 2 LIMIT 5) x"},
		{SQLite, "DELETE FROM t ORDER BY id LIMIT 3",
			"DELETE", nil, "SELECT t.* FROM t ORDER BY id LIMIT 3",
			"SELECT COUNT(*) FROM (SELECT 1 AS n FROM t ORDER BY id LIMIT 3) x"},
		{TSQL, "UPDATE TOP (5) [dbo].[t] SET [a] = 1 WHERE b = 2",
			"UPDATE", []string{"a"},
			`SELECT TOP (5) 1 AS "a → new", [dbo].[t].* FROM [dbo].[t] WHERE b = 2`,
			`SELECT COUNT(*) FROM (SELECT TOP (5) 1 AS n FROM [dbo].[t] WHERE b = 2) x`},
		{TSQL, "DELETE TOP (5) FROM t WHERE b = 2",
			"DELETE", nil, `SELECT TOP (5) t.* FROM t WHERE b = 2`,
			`SELECT COUNT(*) FROM (SELECT TOP (5) 1 AS n FROM t WHERE b = 2) x`},
		{TSQL, "DELETE t WHERE b = 2",
			"DELETE", nil, `SELECT t.* FROM t WHERE b = 2`, `SELECT COUNT(*) FROM t WHERE b = 2`},
		{Oracle, "UPDATE hr.emp e SET e.sal = e.sal * 1.1 WHERE e.dept = 10",
			"UPDATE", []string{"sal"},
			`SELECT e.sal * 1.1 AS "sal → new", e.* FROM hr.emp e WHERE e.dept = 10`,
			`SELECT COUNT(*) FROM hr.emp e WHERE e.dept = 10`},
		{Oracle, "DELETE emp WHERE dept = 10",
			"DELETE", nil, `SELECT emp.* FROM emp WHERE dept = 10`, `SELECT COUNT(*) FROM emp WHERE dept = 10`},
		{ClickHouse, "DELETE FROM db.t WHERE a = 1",
			"DELETE", nil, `SELECT db.t.* FROM db.t WHERE a = 1`, `SELECT COUNT(*) FROM db.t WHERE a = 1`},
	} {
		p, why := PreviewWrite(c.sql, c.d, newName)
		if why != "" || p.Verb != c.verb || !slices.Equal(p.Changed, c.changed) || p.Rows != c.rows || p.Count != c.count {
			t.Errorf("%q:\n got %+v, %q\nwant rows %s\n     count %s", c.sql, p, why, c.rows, c.count)
		}
	}
	for _, c := range []struct {
		d        Dialect
		sql, why string
	}{
		{Postgres, "SELECT 1", "shows the rows an update or delete"},
		{Postgres, "INSERT INTO t VALUES (1)", "shows the rows an update or delete"},
		{Postgres, "WITH x AS (SELECT 1) UPDATE t SET a = 1", "this update starts with a with"},
		{MySQL, "UPDATE t SET a = a + 1, b = a WHERE id = 2", "sets b from a, which it sets before"},
		{Postgres, "UPDATE t SET a = c.a FROM customers c WHERE c.id = t.cid", "also reads customers (from)"},
		{Postgres, "DELETE FROM t USING u WHERE u.id = t.id", "uses using"},
		{MySQL, "UPDATE t JOIN u ON u.id = t.id SET t.a = 1", "joins another table"},
		{MySQL, "UPDATE t STRAIGHT_JOIN u SET t.a = 1", "joins another table"},
		{MySQL, "UPDATE t, u SET t.a = u.a WHERE t.id = u.id", "names more than one table"},
		{MySQL, "DELETE a FROM a JOIN b ON a.id = b.id", "joins another table"},
		{MySQL, "DELETE a, b FROM a, b WHERE a.id = b.id", "names more than one table"},
		{MySQL, "DELETE a FROM a WHERE a.id = 1", "names more than one table"},
		{TSQL, "DELETE FROM t OUTPUT deleted.* WHERE a = 1", "writes its output"},
		{TSQL, "UPDATE t SET a = 1 FROM t CROSS APPLY f(t.id)", "joins another table"},
		{TSQL, "UPDATE TOP (10) PERCENT t SET a = 1", "top … percent"},
		{TSQL, "DELETE TOP (10) PERCENT FROM t", "top … percent"},
		{Postgres, "UPDATE t SET a = 1 WHERE CURRENT OF c", "where current of"},
		{ClickHouse, "DELETE FROM t ON CLUSTER main WHERE a = 1", "on cluster"},
		{ClickHouse, "ALTER TABLE t UPDATE a = 1 WHERE b = 2", "alter table"},
		{ClickHouse, "ALTER TABLE t DELETE WHERE b = 2", "this delete"},
		{Postgres, "UPDATE t SET a = 1; DELETE FROM u", "more than one statement"},
	} {
		_, why := PreviewWrite(c.sql, c.d, newName)
		if !strings.Contains(strings.ToLower(why), c.why) {
			t.Errorf("%q: %q, want %q", c.sql, why, c.why)
		}
		if why != "" && !strings.HasPrefix(why, "Preview Changes ") {
			t.Errorf("%q: %q", c.sql, why)
		}
	}
}
