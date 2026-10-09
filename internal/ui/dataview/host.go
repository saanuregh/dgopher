package dataview

import (
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/safety"
	"dgopher/internal/settings"
	"dgopher/internal/ui/widgets"
)

// Host is what the data view needs of the app.
type Host interface {
	connection.Runner
	Settings() *settings.Settings
	SaveSettings()
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
}
