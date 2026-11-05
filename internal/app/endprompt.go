package app

import (
	"slices"
	"strings"

	"dgopher/internal/connection"
	"dgopher/internal/project"
	"dgopher/internal/ui/dashboard"
	"dgopher/internal/ui/modelview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"
)

// endKind is what an action asking about its losses does to tabs and
// connections: it decides which losses count and how the prompt says so.
type endKind int

const (
	// endClose closes the tabs.
	endClose endKind = iota
	// endDisconnect closes the connections; their tabs stay and wait.
	endDisconnect
	// endQuit closes every tab and connection.
	endQuit
	// endRemove takes a project off the sidebar, closing its tabs.
	endRemove
	// endReload reads a project again, closing its tabs, which reopen.
	endReload
	// endDelete deletes a connection, closing its tabs.
	endDelete
)

// endPrompt builds the question an action of kind asks before it ends
// tabs and stops the work running on conns: what each tab loses (what
// disconnecting loses for an editor that stays, else what closing loses)
// and the work, worded for the action. It is nil when nothing is lost,
// unless removing or deleting, which always ask. action names the
// agreeing button, "Close" when empty.
func (a *App) endPrompt(kind endKind, title, action string, tabs []widgets.Tab, conns []*connection.Conn) *closeRequest {
	var lost []string
	var txs []txHolder
	for _, t := range tabs {
		reason := t.CloseReason()
		if q, ok := t.(*query.Tab); ok && kind == endDisconnect {
			reason = q.DisconnectReason()
		}
		if reason != "" {
			lost = append(lost, t.Title()+": "+reason)
		}
		if h, ok := t.(txHolder); ok && h.OpenTx() {
			txs = append(txs, h)
		}
	}
	for _, j := range a.jobsOf(conns...) {
		// A job of two connections, as a copy, is listed once.
		if !slices.Contains(lost, j.title) {
			lost = append(lost, j.title)
		}
	}
	always := kind == endRemove || kind == endDelete
	if len(lost) == 0 && !always {
		return nil
	}
	list := strings.Join(lost, "\n\n")
	reason := list
	switch kind {
	case endClose:
		if len(tabs) == 1 {
			reason = tabs[0].CloseReason()
		}
	case endQuit:
		reason = "Quitting closes every tab and stops the work running, losing:\n\n" + list
	case endReload:
		reason = "Reloading closes its tabs, which open again as the folder has them, and stops the work running on its connections, losing:\n\n" + list
	case endRemove:
		reason = "Its tabs close and its folder is not touched."
		if len(lost) > 0 {
			reason = "Its tabs close and the work running on its connections stops, losing:\n\n" + list + "\n\nIts folder is not touched."
		}
	case endDelete:
		reason = "The connection's settings and saved passwords are removed. Its database is not touched."
		if len(lost) > 0 {
			reason += "\n\nIts tabs close and the work running on it stops, losing:\n\n" + list
		}
	}
	return &closeRequest{open: true, title: title, reason: reason, action: action, txs: txs}
}

// requestEnd runs then once the user agrees to what the action of kind
// loses (endPrompt), at once when it asks nothing.
func (a *App) requestEnd(kind endKind, title, action string, tabs []widgets.Tab, conns []*connection.Conn, then func()) {
	r := a.endPrompt(kind, title, action, tabs, conns)
	if r == nil {
		then()
		return
	}
	r.onClose = then
	a.closing = r
}

// requestDisconnect runs then, which disconnects, once the user agrees to
// lose what the tabs of the connections hold and to stop the work running
// on them; at once when there is none.
func (a *App) requestDisconnect(title, action string, conns []*connection.Conn, then func()) {
	a.requestEnd(endDisconnect, title, action, a.tabsOf(conns...), conns, then)
}

// askDisconnect disconnects cn once the user agrees to what it loses.
func (a *App) askDisconnect(cn *connection.Conn) {
	a.requestDisconnect("Disconnect "+cn.Config.Name+"?", "Disconnect", []*connection.Conn{cn}, func() { a.disconnect(cn) })
}

// requestQuit reports whether the app may quit now. When a tab would
// lose work, with a connection or without, as an open transaction or a
// model's unsaved table, or work runs on a connection, as Run SQL File,
// it asks first, and quit runs once the user agrees. The quit question
// takes the place of another one open, which counts as cancelled.
func (a *App) requestQuit(quit func()) bool {
	if a.quitting {
		return true
	}
	if r := a.closing; r != nil && r.quit {
		return false
	}
	var tabs []widgets.Tab
	for _, t := range a.everyTab() {
		tabs = append(tabs, t)
	}
	r := a.endPrompt(endQuit, "Quit DGopher?", "Quit", tabs, a.conns)
	if r == nil {
		return true
	}
	r.quit = true
	r.onClose = func() {
		a.quitting = true
		quit()
	}
	a.closing = r
	return false
}

// tabsOf lists the tabs of every window on the connections given.
func (a *App) tabsOf(conns ...*connection.Conn) []widgets.Tab {
	var out []widgets.Tab
	for _, t := range a.everyTab() {
		if slices.Contains(conns, t.Connection()) {
			out = append(out, t)
		}
	}
	return out
}

// tabProject is the project a tab belongs to: its connection's, or the
// project of a dashboard or data model; nil for none.
func tabProject(t widgets.Tab) *project.Project {
	switch t := t.(type) {
	case *dashboard.Tab:
		return t.Project
	case *modelview.Tab:
		return t.Project
	}
	if cn := t.Connection(); cn != nil {
		return cn.Project
	}
	return nil
}

// projectTabs lists the tabs of every window that belong to p.
func (a *App) projectTabs(p *project.Project) []widgets.Tab {
	var out []widgets.Tab
	for _, t := range a.everyTab() {
		if tabProject(t) == p {
			out = append(out, t)
		}
	}
	return out
}

// sayPinned says why a pinned tab stays when its close key or the middle
// button would close it.
func (a *App) sayPinned(t widgets.Tab) {
	a.Toast(t.Title()+" is pinned: close it from its menu, or unpin it first", "", nil)
}
