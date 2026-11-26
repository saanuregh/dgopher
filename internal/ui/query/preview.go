package query

import (
	"cmp"
	"context"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/params"
	"dgopher/internal/safety"
	"dgopher/internal/settings"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// previewReadOnly is why the rows of a preview are not edited.
const previewReadOnly = "A preview of changes: nothing in it can be edited."

// runPreviewed is the run of a preview's Run: the statement previewed,
// with the values it was previewed with (Tab.toRun).
const runPreviewed RunMode = -1

// writePreview is an UPDATE or DELETE whose rows Preview Changes reads,
// without running it, and what it read of them.
type writePreview struct {
	verb  string // "UPDATE" or "DELETE"
	table string // the target, as written, with its schema
	// changed counts the SET columns, whose new values are the rows'
	// first columns.
	changed int
	// sql is the statement as the editor held it at start, before its
	// parameters were bound with values; Run runs it while the editor
	// still holds it there, on conn and database.
	sql      string
	start    int
	values   map[string]params.Input
	conn     *connection.Conn
	database string
	// count reads how many rows it changes, before its rows are read.
	count safety.Statement
	// total is what count read, -1 until then; countErr is why it read
	// nothing. The run sets both before it shows the rows.
	total    int64
	countErr string
}

// runPreview previews the statement at the caret, or the one selected,
// or, for runPreviewed, runs the statement a preview read.
func (q *Tab) runPreview(mode RunMode) {
	q.newTab = false
	if mode == runPreviewed {
		p := q.toRun
		q.toRun = nil
		why := "Nothing to run: the preview is gone."
		if p != nil {
			why = q.previewStale(p)
		}
		if why != "" {
			q.refuse(why)
			q.cellSkipped()
			return
		}
		q.runBound([]string{p.sql}, []int{p.start}, p.values, 0, nil)
		return
	}
	stmts, starts := q.statements(RunStatement)
	if len(stmts) != 1 {
		msg := "Nothing to preview: the caret is not in a statement."
		if q.Editor.SelectedText() != "" {
			msg = "Select one statement to preview."
		}
		q.refuse(msg)
		q.cellSkipped()
		return
	}
	q.withParams(stmts, func(values map[string]params.Input) {
		q.newTab = false
		q.previewBound(stmts[0], starts[0], values)
	})
}

// previewBound reads the rows a statement would change, once its
// parameters have their values: the read-only SELECTs of its rewrite,
// reviewed as any statement is, run on the editor's session as its
// reads are. A statement the rewrite cannot read sends nothing.
func (q *Tab) previewBound(typed string, start int, values map[string]params.Input) {
	cfg := &q.Conn.Config
	d := q.Editor.Dialect
	sql, shown := typed, ""
	var args []any
	if values != nil {
		var err error
		if sql, args, shown, err = params.Bind(typed, cfg.Engine, d, values); err != nil {
			q.note(err.Error(), typed, true)
			q.a.ShowReport(widgets.ErrorReportOf("Could not bind the parameters", err))
			q.cellSkipped()
			return
		}
	}
	var p sqltext.WritePreview
	why := sqltext.NotAWrite
	if r, ok := db.SQL(cfg.Engine); ok {
		p, why = sqltext.PreviewWrite(sql, d, r.PreviewNewName)
	}
	if why == "" && len(args) > 0 {
		// Both reads keep the statement's values, which fit them only
		// when they hold every one of its placeholders.
		n := params.Placeholders(sql, cfg.Engine, d)
		if params.Placeholders(p.Rows, cfg.Engine, d) != n || params.Placeholders(p.Count, cfg.Engine, d) != n {
			why = "Preview Changes cannot give this statement's values to its preview: it reads the rows without some of its parameters."
		}
	}
	if why != "" {
		q.note(why+" Nothing ran.", "", true)
		q.cellSkipped()
		return
	}
	an := safety.Analyze(cfg, []string{p.Count, p.Rows})
	for i := range an {
		// Their text is the app's: an error's position maps to none of
		// the editor's.
		an[i].Args, an[i].Shown, an[i].Start = args, shown, -1
	}
	v, ok := q.review(an)
	if !ok {
		return
	}
	prev := &writePreview{verb: p.Verb, table: qualifiedName(p.Table), changed: len(p.Changed), sql: typed, start: start, values: values,
		conn: q.Conn, database: q.Database, count: an[0], total: -1}
	q.runReviewed(v, an, an[1:], []int{start}, nil, prev)
}

// previewStatement previews a statement of the editor, as Preview
// Changes does on its selection; one the editor no longer holds where it
// was is not.
func (q *Tab) previewStatement(start int, s safety.Statement) {
	typed := cmp.Or(s.Typed, s.SQL)
	if !q.editorHolds(start, typed) {
		q.note("The statement moved in the editor: select it to preview it.", "", true)
		return
	}
	q.Editor.Select(start, start+len(typed))
	q.Run(RunPreview)
}

// countPreview reads, off the main thread, how many rows a preview's
// statement changes, as one of the editor's reads, before the rows.
func (q *Tab) countPreview(ctx context.Context, sess *db.Session, cfg db.Config, database string, p *writePreview, purpose string) {
	s := p.count
	sent := time.Now()
	value, err := db.QueryValue(ctx, sess, s.SQL, s.Args...)
	read := int64(0)
	if err == nil {
		read = 1
	}
	q.a.RecordTyped(cfg, audit.TypedRun{Kind: audit.KindStatement, Database: database, Sent: s.SQL, Params: s.Shown,
		Rows: read, Took: time.Since(sent), Err: err, History: true, Purpose: purpose})
	if err != nil {
		p.countErr = strings.TrimSuffix(widgets.FirstLine(err.Error()), ".")
		return
	}
	n, err := strconv.ParseInt(db.Display(value), 10, 64)
	if err != nil {
		p.countErr = "the count read " + db.Display(value)
		return
	}
	p.total = n
}

// dress makes a result the preview's: named for the statement, its rows
// read-only.
func (p *writePreview) dress(res *result) {
	res.preview = p
	res.tableWhy = previewReadOnly
	res.name = "Preview · " + p.verb + " " + p.table
	res.tip = widgets.OneLine(p.sql, 300)
}

// tint colours the cells a preview's statement changes: an UPDATE's new
// values as pending edits, every row of a DELETE as a deleted row.
func (p *writePreview) tint(row, col int, pal *widgets.Palette) (ui.Color, bool) {
	switch {
	case p.verb == "DELETE":
		return pal.Deleted, true
	case col < p.changed:
		return pal.Modified, true
	}
	return ui.Color{}, false
}

// previewStale says why a preview's Run cannot run its statement, ""
// when it can: the editor no longer holds it where it was previewed, or
// runs on another connection or database.
func (q *Tab) previewStale(p *writePreview) string {
	if p.conn != q.Conn || p.database != q.Database {
		return keymap.Hint("The editor is on another connection or database since this preview: preview it again", keymap.PreviewChanges) + "."
	}
	if !q.editorHolds(p.start, p.sql) {
		return keymap.Hint("The statement changed since this preview: preview it again", keymap.PreviewChanges) + "."
	}
	return ""
}

// previewBar draws a preview's bar between its toolbar and its rows; nil
// for a result that is no preview.
func (q *Tab) previewBar(res *result) func(c *ui.Context) {
	if res.preview == nil {
		return nil
	}
	return func(c *ui.Context) { q.previewNotice(c, res) }
}

// previewNotice says how many rows the statement changes, and that it has
// not run, with Cancel, which closes the preview, and Run, which runs it.
func (q *Tab) previewNotice(c *ui.Context, res *result) {
	p := res.preview
	widgets.NoticeBar(c.Key("preview-bar"), c.Theme().Warning, func() {
		text := ui.Text(c, "").FontSize(widgets.TextBody).Grow(1).Shrink(1)
		text.Children(func() {
			if p.total < 0 {
				ui.Text(c, "The rows could not be counted: "+p.countErr+".")
				return
			}
			ui.Text(c, settings.GroupDigits(strconv.FormatInt(p.total, 10))+" row"+widgets.Plural(int(p.total))).Bold()
			done := "would change"
			if p.verb == "DELETE" {
				done = "would be deleted"
			}
			ui.Text(c, " of "+p.table+" "+done+". Nothing has run.")
		})
		busy := q.busyWhy()
		if widgets.Clicked(ui.Button(c, "Cancel").Label("Close Preview").Tooltip("Close the preview: nothing runs"), busy) {
			q.closeResult(res)
		}
		why := cmp.Or(q.previewStale(p), busy)
		run := widgets.DangerButton(c, "Run", why).Label("Run Previewed Statement")
		if why == "" {
			run.Tooltip("Run the statement previewed, as Run does: it asks first where the connection asks")
		}
		if run.Clicked() && why == "" {
			q.toRun = p
			q.Run(runPreviewed)
		}
	})
}
