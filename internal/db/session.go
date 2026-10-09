package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/duckdb/duckdb-go/v2"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// TxState is whether a session holds a transaction open.
type TxState int

const (
	TxNone   TxState = iota
	TxOpen           // statements wait for COMMIT or ROLLBACK
	TxFailed         // PostgreSQL: an error aborted it; only ROLLBACK helps
)

// Session is one connection of the pool kept for an editor, so that what a
// user sets (search_path, temporary tables, an open transaction) lasts
// from statement to statement, as in a terminal client.
type Session struct {
	db *DB

	mu      sync.Mutex
	conn    *sql.Conn // nil for a pool of one connection, which q is then
	q       runner
	cursor  *Cursor
	tx      TxState // tracked for engines that do not report it
	mysqlID int64
	closed  bool
}

// ErrSessionClosed is the error of statements sent to a closed session:
// it never reconnects, which would run them outside the transaction the
// session had.
var ErrSessionClosed = errors.New("the session is closed")

// Session starts a session on a connection of its own.
func (d *DB) Session(ctx context.Context) (*Session, error) {
	s := &Session{db: d}
	if err := s.connect(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// DB returns the pool the session belongs to.
func (s *Session) DB() *DB { return s.db }

// runner is what runs statements: a connection of the session's own, or
// the pool of one connection.
type runner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// MaxRows is how many rows of a result the app keeps, so that a careless
// SELECT * of a huge table cannot exhaust memory.
const MaxRows = 200_000

func (s *Session) connect(ctx context.Context) error {
	if s.db.single {
		s.q, s.tx = s.db.SQL, TxNone
		return nil
	}
	conn, err := s.db.SQL.Conn(ctx)
	if err != nil {
		return err
	}
	if s.db.Config.Engine == MySQL {
		// Remembered to cancel queries with KILL QUERY from another
		// connection: the driver only drops its own.
		var id int64
		if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err == nil {
			s.mysqlID = id
		}
	}
	s.conn, s.q, s.tx = conn, conn, TxNone
	return nil
}

// Close ends the session, rolling back what it left uncommitted.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeCursor()
	s.closed = true
	if s.q == nil {
		return nil
	}
	// Roll back what this session left open; on a pool of one connection,
	// only a transaction this session began, not another editor's.
	rollback := s.txState() != TxNone
	if s.db.single {
		s.db.txMu.Lock()
		rollback = s.db.txOwner == s
		if rollback {
			s.db.sharedTx, s.db.txOwner = TxNone, nil
		}
		s.db.txMu.Unlock()
	}
	if rollback {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		s.q.ExecContext(ctx, "ROLLBACK")
		cancel()
	}
	var err error
	if s.conn != nil {
		err = s.conn.Close()
	}
	s.conn, s.q = nil, nil
	return err
}

// Tx reports the session's transaction.
func (s *Session) Tx() TxState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.txState()
}

func (s *Session) txState() TxState {
	if s.db.single {
		s.db.txMu.Lock()
		defer s.db.txMu.Unlock()
		return s.db.sharedTx
	}
	if s.q == nil {
		return TxNone
	}
	if s.db.Config.Engine == Postgres && s.conn != nil {
		// PostgreSQL tells, whatever the user typed.
		state := s.tx
		s.conn.Raw(func(c any) error {
			if pc, ok := c.(*stdlib.Conn); ok {
				switch pc.Conn().PgConn().TxStatus() {
				case 'T':
					state = TxOpen
				case 'E':
					state = TxFailed
				default:
					state = TxNone
				}
			}
			return nil
		})
		return state
	}
	return s.tx
}

// Begin opens a transaction.
func (s *Session) Begin(ctx context.Context) error {
	_, err := s.Exec(ctx, beginStatement(s.db.Config.Engine))
	return err
}

// ErrTxFailed is the error of a commit of a transaction an error aborted:
// PostgreSQL answers such a COMMIT with a rollback, and no error.
var ErrTxFailed = errors.New("the transaction failed before the commit and was rolled back: nothing was committed")

