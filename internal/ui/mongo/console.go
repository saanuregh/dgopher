package mongo

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/mongosh"
	"dgopher/internal/redact"
	"dgopher/internal/safety"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/editor"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// runTimeout bounds a statement of the console, and a page of its
// documents.
var runTimeout = 5 * time.Minute

// countWait bounds the count of the documents a change to many asks
// with.
const countWait = 5 * time.Second

// purposeCount is why the console counts before it asks.
const purposeCount = "to count the documents before it asks"

// ConsoleTab runs MongoDB shell statements on a database of a
// connection, each through the safety policy, in autocommit.
type ConsoleTab struct {
	a    Host
	conn *connection.Conn
	// Database is where statements run: the tab's, as use and the
	// switcher change it.
	Database string
	Editor   *editor.Editor
	// OpenCollection, when set, opens a collection's tab, as Open in
	// does with a find's collection.
	OpenCollection func(database, coll string)

	results []*consoleResult
	// shown is the result shown, len(results) for the output.
	shown  int
	output []outputLine

	running  bool
	runStart time.Time
	cancel   context.CancelFunc
	runGen   int // counts the runs, so that what an older one posts is dropped

	// collections are Database's, for completion, read for collsOf.
	collections []string
	collsOf     string
	databases   []string
	editorPct   float32
	// The statements of linesText, and the lines they start on, for the
	// ▶ before each.
	linesText string
	stmts     []statement
	runLines  []int
	// resultBarW is the width of the results' bar as last drawn.
	resultBarW float32
}

// outputLine is a line of the console's output: what ran, and errors.
type outputLine struct {
	text string
	err  bool
}

// consoleResult is the result of a statement.
type consoleResult struct {
	op       mongosh.Op
	database string
	elapsed  time.Duration
	// The documents of a cursor, read a page at a time.
	docs    []bson.Raw
	cursor  *db.DocCursor
	done    bool
	loading bool
	err     string
	// reply is a single reply, shown as a document.
	reply    bson.Raw
	affected int64
	view     *docView
	gen      int
}

// DatabaseFor is the database a MongoDB tab of cn opens on: database,
// else the connection's, else test, as mongosh's.
func DatabaseFor(cn *connection.Conn, database string) string {
	return cmp.Or(database, cn.Config.Database, "test")
}

// NewConsole opens a console on a database of cn, as DatabaseFor says.
func NewConsole(h Host, cn *connection.Conn, database string) *ConsoleTab {
	database = DatabaseFor(cn, database)
	t := &ConsoleTab{a: h, conn: cn, Database: database, editorPct: 45}
	t.Editor = editor.New("", sqltext.Generic)
	t.Editor.State.Label = "MongoDB console"
	t.Editor.State.OnCompletion = t.complete
	t.Editor.WantFocus = true
	t.readNames()
	return t
}

func (t *ConsoleTab) Title() string { return t.conn.Config.Name + " · console" }

func (t *ConsoleTab) Connection() *connection.Conn { return t.conn }

func (t *ConsoleTab) Icon() *ui.SVG { return widgets.IconTerminal }

func (t *ConsoleTab) Places() []connection.Place {
	return []connection.Place{t.conn.PlaceOf(t.Database)}
}

// CloseReason says what closing stops: statements running.
func (t *ConsoleTab) CloseReason() string {
	if t.running {
		return t.Title() + " is running a statement: closing stops it."
	}
	return ""
}

// Mark is empty: a statement running is work passing.
func (t *ConsoleTab) Mark() string { return "" }

func (t *ConsoleTab) Busy() bool { return t.running }

// HoldsConnection reports a statement running or a cursor open.
func (t *ConsoleTab) HoldsConnection(cn *connection.Conn) bool {
	if cn != t.conn {
		return false
	}
	for _, r := range t.results {
		if r.cursor != nil && !r.done {
			return true
		}
	}
	return t.running
}

func (t *ConsoleTab) Close() {
	t.Stop()
	t.closeResults()
}

// Stop ends the statement running, and the script after it.
func (t *ConsoleTab) Stop() {
	t.runGen++
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	if t.running {
		t.running = false
		t.log("Stopped.", true)
	}
}

func (t *ConsoleTab) closeResults() {
	for _, r := range t.results {
		if cur := r.cursor; cur != nil {
			r.cursor = nil
			go cur.Close(context.Background())
		}
	}
}

func (t *ConsoleTab) log(text string, isErr bool) {
	t.output = append(t.output, outputLine{text: text, err: isErr})
}

// statement is a statement of the editor's text and where it starts.
type statement struct {
	text string
	from int
}

// Run runs the statement at the caret, or those selected.
func (t *ConsoleTab) Run() {
	text := t.Editor.Text()
	start, end := t.Editor.Selection()
	if start > end {
		start, end = end, start
	}
	if start != end {
		t.run(statementsOf(text[start:end], start))
		return
	}
	// The statement the caret is in or after, else the first.
	all := statementsOf(text, 0)
	if len(all) == 0 {
		t.run(nil)
		return
	}
	pick := 0
	for i, s := range all {
		if s.from <= start {
			pick = i
		}
	}
	t.run(all[pick : pick+1])
}

