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

// activityPage is a page of a server's activity: what a statement lists,
// and how to stop what a row runs.
type activityPage struct {
	name  string
	query string
	// live pages read again every 2 seconds; the others when asked, as
	// their statements weigh on the server.
	live bool
	// stop and kill return the statement that cancels the query of a row,
	// and the one that ends its session, nil when the page has none. A
	// row's first column names its session.
	stop func(row []any) string
	kill func(row []any) string
	// hint says what the page needs of the server, when it fails.
	hint string
	// metrics shows the page's one row as tiles, with rates between reads.
	metrics bool
}

const (
	pgStop    = "SELECT pg_cancel_backend(%s)"
	pgKill    = "SELECT pg_terminate_backend(%s)"
	mysqlStop = "KILL QUERY %s"
	mysqlKill = "KILL %s"
	// activityMax is how many rows a page shows.
	activityMax = 5000
)

// sessionStatement returns the statement acting on the session a row's
// first column names.
func sessionStatement(format string) func(row []any) string {
	return func(r []any) string { return fmt.Sprintf(format, db.Display(r[0])) }
}

// activityPages are the pages of an engine's server activity, none for
// one that runs inside the app.
func activityPages(e db.Engine) []activityPage {
	switch e {
	case db.Postgres:
		return []activityPage{{
			name: "Sessions", live: true, stop: sessionStatement(pgStop), kill: sessionStatement(pgKill),
			query: `SELECT pid, usename AS "user", datname AS database, application_name AS application, client_addr::text AS client,
  state, date_trunc('second', now() - query_start)::text AS running_for, wait_event_type AS waiting_on, left(query, 400) AS query
FROM pg_stat_activity WHERE pid <> pg_backend_pid() AND backend_type = 'client backend'
ORDER BY state = 'active' DESC, query_start NULLS LAST`,
		}, {
			name: "Locks", live: true, stop: sessionStatement(pgStop), kill: sessionStatement(pgKill),
			query: `SELECT l.pid, a.usename AS "user", l.locktype AS lock_type,
  coalesce(l.relation::regclass::text, l.transactionid::text, l.virtualxid) AS object, l.mode, l.granted,
  array_to_string(pg_blocking_pids(l.pid), ', ') AS waits_for, date_trunc('second', now() - a.xact_start)::text AS transaction_for,
  left(a.query, 300) AS query
FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
WHERE l.pid <> pg_backend_pid() AND a.backend_type = 'client backend' AND l.locktype <> 'virtualxid'
ORDER BY l.granted, a.xact_start`,
		}, {
			name: "Statistics",
			query: `SELECT left(query, 400) AS query, calls, round(total_exec_time::numeric, 1) AS total_ms, round(mean_exec_time::numeric, 2) AS mean_ms,
  round(max_exec_time::numeric, 1) AS max_ms, rows, shared_blks_hit + shared_blks_read AS blocks
FROM pg_stat_statements ORDER BY total_exec_time DESC LIMIT 500`,
			hint: "These come from the pg_stat_statements extension: add it to shared_preload_libraries, restart the server, and run CREATE EXTENSION pg_stat_statements.",
		}}
	case db.MySQL:
		return []activityPage{{
			name: "Sessions", live: true, stop: sessionStatement(mysqlStop), kill: sessionStatement(mysqlKill),
			query: `SELECT ID AS id, USER AS user, HOST AS host, DB AS db, COMMAND AS command, TIME AS seconds, STATE AS state, LEFT(INFO, 400) AS query
FROM information_schema.PROCESSLIST WHERE ID <> CONNECTION_ID() ORDER BY COMMAND = 'Sleep', TIME DESC`,
		}, {
			name: "Locks", live: true, stop: sessionStatement(mysqlStop), kill: sessionStatement(mysqlKill),
			query: `SELECT t.PROCESSLIST_ID AS id, t.PROCESSLIST_USER AS user, CONCAT_WS('.', l.OBJECT_SCHEMA, l.OBJECT_NAME) AS object,
  l.INDEX_NAME AS index_name, l.LOCK_TYPE AS lock_type, l.LOCK_MODE AS mode, l.LOCK_STATUS AS status,
  (SELECT GROUP_CONCAT(bt.PROCESSLIST_ID) FROM performance_schema.data_lock_waits w
   JOIN performance_schema.threads bt ON bt.THREAD_ID = w.BLOCKING_THREAD_ID
   WHERE w.REQUESTING_ENGINE_LOCK_ID = l.ENGINE_LOCK_ID) AS waits_for,
  LEFT(t.PROCESSLIST_INFO, 300) AS query
FROM performance_schema.data_locks l JOIN performance_schema.threads t ON t.THREAD_ID = l.THREAD_ID
WHERE t.PROCESSLIST_ID <> CONNECTION_ID()
UNION ALL
SELECT t.PROCESSLIST_ID, t.PROCESSLIST_USER, CONCAT_WS('.', m.OBJECT_SCHEMA, m.OBJECT_NAME), NULL, CONCAT('METADATA ', m.OBJECT_TYPE),
  m.LOCK_TYPE, m.LOCK_STATUS, NULL, LEFT(t.PROCESSLIST_INFO, 300)
FROM performance_schema.metadata_locks m JOIN performance_schema.threads t ON t.THREAD_ID = m.OWNER_THREAD_ID
WHERE t.PROCESSLIST_ID IS NOT NULL AND t.PROCESSLIST_ID <> CONNECTION_ID() AND m.OBJECT_TYPE IN ('TABLE', 'SCHEMA')
ORDER BY status = 'GRANTED', id`,
			hint: "Locks come from performance_schema, which the server must have on, and the user may read.",
		}, {
			name: "Statistics",
			query: `SELECT LEFT(DIGEST_TEXT, 400) AS query, SCHEMA_NAME AS db, COUNT_STAR AS calls, ROUND(SUM_TIMER_WAIT / 1e9, 1) AS total_ms,
  ROUND(AVG_TIMER_WAIT / 1e9, 2) AS mean_ms, ROUND(MAX_TIMER_WAIT / 1e9, 1) AS max_ms, SUM_ROWS_SENT AS rows_sent, SUM_ROWS_EXAMINED AS rows_examined
FROM performance_schema.events_statements_summary_by_digest WHERE DIGEST_TEXT IS NOT NULL
ORDER BY SUM_TIMER_WAIT DESC LIMIT 500`,
			hint: "Statistics come from performance_schema, which the server must have on, and the user may read.",
		}}
	case db.ClickHouse:
		return []activityPage{{
			name: "Sessions", live: true,
			stop: func(r []any) string {
				return "KILL QUERY WHERE query_id = " + db.Literal(db.ClickHouse, r[0]) + " ASYNC"
			},
			query: `SELECT query_id, user, round(elapsed, 1) AS seconds, read_rows, formatReadableSize(memory_usage) AS memory, left(query, 400) AS query
FROM system.processes WHERE query_id != queryID() ORDER BY elapsed DESC`,
		}, {
			name: "Statistics",
			query: `SELECT any(left(query, 400)) AS query, count() AS calls, round(sum(query_duration_ms)) AS total_ms, round(avg(query_duration_ms), 1) AS mean_ms,
  max(query_duration_ms) AS max_ms, sum(read_rows) AS read_rows, formatReadableSize(sum(read_bytes)) AS read, formatReadableSize(max(memory_usage)) AS peak_memory
FROM system.query_log WHERE type = 'QueryFinish' AND event_time > now() - INTERVAL 1 DAY
GROUP BY normalized_query_hash ORDER BY total_ms DESC LIMIT 500`,
			hint: "Statistics come from system.query_log, which the server keeps when log_queries is on.",
		}, {
			name: "Metrics", live: true, metrics: true,
			query: `SELECT uptime() AS uptime,
  (SELECT value FROM system.metrics WHERE metric = 'Query') AS queries,
  (SELECT sum(value) FROM system.metrics WHERE metric IN ('TCPConnection', 'HTTPConnection', 'MySQLConnection', 'PostgreSQLConnection')) AS connections,
  (SELECT value FROM system.metrics WHERE metric = 'MemoryTracking') AS memory,
  (SELECT value FROM system.metrics WHERE metric = 'Merge') AS merges,
  (SELECT count() FROM system.parts WHERE active) AS parts,
  (SELECT value FROM system.asynchronous_metrics WHERE metric = 'LoadAverage1') AS load,
  (SELECT value FROM system.events WHERE event = 'Query') AS queries_run,
  (SELECT value FROM system.events WHERE event = 'SelectedRows') AS rows_read,
  (SELECT value FROM system.events WHERE event = 'InsertedRows') AS rows_inserted`,
		}}
	case db.Redis:
		return []activityPage{{name: "Clients", live: true}}
	}
	return nil
}

