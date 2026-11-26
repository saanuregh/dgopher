package query

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/params"
	"dgopher/internal/safety"
	"dgopher/internal/settings"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/widgets"
)

// purposeScanCheck and purposeCount are why the app sends a read of its
// own before a production statement asks.
const (
	purposeScanCheck = "to check the plan's scans before it runs"
	purposeCount     = "to count the rows before it asks"
)

// countWait bounds the count of the rows a production change makes.
const countWait = 5 * time.Second

// largeScans is db.LargeScans, which a test makes fail.
var largeScans = db.LargeScans

// inRunAsk is a production run that asks inside itself, before each
// statement that needs it (askStep), so that the policy's reasons, the
// plan's scans and the rows counted share one question.
type inRunAsk struct {
	// verdict is the policy's on the whole run: a single statement asks
	// with it, as it asked before the run; a script's statements are
	// reviewed one by one only when it asks.
	verdict safety.Verdict
	// starts are where the run's statements start in the editor, which
	// Show Plan selects.
	starts []int
	// Filled in as the run starts.
	sess      *db.Session
	database  string
	schema    string // where the session finds names, as last read
	dialect   sqltext.Dialect
	scanLimit int
	// runAll is set once Run All answered for the plain writes left.
	runAll bool
}

// askStep asks, on the main thread, whether to run a statement of a
// production run, and waits for the answer: "run", "skip" or "cancel";
// after, when set, is what to do on the main thread once the run ends,
// as Show Plan's explain. Run All answers for the plain writes and large
// scans left; a destructive statement asks still, as one that commits
// the open transaction, or takes effect at once inside it, does.
// open is whether a transaction is open on the session that holds what
// the statement may commit.
func (q *Tab) askStep(ctx context.Context, ask *inRunAsk, s safety.Statement, i, n int, open bool) (answer string, after func()) {
	cfg := q.Conn.Config
	var v safety.Verdict
	commits := false
	switch {
	case n == 1:
		v = ask.verdict
		v.Reasons = slices.Clone(v.Reasons)
	case ask.verdict.Confirm:
		v = safety.ReviewSQL(&cfg, []safety.Statement{s})
		commits = open && (safety.CommitsImplicitly(cfg.Engine, s) || safety.TakesEffectAtOnce(cfg.Engine, s))
		v.EndsTransaction(&cfg, []safety.Statement{s}, commits)
	}
	asks := v.Confirm && (!ask.runAll || v.TypeName || commits)
	scanned := false
	if asks || !ask.runAll {
		if reasons := q.scanReasons(ctx, ask, s); len(reasons) > 0 {
			v.Confirm, scanned, asks = true, true, true
			v.Reasons = append(v.Reasons, reasons...)
		}
	}
	if !asks {
		return "run", nil
	}
	preview, previewable := sqltext.WritePreview{}, false
	if r, ok := db.SQL(cfg.Engine); ok {
		var why string
		preview, why = sqltext.PreviewWrite(s.SQL, ask.dialect, r.PreviewNewName)
		previewable = why == ""
	}
	var notes []string
	if previewable {
		if note := q.countNote(ctx, ask, s, preview); note != "" {
			notes = append(notes, note)
		}
	}
	if ctx.Err() != nil {
		return "cancel", nil
	}
	answers := make(chan string, 1)
	q.a.Post(func() {
		title := fmt.Sprintf("Run statement %d of %d on %s?", i+1, n, cfg.Name)
		shown := s.SQL
		if n == 1 {
			title = fmt.Sprintf("Run 1 statement on %s?", cfg.Name)
			shown += ";"
			if s.Shown != "" {
				shown += "\n-- " + s.Shown
			}
		}
		r := q.a.AskConfirm(q.Conn, v, title, "Run", shown, func() { answers <- "run" })
		r.OnCancel = func() { answers <- "cancel" }
		r.Notes = notes
		if n > 1 {
			r.OnSkip = func() {
				q.a.Record(&cfg, audit.Event{Kind: audit.KindConfirm, Statement: s.SQL, Detail: title + " — skipped"})
				answers <- "skip"
			}
			if !v.TypeName {
				r.OnRunAll = func() {
					q.a.Record(&cfg, audit.Event{Kind: audit.KindConfirm, Statement: s.SQL, Detail: title + " — run all the script's writes"})
					ask.runAll = true
					answers <- "run"
				}
			}
		}
		// Either stops the run, and the cells after it, to look first.
		lookFirst := func(what string) func() {
			return func() {
				if q.nb != nil {
					q.nb.queue = nil
				}
				answers <- what
			}
		}
		switch {
		case scanned && i < len(ask.starts) && q.editorHolds(ask.starts[i], cmp.Or(s.Typed, s.SQL)):
			r.Extra, r.OnExtra = "Show Plan", lookFirst("plan")
		case previewable:
			r.Extra, r.OnExtra = "Preview Changes", lookFirst("preview")
		}
	})
	select {
	case answer = <-answers:
	case <-ctx.Done():
		return "cancel", nil
	}
	switch answer {
	case "plan":
		start := ask.starts[i]
		return "cancel", func() { q.explainStatement(start, s) }
	case "preview":
		start := ask.starts[i]
		return "cancel", func() { q.previewStatement(start, s) }
	}
	return answer, nil
}