// RunScript runs every statement of the editor, in order.
func (t *ConsoleTab) RunScript() { t.run(statementsOf(t.Editor.Text(), 0)) }

// statementsOf splits a script whose first byte is at offset from.
func statementsOf(script string, from int) []statement {
	var out []statement
	for _, s := range mongosh.Split(script) {
		out = append(out, statement{text: script[s.From:s.To], from: from + s.From})
	}
	return out
}

func (t *ConsoleTab) run(stmts []statement) {
	if t.running {
		return
	}
	if len(stmts) == 0 {
		t.log("Nothing to run: write a statement, as db.orders.find().", true)
		t.shown = len(t.results)
		return
	}
	if t.conn.Doc() == nil {
		t.a.Connect(t.conn, func() { t.run(stmts) })
		return
	}
	t.closeResults()
	t.results, t.output, t.shown = nil, nil, 0
	t.runGen++
	t.running, t.runStart = true, time.Now()
	t.step(t.runGen, stmts, 0)
}

// step runs statement i of a run, then those after it; a statement that
// does not parse, is refused or fails ends the run.
func (t *ConsoleTab) step(gen int, stmts []statement, i int) {
	if gen != t.runGen {
		return
	}
	if i >= len(stmts) {
		t.finish()
		return
	}
	s := stmts[i]
	op, err := mongosh.Parse(s.text)
	if err != nil {
		line, _ := t.Editor.LineCol(s.from)
		t.fail(fmt.Sprintf("Statement %d, from line %d: %s", i+1, line+1, err))
		return
	}
	if op.Database == "" {
		// Named, so that a question such as dropDatabase's says which
		// database it acts on, after use too.
		op.Database = t.Database
	}
	shown := redact.Mongo(op.Text)
	v := safety.ReviewMongo(&t.conn.Config, op)
	if v.Blocked != "" {
		t.a.RecordBlocked(t.conn, v.Blocked, shown)
		t.fail("Not allowed: " + v.Blocked)
		if i+1 < len(stmts) {
			t.log(fmt.Sprintf("The %s after it did not run.", widgets.Count(len(stmts)-i-1, "statement")), true)
		}
		return
	}
	next := func() { t.step(gen, stmts, i+1) }
	exec := func() { t.exec(gen, op, next) }
	if !v.Confirm {
		exec()
		return
	}
	ask := func(notes []string) {
		if gen != t.runGen {
			return
		}
		title := fmt.Sprintf("Run %s on %s?", mongosh.Classify(op).Command, t.conn.Config.Name)
		req := t.a.AskConfirm(t.conn, v, title, "Run", shown, exec)
		if req == nil {
			return
		}
		req.Notes = append(req.Notes, notes...)
		req.OnCancel = func() {
			if gen == t.runGen {
				t.running = false
				t.log("Cancelled: "+widgets.OneLine(shown, 80), true)
			}
		}
		if len(stmts) > 1 {
			req.OnSkip = func() {
				t.log("Skipped: "+widgets.OneLine(shown, 80), false)
				next()
			}
		}
	}
	if filter := mongosh.Classify(op).Filter; filter != nil {
		t.countThenAsk(op, filter, ask)
		return
	}
	ask(nil)
}

// countThenAsk counts, within countWait, the documents a change to many
// matches, for its question.
func (t *ConsoleTab) countThenAsk(op mongosh.Op, filter any, ask func(notes []string)) {
	d, cfg := t.conn.Doc(), t.conn.Config
	database := cmp.Or(op.Database, t.Database)
	t.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), countWait)
		defer cancel()
		start := time.Now()
		n, err := d.Count(ctx, database, op.Collection, filter)
		t.a.Record(&cfg, audit.Event{Kind: audit.KindMongo, Database: database, Statement: collectionRef(op.Collection) + ".countDocuments(" + shellOf(filter) + ")",
			Purpose: purposeCount, Rows: -1, DurationMS: time.Since(start).Milliseconds(), Err: err})
		note := fmt.Sprintf("It matches %s of %s, counted just now.", widgets.Count(n, "document"), op.Collection)
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			note = "The documents could not be counted in 5 seconds."
		case err != nil:
			note = "The documents could not be counted: " + strings.TrimSuffix(widgets.FirstLine(err.Error()), ".") + "."
		}
		return func() { ask([]string{note}) }
	})
}

