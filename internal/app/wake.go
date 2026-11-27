package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/redact"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"
)

// wakePingTimeout is how long a server has to answer after the wake; a
// variable so tests can shorten it.
var wakePingTimeout = 5 * time.Second

// wakeRecheckDelay is how long after a failed ping a connection holding
// work is pinged again, before the work is given up; a variable so tests
// can shorten it.
var wakeRecheckDelay = 3 * time.Second

// absence is what the computer was through before its connections are
// checked again, in the words the reports use.
type absence struct {
	while string // "while asleep": what a lost transaction's report says
	after string // "after sleep": what the audit log and notifications say
}

var (
	asleep  = absence{while: "while asleep", after: "after sleep"}
	offline = absence{while: "while offline", after: "after the network came back"}
)

// checkConnectionsAfterWake runs on the main thread when the computer
// wakes: the idle limits start again (restartIdleClocksAfterWake), as
// sleep is not idle time, and the server connections are checked. While
// the system says it is offline, the check waits for the network to come
// back (checkConnectionsBackOnline), as no server could answer.
func (a *App) checkConnectionsAfterWake() {
	a.idleClocksAfterWake = true
	a.invalidate()
	if !a.offline {
		a.checkConnections(asleep)
	}
}

// checkConnectionsBackOnline checks the server connections when the
// network comes back. The idle limits go on: the computer ran meanwhile,
// and an open transaction held its locks.
func (a *App) checkConnectionsBackOnline() { a.checkConnections(offline) }

// restartIdleClocksAfterWake starts again, at the first frame after a
// wake, the idle periods the idle limits count (checkIdleTransactions,
// checkIdleConnections): at that frame's time, as the time read before it
// is the last frame's, from before the sleep.
func (a *App) restartIdleClocksAfterWake() {
	if !a.idleClocksAfterWake {
		return
	}
	a.idleClocksAfterWake = false
	for _, h := range a.openTxTabs() {
		h.Times().Used = a.now
	}
	for _, cn := range a.conns {
		if cn.Status == connection.StatusConnected {
			cn.HeldAt = a.now
		}
	}
}

// checkConnections pings every connected server, local files aside, which
// lose nothing to a sleep (pingAfterWake).
func (a *App) checkConnections(why absence) {
	for _, cn := range a.conns {
		if cn.Status != connection.StatusConnected || cn.Config.Engine.IsFile() {
			continue
		}
		a.pingAfterWake(cn, cn.Generation, why, false)
	}
}

// pingAfterWake pings cn's server and acts on a failure (afterFailedPing)
// unless the connection changed meanwhile, or the failure says nothing of
// the server: a pool whose every connection is taken waits for one, and
// its ping can time out on the wait alone. A connection holding work is
// given up only when a second ping, wakeRecheckDelay later, fails too;
// again is set for that one.
func (a *App) pingAfterWake(cn *connection.Conn, gen int, why absence, again bool) {
	d, kv, doc := cn.DB(), cn.KV(), cn.Doc()
	a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), wakePingTimeout)
		defer cancel()
		var err error
		saturated := false
		switch {
		case d != nil:
			before := d.SQL.Stats()
			err = d.Ping(ctx)
			after := d.SQL.Stats()
			saturated = before.MaxOpenConnections > 0 && before.InUse >= before.MaxOpenConnections || after.WaitCount > before.WaitCount
		case kv != nil:
			err = kv.Client.Do(ctx, kv.Client.B().Ping().Build()).Error()
		case doc != nil:
			err = doc.Client.Ping(ctx, nil)
		}
		return func() {
			if err == nil || saturated || gen != cn.Generation || cn.Status != connection.StatusConnected {
				return
			}
			if !again && a.connectionWork(cn).loses() {
				time.AfterFunc(wakeRecheckDelay, func() {
					a.Post(func() {
						if gen == cn.Generation && cn.Status == connection.StatusConnected {
							a.pingAfterWake(cn, gen, why, true)
						}
					})
				})
				return
			}
			a.afterFailedPing(cn, err, why)
		}
	})
}

