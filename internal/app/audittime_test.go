package app

import (
	"testing"
	"time"
)

// The log's times always carry their date, and the year when it is not
// this one.
func TestAuditTime(t *testing.T) {
	now := time.Date(2026, 10, 16, 20, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 10, 16, 19, 27, 24, 0, time.Local), "Oct 16 19:27:24"},
		{time.Date(2026, 10, 15, 18, 22, 32, 0, time.Local), "Oct 15 18:22:32"},
		{time.Date(2025, 12, 31, 23, 59, 0, 0, time.Local), "Dec 31 2025 23:59:00"},
	} {
		if got := auditTime(tc.at, now); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.at, got, tc.want)
		}
	}
}
