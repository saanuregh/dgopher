package query

import (
	"slices"

	"dgopher/internal/keymap"
	"dgopher/internal/safety"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// runState is one run of statements: the results it brought, which its
// end looks at alone, whatever else the tab's results hold, as those
// pinned from earlier runs; and whether they were let go meanwhile, as
// when the notebook cell they show under was dropped.
type runState struct {
	results []*result
	dropped bool
}

// addRunResults gives the tab a run's results as they come; a run let go
// closes them instead.
func (q *Tab) addRunResults(run *runState, results ...*result) {
	if run.dropped {
		q.closeLater(releaseResults(results))
		return
	}
	q.results = append(q.results, results...)
	run.results = append(run.results, results...)
}

// replaceResults makes way for a run's results, on the main thread, and
// returns what closes the cursors of those it drops, to call off it. A
// run replaces the results, but those pinned and, run into a new tab, all
// of them; the cursors of those it drops close, and a kept one that was
// still reading ends where it is.
func (q *Tab) replaceResults(stmts []safety.Statement, newTab bool) []func() {
	q.lastRunFailed = false
	q.runReads = slices.ContainsFunc(stmts, func(s safety.Statement) bool { return s.Analysis.Class == sqltext.Read })
	q.showResults() // a run shows what it returns
	var kept []*result
	var closers []func()
	for _, r := range q.results {
		switch {
		case r.view == nil:
		case !newTab && !r.pinned:
			closers = append(closers, r.view.Release())
		case !r.view.Done():
			closers = append(closers, r.view.CutShort())
		}
		if newTab || r.pinned {
			kept = append(kept, r)
		}
	}
	// The cursors other cells still read close too: the session runs one
	// statement at a time.
	for _, r := range q.otherResults() {
		if r.view != nil && !r.view.Done() {
			closers = append(closers, r.view.CutShort())
		}
	}
	// The results it drops stay in view, dimmed and inert, until a new one
	// comes or the run ends: a quick run then replaces them in one frame.
	if len(kept) < len(q.results) {
		q.stale, q.staleIdx, q.staleFrom = q.results, q.resultIdx, len(kept)
	}
	q.results = kept
	q.resultIdx = len(kept)
	return closers
}

// cutHeldCursors ends where they are the results whose rows still to read
// hold the session, which a statement sent on it would end, and returns
// what closes their cursors, to call off the main thread.
func (q *Tab) cutHeldCursors() []func() {
	var closers []func()
	for _, r := range append(slices.Clip(q.results), q.otherResults()...) {
		if r.view != nil && !r.view.Done() && r.view.HoldsCursor() {
			closers = append(closers, r.view.CutShort())
		}
	}
	return closers
}

// dropRun lets the results of the run going on go, theirs to show
// having gone: those to come close as they come.
func (q *Tab) dropRun() {
	if q.ongoing != nil {
		q.ongoing.dropped = true
	}
}

// failed reports whether a statement of the run failed.
func (run *runState) failed() bool {
	return slices.ContainsFunc(run.results, func(r *result) bool { return r.err != "" })
}

// showRunResults ends the run's results as its end shows them: a
// statement's cursor closes as the next one runs, so those before the
// last end where they were read; then the error shows, else Explain's
// plan, else the last result with rows, else the messages.
func (q *Tab) showRunResults(run *runState, explain bool) {
	if run.dropped {
		return
	}
	for i, r := range run.results {
		if i < len(run.results)-1 && r.view != nil {
			r.view.CutShort()
		}
	}
	show := -1
	for i := len(run.results) - 1; i >= 0; i-- {
		if r := run.results[i]; r.rowsMode && r.err == "" {
			show = i
			break
		}
	}
	if explain {
		if i := slices.IndexFunc(run.results, func(r *result) bool { return r.plan != nil }); i >= 0 {
			show = i
		}
	}
	if i := slices.IndexFunc(run.results, func(r *result) bool { return r.err != "" }); i >= 0 {
		show = i
	}
	q.resultIdx = len(q.results)
	if show >= 0 {
		if i := slices.Index(q.results, run.results[show]); i >= 0 {
			q.resultIdx = i
		}
	}
}

// cancelQueryKey stops the run at Cancel Query's key; its Esc goes to the
// editor first, which may have a use for it, as Vim's insert mode does.
func (q *Tab) cancelQueryKey(c *ui.Context) {
	if !q.a.KeysTo(q) || q.chat.focused {
		return
	}
	b, ok := keymap.PressedWith(c, keymap.CancelQuery)
	if !ok {
		return
	}
	if b.Keys == keymap.Of(0, ui.KeyEscape) && q.Editor.HasFocus() && q.Editor.Escape() {
		return
	}
	q.stop()
}

// stop asks the run to stop; in a notebook, the cells after it do not
// run.
func (q *Tab) stop() {
	if !q.Running || q.cancel == nil {
		return
	}
	if q.nb != nil {
		q.nb.queue = nil
	}
	q.cancel()
	q.stopping = true
}

// stopButton is Run turned to Stop, once a run lasts; asked, it says it
// stops until the run ends.
func (q *Tab) stopButton(c *ui.Context, tip string) {
	danger := c.Theme().Danger
	if q.stopping {
		widgets.Unavailable(widgets.LeadToolButton(c.Key("run"), widgets.IconStop, "Stopping…", "", danger), "Asked to stop: the run ends once the server stops it")
		return
	}
	if q.readingPlan {
		tip = "Reading the plan…"
	}
	if widgets.TimedToolButton(c.Key("run"), widgets.IconStop, "Stop", keymap.Hint(tip, keymap.CancelQuery), danger, q.StartedAt).Clicked() {
		q.stop()
	}
}
