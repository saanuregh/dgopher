package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
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

// On a pool of one connection, only the session that began the shared
// transaction commits or rolls it back, closing included: another's
// Commit and Rollback send nothing, and none is sent when no transaction
// is open, as for a session that missed another's COMMIT.
// A statement typed to begin or end a transaction, run by a session that
// does not own the one open on a pool of one connection, is refused once
// it holds the connection: nothing is sent.
func TestNonOwnerTransactionControlRefused(t *testing.T) {
	ctx := context.Background()
	for _, cfg := range []Config{
		{Name: "duck", Engine: DuckDB, Database: ":memory:"},
		{Name: "m", Engine: SQLite, Database: ":memory:"},
	} {
		t.Run(string(cfg.Engine), func(t *testing.T) {
			d, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			d.SQL.Exec("CREATE TABLE t (a INT)")
			a, _ := d.Session(ctx)
			defer a.Close()
			b, _ := d.Session(ctx)
			defer b.Close()
			if _, err := b.ExecTransactionControl(ctx, "BEGIN"); err != nil {
				t.Fatalf("BEGIN with none open: %v", err)
			}
			if _, err := b.ExecTransactionControl(ctx, "ROLLBACK"); err != nil {
				t.Fatalf("its own ROLLBACK: %v", err)
			}
			if _, err := a.Exec(ctx, "BEGIN"); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Exec(ctx, "INSERT INTO t VALUES (1)"); err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{"COMMIT", "ROLLBACK", "BEGIN"} {
				if _, err := b.ExecTransactionControl(ctx, q); !errors.Is(err, ErrNotOwner) {
					t.Fatalf("another session's %s: %v", q, err)
				}
			}
			if state, owner := d.SharedTx(); state != TxOpen || owner != a {
				t.Fatalf("the transaction: %v, owned by a %v", state, owner == a)
			}
			if _, err := a.ExecTransactionControl(ctx, "ROLLBACK"); err != nil {
				t.Fatalf("the owner's ROLLBACK: %v", err)
			}
		})
	}
}

