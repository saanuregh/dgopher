package dashboard

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/params"
	"dgopher/internal/project"
	"dgopher/internal/safety"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// Host is what a dashboard needs of the app.
type Host interface {
	connection.Runner
	// ConnByID finds a connection by its ID in the app.
	ConnByID(id string) *connection.Conn
	Connect(cn *connection.Conn, then func())
	ShowError(title, message string)
	Toast(text, action string, run func())
	RecordRun(cfg db.Config, kind, database, stmt string, rows int64, d time.Duration, err error)
	NewQueryTab(cn *connection.Conn, database, text string)
	// KeysTo reports whether a tab takes the shortcuts pressed.
	KeysTo(t widgets.Tab) bool
}

const (
	panelHeight      = 300
	valuePanelHeight = 140
	// paramsFile keeps the values each user gave the parameters of the
	// project's dashboards, in the project's own state: a value may be
	// no one else's to see.
	paramsFile = "dashboard-parameters.json"
)

// Tab shows a dashboard: its panels in a grid, their parameters, and its
// refresh.
type Tab struct {
	h       Host
	Project *project.Project
	Path    string
	d       *Dashboard
	disk    []byte // the file as last read or written

	panels  []*panelState // as d.Panels
	fields  []paramField  // the parameters, as typed
	values  map[string]params.Input
	editing bool
	dirty   bool // titles edited, saved as editing ends
	lastRun time.Time
	err     string
	refresh string // the refresh's choice, as its select shows it
	closed  bool
	// later are the changes of the list of panels asked while it was
	// drawn, made once it is.
	later []func()
}

// panelState is a panel's result, and its settings as edited.
type panelState struct {
	src     *dataview.Source
	chart   dataview.Chart
	applied bool                   // the panel's chart settings were applied to src
	shown   dataview.ChartSettings // the chart's settings as applied, or last saved
	err     string
	running bool
	cancel  context.CancelFunc
	at      time.Time
	took    time.Duration
	runs    int // counts the runs: an older one's result is dropped
	list    ui.ListState

	title   string // as edited
	sql     string
	view    string
	editSQL bool
}

type paramField struct {
	key, value, kind string
}

// Open opens a dashboard's file; its panels run once the tab shows, and
// their connections connect then, as an editor's do.
func Open(h Host, p *project.Project, path string) (*Tab, error) {
	d, disk, err := LoadFile(path)
	if err != nil {
		return nil, err
	}
	t := &Tab{h: h, Project: p, Path: path, d: d, disk: disk, refresh: refreshLabel(d.Refresh)}
	t.values = t.loadValues()
	t.syncPanels()
	t.syncFields()
	return t, nil
}

func (t *Tab) Title() string { return t.d.Name + " · dashboard" }

// Connection is none: a dashboard's panels are of any connection.
func (t *Tab) Connection() *connection.Conn { return nil }

func (t *Tab) CloseReason() string { return "" }

func (t *Tab) Close() {
	t.saveIfDirty()
	t.closed = true
	t.stop()
}

// stop cancels the panels' runs.
func (t *Tab) stop() {
	for _, p := range t.panels {
		if p.cancel != nil {
			p.cancel()
		}
	}
}

// Reload reads the dashboard's file again, as after a panel was added to
// it elsewhere, or a pull changed it.
func (t *Tab) Reload() {
	d, disk, err := LoadFile(t.Path)
	if err != nil {
		t.err = err.Error()
		return
	}
	t.stop()
	t.d, t.disk, t.panels, t.err, t.dirty = d, disk, nil, "", false
	t.refresh = refreshLabel(d.Refresh)
	t.syncPanels()
	t.syncFields()
	t.refreshAll(true)
}

// syncPanels keeps a state for each panel, after panels came or went.
func (t *Tab) syncPanels() {
	for len(t.panels) < len(t.d.Panels) {
		t.panels = append(t.panels, &panelState{})
	}
	t.panels = t.panels[:len(t.d.Panels)]
	for i, p := range t.d.Panels {
		s := t.panels[i]
		s.title, s.sql, s.view = p.Title, p.SQL, p.View
	}
}

// keys are the parameters the panels' queries take, in order.
func (t *Tab) keys() []string {
	var out []string
	for _, p := range t.d.Panels {
		d := safety.Dialect(db.Postgres)
		if cn := t.conn(p); cn != nil {
			d = safety.Dialect(cn.Config.Engine)
		}
		for _, k := range params.Keys([]string{p.SQL}, d) {
			if !slices.Contains(out, k) {
				out = append(out, k)
			}
		}
	}
	return out
}

