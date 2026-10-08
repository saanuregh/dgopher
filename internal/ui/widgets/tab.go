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
