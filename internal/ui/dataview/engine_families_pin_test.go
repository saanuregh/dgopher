package dataview

// Characterization pins for the engine families: how FakeHost opens a
// connection, by engine. When the code behind the choice moves, leave the
// pinned values alone.

import (
	"fmt"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// fakeHostConnect connects cfg through a FakeHost and says how it ended:
// the error, or which kind of store the connection holds.
func fakeHostConnect(t *testing.T, cfg db.Config) string {
	t.Helper()
	h := NewFakeHost(t)
	cn := h.AddConn(cfg)
	tt := ui.NewTester(h.View, 600, 400)
	h.ConnectInline(cn, nil)
	testutil.WaitFor(t, tt, "the connect", func() bool { return cn.Status != connection.StatusConnecting })
	if cn.Status != connection.StatusConnected {
		return "error: " + cn.Err
	}
	return fmt.Sprintf("connected sql=%v redis=%v", cn.DB() != nil, cn.KV() != nil)
}

// FakeHost opens each engine with its family's opener (db.OpenStore): a
// server engine without a host stops at Validate in its own opener, which
// another opener's engine check would stop first.
func TestPinFakeHostOpensByEngine(t *testing.T) {
	pinValues(t, "fake host open", map[string]string{
		"postgres":   "error: a host is required",
		"mysql":      "error: a host is required",
		"clickhouse": "error: a host is required",
		"sqlite":     "connected sql=true redis=false",
		"duckdb":     "connected sql=true redis=false",
		"redis":      "error: a host is required",
		"unknown":    "error: unknown engine \"unknown\"; a host is required",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			cfg := db.Config{ID: "c", Name: "c", Engine: e}
			if e.IsFile() {
				cfg.Database = ":memory:"
			}
			add(string(e), fakeHostConnect(t, cfg))
		}
	})
}

// A Redis connection through FakeHost holds a Redis client and no pool.
func TestPinIntegrationFakeHostOpensRedis(t *testing.T) {
	testutil.Integration(t)
	got := fakeHostConnect(t, db.Config{ID: "rd", Name: "rd", Engine: db.Redis, Host: "127.0.0.1", Port: 16379})
	if got != "connected sql=false redis=true" {
		t.Fatalf("FakeHost Redis: %s", got)
	}
}
