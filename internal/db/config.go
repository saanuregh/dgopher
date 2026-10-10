// Package db connects to the databases DGopher supports and reads their
// schemas: PostgreSQL, MySQL and ClickHouse through database/sql, and Redis
// through its own client.
package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"dgopher/internal/netproxy"
	"dgopher/internal/sshtunnel"
)

// Engine is the kind of database a connection talks to.
type Engine string

const (
	Postgres   Engine = "postgres"
	MySQL      Engine = "mysql"
	ClickHouse Engine = "clickhouse"
	Redis      Engine = "redis"
	SQLite     Engine = "sqlite"
	DuckDB     Engine = "duckdb"
)

// Engines lists the supported engines in the order the UI offers them.
func Engines() []Engine { return []Engine{Postgres, MySQL, ClickHouse, Redis, SQLite, DuckDB} }

// Label is the engine's display name.
func (e Engine) Label() string {
	switch e {
	case Postgres:
		return "PostgreSQL"
	case MySQL:
		return "MySQL"
	case ClickHouse:
		return "ClickHouse"
	case Redis:
		return "Redis"
	case SQLite:
		return "SQLite"
	case DuckDB:
		return "DuckDB"
	}
	return string(e)
}

// DefaultPort is the port the engine listens on unless configured otherwise.
func (e Engine) DefaultPort() int {
	switch e {
	case Postgres:
		return 5432
	case MySQL:
		return 3306
	case ClickHouse:
		return 9000
	case Redis:
		return 6379
	}
	return 0
}

// DefaultUser is the user most installations of the engine start with.
func (e Engine) DefaultUser() string {
	switch e {
	case Postgres:
		return "postgres"
	case MySQL:
		return "root"
	case ClickHouse:
		return "default"
	}
	return ""
}

// IsSQL reports whether the engine speaks SQL.
func (e Engine) IsSQL() bool { return e != Redis }

// IsFile reports whether the engine's databases are files on this
// computer, opened by path rather than reached over the network.
func (e Engine) IsFile() bool { return e == SQLite || e == DuckDB }

// TransactionalDDL reports whether the engine's schema changes can run in
// a transaction, all or none, as PostgreSQL's, SQLite's and DuckDB's can.
func (e Engine) TransactionalDDL() bool { return e == Postgres || e == SQLite || e == DuckDB }

// Transactions reports whether a client can hold a transaction of the
// engine's open: ClickHouse and Redis have none.
func (e Engine) Transactions() bool { return e != ClickHouse && e != Redis }

// Savepoints reports whether the engine nests a savepoint in an open
// transaction, which a failed statement can roll back to alone: DuckDB's
// grammar has none, and ClickHouse holds no transactions.
func (e Engine) Savepoints() bool { return e == Postgres || e == MySQL || e == SQLite }

// Environment says how careful the app must be with a connection.
type Environment string

const (
	Development Environment = "development"
	Staging     Environment = "staging"
	Production  Environment = "production"
)

// Environments lists them from the least to the most careful.
func Environments() []Environment { return []Environment{Development, Staging, Production} }

// Label is the environment's display name.
func (e Environment) Label() string {
	switch e {
	case Staging:
		return "Staging"
	case Production:
		return "Production"
	}
	return "Development"
}

// TLSMode is how a connection uses TLS, named after PostgreSQL's sslmode.
type TLSMode string

const (
	TLSDisable    TLSMode = "disable"     // plain text
	TLSPrefer     TLSMode = "prefer"      // TLS when the server offers it, unverified
	TLSRequire    TLSMode = "require"     // TLS, server certificate not verified
	TLSVerifyFull TLSMode = "verify-full" // TLS, certificate and host name verified
)

// TLSModes lists the modes in the order the UI offers them.
func TLSModes() []TLSMode { return []TLSMode{TLSDisable, TLSPrefer, TLSRequire, TLSVerifyFull} }

// CommitMode is whether statements commit on their own.
type CommitMode string

const (
	CommitDefault CommitMode = ""       // manual in production, auto elsewhere
	CommitAuto    CommitMode = "auto"   // every statement commits
	CommitManual  CommitMode = "manual" // writes open a transaction the user commits or rolls back
)

