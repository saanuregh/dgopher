// Package query is the query tab: the SQL editor of a file, the statements
// it runs, their results, and its parameters, completion and find.
package query

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/params"
	"dgopher/internal/project"
	"dgopher/internal/redact"
	"dgopher/internal/safety"
	"dgopher/internal/settings"
	"dgopher/internal/sqltext"
	"dgopher/internal/store"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/editor"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// result is the outcome of one statement.
type result struct {
	sql  string
	verb string
	// stmt is the statement as run, which a refresh runs again.
	stmt safety.Statement
	// table is the one table the rows are read from, as written, else
	// tableWhy says why there is none; schema is where the editor's
	// session found an unqualified table, as the statement ran.
	table     sqltext.TableRef
	tableWhy  string
	schema    string
	schemaErr error
	// view shows the rows of a statement that returned some; nil for
	// none.
	view     *dataview.Viewer
	affected int64
	elapsed  time.Duration
	err      string
	rowsMode bool // returned rows, rather than a count
	// errInfo is what the database said of a failed statement, and start
	// where the statement starts in the editor (-1 when unknown).
	errInfo db.ErrorInfo
	start   int
	// skip counts the runes the app put before the editor's text.
	skip int
	// pinned keeps the result when the next run replaces the others.
	pinned bool
	// plan is what an explain's rows tell, nil for other rows, with the
	// advice about it; planMode is how it shows, planSel the step chosen.
	plan     *db.Plan
	advice   []db.Advice
	planMode int
	planSel  *db.PlanNode
}

type message struct {
	at   time.Time
	text string
	sql  string
	err  bool
}

// Tab is a SQL editor with its results, on a session of its own.
type Tab struct {
	a        Host
	Conn     *connection.Conn
	Database string
	Name     string
	closed   bool
	Path     string // the file the SQL was opened from or saved to
	Saved    string // the text as last read from or written to path
	// Created is what the app first wrote in a new query file: closed
	// with only that, the file goes, as a look that kept nothing.
	Created string
	// Typed and typedAt are the text as last seen changing, and when:
	// the file is saved once typing stops.
	Typed   string
	typedAt time.Time
	// DiskConflict says why the file is not saved as typed: it changed or
	// went on disk since the editor read it, as by a git pull.
	DiskConflict string
	// The statement under the caret, as currentStatement last found it.
	stmtText  string
	stmtKey   [2]int
	stmtRange [2]int
	stmtOK    bool
	// newTab keeps the results of earlier runs for the next one.
	newTab bool
	// txs is when the session's transaction opened and was last used.
	txs connection.TxTimes
	// stepConfirm asks before each write of the next run.
	stepConfirm bool
	// paramValues are the last values given to parameters, by key.
	paramValues map[string]params.Input

	Editor  editor.Editor
	editorH float32
	ac      completion
	// snippet is the fields of a snippet being filled in, nil for none.
	snippet *snippetSession
	// schema is the schema the session finds names in, as last read,
	// "" before it is.
	schema string
	// checkedText and checkedCatalog are what the editor's problems were
	// found for: its text, and how much of the catalog was read.
	checkedText, checkedCatalog string
	find                        editor.Find

	sess      *db.Session
	sessErr   string
	Running   bool
	cancel    context.CancelFunc
	StartedAt time.Time
	Tx        db.TxState

	results   []*result
	resultIdx int // len(results) for the messages
	messages  []message

	// named is the connection the file's header names, as namedConnection last
	// read it from namedText.
	named, namedText string
	// exports counts the exports reading on the session.
	exports int
}

// key reports whether mods+key was pressed for this editor: while its
// tab takes the shortcuts.
func (q *Tab) key(c *ui.Context, mods ui.Modifiers, key ui.Key) bool {
	return q.a.KeysTo(q) && c.Shortcut(mods, key)
}

// pressed reports whether a key of a command was pressed, the keys going
// to the tab.
func (q *Tab) pressed(c *ui.Context, id string) bool {
	return q.a.KeysTo(q) && keymap.Pressed(c, id)
}

func New(a Host, cn *connection.Conn, database, name, text string) *Tab {
	q := &Tab{a: a, Conn: cn, Database: database, Name: name, editorH: 260}
	q.Editor.Text = text
	q.ac.lastText = text // what the tab opens on was not typed
	q.Editor.Dialect = safety.Dialect(cn.Config.Engine)
	q.Editor.WantFocus = true
	q.Editor.KeyHook = q.editorKey
	if text != "" {
		n := len([]rune(text))
		q.Editor.PendingSel = &[2]int{n, n}
	}
	return q
}

func (q *Tab) Title() string { return q.Name }

func (q *Tab) Connection() *connection.Conn { return q.Conn }

func (q *Tab) CloseReason() string {
	switch {
	case q.Path != "" && q.DiskConflict != "" && q.Editor.Text != q.Saved:
		return q.Name + " " + q.DiskConflict + ". Closing the editor leaves the file as it is on disk, losing the editor's text."
	case q.pendingCount() > 0:
		return fmt.Sprintf("%d changes to %s in the results have not been applied. Closing discards them.", q.pendingCount(), q.pendingTable())
	case q.Tx != db.TxNone:
		return "A transaction is open on this editor's session. Closing the editor rolls it back, losing its changes."
	case q.Running:
		return "A statement is still running. Closing the editor cancels it."
	case q.exports > 0:
		return "An export is reading on this editor's session. Closing the editor stops it."
	}
	return ""
}

// Close ends the tab's work: its fields are read here, on the main
// thread, and what may block runs on another goroutine.
func (q *Tab) Close() {
	q.closed = true
	q.Flush(false)
	if q.Created != "" && q.DiskConflict == "" && q.Saved == q.Created && q.Editor.Text == q.Created {
		// Only what the app wrote goes: someone may have written there since.
		if disk, err := os.ReadFile(q.Path); err == nil && string(disk) == q.Created && os.Remove(q.Path) == nil {
			q.a.ScanQueries(q.Conn.Project, true)
		}
	}
	if q.Tx != db.TxNone {
		q.a.Record(&q.Conn.Config, audit.Event{Kind: audit.KindStatement, Database: q.Database, Statement: "ROLLBACK", Detail: "the tab closed with its transaction open"})
	}
	var closers []func()
	for _, r := range q.results {
		if r.view != nil {
			closers = append(closers, r.view.Release())
		}
	}
	cancel, sess := q.cancel, q.sess
	go func() {
		for _, close := range closers {
			close()
		}
		connection.CloseThenCancel(nil, cancel)
		if sess != nil {
			sess.Close()
		}
	}()
}

// pendingCount counts the changes of the results not applied yet.
func (q *Tab) pendingCount() int {
	n := 0
	for _, r := range q.results {
		if r.view != nil {
			n += r.view.PendingCount()
		}
	}
	return n
}

// pendingTable names the table of the first result with changes pending.
func (q *Tab) pendingTable() string {
	for _, r := range q.results {
		if r.view != nil && r.view.PendingCount() > 0 {
			return r.view.TableName()
		}
	}
	return ""
}

// settlePending asks, result by result, what becomes of the changes
// pending in results, then runs then: after they are applied or
// discarded, never when the user cancels.
func (q *Tab) settlePending(results []*result, then func()) {
	for i, r := range results {
		if r.view != nil && r.view.PendingCount() > 0 {
			rest := results[i+1:]
			r.view.CheckPending(func() { q.settlePending(rest, then) }, nil)
			return
		}
	}
	then()
}

// replaced lists the results a run replaces: all but those pinned, none
// for a run into a new tab.
func (q *Tab) replaced(mode RunMode) []*result {
	var out []*result
	for _, r := range q.results {
		if mode != RunNewTab && !r.pinned {
			out = append(out, r)
		}
	}
	return out
}

// sessionBusy says why the session cannot take a statement of a result
// now: the editor runs one, or another result reads or applies.
func (q *Tab) sessionBusy(self *result) string {
	switch {
	case q.Running:
		return "A statement is running in the editor."
	case q.exports > 0:
		return "An export is reading on the editor's session."
	}
	for _, r := range q.results {
		if r != self && r.view != nil && r.view.Busy() {
			return "Another result is being read, or its changes applied."
		}
	}
	return ""
}

