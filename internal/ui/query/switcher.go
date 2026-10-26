package query

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/safety"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// currentDatabase is the database the editor works in: its own, else the
// connection's.
func (q *Tab) currentDatabase() string { return cmp.Or(q.Database, q.Conn.Config.Database) }

// currentSchema is the schema the editor's session finds names in, as
// last read, else the connection's.
func (q *Tab) currentSchema() string {
	if q.schema != "" {
		return q.schema
	}
	return q.Conn.DefaultSchema
}

// switcherView offers the databases of a PostgreSQL server, and the
// schemas of the editor's database, to work in.
func (q *Tab) switcherView(c *ui.Context) {
	cn := q.Conn
	if cn.Status != connection.StatusConnected || cn.DB == nil {
		return
	}
	if cn.SwitchesDatabase() {
		// The app's own drop-down, in its theme, as the other choices:
		// a copy, which shows the database switched to only once it is.
		database := q.currentDatabase()
		if ui.Select(c, &database, cn.Databases).Label("Database").Tooltip("The database the editor works in").
			FontSize(widgets.ToolFontSize).Changed() { // as the toolbar's buttons, as tall
			q.switchDatabase(database)
		}
	}
	if db.UseSchemaSQL(cn.Config.Engine, "") == "" {
		return
	}
	schemas, ok := cn.Schemas[q.Database]
	if !ok && cn.SchemasError(q.Database) == "" {
		connection.LoadSchemas(q.a, cn, q.Database, nil)
	}
	current := q.currentSchema()
	if ui.Select(c, &current, schemas).Label("Schema").Tooltip("The schema unqualified names are found in").
		FontSize(widgets.ToolFontSize).Changed() {
		q.switchSchema(current)
	}
}

// switchSchema makes the editor's session find names in a schema, as its
// engine's SET search_path or USE does, through the safety policy.
func (q *Tab) switchSchema(schema string) {
	if q.Busy() {
		q.a.ShowError("Not switched", q.Name+" is still running a statement.")
		return
	}
	cn := q.Conn
	if cn.Status != connection.StatusConnected || cn.DB == nil {
		// Closed since the command was offered, as when left unused.
		q.a.ShowError("Not switched", cn.Config.Name+" is not connected.")
		return
	}
	cfg := cn.Config
	stmt := db.UseSchemaSQL(cfg.Engine, schema)
	v := safety.ReviewSQL(&cfg, safety.Analyze(&cfg, []string{stmt}))
	if v.Blocked != "" {
		q.a.RecordBlocked(cn, v.Blocked, stmt)
		q.a.ShowError("Not allowed", v.Blocked)
		return
	}
	run := func() {
		q.Running, q.StartedAt = true, time.Now()
		sess, pool, database := q.sess, cn.DB, q.Database
		go func() {
			defer dataview.RecoverBackground(q.a.Post, q.a.ShowReport, func() { q.Running = false })
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var err error
			if sess == nil {
				if sess, err = connection.OpenSession(ctx, pool, database); err == nil {
					q.adoptSession(sess)
				}
			}
			var current string
			if err == nil {
				start := time.Now()
				_, err = sess.Exec(ctx, stmt)
				q.a.RecordRun(cfg, audit.KindStatement, database, stmt, -1, time.Since(start), err)
			}
			if err == nil {
				current, err = sess.CurrentSchema(ctx)
			} else if errors.Is(err, db.ErrSessionReset) {
				current, _ = sess.CurrentSchema(ctx)
			}
			q.a.Post(func() {
				q.Running = false
				if err != nil {
					if current != "" {
						q.schema = current // the connection made again finds names there
					}
					q.note("Could not switch to "+schema+": "+err.Error(), stmt, true)
					return
				}
				q.schema = current
				q.note("Names are found in "+current+" now.", stmt, false)
			})
		}()
	}
	if v.Confirm {
		q.a.AskConfirm(cn, v, "Switch to "+schema+"?", "Switch", stmt, run)
		return
	}
	run()
}

