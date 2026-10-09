package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// searchTab finds the objects of a database by their names, and by what
// their definitions say.
type searchTab struct {
	a        *App
	conn     *connection.Conn
	database string

	query       string
	definitions bool
	// searched is the query the hits are of.
	searched  string
	hits      []db.SearchHit
	more      bool
	searching bool
	err       string
	row       int
	list      ui.ListState
}

// openSearch opens the search of a database of a connection, "" for the
// one it connects to.
func (a *App) openSearch(cn *connection.Conn, database string) {
	if !cn.Config.Engine.IsSQL() {
		return
	}
	if a.ActivateTab(func(t widgets.Tab) bool {
		st, ok := t.(*searchTab)
		return ok && st.conn == cn && st.database == database
	}) {
		return
	}
	a.Connect(cn, func() {
		t := &searchTab{a: a, conn: cn, database: database, definitions: true, row: -1}
		t.list.Selected = &t.row
		a.AddTab(t)
	})
}

func (t *searchTab) Title() string {
	if t.database != "" {
		return t.conn.Config.Name + " · " + t.database + " · search"
	}
	return t.conn.Config.Name + " · search"
}
func (t *searchTab) Connection() *connection.Conn { return t.conn }
func (t *searchTab) CloseReason() string          { return "" }
func (t *searchTab) Close()                       {}

// search runs the query, unless it is empty or running.
func (t *searchTab) search() {
	text := strings.TrimSpace(t.query)
	if text == "" || t.searching {
		return
	}
	t.searching = true
	poolOf, definitions := t.conn.PoolFor(t.database), t.definitions
	t.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		d, err := poolOf(ctx)
		var hits []db.SearchHit
		var more bool
		if err == nil {
			hits, more, err = db.Search(ctx, d, text, definitions)
		}
		return func() {
			t.searching = false
			t.searched, t.hits, t.more, t.row = text, hits, more, -1
			t.err = ""
			if err != nil {
				t.err = err.Error()
			}
			if len(hits) > 0 {
				t.row = 0
			}
		}
	})
}

// open opens a hit: a table's rows, or its definition when that is
// where the text was found; a column's table's structure; an item's
// definition.
func (t *searchTab) open(h db.SearchHit) {
	switch {
	case h.Kind == db.KindColumn:
		t.a.OpenTable(t.conn, t.database, h.Object(), dataview.PageStructure)
	case h.IsObject():
		page := dataview.PageData
		if h.Excerpt != "" {
			page = dataview.PageDDL
		}
		t.a.OpenTable(t.conn, t.database, h.Object(), page)
	default:
		t.a.openItem(t.conn, t.database, h.Item())
	}
}

func (t *searchTab) View(c *ui.Context) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(8, 12).Gap(10).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Icon(c, widgets.IconSearch).TextColor(pal.Muted).FontSize(14)
			in := ui.TextInput(c, &t.query).Placeholder("Find tables, columns, routines… by name or definition").
				AutoFocus().Grow(1).Label("Search")
			switch {
			case in.Shortcut(0, ui.KeyDown) && len(t.hits) > 0:
				t.row = min(t.row+1, len(t.hits)-1)
				t.list.ScrollIntoView(t.row)
			case in.Shortcut(0, ui.KeyUp) && len(t.hits) > 0:
				t.row = max(t.row-1, 0)
				t.list.ScrollIntoView(t.row)
			case in.Submitted():
				// Enter searches a new query, and opens the hit chosen
				// among those of the query searched.
				if strings.TrimSpace(t.query) == t.searched && t.row >= 0 && t.row < len(t.hits) {
					t.open(t.hits[t.row])
				} else {
					t.search()
				}
			}
			ui.Checkbox(c, &t.definitions, "In definitions").
				Tooltip("Search the queries of views and the bodies of routines and triggers too")
			if ui.PrimaryButton(c, "Search").Disabled(strings.TrimSpace(t.query) == "" || t.searching).Clicked() {
				t.search()
			}
		})
		ui.Row(c).Padding(6, 12).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Text(c, t.status()).FontSize(12).TextColor(pal.Muted).Grow(1).Shrink(1).SingleLine()
			if t.searching {
				ui.Spinner(c).Size(12, 12)
			}
		})
		if t.err != "" {
			ui.Text(c, t.err).TextColor(th.Danger).Padding(10, 12).Selectable()
		}
		list := ui.List(c, &t.list, len(t.hits), func(i int) {
			t.hitRow(c, t.hits[i], i == t.row)
		}).Grow(1).Label("Objects found").Dividers(1, th.Border)
		if list.Submitted() && t.row >= 0 && t.row < len(t.hits) {
			t.open(t.hits[t.row])
		}
	})
}

// status says where the search looks, or what it found.
func (t *searchTab) status() string {
	where := t.conn.Config.Name
	if t.database != "" {
		where += " · " + t.database
	}
	switch {
	case t.searched == "":
		return "Searches every schema of " + where + "."
	case t.more:
		return fmt.Sprintf("The first %d objects matching %q in %s.", len(t.hits), t.searched, where)
	case len(t.hits) == 0:
		return fmt.Sprintf("Nothing matches %q in %s.", t.searched, where)
	}
	return fmt.Sprintf("%d object%s matching %q in %s.", len(t.hits), widgets.Plural(len(t.hits)), t.searched, where)
}

// hitRow is a hit of the search, on the selection's color when chosen.
func (t *searchTab) hitRow(c *ui.Context, h db.SearchHit, chosen bool) {
	muted := widgets.RowColor(c, widgets.PaletteOf(c).Muted, chosen)
	ui.Column(c).Padding(6, 12).Gap(2).Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.Icon(c, searchIcon(h)).TextColor(muted).FontSize(13)
			name := h.Name
			switch {
			case h.Kind == db.KindColumn:
				name = h.Table + "." + h.Name
			case !h.IsObject():
				name = h.Item().Label()
			}
			ui.Text(c, name).SingleLine().Shrink(1)
			if h.Kind == db.KindColumn {
				ui.Text(c, h.Detail).FontSize(11).TextColor(muted).SingleLine().Shrink(1)
			}
			ui.Spacer(c)
			ui.Text(c, h.Kind).FontSize(11).TextColor(muted)
			ui.Text(c, h.Schema).FontSize(11).TextColor(muted).SingleLine()
		})
		if h.Excerpt != "" {
			ui.Text(c, h.Excerpt).Font(widgets.MonoFont).FontSize(12).TextColor(muted).SingleLine().PaddingX(21)
		}
	})
}

func searchIcon(h db.SearchHit) *ui.SVG {
	switch {
	case h.Kind == db.KindColumn:
		return widgets.IconColumns
	case h.Kind == string(db.KindTable):
		return widgets.IconTable
	case h.IsObject():
		return widgets.IconView
	}
	return itemIcon(db.ItemKind(h.Kind))
}