// cursorOpen says which result still reads its rows, which a statement on
// the session would end; "" when none does.
func (q *Tab) cursorOpen() string {
	for i, r := range q.results {
		if r.view != nil && r.view.HoldsCursor() {
			return fmt.Sprintf("result %d is still reading its rows: Fetch All first, or run the next statement.", i+1)
		}
	}
	return ""
}

// resultsBusy reports a result reading or applying on the session.
func (q *Tab) resultsBusy() bool {
	for _, r := range q.results {
		if r.view != nil && r.view.Busy() {
			return true
		}
	}
	return false
}

// save applies the changes pending in results, reviewed one result after
// the other, then saves the file, as DBeaver's ⌘S does in an editor.
func (q *Tab) save() {
	for _, r := range q.results {
		if r.view != nil && r.view.PendingCount() > 0 {
			r.view.Review(q.save)
			return
		}
	}
	q.a.SaveSQLFile(q, false)
}

// adoptSession keeps a session a goroutine started for the tab, on the
// main thread; a tab closed meanwhile closes it instead.
func (q *Tab) adoptSession(s *db.Session) {
	q.a.Post(func() {
		if q.closed || q.sess != nil {
			if q.sess != s {
				go s.Close()
			}
			return
		}
		q.sess = s
	})
}

// RunMode is what a run takes from the editor.
type RunMode int

const (
	RunStatement      RunMode = iota // the selection, or the statement at the caret
	RunScript                        // every statement
	RunExplain                       // the plan of the statement at the caret
	RunNewTab                        // as runStatement, keeping the results shown
	RunExplainAnalyze                // the plan of the statement at the caret, as it runs
)

// statements returns what a run sends, and where each starts in the
// editor. Explaining, the statement is the selection, or the one at the
// caret, behind EXPLAIN; it is never run as it is.
func (q *Tab) statements(mode RunMode) ([]string, []int) {
	d := q.Editor.Dialect
	text, base := q.Editor.Text, 0
	selected := q.Editor.Selection() != ""
	if selected {
		text, base = q.Editor.Selection(), min(q.Editor.SelStart, q.Editor.SelEnd)
	}
	var out []string
	var starts []int
	switch {
	case explaining(mode) && selected:
		stmts := sqltext.SplitWith(text, d, SplitOptions(q.a.Settings()))
		if len(stmts) != 1 {
			return nil, nil
		}
		out, starts = []string{stmts[0].Text}, []int{base + stmts[0].Start}
	case mode == RunScript || selected:
		for _, s := range sqltext.SplitWith(text, d, SplitOptions(q.a.Settings())) {
			out, starts = append(out, s.Text), append(starts, base+s.Start)
		}
		return out, starts
	default:
		if st, ok := sqltext.StatementAtWith(text, q.Editor.SelEnd, d, SplitOptions(q.a.Settings())); ok {
			out, starts = append(out, st.Text), append(starts, st.Start)
		}
	}
	if explaining(mode) && len(out) == 1 {
		out[0] = db.ExplainPrefix(q.Conn.Config.Engine, mode == RunExplainAnalyze) + out[0]
	}
	return out, starts
}

func explaining(mode RunMode) bool { return mode == RunExplain || mode == RunExplainAnalyze }

// StatementToRun is the statement a run would send: the one selected, or
// the one at the caret; an error when that is not one statement.
func (q *Tab) StatementToRun() (string, error) {
	stmts, _ := q.statements(RunStatement)
	if len(stmts) != 1 {
		return "", fmt.Errorf("choose one statement, by the caret or a selection: %d are", len(stmts))
	}
	return stmts[0], nil
}

// analyzeRefusal says why a statement's plan cannot be measured by
// running it, "" when it can: the engine explains without running, or
// the statement changes the database.
func (q *Tab) analyzeRefusal() string {
	e := q.Conn.Config.Engine
	if db.ExplainPrefix(e, true) == "" {
		return e.Label() + " explains a statement without running it: Explain shows its plan."
	}
	stmts, _ := q.statements(RunStatement)
	if len(stmts) != 1 {
		return "Put the caret in one statement, or select it, to explain it."
	}
	if sqltext.Classify(stmts[0], q.Editor.Dialect).Class != sqltext.Read {
		return "Explain Analyze runs the statement: only one that reads is explained so."
	}
	return ""
}

// namedConnection is the connection the file's header names, "" for
// none.
func (q *Tab) namedConnection() string {
	if q.Editor.Text != q.namedText {
		q.named, q.namedText = project.HeaderConnection(q.Editor.Text), q.Editor.Text
	}
	return q.named
}

// namesAnother returns the connection the file's header names when it
// is not the editor's, "" when it is or it names none, and whether the
// project has that connection.
func (q *Tab) namesAnother() (id string, exists bool) {
	id = q.namedConnection()
	p := q.Conn.Project
	if id == "" || p.Prefix+id == q.Conn.Config.ID {
		return "", false
	}
	exists = slices.ContainsFunc(q.a.ProjectConfigs(p), func(cfg db.Config) bool {
		return cfg.ID == p.Prefix+id && cfg.Engine.IsSQL()
	})
	return id, exists
}

// refuseAnotherConnection says so and reports true when the file names
// another connection than the editor's: a file runs only on the one it
// names, as the next person to open it would run it.
func (q *Tab) refuseAnotherConnection() bool {
	id, _ := q.namesAnother()
	if id == "" {
		return false
	}
	q.a.ShowError("Not run", fmt.Sprintf("%s names the connection %s on its first lines, and this editor is on %s. A file runs only on the connection it names: switch the editor to %s, or change the line.",
		q.Name, id, strings.TrimPrefix(q.Conn.Config.ID, q.Conn.Project.Prefix), id))
	return true
}

// Run runs statements of the editor, through the safety policy, once
// the results it replaces have no changes pending.
func (q *Tab) Run(mode RunMode) {
	if q.Running || q.refuseWhileResultsWork() || q.refuseAnotherConnection() {
		return
	}
	if q.Conn.Status != connection.StatusConnected {
		// A connection without auto-connect opens here; the run resumes once it is open.
		q.a.Connect(q.Conn, func() { q.Run(mode) })
		return
	}
	q.settlePending(q.replaced(mode), func() { q.run(mode) })
}

// refuseWhileResultsWork says so and reports true while a result writes
// its changes, whose transaction a statement would land in, or reads on
// the session, as a count or an export, which cancelled would abort the
// editor's transaction.
func (q *Tab) refuseWhileResultsWork() bool {
	if q.exports > 0 {
		q.a.ShowError("Not run", "An export is reading on the editor's session: run once it ends.")
		return true
	}
	for _, r := range q.results {
		switch {
		case r.view == nil:
		case r.view.Applying():
			q.a.ShowError("Not run", "The changes of a result are being applied: run once they are.")
			return true
		case r.view.Counting():
			q.a.ShowError("Not run", "A count or an export is reading on the editor's session: run once it ends.")
			return true
		}
	}
	return false
}

func (q *Tab) run(mode RunMode) {
	if q.Running {
		return
	}
	if mode == RunExplainAnalyze {
		if why := q.analyzeRefusal(); why != "" {
			q.note(why, "", true)
			return
		}
	}
	q.newTab = mode == RunNewTab
	stmts, starts := q.statements(mode)
	if len(stmts) == 0 {
		msg := "Nothing to run: the caret is not in a statement."
		if explaining(mode) && q.Editor.Selection() != "" {
			msg = "Select one statement to explain."
		}
		q.note(msg, "", true)
		return
	}
	skip := 0
	if explaining(mode) {
		skip = len([]rune(db.ExplainPrefix(q.Conn.Config.Engine, mode == RunExplainAnalyze)))
	}
	keys := params.Keys(stmts, q.Editor.Dialect)
	if len(keys) == 0 {
		q.runBound(stmts, starts, nil, skip)
		return
	}
	f := &paramForm{open: true}
	for _, k := range keys {
		f.fields = append(f.fields, paramField{key: k, value: q.paramValues[k].Value, kind: max(slices.Index(params.Kinds, q.paramValues[k].Kind), 0)})
	}
	newTab := q.newTab
	f.run = func(values map[string]params.Input) {
		if q.paramValues == nil {
			q.paramValues = map[string]params.Input{}
		}
		for k, v := range values {
			q.paramValues[k] = v
		}
		q.newTab = newTab
		q.runBound(stmts, starts, values, skip)
	}
	q.a.QueryDialogs().params = f
}

