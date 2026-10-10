package app

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// txHolder is a tab whose session may hold an open transaction.
type txHolder interface {
	widgets.Tab
	OpenTx() bool
	// Busy reports work on the session, which keeps the transaction in use.
	Busy() bool
	Times() *connection.TxTimes
	// FinishTx commits or rolls back, then calls then on the main thread.
	FinishTx(commit bool, then func(error))
	// RefreshTx reads the transaction again on a connection every session
	// shares, where another tab's statements change it, unless the tab is
	// busy, and reports whether it changed.
	RefreshTx() bool
}

// insideTxTab is a tab whose statements may run inside a transaction
// another session began, on a connection every session shares (DuckDB, an
// in-memory SQLite): only that session's tab holds it (txHolder).
type insideTxTab interface {
	widgets.Tab
	// TxOwner reports whether the tab runs inside another session's
	// transaction, and the name SetTxOwner last gave its owner.
	TxOwner() (name string, inside bool)
	// SetTxOwner names the tab that holds the transaction, "" for none.
	SetTxOwner(name string)
}

// The tabs that hold transactions: a tab of another package that missed
// a method would drop out of the close, quit and idle prompts silently.
var (
	_ txHolder    = (*query.Tab)(nil)
	_ txHolder    = (*dataview.TableTab)(nil)
	_ insideTxTab = (*query.Tab)(nil)
	_ insideTxTab = (*dataview.TableTab)(nil)
)

// openTxTabs lists the tabs with an open transaction, of the connections
// given, or of all.
func (a *App) openTxTabs(conns ...*connection.Conn) []txHolder {
	var out []txHolder
	for _, t := range a.everyTab() {
		h, ok := t.(txHolder)
		if !ok || !h.OpenTx() {
			continue
		}
		if len(conns) == 0 {
			out = append(out, h)
			continue
		}
		for _, cn := range conns {
			if t.Connection() == cn {
				out = append(out, h)
			}
		}
	}
	return out
}

// commitThen commits the transactions, then runs then; when one fails,
// then does not run and the failures are shown, the others committed. A
// transaction another tab already ended has nothing left to commit.
func (a *App) commitThen(txs []txHolder, then func()) {
	left := len(txs)
	var errs []string
	for _, h := range txs {
		h.FinishTx(true, func(err error) {
			if err != nil && !errors.Is(err, db.ErrNoTransaction) {
				errs = append(errs, h.Title()+": "+err.Error())
			}
			if left--; left == 0 {
				if len(errs) > 0 {
					a.ShowError("Could not commit", strings.Join(errs, "\n"))
					return
				}
				then()
			}
		})
	}
}

// idleWarning counts down before an idle transaction is rolled back.
type idleWarning struct {
	open     bool
	tab      txHolder
	deadline time.Time
	used     time.Time // the tab's last use when warned: a newer one cancels
}

// idleWarnAhead is how long the warning shows before the rollback.
const idleWarnAhead = 10 * time.Second

// followSharedTx follows, every frame, the transactions of connections
// every session shares, where a tab's statements commit, fail or begin
// the transaction of every tab, which each saw only after its own runs:
// the tabs read their transaction again; those that run inside another
// session's are told which tab of their connection holds it, as a tab
// sees only its own session; and their work counts as that tab's use of
// it: one busy uses it now, and it was last used when the latest of them
// was.
func (a *App) followSharedTx(c *ui.Context) {
	var tabs []widgets.Tab
	for _, t := range a.everyTab() {
		if h, ok := t.(txHolder); ok && h.RefreshTx() {
			c.Invalidate()
		}
		tabs = append(tabs, t)
	}
	// owners holds, by connection, the first tab with a transaction open.
	var owners map[*connection.Conn]txHolder
	for _, t := range tabs {
		in, ok := t.(insideTxTab)
		if !ok {
			continue
		}
		named, inside := in.TxOwner()
		if !inside {
			continue
		}
		if owners == nil {
			owners = map[*connection.Conn]txHolder{}
			for _, o := range tabs {
				if h, ok := o.(txHolder); ok && h.OpenTx() {
					if _, ok := owners[o.Connection()]; !ok {
						owners[o.Connection()] = h
					}
				}
			}
		}
		owner := owners[t.Connection()]
		ownerName := ""
		if owner != nil {
			ownerName = owner.Title()
		}
		if ownerName != named {
			in.SetTxOwner(ownerName)
			c.Invalidate()
		}
		h, ok := t.(txHolder)
		if _, inside := in.TxOwner(); !ok || !inside {
			continue
		}
		switch {
		case owner == nil:
		case h.Busy():
			owner.Times().Used = a.now
		case h.Times().Used.After(owner.Times().Used):
			owner.Times().Used = h.Times().Used
		}
	}
}