// Commit commits the open transaction.
func (s *Session) Commit(ctx context.Context) error {
	if s.Tx() == TxFailed {
		s.Exec(ctx, "ROLLBACK")
		return ErrTxFailed
	}
	_, err := s.Exec(ctx, "COMMIT")
	return err
}

// Rollback rolls the open transaction back.
func (s *Session) Rollback(ctx context.Context) error {
	_, err := s.Exec(ctx, "ROLLBACK")
	return err
}

// NoteStatement tells the session the verb of a statement it ran, for
// engines whose transaction state it must follow itself.
func (s *Session) noteStatement(verb string) {
	switch s.db.Config.Engine {
	case Postgres, ClickHouse, Redis:
		return
	}
	tx := s.tx
	if s.db.single {
		s.db.txMu.Lock()
		defer s.db.txMu.Unlock()
		tx = s.db.sharedTx
	}
	switch verb {
	case "BEGIN", "START":
		tx = TxOpen
	case "COMMIT", "ROLLBACK", "END", "ABORT":
		tx = TxNone
	case "CREATE", "ALTER", "DROP", "TRUNCATE", "RENAME", "GRANT", "REVOKE", "LOCK", "UNLOCK":
		if s.db.Config.Engine == MySQL {
			// MySQL commits implicitly before and after these.
			tx = TxNone
		}
	}
	if !s.db.single {
		s.tx = tx
		return
	}
	s.db.sharedTx = tx
	switch {
	case tx == TxNone:
		s.db.txOwner = nil
	case s.db.txOwner == nil:
		s.db.txOwner = s // who began it
	}
}

func leadingVerb(q string) string {
	q = strings.TrimSpace(q)
	end := strings.IndexFunc(q, func(r rune) bool { return !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') })
	if end < 0 {
		end = len(q)
	}
	return strings.ToUpper(q[:end])
}

// Exec runs a statement that returns no rows, and reports how many rows it
// changed, -1 when the driver does not say.
func (s *Session) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(ctx); err != nil {
		return 0, err
	}
	ctx, args, err := withServerParams(ctx, s.db.Config.Engine, args)
	if err != nil {
		return 0, err
	}
	wasTx := s.txState()
	// Read s.conn and s.q on each call: a retry replaces them.
	run := func() (int64, error) {
		if s.db.Config.Engine == Postgres && len(args) == 0 && s.conn != nil {
			return execExtended(ctx, s.conn, query)
		}
		res, err := s.q.ExecContext(ctx, query, args...)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return -1, nil
		}
		return n, nil
	}
	stop := s.watchCancel(ctx)
	n, err := run()
	stop()
	if err != nil && s.retry(ctx, err, wasTx) {
		n, err = run()
	}
	if err != nil {
		return 0, s.afterError(err, wasTx)
	}
	s.noteStatement(leadingVerb(query))
	return n, nil
}

// Query runs a statement and returns a cursor over its rows. The cursor
// holds the session until it is closed or the next statement runs.
func (s *Session) Query(ctx context.Context, query string, args ...any) (*Cursor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	ctx, args, err := withServerParams(ctx, s.db.Config.Engine, args)
	if err != nil {
		return nil, err
	}
	wasTx := s.txState()
	stop := s.watchCancel(ctx)
	rows, err := s.q.QueryContext(ctx, query, args...)
	if err != nil && s.retry(ctx, err, wasTx) {
		rows, err = s.q.QueryContext(ctx, query, args...)
	}
	if err != nil {
		stop()
		return nil, s.afterError(err, wasTx)
	}
	s.noteStatement(leadingVerb(query))
	c, err := newCursor(rows, stop)
	if err != nil {
		return nil, err
	}
	if s.conn == nil {
		// The one connection is everyone's: read the rows now rather
		// than hold it while the user looks at the first page.
		if err := c.buffer(MaxRows); err != nil {
			return nil, err
		}
		return c, nil
	}
	s.cursor = c
	return c, nil
}

