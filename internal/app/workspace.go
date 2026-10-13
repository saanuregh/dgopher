package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/project"
	"dgopher/internal/store"
	"dgopher/internal/ui/dashboard"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/modelview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
)

// savedEditor is an editor kept open from one run to the next. Its text
// is in its file, saved as it is typed.
type savedEditor struct {
	// Connection is the connection's ID in dgopher.json.
	Connection string `json:"connection"`
	Database   string `json:"database,omitempty"`
	// Path is relative to the project when the file is inside it.
	Path string `json:"path"`
	// Unsaved is the text of an editor whose file changed on disk, kept
	// until the user chooses between the two.
	Unsaved string `json:"unsaved,omitempty"`
	Pinned  bool   `json:"pinned,omitempty"`
}

type savedWorkspace struct {
	Editors []savedEditor `json:"editors"`
	// Active is the editor in front, -1 when the tab in front was not one
	// of the project's.
	Active int `json:"active"`
	// Dashboards are the files of the dashboards open, relative to the
	// project.
	Dashboards []string `json:"dashboards,omitempty"`
	// Models are the files of the data models open, as Dashboards.
	Models []string `json:"models,omitempty"`
	// Tabs are the connections' tabs open, which wait to connect.
	Tabs []savedTab `json:"tabs,omitempty"`
	// Order is the order of every tab kept, each as its list and its
	// place there, as "editors/0"; a file without it, of an older
	// DGopher, reopens its editors first.
	Order []string `json:"order,omitempty"`
	// Front is the tab in front, as in Order; "" when it was none of the
	// project's. Active stays, for an older DGopher.
	Front string `json:"front,omitempty"`
}

const workspaceFile = "workspace.json"

// workspaceOf returns what a project keeps open: its tabs, in their
// order, and the one in front.
func (a *App) workspaceOf(p *project.Project) savedWorkspace {
	w := savedWorkspace{Editors: []savedEditor{}, Active: -1}
	w.Dashboards = a.dashboardPaths(p)
	w.Models = a.modelPaths(p)
	// The tabs of every window, which come back in the main one.
	dashboards, models := 0, 0
	for win, t := range a.everyTab() {
		ref := ""
		switch t := t.(type) {
		case *query.Tab:
			if t.Conn.Project != p || t.Path == "" {
				continue
			}
			if win == a.main && t == a.main.ActiveTab() {
				w.Active = len(w.Editors)
			}
			e := savedEditor{Connection: strings.TrimPrefix(t.Conn.Config.ID, p.Prefix), Database: t.Database, Path: p.StoredPath(t.Path), Pinned: win.tabs.Pinned(t)}
			if text := t.Editor.FileText(); t.DiskConflict != "" && text != t.Saved {
				e.Unsaved = text
			}
			ref = tabRef("editors", len(w.Editors))
			w.Editors = append(w.Editors, e)
		case *dashboard.Tab:
			if t.Project != p {
				continue
			}
			ref = tabRef("dashboards", dashboards)
			dashboards++
		case *modelview.Tab:
			if t.Project != p {
				continue
			}
			ref = tabRef("models", models)
			models++
		default:
			s, ok := savedTabOf(t)
			if !ok || t.Connection().Project != p {
				continue
			}
			s.Connection = strings.TrimPrefix(s.Connection, p.Prefix)
			s.Pinned = win.tabs.Pinned(t)
			ref = tabRef("tabs", len(w.Tabs))
			w.Tabs = append(w.Tabs, s)
		}
		w.Order = append(w.Order, ref)
		if win == a.main && t == a.main.ActiveTab() {
			w.Front = ref
		}
	}
	return w
}

// tabRef names a tab kept in a workspace: its list and its place there.
func tabRef(list string, i int) string { return list + "/" + strconv.Itoa(i) }

// saveWorkspace keeps the open editors of each project, when they changed.
func (a *App) saveWorkspace(force bool) {
	if !force && a.now.Sub(a.workspaceSaved) < 10*time.Second {
		return
	}
	a.workspaceSaved = a.now
	for _, p := range a.projects {
		if p.Err == "" {
			a.saveWorkspaceOf(p)
		}
	}
}

func (a *App) saveWorkspaceOf(p *project.Project) {
	w := a.workspaceOf(p)
	data, _ := json.Marshal(w)
	if bytes.Equal(data, p.Workspace) {
		return
	}
	if err := p.Local.SaveJSON(workspaceFile, w); err != nil {
		log.Println("workspace:", err)
		return
	}
	p.Workspace = data
}

