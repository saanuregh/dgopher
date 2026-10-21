// Package chat is a SQL editor's AI chat: a conversation with a coding
// agent's command line, Claude Code, Codex or pi, through agent-cli-connector,
// beside the editor. Each message carries what the editor shows: its
// connection, the selection or the statement at the caret, its text and
// its last error. The agent has none of its own tools, settings, hooks,
// plugins or MCP servers (lockDown): only the app's, which read the
// connections and their schemas, and run reads only once the user allows
// each. SQL it writes, the user puts in the editor.
package chat

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	agent "github.com/saanuregh/agent-cli-connector"
	"github.com/saanuregh/agent-cli-connector/claude"
	"github.com/saanuregh/agent-cli-connector/codex"
	"github.com/saanuregh/agent-cli-connector/pi"

	"dgopher/internal/connection"
	"dgopher/internal/redact"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// Host is what a chat needs of the app.
type Host interface {
	dataview.Host
	// Connections are the app's connections, as the navigator lists them.
	Connections() []*connection.Conn
}

// Editor is the SQL editor a chat is about.
type Editor interface {
	Connection() *connection.Conn
	// EditorContext is what the editor shows now, which each message
	// carries.
	EditorContext() EditorContext
	// InsertSQL puts SQL in place of the selection, or at the caret, as
	// a change undo takes back. Nothing runs.
	InsertSQL(sql string)
	// ChartSQL puts a read in the editor, runs it there as the editor
	// runs what the user typed, and shows its rows as a chart drawn as
	// settings say; done is told of the rows, on the UI thread. It
	// returns why it does not, "" when it does.
	ChartSQL(sql string, settings dataview.ChartSettings, done func(ChartResult)) string
}

// ChartResult is what a chart in the editor drew: the rows' columns and
// the rows read, or why there are none.
type ChartResult struct {
	Columns []string
	Rows    [][]any
	Err     string
}

// EditorContext is what an editor shows: its file, its text, the
// selection or else the statement at the caret, with their lines
// counted from 1, and its last run's error.
type EditorContext struct {
	File, Database string
	Text           string
	Selection      string
	Statement      string
	// From and To are the lines of the selection, or of the statement.
	From, To  int
	LastError string
}

// Providers are the agents a chat can talk to, by name, in the order
// the picker shows them.
var Providers = []ProviderInfo{
	{Name: "claude", Label: "Claude Code", Command: "claude"},
	{Name: "pi", Label: "pi", Command: "pi"},
	{Name: "codex", Label: "Codex", Command: "codex"},
}

// ProviderInfo names an agent's command line.
type ProviderInfo struct {
	Name, Label, Command string
}

// providerInfo is the provider of a name; one unknown is named, and
// run, as it is.
func providerInfo(name string) ProviderInfo {
	for _, p := range Providers {
		if p.Name == name {
			return p
		}
	}
	return ProviderInfo{Name: name, Label: name, Command: name}
}

func providerLabel(name string) string { return providerInfo(name).Label }

// NewProvider makes the provider of a name; tests put a scripted one in
// its place.
var NewProvider = func(name string) agent.Provider {
	switch name {
	case "pi":
		return pi.New(pi.Options{})
	case "codex":
		return codex.New(codex.Options{Disable: codexOff(codexKnown()), Config: codexConfig})
	default:
		return claude.New(claude.Options{ClientApp: "dgopher"})
	}
}

