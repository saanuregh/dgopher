package app

import (
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/ui/dataview"
)

// applyStatements runs statements the app wrote, as a rename or a grant,
// one after the other, as dataview.ApplyChange runs a change.
func (a *App) applyStatements(cn *connection.Conn, database, title string, stmts []string, always string, done func(error)) {
	dataview.ApplyChange(a, cn, database, title, db.StatementsChange(stmts), always, nil, done)
}