// activityTab shows the sessions, locks and statistics of a server.
type activityTab struct {
	a     *App
	conn  *connection.Conn
	pages []activityPage
	page  int

	src      dataview.Source
	grid     *dataview.Grid
	err      string
	loading  bool
	auto     bool
	loadedAt time.Time
	// gen counts the pages shown, for a read to tell its page is gone.
	gen int

	// Redis: its INFO, by field.
	info map[string]string
	// metrics and before are the last two reads of a Metrics page, for
	// its rates, and when they were read.
	metrics, before     map[string]float64
	metricsAt, beforeAt time.Time
	// selID is the ID of the session chosen, which follows it across
	// refreshes.
	selID string
}

func newActivityTab(a *App, cn *connection.Conn) *activityTab {
	t := &activityTab{a: a, conn: cn, grid: dataview.NewGrid(), auto: true, pages: activityPages(cn.Config.Engine)}
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
	page, gen := t.pages[t.page], t.gen
	t.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var src dataview.Source
		var info map[string]string
		var err error
		switch {
		case kv != nil:
			info, src, err = redisActivity(ctx, kv)
		case pool != nil:
			var s *db.Session
			if s, err = pool.Session(ctx); err == nil {
				defer s.Close()
				var c *db.Cursor
				if c, err = s.Query(ctx, page.query); err == nil {
					src.Cols = c.Columns
					src.Rows, err = c.Fetch(activityMax)
					c.Close()
				}
			}
		default:
			err = errors.New("not connected")
		}
		return func() {
			if gen != t.gen {
				return // another page shows now, whose read this is not
			}
			t.loading, t.loadedAt = false, time.Now()
			if err != nil {
				t.err = err.Error()
				if page.hint != "" {
					t.err += "\n\n" + page.hint
				}
				return
			}
			t.err = ""
			if page.metrics {
				t.adoptMetrics(src)
				return
			}
			t.adopt(src, info)
		}
	})
}

