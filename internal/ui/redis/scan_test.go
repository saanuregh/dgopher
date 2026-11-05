package redis

import (
	"slices"
	"testing"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

// A scan that runs out of time keeps the keys it found and goes on from
// where it stopped, and still counts the database's keys.
func TestScanTimeoutKeepsKeysFound(t *testing.T) {
	scans := 0
	f := newFakeRedis(t, func(args []string) any {
		switch args[0] {
		case "SCAN":
			scans++
			if args[1] == "0" {
				return []any{"5", []string{"a", "b", "c"}}
			}
			if scans == 2 {
				time.Sleep(600 * time.Millisecond)
			}
			return []any{"0", []string{"d"}}
		case "DBSIZE":
			return int64(4)
		}
		return unhandled{}
	})
	scanTimeout = 200 * time.Millisecond
	t.Cleanup(func() { scanTimeout = 30 * time.Second })
	_, tt, _, r := openFake(t, f, db.Staging)
	testutil.WaitFor(t, tt, "the scan", func() bool { return !r.scanning && (r.scanErr != "" || len(r.keys) > 0) })
	if !slices.Equal(r.keys, []string{"a", "b", "c"}) || r.cursor.Cursor != 5 || r.scanDone {
		t.Fatalf("after the timeout: keys %v, cursor %+v, done %v", r.keys, r.cursor, r.scanDone)
	}
	if r.scanErr == "" || r.dbsize != 4 {
		t.Fatalf("error %q, dbsize %d", r.scanErr, r.dbsize)
	}
	r.scan()
	testutil.WaitFor(t, tt, "Scan More", func() bool { return !r.scanning && r.scanDone })
	if !slices.Equal(r.keys, []string{"a", "b", "c", "d"}) || r.scanErr != "" {
		t.Fatalf("after Scan More: keys %v, error %q", r.keys, r.scanErr)
	}
}
