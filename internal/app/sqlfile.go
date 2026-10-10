package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/redact"
	"dgopher/internal/safety"
	"dgopher/internal/sqlfile"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// sqlFileRun runs a file of SQL, as a database's dump, without an editor:
// it reads the file through once to say what it holds and ask what the
// safety policy asks, then again to run it, as it streams.
type sqlFileRun struct {
	open     bool
	conn     *connection.Conn
	database string
	path     string
	size     int64
	modTime  time.Time
	// restore is what a restore writes into, "" for a plain run.
	restore string

	// What the first reading found.
	reading    bool
	statements int
	verbs      map[string]int
	dangerous  []string // the first destructive statements, by line
	more       int      // destructive statements past those
	txControl  bool     // the file holds BEGIN, COMMIT and the like
	verdict    safety.Verdict
	hash       string

	stopOnError bool
	oneTx       bool

	running  bool
	progress atomic.Int64 // bytes of the file read
	done     atomic.Int64 // statements run
	failures []string
	finished bool
	cancel   context.CancelFunc
	err      string
}

// maxDangerous is how many destructive statements the summary names.
const maxDangerous = 20

// openSQLFileRun asks for a file of SQL to run on a connection's database.
func (a *App) openSQLFileRun(cn *connection.Conn, database string) {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "Run SQL File on " + cn.Config.Name,
			Filters: []mygo.FileFilter{{Name: "SQL files", Extensions: []string{"sql"}}, {Name: "All files", Extensions: []string{"*"}}}})
		if err != nil || len(paths) == 0 {
			return
		}
		a.Post(func() { a.Connect(cn, func() { a.startSQLFileRun(cn, database, paths[0], "") }) })
	}()
}

// startSQLFileRun reads a file of SQL through, then shows what it holds
// and asks to run it; restore, when set, is what a restore writes into,
// and the run is asked for as that restore is.
func (a *App) startSQLFileRun(cn *connection.Conn, database, path, restore string) {
	x := &sqlFileRun{open: true, conn: cn, database: database, path: path, restore: restore, stopOnError: true, reading: true, verbs: map[string]int{}}
	a.sqlFile = x
	ctx, cancel := context.WithCancel(context.Background())
	x.cancel = cancel
	cfg := cn.Config
	dataview.BackgroundResetOnPanic(a, func() { x.reading, x.cancel = false, nil }, func() func() {
		defer cancel()
		sum, err := surveySQLFile(ctx, &cfg, path, &x.progress)
		return func() {
			x.reading, x.cancel = false, nil
			if err != nil {
				x.err = err.Error()
				return
			}
			x.size, x.modTime, x.hash = sum.size, sum.modTime, sum.hash
			x.statements, x.verbs, x.dangerous, x.more, x.txControl, x.verdict = sum.statements, sum.verbs, sum.dangerous, sum.more, sum.txControl, sum.verdict
			// All or nothing where it can be: safer, and on SQLite, which
			// syncs each statement it commits, far faster.
			x.oneTx = cfg.Engine.TransactionalDDL() && !x.txControl
		}
	})
}

// fileSurvey is what a file of SQL holds, as the safety policy reads it.
type fileSurvey struct {
	size       int64
	modTime    time.Time
	hash       string
	statements int
	verbs      map[string]int
	dangerous  []string
	more       int
	txControl  bool
	verdict    safety.Verdict
}

