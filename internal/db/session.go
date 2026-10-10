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
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
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

	mu     sync.Mutex
	conn   *sql.Conn // nil for a pool of one connection, which q is then
	q      runner
	cursor *Cursor
	// verbs follows the transaction from the statements, for when the
	// driver cannot tell (readTxState).
	verbs   txByVerbs
	mysqlID int64
	closed  bool
	// ran is set once a statement ran on the connection, which may then
	// hold what it set; resetPending, once such a connection was lost:
	// the next statement is refused (ErrSessionReset).
	ran          bool
	resetPending bool
}

// ErrSessionClosed is the error of statements sent to a closed session:
// it never reconnects, which would run them outside the transaction the
// session had.
var ErrSessionClosed = errors.New("the session is closed")

// ErrSessionReset is returned once, instead of running the statement, by
// the first statement after the session's connection was lost and made
// again, unless the session had run nothing: a statement relying on what
// the old connection had set would otherwise run elsewhere, as an INSERT
// in the default schema.
var ErrSessionReset = errors.New("the connection was lost and made again: USE, search_path, role, variables and temporary tables were reset; run the statement again to run it on the new connection")

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
		s.q, s.verbs = s.db.SQL, txByVerbs{}
		return nil
	}
	conn, err := s.db.SQL.Conn(ctx)
	if err != nil {
		return err
	}
	if s.db.Config.Engine == MySQL {
		// Remembered to cancel queries with KILL QUERY from another
		// connection (watchCancel): the driver only drops its own. 0, which
		// sends no KILL, when unread: the id of a lost connection could be
		// another's by now.
		var id int64
		if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
			id = 0
		}
		s.mysqlID = id
	}
	s.conn, s.q, s.verbs, s.ran = conn, conn, txByVerbs{}, false
	return nil
}

// Close ends the session, rolling back what it left uncommitted, and
// closes its connection rather than return it to the pool.
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
	if s.db.single {
		s.rollBackOwnSharedTx()
	} else if s.txState() != TxNone {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		s.q.ExecContext(ctx, "ROLLBACK")
		cancel()
	}
	var err error
	if s.conn != nil {
		err = discard(s.conn)
	}
	s.conn, s.q = nil, nil
	return err
}

// rollBackOwnSharedTx rolls back, as the session closes, the transaction
// of a pool of one connection when this session began it, and leaves it
// to no session. It checks again and rolls back holding the connection, so
// that no other session's statement comes between: a COMMIT and BEGIN
// there would make the ROLLBACK end another's transaction. A session that
// does not own the transaction cannot come to own it, as Close holds s.mu.
func (s *Session) rollBackOwnSharedTx() {
	if state, owner := s.db.SharedTx(); state == TxNone || owner != s {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := s.db.SQL.Conn(ctx)
	if err == nil {
		defer conn.Close()
		if state, owner := s.db.SharedTx(); state != TxNone && owner == s {
			_, err = conn.ExecContext(ctx, "ROLLBACK")
		}
	}
	s.db.txMu.Lock()
	defer s.db.txMu.Unlock()
	if s.db.txOwner == s {
		// Not rolled back, it stays open for the next statement's session
		// to own (noteStatement).
		s.db.txOwner = nil
		if err == nil {
			s.db.sharedTx = TxNone
		}
	}
}

// discard closes a session's connection for good: back in the pool, the
// next session would inherit what this one set, its schema, role,
// variables, locks, temporary tables and transaction. Returning
// driver.ErrBadConn from Raw has database/sql close it.
func discard(conn *sql.Conn) error {
	err := conn.Raw(func(any) error { return driver.ErrBadConn })
	conn.Close() // ErrConnDone: Raw closed it
	if errors.Is(err, driver.ErrBadConn) {
		return nil
	}
	return err
}

// Tx reports the session's transaction. On a pool of one connection, it is
// the transaction every session shares, whichever began it (OwnsTx).
func (s *Session) Tx() TxState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.txState()
}

// OwnsTx reports whether the open transaction is this session's: on a pool
// of one connection, whether this session began the transaction the
// others share; otherwise, whether one is open on its connection.
func (s *Session) OwnsTx() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db.single {
		state, owner := s.db.SharedTx()
		return state != TxNone && owner == s
	}
	return s.txState() != TxNone
}