// showPage shows another page, read at once.
func (t *activityTab) showPage(i int) {
	t.page, t.gen = i, t.gen+1
	t.src, t.info, t.err, t.selID, t.metrics, t.before = dataview.Source{}, nil, "", "", nil, nil
	t.grid = dataview.NewGrid()
	t.loading = false // the read of the page before goes unseen
	t.refresh()
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
	page := t.pages[t.page]
	switch {
	case t.conn.KV != nil:
		stmt, title = "CLIENT KILL ID "+db.Display(row[0]), "Disconnect client "+db.Display(row[0])+"?"
	case terminate:
		stmt, title = page.kill(row), "End this session?"
	default:
		stmt, title = page.stop(row), "Cancel this query?"
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
	page := t.pages[t.page]
	if page.live && t.auto && !t.loading && c.Now().Sub(t.loadedAt) >= 2*time.Second {
		t.refresh()
	}
	if page.live && t.auto {
		c.After(time.Second)
	}
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(6, 10).Gap(10).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			if len(t.pages) > 1 {
				names := make([]string, len(t.pages))
				for i, p := range t.pages {
					names[i] = p.name
				}
				if ui.Segmented(c, &t.page, names...).Label("Page").Changed() {
					t.showPage(t.page)
				}
			} else {
				ui.Text(c, "Server activity").Bold()
			}
			if !page.metrics {
				ui.Text(c, fmt.Sprintf("%d rows", len(t.src.Rows))).FontSize(12).TextColor(pal.Muted)
			}
			if t.loading {
				ui.Spinner(c).Size(12, 12)
			}
			ui.Spacer(c)
			if page.live {
				ui.Checkbox(c, &t.auto, "Refresh every 2 seconds")
			}
			if widgets.ToolButton(c, widgets.IconRefresh, "Refresh", "Read the page now").Clicked() {
				t.refresh()
			}
			if row := t.selected(); row != nil {
				if t.conn.KV != nil {
					if ui.Button(c, "Disconnect Client").Clicked() {
						t.act(row, true)
					}
				} else {
					if page.stop != nil && ui.Button(c, "Cancel Query").Clicked() {
						t.act(row, false)
					}
					if page.kill != nil && ui.Button(c, "End Session").Clicked() {
						t.act(row, true)
					}
				}
			}
		})
		if t.err != "" {
			ui.Text(c, t.err).TextColor(th.Danger).Padding(12).Selectable()
		}
		switch {
		case page.metrics:
			t.metricsView(c)
		case t.info != nil:
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

// adoptMetrics keeps a Metrics page's read, and the one before it, for
// the rates between them.
func (t *activityTab) adoptMetrics(src dataview.Source) {
	if len(src.Rows) != 1 {
		t.err = fmt.Sprintf("the metrics came back as %d rows", len(src.Rows))
		return
	}
	m := map[string]float64{}
	for i, col := range src.Cols {
		m[col.Name], _ = strconv.ParseFloat(db.Display(src.Rows[0][i]), 64)
	}
	t.before, t.beforeAt = t.metrics, t.metricsAt
	t.metrics, t.metricsAt = m, time.Now()
}

// metricsView shows a server's figures as tiles: what it holds now, and
// what it does per second, between the last two reads.
func (t *activityTab) metricsView(c *ui.Context) {
	m := t.metrics
	if m == nil {
		return
	}
	rate := func(name string) string {
		secs := t.metricsAt.Sub(t.beforeAt).Seconds()
		if t.before == nil || secs <= 0 {
			return "…"
		}
		return widgets.HumanCount(int64((m[name]-t.before[name])/secs + 0.5))
	}
	tilesView(c, [][2]string{
		{"Uptime", uptime(strconv.Itoa(int(m["uptime"])))},
		{"Queries running", strconv.Itoa(int(m["queries"]))},
		{"Connections", strconv.Itoa(int(m["connections"]))},
		{"Memory", widgets.HumanBytes(int64(m["memory"]))},
		{"Merges running", strconv.Itoa(int(m["merges"]))},
		{"Active parts", widgets.HumanCount(int64(m["parts"]))},
		{"Load average", strconv.FormatFloat(m["load"], 'f', 2, 64)},
		{"Queries per second", rate("queries_run")},
		{"Rows read per second", rate("rows_read")},
		{"Rows inserted per second", rate("rows_inserted")},
	})
}

// tilesView shows figures as tiles, a name above each.
func tilesView(c *ui.Context, tiles [][2]string) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Row(c).Padding(12).Gap(10).Wrap().Children(func() {
		for _, tl := range tiles {
			ui.Column(c).Width(170).Padding(10, 12).Gap(4).Radius(8).Border(1, th.Border).Label(tl[0]).Children(func() {
				ui.Text(c, tl[0]).FontSize(11).TextColor(pal.Muted)
				ui.Text(c, tl[1]).FontSize(18).Bold().SingleLine()
			})
		}
	})
}

