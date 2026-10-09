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
		{"delete from t where id between 1 and 1 = 1", false},
	} {
		a := Classify(c.sql, Postgres)
		if a.Dangerous != c.dangerous {
			t.Errorf("%s: dangerous %v (%s)", c.sql, a.Dangerous, a.Reason)
		}
	}
}