// AutocommitOff reports MySQL's autocommit off on the session's
// connection, as the server reports it however it was turned off: the
// next statement that reads or writes a table begins a transaction. It
// is false on other engines, and when unknown.
func (s *Session) AutocommitOff() bool {
	if s.db.Config.Engine != MySQL {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return false
	}
	off, _ := readMySQLAutocommitOff(s.conn)
	return off
}

// SharedTx reports the transaction of a pool of one connection, which its
// sessions share, and the session that began it, as the last statement
// on the connection left them; TxNone and nil on other pools. It never
// waits for a running statement.
func (d *DB) SharedTx() (state TxState, owner *Session) {
	if !d.single {
		return TxNone, nil
	}
	d.txMu.Lock()
	defer d.txMu.Unlock()
	return d.sharedTx, d.txOwner
}

// txState reads the transaction from the driver, whatever the user typed;
// a pool of one connection keeps what its last statement left
// (noteStatement), as reading DuckDB's takes statements.
func (s *Session) txState() TxState {
	if s.db.single {
		s.db.txMu.Lock()
		defer s.db.txMu.Unlock()
		return s.db.sharedTx
	}
	if s.conn == nil {
		return TxNone
	}
	if state, ok := readTxState(context.Background(), s.conn); ok {
		return state
	}
	return s.verbs.tx
}

// Begin opens a transaction. On a pool of one connection, while a
// transaction another session began is open, it is refused (ErrNotOwner)
// and nothing is sent, as ExecTransactionControl refuses one.
func (s *Session) Begin(ctx context.Context) error {
	_, err := s.exec(ctx, beginStatement(s.db.Config.Engine), s.mayControlSharedTx)
	return err
}

// ErrTxFailed is the error of a commit of a transaction an error aborted:
// PostgreSQL answers such a COMMIT with a rollback, and no error.
var ErrTxFailed = errors.New("the transaction failed before the commit and was rolled back: nothing was committed")

// ErrNotOwner is the error of Commit and Rollback on a pool of one
// connection whose transaction another session began: only that session
// ends it, and nothing is sent.
var ErrNotOwner = errors.New("another editor began the transaction open on this connection, which its editors share: only that editor can commit or roll it back")

// ErrNoTransaction is the error of Commit and Rollback on a pool of one
// connection with no transaction open, as after another session's
// COMMIT: nothing is sent.
var ErrNoTransaction = errors.New("no transaction is open: it was already committed or rolled back")

// Commit commits the open transaction; a failed one is rolled back
// instead (ErrTxFailed). On a pool of one connection, only the session
// that began the transaction ends it (ErrNotOwner, ErrNoTransaction).
func (s *Session) Commit(ctx context.Context) error {
	if s.Tx() == TxFailed {
		if _, err := s.exec(ctx, "ROLLBACK", s.mayEndSharedTx); errors.Is(err, ErrNotOwner) || errors.Is(err, ErrNoTransaction) {
			return err
		}
		return ErrTxFailed
	}
	_, err := s.exec(ctx, "COMMIT", s.mayEndSharedTx)
	return err
}

// Rollback rolls the open transaction back; on a pool of one connection,
// only the session that began it does (ErrNotOwner, ErrNoTransaction).
func (s *Session) Rollback(ctx context.Context) error {
	_, err := s.exec(ctx, "ROLLBACK", s.mayEndSharedTx)
	return err
}

// mayControlSharedTx refuses a statement that begins or ends a
// transaction on a pool of one connection while another session's is
// open.
func (s *Session) mayControlSharedTx() error {
	s.db.txMu.Lock()
	defer s.db.txMu.Unlock()
	if s.db.sharedTx != TxNone && s.db.txOwner != nil && s.db.txOwner != s {
		return ErrNotOwner
	}
	return nil
}

// mayEndSharedTx refuses to end the transaction of a pool of one
// connection when another session began it or none is open.
func (s *Session) mayEndSharedTx() error {
	if state, _ := s.db.SharedTx(); state == TxNone {
		return ErrNoTransaction
	}
	return s.mayControlSharedTx()
}