// CurrentSchema is the schema the session's unqualified names resolve in,
// as its own statements (SET search_path, USE) left it.
func (s *Session) CurrentSchema(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(ctx); err != nil {
		return "", err
	}
	return s.db.Dialect.CurrentSchema(ctx, s.q)
}

// ready closes the last cursor and reconnects a connection that went away
// while no transaction was open.
func (s *Session) ready(ctx context.Context) error {
	if s.closed {
		return ErrSessionClosed
	}
	if s.cursor != nil && s.cursor.failed() && s.conn != nil {
		// A query that failed while its rows were read, as one cancelled,
		// can leave the connection unusable.
		wasTx := s.txState()
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.conn.PingContext(pctx)
		cancel()
		if err != nil {
			s.closeCursor()
			s.conn.Close()
			s.conn, s.q, s.tx = nil, nil, TxNone
			if wasTx != TxNone {
				// The transaction went with the connection: say so, rather
				// than run this statement, a COMMIT say, on a new one.
				return fmt.Errorf("%w (%w)", ErrTxLost, err)
			}
		}
	}
	s.closeCursor()
	if s.q == nil {
		return s.connect(ctx)
	}
	return nil
}

// retry reconnects after an error that database/sql guarantees came
// before the statement reached the server, unless a transaction was open:
// running the statement again is then safe.
func (s *Session) retry(ctx context.Context, err error, wasTx TxState) bool {
	if s.conn == nil || !errors.Is(err, driver.ErrBadConn) || wasTx != TxNone {
		return false
	}
	s.conn.Close()
	s.conn, s.q = nil, nil
	return s.connect(ctx) == nil
}

func (s *Session) closeCursor() {
	if s.cursor != nil {
		s.cursor.abandon()
		s.cursor = nil
	}
}

// ErrClosedEarly is the error of a cursor closed by a later statement of
// its session before its last row was read.
var ErrClosedEarly = errors.New("the result was closed by a later statement before all its rows were read")

// ErrTxLost is wrapped in the errors of statements that lost their
// connection with a transaction open: the server rolled it back.
var ErrTxLost = errors.New("the connection was lost with a transaction open: the server rolled back its changes")

// afterError drops a connection the driver reports broken, so that the
// next statement gets a new one. An open transaction is lost with it,
// which the error says.
func (s *Session) afterError(err error, wasTx TxState) error {
	if s.conn != nil && (errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) || isNetError(err) || endsConnection(err)) {
		s.conn.Close()
		s.conn, s.q = nil, nil
		s.tx = TxNone
		if wasTx != TxNone {
			return fmt.Errorf("%w (%w)", ErrTxLost, err)
		}
	}
	return err
}

