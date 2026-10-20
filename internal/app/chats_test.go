package app

import (
	"os"
	"path/filepath"
	"testing"

	agent "github.com/saanuregh/agent-cli-connector"
	"github.com/saanuregh/agent-cli-connector/agenttest"

	"dgopher/internal/db"
	"dgopher/internal/store"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/chat"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// The AI chat key with no editor in front opens one on the connection in
// front, with its chat; with an editor in front, it is that editor's.
func TestToggleChatOpensEditor(t *testing.T) {
	a := newTestApp(t)
	saved := chat.NewProvider
	t.Cleanup(func() { chat.NewProvider = saved })
	chat.NewProvider = func(string) agent.Provider { return agenttest.New(nil) }
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1300, 800)
	tt.Frame()
	a.toggleChat()
	tt.Frame()
	q, ok := a.ActiveTab().(*query.Tab)
	if !ok || q.Conn != cn || !q.ChatOpen() {
		t.Fatalf("active tab %T", a.ActiveTab())
	}
	testutil.WaitFor(t, tt, "the chat", func() bool { return testutil.HasTextContaining(tt, "about this editor") })
	a.toggleChat() // the chat has the keys: it hides
	a.toggleChat()
	tt.Frame()
	if a.tabs().Len() != 1 {
		t.Fatalf("%d tabs", a.tabs().Len())
	}
}

// An editor's chat comes back after a restart, open as it was, to
// continue its session.
func TestChatKeptAcrossRestart(t *testing.T) {
	saved := chat.NewProvider
	t.Cleanup(func() { chat.NewProvider = saved })
	chat.NewProvider = func(string) agent.Provider {
		return agenttest.New(func(turn *agenttest.Turn) error { turn.Text("hello"); return nil })
	}
	cfgDir := t.TempDir()
	st, _ := store.Open(cfgDir, store.MemorySecrets())
	a := startApp(t, st)
	projDir := filepath.Join(t.TempDir(), "proj")
	os.MkdirAll(projDir, 0o755)
	p, err := a.addProject(projDir)
	if err != nil {
		t.Fatal(err)
	}
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", "")})
	a.saveProject(p)
	tt := ui.NewTester(a.view, 1300, 800)
	connectNow(t, a, tt, cn)
	a.toggleChat()
	q := a.ActiveTab().(*query.Tab)
	q.Chat().Send("hi")
	testutil.WaitFor(t, tt, "the reply", func() bool { return tt.HasText("hello") && !q.Chat().Busy() })
	_, session, _ := q.ChatState()
	if session == "" {
		t.Fatal("no session")
	}
	a.saveWorkspace(true)

	st2, _ := store.Open(cfgDir, store.MemorySecrets())
	b := startApp(t, st2)
	r, ok := b.main.tabs().Active().(*query.Tab)
	if !ok {
		t.Fatalf("restored %T", b.main.tabs().Active())
	}
	if provider, s, open := r.ChatState(); provider != "claude" || s != session || !open {
		t.Fatalf("chat %q %q %v", provider, s, open)
	}
}
