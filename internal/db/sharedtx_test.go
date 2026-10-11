package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// duckFile opens a DuckDB database in a file of the test's own.
func duckFile(t *testing.T) *DB {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "d.duckdb")
	if err := CreateFile(ctx, DuckDB, path); err != nil {
		t.Fatal(err)
	}
	d, err := Open(ctx, Config{Name: "d", Engine: DuckDB, Database: path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func countRows(t *testing.T, d *DB, table string) int {
	t.Helper()
	var n int
	if err := d.SQL.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A catalog read, which runs on DuckDB's one connection outside any
// session, can abort the open transaction when interrupted: its Commit
// then commits nothing, and says so.
func TestCommitAfterOutOfBandAbortFails(t *testing.T) {
	ctx := context.Background()
	d := duckFile(t)
	s, _ := d.Session(ctx)
	defer s.Close()
	if _, err := s.Exec(ctx, "CREATE TABLE t (a INT)"); err != nil {
		t.Fatal(err)
	}
	if err := s.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	rows, err := d.Catalog().QueryContext(cctx, "SELECT count(*) FROM range(10000000000)")
	if err == nil {
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
	}
	cancel()
	if err == nil {
		t.Fatal("the catalog read was not interrupted")
	}
	if err := s.Commit(ctx); !errors.Is(err, ErrTxFailed) {
		t.Fatalf("Commit of an aborted transaction: %v, want ErrTxFailed", err)
	}
	if state, owner := d.SharedTx(); state != TxNone || owner != nil {
		t.Fatalf("after the commit: %v owned by %p, want none", state, owner)
	}
	if n := countRows(t, d, "t"); n != 0 {
		t.Fatalf("%d rows committed from an aborted transaction", n)
	}
	if err := s.Begin(ctx); err != nil {
		t.Fatalf("Begin after the failed commit: %v", err)
	}
	if _, err := s.Exec(ctx, "INSERT INTO t VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(ctx); err != nil {
		t.Fatalf("Commit of the next transaction: %v", err)
	}
	if n := countRows(t, d, "t"); n != 1 {
		t.Fatalf("%d rows after the next commit, want 1", n)
	}
}

// Closing a session waits for the one connection, however long another
// holds it, to roll back the transaction it began; meanwhile the
// transaction stays its own, which no other session's statement takes.
func TestCloseWaitsForSharedConnThenRollsBack(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(t *testing.T) *DB
	}{
		{"duckdb", duckFile},
		{"sqlite memory", func(t *testing.T) *DB {
			d, err := Open(context.Background(), Config{Name: "m", Engine: SQLite, Database: ":memory:"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { d.Close() })
			return d
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			d := tc.open(t)
			a, _ := d.Session(ctx)
			b, _ := d.Session(ctx)
			defer b.Close()
			if _, err := a.Exec(ctx, "CREATE TABLE t (a INT)"); err != nil {
				t.Fatal(err)
			}
			if err := a.Begin(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Exec(ctx, "INSERT INTO t VALUES (1)"); err != nil {
				t.Fatal(err)
			}
			// Another editor's long statement holds the one connection,
			// past the 5 seconds Close used to wait.
			busy, err := d.SQL.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			closed := make(chan error, 1)
			go func() { closed <- a.Close() }()
			select {
			case err := <-closed:
				busy.Close()
				t.Fatalf("Close returned %v while the connection was held", err)
			case <-time.After(6 * time.Second):
			}
			if state, owner := d.SharedTx(); state != TxOpen || owner != a {
				busy.Close()
				t.Fatalf("while Close waits: %v owned by %p, want open and the closing session's", state, owner)
			}
			ran := make(chan error, 1)
			go func() {
				_, err := b.Exec(ctx, "SELECT 1")
				ran <- err
			}()
			time.Sleep(50 * time.Millisecond)
			busy.Close()
			if err := <-closed; err != nil {
				t.Fatalf("Close: %v", err)
			}
			if err := <-ran; err != nil {
				t.Fatalf("the other session's statement: %v", err)
			}
			if err := b.Commit(ctx); !errors.Is(err, ErrNoTransaction) {
				t.Fatalf("the other session's Commit: %v, want ErrNoTransaction", err)
			}
			if state, owner := d.SharedTx(); state != TxNone || owner != nil {
				t.Fatalf("after Close: %v owned by %p, want none", state, owner)
			}
			if n := countRows(t, d, "t"); n != 0 {
				t.Fatalf("%d rows: the closed session's transaction was committed", n)
			}
		})
	}
}

// Closing the pool, as disconnecting and quitting do, ends a Close that
// waits for the one connection.
func TestCloseUnblockedByDBClose(t *testing.T) {
	ctx := context.Background()
	d := duckFile(t)
	a, _ := d.Session(ctx)
	if _, err := a.Exec(ctx, "CREATE TABLE t (a INT)"); err != nil {
		t.Fatal(err)
	}
	if err := a.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	busy, err := d.SQL.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	closed := make(chan error, 1)
	go func() { closed <- a.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned %v while the connection was held", err)
	case <-time.After(300 * time.Millisecond):
	}
	go d.Close()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close still waits 2 seconds after the pool closed")
	}
}

// A DuckDB backup is refused while a transaction is open on the one
// connection: its EXPORT DATABASE would export the uncommitted rows, and
// its failure could abort the transaction.
func TestDuckBackupRefusedInTransaction(t *testing.T) {
	ctx := context.Background()
	d := duckFile(t)
	s, _ := d.Session(ctx)
	defer s.Close()
	if _, err := s.Exec(ctx, "CREATE TABLE t (a INT)"); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(t.TempDir(), "backup")
	run, err := PlanBackup(ctx, d, BackupOptions{Format: BackupParquet, Path: folder})
	if err != nil {
		t.Fatal(err)
	}
	refused := func(why string) {
		t.Helper()
		if err := BackupDuckDB(ctx, d, run.Statement); !errors.Is(err, ErrBackupInTransaction) {
			t.Fatalf("backup %s: %v, want ErrBackupInTransaction", why, err)
		}
		if _, err := os.Stat(folder); !os.IsNotExist(err) {
			t.Fatalf("backup %s wrote %s: %v", why, folder, err)
		}
	}

	if err := s.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(ctx, "INSERT INTO t VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	refused("in a session's transaction")
	if state, owner := d.SharedTx(); state != TxOpen || owner != s {
		t.Fatalf("after the refused backup: %v owned by %p, want the session's still open", state, owner)
	}
	if err := s.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	// Begun outside any session: only the connection knows.
	if _, err := d.SQL.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	refused("in a transaction no session recorded")
	if _, err := d.SQL.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}

	if err := BackupDuckDB(ctx, d, run.Statement); err != nil {
		t.Fatalf("backup with no transaction open: %v", err)
	}
	if _, err := os.Stat(filepath.Join(folder, "schema.sql")); err != nil {
		t.Fatalf("the backup wrote no schema: %v", err)
	}
}

// A COMMIT typed in an editor goes through ExecTransactionControl: of a
// transaction an interrupted catalog read aborted, it commits nothing and
// says so too, where DuckDB would answer it with a rollback and no error.
// A typed ROLLBACK of it goes through.
func TestTypedCommitAfterOutOfBandAbortFails(t *testing.T) {
	for _, end := range []string{"COMMIT", "ROLLBACK"} {
		t.Run(end, func(t *testing.T) {
			ctx := context.Background()
			d := duckFile(t)
			s, _ := d.Session(ctx)
			defer s.Close()
			for _, q := range []string{"CREATE TABLE t (a INT)", "BEGIN", "INSERT INTO t VALUES (1)"} {
				if _, err := s.Exec(ctx, q); err != nil {
					t.Fatal(q, err)
				}
			}
			cctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			rows, err := d.Catalog().QueryContext(cctx, "SELECT count(*) FROM range(10000000000)")
			if err == nil {
				for rows.Next() {
				}
				err = rows.Err()
				rows.Close()
			}
			cancel()
			if err == nil {
				t.Fatal("the catalog read was not interrupted")
			}
			_, err = s.ExecTransactionControl(ctx, end)
			if want := end == "COMMIT"; errors.Is(err, ErrTxFailed) != want || !want && err != nil {
				t.Fatalf("typed %s of an aborted transaction: %v", end, err)
			}
			if state, owner := d.SharedTx(); state != TxNone || owner != nil {
				t.Fatalf("after %s: %v owned by %p, want none", end, state, owner)
			}
			if n := countRows(t, d, "t"); n != 0 {
				t.Fatalf("%d rows committed from an aborted transaction", n)
			}
		})
	}
}
