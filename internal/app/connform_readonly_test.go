package app

import (
	"strings"
	"testing"

	"dgopher/internal/db"
)

// The read-only checkbox says the server enforces it too only where it
// does: on TiDB and Redis the app's check is the only one.
func TestReadOnlyLabelSaysWhoEnforces(t *testing.T) {
	for _, e := range db.Engines() {
		label := readOnlyLabel(e)
		if server := strings.Contains(label, "the server enforces it too"); server != e.ServerEnforcesReadOnly() {
			t.Errorf("%s: %q", e, label)
		}
	}
	if label := readOnlyLabel(db.TiDB); !strings.Contains(label, "the app's check alone: TiDB has no read-only session") {
		t.Errorf("TiDB: %q", label)
	}
}
