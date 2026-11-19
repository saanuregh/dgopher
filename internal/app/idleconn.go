package app

import (
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// idleConnectionLimit is how long a connection may go unused before it is
// disconnected; tests replace it, as SQLite, which they use, never is.
var idleConnectionLimit = connection.IdleConnectionLimit

// checkIdleConnections disconnects the connections unused past their
// limit, keeping their editors, and wakes the frame loop when the next
// one is due. Unused counts from the last time the pool sent something
// (db.DB.LastUsed), or something held the connection (connectionHeld).
func (a *App) checkIdleConnections(c *ui.Context) {
	next := time.Duration(-1)
	for _, cn := range a.conns {
		limit := idleConnectionLimit(&cn.Config)
		if cn.Status != connection.StatusConnected || limit <= 0 {
			continue
		}
		held := a.connectionHeld(cn)
		if held {
			cn.HeldAt = a.now
		}
		last := cn.HeldAt
		if d := cn.DB(); d != nil && d.LastUsed().After(last) {
			last = d.LastUsed()
		}
		idle := a.now.Sub(last)
		if idle >= limit {
			a.disconnectIdle(cn, idle)
			continue
		}
		wait := limit - idle
		if held {
			// Looked at again within a minute, the hold's end counts.
			wait = min(wait, time.Minute)
		}
		if next < 0 || wait < next {
			next = wait
		}
	}
	if next > 0 {
		c.After(next)
	}
}

// connectionHeld reports whether something holds a connection now
// (connectionWork.held).
func (a *App) connectionHeld(cn *connection.Conn) bool { return a.connectionWork(cn).held }

// connectionWork is what holds a connection now, and of it what the end
// of its sessions loses.
type connectionWork struct {
	// held is whether anything holds it: a tab other than an editor, or a
	// copy, row comparison or import dialog, which cannot do without it;
	// an editor's work, transaction or changes not applied; a dashboard's
	// panel running; a job; or a catalog read.
	held bool
	// txs are the tabs with a transaction open, or rows waiting to be
	// committed; changes the tabs other than editors, which give their
	// session up by closing, with changes not applied; running the
	// editors running a statement, and the jobs. A connection every
	// session shares (db.DB.Single) lists the tab owning its transaction
	// anyway.
	txs, changes, running []string
}

// loses reports whether the end of the connection's sessions loses work.
func (w connectionWork) loses() bool { return len(w.txs)+len(w.changes)+len(w.running) > 0 }

// connectionWork reports what holds cn now. The dialogs' work that
// writes runs as jobs.
func (a *App) connectionWork(cn *connection.Conn) connectionWork {
	var w connectionWork
	if d := a.copying; d != nil && d.open && (d.from == cn || d.target.conn(a) == cn) {
		w.held = true
	}
	if d := a.rowCompare; d != nil && d.open && (d.from == cn || d.target.conn(a) == cn) {
		w.held = true
	}
	if d := a.importing; d != nil && (d.open || d.running) && d.conn == cn {
		w.held = true
	}
	for _, t := range a.everyTab() {
		if h, ok := t.(widgets.ConnectionHolder); ok {
			w.held = w.held || h.HoldsConnection(cn)
		} else if t.Connection() == cn {
			w.held = true
		}
		if t.Connection() != cn {
			continue
		}
		q, editor := t.(*query.Tab)
		h, holder := t.(txHolder)
		switch {
		case holder && h.OpenTx(), editor && q.Uncommitted() > 0:
			w.txs = append(w.txs, t.Title())
		case !editor && t.CloseReason() != "":
			w.changes = append(w.changes, t.Title())
		case editor && q.Busy():
			w.running = append(w.running, t.Title())
		}
	}
	jobs := a.jobsOf(cn)
	for _, j := range jobs {
		w.running = append(w.running, j.title)
	}
	w.held = w.held || len(jobs) > 0 || len(cn.Loading) > 0
	return w
}

// disconnectIdle disconnects a connection unused for idle. Its editors
// stay, and connect it again at their next run, or as one comes into
// view with auto-connect.
func (a *App) disconnectIdle(cn *connection.Conn, idle time.Duration) {
	for _, t := range a.everyTab() {
		if q, ok := t.(*query.Tab); ok && q.Conn == cn {
			q.ConnectionClosed()
		}
	}
	span := idleSpan(idle)
	a.dropConnection(cn, "idle for "+span)
	a.Toast("Disconnected "+cn.Config.Name+" after "+span+" idle", "", nil)
}

// idleSpan says how long a connection went unused, in whole hours when
// it is, else in minutes, or seconds under one.
func idleSpan(d time.Duration) string {
	minutes := int(d.Round(time.Minute) / time.Minute)
	switch {
	case minutes >= 60 && minutes%60 == 0:
		return widgets.Count(minutes/60, "hour")
	case minutes > 0:
		return widgets.Count(minutes, "minute")
	}
	return widgets.Count(int(d.Round(time.Second)/time.Second), "second")
}
