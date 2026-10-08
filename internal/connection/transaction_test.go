package connection

import (
	"testing"
	"time"

	"dgopher/internal/db"
)

// A transaction keeps the time it opened while in use, and forgets it
// once it ends.
func TestTxTimes(t *testing.T) {
	var tt TxTimes
	t0 := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	tt.Set(db.TxOpen, t0)
	tt.Set(db.TxOpen, t0.Add(time.Minute))
	if !tt.Opened.Equal(t0) || !tt.Used.Equal(t0.Add(time.Minute)) {
		t.Fatalf("open: %+v", tt)
	}
	tt.Set(db.TxNone, t0.Add(2*time.Minute))
	if !tt.Opened.IsZero() || !tt.Used.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("ended: %+v", tt)
	}
}
