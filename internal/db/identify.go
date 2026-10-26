package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Identify reads what the server is, among the engines of the
// connection's family (PostgreSQL's or MySQL's), and its version: a
// connection may be set to one engine and reach another of its family.
// An engine with no others in its family is itself, with no version.
func (d *DB) Identify(ctx context.Context) (Engine, string, error) {
	r, ok := SQL(d.Config.Engine)
	if !ok {
		return "", "", fmt.Errorf("%s has no SQL rules", d.Config.Engine.Label())
	}
	return r.identify(ctx, d.SQL)
}

// identifyPostgresFamily reads a server of PostgreSQL's family: its
// version() text, and whether the database has TimescaleDB, which is an
// extension of each database.
func identifyPostgresFamily(ctx context.Context, q *sql.DB) (Engine, string, error) {
	var version string
	var timescale sql.NullString
	err := q.QueryRowContext(ctx, `SELECT version(), (SELECT extversion FROM pg_extension WHERE extname = 'timescaledb')`).Scan(&version, &timescale)
	if err != nil {
		// A server without pg_extension has no TimescaleDB.
		timescale = sql.NullString{}
		if err := q.QueryRowContext(ctx, `SELECT version()`).Scan(&version); err != nil {
			return "", "", err
		}
	}
	e, v := postgresServer(version, timescale.String)
	return e, v, nil
}

// identifyMySQLFamily reads a server of MySQL's family by its version and
// version comment.
func identifyMySQLFamily(ctx context.Context, q *sql.DB) (Engine, string, error) {
	var version, comment sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT VERSION(), @@version_comment`).Scan(&version, &comment); err != nil {
		return "", "", err
	}
	e, v := mysqlServer(version.String, comment.String)
	return e, v, nil
}

// postgresServer is the engine of PostgreSQL's family whose version()
// says version, with TimescaleDB's extension version when the database
// has it, and the engine's own version.
func postgresServer(version, timescale string) (Engine, string) {
	if rest, ok := strings.CutPrefix(version, "CockroachDB "); ok {
		// CockroachDB CCL v25.2.1 (x86_64-pc-linux-gnu, …)
		for _, f := range strings.Fields(rest) {
			if v, ok := strings.CutPrefix(f, "v"); ok {
				return CockroachDB, v
			}
		}
		return CockroachDB, ""
	}
	pg := strings.TrimPrefix(version, "PostgreSQL ")
	if i := strings.IndexAny(pg, " ,"); i >= 0 {
		pg = pg[:i]
	}
	if _, yb, ok := strings.Cut(pg, "-YB-"); ok {
		// PostgreSQL 15.2-YB-2.25.1.0-b0: YugabyteDB 2.25.1.0, build 0.
		v, _, _ := strings.Cut(yb, "-")
		return YugabyteDB, v
	}
	if timescale != "" {
		return TimescaleDB, timescale
	}
	return Postgres, pg
}

// mysqlServer is the engine of MySQL's family whose VERSION() says
// version, and @@version_comment comment, and the engine's own version.
func mysqlServer(version, comment string) (Engine, string) {
	if _, tidb, ok := strings.Cut(version, "-TiDB-v"); ok {
		// 8.0.11-TiDB-v8.5.1: TiDB 8.5.1, speaking as MySQL 8.0.11.
		v, _, _ := strings.Cut(tidb, "-")
		return TiDB, v
	}
	v, _, _ := strings.Cut(version, "-")
	if strings.Contains(strings.ToLower(version), "mariadb") || strings.Contains(strings.ToLower(comment), "mariadb") {
		return MariaDB, v
	}
	return MySQL, v
}
