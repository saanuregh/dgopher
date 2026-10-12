package connection

import (
	"testing"
	"time"

	"dgopher/internal/db"
)

// A connection disconnects after its environment's idle time, its own,
// or never; DuckDB and SQLite never do.
func TestIdleConnectionLimit(t *testing.T) {
	cases := []struct {
		cfg  db.Config
		want time.Duration
	}{
		{db.Config{Engine: db.Postgres, Env: db.Production}, time.Hour},
		{db.Config{Engine: db.MySQL, Env: db.Staging}, 2 * time.Hour},
		{db.Config{Engine: db.ClickHouse, Env: db.Development}, 4 * time.Hour},
		{db.Config{Engine: db.Redis, Env: db.Production, IdleConnectionTimeout: 90}, 90 * time.Second},
		{db.Config{Engine: db.Postgres, Env: db.Production, IdleConnectionTimeout: -1}, 0},
		{db.Config{Engine: db.SQLite, Env: db.Production}, 0},
		{db.Config{Engine: db.DuckDB, Env: db.Development, IdleConnectionTimeout: 60}, 0},
	}
	for _, tc := range cases {
		if got := IdleConnectionLimit(&tc.cfg); got != tc.want {
			t.Errorf("%s %s %d: %s, want %s", tc.cfg.Engine, tc.cfg.Env, tc.cfg.IdleConnectionTimeout, got, tc.want)
		}
	}
}