// SSHConfig reaches the database through an SSH server.
type SSHConfig struct {
	Enabled       bool   `json:"enabled"`
	Host          string `json:"host"`
	Port          int    `json:"port,omitempty"`
	User          string `json:"user"`
	KeyPath       string `json:"keyPath,omitempty"`
	UseAgent      bool   `json:"useAgent,omitempty"`
	Password      string `json:"-"`
	KeyPassphrase string `json:"-"`
	// PasswordCommand prints the SSH password, or the key's passphrase.
	PasswordCommand string `json:"passwordCommand,omitempty"`
	// Jump lists SSH servers reached in turn before Host, as OpenSSH's
	// -J: [user@]host[:port], separated by commas, as User when it names
	// no user. They log in with the key file or the agent only: the
	// tunnel's password is the target's, never sent to a jump host.
	Jump string `json:"jump,omitempty"`
}

// Jumps reads the jump hosts of Jump.
func (s *SSHConfig) Jumps() ([]sshtunnel.Hop, error) {
	var hops []sshtunnel.Hop
	for spec := range strings.SplitSeq(s.Jump, ",") {
		spec = strings.TrimSpace(spec)
		if spec == "" {
			continue
		}
		hop := sshtunnel.Hop{User: s.User}
		if user, host, ok := strings.Cut(spec, "@"); ok {
			hop.User, spec = user, host
		}
		hop.Host = spec
		if host, port, err := net.SplitHostPort(spec); err == nil {
			p, err := strconv.Atoi(port)
			if err != nil || p < 1 || p > 65535 {
				return nil, fmt.Errorf("the jump host %s has no valid port", spec)
			}
			hop.Host, hop.Port = host, p
		}
		if hop.Host == "" || hop.User == "" || strings.ContainsAny(hop.Host, " /") {
			return nil, fmt.Errorf("%q is no jump host: write [user@]host[:port]", spec)
		}
		hops = append(hops, hop)
	}
	return hops, nil
}

// Config describes a saved connection. Secrets are kept out of its JSON:
// the app stores them in the system keychain.
type Config struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Engine   Engine `json:"engine"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"-"`
	// PasswordEnv names an environment variable holding the password, so
	// that a shared configuration needs no secret.
	PasswordEnv string `json:"passwordEnv,omitempty"`
	// AskPassword asks for the password on every connect instead of
	// keeping it in the keychain.
	AskPassword bool `json:"askPassword,omitempty"`
	// PasswordCommand runs to print the password, as a password
	// manager's CLI does; the app asks before it first runs.
	PasswordCommand string `json:"passwordCommand,omitempty"`
	// Identity logs in with a token of a cloud identity in place of a
	// password; IdentityRegion and IdentityProfile choose AWS's.
	Identity        Identity `json:"identity,omitempty"`
	IdentityRegion  string   `json:"identityRegion,omitempty"`
	IdentityProfile string   `json:"identityProfile,omitempty"`
	// ClearTextPassword sends MySQL the password as it is, as logging in
	// through LDAP or PAM needs; only over TLS.
	ClearTextPassword bool    `json:"clearTextPassword,omitempty"`
	Database          string  `json:"database,omitempty"`
	TLS               TLSMode `json:"tls,omitempty"`
	CAFile            string  `json:"caFile,omitempty"`
	// CertFile and KeyFile are the PEM files of a TLS client certificate
	// and its key, which the server may ask the connection to log in with.
	CertFile string      `json:"certFile,omitempty"`
	KeyFile  string      `json:"keyFile,omitempty"`
	Env      Environment `json:"environment"`
	ReadOnly bool        `json:"readOnly,omitempty"`
	Commit   CommitMode  `json:"commit,omitempty"`
	// StatementTimeout stops a statement after so many seconds; 0 never.
	StatementTimeout int `json:"statementTimeout,omitempty"`
	// IdleTxTimeout rolls back a transaction left idle for so many
	// seconds; 0 takes the environment's default, -1 never.
	IdleTxTimeout int `json:"idleTxTimeout,omitempty"`
	// AutoConnect opens the connection as the app starts, and lets an
	// editor shown while it is closed open it; off, it opens when asked.
	AutoConnect bool `json:"autoConnect,omitempty"`
	// Color overrides the environment's color, as a hex string.
	Color string    `json:"color,omitempty"`
	SSH   SSHConfig `json:"ssh"`
	// Proxy is a SOCKS5 or HTTP proxy the connection goes through to its
	// server, or to its SSH host when it has one.
	Proxy ProxyConfig `json:"proxy,omitzero"`
	// Redis says how a Redis connection reaches its data, when not on the
	// one server of Host and Port.
	Redis RedisConfig `json:"redis,omitzero"`
	// KeySeparator splits a Redis connection's key names into folders;
	// "" for the usual ":".
	KeySeparator string `json:"keySeparator,omitempty"`
	// Catalog is how much of a schema's catalog is read, by schema, or
	// database/schema for another of PostgreSQL's databases; a schema not
	// named is read as needed.
	Catalog map[string]CatalogDepth `json:"catalog,omitempty"`
}

