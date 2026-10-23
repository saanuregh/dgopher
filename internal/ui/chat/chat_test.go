package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	agent "github.com/saanuregh/agent-cli-connector/bindings/go"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// fakeEditor is the editor a chat is about in tests: what it shows is
// set, and what is inserted kept.
type fakeEditor struct {
	cn       *connection.Conn
	ctx      EditorContext
	inserted []string
	charted  []dataview.ChartSettings
}

func (e *fakeEditor) Connection() *connection.Conn { return e.cn }
func (e *fakeEditor) EditorContext() EditorContext { return e.ctx }
func (e *fakeEditor) InsertSQL(sql string)         { e.inserted = append(e.inserted, sql) }

func (e *fakeEditor) ChartSQL(sql string, s dataview.ChartSettings, done func(ChartResult)) string {
	e.charted = append(e.charted, s)
	done(ChartResult{Columns: []string{"k", "n"}, Rows: [][]any{{"a", 1}, {"b", 2}}})
	return ""
}

// chatTab shows a chat as the host's tab, which runs what is posted.
type chatTab struct{ *Chat }

func (t chatTab) Title() string                { return "chat" }
func (t chatTab) Connection() *connection.Conn { return t.conn() }
func (t chatTab) View(c *ui.Context)           { t.Chat.View(c, true) }

// chatWith opens a chat about an editor on a connected SQLite in memory.
// Its agent is the agent library's fake, which replies with the message
// it was sent; models are those the chat's agent lists.
func chatWith(t *testing.T, models ...agent.Model) (*dataview.FakeHost, *ui.Tester, *fakeEditor, *Chat) {
	t.Helper()
	savedLists := modelLists
	t.Cleanup(func() { modelLists = savedLists })
	modelLists = map[string]*modelList{}
	if models != nil {
		modelLists["claude"] = &modelList{models: models}
	}
	FakeAgent(t)
	h := dataview.NewFakeHost(t)
	cn := h.AddConn(db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	ed := &fakeEditor{cn: cn}
	tab := New(h, ed, "claude", t.TempDir())
	h.AddTab(chatTab{tab})
	tt := ui.NewTester(h.View, 760, 760)
	h.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	t.Cleanup(tab.Close)
	return h, tt, ed, tab
}

// replies are the agent's replies so far, ended or not.
func replies(tab *Chat) []string {
	var out []string
	for _, it := range tab.transcript.Items() {
		if it.Kind == agent.ItemKindAssistantMessage {
			out = append(out, it.Text)
		}
	}
	return out
}

// event is an event carrying payload.
func event(payload agent.EventPayload) agent.Event { return agent.Event{Payload: payload} }

// A message starts the agent and carries the editor's block, which shows
// as a line under it; the reply streams in as Markdown.
func TestConversation(t *testing.T) {
	_, tt, ed, tab := chatWith(t)
	testutil.WaitFor(t, tt, "the empty chat", func() bool { return testutil.HasTextContaining(tt, "about this editor") })
	ed.ctx = EditorContext{File: "q.sql", Text: "SELECT 1;\nSELECT 2;\n", Statement: "SELECT 2;", From: 2, To: 2, LastError: "no such table: x"}
	tab.Send("why does this **fail**")
	testutil.WaitFor(t, tt, "the reply", func() bool { return len(replies(tab)) == 1 && tt.HasText("fail") && !tab.Busy() })
	sent := replies(tab)[0] // the fake's reply is the message
	for _, want := range []string{`<editor summary="q.sql · statement, line 2 · lite">`, "Connection: lite (SQLite, ", "Statement at the caret, line 2:\n```sql\nSELECT 2;\n```", "SELECT 1;\nSELECT 2;", "The last run's error: no such table: x", "</editor>\n\nwhy does this **fail**"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("the message lacks %q:\n%s", want, sent)
		}
	}
	tab.transcript.Apply(event(agent.ItemCompleted{Item: agent.Item{ID: "r1", Kind: agent.ItemKindReasoning, Text: "they ask why"}}))
	tt.Frame()
	if !tt.HasText("q.sql · statement, line 2 · lite") || !tt.HasText("Thinking ▸") {
		t.Fatalf("texts %q", tt.Texts())
	}
	widgets.ShadersWithoutGPU = true
	t.Cleanup(func() { widgets.ShadersWithoutGPU = false })
	tt.Frame()
	testutil.Snapshot(t, tt, "chat")
	tab.Restart("pi")
	tt.Frame()
	if tab.Provider() != "pi" || !tab.transcriptEmpty() {
		t.Fatal("not started over")
	}
}

