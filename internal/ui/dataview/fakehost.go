package dataview

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/safety"
	"dgopher/internal/secretcmd"
	"dgopher/internal/settings"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// FakeHost is a Host for tests, of this package and of those built on it:
// a window of tabs whose errors, toasts, confirmations and audit records
// are kept to check. Connections open as the app opens them, without its
// prompts.
type FakeHost struct {
	tb       TB
	settings settings.Settings
	layout   widgets.Layout
	Project  *project.Project
	Conns    []*connection.Conn
	Tabs     []widgets.Tab
	active   int
	dialogs  Dialogs
	focus    string

	// What the app would have shown or kept.
	Confirm *widgets.ConfirmRequest
	Pending *PendingRequest
	Discard func()
	Errors  []string
	Toasts  []string
	// ToastRun is what the button of the last toast does, nil without one.
	ToastRun func()
	// Notified are the notifications asked for, as "title: body", however
	// long the work took.
	Notified  []string
	Clipboard string
	Queries   []string // texts of the query tabs asked for
	Events    []audit.Event

	mu    sync.Mutex
	queue []func()
}

// PendingRequest is a question about changes not applied yet, with what
// each answer does.
type PendingRequest struct {
	N                      int
	Apply, Discard, Cancel func()
}

// TB is what FakeHost needs of a test: testing.TB's methods.
type TB interface {
	Helper()
	TempDir() string
	Fatal(args ...any)
	Cleanup(func())
}

// NewFakeHost returns a host with one empty project, in a folder of the
// test's.
func NewFakeHost(t TB) *FakeHost {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p, _, err := project.Load(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	h := &FakeHost{tb: t, settings: settings.Default(), layout: widgets.DefaultLayout(), Project: p}
	// As the app quits: the work posted since the last frame first, as it
	// may hand a tab its session, then the tabs, as their sessions hold a
	// connection of the pools, then the pools, then the project's state.
	t.Cleanup(func() {
		h.mu.Lock()
		queue := h.queue
		h.queue = nil
		h.mu.Unlock()
		for _, fn := range queue {
			fn()
		}
		for _, tab := range h.Tabs {
			tab.Close()
		}
		for _, cn := range h.Conns {
			if cn.DB != nil {
				cn.DB.Close()
			}
			if cn.KV != nil {
				cn.KV.Close()
			}
		}
		p.Close()
	})
	return h
}

// AddConn adds a connection of the host's project, not yet connected.
func (h *FakeHost) AddConn(cfg db.Config) *connection.Conn {
	cfg.ID = h.Project.Prefix + cfg.ID
	cn := &connection.Conn{Config: cfg, Project: h.Project}
	cn.Reset()
	h.Conns = append(h.Conns, cn)
	return cn
}

// View draws the active tab and the data view's dialogs, after the work
// posted since the last frame, as the app does.
func (h *FakeHost) View(c *ui.Context) {
	h.mu.Lock()
	queue := h.queue
	h.queue = nil
	h.mu.Unlock()
	for _, fn := range queue {
		fn()
	}
	if h.active < len(h.Tabs) {
		h.Tabs[h.active].View(c)
	}
	if ExportOpen(h) {
		ExportView(h, c)
	}
	DialogsView(h, c)
	if r := h.Confirm; r != nil {
		widgets.ConfirmView(c, r)
		if !r.Open {
			if !r.Answered && r.OnCancel != nil {
				r.OnCancel()
			}
			h.Confirm = nil
		}
	}
}

func (h *FakeHost) Post(fn func()) {
	h.mu.Lock()
	h.queue = append(h.queue, fn)
	h.mu.Unlock()
}

func (h *FakeHost) Background(work func() func()) {
	go func() {
		defer RecoverBackground(h.Post, h.ShowError, nil)
		if done := work(); done != nil {
			h.Post(done)
		}
	}()
}

// StartJob keeps no job: nothing quits or disconnects the fake.
func (h *FakeHost) StartJob(cn *connection.Conn, title string, cancel context.CancelFunc) (finish func()) {
	return func() {}
}

func (h *FakeHost) Settings() *settings.Settings { return &h.settings }

func (h *FakeHost) Layout() *widgets.Layout { return &h.layout }

func (h *FakeHost) SaveSettings() {}

func (h *FakeHost) ShowError(title, message string) { h.Errors = append(h.Errors, title+": "+message) }

func (h *FakeHost) Toast(text, action string, run func()) {
	h.Toasts = append(h.Toasts, text)
	h.ToastRun = nil
	if action != "" {
		h.ToastRun = run
	}
}

func (h *FakeHost) Notify(started time.Time, title, body string, show func()) {
	h.Notified = append(h.Notified, title+": "+body)
}

func (h *FakeHost) WriteClipboard(text string) { h.Clipboard = text }

func (h *FakeHost) ReadClipboard() string { return h.Clipboard }

func (h *FakeHost) Now() time.Time { return time.Now() }

func (h *FakeHost) FocusWant() *string { return &h.focus }

func (h *FakeHost) Dialogs() *Dialogs { return &h.dialogs }

// Connect opens the connection as the app does, without its prompts.
func (h *FakeHost) Connect(cn *connection.Conn, then func()) {
	switch cn.Status {
	case connection.StatusConnected:
		if then != nil {
			then()
		}
		return
	case connection.StatusConnecting:
		if then != nil {
			cn.Waiters = append(cn.Waiters, then)
		}
		return
	}
	if then != nil {
		cn.Waiters = append(cn.Waiters, then)
	}
	cn.Status = connection.StatusConnecting
	cfg := cn.Config
	h.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var d *db.DB
		var kv *db.KV
		var err error
		if cfg.Engine == db.Redis {
			kv, err = db.OpenRedis(ctx, cfg, nil)
		} else {
			d, err = db.Open(ctx, cfg, nil)
		}
		return func() {
			if err != nil {
				cn.Status, cn.Err, cn.Waiters = connection.StatusFailed, err.Error(), nil
				h.ShowError("Could not connect to "+cfg.Name, err.Error())
				return
			}
			cn.Reset()
			cn.Status, cn.DB, cn.KV = connection.StatusConnected, d, kv
			if d != nil {
				connection.LoadSchemas(h, cn, "", nil)
			}
			waiters := cn.Waiters
			cn.Waiters = nil
			for _, w := range waiters {
				w()
			}
		}
	})
}