// CatalogDepth is how much of a schema's catalog is read: on a very large
// schema, less than all at once, or more.
type CatalogDepth string

const (
	// CatalogAsNeeded reads the tables with their sizes, and a table's
	// columns once it is opened or named.
	CatalogAsNeeded CatalogDepth = ""
	// CatalogNames reads the tables' names alone, and a table's columns
	// only once it is opened.
	CatalogNames CatalogDepth = "names"
	// CatalogColumns reads the tables with every column at once.
	CatalogColumns CatalogDepth = "columns"
	// CatalogEverything reads the columns, and the schema's routines,
	// triggers, sequences and types with them.
	CatalogEverything CatalogDepth = "everything"
)

// CatalogDepths are the depths, in the order a menu offers them.
var CatalogDepths = []CatalogDepth{CatalogAsNeeded, CatalogNames, CatalogColumns, CatalogEverything}

// catalogKey is what Catalog keeps a schema's depth by.
func catalogKey(database, schema string) string {
	if database == "" {
		return schema
	}
	return database + "/" + schema
}

// CatalogOf is how much of a schema's catalog is read.
func (c *Config) CatalogOf(database, schema string) CatalogDepth {
	return c.Catalog[catalogKey(database, schema)]
}

// SetCatalog sets how much of a schema's catalog is read, in a map of
// its own: copies of the config made before keep theirs.
func (c *Config) SetCatalog(database, schema string, d CatalogDepth) {
	key := catalogKey(database, schema)
	next := maps.Clone(c.Catalog)
	if next == nil {
		next = map[string]CatalogDepth{}
	}
	if d == CatalogAsNeeded {
		delete(next, key)
	} else {
		next[key] = d
	}
	if len(next) == 0 {
		next = nil
	}
	c.Catalog = next
}

// DefaultKeySeparator splits Redis key names into folders unless a
// connection says otherwise.
const DefaultKeySeparator = ":"

// Separator is the string a Redis connection's key names split at.
func (c *Config) Separator() string {
	if c.KeySeparator == "" {
		return DefaultKeySeparator
	}
	return c.KeySeparator
}

// ProxyConfig is a proxy a connection goes through, none without a Kind.
type ProxyConfig struct {
	Kind     string `json:"kind,omitempty"` // netproxy.SOCKS5 or netproxy.HTTP
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	User     string `json:"user,omitempty"`
	Password string `json:"-"`
}

// dialer is the proxy's, nil for none.
func (p *ProxyConfig) dialer() func(ctx context.Context, network, addr string) (net.Conn, error) {
	if p.Kind == "" {
		return nil
	}
	return netproxy.Config{Kind: p.Kind, Host: p.Host, Port: p.Port, User: p.User, Password: p.Password}.Dial
}

// RedisMode is how a Redis connection reaches its data.
type RedisMode string

const (
	// RedisStandalone talks to the one server of Host and Port.
	RedisStandalone RedisMode = ""
	// RedisCluster talks to every node of a cluster, which Host and Port
	// and Nodes lead to.
	RedisCluster RedisMode = "cluster"
	// RedisSentinel talks to the master the sentinels of Host and Port and
	// Nodes name, and to the next one after a failover.
	RedisSentinel RedisMode = "sentinel"
)

