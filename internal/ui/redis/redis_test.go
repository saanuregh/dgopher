package redis

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

	// Another separator: no key has it, so none is in a folder.
	cn.Config.KeySeparator = "."
	tt.Frame()
	if n := len(r.children[""]); n != 42 {
		t.Fatalf("%d keys at the top with the separator '.', want 42", n)
	}
	cn.Config.KeySeparator = ""

	kv, err = db.OpenRedis(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	args := []string{"HSET", "big:hash"}
	for i := range 1200 {
		args = append(args, fmt.Sprintf("f%04d", i), "v")
	}
	kv.Do(ctx, args)
	kv.Do(ctx, []string{"SET", "big:string", strings.Repeat("x", stringStart+10)})

	r.open("big:hash")
	testutil.WaitFor(t, tt, "the hash's first page", func() bool { return !r.loadingKey && len(r.fields) > 0 })
	for !r.itemsDone {
		r.loadMore()
		testutil.WaitFor(t, tt, "a page", func() bool { return !r.loadingKey })
	}
	if len(r.fields) != 1200 || r.fields[0].Name != "f0000" || r.fields[1199].Name != "f1199" {
		t.Fatalf("%d fields read, in order: %s … %s", len(r.fields), r.fields[0].Name, r.fields[len(r.fields)-1].Name)
	}
	r.open("big:string")
	testutil.WaitFor(t, tt, "the string's start", func() bool { return !r.loadingKey && r.info.Type == "string" })
	if r.whole || len(r.value) != stringStart {
		t.Fatalf("a string past %d bytes read whole: %d bytes", stringStart, len(r.value))
	}
	r.loadWhole()
	testutil.WaitFor(t, tt, "the whole string", func() bool { return r.whole })

	// Export the user's key, delete the sessions and the user, import it.
	file := filepath.Join(t.TempDir(), "keys.jsonl")
	r.openBulk(bulkExport)
	b := r.bulk
	b.pattern, b.path = "user:*", file
	r.exportKeys(b)
	testutil.WaitFor(t, tt, "the export", func() bool { return !b.running })
	if b.err != "" || !strings.Contains(b.result, "Exported 1 keys") {
		t.Fatalf("export: %q %q", b.err, b.result)
	}
	for _, pattern := range []string{"session:*", "user:*"} {
		r.openBulk(bulkDelete)
		b = r.bulk
		b.pattern = pattern
		r.countKeys(b)
		testutil.WaitFor(t, tt, "the count", func() bool { return !b.running && b.counted == pattern })
		r.deleteKeys(b)
		if a.Confirm == nil || !a.Confirm.TypeName == (cfg.Env == db.Production) {
			t.Fatal("deleting keys by pattern is not confirmed")
		}
		a.Confirm.OnConfirm()
		a.Confirm = nil
		testutil.WaitFor(t, tt, "the deletion", func() bool { return !b.running && b.result != "" })
	}
	if n, _ := kv.DBSize(ctx); n != 3 {
		t.Fatalf("%d keys left, want the queue and the two big ones", n)
	}
	r.openBulk(bulkImport)
	b = r.bulk
	r.readImport(b, file)
	testutil.WaitFor(t, tt, "the file read", func() bool { return !b.running && b.dumps != nil })
	r.importKeys(b)
	testutil.WaitFor(t, tt, "the import", func() bool { return !b.running && b.result != "" })
	if v, err := kv.Do(ctx, []string{"HGET", "user:42", "email"}); err != nil || v != "ada@example.com" {
		t.Fatalf("imported user: %v %v", v, err)
	}

	// The console completes a command's name, then shows its syntax.
	r.bulk.open = false
	testutil.WaitFor(t, tt, "the commands' help", func() bool { return r.docs != nil && r.bulk == nil })
	if err := tt.Click("Command"); err != nil {
		t.Fatal(err)
	}
	tt.Type("hge")
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if r.consoleIn != "HGET" {
		t.Fatalf("hge completed as %q, want HGET", r.consoleIn)
	}
	tt.Type(" ")
	tt.Frame()
	if !tt.HasText("HGET key field") {
		t.Fatalf("no help for HGET after %q: %v", r.consoleIn, tt.Texts())
	}
	r.consoleIn = ""

	// A file of commands runs, once confirmed, to the first that fails.
	cmds := filepath.Join(t.TempDir(), "seed.redis")
	os.WriteFile(cmds, []byte("# seed\nSET seed:a 1\nINCR seed:a\nHSET seed:a f v\nSET seed:b 2\n"), 0o600)
	before := len(r.consoleLog)
	r.runCommandFile(cmds)
	if a.Confirm == nil || !strings.Contains(a.Confirm.Title, "Run 4 commands") {
		t.Fatalf("a file of writes is not confirmed: %+v", a.Confirm)
	}
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "the file", func() bool { return r.file == nil && len(r.consoleLog) > before+1 })
	if v, _ := kv.Do(ctx, []string{"GET", "seed:a"}); v != "2" {
		t.Fatalf("seed:a is %v, want 2", v)
	}
	if v, _ := kv.Do(ctx, []string{"GET", "seed:b"}); v != nil {
		t.Fatalf("a command after the failing one ran: seed:b is %v", v)
	}
	if last := r.consoleLog[len(r.consoleLog)-1]; !last.err || !strings.Contains(last.text, "WRONGTYPE") {
		t.Fatalf("the failure is not shown last: %+v", last)
	}

	// The panels below the keys: the commands the server runs, its slow
	// log, and the keys' memory.
	r.panel = panelMonitor
	r.startMonitor()
	testutil.WaitFor(t, tt, "the monitor", func() bool {
		kv.Do(ctx, []string{"GET", "monitored"})
		return slices.ContainsFunc(r.monitor.lines, func(l db.MonitorLine) bool { return strings.Contains(l.Command, "monitored") })
	})
	r.monitor.cancel()
	testutil.WaitFor(t, tt, "the monitor to stop", func() bool { return r.monitor.cancel == nil })
	if r.monitor.err != "" {
		t.Fatal(r.monitor.err)
	}
	r.panel = panelSlowLog
	testutil.WaitFor(t, tt, "the slow log", func() bool { return r.slow.asked && !r.slow.loading })
	if r.slow.err != "" {
		t.Fatal(r.slow.err)
	}
	r.panel = panelMemory
	r.analyseMemory()
	testutil.WaitFor(t, tt, "the analysis", func() bool { return r.memory.cancel == nil })
	if rep := r.memory.report; r.memory.err != "" || rep == nil || rep.Largest[0].Key != "big:hash" && rep.Largest[0].Key != "big:string" {
		t.Fatalf("analysis %q %+v", r.memory.err, rep)
	}
	testutil.Snapshot(t, tt, "redis-memory")

	// Messages published to a channel listened to show as they come.
	r.panel = panelPubSub
	r.pubsub.listenIn = "news orders.*"
	r.listen()
	testutil.WaitFor(t, tt, "a message", func() bool {
		kv.Do(ctx, []string{"PUBLISH", "orders.eu", "42"})
		return slices.ContainsFunc(r.pubsub.messages, func(m db.PubSubMessage) bool { return m.Pattern == "orders.*" && m.Message == "42" })
	})
	r.pubsub.cancel()
	testutil.WaitFor(t, tt, "the subscription to end", func() bool { return r.pubsub.cancel == nil })

	// A list's item is removed at its index, not the first of its value.
	kv.Do(ctx, []string{"RPUSH", "dup:list", "a", "b", "a"})
	r.open("dup:list")
	testutil.WaitFor(t, tt, "the list", func() bool { return !r.loadingKey && len(r.fields) == 3 })
	r.write([]string{"EVAL", removeListItem, "1", "dup:list", "2", "a"}, r.loadKey)
	testutil.WaitFor(t, tt, "the removal", func() bool { return !r.loadingKey && len(r.fields) == 2 })
	if v, _ := kv.Do(ctx, []string{"LRANGE", "dup:list", "0", "-1"}); fmt.Sprint(v) != "[a b]" {
		t.Fatalf("removing index 2 left %v", v)
	}

	// A stream's groups, with a group's consumers and pending entries.
	for _, cmd := range [][]string{{"XADD", "jobs", "1-1", "n", "1"}, {"XGROUP", "CREATE", "jobs", "workers", "0"},
		{"XREADGROUP", "GROUP", "workers", "w1", "COUNT", "1", "STREAMS", "jobs", ">"}} {
		if _, err := kv.Do(ctx, cmd); err != nil {
			t.Fatal(cmd, err)
		}
	}
	r.open("jobs")
	testutil.WaitFor(t, tt, "the stream", func() bool { return !r.loadingKey && r.info.Type == "stream" })
	r.stream.panel = streamGroups
	r.loadGroups()
	testutil.WaitFor(t, tt, "the groups", func() bool { return !r.stream.loading && len(r.stream.groups) == 1 })
	r.stream.group = 0
	r.loadGroups()
	testutil.WaitFor(t, tt, "the group's consumers", func() bool { return !r.stream.loading && len(r.stream.pending) == 1 })
	if r.stream.consumers[0].Name != "w1" || r.stream.pending[0].ID != "1-1" {
		t.Fatalf("consumers %+v, pending %+v", r.stream.consumers, r.stream.pending)
	}
	testutil.Snapshot(t, tt, "redis-stream-groups")
	r.groupWrite("XACK", "jobs", "workers", "1-1")
	testutil.WaitFor(t, tt, "the acknowledgement", func() bool { return !r.stream.loading && len(r.stream.pending) == 0 && r.stream.group == 0 })

	// A value compressed shows decoded; an item, as chosen.
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	w.Write([]byte(`{"user":42}`))
	w.Close()
	kv.Do(ctx, []string{"SET", "packed", gz.String()})
	kv.Do(ctx, []string{"HSET", "packed:h", "f", "\x81\xa1a\x01"})
	r.open("packed")
	testutil.WaitFor(t, tt, "the decoded value", func() bool { return !r.loadingKey && r.strView.display != "" })
	if r.strView.as != viewAuto || r.strView.display != `{"user":42}` {
		t.Fatalf("gzip shown as %q: %q", r.strView.as, r.strView.display)
	}
	r.open("packed:h")
	testutil.WaitFor(t, tt, "the hash", func() bool { return !r.loadingKey && len(r.fields) == 1 })
	r.fieldRow = 0
	tt.Frame()
	r.itemView.as = "As MessagePack"
	testutil.WaitFor(t, tt, "the item decoded", func() bool { return r.itemView.asFor == r.itemView.as && !r.itemView.running })
	if got := strings.Join(strings.Fields(r.itemView.display), ""); got != `{"a":1}` {
		t.Fatalf("an item as MessagePack: %q", r.itemView.display)
	}

	// A file running holds the tab: closing it asks, then stops the file.
	stop, cancel := context.WithCancel(ctx)
	r.file = &fileRun{path: cmds, total: 9, done: 2, cancel: cancel}
	if why := r.CloseReason(); !strings.Contains(why, "2 of its 9") {
		t.Fatalf("closing during a file run: %q", why)
	}
	r.Close()
	if stop.Err() == nil {
		t.Fatal("closing the tab left the file running")
	}
	r.file = nil
}
