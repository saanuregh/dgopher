package query

import (
	"errors"

	"dgopher/internal/connection"
	"dgopher/internal/db"
)

func (q *Tab) OpenTx() bool { return q.Tx != db.TxNone }

// Busy reports a statement running, or a result reading or applying on
// the session.
func (q *Tab) Busy() bool { return q.Running || q.resultsBusy() }

func (q *Tab) Times() *connection.TxTimes { return &q.txs }

func (q *Tab) FinishTx(commit bool, then func(error)) {
	switch {
	case q.sess == nil || q.Tx == db.TxNone:
		then(nil)
	case q.Busy():
		then(errors.New(q.Name + " is still running a statement"))
	default:
		q.txs.Then = then
		q.endTx(commit)
	}
}
