package datamodel

import (
	"fmt"
	"testing"

	"dgopher/internal/db"
)

// The conditions on an engine that the rules replaced, as each site now
// writes them, against what the comparisons they replaced gave for every
// engine and an unknown one.
func TestRewrittenEngineConditions(t *testing.T) {
	type row struct {
		ignoresVarcharLength, keepsKeyNames, changesTablesWhole, splitsCycles, addsNoKeyToMadeTable,
		numbersNoRows, keyNamesInSchema, keepsNoTimeZone, textIsLongtext, keepsNoComments,
		namesConstraintsItself, noSecondaryIndexes, noForeignKeys, noKeyActions, hasBoolean bool
	}
	// Each field is the old comparison: e == DuckDB; e != SQLite && e !=
	// DuckDB; e != Postgres && e != MySQL; e != SQLite; e == DuckDB; e is
	// DuckDB or ClickHouse; e == MySQL; e is MySQL or SQLite; e == MySQL;
	// e == SQLite; e == SQLite; e == ClickHouse; e == ClickHouse; e ==
	// DuckDB; e != SQLite.
	old := map[db.Engine]row{
		db.Postgres:   {false, true, false, true, false, false, false, false, false, false, false, false, false, false, true},
		db.MySQL:      {false, true, false, true, false, false, true, true, true, false, false, false, false, false, true},
		db.ClickHouse: {false, true, true, true, false, true, false, false, false, false, false, true, true, false, true},
		db.SQLite:     {false, false, true, false, false, false, false, true, false, true, true, false, false, false, false},
		db.DuckDB:     {true, false, true, true, true, true, false, false, false, false, false, false, false, true, true},
		db.Redis:      {false, true, true, true, false, false, false, false, false, false, false, false, false, false, true},
		"unknown":     {false, true, true, true, false, false, false, false, false, false, false, false, false, false, true},
	}
	for _, e := range pinEngines {
		got := row{
			ignoresVarcharLength:   db.IgnoresVarcharLength(e),
			keepsKeyNames:          db.KeepsKeyNames(e),
			changesTablesWhole:     changesTablesWhole(e),
			splitsCycles:           !db.RebuildsToAlter(e),
			addsNoKeyToMadeTable:   addsNoKeyToMadeTable(e),
			numbersNoRows:          !db.NumbersRows(e),
			keyNamesInSchema:       db.ForeignKeyNamesUniqueInSchema(e),
			keepsNoTimeZone:        !keepsKind(e, db.TypeKindTimestampTZ),
			textIsLongtext:         db.TypeOfKind(e, db.TypeKindText, "", "") == "LONGTEXT",
			keepsNoComments:        !db.Comments(e),
			namesConstraintsItself: db.NamesConstraintsItself(e),
			noSecondaryIndexes:     !db.SecondaryIndexes(e),
			noForeignKeys:          !db.HasForeignKeys(e),
			noKeyActions:           !db.ForeignKeyActions(e),
			hasBoolean:             keepsKind(e, db.TypeKindBoolean),
		}
		if want := old[e]; got != want {
			t.Errorf("%s:\n got  %s\n want %s", e, fmt.Sprintf("%+v", got), fmt.Sprintf("%+v", want))
		}
	}
}