// runBound reviews statements whose parameters have their values, then
// runs them. The policy reads them as they will run: a ${var} that holds
// a DELETE is a DELETE.
func (q *Tab) runBound(stmts []string, starts []int, values map[string]params.Input, skip int) {
	cfg := &q.Conn.Config
	sqls := make([]string, len(stmts))
	args := make([][]any, len(stmts))
	shown := make([]string, len(stmts))
	for i, s := range stmts {
		sqls[i] = s
		if values != nil {
			var err error
			if sqls[i], args[i], shown[i], err = params.Bind(s, cfg.Engine, q.Editor.Dialect, values); err != nil {
				q.note(err.Error(), s, true)
				q.a.ShowError("Could not bind the parameters", err.Error())
				return
			}
		}
	}
	// A value written into the SQL may hold more: what runs is reviewed,
	// and it must still be the one statement the editor showed.
	for i, s := range sqls {
		if s != stmts[i] && len(sqltext.SplitWith(s, q.Editor.Dialect, sqltext.SplitOptions{Mode: sqltext.SemicolonOnly})) != 1 {
			msg := "A ${…} value adds a statement to " + widgets.OneLine(stmts[i], 60) + ": it is not run."
			q.note(msg, s, true)
			q.a.RecordBlocked(q.Conn, msg, s)
			q.a.ShowError("Not allowed", msg)
			return
		}
	}
	an := safety.Analyze(cfg, sqls)
	for i := range an {
		an[i].Start, an[i].Args, an[i].Shown = starts[i], args[i], shown[i]
		if sqls[i] != stmts[i] {
			an[i].Start = -1 // the text moved: an error's position no longer maps
		}
		an[i].Skip = skip
	}
	v := safety.ReviewSQL(cfg, an)
	if v.Blocked != "" {
		q.a.RecordBlocked(q.Conn, v.Blocked, strings.Join(sqls, ";\n"))
		q.note(v.Blocked, "", true)
		q.a.ShowError("Not allowed", v.Blocked)
		return
	}
	if v.Confirm && cfg.Env == db.Production && len(an) > 1 {
		// A script on production asks before each write, as it comes.
		q.stepConfirm = true
		q.execute(an, v.Writes)
		return
	}
	if v.Confirm {
		title := fmt.Sprintf("Run %d statement%s on %s?", len(sqls), widgets.Plural(len(sqls)), cfg.Name)
		preview := strings.Join(sqls, ";\n\n") + ";"
		for _, sh := range shown {
			if sh != "" {
				preview += "\n-- " + sh
			}
		}
		q.a.AskConfirm(q.Conn, v, title, "Run", preview, func() { q.execute(an, v.Writes) })
		return
	}
	q.execute(an, v.Writes)
}

// wantsRows reports whether a statement, in dialect d, is read with a
// cursor rather than executed for a count of rows.
func wantsRows(s safety.Statement, d sqltext.Dialect) bool {
	if s.Analysis.Class == sqltext.Read {
		return true
	}
	switch s.Analysis.Verb {
	case "PRAGMA", "CALL", "EXEC", "EXECUTE", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "WITH",
		"SUMMARIZE", "PIVOT", "UNPIVOT", "FETCH":
		return true
	case "SELECT", "TABLE", "VALUES", "FROM":
		// A query writing INTO a table or file returns no rows.
		for _, t := range sqltext.Tokenize(s.SQL, d) {
			if t.Kind == sqltext.Keyword && strings.EqualFold(t.Text, "INTO") {
				return false
			}
		}
		return true
	}
	return strings.Contains(strings.ToUpper(s.SQL), "RETURNING")
}

func (q *Tab) execute(stmts []safety.Statement, writes bool) {
	// The context lives as long as the cursors of the results it reads:
	// the next run, or closing the tab, ends it.
	oldCancel := q.cancel
	ctx, cancel := context.WithCancel(context.Background())
	q.Running, q.cancel, q.StartedAt = true, cancel, time.Now()
	// A run replaces the results, but those pinned and, run into a new
	// tab, all of them; the cursors of those it drops close, and a kept
	// one that was still reading ends where it is.
	var kept []*result
	var closers []func()
	for _, r := range q.results {
		switch {
		case r.view == nil:
		case !q.newTab && !r.pinned:
			closers = append(closers, r.view.Release())
		case !r.view.Done():
			closers = append(closers, r.view.CutShort())
		}
		if q.newTab || r.pinned {
			kept = append(kept, r)
		}
	}
	q.newTab = false
	stepConfirm := q.stepConfirm
	q.stepConfirm = false
	q.results = kept
	firstNew := len(kept)
	q.resultIdx = firstNew
	timeout := time.Duration(q.Conn.Config.StatementTimeout) * time.Second
	continueOnError := q.a.Settings().ContinueOnError
	manual := q.Conn.Config.ManualCommit()
	pageSize := q.a.Settings().PageSize
	cfg := q.Conn.Config
	st := q.Conn.Project.Local
	sess, pool, database := q.sess, q.Conn.DB, q.Database
	dialect := q.Editor.Dialect
	go func() {
		for _, close := range closers {
			close()
		}
		connection.CloseThenCancel(nil, oldCancel)
		var err error
		if sess == nil {
			if sess, err = connection.OpenSession(ctx, pool, database); err != nil {
				q.a.Post(func() {
					q.Running = false
					q.note(err.Error(), "", true)
				})
				return
			}
			q.adoptSession(sess)
		}
		if manual && writes && sess.Tx() == db.TxNone {
			err := sess.Begin(ctx)
			q.a.RecordRun(cfg, audit.KindStatement, database, "BEGIN -- manual commit", -1, 0, err)
			if err != nil {
				q.a.Post(func() {
					q.Running = false
					q.note("Could not open a transaction: "+err.Error(), "", true)
				})
				return
			}
			q.a.Post(func() { q.note("Manual commit: opened a transaction. Commit or roll back when done.", "", false) })
		}
		runAll := false
		for i, s := range stmts {
			if stepConfirm {
				answer := q.askStep(ctx, s, i, len(stmts), &runAll)
				if answer == "skip" {
					q.a.Post(func() { q.note("Skipped: "+widgets.OneLine(s.SQL, 120), s.SQL, false) })
					continue
				}
				if answer == "cancel" {
					q.a.Post(func() { q.note(fmt.Sprintf("Stopped before statement %d of %d.", i+1, len(stmts)), "", true) })
					break
				}
			}
			res := &result{sql: s.SQL, verb: s.Analysis.Verb, stmt: s, start: s.Start, skip: s.Skip}
			res.table, res.tableWhy = sqltext.SingleTable(s.SQL, dialect)
			var cursor *db.Cursor
			var first [][]any
			var done bool
			start := time.Now()
			var rows int64
			// The timeout bounds the statement and its first page, not the
			// cursor's later pages: a deadline on the context would end
			// them as the user scrolls, so a timer cancels it instead.
			sctx, scancel := context.WithCancel(ctx)
			var timedOut atomic.Bool
			var timer *time.Timer
			if timeout > 0 {
				timer = time.AfterFunc(timeout, func() { timedOut.Store(true); scancel() })
			}
			if wantsRows(s, dialect) && res.tableWhy == "" && res.table.Schema == "" {
				// Where the session finds the table now, before the
				// statement: its own SET search_path or USE may have moved.
				if sess.Tx() == db.TxFailed {
					res.schemaErr = errors.New("the transaction failed")
				} else {
					res.schema, res.schemaErr = sess.CurrentSchema(sctx)
				}
			}
			if wantsRows(s, dialect) {
				res.rowsMode = true
				cursor, err = sess.Query(sctx, s.SQL, s.Args...)
				if err == nil {
					first, err = cursor.Fetch(pageSize)
					done = cursor.Done()
					rows = int64(len(first))
					if len(cursor.Columns) == 0 {
						res.rowsMode = false
						res.affected = -1
					}
					if strings.EqualFold(res.verb, "EXPLAIN") {
						names := make([]string, len(cursor.Columns))
						for i, col := range cursor.Columns {
							names[i] = col.Name
						}
						if plan, ok := db.ParsePlan(cfg.Engine, names, first); ok {
							res.plan, res.advice = plan, db.Advise(cfg.Engine, plan)
						}
					}
				}
			} else {
				res.affected, err = sess.Exec(sctx, s.SQL, s.Args...)
				rows = res.affected
			}
			if timer != nil {
				timer.Stop()
			}
			stop := context.CancelFunc(scancel) // with the cursor
			if !res.rowsMode || err != nil {
				scancel()
				stop = nil
			}
			if err != nil && timedOut.Load() {
				err = fmt.Errorf("stopped after the connection's statement timeout of %s: %w", timeout, err)
			}
			res.elapsed = time.Since(start)
			logged := s.SQL
			if s.Shown != "" {
				logged += "\n-- parameters: " + s.Shown
			}
			q.a.RecordRun(cfg, audit.KindStatement, database, logged, rows, res.elapsed, err)
			entry := store.HistoryEntry{Time: start, ConnectionID: cfg.ID, Connection: cfg.Name, Database: database,
				SQL: redact.Secrets(logged), Duration: res.elapsed, Rows: rows}
			if err != nil {
				res.err = err.Error()
				res.errInfo = db.DescribeError(err)
				if timedOut.Load() {
					res.errInfo.Message = fmt.Sprintf("Stopped after the connection's statement timeout of %s. %s", timeout, res.errInfo.Message)
				}
				entry.Error = res.err
			}
			st.AppendHistory(entry)
			q.a.Post(func() {
				if res.rowsMode && cursor != nil {
					res.view = q.newView(res, cursor, first, done, stop)
				}
				q.results = append(q.results, res)
				switch {
				case res.err != "":
					q.note(res.err, res.sql, true)
				case res.rowsMode:
					more := ""
					if !done {
						more = "+"
					}
					q.note(fmt.Sprintf("%s · %d%s rows · %s", verbLabel(res.verb), len(first), more, widgets.FormatDuration(res.elapsed)), res.sql, false)
				default:
					q.note(fmt.Sprintf("%s · %s · %s", verbLabel(res.verb), affectedLabel(res.affected), widgets.FormatDuration(res.elapsed)), res.sql, false)
				}
			})
			if err != nil && !continueOnError {
				break
			}
		}
		tx := sess.Tx()
		// A SET search_path or a USE may have moved where names are found.
		schema := ""
		if tx != db.TxFailed && slices.ContainsFunc(stmts, func(s safety.Statement) bool { return s.Analysis.Verb == "SET" || s.Analysis.Verb == "USE" }) {
			schema, _ = sess.CurrentSchema(ctx)
		}
		q.a.Post(func() {
			q.Running = false
			if schema != "" {
				q.schema = schema
			}
			q.txs.Set(tx, q.a.Now())
			q.Tx = tx
			// A statement's cursor closes as the next one runs: those
			// results end where they were read.
			for i, r := range q.results {
				if i >= firstNew && i < len(q.results)-1 && r.view != nil {
					r.view.CutShort()
				}
			}
			// Show the error, else the last result with rows, else the
			// messages.
			q.resultIdx = len(q.results)
			for i := len(q.results) - 1; i >= firstNew; i-- {
				if q.results[i].rowsMode && q.results[i].err == "" {
					q.resultIdx = i
					break
				}
			}
			for i, r := range q.results[firstNew:] {
				if r.err != "" {
					i += firstNew
					q.resultIdx = i
					break
				}
			}
			refreshAfterDDL(q.Conn, stmts)
			q.notifyRun(len(stmts), q.results[firstNew:])
		})
	}()
}

