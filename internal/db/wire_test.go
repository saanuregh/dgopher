package db

import (
	"cmp"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// sends collects what the hook is given for the connection named name;
// other tests' pools, which may run alongside, are left out.
type sends struct {
	mu   sync.Mutex
	list []Sent
}

func captureSends(t *testing.T, name string) *sends {
	t.Helper()
	l := &sends{}
	SetOnSend(func(cfg *Config, s Sent) {
		if cfg == nil || cfg.Name != name {
			return
		}
		l.mu.Lock()
		l.list = append(l.list, s)
		l.mu.Unlock()
	})
	t.Cleanup(func() { SetOnSend(nil) })
	return l
}

// take returns what was recorded since the last take.
func (l *sends) take() []Sent {
	l.mu.Lock()
	defer l.mu.Unlock()
	list := l.list
	l.list = nil
	return list
}

func texts(list []Sent) []string {
	var out []string
	for _, s := range list {
		out = append(out, cmp.Or(s.Text, strings.Join(s.Redis, " ")))
	}
	return out
}

// expectSent checks that exactly the statements want were recorded, in
// order, since the last take.
func expectSent(t *testing.T, l *sends, step string, want ...string) []Sent {
	t.Helper()
	got := l.take()
	if !slices.Equal(texts(got), want) {
		t.Fatalf("%s: recorded %q, want %q", step, texts(got), want)
	}
	return got
}

// wireCycle runs, on a pool opened through Open, what every engine's pool
// sends, and checks each is recorded once with the text the driver sent.
func wireCycle(t *testing.T, cfg Config, ping, begin, commit, rollback []string) {
	ctx := context.Background()
	l := captureSends(t, cfg.Name)
	d, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	expectSent(t, l, "open", ping...)

	if _, err := d.SQL.ExecContext(ctx, "CREATE TABLE t (a INTEGER PRIMARY KEY, b TEXT)"); err != nil {
		t.Fatal(err)
	}
	expectSent(t, l, "exec", "CREATE TABLE t (a INTEGER PRIMARY KEY, b TEXT)")

	if _, err := d.SQL.ExecContext(ctx, "INSERT INTO t VALUES (?, ?)", 1, "x"); err != nil {
		t.Fatal(err)
	}
	got := expectSent(t, l, "exec with args", "INSERT INTO t VALUES (?, ?)")
	if got[0].Args != 2 || got[0].Err != nil || got[0].Database != "" {
		t.Fatalf("exec with args recorded as %+v", got[0])
	}

	var b string
	if err := d.SQL.QueryRowContext(ctx, "SELECT b FROM t WHERE a = ?", 1).Scan(&b); err != nil || b != "x" {
		t.Fatal(b, err)
	}
	got = expectSent(t, l, "query", "SELECT b FROM t WHERE a = ?")
	if got[0].Args != 1 {
		t.Fatalf("query recorded as %+v", got[0])
	}

	if _, err := d.SQL.ExecContext(ctx, "SELECT nope FROM nowhere"); err == nil {
		t.Fatal("a bad statement ran")
	}
	got = expectSent(t, l, "failed exec", "SELECT nope FROM nowhere")
	if got[0].Err == nil {
		t.Fatal("the failure was not recorded")
	}

	tx, err := d.SQL.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO t VALUES (2, 'y')"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	expectSent(t, l, "commit", append(append(slices.Clone(begin), "INSERT INTO t VALUES (2, 'y')"), commit...)...)

	tx, err = d.SQL.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	expectSent(t, l, "rollback", append(slices.Clone(begin), rollback...)...)

	if err := d.SQL.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	expectSent(t, l, "ping", ping...)

	stmt, err := d.SQL.PrepareContext(ctx, "INSERT INTO t VALUES (?, ?)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stmt.ExecContext(ctx, 3, "z"); err != nil {
		t.Fatal(err)
	}
	if _, err := stmt.ExecContext(ctx, 4, "w"); err != nil {
		t.Fatal(err)
	}
	stmt.Close()
	got = expectSent(t, l, "prepared statement", "INSERT INTO t VALUES (?, ?)", "INSERT INTO t VALUES (?, ?)")
	if got[0].Args != 2 {
		t.Fatalf("prepared statement recorded as %+v", got[0])
	}

	// The transaction state is read from the driver's own connection,
	// under the wrapper.
	conn, err := d.SQL.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		t.Fatal(err)
	}
	state, ok := readTxState(ctx, conn)
	if !ok || state != TxOpen {
		t.Fatalf("transaction read through the wrapper: %v %v", state, ok)
	}
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
}

