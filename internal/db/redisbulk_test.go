package db

import (
	"bytes"
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestIntegrationRedisBulk exports keys of every type, binary values
// among them, imports them into another database as they were, and
// deletes those matching a pattern.
func TestIntegrationRedisBulk(t *testing.T) {
	integration(t)
	ctx := context.Background()
	open := func(index string) *KV {
		k, err := OpenRedis(ctx, Config{Name: "r", Engine: Redis, Host: "127.0.0.1", Port: 16379, Database: index}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { k.Close() })
		if _, err := k.Do(ctx, []string{"FLUSHDB"}); err != nil {
			t.Fatal(err)
		}
		return k
	}
	from, to := open("3"), open("4")
	for _, cmd := range [][]string{
		{"SET", "app:s", "hello", "PX", "500000"},
		{"SET", "app:bin", "\xff\x00\x01"},
		{"RPUSH", "app:l", "a", "b", "a"},
		{"SADD", "app:set", "x", "y"},
		{"HSET", "app:h", "f", "v", "g", "w"},
		{"ZADD", "app:z", "1.5", "m", "-2", "n", "+inf", "top"},
		{"XADD", "app:x", "1-1", "b", "2", "a", "1"},
		{"SET", "other", "kept"},
	} {
		if _, err := from.Do(ctx, cmd); err != nil {
			t.Fatal(cmd, err)
		}
	}
	var buf bytes.Buffer
	written, skipped, err := from.ExportKeys(ctx, "app:*", &buf, func(int64) {})
	if err != nil || written != 7 || skipped != 0 {
		t.Fatalf("exported %d, passed over %d: %v", written, skipped, err)
	}
	if !strings.Contains(buf.String(), `"encoding":"base64"`) {
		t.Fatalf("binary value not encoded:\n%s", buf.String())
	}
	dumps, err := ReadKeyDumps(&buf)
	if err != nil || len(dumps) != 7 {
		t.Fatalf("read %d dumps: %v", len(dumps), err)
	}
	if n, _, err := to.ImportKeys(ctx, dumps, false, func(int64) {}); err != nil || n != 7 {
		t.Fatalf("imported %d: %v", n, err)
	}
	if n, skipped, err := to.ImportKeys(ctx, dumps, false, func(int64) {}); err != nil || n != 0 || skipped != 7 {
		t.Fatalf("again without replacing: %d written, %d passed over, %v", n, skipped, err)
	}
	if n, _, err := to.ImportKeys(ctx, dumps, true, func(int64) {}); err != nil || n != 7 {
		t.Fatalf("replaced %d: %v", n, err)
	}
	for _, want := range dumps {
		got, ok, err := to.DumpKey(ctx, want.Key)
		if err != nil || !ok {
			t.Fatalf("%s: %v", want.Key, err)
		}
		if err := got.decode(); err != nil {
			t.Fatal(err)
		}
		if want.TTL > 0 && got.TTL > 0 && got.TTL <= want.TTL {
			got.TTL = want.TTL
		}
		got.Encoding, want.Encoding = "", ""
		// A set's members and a hash's fields come in no set order.
		for _, d := range []*KeyDump{&got, &want} {
			if d.Type == "set" {
				slices.Sort(d.Items)
			}
			slices.SortFunc(d.Fields, func(a, b [2]string) int { return strings.Compare(a[0], b[0]) })
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s imported as %+v, want %+v", want.Key, got, want)
		}
	}

	// What the server would refuse midway is refused before a key is
	// touched: a transaction does not undo a command that ran.
	for _, line := range []string{
		`{"key":"app:x","type":"stream","entries":[{"id":"2-1","fields":[["a","1"]]},{"id":"1-1","fields":[["a","2"]]}]}`,
		`{"key":"app:z","type":"zset","members":[{"member":"m","score":"many"}]}`,
	} {
		if _, err := ReadKeyDumps(strings.NewReader(line)); err == nil {
			t.Errorf("%s: read", line)
		}
	}

	n, sample, err := from.CountMatching(ctx, "app:*", 3)
	if err != nil || n != 7 || len(sample) != 3 {
		t.Fatalf("counted %d, %v: %v", n, sample, err)
	}
	if gone, err := from.DeleteMatching(ctx, "app:*", func(int64) {}); err != nil || gone != 7 {
		t.Fatalf("deleted %d: %v", gone, err)
	}
	if n, _ := from.DBSize(ctx); n != 1 {
		t.Fatalf("%d keys left, want the one not matching", n)
	}
}
