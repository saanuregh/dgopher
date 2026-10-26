package query

import (
	"testing"

	"dgopher/internal/db"
)

// The editor offers to switch schemas where the engine's sessions have a
// schema to switch, as it did through the old wrapper's "" for none.
func TestRewrittenSchemaSwitch(t *testing.T) {
	want := map[db.Engine]bool{db.Postgres: true, db.MySQL: true, db.ClickHouse: true, db.SQLite: false, db.DuckDB: true, db.Redis: false, "unknown": false}
	for _, e := range pinEngines {
		if got := switchesSchema(e); got != want[e] {
			t.Errorf("%s: switches schema %v, want %v", e, got, want[e])
		}
	}
}