// RedisConfig is how a Redis connection reaches a cluster, or a master
// through Sentinel.
type RedisConfig struct {
	Mode RedisMode `json:"mode,omitempty"`
	// Nodes are more addresses, host:port separated by commas, of the
	// cluster's nodes or of the sentinels, beside Host and Port: any one
	// answering is enough. A string, for a Config to compare with ==.
	Nodes string `json:"nodes,omitempty"`
	// Master is the name of the master the sentinels watch.
	Master string `json:"master,omitempty"`
	// SentinelUser and SentinelPassword log in to the sentinels, which
	// keep credentials of their own: the database's password never goes
	// to them.
	SentinelUser     string `json:"sentinelUser,omitempty"`
	SentinelPassword string `json:"-"`
}

// RedisAddrs are the addresses a Redis connection starts from: Host and
// Port, then Nodes.
func (c *Config) RedisAddrs() ([]string, error) {
	addrs := []string{net.JoinHostPort(c.Host, strconv.Itoa(c.port()))}
	for n := range strings.SplitSeq(c.Redis.Nodes, ",") {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		host, port, err := net.SplitHostPort(n)
		if err != nil {
			return nil, fmt.Errorf("the node %q is not host:port", n)
		}
		if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 || host == "" {
			return nil, fmt.Errorf("the node %q is not host:port", n)
		}
		addrs = append(addrs, n)
	}
	return addrs, nil
}

// NewID returns a random connection ID.
func NewID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ManualCommit reports whether writes wait for an explicit commit.
func (c *Config) ManualCommit() bool {
	if !c.Engine.Transactions() {
		return false
	}
	switch c.Commit {
	case CommitAuto:
		return false
	case CommitManual:
		return true
	}
	return c.Env == Production
}