// Begin, and an atomic change's BEGIN, inside another session's shared
// transaction are refused while they hold the connection: nothing is
// sent, so DuckDB's transaction is not aborted by a failed BEGIN.
func TestNonOwnerBeginRefused(t *testing.T) {
	ctx := context.Background()
	for _, cfg := range []Config{
		{Name: "duck", Engine: DuckDB, Database: ":memory:"},
		{Name: "m", Engine: SQLite, Database: ":memory:"},
	} {
		t.Run(string(cfg.Engine), func(t *testing.T) {
			d, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			d.SQL.Exec("CREATE TABLE t (a INT)")
			a, _ := d.Session(ctx)
			defer a.Close()
			b, _ := d.Session(ctx)
			defer b.Close()
			if err := a.Begin(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := a.Exec(ctx, "INSERT INTO t VALUES (1)"); err != nil {
				t.Fatal(err)
			}
			if err := b.Begin(ctx); !errors.Is(err, ErrNotOwner) {
				t.Fatalf("another session's Begin: %v, want ErrNotOwner", err)
			}
			var sent []string
			change := SchemaChange{Atomic: true, Steps: []Step{{SQL: "CREATE TABLE u (a INT)"}}}
			err = b.Apply(ctx, change, func(stmt string, _ int64, _ time.Duration, err error) {
				if err == nil {
					sent = append(sent, stmt)
				}
			})
			if !errors.Is(err, ErrNotOwner) || len(sent) > 0 {
				t.Fatalf("another session's atomic change: %v, ran %q; want ErrNotOwner and nothing", err, sent)
			}
			if state, owner := d.SharedTx(); state != TxOpen || owner != a {
				t.Fatalf("the transaction: %v, owned by a %v", state, owner == a)
			}
			if _, err := a.Exec(ctx, "INSERT INTO t VALUES (2)"); err != nil {
				t.Fatalf("the owner's next statement: %v", err)
			}
			if err := a.Commit(ctx); err != nil {
				t.Fatalf("the owner's Commit: %v", err)
			}
			var n int
			if err := d.SQL.QueryRowContext(ctx, "SELECT count(*) FROM t").Scan(&n); err != nil || n != 2 {
				t.Fatalf("%d rows (%v), want the owner's 2", n, err)
			}
		})
	}
}

func TestNonOwnerCannotEndSharedTx(t *testing.T) {
	ctx := context.Background()
	for _, cfg := range []Config{
		{Name: "duck", Engine: DuckDB, Database: ":memory:"},
		{Name: "m", Engine: SQLite, Database: ":memory:"},
	} {
		t.Run(string(cfg.Engine), func(t *testing.T) {
			d, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			d.SQL.Exec("CREATE TABLE t (a INT)")
			a, _ := d.Session(ctx)
			defer a.Close()
			b, _ := d.Session(ctx)
			defer b.Close()
			count := func() int {
				t.Helper()
				c, err := a.Query(ctx, "SELECT count(*) FROM t")
				if err != nil {
					t.Fatal(err)
				}
				rows, err := c.Fetch(1)
				if err != nil {
					t.Fatal(err)
				}
				n, _ := strconv.Atoi(Display(rows[0][0]))
				return n
			}
			if err := a.Begin(ctx); err != nil {
				t.Fatal(err)
			}
			a.Exec(ctx, "INSERT INTO t VALUES (1)")
			if err := b.Commit(ctx); !errors.Is(err, ErrNotOwner) {
				t.Fatalf("another session's Commit: %v, want ErrNotOwner", err)
			}
			if err := b.Rollback(ctx); !errors.Is(err, ErrNotOwner) {
				t.Fatalf("another session's Rollback: %v, want ErrNotOwner", err)
			}
			closing, _ := d.Session(ctx)
			closing.Exec(ctx, "SELECT 1")
			closing.Close()
			if state, owner := d.SharedTx(); state != TxOpen || owner != a || count() != 1 {
				t.Fatalf("after another session's Commit, Rollback and Close: %v, owned by a %v, %d rows", state, owner == a, count())
			}
			if err := a.Commit(ctx); err != nil {
				t.Fatalf("the owner's Commit: %v", err)
			}
			for _, s := range []*Session{a, b} {
				if err := s.Commit(ctx); !errors.Is(err, ErrNoTransaction) {
					t.Fatalf("Commit with no transaction open: %v, want ErrNoTransaction", err)
				}
				if err := s.Rollback(ctx); !errors.Is(err, ErrNoTransaction) {
					t.Fatalf("Rollback with no transaction open: %v, want ErrNoTransaction", err)
				}
			}
			// a missed b's typed COMMIT and the transaction b began after
			// it: a's Roll Back must not take b's row.
			a.Begin(ctx)
			b.Exec(ctx, "COMMIT")
			b.Begin(ctx)
			b.Exec(ctx, "INSERT INTO t VALUES (2)")
			if err := a.Rollback(ctx); !errors.Is(err, ErrNotOwner) {
				t.Fatalf("a stale owner's Rollback: %v, want ErrNotOwner", err)
			}
			if err := b.Commit(ctx); err != nil {
				t.Fatalf("b's Commit: %v", err)
			}
			if n := count(); n != 2 {
				t.Fatalf("%d rows, want 2: a's Rollback took b's transaction", n)
			}
		})
	}

	// Elsewhere each session's own connection answers for itself.
	d, err := Open(ctx, sqliteFile(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s, _ := d.Session(ctx)
	defer s.Close()
	if err := s.Rollback(ctx); err == nil || errors.Is(err, ErrNoTransaction) {
		t.Fatalf("Rollback with none open on a file's connection: %v, want the server's error", err)
	}
	if state, owner := d.SharedTx(); state != TxNone || owner != nil {
		t.Fatalf("SharedTx of a pool of many: %v, %v", state, owner)
	}
}

// Closing a session closes its connection rather than pool it, where the
// next session would inherit its settings and its transaction; the one
// connection a pool of one shares stays.
func TestCloseDiscardsConnection(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, sqliteFile(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.SQL.Exec("CREATE TABLE t (a INT)")
	s, _ := d.Session(ctx)
	for _, q := range []string{"PRAGMA foreign_keys = 0", "/* why */ BEGIN", "INSERT INTO t VALUES (1)"} {
		if _, err := s.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	s.Close()
	if n := d.SQL.Stats().OpenConnections; n != 0 {
		t.Fatalf("%d connections open after Close: the session's went back to the pool", n)
	}
	s2, _ := d.Session(ctx)
	defer s2.Close()
	c, err := s2.Query(ctx, "PRAGMA foreign_keys")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.Fetch(1)
	if err != nil || len(rows) != 1 || Display(rows[0][0]) != "1" {
		t.Fatalf("foreign_keys in the next session: %v %v", rows, err)
	}
	var n int
	d.SQL.QueryRow("SELECT count(*) FROM t").Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows: the closed session's transaction was committed", n)
	}

	m, err := Open(ctx, Config{Name: "m", Engine: SQLite, Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s3, _ := m.Session(ctx)
	if _, err := s3.Exec(ctx, "CREATE TABLE kept (a INT)"); err != nil {
		t.Fatal(err)
	}
	s3.Close()
	if n := m.SQL.Stats().OpenConnections; n != 1 {
		t.Fatalf("%d connections open after Close on a pool of one", n)
	}
	if _, err := m.SQL.Exec("SELECT * FROM kept"); err != nil {
		t.Fatalf("the shared in-memory database went with the session: %v", err)
	}
}

// A session owns the transaction open on its own connection; on a pool of
// one, only the session that began the shared transaction owns it.
func TestOwnsTx(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, sqliteFile(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s, _ := d.Session(ctx)
	defer s.Close()
	if s.OwnsTx() {
		t.Fatal("owns a transaction before BEGIN")
	}
	s.Begin(ctx)
	if !s.OwnsTx() {
		t.Fatal("does not own the transaction it began")
	}
	s.Rollback(ctx)
	if s.OwnsTx() {
		t.Fatal("owns a transaction after ROLLBACK")
	}

	m, err := Open(ctx, Config{Name: "m", Engine: SQLite, Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	a, _ := m.Session(ctx)
	b, _ := m.Session(ctx)
	defer a.Close()
	defer b.Close()
	if err := a.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if !a.OwnsTx() || b.OwnsTx() || b.Tx() != TxOpen {
		t.Fatalf("shared transaction: a owns %v, b owns %v, b sees %v", a.OwnsTx(), b.OwnsTx(), b.Tx())
	}
	a.Rollback(ctx)
	if a.OwnsTx() || b.OwnsTx() {
		t.Fatal("a transaction owned after ROLLBACK")
	}
}