// Chat is an editor's chat.
type Chat struct {
	a Host
	// provider is the agent's name; ed the editor the chat is about; dir
	// where the agent works.
	provider string
	ed       Editor
	dir      string
	// modelID and effort are those chosen, "" for the agent's default.
	modelID string
	effort  agent.Effort
	// resume is the session of an earlier run of the app that the next
	// start continues, "" for a new one.
	resume string
	// textSent is the editor's text as the last message carried it, nil
	// before one did in this session; frameCtx what the editor showed as
	// the frame began.
	textSent *string
	frameCtx EditorContext

	sess     agent.Session
	starting bool
	// gen counts the sessions started, by which the events of one
	// stopped are told from the next's.
	gen      int
	startErr string
	closed   bool

	transcript agent.Transcript
	// sent are the messages sent that the agent did not show back yet,
	// by their client ID.
	sent []sentMessage
	// waiting are the messages typed before the session started.
	waiting []string
	running bool
	turnAt  time.Time
	input   string
	// wantFocus asks the message field to take the keys.
	wantFocus bool
	// OnHide hides the chat, nil where it cannot be.
	OnHide func()
	// fx is the chat's motion; fieldFocused whether the message field
	// had the keys in the last frame.
	fx           glow
	fieldFocused bool
	picker       modelPicker
	// requests wait for the user: the agent's own asking to run a tool,
	// and the app's tools asking to run a read.
	requests []*request
	notices  []notice
	model    string
	cost     float64
	// expanded are the tool calls and reasoning shown in full, by item.
	expanded map[string]bool
	// follow keeps the transcript scrolled to its end as it grows,
	// until the user scrolls up.
	scroll   ui.ScrollState
	follow   bool
	lastLen  int
	inputKey int
	// allowReads lets the app's read tool run reads on a connection
	// without asking again, by connection ID, for this chat.
	allowReads map[string]bool
	// connecting are the connections the user is asked to connect, by ID.
	connecting map[string]*connectAsk
	nextID     atomic.Int64
	// idPrefix starts the IDs of the chat's messages: its own, as a
	// conversation continued holds those of the chats before.
	idPrefix string
}

type sentMessage struct {
	id, text string
}

type notice struct {
	text string
	err  bool
}

// request is something the user answers in the chat: a tool the agent
// asks to run, a question it asks, or a read the app's tool asks to run.
type request struct {
	id     string
	title  string
	detail string
	sql    string
	conn   string
	// connName names conn for the card.
	connName string
	// perm is the agent's request; question its question; answer the
	// app's tool's, which waits for a decision.
	perm     *agent.PermissionRequest
	question *agent.UserInputRequest
	answer   chan decision
	// always offers to allow reads of the connection for the rest of the
	// chat; connect asks to connect a connection, not to run something.
	always  bool
	connect bool
}

type decision int

const (
	deny decision = iota
	allow
	allowAlways
)

// New is a chat about an editor with the agent of a provider, working in
// dir, with the model and effort last chosen for it.
func New(a Host, ed Editor, provider, dir string) *Chat {
	t := &Chat{a: a, ed: ed, dir: dir, expanded: map[string]bool{}, follow: true, allowReads: map[string]bool{}, connecting: map[string]*connectAsk{}, fx: newGlow(),
		idPrefix: fmt.Sprintf("dgopher-%08x-", rand.Uint32())}
	t.setProvider(provider)
	return t
}

// setProvider takes an agent, with the model and effort last chosen for
// it.
func (t *Chat) setProvider(provider string) {
	s := t.a.Settings()
	t.provider, t.modelID, t.effort = provider, s.ChatModels[provider], agent.Effort(s.ChatEfforts[provider])
}

// conn is the editor's connection.
func (t *Chat) conn() *connection.Conn { return t.ed.Connection() }

// Focus has the message field take the keys.
func (t *Chat) Focus() { t.wantFocus = true }

// Provider is the agent's name.
func (t *Chat) Provider() string { return t.provider }

// SessionID is the agent's session, to continue in a later run of the
// app: the one running, else the one the chat is to continue; "" for
// none yet.
func (t *Chat) SessionID() string {
	if t.sess != nil {
		return t.sess.ID()
	}
	return t.resume
}

// historyItems is how much of an earlier conversation shows again.
const historyItems = 400

