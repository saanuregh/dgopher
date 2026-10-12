package app

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"dgopher/internal/connection"
	"dgopher/internal/ui/widgets"
)

// tabPlaces are where a tab's work runs (widgets.Placed): an editor's or a
// table's connection and database, a dashboard's panels', a data model's
// source.
func tabPlaces(t widgets.Tab) []connection.Place {
	if p, ok := t.(widgets.Placed); ok {
		return p.Places()
	}
	if cn := t.Connection(); cn != nil {
		return []connection.Place{{Conn: cn}}
	}
	return nil
}

// connPlaces are the places of one connection: its databases.
type connPlaces struct {
	conn      *connection.Conn
	databases []string
}

// byConnection groups places by their connection, each database once,
// problems first (connection.Conn.Rank), else in their order.
func byConnection(places []connection.Place) []connPlaces {
	var out []connPlaces
	for _, pl := range places {
		i := slices.IndexFunc(out, func(g connPlaces) bool { return g.conn == pl.Conn })
		if i < 0 {
			out = append(out, connPlaces{conn: pl.Conn})
			i = len(out) - 1
		}
		if !slices.Contains(out[i].databases, pl.Database) {
			out[i].databases = append(out[i].databases, pl.Database)
		}
	}
	slices.SortStableFunc(out, func(x, y connPlaces) int { return cmp.Compare(x.conn.Rank(), y.conn.Rank()) })
	return out
}

// where is which of its databases a connection's places are: the one
// when not its own, how many when several, "" for its own.
func (g connPlaces) where() string {
	switch {
	case len(g.databases) > 1:
		return fmt.Sprintf("%d databases", len(g.databases))
	case len(g.databases) == 1:
		return g.databases[0]
	}
	return ""
}

// label names a connection's places: its name, and where.
func (g connPlaces) label() string {
	if w := g.where(); w != "" {
		return g.conn.Config.Name + " · " + w
	}
	return g.conn.Config.Name
}

// placesSubtitle is what a tab shows under its title: its connection's
// places, or the first connection's and how many more.
func placesSubtitle(groups []connPlaces) string {
	switch len(groups) {
	case 0:
		return ""
	case 1:
		return groups[0].label()
	}
	return fmt.Sprintf("%s +%d", groups[0].label(), len(groups)-1)
}

// tabSubtitle is a tab's subtitle (placesSubtitle).
func tabSubtitle(t widgets.Tab) string { return placesSubtitle(byConnection(tabPlaces(t))) }

// placesTooltip lists every place, with its connection's state.
func placesTooltip(groups []connPlaces) string {
	var lines []string
	for _, g := range groups {
		for _, database := range g.databases {
			name := g.conn.Config.Name
			if shown := cmp.Or(database, g.conn.Config.Database); shown != "" {
				name += " · " + shown
			}
			lines = append(lines, name+" — "+connStateText(g.conn))
		}
	}
	return strings.Join(lines, "\n")
}

// connStateText says how a connection stands.
func connStateText(cn *connection.Conn) string {
	switch cn.Status {
	case connection.StatusConnected:
		return "connected"
	case connection.StatusConnecting:
		return "connecting…"
	case connection.StatusFailed:
		return "failed: " + widgets.FirstLine(cn.Err)
	}
	return "not connected"
}
