package db

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"dgopher/internal/sshtunnel"

	"github.com/ClickHouse/clickhouse-go/v2"
	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// appName is how the app introduces itself to servers that ask.
const appName = "DGopher"

// DB is an open SQL connection pool, with what it needs to read the
// schema and to start sessions.
type DB struct {
	Config  Config
	Dialect Dialect
	SQL     *sql.DB

	tunnel     *sshtunnel.Tunnel
	ownsTunnel bool
	// single is set when the pool holds exactly one connection, which
	// every session shares: DuckDB, whose driver opens the file anew for
	// each connection, and in-memory SQLite, private to its connection.
	single bool
	// sharedTx is the transaction of that one connection, which all its
	// sessions see.
	txMu     sync.Mutex
	sharedTx TxState
	txOwner  *Session // the session that began sharedTx

	mu        sync.Mutex
	databases map[string]*DB // other databases of a PostgreSQL server
}

// endpoint is where the driver connects, and the host its certificate
// must name.
type endpoint struct {
	host       string
	port       int
	serverName string
}

// openTunnel starts the SSH tunnel of a configuration, if it has one, and
// returns where the driver should connect.
func openTunnel(ctx context.Context, cfg *Config, knownHosts []string) (endpoint, *sshtunnel.Tunnel, error) {
	ep := endpoint{host: cfg.Host, port: cfg.port(), serverName: cfg.Host}
	if !cfg.SSH.Enabled {
		return ep, nil, nil
	}
	t, err := sshtunnel.Open(ctx, sshtunnel.Config{
		Host:          cfg.SSH.Host,
		Port:          cfg.SSH.Port,
		User:          cfg.SSH.User,
		Password:      cfg.SSH.Password,
		KeyPath:       cfg.SSH.KeyPath,
		KeyPassphrase: cfg.SSH.KeyPassphrase,
		UseAgent:      cfg.SSH.UseAgent,
	}, cfg.Host, cfg.port(), knownHosts)
	if err != nil {
		return ep, nil, err
	}
	ep.host, ep.port = "127.0.0.1", t.LocalPort()
	return ep, t, nil
}

// tlsConfig returns the TLS settings of a mode, nil for none.
func tlsConfig(cfg *Config, serverName string) (*tls.Config, error) {
	switch cfg.TLS {
	case "", TLSDisable:
		return nil, nil
	case TLSPrefer, TLSRequire:
		// Encrypted but unverified, as PostgreSQL's sslmode=require.
		return &tls.Config{InsecureSkipVerify: true, ServerName: serverName}, nil
	case TLSVerifyFull:
		tc := &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12}
		if cfg.CAFile != "" {
			pem, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				return nil, fmt.Errorf("reading the CA certificate: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("%s holds no PEM certificate", cfg.CAFile)
			}
			tc.RootCAs = pool
		}
		return tc, nil
	}
	return nil, fmt.Errorf("unknown TLS mode %q", cfg.TLS)
}

