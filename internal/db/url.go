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
		cfg.Engine, cfg.TLS = Redis, TLSVerifyFull
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
	mode, err := urlTLS(cfg.Engine, q)
	if err != nil {
		return cfg, err
	}
	if mode != "" {
		cfg.TLS = mode
	}
	if _, ok := q["tls_server_name"]; ok {
		return cfg, fmt.Errorf("tls_server_name in the URL cannot be used: the server is verified against the URL's host")
	}
	if _, ok := q["skip_verify"]; ok && (cfg.Engine == ClickHouse || cfg.Engine == Redis) {
		skip, err := urlBool("skip_verify", q.Get("skip_verify"))
		if err != nil {
			return cfg, err
		}
		if cfg.TLS == "" || cfg.TLS == TLSDisable {
			return cfg, fmt.Errorf("the URL's skip_verify needs TLS")
		}
		if skip {
			cfg.TLS = TLSRequire
		}
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

// urlTLSParams are the TLS parameters each scheme's tools take, and the
// modes their values mean to that scheme's driver: MySQL's tls=true and
// ClickHouse's secure=true verify the server, as rediss:// does, unless
// skip_verify says otherwise.
var urlTLSParams = map[Engine]map[string]map[string]TLSMode{
	Postgres: {
		"sslmode": {"disable": TLSDisable, "allow": TLSPrefer, "prefer": TLSPrefer, "require": TLSRequire,
			"verify-ca": TLSVerifyFull, "verify-full": TLSVerifyFull},
		"ssl": {"true": TLSRequire, "false": TLSDisable},
	},
	MySQL: {
		"ssl-mode": {"disabled": TLSDisable, "preferred": TLSPrefer, "required": TLSRequire,
			"verify_ca": TLSVerifyFull, "verify_identity": TLSVerifyFull},
		"tls": {"true": TLSVerifyFull, "skip-verify": TLSRequire, "preferred": TLSPrefer, "false": TLSDisable},
		"ssl": {"true": TLSRequire, "false": TLSDisable},
	},
	ClickHouse: {
		"secure": {"true": TLSVerifyFull, "false": TLSDisable},
	},
}

// urlTLS is the TLS mode a URL's parameters ask for, "" when they name
// none. A TLS parameter of another scheme, a value the scheme does not
// know, or parameters that disagree are an error: ignored, they would
// leave the connection less safe than the URL said.
func urlTLS(e Engine, q url.Values) (TLSMode, error) {
	params := urlTLSParams[e]
	var mode TLSMode
	var from string
	for _, key := range []string{"sslmode", "ssl-mode", "tls", "ssl", "secure"} {
		values, ok := q[key]
		if !ok {
			continue
		}
		meanings, known := params[key]
		if !known {
			return "", fmt.Errorf("%s is no %s URL parameter", key, e.Label())
		}
		for _, v := range values {
			value := strings.ToLower(v)
			if e == ClickHouse {
				// clickhouse-go reads secure with strconv.ParseBool, bare as true.
				b, err := urlBool(key, v)
				if err != nil {
					return "", err
				}
				value = strconv.FormatBool(b)
			}
			m, ok := meanings[value]
			if !ok {
				return "", fmt.Errorf("unknown %s=%s in the URL", key, v)
			}
			if mode != "" && m != mode {
				return "", fmt.Errorf("the URL's %s=%s disagrees with %s", key, v, from)
			}
			mode, from = m, key+"="+v
		}
	}
	return mode, nil
}

// urlBool reads a flag as clickhouse-go and rueidis do: bare means true,
// otherwise what strconv.ParseBool takes.
func urlBool(key, v string) (bool, error) {
	if v == "" {
		return true, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("unknown %s=%s in the URL", key, v)
	}
	return b, nil
}
