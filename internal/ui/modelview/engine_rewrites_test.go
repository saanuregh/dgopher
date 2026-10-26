package modelview

import (
	"testing"

	"dgopher/internal/db"
)

// compare.go asks for a database to compare with only where a database
// needs a connection of its own, as it did on PostgreSQL only.
func TestRewrittenDatabaseField(t *testing.T) {
	for _, e := range pinEngines {
		if got, old := e.DatabasesNeedOwnConnection(), e == db.Postgres; got != old {
			t.Errorf("%s: database field %v, was %v", e, got, old)
		}
	}
}