// Open connects to a SQL database and checks the connection.
// knownHosts are the known_hosts files an SSH tunnel verifies hosts with.
func Open(ctx context.Context, cfg Config, knownHosts []string) (*DB, error) {
	if !cfg.Engine.IsSQL() {
		return nil, fmt.Errorf("%s is not a SQL database", cfg.Engine.Label())
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	ep, tunnel, err := openTunnel(ctx, &cfg, knownHosts)
	if err != nil {
		return nil, redact(err, cfg)
	}
	d, err := openWith(ctx, cfg, ep, tunnel)
	if err != nil {
		if tunnel != nil {
			tunnel.Close()
		}
		return nil, redact(err, cfg)
	}
	d.ownsTunnel = tunnel != nil
	return d, nil
}

func openWith(ctx context.Context, cfg Config, ep endpoint, tunnel *sshtunnel.Tunnel) (*DB, error) {
	tc, err := tlsConfig(&cfg, ep.serverName)
	if err != nil {
		return nil, err
	}
	var sqldb *sql.DB
	single := false
	switch cfg.Engine {
	case Postgres:
		sqldb, err = openPostgres(cfg, ep, tc)
	case MySQL:
		sqldb, err = openMySQL(cfg, ep, tc)
	case ClickHouse:
		if cfg.TLS == TLSPrefer && !speaksTLS(ctx, ep, tc) {
			tc = nil
		}
		sqldb = openClickHouse(cfg, ep, tc)
	case SQLite:
		sqldb, err = openSQLite(cfg)
		single = isMemory(cfg.Database)
	case DuckDB:
		sqldb, err = openDuckDB(cfg)
		single = true
	}
	if err != nil {
		return nil, err
	}
	if single {
		sqldb.SetMaxOpenConns(1)
		sqldb.SetMaxIdleConns(1)
	} else {
		sqldb.SetMaxOpenConns(8)
		sqldb.SetMaxIdleConns(2)
		sqldb.SetConnMaxIdleTime(5 * time.Minute)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := sqldb.PingContext(pingCtx); err != nil {
		sqldb.Close()
		return nil, redact(err, cfg)
	}
	return &DB{Config: cfg, Dialect: DialectOf(cfg.Engine), SQL: sqldb, tunnel: tunnel, single: single}, nil
}

func openPostgres(cfg Config, ep endpoint, tc *tls.Config) (*sql.DB, error) {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.User, cfg.Password),
		Host:   net.JoinHostPort(ep.host, strconv.Itoa(ep.port)),
		Path:   "/" + cfg.Database,
	}
	q := url.Values{}
	q.Set("connect_timeout", "15")
	switch cfg.TLS {
	case "", TLSDisable:
		q.Set("sslmode", "disable")
	case TLSPrefer:
		q.Set("sslmode", "prefer")
	default:
		q.Set("sslmode", "require")
	}
	u.RawQuery = q.Encode()
	pc, err := pgx.ParseConfig(u.String())
	if err != nil {
		return nil, redact(err, cfg)
	}
	// Set here rather than in the URL, whose query writes a space as +.
	pc.RuntimeParams["application_name"] = appName
	// A cancelled query asks the server to stop it, and keeps the
	// connection, its session settings and its transaction; pgx would
	// otherwise close the connection.
	pc.BuildContextWatcherHandler = func(c *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: c, DeadlineDelay: 10 * time.Second}
	}
	if tc != nil {
		// Our own TLS settings: verification against the real host name
		// even through a tunnel, and the CA the user chose.
		pc.TLSConfig = tc
		for _, fb := range pc.Fallbacks {
			if fb.TLSConfig != nil {
				fb.TLSConfig = tc
			}
		}
	}
	if cfg.ReadOnly {
		// The server refuses writes in every transaction of the session.
		pc.RuntimeParams["default_transaction_read_only"] = "on"
	}
	return stdlib.OpenDB(*pc), nil
}

func openMySQL(cfg Config, ep endpoint, tc *tls.Config) (*sql.DB, error) {
	mc := mysql.NewConfig()
	mc.User = cfg.User
	mc.Passwd = cfg.Password
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(ep.host, strconv.Itoa(ep.port))
	mc.DBName = cfg.Database
	mc.Timeout = 15 * time.Second
	mc.ConnectionAttributes = "program_name:" + appName
	// Rows matched, not only those changed: an edit must touch exactly
	// one row, even one already holding the new value.
	mc.ClientFoundRows = true
	switch cfg.TLS {
	case TLSPrefer:
		mc.TLSConfig = "preferred"
	default:
		mc.TLS = tc
	}
	if cfg.ReadOnly {
		// Sent as SET on every new connection.
		mc.Params = map[string]string{"transaction_read_only": "1"}
	}
	conn, err := mysql.NewConnector(mc)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(conn), nil
}

func openClickHouse(cfg Config, ep endpoint, tc *tls.Config) *sql.DB {
	opt := &clickhouse.Options{
		Addr: []string{net.JoinHostPort(ep.host, strconv.Itoa(ep.port))},
		Auth: clickhouse.Auth{
			Database: cfg.Database,
			Username: cfg.User,
			Password: cfg.Password,
		},
		DialTimeout: 15 * time.Second,
		ClientInfo: clickhouse.ClientInfo{Products: []struct{ Name, Version string }{
			{Name: "dgopher", Version: "0.1"},
		}},
	}
	opt.TLS = tc
	if p := cfg.port(); p == 8123 || p == 8443 {
		opt.Protocol = clickhouse.HTTP
	}
	if cfg.ReadOnly {
		// 2: reads, and changes of settings, which the driver makes.
		opt.Settings = clickhouse.Settings{"readonly": 2}
	}
	return clickhouse.OpenDB(opt)
}

// Database returns a pool for another database of the same PostgreSQL
// server, which needs a connection of its own; for other engines, where
// databases are schemas of one connection, it returns d.
func (d *DB) Database(ctx context.Context, name string) (*DB, error) {
	if d.Config.Engine != Postgres || name == "" || name == d.Config.Database {
		return d, nil
	}
	d.mu.Lock()
	if other, ok := d.databases[name]; ok {
		d.mu.Unlock()
		return other, nil
	}
	d.mu.Unlock()
	cfg := d.Config
	cfg.Database = name
	ep := endpoint{host: cfg.Host, port: cfg.port(), serverName: cfg.Host}
	if d.tunnel != nil {
		ep.host, ep.port = "127.0.0.1", d.tunnel.LocalPort()
	}
	other, err := openWith(ctx, cfg, ep, d.tunnel)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if existing, ok := d.databases[name]; ok {
		other.SQL.Close()
		return existing, nil
	}
	if d.databases == nil {
		d.databases = map[string]*DB{}
	}
	d.databases[name] = other
	return other, nil
}

