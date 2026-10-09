package redis

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// searchHits is how many documents a search shows.
const searchHits = 100

// newIndexTemplate is what New Index puts in the console to complete.
const newIndexTemplate = "FT.CREATE idx ON HASH PREFIX 1 doc: SCHEMA title TEXT"

// searchState is the Search panel: the server's indexes, the chosen one's
// description, and a search of it.
type searchState struct {
	asked     bool
	loading   bool
	err       string
	indexes   []string
	index     int // the chosen index's row
	info      *db.SearchIndex
	query     string
	total     int64
	hits      []db.IndexHit
	searched  bool
	reads     int // counts the reads: a slower one's result is dropped
	indexList ui.ListState
	hitList   ui.ListState
}

func (s *searchState) chosen() string {
	if s.index >= 0 && s.index < len(s.indexes) {
		return s.indexes[s.index]
	}
	return ""
}

// loadIndexes reads the server's search indexes, and the chosen one's
// description.
func (r *Tab) loadIndexes() {
	s := &r.search
	s.asked, s.loading, s.err = true, true, ""
	s.reads++
	kv, chosen, read := r.conn.KV, s.chosen(), s.reads
	r.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		indexes, err := kv.SearchIndexes(ctx)
		var info *db.SearchIndex
		if err == nil && chosen != "" {
			if i, ierr := kv.SearchIndexInfo(ctx, chosen); ierr == nil {
				info = &i
			} else {
				err = ierr
			}
		}
		return func() {
			if read != s.reads {
				return
			}
			s.loading = false
			if err != nil {
				s.err = err.Error()
				return
			}
			s.indexes, s.info, s.index = indexes, info, -1
			for i, name := range indexes {
				if name == chosen {
					s.index = i
				}
			}
			if s.index < 0 {
				s.info = nil
			}
		}
	})
}

// runSearch searches the chosen index.
func (r *Tab) runSearch() {
	s := &r.search
	index := s.chosen()
	if index == "" {
		return
	}
	s.loading, s.err = true, ""
	s.reads++
	kv, query, read := r.conn.KV, strings.TrimSpace(s.query), s.reads
	r.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		total, hits, err := kv.Search(ctx, index, query, searchHits)
		return func() {
			if read != s.reads {
				return
			}
			s.loading, s.searched = false, true
			if err != nil {
				s.err = err.Error()
				return
			}
			s.total, s.hits = total, hits
		}
	})
}

func (r *Tab) searchActions(c *ui.Context) {
	s := &r.search
	if s.loading {
		ui.Spinner(c).Size(12, 12)
	}
	if ui.Link(c, "Refresh", "").FontSize(12).Clicked() {
		r.loadIndexes()
	}
	if r.conn.Config.ReadOnly {
		return
	}
	if ui.Link(c, "New Index…", "").FontSize(12).Clicked() {
		// FT.CREATE's schema is the index: written in the console, its
		// syntax and help beside it.
		r.panel, r.consoleIn, r.caretToEnd = panelConsole, newIndexTemplate, true
	}
	if index := s.chosen(); index != "" && ui.Link(c, "Drop Index", "").FontSize(12).Clicked() {
		r.a.AskDiscard("Drop the index "+index+"?", "The index goes; the keys it indexes stay.", func() {
			r.write([]string{"FT.DROPINDEX", index}, r.loadIndexes)
		})
	}
}

func (r *Tab) searchView(c *ui.Context) {
	s := &r.search
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	if !r.conn.KV.HasSearch() {
		ui.Text(c, "This server has no Redis Search: it comes with Redis 8, or as a module before.").FontSize(12.5).TextColor(pal.Muted).Padding(8, 12)
		return
	}
	if db := r.conn.Config.Database; db != "" && db != "0" {
		ui.Text(c, "Search indexes are of database 0, and this connection opens database "+db+": connect to database 0 to search.").FontSize(12.5).TextColor(pal.Muted).Padding(8, 12)
		return
	}
	if !s.asked {
		r.loadIndexes()
	}
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(200).BorderWidth(0, 1, 0, 0).BorderColor(th.Border).Children(func() {
			s.indexList.Selected = &s.index
			list := ui.List(c, &s.indexList, len(s.indexes), func(i int) {
				ui.Text(c, s.indexes[i]).Font(widgets.MonoFont).FontSize(12.5).Padding(3, 10).SingleLine()
			}).Grow(1).Label("Search indexes")
			if list.Changed() {
				s.hits, s.searched, s.info = nil, false, nil
				r.loadIndexes()
			}
		})
		ui.Column(c).Grow(1).Padding(6, 12).Gap(6).Children(func() {
			if s.err != "" {
				ui.Text(c, s.err).FontSize(12).TextColor(th.Danger).Selectable()
			}
			if s.info == nil {
				if len(s.indexes) == 0 && !s.loading {
					ui.Text(c, "The server has no search index yet.").FontSize(12.5).TextColor(pal.Muted)
				} else if s.chosen() == "" {
					ui.Text(c, "Choose an index to search it.").FontSize(12.5).TextColor(pal.Muted)
				}
				return
			}
			fields := make([]string, len(s.info.Fields))
			for i, f := range s.info.Fields {
				fields[i] = f.As + " " + f.Type
			}
			ui.Text(c, fmt.Sprintf("%s keys starting %s · %s documents · %s", s.info.On, strings.Join(s.info.Prefixes, ", "), widgets.HumanCount(s.info.Docs), strings.Join(fields, ", "))).
				FontSize(12).TextColor(pal.Muted).SingleLine().Tooltip(strings.Join(fields, ", "))
			ui.Row(c).Gap(6).Children(func() {
				in := ui.TextInput(c, &s.query).Placeholder("A query, e.g. hello @n:[1 10], * for every document").Font(widgets.MonoFont).FontSize(12.5).Grow(1).Label("Search query")
				if ui.Button(c, "Search").Clicked() || in.Submitted() {
					r.runSearch()
				}
			})
			if s.searched {
				ui.Text(c, fmt.Sprintf("%s documents match; the first %d show.", widgets.HumanCount(s.total), len(s.hits))).FontSize(12).TextColor(pal.Muted)
			}
			ui.List(c, &s.hitList, len(s.hits), func(i int) {
				h := s.hits[i]
				row := ui.ButtonBase(c).Padding(3, 6).Label(h.Key)
				row.Children(func() {
					ui.Row(c).Gap(10).FillWidth().Children(func() {
						ui.Text(c, h.Key).Font(widgets.MonoFont).FontSize(12).Width(220).SingleLine()
						parts := make([]string, len(h.Fields))
						for j, f := range h.Fields {
							parts[j] = f[0] + "=" + f[1]
						}
						ui.Text(c, widgets.OneLine(strings.Join(parts, "  "), 300)).Font(widgets.MonoFont).FontSize(12).TextColor(pal.Muted).SingleLine().Grow(1).Shrink(1)
					})
				})
				if row.Clicked() {
					r.open(h.Key)
				}
			}).Grow(1).Label("Documents found")
		})
	})
}
