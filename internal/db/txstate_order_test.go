package db

import (
	"errors"
	"testing"

	"github.com/duckdb/duckdb-go/v2"
	"github.com/go-sql-driver/mysql"
)

// The order of txByVerbs.after's checks: a failed statement is judged
// before any verb, MySQL's deadlock before autocommit off; an implicit
// commit before the verb that begins again; SET apart from implicit
// commits; and autocommit off's "any statement begins one" last, as it
// was before the statement, for MySQL only.
func TestAfterFailedStatementOrder(t *testing.T) {
	failed := errors.New("failed")
	duplicate := &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}
	deadlock := &mysql.MySQLError{Number: 1213, Message: "Deadlock found"}
	lockWait := &mysql.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded"}
	exists := &mysql.MySQLError{Number: 1050, Message: "Table already exists"}
	duckConstraint := &duckdb.Error{Type: duckdb.ErrorTypeConstraint, Msg: "Constraint Error"}
	duckParser := &duckdb.Error{Type: duckdb.ErrorTypeParser, Msg: "Parser Error"}
	none, open := txByVerbs{}, txByVerbs{tx: TxOpen}
	off, offOpen := txByVerbs{autocommitOff: true}, txByVerbs{tx: TxOpen, autocommitOff: true}
	failedTx := txByVerbs{tx: TxFailed}
	others := []Engine{Postgres, ClickHouse, SQLite, DuckDB, Redis, "unknown"}
	notDuck := []Engine{Postgres, ClickHouse, SQLite, Redis, "unknown"}
	for _, c := range []struct {
		engines   []Engine
		before    txByVerbs
		sql       string
		err       error
		rollsBack bool
		want      txByVerbs
	}{
		// MySQL, a failed statement: deadlock and a lock wait rolled back
		// first, then autocommit off; no verb is read.
		{engines: []Engine{MySQL}, before: off, sql: "COMMIT", err: duplicate, want: offOpen},
		{engines: []Engine{MySQL}, before: off, sql: "SET autocommit = 1", err: duplicate, want: offOpen},
		{engines: []Engine{MySQL}, before: off, sql: "CREATE TABLE t (a INT)", err: exists, want: offOpen},
		{engines: []Engine{MySQL}, before: off, sql: "COMMIT", err: deadlock, want: off},
		{engines: []Engine{MySQL}, before: offOpen, sql: "UPDATE t SET a = 1", err: lockWait, rollsBack: true, want: off},
		{engines: []Engine{MySQL}, before: off, sql: "UPDATE t SET a = 1", err: lockWait, want: offOpen},
		{engines: []Engine{MySQL}, before: open, sql: "CREATE TABLE t (a INT)", err: exists, want: open},
		{engines: []Engine{MySQL}, before: none, sql: "BEGIN", err: failed, want: none},
		{engines: []Engine{MySQL}, before: open, sql: "COMMIT", err: failed, want: open},

		// Elsewhere a failed statement leaves the transaction, but
		// DuckDB's, which any error but a parse error aborts.
		{engines: notDuck, before: open, sql: "INSERT INTO t VALUES (1)", err: failed, want: open},
		{engines: notDuck, before: open, sql: "COMMIT", err: duckConstraint, want: open},
		{engines: others, before: off, sql: "SELECT 1", err: duplicate, want: off},
		{engines: others, before: none, sql: "BEGIN", err: failed, want: none},
		{engines: []Engine{DuckDB}, before: open, sql: "INSERT INTO t VALUES (1)", err: duckConstraint, want: txByVerbs{tx: TxFailed}},
		{engines: []Engine{DuckDB}, before: open, sql: "COMMIT", err: failed, want: txByVerbs{tx: TxFailed}},
		{engines: []Engine{DuckDB}, before: open, sql: "SELEC 1", err: duckParser, want: open},
		{engines: []Engine{DuckDB}, before: failedTx, sql: "SELECT 1", err: duckConstraint, want: failedTx},
		{engines: []Engine{DuckDB}, before: none, sql: "INSERT INTO t VALUES (1)", err: duckConstraint, want: none},

		// MySQL's implicit commit, then the verb: BEGIN commits and begins.
		{engines: []Engine{MySQL}, before: open, sql: "BEGIN", want: open},
		{engines: []Engine{MySQL}, before: open, sql: "START TRANSACTION", want: open},
		{engines: []Engine{MySQL}, before: offOpen, sql: "CREATE TABLE t (a INT)", want: off},
		{engines: []Engine{MySQL}, before: off, sql: "LOCK TABLES t WRITE", want: off},
		{engines: []Engine{MySQL}, before: off, sql: "/*!50001 CREATE ALGORITHM=UNDEFINED */ /*!50001 VIEW v AS SELECT 1 */", want: off},
		{engines: []Engine{MySQL}, before: off, sql: "CREATE TEMPORARY TABLE t (a INT)", want: offOpen},
		{engines: []Engine{MySQL}, before: none, sql: "BEGIN NOT ATOMIC SELECT 1; END", want: none},
		{engines: []Engine{MySQL}, before: off, sql: "BEGIN NOT ATOMIC SELECT 1; END", want: offOpen},

		// SET is read for autocommit alone, not as an implicit commit.
		{engines: []Engine{MySQL}, before: open, sql: "SET autocommit = 1", want: open},
		{engines: []Engine{MySQL}, before: open, sql: "SET PASSWORD = 'x'", want: open},
		{engines: []Engine{MySQL}, before: off, sql: "SET PASSWORD = 'x'", want: offOpen},
		{engines: []Engine{MySQL}, before: off, sql: "SET @a = 1", want: offOpen},
		{engines: []Engine{MySQL}, before: offOpen, sql: "SET autocommit = 1", want: none},
		{engines: []Engine{MySQL}, before: off, sql: "SET autocommit = 0", want: off},
		{engines: []Engine{MySQL}, before: none, sql: "SET autocommit = 0", want: off},
		{engines: []Engine{MySQL}, before: off, sql: "SET autocommit = 1", want: none},

		// Under autocommit off what ends a transaction begins none; the
		// rest begins one.
		{engines: []Engine{MySQL}, before: offOpen, sql: "COMMIT", want: off},
		{engines: []Engine{MySQL}, before: offOpen, sql: "COMMIT AND CHAIN", want: offOpen},
		{engines: []Engine{MySQL}, before: off, sql: "ROLLBACK TO SAVEPOINT s", want: offOpen},
		{engines: []Engine{MySQL}, before: off, sql: "XA COMMIT 'x'", want: off},
		{engines: []Engine{MySQL}, before: off, sql: "SAVEPOINT a", want: offOpen},
		{engines: []Engine{MySQL}, before: none, sql: "SAVEPOINT a", want: none},

		// Autocommit off counts for MySQL alone.
		{engines: others, before: off, sql: "SELECT 1", want: off},
		{engines: others, before: offOpen, sql: "SET autocommit = 1", want: offOpen},
		{engines: others, before: open, sql: "CREATE TABLE t (a INT)", want: open},
		{engines: others, before: open, sql: "BEGIN NOT ATOMIC SELECT 1; END", want: open},
		{engines: []Engine{SQLite}, before: off, sql: "SAVEPOINT a", want: offOpen},
		{engines: []Engine{Postgres, ClickHouse, DuckDB, Redis, "unknown"}, before: off, sql: "SAVEPOINT a", want: off},
	} {
		for _, e := range c.engines {
			got := c.before.after(e, c.sql, c.err, func() bool { return c.rollsBack })
			if got != c.want {
				t.Errorf("%s from %+v: %q (error %v): %+v; want %+v", e, c.before, c.sql, c.err, got, c.want)
			}
		}
	}
}
