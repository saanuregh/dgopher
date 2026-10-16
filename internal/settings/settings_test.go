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

func TestExportLimit(t *testing.T) {
	for p, want := range map[ExportPrefs]int{
		{}:                               DefaultExportRowLimit,
		{RowLimit: 500}:                  500,
		{Unlimited: true}:                0,
		{RowLimit: 500, Unlimited: true}: 0,
	} {
		if got := p.Limit(); got != want {
			t.Errorf("%+v: %d, want %d", p, got, want)
		}
	}
}

// Only plain decimals are grouped: a date or Infinity stays as written.
func TestReadableNumber(t *testing.T) {
	for in, want := range map[string]string{"987949": "987,949", "-1234.5": "-1,234.5", "15": "15", "2024-01-01": "2024-01-01", "+Inf": "+Inf", "1e+06": "1e+06"} {
		if got := ReadableNumber(in); got != want {
			t.Errorf("ReadableNumber(%q) = %q, want %q", in, got, want)
		}
	}
}
