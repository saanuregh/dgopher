package redis

import (
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

// A server without search says where each engine's search comes from.
func TestSearchEmptyNamesSource(t *testing.T) {
	for engine, source := range map[db.Engine]string{
		db.Redis:  "This server has no Redis Search: it comes with Redis 8, or as a module before.",
		db.Valkey: "This server has no Valkey Search: it comes with valkey-bundle, or as the valkey-search module.",
	} {
		t.Run(string(engine), func(t *testing.T) {
			f := newFakeRedis(t, func([]string) any { return unhandled{} })
			_, tt, _, r := openFakeAs(t, f, db.Development, engine)
			r.panel = panelSearch
			testutil.WaitFor(t, tt, "the note", func() bool { return testutil.HasTextContaining(tt, source) })
		})
	}
}

// Switching logs while one is read shows the log chosen, though the
// read of the one left answers last.
func TestCommandLogSwitchKeepsChosen(t *testing.T) {
	entry := func(arg string, value int64) []any {
		return []any{int64(1), int64(1795700000), value, []any{"SET", arg}, "127.0.0.1:1", ""}
	}
	f := newFakeRedis(t, func(args []string) any {
		switch strings.ToUpper(args[0]) {
		case "SLOWLOG":
			time.Sleep(300 * time.Millisecond)
			return []any{entry("slow", 5)}
		case "COMMANDLOG":
			return []any{entry("large", 2048)}
		}
		return unhandled{}
	})
	_, tt, _, r := openFakeAs(t, f, db.Development, db.Valkey)
	r.panel = panelSlowLog
	r.loadSlowLog()
	r.slow.shown = slices.IndexFunc(commandLogs, func(l commandLogChoice) bool { return l.log == db.LogLargeRequest })
	r.loadSlowLog()
	time.Sleep(500 * time.Millisecond)
	tt.Frame()
	if r.slow.loading || len(r.slow.entries) != 1 || r.slow.entries[0].Args[1] != "large" {
		t.Fatalf("large requests shown as %+v, loading %v", r.slow.entries, r.slow.loading)
	}
}