// Validate reports the first problem that keeps the connection from being
// used.
func (c *Config) Validate() error {
	var errs []string
	if strings.TrimSpace(c.Name) == "" {
		errs = append(errs, "a name is required")
	}
	switch c.Engine {
	case Postgres, MySQL, ClickHouse, Redis, SQLite, DuckDB:
	default:
		errs = append(errs, fmt.Sprintf("unknown engine %q", c.Engine))
	}
	for schema, d := range c.Catalog {
		if !slices.Contains(CatalogDepths, d) {
			errs = append(errs, fmt.Sprintf("%q is no depth to read the catalog of %s at", d, schema))
		}
	}
	if c.Engine.IsFile() {
		if strings.TrimSpace(c.Database) == "" {
			errs = append(errs, "a database file is required")
		}
	} else if strings.TrimSpace(c.Host) == "" {
		errs = append(errs, "a host is required")
	}
	if c.Port < 0 || c.Port > 65535 {
		errs = append(errs, "the port must be between 1 and 65535")
	}
	if (c.CertFile == "") != (c.KeyFile == "") {
		errs = append(errs, "a client certificate needs both its file and its key's")
	}
	if c.CertFile != "" && (c.TLS == "" || c.TLS == TLSDisable) {
		errs = append(errs, "a client certificate needs TLS")
	}
	if c.Engine == Redis && c.Database != "" {
		var n int
		if _, err := fmt.Sscanf(c.Database, "%d", &n); err != nil || n < 0 {
			errs = append(errs, "the Redis database must be a number such as 0")
		} else if n != 0 && c.Redis.Mode == RedisCluster {
			errs = append(errs, "a Redis cluster has only database 0")
		}
	}
	if c.Engine == Redis {
		switch c.Redis.Mode {
		case RedisStandalone, RedisCluster:
		case RedisSentinel:
			if strings.TrimSpace(c.Redis.Master) == "" {
				errs = append(errs, "the name of the master the sentinels watch is required")
			}
		default:
			errs = append(errs, fmt.Sprintf("unknown Redis mode %q", c.Redis.Mode))
		}
		if _, err := c.RedisAddrs(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if c.SSH.Enabled && !c.Engine.IsFile() {
		if strings.TrimSpace(c.SSH.Host) == "" {
			errs = append(errs, "the SSH host is required")
		}
		if strings.TrimSpace(c.SSH.User) == "" {
			errs = append(errs, "the SSH user is required")
		}
		if _, err := c.SSH.Jumps(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	switch c.Proxy.Kind {
	case "":
	case netproxy.SOCKS5, netproxy.HTTP:
		if strings.TrimSpace(c.Proxy.Host) == "" || c.Proxy.Port < 1 || c.Proxy.Port > 65535 {
			errs = append(errs, "the proxy needs a host and a port")
		}
	default:
		errs = append(errs, fmt.Sprintf("unknown proxy kind %q", c.Proxy.Kind))
	}
	sources := 0
	for _, set := range []bool{c.AskPassword, c.PasswordEnv != "", c.PasswordCommand != "", c.Identity != ""} {
		if set {
			sources++
		}
	}
	if c.StatementTimeout < 0 {
		errs = append(errs, "the statement timeout cannot be negative")
	}
	if c.IdleTxTimeout < -1 {
		errs = append(errs, "the idle transaction timeout must be -1 (never), 0 (default) or more")
	}
	if sources > 1 {
		errs = append(errs, "choose one way to get the password: ask, an environment variable, a command or a cloud identity")
	}
	// Prefer may fall back to plain text, and require trusts any
	// certificate, so whoever sits in the middle would read the token or
	// the clear password: they go only to a verified server.
	verified := c.TLS == TLSVerifyFull
	switch {
	case c.Identity == "":
	case !slices.Contains(Identities(), c.Identity):
		errs = append(errs, fmt.Sprintf("unknown identity %q", c.Identity))
	case c.Engine != Postgres && c.Engine != MySQL:
		errs = append(errs, "a cloud identity logs in to PostgreSQL or MySQL")
	case !verified:
		errs = append(errs, "a cloud identity's token goes only over verified TLS: choose TLS verify-full "+
			"(Verify certificate and host) and, unless the system trusts it, the server's CA file: AWS's RDS CA bundle, Cloud SQL's server CA")
	}
	if c.Identity == IdentityAWS {
		errs = append(errs, awsIdentityProblems(c)...)
	}
	switch {
	case !c.ClearTextPassword:
	case c.Engine != MySQL:
		errs = append(errs, "a password sent as clear text goes only to MySQL")
	case !verified:
		errs = append(errs, "a password sent as clear text goes only over verified TLS: choose TLS verify-full "+
			"(Verify certificate and host) and, unless the system trusts it, the server's CA file")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// AWS identity values become words of the aws command line, which on
// Windows is a batch file whose quoting Go cannot get right: they keep
// to the characters such names have, and never start as an option.
var (
	awsHost    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`)
	awsUser    = regexp.MustCompile(`^[A-Za-z0-9_.@][A-Za-z0-9_.@-]*$`)
	awsRegion  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	awsProfile = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_.-]*$`)
)

// awsIdentityProblems are the values of an AWS identity its command
// cannot take.
func awsIdentityProblems(c *Config) []string {
	var errs []string
	if !awsHost.MatchString(c.Host) {
		errs = append(errs, "an AWS identity's host is a name of letters, digits, dots and dashes")
	}
	if !awsUser.MatchString(c.User) {
		errs = append(errs, "an AWS identity's user is made of letters, digits and _ . @ -")
	}
	if c.IdentityRegion != "" && !awsRegion.MatchString(c.IdentityRegion) {
		errs = append(errs, "an AWS region is made of lowercase letters, digits and dashes, as eu-west-1")
	}
	if c.IdentityProfile != "" && !awsProfile.MatchString(c.IdentityProfile) {
		errs = append(errs, "an AWS profile is made of letters, digits and _ . -")
	}
	return errs
}

func (c *Config) port() int {
	switch {
	case c.Port > 0:
		return c.Port
	case c.Engine == Redis && c.Redis.Mode == RedisSentinel:
		return SentinelPort
	}
	return c.Engine.DefaultPort()
}

// SentinelPort is the port Redis Sentinel listens on unless told another.
const SentinelPort = 26379

func (c *Config) env() Environment {
	if c.Env == "" {
		return Development
	}
	return c.Env
}

// NormalizeEnvironment reads an environment as a person may write it in a
// file: in any case, or shortened. A value it does not know is
// production, the careful reading of a typo, never development.
func NormalizeEnvironment(e Environment) Environment {
	switch strings.ToLower(strings.TrimSpace(string(e))) {
	case "", "development", "dev", "local":
		return Development
	case "staging", "stage", "test", "testing", "qa", "uat":
		return Staging
	}
	return Production
}

// ValidColor reports whether s is a color as a connection keeps it:
// #rrggbb, in either case.
func ValidColor(s string) bool {
	if len(s) != 7 || s[0] != '#' {
		return false
	}
	_, err := hex.DecodeString(s[1:])
	return err == nil
}