// Close closes the pool, those of the other databases, and the tunnel.
func (d *DB) Close() error {
	d.mu.Lock()
	others := d.databases
	d.databases = nil
	d.mu.Unlock()
	for _, o := range others {
		o.SQL.Close()
	}
	err := d.SQL.Close()
	if d.ownsTunnel {
		d.tunnel.Close()
	}
	return err
}

// Ping checks that the server still answers.
func (d *DB) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return d.SQL.PingContext(ctx)
}

// ServerVersion returns the server's version string.
func (d *DB) ServerVersion(ctx context.Context) string {
	var q string
	switch d.Config.Engine {
	case Postgres:
		q = "SHOW server_version"
	case MySQL:
		q = "SELECT VERSION()"
	case ClickHouse:
		q = "SELECT version()"
	case SQLite:
		q = "SELECT sqlite_version()"
	case DuckDB:
		q = "SELECT version()"
	}
	var v string
	if err := d.SQL.QueryRowContext(ctx, q).Scan(&v); err != nil {
		return ""
	}
	return v
}

func isMemory(path string) bool {
	return path == ":memory:" || strings.Contains(path, "mode=memory")
}

// ExpandPath resolves a leading ~ to the home directory.
func ExpandPath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func openSQLite(cfg Config) (*sql.DB, error) {
	path := ExpandPath(cfg.Database)
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	if cfg.ReadOnly {
		q.Set("mode", "ro")
		q.Add("_pragma", "query_only(1)")
	} else if !isMemory(path) {
		if _, err := os.Stat(path); err != nil {
			// Opening a missing file would create an empty database
			// where the user expected theirs.
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	dsn := path
	if !isMemory(path) {
		dsn = "file:" + (&url.URL{Path: path}).EscapedPath()
	}
	if strings.Contains(dsn, "?") {
		dsn += "&" + q.Encode()
	} else {
		dsn += "?" + q.Encode()
	}
	return sql.Open("sqlite", dsn)
}

// CreateFile makes an empty database of a file engine at path. A file
// already there is left as it is: it may hold someone's data.
func CreateFile(ctx context.Context, e Engine, path string) error {
	path = ExpandPath(path)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	switch e {
	case SQLite:
		// An empty file is an empty SQLite database.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		return f.Close()
	case DuckDB:
		// An empty file is not a DuckDB database: DuckDB writes its
		// header as it opens a path that does not exist.
		if strings.Contains(path, "?") {
			return fmt.Errorf("%s: DuckDB cannot open a path that contains '?'", path)
		}
		sqldb, err := sql.Open("duckdb", path)
		if err != nil {
			return err
		}
		return errors.Join(sqldb.PingContext(ctx), sqldb.Close())
	}
	return fmt.Errorf("%s keeps no database in a file", e.Label())
}

func openDuckDB(cfg Config) (*sql.DB, error) {
	path := ExpandPath(cfg.Database)
	if path != ":memory:" && !cfg.ReadOnly {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	// The driver cuts the path at its first ?, so a path holding one
	// cannot be named.
	if strings.Contains(path, "?") {
		return nil, fmt.Errorf("%s: DuckDB cannot open a path that contains '?'", path)
	}
	dsn := path
	if cfg.ReadOnly {
		dsn = path + "?access_mode=READ_ONLY"
	}
	return sql.Open("duckdb", dsn)
}

// speaksTLS reports whether the server completes a TLS handshake, which
// "prefer" asks before choosing: the probe sends no credentials, so a
// server without TLS never gets a password meant for an encrypted link.
func speaksTLS(ctx context.Context, ep endpoint, tc *tls.Config) bool {
	if ip := net.ParseIP(ep.serverName); ep.serverName == "localhost" || ip != nil && ip.IsLoopback() {
		return false // traffic that never leaves the computer
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var d net.Dialer
	raw, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ep.host, strconv.Itoa(ep.port)))
	if err != nil {
		return true // unreachable: let the real connection report it
	}
	defer raw.Close()
	conn := tls.Client(raw, tc)
	return conn.HandshakeContext(ctx) == nil
}

// redact takes the connection's secrets out of an error's message, as
// drivers may quote what they were given.
func redact(err error, cfg Config) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, secret := range []string{cfg.Password, cfg.SSH.Password, cfg.SSH.KeyPassphrase, url.QueryEscape(cfg.Password), url.PathEscape(cfg.Password)} {
		if len(secret) >= 3 {
			msg = strings.ReplaceAll(msg, secret, "•••")
		}
	}
	if msg == err.Error() {
		return err
	}
	return errors.New(msg)
}