// syncFields keeps a field for each parameter, with its value as last
// applied.
func (t *Tab) syncFields() {
	var fields []paramField
	for _, k := range t.keys() {
		f := paramField{key: k, value: t.values[k].Value, kind: t.values[k].Kind}
		if i := slices.IndexFunc(t.fields, func(f paramField) bool { return f.key == k }); i >= 0 {
			f = t.fields[i]
		}
		if !slices.Contains(params.Kinds, f.kind) {
			f.kind = params.Kinds[0]
		}
		fields = append(fields, f)
	}
	t.fields = fields
}

func (t *Tab) conn(p Panel) *connection.Conn { return t.h.ConnByID(t.Project.Prefix + p.Connection) }

// valuesKey is the dashboard's in the user's state of its parameters.
func (t *Tab) valuesKey() string { return t.Project.StoredPath(t.Path) }

// loadValues reads the values the user last gave the parameters.
func (t *Tab) loadValues() map[string]params.Input {
	var all map[string]map[string]params.Input
	if t.Project.Local == nil || t.Project.Local.LoadJSON(paramsFile, &all) != nil {
		return map[string]params.Input{}
	}
	if v := all[t.valuesKey()]; v != nil {
		return v
	}
	return map[string]params.Input{}
}

// saveValues keeps the values given the parameters, the user's own.
func (t *Tab) saveValues() {
	if t.Project.Local == nil {
		return
	}
	var all map[string]map[string]params.Input
	if t.Project.Local.LoadJSON(paramsFile, &all) != nil || all == nil {
		all = map[string]map[string]params.Input{}
	}
	all[t.valuesKey()] = t.values
	if err := t.Project.Local.SaveJSON(paramsFile, all); err != nil {
		t.err = "Could not keep the parameters: " + err.Error()
	}
}

// save writes the dashboard's file, as its panels and settings now are,
// unless the file changed since it was read.
func (t *Tab) save() {
	disk, err := t.d.Save(t.Path, t.disk)
	if err != nil {
		t.err = "Could not save the dashboard: " + err.Error()
		return
	}
	t.disk, t.err, t.dirty = disk, "", false
}

func (t *Tab) saveIfDirty() {
	if t.dirty {
		t.save()
	}
}

// applyParams keeps the parameters as typed, and runs the panels again.
func (t *Tab) applyParams() {
	t.values = map[string]params.Input{}
	for _, f := range t.fields {
		t.values[f.key] = params.Input{Value: f.value, Kind: f.kind}
	}
	t.saveValues()
	t.refreshAll(true)
}

// refreshAll runs every panel again, but those running. Asked for, it
// connects again the connections that failed, which an automatic refresh
// leaves as they are: a prompt for a password each minute is no help.
func (t *Tab) refreshAll(asked bool) {
	t.lastRun = time.Now()
	for i := range t.d.Panels {
		if !t.panels[i].running {
			t.runPanel(i, asked)
		}
	}
}

