package db

import (
	"context"
	"testing"
	"time"

	"github.com/redis/rueidis"
)

func TestIntegrationRedisWireRecords(t *testing.T) {
	integration(t)
	ctx := context.Background()
	cfg := Config{Name: "wire-redis", Engine: Redis, Host: "127.0.0.1", Port: 16379, Database: "1"}
	l := captureSends(t, cfg.Name)
	k, err := OpenRedis(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	expectSent(t, l, "open", "PING", "COMMAND")

	if _, err := k.Do(ctx, []string{"SET", "wire:a", "1"}); err != nil {
		t.Fatal(err)
	}
	k.Do(ctx, []string{"GET", "wire:missing"})
	got := expectSent(t, l, "do", "SET wire:a 1", "GET wire:missing")
	if got[1].Err != nil {
		t.Fatalf("a nil reply recorded as %v", got[1].Err)
	}

	c := k.Client
	c.DoMulti(ctx, c.B().Get().Key("wire:a").Build(), c.B().Incr().Key("wire:a").Build())
	got = expectSent(t, l, "pipeline", "GET wire:a", "INCR wire:a")
	if got[0].Took != got[1].Took {
		t.Fatalf("a pipeline's commands took %v and %v", got[0].Took, got[1].Took)
	}

	k.Do(ctx, []string{"HSET", "wire:h", "f", "v"})
	l.take()
	if _, err := k.Do(ctx, []string{"INCR", "wire:h"}); err == nil {
		t.Fatal("INCR of a hash ran")
	}
	if got := expectSent(t, l, "error", "INCR wire:h"); got[0].Err == nil {
		t.Fatal("the error was not recorded")
	}

	err = c.Dedicated(func(dc rueidis.DedicatedClient) error {
		return dc.DoMulti(ctx, dc.B().Multi().Build(), dc.B().Incr().Key("wire:a").Build(), dc.B().Exec().Build())[2].Error()
	})
	if err != nil {
		t.Fatal(err)
	}
	expectSent(t, l, "dedicated", "MULTI", "INCR wire:a", "EXEC")

	sctx, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stop()
	c.Receive(sctx, c.B().Subscribe().Channel("wire:ch").Build(), func(rueidis.PubSubMessage) {})
	expectSent(t, l, "subscribe", "SUBSCRIBE wire:ch")

	for addr, n := range c.Nodes() {
		if n.Do(ctx, n.B().Ping().Build()).Error() != nil {
			t.Fatal(addr)
		}
	}
	expectSent(t, l, "node", "PING")

	mctx, stopMonitor := context.WithTimeout(ctx, 300*time.Millisecond)
	defer stopMonitor()
	k.Monitor(mctx, func(MonitorLine) {})
	expectSent(t, l, "monitor", "MONITOR")

	// A rename reaches the client, its nodes and MONITOR.
	renamed := cfg
	renamed.Name = "wire-redis-renamed"
	k.RecordAs(renamed)
	l = captureSends(t, renamed.Name)
	if _, err := k.Do(ctx, []string{"GET", "wire:a"}); err != nil {
		t.Fatal(err)
	}
	for _, n := range c.Nodes() {
		n.Do(ctx, n.B().Ping().Build())
	}
	mctx, stopMonitor = context.WithTimeout(ctx, 300*time.Millisecond)
	defer stopMonitor()
	k.Monitor(mctx, func(MonitorLine) {})
	expectSent(t, l, "after RecordAs", "GET wire:a", "PING", "MONITOR")
}

// MONITOR logs in as the client does, recorded as AUTH with its arguments,
// which the hook redacts.
func TestIntegrationRedisWireMonitorAuth(t *testing.T) {
	integration(t)
	ctx := context.Background()
	cfg := redisSentinel()
	cfg.Name = "wire-sentinel"
	l := captureSends(t, cfg.Name)
	k, err := OpenRedis(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	addrs, err := k.monitorAddrs(ctx)
	if err != nil || len(addrs) == 0 {
		t.Fatal(addrs, err)
	}
	l.take()
	mctx, stop := context.WithTimeout(ctx, 500*time.Millisecond)
	defer stop()
	k.Monitor(mctx, func(MonitorLine) {})
	auths, monitors := 0, 0
	for _, s := range l.take() {
		switch s.Redis[0] {
		case "AUTH":
			auths++
		case "MONITOR":
			monitors++
		}
	}
	if auths != len(addrs) || monitors != len(addrs) {
		t.Fatalf("%d AUTH and %d MONITOR for %d servers", auths, monitors, len(addrs))
	}
}