func TestWireRecordsSQLite(t *testing.T) {
	cfg := sqliteFile(t)
	cfg.Name = "wire-sqlite"
	wireCycle(t, cfg, []string{"select 1"}, []string{"begin"}, []string{"commit"}, []string{"rollback"})
}

func TestWireRecordsDuckDB(t *testing.T) {
	// DuckDB's connection has no ping: database/sql sends nothing.
	wireCycle(t, Config{Name: "wire-duck", Engine: DuckDB, Database: ":memory:"},
		nil, []string{"BEGIN TRANSACTION"}, []string{"COMMIT TRANSACTION"}, []string{"ROLLBACK"})
}

// fakeConn answers a statement with arguments with driver.ErrSkip, as
// MySQL's does, so that database/sql prepares it instead.
type fakeConn struct{ ran *[]string }

func (c fakeConn) Prepare(query string) (driver.Stmt, error) { return fakeStmt{c, query}, nil }
func (c fakeConn) PrepareContext(_ context.Context, query string) (driver.Stmt, error) {
	return fakeStmt{c, query}, nil
}
func (fakeConn) Close() error              { return nil }
func (fakeConn) Begin() (driver.Tx, error) { return nil, errors.New("no transactions") }
func (c fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if len(args) > 0 {
		return nil, driver.ErrSkip
	}
	*c.ran = append(*c.ran, query)
	return driver.RowsAffected(0), nil
}

type fakeStmt struct {
	c     fakeConn
	query string
}

func (s fakeStmt) Close() error  { return nil }
func (s fakeStmt) NumInput() int { return -1 }
func (s fakeStmt) Exec([]driver.Value) (driver.Result, error) {
	*s.c.ran = append(*s.c.ran, s.query)
	return driver.RowsAffected(1), nil
}
func (s fakeStmt) Query([]driver.Value) (driver.Rows, error) { return nil, errors.New("no rows") }

type fakeConnector struct {
	ran    *[]string
	closed bool
}

func (c *fakeConnector) Connect(context.Context) (driver.Conn, error) { return fakeConn{c.ran}, nil }
func (c *fakeConnector) Driver() driver.Driver                        { return nil }
func (c *fakeConnector) Close() error                                 { c.closed = true; return nil }