// restoreWorkspace reopens a project's tabs of its last run, in their
// order; they connect when shown.
func (a *App) restoreWorkspace(p *project.Project) {
	var w savedWorkspace
	if err := p.Local.LoadJSON(workspaceFile, &w); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Println("workspace:", err)
		}
		return
	}
	p.Workspace, _ = json.Marshal(w)
	type restored struct {
		ref    string
		tab    widgets.Tab
		pinned bool
	}
	var tabs []restored
	keep := func(list string, i int, t widgets.Tab, pinned bool) {
		tabs = append(tabs, restored{tabRef(list, i), t, pinned})
	}
	// The workspace may come from a clone: only the project's own files
	// come back, never one outside it or under its .git or .dgopher.
	for i, e := range w.Editors {
		if !project.Contains(p.Dir, e.Path) {
			continue
		}
		path := p.ResolvePath(e.Path)
		data, err := os.ReadFile(path)
		if err != nil && e.Unsaved == "" {
			continue // deleted, or moved, since: nothing was only in the editor
		}
		// The file says where it runs, as after a pull that changed its
		// header; the editor's last connection serves a file naming none,
		// or one the project lacks, which then runs nothing.
		cn := a.connByID(p.Prefix + project.HeaderConnection(string(data)))
		if cn == nil || !cn.Config.Engine.IsSQL() {
			cn = a.connByID(p.Prefix + e.Connection)
		}
		if cn == nil || !cn.Config.Engine.IsSQL() {
			continue
		}
		text := string(data)
		unsaved := e.Unsaved != "" && e.Unsaved != text
		if unsaved {
			text = e.Unsaved
		}
		q := query.New(a, cn, e.Database, filepath.Base(path), text)
		q.Path, q.Saved = path, string(data)
		if unsaved {
			q.Typed = e.Unsaved
			q.DiskConflict = "changed on disk"
			if err != nil {
				q.DiskConflict = "was deleted on disk"
			}
		}
		keep("editors", i, q, e.Pinned)
	}
	for i, path := range w.Dashboards {
		// A dashboard deleted, or broken, since is left closed.
		if !project.Contains(p.Dir, path) {
			continue
		}
		if d, err := dashboard.Open(a, p, p.ResolvePath(path)); err == nil {
			keep("dashboards", i, d, false)
		}
	}
	for i, path := range w.Models {
		if !project.Contains(p.Dir, path) {
			continue
		}
		if m, err := modelview.Open(a, p, p.ResolvePath(path)); err == nil {
			keep("models", i, m, false)
		}
	}
	// A connection's tab waits for it to connect; one whose connection
	// is gone, or no longer has such a tab, is left closed.
	for i, s := range w.Tabs {
		s.Connection = p.Prefix + s.Connection
		if cn := a.connByID(s.Connection); cn != nil && s.availableOn(cn) {
			keep("tabs", i, &waitingTab{a: a, conn: cn, saved: s}, s.Pinned)
		}
	}
	front := w.Front
	if w.Order == nil && w.Active >= 0 {
		front = tabRef("editors", w.Active)
	}
	// In their order; those it does not name, after.
	at := make(map[string]int, len(w.Order))
	for i, ref := range w.Order {
		at[ref] = i
	}
	place := func(ref string) int {
		if i, ok := at[ref]; ok {
			return i
		}
		return len(w.Order)
	}
	slices.SortStableFunc(tabs, func(x, y restored) int { return place(x.ref) - place(y.ref) })
	for _, r := range tabs {
		a.main.tabs.Append(r.tab)
		if r.pinned {
			a.main.tabs.SetPinned(r.tab, true)
		}
		if r.ref == front {
			a.main.tabs.Select(r.tab)
		}
	}
}

// openSQLFile opens a file of SQL in an editor of a connection.
func (a *App) openSQLFile(cn *connection.Conn) {
	go func() {
		defer dataview.RecoverBackground(a.Post, a.ShowError, nil)
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "Open SQL Script",
			Filters: []mygo.FileFilter{{Name: "SQL scripts", Extensions: []string{"sql"}}, {Name: "All files", Extensions: []string{"*"}}}})
		if err != nil || len(paths) == 0 {
			return
		}
		data, err := os.ReadFile(paths[0])
		a.Post(func() {
			if err != nil {
				a.ShowError("Could not open the script", err.Error())
				return
			}
			a.Connect(cn, func() {
				q := query.New(a, cn, "", filepath.Base(paths[0]), string(data))
				q.Path = paths[0]
				q.Saved = string(data)
				a.AddTab(q)
			})
		})
	}()
}

// SaveSQLFile writes an editor's SQL to its file, asking for one first
// when it has none or saveAs is set.
func (a *App) SaveSQLFile(q *query.Tab, saveAs bool) {
	text := q.Editor.FileText()
	write := func(path string) {
		err := os.WriteFile(path, []byte(text), 0o600)
		a.Post(func() {
			if err != nil {
				a.ShowError("Could not save the script", err.Error())
				return
			}
			q.Path, q.Saved, q.Name = path, text, filepath.Base(path)
			q.Created, q.DiskConflict = "", ""
			a.toast = &pendingToast{text: "Saved " + filepath.Base(path)}
		})
	}
	if q.Path != "" && !saveAs {
		q.Flush(false)
		if q.DiskConflict == "" {
			a.toast = &pendingToast{text: "Saved " + filepath.Base(q.Path)}
		}
		return
	}
	go func() {
		defer dataview.RecoverBackground(a.Post, a.ShowError, nil)
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: "Save SQL Script", DefaultPath: "query.sql",
			Filters: []mygo.FileFilter{{Name: "SQL scripts", Extensions: []string{"sql"}}}})
		if err != nil || path == "" {
			return
		}
		if filepath.Ext(path) == "" {
			path += ".sql"
		}
		write(path)
	}()
}