// checkIdleTransactions warns about, then rolls back, transactions left
// idle past their connection's limit, and wakes the frame loop when the
// next one is due. On connections every session shares, it first follows
// their transactions (followSharedTx).
func (a *App) checkIdleTransactions(c *ui.Context) {
	a.followSharedTx(c)
	if w := a.idleWarn; w != nil {
		if !w.tab.OpenTx() || !w.tab.Times().Used.Equal(w.used) {
			a.idleWarn = nil // ended, or used since the warning
			return
		}
		if !a.now.Before(w.deadline) {
			a.idleWarn = nil
			h, cfg := w.tab, w.tab.Connection().Config
			if h.Busy() {
				return // it came back to work: no longer idle
			}
			h.FinishTx(false, func(err error) {
				switch {
				case errors.Is(err, db.ErrNoTransaction):
					return // another tab ended it: nothing was rolled back
				case err != nil:
					a.ShowError("Could not roll back the idle transaction of "+h.Title(), err.Error())
					return
				}
				a.Record(&cfg, audit.Event{Kind: audit.KindConfirm, Detail: "rolled back the transaction of " + h.Title() + ", idle past the connection's limit"})
				a.toast = &pendingToast{text: "Rolled back the idle transaction of " + h.Title()}
			})
			return
		}
		c.After(time.Second)
		return
	}
	next := time.Duration(-1)
	for _, h := range a.openTxTabs() {
		limit := connection.IdleTxLimit(&h.Connection().Config)
		if limit <= 0 {
			continue
		}
		if h.Busy() {
			// A statement running in the transaction uses it.
			h.Times().Used = a.now
		}
		due := h.Times().Used.Add(limit - idleWarnAhead)
		if !a.now.Before(due) {
			a.idleWarn = &idleWarning{open: true, tab: h, deadline: a.now.Add(idleWarnAhead), used: h.Times().Used}
			c.After(time.Second)
			return
		}
		if wait := due.Sub(a.now); next < 0 || wait < next {
			next = wait
		}
	}
	if next > 0 {
		c.After(next)
	}
}

func (a *App) idleWarningView(c *ui.Context) {
	w := a.idleWarn
	if !w.open {
		return // answered: the next check acts on it
	}
	left := max(0, int(w.deadline.Sub(a.now).Round(time.Second).Seconds()))
	cfg := w.tab.Connection().Config
	idle := a.now.Sub(w.tab.Times().Used)
	if idle >= time.Minute {
		idle = idle.Round(time.Minute)
	} else {
		idle = idle.Round(time.Second)
	}
	msg := fmt.Sprintf("The transaction of %s on %s has been idle for %s. It holds its locks until it ends, so it will be rolled back in %d seconds.",
		w.tab.Title(), cfg.Name, idle, left)
	choice := -1
	ui.Modal(c, &w.open, func() {
		ui.Column(c).Width(460).Gap(12).Children(func() {
			ui.Text(c, "Roll back the idle transaction?").FontSize(15).Bold()
			ui.Text(c, msg).TextColor(widgets.PaletteOf(c).Muted)
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Keep Open").Clicked() {
					choice = 0
				}
				if ui.Button(c, "Commit").Clicked() {
					choice = 1
				}
				if ui.PrimaryButton(c, "Roll Back").Clicked() {
					choice = 2
				}
			})
		})
	})
	if choice >= 0 {
		w.open = false
	}
	switch {
	case choice == 0 || choice < 0 && !w.open:
		w.tab.Times().Used = a.now // a new idle period starts
	case choice == 1:
		h := w.tab
		h.FinishTx(true, func(err error) {
			if err != nil && !errors.Is(err, db.ErrNoTransaction) {
				a.ShowError("Could not commit", err.Error())
			}
		})
	case choice == 2:
		w.deadline = a.now // the next check rolls it back
	}
}

// txIndicator lists the open transactions in the status bar.
func (a *App) txIndicator(c *ui.Context) {
	txs := a.openTxTabs()
	if len(txs) == 0 {
		return
	}
	label := "1 open transaction"
	if len(txs) > 1 {
		label = fmt.Sprintf("%d open transactions", len(txs))
	}
	b := ui.ButtonBase(c).Padding(1, 8).Radius(4).Background(c.Theme().Warning.Alpha(0.2)).Label(label)
	b.Children(func() { ui.Text(c, label).FontSize(12).Bold() })
	b.Menu(func(m *ui.Menu) {
		for _, h := range txs {
			age := a.now.Sub(h.Times().Opened).Round(time.Second)
			if m.Item(fmt.Sprintf("%s — %s — open %s", h.Title(), h.Connection().Config.Name, age)).Chosen() {
				a.ActivateTab(func(t widgets.Tab) bool { return t == h })
			}
		}
	})
	c.After(time.Second) // the ages move on
}