// rereadSchema reads again, off the main thread, where the session finds
// names, after a statement found its connection made again
// (db.ErrSessionReset): the switcher shows the new connection's.
func (q *Tab) rereadSchema() {
	sess := q.sess
	if sess == nil {
		return
	}
	q.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		schema, err := sess.CurrentSchema(ctx)
		return func() {
			if err == nil && q.sess == sess {
				q.schema = schema
			}
		}
	})
}

// switchDatabase moves the editor to another database of its PostgreSQL
// server, on a session of its own there; never with a transaction open,
// which it would end.
func (q *Tab) switchDatabase(name string) {
	switch {
	case name == q.Database:
		return
	case q.Tx != db.TxNone:
		q.a.ShowError("Not switched", "Commit or roll back the open transaction first: switching ends the session it is on.")
		return
	case q.Busy():
		q.a.ShowError("Not switched", q.Name+" is still running a statement.")
		return
	}
	q.releaseConnection()
	q.Database = name
	q.note("The editor works in the database "+name+" now.", "", false)
}

// releaseConnection closes the editor's session and keeps the editor: the
// next run opens another. The results' rows end where they were read;
// their cursors close before the session they are on.
func (q *Tab) releaseConnection() {
	var closers []func()
	for _, r := range q.results {
		if r.view != nil {
			closers = append(closers, r.view.CutShort())
		}
	}
	sess := q.sess
	go func() {
		defer dataview.RecoverBackground(q.a.Post, q.a.ShowReport, nil)
		for _, close := range closers {
			close()
		}
		if sess != nil {
			sess.Close()
		}
	}()
	q.sess, q.schema, q.autocommitOff = nil, "", false
}

// ConnectionClosed lets the session go with its connection, closed as
// unused. The next run is refused once, as after a lost connection: it
// would run without what the session had set, as USE or search_path.
func (q *Tab) ConnectionClosed() {
	q.sessionLost = q.sessionLost || q.sess != nil
	q.releaseConnection()
}

// Disconnected lets the editor's session go with its connection, which
// the user disconnected: a statement running stops, a transaction rolls
// back and changes not applied are discarded. The editor stays, and
// connects again at its next run.
func (q *Tab) Disconnected() {
	if q.Tx != db.TxNone {
		q.a.Record(&q.Conn.Config, audit.Event{Kind: audit.KindStatement, Database: q.Database, Statement: "ROLLBACK", Detail: "disconnected with its transaction open"})
	}
	for _, r := range q.results {
		if r.view != nil {
			r.view.Discard()
		}
	}
	if q.Running && q.cancel != nil {
		q.cancel()
	}
	q.setTx(db.TxNone, db.TxNone)
	q.uncommitted = 0
	q.releaseConnection()
}

// DisconnectReason is what disconnecting loses of the editor, as
// CloseReason is what closing it loses; "" for nothing.
func (q *Tab) DisconnectReason() string {
	switch {
	case q.Running:
		return "A statement is still running. Disconnecting cancels it."
	case q.exports > 0:
		return "An export is reading on this editor's session. Disconnecting stops it."
	case q.pendingCount() > 0:
		return fmt.Sprintf("%s to %s in the results not applied. Disconnecting discards them.", widgets.Count(q.pendingCount(), "change"), q.pendingTable())
	case q.Tx != db.TxNone:
		return "A transaction is open on this editor's session. Disconnecting rolls it back, losing its changes."
	case q.uncommitted > 0:
		return fmt.Sprintf("%d changed rows wait to be committed. Disconnecting rolls them back.", q.uncommitted)
	}
	return ""
}

// HoldsConnection reports work that needs the editor's session on cn as
// it is: a statement or export running, a transaction or rows to commit,
// or grid changes not applied. An editor alone does not hold it.
func (q *Tab) HoldsConnection(cn *connection.Conn) bool {
	return cn == q.Conn && (q.Busy() || q.Tx != db.TxNone || q.uncommitted > 0 || q.pendingCount() > 0)
}
