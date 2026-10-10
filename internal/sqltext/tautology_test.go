package sqltext

import "testing"

// A WHERE always true is as none: the statement changes every row.
func TestAlwaysTrueWhere(t *testing.T) {
	for _, c := range []struct {
		sql       string
		dangerous bool
	}{
		{"delete from t where true", true},
		{"DELETE FROM t WHERE 1=1;", true},
		{"update t set a = 1 where (1 = 1)", true},
		{"update t set a = 1 where id = id", true},
		{"update t set a = 1 where t.id = t.id returning *", true},
		{"delete from t where 1", true},
		{"delete from t where not false", true},
		{"delete from t where 'a' <> 'b'", true},
		{"delete from t where 2 > 1", true},
		{"delete from t where id = 5 or 1 = 1", true},
		{"delete from t where id = 5", false},
		{"delete from t where id = 5 and 1 = 1", false},
		{"delete from t where a = b", false},
		{"delete from t where 1 = 2", false},
		{"delete from t where id in (select id from u where 1 = 1)", false},
		{"update t set a = 1 where (id = 1) or (id = 2)", false},
		{"delete from t where 1 = 1 and 2 = 2", true},
		{"delete from t where (1 = 1 and true) or id = 4", true},
		{"delete from t where id = 4 and (1 = 1 or id = 5)", false},
		{"delete from t where 1 between 0 and 2", false},
		{"delete from t where 1 = 1 && 2 = 2", true},
		{"delete from t where id = 4 && 1 = 1", false},
		{"delete from t where id between 1 and 1 = 1", false},
		{"delete from t where TRUE::bool", true},
		{"delete from t where 1::int", true},
		{"delete from t where 1::int = 1::int", true},
		{"delete from t where CAST(1 AS bool)", true},
		{"delete from t where 't'::boolean", true},
		{"delete from t where NOT FALSE::bool", true},
		{"delete from t where 'Yes'::bool", true},
		{"delete from t where not 'off'::boolean", true},
		{"delete from t where cast(id as int) = cast(id as int)", true},
		{"delete from t where 1::numeric(10,2) = 1", true},
		{"delete from t where TRUE::pg_catalog.bool", true},
		{"delete from t where 'f'::bool", false},
		{"delete from t where 't'::text", false},
		{"delete from t where id::int = 5", false},
		{"delete from t where 1::int = 2", false},
		{"delete from t where d::date = '2024-01-01'::date", false},
	} {
		a := Classify(c.sql, Postgres)
		if a.Dangerous != c.dangerous {
			t.Errorf("%s: dangerous %v (%s)", c.sql, a.Dangerous, a.Reason)
		}
	}
}
