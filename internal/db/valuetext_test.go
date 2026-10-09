package db

import (
	"reflect"
	"testing"
	"time"
)

func TestValueText(t *testing.T) {
	at := time.Date(2024, 3, 1, 9, 30, 15, 120000000, time.FixedZone("x", 3600))
	for _, c := range []struct {
		v    any
		typ  string
		want any
	}{
		{at, "DATE", "2024-03-01"},
		{at, "TIMESTAMP", "2024-03-01 09:30:15.12"},
		{at, "TIMESTAMP WITH TIME ZONE", "2024-03-01 09:30:15.12+01:00"},
		{at, "TIME", "09:30:15.12"},
		{true, "BOOLEAN", "true"},
		{[]any{"a", 1.0}, "JSON", `["a",1]`},
		{nil, "VARCHAR", nil},
		{1.5, "DOUBLE", "1.5"},
	} {
		if got := ValueText(c.v, c.typ); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v as %s: %v, want %v", c.v, c.typ, got, c.want)
		}
	}
}

// TestInsertBoolean checks a boolean, read as true or as 1, is written as
// each engine keeps it.
func TestInsertBoolean(t *testing.T) {
	for _, c := range []struct {
		e    Engine
		v    any
		want any
	}{
		{MySQL, int64(1), Typed("1")},
		{MySQL, true, Typed("1")},
		{SQLite, int64(0), Typed("0")},
		{SQLite, "t", Typed("1")},
		{Postgres, int64(1), Typed("true")},
		{DuckDB, "0", Typed("false")},
		{MySQL, nil, nil},
	} {
		if got := InsertValue(c.e, c.v, "BOOLEAN"); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v into %s: %v, want %v", c.v, c.e, got, c.want)
		}
	}
}
