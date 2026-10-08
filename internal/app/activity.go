package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/safety"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// activitySpec is how an engine lists what runs on the server and stops
// it.
type activitySpec struct {
	query string
	// stop and kill return the statement that cancels the query of a row,
	// and the one that ends its session, "" when the engine has none.
	stop func(row []any) string
	kill func(row []any) string
}

func activityOf(e db.Engine) (activitySpec, bool) {
	switch e {
	case db.Postgres:
		return activitySpec{
			query: `SELECT pid, usename AS "user", datname AS database, application_name AS application, client_addr::text AS client,
  state, date_trunc('second', now() - query_start)::text AS running_for, wait_event_type AS waiting_on, left(query, 400) AS query
FROM pg_stat_activity WHERE pid <> pg_backend_pid() AND backend_type = 'client backend'
ORDER BY state = 'active' DESC, query_start NULLS LAST`,
			stop: func(r []any) string { return "SELECT pg_cancel_backend(" + db.Display(r[0]) + ")" },
			kill: func(r []any) string { return "SELECT pg_terminate_backend(" + db.Display(r[0]) + ")" },
		}, true
	case db.MySQL:
		return activitySpec{
			query: `SELECT ID AS id, USER AS user, HOST AS host, DB AS db, COMMAND AS command, TIME AS seconds, STATE AS state, LEFT(INFO, 400) AS query
FROM information_schema.PROCESSLIST WHERE ID <> CONNECTION_ID() ORDER BY COMMAND = 'Sleep', TIME DESC`,
			stop: func(r []any) string { return "KILL QUERY " + db.Display(r[0]) },
			kill: func(r []any) string { return "KILL " + db.Display(r[0]) },
		}, true
	case db.ClickHouse:
		return activitySpec{
			query: `SELECT query_id, user, round(elapsed, 1) AS seconds, read_rows, formatReadableSize(memory_usage) AS memory, left(query, 400) AS query
FROM system.processes WHERE query_id != queryID() ORDER BY elapsed DESC`,
			stop: func(r []any) string {
				return "KILL QUERY WHERE query_id = " + db.Literal(db.ClickHouse, r[0]) + " ASYNC"
			},
		}, true
	}
	return activitySpec{}, false
}

// activityTab shows the sessions and queries of a server, as they run.
type activityTab struct {
	a    *App
	conn *connection.Conn
	spec activitySpec

	src      dataview.Source
	grid     *dataview.Grid
	err      string
	loading  bool
	auto     bool
	loadedAt time.Time

	// Redis: its INFO, by section and field.
	info map[string]string
	// selID is the ID of the session chosen, which follows it across
	// refreshes.
	selID string
}

func newActivityTab(a *App, cn *connection.Conn) *activityTab {
	t := &activityTab{a: a, conn: cn, grid: dataview.NewGrid(), auto: true}
	t.spec, _ = activityOf(cn.Config.Engine)
	t.refresh()
	return t
}

func (t *activityTab) Title() string                { return t.conn.Config.Name + " · activity" }
func (t *activityTab) Connection() *connection.Conn { return t.conn }
func (t *activityTab) CloseReason() string          { return "" }
func (t *activityTab) Close()                       {}

func (t *activityTab) refresh() {
	if t.loading {
		return
	}
	t.loading = true
	kv, pool := t.conn.KV, t.conn.DB // read on the main thread
	query := t.spec.query
	t.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var src dataview.Source
		var info map[string]string
		var err error
		if kv != nil {
			info, src, err = redisActivity(ctx, kv)
		} else if pool != nil {
			var s *db.Session
			if s, err = pool.Session(ctx); err == nil {
				defer s.Close()
				var c *db.Cursor
				if c, err = s.Query(ctx, query); err == nil {
					src.Cols = c.Columns
					src.Rows, err = c.Fetch(5000)
				}
			}
		}
		return func() {
			t.loading, t.loadedAt = false, time.Now()
			if err != nil {
				t.err = err.Error()
				return
			}
			t.err = ""
			t.adopt(src, info)
		}
	})
}

