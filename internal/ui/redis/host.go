package redis

import (
	"context"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/safety"
	"dgopher/internal/ui/widgets"
)

// Host is what the Redis view needs of the app.
type Host interface {
	connection.Runner
	ShowError(title, message string)
	WriteClipboard(text string)
	// Layout is the windows' layout, which the app keeps as it changes.
	Layout() *widgets.Layout
	AskConfirm(cn *connection.Conn, v safety.Verdict, title, action, preview string, onConfirm func()) *widgets.ConfirmRequest
	// AskDiscard asks whether to drop what reason says, then calls
	// onDiscard.
	AskDiscard(title, reason string, onDiscard func())
	RecordBlocked(cn *connection.Conn, why, what string)
	RecordRun(cfg db.Config, kind, database, stmt string, rows int64, d time.Duration, err error)
	// KeysTo reports whether a tab takes the shortcuts pressed.
	KeysTo(t widgets.Tab) bool
	// FocusWant is where the keyboard focus is wanted: "editor" for the
	// tab's main view, "filter" for its filter; whoever takes it clears it.
	FocusWant() *string
	// StartJob counts work on a connection as running until finish is
	// called, on any goroutine: quitting, disconnecting and deleting the
	// connection list title, which says what the work does and what
	// stopping it loses, and stop it with cancel.
	StartJob(cn *connection.Conn, title string, cancel context.CancelFunc) (finish func())
}
