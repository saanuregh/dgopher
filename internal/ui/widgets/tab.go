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