// The editor's block keeps a message's text, and ends only where it
// does.
func TestContextBlock(t *testing.T) {
	block := editorBlock(EditorContext{File: "a.sql", Text: "SELECT '</editor>';", Selection: "SELECT '</editor>';", From: 1, To: 1}, "", "", false)
	msg, summary := splitContext(block + "\n\nhi\n</editor>")
	if msg != "hi\n</editor>" || summary != "a.sql · selection, line 1" {
		t.Fatalf("message %q, summary %q", msg, summary)
	}
	big := editorBlock(EditorContext{Text: strings.Repeat("x", maxEditorText+1)}, "", "", false)
	if strings.Contains(big, "xxxx") || !strings.Contains(big, "too long") {
		t.Fatal("the long text went in")
	}
	if msg, summary := splitContext("plain"); msg != "plain" || summary != "" {
		t.Fatal("a message without a block changed")
	}
}

// The model and effort chosen start the session, and switch it once
// started; they are kept for the next chat.
func TestModelAndEffort(t *testing.T) {
	models := []agent.Model{
		{ID: "default", DisplayName: "Sonnet", IsDefault: true, Efforts: []agent.Effort{agent.EffortLow, agent.EffortHigh}},
		{ID: "opus", DisplayName: "Opus", Efforts: []agent.Effort{agent.EffortLow, agent.EffortHigh, agent.EffortMax}},
		{ID: "haiku", DisplayName: "Haiku"},
	}
	h, tt, _, tab := chatWith(t, models...)
	if err := tt.Click("Default model ▾"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the picker", func() bool { return tt.HasText("Opus") })
	tt.Type("opu")
	tt.Frame()
	tt.Key(0, ui.KeyEnter) // the one match
	tt.Frame()
	if tab.modelID != "opus" || tab.picker.open {
		t.Fatalf("chose %q, open %v", tab.modelID, tab.picker.open)
	}
	tt.Frame()
	if err := tt.Click("default effort ▾"); err != nil {
		t.Fatal(err)
	}
	if err := testutil.ChooseMenu(tt, "max"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tab.effort != agent.EffortMax {
		t.Fatalf("effort %q", tab.effort)
	}
	tab.Send("hi")
	testutil.WaitFor(t, tt, "the reply", func() bool { return len(replies(tab)) == 1 && !tab.Busy() })
	if model := tab.sess.Info().Model; model != "opus" {
		t.Fatalf("started with %q", model)
	}
	// Haiku takes no effort: the effort goes, and its menu.
	tab.chooseModel("haiku", tab.effort)
	testutil.WaitFor(t, tt, "the switch", func() bool { return tab.sess.Info().Model == "haiku" })
	if tab.effort != "" || testutil.HasTextContaining(tt, "effort ▾") {
		t.Fatalf("effort %q, texts %q", tab.effort, tt.Texts())
	}
	if s := h.Settings(); s.ChatModels["claude"] != "haiku" || len(s.ChatEfforts) != 0 {
		t.Fatalf("kept %v %v", s.ChatModels, s.ChatEfforts)
	}
	if next := New(h, tab.ed, "claude", ""); next.modelID != "haiku" {
		t.Fatalf("the next chat uses %q", next.modelID)
	}
}

// A SQL block of a reply goes in the editor with a click.
func TestInsertSQL(t *testing.T) {
	_, tt, ed, tab := chatWith(t)
	tab.Send("Try:\n\n```sql\nSELECT 42;\n```\n")
	testutil.WaitFor(t, tt, "the block", func() bool { return tt.HasText("Insert") && !tab.Busy() })
	if err := tt.Click("Insert"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ed.inserted, []string{"SELECT 42;"}) {
		t.Fatalf("inserted %q", ed.inserted)
	}
	ed.ctx.Selection = "SELECT 1;"
	tt.Frame()
	if !tt.HasText("Replace Selection") {
		t.Fatalf("texts %q", tt.Texts())
	}
}

// What the agent's own tools ask to do is refused unasked, and said so.
func TestAgentPermission(t *testing.T) {
	_, tt, _, tab := chatWith(t)
	tab.Send("needs permission") // the fake asks to run a command
	testutil.WaitFor(t, tt, "the reply", func() bool { return tt.HasText("permission denied") && !tab.Busy() })
	if len(tab.requests) != 0 || !testutil.HasTextContaining(tt, "Refused: Run a command") {
		t.Fatalf("%d cards, texts %q", len(tab.requests), tt.Texts())
	}
}

// The read tool runs a read once the user allows it, audited, and gives
// its rows; a write, or a read on production, it refuses without asking.
// Without a connection named, it reads the editor's.
func TestReadQueryTool(t *testing.T) {
	h, tt, ed, tab := chatWith(t)
	h.AddConn(db.Config{ID: "other", Name: "other", Engine: db.SQLite, Database: ":memory:"})
	type answer struct {
		res agent.ToolResult
		err error
	}
	call := func(sql string) chan answer {
		out := make(chan answer, 1)
		go func() {
			res, err := tab.runReadQuery(context.Background(), place{SQL: sql})
			out <- answer{res, err}
		}()
		return out
	}
	got := call("SELECT 41 + 1 AS answer")
	testutil.WaitFor(t, tt, "the request", func() bool {
		return len(tab.requests) == 1 && testutil.HasTextContaining(tt, "41 + 1 AS answer") && tt.HasText("Run a read on lite")
	})
	if err := tt.Click("Allow"); err != nil {
		t.Fatal(err)
	}
	var a answer
	testutil.WaitFor(t, tt, "the rows", func() bool {
		select {
		case a = <-got:
			return true
		default:
			return false
		}
	})
	if a.err != nil || a.res.IsError || !strings.Contains(a.res.Text(), "42") {
		t.Fatalf("result %q, %v", a.res.Text(), a.err)
	}
	if !slices.ContainsFunc(h.Events, func(e audit.Event) bool { return strings.Contains(e.Statement, "41 + 1") }) {
		t.Fatal("not audited")
	}

	for _, sql := range []string{"DELETE FROM t", "SELECT 1; SELECT 2"} {
		got := call(sql)
		testutil.WaitFor(t, tt, "the refusal", func() bool {
			select {
			case a = <-got:
				return true
			default:
				return false
			}
		})
		if !a.res.IsError || len(tab.requests) != 0 {
			t.Fatalf("%s: %q", sql, a.res.Text())
		}
	}
	ed.cn.Config.Env = db.Production
	if why := readRefusal(&ed.cn.Config, "SELECT 1"); !strings.Contains(why, "production") {
		t.Fatalf("production: %q", why)
	}
}

func countText(tt *ui.Tester, s string) int {
	n := 0
	for _, t := range tt.Texts() {
		if t == s {
			n++
		}
	}
	return n
}

// Two requests waiting are both refused when the chat stops, and both
// cards answer one at a time.
func TestTwoRequests(t *testing.T) {
	_, tt, _, tab := chatWith(t)
	answers := []chan decision{make(chan decision, 1), make(chan decision, 1), make(chan decision, 1)}
	for i, ch := range answers {
		tab.requests = append(tab.requests, &request{id: fmt.Sprint("r", i), title: fmt.Sprint("request ", i), answer: ch})
	}
	testutil.WaitFor(t, tt, "the cards", func() bool { return tt.HasText("request 2") })
	if err := tt.Click("Allow"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the answer", func() bool { return len(tab.requests) == 2 })
	if len(tab.requests) != 2 || <-answers[0] != allow {
		t.Fatalf("%d requests left", len(tab.requests))
	}
	tab.denyAll("stopped")
	for _, ch := range answers[1:] {
		if d := <-ch; d != deny {
			t.Fatalf("decided %v", d)
		}
	}
}

// Both agents get the app's tools alone: none of their own, nothing the
// user's settings add, and whatever else refused without asking.
// Checked as the library gets them: an empty list must reach it, as nil
// would leave the agent its defaults.
func TestLockDown(t *testing.T) {
	var cfg agent.Config
	lockDown("claude", &cfg)
	claude := string(cfg.ProviderOptions)
	if cfg.PermissionMode != agent.PermissionModeDontAsk {
		t.Fatalf("claude: %q", cfg.PermissionMode)
	}
	for _, want := range []string{`"builtin_tools":[]`, `"setting_sources":[]`, `"strict_mcp_config":true`} {
		if !strings.Contains(claude, want) {
			t.Fatalf("claude: %s", claude)
		}
	}
	cfg = agent.Config{}
	lockDown("codex", &cfg)
	if cfg.PermissionMode != agent.PermissionModeReadOnly || !slices.Contains(codexFeaturesOff, "shell_tool") || codexConfig["mcp_servers"] != "{}" {
		t.Fatalf("codex: %q", cfg.PermissionMode)
	}
	cfg = agent.Config{}
	lockDown("pi", &cfg)
	var p agent.PiSessionOptions
	if err := json.Unmarshal(cfg.ProviderOptions, &p); err != nil || !p.NoBuiltinTools || !p.NoExtensions || !p.NoSkills || !p.NoMcp || p.TrustProject == nil || *p.TrustProject || cfg.PermissionMode != agent.PermissionModeDontAsk {
		t.Fatalf("pi: %s, %q, %v", cfg.ProviderOptions, cfg.PermissionMode, err)
	}
}

// A chart waits for the user, then is the editor's, and the agent hears
// of its first rows; a write, or a kind unknown, it refuses unasked.
func TestChartTool(t *testing.T) {
	_, tt, ed, tab := chatWith(t)
	type answer struct {
		res agent.ToolResult
		err error
	}
	call := func(args chartArgs) chan answer {
		out := make(chan answer, 1)
		go func() {
			res, err := tab.chartInEditor(context.Background(), args)
			out <- answer{res, err}
		}()
		return out
	}
	wait := func(ch chan answer) (a answer) {
		testutil.WaitFor(t, tt, "the answer", func() bool {
			select {
			case a = <-ch:
				return true
			default:
				return false
			}
		})
		return a
	}
	got := call(chartArgs{SQL: "SELECT k, n FROM t", Kind: "Bar", X: "k", Series: []string{"n"}})
	testutil.WaitFor(t, tt, "the card", func() bool { return tt.HasText("Chart in the editor on lite") })
	if !testutil.HasTextContaining(tt, "bar chart of n by k") {
		t.Fatalf("texts %q", tt.Texts())
	}
	if err := tt.Click("Allow"); err != nil {
		t.Fatal(err)
	}
	a := wait(got)
	if a.err != nil || a.res.IsError || !strings.Contains(a.res.Text(), "bar chart of 2 rows") || !strings.Contains(a.res.Text(), "| b ") {
		t.Fatalf("result %q, %v", a.res.Text(), a.err)
	}
	if len(ed.charted) != 1 || ed.charted[0].X != "k" || ed.charted[0].Series[0] != "n" {
		t.Fatalf("charted %+v", ed.charted)
	}
	for _, args := range []chartArgs{{SQL: "DELETE FROM t", Kind: "Bar"}, {SQL: "SELECT 1", Kind: "Radar"}} {
		if a := wait(call(args)); !a.res.IsError || len(tab.requests) != 0 {
			t.Fatalf("%+v: %q", args, a.res.Text())
		}
	}
}

// A message the agent shows back while it is still kept as sent shows
// once.
func TestSentShownBack(t *testing.T) {
	_, tt, _, tab := chatWith(t)
	tab.sent = []sentMessage{{id: "dgopher-1", text: "hello"}}
	tab.transcript.Apply(event(agent.ItemStarted{Item: agent.Item{ID: "u1", Kind: agent.ItemKindUserMessage, ClientID: "dgopher-1", Text: "hello"}}))
	tt.Frame()
	if n := countText(tt, "hello"); n != 1 {
		t.Fatalf("shown %d times", n)
	}
}

// A tool needing a connection not open asks the user to connect it, once
// for every call waiting, and goes on once it is; refused, it says so.
func TestAskToConnect(t *testing.T) {
	h, tt, _, tab := chatWith(t)
	other := h.AddConn(db.Config{ID: "other", Name: "other", Engine: db.SQLite, Database: ":memory:"})
	type answer struct {
		res agent.ToolResult
		err error
	}
	call := func() chan answer {
		out := make(chan answer, 1)
		go func() {
			res, err := tab.listTables(context.Background(), place{Connection: "other"})
			out <- answer{res, err}
		}()
		return out
	}
	wait := func(ch chan answer) (a answer) {
		testutil.WaitFor(t, tt, "the answer", func() bool {
			select {
			case a = <-ch:
				return true
			default:
				return false
			}
		})
		return a
	}
	first, second := call(), call()
	testutil.WaitFor(t, tt, "the card", func() bool { return tt.HasText("Connect to other?") })
	for range 5 {
		tt.Frame()
	}
	if len(tab.requests) != 1 {
		t.Fatalf("%d cards for one connection", len(tab.requests))
	}
	if err := tt.Click("Not Now"); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []chan answer{first, second} {
		if a := wait(ch); !a.res.IsError || !strings.Contains(a.res.Text(), "chose not to connect") {
			t.Fatalf("refused: %q %v", a.res.Text(), a.err)
		}
	}
	got := call()
	testutil.WaitFor(t, tt, "the card again", func() bool { return tt.HasText("Connect to other?") })
	if err := tt.Click("Connect"); err != nil {
		t.Fatal(err)
	}
	if a := wait(got); a.err != nil || a.res.IsError || other.Status != connection.StatusConnected {
		t.Fatalf("connected: %q %v, status %v", a.res.Text(), a.err, other.Status)
	}
}

// Hundreds of models fit the picker's height, its list scrolling, and
// search narrows them by every word typed, in name, ID or provider.
func TestModelPickerSearch(t *testing.T) {
	var models []agent.Model
	for i := range 300 {
		provider := []string{"openrouter", "deepseek", "google"}[i%3]
		models = append(models, agent.Model{ID: fmt.Sprintf("%s/model-%d", provider, i), DisplayName: fmt.Sprintf("Model %d", i), Provider: provider})
	}
	models = append(models, agent.Model{ID: "openrouter/anthropic/claude-haiku-5.5", DisplayName: "Anthropic: Claude Haiku 5.5", Provider: "openrouter"})
	_, tt, _, tab := chatWith(t, models...)
	if err := tt.Click("Default model ▾"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the picker", func() bool { return tt.HasText("Search models") || tab.picker.open })
	tt.Frame()
	if r, ok := tt.Find("Model 0"); !ok || r.H <= 0 {
		t.Fatal("no model shown")
	}
	if _, ok := tt.Find("Model 299"); ok {
		if r, _ := tt.Find("Model 299"); r.Y < 760 && r.Y > 0 {
			t.Fatalf("all 300 show at once: Model 299 at %v", r)
		}
	}
	tt.Type("haiku router")
	tt.Frame()
	if len(tab.pickEntries(tab.listed(), tab.picker.query)) != 1 || !tt.HasText("Anthropic: Claude Haiku 5.5") {
		t.Fatalf("matches %v", tab.pickEntries(tab.listed(), tab.picker.query))
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if tab.modelID != "openrouter/anthropic/claude-haiku-5.5" {
		t.Fatalf("chose %q", tab.modelID)
	}
}

// A connection keeping its rows from the chat runs no read for the agent,
// and its charts tell the agent their columns and count alone.
func TestNoRowsToChat(t *testing.T) {
	_, tt, ed, tab := chatWith(t)
	ed.cn.Config.NoRowsToChat = true
	read := make(chan agent.ToolResult, 1)
	go func() {
		res, _ := tab.runReadQuery(context.Background(), place{SQL: "SELECT 1"})
		read <- res
	}()
	var res agent.ToolResult
	testutil.WaitFor(t, tt, "the refusal", func() bool {
		select {
		case res = <-read:
			return true
		default:
			return false
		}
	})
	if !res.IsError || !strings.Contains(res.Text(), "keeps its rows") || len(tab.requests) != 0 {
		t.Fatalf("read: %q, %d cards", res.Text(), len(tab.requests))
	}
	got := make(chan agent.ToolResult, 1)
	go func() {
		res, _ := tab.chartInEditor(context.Background(), chartArgs{SQL: "SELECT k, n FROM t", Kind: "Bar", X: "k", Series: []string{"n"}})
		got <- res
	}()
	testutil.WaitFor(t, tt, "the card", func() bool { return testutil.HasTextContaining(tt, "no data") })
	if err := tt.Click("Allow"); err != nil {
		t.Fatal(err)
	}
	var chart agent.ToolResult
	testutil.WaitFor(t, tt, "the chart", func() bool {
		select {
		case chart = <-got:
			return true
		default:
			return false
		}
	})
	if text := chart.Text(); chart.IsError || !strings.Contains(text, "Its columns: k, n") || strings.Contains(text, "| a ") {
		t.Fatalf("chart told %q", text)
	}
	// An error's message can hold values: the agent hears it failed.
	ed.ctx.LastError = "Duplicate entry 'alice@corp.com' for key 'email'"
	if block := tab.withContext("why?"); strings.Contains(block, "alice@corp.com") || !strings.Contains(block, "it failed") {
		t.Fatalf("block:\n%s", block)
	}
}

// The next message after Resume continues the earlier run's session. The
// fake keeps no history, so the conversation is loaded here, and that it
// shows is checked only as Load draws it.
func TestResumeChat(t *testing.T) {
	_, tt, _, tab := chatWith(t)
	tab.Resume("earlier")
	tab.Load([]agent.Item{
		{ID: "h1", Kind: agent.ItemKindUserMessage, Text: "<editor summary=\"q.sql · lite\">\n</editor>\n\nhow many orders?"},
		{ID: "h2", Kind: agent.ItemKindAssistantMessage, Text: "There are 42 orders."},
	})
	testutil.WaitFor(t, tt, "the history", func() bool { return tt.HasText("There are 42 orders.") })
	if !tt.HasText("how many orders?") || !tt.HasText("q.sql · lite") || tab.SessionID() != "earlier" {
		t.Fatalf("texts %q, session %q", tt.Texts(), tab.SessionID())
	}
	tab.Send("still here")
	testutil.WaitFor(t, tt, "the reply", func() bool { return len(replies(tab)) == 2 && !tab.Busy() })
	if id := tab.sess.ID(); id != "earlier" {
		t.Fatalf("started session %q", id)
	}
	tab.Restart("claude")
	if tab.SessionID() != "" {
		t.Fatal("starting over kept the session")
	}
}

// A conversation holding one message ID twice, as an earlier chat's may,
// shows both.
func TestRepeatedMessageIDs(t *testing.T) {
	_, tt, _, tab := chatWith(t)
	for i, text := range []string{"first", "second"} {
		tab.transcript.Apply(event(agent.ItemCompleted{Item: agent.Item{ID: fmt.Sprint("u", i), Kind: agent.ItemKindUserMessage, ClientID: "dgopher-1", Text: text}}))
	}
	tt.Frame()
	if !tt.HasText("first") || !tt.HasText("second") {
		t.Fatalf("texts %q", tt.Texts())
	}
}

// A tool call's arguments read as their values, in order; what is not an
// object of plain values stays JSON.
func TestArgsSummary(t *testing.T) {
	for in, want := range map[string]string{
		`{"table":"orders","schema":"main"}`:            "orders · main",
		`{"sql":"SELECT 1","series":["n","m"],"x":"k"}`: "SELECT 1 · n, m · k",
		`{"connection":"","table":"t"}`:                 "t",
		`{"nested":{"a":1}}`:                            `{"nested":{"a":1}}`,
		`[1,2]`:                                         `[1,2]`,
	} {
		if got := argsSummary(json.RawMessage(in)); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

// Codex is told to turn off only the features it has: one it does not
// know, it refuses to start with; one removed, it lists but has no more.
func TestCodexOff(t *testing.T) {
	known := parseCodexFeatures("apps   stable   true\nshell_tool  stable  true\nmulti_agent  under development  false\nworktrees  removed  false\n")
	if !known["shell_tool"] || !known["multi_agent"] || known["worktrees"] {
		t.Fatalf("known %v", known)
	}
	off := codexOff(known)
	if !slices.Contains(off, "shell_tool") || !slices.Contains(off, "apps") || slices.Contains(off, "worktrees") || slices.Contains(off, "hooks") {
		t.Fatalf("off %v", off)
	}
	if got := codexOff(nil); !slices.Equal(got, codexFeaturesOff) {
		t.Fatal("unread, not all of them")
	}
}

// The editor's text goes with a message only where the agent has not had
// it as it is: the statement at the caret goes with each.
func TestTextSentOnce(t *testing.T) {
	_, tt, ed, tab := chatWith(t)
	ed.ctx = EditorContext{Text: "SELECT 1;\nSELECT 2;\n", Statement: "SELECT 2;", From: 2, To: 2}
	next := func(msg string) string {
		n := len(replies(tab))
		tab.Send(msg)
		testutil.WaitFor(t, tt, "the reply", func() bool { return len(replies(tab)) == n+1 && !tab.Busy() })
		return replies(tab)[n] // the fake's reply is the message
	}
	first, second := next("one"), next("two")
	if !strings.Contains(first, "SELECT 1;\nSELECT 2;") || strings.Contains(second, "SELECT 1;") ||
		!strings.Contains(second, "as in the last message") || !strings.Contains(second, "SELECT 2;") {
		t.Fatalf("first:\n%s\nsecond:\n%s", first, second)
	}
	ed.ctx.Text = "SELECT 3;\n"
	if third := next("three"); !strings.Contains(third, "SELECT 3;") || strings.Contains(third, "as in the last message") {
		t.Fatalf("third:\n%s", third)
	}
}

// Reads that reach files or URLs are refused, whatever the user allowed
// before; names in strings and comments are not calls.
func TestFileReadersRefused(t *testing.T) {
	for _, c := range []struct {
		engine db.Engine
		sql    string
		refuse bool
	}{
		{db.DuckDB, "SELECT content FROM read_text('/home/u/.ssh/id_ed25519')", true},
		{db.DuckDB, "SELECT * FROM 'https://example.com/x.csv'", true},
		{db.DuckDB, "SELECT * FROM t JOIN '/tmp/a.parquet' USING (id)", true},
		{db.Postgres, "SELECT pg_read_file('/etc/passwd')", true},
		{db.MySQL, "SELECT LOAD_FILE('/etc/passwd')", true},
		{db.ClickHouse, "SELECT * FROM url('http://x', CSV)", true},
		{db.SQLite, "SELECT readfile('a')", true},
		{db.DuckDB, "SELECT * FROM range(1) r, '/home/sr/.ssh/id_ed25519.csv' f", true},
		{db.DuckDB, `SELECT * FROM "/etc/hosts.csv"`, true},
		{db.DuckDB, `SELECT * FROM "data.parquet"`, true},
		{db.DuckDB, `SELECT length(content) FROM "read_text"('/etc/passwd')`, true},
		{db.DuckDB, "SELECT * FROM sqlite_scan('x.db', 't')", true},
		{db.Postgres, `SELECT "pg_read_file"('/etc/passwd')`, true},
		{db.Postgres, `SELECT U&"pg_read_file"('/etc/passwd')`, true},
		{db.Postgres, `SELECT U&"\0070g_read_file"('/etc/passwd')`, true},
		{db.DuckDB, `SELECT "users"."name" FROM "users"`, false},
		{db.Postgres, "SELECT 'pg_read_file(x)' AS s -- pg_read_file(", false},
		{db.DuckDB, "SELECT read_text FROM t", false},
		{db.Postgres, "SELECT count(*) FROM orders", false},
	} {
		cfg := db.Config{Name: "x", Engine: c.engine}
		why := readRefusal(&cfg, c.sql)
		if got := strings.Contains(why, "files or URLs"); got != c.refuse {
			t.Errorf("%s %q: %q", c.engine, c.sql, why)
		}
	}
}

// No password in the editor goes to the agent.
func TestContextRedacted(t *testing.T) {
	_, _, ed, tab := chatWith(t)
	ed.ctx = EditorContext{Text: "ALTER ROLE app PASSWORD 's3cret';\n", Statement: "ALTER ROLE app PASSWORD 's3cret'", From: 1, To: 1}
	if block := tab.withContext("hi"); strings.Contains(block, "s3cret") || !strings.Contains(block, "PASSWORD '•••'") {
		t.Fatalf("block:\n%s", block)
	}
}

// Cut output ends on a whole character.
func TestClipWholeCharacters(t *testing.T) {
	s := strings.Repeat("é", maxToolOutput) // two bytes each
	if got := clip(s); !utf8.ValidString(got) || len(got) > maxToolOutput+len("\n…") {
		t.Fatalf("cut at %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
}

// A tool call left running by an agent that stopped shows as stopped.
func TestToolLeftRunning(t *testing.T) {
	_, tt, _, tab := chatWith(t)
	tab.transcript.Apply(event(agent.ItemStarted{Item: agent.Item{ID: "t1", Kind: agent.ItemKindToolCall, Status: agent.ItemStatusInProgress, Tool: &agent.ToolCall{Name: "describe_table"}}}))
	tt.Frame()
	if tab.Busy() || !tt.HasText("stopped") {
		t.Fatalf("busy %v, texts %q", tab.Busy(), tt.Texts())
	}
}

// An agent whose command line is not there says to install it.
func TestNotInstalled(t *testing.T) {
	_, tt, _, tab := chatWith(t)
	OpenClient = func(ctx context.Context, provider string) (*agent.Client, string, error) {
		client, err := agent.Open(ctx, agent.WithProviders(map[string]any{provider: agent.ClaudeOptions{Path: filepath.Join(t.TempDir(), "claude")}}))
		return client, provider, err
	}
	tab.Send("hi")
	testutil.WaitFor(t, tt, "the error", func() bool { return tab.startErr != "" })
	if !strings.Contains(tab.startErr, "Claude Code is not installed") {
		t.Fatalf("start error %q", tab.startErr)
	}
}

// The models are listed through the chat's own client: the fake lists
// none, which is no error. Closing the chat while they load leaves no
// error for the other chats.
func TestModelsListed(t *testing.T) {
	_, tt, _, tab := chatWith(t)
	l := tab.models()
	testutil.WaitFor(t, tt, "the models", func() bool { return !l.loading })
	if l.err != "" || len(l.models) != 0 || modelLists["claude"] != l {
		t.Fatalf("listed %v, error %q", l.models, l.err)
	}
	forgetModels("claude")
	tab.Close()
	l = tab.models()
	testutil.WaitFor(t, tt, "the closed chat's list", func() bool { return modelLists["claude"] == nil })
	if l.err != "" {
		t.Fatalf("error %q kept for the other chats", l.err)
	}
}
