package app

import (
	"errors"
	"strings"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/ui/dataview"
)

// watchRoute waits for the SSH tunnel of a connection's pool, opened in
// generation gen, to end: done closes then, and why says why. A nil done
// has no tunnel to watch. Closing the pool closes its tunnel, and so done:
// the goroutine always ends.
func (a *App) watchRoute(cn *connection.Conn, gen int, done <-chan struct{}, why func() error) {
	if done == nil {
		return
	}
	go func() {
		defer dataview.RecoverBackground(a.Post, a.ShowReport, nil)
		<-done
		err := why()
		a.Post(func() { a.routeLost(cn, gen, err) })
	}()
}

// routeLost fails a connection whose SSH tunnel ended under it, unless it
// was disconnected, edited or connected again since gen. The server ended
// the sessions with the tunnel: the tabs give theirs up as at a
// disconnect, an open transaction's rollback is audited and named in the
// notification, and the user is offered to connect again, never connected
// on its own.
func (a *App) routeLost(cn *connection.Conn, gen int, err error) {
	if gen != cn.Generation || cn.Status != connection.StatusConnected {
		return
	}
	var closed *db.TunnelClosedError
	if !errors.As(err, &closed) {
		err = &db.TunnelClosedError{Err: err}
	}
	var rolledBack []string
	for _, h := range a.openTxTabs(cn) {
		rolledBack = append(rolledBack, h.Title())
	}
	a.releaseTabs(cn)
	a.dropConnection(cn, "SSH tunnel closed")
	cn.Fail(err) // after the drop, which leaves the connection idle

	r := a.connectionReport(cn, nil)
	r.Title = "Lost " + cn.Config.Name
	reconnect := &r.Actions[1] // after Edit Connection
	reconnect.Label = "Reconnect"
	if len(rolledBack) > 0 {
		r.Detail += "\n\nRolled back the open transaction of " + strings.Join(rolledBack, ", ") + "."
	}
	a.ShowReport(r)
}
