// Package mongo is the MongoDB tabs: a collection's documents, browsed
// and edited, and a console of shell statements, all through the safety
// policy.
package mongo

import (
	"dgopher/internal/connection"
	"dgopher/internal/ui/dataview"
)

// Host is what the MongoDB tabs need of the app: what the Redis tab needs
// (ui/redis Host), the data view's, whose grid shows the documents, and
// the SQL editor's connection status, which the console's bar shows.
type Host interface {
	dataview.Host
	// Online reports whether the system says the computer is online.
	Online() bool
	// Disconnect disconnects cn once the user agrees to what it loses.
	Disconnect(cn *connection.Conn)
	// AskDiscard asks whether to drop what reason says, on a button named
	// action, then calls onDiscard.
	AskDiscard(title, reason, action string, onDiscard func())
}