// Resume has the chat continue an earlier run's session: its messages
// show again now, read from the agent's store; the agent starts with the
// next message.
func (t *Chat) Resume(session string) {
	if session == "" || t.sess != nil || t.starting {
		return
	}
	t.resume = session
	gen, name := t.gen, t.provider
	go func() {
		defer dataview.RecoverBackground(t.a.Post, t.a.ShowReport, nil)
		reader, ok := NewProvider(name).(agent.HistoryReader) // off the UI thread, as in start
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		items, err := reader.ReadHistory(ctx, session, agent.ReadHistoryOptions{Cwd: t.dir, Limit: historyItems})
		t.a.Post(func() {
			if gen != t.gen || t.closed || !t.transcriptEmpty() {
				return // started over, or written in, meanwhile
			}
			if err != nil {
				t.notices = append(t.notices, notice{text: "The earlier conversation did not show: " + redact.Secrets(err.Error())})
				return
			}
			for _, it := range items {
				t.transcript.Apply(&agent.ItemCompleted{Item: it})
			}
		})
	}()
}

// Busy reports a reply being written.
func (t *Chat) Busy() bool { return t.running || t.starting }

// CloseReason says why closing the chat's editor asks first: a reply
// being written.
func (t *Chat) CloseReason() string {
	if t.running {
		return "The AI chat is still answering. Closing the editor stops it."
	}
	return ""
}

// Close stops the agent.
func (t *Chat) Close() {
	t.closed = true
	t.denyAll("the chat was closed")
	if sess := t.sess; sess != nil {
		t.sess = nil
		go func() {
			defer dataview.RecoverBackground(t.a.Post, t.a.ShowReport, nil)
			sess.Close()
		}()
	}
}

// denyAll answers every request waiting with a refusal.
func (t *Chat) denyAll(why string) {
	for _, r := range slices.Clone(t.requests) { // answer takes each out
		t.answer(r, deny, why)
	}
	t.requests = nil
}

// systemPrompt tells the agent where it is and what the app's tools do.
const systemPrompt = `You are the assistant inside a SQL editor of DGopher, a desktop database client. The user asks about the SQL in the editor, its connection's data, and SQL to write.
You cannot read or change files, or run commands: your only tools are DGopher's, below.
Each user message begins with an <editor> block: the editor's connection, its file, the selection or else the statement at the caret with its lines, the editor's text, and the last run's error. It is what the user sees now; "this", "the query" and "the error" refer to it. Where it says the editor's text is as in the last message, it is the text the last block gave. Do not repeat the block back.
A connection listed with noRowsToAgent keeps its data from you: run_read_query refuses it, and a chart of it tells you its columns and count only. Help from the schema, and write SQL for the user to run.
When a tool needs a connection that is not open, DGopher asks the user to connect it, and the tool goes on once it is; if the user does not, answer without it.
DGopher's own tools: list_connections, list_tables and describe_table read the connections and their schemas; run_read_query runs one read-only statement once the user allows it, on a connection that is not production, and returns at most a few hundred rows. Without a connection named, they use the editor's. chart_in_editor charts a read in the editor itself, once the user allows it: use it when the user asks for a chart or a plot, aggregating in SQL so that the chart has few rows.
Write SQL in fenced sql code blocks, one statement or script per block, for the editor's connection: the user inserts a block in the editor with a click, and runs it there. Never run statements that change data or schema yourself. Do not ask for passwords: DGopher keeps them.`

