package dashboard

import (
	"context"
	"errors"
	"fmt"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/params"
	"dgopher/internal/safety"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/dataview"
)

const (
	// maxRows is how many rows a panel reads of its result.
	maxRows = 5000
	// runTimeout is how long a panel's query may run.
	runTimeout = time.Minute
)

// prepared is a panel's statement, bound and reviewed, ready to run.
type prepared struct {
	sql    string
	args   []any
	logged string // as the audit log and an error show it
}

// prepare binds a panel's parameters and puts its statement through the
// safety policy: a dashboard runs one read, which the policy lets run
// without asking.
func prepare(cn *connection.Conn, sql string, values map[string]params.Input) (prepared, error) {
	cfg := cn.Config
	d := safety.Dialect(cfg.Engine)
	stmts := sqltext.SplitWith(sql, d, sqltext.SplitOptions{Mode: sqltext.SemicolonOnly})
	if len(stmts) != 1 {
		return prepared{}, fmt.Errorf("a panel runs one statement, and its query holds %d", len(stmts))
	}
	bound, args, shown, err := params.Bind(stmts[0].Text, cfg.Engine, d, values)
	if err != nil {
		return prepared{}, err
	}
	if len(sqltext.SplitWith(bound, d, sqltext.SplitOptions{Mode: sqltext.SemicolonOnly})) != 1 {
		return prepared{}, errors.New("a ${…} value adds a statement to the panel's query: it is not run")
	}
	an := safety.Analyze(&cfg, []string{bound})
	an[0].Args, an[0].Shown = args, shown
	if err := notRead(an[0].Analysis); err != nil {
		return prepared{}, err
	}
	switch v := safety.ReviewSQL(&cfg, an); {
	case v.Blocked != "":
		return prepared{}, errors.New(v.Blocked)
	case v.Confirm:
		return prepared{}, errors.New("the safety policy asks before this query runs, which a dashboard cannot: run it in an editor")
	}
	logged := bound
	if shown != "" {
		logged += "\n-- parameters: " + shown
	}
	return prepared{sql: bound, args: args, logged: logged}, nil
}

// CheckRead says why a statement cannot be a panel's: a panel runs one
// statement that reads.
func CheckRead(cn *connection.Conn, sql string) error {
	d := safety.Dialect(cn.Config.Engine)
	stmts := sqltext.SplitWith(sql, d, sqltext.SplitOptions{Mode: sqltext.SemicolonOnly})
	if len(stmts) != 1 {
		return fmt.Errorf("a panel runs one statement, and this holds %d", len(stmts))
	}
	return notRead(safety.Analyze(&cn.Config, []string{stmts[0].Text})[0].Analysis)
}

// notRead refuses a statement that is not a read.
func notRead(a sqltext.Analysis) error {
	if a.Class != sqltext.Read || a.Dangerous {
		return fmt.Errorf("a dashboard runs reads only, and %s is not one", verbOf(a))
	}
	return nil
}

func verbOf(a sqltext.Analysis) string {
	if a.Verb == "" {
		return "a statement the app cannot classify"
	}
	return a.Verb
}

// readOnlyBegin starts a transaction in which the server itself refuses
// writes, where the engine has one: a read the app classified may still
// call a function that writes.
func readOnlyBegin(e db.Engine) string {
	switch e {
	case db.Postgres, db.MySQL:
		return "START TRANSACTION READ ONLY"
	}
	return ""
}

// run runs a panel's statement on a session of its own, in a read-only
// transaction where the engine has one, and reads up to maxRows of its
// result.
func run(ctx context.Context, pool *db.DB, cfg db.Config, database string, p prepared) (*dataview.Source, error) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	sess, err := connection.OpenSession(ctx, pool, database)
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	if begin := readOnlyBegin(cfg.Engine); begin != "" {
		if _, err := sess.Exec(ctx, begin); err != nil {
			return nil, err
		}
		defer func() {
			// Even when ctx has ended: the session goes back to the pool.
			rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer rcancel()
			sess.Rollback(rctx)
		}()
	}
	cursor, err := sess.Query(ctx, p.sql, p.args...)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()
	rows, err := cursor.Fetch(maxRows)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = [][]any{}
	}
	return &dataview.Source{Cols: cursor.Columns, Rows: rows}, nil
}

// audited records a panel's run as an editor's is recorded.
func audited(h Host, cfg db.Config, database string, p prepared, src *dataview.Source, took time.Duration, err error) {
	rows := int64(-1)
	if src != nil {
		rows = int64(len(src.Rows))
	}
	h.RecordRun(cfg, audit.KindStatement, database, p.logged, rows, took, err)
}
