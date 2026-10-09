package modelview

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/datamodel"
	"dgopher/internal/db"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// What a model is compared with.
const (
	withDatabase = iota
	withModel
)

// compareForm asks what a model is compared with: a schema of one of the
// project's connections of its engine, or another of its models.
type compareForm struct {
	open     bool
	with     int
	conns    []*connection.Conn
	conn     string // the name of the connection chosen
	database string
	schema   string
	models   []*datamodel.Model // the project's other models of the engine
	model    string             // the name of the model chosen
	running  bool
	err      string
}

func newCompareForm(t *Tab) *compareForm {
	f := &compareForm{open: true, conns: t.conns(t.m.Engine)}
	if src := t.source(); src != nil && src.Config.Engine == t.m.Engine {
		f.conn, f.database, f.schema = src.Config.Name, t.m.Source.Database, t.m.Source.Schema
	} else if len(f.conns) > 0 {
		f.conn = f.conns[0].Config.Name
	}
	files, _ := datamodel.List(t.Project)
	for _, path := range files {
		if m, err := datamodel.Load(path); err == nil && path != t.Path && m.Engine == t.m.Engine {
			f.models = append(f.models, m)
		}
	}
	if len(f.models) > 0 {
		f.model = f.models[0].Name
	}
	if len(f.conns) == 0 && len(f.models) > 0 {
		f.with = withModel
	}
	return f
}

func (f *compareForm) chosenConn() *connection.Conn {
	i := slices.IndexFunc(f.conns, func(cn *connection.Conn) bool { return cn.Config.Name == f.conn })
	if i < 0 {
		return nil
	}
	return f.conns[i]
}

func (t *Tab) compareView(c *ui.Context) {
	f := t.comparing
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(480).Gap(12).Children(func() {
			ui.Text(c, "Compare "+t.m.Name).FontSize(15).Bold()
			ui.Segmented(c, &f.with, "A Database", "Another Model").Label("Compare with").AutoFocus()
			ui.Form(c, func() {
				// One slot each, whichever shows, so that focus keeps its
				// place.
				ui.Column(c).Gap(8).Children(func() {
					if f.with != withDatabase {
						return
					}
					if len(f.conns) == 0 {
						ui.Text(c, "The project has no "+t.m.Engine.Label()+" connection.").TextColor(pal.Muted)
						return
					}
					names := make([]string, len(f.conns))
					for i, cn := range f.conns {
						names[i] = cn.Config.Name
					}
					ui.Field(c, "Connection", func() {
						ui.Select(c, &f.conn, names).Label("Connection")
					})
					if t.m.Engine == db.Postgres {
						ui.Field(c, "Database", func() {
							ui.TextInput(c, &f.database).Placeholder("the connection's").Font(widgets.MonoFont).Label("Database")
						})
					}
					ui.Field(c, "Schema", func() {
						ui.TextInput(c, &f.schema).Placeholder(defaultSchema(t.m.Engine, f.chosenConn())).Font(widgets.MonoFont).Label("Schema")
					})
				})
				ui.Column(c).Gap(8).Children(func() {
					if f.with != withModel {
						return
					}
					if len(f.models) == 0 {
						ui.Text(c, "The project has no other "+t.m.Engine.Label()+" model.").TextColor(pal.Muted)
						return
					}
					names := make([]string, len(f.models))
					for i, m := range f.models {
						names[i] = m.Name
					}
					ui.Field(c, "Model", func() {
						ui.Select(c, &f.model, names).Label("Model")
					})
				})
			})
			if f.err != "" {
				ui.Text(c, f.err).TextColor(th.Danger).Selectable()
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if f.running {
					ui.Spinner(c).Size(14, 14)
					ui.Text(c, "Reading the schema…").FontSize(12.5).TextColor(pal.Muted)
					c.After(200 * time.Millisecond)
				}
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					f.open = false
				}
				ready := f.with == withDatabase && f.chosenConn() != nil || f.with == withModel && len(f.models) > 0
				if widgets.Activated(c, ui.PrimaryButton(c, "Compare").Disabled(!ready || f.running)) {
					t.runCompare(f)
				}
			})
		})
	})
	if !f.open {
		t.comparing = nil
	}
}

// defaultSchema is the schema a connection's tables are in unless named.
func defaultSchema(e db.Engine, cn *connection.Conn) string {
	switch {
	case cn != nil && cn.DefaultSchema != "":
		return cn.DefaultSchema
	case e == db.Postgres:
		return "public"
	case e == db.SQLite, e == db.DuckDB:
		return "main"
	case e == db.ClickHouse:
		return "default"
	case cn != nil:
		return cn.Config.Database
	}
	return ""
}