// notifyRun tells of the end of a run by a system notification, when it
// took long and the window is in the background: what ran, where, and
// the first error, without the SQL, which may hold what the screen of
// another app should not.
func (q *Tab) notifyRun(stmts int, results []*result) {
	what := "Query"
	if stmts > 1 {
		what = "Script"
	}
	title := what + " finished"
	body := q.Name + " on " + q.Conn.Config.Name + " · " + widgets.FormatDuration(time.Since(q.StartedAt))
	for _, r := range results {
		if r.err != "" {
			title = what + " failed"
			body += "\n" + redact.Secrets(widgets.FirstLine(r.err))
			break
		}
	}
	q.a.Notify(q.StartedAt, title, body, func() {
		q.a.ActivateTab(func(t widgets.Tab) bool { return t == q })
	})
}

func verbLabel(v string) string {
	if v == "" {
		return "OK"
	}
	return v
}

func affectedLabel(n int64) string {
	switch {
	case n < 0:
		return "done"
	case n == 1:
		return "1 row affected"
	}
	return fmt.Sprintf("%d rows affected", n)
}

func (q *Tab) note(text, sql string, isErr bool) {
	q.messages = append(q.messages, message{at: time.Now(), text: text, sql: sql, err: isErr})
	if isErr {
		q.resultIdx = len(q.results)
	}
}

func (q *Tab) endTx(commit bool) {
	if q.sess == nil || q.Busy() {
		return
	}
	q.Running = true
	sess, cfg, database := q.sess, q.Conn.Config, q.Database
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var err error
		word, stmt := "Committed", "COMMIT"
		start := time.Now()
		if commit {
			err = sess.Commit(ctx)
		} else {
			word, stmt = "Rolled back", "ROLLBACK"
			err = sess.Rollback(ctx)
		}
		q.a.RecordRun(cfg, audit.KindStatement, database, stmt, -1, time.Since(start), err)
		tx := sess.Tx()
		q.a.Post(func() {
			q.Running, q.Tx = false, tx
			q.txs.Set(tx, q.a.Now())
			if f := q.txs.Then; f != nil {
				q.txs.Then = nil
				f(err)
			}
			for _, r := range q.results {
				if r.view != nil {
					r.view.CutShort()
				}
			}
			if err != nil {
				q.note(err.Error(), "", true)
				return
			}
			q.note(word+".", "", false)
			refreshAfterDDL(q.Conn, nil)
		})
	}()
}

// format formats the selected statements, or all of them.
func (q *Tab) format() {
	d := q.Editor.Dialect
	if q.Editor.Selection() != "" {
		start, end := min(q.Editor.SelStart, q.Editor.SelEnd), max(q.Editor.SelStart, q.Editor.SelEnd)
		text, from, to := sqltext.FormatRange(q.Editor.Text, start, end, d, SplitOptions(q.a.Settings()))
		q.Editor.Text = text
		q.Editor.PendingSel = &[2]int{from, to}
		return
	}
	q.Editor.Text = sqltext.FormatWith(q.Editor.Text, d, SplitOptions(q.a.Settings()))
}

// currentStatement is the range of the statement a run would send, for
// the editor to show it; cached until the text or the caret moves.
func (q *Tab) currentStatement() (int, int, bool) {
	if q.Editor.SelStart != q.Editor.SelEnd {
		return 0, 0, false
	}
	key := [2]int{len(q.Editor.Text), q.Editor.SelEnd}
	if q.stmtText != q.Editor.Text || q.stmtKey != key {
		q.stmtText, q.stmtKey = q.Editor.Text, key
		st, ok := sqltext.StatementAtWith(q.Editor.Text, q.Editor.SelEnd, q.Editor.Dialect, SplitOptions(q.a.Settings()))
		q.stmtRange = [2]int{st.Start, st.End}
		q.stmtOK = ok
		if ok {
			q.Editor.CurrentLines = [2]int{editor.LineOf(q.Editor.Text, st.Start), editor.LineOf(q.Editor.Text, max(st.Start, st.End-1))}
		}
	}
	return q.stmtRange[0], q.stmtRange[1], q.stmtOK
}

