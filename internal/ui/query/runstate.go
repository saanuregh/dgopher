package query

import (
	"slices"

	"dgopher/internal/keymap"
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
		widgets.LeadToolButton(c.Key("run"), widgets.IconStop, "Stopping…", "Asked to stop: the run ends once the server stops it", danger).Disabled(true)
		return
	}
	if widgets.LeadToolButton(c.Key("run"), widgets.IconStop, "Stop", keymap.Hint(tip, keymap.CancelQuery), danger).Clicked() {
		q.stop()
	}
}