// comparison is a model compared with a database's schema, or another
// model.
type comparison struct {
	label  string // what the model is compared with
	other  *datamodel.Model
	diffs  []datamodel.TableDiff
	counts map[datamodel.State]int // diffs in each state, counted once
	// The database's, when the model is compared with one.
	conn             *connection.Conn
	database, schema string

	drop     bool // a migration drops what the model lacks
	showSame bool
	applying bool
	cancel   func() // stops the migration running
}

func (t *Tab) runCompare(f *compareForm) {
	f.err = ""
	if f.with == withModel {
		i := slices.IndexFunc(f.models, func(m *datamodel.Model) bool { return m.Name == f.model })
		if i < 0 {
			return
		}
		other := f.models[i]
		diffs, err := datamodel.Compare(t.m, other)
		if err != nil {
			f.err = err.Error()
			return
		}
		schema := ""
		if len(other.Tables) > 0 {
			schema = other.Tables[0].Schema
		}
		t.result = &comparison{label: "the model " + other.Name, other: other, diffs: diffs, schema: schema}
		f.open = false
		return
	}
	cn := f.chosenConn()
	if cn == nil {
		return
	}
	database := strings.TrimSpace(f.database)
	schema := strings.TrimSpace(f.schema)
	f.running = true
	t.readSchema(cn, database, schema, func(r *comparison, err error) {
		f.running = false
		if t.comparing != f || !f.open {
			return // cancelled
		}
		if err != nil {
			f.err = err.Error()
			return
		}
		t.result, f.open = r, false
	})
}

// readSchema reads a schema of a connection as a model, and compares the
// tab's with it; done hears of it on the main thread.
func (t *Tab) readSchema(cn *connection.Conn, database, schema string, done func(*comparison, error)) {
	t.h.Connect(cn, func() {
		if cn.Status != connection.StatusConnected {
			done(nil, fmt.Errorf("could not connect to %s: %s", cn.Config.Name, cn.Err))
			return
		}
		if schema == "" {
			schema = defaultSchema(t.m.Engine, cn)
		}
		pool := cn.PoolFor(database)
		m, rev := t.m, t.rev
		t.h.Background(func() func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			var tables []db.TableDesign
			d, err := pool(ctx)
			if err == nil {
				tables, _, err = datamodel.Build(ctx, d, schema, nil, nil)
			}
			other := &datamodel.Model{Name: cn.Config.Name, Engine: cn.Config.Engine, Tables: tables}
			var diffs []datamodel.TableDiff
			if err == nil {
				diffs, err = datamodel.Compare(m, other)
			}
			return func() {
				if err == nil && t.rev != rev {
					err = errors.New("the model changed while the schema was read: compare again")
				}
				if err != nil {
					done(nil, err)
					return
				}
				done(&comparison{label: cn.Config.Name + ", " + schema, other: other, diffs: diffs, conn: cn, database: database, schema: schema}, nil)
			}
		})
	})
}

// stateLabel names how a table of the model stands in what it is compared
// with.
func stateLabel(s datamodel.State) string {
	switch s {
	case datamodel.OnlyInA:
		return "missing"
	case datamodel.OnlyInB:
		return "not in the model"
	case datamodel.Changed:
		return "differs"
	}
	return "same"
}

func (t *Tab) comparisonView(c *ui.Context) {
	r := t.result
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	if r.counts == nil {
		r.counts = map[datamodel.State]int{}
		for _, d := range r.diffs {
			r.counts[d.State]++
		}
	}
	counts := r.counts
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(6, 12).Gap(10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Text(c, "Compared with "+r.label).Bold().SingleLine().Shrink(1)
			ui.Text(c, fmt.Sprintf("%d missing · %d differ · %d not in the model · %d the same",
				counts[datamodel.OnlyInA], counts[datamodel.Changed], counts[datamodel.OnlyInB], counts[datamodel.Same])).FontSize(12).TextColor(pal.Muted).SingleLine()
			ui.Spacer(c)
			ui.Checkbox(c, &r.showSame, "Show the same")
			if r.conn != nil && widgets.ToolButton(c, widgets.IconRefresh, "Compare Again", "Read the schema again").Disabled(r.applying).Clicked() {
				t.recompare()
			}
			if widgets.ToolButton(c, widgets.IconX, "Close", "Back to the model's tables").Clicked() {
				t.result = nil
			}
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(12, 16).Gap(10).Children(func() {
				shown := 0
				for i, d := range r.diffs {
					if d.State == datamodel.Same && !r.showSame {
						continue
					}
					shown++
					ui.Column(c.Key(fmt.Sprint("diff-", i))).Gap(3).Children(func() {
						ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Text(c, d.Name).Bold().Font(widgets.MonoFont)
							color := pal.Muted
							switch d.State {
							case datamodel.OnlyInA:
								color = th.Success
							case datamodel.OnlyInB:
								color = th.Danger
							case datamodel.Changed:
								color = th.Warning
							}
							ui.Text(c, stateLabel(d.State)).FontSize(12).TextColor(color)
						})
						for _, ch := range d.Changes {
							ui.Row(c).Gap(12).PaddingX(12).Children(func() {
								ui.Text(c, strings.TrimSpace(ch.What+" "+ch.Name)).Font(widgets.MonoFont).FontSize(12).Width(260).SingleLine()
								side := func(s, none string) {
									if s == "" {
										ui.Text(c, none).FontSize(12).TextColor(pal.Muted).Width(320).SingleLine()
									} else {
										ui.Text(c, s).Font(widgets.MonoFont).FontSize(12).Width(320).SingleLine().Tooltip(s)
									}
								}
								side(ch.A, "not in the model")
								side(ch.B, "missing")
							})
						}
					})
				}
				if shown == 0 {
					ui.Text(c, "No table differs.").TextColor(pal.Muted)
				}
			})
		})
		t.migrationBar(c)
	})
}