// View builds the tab.
func (q *Tab) View(c *ui.Context) {
	a := q.a
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	// Shortcuts of the editor: by default, those of DataGrip and DBeaver.
	if q.pressed(c, keymap.Run) {
		q.Run(RunStatement)
	}
	if q.pressed(c, keymap.RunInNewTab) {
		q.Run(RunNewTab)
	}
	if q.pressed(c, keymap.RunScript) {
		q.Run(RunScript)
	}
	if q.pressed(c, keymap.ToggleResults) {
		// Between the editor and its results, without the pointer.
		if q.Editor.HasFocus && q.current() != nil {
			*a.FocusWant() = "results"
		} else {
			*a.FocusWant() = "editor"
		}
	}
	if *a.FocusWant() == "editor" && a.KeysTo(q) {
		q.Editor.WantFocus = true
		*a.FocusWant() = ""
	}
	if q.pressed(c, keymap.Format) {
		q.format()
	}
	if q.pressed(c, keymap.GoToStatement) {
		q.openOutline()
	}
	if q.Editor.HasFocus {
		switch {
		case q.pressed(c, keymap.QuickFix):
			q.fixProblem()
		case q.pressed(c, keymap.GoToDefinition):
			q.goToDefinition()
		case q.pressed(c, keymap.FindUsages):
			q.findUsages()
		case q.pressed(c, keymap.Rename):
			q.askRename()
		}
	}
	if q.pressed(c, keymap.ExplainAnalyze) {
		q.Run(RunExplainAnalyze)
	}
	if q.pressed(c, keymap.Explain) {
		q.Run(RunExplain)
	}
	if q.Running && q.key(c, 0, ui.KeyEscape) && q.cancel != nil {
		q.cancel()
	}
	if q.pressed(c, keymap.Save) {
		q.save()
	}
	// What is typed goes to the file once typing stops for a second.
	if q.Path != "" && q.DiskConflict == "" && q.Editor.Text != q.Saved {
		if q.Editor.Text != q.Typed {
			q.Typed, q.typedAt = q.Editor.Text, a.Now()
		}
		if wait := time.Second - a.Now().Sub(q.typedAt); wait > 0 {
			c.After(wait)
		} else {
			q.Flush(false)
		}
	}
	if q.pressed(c, keymap.SaveAs) {
		a.SaveSQLFile(q, true)
	}
	// Editors of a connection set to auto-connect open it as they are
	// shown; the others wait for Connect or a run.
	if q.Conn.Status == connection.StatusIdle && q.Conn.Config.AutoConnect {
		a.Connect(q.Conn, nil)
	}
	ui.Column(c).Grow(1).Children(func() {
		ui.Toolbar(c, func() {
			if q.Running {
				if widgets.ToolButton(c, widgets.IconStop, "Cancel", "Cancel (Esc)").Clicked() && q.cancel != nil {
					q.cancel()
				}
			} else if widgets.ToolButton(c, widgets.IconPlay, "Run", widgets.KeyLabel("Run statement (⌘↵)")).Clicked() {
				q.Run(RunStatement)
			}
			if widgets.ToolButton(c, widgets.IconLayers, "Run Script", widgets.KeyLabel("Run all statements (⌘⇧↵)")).Clicked() {
				q.Run(RunScript)
			}
			if widgets.ToolButton(c, widgets.IconCode, "Explain", widgets.KeyLabel("Explain plan (⌘E)")).Clicked() {
				q.Run(RunExplain)
			}
			if widgets.ToolButton(c, widgets.IconClock, "Analyze", widgets.KeyLabel("Run the statement to measure its plan (⌘⇧E)")).Clicked() {
				q.Run(RunExplainAnalyze)
			}
			if widgets.ToolButton(c, widgets.IconWand, "Format", widgets.KeyLabel("Format SQL (⌘⇧F)")).Clicked() {
				q.format()
			}
			if widgets.ToolButton(c, widgets.IconSave, "Save As", widgets.KeyLabel("Save a copy under another name (⌘⇧S)")).Clicked() {
				a.SaveSQLFile(q, true)
			}
			ui.Spacer(c)
			q.switcherView(c)
			if q.Running {
				ui.Spinner(c).Size(14, 14)
				ui.Text(c, widgets.FormatDuration(time.Since(q.StartedAt).Round(100*time.Millisecond))).FontSize(12).TextColor(pal.Muted)
				c.After(100 * time.Millisecond)
			}
			if q.Conn.Config.ManualCommit() {
				ui.Badge(c, "Manual commit").Tooltip("Writes open a transaction that you commit or roll back")
			} else if q.Conn.Config.Engine.IsSQL() && q.Conn.Config.Engine != db.ClickHouse {
				ui.Badge(c, "Auto-commit")
			}
		}).Label("Editor").Padding(4, 8).BorderWidth(0, 0, 1, 0).BorderColor(t.Border)
		// The bars come and go, as one while a header is typed: in a
		// column always there, the editor keeps its place, and the focus.
		ui.Column(c).Children(func() {
			if q.Tx != db.TxNone {
				q.txBar(c)
			}
			if q.DiskConflict != "" {
				q.conflictBar(c)
			}
			if id, exists := q.namesAnother(); id != "" {
				q.headerBar(c, id, exists)
			}
			if q.Conn.Status == connection.StatusFailed || q.Conn.Status == connection.StatusIdle {
				q.connectBar(c)
			}
		})
		ui.SplitVertical(c, &q.editorH, func() {
			ui.Column(c).Fill().Children(func() {
				q.findView(c, a.Settings().EditorFont)
				_, _, ok := q.currentStatement()
				q.Editor.HasCurrent = ok && q.Editor.HasFocus
				q.checkProblems()
				q.Editor.View(c, a.Settings().EditorFont).ContextMenu(q.editorMenu)
				q.trackSnippet()
				q.completionView(c, a)
				q.problemBar(c)
			})
		}, func() {
			q.resultsView(c, a)
		}).Grow(1)
	})
}

func (q *Tab) connectBar(c *ui.Context) {
	t := c.Theme()
	failed := q.Conn.Status == connection.StatusFailed
	row := ui.Row(c).Padding(6, 12).Gap(10)
	if failed {
		row.Background(t.Danger.Alpha(0.14))
	} else {
		row.Background(t.Surface).BorderWidth(0, 0, 1, 0).BorderColor(t.Border)
	}
	row.Children(func() {
		label, button := "Not connected.", "Connect"
		if failed {
			ui.Icon(c, widgets.IconAlert).TextColor(t.Danger).FontSize(14)
			label, button = "Not connected: "+widgets.FirstLine(q.Conn.Err), "Retry"
		}
		ui.Text(c, label).Grow(1).Shrink(1).SingleLine()
		if ui.Button(c, button).Clicked() {
			q.a.Connect(q.Conn, nil)
		}
	})
}

func (q *Tab) txBar(c *ui.Context) {
	t := c.Theme()
	label := "Transaction open: changes are not visible to others until you commit."
	col := t.Warning
	if q.Tx == db.TxFailed {
		label = "The transaction failed: roll it back to go on."
		col = t.Danger
	}
	ui.Row(c).Padding(6, 12).Gap(10).Background(col.Alpha(0.16)).BorderWidth(0, 0, 1, 0).BorderColor(col).Children(func() {
		ui.Icon(c, widgets.IconAlert).TextColor(col).FontSize(14)
		ui.Text(c, label).Grow(1).Shrink(1)
		if q.Tx == db.TxOpen && ui.PrimaryButton(c, "Commit").Disabled(q.Busy()).Clicked() {
			q.endTx(true)
		}
		if ui.Button(c, "Roll Back").Disabled(q.Busy()).Clicked() {
			q.endTx(false)
		}
	})
}

