package app

import (
	"os"
	"path/filepath"
	"slices"

	"dgopher/internal/connection"
	"dgopher/internal/project"
	"dgopher/internal/ui/dashboard"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/modelview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/redis"
	"dgopher/internal/ui/widgets"
)

// maxClosedTabs is how many closed tabs Reopen Closed Tab keeps.
const maxClosedTabs = 20

// closedTab is a tab the user closed, and how to open it again.
type closedTab struct {
	title string
	at    int // its place in its window
	// open opens it again, and says why it cannot, "" when it did.
	open func() string
}

// rememberClosed keeps a tab the user closed, to open it again; one that
// cannot be, as a table's design, is not kept.
func (a *App) rememberClosed(t widgets.Tab, at int) {
	open := a.reopener(t)
	if open == nil {
		return
	}
	a.closedTabs = append(a.closedTabs, closedTab{title: t.Title(), at: at, open: open})
	if n := len(a.closedTabs); n > maxClosedTabs {
		a.closedTabs = slices.Delete(a.closedTabs, 0, n-maxClosedTabs)
	}
}

// reopenTab opens again the tab closed last, at its place when it opens
// at once; one whose connection or file is gone since says so.
func (a *App) reopenTab() {
	n := len(a.closedTabs)
	if n == 0 {
		return
	}
	c := a.closedTabs[n-1]
	a.closedTabs = a.closedTabs[:n-1]
	w, before := a.window, a.tabs.Tabs()
	if why := c.open(); why != "" {
		a.ShowError("Could not reopen "+c.title, why)
		return
	}
	// One open in another window came forward there, and stays as it is.
	for _, t := range w.tabs.Tabs() {
		if !slices.Contains(before, t) {
			w.tabs.Move(t, c.at)
		}
	}
}

// reopener is how a tab opens again, by what it showed rather than the
// tab itself, closed: nil for a tab that does not.
func (a *App) reopener(t widgets.Tab) func() string {
	gone := func(id string) string { return "Its connection " + id + " is gone." }
	switch t := t.(type) {
	case *query.Tab:
		if t.Path == "" {
			return nil
		}
		path, id, database := t.Path, t.Conn.Config.ID, t.Database
		return func() string {
			if a.ActivateTab(func(o widgets.Tab) bool { q, ok := o.(*query.Tab); return ok && q.Path == path }) {
				return ""
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return filepath.Base(path) + " is gone."
			}
			cn := a.connByID(id)
			if cn == nil {
				return gone(id)
			}
			a.openFileTab(cn, database, path, string(data)) // unconnected, as a restored editor
			return ""
		}
	case *waitingTab:
		s, _ := savedTabOf(t)
		return a.withConn(s.Connection, gone, func(cn *connection.Conn) { a.tabs.Add(&waitingTab{a: a, conn: cn, saved: s}) })
	case *dataview.TableTab:
		id, database, obj, page := t.Conn.Config.ID, t.Database, t.Object, t.Page
		return a.withConn(id, gone, func(cn *connection.Conn) { a.OpenTable(cn, database, obj, page) })
	case *dataview.ERTab:
		database, schema := t.Where()
		return a.withConn(t.Connection().Config.ID, gone, func(cn *connection.Conn) { dataview.OpenER(a, cn, database, schema) })
	case *redis.Tab:
		return a.withConn(t.Connection().Config.ID, gone, a.openRedis)
	case *activityTab:
		return a.withConn(t.conn.Config.ID, gone, a.openActivity)
	case *usersTab:
		return a.withConn(t.conn.Config.ID, gone, a.openUsers)
	case *searchTab:
		database := t.database
		return a.withConn(t.conn.Config.ID, gone, func(cn *connection.Conn) { a.openSearch(cn, database) })
	case *dashboard.Tab:
		p, path := t.Project, t.Path
		return a.whileProject(p, func() { a.openDashboard(p, path) })
	case *modelview.Tab:
		p, path := t.Project, t.Path
		return a.whileProject(p, func() { a.openModel(p, path) })
	}
	return nil
}

// withConn is a reopener that finds its connection by its ID first, as it
// may have gone since.
func (a *App) withConn(id string, gone func(string) string, open func(*connection.Conn)) func() string {
	return func() string {
		cn := a.connByID(id)
		if cn == nil {
			return gone(id)
		}
		open(cn)
		return ""
	}
}

// whileProject is a reopener of a project's file, while the project is in
// the sidebar.
func (a *App) whileProject(p *project.Project, open func()) func() string {
	return func() string {
		if !slices.Contains(a.projects, p) {
			return p.Name + " was removed from the sidebar."
		}
		open()
		return ""
	}
}
