package app

import (
	"dgopher/internal/connection"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// navError is an error a note of the tree stands for.
type navError struct {
	title, full string
	// refresh reads again what could not be read, nil when nothing does.
	refresh func()
}

// errorNote is a note saying an error, whose text is n's name; it keeps
// the error for the note to show whole.
func (a *App) errorNote(n navNode, e navError) []navNode {
	n.kind = nodeInfo
	if a.nav.errors == nil {
		a.nav.errors = map[navNode]navError{}
	}
	a.nav.errors[n] = e
	return []navNode{n}
}

// catalogError is a note, under a node, of a part of cn's catalog that
// could not be read, which Refresh reads again.
func (a *App) catalogError(cn *connection.Conn, under navNode, what, text, full string) []navNode {
	// A notification keeps Refresh: it holds the ID, not the connection.
	id := cn.Config.ID
	refresh := func() {
		if cn := a.connByID(id); cn != nil {
			a.refresh(cn)
		}
	}
	return a.errorNote(navNode{conn: id, name: text, under: noteUnder(under)}, navError{title: "Could not read " + what, full: full, refresh: refresh})
}

// navReport is the report of the error a note stands for, with what can
// be done.
func navReport(e navError) widgets.ErrorReport {
	r := widgets.ErrorReport{Title: e.title, Detail: e.full}
	if e.refresh != nil {
		r.Actions = []widgets.ErrorAction{{Label: "Refresh", Run: e.refresh}}
	}
	return r
}

// showNavError raises the error a note stands for.
func (a *App) showNavError(e navError) { a.ShowReport(navReport(e)) }

// errorNoteText draws a note that says an error: a line of it, the whole
// on hover; a click raises it. The first time it shows, a catalog error
// (one Refresh reads again) is raised, the user's opening of its place
// having asked for it; the others, read at startup, are only noted.
func (a *App) errorNoteText(c *ui.Context, row ui.Element, n navNode, e navError) {
	ui.Text(c, n.name).FontSize(widgets.TextSmall).TextColor(c.Theme().Danger).Italic().SingleLine().Tooltip(e.full)
	if a.nav.raised[n] != e.full {
		if a.nav.raised == nil {
			a.nav.raised = map[navNode]string{}
		}
		a.nav.raised[n] = e.full
		if e.refresh != nil {
			a.showNavError(e)
		} else {
			a.noteReport(navReport(e))
		}
		c.Invalidate()
	}
	if row.Clicked() {
		a.showNavError(e)
	}
}

// forgetGoneErrors forgets the raised notes whose error the tree no longer
// lists, so that failing again raises again.
func (a *App) forgetGoneErrors() {
	for n, full := range a.nav.raised {
		if a.nav.errors[n].full != full {
			delete(a.nav.raised, n)
		}
	}
}

// failedConn is the connection n is the row of when it failed to connect.
func (a *App) failedConn(n navNode) *connection.Conn {
	if n.kind != nodeConn {
		return nil
	}
	if cn := a.connByID(n.conn); cn != nil && cn.Status == connection.StatusFailed {
		return cn
	}
	return nil
}

// showConnError raises why cn failed to connect, unless that is in front
// already, as after the click a double click starts with.
func (a *App) showConnError(cn *connection.Conn) {
	r := a.connectionReport(cn, nil)
	if f := a.frontAlert(); f != nil && f.report.Title == r.Title && f.report.Detail == r.Detail {
		return
	}
	a.ShowReport(r)
}
