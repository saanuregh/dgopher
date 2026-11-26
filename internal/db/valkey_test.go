package db

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Valkey is Redis's flavor: its own name and URLs, Redis's rules.
func TestValkeyEngine(t *testing.T) {
	if Valkey.Base() != Redis || !Valkey.IsKeyValue() || Valkey.Mark() != "VK" {
		t.Fatalf("base %q, key-value %v, mark %q", Valkey.Base(), Valkey.IsKeyValue(), Valkey.Mark())
	}
	if i := slices.Index(Engines(), Valkey); i < 0 || Engines()[i-1] != Redis {
		t.Fatalf("Valkey is not right after Redis in %v", Engines())
	}
	r, ok := KeyValue(Valkey)
	if !ok || r.DefaultKeySeparator() != DefaultKeySeparator {
		t.Fatalf("key-value rules %v, separator %q", ok, r.DefaultKeySeparator())
	}
}

func TestValkeyURL(t *testing.T) {
	cases := map[string]Config{
		"valkey://:pw@cache:6380/2":       {Engine: Valkey, Host: "cache", Port: 6380, Password: "pw", Database: "2", Name: "cache/2"},
		"valkeys://cache/0":               {Engine: Valkey, Host: "cache", Database: "0", TLS: TLSVerifyFull, Name: "cache/0"},
		"valkeys://cache/0?skip_verify=1": {Engine: Valkey, Host: "cache", Database: "0", TLS: TLSRequire, Name: "cache/0"},
		"redis://cache/0":                 {Engine: Redis, Host: "cache", Database: "0", Name: "cache/0"},
	}
	for in, want := range cases {
		got, err := ParseURL(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %+v %v, want %+v", in, got, err, want)
		}
	}
}

// A Valkey connection is checked as a Redis one: its database index and
// Sentinel; but a Valkey cluster may hold databases, from Valkey 9.
func TestValkeyConfigRedisRules(t *testing.T) {
	cluster := Config{Name: "v", Engine: Valkey, Host: "h", Database: "3", Redis: RedisConfig{Mode: RedisCluster}}
	if err := cluster.Validate(); err != nil {
		t.Fatalf("a Valkey cluster's database 3: %v", err)
	}
	cluster.Engine = Redis
	if err := cluster.Validate(); err == nil || !strings.Contains(err.Error(), "a Redis cluster has only database 0") {
		t.Fatalf("a Redis cluster's database 3: %v", err)
	}
	sentinel := Config{Name: "v", Engine: Valkey, Host: "h", Redis: RedisConfig{Mode: RedisSentinel}}
	if err := sentinel.Validate(); err == nil {
		t.Fatal("a Sentinel connection without its master was accepted")
	}
	sentinel.Redis.Master = "mymaster"
	if sentinel.DefaultPort() != SentinelPort {
		t.Fatalf("Sentinel port %d", sentinel.DefaultPort())
	}
	if err := (&Config{Name: "v", Engine: Valkey, Host: "h", Database: "x"}).Validate(); err == nil {
		t.Fatal("database x was accepted")
	}
}
