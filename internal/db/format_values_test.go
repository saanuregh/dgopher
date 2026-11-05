package db

import (
	"math"
	"testing"
	"time"
)

// A time keeps its nanoseconds, as SQL Server's datetime2, Oracle's
// TIMESTAMP(9) and ClickHouse's DateTime64(9) hold them: a filter or a
// script of six digits would find, or write, another value.
func TestLiteralTimeNanoseconds(t *testing.T) {
	v := time.Date(2024, 2, 3, 4, 5, 6, 123456789, time.UTC)
	if got, want := Literal(Postgres, v), "'2024-02-03 04:05:06.123456789'"; got != want {
		t.Errorf("Literal = %s, want %s", got, want)
	}
	if got, want := Display(time.Date(2024, 2, 3, 4, 5, 6, 120000000, time.UTC)), "2024-02-03 04:05:06.12"; got != want {
		t.Errorf("Display = %s, want %s", got, want)
	}
}

// NaN and the infinities are written as each engine reads them, never
// bare, which no engine reads as a number.
func TestLiteralNonFinite(t *testing.T) {
	for _, c := range []struct {
		e    Engine
		v    any
		want string
	}{
		{Postgres, math.NaN(), "'NaN'::float8"},
		{Postgres, math.Inf(1), "'Infinity'::float8"},
		{Postgres, math.Inf(-1), "'-Infinity'::float8"},
		{CockroachDB, float32(math.Inf(1)), "'Infinity'::float8"},
		{DuckDB, math.NaN(), "'NaN'::DOUBLE"},
		{DuckDB, math.Inf(-1), "'-Infinity'::DOUBLE"},
		{ClickHouse, math.NaN(), "nan"},
		{ClickHouse, math.Inf(-1), "-inf"},
		{Oracle, math.NaN(), "BINARY_DOUBLE_NAN"},
		{Oracle, math.Inf(-1), "-BINARY_DOUBLE_INFINITY"},
		{SQLite, math.Inf(1), "9e999"},
		{SQLite, math.Inf(-1), "-9e999"},
		{SQLite, math.NaN(), "NULL"},
		{MySQL, math.NaN(), "NULL"},
		{SQLServer, math.Inf(1), "NULL"},
		{Postgres, 1.5, "1.5"},
	} {
		if got := Literal(c.e, c.v); got != c.want {
			t.Errorf("Literal(%s, %v) = %s, want %s", c.e, c.v, got, c.want)
		}
	}
}
