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
	"dgopher/internal/lists"
	"dgopher/internal/safety"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// activityMax is how many rows a page shows.
const activityMax = 5000

// activityTab shows the sessions, locks and statistics of a server.
type activityTab struct {
	a     *App
	conn  *connection.Conn
	pages []db.ActivityPage
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
	t := &activityTab{a: a, conn: cn, grid: dataview.NewGrid(), auto: true, pages: cn.Config.Engine.ActivityPages()}
	t.refresh()
	return t
}

func (t *activityTab) Title() string                { return t.conn.Config.Name + " · activity" }
func (t *activityTab) Connection() *connection.Conn { return t.conn }
func (t *activityTab) Icon() *ui.SVG                { return widgets.IconClock }
func (t *activityTab) CloseReason() string          { return "" }
func (t *activityTab) Close()                       {}

func (t *activityTab) refresh() {
	if t.loading {
		return
	}
	t.loading = true
	kv, pool := t.conn.KV(), t.conn.DB() // read on the main thread
	page, gen := t.pages[t.page], t.gen
	dataview.BackgroundResetOnPanic(t.a, func() {
		if gen == t.gen {
			t.loading = false
		}
	}, func() func() {
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
				if c, err = s.Query(ctx, page.Query); err == nil {
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
				if page.Hint != "" {
					t.err += "\n\n" + page.Hint
				}
				return
			}
			t.err = ""
			if page.Metrics {
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
		row := lists.Map(names, func(n string) any { return fields[n] })
		src.Rows = append(src.Rows, row)
	}
	return info, src, nil
}

// act stops a row's query or ends its session, once the user confirms.
func (t *activityTab) act(row []any, terminate bool) {
	var stmt, title string
	page := t.pages[t.page]
	redis := t.conn.Config.Engine == db.Redis
	switch {
	case redis:
		stmt, title = "CLIENT KILL ID "+db.Display(row[0]), "Disconnect client "+db.Display(row[0])+"?"
	case terminate:
		stmt, title = page.Kill(row), "End this session?"
	default:
		stmt, title = page.Stop(row), "Cancel this query?"
	}
	if stmt == "" {
		t.a.ShowError("Could not stop it", page.NoSession)
		return
	}
	// Stopping another session's work is exempt from read-only, which the
	// server enforces through its own privileges; the rest of the policy holds.
	policy := t.conn.Config
	policy.ReadOnly = false
	var verdict safety.Verdict
	if redis {
		verdict = safety.ReviewRedis(&policy, t.conn.KV(), strings.Fields(stmt))
	} else {
		verdict = safety.ReviewSQL(&policy, safety.Analyze(&policy, []string{stmt}))
	}
	if verdict.Blocked != "" {
		t.a.RecordBlocked(t.conn, verdict.Blocked, stmt)
		t.a.ShowError("Could not stop it", verdict.Blocked)
		return
	}
	v := safety.Verdict{Confirm: true, Reasons: []string{"This acts on another user's work on " + t.conn.Config.Name + ":\n" + t.describe(row)}}
	if t.conn.Config.ReadOnly {
		v.Reasons = append(v.Reasons, "The connection is read-only, but stopping queries is not a write: the server decides whether you may.")
	}
	v.Reasons = append(v.Reasons, verdict.Reasons...)
	v.TypeName = verdict.TypeName || (terminate && t.conn.Config.Env == db.Production)
	cn := t.conn
	t.a.AskConfirm(cn, v, title, "Stop", stmt, func() {
		kv, pool, cfg := cn.KV(), cn.DB(), cn.Config // read on the main thread
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
				t.a.Toast("Done: "+stmt, "", nil)
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
	if page.Live && t.auto && !t.loading && c.Now().Sub(t.loadedAt) >= 2*time.Second {
		t.refresh()
	}
	if page.Live && t.auto {
		c.After(time.Second)
	}
	ui.Column(c).Grow(1).Children(func() {
		widgets.Bar(c, "Server activity", func() {
			if len(t.pages) > 1 {
				names := lists.Map(t.pages, func(p db.ActivityPage) string { return p.Name })
				if ui.Segmented(c, &t.page, names...).Label("Page").FontSize(widgets.ToolFontSize).Changed() {
					t.showPage(t.page)
				}
			} else {
				ui.Text(c, "Server activity").Bold()
			}
			if !page.Metrics {
				ui.Text(c, widgets.Count(len(t.src.Rows), "row")).FontSize(12).TextColor(pal.Muted)
			}
			ui.Spacer(c)
			if page.Live {
				ui.Checkbox(c, &t.auto, "Refresh every 2 seconds")
			}
			if widgets.ToolButton(c, widgets.IconRefresh, "Refresh", "Read the page now").Clicked() {
				t.refresh()
			}
			if row := t.selected(); row != nil {
				if t.conn.KV() != nil {
					if widgets.Appear(ui.Button(c.Key("disconnect-client"), "Disconnect Client")).Clicked() {
						t.act(row, true)
					}
				} else {
					if page.Stop != nil && widgets.Appear(ui.Button(c.Key("cancel-query"), "Cancel Query")).Clicked() {
						t.act(row, false)
					}
					if page.Kill != nil && widgets.Appear(ui.Button(c.Key("end-session"), "End Session")).Clicked() {
						t.act(row, true)
					}
				}
			}
		})
		if t.err != "" {
			ui.Text(c, t.err).TextColor(th.Danger).Padding(12).Selectable()
		} else if page.EmptyHint != "" && t.src.Cols != nil && len(t.src.Rows) == 0 {
			ui.Text(c, page.EmptyHint).TextColor(pal.Muted).Padding(12).Selectable()
		}
		switch {
		case page.Metrics:
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
	if len(cn.Config.Engine.ActivityPages()) == 0 {
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
