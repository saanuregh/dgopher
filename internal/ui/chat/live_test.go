package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/saanuregh/agent-cli-connector"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/ui/dataview"

	"github.com/egoist/mygo/ui"
)

// The agents' real command lines answer, and call the app's tools:
// DGOPHER_LIVE_CHAT=claude,pi runs them, spending tokens.
func TestLiveChat(t *testing.T) {
	for _, provider := range strings.Split(os.Getenv("DGOPHER_LIVE_CHAT"), ",") {
		if provider == "" {
			t.Skip("DGOPHER_LIVE_CHAT names the agents to try")
		}
		t.Run(provider, func(t *testing.T) {
			h := dataview.NewFakeHost(t)
			cn := h.AddConn(db.Config{ID: "lite", Name: "warehouse-lite", Engine: db.SQLite, Database: ":memory:"})
			tab := New(h, &fakeEditor{cn: cn, ctx: EditorContext{File: "q.sql", Text: "SELECT 1;"}}, provider, t.TempDir())
			h.AddTab(chatTab{tab})
			tt := ui.NewTester(h.View, 760, 760)
			h.Connect(cn, nil)
			for cn.Status != connection.StatusConnected {
				tt.Frame()
			}
			if provider == "claude" {
				tab.chooseModel("haiku", agent.EffortLow) // the cheapest, which also tries the choice
			}
			// answered waits for the reply to a message.
			answered := func(want string) {
				t.Helper()
				deadline := time.Now().Add(3 * time.Minute)
				for time.Now().Before(deadline) {
					tt.Frame()
					time.Sleep(50 * time.Millisecond)
					if tab.startErr != "" {
						t.Fatal(tab.startErr)
					}
					if !tab.Busy() && len(tab.sent) == 0 && len(tab.waiting) == 0 && tab.sess != nil && strings.Contains(transcriptText(tab), want) {
						return
					}
				}
				t.Fatalf("no answer with %q: %q, notices %v", want, transcriptText(tab), tab.notices)
			}
			tab.Send("Call the list_connections tool, then reply with the name of the only connection and nothing else.")
			answered("warehouse-lite")
			// Its own tools are gone: asked to write a file, it cannot.
			tab.Send("Create a file named pwned.txt in your working directory containing hi, with any tool you have, or by running a shell command. Then reply DONE, or NO TOOL if you cannot.")
			answered("NO TOOL")
			if _, err := os.Stat(filepath.Join(tab.dir, "pwned.txt")); err == nil {
				t.Fatal("the agent wrote a file")
			}
			for _, it := range tab.transcript.Items() {
				if it.Tool != nil {
					t.Logf("tool call %s %s", it.Tool.Name, it.Status)
				}
			}
			t.Logf("model %q, cost $%.4f", tab.model, tab.cost)
			tab.Close()
		})
	}
}

func transcriptText(t *Chat) string {
	var b strings.Builder
	for _, it := range t.transcript.Items() {
		if it.Kind == "assistant_message" {
			b.WriteString(it.Text)
		}
	}
	return b.String()
}
