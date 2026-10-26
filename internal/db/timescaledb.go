package db

import (
	"context"
	"database/sql"
)

// timescaledbDialect is PostgreSQL's Dialect but for the engine it names, so that
// the rules found through it are TimescaleDB's.
type timescaledbDialect struct{ postgresDialect }

func (timescaledbDialect) Engine() Engine { return TimescaleDB }

// timescaledbEngine is TimescaleDB: PostgreSQL's rules but for its name and how it is
// reached by default, until it overrides what differs.
type timescaledbEngine struct{ postgresEngine }

var _ SQLRules = timescaledbEngine{}

func (timescaledbEngine) Engine() Engine       { return TimescaleDB }
func (timescaledbEngine) dialect() Dialect     { return timescaledbDialect{} }
func (timescaledbEngine) Label() string        { return "TimescaleDB" }
func (timescaledbEngine) Base() Engine         { return Postgres }
func (timescaledbEngine) ColorHex() string     { return "#3fa34d" }
func (timescaledbEngine) DefaultPort() int     { return 5432 }
func (timescaledbEngine) DefaultUser() string  { return "postgres" }
func (timescaledbEngine) URLSchemes() []string { return []string{"timescaledb"} }

// identify keeps a TimescaleDB connection TimescaleDB on a database that
// has not created the extension, when the server has it to create: the
// extension belongs to each database, the server stays TimescaleDB's.
func (timescaledbEngine) identify(ctx context.Context, q *sql.DB) (Engine, string, error) {
	e, v, err := identifyPostgresFamily(ctx, q)
	if err != nil || e != Postgres {
		return e, v, err
	}
	var available sql.NullString
	_ = q.QueryRowContext(ctx, `SELECT default_version FROM pg_available_extensions WHERE name = 'timescaledb'`).Scan(&available)
	if available.Valid {
		return TimescaleDB, available.String, nil
	}
	return e, v, nil
}
