package redis

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/redact"
	"dgopher/internal/safety"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// The panels below the keys.
const (
	panelConsole = iota
	panelMonitor
	panelSlowLog
	panelMemory
	panelPubSub
	panelSearch
)

const (
	// monitorKeep is how many commands the monitor keeps, the latest.
	monitorKeep = 5000
	// slowLogRead is how many of the latest slow commands are read of
	// each server.
	slowLogRead = 128
	// largestKeys is how many of the largest keys an analysis lists.
	largestKeys = 50
)

// memoryLimits are how many keys an analysis may read, as its select
// offers them.
var memoryLimits = []int64{10_000, 100_000, 1_000_000}

type monitorState struct {
	cancel  context.CancelFunc // set while it runs
	lines   []db.MonitorLine
	filter  string
	allDBs  bool // the commands of every database, not only the connection's
	err     string
	list    ui.ListState
	started time.Time
}

type slowLogState struct {
	asked   bool
	loading bool
	entries []db.SlowEntry
	err     string
	list    ui.ListState
}

type memoryState struct {
	limit   int // an index of memoryLimits
	cancel  context.CancelFunc
	read    int64
	report  *db.MemoryReport
	byType  []string // the report's types, the largest first
	err     string
	largest ui.ListState
	row     int
	spaces  ui.ListState
	types   ui.ListState
}

func (r *Tab) panelView(c *ui.Context) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Column(c).Fill().Background(pal.EditorBg).Children(func() {
		ui.Row(c).Padding(4, 10).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Segmented(c, &r.panel, "Console", "Monitor", "Slow Log", "Memory", "Pub/Sub", "Search").Label("Panel")
			ui.Spacer(c)
			switch r.panel {
			case panelConsole:
				r.consoleActions(c)
			case panelMonitor:
				r.monitorActions(c)
			case panelSlowLog:
				r.slowLogActions(c)
			case panelMemory:
				r.memoryActions(c)
			case panelPubSub:
				r.pubSubActions(c)
			case panelSearch:
				r.searchActions(c)
			}
		})
		switch r.panel {
		case panelConsole:
			r.consoleView(c)
		case panelMonitor:
			r.monitorView(c)
		case panelSlowLog:
			r.slowLogView(c)
		case panelMemory:
			r.memoryView(c)
		case panelPubSub:
			r.pubSubView(c)
		case panelSearch:
			r.searchView(c)
		}
	})
}

// startMonitor shows the commands the server runs, once agreed on
// production, where watching them slows a busy server.
func (r *Tab) startMonitor() {
	cfg := r.conn.Config
	if cfg.Env != db.Production {
		r.runMonitor()
		return
	}
	v := safety.Verdict{Confirm: true, Reasons: []string{"MONITOR makes the server send the app every command it runs, which slows a busy server while it watches."}}
	r.a.AskConfirm(r.conn, v, "Monitor "+cfg.Name+"?", "Monitor", "MONITOR", r.runMonitor)
}

func (r *Tab) runMonitor() {
	m := &r.monitor
	if m.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel, m.err, m.started = cancel, "", time.Now()
	kv, cfg := r.conn.KV, r.conn.Config
	lines := feed[db.MonitorLine]{keep: monitorKeep}
	go func() {
		defer dataview.RecoverBackground(r.a.Post, r.a.ShowError, func() { m.cancel = nil })
		defer cancel()
		done := make(chan struct{})
		go lines.run(done, func(batch []db.MonitorLine) {
			// Shown through the redaction the console's commands go
			// through: older servers tell CONFIG SET requirepass as sent.
			for i := range batch {
				if args, err := db.SplitCommand(batch[i].Command); err == nil && len(args) > 0 {
					batch[i].Command = redact.Redis(args)
				}
			}
			r.a.Post(func() { m.lines = keepLatest(m.lines, batch, monitorKeep) })
		})
		start := time.Now()
		err := kv.Monitor(ctx, lines.add)
		close(done)
		r.a.RecordRun(cfg, audit.KindCommand, cfg.Database, "MONITOR", -1, time.Since(start), err)
		r.a.Post(func() {
			m.cancel = nil
			if err != nil {
				m.err = err.Error()
			}
		})
	}()
}

