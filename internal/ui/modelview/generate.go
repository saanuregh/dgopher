package modelview

import (
	"fmt"
	"slices"
	"strings"

	"dgopher/internal/datamodel"
	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// generateForm asks for what engine and schema a model's DDL is written.
type generateForm struct {
	open    bool
	engines []string // the labels of the engines a model may be written for
	engine  string
	schema  string
	conn    string // the connection whose editor the script opens in

	// The script, written again once the engine or schema changes.
	wrote string // the engine and schema it was written for
	text  string
	notes bool
}

// previewBytes is how much of a script the dialog shows.
const previewBytes = 64 << 10

// engines are the engines a model is written for, in the order the app
// offers them.
func engines() []db.Engine {
	var out []db.Engine
	for _, e := range db.Engines() {
		if datamodel.Engine(e) {
			out = append(out, e)
		}
	}
	return out
}

func newGenerateForm(t *Tab) *generateForm {
	f := &generateForm{open: true, engine: t.m.Engine.Label()}
	for _, e := range engines() {
		f.engines = append(f.engines, e.Label())
	}
	if s := t.m.Source; s != nil {
		f.schema = s.Schema
	}
	return f
}

// target is the engine chosen.
func (f *generateForm) target() db.Engine {
	all := engines()
	return all[max(slices.Index(f.engines, f.engine), 0)]
}

func (t *Tab) generateView(c *ui.Context) {
	f := t.generating
	pal := widgets.PaletteOf(c)
	to := f.target()
	schema := strings.TrimSpace(f.schema)
	if key := fmt.Sprint(to, "\x00", schema, "\x00", t.rev); key != f.wrote {
		ch, notes := datamodel.Script(t.m, to, schema)
		f.wrote, f.text, f.notes = key, datamodel.ScriptText(t.m, to, ch, notes), len(notes) > 0
	}
	text := f.text
	conns := t.conns(to)
	names := make([]string, len(conns))
	for i, cn := range conns {
		names[i] = cn.Config.Name
	}
	if !slices.Contains(names, f.conn) && len(names) > 0 {
		f.conn = names[0]
	}
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(720).Height(560).Gap(12).Children(func() {
			ui.Text(c, "Generate DDL").FontSize(15).Bold()
			ui.Row(c).Gap(12).Children(func() {
				ui.Field(c, "Engine", func() {
					ui.Select(c, &f.engine, f.engines).Label("Engine").Width(180)
				})
				ui.Field(c, "Schema", func() {
					ui.TextInput(c, &f.schema).Placeholder("none: the editor's own").Font(widgets.MonoFont).Width(220).Label("Schema")
				}).Description("The tables are made in it, and their keys point into it.")
			})
			if f.notes {
				ui.Text(c, "The script leaves out what "+to.Label()+" cannot have of the model; its first lines say what.").FontSize(12).TextColor(pal.Muted)
			}
			ui.Scroll(c).Grow(1).Background(pal.EditorBg).Radius(4).Children(func() {
				ui.Text(c, widgets.TextStart(text, previewBytes)).Font(widgets.MonoFont).FontSize(12).Selectable().Padding(8)
			})
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if len(conns) > 0 {
					ui.Text(c, "Open in an editor of").FontSize(12.5).TextColor(pal.Muted)
					ui.Select(c, &f.conn, names).Label("Connection").Width(200)
				}
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					f.open = false
				}
				if ui.Button(c, "Copy").Clicked() {
					t.h.WriteClipboard(text)
					t.h.Toast("The script is copied.", "", nil)
					f.open = false
				}
				if i := slices.Index(names, f.conn); len(conns) > 0 && ui.PrimaryButton(c, "Open in Editor").Clicked() && i >= 0 {
					cn := conns[i]
					t.h.NewQueryTab(cn, cn.Config.Database, text)
					f.open = false
				}
			})
		})
	})
	if !f.open {
		t.generating = nil
	}
}
