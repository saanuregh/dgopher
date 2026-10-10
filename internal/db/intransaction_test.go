package db_test

import (
	"context"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

// With MySQL's autocommit off, however it was turned off, a transaction is
// open only from a statement that begins one to its COMMIT, as the server
// reports it: Tx, OwnsTx and InTransaction agree, and AutocommitOff says
// that the next statement will begin one.
func TestIntegrationAutocommitOff(t *testing.T) {
	testutil.Integration(t)
	ctx := context.Background()
	d := openFor(t, db.Config{Name: "mysql", Engine: db.MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dgopher", Database: "shop"})
	for _, s := range []string{"DROP TABLE IF EXISTS zz_autocommit_off", "DROP PROCEDURE IF EXISTS zz_autocommit_off",
		"CREATE TABLE zz_autocommit_off (id int) ENGINE=InnoDB", "CREATE PROCEDURE zz_autocommit_off() SET autocommit = 0"} {
		if _, err := d.SQL.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		d.SQL.Exec("DROP TABLE IF EXISTS zz_autocommit_off")
		d.SQL.Exec("DROP PROCEDURE IF EXISTS zz_autocommit_off")
	})
	sess, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if sess.AutocommitOff() {
		t.Fatal("autocommit off before any statement")
	}
	for _, step := range []struct {
		sql           string
		tx            db.TxState
		autocommitOff bool
	}{
		{"SET autocommit = 0", db.TxNone, true},
		{"INSERT INTO zz_autocommit_off VALUES (1)", db.TxOpen, true},
		{"COMMIT", db.TxNone, true},
		{"SET @x = 1", db.TxNone, true},
		{"INSERT INTO zz_autocommit_off VALUES (2)", db.TxOpen, true},
		{"SET autocommit = 1", db.TxNone, false},
		// A procedure hides the statement from its verbs: the server says.
		{"CALL zz_autocommit_off()", db.TxNone, true},
		{"INSERT INTO zz_autocommit_off VALUES (3)", db.TxOpen, true},
		{"ROLLBACK", db.TxNone, true},
		{"SET autocommit = 1", db.TxNone, false},
	} {
		if _, err := sess.Exec(ctx, step.sql); err != nil {
			t.Fatalf("%s: %v", step.sql, err)
		}
		tx, owns, off := sess.Tx(), sess.OwnsTx(), sess.AutocommitOff()
		if tx != step.tx || owns != (tx != db.TxNone) || off != step.autocommitOff {
			t.Fatalf("after %s: Tx %v, OwnsTx %v, AutocommitOff %v; want %v, autocommit off %v",
				step.sql, tx, owns, off, step.tx, step.autocommitOff)
		}
	}
	var n int
	d.SQL.QueryRow("SELECT count(*) FROM zz_autocommit_off").Scan(&n)
	if n != 2 {
		t.Fatalf("%d rows, want the 2 committed", n)
	}
}
