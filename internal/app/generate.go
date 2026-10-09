package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/schemadoc"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// What the generate dialog writes.
const (
	generateScript = iota
	generateDocs
)

// Formats of the documentation.
const (
	docsHTML = iota
	docsMarkdown
)

// generateState is the dialog writing the script creating a schema's
// objects, or documentation of them, from those the user chooses.
type generateState struct {
	open             bool
	conn             *connection.Conn
	database, schema string

	output, format int
	choices        []generateChoice
	filter         string
	loading        bool
	err            string

	running     bool
	done, total atomic.Int64
	cancel      context.CancelFunc
}

// generateChoice is an object the user may choose to write.
type generateChoice struct {
	object *db.Object
	item   *db.Item
	chosen bool
}

func (ch generateChoice) label() string {
	if ch.object != nil {
		return ch.object.Name
	}
	return ch.item.Label()
}

func (ch generateChoice) kind() string {
	if ch.object != nil {
		return string(ch.object.Kind)
	}
	return string(ch.item.Kind)
}

// openGenerate opens the dialog on a schema's objects, every one chosen.
func (a *App) openGenerate(cn *connection.Conn, database, schema string, output int) {
	g := &generateState{open: true, conn: cn, database: database, schema: schema, output: output, loading: true}
	a.generating = g
	poolOf := cn.PoolFor(database)
	a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		d, err := poolOf(ctx)
		var objs []db.Object
		var items []db.Item
		if err == nil {
			objs, err = d.Dialect.Objects(ctx, d.Catalog(), schema)
		}
		if err == nil {
			items, err = d.Dialect.Items(ctx, d.Catalog(), schema)
		}
		return func() {
			g.loading = false
			if err != nil {
				g.err = err.Error()
				return
			}
			for i := range objs {
				g.choices = append(g.choices, generateChoice{object: &objs[i], chosen: true})
			}
			for i := range items {
				g.choices = append(g.choices, generateChoice{item: &items[i], chosen: true})
			}
		}
	})
}

// shown is the choices the filter keeps.
func (g *generateState) shown() []int {
	f := strings.ToLower(strings.TrimSpace(g.filter))
	var out []int
	for i, ch := range g.choices {
		if f == "" || strings.Contains(strings.ToLower(ch.label()), f) || strings.Contains(ch.kind(), f) {
			out = append(out, i)
		}
	}
	return out
}

// title names what is written: the schema, its database, its
// connection.
func (g *generateState) title() string {
	name := g.schema
	if g.database != "" {
		name = g.database + "." + g.schema
	}
	return name + " · " + g.conn.Config.Name
}

// generate reads the chosen objects and writes what the dialog says: the
// script into a new editor, documentation into a file the user names.
func (a *App) generate(g *generateState) {
	var objs []db.Object
	var items []db.Item
	for _, ch := range g.choices {
		switch {
		case !ch.chosen:
		case ch.object != nil:
			objs = append(objs, *ch.object)
		default:
			items = append(items, *ch.item)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	g.running, g.cancel, g.err = true, cancel, ""
	g.done.Store(0)
	g.total.Store(int64(len(objs) + len(items)))
	poolOf, output, format, title := g.conn.PoolFor(g.database), g.output, g.format, g.title()
	a.Background(func() func() {
		defer cancel()
		d, err := poolOf(ctx)
		var s *schemadoc.Schema
		if err == nil {
			s, err = schemadoc.Read(ctx, d, objs, items, output == generateDocs, func(done, _ int) { g.done.Store(int64(done)) })
		}
		var text string
		if err == nil {
			switch {
			case output == generateScript:
				text = schemadoc.Script(s)
			case format == docsMarkdown:
				text = schemadoc.Markdown(s, title)
			default:
				text, err = schemadoc.HTML(s, title)
			}
		}
		canceled := ctx.Err() != nil
		return func() {
			g.running = false
			switch {
			case a.generating != g || canceled:
				return // closed or canceled
			case err != nil:
				g.err = err.Error()
			case output == generateScript:
				g.open = false
				a.NewQueryTab(g.conn, g.database, text)
			default:
				g.open = false
				a.saveDocs(g.schema, format, text)
			}
		}
	})
}

// saveDocs writes documentation to a file the user names.
func (a *App) saveDocs(schema string, format int, text string) {
	ext := ".html"
	if format == docsMarkdown {
		ext = ".md"
	}
	go func() {
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: "Save the Documentation", DefaultPath: schema + ext})
		if err != nil || path == "" {
			return
		}
		err = os.WriteFile(path, []byte(text), 0o644)
		a.Post(func() {
			if err != nil {
				a.ShowError("Could not save the documentation", err.Error())
				return
			}
			a.toast = &pendingToast{text: "Saved the documentation to " + filepath.Base(path)}
		})
	}()
}

