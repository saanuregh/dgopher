package db

// Characterization pins for the engine families (K1): what each Redis
// comparison of this package decides today, for every engine and for an
// ID no build knows. When a comparison moves behind Kind() or
// KeyValue(e), re-point the closure and leave the pinned values alone.

import (
	"cmp"
	"context"
	"fmt"
	"testing"
)

// A database index is checked only for Redis, and database 0 alone for
// a Redis cluster (config.go: c.Engine == Redis && c.Database != "").
func TestPinRedisDatabaseIndexChecked(t *testing.T) {
	pinValues(t, "redis database index", map[string]string{
		"postgres word":              "ok",
		"postgres negative":          "ok",
		"postgres index 1 cluster":   "ok",
		"mysql word":                 "ok",
		"mysql negative":             "ok",
		"mysql index 1 cluster":      "ok",
		"clickhouse word":            "ok",
		"clickhouse negative":        "ok",
		"clickhouse index 1 cluster": "ok",
		"sqlite word":                "ok",
		"sqlite negative":            "ok",
		"sqlite index 1 cluster":     "ok",
		"duckdb word":                "ok",
		"duckdb negative":            "ok",
		"duckdb index 1 cluster":     "ok",
		"redis word":                 "error: the Redis database must be a number such as 0",
		"redis negative":             "error: the Redis database must be a number such as 0",
		"redis index 1 cluster":      "error: a Redis cluster has only database 0",
		"unknown word":               "error: unknown engine \"unknown\"",
		"unknown negative":           "error: unknown engine \"unknown\"",
		"unknown index 1 cluster":    "error: unknown engine \"unknown\"",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			for _, c := range []struct {
				name, database string
				mode           RedisMode
			}{
				{"word", "two", RedisStandalone},
				{"negative", "-1", RedisStandalone},
				{"index 1 cluster", "1", RedisCluster},
			} {
				cfg := Config{Name: "n", Engine: e, Host: "h", Database: c.database, Redis: RedisConfig{Mode: c.mode}}
				add(string(e)+" "+c.name, errText(cfg.Validate()))
			}
		}
	})
}

// A Redis topology, its sentinel master and its other nodes are checked
// only for Redis (config.go: if c.Engine == Redis).
func TestPinRedisTopologyChecked(t *testing.T) {
	pinValues(t, "redis topology", map[string]string{
		"postgres unknown mode":              "ok",
		"postgres sentinel without master":   "ok",
		"postgres bad node":                  "ok",
		"mysql unknown mode":                 "ok",
		"mysql sentinel without master":      "ok",
		"mysql bad node":                     "ok",
		"clickhouse unknown mode":            "ok",
		"clickhouse sentinel without master": "ok",
		"clickhouse bad node":                "ok",
		"sqlite unknown mode":                "ok",
		"sqlite sentinel without master":     "ok",
		"sqlite bad node":                    "ok",
		"duckdb unknown mode":                "ok",
		"duckdb sentinel without master":     "ok",
		"duckdb bad node":                    "ok",
		"redis unknown mode":                 "error: unknown Redis mode \"ring\"",
		"redis sentinel without master":      "error: the name of the master the sentinels watch is required",
		"redis bad node":                     "error: the node \"nohostport\" is not host:port",
		"unknown unknown mode":               "error: unknown engine \"unknown\"",
		"unknown sentinel without master":    "error: unknown engine \"unknown\"",
		"unknown bad node":                   "error: unknown engine \"unknown\"",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			for _, c := range []struct {
				name  string
				redis RedisConfig
			}{
				{"unknown mode", RedisConfig{Mode: "ring"}},
				{"sentinel without master", RedisConfig{Mode: RedisSentinel}},
				{"bad node", RedisConfig{Nodes: "nohostport"}},
			} {
				cfg := Config{Name: "n", Engine: e, Host: "h", Database: "0", Redis: c.redis}
				add(string(e)+" "+c.name, errText(cfg.Validate()))
			}
		}
	})
}

