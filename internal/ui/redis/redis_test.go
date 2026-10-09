package redis

import (
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