// watchCancel kills a MySQL query server-side when its context ends; the
// other drivers cancel on their own.
func (s *Session) watchCancel(ctx context.Context) (stop func()) {
	if s.db.Config.Engine != MySQL || s.mysqlID == 0 || ctx.Done() == nil {
		return func() {}
	}
	done := make(chan struct{})
	id := s.mysqlID
	go func() {
		select {
		case <-ctx.Done():
			kctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			s.db.SQL.ExecContext(kctx, fmt.Sprintf("KILL QUERY %d", id))
			cancel()
		case <-done:
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// ColumnInfo describes a column of a result.
type ColumnInfo struct {
	Name string
	Type string // the database's name for its type, as VARCHAR
}

// Cursor reads the rows of a query a page at a time.
type Cursor struct {
	Columns []ColumnInfo

	mu       sync.Mutex
	rows     *sql.Rows
	buffered [][]any // rows read ahead, served before rows
	binary   []bool
	done     bool
	// truncated is set when reading ahead stopped at its limit with rows
	// left.
	truncated bool
	stop      func()
	err       error
	// finished is set once the cursor let its connection go, read
	// without the lock, which a page being read holds.
	finished atomic.Bool
}

func newCursor(rows *sql.Rows, stop func()) (*Cursor, error) {
	types, err := rows.ColumnTypes()
	if err != nil {
		rows.Close()
		stop()
		return nil, err
	}
	c := &Cursor{rows: rows, stop: stop, binary: make([]bool, len(types))}
	for i, t := range types {
		name := strings.ToUpper(t.DatabaseTypeName())
		c.Columns = append(c.Columns, ColumnInfo{Name: t.Name(), Type: name})
		c.binary[i] = isBinaryType(name)
	}
	if len(types) == 0 {
		c.finish()
	}
	return c, nil
}

func isBinaryType(name string) bool {
	switch {
	case name == "BYTEA", strings.Contains(name, "BLOB"), strings.Contains(name, "BINARY"), name == "GEOMETRY", name == "BIT":
		return true
	}
	return false
}

// Fetch reads up to n more rows; it returns fewer at the end, after which
// Done reports true.
func (c *Cursor) Fetch(n int) ([][]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.buffered) > 0 {
		k := min(n, len(c.buffered))
		out := c.buffered[:k:k]
		c.buffered = c.buffered[k:]
		return out, nil
	}
	if c.done {
		return nil, c.err
	}
	return c.read(n)
}

// buffer reads up to n rows ahead and lets the connection go.
func (c *Cursor) buffer(n int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	rows, err := c.read(n + 1)
	if len(rows) > n {
		rows, c.truncated = rows[:n], true
	}
	c.buffered = rows
	c.finish()
	return err
}

func (c *Cursor) read(n int) ([][]any, error) {
	out := make([][]any, 0, min(n, 1024))
	cols := len(c.Columns)
	for len(out) < n {
		if !c.rows.Next() {
			c.err = c.rows.Err()
			c.finish()
			return out, c.err
		}
		vals := make([]any, cols)
		ptrs := make([]any, cols)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := c.rows.Scan(ptrs...); err != nil {
			c.err = err
			c.finish()
			return out, err
		}
		for i, v := range vals {
			vals[i] = duckValue(c.Columns[i].Type, v)
			v = vals[i]
			// MySQL's text protocol sends every value as bytes: text is
			// text unless the column holds binary data.
			if b, ok := v.([]byte); ok && !c.binary[i] && utf8.Valid(b) {
				vals[i] = string(b)
			} else if ok {
				vals[i] = append([]byte(nil), b...)
			}
		}
		out = append(out, vals)
	}
	return out, nil
}

// duckValue turns what DuckDB's driver gives for a UUID (16 bytes) and a
// TIME (a time on 1 January of year 1) into their text.
func duckValue(typ string, v any) any {
	switch x := v.(type) {
	case []byte:
		if typ == "UUID" && len(x) == 16 {
			u := duckdb.UUID(x)
			return u.String()
		}
	case time.Time:
		if typ == "TIME" && x.Year() == 1 && x.YearDay() == 1 {
			return x.Format("15:04:05.999999")
		}
	}
	return v
}

func (c *Cursor) failed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err != nil
}

// Done reports whether every row has been read.
func (c *Cursor) Done() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.done && len(c.buffered) == 0
}

func (c *Cursor) finish() {
	if c.done {
		return
	}
	c.done = true
	c.rows.Close()
	c.stop()
	c.finished.Store(true)
}

// HoldsConnection reports rows still to read from the server, which keep
// the session's connection: a statement on it would end them. A cursor
// that read its rows ahead holds none.
func (c *Cursor) HoldsConnection() bool { return !c.finished.Load() }

// abandon closes the cursor for a later statement: reading it again
// reports that it was cut short.
func (c *Cursor) abandon() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.done {
		c.err = ErrClosedEarly
	}
	c.finish()
}

// Err returns why the cursor stopped early, nil when it read every row.
func (c *Cursor) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close stops reading the rows.
func (c *Cursor) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finish()
}

// endsConnection reports a PostgreSQL error after which the server closes
// the connection, such as pg_terminate_backend's.
func endsConnection(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && (pe.Severity == "FATAL" || pe.Severity == "PANIC")
}

func isNetError(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "EOF") && strings.Contains(msg, "unexpected")
}

// Truncated reports whether the rows stopped at MaxRows with more left,
// as on a database of one connection, which reads its rows ahead.
func (c *Cursor) Truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.truncated
}