func (r *Tab) monitorActions(c *ui.Context) {
	m := &r.monitor
	pal := widgets.PaletteOf(c)
	if m.cancel != nil {
		ui.Spinner(c).Size(12, 12)
		ui.Text(c, widgets.Count(len(m.lines), "command")).FontSize(12).TextColor(pal.Muted)
		if ui.Link(c, "Stop", "").FontSize(12).Clicked() {
			m.cancel()
		}
	} else if ui.Link(c, "Start", "").FontSize(12).Clicked() {
		r.startMonitor()
	}
	if ui.Link(c, "Clear", "").FontSize(12).Clicked() {
		m.lines = nil
	}
}

func (r *Tab) monitorView(c *ui.Context) {
	m := &r.monitor
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(6, 10).Gap(10).AlignItems(ui.Center).Children(func() {
			widgets.SearchBox(c, &m.filter, "Filter commands", 260)
			ui.Checkbox(c, &m.allDBs, "Every database")
		})
		if m.err != "" {
			ui.Text(c, m.err).TextColor(th.Danger).Padding(4, 12).Selectable()
		}
		if m.cancel == nil && len(m.lines) == 0 {
			ui.Text(c, "Start shows each command the server runs as it runs it, from every client. It slows a busy server while it watches.").
				FontSize(12.5).TextColor(pal.Muted).Padding(8, 12)
			return
		}
		ownDB := r.conn.Config.Database
		if ownDB == "" {
			ownDB = "0"
		}
		filter := strings.ToLower(m.filter)
		shown := make([]int, 0, len(m.lines))
		for i, l := range m.lines {
			if (m.allDBs || l.DB == ownDB) && (filter == "" || strings.Contains(strings.ToLower(l.Command), filter)) {
				shown = append(shown, i)
			}
		}
		m.list.FollowEnd = true
		ui.List(c, &m.list, len(shown), func(i int) {
			l := m.lines[shown[i]]
			ui.Row(c).Padding(1, 12).Gap(10).Children(func() {
				ui.Text(c, l.At.Format("15:04:05.000")).Font(widgets.MonoFont).FontSize(11.5).TextColor(pal.Muted)
				where := l.Client
				if m.allDBs {
					where = "db" + l.DB + " " + where
				}
				ui.Text(c, where).FontSize(11.5).TextColor(pal.Muted).Width(170).SingleLine()
				ui.Text(c, l.Command).Font(widgets.MonoFont).FontSize(12).SingleLine().Grow(1).Shrink(1).Selectable()
			})
		}).Grow(1).Label("Commands monitored")
	})
}

// loadSlowLog reads the latest slow commands.
func (r *Tab) loadSlowLog() {
	s := &r.slow
	s.asked, s.loading, s.err = true, true, ""
	kv := r.conn.KV
	dataview.BackgroundResetOnPanic(r.a, func() { s.loading = false }, func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		entries, err := kv.SlowLog(ctx, slowLogRead)
		return func() {
			s.loading = false
			if err != nil {
				s.err = err.Error()
				return
			}
			s.entries = entries
		}
	})
}

func (r *Tab) slowLogActions(c *ui.Context) {
	s := &r.slow
	if s.loading {
		ui.Spinner(c).Size(12, 12)
	}
	if ui.Link(c, "Refresh", "").FontSize(12).Clicked() {
		r.loadSlowLog()
	}
	if !r.conn.Config.ReadOnly && ui.Link(c, "Reset", "").FontSize(12).Clicked() {
		r.resetSlowLog()
	}
}

// resetSlowLog empties the slow log of every master, as the policy says.
func (r *Tab) resetSlowLog() {
	cfg := r.conn.Config
	args := []string{"SLOWLOG", "RESET"}
	v := safety.ReviewRedis(&cfg, r.conn.KV, args)
	if v.Blocked != "" {
		r.a.RecordBlocked(r.conn, v.Blocked, "SLOWLOG RESET")
		r.slow.err = v.Blocked
		return
	}
	run := func() {
		kv := r.conn.KV
		r.a.Background(func() func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			start := time.Now()
			err := kv.ResetSlowLog(ctx)
			r.a.RecordRun(cfg, audit.KindCommand, cfg.Database, "SLOWLOG RESET", -1, time.Since(start), err)
			return func() {
				if err != nil {
					r.slow.err = err.Error()
					return
				}
				r.loadSlowLog()
			}
		})
	}
	if v.Confirm {
		r.a.AskConfirm(r.conn, v, "Reset the slow log of "+cfg.Name+"?", "Reset", "SLOWLOG RESET", run)
		return
	}
	run()
}

