package redis

import (
	"context"
	"testing"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"

	"github.com/egoist/mygo/ui"
)

// Opening a key audits that its value was read, once.
func TestOpeningKeyAuditsItsRead(t *testing.T) {
	testutil.Integration(t)
	a := dataview.NewFakeHost(t)
	cfg := db.Config{ID: "r", Name: "Cache", Engine: db.Redis, Host: "127.0.0.1", Port: 16379, Database: "2", Env: db.Staging}
	cn := a.AddConn(cfg)
	ctx := context.Background()
	kv, err := db.OpenRedis(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	kv.Do(ctx, []string{"FLUSHDB"})
	kv.Do(ctx, []string{"SET", "greeting", "hello"})
	kv.Do(ctx, []string{"HSET", "user:42", "name", "Ada", "plan", "pro"})
	kv.Close()
	tt := ui.NewTester(a.View, 1360, 860)
	a.Connect(cn, func() { a.AddTab(New(a, cn)) })
	testutil.WaitFor(t, tt, "redis tab", func() bool { return len(a.Tabs) == 1 })
	r := a.Tabs[0].(*Tab)
	testutil.WaitFor(t, tt, "keys", func() bool { return len(r.keys) == 2 })

	read := func(key, wantType string) []audit.Event {
		before := len(a.Events)
		r.open(key)
		testutil.WaitFor(t, tt, key, func() bool { return !r.loadingKey && r.info.Type == wantType })
		return a.Events[before:]
	}
	events := read("greeting", "string")
	want := "read the value of string key greeting"
	if len(events) != 1 || events[0].Kind != audit.KindCommand || events[0].Detail != want || events[0].Statement != "" || events[0].Origin != audit.OriginApp {
		t.Fatalf("opening a string audited %+v, want one %q", events, want)
	}
	events = read("user:42", "hash")
	want = "read the value of hash key user:42"
	if len(events) != 1 || events[0].Detail != want || events[0].Rows != 2 {
		t.Fatalf("opening a hash audited %+v, want one %q of 2 rows", events, want)
	}
}
