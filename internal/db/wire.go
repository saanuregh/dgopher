package db

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
)

// Sent is a statement or command as it went to a server.
type Sent struct {
	Text     string        // a statement as sent; "" for a Redis command
	Redis    []string      // a Redis command's arguments as sent
	Args     int           // values sent apart from the text
	Database string        // the database the pool is of, "" for the connection's own
	Took     time.Duration // until the server answered (first response for a query)
	Err      error
}

var onSend atomic.Pointer[func(cfg *Config, s Sent)]

// SetOnSend sets the hook every pool and Redis client calls for what it
// sends; nil for none. Set it before pools open; tests may set it again.
func SetOnSend(f func(cfg *Config, s Sent)) {
	if f == nil {
		onSend.Store(nil)
		return
	}
	onSend.Store(&f)
}

// sent gives s to the hook, when there is one.
func sent(cfg *Config, s Sent) {
	if f := onSend.Load(); f != nil {
		(*f)(cfg, s)
	}
}

// sendsAs is the connection its sends are recorded as. One is shared by
// everything of a connection that sends, so that a rename reaches them all.
type sendsAs struct{ cfg atomic.Pointer[Config] }

func newSendsAs(cfg Config) *sendsAs {
	s := &sendsAs{}
	s.set(cfg)
	return s
}

// set records from now on as cfg, which it copies.
func (s *sendsAs) set(cfg Config) { s.cfg.Store(&cfg) }

// config is what the sends are recorded as now; nil when s is nil.
func (s *sendsAs) config() *Config {
	if s == nil {
		return nil
	}
	return s.cfg.Load()
}

// RecordAs sets the connection its sends are recorded as, from now on,
// for this pool and the pools of its other databases: a connection renamed,
// or given another ID or environment, without connecting again.
func (d *DB) RecordAs(cfg Config) { d.who.set(cfg) }

// wire is whose sends a pool records: the connection they are recorded
// as, the database the pool is of, "" for the connection's own, and the
// engine, which the wrapper decides by and the label may not change.
type wire struct {
	who      *sendsAs
	engine   Engine
	database string
}

// record gives the hook a statement the driver answered. driver.ErrSkip
// sent nothing: database/sql then prepares the statement, which is
// recorded as it runs.
func (w wire) record(text string, args int, start time.Time, err error) {
	if err == driver.ErrSkip {
		return
	}
	sent(w.who.config(), Sent{Text: text, Args: args, Database: w.database, Took: time.Since(start), Err: err})
}

// recordSent is record for a statement the driver may have refused before
// sending: driver.ErrBadConn means it was not sent, and database/sql
// retries it on another connection.
func (w wire) recordSent(text string, args int, start time.Time, err error) {
	if errors.Is(err, driver.ErrBadConn) {
		return
	}
	w.record(text, args, start, err)
}

// connector wraps a driver's connector so that its connections record
// what they send. onConnect are statements the driver sends as it
// connects, recorded after each connection opens.
func (w wire) connector(inner driver.Connector, onConnect ...string) driver.Connector {
	c := &wireConnector{inner: inner, wire: w, onConnect: onConnect}
	if _, ok := inner.(io.Closer); ok {
		// database/sql closes a connector that has Close; DuckDB's
		// releases the database file.
		return closingConnector{c}
	}
	return c
}

type wireConnector struct {
	inner driver.Connector
	wire
	onConnect []string
}

func (c *wireConnector) Connect(ctx context.Context) (driver.Conn, error) {
	dc, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	for _, text := range c.onConnect {
		c.record(text, 0, time.Now(), nil)
	}
	return c.wrap(dc), nil
}

func (c *wireConnector) Driver() driver.Driver { return c.inner.Driver() }

type closingConnector struct{ *wireConnector }

func (c closingConnector) Close() error { return c.inner.(io.Closer).Close() }

// wrap returns a recording connection that has the optional interfaces
// database/sql asks of dc for whether a transaction ended by a cancelled
// context keeps its connection: SessionResetter and Validator.
func (w wire) wrap(dc driver.Conn) driver.Conn {
	c := &wireConn{inner: dc, wire: w}
	_, resets := dc.(driver.SessionResetter)
	_, validates := dc.(driver.Validator)
	switch {
	case resets && validates:
		return resettingValidatingConn{c}
	case resets:
		return resettingConn{c}
	case validates:
		return validatingConn{c}
	}
	return c
}

// driverConn is the driver's own connection under a recording one, for
// the sql.Conn.Raw callbacks that read the driver's state.
func driverConn(dc any) any {
	if c, ok := dc.(interface{ wired() *wireConn }); ok {
		return c.wired().inner
	}
	return dc
}

