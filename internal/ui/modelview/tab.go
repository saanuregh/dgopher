// Package modelview is the data model tab: a model's tables, its DDL for
// any engine, its comparison with a database or another model, and the
// migration making a database like it.
package modelview

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/datamodel"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// Host is what the tab needs of the app.
type Host interface {
	dataview.Host
	ConnByID(id string) *connection.Conn
}

// Tab shows a data model of a project.
type Tab struct {
	h       Host
	Project *project.Project
	Path    string
	m       *datamodel.Model
	was     []byte // the file as read, which a save checks it still holds
	err     string
	// rev counts the changes of the model, by which work begun on it
	// knows it is stale.
	rev int

	tables ui.ListState
	table  int

	generating *generateForm
	comparing  *compareForm
	result     *comparison

	// updating is set while the model is built from its source again.
	updating      bool
	confirmUpdate bool

	// form designs a table of the model: the table at editing, or a new
	// one at -1.
	form        *dataview.TableForm
	editing     int
	confirmDrop bool
}

// Open reads a model's file into a tab.
func Open(h Host, p *project.Project, path string) (*Tab, error) {
	m, was, err := datamodel.LoadFile(path)
	if err != nil {
		return nil, err
	}
	return &Tab{h: h, Project: p, Path: path, m: m, was: was}, nil
}

func (t *Tab) Title() string { return t.m.Name + " · model" }

// Connection is none: a model is a project's, not a connection's.
func (t *Tab) Connection() *connection.Conn { return nil }

func (t *Tab) CloseReason() string {
	if t.form != nil && t.form.Changed() {
		return "The table's changes are not saved to the model. Closing discards them."
	}
	return ""
}

func (t *Tab) Close() {}

// Reload reads the model's file again, as changed outside the app.
func (t *Tab) Reload() {
	m, was, err := datamodel.LoadFile(t.Path)
	if err != nil {
		t.err = err.Error()
		return
	}
	t.m, t.was, t.err, t.result, t.form = m, was, "", nil, nil
	t.rev++
	t.table = min(t.table, max(len(m.Tables)-1, 0))
}

// save writes the model's file, unless it changed on disk since it was
// read.
func (t *Tab) save() error {
	data, err := t.m.Save(t.Path, t.was)
	if err != nil {
		return err
	}
	t.was = data
	return nil
}

// conns are the project's connections of an engine.
func (t *Tab) conns(e db.Engine) []*connection.Conn {
	var out []*connection.Conn
	for _, cfg := range t.h.ProjectConfigs(t.Project) {
		if cn := t.h.ConnByID(cfg.ID); cn != nil && cfg.Engine == e {
			out = append(out, cn)
		}
	}
	return out
}

// source is the connection the model was built from, nil when it has
// none, or the project no longer does.
func (t *Tab) source() *connection.Conn {
	if t.m.Source == nil {
		return nil
	}
	return t.h.ConnByID(t.Project.Prefix + t.m.Source.Connection)
}

// update builds the model again from its source, keeping its name.
func (t *Tab) update() {
	cn := t.source()
	if cn == nil || t.updating {
		return
	}
	if cn.Config.Engine != t.m.Engine {
		t.err = fmt.Sprintf("%s is a %s connection now, and the model is of %s.", cn.Config.Name, cn.Config.Engine.Label(), t.m.Engine.Label())
		return
	}
	src := *t.m.Source
	t.updating, t.err = true, ""
	t.h.Connect(cn, func() {
		if cn.Status != connection.StatusConnected {
			t.updating, t.err = false, "Could not connect to "+cn.Config.Name+": "+cn.Err
			return
		}
		pool := cn.PoolFor(src.Database)
		t.h.Background(func() func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			var tables []db.TableDesign
			var notes []string
			d, err := pool(ctx)
			if err == nil {
				tables, notes, err = datamodel.Build(ctx, d, src.Schema, nil, nil)
			}
			return func() {
				t.updating = false
				if err != nil {
					t.err = "Could not read " + src.Schema + ": " + err.Error()
					return
				}
				was := t.m.Tables
				t.m.Tables = tables
				if err := t.save(); err != nil {
					t.m.Tables, t.err = was, "Could not save the model: "+err.Error()
					return
				}
				t.result = nil
				t.rev++
				t.table = min(t.table, max(len(tables)-1, 0))
				msg := "The model holds " + src.Schema + "'s tables as they are now."
				if len(notes) > 0 {
					msg += " " + strings.Join(notes, " ")
				}
				t.h.Toast(msg, "", nil)
			}
		})
	})
}