// redisActivity reads the server's INFO and its clients.
func redisActivity(ctx context.Context, kv *db.KV) (map[string]string, dataview.Source, error) {
	var src dataview.Source
	raw, err := kv.Client.Do(ctx, kv.Client.B().Info().Build()).ToString()
	if err != nil {
		return nil, src, err
	}
	info := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, ":"); ok && !strings.HasPrefix(line, "#") {
			info[k] = v
		}
	}
	list, err := kv.Client.Do(ctx, kv.Client.B().ClientList().Build()).ToString()
	if err != nil {
		return info, src, nil // CLIENT LIST may be forbidden by the ACL
	}
	names := []string{"id", "addr", "name", "age", "idle", "db", "cmd", "user"}
	for _, n := range names {
		src.Cols = append(src.Cols, db.ColumnInfo{Name: n})
	}
	for _, line := range strings.Split(strings.TrimSpace(list), "\n") {
		fields := map[string]string{}
		for _, f := range strings.Fields(line) {
			if k, v, ok := strings.Cut(f, "="); ok {
				fields[k] = v
			}
		}
		row := make([]any, len(names))
		for i, n := range names {
			row[i] = fields[n]
		}
		src.Rows = append(src.Rows, row)
	}
	return info, src, nil
}

// act stops a row's query or ends its session, once the user confirms.
func (t *activityTab) act(row []any, terminate bool) {
	var stmt, title string
	switch {
	case t.conn.KV != nil:
		stmt, title = "CLIENT KILL ID "+db.Display(row[0]), "Disconnect client "+db.Display(row[0])+"?"
	case terminate:
		stmt, title = t.spec.kill(row), "End this session?"
	default:
		stmt, title = t.spec.stop(row), "Cancel this query?"
	}
	v := safety.Verdict{Confirm: true, Reasons: []string{"This acts on another user's work on " + t.conn.Config.Name + ":\n" + t.describe(row)}}
	if t.conn.Config.ReadOnly {
		v.Reasons = append(v.Reasons, "The connection is read-only, but stopping queries is not a write: the server decides whether you may.")
	}
	cn := t.conn
	t.a.AskConfirm(cn, v, title, "Stop", stmt, func() {
		kv, pool, cfg := cn.KV, cn.DB, cn.Config // read on the main thread
		t.a.Background(func() func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var err error
			switch {
			case kv != nil:
				_, err = kv.Do(ctx, strings.Fields(stmt))
			case pool != nil:
				_, err = pool.SQL.ExecContext(ctx, stmt)
			default:
				err = errors.New("not connected")
			}
			t.a.RecordRun(cfg, audit.KindKill, "", stmt, -1, 0, err)
			return func() {
				if err != nil {
					t.a.ShowError("Could not stop it", err.Error())
					return
				}
				t.a.toast = &pendingToast{text: "Done: " + stmt}
				t.refresh()
			}
		})
	})
}

func (t *activityTab) View(c *ui.Context) {
	a := t.a
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	if t.auto && !t.loading && c.Now().Sub(t.loadedAt) >= 2*time.Second {
		t.refresh()
	}
	if t.auto {
		c.After(time.Second)
	}
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(6, 10).Gap(10).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Text(c, "Server activity").Bold()
			ui.Text(c, fmt.Sprintf("%d sessions", len(t.src.Rows))).FontSize(12).TextColor(pal.Muted)
			if t.loading {
				ui.Spinner(c).Size(12, 12)
			}
			ui.Spacer(c)
			ui.Checkbox(c, &t.auto, "Refresh every 2 seconds")
			if widgets.ToolButton(c, widgets.IconRefresh, "Refresh", "Read the activity now").Clicked() {
				t.refresh()
			}
			row := t.selected()
			if row != nil {
				if t.conn.KV != nil {
					if ui.Button(c, "Disconnect Client").Clicked() {
						t.act(row, true)
					}
				} else {
					if t.spec.stop != nil && ui.Button(c, "Cancel Query").Clicked() {
						t.act(row, false)
					}
					if t.spec.kill != nil && ui.Button(c, "End Session").Clicked() {
						t.act(row, true)
					}
				}
			}
		})
		if t.err != "" {
			ui.Text(c, t.err).TextColor(th.Danger).Padding(12).Selectable()
		}
		if t.info != nil {
			t.redisMetrics(c)
		}
		if t.src.Cols != nil {
			t.grid.View(c, a, &t.src)
		}
	})
}

