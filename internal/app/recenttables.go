package app

import (
	"slices"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/ui/widgets"
)

// recentTablesFile keeps, in a project's own folder, the tables opened
// last, newest first, for ⌘P to list first.
const recentTablesFile = "recent-tables.json"

const recentTablesKept = 50

type recentTable struct {
	Key string    // "connection/database/schema.name", the connection's ID local to its project
	At  time.Time // when it was opened last
}

func recentTableKey(cn *connection.Conn, database, schema, name string) string {
	return localConnID(cn) + "/" + database + "/" + schema + "." + name
}

func loadRecentTables(p *project.Project) []recentTable {
	var kept []recentTable
	if p == nil || p.Local == nil || p.Local.LoadJSON(recentTablesFile, &kept) != nil {
		return nil
	}
	return kept
}

// recordRecentTable moves the table to the front of its project's recent
// tables.
func (a *App) recordRecentTable(cn *connection.Conn, database string, obj db.Object) {
	p := cn.Project
	if p == nil || p.Local == nil {
		return
	}
	key := recentTableKey(cn, database, obj.Schema, obj.Name)
	kept := slices.DeleteFunc(loadRecentTables(p), func(r recentTable) bool { return r.Key == key })
	kept = slices.Insert(kept, 0, recentTable{Key: key, At: time.Now()})
	if len(kept) > recentTablesKept {
		kept = kept[:recentTablesKept]
	}
	if err := p.Local.SaveJSON(recentTablesFile, kept); err != nil {
		a.noteReport(widgets.ErrorReportOf("Could not keep the recent tables", err)) // a hint for ⌘P: no dialog over the table opened
	}
}

// recentTableRanks gives each recent table's key across the projects its
// place, 0 for the latest.
func (a *App) recentTableRanks() map[*project.Project]map[string]int {
	ranks := map[*project.Project]map[string]int{}
	for _, p := range a.projects {
		m := map[string]int{}
		for i, r := range loadRecentTables(p) {
			m[r.Key] = i
		}
		ranks[p] = m
	}
	return ranks
}

// renameRecentTables moves the recent tables of the connection old of p
// to id, as renameConn does the connection's other state.
func renameRecentTables(p *project.Project, old, id string) error {
	kept := loadRecentTables(p)
	changed := false
	for i, r := range kept {
		if rest, ok := strings.CutPrefix(r.Key, old+"/"); ok {
			kept[i].Key = id + "/" + rest
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return p.Local.SaveJSON(recentTablesFile, kept)
}

// forgetRecentTables drops the recent tables of a connection deleted, as
// forgetConn does the connection's other state.
func forgetRecentTables(p *project.Project, id string) error {
	all := loadRecentTables(p)
	kept := slices.DeleteFunc(slices.Clone(all), func(r recentTable) bool { return strings.HasPrefix(r.Key, id+"/") })
	if len(kept) == len(all) {
		return nil
	}
	return p.Local.SaveJSON(recentTablesFile, kept)
}
