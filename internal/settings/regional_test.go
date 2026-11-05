package settings

import (
	"math"
	"math/big"
	"testing"
	"time"
)

func withRegion(t *testing.T, locale string) {
	old := RegionLocale
	RegionLocale = locale
	t.Cleanup(func() { RegionLocale = old })
}

func TestRegionalNumbersKeepEveryDigit(t *testing.T) {
	huge, _ := new(big.Int).SetString("123456789012345678901234567890123456789", 10)
	cases := []struct {
		locale string
		v      any
		want   string
	}{
		{"de-DE", 1234567.89, "1.234.567,89"},
		{"en-IN", 1234567.89, "12,34,567.89"},
		{"de-DE", int64(-1234567), "-1.234.567"},
		{"de-DE", 0.1234567891234, "0,1234567891234"},
		{"de-DE", 1e21, "1.000.000.000.000.000.000.000"},
		{"de-DE", huge, "123.456.789.012.345.678.901.234.567.890.123.456.789"},
		{"en-IN", uint64(math.MaxUint64), "1,84,46,74,40,73,70,95,51,615"},
		{"de-DE", math.NaN(), "NaN"},
		{"de-DE", math.Inf(-1), "-Inf"},
		{"de-DE", "1234567.0123456789", "1234567.0123456789"},
	}
	for _, c := range cases {
		withRegion(t, c.locale)
		if got := (ViewFormat{}).Format(c.v); got != c.want {
			t.Errorf("%s %v: got %q, want %q", c.locale, c.v, got, c.want)
		}
	}
}

// The two number toggles combine: regional or not, grouped or not.
func TestNumberToggles(t *testing.T) {
	cases := []struct {
		locale string
		f      ViewFormat
		want   string
	}{
		{"de-DE", ViewFormat{}, "1.234.567,89"},
		{"de-DE", ViewFormat{UngroupedNumbers: true}, "1234567,89"},
		{"de-DE", ViewFormat{NonRegionalNumbers: true}, "1,234,567.89"},
		{"de-DE", ViewFormat{NonRegionalNumbers: true, UngroupedNumbers: true}, "1234567.89"},
		{"", ViewFormat{}, "1,234,567.89"}, // no region known: grouped with "," and "."
		{"", ViewFormat{UngroupedNumbers: true}, "1234567.89"},
	}
	for _, c := range cases {
		withRegion(t, c.locale)
		if got := c.f.Format(1234567.89); got != c.want {
			t.Errorf("%q %+v: got %q, want %q", c.locale, c.f, got, c.want)
		}
	}
}

func TestRegionalDates(t *testing.T) {
	when := time.Date(2026, 10, 8, 14, 5, 0, 0, time.UTC)
	day := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		locale string
		v      time.Time
		want   string
	}{
		{"de-DE", when, "08.10.2026, 14:05"},
		{"de-DE", day, "08.10.2026"},
		{"en-IN", day, "8 Oct 2026"},
	}
	for _, c := range cases {
		withRegion(t, c.locale)
		if got := (ViewFormat{Dates: "regional"}).Format(c.v); got != c.want {
			t.Errorf("%s %v: got %q, want %q", c.locale, c.v, got, c.want)
		}
	}
	withRegion(t, "")
	if got, want := (ViewFormat{Dates: "regional"}).Format(when), (ViewFormat{}).Format(when); got != want {
		t.Errorf("no region: got %q, want %q", got, want)
	}
}
