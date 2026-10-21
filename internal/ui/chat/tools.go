package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agent "github.com/saanuregh/agent-cli-connector"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/export"
	"dgopher/internal/lists"
	"dgopher/internal/safety"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/dataview"
)

// maxRows is how many rows a read gives the agent; maxResult how many
// bytes of them.
const (
	maxRows   = 200
	maxResult = 24 << 10
)

// onMain runs f on the UI thread, where the app's state is read, and
// returns what it returned, or the context's error once it ends first.
func onMain[T any](ctx context.Context, post func(func()), f func() T) (T, error) {
	done := make(chan T, 1)
	post(func() { done <- f() })
	select {
	case v := <-done:
		return v, nil
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

var (
	noArgs       = json.RawMessage(`{"type":"object","properties":{}}`)
	tablesSchema = json.RawMessage(`{"type":"object","properties":{
		"connection":{"type":"string","description":"The connection's name, as list_connections gives it. Empty for the editor's."},
		"database":{"type":"string","description":"The database, on servers with several; empty for the connection's."},
		"schema":{"type":"string","description":"The schema; empty for the connection's default."}}}`)
	describeSchema = json.RawMessage(`{"type":"object","properties":{
		"connection":{"type":"string"},
		"database":{"type":"string"},
		"schema":{"type":"string"},
		"table":{"type":"string","description":"The table or view's name."}},"required":["table"]}`)
	querySchema = json.RawMessage(`{"type":"object","properties":{
		"connection":{"type":"string"},
		"database":{"type":"string"},
		"sql":{"type":"string","description":"One statement that only reads, such as a SELECT."}},"required":["sql"]}`)
)

type place struct {
	Connection string `json:"connection"`
	Database   string `json:"database"`
	Schema     string `json:"schema"`
	Table      string `json:"table"`
	SQL        string `json:"sql"`
}

// tools are the app's tools the agent may call.
func (t *Chat) tools() []agent.Tool {
	read := func(tool agent.Tool) agent.Tool {
		tool.ReadOnly = true
		return tool
	}
	return []agent.Tool{
		read(agent.NewTool("list_connections", "Lists DGopher's database connections: name, engine, environment, whether connected and read-only, and the database.", noArgs, t.listConnections)),
		read(agent.NewTool("list_tables", "Lists the tables and views of a schema of a connection, with the schemas there are.", tablesSchema, t.listTables)),
		read(agent.NewTool("describe_table", "Describes a table's columns: name, type, nullable, default, primary key and comment.", describeSchema, t.describeTable)),
		read(agent.NewTool("run_read_query", fmt.Sprintf("Runs one read-only statement on a connection, once the user allows it, and returns up to %d rows as a Markdown table. Refused on production, and for any statement that changes something.", maxRows), querySchema, t.runReadQuery)),
		// ReadOnly, though it adds to the editor's file: the agent's
		// harness (pi's dontAsk) then runs it without a prompt of its own,
		// as the chat asks the user itself.
		read(t.chartTool()),
	}
}

// connInfo is a connection as list_connections tells it.
type connInfo struct {
	Name        string `json:"name"`
	Engine      string `json:"engine"`
	Environment string `json:"environment"`
	Connected   bool   `json:"connected"`
	ReadOnly    bool   `json:"readOnly,omitempty"`
	Database    string `json:"database,omitempty"`
	Current     bool   `json:"editorConnection,omitempty"`
	// NoRows is set where the user keeps the rows from the agent.
	NoRows bool `json:"noRowsToAgent,omitempty"`
}

func (t *Chat) listConnections(ctx context.Context, _ struct{}) (agent.ToolResult, error) {
	infos, err := onMain(ctx, t.a.Post, func() []connInfo {
		var out []connInfo
		for _, cn := range t.a.Connections() {
			cfg := cn.Config
			out = append(out, connInfo{Name: cfg.Name, Engine: cfg.Engine.Label(), Environment: cfg.Env.Label(),
				Connected: cn.Status == connection.StatusConnected, ReadOnly: cfg.ReadOnly, Database: cfg.Database, Current: cn == t.conn(), NoRows: cfg.NoRowsToChat})
		}
		return out
	})
	if err != nil {
		return agent.ToolResult{}, err
	}
	return agent.JSONResult(infos)
}

// findConn is the connection a tool names, by name or ID, any case; the
// editor's for none.
func (t *Chat) findConn(name string) (*connection.Conn, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		if cn := t.conn(); cn != nil {
			return cn, nil
		}
		if conns := t.a.Connections(); len(conns) == 1 {
			return conns[0], nil
		}
		return nil, errors.New("name a connection: list_connections gives their names")
	}
	for _, cn := range t.a.Connections() {
		if strings.EqualFold(cn.Config.Name, name) || strings.EqualFold(cn.Config.ID, name) {
			return cn, nil
		}
	}
	return nil, fmt.Errorf("there is no connection %q: list_connections gives their names", name)
}

// sqlPool is a connected SQL connection's pool for a database, read on
// the UI thread.
type sqlPool struct {
	cn     *connection.Conn
	cfg    db.Config
	poolOf func(context.Context) (*db.DB, error)
	pool   *db.DB
}

// connected finds a connection a tool names, connected and of SQL: one
// not connected, the user is asked to connect first.
func (t *Chat) connected(ctx context.Context, name, database string) (sqlPool, error) {
	type found struct {
		p    sqlPool
		need *connection.Conn
		err  error
	}
	find := func() (found, error) {
		return onMain(ctx, t.a.Post, func() found {
			cn, err := t.findConn(name)
			switch {
			case err != nil:
				return found{err: err}
			case !cn.Config.Engine.IsSQL():
				return found{err: fmt.Errorf("%s is %s, which runs no SQL", cn.Config.Name, cn.Config.Engine.Label())}
			case cn.Status != connection.StatusConnected:
				return found{need: cn}
			}
			return found{p: sqlPool{cn: cn, cfg: cn.Config, poolOf: cn.PoolFor(database), pool: cn.DB}}
		})
	}
	f, err := find()
	if err == nil && f.need != nil {
		if err = t.askToConnect(ctx, f.need); err == nil {
			f, err = find()
			if err == nil && f.need != nil {
				err = fmt.Errorf("%s is not connected", f.need.Config.Name)
			}
		}
	}
	if err == nil {
		err = f.err
	}
	return f.p, err
}

// connectAsk is the user's answer to connecting a connection the agent's
// tools need, which every call waiting for it shares: done closes once it
// connected, or err says why not.
type connectAsk struct {
	done chan struct{}
	err  error
}

// connectWait is how long a connection the user agreed to may take to
// open, its password asked included.
const connectWait = 2 * time.Minute

// askToConnect asks the user to connect cn, once for every call that
// needs it meanwhile, and waits for it to connect.
func (t *Chat) askToConnect(ctx context.Context, cn *connection.Conn) error {
	r := &request{id: fmt.Sprintf("connect-%d", t.nextID.Add(1)), connect: true, answer: make(chan decision, 1)}
	ask, err := onMain(ctx, t.a.Post, func() *connectAsk {
		if ask := t.connecting[cn.Config.ID]; ask != nil {
			r = nil
			return ask
		}
		ask := &connectAsk{done: make(chan struct{})}
		t.connecting[cn.Config.ID] = ask
		r.title = "Connect to " + cn.Config.Name + "?"
		r.detail = providerLabel(t.provider) + " needs " + cn.Config.Name + " (" + cn.Config.Env.Label() + ") to answer: its tables, and the reads you allow. DGopher connects as it always does, asking for a password where it needs one."
		t.requests = append(t.requests, r)
		return ask
	})
	if err != nil {
		return err
	}
	if r != nil {
		go t.connectAnswered(ctx, cn, r, ask)
	}
	select {
	case <-ask.done:
		return ask.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// connectAnswered takes the user's answer to connecting, connects, and
// waits until the connection opens or fails.
func (t *Chat) connectAnswered(ctx context.Context, cn *connection.Conn, r *request, ask *connectAsk) {
	defer dataview.RecoverBackground(t.a.Post, t.a.ShowReport, nil)
	finish := func(err error) {
		t.a.Post(func() {
			ask.err = err
			delete(t.connecting, cn.Config.ID)
			close(ask.done)
		})
	}
	select {
	case d := <-r.answer:
		if d == deny {
			finish(fmt.Errorf("%s is not connected: the user chose not to connect it now", cn.Config.Name))
			return
		}
	case <-ctx.Done():
		t.a.Post(func() { t.answer(r, deny, "") })
		finish(ctx.Err())
		return
	}
	onMain(ctx, t.a.Post, func() struct{} { t.a.Connect(cn, nil); return struct{}{} })
	type state struct {
		status connection.Status
		err    string
	}
	// Idle while a prompt asks for its password or its destination: it
	// is waited for.
	deadline := time.Now().Add(connectWait)
	for time.Now().Before(deadline) {
		st, err := onMain(ctx, t.a.Post, func() state { return state{cn.Status, cn.Err} })
		switch {
		case err != nil:
			finish(err)
			return
		case st.status == connection.StatusConnected:
			finish(nil)
			return
		case st.status == connection.StatusFailed:
			finish(fmt.Errorf("%s did not connect: %s", cn.Config.Name, st.err))
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	finish(fmt.Errorf("%s had not connected after %s", cn.Config.Name, connectWait))
}

// schemaOf is the pool of the database a tool names, and its schema:
// the one named, else the connection's default.
func (t *Chat) schemaOf(ctx context.Context, p place) (*db.DB, string, error) {
	c, err := t.connected(ctx, p.Connection, p.Database)
	if err != nil {
		return nil, "", err
	}
	d, err := c.poolOf(ctx)
	if err != nil {
		return nil, "", err
	}
	schema := p.Schema
	if schema == "" {
		schema, _ = d.Dialect.CurrentSchema(ctx, d.Catalog())
	}
	return d, schema, nil
}

type tableInfo struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

func (t *Chat) listTables(ctx context.Context, p place) (agent.ToolResult, error) {
	d, schema, err := t.schemaOf(ctx, p)
	if err != nil {
		return agent.ErrorResult(err.Error()), nil
	}
	schemas, _ := d.Dialect.Schemas(ctx, d.Catalog())
	objs, err := d.Dialect.ObjectNames(ctx, d.Catalog(), schema)
	if err != nil {
		return agent.ErrorResult(err.Error()), nil
	}
	tables := lists.Map(objs, func(o db.Object) tableInfo { return tableInfo{Name: o.Name, Kind: string(o.Kind)} })
	return agent.JSONResult(map[string]any{"schema": schema, "schemas": schemas, "tables": tables})
}

func (t *Chat) describeTable(ctx context.Context, p place) (agent.ToolResult, error) {
	d, schema, err := t.schemaOf(ctx, p)
	if err != nil {
		return agent.ErrorResult(err.Error()), nil
	}
	cols, err := d.Dialect.Columns(ctx, d.Catalog(), schema, p.Table)
	if err != nil {
		return agent.ErrorResult(err.Error()), nil
	}
	if len(cols) == 0 {
		return agent.ErrorResult(fmt.Sprintf("%s.%s has no columns, or is not there: list_tables gives the tables", schema, p.Table)), nil
	}
	return agent.JSONResult(map[string]any{"schema": schema, "table": p.Table, "columns": cols})
}

// formatSQL is a statement as the editor's Format writes it, for a card
// to read easily.
func formatSQL(cfg *db.Config, sql string) string {
	return strings.TrimSpace(sqltext.FormatWith(sql, safety.Dialect(cfg.Engine), sqltext.SplitOptions{}))
}

// readRefusal says why a statement is not run for the agent, "" when it
// may be, once the user allows it: one statement, that only reads, on a
// connection not production, that the safety policy lets run without
// asking.
func readRefusal(cfg *db.Config, sql string) string {
	if cfg.Env == db.Production {
		return cfg.Name + " is production: the chat runs nothing there. Write the statement in a sql block for the user to insert in the editor and run."
	}
	d := safety.Dialect(cfg.Engine)
	stmts := sqltext.SplitWith(sql, d, sqltext.SplitOptions{Mode: sqltext.SemicolonOnly})
	if len(stmts) != 1 {
		return fmt.Sprintf("give one statement, not %d", len(stmts))
	}
	if how := fileReader(cfg.Engine, stmts[0].Text); how != "" {
		return "the statement reads the server's files or URLs (" + how + "): the chat reads the connection's data alone; write it in a sql block for the user to run"
	}
	an := safety.Analyze(cfg, []string{stmts[0].Text})
	if an[0].Analysis.Class != sqltext.Read {
		return "the statement is not a read: write it in a sql block for the user to insert in the editor, review and run"
	}
	v := safety.ReviewSQL(cfg, an)
	switch {
	case v.Blocked != "":
		return v.Blocked
	case v.Confirm || v.Writes:
		return "the statement needs the user's confirmation: write it in a sql block for the user to run in the editor"
	}
	return ""
}

func (t *Chat) runReadQuery(ctx context.Context, p place) (agent.ToolResult, error) {
	sql := strings.TrimSpace(p.SQL)
	c, err := t.connected(ctx, p.Connection, p.Database)
	if err != nil {
		return agent.ErrorResult(err.Error()), nil
	}
	why := readRefusal(&c.cfg, sql)
	if why == "" && c.cfg.NoRowsToChat {
		why = c.cfg.Name + " keeps its rows from the AI chat: write the statement in a sql block for the user to run in the editor"
	}
	if why != "" {
		onMain(ctx, t.a.Post, func() struct{} { t.a.RecordBlocked(c.cn, "AI chat: "+why, sql); return struct{}{} })
		return agent.ErrorResult("Not run: " + why), nil
	}
	// The user allows each read, or every read of the connection for
	// the rest of the chat.
	r := &request{id: fmt.Sprintf("read-%d", t.nextID.Add(1)), title: "Run a read on " + c.cfg.Name, sql: formatSQL(&c.cfg, sql), conn: c.cfg.ID,
		connName: c.cfg.Name, answer: make(chan decision, 1), always: true}
	asked, err := onMain(ctx, t.a.Post, func() bool {
		if t.allowReads[c.cfg.ID] || ctx.Err() != nil {
			return false // allowed, or no one waits for the answer any more
		}
		r.detail = "Up to " + fmt.Sprint(maxRows) + " of its rows go to " + providerLabel(t.provider) + "."
		t.requests = append(t.requests, r)
		return true
	})
	if err != nil {
		return agent.ToolResult{}, err
	}
	if asked {
		select {
		case d := <-r.answer:
			switch d {
			case deny:
				return agent.ErrorResult("The user did not allow the statement to run."), nil
			case allowAlways:
				onMain(ctx, t.a.Post, func() struct{} { t.allowReads[c.cfg.ID] = true; return struct{}{} })
			}
		case <-ctx.Done():
			t.a.Post(func() { t.answer(r, deny, "") })
			return agent.ToolResult{}, ctx.Err()
		}
	}
	return t.read(ctx, c, p.Database, sql)
}

// read runs a read on a session of its own, as a job of the connection,
// audited, and returns its first rows.
func (t *Chat) read(ctx context.Context, c sqlPool, database, sql string) (agent.ToolResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	finish, err := onMain(ctx, t.a.Post, func() func() { return t.a.StartJob(c.cn, "A read the AI chat runs", cancel) })
	if err != nil {
		return agent.ToolResult{}, err
	}
	defer finish()
	if timeout := time.Duration(c.cfg.StatementTimeout) * time.Second; timeout > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, timeout)
		defer stop()
	}
	start := time.Now()
	columns, rows, more, err := fetch(ctx, c.pool, database, sql)
	// The app's, not typed by the user: the user allowed it.
	cfg := c.cfg
	t.a.Record(&cfg, audit.Event{Kind: audit.KindStatement, Database: database, Statement: sql, Purpose: "a read the AI chat ran, as the user allowed",
		Rows: int64(len(rows)), DurationMS: time.Since(start).Milliseconds(), Err: err})
	if err != nil {
		return agent.ErrorResult(err.Error()), nil
	}
	return agent.TextResult(rowsText(columns, rows, more)), nil
}

// fetch reads a statement's first rows on a session of its own.
func fetch(ctx context.Context, pool *db.DB, database, sql string) (columns []string, rows [][]any, more bool, err error) {
	sess, err := connection.OpenSession(ctx, pool, database)
	if err != nil {
		return nil, nil, false, err
	}
	defer sess.Close()
	cur, err := sess.Query(ctx, sql)
	if err != nil {
		return nil, nil, false, err
	}
	defer cur.Close()
	rows, err = cur.Fetch(maxRows)
	columns = lists.Map(cur.Columns, func(c db.ColumnInfo) string { return c.Name })
	return columns, rows, !cur.Done(), err
}

// rowsText writes rows as a Markdown table for the agent, cut at
// maxResult bytes.
func rowsText(columns []string, rows [][]any, more bool) string {
	if len(columns) == 0 {
		return "The statement returned no columns."
	}
	text, err := export.Text(export.Markdown, columns, rows, export.Options{Header: true})
	if err != nil {
		return err.Error()
	}
	if len(text) > maxResult {
		text = strings.ToValidUTF8(text[:maxResult], "") + "\n… cut here, too long."
	}
	note := fmt.Sprintf("%d rows.", len(rows))
	if more {
		note = fmt.Sprintf("The first %d rows; there are more.", len(rows))
	}
	return text + "\n" + note
}
