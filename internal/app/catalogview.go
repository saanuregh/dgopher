package app

import (
	"fmt"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/redact"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// catalogView shows the catalog queries the app ran on a connection, the
// newest first.
type catalogView struct {
	open    bool
	conn    *connection.Conn
	filter  string
	entries []db.CatalogQuery
	read    time.Time // when entries were taken from the log
	sel     int
	list    ui.ListState
}

func (a *App) openCatalogQueries(cn *connection.Conn) {
	v := &catalogView{open: true, conn: cn}
	v.list.Selected = &v.sel
	a.catalogQueries = v
}

// shown are the entries the filter keeps.
func (v *catalogView) shown() []db.CatalogQuery {
	f := strings.ToLower(strings.TrimSpace(v.filter))
	if f == "" {
		return v.entries
	}
	var out []db.CatalogQuery
	for _, e := range v.entries {
		if strings.Contains(strings.ToLower(e.SQL+" "+e.Err), f) {
			out = append(out, e)
		}
	}
	return out
}

func (a *App) catalogQueriesView(c *ui.Context) {
	v := a.catalogQueries
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	// The log grows as the app reads: taken again every second.
	if pool := v.conn.DB; pool != nil && time.Since(v.read) >= time.Second {
		v.entries, v.read = pool.CatalogQueries(), time.Now()
	}
	c.After(time.Second)
	shown := v.shown()
	v.sel = min(v.sel, max(0, len(shown)-1))
	ui.DialogBase(c, &v.open, func(backdrop, panel ui.Element) {
		backdrop.Background(widgets.Backdrop)
		panel.Width(900).Height(600).Radius(12).Background(th.Background).Border(1, th.Border).Clip().Label("Catalog queries")
		ui.Row(c).Padding(12, 16).Gap(10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Text(c, "Catalog Queries · "+v.conn.Config.Name).FontSize(15).Bold().SingleLine().Shrink(1)
			ui.Text(c, fmt.Sprintf("the latest %d", len(v.entries))).FontSize(12).TextColor(pal.Muted).Grow(1)
			widgets.SearchBox(c, &v.filter, "Filter by SQL", 220).AutoFocus()
		})
		ui.Text(c, "What the app reads on its own, as the navigator's tables and an editor's completions, beside the statements the audit log keeps.").
			FontSize(12).TextColor(pal.Muted).Padding(8, 16)
		ui.List(c, &v.list, len(shown), func(i int) {
			e := shown[i]
			muted, danger := widgets.RowColor(c, pal.Muted, i == v.sel), widgets.RowColor(c, th.Danger, i == v.sel)
			ui.Row(c).Padding(5, 16).Gap(10).Children(func() {
				ui.Text(c, e.At.Format("15:04:05.000")).Font(widgets.MonoFont).FontSize(11.5).TextColor(muted)
				ui.Text(c, widgets.FormatDuration(e.Took)).FontSize(11.5).TextColor(muted).Width(56).TextAlign(ui.End)
				if e.Err != "" {
					ui.Text(c, "failed").FontSize(11.5).TextColor(danger).Tooltip(e.Err)
				}
				ui.Text(c, widgets.OneLine(redact.Secrets(e.SQL), 200)).Font(widgets.MonoFont).FontSize(12).SingleLine().Grow(1).Shrink(1)
			})
		}).Grow(1).Label("Catalog queries").Dividers(1, th.Border).Children(func() {
			if len(shown) == 0 {
				ui.Text(c, "No catalog query yet.").TextColor(pal.Muted).Padding(16)
			}
		})
		if v.sel < len(shown) {
			e := shown[v.sel]
			text := redact.Secrets(e.SQL)
			if len(e.Args) > 0 {
				args := make([]string, len(e.Args))
				for i, arg := range e.Args {
					args[i] = db.Display(arg)
				}
				text += "\n-- arguments: " + strings.Join(args, ", ")
			}
			if e.Err != "" {
				text += "\n-- " + e.Err
			}
			ui.Column(c).Padding(10, 16).Gap(6).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
				ui.Scroll(c).MaxHeight(150).Radius(6).Background(pal.EditorBg).Children(func() {
					ui.Text(c, text).Font(widgets.MonoFont).FontSize(12).Padding(8, 10).Selectable()
				})
				ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, "Copy").Clicked() {
						a.WriteClipboard(text)
					}
					if ui.Button(c, "Close").Clicked() {
						v.open = false
					}
				})
			})
		}
	})
	if !v.open {
		a.catalogQueries = nil
	}
}
