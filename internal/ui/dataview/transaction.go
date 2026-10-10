package dataview

import (
	"errors"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
)

func (t *TableTab) Busy() bool { return t.view.applying || t.view.loading || t.ending }

func (t *TableTab) FinishTx(commit bool, then func(error)) {
	switch {
	case t.sess == nil || t.Tx == db.TxNone:
		then(nil)
	case t.Busy():
		then(errors.New(t.Title() + " is still busy"))
	default:
		t.Times().Then = then
		t.endTx(commit)
	}
}

// setTx keeps the session's transaction as last read (SessionTx).
func (t *TableTab) setTx(own, inside db.TxState) { t.KeepTx(own, inside, t.a.Now()) }

// endTxStopped ends a Commit or Roll Back that stopped on an internal
// error: what waited for it (FinishTx) is told it failed, and the
// transaction, which may still be open, is read again.
func (t *TableTab) endTxStopped() {
	t.ending = false
	t.TellFinishTx(ErrTxEndStopped)
	if sess := t.sess; sess != nil {
		t.ReadTxAgain(t.a, sess, func() bool { return t.sess == sess && !t.Busy() }, nil)
	}
}

// RefreshTx reads the session's transaction again on a connection every
// session shares (SessionTxState.RefreshShared).
func (t *TableTab) RefreshTx() bool { return t.RefreshShared(t.a, t.sess, t.Busy) }

// SessionTxState is what a tab keeps of its session's transaction, as
// last read (SessionTx), with what it does with it alike for the editor
// and the table tabs.
type SessionTxState struct {
	// Tx is the transaction the session began; InsideTx, one another
	// session began on a connection every session shares, which the
	// session's statements run in (SessionTx), and owner names the tab
	// that began it, "" when unknown.
	Tx       db.TxState
	InsideTx db.TxState
	owner    string
	// times is when the session's transaction opened and was last used.
	times connection.TxTimes
}

func (s *SessionTxState) OpenTx() bool { return s.Tx != db.TxNone }

func (s *SessionTxState) Times() *connection.TxTimes { return &s.times }

// InTx reports a transaction open on the session, its own or one it runs
// inside.
func (s *SessionTxState) InTx() bool { return s.Tx != db.TxNone || s.InsideTx != db.TxNone }

// KeepTx keeps the session's transaction as last read (SessionTx), now.
func (s *SessionTxState) KeepTx(own, inside db.TxState, now time.Time) {
	s.Tx, s.InsideTx = own, inside
	s.times.Set(own, now)
}

// TellFinishTx tells what waited for the transaction's end (FinishTx)
// how it ended, and reports whether anything waited.
func (s *SessionTxState) TellFinishTx(err error) bool {
	f := s.times.Then
	if f == nil {
		return false
	}
	s.times.Then = nil
	f(err)
	return true
}

// ReadTxAgain reads sess's transaction off the main thread (SessionTx),
// then keeps it, as a work's end does, when keep, asked on the main
// thread, agrees: a tab on another session or busy by then is left
// alone, as RefreshShared leaves it, since its work reads it as it ends.
// also, when set, reads more of sess off the main thread, and returns
// what keeps it once the transaction is kept.
func (s *SessionTxState) ReadTxAgain(a Host, sess *db.Session, keep func() bool, also func(*db.Session) func()) {
	a.Background(func() func() {
		own, inside := SessionTx(sess)
		var kept func()
		if also != nil {
			kept = also(sess)
		}
		return func() {
			if !keep() {
				return
			}
			s.KeepTx(own, inside, a.Now())
			if kept != nil {
				kept()
			}
		}
	})
}

// RefreshShared reads sess's transaction again on a connection every
// session shares, where another tab's statement may have ended, failed or
// begun it, and reports whether it changed. A busy tab is left alone: it
// reads it as its work ends.
func (s *SessionTxState) RefreshShared(a Host, sess *db.Session, busy func() bool) bool {
	if sess == nil || !sess.DB().Single() || busy() {
		return false
	}
	own, inside, _ := SharedSessionTx(sess)
	if own == s.Tx && inside == s.InsideTx {
		return false
	}
	s.KeepTx(own, inside, a.Now())
	return true
}

// TxOwner reports whether the tab's statements run inside a transaction
// another session began, and names the tab that began it, "" when unknown.
func (s *SessionTxState) TxOwner() (name string, inside bool) {
	return s.owner, s.InsideTx != db.TxNone
}

// SetTxOwner names the tab whose transaction the tab's statements run
// inside; "" once no tab holds it, as when the one named ended it or
// closed, which takes the transaction with it.
func (s *SessionTxState) SetTxOwner(name string) {
	if name == "" && s.owner != "" {
		s.InsideTx = db.TxNone
	}
	s.owner = name
}

// RefuseEndInside says why a tab does not commit or roll back the
// transaction its statements run inside, when another session began it,
// and reports whether it refused.
func (s *SessionTxState) RefuseEndInside(a Host, commit bool) bool {
	if s.InsideTx == db.TxNone {
		return false
	}
	a.ShowError(endTitle(commit), insideTxText(s.owner, "commit or roll it back there"))
	return true
}

// ErrTxEndStopped is what FinishTx's then is told when the commit or
// rollback stopped on an internal error.
var ErrTxEndStopped = errors.New("ending the transaction stopped on an internal error: it may still be open")

// SharedSessionTx is SessionTx on a pool of one connection, read from
// what the connection's last statement left (db.DB.SharedTx), so it never
// waits for a statement and may run on the main thread. ok is false on
// other pools, where no other session's statement changes the session's
// transaction.
func SharedSessionTx(sess *db.Session) (own, inside db.TxState, ok bool) {
	if !sess.DB().Single() {
		return db.TxNone, db.TxNone, false
	}
	state, owner := sess.DB().SharedTx()
	switch {
	case state == db.TxNone:
		return db.TxNone, db.TxNone, true
	case owner == sess:
		return state, db.TxNone, true
	}
	return db.TxNone, state, true
}

// NotEnded says why a Commit or Roll Back sent nothing on a pool of one
// connection, whose sessions share its transaction: another session
// already ended it (db.ErrNoTransaction), or began the one open now
// (db.ErrNotOwner). ok is false for any other error.
func NotEnded(err error, commit bool) (title, text string, ok bool) {
	title = endTitle(commit)
	switch {
	case errors.Is(err, db.ErrNoTransaction):
		return title, "The transaction was already committed or rolled back elsewhere. Nothing was sent.", true
	case errors.Is(err, db.ErrNotOwner):
		return title, "The transaction open now is another tab's, which began it: commit or roll it back there. Nothing was sent.", true
	}
	return "", "", false
}

// endTitle titles why a Commit, or a Roll Back, did not end the
// transaction.
func endTitle(commit bool) string {
	if commit {
		return "Not committed"
	}
	return "Not rolled back"
}
