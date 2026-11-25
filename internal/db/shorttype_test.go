package db

import "testing"

// The SQL standard's long type names show by their short ones, a length
// and a precision kept; others as they are.
func TestShortType(t *testing.T) {
	for in, want := range map[string]string{
		"character varying(256)":      "varchar(256)",
		"character varying":           "varchar",
		"character(2)":                "char(2)",
		"character varying(32)[]":     "varchar(32)[]",
		"timestamp with time zone":    "timestamptz",
		"timestamp(3) with time zone": "timestamptz(3)",
		"timestamp without time zone": "timestamp",
		"time with time zone":         "timetz",
		"bit varying(8)":              "varbit(8)",
		"bigint":                      "bigint",
		"characteristic":              "characteristic",
		"double precision":            "double precision",
	} {
		if got := ShortType(in); got != want {
			t.Errorf("ShortType(%q) = %q, want %q", in, got, want)
		}
	}
}