// migrationBar offers the migration making what the model is compared
// with like it: to run on the database, or as a script.
func (t *Tab) migrationBar(c *ui.Context) {
	r := t.result
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	differs := slices.ContainsFunc(r.diffs, func(d datamodel.TableDiff) bool { return d.State != datamodel.Same })
	ui.Row(c).Padding(8, 12).Gap(10).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
		ui.Checkbox(c, &r.drop, "Drop what the model lacks")
		ui.Text(c, "Otherwise kept: a column renamed reads as one dropped and one added.").FontSize(12).TextColor(pal.Muted).SingleLine().Shrink(1)
		ui.Spacer(c)
		if r.applying {
			ui.Spinner(c).Size(14, 14)
			c.After(200 * time.Millisecond)
			if r.cancel != nil && ui.Button(c, "Cancel").Clicked() {
				r.cancel()
			}
		}
		if r.conn == nil {
			if ui.Button(c, "Copy Migration Script").Disabled(!differs).Tooltip("The statements making the other model's schema like this one").Clicked() {
				if text, ok := t.migrationText(); ok {
					t.h.WriteClipboard(text)
					t.h.Toast("The migration script is copied.", "", nil)
				}
			}
			return
		}
		if ui.Button(c, "Open as Script").Disabled(!differs || r.applying).Clicked() {
			if text, ok := t.migrationText(); ok {
				t.h.NewQueryTab(r.conn, r.database, text)
			}
		}
		if ui.PrimaryButton(c, "Apply…").Disabled(!differs || r.applying || r.conn.Config.ReadOnly).
			Tooltip("Run the migration on " + r.conn.Config.Name + ", once you have reviewed it").Clicked() {
			t.applyMigration()
		}
	})
}

// migration is the change making what the model is compared with like
// it, with what it leaves out.
func (t *Tab) migration() (db.SchemaChange, []string, bool) {
	r := t.result
	ch, notes, err := datamodel.Migration(t.m, r.other, r.schema, r.drop)
	if err != nil {
		t.h.ShowError("Could not write the migration", err.Error())
		return db.SchemaChange{}, nil, false
	}
	if len(ch.Steps) == 0 {
		t.h.Toast("Nothing to change: "+strings.Join(notes, " "), "", nil)
		return db.SchemaChange{}, nil, false
	}
	return ch, notes, true
}

func (t *Tab) migrationText() (string, bool) {
	ch, notes, ok := t.migration()
	if !ok {
		return "", false
	}
	var b strings.Builder
	fmt.Fprintf(&b, "-- Makes %s like the data model %q.\n", t.result.label, t.m.Name)
	for _, n := range notes {
		b.WriteString("-- " + n + "\n")
	}
	b.WriteString("\n" + ch.Text() + "\n")
	return b.String(), true
}

func (t *Tab) applyMigration() {
	r := t.result
	ch, notes, ok := t.migration()
	if !ok {
		return
	}
	always := "Review the statements before they run."
	if len(notes) > 0 {
		always += " " + strings.Join(notes, " ")
	}
	dataview.ApplyChange(t.h, r.conn, r.database, "Make "+r.schema+" like the model?", ch, always,
		func(cancel func()) { r.applying, r.cancel = true, cancel },
		func(err error) {
			r.applying, r.cancel = false, nil
			r.conn.ForgetCatalog()
			if err != nil {
				t.h.ShowError("The migration failed", err.Error())
			}
			t.recompare()
		})
}

// recompare reads the database compared with again, and compares anew.
func (t *Tab) recompare() {
	r := t.result
	if r == nil || r.conn == nil {
		return
	}
	r.applying = true
	t.readSchema(r.conn, r.database, r.schema, func(next *comparison, err error) {
		r.applying = false
		if t.result != r {
			return // closed, or compared with another since
		}
		if err != nil {
			t.err = err.Error()
			return
		}
		next.drop, next.showSame = r.drop, r.showSame
		t.result = next
	})
}
