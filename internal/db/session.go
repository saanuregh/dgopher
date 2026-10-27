package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/duckdb/duckdb-go/v2"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	mssql "github.com/microsoft/go-mssqldb"
	"github.com/sijms/go-ora/v3/network"
)

// TxState is whether a session holds a transaction open.
type TxState int

const (
	TxNone   TxState = iota
	TxOpen           // statements wait for COMMIT or ROLLBACK
	TxFailed         // PostgreSQL, DuckDB: an error aborted it; only ROLLBACK helps
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
	// messages keeps what the server said besides results and errors, as
	// SQL Server's PRINT, until TakeMessages.
	messages messageLog
	// driverHolds is set while a driver that commits each statement by
	// itself (driverCommitsStatements) has been told a transaction is
	// open, so that it holds the statements in it.
	driverHolds bool
}

// messagesKey holds, in a statement's context, the messageLog of the
// session that runs it.
type messagesKey struct{}

// maxMessages is the most messages a session keeps until TakeMessages:
// a loop of PRINTs would otherwise fill memory.
const maxMessages = 1000

// messageLog keeps a session's messages, added as the driver reads them,
// and counts those past maxMessages.
type messageLog struct {
	mu      sync.Mutex
	text    []string
	dropped int
}

func (l *messageLog) add(msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.text) >= maxMessages {
		l.dropped++
		return
	}
	l.text = append(l.text, msg)
}

// addRead adds what messagesSQL read: lines each ended by a unit
// separator, then, when lines were left out, a record separator and
// their count.
func (l *messageLog) addRead(text string) {
	lines, more, _ := strings.Cut(text, "\x1e")
	for line := range strings.SplitSeq(strings.TrimSuffix(lines, unitSep), unitSep) {
		l.add(line)
	}
	if n, err := strconv.Atoi(more); err == nil && n > 0 {
		l.mu.Lock()
		l.dropped += n
		l.mu.Unlock()
	}
}

