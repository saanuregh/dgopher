package query

import (
	"context"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/safety"

	"github.com/egoist/mygo/ui"
)

// schemaStatement makes a session find unqualified names in a schema, ""
// for an engine whose sessions have no schema to change (SQLite).
func schemaStatement(e db.Engine, d db.Dialect, schema string) string {
	switch e {
	case db.Postgres:
		return "SET search_path TO " + d.Quote(schema)
	case db.MySQL, db.ClickHouse, db.DuckDB:
		return "USE " + d.Quote(schema)
	}
	return ""
}

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
	if cn.Config.Engine == db.Postgres && len(cn.Databases) > 1 {
		database := q.Database
		if database == "" {
			database = cn.Config.Database
		}
		ui.MenuButton(c, database, func(m *ui.Menu) {
			for _, name := range cn.Databases {
				if m.Item(name).Checked(name == database).Chosen() {
					q.switchDatabase(name)
				}
			}
		}).Tooltip("The database the editor works in")
	}
	if schemaStatement(cn.Config.Engine, cn.DB.Dialect, "") == "" {
		return
	}
	schemas, ok := cn.Schemas[q.Database]
	if !ok && cn.SchemasError(q.Database) == "" {
		connection.LoadSchemas(q.a, cn, q.Database, nil)
	}
	current := q.currentSchema()
	ui.MenuButton(c, current, func(m *ui.Menu) {
		for _, name := range schemas {
			if m.Item(name).Checked(name == current).Chosen() {
				q.switchSchema(name)
			}
		}
	}).Tooltip("The schema unqualified names are found in")
}

// switchSchema makes the editor's session find names in a schema, as its
// engine's SET search_path or USE does, through the safety policy.
func (q *Tab) switchSchema(schema string) {
	if q.Busy() {
		q.a.ShowError("Not switched", q.Name+" is still running a statement.")
		return
	}
	cn := q.Conn
	cfg := cn.Config
	stmt := schemaStatement(cfg.Engine, cn.DB.Dialect, schema)
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
			}
			q.a.Post(func() {
				q.Running = false
				if err != nil {
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
	// The results' rows end where they were read; their cursors close
	// before the session they are on.
	var closers []func()
	for _, r := range q.results {
		if r.view != nil {
			closers = append(closers, r.view.CutShort())
		}
	}
	sess := q.sess
	go func() {
		for _, close := range closers {
			close()
		}
		if sess != nil {
			sess.Close()
		}
	}()
	q.sess, q.Database, q.schema = nil, name, ""
	q.note("The editor works in the database "+name+" now.", "", false)
}
