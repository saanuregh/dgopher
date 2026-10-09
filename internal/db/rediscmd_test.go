package db

import (
	"context"
	"slices"
	"testing"
)

// TestIntegrationRedisCommandDocs reads the commands' help as redis-cli
// shows it.
func TestIntegrationRedisCommandDocs(t *testing.T) {
	integration(t)
	ctx := context.Background()
	k, err := OpenRedis(ctx, Config{Name: "r", Engine: Redis, Host: "127.0.0.1", Port: 16379}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	docs, err := k.CommandDocs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for name, syntax := range map[string]string{
		"SET":        "key value [NX | XX] [GET] [EX seconds | PX milliseconds | EXAT unix-time-seconds | PXAT unix-time-milliseconds | KEEPTTL]",
		"HSET":       "key field value [field value ...]",
		"CONFIG GET": "parameter [parameter ...]",
		"DEL":        "key [key ...]",
	} {
		if got := docs[name].Syntax; got != syntax {
			t.Errorf("%s: %q, want %q", name, got, syntax)
		}
	}
	if d, ok := DocFor(docs, []string{"config", "get", "x"}); !ok || d.Name != "CONFIG GET" || d.Summary == "" {
		t.Errorf("CONFIG GET's help: %+v", d)
	}
	// Newer servers have more HGET commands: these two, in order, first.
	got := CompleteCommand(docs, "hge")
	if len(got) < 2 || got[0] != "HGET" || got[1] != "HGETALL" || !slices.IsSorted(got) || slices.ContainsFunc(got, func(n string) bool { return n[:3] != "HGE" }) {
		t.Errorf("completions of hge: %v", got)
	}
}