func (q *Tab) resultsView(c *ui.Context, a Host) {
	t := c.Theme()
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(4, 8).Gap(4).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Row(c).Gap(2).Grow(1).Shrink(1).ClipX().Children(func() {
				for i, r := range q.results {
					if !r.rowsMode && r.err == "" {
						continue
					}
					label := fmt.Sprintf("Result %d", i+1)
					switch {
					case r.err != "" && r.view == nil:
						label = "Error"
					case r.err != "":
						label += " ⚠"
					}
					if r.pinned {
						label += " (pinned)"
					}
					p := widgets.Pill(c, label, q.resultIdx == i)
					if p.Clicked() {
						q.resultIdx = i
					}
					p.ContextMenu(func(m *ui.Menu) {
						title := "Pin"
						if r.pinned {
							title = "Unpin"
						}
						if m.Item(title).Chosen() {
							r.pinned = !r.pinned
						}
						if m.Item("Close Result").Disabled(q.Running || r.view != nil && (r.view.Applying() || r.view.Counting())).Chosen() {
							q.a.Post(func() { q.settlePending([]*result{r}, func() { q.closeResult(r) }) })
						}
					})
				}
				label := "Messages"
				if n := len(q.messages); n > 0 {
					label = fmt.Sprintf("Messages (%d)", n)
				}
				if widgets.Pill(c, label, q.resultIdx >= len(q.results)).Clicked() {
					q.resultIdx = len(q.results)
				}
			})
		})
		r := q.current()
		switch {
		case r == nil:
			q.messagesView(c)
		case r.view == nil:
			q.errorPanel(c, r)
		case r.plan != nil:
			q.planView(c, r)
		default:
			r.view.View(c)
		}
	})
}

func (q *Tab) current() *result {
	if q.resultIdx >= 0 && q.resultIdx < len(q.results) && (q.results[q.resultIdx].rowsMode || q.results[q.resultIdx].err != "") {
		return q.results[q.resultIdx]
	}
	return nil
}

// newView shows a result's rows from the first page the run read. A
// filter, a count or an order wraps the statement, run again on the
// editor's session with its parameters; the rows edit when they are one
// table's.
func (q *Tab) newView(res *result, c *db.Cursor, rows [][]any, done bool, stop context.CancelFunc) *dataview.Viewer {
	s := res.stmt
	why := res.tableWhy
	switch {
	case why != "":
	case res.schemaErr != nil:
		why = "Could not read the editor's schema: " + res.schemaErr.Error()
	default:
		why = "Reading the table's columns…"
	}
	v := dataview.NewViewer(q.a, dataview.ViewerSource{
		Keys:           func() bool { return q.a.KeysTo(q) },
		Conn:           q.Conn,
		Database:       q.Database,
		Statement:      s.SQL,
		Args:           s.Args,
		Wrap:           true,
		Reads:          s.Analysis.Class == sqltext.Read,
		ReadOnly:       why,
		CountOnSession: true,
		CursorOpen:     q.cursorOpen,
		Session:        func() *db.Session { return q.sess },
		AdoptSession:   q.adoptSession,
		SessionBusy:    func() string { return q.sessionBusy(res) },
		TxChanged: func(tx db.TxState) {
			q.Tx = tx
			q.txs.Set(tx, q.a.Now())
		},
		Rerun: func() { q.rerun(s) },
	})
	v.Adopt(c, rows, done, res.elapsed, stop)
	if res.tableWhy == "" && res.schemaErr == nil {
		schema := res.table.Schema
		if schema == "" {
			schema = res.schema
		}
		q.bindTable(v, res.table, schema)
	}
	return v
}

// bindTable finds, off the main thread, the table a result's rows are
// read from, with its columns and foreign keys, for the rows to edit as
// its own. An unqualified name is in schema, where the editor's session
// found it.
func (q *Tab) bindTable(v *dataview.Viewer, ref sqltext.TableRef, schema string) {
	cn, database, engine := q.Conn, q.Database, q.Conn.Config.Engine
	if ref.Schema != "" {
		if i := matchName(cn.Schemas[database], ref.Schema, ref.SchemaQuoted, engine); i >= 0 {
			schema = cn.Schemas[database][i]
		}
	}
	cached := cn.Objects[connection.SchemaKey{Database: database, Schema: schema}]
	poolOf := cn.PoolFor(database) // read on the main thread
	q.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		obj, found := findObject(cached, ref.Name, ref.Quoted, engine)
		d, err := poolOf(ctx)
		if err == nil && !found {
			// The schema's list may be older than the table.
			var objs []db.Object
			if objs, err = d.Dialect.Objects(ctx, d.Catalog(), schema); err == nil {
				obj, found = findObject(objs, ref.Name, ref.Quoted, engine)
			}
		}
		var cols []db.Column
		var fks []db.ForeignKey
		if err == nil && found {
			if obj.Schema == "" {
				obj.Schema = schema
			}
			if cols, err = d.Dialect.Columns(ctx, d.Catalog(), obj.Schema, obj.Name); err == nil {
				fks, _ = d.Dialect.ForeignKeys(ctx, d.Catalog(), obj.Schema, obj.Name)
			}
		}
		return func() {
			switch {
			case err != nil:
				v.SetReadOnly("Could not read the table's columns: " + err.Error())
			case !found:
				v.SetReadOnly("The table " + ref.Name + " is not among the tables of " + schema + ".")
			default:
				cn.Columns[connection.ObjectKey{Database: database, Schema: obj.Schema, Name: obj.Name}] = cols
				v.BindTable(obj, cols, fks)
			}
		}
	})
}

// findObject finds a table by its name, as matchName does.
func findObject(objs []db.Object, name string, quoted bool, engine db.Engine) (db.Object, bool) {
	names := make([]string, len(objs))
	for i, o := range objs {
		names[i] = o.Name
	}
	if i := matchName(names, name, quoted, engine); i >= 0 {
		return objs[i], true
	}
	return db.Object{}, false
}

// matchName finds a name among names as the engine folds it, -1 when
// none: PostgreSQL folds an unquoted name to lower case, ClickHouse folds
// none, and the others find names whatever the case, the exact one first.
func matchName(names []string, name string, quoted bool, engine db.Engine) int {
	switch {
	case engine == db.Postgres && !quoted:
		return slices.Index(names, strings.ToLower(name))
	case engine == db.Postgres || engine == db.ClickHouse:
		return slices.Index(names, name)
	}
	if i := slices.Index(names, name); i >= 0 {
		return i
	}
	found := -1
	for i, n := range names {
		if strings.EqualFold(n, name) {
			if found >= 0 {
				return -1
			}
			found = i
		}
	}
	return found
}

// rerun runs a result's statement again, for a refresh of a statement
// that writes: always once confirmed, as DBeaver does, since it writes
// again.
func (q *Tab) rerun(s safety.Statement) {
	if q.Running || q.refuseWhileResultsWork() {
		return
	}
	q.settlePending(q.replaced(RunStatement), func() {
		cfg := &q.Conn.Config
		s.Start = -1 // the editor's text may have moved since the run
		v := safety.ReviewSQL(cfg, []safety.Statement{s})
		if v.Blocked != "" {
			q.a.RecordBlocked(q.Conn, v.Blocked, s.SQL)
			q.note(v.Blocked, "", true)
			q.a.ShowError("Not allowed", v.Blocked)
			return
		}
		v.Reasons = append(v.Reasons, "Refreshing runs the statement again: "+verbLabel(s.Analysis.Verb)+" changes the database again.")
		preview := s.SQL + ";"
		if s.Shown != "" {
			preview += "\n-- " + s.Shown
		}
		title := fmt.Sprintf("Run the statement again on %s?", cfg.Name)
		q.a.AskConfirm(q.Conn, v, title, "Run", preview, func() { q.execute([]safety.Statement{s}, v.Writes) })
	})
}

func (q *Tab) messagesView(c *ui.Context) {
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(8, 12).Gap(8).Children(func() {
			if len(q.messages) == 0 {
				ui.Text(c, widgets.KeyLabel("Run a statement with ⌘↵, or the whole script with ⌘⇧↵.")).TextColor(pal.Muted)
			}
			for i := len(q.messages) - 1; i >= 0; i-- {
				m := q.messages[i]
				ui.Column(c).Gap(2).Children(func() {
					ui.Row(c).Gap(8).Children(func() {
						ui.Text(c, m.at.Format("15:04:05")).FontSize(11).TextColor(pal.Muted).Font(widgets.MonoFont)
						txt := ui.Text(c, m.text).Selectable().Grow(1).Shrink(1)
						if m.err {
							txt.TextColor(t.Danger)
						}
					})
					if m.sql != "" {
						ui.Text(c, widgets.OneLine(m.sql, 160)).Font(widgets.MonoFont).FontSize(11).TextColor(pal.Muted).SingleLine()
					}
				})
			}
		})
	})
}