// start starts the agent's session, then sends what was typed meanwhile.
func (t *Chat) start() {
	if t.sess != nil || t.starting || t.closed {
		return
	}
	t.starting, t.startErr = true, ""
	t.models() // for the effort menu, which shows once they come
	t.gen++
	gen := t.gen
	cfg := agent.Config{
		Cwd:                t.dir,
		Resume:             t.resume,
		Model:              t.modelID,
		Effort:             t.effort,
		AppendSystemPrompt: systemPrompt + t.contextPrompt(),
		Tools:              t.tools(),
		Logger:             slog.New(slog.DiscardHandler),
		Title:              "DGopher chat",
	}
	lockDown(t.provider, &cfg)
	name := t.provider
	go func() {
		defer dataview.RecoverBackground(t.a.Post, t.a.ShowReport, func() { t.starting = false })
		// Made here, not on the UI thread: Codex's asks its command line
		// which features it has.
		provider := NewProvider(name)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		sess, err := provider.Start(ctx, cfg)
		t.a.Post(func() {
			if gen != t.gen || t.closed {
				if err == nil {
					go sess.Close() // a start the chat no longer waits for
				}
				return
			}
			t.starting = false
			if err != nil && t.resume != "" {
				// The conversation went from the agent's store: a new one,
				// what was typed going to it.
				t.resume, t.textSent = "", nil
				t.notices = append(t.notices, notice{text: "The earlier conversation could not be continued (" + redact.Secrets(err.Error()) + "); this message starts a new one."})
				t.start()
				return
			}
			if err != nil {
				t.startErr = startError(t.provider, err)
				return
			}
			t.sess = sess
			go t.watch(sess, gen)
			waiting := t.waiting
			t.waiting = nil
			for _, text := range waiting {
				t.sendNow(text)
			}
		})
	}()
}

// lockDown leaves the agent the app's tools alone: none of its own, which
// read and write files and run commands, nor what the user's or the
// project's settings add, as hooks, plugins, skills, extensions and MCP
// servers. Whatever is left that is not the app's, it refuses without
// asking (dontAsk).
func lockDown(provider string, cfg *agent.Config) {
	cfg.PermissionMode = agent.PermissionDontAsk
	switch provider {
	case "codex":
		// Codex has no switch for all of its tools: its shell, patches,
		// web search and the rest go by feature (codexFeaturesOff) and
		// config (codexConfig), and its sandbox, read-only, holds what
		// would be left.
		cfg.PermissionMode = agent.PermissionReadOnly
	case "pi":
		no := false
		cfg.ProviderOptions = &pi.SessionOptions{NoBuiltinTools: true, NoExtensions: true, NoSkills: true,
			NoPromptTemplates: true, NoMCP: true, TrustProject: &no}
	default:
		cfg.ProviderOptions = &claude.SessionOptions{BuiltinTools: []string{}, SettingSources: []string{}, StrictMCPConfig: true}
	}
}

// codexFeaturesOff are Codex's features that give it tools of its own,
// or load what the user's config adds: off, the chat leaves it DGopher's.
// Those the installed Codex lacks are left out (codexOff).
var codexFeaturesOff = []string{
	"shell_tool", "unified_exec", "shell_snapshot", "view_image", "image_generation",
	"multi_agent", "multi_agent_v2", "plugins", "remote_plugin", "apps", "hooks",
	"browser_use", "browser_use_external", "in_app_browser", "computer_use",
	"skill_search", "skill_mcp_dependency_install", "tool_suggest",
	"memories", "worktrees", "workspace_dependencies",
}

// codexConfig turns off what Codex's features do not: web search, and
// the MCP servers of the user's config. Its patch tool goes with its
// shell.
var codexConfig = map[string]string{
	"web_search":  `"disabled"`,
	"mcp_servers": "{}",
}

// startError says why the agent did not start, with what to do.
func startError(provider string, err error) string {
	msg := redact.Secrets(err.Error())
	p := providerInfo(provider)
	if strings.Contains(msg, "executable file not found") || strings.Contains(msg, "no such file") {
		return fmt.Sprintf("%s is not installed, or not on the PATH: the chat runs its command line, %s. Install it, sign in, then try again.", p.Label, p.Command)
	}
	return p.Label + " did not start: " + msg
}

// contextPrompt names the editor's connection.
func (t *Chat) contextPrompt() string {
	cn := t.conn()
	if cn == nil {
		return ""
	}
	cfg := cn.Config
	return fmt.Sprintf("\nThe editor is on the connection %q (%s, %s).", cfg.Name, cfg.Engine.Label(), cfg.Env.Label())
}

