package db

import "testing"

// Conditions of the design code that combine rules hooks, against the
// comparisons they replaced. Only an engine with a Dialect reaches them:
// Redis and an unknown engine have no SQL rules.
func TestRewrittenDesignConditions(t *testing.T) {
	type row struct {
		commentsApart           bool
		keysError, indexesError string
	}
	// commentsApart was e == Postgres || e == DuckDB; supported gave
	// ClickHouse's error for a change of keys or indexes, DuckDB's for one
	// of keys.
	const clickhouseError = "ClickHouse cannot change a table's key, indexes or constraints here: its sorting key is fixed when the table is made"
	const duckdbError = "DuckDB cannot change the keys or checks of a table it made: make the table again"
	old := map[Engine]row{
		Postgres:   {commentsApart: true},
		MySQL:      {},
		ClickHouse: {keysError: clickhouseError, indexesError: clickhouseError},
		SQLite:     {},
		DuckDB:     {commentsApart: true, keysError: duckdbError},
	}
	errText := func(err error) string {
		if err == nil {
			return ""
		}
		return err.Error()
	}
	for _, e := range pinEngines {
		r, ok := sqlRulesOf(e)
		if want, sql := old[e]; ok != sql {
			t.Fatalf("%s: SQL rules %v, want %v", e, ok, sql)
		} else if !ok {
			continue
		} else {
			got := row{
				commentsApart: commentsApart(r),
				keysError:     errText(designDiff{primaryKeyChanged: true}.supported(r)),
				indexesError:  errText(designDiff{addedIndexes: []IndexDesign{{Name: "i"}}}.supported(r)),
			}
			if got != want {
				t.Errorf("%s:\n got  %+v\n want %+v", e, got, want)
			}
		}
	}
}
