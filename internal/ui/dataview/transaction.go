package dataview

import (
	"errors"

	"dgopher/internal/connection"
	"dgopher/internal/db"
)

func (t *TableTab) Busy() bool { return t.view.applying || t.view.loading || t.ending }

func (t *TableTab) OpenTx() bool { return t.tx != db.TxNone }

func (t *TableTab) Times() *connection.TxTimes { return &t.txs }

func (t *TableTab) FinishTx(commit bool, then func(error)) {
	switch {
	case t.sess == nil || t.tx == db.TxNone:
		then(nil)
	case t.Busy():
		then(errors.New(t.Title() + " is still busy"))
	default:
		t.txs.Then = then
		t.endTx(commit)
	}
}