// surveySQLFile reads a file of SQL through, reviewing each statement as
// the safety policy reviews an editor's, and counting what it holds.
func surveySQLFile(ctx context.Context, cfg *db.Config, path string, progress *atomic.Int64) (fileSurvey, error) {
	sum := fileSurvey{verbs: map[string]int{}}
	f, err := os.Open(path)
	if err != nil {
		return sum, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return sum, err
	}
	sum.size, sum.modTime = info.Size(), info.ModTime()
	h := sha256.New()
	r := sqlfile.NewReader(io.TeeReader(f, h), safety.Dialect(cfg.Engine))
	// begun is the line of the BEGIN of the transaction the file holds
	// open, 0 for none; commits counts the statements that commit it
	// implicitly, as MySQL's DDL does, which the prompt names up to
	// maxImplicitCommits of.
	begun, commits := 0, 0
	const maxImplicitCommits = 5
	for {
		st, err := r.Next()
		progress.Store(r.Read())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return sum, err
		}
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		an := safety.Analyze(cfg, []string{st.SQL})
		sum.statements++
		verb := an[0].Analysis.Verb
		if verb == "" {
			verb = "other"
		}
		sum.verbs[verb]++
		sum.txControl = sum.txControl || an[0].Analysis.Class == sqltext.Transaction
		if an[0].Analysis.Dangerous {
			if len(sum.dangerous) < maxDangerous {
				sum.dangerous = append(sum.dangerous, fmt.Sprintf("line %d: %s", st.Line, widgets.OneLine(redact.Secrets(st.SQL), 100)))
			} else {
				sum.more++
			}
		}
		sum.verdict.Add(safety.ReviewSQL(cfg, an))
		if sum.verdict.Blocked != "" {
			return sum, fmt.Errorf("line %d: %s", st.Line, sum.verdict.Blocked)
		}
		if begun > 0 && safety.CommitsImplicitly(cfg.Engine, an[0]) {
			sum.verdict.Confirm = true
			if commits++; commits <= maxImplicitCommits {
				sum.verdict.Reasons = append(sum.verdict.Reasons, fmt.Sprintf("line %d: %s commits the transaction begun at line %d: if the file fails later, what ran up to it stays", st.Line, verb, begun))
			}
			begun = 0
		}
		switch {
		case safety.BeginsTransaction(an[0]):
			begun = st.Line
		case safety.CommitsOrRollsBack(cfg.Engine, an[0]):
			begun = 0
		}
	}
	if commits > maxImplicitCommits {
		sum.verdict.Reasons = append(sum.verdict.Reasons, fmt.Sprintf("%d more statements commit a transaction the file began", commits-maxImplicitCommits))
	}
	sum.hash = hex.EncodeToString(h.Sum(nil))
	if sum.statements == 0 {
		return sum, errors.New("the file holds no statements")
	}
	return sum, nil
}

func (a *App) sqlFileView(c *ui.Context) {
	x := a.sqlFile
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Modal(c, &x.open, func() {
		ui.Column(c).Width(640).Gap(12).Children(func() {
			ui.Text(c, "Run "+filepath.Base(x.path)+" on "+x.conn.Config.Name).FontSize(15).Bold()
			ui.Text(c, x.path).Font(widgets.MonoFont).FontSize(12).TextColor(pal.Muted).SingleLine()
			switch {
			case x.reading:
				ui.Row(c).Gap(8).Children(func() {
					ui.Spinner(c).Size(14, 14)
					ui.Text(c, "Reading the file: "+widgets.HumanBytes(x.progress.Load())).TextColor(pal.Muted)
				})
				c.After(200 * time.Millisecond)
			case x.statements > 0:
				a.sqlFileSummary(c, x)
			}
			if x.running || x.finished {
				ui.Row(c).Gap(8).Children(func() {
					if x.running {
						ui.Spinner(c).Size(14, 14)
						c.After(200 * time.Millisecond)
					}
					pct := 0.0
					if x.size > 0 {
						pct = float64(x.progress.Load()) / float64(x.size) * 100
					}
					ui.Text(c, fmt.Sprintf("%d of %d statements run (%.0f%% of the file)", x.done.Load(), x.statements, pct)).TextColor(pal.Muted)
				})
			}
			for _, f := range x.failures {
				ui.Text(c, f).FontSize(12).TextColor(th.Danger).Selectable()
			}
			if x.err != "" {
				ui.Text(c, x.err).TextColor(th.Danger).Selectable()
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if x.running || x.reading {
					if ui.Button(c, "Cancel").Clicked() && x.cancel != nil {
						x.cancel()
					}
					return
				}
				close := "Cancel"
				if x.finished {
					close = "Close"
				}
				if ui.Button(c, close).Clicked() {
					x.open = false
				}
				if !x.finished {
					disabled := x.statements == 0 || x.err != ""
					var run ui.Element
					if len(x.dangerous) > 0 {
						run = widgets.DangerButton(c, "Run", disabled)
					} else {
						run = ui.PrimaryButton(c, "Run").Disabled(disabled)
					}
					if run.Clicked() && !disabled {
						a.confirmSQLFile(x)
					}
				}
			})
		})
	})
	if !x.open && !x.running {
		if x.cancel != nil {
			x.cancel()
		}
		a.sqlFile = nil
	}
}