// recordOn records, for the pool dc belongs to, a statement sent on the
// driver's connection directly, past the wrapper.
func recordOn(dc any, text string, start time.Time, err error) {
	if c, ok := dc.(interface{ wired() *wireConn }); ok {
		c.wired().record(text, 0, start, err)
	}
}

// wireConn records what a driver connection sends. Every driver here
// has the Context variants of Prepare, Begin, Exec and Query; for the other
// optional interfaces, where the driver lacks one, it does what database/sql
// does without it.
type wireConn struct {
	inner driver.Conn
	wire
}

func (c *wireConn) wired() *wireConn { return c }

func (c *wireConn) Close() error { return c.inner.Close() }

func (c *wireConn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

func (c *wireConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	p, ok := c.inner.(driver.ConnPrepareContext)
	if !ok {
		return nil, errors.New("db: driver lacks PrepareContext")
	}
	start := time.Now()
	si, err := p.PrepareContext(ctx, query)
	if err != nil {
		// A statement is recorded as it runs; one that never ran is
		// recorded here.
		c.recordSent(query, 0, start, err)
		return nil, err
	}
	s := &wireStmt{inner: si, conn: c, query: query}
	if _, ok := si.(driver.ColumnConverter); ok {
		return convertingStmt{s}, nil
	}
	return s, nil
}

func (c *wireConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *wireConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	b, ok := c.inner.(driver.ConnBeginTx)
	if !ok {
		return nil, errors.New("db: driver lacks BeginTx")
	}
	start := time.Now()
	tx, err := b.BeginTx(ctx, opts)
	for _, text := range beginStatements(c.engine, opts) {
		c.recordSent(text, 0, start, err)
	}
	if err != nil {
		return nil, err
	}
	return &wireTx{inner: tx, conn: c}, nil
}

func (c *wireConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := c.inner.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	start := time.Now()
	res, err := e.ExecContext(ctx, query, args)
	c.recordSent(query, len(args), start, err)
	return res, err
}

func (c *wireConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.inner.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	start := time.Now()
	rows, err := q.QueryContext(ctx, query, args)
	c.recordSent(query, len(args), start, err)
	return rows, err
}

func (c *wireConn) Ping(ctx context.Context) error {
	p, ok := c.inner.(driver.Pinger)
	if !ok {
		return nil
	}
	start := time.Now()
	err := p.Ping(ctx)
	c.record(pingStatement(c.engine), 0, start, err)
	return err
}

func (c *wireConn) CheckNamedValue(nv *driver.NamedValue) error {
	if n, ok := c.inner.(driver.NamedValueChecker); ok {
		return n.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

// pingedKey holds, in the context of a PostgreSQL connection's
// ResetSession, whether pgx decided to ping (postgresShouldPing).
type pingedKey struct{}

func (c *wireConn) resetSession(ctx context.Context) error {
	r := c.inner.(driver.SessionResetter)
	if rules, ok := SQL(c.engine); !ok || !rules.pingsOnReset() {
		return r.ResetSession(ctx)
	}
	pinged := false
	start := time.Now()
	err := r.ResetSession(context.WithValue(ctx, pingedKey{}, &pinged))
	if pinged {
		c.record(pingStatement(c.engine), 0, start, err)
	}
	return err
}

// postgresShouldPing is pgx's own rule for pinging a connection the pool
// hands out again, which this option replaces, telling the wrapper
// around it when it pings.
func postgresShouldPing(ctx context.Context, p stdlib.ShouldPingParams) bool {
	ping := p.IdleDuration > time.Second
	if pinged, ok := ctx.Value(pingedKey{}).(*bool); ok {
		*pinged = ping
	}
	return ping
}

type resettingConn struct{ *wireConn }

func (c resettingConn) ResetSession(ctx context.Context) error { return c.resetSession(ctx) }

type validatingConn struct{ *wireConn }

func (c validatingConn) IsValid() bool { return c.inner.(driver.Validator).IsValid() }

type resettingValidatingConn struct{ *wireConn }

func (c resettingValidatingConn) ResetSession(ctx context.Context) error { return c.resetSession(ctx) }
func (c resettingValidatingConn) IsValid() bool                          { return c.inner.(driver.Validator).IsValid() }

// wireStmt records each run of a prepared statement.
type wireStmt struct {
	inner driver.Stmt
	conn  *wireConn
	query string
}

func (s *wireStmt) Close() error  { return s.inner.Close() }
func (s *wireStmt) NumInput() int { return s.inner.NumInput() }

func (s *wireStmt) Exec(args []driver.Value) (driver.Result, error) {
	start := time.Now()
	res, err := s.inner.Exec(args)
	s.conn.recordSent(s.query, len(args), start, err)
	return res, err
}

func (s *wireStmt) Query(args []driver.Value) (driver.Rows, error) {
	start := time.Now()
	rows, err := s.inner.Query(args)
	s.conn.recordSent(s.query, len(args), start, err)
	return rows, err
}

func (s *wireStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	start := time.Now()
	var res driver.Result
	var err error
	if e, ok := s.inner.(driver.StmtExecContext); ok {
		res, err = e.ExecContext(ctx, args)
	} else {
		dargs, verr := legacyArgs(ctx, args)
		if verr != nil {
			return nil, verr
		}
		res, err = s.inner.Exec(dargs)
	}
	s.conn.recordSent(s.query, len(args), start, err)
	return res, err
}

func (s *wireStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	start := time.Now()
	var rows driver.Rows
	var err error
	if q, ok := s.inner.(driver.StmtQueryContext); ok {
		rows, err = q.QueryContext(ctx, args)
	} else {
		dargs, verr := legacyArgs(ctx, args)
		if verr != nil {
			return nil, verr
		}
		rows, err = s.inner.Query(dargs)
	}
	s.conn.recordSent(s.query, len(args), start, err)
	return rows, err
}

// CheckNamedValue is the statement's checker, else its connection's, as
// database/sql picks them.
func (s *wireStmt) CheckNamedValue(nv *driver.NamedValue) error {
	if n, ok := s.inner.(driver.NamedValueChecker); ok {
		return n.CheckNamedValue(nv)
	}
	return s.conn.CheckNamedValue(nv)
}

type convertingStmt struct{ *wireStmt }

func (s convertingStmt) ColumnConverter(idx int) driver.ValueConverter {
	return s.inner.(driver.ColumnConverter).ColumnConverter(idx)
}

// wireTx records the end of a transaction.
type wireTx struct {
	inner driver.Tx
	conn  *wireConn
}

func (t *wireTx) Commit() error   { return t.end(true) }
func (t *wireTx) Rollback() error { return t.end(false) }

func (t *wireTx) end(commit bool) error {
	start := time.Now()
	var err error
	if commit {
		err = t.inner.Commit()
	} else {
		err = t.inner.Rollback()
	}
	if text := endStatement(t.conn.engine, commit); text != "" {
		t.conn.record(text, 0, start, err)
	}
	return err
}

// legacyArgs are args as a driver without the Context interfaces takes them,
// checked as database/sql checks them: without names, before a cancelled
// context.
func legacyArgs(ctx context.Context, args []driver.NamedValue) ([]driver.Value, error) {
	dargs, err := namedValues(args)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return dargs, nil
}

// namedValues are args without names, as database/sql passes them to a
// driver that takes no names.
func namedValues(args []driver.NamedValue) ([]driver.Value, error) {
	out := make([]driver.Value, len(args))
	for i, a := range args {
		if a.Name != "" {
			return nil, errors.New("sql: driver does not support the use of Named Parameters")
		}
		out[i] = a.Value
	}
	return out, nil
}

// pingStatement is what a driver's ping sends: SQLite's runs a statement;
// PostgreSQL's sends an empty one, "-- ping"; MySQL's and ClickHouse's are
// protocol messages, shown as PostgreSQL's.
func pingStatement(e Engine) string {
	if r, ok := SQL(e); ok {
		return r.pingStatement()
	}
	return "-- ping"
}

// beginStatements are what each driver sends to begin a transaction of
// database/sql's: pgx's beginSQL, go-sql-driver/mysql's BeginTx,
// modernc.org/sqlite's newTx and DuckDB's BeginTx. ClickHouse's sends
// nothing.
func beginStatements(e Engine, opts driver.TxOptions) []string {
	if r, ok := SQL(e); ok {
		return r.beginStatements(opts)
	}
	return nil
}

// endStatement is what each driver sends to commit, or roll back, a
// transaction of database/sql's; "" for nothing. ClickHouse's sends a
// prepared batch, or nothing.
func endStatement(e Engine, commit bool) string {
	if r, ok := SQL(e); ok {
		return r.endStatement(commit)
	}
	return ""
}

// lowerEndStatement is how pgx and modernc.org/sqlite end a transaction.
func lowerEndStatement(commit bool) string {
	if commit {
		return "commit"
	}
	return "rollback"
}
