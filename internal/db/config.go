// Package db connects to the databases DGopher supports and reads their
// schemas: PostgreSQL, MySQL and ClickHouse through database/sql, and Redis
// through its own client.
package db

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
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
	PasswordCommand string      `json:"passwordCommand,omitempty"`
	Database        string      `json:"database,omitempty"`
	TLS             TLSMode     `json:"tls,omitempty"`
	CAFile          string      `json:"caFile,omitempty"`
	Env             Environment `json:"environment"`
	ReadOnly        bool        `json:"readOnly,omitempty"`
	Commit          CommitMode  `json:"commit,omitempty"`
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
}

// NewID returns a random connection ID.
func NewID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// ManualCommit reports whether writes wait for an explicit commit.
func (c *Config) ManualCommit() bool {
	if c.Engine == ClickHouse || c.Engine == Redis {
		return false // neither has transactions a client can hold open
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
	if c.Engine == Redis && c.Database != "" {
		var n int
		if _, err := fmt.Sscanf(c.Database, "%d", &n); err != nil || n < 0 {
			errs = append(errs, "the Redis database must be a number such as 0")
		}
	}
	if c.SSH.Enabled && !c.Engine.IsFile() {
		if strings.TrimSpace(c.SSH.Host) == "" {
			errs = append(errs, "the SSH host is required")
		}
		if strings.TrimSpace(c.SSH.User) == "" {
			errs = append(errs, "the SSH user is required")
		}
	}
	sources := 0
	for _, set := range []bool{c.AskPassword, c.PasswordEnv != "", c.PasswordCommand != ""} {
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
		errs = append(errs, "choose one way to get the password: ask, an environment variable or a command")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (c *Config) port() int {
	if c.Port > 0 {
		return c.Port
	}
	return c.Engine.DefaultPort()
}

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
