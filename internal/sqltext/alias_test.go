package sqltext

import (
	"strings"
	"testing"
)

func TestTableAlias(t *testing.T) {
	for _, c := range []struct {
		name  string
		taken []string
		want  string
	}{
		{"reflex_order", nil, "ro"},
		{"ReflexOrder", nil, "ro"},
		{"HTTPLog", nil, "hl"},
		{"users", nil, "u"},
		{"Order Items", nil, "oi"},
		{"order_item", []string{"oi"}, "oi2"},
		{"order_item", []string{"OI", "oi2"}, "oi3"},
		{"order_number", nil, "on2"}, // ON is a keyword
		{"_1", nil, "t"},
	} {
		if got := TableAlias(c.name, c.taken, Postgres); got != c.want {
			t.Errorf("TableAlias(%q, %q) = %q, want %q", c.name, c.taken, got, c.want)
		}
	}
}

// A table typed in a query's FROM or JOIN may take an alias; one that
// DELETE, INSERT, UPDATE or SHOW names does not.
func TestCompletionInQueryFrom(t *testing.T) {
	for _, c := range []struct {
		sql  string
		want bool
	}{
		{"SELECT * FROM |", true},
		{"SELECT * FROM a JOIN |", true},
		{"SELECT * FROM a, |", true},
		{"SELECT * FROM algo.|", true},
		{"INSERT INTO t SELECT * FROM |", true},
		{"DELETE FROM t WHERE id IN (SELECT id FROM |", true},
		{"SELECT * FROM a WHERE b IN (SELECT 1) UNION SELECT * FROM |", true},
		{"DELETE FROM |", false},
		{"WITH x AS (SELECT 1) DELETE FROM |", false},
		{"INSERT INTO |", false},
		{"UPDATE |", false},
		{"UPDATE t SET a = (SELECT 1) FROM |", false},
		{"SHOW COLUMNS FROM |", false},
		{"SELECT * FROM a WHERE |", false},
	} {
		caret := strings.Index(c.sql, "|")
		cc := CompletionAt(strings.Replace(c.sql, "|", "", 1), caret, Postgres)
		if cc.InQueryFrom != c.want {
			t.Errorf("%q: InQueryFrom %v, want %v (WantTable %v)", c.sql, cc.InQueryFrom, c.want, cc.WantTable)
		}
	}
}