// noteStatement follows the transaction after a statement, err being its
// error. On a pool of one connection it reads it on shared, the connection
// the statement ran on, before another session's statement can come
// between; elsewhere txState reads it whenever asked, and the statements'
// verbs are followed only when the driver cannot tell.
func (s *Session) noteStatement(query string, err error, shared *sql.Conn) {
	e := s.db.Config.Engine
	switch e {
	case Postgres, ClickHouse, Redis:
		return // PostgreSQL's is read whenever asked; the others keep none
	}
	if shared != nil {
		// DuckDB's is read with statements (duckTxState).
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.db.txMu.Lock()
		lastTxID := s.db.lastTxID
		s.db.txMu.Unlock()
		state, ok := readTxStateWithLastID(ctx, shared, &lastTxID)
		s.db.txMu.Lock()
		defer s.db.txMu.Unlock()
		s.db.lastTxID = lastTxID
		if !ok {
			state = txByVerbs{tx: s.db.sharedTx}.after(e, query, err, nil).tx
		}
		s.db.sharedTx = state
		switch {
		case state == TxNone:
			s.db.txOwner = nil
		case s.db.txOwner == nil:
			s.db.txOwner = s // who began it
		}
		return
	}
	if s.conn == nil {
		return // lost, and its transaction with it
	}
	// Only DuckDB's reading takes a statement, and its pool is of one.
	if _, ok := readTxState(context.Background(), s.conn); !ok {
		s.verbs = s.verbs.after(e, query, err, s.rollsBackOnTimeout)
	}
}

// rollsBackOnTimeout reports MySQL's innodb_rollback_on_timeout, under
// which a lock wait timeout rolls back the whole transaction; false when
// unknown, which keeps the transaction shown.
func (s *Session) rollsBackOnTimeout() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var on bool
	return s.conn.QueryRowContext(ctx, "SELECT @@innodb_rollback_on_timeout").Scan(&on) == nil && on
}

// sharedConn takes the one connection of a pool of one for a statement,
// nil elsewhere, so that the statement and the reading of the transaction
// it left run with no other session's statement between them.
func (s *Session) sharedConn(ctx context.Context) (*sql.Conn, error) {
	if !s.db.single {
		return nil, nil
	}
	return s.db.SQL.Conn(ctx)
}

// Exec runs a statement that returns no rows, and reports how many rows it
// changed, -1 when the driver does not say.
func (s *Session) Exec(ctx context.Context, query string, args ...any) (int64, error) {
	return s.exec(ctx, query, nil, args...)
}

// ExecTransactionControl runs a statement typed to begin or end a
// transaction, as BEGIN or COMMIT. On a pool of one connection, while a
// transaction another session began is open, it is refused (ErrNotOwner)
// and nothing is sent: only that session ends it, and a BEGIN would fail
// and, on DuckDB, abort it.
func (s *Session) ExecTransactionControl(ctx context.Context, query string) (int64, error) {
	return s.exec(ctx, query, s.mayControlSharedTx)
}

