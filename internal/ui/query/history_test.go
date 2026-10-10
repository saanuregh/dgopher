package query

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/secretcmd"
)

// A statement that failed as its pool's new connection ran the identity's
// command keeps in the history what the audit log keeps, not what the
// command printed to stderr, which only the live error shows.
func TestHistoryHidesCommandStderr(t *testing.T) {
	ce := &secretcmd.CommandError{Command: "aws", Status: 254, Stderr: "profile secret-hunter2 has expired"}
	err := fmt.Errorf("could not connect: %w", fmt.Errorf("%s: %w", db.IdentityAWS.Label(), ce))
	e := historyEntry(db.Config{ID: "pg", Name: "pg"}, "shop", "SELECT 1", time.Now(), time.Second, -1, err)
	if strings.Contains(e.Error, "hunter2") || e.Error != secretcmd.AuditText(err) {
		t.Fatalf("the history keeps %q, want %q", e.Error, secretcmd.AuditText(err))
	}
	if e := historyEntry(db.Config{ID: "pg"}, "shop", "SELECT 1", time.Now(), 0, 1, nil); e.Error != "" {
		t.Fatalf("a statement without an error keeps %q", e.Error)
	}
}
