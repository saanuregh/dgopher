package dataview

import (
	"testing"

	"dgopher/internal/db"
)

// table.go lists a table's foreign keys, even none, where the engine has
// them, as it did on every engine but ClickHouse.
func TestRewrittenForeignKeysSection(t *testing.T) {
	for _, e := range pinEngines {
		if got, old := db.HasForeignKeys(e), e != db.ClickHouse; got != old {
			t.Errorf("%s: foreign keys section %v, was %v", e, got, old)
		}
	}
}
