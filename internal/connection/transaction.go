package connection

import (
	"time"

	"dgopher/internal/db"
)

// TxTimes is when a tab's transaction opened and was last used, and what
// waits for it to end.
type TxTimes struct {
	Opened, Used time.Time
	Then         func(error)
}

// Set follows the transaction's state after a statement.
func (t *TxTimes) Set(tx db.TxState, now time.Time) {
	switch {
	case tx == db.TxNone:
		t.Opened = time.Time{}
	case t.Opened.IsZero():
		t.Opened = now
	}
	t.Used = now
}
