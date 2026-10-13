package widgets

import (
	"dgopher/internal/connection"

	"github.com/egoist/mygo/ui"
)

// Tab is a page of the workspace.
type Tab interface {
	Title() string
	Connection() *connection.Conn
	View(c *ui.Context)
	// CloseReason says why closing the tab needs a confirmation, as an
	// open transaction or changes not applied; "" when it does not.
	CloseReason() string
	Close()
}

// Iconed is a tab with an icon of its own; the others show IconCode.
type Iconed interface {
	Icon() *ui.SVG
}

// Placed is a tab that says where its work runs; the others run on their
// connection, at its own database, or nowhere without one.
type Placed interface {
	Places() []connection.Place
}

// ConnectionHolder is a tab that says when it holds a connection open,
// which then is not disconnected as unused; the others hold theirs while
// they are open.
type ConnectionHolder interface {
	HoldsConnection(cn *connection.Conn) bool
}

// ShownNotifier is a tab told as it comes into view: brought in front of
// its window, or beside the one in front.
type ShownNotifier interface {
	Shown()
}

// Marked is a tab whose CloseReason may be work passing, as a statement
// running: its tab's dot shows only Mark, what closing loses that stays,
// as the status bar shows work going on.
type Marked interface {
	Mark() string
}

// Worker is a tab that says when it works, as it reads rows on its own:
// the status bar shows work going on. Work through the host's Background
// shows without it.
type Worker interface {
	Busy() bool
}

// Command is something a tab does, which the command palette offers.
type Command struct {
	Title, Detail string
	// Key is the keymap command whose keys the palette shows, "" for none.
	Key  string
	Icon *ui.SVG
	Run  func()
}

// Commander is a tab with commands of its own for the palette.
type Commander interface {
	Commands() []Command
}
