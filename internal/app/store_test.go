package app

import (
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// A connection holds the store its family opens, read through DB() or
// KV(), and none once disconnected or when the open failed.
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
		if cn.Store == nil || cn.DB() == nil || cn.KV() != nil {
			t.Fatalf("store %v db %v kv %v", cn.Store != nil, cn.DB() != nil, cn.KV() != nil)
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
		if cn.Store == nil || cn.KV() == nil || cn.DB() != nil {
			t.Fatalf("store %v kv %v db %v", cn.Store != nil, cn.KV() != nil, cn.DB() != nil)
		}
		disconnected(t, a, cn)
	})
}