// runPanel runs a panel's query, connecting first when it must: when its
// connection never tried, or when asked.
func (t *Tab) runPanel(i int, asked bool) {
	if t.closed {
		return
	}
	p, s := t.d.Panels[i], t.panels[i]
	cn := t.conn(p)
	if cn == nil {
		s.err = fmt.Sprintf("The project has no connection %q.", p.Connection)
		return
	}
	if cn.DB == nil {
		// Its state shows while it connects, or why it did not; it runs
		// once connected, or at the next refresh.
		if cn.Status == connection.StatusIdle || asked && cn.Status == connection.StatusFailed {
			t.h.Connect(cn, func() {
				if at := slices.Index(t.panels, s); at >= 0 {
					t.runPanel(at, false)
				}
			})
		}
		return
	}
	prep, err := prepare(cn, p.SQL, t.values)
	if err != nil {
		s.err, s.src = err.Error(), nil
		return
	}
	if s.cancel != nil {
		s.cancel() // a run of the query before it changed
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.running, s.cancel, s.err = true, cancel, ""
	s.runs++
	run0, pool, cfg, database := s.runs, cn.DB, cn.Config, p.Database
	t.h.Background(func() func() {
		defer cancel()
		start := time.Now()
		src, err := run(ctx, pool, cfg, database, prep)
		took := time.Since(start)
		audited(t.h, cfg, database, prep, src, took, err)
		return func() {
			if run0 != s.runs {
				return
			}
			s.running, s.cancel, s.at, s.took = false, nil, time.Now(), took
			if err != nil {
				if errors.Is(err, context.Canceled) {
					err = errors.New("stopped")
				}
				s.err, s.src = widgets.Sentence(err.Error()), nil
				return
			}
			s.src, s.applied = src, false
		}
	})
}

// connState says how a panel's connection stands while it is not open:
// connecting, or why it failed; "" when it is open.
func (t *Tab) connState(p Panel) (connecting bool, failed string) {
	cn := t.conn(p)
	if cn == nil || cn.DB != nil {
		return false, ""
	}
	switch cn.Status {
	case connection.StatusConnecting:
		return true, ""
	case connection.StatusFailed:
		return false, "Could not connect to " + cn.Config.Name + ": " + cn.Err
	}
	return false, ""
}

// refreshLabel is how the refresh's select shows an interval.
func refreshLabel(secs int) string {
	switch {
	case secs == 0:
		return "Refresh by hand"
	case secs < 3600:
		return fmt.Sprintf("Refresh every %d min", secs/60)
	}
	return "Refresh every hour"
}

func (t *Tab) View(c *ui.Context) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	if t.lastRun.IsZero() {
		t.refreshAll(false)
	}
	t.autoRefresh(c)
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(6, 12).Gap(10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Icon(c, widgets.IconLayers).TextColor(pal.Muted).FontSize(14)
			ui.Text(c, t.d.Name).Bold().SingleLine()
			if !t.lastRun.IsZero() {
				ui.Text(c, "Read at "+t.lastRun.Format(time.TimeOnly)).FontSize(12).TextColor(pal.Muted)
			}
			ui.Spacer(c)
			labels := make([]string, len(Refreshes))
			for i, r := range Refreshes {
				labels[i] = refreshLabel(r)
			}
			if ui.Select(c, &t.refresh, labels).Label("Refresh").Width(180).Changed() {
				t.d.Refresh = Refreshes[max(slices.Index(labels, t.refresh), 0)]
				t.save()
			}
			if widgets.ToolButton(c, widgets.IconRefresh, "Refresh", keymap.Hint("Run every panel again", keymap.Refresh)).Clicked() ||
				!t.editing && t.h.KeysTo(t) && keymap.Pressed(c, keymap.Refresh) {
				t.refreshAll(true)
			}
			label := "Edit"
			if t.editing {
				label = "Done"
			}
			if widgets.ToolButton(c, widgets.IconPencil, label, "Arrange, resize and change the panels").Clicked() {
				if t.editing {
					t.saveIfDirty()
				}
				t.editing = !t.editing
			}
		})
		if len(t.fields) > 0 {
			t.paramsBar(c)
		}
		if t.err != "" {
			ui.Row(c).Padding(6, 12).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, t.err).TextColor(th.Danger).Selectable().Grow(1).Shrink(1)
				if strings.Contains(t.err, project.ErrChanged.Error()) && ui.Button(c, "Reload").Clicked() {
					t.Reload()
				}
			})
		}
		if len(t.d.Panels) == 0 {
			ui.Text(c, "No panels yet: in an editor, Add to Dashboard puts the statement at its caret here.").TextColor(pal.Muted).Padding(16)
			return
		}
		ui.Scroll(c).Grow(1).Background(pal.EditorBg).Children(func() {
			ui.Column(c).Padding(12).Gap(12).Children(func() {
				// Panels flow in rows of Columns grid columns.
				for start := 0; start < len(t.d.Panels); {
					end, used := start, 0
					for end < len(t.d.Panels) && used+t.d.Panels[end].Width <= Columns {
						used += t.d.Panels[end].Width
						end++
					}
					end = max(end, start+1)
					first := start
					// A row is as high as its highest panel, every panel of it so.
					height := float32(0)
					for i := first; i < end; i++ {
						height = max(height, t.panelHeight(i))
					}
					ui.Row(c.Key(fmt.Sprint("row-", first))).Gap(12).Children(func() {
						for i := first; i < end; i++ {
							t.panelView(c, i, height)
						}
						if rest := Columns - used; rest > 0 && used > 0 {
							ui.Box(c).Grow(float32(rest))
						}
					})
					start = end
				}
			})
		})
	})
	// The panels drawn, the changes asked of their list are made.
	for _, change := range t.later {
		change()
	}
	t.later = nil
}

// autoRefresh runs the panels again when the interval has passed since
// they last ran, and asks for a frame then; only while the tab shows.
func (t *Tab) autoRefresh(c *ui.Context) {
	if t.d.Refresh == 0 || t.lastRun.IsZero() {
		return
	}
	next := t.lastRun.Add(time.Duration(t.d.Refresh) * time.Second)
	if wait := time.Until(next); wait > 0 {
		c.After(wait)
		return
	}
	t.refreshAll(false)
}

