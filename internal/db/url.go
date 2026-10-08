package db

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// ParseURL reads a connection from a URL, as tools and hosting services
// print them: postgres://user:pass@host:5432/db?sslmode=require,
// mysql://…, clickhouse://…, redis://…/0, rediss:// for TLS, or a path to
// a SQLite or DuckDB file. The password, if any, comes back in Password.
func ParseURL(s string) (Config, error) {
	s = strings.TrimSpace(s)
	var cfg Config
	lower := strings.ToLower(s)
	switch {
	case strings.HasSuffix(lower, ".duckdb") && !strings.Contains(s, "://"):
		return Config{Engine: DuckDB, Database: s, Name: filepath.Base(s)}, nil
	case !strings.Contains(s, "://") && (strings.HasSuffix(lower, ".sqlite") || strings.HasSuffix(lower, ".sqlite3") || strings.HasSuffix(lower, ".db")):
		return Config{Engine: SQLite, Database: s, Name: filepath.Base(s)}, nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return cfg, err
	}
	switch strings.ToLower(u.Scheme) {
	case "postgres", "postgresql":
		cfg.Engine = Postgres
	case "mysql", "mariadb":
		cfg.Engine = MySQL
	case "clickhouse", "ch":
		cfg.Engine = ClickHouse
	case "redis":
		cfg.Engine = Redis
	case "rediss":
		cfg.Engine, cfg.TLS = Redis, TLSRequire
	case "sqlite", "sqlite3", "file":
		return Config{Engine: SQLite, Database: u.Host + u.Path, Name: filepath.Base(u.Path)}, nil
	case "duckdb":
		return Config{Engine: DuckDB, Database: u.Host + u.Path, Name: filepath.Base(u.Path)}, nil
	default:
		return cfg, fmt.Errorf("unknown kind of URL %q: use postgres://, mysql://, clickhouse://, redis:// or a file path", u.Scheme)
	}
	cfg.Host = u.Hostname()
	if p := u.Port(); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil {
			return cfg, fmt.Errorf("bad port %q", p)
		}
		cfg.Port = port
	}
	if u.User != nil {
		cfg.User = u.User.Username()
		cfg.Password, _ = u.User.Password()
	}
	cfg.Database = strings.TrimPrefix(u.Path, "/")
	q := u.Query()
	switch strings.ToLower(q.Get("sslmode") + q.Get("ssl-mode") + q.Get("tls") + q.Get("secure")) {
	case "disable", "false", "0":
		cfg.TLS = TLSDisable
	case "prefer", "preferred", "allow":
		cfg.TLS = TLSPrefer
	case "require", "required", "true", "1", "skip-verify":
		cfg.TLS = TLSRequire
	case "verify-ca", "verify-full", "verify_identity":
		cfg.TLS = TLSVerifyFull
	}
	if cfg.Engine == ClickHouse && q.Get("database") != "" {
		cfg.Database = q.Get("database")
	}
	cfg.Name = cfg.Host
	if cfg.Database != "" {
		cfg.Name += "/" + cfg.Database
	}
	return cfg, nil
}