func (r *Tab) slowLogView(c *ui.Context) {
	s := &r.slow
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	if !s.asked {
		r.loadSlowLog()
	}
	if s.err != "" {
		ui.Text(c, s.err).TextColor(th.Danger).Padding(8, 12).Selectable()
		return
	}
	if !s.loading && len(s.entries) == 0 {
		ui.Text(c, "No command ran slower than the server's slowlog-log-slower-than.").FontSize(12.5).TextColor(pal.Muted).Padding(8, 12)
		return
	}
	cols := []ui.TableColumn{{Title: "Took", Width: 90, Align: ui.End}, {Title: "When", Width: 150}, {Title: "Command"}, {Title: "Client", Width: 200}}
	ui.Table(c, &s.list, cols, len(s.entries), func(row, col int) {
		e := s.entries[row]
		var text string
		switch col {
		case 0:
			text = e.Duration.String()
		case 1:
			text = e.At.Format(time.DateTime)
		case 2:
			text = redact.Redis(e.Args)
			if e.Node != "" {
				text = e.Node + "  " + text
			}
		case 3:
			text = e.Client
		}
		ui.Text(c, text).Font(widgets.MonoFont).FontSize(12).SingleLine()
	}).Grow(1).Label("Slow commands")
}

// startAnalysis analyses the keys' memory, once agreed on production,
// where reading many keys slows a busy server.
func (r *Tab) startAnalysis() {
	cfg := r.conn.Config
	if cfg.Env != db.Production {
		r.analyseMemory()
		return
	}
	n := widgets.HumanCount(memoryLimits[r.memory.limit])
	v := safety.Verdict{Confirm: true, Reasons: []string{"The analysis scans up to " + n + " keys and asks the size and TTL of each, which adds to a busy server's load while it runs."}}
	r.a.AskConfirm(r.conn, v, "Analyse the keys of "+cfg.Name+"?", "Analyse", "SCAN, TYPE, MEMORY USAGE and PTTL of up to "+n+" keys", r.analyseMemory)
}

// analyseMemory reads the keys' sizes and TTLs, up to the limit chosen.
func (r *Tab) analyseMemory() {
	m := &r.memory
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel, m.read, m.err, m.row = cancel, 0, "", -1
	kv, sep, limit := r.conn.KV, r.conn.Config.Separator(), memoryLimits[m.limit]
	dataview.BackgroundResetOnPanic(r.a, func() { m.cancel = nil }, func() func() {
		defer cancel()
		report, err := kv.AnalyseMemory(ctx, sep, limit, largestKeys, func(n int64) { r.a.Post(func() { m.read = n }) })
		if ctx.Err() != nil {
			err = errors.New("cancelled")
		}
		return func() {
			m.cancel = nil
			if err != nil {
				m.err = err.Error()
				return
			}
			m.report, m.byType = report, typesBySize(report)
		}
	})
}

func (r *Tab) memoryActions(c *ui.Context) {
	m := &r.memory
	pal := widgets.PaletteOf(c)
	if m.cancel != nil {
		ui.Spinner(c).Size(12, 12)
		ui.Text(c, widgets.HumanCount(m.read)+" keys read").FontSize(12).TextColor(pal.Muted)
		if ui.Link(c, "Stop", "").FontSize(12).Clicked() {
			m.cancel()
		}
		return
	}
	labels := make([]string, len(memoryLimits))
	for i, n := range memoryLimits {
		labels[i] = "Up to " + widgets.HumanCount(n) + " keys"
	}
	ui.Segmented(c, &m.limit, labels...).Label("Keys to read")
	if ui.Link(c, "Analyse", "").FontSize(12).Clicked() {
		r.startAnalysis()
	}
}