// exec is Exec. On a pool of one connection, check, when set, refuses the
// statement once it holds the connection, so that no other session's
// statement comes between (mayEndSharedTx, mayControlSharedTx).
func (s *Session) exec(ctx context.Context, query string, check func() error, args ...any) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(ctx); err != nil {
		return 0, err
	}
	ctx, args, err := withServerParams(ctx, s.db.Config.Engine, args)
	if err != nil {
		return 0, err
	}
	shared, err := s.sharedConn(ctx)
	if err != nil {
		return 0, err
	}
	if shared != nil {
		defer shared.Close()
		if check != nil {
			if err := check(); err != nil {
				return 0, err
			}
		}
	}
	wasTx := s.txState()
	// Read s.conn and s.q on each call: a retry replaces them.
	run := func() (int64, *cancelWatch, error) {
		ctx, watch := s.watchCancel(ctx)
		defer watch.stop()
		if s.db.Config.Engine == Postgres && len(args) == 0 && s.conn != nil {
			n, err := execExtended(ctx, s.conn, query)
			return n, watch, err
		}
		q := s.q
		if shared != nil {
			q = shared
		}
		res, err := q.ExecContext(ctx, query, args...)
		if err != nil {
			return 0, watch, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return -1, watch, nil
		}
		return n, watch, nil
	}
	n, watch, err := run()
	if err != nil && s.retry(ctx, err, wasTx) {
		if err := s.takeReset(); err != nil {
			return 0, err
		}
		n, watch, err = run()
	}
	if err != nil {
		// Said cancelled only once afterError read the server's error: a
		// deadline's reads as a network error, which drops the connection.
		// A statement that ended without an error after its cancel ran,
		// and is reported as it ran.
		err = watch.cancelled(s.afterError(err, wasTx))
	}
	s.ran = s.ran || s.conn != nil
	s.noteStatement(query, err, shared)
	if err != nil {
		return 0, err
	}
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
	shared, err := s.sharedConn(ctx)
	if err != nil {
		return nil, err
	}
	if shared != nil {
		defer shared.Close()
	}
	wasTx := s.txState()
	// Read s.q on each call: a retry replaces it. The watch goes on with
	// the rows, until the cursor lets them go.
	run := func() (*sql.Rows, *cancelWatch, error) {
		ctx, watch := s.watchCancel(ctx)
		q := s.q
		if shared != nil {
			q = shared
		}
		rows, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			watch.stop()
		}
		return rows, watch, err
	}
	rows, watch, err := run()
	if err != nil && s.retry(ctx, err, wasTx) {
		if err := s.takeReset(); err != nil {
			return nil, err
		}
		rows, watch, err = run()
	}
	if err != nil {
		err = watch.cancelled(s.afterError(err, wasTx)) // as in exec
		s.ran = s.ran || s.conn != nil
		s.noteStatement(query, err, shared)
		return nil, err
	}
	c, err := newCursor(rows, watch, s.db.Config.Engine == SQLite)
	if err == nil {
		if err = watch.ranPastCancel(); err != nil {
			c.Close() // its rows go unread
		}
	}
	if err == nil && shared != nil {
		// The one connection is everyone's: read the rows now rather
		// than hold it while the user looks at the first page.
		err = c.buffer(MaxRows)
	}
	// Rows still to come count once the next statement finds them read
	// without an error (ensureConnection): the statement may yet end with
	// its connection.
	s.ran = s.ran || s.conn != nil && (err != nil || !c.HoldsConnection())
	s.noteStatement(query, err, shared)
	if err != nil {
		return nil, err
	}
	if shared == nil {
		s.cursor = c
	}
	return c, nil
}

// CurrentSchema is the schema the session's unqualified names resolve in,
// as its own statements (SET search_path, USE) left it. On a connection
// made again, it is the new one's, read without refusing (ErrSessionReset).
func (s *Session) CurrentSchema(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureConnection(ctx); err != nil {
		return "", err
	}
	return s.db.Dialect.CurrentSchema(ctx, s.q)
}

// ready makes the session ready for a statement (ensureConnection), and
// refuses the statement once when the connection was made again after
// statements had run on the lost one.
func (s *Session) ready(ctx context.Context) error {
	if err := s.ensureConnection(ctx); err != nil {
		return err
	}
	return s.takeReset()
}

