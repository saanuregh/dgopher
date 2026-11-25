package app

import (
	"slices"
	"strings"

	"dgopher/internal/connection"
	"dgopher/internal/project"
	"dgopher/internal/ui/widgets"
)

// What the navigator keeps from one run to the next: the headings left
// closed, in the layout, and the databases read of each server, in its
// project's own state, for what lists them while it is not connected.

// headingKey names a heading in the layout's list of those left closed,
// "" for one that is not kept: the two that open by themselves are.
func headingKey(n navNode) string {
	switch n.kind {
	case nodeConnections:
		return "connections:" + n.projectDir
	case nodeQueries:
		return "queries:" + n.projectDir
	}
	return ""
}

// openHeading opens a heading as it first shows, unless it was left
// closed in the run before: after that it is as the user leaves it.
func (a *App) openHeading(heading navNode) {
	if a.nav.listed[heading] {
		return
	}
	if a.nav.listed == nil {
		a.nav.listed = map[navNode]bool{}
	}
	a.nav.listed[heading] = true
	if !slices.Contains(strings.Split(a.layout.NavClosed, "\n"), headingKey(heading)) {
		a.nav.expand(heading)
	}
}

// keepClosedHeadings keeps in the layout which of the headings shown are
// closed, and leaves those of projects not open as they were. Not under a
// filter, which opens and closes them itself.
func (a *App) keepClosedHeadings() {
	if a.nav.needle != "" || len(a.nav.listed) == 0 {
		return
	}
	var closed []string
	for _, key := range strings.Split(a.layout.NavClosed, "\n") {
		if key != "" {
			closed = append(closed, key)
		}
	}
	for heading := range a.nav.listed {
		key := headingKey(heading)
		closed = slices.DeleteFunc(closed, func(k string) bool { return k == key })
		if !a.nav.isOpen(heading) {
			closed = append(closed, key)
		}
	}
	slices.Sort(closed)
	if kept := strings.Join(closed, "\n"); kept != a.layout.NavClosed {
		a.layout.NavClosed = kept
	}
}

// knownDatabasesFile keeps, by a connection's ID, the databases last read
// of its server.
const knownDatabasesFile = "known-databases.json"

func loadKnownDatabases(p *project.Project) map[string][]string {
	var kept map[string][]string
	if p == nil || p.Local == nil || p.Local.LoadJSON(knownDatabasesFile, &kept) != nil {
		return nil
	}
	return kept
}

// keepKnownDatabases keeps the databases of each server that is open,
// once they are read and whenever they change, for the new editor's picker
// to list while the server is not connected.
func (a *App) keepKnownDatabases() {
	for _, cn := range a.conns {
		p := cn.Project
		// One database is kept too: it says the server's were read.
		if cn.Status != connection.StatusConnected || len(cn.Databases) == 0 || p == nil || p.Local == nil {
			continue
		}
		read := strings.Join(cn.Databases, "\n")
		if a.nav.databasesKept[cn.Config.ID] == read {
			continue
		}
		if a.nav.databasesKept == nil {
			a.nav.databasesKept = map[string]string{}
		}
		a.nav.databasesKept[cn.Config.ID] = read
		kept := loadKnownDatabases(p)
		if kept == nil {
			kept = map[string][]string{}
		}
		if slices.Equal(kept[localConnID(cn)], cn.Databases) {
			continue
		}
		kept[localConnID(cn)] = slices.Clone(cn.Databases)
		if err := p.Local.SaveJSON(knownDatabasesFile, kept); err != nil {
			a.noteReport(widgets.ErrorReportOf("Could not keep the databases of "+cn.Config.Name, err)) // a hint for the picker: no dialog
		}
	}
}

// renameKnownDatabases moves the databases kept for the connection old of
// p to id, as renameConn does the connection's other state.
func renameKnownDatabases(p *project.Project, old, id string) error {
	kept := loadKnownDatabases(p)
	dbs, ok := kept[old]
	if !ok {
		return nil
	}
	delete(kept, old)
	kept[id] = dbs
	return p.Local.SaveJSON(knownDatabasesFile, kept)
}

// forgetKnownDatabases drops the databases kept of a connection deleted,
// as forgetConn does the connection's other state.
func forgetKnownDatabases(p *project.Project, id string) error {
	kept := loadKnownDatabases(p)
	if _, ok := kept[id]; !ok {
		return nil
	}
	delete(kept, id)
	return p.Local.SaveJSON(knownDatabasesFile, kept)
}