// paramsBar edits the parameters the panels take, applied on Enter or
// Apply.
func (t *Tab) paramsBar(c *ui.Context) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Row(c).Padding(6, 12).Gap(10).Wrap().AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
		submitted := false
		for i := range t.fields {
			f := &t.fields[i]
			ui.Row(c.Key("param-" + f.key)).Gap(4).AlignItems(ui.Center).Children(func() {
				ui.Text(c, f.key).Font(widgets.MonoFont).FontSize(12).TextColor(pal.Muted)
				if ui.TextInput(c, &f.value).Width(140).FontSize(12.5).Label("Parameter " + f.key).Submitted() {
					submitted = true
				}
				ui.Select(c, &f.kind, params.Kinds).Width(90).Label("Kind of " + f.key)
			})
		}
		if ui.Button(c, "Apply").Clicked() || submitted {
			t.applyParams()
		}
	})
}

// panelHeight is how high a panel draws its view: a single value less
// high than a chart or rows, unless edited.
func (t *Tab) panelHeight(i int) float32 {
	height := float32(panelHeight)
	if t.d.Panels[i].View == ViewValue && !t.editing {
		height = valuePanelHeight
	}
	if t.panels[i].editSQL {
		height += 160
	}
	return height
}

// panelView draws a panel: its title and state, its result as its view
// says, and, in edit mode, its settings.
func (t *Tab) panelView(c *ui.Context, i int, height float32) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	p, s := &t.d.Panels[i], t.panels[i]
	connecting, failed := t.connState(*p)
	ui.Column(c.Key(fmt.Sprintf("panel-%p", s))).Grow(float32(p.Width)).Basis(0).Height(height).Radius(8).Background(th.Background).Border(1, th.Border).Clip().Children(func() {
		ui.Row(c).Padding(6, 10).Gap(6).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			if t.editing {
				if ui.TextInput(c, &s.title).FontSize(12.5).Grow(1).Label("Panel title").Changed() {
					p.Title, t.dirty = strings.TrimSpace(s.title), true
				}
			} else {
				ui.Text(c, p.Title).Bold().FontSize(12.5).SingleLine().Grow(1).Shrink(1)
			}
			switch {
			case s.running || connecting:
				ui.Spinner(c).Size(12, 12)
			case s.src != nil:
				ui.Text(c, fmt.Sprintf("%d row%s · %s", len(s.src.Rows), widgets.Plural(len(s.src.Rows)), s.took.Round(time.Millisecond))).FontSize(11.5).TextColor(pal.Muted).Tooltip("Read at " + s.at.Format(time.TimeOnly))
			}
			if t.editing {
				t.panelTools(c, i)
			}
		})
		if s.editSQL {
			ui.Column(c).Height(160).Padding(6, 10).Gap(6).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
				ui.TextArea(c, &s.sql).Font(widgets.MonoFont).FontSize(12).Grow(1).Label("Panel query")
				ui.Row(c).Gap(6).Children(func() {
					ui.Spacer(c)
					if ui.Button(c, "Cancel").Clicked() {
						s.sql, s.editSQL = p.SQL, false
					}
					if ui.PrimaryButton(c, "Run and Keep").Disabled(strings.TrimSpace(s.sql) == "").Clicked() {
						p.SQL, s.editSQL = s.sql, false
						t.save()
						t.syncFields()
						t.runPanel(i, true)
					}
				})
			})
		}
		switch {
		case failed != "":
			ui.Text(c, failed).FontSize(12.5).TextColor(th.Danger).Padding(10).Selectable()
		case s.err != "":
			ui.Text(c, s.err).FontSize(12.5).TextColor(th.Danger).Padding(10).Selectable()
		case s.src == nil:
		case p.View == ViewValue:
			valueView(c, s)
		case p.View == ViewTable:
			tableView(c, s)
		default:
			t.chartView(c, p, s)
		}
	})
}