func (h *FakeHost) AddTab(t widgets.Tab) {
	h.Tabs = append(h.Tabs, t)
	h.active = len(h.Tabs) - 1
}

func (h *FakeHost) ReplaceTab(old, next widgets.Tab) {
	if i := slices.Index(h.Tabs, old); i >= 0 {
		h.Tabs[i] = next
		old.Close()
	}
}

func (h *FakeHost) KeysTo(t widgets.Tab) bool { return t == h.ActiveTab() }

func (h *FakeHost) ActiveTab() widgets.Tab {
	if h.active < len(h.Tabs) {
		return h.Tabs[h.active]
	}
	return nil
}

func (h *FakeHost) ActivateTab(match func(widgets.Tab) bool) bool {
	for i, t := range h.Tabs {
		if match(t) {
			h.active = i
			return true
		}
	}
	return false
}

func (h *FakeHost) OpenTable(cn *connection.Conn, database string, obj db.Object, page int) {
	if h.ActivateTab(func(t widgets.Tab) bool {
		tt, ok := t.(*TableTab)
		if ok && tt.Conn == cn && tt.Database == database && tt.Object.Schema == obj.Schema && tt.Object.Name == obj.Name {
			tt.Page = page
			return true
		}
		return false
	}) {
		return
	}
	h.Connect(cn, func() { h.AddTab(NewTableTab(h, cn, database, obj, page)) })
}

func (h *FakeHost) NewQueryTab(cn *connection.Conn, database, text string) {
	h.Queries = append(h.Queries, text)
}

func (h *FakeHost) AskConfirm(cn *connection.Conn, v safety.Verdict, title, action, preview string, onConfirm func()) *widgets.ConfirmRequest {
	h.Confirm = &widgets.ConfirmRequest{Open: true, Conn: cn, Title: title, Reasons: v.Reasons, Preview: preview,
		TypeName: v.TypeName, Action: action, OnConfirm: onConfirm}
	return h.Confirm
}

// AskDiscard is the Redis tab's, whose tests use this host too.
func (h *FakeHost) AskDiscard(title, reason string, onDiscard func()) { h.Discard = onDiscard }

func (h *FakeHost) AskPending(n int, apply, discard, cancel func()) {
	h.Pending = &PendingRequest{N: n, Apply: apply, Discard: discard, Cancel: cancel}
}

func (h *FakeHost) RecordBlocked(cn *connection.Conn, why, what string) {
	h.Events = append(h.Events, audit.Event{Kind: audit.KindBlocked, Statement: what, Detail: why})
}

// Record writes the error as the app's does.
func (h *FakeHost) Record(cfg *db.Config, e audit.Event) {
	if e.Err != nil && e.Error == "" {
		e.Error = secretcmd.AuditText(e.Err)
	}
	h.Events = append(h.Events, e)
}

func (h *FakeHost) RecordRun(cfg db.Config, kind, database, stmt string, rows int64, d time.Duration, err error) {
	h.Record(&cfg, audit.Event{Kind: kind, Statement: stmt, Rows: rows, Err: err})
}

// ConnByID finds a connection of the host by its ID, as the app does.
func (h *FakeHost) ConnByID(id string) *connection.Conn {
	for _, cn := range h.Conns {
		if cn.Config.ID == id {
			return cn
		}
	}
	return nil
}

func (h *FakeHost) ProjectConfigs(p *project.Project) []db.Config {
	var out []db.Config
	for _, cn := range h.Conns {
		if cn.Project == p {
			out = append(out, cn.Config)
		}
	}
	return out
}