func (r *Tab) memoryView(c *ui.Context) {
	m := &r.memory
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	if m.err != "" {
		ui.Text(c, m.err).TextColor(th.Danger).Padding(8, 12).Selectable()
	}
	rep := m.report
	if rep == nil {
		if m.cancel == nil {
			ui.Text(c, "Analyse reads each key's type, size and TTL, as SCAN finds them, and sums them: the largest keys, the namespaces before the key separator, and when keys expire.").
				FontSize(12.5).TextColor(pal.Muted).Padding(8, 12)
		}
		return
	}
	share := func(n int64) string {
		if rep.Memory == 0 {
			return ""
		}
		return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(rep.Memory))
	}
	ui.Column(c).Grow(1).Padding(8, 12).Gap(8).Children(func() {
		w := rep.ExpiringWithin
		summary := fmt.Sprintf("%s of %s keys read, taking %s. %s expire: %s within an hour, %s within a day, %s within a week, %s later.",
			widgets.HumanCount(rep.Keys), widgets.HumanCount(rep.Total), widgets.HumanBytes(rep.Memory), widgets.HumanCount(rep.Expiring),
			widgets.HumanCount(w[0]), widgets.HumanCount(w[1]), widgets.HumanCount(w[2]), widgets.HumanCount(w[3]))
		ui.Text(c, summary).FontSize(12.5)
		ui.Row(c).Grow(1).Gap(12).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Grow(2).Gap(4).Children(func() {
				ui.Text(c, "Largest keys").FontSize(12).Bold()
				m.largest.Selected = &m.row
				cols := []ui.TableColumn{{Title: "Key"}, {Title: "Type", Width: 60}, {Title: "Size", Width: 80, Align: ui.End}, {Title: "TTL", Width: 90, Align: ui.End}}
				t := ui.Table(c, &m.largest, cols, len(rep.Largest), func(row, col int) {
					k := rep.Largest[row]
					text := k.Key
					switch col {
					case 1:
						text = k.Type
					case 2:
						text = widgets.HumanBytes(k.Memory)
					case 3:
						text = ""
						if k.TTL >= 0 {
							text = k.TTL.Round(time.Second).String()
						}
					}
					ui.Text(c, text).Font(widgets.MonoFont).FontSize(12).SingleLine()
				}).Grow(1).Label("Largest keys")
				if t.Changed() && m.row >= 0 && m.row < len(rep.Largest) {
					r.open(rep.Largest[m.row].Key)
				}
			})
			ui.Column(c).Grow(1).Gap(4).Children(func() {
				ui.Text(c, "Namespaces").FontSize(12).Bold()
				cols := []ui.TableColumn{{Title: "Prefix"}, {Title: "Keys", Width: 70, Align: ui.End}, {Title: "Size", Width: 80, Align: ui.End}, {Title: "Share", Width: 60, Align: ui.End}}
				ui.Table(c, &m.spaces, cols, len(rep.Namespaces), func(row, col int) {
					ns := rep.Namespaces[row]
					text := ns.Prefix + r.conn.Config.Separator()
					switch {
					case col == 0 && ns.Prefix == "":
						text = "(no separator)"
					case col == 0 && ns.Prefix == db.OtherNamespaces:
						text = "(other namespaces)"
					case col == 1:
						text = widgets.HumanCount(ns.Keys)
					case col == 2:
						text = widgets.HumanBytes(ns.Memory)
					case col == 3:
						text = share(ns.Memory)
					}
					ui.Text(c, text).Font(widgets.MonoFont).FontSize(12).SingleLine()
				}).Grow(1).Label("Namespaces")
				ui.Text(c, "Types").FontSize(12).Bold()
				types := m.byType
				cols = []ui.TableColumn{{Title: "Type"}, {Title: "Keys", Width: 70, Align: ui.End}, {Title: "Size", Width: 80, Align: ui.End}, {Title: "Share", Width: 60, Align: ui.End}}
				ui.Table(c, &m.types, cols, len(types), func(row, col int) {
					sum := rep.ByType[types[row]]
					text := types[row]
					switch col {
					case 1:
						text = widgets.HumanCount(sum.Keys)
					case 2:
						text = widgets.HumanBytes(sum.Memory)
					case 3:
						text = share(sum.Memory)
					}
					ui.Text(c, text).Font(widgets.MonoFont).FontSize(12).SingleLine()
				}).Height(150).Label("Types")
			})
		})
	})
}

// typesBySize is the report's types, the most memory first.
func typesBySize(rep *db.MemoryReport) []string {
	types := make([]string, 0, len(rep.ByType))
	for t := range rep.ByType {
		types = append(types, t)
	}
	slices.SortFunc(types, func(a, b string) int {
		if d := cmp.Compare(rep.ByType[b].Memory, rep.ByType[a].Memory); d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})
	return types
}
