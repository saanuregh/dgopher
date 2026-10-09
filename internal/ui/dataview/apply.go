package dataview

import (
	"context"
	"fmt"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/redact"
	"dgopher/internal/safety"
)

// ApplyChange runs a change the app wrote, as a table's or an account's,
// on a session of its own in a database of a connection, once the safety
// policy agrees: asking first when it asks, or always, with why, when
// always is set. The change shows, and is audited, with its secrets
// hidden. running, when set, hears how to cancel it once it runs, else
// it gives up after a minute; done hears how it ended, on the main thread.
func ApplyChange(a Host, cn *connection.Conn, database, title string, ch db.SchemaChange, always string, running func(cancel func()), done func(error)) {
	cfg := cn.Config
	stmts := ch.Statements()
	v := safety.ReviewSQL(&cfg, safety.Analyze(&cfg, stmts))
	preview := redact.Secrets(ch.Text())
	if v.Blocked != "" {
		a.RecordBlocked(cn, v.Blocked, preview)
		a.ShowError("Not allowed", v.Blocked)
		return
	}
	if !ch.Atomic && len(stmts) > 1 {
		v.Reasons = append(v.Reasons, cfg.Engine.Label()+" commits each statement as it runs: if one fails, those before it stay.")
	}
	run := func() {
		var ctx context.Context
		var cancel context.CancelFunc
		if running != nil {
			ctx, cancel = context.WithCancel(context.Background())
			running(cancel)
		} else {
			// Nothing can cancel it: a change waiting on a lock gives up.
			ctx, cancel = context.WithTimeout(context.Background(), time.Minute)
		}
		pool := cn.DB
		a.Background(func() func() {
			defer cancel()
			var failed string
			sess, err := connection.OpenSession(ctx, pool, database)
			if err == nil {
				err = sess.Apply(ctx, ch, func(stmt string, rows int64, took time.Duration, err error) {
					a.RecordRun(cfg, audit.KindStatement, database, stmt, rows, took, err)
					if err != nil && failed == "" {
						failed = stmt
					}
				})
				sess.Close()
			}
			if err != nil && failed != "" {
				err = fmt.Errorf("%s: %w", redact.Secrets(failed), err)
			}
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
