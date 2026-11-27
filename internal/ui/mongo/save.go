package mongo

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/redact"
	"dgopher/internal/safety"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"
)

// saveTimeout bounds saving the changes.
var saveTimeout = 2 * time.Minute

// The confirmation's note of how the changes apply.
const (
	atomicNote   = "They apply in one transaction: if one fails, none stay."
	oneByOneNote = "This server has no transactions: changes apply one by one, and those before a failure stay."
)

// Save reviews the pending changes, each statement through the safety
// policy, asks once for all of them, then saves them: in one transaction
// where the server has them, else one by one.
func (t *CollectionTab) Save() {
	if t.saving || t.edits.count() == 0 {
		return
	}
	if why := t.readOnly(); why != "" && !t.conn.Config.ReadOnly {
		t.a.ShowError("Not saved", why)
		return
	}
	d := t.conn.Doc()
	if d == nil {
		t.a.ShowError("Not saved", t.conn.Config.Name+" is not connected.")
		return
	}
	plan, err := t.edits.plan(t.read, t.Collection)
	if err != nil {
		t.a.ShowError("Not saved", err.Error())
		return
	}
	// One verdict for all: refused when any is, asking for every reason,
	// the name typed when any wants it.
	v := safety.Verdict{Confirm: true}
	for _, op := range plan.ops {
		ov := safety.ReviewMongo(&t.conn.Config, op)
		if ov.Blocked != "" {
			t.a.RecordBlocked(t.conn, ov.Blocked, redact.Mongo(op.Text))
			t.a.ShowError("Not allowed", ov.Blocked)
			return
		}
		v.Writes = v.Writes || ov.Writes
		v.TypeName = v.TypeName || ov.TypeName
		for _, r := range ov.Reasons {
			if !slices.Contains(v.Reasons, r) {
				v.Reasons = append(v.Reasons, r)
			}
		}
	}
	texts := make([]string, len(plan.ops))
	for i, op := range plan.ops {
		texts[i] = redact.Mongo(op.Text)
	}
	n := len(plan.ops)
	note := atomicNote
	if !d.Transactions() {
		note = oneByOneNote
	}
	title := fmt.Sprintf("Save %s to %s?", widgets.Count(n, "change"), t.Title())
	req := t.a.AskConfirm(t.conn, v, title, "Save", strings.Join(texts, "\n"), func() { t.apply(plan) })
	if req != nil {
		req.Notes = append(req.Notes, note)
	}
}

// apply saves plan.
func (t *CollectionTab) apply(plan savePlan) {
	d := t.conn.Doc()
	if d == nil {
		t.a.ShowError("Not saved", t.conn.Config.Name+" is not connected.")
		return
	}
	cfg, database, coll := t.conn.Config, t.Database, t.Collection
	stop := &atomic.Bool{}
	t.saving, t.saveStop, t.saveStart = true, stop, time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), saveTimeout)
	title := fmt.Sprintf("Saving %s to %s: stopping rolls back what a transaction holds", widgets.Count(len(plan.edits), "change"), t.Title())
	dataview.RunJob(t.a, t.conn, title, cancel, func() { t.saving = false }, func() func() {
		defer cancel()
		start := time.Now()
		res, err := d.ApplyEdits(ctx, database, coll, plan.edits, stop)
		took := time.Since(start)
		// Each statement saved is logged alone; those that did not stay,
		// together with the error. They are logged as written: the audit
		// log hides their secrets, in the error too.
		texts := make([]string, len(plan.ops))
		for i, op := range plan.ops {
			texts[i] = op.Text
		}
		for _, text := range texts[:res.Applied] {
			t.a.RecordRun(cfg, audit.KindMongo, database, text, 1, took, nil)
		}
		if err != nil {
			t.a.RecordRun(cfg, audit.KindMongo, database, strings.Join(texts[res.Applied:], "\n"), 0, took, err)
		}
		return func() {
			t.saving, t.saveStop = false, nil
			switch {
			case err == nil:
				t.edits.clear()
				t.a.Toast(fmt.Sprintf("Saved %s to %s", widgets.Count(res.Applied, "change"), t.Title()), "", nil)
				t.Find()
			case res.Applied > 0:
				t.read = t.edits.applySaved(t.read, plan, res.Applied)
				t.readVer++
				t.detail = -1
				t.a.ShowReport(widgets.ErrorReportOf(fmt.Sprintf("%d of %d changes saved", res.Applied, len(plan.edits)), err))
			default:
				t.a.ShowReport(widgets.ErrorReportOf("Not saved", err))
			}
		}
	})
}

// StopSave stops saving before the next change.
func (t *CollectionTab) StopSave() {
	if t.saveStop != nil {
		t.saveStop.Store(true)
	}
}