func (t *Tab) View(c *ui.Context) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(6, 12).Gap(10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Icon(c, widgets.IconSchema).TextColor(pal.Muted).FontSize(14)
			ui.Text(c, t.m.Name).Bold().SingleLine()
			about := fmt.Sprintf("%s · %d table%s", t.m.Engine.Label(), len(t.m.Tables), widgets.Plural(len(t.m.Tables)))
			if s := t.m.Source; s != nil {
				about += " · from " + s.Connection + ", " + s.Schema
			}
			ui.Text(c, about).FontSize(12).TextColor(pal.Muted).SingleLine().Shrink(1)
			ui.Spacer(c)
			if t.updating {
				ui.Spinner(c).Size(14, 14)
				c.After(200 * time.Millisecond)
			}
			if t.m.Source != nil && widgets.ToolButton(c, widgets.IconRefresh, "Update", "Read the model's tables again from the schema it was built from").
				Disabled(t.updating || t.source() == nil).Clicked() {
				t.confirmUpdate = true
			}
			if widgets.ToolButton(c, widgets.IconCode, "Generate DDL…", "Write the statements making the model's tables, for any engine").Clicked() {
				t.generating = newGenerateForm(t)
			}
			if widgets.ToolButton(c, widgets.IconCompare, "Compare…", "Compare the model with a database or another model").Clicked() {
				t.comparing = newCompareForm(t)
			}
		})
		if t.err != "" {
			ui.Row(c).Padding(6, 12).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, t.err).TextColor(th.Danger).Selectable().Grow(1).Shrink(1)
				if ui.Button(c, "Reload").Clicked() {
					t.Reload()
				}
			})
		}
		if t.result != nil {
			t.comparisonView(c)
		} else {
			t.tablesView(c)
		}
	})
	if t.generating != nil {
		t.generateView(c)
	}
	if t.comparing != nil {
		t.compareView(c)
	}
	if t.confirmUpdate {
		t.confirmUpdateView(c)
	}
	if t.confirmDrop {
		t.confirmDropView(c)
	}
}

func (t *Tab) confirmUpdateView(c *ui.Context) {
	s := t.m.Source
	ui.Modal(c, &t.confirmUpdate, func() {
		ui.Column(c).Width(440).Gap(12).Children(func() {
			ui.Text(c, "Update the model?").FontSize(15).Bold()
			ui.Text(c, "Its tables are read again from "+s.Connection+", "+s.Schema+", in place of those it holds.")
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					t.confirmUpdate = false
				}
				if widgets.Activated(c, ui.PrimaryButton(c, "Update")) {
					t.confirmUpdate = false
					t.update()
				}
			})
		})
	})
}