// The default port follows Sentinel only for Redis (config.go:
// c.Engine == Redis && c.Redis.Mode == RedisSentinel).
func TestPinSentinelDefaultPort(t *testing.T) {
	pinValues(t, "sentinel default port", map[string]string{
		"postgres standalone":   "default=5432 port=5432",
		"postgres cluster":      "default=5432 port=5432",
		"postgres sentinel":     "default=5432 port=5432",
		"mysql standalone":      "default=3306 port=3306",
		"mysql cluster":         "default=3306 port=3306",
		"mysql sentinel":        "default=3306 port=3306",
		"clickhouse standalone": "default=9000 port=9000",
		"clickhouse cluster":    "default=9000 port=9000",
		"clickhouse sentinel":   "default=9000 port=9000",
		"sqlite standalone":     "default=0 port=0",
		"sqlite cluster":        "default=0 port=0",
		"sqlite sentinel":       "default=0 port=0",
		"duckdb standalone":     "default=0 port=0",
		"duckdb cluster":        "default=0 port=0",
		"duckdb sentinel":       "default=0 port=0",
		"redis standalone":      "default=6379 port=6379",
		"redis cluster":         "default=6379 port=6379",
		"redis sentinel":        "default=26379 port=26379",
		"unknown standalone":    "default=0 port=0",
		"unknown cluster":       "default=0 port=0",
		"unknown sentinel":      "default=0 port=0",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			for _, mode := range []RedisMode{RedisStandalone, RedisCluster, RedisSentinel} {
				cfg := Config{Engine: e, Redis: RedisConfig{Mode: mode}}
				add(fmt.Sprintf("%s %s", e, cmp.Or(string(mode), "standalone")), fmt.Sprintf("default=%d port=%d", cfg.DefaultPort(), cfg.port()))
			}
		}
	})
}

// OpenRedis takes Redis connections only (redis.go: cfg.Engine != Redis);
// past that check, a Redis connection without a host stops at Validate.
func TestPinOpenRedisEngineCheck(t *testing.T) {
	pinValues(t, "open redis", map[string]string{
		"postgres":   "error: PostgreSQL is not Redis",
		"mysql":      "error: MySQL is not Redis",
		"clickhouse": "error: ClickHouse is not Redis",
		"sqlite":     "error: SQLite is not Redis",
		"duckdb":     "error: DuckDB is not Redis",
		"redis":      "error: a host is required",
		"unknown":    "error: unknown is not Redis",
	}, func(add func(key, got string)) {
		for _, e := range pinEngines {
			k, err := OpenRedis(context.Background(), Config{Name: "n", Engine: e}, nil)
			if k != nil {
				k.Close()
			}
			add(string(e), errText(err))
		}
	})
}

// A URL's skip_verify is read for ClickHouse and Redis only (url.go:
// cfg.Engine == ClickHouse || cfg.Engine == Redis), with TLS on as each
// scheme turns it on.
func TestPinURLSkipVerify(t *testing.T) {
	urls := []string{
		"postgres://h/d?sslmode=verify-full&skip_verify=true",
		"postgres://h/d?sslmode=verify-full&skip_verify=maybe",
		"postgres://h/d?skip_verify=true",
		"mysql://h/d?ssl-mode=VERIFY_IDENTITY&skip_verify=true",
		"mysql://h/d?ssl-mode=VERIFY_IDENTITY&skip_verify=maybe",
		"clickhouse://h/d?secure=true&skip_verify=true",
		"clickhouse://h/d?secure=true&skip_verify=false",
		"sqlite:///tmp/a.db?skip_verify=true",
		"duckdb:///tmp/a.duckdb?skip_verify=true",
		"rediss://h/0?skip_verify=true",
		"rediss://h/0?skip_verify=false",
		"rediss://h/0?skip_verify=maybe",
	}
	pinValues(t, "url skip_verify", map[string]string{
		"postgres://h/d?sslmode=verify-full&skip_verify=true":    "engine=postgres tls=\"verify-full\"",
		"postgres://h/d?sslmode=verify-full&skip_verify=maybe":   "engine=postgres tls=\"verify-full\"",
		"postgres://h/d?skip_verify=true":                        "engine=postgres tls=\"\"",
		"mysql://h/d?ssl-mode=VERIFY_IDENTITY&skip_verify=true":  "engine=mysql tls=\"verify-full\"",
		"mysql://h/d?ssl-mode=VERIFY_IDENTITY&skip_verify=maybe": "engine=mysql tls=\"verify-full\"",
		"clickhouse://h/d?secure=true&skip_verify=true":          "engine=clickhouse tls=\"require\"",
		"clickhouse://h/d?secure=true&skip_verify=false":         "engine=clickhouse tls=\"verify-full\"",
		"sqlite:///tmp/a.db?skip_verify=true":                    "engine=sqlite tls=\"\"",
		"duckdb:///tmp/a.duckdb?skip_verify=true":                "engine=duckdb tls=\"\"",
		"rediss://h/0?skip_verify=true":                          "engine=redis tls=\"require\"",
		"rediss://h/0?skip_verify=false":                         "engine=redis tls=\"verify-full\"",
		"rediss://h/0?skip_verify=maybe":                         "error: unknown skip_verify=maybe in the URL",
	}, func(add func(key, got string)) {
		for _, u := range urls {
			cfg, err := ParseURL(u)
			if err != nil {
				add(u, errText(err))
				continue
			}
			add(u, fmt.Sprintf("engine=%s tls=%q", cfg.Engine, cfg.TLS))
		}
	})
}
