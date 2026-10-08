package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func sqliteFile(t *testing.T) Config {
	path := filepath.Join(t.TempDir(), "s.sqlite")
	os.WriteFile(path, nil, 0o600)
	return Config{Name: "s", Engine: SQLite, Database: path}
}

// A closed session never reconnects: what follows its Close would run
// outside its transaction.
func TestClosedSessionRefusesStatements(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, sqliteFile(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SQL.Exec("CREATE TABLE t (a INT)")
	s, _ := d.Session(ctx)
	s.Begin(ctx)
	s.Exec(ctx, "INSERT INTO t VALUES (1)")
	s.Close()
	if _, err := s.Exec(ctx, "INSERT INTO t VALUES (2)"); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("a statement after Close: %v", err)
	}
	var n int
	d.SQL.QueryRow("SELECT count(*) FROM t").Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows: the closed session's transaction was not rolled back, or a later statement ran", n)
	}
}

// On a pool of one connection, closing a session leaves another's
// transaction alone.
func TestSharedTransactionBelongsToItsSession(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, Config{Name: "m", Engine: SQLite, Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SQL.Exec("CREATE TABLE t (a INT)")
	s1, _ := d.Session(ctx)
	s2, _ := d.Session(ctx)
	if err := s1.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	s1.Exec(ctx, "INSERT INTO t VALUES (1)")
	s2.Close()
	if s1.Tx() != TxOpen {
		t.Fatalf("tx %v after another session closed", s1.Tx())
	}
	if err := s1.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	var n int
	d.SQL.QueryRow("SELECT count(*) FROM t").Scan(&n)
	if n != 0 || s1.Tx() != TxNone {
		t.Fatalf("after rollback: %d rows, tx %v", n, s1.Tx())
	}
	// Closing the session that began it rolls it back, and frees it.
	s1.Begin(ctx)
	s1.Close()
	s3, _ := d.Session(ctx)
	if s3.Tx() != TxNone {
		t.Fatalf("tx %v after its session closed", s3.Tx())
	}
}
