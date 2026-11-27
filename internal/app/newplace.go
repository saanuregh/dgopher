package app

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"dgopher/internal/connection"
	"dgopher/internal/ui/widgets"
)

// A new SQL editor asks where it opens, when there is more than one
// place: a connection, and a database of it. One rule for every way of
// asking for an editor that names no place, as the key, the palette's
// command and the Queries heading's button; a guess from the tab in front
// or the navigator's chosen row could not be seen before it was made, nor
// reach a database far from both. The place at hand is first and chosen,
// so ↵ alone gives another editor there. What names its place, as a
// database's row or a connection's menu, opens at once.

// newEditor opens a SQL editor: at once where there is one place and no
// server whose databases are still to be read, else on the place chosen in
// the picker.
func (a *App) newEditor() {
	places, _ := a.editorPlaces()
	switch {
	case len(places) == 0:
		a.openPalette(false) // no connection yet: what makes one is there
	case len(places) == 1 && len(a.unlistedServers()) == 0:
		a.NewQueryTab(places[0].Conn, places[0].Database, "")
	default:
		p := &paletteState{open: true, places: true, shownQ: "\x00"}
		a.listPlaces(p)
		a.palette = p
	}
}

// listPlaces lists the picker's rows, again whenever what they are made
// of changes: a server that connects while the picker is open shows its
// databases there. After the places, a row for each server whose
// databases were never read, which connects it to read them and leaves
// the picker open for them.
func (a *App) listPlaces(p *paletteState) {
	places, here := a.editorPlaces()
	servers := a.unlistedServers()
	var of strings.Builder
	for _, at := range places {
		fmt.Fprintf(&of, "%s/%s/%d\n", at.Conn.Config.ID, at.Database, at.Conn.Status)
	}
	for _, cn := range servers {
		fmt.Fprintf(&of, "?%s/%d\n", cn.Config.ID, cn.Status)
	}
	if of.String() == p.placesOf {
		return
	}
	p.placesOf, p.shownQ = of.String(), "\x00"
	p.items = p.items[:0]
	for i, at := range places {
		p.items = append(p.items, a.placeItem(at, here && i == 0))
	}
	for _, cn := range servers {
		detail := "connect to list its databases"
		if cn.Status == connection.StatusConnecting {
			detail = "connecting…"
		}
		p.items = append(p.items, paletteItem{title: cn.Config.Name, detail: detail, group: cn.Config.Engine.Label(), icon: widgets.IconDatabase, conn: cn, stay: true,
			run: func() { a.Connect(cn, func() { a.loadSchemas(cn, "") }) }})
	}
}

// unlistedServers are the servers that are not open and whose databases
// were never read: the picker can list only their own until they are.
func (a *App) unlistedServers() []*connection.Conn {
	var out []*connection.Conn
	for _, cn := range a.conns {
		e := cn.Config.Engine
		if cn.Status != connection.StatusConnected && e.IsSQL() && !e.IsFile() && len(loadKnownDatabases(cn.Project)[localConnID(cn)]) == 0 {
			out = append(out, cn)
		}
	}
	return out
}

// editorPlaces are the places an editor can open on: the place at hand
// first, which here reports there is, then the other databases of its
// connection, then the other connections with theirs. Only databases
// already listed are known: a connection not open is one place, its own
// database.
func (a *App) editorPlaces() (places []connection.Place, here bool) {
	at, here := a.activePlace()
	if here {
		places = append(places, at)
	}
	add := func(cn *connection.Conn) {
		for _, p := range editorPlacesOf(cn) {
			if !here || p != at {
				places = append(places, p)
			}
		}
	}
	if here {
		add(at.Conn)
	}
	for _, cn := range a.conns {
		if !here || cn != at.Conn {
			add(cn)
		}
	}
	return places, here
}

// editorPlacesOf are a connection's places: its databases, as the navigator
// lists them once it is open, and while it is not, those read of it the
// last time it was; its own alone when none are known.
func editorPlacesOf(cn *connection.Conn) []connection.Place {
	dbs := cn.Databases
	if cn.Status != connection.StatusConnected {
		dbs = loadKnownDatabases(cn.Project)[localConnID(cn)]
	}
	own := cn.PlaceOf("")
	if len(dbs) < 2 {
		return []connection.Place{own}
	}
	places := make([]connection.Place, len(dbs))
	for i, d := range dbs {
		places[i] = cn.PlaceOf(d)
	}
	if !slices.Contains(places, own) {
		places = slices.Insert(places, 0, own) // its database changed since
	}
	return places
}

// placeItem is a place's row in the picker: the connection, then its
// database, or why it has none to show.
func (a *App) placeItem(at connection.Place, here bool) paletteItem {
	cn := at.Conn
	detail := cmp.Or(at.Database, cn.Config.Database)
	if cn.Config.Engine.IsFile() {
		detail = filepath.Base(detail) // its folders say nothing here
	}
	switch {
	case cn.Config.Engine.IsKeyValue():
		detail = "key browser"
	case cn.Config.Engine.IsDocument():
		detail = "console"
	case cn.Status != connection.StatusConnected:
		detail = strings.TrimPrefix(detail+" · not connected", " · ")
	}
	group := cn.Config.Engine.Label()
	if here {
		group = "where you are"
	}
	return paletteItem{title: cn.Config.Name, detail: detail, group: group, icon: widgets.IconDatabase, conn: cn,
		run: func() { a.NewQueryTab(cn, at.Database, "") }}
}
