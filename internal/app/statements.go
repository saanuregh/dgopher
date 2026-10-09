package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/redact"
	"dgopher/internal/safety"
)

// applyStatements runs statements the app wrote, as a rename or a grant,
// on a session of their own in a database of a connection, once the
// safety policy agrees: asking first when it asks, or always, with why,
// when always is set. The statements show, and are audited, with their
// secrets hidden. done gets the error, on the main thread.
func (a *App) applyStatements(cn *connection.Conn, database, title string, stmts []string, always string, done func(error)) {
	cfg := cn.Config
	v := safety.ReviewSQL(&cfg, safety.Analyze(&cfg, stmts))
	shown := make([]string, len(stmts))
	for i, s := range stmts {
		shown[i] = redact.Secrets(s) + ";"
	}
	preview := strings.Join(shown, "\n")
	if v.Blocked != "" {
		a.RecordBlocked(cn, v.Blocked, preview)
		a.ShowError("Not allowed", v.Blocked)
		return
	}
	run := func() {
		pool := cn.DB
		a.Background(func() func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			err := runAll(ctx, a, pool, cfg, database, stmts)
			return func() { done(err) }
		})
	}
	if always != "" {
		v.Confirm = true
		v.Reasons = append(v.Reasons, always)
	}
	if !v.Confirm {
		run()
		return
	}
	a.AskConfirm(cn, v, title, "Apply", preview, run)
}

// runAll runs statements one after the other on a session of their own,
// each audited, up to the first that fails.
func runAll(ctx context.Context, a *App, pool *db.DB, cfg db.Config, database string, stmts []string) error {
	sess, err := connection.OpenSession(ctx, pool, database)
	if err != nil {
		return err
	}
	defer sess.Close()
	for _, s := range stmts {
		start := time.Now()
		n, err := sess.Exec(ctx, s)
		a.RecordRun(cfg, audit.KindStatement, database, s, n, time.Since(start), err)
		if err != nil {
			return fmt.Errorf("%s: %w", redact.Secrets(s), err)
		}
	}
	return nil
}
