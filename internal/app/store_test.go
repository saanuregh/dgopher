package app

import (
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/testutil/mongoserver"

	"github.com/egoist/mygo/ui"
)

// A connection holds the store its family opens, read through DB(), KV()
// or Doc(), and none once disconnected or when the open failed.
func TestConnHoldsOneStore(t *testing.T) {
	disconnected := func(t *testing.T, a *App, cn *connection.Conn) {
		t.Helper()
		a.disconnect(cn)
		if cn.Store != nil {
			t.Fatal("a store is held after the disconnect")
		}
	}
	t.Run("sql", func(t *testing.T) {
		a := newTestApp(t)
		tt := ui.NewTester(a.view, 1000, 700)
		cn, _ := liteConn(t, a, tt)
		if cn.Store == nil || cn.DB() == nil || cn.KV() != nil || cn.Doc() != nil {
			t.Fatalf("store %v db %v kv %v doc %v", cn.Store != nil, cn.DB() != nil, cn.KV() != nil, cn.Doc() != nil)
		}
		disconnected(t, a, cn)
	})
	t.Run("failed", func(t *testing.T) {
		a := newTestApp(t)
		tt := ui.NewTester(a.view, 1000, 700)
		cn := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "127.0.0.1", Port: 1, User: "x", Database: "x"})
		a.open(cn, cn.Config, false)
		testutil.WaitFor(t, tt, "the open to fail", func() bool { return cn.Status == connection.StatusFailed })
		if cn.Store != nil {
			t.Fatal("a store is held after a failed open")
		}
	})
	t.Run("redis", func(t *testing.T) {
		testutil.Integration(t)
		a := newTestApp(t)
		tt := ui.NewTester(a.view, 1000, 700)
		cn := redisConn(t, a, tt)
		if cn.Store == nil || cn.KV() == nil || cn.DB() != nil || cn.Doc() != nil {
			t.Fatalf("store %v kv %v db %v doc %v", cn.Store != nil, cn.KV() != nil, cn.DB() != nil, cn.Doc() != nil)
		}
		disconnected(t, a, cn)
	})
	t.Run("mongo", func(t *testing.T) {
		cfg, err := db.ParseURL(mongoserver.Primary(t))
		if err != nil {
			t.Fatal(err)
		}
		cfg.ID, cfg.Name = "mg", "mg"
		a := newTestApp(t)
		tt := ui.NewTester(a.view, 1000, 700)
		cn := addConn(a, cfg)
		connectNow(t, a, tt, cn)
		if cn.Store == nil || cn.Doc() == nil || cn.DB() != nil || cn.KV() != nil {
			t.Fatalf("store %v doc %v db %v kv %v", cn.Store != nil, cn.Doc() != nil, cn.DB() != nil, cn.KV() != nil)
		}
		settle(t, a, tt)
		if len(cn.Schemas) != 0 || len(cn.Loading) != 0 {
			t.Fatalf("schemas read for MongoDB: %v, loading %v", cn.Schemas, cn.Loading)
		}
		disconnected(t, a, cn)
	})
}