// exec runs op, shows its result, and goes on with next.
func (t *ConsoleTab) exec(gen int, op mongosh.Op, next func()) {
	d, cfg := t.conn.Doc(), t.conn.Config
	if d == nil {
		t.fail(cfg.Name + " is not connected.")
		return
	}
	runOn := t.Database
	database := cmp.Or(op.Database, runOn)
	shown := redact.Mongo(op.Text)
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	t.cancel = cancel
	finish := t.a.StartJob(t.conn, "A statement of "+t.Title()+" is running: stopping ends it", cancel)
	dataview.BackgroundResetOnPanic(t.a, func() {
		if gen == t.runGen {
			t.running = false
		}
	}, func() func() {
		defer finish()
		defer cancel()
		start := time.Now()
		res, err := d.Run(ctx, runOn, op)
		var docs []bson.Raw
		done := true
		if err == nil && res.Cursor != nil {
			docs, done, err = res.Cursor.Fetch(ctx, pageSize)
		}
		took := time.Since(start)
		rows := res.Affected
		if res.Cursor != nil {
			rows = int64(len(docs))
		}
		// Recorded as written: the audit log hides its secrets, in the
		// error too, which it can only find before they are hidden.
		t.a.RecordTyped(cfg, audit.TypedRun{Kind: audit.KindMongo, Database: database, Sent: op.Text, Rows: rows, Took: took, Err: err, History: true})
		return func() {
			if gen != t.runGen {
				if res.Cursor != nil {
					go res.Cursor.Close(context.Background())
				}
				return
			}
			t.cancel = nil
			r := &consoleResult{op: op, database: database, elapsed: took, affected: res.Affected, view: newDocView()}
			if err != nil {
				if res.Cursor != nil {
					go res.Cursor.Close(context.Background())
				}
				t.fail(widgets.OneLine(shown, 80) + ": " + err.Error())
				return
			}
			r.reply, r.docs, r.done = res.Reply, docs, done
			if !done {
				r.cursor = res.Cursor
			}
			if op.Method == "use" && op.Collection == "" {
				if name := res.Reply.Lookup("database"); name.Type == bson.TypeString {
					t.Database = name.StringValue()
					t.readNames()
				}
			}
			t.results = append(t.results, r)
			t.shown = len(t.results) - 1
			t.log(fmt.Sprintf("%s · %s in %s", widgets.OneLine(shown, 80), r.summary(), widgets.FormatDuration(took)), false)
			next()
		}
	})
}

func (t *ConsoleTab) fail(text string) {
	t.running = false
	t.log(text, true)
	t.shown = len(t.results)
}

func (t *ConsoleTab) finish() {
	t.running = false
	if len(t.results) == 0 {
		t.shown = len(t.results)
	}
}

// summary says what a result holds.
func (r *consoleResult) summary() string {
	switch {
	case r.docs != nil || r.cursor != nil:
		n := widgets.Count(len(r.docs), "document")
		if !r.done {
			n += " loaded"
		}
		return n
	case r.affected >= 0:
		return widgets.Count(r.affected, "document") + " changed"
	case r.reply == nil:
		return "no document"
	}
	return "a reply"
}

// title names a result's pill: what ran, and how many documents.
func (r *consoleResult) title() string {
	what := r.op.Method
	if r.op.Collection != "" {
		what = r.op.Collection + "." + what
	}
	if (r.op.Method == "show" || r.op.Method == "use") && r.op.Collection == "" && len(r.op.Args) > 0 {
		what = r.op.Method + " " + fmt.Sprint(r.op.Args[0])
	}
	switch {
	case r.docs != nil || r.cursor != nil:
		return fmt.Sprintf("%s · %d", what, len(r.docs))
	case r.affected >= 0:
		return fmt.Sprintf("%s · %d", what, r.affected)
	}
	return what
}

// loadMore reads the next page of a result's documents, or every one
// left with all.
func (t *ConsoleTab) loadMore(r *consoleResult, all bool) {
	if r.loading || r.done || r.cursor == nil {
		return
	}
	r.loading = true
	cur := r.cursor
	dataview.BackgroundResetOnPanic(t.a, func() { r.loading = false }, func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
		defer cancel()
		start := time.Now()
		var docs []bson.Raw
		done := false
		var err error
		for {
			var page []bson.Raw
			page, done, err = cur.Fetch(ctx, pageSize)
			docs = append(docs, page...)
			if !all || done || err != nil || len(r.docs)+len(docs) >= db.MaxRows {
				break
			}
		}
		took := time.Since(start)
		return func() {
			r.loading = false
			if err != nil {
				r.err = err.Error()
				return
			}
			r.docs = append(r.docs, docs...)
			r.done, r.elapsed = done, r.elapsed+took
			r.gen++
			if done {
				r.cursor = nil
				go cur.Close(context.Background())
			}
		}
	})
}

// readNames reads the databases and the collections of the database, for
// completion and the switcher.
func (t *ConsoleTab) readNames() {
	d, database := t.conn.Doc(), t.Database
	if d == nil || t.collsOf == database {
		return
	}
	t.collsOf = database
	t.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		colls, err1 := d.Collections(ctx, database)
		dbs, err2 := d.Databases(ctx)
		return func() {
			if err1 == nil && t.collsOf == database {
				t.collections = t.collections[:0]
				for _, c := range colls {
					t.collections = append(t.collections, c.Name)
				}
			}
			if err2 == nil {
				t.databases = t.databases[:0]
				for _, d := range dbs {
					t.databases = append(t.databases, d.Name)
				}
			}
		}
	})
}

// SwitchDatabase makes statements run on database.
func (t *ConsoleTab) SwitchDatabase(database string) {
	if t.running {
		t.a.ShowError("Not switched", t.Title()+" is still running a statement.")
		return
	}
	t.Database = database
	t.readNames()
}