// tablesView lists the model's tables, and shows the one chosen, or the
// table form on it.
func (t *Tab) tablesView(c *ui.Context) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	t.table = min(t.table, max(len(t.m.Tables)-1, 0))
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(220).BorderWidth(0, 1, 0, 0).BorderColor(th.Border).Children(func() {
			t.tables.Selected = &t.table
			list := ui.List(c, &t.tables, len(t.m.Tables), func(i int) {
				tb := t.m.Tables[i]
				ui.Row(c).Gap(6).Padding(3, 10).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, widgets.IconTable).FontSize(12).TextColor(widgets.RowColor(c, pal.Muted, i == t.table))
					ui.Text(c, tb.Name).Font(widgets.MonoFont).FontSize(12.5).SingleLine()
				})
			}).Grow(1).Label("Tables")
			if list.Changed() && t.form != nil && !t.form.Changed() {
				t.form = nil
			}
			ui.Row(c).Padding(6).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
				if widgets.ToolButton(c, widgets.IconPlus, "New Table", "Design a table of the model").Disabled(t.form != nil && t.form.Changed()).Clicked() {
					t.edit(-1)
				}
			})
		})
		ui.Column(c).Grow(1).Children(func() {
			switch {
			case t.form != nil:
				ui.Row(c).Padding(6, 12).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
					label := "New table of the model"
					if t.editing >= 0 {
						label = "Editing " + t.m.Tables[t.editing].Name
					}
					ui.Text(c, label).FontSize(12.5).TextColor(pal.Muted)
					ui.Spacer(c)
					if ui.Button(c, "Close").Tooltip("Back to the table, its unsaved changes discarded").Clicked() {
						t.form = nil
					}
				})
				if t.form != nil {
					t.form.View(c)
				}
			case len(t.m.Tables) == 0:
				ui.Text(c, "The model has no tables: New Table designs one.").TextColor(pal.Muted).Padding(16)
			default:
				ui.Row(c).Padding(6, 12).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
					ui.Spacer(c)
					if widgets.ToolButton(c, widgets.IconPencil, "Edit Table", "Change the table in the table form").Clicked() {
						t.edit(t.table)
					}
					if widgets.ToolButton(c, widgets.IconTrash, "Drop Table…", "Take the table out of the model").Clicked() {
						t.confirmDrop = true
					}
				})
				ui.Scroll(c).Grow(1).Children(func() {
					tableDetail(c, t.m.Engine, t.m.Tables[t.table])
				})
			}
		})
	})
}

// schema is the schema a new table of the model is in: its tables'.
func (t *Tab) schema() string {
	switch {
	case len(t.m.Tables) > 0:
		return t.m.Tables[0].Schema
	case t.m.Source != nil:
		return t.m.Source.Schema
	}
	return ""
}

// edit opens the table form on a table of the model, or a new one at -1.
func (t *Tab) edit(i int) {
	start := db.TableDesign{Schema: t.schema(), Columns: []db.ColumnDesign{dataview.FirstColumn(t.m.Engine)}}
	if i >= 0 {
		start = t.m.Tables[i]
	}
	t.editing = i
	tables := func() []string {
		var out []string
		for _, tb := range t.m.Tables {
			if tb.Schema == start.Schema {
				out = append(out, tb.Name)
			}
		}
		return out
	}
	columns := func(table string) []string {
		tb, _ := t.m.Table(start.Schema, table)
		var out []string
		for _, col := range tb.Columns {
			out = append(out, col.Name)
		}
		return out
	}
	t.form = dataview.NewModelTableForm(t.h, t.m.Engine, start, tables, columns, t.saveTable)
}

// saveTable keeps a table the form designed in the model, and its file.
func (t *Tab) saveTable(design db.TableDesign) error {
	design.Name = strings.TrimSpace(design.Name)
	for i, tb := range t.m.Tables {
		if i != t.editing && tb.Schema == design.Schema && tb.Name == design.Name {
			return fmt.Errorf("the model has a table %s already", design.Name)
		}
	}
	was := slices.Clone(t.m.Tables)
	design = datamodel.Modelled(t.m.Engine, design)
	if t.editing >= 0 {
		t.m.Tables[t.editing] = design
	} else {
		t.m.Tables = append(t.m.Tables, design)
	}
	if err := t.save(); err != nil {
		t.m.Tables = was
		return err
	}
	t.rev++
	t.form = nil
	t.table = slices.IndexFunc(t.m.Tables, func(tb db.TableDesign) bool { return tb.Schema == design.Schema && tb.Name == design.Name })
	return nil
}

func (t *Tab) confirmDropView(c *ui.Context) {
	name := t.m.Tables[t.table].Name
	ui.Modal(c, &t.confirmDrop, func() {
		ui.Column(c).Width(420).Gap(12).Children(func() {
			ui.Text(c, "Drop "+name+" from the model?").FontSize(15).Bold()
			ui.Text(c, "The model's file loses the table; no database changes.")
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					t.confirmDrop = false
				}
				if ui.PrimaryButton(c, "Drop").Clicked() {
					t.confirmDrop = false
					was := slices.Clone(t.m.Tables)
					t.m.Tables = slices.Delete(t.m.Tables, t.table, t.table+1)
					if err := t.save(); err != nil {
						t.m.Tables, t.err = was, "Could not save the model: "+err.Error()
						return
					}
					t.rev++
				}
			})
		})
	})
}

