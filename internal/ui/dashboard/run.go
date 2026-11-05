package dashboard

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	// runTimeout is how long a panel's query may run on a connection
	// without a statement timeout.
	runTimeout = time.Minute
)

// prepared is a panel's statement, bound and reviewed, ready to run.
type prepared struct {
	sql   string
	args  []any
	typed string // the panel's statement as written, when ${var} values changed it
	shown string // the parameters' values
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
		return prepared{}, errors.New("this query needs your confirmation first, which a dashboard cannot ask: run it in an editor")
	}
	p := prepared{sql: bound, args: args, shown: shown}
	if bound != stmts[0].Text {
		p.typed = stmts[0].Text
	}
	return p, nil
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

// panelTimeout is how long a panel's query may run: the connection's
// statement timeout, else runTimeout.
func panelTimeout(cfg db.Config) time.Duration {
	if d := time.Duration(cfg.StatementTimeout) * time.Second; d > 0 {
		return d
	}
	return runTimeout
}

// run runs a panel's statement on a session of its own, in a read-only
// transaction where the engine has one, and reads up to maxRows of its
// result.
func run(ctx context.Context, pool *db.DB, cfg db.Config, database string, p prepared) (src *dataview.Source, err error) {
	ctx, cancel := context.WithTimeout(ctx, panelTimeout(cfg))
	defer cancel()
	defer func() {
		// The driver's error need not say the timeout ended it.
		if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) && !errors.Is(err, context.DeadlineExceeded) {
			err = fmt.Errorf("%w: %w", err, context.DeadlineExceeded)
		}
	}()
	cursor, release, err := connection.ReadQuery(ctx, pool, cfg, database, p.sql, p.args...)
	if err != nil {
		return nil, err
	}
	defer release()
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
	h.RecordTyped(cfg, audit.TypedRun{Kind: audit.KindStatement, Database: database, Sent: p.sql, Typed: p.typed, Params: p.shown, Rows: rows, Took: took, Err: err})
}

// stoppedText says what stopped a panel's run that err ended, as the
// cause its run's context was cancelled with, or its timeout; "" for a
// run that failed by itself.
func stoppedText(ctx context.Context, err error, timeout time.Duration) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "Timed out after " + shortDuration(timeout)
	case errors.Is(err, context.Canceled) || ctx.Err() != nil:
		if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
			return "Stopped: " + cause.Error()
		}
		return "Stopped"
	}
	return ""
}

// shortDuration writes d as "1m" or "30s", not "1m0s".
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// whyNotRunAndKeep says why an edited panel cannot run yet, "" when it
// can.
func whyNotRunAndKeep(sql string, cn *connection.Conn) string {
	switch {
	case strings.TrimSpace(sql) == "":
		return "Write the panel's query first"
	case cn == nil:
		return "Choose the panel's connection first"
	}
	return ""
}
