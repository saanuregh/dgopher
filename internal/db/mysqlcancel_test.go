package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// mysqlCancelTable makes a table of the MySQL fixture for the cancel tests,
// with row 9 committed, and drops it after the test.
func mysqlCancelTable(t *testing.T, d *DB) {
	t.Helper()
	for _, q := range []string{
		"DROP TABLE IF EXISTS zz_cancel",
		"CREATE TABLE zz_cancel (id INT PRIMARY KEY, v INT)",
		"INSERT INTO zz_cancel VALUES (9, 0)",
	} {
		if _, err := d.SQL.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() { d.SQL.Exec("DROP TABLE IF EXISTS zz_cancel") })
}

// cancelAt returns a context its caller's cancel ends after d, as Esc
// would, not by a deadline.
func cancelAt(t *testing.T, d time.Duration) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(d, cancel)
	t.Cleanup(func() { timer.Stop(); cancel() })
	return ctx
}

// queryErr runs a query and reads its first page, and returns the error
// of either.
func queryErr(s *Session, ctx context.Context, q string) error {
	c, err := s.Query(ctx, q)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Fetch(100)
	return err
}

// zzVar reads @zz on the session, failing the test on any error, as
// ErrSessionReset.
func zzVar(t *testing.T, s *Session) any {
	t.Helper()
	c, err := s.Query(context.Background(), "SELECT @zz")
	if err != nil {
		t.Fatalf("after the cancel: %v", err)
	}
	defer c.Close()
	rows, err := c.Fetch(1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("after the cancel: %v %v", rows, err)
	}
	return rows[0][0]
}

// isCancelled reports an error that says the statement was cancelled.
func isCancelled(err error) bool {
	return errors.Is(err, context.Canceled) && strings.Contains(err.Error(), "cancelled")
}