// TakeMessages returns the messages the server sent with the session's
// statements since it was last called, as SQL Server's PRINT and
// RAISERROR of a low severity, and forgets them.
func (s *Session) TakeMessages() []string {
	s.messages.mu.Lock()
	defer s.messages.mu.Unlock()
	out := s.messages.text
	if s.messages.dropped > 0 {
		out = append(out, fmt.Sprintf("%d more messages not shown.", s.messages.dropped))
	}
	s.messages.text, s.messages.dropped = nil, 0
	return out
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
	d.touch()
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
	if r, ok := SQL(s.db.Config.Engine); ok && r.cancelQuerySQL(0) != "" {
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
	if r, ok := SQL(s.db.Config.Engine); ok {
		for _, text := range r.sessionStartSQL() {
			if _, err := conn.ExecContext(ctx, text); err != nil {
				discard(conn)
				return err
			}
		}
	}
	s.conn, s.q, s.verbs, s.ran, s.driverHolds = conn, conn, txByVerbs{}, false, false
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
		if r, ok := SQL(s.db.Config.Engine); ok && r.driverCommitsStatements() && s.conn != nil {
			s.endInDriver(ctx, false)
		} else {
			s.q.ExecContext(ctx, "ROLLBACK")
		}
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
// It waits for the connection however long another statement holds it:
// Close runs off the UI thread, and closing the pool ends the wait.
// Meanwhile, and should the ROLLBACK fail, the transaction stays this
// session's, for no other session's statement to take it and its COMMIT
// to commit what this session left (noteStatement).
func (s *Session) rollBackOwnSharedTx() {
	if state, owner := s.db.SharedTx(); state == TxNone || owner != s {
		return
	}
	conn, err := s.db.SQL.Conn(context.Background())
	if err != nil {
		return // the pool is closed, and the transaction with it
	}
	defer conn.Close()
	if state, owner := s.db.SharedTx(); state == TxNone || owner != s {
		return
	}
	if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		// Unless the connection shows it ended anyway.
		s.refreshSharedTx(conn)
		return
	}
	s.db.txMu.Lock()
	defer s.db.txMu.Unlock()
	if s.db.txOwner == s {
		s.db.txOwner, s.db.sharedTx = nil, TxNone
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

// AutocommitOff reports autocommit off on the session's connection, as
// the server reports it however it was turned off: MySQL's autocommit,
// SQL Server's IMPLICIT_TRANSACTIONS. The next statement that reads or
// writes a table begins a transaction. It is false on other engines, and
// while rows still to read hold the connection; true when SQL Server's
// cannot be read.
func (s *Session) AutocommitOff() bool {
	if r, ok := SQL(s.db.Config.Engine); !ok || !r.autocommitMayBeOff() {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return false
	}
	off, _ := readAutocommitOff(s.conn, s.cursor == nil || !s.cursor.HoldsConnection())
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
	_, err := s.exec(ctx, beginStatement(s.db.Config.Engine), s.mayControlSharedTx, false)
	return err
}

// ErrTxFailed is the error of a commit of a transaction an error aborted:
// PostgreSQL and DuckDB answer such a COMMIT with a rollback, and no error.
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
// that began the transaction ends it (ErrNotOwner, ErrNoTransaction), and
// whether it failed is read on the connection before the COMMIT is sent:
// a statement outside the sessions, as an interrupted catalog read, can
// abort DuckDB's.
func (s *Session) Commit(ctx context.Context) error {
	if r, ok := SQL(s.db.Config.Engine); ok && r.driverCommitsStatements() {
		return s.endThroughDriver(ctx, true)
	}
	if s.Tx() != TxFailed {
		_, err := s.exec(ctx, "COMMIT", s.mayCommitSharedTx, false)
		if err == nil {
			err = s.commitImplicit(ctx)
		}
		if !errors.Is(err, errSharedTxFailed) {
			return err
		}
	}
	if _, err := s.exec(ctx, "ROLLBACK", s.mayEndSharedTx, false); errors.Is(err, ErrNotOwner) || errors.Is(err, ErrNoTransaction) {
		return err
	}
	return ErrTxFailed
}

// ErrStillOpen is the error of a Commit that left a transaction open, as
// one the user began around the app's: what ran in it is not committed.
var ErrStillOpen = errors.New("a transaction is still open after the commit, begun around this one: nothing is committed until it is")

// commitImplicit commits, once, the transaction the session began
// implicitly and the app's Begin nested in (implicitTransactionsSQL);
// another left open is ErrStillOpen.
func (s *Session) commitImplicit(ctx context.Context) error {
	r, ok := SQL(s.db.Config.Engine)
	if !ok || r.implicitTransactionsSQL() == "" || s.Tx() == TxNone {
		return nil
	}
	on, err := s.queryStrings(ctx, r.implicitTransactionsSQL())
	if err != nil {
		return err
	}
	if len(on) == 1 && on[0] == "1" {
		if _, err := s.exec(ctx, "COMMIT", nil, false); err != nil {
			return err
		}
	}
	if s.Tx() != TxNone {
		return ErrStillOpen
	}
	return nil
}

// errSharedTxFailed refuses the COMMIT of a failed transaction of a pool
// of one connection, which Commit then rolls back.
var errSharedTxFailed = errors.New("the shared transaction failed")

// mayCommitSharedTx is mayEndSharedTx, and refuses the COMMIT of a failed
// transaction (errSharedTxFailed), which DuckDB would answer with a
// rollback and no error.
func (s *Session) mayCommitSharedTx() error {
	if err := s.mayEndSharedTx(); err != nil {
		return err
	}
	if state, _ := s.db.SharedTx(); state == TxFailed {
		return errSharedTxFailed
	}
	return nil
}

// Rollback rolls the open transaction back; on a pool of one connection,
// only the session that began it does (ErrNotOwner, ErrNoTransaction).
func (s *Session) Rollback(ctx context.Context) error {
	if r, ok := SQL(s.db.Config.Engine); ok && r.driverCommitsStatements() {
		return s.endThroughDriver(ctx, false)
	}
	_, err := s.exec(ctx, "ROLLBACK", s.mayEndSharedTx, false)
	return err
}

// endThroughDriver commits, or rolls back, the session's transaction by
// the driver's own call, as a driver that commits each statement by
// itself must be told it ended (driverCommitsStatements): Oracle's
// READ_ONLY session refuses the COMMIT and ROLLBACK statements, not the
// calls. The transaction is read again after.
func (s *Session) endThroughDriver(ctx context.Context, commit bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(ctx); err != nil {
		return err
	}
	wasTx := s.txState()
	err := s.endInDriver(ctx, commit)
	if err != nil {
		err = s.afterError(err, wasTx)
	}
	s.ran = s.ran || s.conn != nil
	s.noteStatement(endStatement(s.db.Config.Engine, commit), err, nil)
	return err
}

// endInDriver ends the transaction by the driver's commit or rollback
// call, which the driver then follows by committing each statement again.
func (s *Session) endInDriver(ctx context.Context, commit bool) error {
	err := s.conn.Raw(func(dc any) error {
		b, ok := dc.(driver.ConnBeginTx)
		if !ok {
			return errors.New("the driver cannot end a transaction")
		}
		tx, err := b.BeginTx(ctx, driver.TxOptions{})
		if err != nil {
			return err
		}
		if commit {
			return tx.Commit()
		}
		return tx.Rollback()
	})
	s.driverHolds = false
	return err
}

// holdDriverTransaction tells a driver that commits each statement by
// itself (driverCommitsStatements) whether the server holds a transaction
// open, as read after the last statement: while one is, it must hold the
// statements in it rather than commit them; once none is, it commits each
// again. Its BeginTx sends nothing; its commit, sent once the server holds
// no transaction, ends none.
func (s *Session) holdDriverTransaction() error {
	r, ok := SQL(s.db.Config.Engine)
	if !ok || !r.driverCommitsStatements() || s.conn == nil {
		return nil
	}
	hold := s.verbs.tx != TxNone
	if hold == s.driverHolds {
		return nil
	}
	err := s.conn.Raw(func(dc any) error {
		b, ok := dc.(driver.ConnBeginTx)
		if !ok {
			return errors.New("the driver cannot begin a transaction")
		}
		tx, err := b.BeginTx(context.Background(), driver.TxOptions{})
		if err != nil || hold {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return fmt.Errorf("the driver could not follow the transaction, so the statement was not run: %w", err)
	}
	s.driverHolds = hold
	return nil
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

// followsTransactionByVerbs reports whether a session of an engine knows
// its transaction by its statements' verbs: so does an engine without
// rules, but not Redis, which keeps none.
func followsTransactionByVerbs(e Engine) bool {
	if r, ok := SQL(e); ok {
		return r.followsTransactionByVerbs()
	}
	return e.IsSQL()
}

// noteStatement follows the transaction after a statement, err being its
// error. On a pool of one connection it reads it on shared, the connection
// the statement ran on, before another session's statement can come
// between; elsewhere txState reads it whenever asked, and the statements'
// verbs are followed only when the driver cannot tell.
func (s *Session) noteStatement(query string, err error, shared *sql.Conn) {
	e := s.db.Config.Engine
	if !followsTransactionByVerbs(e) {
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
	if r, ok := SQL(e); ok && r.transactionStateSQL() != "" {
		s.verbs = s.serverTxState(r.transactionStateSQL(), query, err)
		return
	}
	// Only DuckDB's reading takes a statement, and its pool is of one.
	if _, ok := readTxState(context.Background(), s.conn); !ok {
		s.verbs = s.verbs.after(e, query, err, s.rollsBackOnTimeout)
	}
}

// serverTxState reads whether the server holds a transaction open for the
// session (transactionStateSQL), whatever the statement did; when it
// cannot be read, the statement's verbs say, keeping one open in doubt.
func (s *Session) serverTxState(query, statement string, err error) txByVerbs {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var id sql.NullString
	if rerr := s.conn.QueryRowContext(ctx, query).Scan(&id); rerr != nil {
		return s.verbs.after(s.db.Config.Engine, statement, err, s.rollsBackOnTimeout)
	}
	if id.Valid {
		return txByVerbs{tx: TxOpen}
	}
	return txByVerbs{tx: TxNone}
}

// refreshSharedTx reads again, on shared, the one connection of a pool of
// one held, a transaction recorded open on it: a statement that ran on it
// outside the sessions, as a catalog read, may have ended it or, on
// DuckDB, aborted it. With none recorded open it sends nothing. An ended
// transaction is left to no session, as noteStatement leaves it.
func (s *Session) refreshSharedTx(shared *sql.Conn) {
	s.db.txMu.Lock()
	recorded, lastTxID := s.db.sharedTx, s.db.lastTxID
	s.db.txMu.Unlock()
	if recorded == TxNone {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state, ok := readTxStateWithLastID(ctx, shared, &lastTxID)
	if !ok {
		return
	}
	s.db.txMu.Lock()
	defer s.db.txMu.Unlock()
	s.db.lastTxID, s.db.sharedTx = lastTxID, state
	if state == TxNone {
		s.db.txOwner = nil
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
	return s.exec(ctx, query, nil, true, args...)
}

// ExecChange runs a change of rows the app made, as a grid edit, and
// returns the rows it changed itself (ownRowCountSQL): not those of the
// triggers it fired.
func (s *Session) ExecChange(ctx context.Context, query string, args ...any) (int64, error) {
	r, ok := SQL(s.db.Config.Engine)
	if !ok || r.ownRowCountSQL() == "" {
		return s.exec(ctx, query, nil, false, args...)
	}
	counts, err := s.queryStrings(ctx, query+";\n"+r.ownRowCountSQL(), args...)
	if err != nil {
		return 0, err
	}
	if len(counts) != 1 {
		return 0, fmt.Errorf("the change's row count came as %d rows", len(counts))
	}
	return strconv.ParseInt(counts[0], 10, 64)
}

// ExecTransactionControl runs a statement typed to begin or end a
// transaction, as BEGIN or COMMIT. On a pool of one connection, while a
// transaction another session began is open, it is refused (ErrNotOwner)
// and nothing is sent: only that session ends it, and a BEGIN would fail
// and, on DuckDB, abort it.
//
// A COMMIT of a shared transaction that failed is not sent either: DuckDB
// would answer it with a rollback and no error. It is rolled back, as
// Commit does, and ErrTxFailed says that nothing was committed.
func (s *Session) ExecTransactionControl(ctx context.Context, query string) (int64, error) {
	words := leadingWords(s.db.Config.Engine, query)
	ends, chains := endsTransaction(words)
	commits := ends && (wordAt(words, 0) == "COMMIT" || wordAt(words, 0) == "END")
	if r, ok := SQL(s.db.Config.Engine); ok && r.driverCommitsStatements() && ends && !chains &&
		(len(words) == 1 || len(words) == 2 && words[1] == "WORK" || len(words) == 2 && words[1] == ";") {
		// A plain COMMIT or ROLLBACK is the driver's call, which ends the
		// transaction on a read-only session too, and tells the driver.
		return 0, s.endThroughDriver(ctx, commits)
	}
	n, err := s.exec(ctx, query, func() error {
		if err := s.mayControlSharedTx(); err != nil || !commits {
			return err
		}
		if state, _ := s.db.SharedTx(); state == TxFailed {
			return errSharedTxFailed
		}
		return nil
	}, true)
	if !errors.Is(err, errSharedTxFailed) {
		return n, err
	}
	if _, err := s.exec(ctx, "ROLLBACK", s.mayEndSharedTx, false); errors.Is(err, ErrNotOwner) || errors.Is(err, ErrNoTransaction) {
		return 0, err
	}
	return 0, ErrTxFailed
}

// exec is Exec. On a pool of one connection, check, when set, refuses the
// statement once it holds the connection, so that no other session's
// statement comes between (mayEndSharedTx, mayControlSharedTx). userRan
// reads what the server printed, as the app's own statements print none.
func (s *Session) exec(ctx context.Context, query string, check func() error, userRan bool, args ...any) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ready(ctx); err != nil {
		return 0, err
	}
	ctx, args, err := withServerParams(ctx, s.db.Config.Engine, args)
	if err != nil {
		return 0, err
	}
	ctx = context.WithValue(ctx, messagesKey{}, &s.messages)
	shared, err := s.sharedConn(ctx)
	if err != nil {
		return 0, err
	}
	if shared != nil {
		defer shared.Close()
		s.refreshSharedTx(shared)
		if check != nil {
			if err := check(); err != nil {
				return 0, err
			}
		}
	}
	if err := s.holdDriverTransaction(); err != nil {
		return 0, err
	}
	wasTx := s.txState()
	// Read s.conn and s.q on each call: a retry replaces them.
	run := func() (int64, *cancelWatch, error) {
		ctx, watch := s.watchCancel(ctx)
		defer watch.stop()
		if r, ok := SQL(s.db.Config.Engine); ok && r.execUnprepared() && len(args) == 0 && s.conn != nil {
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
	s.afterStatement(query, err, shared, userRan)
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
	ctx = context.WithValue(ctx, messagesKey{}, &s.messages)
	shared, err := s.sharedConn(ctx)
	if err != nil {
		return nil, err
	}
	if shared != nil {
		defer shared.Close()
		s.refreshSharedTx(shared)
	}
	if err := s.holdDriverTransaction(); err != nil {
		return nil, err
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
		s.afterStatement(query, err, shared, true)
		return nil, err
	}
	r, ok := SQL(s.db.Config.Engine)
	c, err := newCursor(rows, watch, ok && r.bytesAreBinary())
	if err == nil {
		c.batch = ok && r.readsAllResults()
	}
	if err == nil {
		c.pool = s.db
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
	s.afterStatement(query, err, shared, true)
	if err != nil {
		return nil, err
	}
	if shared == nil {
		s.cursor = c
	}
	return c, nil
}

// afterStatement records the transaction state after a statement. After
// a statement the user ran, it also reads what the server printed, in one
// query where the engine supports that. The app's own statements print
// nothing, so only the transaction is read after them. When that one query
// fails in a way that leaves the connection unusable, or runs out of time,
// the statement's verbs give the state, as reading again would fail too.
func (s *Session) afterStatement(query string, err error, shared *sql.Conn, userRan bool) {
	if userRan {
		switch s.readTxStateAndMessages(shared) {
		case combinedRead:
			return
		case combinedReadUnusable:
			s.verbs = s.verbs.after(s.db.Config.Engine, query, err, s.rollsBackOnTimeout)
			return
		}
	}
	s.noteStatement(query, err, shared)
	if userRan {
		s.readMessages()
	}
}

type combinedReadResult int

const (
	combinedReadUnsupported combinedReadResult = iota
	combinedRead
	combinedReadFailed
	combinedReadUnusable
)

// readTxStateAndMessages is noteStatement's serverTxState and
// readMessages in one query, on a connection of the session's own. Where
// the engine reads them apart, or the query failed, they are read apart,
// the messages it took being lost.
func (s *Session) readTxStateAndMessages(shared *sql.Conn) combinedReadResult {
	r, ok := SQL(s.db.Config.Engine)
	if !ok || r.transactionStateAndMessagesSQL() == "" || shared != nil || s.conn == nil {
		return combinedReadUnsupported
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var id, text sql.NullString
	if err := s.conn.QueryRowContext(ctx, r.transactionStateAndMessagesSQL()).Scan(&id, &text); err != nil {
		if endsConnection(err) || isNetError(err) || ctx.Err() != nil {
			return combinedReadUnusable
		}
		return combinedReadFailed
	}
	if text.Valid {
		s.messages.addRead(text.String)
	}
	s.verbs = txByVerbs{tx: TxNone}
	if id.Valid {
		s.verbs = txByVerbs{tx: TxOpen}
	}
	return combinedRead
}

// readMessages reads, where the engine keeps them for the session to ask
// (messagesSQL), what the server printed since the last statement: the
// statement's, and of a query those its first rows' functions printed.
// They are kept until TakeMessages; a failure to read them loses them.
func (s *Session) readMessages() {
	r, ok := SQL(s.db.Config.Engine)
	if !ok || r.messagesSQL() == "" || s.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var text sql.NullString
	if err := s.conn.QueryRowContext(ctx, r.messagesSQL()).Scan(&text); err == nil && text.Valid {
		s.messages.addRead(text.String)
	}
}

// RunExplain gets a plan on the session: run's Before steps, in order,
// then statement, which runs run's Statement and its Read, then run's
// After steps, which run even when a step or the statement failed, ctx
// ended or statement panicked, on a context of their own, so that the
// session is not left as Before set it: under SQL Server's SHOWPLAN_XML,
// statements are compiled, never run. statement is not called once a
// Before step failed. A connection lost before After took Before's
// settings with it, and gets none; one on which an After step failed is
// dropped, which the next statement is told of (ErrSessionReset). step
// is told of each step sent, for the audit log. What the statement wrote
// for its plan goes before After runs (discardExplain).
func (s *Session) RunExplain(ctx context.Context, run ExplainRun, step func(sql string, took time.Duration, err error),
	statement func(context.Context) error) (err error) {
	if len(run.Before)+len(run.After)+len(run.Discard) == 0 {
		return statement(ctx)
	}
	send := func(ctx context.Context, sql string) error {
		start := time.Now()
		_, err := s.Exec(ctx, sql)
		step(sql, time.Since(start), err)
		return err
	}
	s.mu.Lock()
	wasTx := s.txState()
	s.mu.Unlock()
	for _, sql := range run.Before {
		if err = send(ctx, sql); err != nil {
			break
		}
	}
	held := s.heldRunner()
	defer func() {
		err = errors.Join(err, s.discardExplain(ctx, run.Discard, wasTx, held, step, send), s.runAfter(ctx, run.After, held, send))
	}()
	if err != nil {
		return err
	}
	return statement(ctx)
}

// discardExplain removes what an explain's statement wrote for its plan,
// when steps are set: a transaction the session held none of before the
// run, and holds now, is rolled back (Rollback), which removes all the run
// wrote; inside one held before, the steps run, as After's do. A failed
// rollback drops the connection, as a failed After step does: it would
// hold a transaction of the app's own.
func (s *Session) discardExplain(ctx context.Context, steps []string, wasTx TxState, held runner,
	step func(string, time.Duration, error), send func(context.Context, string) error) error {
	if len(steps) == 0 || held == nil {
		return nil
	}
	if wasTx != TxNone {
		return s.runAfter(ctx, steps, held, send)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), AfterStepsWait)
	defer cancel()
	s.mu.Lock()
	open := s.q == held && s.txState() != TxNone
	s.mu.Unlock()
	if !open {
		return nil
	}
	start := time.Now()
	err := s.Rollback(ctx)
	step(endStatement(s.db.Config.Engine, false), time.Since(start), err)
	if err != nil {
		s.mu.Lock()
		if s.q == held && s.conn != nil {
			err = errors.Join(err, s.drop())
		}
		s.mu.Unlock()
	}
	return err
}

// heldRunner is what the session runs statements on now, nil once its
// connection was lost.
func (s *Session) heldRunner() runner {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.q
}

// runAfter sends an explain's After steps on held, the connection Before
// set, while the session still holds it, and drops it when one fails.
func (s *Session) runAfter(ctx context.Context, after []string, held runner, send func(context.Context, string) error) error {
	if held == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), AfterStepsWait)
	defer cancel()
	var errs []error
	for _, sql := range after {
		// Sent on a connection made again, it would take the session's
		// ErrSessionReset, and on a new connection do nothing.
		if s.heldRunner() != held {
			break
		}
		if err := send(ctx, sql); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		s.mu.Lock()
		if s.q == held && s.conn != nil {
			errs = append(errs, s.drop())
		}
		s.mu.Unlock()
	}
	return errors.Join(errs...)
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

// SessionDatabase is the database the session works in, read on the
// server where a statement can move it to another (sessionDatabaseSQL);
// ok is false where none can.
func (s *Session) SessionDatabase(ctx context.Context) (name string, ok bool, err error) {
	r, isSQL := SQL(s.db.Config.Engine)
	if !isSQL || r.sessionDatabaseSQL() == "" {
		return "", false, nil
	}
	names, err := s.queryStrings(ctx, r.sessionDatabaseSQL())
	if err != nil || len(names) != 1 {
		return "", true, err
	}
	return names[0], true, nil
}

// ready makes the session ready for a statement (ensureConnection), and
// refuses the statement once when the connection was made again after
// statements had run on the lost one.
func (s *Session) ready(ctx context.Context) error {
	s.db.touch()
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
			if dropErr := s.drop(); dropErr != nil {
				return fmt.Errorf("%w (%w)", dropErr, err)
			}
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

// drop closes a connection that was lost, or that the session gives up
// on. What statements had set on it went with it, which the next statement
// is told once (ErrSessionReset). A driver that commits each statement by
// itself (driverCommitsStatements) commits an open transaction as it
// closes the connection, as go-ora's logoff does, so the transaction is
// rolled back first; when that rollback failed, or ran out of time, on a
// connection not gone, the error is ErrTxMayBeCommitted.
func (s *Session) drop() error {
	var err error
	if r, ok := SQL(s.db.Config.Engine); ok && r.driverCommitsStatements() && s.conn != nil && s.txState() != TxNone {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var ne net.Error
		if rerr := s.endInDriver(ctx, false); rerr != nil && (!connectionLost(rerr) || errors.As(rerr, &ne) && ne.Timeout()) {
			err = fmt.Errorf("%w (%w)", ErrTxMayBeCommitted, rerr)
		}
		cancel()
	}
	discard(s.conn)
	s.conn, s.q, s.verbs, s.driverHolds = nil, nil, txByVerbs{}, false
	s.resetPending = s.resetPending || s.ran
	s.ran = false
	return err
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

// ErrTxMayBeCommitted is wrapped in the errors of statements whose
// connection the session dropped with a transaction open after the
// rollback it sent first failed (drop): closing the connection may have
// committed the transaction. It is ErrTxLost too (errors.Is), as the
// transaction is gone from the session either way.
var ErrTxMayBeCommitted error = txLostError("the connection was dropped with a transaction open, and its rollback failed: closing the connection may have committed the transaction's changes")

type txLostError string

func (e txLostError) Error() string        { return string(e) }
func (e txLostError) Is(target error) bool { return target == ErrTxLost }

// connectionLost reports an error after which the connection is gone.
func connectionLost(err error) bool {
	return errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) || errors.Is(err, mysql.ErrInvalidConn) ||
		isNetError(err) || endsConnection(err)
}

// afterError drops a connection the driver reports broken, so that the
// next statement gets a new one. An open transaction is lost with it,
// which the error says.
func (s *Session) afterError(err error, wasTx TxState) error {
	if s.conn == nil {
		return err
	}
	lost := connectionLost(err)
	r, ok := SQL(s.db.Config.Engine)
	if !lost && ok && r.execUnprepared() {
		// pgx's connection, which PostgreSQL's family runs on, may have
		// closed with an ordinary error.
		lost = pgConnClosed(s.conn)
	}
	if !lost && ok && r.pingAfterError() {
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
	if dropErr := s.drop(); dropErr != nil {
		return fmt.Errorf("%w (%w)", dropErr, err)
	}
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
	if r, ok := SQL(s.db.Config.Engine); !ok || r.cancelQuerySQL(s.mysqlID) == "" || s.db.mysqlConnector == nil || s.conn == nil || s.mysqlID == 0 ||
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
	r, ok := SQL(d.Config.Engine)
	if !ok || r.cancelQuerySQL(id) == "" {
		return fmt.Errorf("%s cancels no query from another connection", d.Config.Engine.Label())
	}
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
	_, err = exec.ExecContext(kctx, r.cancelQuerySQL(id), nil)
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
	// pool is the pool whose use a page read counts as (DB.LastUsed).
	pool *DB
	// batch: more results may follow the cursor's in its statement, as
	// those of a T-SQL batch, whose errors are the cursor's too.
	batch bool
}

// newCursor reads rows. bytesAreBinary is set for a driver whose []byte
// values are binary whatever their column declares, as SQLite's, which
// keeps each value's own type.
func newCursor(rows *sql.Rows, watch *cancelWatch, bytesAreBinary bool) (*Cursor, error) {
	c := &Cursor{rows: rows, watch: watch}
	if err := c.readColumns(bytesAreBinary); err != nil {
		rows.Close()
		watch.stop()
		return nil, watch.cancelled(err)
	}
	if len(c.Columns) == 0 {
		c.finish()
	}
	return c, nil
}

// readColumns reads the columns of the rows' current result.
func (c *Cursor) readColumns(bytesAreBinary bool) error {
	types, err := c.rows.ColumnTypes()
	if err != nil {
		return err
	}
	c.Columns, c.binary = nil, make([]bool, len(types))
	for i, t := range types {
		name := strings.ToUpper(t.DatabaseTypeName())
		c.Columns = append(c.Columns, ColumnInfo{Name: t.Name(), Type: name})
		c.binary[i] = bytesAreBinary || isBinaryType(name)
	}
	return nil
}

// ReadAll reads the cursor's rows ahead, up to n, then each result that
// follows them in the statement, as a T-SQL batch's, up to n rows each,
// as cursors of their own: the statement ran to its end, and the
// connection is free. The first error of any is the cursor's; rows past
// n are read and dropped (Truncated).
func (c *Cursor) ReadAll(n int) ([]*Cursor, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil, c.err
	}
	var following []*Cursor
	rows, err := c.readResult(n)
	c.buffered = rows
	for err == nil && c.rows.NextResultSet() {
		next := &Cursor{rows: c.rows, watch: c.watch, done: true, pool: c.pool}
		next.finished.Store(true)
		if err = next.readColumns(false); err != nil {
			break
		}
		next.buffered, err = next.readResult(n)
		next.truncated, next.err = len(next.buffered) > n, err
		next.buffered = next.buffered[:min(n, len(next.buffered))]
		following = append(following, next)
	}
	if err == nil {
		err = c.rows.Err()
	}
	c.truncated = len(c.buffered) > n
	c.buffered = c.buffered[:min(n, len(c.buffered))]
	c.err = err
	c.finish()
	c.err = c.watch.cancelled(c.err)
	if c.err == nil {
		c.err = c.watch.ranPastCancel()
	}
	return following, c.err
}

// readResult reads up to n+1 rows of the current result, then reads the
// rest of it, dropping them, to come to the next.
func (c *Cursor) readResult(n int) ([][]any, error) {
	var out [][]any
	for c.rows.Next() {
		if len(out) > n {
			continue
		}
		vals, err := c.scan()
		if err != nil {
			return out, err
		}
		out = append(out, vals)
	}
	return out, c.rows.Err()
}

// skipFollowing reads and drops the results that follow the cursor's in
// its statement, for the first error of any.
func (c *Cursor) skipFollowing() error {
	for c.rows.NextResultSet() {
		for c.rows.Next() {
		}
		if err := c.rows.Err(); err != nil {
			return err
		}
	}
	return c.rows.Err()
}

func isBinaryType(name string) bool {
	switch {
	case name == "BYTEA", strings.Contains(name, "BLOB"), strings.Contains(name, "BINARY"), name == "GEOMETRY", name == "BIT", name == "RAW",
		name == "GEOGRAPHY", name == "HIERARCHYID", name == "IMAGE", name == "UNIQUEIDENTIFIER":
		return true
	}
	return false
}

// Fetch reads up to n more rows; it returns fewer at the end, after which
// Done reports true.
func (c *Cursor) Fetch(n int) ([][]any, error) {
	if c.pool != nil {
		c.pool.touch()
	}
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
	for len(out) < n {
		if !c.rows.Next() {
			c.err = c.rows.Err()
			if c.err == nil && c.batch {
				c.err = c.skipFollowing()
			}
			c.finish()
			c.err = c.watch.cancelled(c.err)
			if c.err == nil {
				c.err = c.watch.ranPastCancel()
			}
			return out, c.err
		}
		vals, err := c.scan()
		if err != nil {
			c.err = err
			c.finish()
			c.err = c.watch.cancelled(c.err)
			return out, c.err
		}
		out = append(out, vals)
	}
	return out, nil
}

// scan reads the current row's values, as the app shows them.
func (c *Cursor) scan() ([]any, error) {
	vals := make([]any, len(c.Columns))
	ptrs := make([]any, len(vals))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := c.rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	for i, v := range vals {
		vals[i] = oracleValue(c.Columns[i].Type, sqlserverValue(c.Columns[i].Type, duckValue(c.Columns[i].Type, v)))
		v = vals[i]
		// MySQL's text protocol sends every value as bytes: text is
		// text unless the column holds binary data.
		if b, ok := v.([]byte); ok && !c.binary[i] && utf8.Valid(b) {
			vals[i] = string(b)
		} else if ok {
			vals[i] = append([]byte(nil), b...)
		}
	}
	return vals, nil
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
			// To SQL Server's 100 ns; DuckDB's microseconds read the same.
			return x.Format("15:04:05.9999999")
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

// endsConnection reports an error after which the server closes the
// connection, such as pg_terminate_backend's, or Oracle's ALTER SYSTEM
// KILL SESSION's.
func endsConnection(err error) bool {
	var pe *pgconn.PgError
	var me mssql.Error
	var oe *network.OracleError
	return errors.As(err, &pe) && (pe.Severity == "FATAL" || pe.Severity == "PANIC") ||
		errors.As(err, &me) && me.Class >= 20 || // SQL Server's fatal severities close the connection
		errors.As(err, &oe) && oe.Bad() // Oracle's session killed, or its connection gone
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