func (t *activityTab) selected() []any {
	if t.grid.SelRow < 0 || t.grid.SelRow >= len(t.grid.Order) {
		t.selID = ""
		return nil
	}
	r := t.grid.Order[t.grid.SelRow]
	if r >= len(t.src.Rows) {
		return nil
	}
	t.selID = db.Display(t.src.Rows[r][0])
	return t.src.Rows[r]
}

// describe writes a session's row for a confirmation: who, and what.
func (t *activityTab) describe(row []any) string {
	var parts []string
	for i, col := range t.src.Cols {
		if i < len(row) && row[i] != nil && db.Display(row[i]) != "" {
			parts = append(parts, col.Name+": "+widgets.OneLine(db.Display(row[i]), 160))
		}
	}
	return strings.Join(parts, "\n")
}

// redisMetrics shows the figures of INFO that say how the server is.
func (t *activityTab) redisMetrics(c *ui.Context) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	tiles := [][2]string{
		{"Version", t.info["redis_version"]},
		{"Uptime", uptime(t.info["uptime_in_seconds"])},
		{"Clients", t.info["connected_clients"]},
		{"Memory", t.info["used_memory_human"]},
		{"Peak memory", t.info["used_memory_peak_human"]},
		{"Ops per second", t.info["instantaneous_ops_per_sec"]},
		{"Hit rate", hitRate(t.info["keyspace_hits"], t.info["keyspace_misses"])},
		{"Role", t.info["role"]},
	}
	if t.conn.Config.Redis.Mode == db.RedisCluster {
		ui.Text(c, "These are the figures, and the clients, of one node of the cluster.").FontSize(12).TextColor(pal.Muted).Padding(12, 12, 0, 12)
	}
	ui.Row(c).Padding(12).Gap(10).Wrap().Children(func() {
		for _, tl := range tiles {
			ui.Column(c).Width(150).Padding(10, 12).Gap(4).Radius(8).Border(1, th.Border).Children(func() {
				ui.Text(c, tl[0]).FontSize(11).TextColor(pal.Muted)
				ui.Text(c, tl[1]).FontSize(18).Bold().SingleLine()
			})
		}
	})
	var spaces []string
	for k, v := range t.info {
		if strings.HasPrefix(k, "db") && strings.Contains(v, "keys=") {
			spaces = append(spaces, k+"  "+v)
		}
	}
	sort.Strings(spaces)
	if len(spaces) > 0 {
		ui.Text(c, strings.Join(spaces, "\n")).Font(widgets.MonoFont).FontSize(12).TextColor(pal.Muted).Padding(0, 12, 8, 12)
	}
}

func uptime(s string) string {
	n, err := strconv.Atoi(s)
	if err != nil {
		return s
	}
	d := time.Duration(n) * time.Second
	if d > 48*time.Hour {
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
	return d.String()
}

func hitRate(hits, misses string) string {
	h, err1 := strconv.ParseFloat(hits, 64)
	m, err2 := strconv.ParseFloat(misses, 64)
	if err1 != nil || err2 != nil || h+m == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", h/(h+m)*100)
}

// openActivity opens a connection's server activity.
func (a *App) openActivity(cn *connection.Conn) {
	if _, ok := activityOf(cn.Config.Engine); !ok && cn.Config.Engine != db.Redis {
		a.ShowError("No server activity", cn.Config.Engine.Label()+" runs inside this app: there is no server to watch.")
		return
	}
	for i, t := range a.tabs {
		if at, ok := t.(*activityTab); ok && at.conn == cn {
			a.active = i
			return
		}
	}
	a.Connect(cn, func() { a.AddTab(newActivityTab(a, cn)) })
}

// adopt shows a refresh. The rows come back in another order: the one
// chosen stays chosen by its ID, never by its place, which another
// session may now hold.
func (t *activityTab) adopt(src dataview.Source, info map[string]string) {
	t.info, t.src = info, src
	t.grid.SelRow = -1
	if t.selID == "" {
		return
	}
	for pos, data := range t.grid.ViewOrder(&t.src) {
		if data < len(src.Rows) && db.Display(src.Rows[data][0]) == t.selID {
			t.grid.SelRow = pos
		}
	}
	if t.grid.SelRow < 0 {
		t.selID = "" // it ended
	}
}