// tableDetail shows a table of a model: its columns, keys, indexes and
// checks, and the statement making it.
func tableDetail(c *ui.Context, e db.Engine, tb db.TableDesign) {
	pal := widgets.PaletteOf(c)
	heading := func(s string) { ui.Text(c, s).Bold().FontSize(12.5).Padding(8, 0, 2, 0) }
	mono := func(s string) ui.Element { return ui.Text(c, s).Font(widgets.MonoFont).FontSize(12.5) }
	ui.Column(c).Padding(12, 16).Gap(4).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, tb.Name).Bold().FontSize(15).Font(widgets.MonoFont)
			if tb.Schema != "" {
				ui.Text(c, "in "+tb.Schema).FontSize(12).TextColor(pal.Muted)
			}
		})
		if tb.Comment != "" {
			ui.Text(c, tb.Comment).TextColor(pal.Muted).Selectable()
		}
		heading("Columns")
		for _, col := range tb.Columns {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				name := mono(col.Name).Width(200).SingleLine()
				if col.PrimaryKey {
					name.Bold()
				}
				mono(col.Type).Width(200).SingleLine().TextColor(pal.Muted)
				var more []string
				if col.PrimaryKey {
					more = append(more, "primary key")
				}
				if !col.Nullable && !col.PrimaryKey {
					more = append(more, "not null")
				}
				if col.AutoIncrement {
					more = append(more, "numbered")
				}
				if col.Generated {
					more = append(more, "computed")
				}
				if col.Default != "" {
					more = append(more, "default "+col.Default)
				}
				if col.Comment != "" {
					more = append(more, "— "+col.Comment)
				}
				ui.Text(c, strings.Join(more, " · ")).FontSize(12).TextColor(pal.Muted).SingleLine().Shrink(1)
			})
		}
		if len(tb.Indexes) > 0 {
			heading("Indexes")
			for _, ix := range tb.Indexes {
				what := "(" + strings.Join(ix.Columns, ", ") + ")"
				if ix.Unique {
					what = "unique " + what
				}
				ui.Row(c).Gap(12).Children(func() {
					mono(ix.Name).Width(200).SingleLine()
					mono(what).TextColor(pal.Muted).SingleLine().Shrink(1)
				})
			}
		}
		if len(tb.ForeignKeys) > 0 {
			heading("Foreign keys")
			for _, fk := range tb.ForeignKeys {
				what := "(" + strings.Join(fk.Columns, ", ") + ") → " + fk.RefTable + " (" + strings.Join(fk.RefColumns, ", ") + ")"
				if fk.OnDelete != "" {
					what += " on delete " + strings.ToLower(fk.OnDelete)
				}
				if fk.OnUpdate != "" {
					what += " on update " + strings.ToLower(fk.OnUpdate)
				}
				ui.Row(c).Gap(12).Children(func() {
					mono(fk.Name).Width(200).SingleLine()
					mono(what).TextColor(pal.Muted).SingleLine().Shrink(1)
				})
			}
		}
		if len(tb.Checks) > 0 {
			heading("Checks")
			for _, ch := range tb.Checks {
				ui.Row(c).Gap(12).Children(func() {
					mono(ch.Name).Width(200).SingleLine()
					mono(ch.Expression).TextColor(pal.Muted).SingleLine().Shrink(1)
				})
			}
		}
		heading("DDL")
		text := "-- " + e.Label() + " cannot write this table."
		if ch, err := db.NewTableChange(db.DialectOf(e), tb); err == nil {
			text = ch.Text()
		}
		ui.Text(c, text).Font(widgets.MonoFont).FontSize(12).Selectable().Padding(8).Background(pal.EditorBg).Radius(4)
	})
}