// afterFailedPing acts on a server that stopped answering: one whose
// tabs or jobs hold work the server ended with the session is failed and
// the loss reported (lostAfterWake); any other is connected again quietly
// (reconnectAfterWake).
func (a *App) afterFailedPing(cn *connection.Conn, err error, why absence) {
	if w := a.connectionWork(cn); w.loses() {
		a.lostAfterWake(cn, err, why, w)
		return
	}
	a.reconnectAfterWake(cn, why)
}

// lostAfterWake fails a connection whose server stopped answering while
// it held work, as routeLost does after a tunnel ends: the tabs give
// their sessions up, an open transaction's rollback is audited, the jobs
// stop, and one report says what was lost, offering to connect again,
// never connecting on its own.
func (a *App) lostAfterWake(cn *connection.Conn, err error, why absence, w connectionWork) {
	a.releaseTabs(cn)
	a.dropConnection(cn, "the server did not answer "+why.after)
	cn.Fail(fmt.Errorf("the server did not answer %s: %w", why.after, err)) // after the drop, which leaves the connection idle

	r := a.connectionReport(cn, nil)
	switch {
	case len(w.txs) > 0:
		r.Title = "Transaction lost " + why.while
	case len(w.changes) > 0:
		r.Title = "Changes lost " + why.while
	default:
		r.Title = "Work lost " + why.while
	}
	r.Detail = cn.Config.Name + ": " + r.Detail
	if len(w.txs) > 0 {
		r.Detail += "\n\nRolled back the open transaction of " + strings.Join(w.txs, ", ") + "."
	}
	if len(w.changes) > 0 {
		r.Detail += "\n\nDiscarded the changes not applied of " + strings.Join(w.changes, ", ") + "."
	}
	if len(w.running) > 0 {
		r.Detail += "\n\nStopped what was running: " + strings.Join(w.running, "; ")
	}
	retryAsReconnect(&r)
	a.ShowReport(r)
	lost := slices.Concat(w.txs, w.changes, w.running)
	a.Notify(dataview.Notice{AtRisk: true, Title: r.Title, Body: cn.Config.Name + ": " + redact.Secrets(strings.Join(lost, ", "))})
}

// reconnectAfterWake connects again a connection whose server stopped
// answering while nothing of its sessions was at stake. Its editors stay,
// their next statement refused once as their session was reset; its other
// tabs wait for it and open again. The connect checks all a connect
// does, but asks nothing (connectAttempt.quiet): what needs the user
// leaves the connection failed, with a notification (reconnectNeeds).
func (a *App) reconnectAfterWake(cn *connection.Conn, why absence) {
	a.releaseTabsAs(cn, (*query.Tab).ConnectionClosed)
	a.dropConnection(cn, "the server did not answer "+why.after+"; connecting again")
	name := cn.Config.Name
	a.connect(cn, func() { a.note(notification{title: "Reconnected " + name + " " + why.after}) }, false, true)
}

// reconnectNeeds leaves failed a connection a quiet reconnect would have
// asked the user about, saying why in a notification alone, whose
// Reconnect connects as the user's own connect does, asking.
func (a *App) reconnectNeeds(cn *connection.Conn, why string) {
	a.endAttempt(cn, nil)
	cn.Waiters = nil
	cn.Fail(errors.New(why))
	r := a.connectionReport(cn, nil)
	r.Title = "Reconnect " + cn.Config.Name + ": " + why
	r.Detail = "DGopher did not connect it again on its own, as connecting asks you first."
	retryAsReconnect(&r)
	a.noteReport(r)
}

// retryAsReconnect names a connection report's Retry Reconnect, as what
// failed was connected.
func retryAsReconnect(r *widgets.ErrorReport) {
	for i := range r.Actions {
		if r.Actions[i].Label == "Retry" {
			r.Actions[i].Label = "Reconnect"
		}
	}
}