// refreshAfterDDL forgets the schema a statement may have changed.
func refreshAfterDDL(cn *connection.Conn, stmts []safety.Statement) {
	for _, s := range stmts {
		if s.Analysis.Class == sqltext.DDL {
			cn.ForgetCatalog()
			return
		}
	}
}

// Flush writes the editor's text to its file. Unless force is set, it
// first checks that the file is still as the editor last read or wrote
// it: a file changed by someone else, as by a git pull, is not
// overwritten, and the editor shows why.
func (q *Tab) Flush(force bool) {
	if q.Path == "" || q.Editor.Text == q.Saved || q.DiskConflict != "" && !force {
		return
	}
	if !force {
		disk, err := os.ReadFile(q.Path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			q.DiskConflict = "was deleted on disk"
			return
		case err != nil:
			q.DiskConflict = "could not be read: " + err.Error()
			return
		case string(disk) != q.Saved:
			q.DiskConflict = "changed on disk"
			return
		}
	}
	if err := os.WriteFile(q.Path, []byte(q.Editor.Text), 0o644); err != nil {
		q.DiskConflict = "could not be saved: " + err.Error()
		return
	}
	q.Saved, q.DiskConflict = q.Editor.Text, ""
}

// conflictBar says why the file is not saved, and offers the ways out.
func (q *Tab) conflictBar(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).Padding(6, 12).Gap(10).Background(t.Warning.Alpha(0.16)).Children(func() {
		ui.Icon(c, widgets.IconAlert).TextColor(t.Warning).FontSize(14)
		ui.Text(c, filepath.Base(q.Path)+" "+q.DiskConflict+": the editor's text is not saved.").Grow(1).Shrink(1).SingleLine().Tooltip(q.Path)
		if disk, err := os.ReadFile(q.Path); err == nil {
			if ui.Button(c, "Use the File").Tooltip("Replace the editor's text with the file's").Clicked() {
				q.Editor.Replace(0, len([]rune(q.Editor.Text)), string(disk))
				q.Saved, q.Typed, q.DiskConflict = string(disk), string(disk), ""
			}
		}
		if ui.Button(c, "Keep Mine").Tooltip("Write the editor's text over the file").Clicked() {
			q.Flush(true)
		}
	})
}

// headerBar says that the file names another connection than the
// editor's, so it runs nothing, and offers the way out.
func (q *Tab) headerBar(c *ui.Context, id string, exists bool) {
	t := c.Theme()
	col := t.Warning
	msg := fmt.Sprintf("This file names the connection %s, not this editor's %s: it runs nothing here.", id, q.Conn.Config.Name)
	if !exists {
		col = t.Danger
		msg = fmt.Sprintf("This file names the connection %s, which is not a SQL connection of %s: fix its first lines to run it.", id, q.Conn.Project.Name)
	}
	ui.Row(c).Padding(6, 12).Gap(10).Background(col.Alpha(0.16)).Children(func() {
		ui.Icon(c, widgets.IconAlert).TextColor(col).FontSize(14)
		ui.Text(c, msg).Grow(1).Shrink(1)
		if exists && ui.Button(c, "Switch to "+id).Disabled(q.Busy()).Clicked() {
			q.a.SwitchConnection(q, id)
		}
	})
}

// errorOffset is where in the editor an error points: from the
// statement's start, by the character position the database gave, else
// by its line and column, else by the line and the text it quoted.
// It returns -1 when the error says nothing of where.
func errorOffset(stmt string, start, skip int, info db.ErrorInfo) int {
	if start < 0 {
		return -1
	}
	rs := []rune(stmt)[min(skip, len([]rune(stmt))):]
	switch {
	case info.Position > 0:
		return start + min(max(info.Position-1-skip, 0), len(rs))
	case info.Line > 0:
		line := 1
		i := 0
		for ; i < len(rs) && line < info.Line; i++ {
			if rs[i] == '\n' {
				line++
			}
		}
		if line < info.Line {
			return -1
		}
		switch {
		case info.Column > 0 && info.Line == 1:
			i += max(info.Column-1-skip, 0)
		case info.Column > 0:
			i += info.Column - 1
		case info.Near != "":
			if at := strings.Index(string(rs[i:]), info.Near); at >= 0 {
				i += len([]rune(string(rs[i:])[:at]))
			}
		}
		return start + min(i, len(rs))
	}
	return -1
}

// goToError puts the caret where the error points, and selects the word
// there.
func (q *Tab) goToError(r *result) {
	at := errorOffset(r.sql, r.start, r.skip, r.errInfo)
	if at < 0 {
		return
	}
	rs := []rune(q.Editor.Text)
	end := at
	for end < len(rs) && (unicode.IsLetter(rs[end]) || unicode.IsDigit(rs[end]) || rs[end] == '_') {
		end++
	}
	if end == at && end < len(rs) {
		end++
	}
	q.Editor.PendingSel = &[2]int{at, end}
	q.Editor.WantFocus = true
}

// errorPanel shows what the database said of a failed statement.
func (q *Tab) errorPanel(c *ui.Context, r *result) {
	t := c.Theme()
	pal := widgets.PaletteOf(c)
	info := r.errInfo
	if info.Message == "" {
		info.Message = r.err
	}
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(16, 20).Gap(12).MaxWidth(900).Children(func() {
			ui.Row(c).Gap(10).Children(func() {
				ui.Icon(c, widgets.IconAlert).TextColor(t.Danger).FontSize(18)
				ui.Text(c, info.Message).FontSize(15).Bold().Selectable().Grow(1).Shrink(1)
			})
			field := func(label, value string) {
				if value == "" {
					return
				}
				ui.Row(c).Gap(12).Children(func() {
					ui.Text(c, label).FontSize(12).TextColor(pal.Muted).Width(70)
					v := ui.Text(c, value).FontSize(13).Selectable().Grow(1).Shrink(1)
					// A detail that points with a caret needs its columns.
					if strings.Contains(value, "\n") {
						v.Font(widgets.MonoFont).FontSize(12)
					}
				})
			}
			field("Code", info.Code)
			field("Detail", info.Detail)
			field("Hint", info.Hint)
			where := ""
			if at := errorOffset(r.sql, r.start, r.skip, info); at >= 0 {
				line, col := lineCol(q.Editor.Text, at)
				where = fmt.Sprintf("line %d, column %d", line, col)
			}
			field("Where", where)
			ui.Text(c, r.sql).Font(widgets.MonoFont).FontSize(12).Padding(10).Radius(6).Background(pal.EditorBg).Selectable()
			ui.Row(c).Gap(8).Children(func() {
				if where != "" && ui.PrimaryButton(c, "Go to Error").Clicked() {
					q.goToError(r)
				}
				if ui.Button(c, "Copy Error").Clicked() {
					text := info.Message
					for _, f := range [][2]string{{"Code", info.Code}, {"Detail", info.Detail}, {"Hint", info.Hint}, {"Where", where}} {
						if f[1] != "" {
							text += "\n" + f[0] + ": " + f[1]
						}
					}
					q.a.WriteClipboard(text + "\n\n" + r.sql)
				}
			})
		})
	})
}

// lineCol is the 1-based line and column of a rune offset.
func lineCol(text string, at int) (int, int) {
	line, col := 1, 1
	for i, r := range []rune(text) {
		if i >= at {
			break
		}
		if r == '\n' {
			line, col = line+1, 1
		} else {
			col++
		}
	}
	return line, col
}

// closeResult drops a result, closing its cursor.
func (q *Tab) closeResult(r *result) {
	i := slices.Index(q.results, r)
	if i < 0 {
		return
	}
	q.results = slices.Delete(q.results, i, i+1)
	if r.view != nil {
		go r.view.Release()()
	}
	if q.resultIdx > i || q.resultIdx >= len(q.results) {
		q.resultIdx = max(0, q.resultIdx-1)
	}
}