// sqlFileSummary says what the file holds, and how it will run.
func (a *App) sqlFileSummary(c *ui.Context, x *sqlFileRun) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	verbs := make([]string, 0, len(x.verbs))
	for v := range x.verbs {
		verbs = append(verbs, v)
	}
	sort.Slice(verbs, func(i, j int) bool {
		return x.verbs[verbs[i]] > x.verbs[verbs[j]] || x.verbs[verbs[i]] == x.verbs[verbs[j]] && verbs[i] < verbs[j]
	})
	parts := make([]string, len(verbs))
	for i, v := range verbs {
		parts[i] = fmt.Sprintf("%d %s", x.verbs[v], v)
	}
	ui.Text(c, fmt.Sprintf("%s in %s: %s.", widgets.Count(x.statements, "statement"), widgets.HumanBytes(x.size), strings.Join(parts, ", "))).FontSize(13)
	if len(x.dangerous) > 0 {
		ui.Column(c).Gap(2).Padding(8, 10).Radius(6).Background(th.Danger.Alpha(0.1)).Children(func() {
			ui.Text(c, "It destroys data:").Bold().FontSize(12).TextColor(th.Danger)
			for _, d := range x.dangerous {
				ui.Text(c, d).Font(widgets.MonoFont).FontSize(11.5).SingleLine()
			}
			if x.more > 0 {
				ui.Text(c, fmt.Sprintf("and %d more", x.more)).FontSize(12).TextColor(pal.Muted)
			}
		})
	}
	ui.Column(c).Gap(6).Children(func() {
		ui.Checkbox(c, &x.stopOnError, "Stop at the first error").Disabled(x.oneTx || x.running || x.finished)
		why := ""
		switch {
		case !x.conn.Config.Engine.TransactionalDDL():
			why = x.conn.Config.Engine.Label() + " cannot hold schema changes in a transaction."
		case x.txControl:
			why = "The file begins and ends transactions of its own."
		}
		ui.Checkbox(c, &x.oneTx, "In one transaction: all or nothing").Disabled(why != "" || x.running || x.finished)
		if why != "" {
			ui.Text(c, why).FontSize(12).TextColor(pal.Muted)
		}
		if x.oneTx {
			x.stopOnError = true
		}
	})
}

// confirmSQLFile asks what the safety policy asked of the file's
// statements, once for them all, then runs it. On production, where the
// policy asks only of what writes, a file that writes asks for the
// connection's name: it then runs without asking again, however many
// writes it holds. The verdict each statement is checked against as it
// runs stays the survey's.
func (a *App) confirmSQLFile(x *sqlFileRun) {
	v := x.verdict
	title, action := "Run "+filepath.Base(x.path)+"?", "Run"
	if x.restore != "" {
		rv := restoreVerdict(x.conn, x.restore)
		v.Confirm, v.Reasons = true, append(rv.Reasons, v.Reasons...)
		title, action = "Restore into "+x.restore+"?", "Restore"
	}
	if !v.Confirm {
		a.runSQLFile(x)
		return
	}
	if x.conn.Config.Env == db.Production {
		v.TypeName = true
	}
	preview := fmt.Sprintf("%s of %s", widgets.Count(x.statements, "statement"), filepath.Base(x.path))
	if len(x.dangerous) > 0 {
		preview += ", among them:\n" + strings.Join(x.dangerous, "\n")
	}
	a.AskConfirm(x.conn, v, title, action, preview, func() { a.runSQLFile(x) })
}

