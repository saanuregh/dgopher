package db

import (
	"context"
	"slices"
	"testing"

	"dgopher/internal/testutil/redisserver"
)

// TestIntegrationRedisCommandDocs reads the commands' help as redis-cli
// shows it.
func TestIntegrationRedisCommandDocs(t *testing.T) {
	integration(t)
	rs := redisserver.Get(t)
	ctx := context.Background()
	k, err := OpenRedis(ctx, Config{Name: "r", Engine: Engine(rs.Engine), Host: redisserver.Host, Port: rs.Port}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	docs, err := k.CommandDocs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for name, syntax := range map[string]string{
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
	// Valkey's SET can also set only while the value is, or is not, one
	// given (IFEQ, and from 9.2 IFNE): compose.yaml's Valkey is 9.2.
	t.Run("SET", func(t *testing.T) {
		syntax := "key value [NX | XX] [GET] [EX seconds | PX milliseconds | EXAT unix-time-seconds | PXAT unix-time-milliseconds | KEEPTTL]"
		if rs.Valkey() {
			syntax = "key value [NX | XX | IFEQ comparison-value | IFNE comparison-not-equal] [GET] [EX seconds | PX milliseconds | EXAT unix-time-seconds | PXAT unix-time-milliseconds | KEEPTTL]"
		}
		if got := docs["SET"].Syntax; got != syntax {
			t.Errorf("SET: %q, want %q", got, syntax)
		}
	})
}