// SplitOptions is how the user's settings split statements.
func SplitOptions(s *settings.Settings) sqltext.SplitOptions {
	if s.SemicolonOnly {
		return sqltext.SplitOptions{Mode: sqltext.SemicolonOnly}
	}
	return sqltext.SplitOptions{Mode: sqltext.BlankLineAndSemicolon}
}

// askStep asks, on the main thread, whether to run a statement of a
// script on production, and waits for the answer: "run", "skip" or
// "cancel". Run All answers for the plain writes left; a destructive
// statement asks still.
func (q *Tab) askStep(ctx context.Context, s safety.Statement, i, n int, runAll *bool) string {
	cfg := q.Conn.Config
	v := safety.ReviewSQL(&cfg, []safety.Statement{s})
	if !v.Confirm || *runAll && !v.TypeName {
		return "run"
	}
	answer := make(chan string, 1)
	q.a.Post(func() {
		title := fmt.Sprintf("Run statement %d of %d on %s?", i+1, n, cfg.Name)
		r := q.a.AskConfirm(q.Conn, v, title, "Run", s.SQL, func() { answer <- "run" })
		r.OnCancel = func() { answer <- "cancel" }
		r.OnSkip = func() {
			q.a.Record(&cfg, audit.Event{Kind: audit.KindConfirm, Statement: s.SQL, Detail: title + " — skipped"})
			answer <- "skip"
		}
		if !v.TypeName {
			r.OnRunAll = func() {
				q.a.Record(&cfg, audit.Event{Kind: audit.KindConfirm, Statement: s.SQL, Detail: title + " — run all the script's writes"})
				*runAll = true
				answer <- "run"
			}
		}
	})
	select {
	case a := <-answer:
		return a
	case <-ctx.Done():
		return "cancel"
	}
}

// target is the range the editor's menu acts on: the selection, else the
// statement at the caret.
func (q *Tab) target() (int, int, bool) {
	if q.Editor.SelStart != q.Editor.SelEnd {
		return min(q.Editor.SelStart, q.Editor.SelEnd), max(q.Editor.SelStart, q.Editor.SelEnd), true
	}
	return q.currentStatement()
}

// changeCase upper- or lower-cases the selection, or the statement at
// the caret.
func (q *Tab) changeCase(upper bool) {
	if start, end, ok := q.target(); ok {
		q.Editor.Text = sqltext.ChangeCase(q.Editor.Text, start, end, q.Editor.Dialect, upper)
		q.Editor.PendingSel = &[2]int{start, end}
	}
}

// toggleComment comments out, or in, the lines of the selection or the
// caret.
func (q *Tab) toggleComment() {
	s, e := min(q.Editor.SelStart, q.Editor.SelEnd), max(q.Editor.SelStart, q.Editor.SelEnd)
	text, from, to := sqltext.ToggleComment(q.Editor.Text, s, e)
	q.Editor.Text = text
	q.Editor.PendingSel = &[2]int{from, to}
}

// editorMenu is the editor's context menu: running, editing, formatting.
func (q *Tab) editorMenu(m *ui.Menu) {
	m.Submenu("Execute", func(m *ui.Menu) {
		if keymap.Item(m.Item("Execute Statement"), keymap.Run).Chosen() {
			q.Run(RunStatement)
		}
		if keymap.Item(m.Item("Execute in New Tab"), keymap.RunInNewTab).Chosen() {
			q.Run(RunNewTab)
		}
		if keymap.Item(m.Item("Execute Script"), keymap.RunScript).Chosen() {
			q.Run(RunScript)
		}
		if keymap.Item(m.Item("Explain Plan"), keymap.Explain).Chosen() {
			q.Run(RunExplain)
		}
		if keymap.Item(m.Item("Explain Analyze"), keymap.ExplainAnalyze).Chosen() {
			q.Run(RunExplainAnalyze)
		}
		m.Separator()
		if m.Item("Export From Query…").Chosen() {
			q.exportFromQuery()
		}
	})
	m.Separator()
	m.EditItems()
	m.Separator()
	if keymap.Item(m.Item("Find…"), keymap.Find).Chosen() {
		q.find.Open, q.find.Replacing, q.find.Shown = true, false, -1
	}
	if keymap.Item(m.Item("Replace…"), keymap.Replace).Chosen() {
		q.find.Open, q.find.Replacing, q.find.Shown = true, true, -1
	}
	if keymap.Item(m.Item("Go to Statement…"), keymap.GoToStatement).Chosen() {
		q.openOutline()
	}
	if keymap.Item(m.Item("Go to Definition"), keymap.GoToDefinition).Chosen() {
		q.goToDefinition()
	}
	if keymap.Item(m.Item("Find Usages"), keymap.FindUsages).Chosen() {
		q.findUsages()
	}
	if keymap.Item(m.Item("Rename in File…"), keymap.Rename).Chosen() {
		q.askRename()
	}
	m.Separator()
	m.Submenu("Format", func(m *ui.Menu) {
		if keymap.Item(m.Item("Format SQL"), keymap.Format).Chosen() {
			q.format()
		}
		_, _, ok := q.target()
		if m.Item("To Upper Case").Disabled(!ok).Chosen() {
			q.changeCase(true)
		}
		if m.Item("To Lower Case").Disabled(!ok).Chosen() {
			q.changeCase(false)
		}
		if m.Item("Toggle Comment").Chosen() {
			q.toggleComment()
		}
	})
	start, end, ok := q.target()
	if m.Item("Copy Statement").Disabled(!ok).Chosen() {
		q.a.WriteClipboard(string([]rune(q.Editor.Text)[start:end]))
	}
	if m.Item("Save as Snippet…").Disabled(!ok).Chosen() {
		q.a.AskSnippet(q)
	}
}

// lendSession lends the editor's session to an export of its statement,
// for it to see the editor's schema, temporary tables and transaction:
// once nothing runs there and no result reads rows from it. Without a
// session yet, the export opens its own, which knows as much.
func (q *Tab) lendSession() (sess *db.Session, done func(), why string) {
	if why = q.cursorOpen(); why == "" {
		why = q.sessionBusy(nil)
	}
	if sess = q.sess; why != "" || sess == nil {
		return nil, nil, why
	}
	q.exports++
	return sess, func() {
		tx := sess.Tx()
		q.a.Post(func() {
			q.exports--
			if !q.closed {
				q.Tx = tx
				q.txs.Set(tx, q.a.Now())
			}
		})
	}, ""
}

// exportFromQuery exports the rows of the statement at the caret, or the
// one selected, without showing them first; its parameters are asked as
// a run asks them.
func (q *Tab) exportFromQuery() {
	if q.refuseAnotherConnection() {
		return
	}
	stmts, _ := q.statements(RunStatement)
	if len(stmts) != 1 {
		q.a.ShowError("Which statement?", "Put the caret in one statement, or select only one, to export its rows.")
		return
	}
	stmt := stmts[0]
	open := func(values map[string]params.Input) {
		sql, args, _, err := params.Bind(stmt, q.Conn.Config.Engine, q.Editor.Dialect, values)
		if err != nil {
			q.a.ShowError("Could not bind the parameters", err.Error())
			return
		}
		dataview.OpenExport(q.a, dataview.ExportSource{Conn: q.Conn, Database: q.Database, Name: "query", SQL: sql, Args: args, OnSession: q.lendSession})
	}
	keys := params.Keys(stmts, q.Editor.Dialect)
	if len(keys) == 0 {
		open(nil)
		return
	}
	f := &paramForm{open: true, run: func(values map[string]params.Input) {
		if q.paramValues == nil {
			q.paramValues = map[string]params.Input{}
		}
		maps.Copy(q.paramValues, values)
		open(values)
	}}
	for _, k := range keys {
		f.fields = append(f.fields, paramField{key: k, value: q.paramValues[k].Value, kind: max(slices.Index(params.Kinds, q.paramValues[k].Kind), 0)})
	}
	q.a.QueryDialogs().params = f
}