// watch hands the session's events to the UI thread, in order, until it
// ends.
func (t *Chat) watch(sess agent.Session, gen int) {
	defer dataview.RecoverBackground(t.a.Post, t.a.ShowReport, nil)
	for ev := range sess.Events() {
		t.a.Post(func() {
			if gen == t.gen {
				t.apply(ev)
			}
		})
	}
	t.a.Post(func() {
		if gen != t.gen || t.sess != sess {
			return
		}
		t.sess, t.running, t.textSent = nil, false, nil // a new session has had nothing
		if err := sess.Err(); err != nil && !t.closed {
			t.notices = append(t.notices, notice{text: providerLabel(t.provider) + " stopped: " + redact.Secrets(err.Error()), err: true})
		}
		t.denyAll("the agent stopped")
	})
}

// apply takes an event of the session.
func (t *Chat) apply(ev agent.Event) {
	t.transcript.Apply(ev)
	switch ev := ev.(type) {
	case *agent.SessionStarted:
		t.model = ev.Info.Model
	case *agent.SessionUpdated:
		t.model = ev.Info.Model
	case *agent.TurnStarted:
		t.running, t.turnAt = true, time.Now()
	case *agent.TurnCompleted:
		t.running = false
		if r := ev.Result; r != nil {
			t.cost += r.Usage.CostUSD
			if r.Err != nil && !errors.Is(r.Err, agent.ErrInterrupted) {
				t.notices = append(t.notices, notice{text: redact.Secrets(r.Err.Error()), err: true})
			}
		}
	case *agent.ItemCompleted:
		if ev.Item.Kind == agent.ItemUserMessage {
			t.sent = slices.DeleteFunc(t.sent, func(s sentMessage) bool { return s.id == ev.Item.ClientID || s.text == ev.Item.Text })
		}
	case *agent.Notice:
		if ev.Level != agent.NoticeInfo {
			t.notices = append(t.notices, notice{text: redact.Secrets(ev.Message), err: ev.Level == agent.NoticeError})
		}
	case *agent.PermissionRequest:
		// The agent's own tools are not the chat's (lockDown): what one
		// still asks is refused, unasked.
		t.notices = append(t.notices, notice{text: "Refused: " + permissionTitle(ev) + ". The chat uses DGopher's tools alone."})
		r := &request{id: ev.ID, perm: ev}
		t.answer(r, deny, "DGopher's chat allows only DGopher's own tools; write the SQL instead, or use them.")
	case *agent.UserInputRequest:
		t.requests = append(t.requests, &request{id: ev.ID, title: cmp.Or(ev.Title, "A question"), question: ev})
	case *agent.RequestResolved:
		id := ev.Request.RequestID()
		t.requests = slices.DeleteFunc(t.requests, func(r *request) bool { return r.answer == nil && r.id == id })
	}
}

// permissionTitle is what a request of the agent asks, on a line.
func permissionTitle(r *agent.PermissionRequest) string {
	switch {
	case r.Title != "":
		return r.Title
	case r.Kind == agent.PermissionCommand && r.Command != "":
		return "Run a command"
	case r.Kind == agent.PermissionFileChange:
		return "Change files"
	case r.Kind == agent.PermissionPlanApproval:
		return "Approve the plan"
	case r.ToolName != "":
		return "Use " + r.ToolName
	}
	return "Allow an action"
}

// permissionDetail is what a request of the agent would do.
func permissionDetail(r *agent.PermissionRequest) string {
	var parts []string
	if r.Description != "" {
		parts = append(parts, r.Description)
	}
	if r.Command != "" {
		parts = append(parts, r.Command)
	}
	for _, ch := range r.Changes {
		parts = append(parts, string(ch.Kind)+" "+ch.Path)
	}
	if r.Plan != "" {
		parts = append(parts, r.Plan)
	}
	if len(parts) == 0 && len(r.Input) > 0 {
		parts = append(parts, widgets.OneLine(string(r.Input), 600))
	}
	return strings.Join(parts, "\n")
}

