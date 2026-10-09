package db

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// redis8 opens the server of Redis 8, with its modules and arrays.
func redis8(t *testing.T) *KV {
	t.Helper()
	integration(t)
	k, err := OpenRedis(context.Background(), Config{Name: "r8", Engine: Redis, Host: "127.0.0.1", Port: 16385}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	if _, err := k.Do(context.Background(), []string{"FLUSHDB"}); err != nil {
		t.Fatal(err)
	}
	return k
}

// TestIntegrationRedisModules reads a JSON document, an array and a
// vector set, page by page.
func TestIntegrationRedisModules(t *testing.T) {
	k := redis8(t)
	ctx := context.Background()
	do := func(args ...string) {
		t.Helper()
		if _, err := k.Do(ctx, args); err != nil {
			t.Fatal(args, err)
		}
	}
	do("JSON.SET", "doc", "$", `{"a":[1,2],"b":"x"}`)
	if doc, err := k.JSONDocument(ctx, "doc"); err != nil || doc != `{"a":[1,2],"b":"x"}` {
		t.Fatalf("document %q %v", doc, err)
	}
	if info, err := k.Info(ctx, "doc"); err != nil || info.Type != "ReJSON-RL" {
		t.Fatalf("info %+v %v", info, err)
	}

	// A sparse array: 25 values among 1000 indexes.
	for i := range 25 {
		do("ARSET", "arr", strconv.Itoa(i*40), "v"+strconv.Itoa(i))
	}
	if info, _ := k.Info(ctx, "arr"); info.Type != "array" || info.Length != 25 {
		t.Fatalf("array info %+v", info)
	}
	var pos ItemsPos
	var got []Field
	for pages := 0; ; pages++ {
		items, next, done, err := k.ReadItems(ctx, "arr", "array", pos, 10)
		if err != nil || pages > 5 {
			t.Fatalf("page %d: %v", pages, err)
		}
		got = append(got, items...)
		if pos = next; done {
			break
		}
	}
	if len(got) != 25 || got[24].Name != "960" || got[24].Value != "v24" {
		t.Fatalf("array read as %+v", got)
	}

	for i := range 12 {
		n := strconv.Itoa(i)
		do("VADD", "vec", "VALUES", "2", n, strconv.Itoa(12-i), "e"+n, "SETATTR", `{"n":`+n+`}`)
	}
	pos, got = ItemsPos{}, nil
	for {
		items, next, done, err := k.ReadItems(ctx, "vec", "vectorset", pos, 5)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, items...)
		if pos = next; done {
			break
		}
	}
	if len(got) != 12 || got[0].Name != "e0" || got[0].Value != `{"n":0}` {
		t.Fatalf("vector set read as %+v", got)
	}
	if info, _ := k.VectorSetInfo(ctx, "vec"); len(info) == 0 {
		t.Fatal("no vector set info")
	}
	if v, err := k.VectorOf(ctx, "vec", "e3"); err != nil || len(v) != 2 {
		t.Fatalf("vector %v %v", v, err)
	}
	if sim, err := k.Similar(ctx, "vec", "e3", 3); err != nil || len(sim) != 3 || sim[0].Score <= 0 {
		t.Fatalf("similar %+v %v", sim, err)
	}
}

// TestIntegrationRedisSearchAndSeries reads a search index, searches it,
// and reads a time series by buckets.
func TestIntegrationRedisSearchAndSeries(t *testing.T) {
	k := redis8(t)
	ctx := context.Background()
	for _, cmd := range [][]string{
		{"HSET", "doc:1", "title", "hello world", "n", "5"},
		{"HSET", "doc:2", "title", "goodbye", "n", "7"},
		{"FT.CREATE", "it_idx", "ON", "HASH", "PREFIX", "1", "doc:", "SCHEMA", "title", "TEXT", "n", "NUMERIC"},
		{"TS.CREATE", "temp", "LABELS", "room", "kitchen"},
	} {
		if _, err := k.Do(ctx, cmd); err != nil {
			t.Fatal(cmd, err)
		}
	}
	defer k.Do(ctx, []string{"FT.DROPINDEX", "it_idx"})
	for i := range 100 {
		k.Do(ctx, []string{"TS.ADD", "temp", strconv.Itoa(1000 + i*1000), strconv.Itoa(i)})
	}
	if !k.HasSearch() {
		t.Fatal("Redis 8 has search")
	}
	if names, err := k.SearchIndexes(ctx); err != nil || !slices.Contains(names, "it_idx") {
		t.Fatalf("indexes %v %v", names, err)
	}
	// Indexing is asynchronous: until both documents are.
	var info SearchIndex
	for start := time.Now(); info.Docs < 2 && time.Since(start) < 5*time.Second; time.Sleep(50 * time.Millisecond) {
		var err error
		if info, err = k.SearchIndexInfo(ctx, "it_idx"); err != nil {
			t.Fatal(err)
		}
	}
	if info.On != "HASH" || !slices.Equal(info.Prefixes, []string{"doc:"}) || len(info.Fields) != 2 || info.Fields[1].Type != "NUMERIC" || info.Docs != 2 {
		t.Fatalf("index %+v", info)
	}
	total, hits, err := k.Search(ctx, "it_idx", "hello", 10)
	if err != nil || total != 1 || hits[0].Key != "doc:1" || !slices.Contains(hits[0].Fields, [2]string{"title", "hello world"}) {
		t.Fatalf("search %d %+v %v", total, hits, err)
	}
	first, last, props, err := k.SeriesInfo(ctx, "temp")
	if err != nil || first.UnixMilli() != 1000 || last.UnixMilli() != 100_000 || !slices.Contains(props, Property{"labels", "room=kitchen"}) {
		t.Fatalf("series %v %v %+v %v", first, last, props, err)
	}
	samples, err := k.SeriesRange(ctx, "temp", first, last, 10*time.Second)
	if err != nil || len(samples) != 11 || samples[1].Value != 13.5 {
		t.Fatalf("10 s buckets: %+v %v", samples, err)
	}

	// A rule keeps its bucket; a series without samples has no times.
	k.Do(ctx, []string{"TS.CREATE", "temp_avg"})
	k.Do(ctx, []string{"TS.CREATERULE", "temp", "temp_avg", "AGGREGATION", "avg", "60000"})
	if _, _, props, _ := k.SeriesInfo(ctx, "temp"); !slices.ContainsFunc(props, func(p Property) bool {
		return p.Name == "rules" && strings.HasPrefix(p.Value, "temp_avg 60000 AVG")
	}) {
		t.Fatalf("rules %+v", props)
	}
	if first, last, _, err := k.SeriesInfo(ctx, "temp_avg"); err != nil || !first.IsZero() || !last.IsZero() {
		t.Fatalf("a series without samples: %v %v %v", first, last, err)
	}
}
