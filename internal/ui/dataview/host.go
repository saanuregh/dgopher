package dataview

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/redact"
	"dgopher/internal/safety"
	"dgopher/internal/settings"
	"dgopher/internal/ui/widgets"
)

// Host is what the data view needs of the app.
type Host interface {
	connection.Runner
	Settings() *settings.Settings
	SaveSettings()
	// Layout is the windows' layout, which the app keeps as it changes.
	Layout() *widgets.Layout
	ShowError(title, message string)
	// Toast shows text, with a button doing run when action is set.
	Toast(text, action string, run func())
	// Notify tells of the end of work begun at started by a system
	// notification, when it took long and the window is in the
	// background; clicking it brings the window forward and runs show,
	// when set.
	Notify(started time.Time, title, body string, show func())
	WriteClipboard(text string)
	ReadClipboard() string
	// Now is the time the current frame started.
	Now() time.Time
	// FocusWant is where a key asked the keyboard focus to go; the view
	// holding that place takes it and clears it.
	FocusWant() *string
	Connect(cn *connection.Conn, then func())
	AddTab(t widgets.Tab)
	// ReplaceTab puts next in the place of old, which closes.
	ReplaceTab(old, next widgets.Tab)
	ActiveTab() widgets.Tab
	// KeysTo reports whether a tab takes the shortcuts pressed: the tab in
	// front, of the pane with the focus when two tabs show side by side.
	KeysTo(t widgets.Tab) bool
	// ActivateTab brings forward the first tab match accepts, and reports
	// whether there was one.
	ActivateTab(match func(widgets.Tab) bool) bool
	OpenTable(cn *connection.Conn, database string, obj db.Object, page int)
	NewQueryTab(cn *connection.Conn, database, text string)
	AskConfirm(cn *connection.Conn, v safety.Verdict, title, action, preview string, onConfirm func()) *widgets.ConfirmRequest
	// AskPending asks what becomes of n changes not applied yet before
	// the rows are read again: apply them first, discard them, or cancel.
	AskPending(n int, apply, discard, cancel func())
	RecordBlocked(cn *connection.Conn, why, what string)
	Record(cfg *db.Config, e audit.Event)
	RecordRun(cfg db.Config, kind, database, stmt string, rows int64, d time.Duration, err error)
	ProjectConfigs(p *project.Project) []db.Config
	Dialogs() *Dialogs
	// StartJob counts work on a connection as running until finish is
	// called, on any goroutine: quitting, disconnecting and deleting the
	// connection list title, which says what the work does and what
	// stopping it loses, and stop it with cancel.
	StartJob(cn *connection.Conn, title string, cancel context.CancelFunc) (finish func())
}

// JobHost is what RunJob needs of the app.
type JobHost interface {
	connection.Runner
	StartJob(cn *connection.Conn, title string, cancel context.CancelFunc) (finish func())
}

// RunJob runs work as h.Background does, as a job of cn (StartJob) until
// work returns. When work stops on a panic, stopped runs on the UI thread
// in place of what work returns, to end what showed the work running.
func RunJob(h JobHost, cn *connection.Conn, title string, cancel context.CancelFunc, stopped func(), work func() func()) {
	finish := h.StartJob(cn, title, cancel)
	BackgroundResetOnPanic(h, stopped, func() func() {
		defer finish()
		return work()
	})
}

// BackgroundResetOnPanic runs work as h.Background does; when work stops
// on a panic, reset is posted to the UI thread, ahead of the error
// h.Background posts, to end what showed the work running.
func BackgroundResetOnPanic(h connection.Runner, reset func(), work func() func()) {
	h.Background(func() func() {
		returned := false
		defer func() {
			if !returned {
				h.Post(reset)
			}
		}()
		apply := work()
		returned = true
		return apply
	})
}

// RecoverBackground, deferred by the goroutine running background work,
// keeps a panic of the work from ending the app: it logs the panic, its
// secrets hidden, with its stack, and has show tell the user, through
// post; then reset, when set, runs through post, to end what showed the
// work running.
func RecoverBackground(post func(func()), show func(title, message string), reset func()) {
	p := recover()
	if p == nil {
		return
	}
	text := redact.Secrets(fmt.Sprint(p))
	log.Printf("background task: panic: %s\n%s", text, debug.Stack())
	post(func() {
		show("Something went wrong", "A background task stopped on an internal error: "+widgets.OneLine(widgets.FirstLine(text), 300)+
			". The app kept running; the task may be incomplete.")
	})
	if reset != nil {
		post(reset)
	}
}