// runSQLFile runs the file's statements as it reads them again, on a
// session of its own. Each is reviewed again: what the survey did not
// find, as the file changed since, stops it.
func (a *App) runSQLFile(x *sqlFileRun) {
	if info, err := os.Stat(x.path); err != nil || info.Size() != x.size || !info.ModTime().Equal(x.modTime) {
		x.err = "The file changed since it was read: open it again to run it."
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	x.running, x.cancel, x.err, x.failures = true, cancel, "", nil
	x.done.Store(0)
	x.progress.Store(0)
	cn, cfg, database := x.conn, x.conn.Config, x.database
	approved, stopOnError, oneTx := x.verdict, x.stopOnError, x.oneTx
	path, hash := x.path, x.hash
	started := time.Now()
	pool := cn.DB
	job := fmt.Sprintf("Running %s on %s: stopping it keeps what ran so far, and rolls back a transaction the file left open.", filepath.Base(path), cfg.Name)
	if oneTx {
		job = fmt.Sprintf("Running %s on %s in one transaction: stopping it rolls it back, and nothing of the file stays.", filepath.Base(path), cfg.Name)
	}
	stopped := func() {
		x.running, x.finished, x.cancel, x.err = false, true, nil, "The file stopped on an internal error."
	}
	dataview.RunJob(a, cn, job, cancel, stopped, func() func() {
		defer cancel()
		var failures []string
		loaded := map[string]int64{}
		err := func() (err error) {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			sess, err := connection.OpenSession(ctx, pool, database)
			if err != nil {
				return err
			}
			defer sess.Close()
			if sess.Tx() != db.TxNone {
				return errors.New("a transaction is open on this database: commit or roll it back first")
			}
			// The statement that began the transaction the file holds open,
			// by its line, 0 for none.
			var begun int
			var opener string
			// However the file ends, a transaction it leaves open is rolled
			// back here, said and audited, not by closing the session.
			defer func() {
				if sess.OwnsTx() {
					err = a.rollBackFile(sess, cfg, database, path, begun, opener, err)
				}
			}()
			if oneTx {
				if err := sess.Begin(ctx); err != nil {
					return err
				}
			}
			r := sqlfile.NewReader(f, safety.Dialect(cfg.Engine))
			for {
				st, err := r.Next()
				x.progress.Store(r.Read())
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					return err
				}
				an := safety.Analyze(&cfg, []string{st.SQL})
				if w := safety.ReviewSQL(&cfg, an); !approved.Covers(w) {
					return fmt.Errorf("line %d asks for what was not agreed to: the file changed since it was read", st.Line)
				}
				start := time.Now()
				var n int64
				if st.Copy {
					n, err = sess.CopyFrom(ctx, st.SQL, r.Data())
				} else {
					n, err = sess.Exec(ctx, st.SQL)
				}
				verb := an[0].Analysis.Verb
				if !oneTx {
					switch open := sess.OwnsTx(); {
					case !open:
						begun = 0
					case begun == 0:
						begun, opener = st.Line, verb
					}
				}
				// The rows a dump loads are counted below; what else ran is
				// recorded as from an editor.
				if verb == "INSERT" || verb == "COPY" {
					if err == nil {
						loaded[verb] += max(n, 0)
					}
				} else {
					a.RecordRun(cfg, audit.KindStatement, database, st.SQL, n, time.Since(start), err)
				}
				if err != nil {
					if verb == "INSERT" || verb == "COPY" {
						a.RecordRun(cfg, audit.KindStatement, database, st.SQL, n, time.Since(start), err)
					}
					failures = append(failures, fmt.Sprintf("Line %d: %s\n%s", st.Line, err.Error(), widgets.OneLine(redact.Secrets(st.SQL), 160)))
					if ctx.Err() != nil {
						return errors.New("cancelled")
					}
					if stopOnError {
						return errors.New("stopped at the first error")
					}
					if errors.Is(err, db.ErrSessionReset) || errors.Is(err, db.ErrTxLost) {
						return errors.New("stopped where the connection was lost: the rest of the file would run on a new connection, without what the file had set on the lost one or the transaction it had open")
					}
					continue
				}
				x.done.Add(1)
				if err := ctx.Err(); err != nil {
					return errors.New("cancelled")
				}
			}
			if oneTx {
				return sess.Commit(ctx)
			}
			return nil
		}()
		if err != nil && oneTx {
			err = fmt.Errorf("%w: the transaction was rolled back, and nothing of the file stays", err)
		}
		ev := audit.Event{Kind: audit.KindScript, Database: database, Statement: path, Rows: loaded["INSERT"] + loaded["COPY"],
			Detail: fmt.Sprintf("ran the file %s (sha256 %s): %d statements, %d failed", path, hash, x.done.Load(), len(failures))}
		ev.Err = err
		a.Record(&cfg, ev)
		return func() {
			x.running, x.finished, x.cancel = false, true, nil
			x.failures = failures
			title := "SQL file finished"
			if err != nil {
				x.err = err.Error()
				title = "SQL file failed"
			} else if len(failures) > 0 {
				title = fmt.Sprintf("SQL file finished with %d errors", len(failures))
			}
			cn.ForgetCatalog()
			a.Notify(started, title, filepath.Base(path)+" on "+cfg.Name, nil)
		}
	})
}

// rollBackFile rolls back, and audits, the transaction a file of SQL left
// open on its session: one the file began at line begun with the verb
// opener, or, with begun 0, the one transaction the app ran the file in.
// It returns the run's error, err, with what became of a transaction the
// file began, which fails a file that ended without committing it.
func (a *App) rollBackFile(sess *db.Session, cfg db.Config, database, path string, begun int, opener string, err error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	rerr := sess.Rollback(ctx)
	ev := audit.Event{Kind: audit.KindStatement, Database: database, Statement: "ROLLBACK", DurationMS: time.Since(start).Milliseconds(),
		Detail: "Run SQL File: " + path + " did not finish its one transaction"}
	if begun > 0 {
		ev.Detail = fmt.Sprintf("Run SQL File: %s left the transaction begun at line %d open", path, begun)
	}
	ev.Err = rerr
	a.Record(&cfg, ev)
	if begun == 0 {
		// The caller says the one transaction was rolled back: closing the
		// session ends it, should this ROLLBACK have failed.
		if err == nil {
			err = errors.New("the transaction did not commit")
		}
		return err
	}
	what := "the transaction begun"
	if begin := safety.BeginStatement(opener); begin != "" {
		what = begin
	}
	msg := fmt.Sprintf("%s at line %d was never committed; its changes were rolled back", what, begun)
	if rerr != nil {
		msg = fmt.Sprintf("%s at line %d was never committed, and rolling it back failed (%v): closing the file's session ends it", what, begun, rerr)
	}
	if err == nil {
		return errors.New(msg)
	}
	return fmt.Errorf("%w: %s", err, msg)
}
