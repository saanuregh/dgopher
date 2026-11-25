package fileimport

import (
	"testing"
)

// An integer past 64 bits keeps its digits, in CSV, JSON and XML alike:
// DuckDB reads one in a CSV as a HUGEINT, or past 128 bits a BIGNUM, and
// a column it would read as a DOUBLE that rounds it is read as text.
func TestIntegersPastInt64KeepTheirDigits(t *testing.T) {
	const big = "99999999999999999999"
	csv := write(t, "n.csv", "id,n\n1,"+big+"\n2,5\n")
	if got := rows(t, open(t, csv, Options{}, Column{"id", "BIGINT"}, Column{"n", "HUGEINT"})); got[0][1] != big || got[1][1] != "5" {
		t.Errorf("csv rows %v", got)
	}
	json := write(t, "n.json", `[{"id": 1, "n": `+big+`, "o": {"k": 1}}, {"id": 2, "n": 5, "o": null}]`)
	if got := rows(t, open(t, json, Options{}, Column{"id", "BIGINT"}, Column{"n", "VARCHAR"}, Column{"o", "JSON"})); got[0][1] != big || got[0][2] != `{"k":1}` {
		t.Errorf("json rows %v", got)
	}
	const huge = "9999999999999999999999999999999999999999" // past 128 bits
	wide := write(t, "w.csv", "id,n\n1,"+huge+"\n2,5\n")
	if got := rows(t, open(t, wide, Options{}, Column{"id", "BIGINT"}, Column{"n", "BIGNUM"})); got[0][1] != huge || got[1][1] != "5" {
		t.Errorf("csv rows past 128 bits %v", got)
	}
	xml := write(t, "n.xml", `<r><x n="`+big+`"/><x n="5"/></r>`)
	if got := rows(t, open(t, xml, Options{}, Column{"n", "VARCHAR"})); got[0][0] != big {
		t.Errorf("xml rows %v", got)
	}
	// A column of ordinary numbers stays a number.
	open(t, write(t, "f.csv", "x\n1.5\n2\n"), Options{}, Column{"x", "DOUBLE"})
}