// redisMetrics shows the figures of INFO that say how the server is.
func (t *activityTab) redisMetrics(c *ui.Context) {
	pal := widgets.PaletteOf(c)
	if t.conn.Config.Redis.Mode == db.RedisCluster {
		ui.Text(c, "These are the figures, and the clients, of one node of the cluster.").FontSize(12).TextColor(pal.Muted).Padding(12, 12, 0, 12)
	}
	tilesView(c, [][2]string{
		{"Version", t.info["redis_version"]},
		{"Uptime", uptime(t.info["uptime_in_seconds"])},
		{"Clients", t.info["connected_clients"]},
		{"Memory", t.info["used_memory_human"]},
		{"Peak memory", t.info["used_memory_peak_human"]},
		{"Ops per second", t.info["instantaneous_ops_per_sec"]},
		{"Hit rate", hitRate(t.info["keyspace_hits"], t.info["keyspace_misses"])},
		{"Role", t.info["role"]},
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
	if len(activityPages(cn.Config.Engine)) == 0 {
		a.ShowError("No server activity", cn.Config.Engine.Label()+" runs inside this app: there is no server to watch.")
		return
	}
	if a.ActivateTab(func(t widgets.Tab) bool { at, ok := t.(*activityTab); return ok && at.conn == cn }) {
		return
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