// A statement the driver skips is recorded once, when database/sql runs
// it as a prepared statement; and the pool's Close reaches the driver's
// connector, which for DuckDB releases the database.
func TestWireErrSkipNotDoubled(t *testing.T) {
	cfg := Config{Name: "wire-fake", Engine: MySQL}
	l := captureSends(t, cfg.Name)
	var ran []string
	inner := &fakeConnector{ran: &ran}
	pool := sql.OpenDB(wire{who: newSendsAs(cfg), engine: cfg.Engine}.connector(inner))
	if _, err := pool.Exec("UPDATE t SET a = ?", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec("UPDATE t SET a = 2"); err != nil {
		t.Fatal(err)
	}
	got := expectSent(t, l, "exec", "UPDATE t SET a = ?", "UPDATE t SET a = 2")
	if got[0].Args != 1 || got[1].Args != 0 {
		t.Fatalf("recorded %+v", got)
	}
	if want := []string{"UPDATE t SET a = ?", "UPDATE t SET a = 2"}; !slices.Equal(ran, want) {
		t.Fatalf("the driver ran %q, want %q", ran, want)
	}
	if _, ok := (wire{who: newSendsAs(cfg), engine: cfg.Engine}).connector(inner).(io.Closer); !ok {
		t.Fatal("the wrapper hides the connector's Close")
	}
	pool.Close()
	if !inner.closed {
		t.Fatal("closing the pool did not close the driver's connector")
	}
}

// find returns the one record of text in list.
func find(t *testing.T, list []Sent, text string) Sent {
	t.Helper()
	var found []Sent
	for _, s := range list {
		if s.Text == text {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%q recorded %d times in %q", text, len(found), texts(list))
	}
	return found[0]
}

func TestIntegrationWirePostgres(t *testing.T) {
	integration(t)
	ctx := context.Background()
	cfg := fixtureOf(t, Postgres).cfg
	cfg.Name = "wire-pg"
	l := captureSends(t, cfg.Name)
	d, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// The open's ping, and pgx's on handing out a new connection again,
	// which it counts as idle since ever.
	if got := texts(l.take()); !slices.Equal(got, []string{"-- ping", "-- ping"}) {
		t.Fatalf("on open: %q", got)
	}

	var n int
	if err := d.SQL.QueryRowContext(ctx, "SELECT 1 + $1", 1).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if s := find(t, l.take(), "SELECT 1 + $1"); s.Args != 1 || s.Took <= 0 {
		t.Fatalf("query recorded as %+v", s)
	}

	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Exec(ctx, "CREATE TEMP TABLE wire_copy (a int)"); err != nil {
		t.Fatal(err)
	}
	find(t, l.take(), "CREATE TEMP TABLE wire_copy (a int)") // execExtended
	if _, err := s.CopyFrom(ctx, "COPY wire_copy FROM STDIN", strings.NewReader("1\n2\n")); err != nil {
		t.Fatal(err)
	}
	find(t, l.take(), "COPY wire_copy FROM STDIN")

	// A connection idle for over a second is pinged as the pool hands it
	// out again.
	d.SQL.ExecContext(ctx, "SELECT 1")
	l.take()
	time.Sleep(1100 * time.Millisecond)
	d.SQL.ExecContext(ctx, "SELECT 2")
	if got := texts(l.take()); !slices.Equal(got, []string{"-- ping", "SELECT 2"}) {
		t.Fatalf("after a second idle: %q", got)
	}

	other, err := d.Database(ctx, "template1")
	if err != nil {
		t.Fatal(err)
	}
	l.take()
	other.SQL.ExecContext(ctx, "SELECT 3")
	if s := find(t, l.take(), "SELECT 3"); s.Database != "template1" {
		t.Fatalf("another database's statement recorded as %+v", s)
	}

	// A rename reaches the pool of the other database too.
	renamed := cfg
	renamed.Name = "wire-pg-renamed"
	d.RecordAs(renamed)
	l = captureSends(t, renamed.Name)
	other.SQL.ExecContext(ctx, "SELECT 4")
	d.SQL.ExecContext(ctx, "SELECT 5")
	if got := texts(l.take()); !slices.Equal(got, []string{"SELECT 4", "SELECT 5"}) {
		t.Fatalf("after RecordAs: %q", got)
	}
}

func TestIntegrationWireMySQL(t *testing.T) {
	integration(t)
	ctx := context.Background()
	cfg := fixtureOf(t, MySQL).cfg
	cfg.Name, cfg.ReadOnly = "wire-mysql", true
	l := captureSends(t, cfg.Name)
	d, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if got := texts(l.take()); len(got) < 2 || got[0] != "SET transaction_read_only = 1" || got[1] != "-- ping" {
		t.Fatalf("on open: %q", got)
	}

	// The driver skips a query with arguments, which database/sql then
	// prepares: one record.
	var id int64
	if err := d.SQL.QueryRowContext(ctx, "SELECT CONNECTION_ID() + ?", 0).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if s := find(t, l.take(), "SELECT CONNECTION_ID() + ?"); s.Args != 1 || s.Err != nil {
		t.Fatalf("query recorded as %+v", s)
	}

	d.killQuery(ctx, time.Now().Add(5*time.Second), id)
	got := l.take()
	find(t, got, "SET transaction_read_only = 1")
	find(t, got, fmt.Sprintf("KILL QUERY %d", id))
}

func TestIntegrationWireClickHouse(t *testing.T) {
	integration(t)
	ctx := context.Background()
	cfg := fixtureOf(t, ClickHouse).cfg
	cfg.Name = "wire-ch"
	l := captureSends(t, cfg.Name)
	d, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	find(t, l.take(), "-- ping")
	var n uint8
	if err := d.SQL.QueryRowContext(ctx, "SELECT 1").Scan(&n); err != nil {
		t.Fatal(err)
	}
	find(t, l.take(), "SELECT 1")
}

// badConn refuses every statement as database/sql's driver contract says
// a connection that never sent it does.
type badConn struct{ fakeConn }

func (badConn) PrepareContext(context.Context, string) (driver.Stmt, error) {
	return nil, driver.ErrBadConn
}
func (badConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return nil, driver.ErrBadConn
}
func (badConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, driver.ErrBadConn
}
func (badConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, driver.ErrBadConn
}
func (badConn) Ping(context.Context) error { return driver.ErrBadConn }

// driver.ErrBadConn means the statement was not sent, so it is not
// recorded; a ping that fails so was sent, and is.
func TestWireBadConnNotRecorded(t *testing.T) {
	cfg := Config{Name: "wire-bad", Engine: Postgres}
	l := captureSends(t, cfg.Name)
	ctx := context.Background()
	c := wire{who: newSendsAs(cfg), engine: cfg.Engine}.wrap(badConn{})
	if _, err := c.(driver.ExecerContext).ExecContext(ctx, "update t set a = 1", nil); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("exec: %v", err)
	}
	if _, err := c.(driver.QueryerContext).QueryContext(ctx, "select 1", nil); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("query: %v", err)
	}
	if _, err := c.(driver.ConnPrepareContext).PrepareContext(ctx, "select 1"); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := c.(driver.ConnBeginTx).BeginTx(ctx, driver.TxOptions{}); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("begin: %v", err)
	}
	expectSent(t, l, "refused")
	if err := c.(driver.Pinger).Ping(ctx); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("ping: %v", err)
	}
	got := expectSent(t, l, "ping", "-- ping")
	if !errors.Is(got[0].Err, driver.ErrBadConn) {
		t.Fatalf("ping recorded with %v", got[0].Err)
	}
}

// A statement sent after RecordAs is recorded as the new connection, by
// every pool that shares the sendsAs.
func TestRecordAs(t *testing.T) {
	cfg := sqliteFile(t)
	cfg.Name, cfg.ID = "record-old", "old"
	var mu sync.Mutex
	var seen []Config
	SetOnSend(func(c *Config, s Sent) {
		if c == nil || s.Text != "select 42" {
			return
		}
		mu.Lock()
		seen = append(seen, *c)
		mu.Unlock()
	})
	t.Cleanup(func() { SetOnSend(nil) })
	d, err := Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.SQL.Exec("select 42"); err != nil {
		t.Fatal(err)
	}
	renamed := cfg
	renamed.Name, renamed.ID, renamed.Env = "record-new", "new", Environment("prod")
	d.RecordAs(renamed)
	renamed.Name = "changed after the call"
	if _, err := d.SQL.Exec("select 42"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0].Name != "record-old" || seen[1].Name != "record-new" || seen[1].ID != "new" || seen[1].Env != Environment("prod") {
		t.Fatalf("recorded as %+v", seen)
	}
}
