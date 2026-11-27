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
	"dgopher/internal/testutil/mongoserver"
	"dgopher/internal/testutil/redisserver"

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
	return fmt.Sprintf("connected sql=%v redis=%v doc=%v", cn.DB() != nil, cn.KV() != nil, cn.Doc() != nil)
}

// FakeHost opens each engine with its family's opener (db.OpenStore): a
// server engine without a host stops at Validate in its own opener, which
// another opener's engine check would stop first.
func TestPinFakeHostOpensByEngine(t *testing.T) {
	pinValues(t, "fake host open", map[string]string{
		"postgres":   "error: a host is required",
		"mysql":      "error: a host is required",
		"clickhouse": "error: a host is required",
		"sqlite":     "connected sql=true redis=false doc=false",
		"duckdb":     "connected sql=true redis=false doc=false",
		"redis":      "error: a host is required",
		"mongodb":    "error: a host is required",
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
	rs := redisserver.Get(t)
	got := fakeHostConnect(t, db.Config{ID: "rd", Name: "rd", Engine: db.Engine(rs.Engine), Host: redisserver.Host, Port: rs.Port})
	if got != "connected sql=false redis=true doc=false" {
		t.Fatalf("FakeHost Redis: %s", got)
	}
}

// A MongoDB connection through FakeHost holds a document client and no
// pool.
func TestPinIntegrationFakeHostOpensMongo(t *testing.T) {
	cfg, err := db.ParseURL(mongoserver.Primary(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ID, cfg.Name = "mg", "mg"
	if got := fakeHostConnect(t, cfg); got != "connected sql=false redis=false doc=true" {
		t.Fatalf("FakeHost MongoDB: %s", got)
	}
}
