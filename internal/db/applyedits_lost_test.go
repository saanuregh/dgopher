package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

// A connection lost while edits apply is reported once, as lost: the
// rollback that follows is not a second failure.
func TestIntegrationApplyEditsLostConnection(t *testing.T) {
	testutil.Integration(t)
	ctx := context.Background()
	d := openFor(t, testutil.PGConfig())
	sess, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	err = sess.ApplyEdits(ctx, []db.Statement{
		{SQL: `SELECT pg_terminate_backend(pg_backend_pid())`, Want: -1},
	}, func(int, int64, time.Duration, error) {})
	if !errors.Is(err, db.ErrTxLost) {
		t.Fatalf("got %v, want the transaction reported lost", err)
	}
	if strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("the lost connection was reported twice: %v", err)
	}
}