func (a *App) generateView(c *ui.Context) {
	g := a.generating
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Modal(c, &g.open, func() {
		ui.Column(c).Width(620).Gap(12).Children(func() {
			ui.Text(c, "Generate from "+g.title()).FontSize(15).Bold().SingleLine()
			ui.Row(c).Gap(12).Children(func() {
				ui.Segmented(c, &g.output, "SQL Script", "Documentation").Label("Output")
				if g.output == generateDocs {
					ui.Segmented(c, &g.format, "HTML", "Markdown").Label("Format")
				}
			})
			shown := g.shown()
			chosen := 0
			for _, ch := range g.choices {
				if ch.chosen {
					chosen++
				}
			}
			ui.Row(c).Gap(8).Children(func() {
				widgets.SearchBox(c, &g.filter, "Filter objects", 0).Grow(1)
				if ui.Button(c, "All").Disabled(g.running).Clicked() {
					for _, i := range shown {
						g.choices[i].chosen = true
					}
				}
				if ui.Button(c, "None").Disabled(g.running).Clicked() {
					for _, i := range shown {
						g.choices[i].chosen = false
					}
				}
			})
			ui.Scroll(c).Height(320).Border(1, th.Border).Radius(6).Children(func() {
				ui.Column(c).Padding(4).Children(func() {
					if g.loading {
						ui.Spinner(c).Size(14, 14).Margin(12)
					}
					for _, i := range shown {
						ch := &g.choices[i]
						ui.Row(c.Key(fmt.Sprint("choice-", i))).Padding(3, 6).Gap(8).Children(func() {
							ui.Checkbox(c, &ch.chosen, ch.label()).Disabled(g.running).Grow(1).Shrink(1)
							ui.Text(c, ch.kind()).FontSize(11).TextColor(pal.Muted)
						})
					}
					if !g.loading && len(shown) == 0 {
						ui.Text(c, "No objects.").TextColor(pal.Muted).Padding(12)
					}
				})
			})
			status := fmt.Sprintf("%d of %d objects chosen", chosen, len(g.choices))
			if g.running {
				status = fmt.Sprintf("Reading %d of %d objects…", g.done.Load(), g.total.Load())
				c.After(200 * time.Millisecond)
			}
			ui.Text(c, status).FontSize(12).TextColor(pal.Muted)
			if g.err != "" {
				ui.Text(c, g.err).TextColor(th.Danger).Selectable()
			}
			ui.Row(c).Gap(8).Children(func() {
				if g.running {
					ui.Spinner(c).Size(14, 14)
				}
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					if g.running {
						g.cancel()
					} else {
						g.open = false
					}
				}
				action := "Open in Editor"
				if g.output == generateDocs {
					action = "Save…"
				}
				if widgets.Activated(c, ui.PrimaryButton(c, action).Disabled(g.running || g.loading || chosen == 0)) {
					a.generate(g)
				}
			})
		})
	})
	if !g.open {
		if g.cancel != nil {
			g.cancel()
		}
		a.generating = nil
	}
}
