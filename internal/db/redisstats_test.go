package db

import (
	"bufio"
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseMonitorLine(t *testing.T) {
	l, ok := parseMonitorLine(`1339518083.107412 [0 127.0.0.1:60866] "SET" "a b" "1"`)
	if !ok || l.DB != "0" || l.Client != "127.0.0.1:60866" || l.Command != `"SET" "a b" "1"` || l.At.Unix() != 1339518083 {
		t.Fatalf("%+v %v", l, ok)
	}
	if _, ok := parseMonitorLine("OK"); ok {
		t.Fatal("a reply read as a command")
	}
}

// A long line keeps its start; the next line reads whole after it.
func TestReadLine(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 10_000)+"\r\nnext\r\n"), 16)
	if line, err := readLine(r, 100); err != nil || line != strings.Repeat("x", 100) {
		t.Fatalf("%d bytes, %v", len(line), err)
	}
	if line, err := readLine(r, 100); err != nil || line != "next" {
		t.Fatalf("%q, %v", line, err)
	}
}

// TestIntegrationRedisStats watches commands with MONITOR, reads the slow
// log, and analyses the keys' memory.
func TestIntegrationRedisStats(t *testing.T) {
	integration(t)
	ctx := context.Background()
	k, err := OpenRedis(ctx, Config{Name: "r", Engine: Redis, Host: "127.0.0.1", Port: 16379, Database: "5"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if _, err := k.Do(ctx, []string{"FLUSHDB"}); err != nil {
		t.Fatal(err)
	}

	mctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	seen := make(chan MonitorLine, 100)
	done := make(chan error, 1)
	go func() { done <- k.Monitor(mctx, func(l MonitorLine) { seen <- l }) }()
	marker := "monitor-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	for found := false; !found; {
		k.Do(ctx, []string{"SET", marker, "1"})
		select {
		case l := <-seen:
			found = strings.Contains(l.Command, marker) && l.DB == "5"
		case <-time.After(200 * time.Millisecond):
		case <-mctx.Done():
			t.Fatal("MONITOR did not tell the command")
		}
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("MONITOR stopped with %v", err)
	}

	// Every command is slow for a moment.
	was, err := k.Do(ctx, []string{"CONFIG", "GET", "slowlog-log-slower-than"})
	if err != nil {
		t.Fatal(err)
	}
	k.Do(ctx, []string{"CONFIG", "SET", "slowlog-log-slower-than", "0"})
	k.Do(ctx, []string{"SET", "slow:" + marker, "1"})
	k.Do(ctx, []string{"CONFIG", "SET", "slowlog-log-slower-than", was.([]any)[1].(string)})
	entries, err := k.SlowLog(ctx, 128)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		found = found || len(e.Args) > 1 && e.Args[1] == "slow:"+marker
	}
	if !found {
		t.Fatalf("the slow command is not in the slow log: %+v", entries)
	}

	k.Do(ctx, []string{"FLUSHDB"})
	for i := range 30 {
		n := strconv.Itoa(i)
		k.Do(ctx, []string{"SET", "cache:" + n, strings.Repeat("x", 100), "EX", "600"})
		k.Do(ctx, []string{"HSET", "user:" + n, "name", "ada"})
	}
	k.Do(ctx, []string{"SET", "big", strings.Repeat("y", 50_000)})
	r, err := k.AnalyseMemory(ctx, ":", 1000, 5, func(int64) {})
	if err != nil {
		t.Fatal(err)
	}
	if r.Keys != 61 || r.Total != 61 || r.Expiring != 30 || r.ExpiringWithin[0] != 30 || r.Largest[0].Key != "big" || len(r.Largest) != 5 {
		t.Fatalf("report %+v", r)
	}
	if ns := r.Namespaces; len(ns) != 3 || ns[0].Prefix != "" || ns[0].Keys != 1 || ns[1].Prefix != "cache" || ns[1].Keys != 30 {
		t.Fatalf("namespaces %+v", ns)
	}
	if r.ByType["hash"].Keys != 30 || r.ByType["string"].Keys != 31 {
		t.Fatalf("by type %+v", r.ByType)
	}
	if r, err := k.AnalyseMemory(ctx, ":", 10, 5, func(int64) {}); err != nil || r.Keys != 10 {
		t.Fatalf("limited to 10: %+v %v", r, err)
	}
}