// scanReasons explains a statement that reads or writes rows, and says
// of each scan of its plan past the limit that it asks; nothing of a
// plan the engine does not estimate, or that cannot be read, which
// Output tells of.
func (q *Tab) scanReasons(ctx context.Context, ask *inRunAsk, s safety.Statement) []string {
	if ask.scanLimit <= 0 || !explainable(s) {
		return nil
	}
	cfg := q.Conn.Config
	q.a.Post(func() { q.readingPlan = true })
	scans, _, err := largeScans(ctx, ask.sess, s.SQL, float64(ask.scanLimit), func(sql string, took time.Duration, err error) {
		q.a.Record(&cfg, audit.Event{Kind: audit.KindStatement, Database: ask.database, Statement: sql, Params: s.Shown, Purpose: purposeScanCheck,
			Rows: -1, DurationMS: took.Milliseconds(), Err: err})
	}, s.Args...)
	q.a.Post(func() { q.readingPlan = false })
	if err != nil {
		// A connection made again refuses the statement itself next
		// (ErrSessionReset), which says why.
		if ctx.Err() == nil && !errors.Is(err, db.ErrSessionReset) {
			msg := "Ran without the scan check: " + strings.TrimSuffix(widgets.FirstLine(err.Error()), ".") + "."
			q.a.Post(func() { q.noteDetail(msg, err.Error(), s.SQL, false) })
		}
		return nil
	}
	if ask.schema == "" && slices.ContainsFunc(scans, func(sc db.Scan) bool { return sc.Schema != "" }) {
		ask.schema, _ = ask.sess.CurrentSchema(ctx)
	}
	limit := settings.GroupDigits(strconv.Itoa(ask.scanLimit))
	var reasons []string
	for _, sc := range scans {
		table := sc.Table
		if sc.Schema != "" && sc.Schema != ask.schema {
			table = sc.Schema + "." + sc.Table
		}
		rows := settings.GroupDigits(strconv.FormatFloat(math.Round(sc.Rows), 'f', 0, 64))
		reasons = append(reasons, fmt.Sprintf("It reads about %s rows of %s (%s), more than the %s a scan reads without asking (Settings).",
			rows, table, sc.Step, limit))
	}
	return reasons
}

// explainable reports whether a statement reads or changes rows in a way
// a plan tells: not a SHOW, a CALL, a VACUUM or another statement without
// one, whose EXPLAIN would fail at every run.
func explainable(s safety.Statement) bool {
	if c := s.Analysis.Class; c != sqltext.Read && c != sqltext.Write {
		return false
	}
	switch strings.ToUpper(s.Analysis.Verb) {
	case "SELECT", "TABLE", "VALUES", "WITH", "FROM", "INSERT", "UPDATE", "DELETE", "MERGE", "REPLACE", "UPSERT":
		return true
	}
	return false
}

// countNote counts, within countWait, the rows a change the preview can
// read makes, for its question; "" when the count is not a plain read,
// or the statement's values cannot be given to it.
func (q *Tab) countNote(ctx context.Context, ask *inRunAsk, s safety.Statement, p sqltext.WritePreview) string {
	cfg := q.Conn.Config
	an := safety.Analyze(&cfg, []string{p.Count})
	if an[0].Analysis.Class != sqltext.Read || an[0].Analysis.Dangerous {
		return ""
	}
	if v := safety.ReviewSQL(&cfg, an); v.Blocked != "" || v.Confirm || v.Writes {
		return ""
	}
	var args []any
	if len(s.Args) > 0 {
		// The count keeps the statement's WHERE: its values apply when
		// it dropped none of the statement's placeholders.
		if params.Placeholders(p.Count, cfg.Engine, ask.dialect) != params.Placeholders(s.SQL, cfg.Engine, ask.dialect) {
			return ""
		}
		args = s.Args
	}
	cctx, cancel := context.WithTimeout(ctx, countWait)
	defer cancel()
	var count any
	start := time.Now()
	err := ask.sess.ReadAside(cctx, func(ctx context.Context) (err error) {
		count, err = db.QueryValue(ctx, ask.sess, p.Count, args...)
		return err
	})
	q.a.Record(&cfg, audit.Event{Kind: audit.KindStatement, Database: ask.database, Statement: p.Count, Params: s.Shown,
		Purpose: purposeCount, Rows: -1, DurationMS: time.Since(start).Milliseconds(), Err: err})
	switch {
	case ctx.Err() != nil || errors.Is(err, db.ErrSessionReset):
		return ""
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(cctx.Err(), context.DeadlineExceeded):
		return "The rows could not be counted in 5 seconds."
	case err != nil:
		return "The rows could not be counted: " + strings.TrimSuffix(widgets.FirstLine(err.Error()), ".") + "."
	}
	shown := db.Display(count)
	n, nErr := strconv.Atoi(shown)
	if nErr == nil {
		shown = settings.GroupDigits(shown)
	}
	return fmt.Sprintf("It changes %s row%s of %s, counted just now.", shown, widgets.Plural(n), qualifiedName(p.Table))
}

// explainStatement explains a statement of the editor, as Explain does
// on its selection; one the editor no longer holds where it was is not.
func (q *Tab) explainStatement(start int, s safety.Statement) {
	typed := cmp.Or(s.Typed, s.SQL)
	if !q.editorHolds(start, typed) {
		q.note("The statement moved in the editor: select it to explain it.", "", true)
		return
	}
	q.Editor.Select(start, start+len(typed))
	q.Run(RunExplain)
}

// editorHolds reports whether the editor holds sql at start.
func (q *Tab) editorHolds(start int, sql string) bool {
	text := q.Editor.Text()
	return start >= 0 && start+len(sql) <= len(text) && text[start:start+len(sql)] == sql
}

// qualifiedName is a table's name, with its schema when it is written.
func qualifiedName(t sqltext.TableRef) string {
	if t.Schema != "" {
		return t.Schema + "." + t.Name
	}
	return t.Name
}
