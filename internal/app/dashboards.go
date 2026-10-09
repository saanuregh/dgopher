package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/project"
	"dgopher/internal/ui/dashboard"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// ConnByID finds a connection by its ID in the app.
func (a *App) ConnByID(id string) *connection.Conn { return a.connByID(id) }

// openDashboard opens a dashboard's file in a tab, or brings its tab
// forward, read again.
func (a *App) openDashboard(p *project.Project, path string) {
	for i, t := range a.tabs {
		if d, ok := t.(*dashboard.Tab); ok && d.Path == path {
			a.active = i
			d.Reload()
			return
		}
	}
	d, err := dashboard.Open(a, p, path)
	if err != nil {
		a.ShowError("Could not open the dashboard", err.Error())
		return
	}
	a.AddTab(d)
}

// dashboardList is a project's dashboards as last read, with when their
// folder last changed.
type dashboardList struct {
	changed      time.Time
	names, paths []string
}

// dashboardNames are a project's dashboards, by their names as their
// files hold them, with their paths; read again once their folder
// changes, as a file comes or goes.
func (a *App) dashboardNames(p *project.Project) (names, paths []string) {
	info, err := os.Stat(filepath.Join(p.Dir, dashboard.Folder))
	if err != nil {
		return nil, nil
	}
	if l, ok := a.dashboardLists[p.Dir]; ok && l.changed.Equal(info.ModTime()) {
		return l.names, l.paths
	}
	files, _ := dashboard.List(p)
	for _, f := range files {
		if d, err := dashboard.Load(f); err == nil {
			names, paths = append(names, d.Name), append(paths, f)
		}
	}
	if a.dashboardLists == nil {
		a.dashboardLists = map[string]dashboardList{}
	}
	a.dashboardLists[p.Dir] = dashboardList{changed: info.ModTime(), names: names, paths: paths}
	return names, paths
}

// newDashboardChoice is the choice of a dashboard not made yet.
const newDashboardChoice = "A new dashboard…"

// addToDashboard adds an editor's statement to a dashboard of its
// connection's project, as a panel.
type addToDashboard struct {
	open     bool
	conn     *connection.Conn
	database string
	sql      string
	target   string   // a dashboard's name, or newDashboardChoice
	choices  []string // the project's dashboards, then newDashboardChoice
	newName  string
	title    string
	view     int // an index of dashboard.Views
	err      string
}

func (a *App) openAddToDashboard(q *query.Tab) {
	sql, err := q.StatementToRun()
	if err != nil {
		a.ShowError("Nothing to add", err.Error())
		return
	}
	first := strings.TrimSpace(strings.SplitN(strings.TrimSpace(sql), "\n", 2)[0])
	x := &addToDashboard{open: true, conn: q.Conn, database: q.Database, sql: sql, title: widgets.OneLine(first, 40), target: newDashboardChoice}
	names, _ := a.dashboardNames(q.Conn.Project)
	x.choices = append(names, newDashboardChoice)
	x.target = x.choices[0]
	a.addingPanel = x
}

// addPanel adds the panel, making the dashboard when it is new, and opens
// it.
func (a *App) addPanel(x *addToDashboard) {
	x.err = ""
	p := x.conn.Project
	names, paths := a.dashboardNames(p)
	if err := dashboard.CheckRead(x.conn, x.sql); err != nil {
		x.err = "This statement cannot be a panel: " + err.Error() + "."
		return
	}
	var path string
	var d *dashboard.Dashboard
	var was []byte
	if i := slices.Index(names, x.target); i >= 0 && x.target != newDashboardChoice {
		path = paths[i]
		var err error
		if d, was, err = dashboard.LoadFile(path); err != nil {
			x.err = err.Error()
			return
		}
	} else {
		name := strings.TrimSpace(x.newName)
		if name == "" {
			x.err = "Name the new dashboard."
			return
		}
		path = dashboard.File(p, name)
		if _, err := os.Stat(path); err == nil || slices.Contains(names, name) {
			x.err = "A dashboard of that name exists: choose it, or another name."
			return
		}
		d = &dashboard.Dashboard{Name: name}
	}
	title := strings.TrimSpace(x.title)
	if title == "" {
		title = "Panel"
	}
	d.Panels = append(d.Panels, dashboard.Panel{Title: title, Connection: strings.TrimPrefix(x.conn.Config.ID, p.Prefix),
		Database: x.database, SQL: x.sql, View: dashboard.Views[x.view], Width: 2})
	if _, err := d.Save(path, was); err != nil {
		x.err = "Could not save the dashboard: " + err.Error()
		return
	}
	x.open = false
	a.openDashboard(p, path)
}