// ensureConnection closes the last cursor and reconnects a connection that
// went away while no transaction was open.
func (s *Session) ensureConnection(ctx context.Context) error {
	if s.closed {
		return ErrSessionClosed
	}
	if s.cursor != nil && !s.cursor.failed() && s.conn != nil {
		s.ran = true // the last query's rows came without an error
	}
	if s.cursor != nil && s.cursor.failed() && s.conn != nil {
		// A query that failed while its rows were read, as one cancelled,
		// can leave the connection unusable. A ping also refreshes the
		// transaction flags MySQL's driver keeps.
		wasTx := s.txState()
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.conn.PingContext(pctx)
		cancel()
		if err != nil {
			s.closeCursor()
			s.drop()
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

// takeReset returns ErrSessionReset once after a connection was lost
// under statements that had run on it (drop).
func (s *Session) takeReset() error {
	if !s.resetPending {
		return nil
	}
	s.resetPending = false
	return ErrSessionReset
}

// retry reconnects after an error that database/sql guarantees came
// before the statement reached the server, unless a transaction was open:
// running the statement again is then safe, unless statements had run on
// the lost connection, which the caller then refuses (takeReset).
func (s *Session) retry(ctx context.Context, err error, wasTx TxState) bool {
	if s.conn == nil || !errors.Is(err, driver.ErrBadConn) || wasTx != TxNone {
		return false
	}
	s.drop()
	return s.connect(ctx) == nil
}

// drop closes a connection that was lost. What statements had set on it
// went with it, which the next statement is told once (ErrSessionReset).
func (s *Session) drop() {
	discard(s.conn)
	s.conn, s.q, s.verbs = nil, nil, txByVerbs{}
	s.resetPending = s.resetPending || s.ran
	s.ran = false
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
	if s.conn == nil {
		return err
	}
	lost := errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) || errors.Is(err, mysql.ErrInvalidConn) ||
		isNetError(err) || endsConnection(err)
	if !lost && s.db.Config.Engine == MySQL {
		// The driver keeps the transaction's flags from OK packets, not
		// errors: a ping refreshes them, as after a deadlock, which rolled
		// the transaction back, or a failed CREATE TABLE, which committed
		// it.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		lost = s.conn.PingContext(ctx) != nil
		cancel()
	}
	if !lost {
		return err
	}
	s.drop()
	if wasTx != TxNone {
		return fmt.Errorf("%w (%w)", ErrTxLost, err)
	}
	return err
}

// killGrace is how long a cancelled MySQL statement has, from the end of
// its context, to end after the KILL QUERY sent for it, before the driver
// drops its connection; a var that tests shorten.
var killGrace = 5 * time.Second

// cancelWatch follows the context of a MySQL statement, which the driver
// runs on a context of its own: MySQL's driver closes the connection when
// its context ends, and the session's transaction, variables, USE and
// temporary tables go with it. When the caller's context ends, the watch
// sends KILL QUERY, which stops the statement and keeps the connection,
// and ends the driver's context, as the caller's would have, only when the
// KILL could not be sent or the statement has not ended killGrace after
// the cancel. On other engines, and when no KILL can be sent, the driver
// runs on the caller's context, and the watch does nothing.
type cancelWatch struct {
	caller context.Context
	// cancelDriver ends the driver's context; nil when the driver runs on
	// the caller's.
	cancelDriver context.CancelFunc
	stopped      context.Context // ended by stop
	stopWatching context.CancelFunc
	exited       chan struct{} // closed as the watching goroutine returns
	once         sync.Once
	// fired is set when the caller's context ended before stop.
	fired atomic.Bool
}

// watchCancel returns the context the driver runs a statement on, and the
// watch that stops it when ctx ends (cancelWatch); the watch's stop must
// be called once the statement, and its rows, ended.
func (s *Session) watchCancel(ctx context.Context) (context.Context, *cancelWatch) {
	w := &cancelWatch{caller: ctx}
	// An ended context is left to the driver, which then sends nothing.
	if s.db.Config.Engine != MySQL || s.db.mysqlConnector == nil || s.conn == nil || s.mysqlID == 0 ||
		ctx.Done() == nil || ctx.Err() != nil {
		return ctx, w
	}
	driverCtx, cancelDriver := context.WithCancel(context.WithoutCancel(ctx))
	w.cancelDriver = cancelDriver
	w.stopped, w.stopWatching = context.WithCancel(context.Background())
	w.exited = make(chan struct{})
	id, grace := s.mysqlID, killGrace
	go func() {
		defer close(w.exited)
		select {
		case <-ctx.Done():
		case <-w.stopped.Done():
			return
		}
		w.fired.Store(true)
		deadline := time.Now().Add(grace)
		if err := s.db.killQuery(w.stopped, deadline, id); err != nil {
			cancelDriver()
			return
		}
		t := time.NewTimer(time.Until(deadline))
		defer t.Stop()
		select {
		case <-t.C:
			cancelDriver()
		case <-w.stopped.Done():
		}
	}()
	return driverCtx, w
}

// detached reports a driver running on a context of its own.
func (w *cancelWatch) detached() bool { return w.cancelDriver != nil }

// stop ends the watch once the statement and its rows ended. It waits for
// a KILL on its way, so that none reaches the session's next statement.
func (w *cancelWatch) stop() {
	if !w.detached() {
		return
	}
	w.once.Do(func() {
		w.stopWatching()
		<-w.exited
		w.cancelDriver()
	})
}

// cancelled is err, the error of a statement whose watch stopped, saying
// that the statement was cancelled when the caller's context ended before
// it did; nil stays nil.
func (w *cancelWatch) cancelled(err error) error {
	if err == nil || !w.fired.Load() || errors.Is(err, w.caller.Err()) {
		return err
	}
	return &cancelledError{cause: w.caller.Err(), err: err}
}

// ranPastCancel is the error of a statement, or of its rows, that ended
// without one after the caller's context did, as a SELECT SLEEP the KILL
// interrupted: reported cancelled, its rows unread. Before the watch
// stops, that is once the caller's context ended; after, once the watch
// saw it end. Nil otherwise.
func (w *cancelWatch) ranPastCancel() error {
	if !w.detached() {
		return nil
	}
	if w.stopped.Err() != nil && !w.fired.Load() || w.caller.Err() == nil {
		return nil
	}
	return &cancelledError{cause: w.caller.Err()}
}

// cancelledError is the error of a MySQL statement stopped once its
// context ended: the context's error, and the server's, as 1317 "Query
// execution was interrupted", or none, when the statement ended without
// one and its rows were not read.
type cancelledError struct {
	cause error // context.Canceled or context.DeadlineExceeded
	err   error
}

func (e *cancelledError) Error() string {
	word := "cancelled"
	if errors.Is(e.cause, context.DeadlineExceeded) {
		word = "timed out"
	}
	if e.err == nil {
		return word + " before its rows were read"
	}
	return word + ": " + e.err.Error()
}

func (e *cancelledError) Unwrap() []error {
	if e.err == nil {
		return []error{e.cause}
	}
	return []error{e.cause, e.err}
}

// killQuery sends KILL QUERY for the MySQL connection id, from a connection
// of its own outside the pool, whose connections the sessions may all
// hold. stopped, the end of the statement, abandons it while it connects;
// once connected, the KILL waits for its answer, until deadline: left on
// its way, it could stop the session's next statement.
func (d *DB) killQuery(stopped context.Context, deadline time.Time, id int64) error {
	ctx, cancel := context.WithDeadline(stopped, deadline)
	defer cancel()
	conn, err := d.mysqlConnector.Connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	exec, ok := conn.(driver.ExecerContext)
	if !ok {
		return errors.New("the MySQL driver cannot run a statement")
	}
	kctx, kcancel := context.WithDeadline(context.Background(), deadline)
	defer kcancel()
	// id is CONNECTION_ID() of the session's own connection: behind a proxy
	// that shares server connections, it may not name the server's.
	_, err = exec.ExecContext(kctx, fmt.Sprintf("KILL QUERY %d", id), nil)
	return err
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
	watch     *cancelWatch
	err       error
	// finished is set once the cursor let its connection go, read
	// without the lock, which a page being read holds.
	finished atomic.Bool
}

// newCursor reads rows. bytesAreBinary is set for a driver whose []byte
// values are binary whatever their column declares, as SQLite's, which
// keeps each value's own type.
func newCursor(rows *sql.Rows, watch *cancelWatch, bytesAreBinary bool) (*Cursor, error) {
	types, err := rows.ColumnTypes()
	if err != nil {
		rows.Close()
		watch.stop()
		return nil, watch.cancelled(err)
	}
	c := &Cursor{rows: rows, watch: watch, binary: make([]bool, len(types))}
	for i, t := range types {
		name := strings.ToUpper(t.DatabaseTypeName())
		c.Columns = append(c.Columns, ColumnInfo{Name: t.Name(), Type: name})
		c.binary[i] = bytesAreBinary || isBinaryType(name)
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
			c.err = c.watch.cancelled(c.err)
			if c.err == nil {
				c.err = c.watch.ranPastCancel()
			}
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
			c.err = c.watch.cancelled(c.err)
			return out, c.err
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
	c.watch.stop()
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
