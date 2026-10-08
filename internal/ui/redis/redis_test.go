package redis

import (
	"context"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"

	"github.com/egoist/mygo/ui"
)

func TestRedisBrowser(t *testing.T) {
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
	for i := range 40 {
		kv.Do(ctx, []string{"SET", "session:" + string(rune('a'+i%26)) + string(rune('0'+i/26)), `{"user":42,"roles":["admin"]}`, "EX", "3600"})
	}
	kv.Do(ctx, []string{"HSET", "user:42", "name", "Ada", "email", "ada@example.com", "plan", "pro"})
	kv.Do(ctx, []string{"RPUSH", "queue:emails", "welcome:42", "digest:7"})
	kv.Close()
	tt := ui.NewTester(a.View, 1360, 860)
	a.Connect(cn, func() { a.AddTab(New(a, cn)) })
	testutil.WaitFor(t, tt, "redis tab", func() bool { return len(a.Tabs) == 1 })
	r := a.Tabs[0].(*Tab)

	testutil.WaitFor(t, tt, "keys", func() bool { return len(r.keys) == 42 })
	r.tree.Open.Add("user:")
	r.open("user:42")
	testutil.WaitFor(t, tt, "hash", func() bool { return len(r.fields) == 3 })
	r.runConsole(`HGET user:42 name`)
	testutil.WaitFor(t, tt, "console", func() bool { return len(r.consoleLog) == 2 })
	if r.consoleLog[1].text != `"Ada"` {
		t.Fatalf("console %+v", r.consoleLog)
	}
	testutil.Snapshot(t, tt, "redis")
}
