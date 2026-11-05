package redis

import (
	"strings"
	"testing"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

// Deleting keys stopped by the user says so in words, with the count
// deleted.
func TestStoppedBulkDeleteSaysSo(t *testing.T) {
	f := newFakeRedis(t, func(args []string) any {
		switch strings.ToUpper(args[0]) {
		case "SCAN":
			return []any{"7", []string{"a", "b"}}
		case "UNLINK", "DEL":
			time.Sleep(150 * time.Millisecond)
			return int64(len(args) - 1)
		}
		return unhandled{}
	})
	a, tt, _, r := openFake(t, f, db.Development)
	r.openBulk(bulkDelete)
	b := r.bulk
	b.counted, b.count = "x*", 4
	r.deleteKeys(b)
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "a deletion", func() bool { return b.done > 0 })
	b.cancel()
	testutil.WaitFor(t, tt, "the stop", func() bool { return !b.running })
	if !strings.HasPrefix(b.err, "Stopped after ") || strings.Contains(b.err, "context") {
		t.Fatalf("stopped: %q", b.err)
	}
}