// panelTools are a panel's settings in edit mode: its view, its width,
// its place, its query, and its removal. Changes of the list of panels
// wait for the panels to be drawn.
func (t *Tab) panelTools(c *ui.Context, i int) {
	p, s := &t.d.Panels[i], t.panels[i]
	if ui.Select(c, &s.view, Views).Width(90).Label("Panel view").Changed() {
		p.View = s.view
		t.save()
	}
	button := func(label, tip string, disabled bool) bool {
		return ui.Button(c, label).Disabled(disabled).Tooltip(tip).Clicked()
	}
	if button("−", "Narrower", p.Width <= 1) {
		p.Width--
		t.save()
	}
	if button("+", "Wider", p.Width >= Columns) {
		p.Width++
		t.save()
	}
	if button("←", "Earlier", i == 0) {
		t.later = append(t.later, func() { t.swap(s, -1) })
	}
	if button("→", "Later", i == len(t.d.Panels)-1) {
		t.later = append(t.later, func() { t.swap(s, 1) })
	}
	if button("SQL", "Change the panel's query", false) {
		s.editSQL, s.sql = !s.editSQL, p.SQL
	}
	if cn := t.conn(*p); cn != nil && button("Editor", "Open the panel's query in an editor", false) {
		t.h.NewQueryTab(cn, p.Database, p.SQL)
	}
	if button("Remove", "Remove the panel", false) {
		t.later = append(t.later, func() { t.remove(s) })
	}
}

// swap moves a panel by one place, earlier or later.
func (t *Tab) swap(s *panelState, by int) {
	i := slices.Index(t.panels, s)
	j := i + by
	if i < 0 || j < 0 || j >= len(t.panels) {
		return
	}
	t.d.Panels[i], t.d.Panels[j] = t.d.Panels[j], t.d.Panels[i]
	t.panels[i], t.panels[j] = t.panels[j], t.panels[i]
	t.save()
}

// remove removes a panel, which the toast's Undo puts back.
func (t *Tab) remove(s *panelState) {
	i := slices.Index(t.panels, s)
	if i < 0 {
		return
	}
	removed := t.d.Panels[i]
	t.d.Panels = slices.Delete(t.d.Panels, i, i+1)
	t.panels = slices.Delete(t.panels, i, i+1)
	t.save()
	t.syncFields()
	t.h.Toast("Removed "+removed.Title, "Undo", func() {
		if t.closed {
			return
		}
		at := min(i, len(t.d.Panels))
		t.d.Panels = slices.Insert(t.d.Panels, at, removed)
		t.panels = slices.Insert(t.panels, at, s)
		t.save()
		t.syncFields()
	})
}

// chartView charts a panel's result; its chart's settings, changed while
// the dashboard is edited, are kept.
func (t *Tab) chartView(c *ui.Context, p *Panel, s *panelState) {
	if !s.applied {
		if p.Chart != nil {
			s.chart.Use(*p.Chart, s.src)
		}
		s.applied, s.shown = true, s.chart.Settings(s.src)
	}
	if len(s.src.Rows) == 0 {
		ui.Text(c, "No rows.").FontSize(12.5).TextColor(widgets.PaletteOf(c).Muted).Padding(10)
		return
	}
	if !t.editing {
		dataview.ChartPlot(c, &s.chart, s.src)
		return
	}
	dataview.ChartControls(c, &s.chart, s.src)
	dataview.ChartPlot(c, &s.chart, s.src)
	if now := s.chart.Settings(s.src); !chartSettingsEqual(s.shown, now) {
		p.Chart, s.shown = &now, now
		t.save()
	}
}

func chartSettingsEqual(a, b dataview.ChartSettings) bool {
	return a.Kind == b.Kind && a.X == b.X && slices.Equal(a.Series, b.Series)
}

// tableView lists a panel's rows.
func tableView(c *ui.Context, s *panelState) {
	cols := make([]ui.TableColumn, len(s.src.Cols))
	for i, col := range s.src.Cols {
		cols[i] = ui.TableColumn{Title: col.Name}
	}
	ui.Table(c, &s.list, cols, len(s.src.Rows), func(row, col int) {
		v := s.src.Rows[row][col]
		text := "NULL"
		if v != nil {
			text = widgets.OneLine(db.Display(v), 200)
		}
		ui.Text(c, text).Font(widgets.MonoFont).FontSize(12).SingleLine()
	}).Grow(1).Label("Panel rows")
}

// valueView shows a panel's first value, large, under its column's name.
func valueView(c *ui.Context, s *panelState) {
	pal := widgets.PaletteOf(c)
	ui.Column(c).Grow(1).Center().Gap(4).Children(func() {
		if len(s.src.Rows) == 0 || len(s.src.Cols) == 0 {
			ui.Text(c, "No rows.").TextColor(pal.Muted)
			return
		}
		v := s.src.Rows[0][0]
		text := "NULL"
		if v != nil {
			text = db.Display(v)
			if f, err := strconv.ParseFloat(text, 64); err == nil {
				text = strconv.FormatFloat(f, 'f', -1, 64)
			}
		}
		ui.Text(c, widgets.OneLine(text, 40)).FontSize(34).Bold().SingleLine()
		ui.Text(c, s.src.Cols[0].Name).FontSize(12).TextColor(pal.Muted)
	})
}
