package db

import (
	"context"
	"testing"
	"time"
)

// TestIntegrationRedisPubSubAndGroups listens to a channel and a pattern,
// and reads a stream's consumer groups.
func TestIntegrationRedisPubSubAndGroups(t *testing.T) {
	integration(t)
	ctx := context.Background()
	k, err := OpenRedis(ctx, Config{Name: "r", Engine: Redis, Host: "127.0.0.1", Port: 16379, Database: "6"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()

	sctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	got := make(chan PubSubMessage, 10)
	done := make(chan error, 1)
	go func() {
		done <- k.Subscribe(sctx, []string{"news"}, []string{"orders.*"}, func(m PubSubMessage) { got <- m })
	}()
	// The subscriptions take a moment: publish until both are heard.
	heard := map[string]bool{}
	for len(heard) < 2 {
		k.Do(ctx, []string{"PUBLISH", "news", "hello"})
		k.Do(ctx, []string{"PUBLISH", "orders.eu", "42"})
		select {
		case m := <-got:
			heard[m.Channel+"|"+m.Pattern+"|"+m.Message] = true
		case <-time.After(100 * time.Millisecond):
		case <-sctx.Done():
			t.Fatalf("heard %v", heard)
		}
	}
	if !heard["news||hello"] || !heard["orders.eu|orders.*|42"] {
		t.Fatalf("heard %v", heard)
	}
	channels, err := k.Channels(ctx, "*")
	if err != nil || len(channels) == 0 || channels[0].Subscribers < 1 {
		t.Fatalf("channels %+v %v", channels, err)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("subscription ended with %v", err)
	}

	for _, cmd := range [][]string{
		{"DEL", "jobs"}, {"XADD", "jobs", "1-1", "n", "1"}, {"XADD", "jobs", "1-2", "n", "2"},
		{"XGROUP", "CREATE", "jobs", "workers", "0"},
		{"XREADGROUP", "GROUP", "workers", "w1", "COUNT", "1", "STREAMS", "jobs", ">"},
	} {
		if _, err := k.Do(ctx, cmd); err != nil {
			t.Fatal(cmd, err)
		}
	}
	groups, err := k.StreamGroups(ctx, "jobs")
	if err != nil || len(groups) != 1 || groups[0].Name != "workers" || groups[0].Pending != 1 || groups[0].Lag != 1 || groups[0].LastDelivered != "1-1" {
		t.Fatalf("groups %+v %v", groups, err)
	}
	consumers, err := k.StreamConsumers(ctx, "jobs", "workers")
	if err != nil || len(consumers) != 1 || consumers[0].Name != "w1" || consumers[0].Pending != 1 {
		t.Fatalf("consumers %+v %v", consumers, err)
	}
	pending, err := k.PendingEntries(ctx, "jobs", "workers", 10)
	if err != nil || len(pending) != 1 || pending[0].ID != "1-1" || pending[0].Consumer != "w1" || pending[0].Deliveries != 1 {
		t.Fatalf("pending %+v %v", pending, err)
	}
}