// A cancelled MySQL statement is stopped by KILL QUERY, which keeps the
// session's connection: its transaction, variables and rows stay, and no
// statement is refused after it.
func TestIntegrationCancelKeepsSession(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d := open(t, fixtureOf(t, MySQL))
	mysqlCancelTable(t, d)
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, q := range []string{"SET @zz = 42", "BEGIN", "INSERT INTO zz_cancel VALUES (1, 1)"} {
		if _, err := s.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	// SLEEP returns 1 when killed and its statement no error: the
	// statement ran, which the Exec reports.
	start := time.Now()
	if _, err := s.Exec(cancelAt(t, 300*time.Millisecond), "DO SLEEP(5)"); err != nil {
		t.Fatalf("DO SLEEP(5) killed: %v, want the statement reported as it ended, without an error", err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("the cancel took %v: the statement was not killed", el)
	}

	// A lock wait the KILL interrupts fails with the server's 1317.
	other, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for _, q := range []string{"BEGIN", "UPDATE zz_cancel SET v = 1 WHERE id = 9"} {
		if _, err := other.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	start = time.Now()
	_, err = s.Exec(cancelAt(t, 300*time.Millisecond), "UPDATE zz_cancel SET v = 2 WHERE id = 9")
	if !isCancelled(err) || mysqlErrorNumber(err) != 1317 {
		t.Fatalf("lock wait cancelled: %v, want it cancelled with error 1317", err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("the cancel took %v", el)
	}
	// A deadline ends it the same way, though the context's error reads
	// as a network error.
	dctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	_, err = s.Exec(dctx, "UPDATE zz_cancel SET v = 2 WHERE id = 9")
	if !errors.Is(err, context.DeadlineExceeded) || mysqlErrorNumber(err) != 1317 || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("lock wait past its deadline: %v, want it timed out with error 1317", err)
	}
	other.Rollback(ctx)

	if v := zzVar(t, s); v != "42" && v != int64(42) {
		t.Fatalf("@zz = %v after the cancels, want 42", v)
	}
	if s.Tx() != TxOpen {
		t.Fatalf("transaction %v after the cancels, want it open", s.Tx())
	}
	c, err := s.Query(ctx, "SELECT COUNT(*) FROM zz_cancel WHERE id = 1")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.Fetch(1)
	c.Close()
	if err != nil || len(rows) != 1 || rows[0][0] != "1" && rows[0][0] != int64(1) {
		t.Fatalf("the session's own row: %v %v", rows, err)
	}
	if err := s.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	d.SQL.QueryRow("SELECT COUNT(*) FROM zz_cancel WHERE id = 1").Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows after the rollback", n)
	}
}

// A context that ended before the statement keeps it from running.
func TestIntegrationCancelledBeforeStart(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d := open(t, fixtureOf(t, MySQL))
	mysqlCancelTable(t, d)
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ended, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Exec(ended, "INSERT INTO zz_cancel VALUES (5, 5)"); !errors.Is(err, context.Canceled) {
		t.Fatalf("an Exec on an ended context: %v", err)
	}
	if err := queryErr(s, ended, "SELECT 1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("a Query on an ended context: %v", err)
	}
	var n int
	d.SQL.QueryRow("SELECT COUNT(*) FROM zz_cancel WHERE id = 5").Scan(&n)
	if n != 0 {
		t.Fatal("the statement ran on an ended context")
	}
}

// A SELECT the KILL interrupts ends without an error, its SLEEP returning
// 1: the query reports it cancelled, and the session stays.
func TestIntegrationCancelSleepSelect(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d := open(t, fixtureOf(t, MySQL))
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Exec(ctx, "SET @zz = 42"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := queryErr(s, cancelAt(t, 300*time.Millisecond), "SELECT SLEEP(5)"); !isCancelled(err) {
		t.Fatalf("SELECT SLEEP(5) cancelled: %v, want it reported cancelled", err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("the cancel took %v", el)
	}
	if v := zzVar(t, s); v != "42" && v != int64(42) {
		t.Fatalf("@zz = %v after the cancel, want 42", v)
	}
}

// The KILL comes from a connection outside the pool, which the sessions
// may hold whole.
func TestIntegrationCancelWithFullPool(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d := open(t, fixtureOf(t, MySQL))
	var sessions []*Session
	for range 8 {
		s, err := d.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		sessions = append(sessions, s)
	}
	s := sessions[0]
	if _, err := s.Exec(ctx, "SET @zz = 42"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := queryErr(s, cancelAt(t, 300*time.Millisecond), "SELECT SLEEP(5)"); !isCancelled(err) {
		t.Fatalf("SELECT SLEEP(5) cancelled: %v", err)
	}
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("the cancel took %v", el)
	}
	if v := zzVar(t, s); v != "42" && v != int64(42) {
		t.Fatalf("@zz = %v after the cancel, want 42", v)
	}
}

// Rows cancelled while they stream stop with the server's 1317, and the
// session keeps its transaction.
func TestIntegrationCancelWhileStreaming(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d := open(t, fixtureOf(t, MySQL))
	mysqlCancelTable(t, d)
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, q := range []string{"SET @zz = 42", "SET SESSION cte_max_recursion_depth = 30000000", "BEGIN", "INSERT INTO zz_cancel VALUES (1, 1)"} {
		if _, err := s.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	qctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c, err := s.Query(qctx, "WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 20000000) SELECT i FROM n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Fetch(10); err != nil {
		t.Fatal(err)
	}
	cancel() // Esc while the rows come
	start := time.Now()
	for !c.Done() && time.Since(start) < 10*time.Second {
		if _, err = c.Fetch(10000); err != nil {
			break
		}
	}
	if !isCancelled(err) {
		t.Fatalf("rows cancelled while read: %v, want them cancelled", err)
	}
	if v := zzVar(t, s); v != "42" && v != int64(42) {
		t.Fatalf("@zz = %v after the cancel, want 42", v)
	}
	if s.Tx() != TxOpen {
		t.Fatalf("transaction %v after the cancel, want it open", s.Tx())
	}
	if err := s.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

// When the KILL cannot be sent, or does not stop the statement in time,
// the driver drops the connection, as without the KILL: the transaction
// is lost, and the next statement refused once.
func TestIntegrationCancelFallback(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d := open(t, fixtureOf(t, MySQL))
	mysqlCancelTable(t, d)
	idle, err := d.SQL.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	var idleID int64
	if err := idle.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&idleID); err != nil {
		t.Fatal(err)
	}
	grace := killGrace
	t.Cleanup(func() { killGrace = grace })
	killGrace = 500 * time.Millisecond
	for _, c := range []struct {
		name string
		// target is the connection id the KILL goes to: none such, so
		// that the KILL fails, or another, idle, which it does not stop.
		target int64
	}{
		{"kill fails", 1 << 40},
		{"kill does not stop it", idleID},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, err := d.Session(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			for _, q := range []string{"SET @zz = 42", "BEGIN", "INSERT INTO zz_cancel VALUES (1, 1)"} {
				if _, err := s.Exec(ctx, q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			// The server goes on with the statement the dropped connection
			// ran, holding its row, until the statement ends: killed here.
			lost := s.mysqlID
			defer d.SQL.Exec(fmt.Sprintf("KILL %d", lost))
			s.mysqlID = c.target
			start := time.Now()
			_, err = s.Exec(cancelAt(t, 200*time.Millisecond), "DO SLEEP(5)")
			if !errors.Is(err, ErrTxLost) {
				t.Fatalf("got %v, want the transaction reported lost", err)
			}
			if el := time.Since(start); el > 3*time.Second {
				t.Fatalf("the fallback took %v", el)
			}
			if _, err := s.Exec(ctx, "SELECT 1"); !errors.Is(err, ErrSessionReset) {
				t.Fatalf("after the lost connection: %v, want ErrSessionReset", err)
			}
			if v := zzVar(t, s); v != nil {
				t.Fatalf("@zz = %v on the new connection, want NULL", v)
			}
			var n int
			d.SQL.QueryRow("SELECT COUNT(*) FROM zz_cancel WHERE id = 1").Scan(&n)
			if n != 0 {
				t.Fatalf("%d rows of the lost transaction", n)
			}
		})
	}
}