// Send sends a message, starting the agent first when it has not.
func (t *Chat) Send(text string) {
	text = strings.TrimSpace(text)
	if text == "" || t.closed {
		return
	}
	text = t.withContext(text)
	t.follow = true
	if t.sess == nil {
		t.waiting = append(t.waiting, text)
		t.start()
		return
	}
	t.sendNow(text)
}

// sendNow sends a message to the session started: while a reply is
// written, it waits for the reply's end.
func (t *Chat) sendNow(text string) {
	sess := t.sess
	id := t.idPrefix + strconv.FormatInt(t.nextID.Add(1), 10)
	t.sent = append(t.sent, sentMessage{id: id, text: text})
	in := agent.Text(text)
	in.ClientID = id
	go func() {
		defer dataview.RecoverBackground(t.a.Post, t.a.ShowReport, nil)
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := sess.Send(ctx, in); err != nil {
			t.a.Post(func() {
				t.sent = slices.DeleteFunc(t.sent, func(s sentMessage) bool { return s.id == id })
				t.notices = append(t.notices, notice{text: "Not sent: " + redact.Secrets(err.Error()), err: true})
				t.textSent = nil // the agent did not get it
			})
		}
	}()
}

// Stop interrupts the reply being written.
func (t *Chat) Stop() {
	sess := t.sess
	if sess == nil || !t.running {
		return
	}
	t.denyAll("the user stopped the reply")
	t.goWithin(10*time.Second, func(ctx context.Context) { sess.Interrupt(ctx) })
}

// Restart ends the conversation and starts a new one, with the same
// agent, or another.
func (t *Chat) Restart(provider string) {
	if t.running {
		t.Stop()
	}
	if sess := t.sess; sess != nil {
		go sess.Close()
	}
	t.denyAll("the chat started over")
	t.sess, t.starting, t.startErr = nil, false, ""
	t.gen++
	t.resume, t.textSent = "", nil
	t.setProvider(provider)
	t.transcript.Reset()
	t.sent, t.waiting, t.notices, t.model, t.cost = nil, nil, nil, "", 0
	t.running = false
	t.allowReads = map[string]bool{}
}

// answer answers a request: the agent's, or the app's tool's.
func (t *Chat) answer(r *request, d decision, why string) {
	t.requests = slices.DeleteFunc(t.requests, func(o *request) bool { return o == r })
	switch {
	case r.answer != nil:
		select {
		case r.answer <- d:
		default:
		}
	case r.perm != nil:
		perm := r.perm
		t.goWithin(30*time.Second, func(ctx context.Context) {
			switch d {
			case allow:
				perm.Allow(ctx)
			case allowAlways:
				perm.AllowForSession(ctx)
			default:
				perm.Deny(ctx, cmp.Or(why, "The user refused."))
			}
		})
	case r.question != nil:
		q := r.question
		t.goWithin(30*time.Second, func(ctx context.Context) { q.Cancel(ctx) })
	}
}

// answerQuestion answers the agent's question with an option.
func (t *Chat) answerQuestion(r *request, q agent.Question, label string) {
	t.requests = slices.DeleteFunc(t.requests, func(o *request) bool { return o == r })
	ui := r.question
	t.goWithin(30*time.Second, func(ctx context.Context) { ui.Answer(ctx, map[string][]string{q.ID: {label}}) })
}

// goWithin runs f off the UI thread, its context ending after timeout.
func (t *Chat) goWithin(timeout time.Duration, f func(ctx context.Context)) {
	go func() {
		defer dataview.RecoverBackground(t.a.Post, t.a.ShowReport, nil)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		f(ctx)
	}()
}
