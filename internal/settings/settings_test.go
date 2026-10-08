package settings

import (
	"testing"
	"time"
)

func TestValueFormats(t *testing.T) {
	f := ViewFormat{GroupNumbers: true, BoolTicks: true, Binary: "base64", NullText: "[NULL]", Dates: "short"}
	when := time.Date(2026, 10, 8, 14, 5, 0, 0, time.UTC)
	cases := []struct {
		v    any
		want string
	}{
		{int64(1234567), "1,234,567"},
		{-9876.5, "-9,876.5"},
		{"12345", "12345"}, // text stays as it is
		{true, "✓"},
		{false, "✗"},
		{[]byte{0, 1, 2}, "AAEC"},
		{nil, "[NULL]"},
		{when, "Oct 8, 2026 14:05"},
	}
	for _, tc := range cases {
		if got := f.Format(tc.v); got != tc.want {
			t.Errorf("%#v: %q, want %q", tc.v, got, tc.want)
		}
	}
	if got := (ViewFormat{}).Format(int64(1234567)); got != "1234567" {
		t.Errorf("default: %q", got)
	}
	if got := (ViewFormat{Binary: "hex"}).Format([]byte("hi")); got != `\x6869` {
		t.Errorf("hex: %q", got)
	}
}
