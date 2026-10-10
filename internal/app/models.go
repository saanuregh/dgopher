package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/datamodel"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/modelview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// openModel opens a data model's file in a tab, or brings its tab
// forward, read again.
func (a *App) openModel(p *project.Project, path string) {
	if t, ok := a.findTab(func(t widgets.Tab) bool { m, ok := t.(*modelview.Tab); return ok && m.Path == path }); ok {
		t.(*modelview.Tab).Reload()
		return
	}
	m, err := modelview.Open(a, p, path)
	if err != nil {
		a.ShowError("Could not open the data model", err.Error())
		return
	}
	a.AddTab(m)
}

// modelNames are a project's data models, by their names, with their
// paths.
func (a *App) modelNames(p *project.Project) (names, paths []string) {
	return a.fileNames(filepath.Join(p.Dir, datamodel.Folder), func() ([]string, error) { return datamodel.List(p) },
		func(path string) (string, error) {
			m, err := datamodel.Load(path)
			if err != nil {
				return "", err
			}
			return m.Name, nil
		})
}

// newModelForm asks the name and engine of a data model to design from
// nothing.
type newModelForm struct {
	open    bool
	project *project.Project
	name    string
	engine  string // its label
	err     string
}

func (a *App) newModelView(c *ui.Context) {
	f := a.newModel
	var labels []string
	var engines []db.Engine
	for _, e := range db.Engines() {
		if datamodel.Engine(e) {
			labels, engines = append(labels, e.Label()), append(engines, e)
		}
	}
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(420).Gap(12).Children(func() {
			ui.Text(c, "New Data Model").FontSize(15).Bold()
			ui.Form(c, func() {
				ui.Field(c, "Name", func() {
					ui.TextInput(c, &f.name).AutoFocus().Label("Model name")
				})
				ui.Field(c, "Engine", func() {
					ui.Select(c, &f.engine, labels).Label("Engine")
				}).Description("Its tables are typed as this engine types them; its DDL can be written for any.")
			})
			if f.err != "" {
				ui.Text(c, f.err).TextColor(c.Theme().Danger)
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					f.open = false
				}
				if widgets.Activated(c, ui.PrimaryButton(c, "Create")) {
					name := strings.TrimSpace(f.name)
					path := datamodel.File(f.project, name)
					names, _ := a.modelNames(f.project)
					m := &datamodel.Model{Name: name, Engine: engines[max(slices.Index(labels, f.engine), 0)], Tables: []db.TableDesign{}}
					switch _, statErr := os.Stat(path); {
					case name == "":
						f.err = "Name the model."
					case statErr == nil || slices.Contains(names, name):
						f.err = "A model of that name exists."
					default:
						if _, err := m.Save(path, nil); err != nil {
							f.err = err.Error()
						} else {
							f.open = false
							a.openModel(f.project, path)
						}
					}
				}
			})
		})
	})
	if !f.open {
		a.newModel = nil
	}
}

// modelPaths are the files of a project's open data models, as the
// workspace keeps them.
func (a *App) modelPaths(p *project.Project) []string {
	var out []string
	for _, t := range a.everyTab() {
		if m, ok := t.(*modelview.Tab); ok && m.Project == p {
			out = append(out, filepath.ToSlash(p.StoredPath(m.Path)))
		}
	}
	return out
}

// saveModelForm asks a name for a data model of a schema's tables, and
// builds it.
type saveModelForm struct {
	open     bool
	conn     *connection.Conn
	database string
	schema   string
	name     string
	building bool
	read     int // the tables read so far
	err      string
	cancel   context.CancelFunc
}

func (a *App) openSaveModel(cn *connection.Conn, database, schema string) {
	a.savingModel = &saveModelForm{open: true, conn: cn, database: database, schema: schema, name: schema}
}

// buildModel reads the schema's tables, and saves them as a new model of
// the connection's project, which it opens.
func (a *App) buildModel(f *saveModelForm) {
	f.err = ""
	p := f.conn.Project
	name := strings.TrimSpace(f.name)
	if name == "" {
		f.err = "Name the model."
		return
	}
	path := datamodel.File(p, name)
	names, _ := a.modelNames(p)
	if _, err := os.Stat(path); err == nil || slices.Contains(names, name) {
		f.err = "A model of that name exists: choose another name, or open it and update it from the database."
		return
	}
	cn, schema := f.conn, f.schema
	pool := cn.PoolFor(f.database)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	f.building, f.read, f.cancel = true, 0, cancel
	dataview.BackgroundResetOnPanic(a, func() { f.building, f.cancel = false, nil }, func() func() {
		defer cancel()
		var tables []db.TableDesign
		var notes []string
		d, err := pool(ctx)
		if err == nil {
			tables, notes, err = datamodel.Build(ctx, d, schema, nil, func(n int) { a.Post(func() { f.read = n }) })
		}
		return func() {
			f.building, f.cancel = false, nil
			if a.savingModel != f || !f.open {
				return // cancelled
			}
			if err != nil {
				f.err = "Could not read " + schema + ": " + err.Error()
				return
			}
			m := &datamodel.Model{Name: name, Engine: cn.Config.Engine, Tables: tables,
				Source: &datamodel.Source{Connection: strings.TrimPrefix(cn.Config.ID, p.Prefix), Database: f.database, Schema: schema}}
			if _, err := m.Save(path, nil); err != nil {
				f.err = "Could not save the model: " + err.Error()
				return
			}
			f.open = false
			a.openModel(p, path)
			if len(notes) > 0 {
				a.Toast(strings.Join(notes, " "), "", nil)
			}
		}
	})
}

func (a *App) saveModelView(c *ui.Context) {
	f := a.savingModel
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(460).Gap(12).Children(func() {
			ui.Text(c, "Save as Data Model").FontSize(15).Bold()
			ui.Text(c, "The tables of "+f.schema+", with their columns, keys, indexes and checks, in a file of the project's "+datamodel.Folder+
				" folder, for its team.").FontSize(12.5).TextColor(pal.Muted)
			ui.Form(c, func() {
				ui.Field(c, "Name", func() {
					ui.TextInput(c, &f.name).AutoFocus().Label("Model name")
				})
			})
			if f.err != "" {
				ui.Text(c, f.err).TextColor(th.Danger).Selectable()
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if f.building {
					ui.Spinner(c).Size(14, 14)
					ui.Text(c, widgets.Count(f.read, "table")+" read…").FontSize(12.5).TextColor(pal.Muted)
					c.After(200 * time.Millisecond)
				}
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					f.open = false
				}
				if widgets.Activated(c, ui.PrimaryButton(c, "Save").Disabled(f.building)) {
					a.buildModel(f)
				}
			})
		})
	})
	if !f.open {
		if f.cancel != nil {
			f.cancel()
		}
		a.savingModel = nil
	}
}