func (a *App) addToDashboardView(c *ui.Context) {
	x := a.addingPanel
	th := c.Theme()
	ui.Modal(c, &x.open, func() {
		ui.Column(c).Width(520).Gap(12).Children(func() {
			ui.Text(c, "Add to Dashboard").FontSize(15).Bold()
			ui.Form(c, func() {
				ui.Field(c, "Dashboard", func() {
					ui.Select(c, &x.target, x.choices).Label("Dashboard")
				})
				if x.target == newDashboardChoice {
					ui.Field(c, "Name", func() {
						ui.TextInput(c, &x.newName).Placeholder("Sales").AutoFocus().Label("New dashboard's name")
					}).Description("Kept in the project's " + dashboard.Folder + " folder, for its team.")
				}
				ui.Field(c, "Panel", func() {
					ui.TextInput(c, &x.title).Label("Panel title")
				})
				ui.Field(c, "Shown as", func() {
					ui.Segmented(c, &x.view, "Chart", "Table", "Value").Label("Shown as")
				}).Description("A panel runs reads only, and on PostgreSQL and MySQL in a read-only transaction.")
			})
			ui.Text(c, widgets.OneLine(x.sql, 200)).Font(widgets.MonoFont).FontSize(12).TextColor(widgets.PaletteOf(c).Muted).SingleLine()
			if x.err != "" {
				ui.Text(c, x.err).TextColor(th.Danger)
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					x.open = false
				}
				if ui.PrimaryButton(c, "Add").Clicked() {
					a.addPanel(x)
				}
			})
		})
	})
	if !x.open {
		a.addingPanel = nil
	}
}

// newDashboard asks a dashboard's name, makes it empty, and opens it.
func (a *App) newDashboard(p *project.Project) {
	a.renaming = &renameForm{open: true, title: "New Dashboard", action: "Create", rename: func(name string) error {
		name = strings.TrimSpace(name)
		if name == "" {
			return errors.New("Name the dashboard.")
		}
		path := dashboard.File(p, name)
		if _, err := os.Stat(path); err == nil {
			return errors.New("A dashboard of that name exists.")
		}
		if _, err := (&dashboard.Dashboard{Name: name, Panels: []dashboard.Panel{}}).Save(path, nil); err != nil {
			return err
		}
		a.openDashboard(p, path)
		return nil
	}}
}

// dashboardsMenu lists a project's dashboards to open, and makes one.
func (a *App) dashboardsMenu(m *ui.Menu, p *project.Project) {
	m.Submenu("Dashboards", func(m *ui.Menu) {
		names, paths := a.dashboardNames(p)
		for i, name := range names {
			if m.Item(name).Chosen() {
				a.openDashboard(p, paths[i])
			}
		}
		if len(names) > 0 {
			m.Separator()
		}
		if m.Item("New Dashboard…").Chosen() {
			a.newDashboard(p)
		}
	})
}

// dashboardPaths are the files of a project's open dashboards, as the
// workspace keeps them.
func (a *App) dashboardPaths(p *project.Project) []string {
	var out []string
	for _, t := range a.tabs {
		if d, ok := t.(*dashboard.Tab); ok && d.Project == p {
			out = append(out, filepath.ToSlash(p.StoredPath(d.Path)))
		}
	}
	return out
}
