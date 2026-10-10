package query

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/safety"
	"dgopher/internal/ui/dataview"
)

// Busy reports a statement running, or a result or an export reading or
// applying on the session.
func (q *Tab) Busy() bool { return q.Running || q.exports > 0 || q.resultsBusy() }

func (q *Tab) FinishTx(commit bool, then func(error)) {
	switch {
	case q.sess == nil || q.Tx == db.TxNone:
		then(nil)
	case q.Busy():
		then(errors.New(q.Name + " is still running a statement"))
	default:
		q.Times().Then = then
		q.endTx(commit)
	}
}

// endOpenTx commits or rolls back the open transaction, when one is open
// that can: a failed one only rolls back, none ends while busy, and
// another session's is refused.
func (q *Tab) endOpenTx(commit bool) {
	if q.RefuseEndInside(q.a, commit) {
		return
	}
	if q.Tx == db.TxNone || q.Busy() || commit && q.Tx != db.TxOpen {
		return
	}
	q.endTx(commit)
}

// setTx keeps the session's transaction as last read (dataview.SessionTx).
func (q *Tab) setTx(own, inside db.TxState) { q.KeepTx(own, inside, q.a.Now()) }

// runStopped ends a run that stopped on an internal error before its end
// read what the run left: the transaction is read again, so that one the
// run opened still shows, and closing and quitting still ask.
func (q *Tab) runStopped() {
	q.Running = false
	q.readTxAgain()
}

// endTxStopped ends a Commit or Roll Back that stopped on an internal
// error: what waited for it (FinishTx) is told it failed, and the
// transaction, which may still be open, is read again.
func (q *Tab) endTxStopped() {
	q.Running = false
	q.TellFinishTx(dataview.ErrTxEndStopped)
	q.readTxAgain()
}

// readTxAgain reads the session's transaction off the main thread, then
// keeps it, as a run's end does. A tab busy by then is left alone, as
// RefreshTx leaves it: its work reads it as it ends.
func (q *Tab) readTxAgain() {
	sess := q.sess
	if sess == nil {
		return
	}
	q.ReadTxAgain(q.a, sess, func() bool { return q.sess == sess && !q.Busy() }, func(sess *db.Session) func() {
		autocommitOff := sess.AutocommitOff()
		return func() { q.autocommitOff = autocommitOff }
	})
}

// RefreshTx reads the session's transaction again on a connection every
// session shares (dataview.SessionTxState.RefreshShared).
func (q *Tab) RefreshTx() bool { return q.RefreshShared(q.a, q.sess, q.Busy) }

// insideOthersTx reports whether the tab's statements run inside a
// transaction another session began on a connection every session shares,
// as the connection's last statement left it: a tab that has run nothing
// since, or has no session yet, runs inside it too.
func (q *Tab) insideOthersTx() bool {
	if q.sess != nil {
		_, inside, ok := dataview.SharedSessionTx(q.sess)
		return ok && inside != db.TxNone
	}
	if q.Conn.DB == nil {
		return false
	}
	state, _ := q.Conn.DB.SharedTx()
	return state != db.TxNone
}

// refuseEndingOthersTx refuses a run, saying so, when one of its
// statements would end a transaction another session began on a
// connection every session shares: only the tab that began it ends it, as
// this tab's Commit and Roll Back say. A BEGIN is refused too: it cannot
// begin a transaction inside that one, and its failure would abort it on
// DuckDB. It reports whether it refused.
func (q *Tab) refuseEndingOthersTx(stmts []safety.Statement) bool {
	i := slices.IndexFunc(stmts, func(s safety.Statement) bool {
		return safety.ControlsTransaction(q.Conn.Config.Engine, s)
	})
	if i < 0 || !q.insideOthersTx() {
		return false
	}
	msg := q.insideTxText(stmts[i])
	sqls := make([]string, len(stmts))
	for j, s := range stmts {
		sqls[j] = s.SQL
	}
	q.a.RecordBlocked(q.Conn, msg, strings.Join(sqls, ";\n"))
	q.note(msg, stmts[i].SQL, true)
	q.a.ShowError("Not run", msg)
	return true
}

// refuseInsideRun stops a run before s, its statement i of n, which would
// end or begin a transaction that another session began meanwhile on a
// connection every session shares, as refuseEndingOthersTx refuses it
// before a run; notRun is the text of the statements left.
func (q *Tab) refuseInsideRun(s safety.Statement, i, n int, notRun string) {
	msg := q.insideTxText(s)
	q.a.RecordBlocked(q.Conn, msg, notRun)
	q.note(fmt.Sprintf("Stopped before statement %d of %d: %s", i+1, n, msg), s.SQL, true)
	q.a.ShowError("Not run", msg)
}

// insideTxText says why s, which ends or begins a transaction, does not
// run inside the one another session began.
func (q *Tab) insideTxText(s safety.Statement) string {
	owner, named := q.TxOwner()
	if !named {
		owner = "" // a name kept from an earlier transaction
	}
	if safety.BeginsTransaction(s) {
		return dataview.InsideTxBeginText(owner, s.Analysis.Verb)
	}
	return dataview.InsideTxEndText(owner, s.Analysis.Verb)
}

// warnImplicitCommits makes statements that commit a transaction without
// being asked, as MySQL's DDL, ask first: the transaction open, or, with
// none open, the one whose BEGIN the run sends before its writes under
// manual commit, or one a typed BEGIN or START TRANSACTION of the run
// begins under auto-commit.
func (q *Tab) warnImplicitCommits(v *safety.Verdict, stmts []safety.Statement) {
	cfg := &q.Conn.Config
	switch {
	case q.InTx():
		v.EndsTransaction(cfg, stmts, true)
	default:
		v.EndsRunTransaction(cfg, stmts, cfg.ManualCommit())
	}
}
