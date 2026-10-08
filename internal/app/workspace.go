package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/project"
	"dgopher/internal/store"
	"dgopher/internal/ui/query"

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
}

type savedWorkspace struct {
	Editors []savedEditor `json:"editors"`
	// Active is the editor in front, -1 when the tab in front was not one
	// of the project's.
	Active int `json:"active"`
}

const workspaceFile = "workspace.json"

// workspaceOf returns the editors of a project to keep.
func (a *App) workspaceOf(p *project.Project) savedWorkspace {
	w := savedWorkspace{Editors: []savedEditor{}, Active: -1}
	for i, t := range a.tabs {
		q, ok := t.(*query.Tab)
		if !ok || q.Conn.Project != p || q.Path == "" {
			continue
		}
		if i == a.active {
			w.Active = len(w.Editors)
		}
		e := savedEditor{Connection: strings.TrimPrefix(q.Conn.Config.ID, p.Prefix), Database: q.Database, Path: p.StoredPath(q.Path)}
		if q.DiskConflict != "" && q.Editor.Text != q.Saved {
			e.Unsaved = q.Editor.Text
		}
		w.Editors = append(w.Editors, e)
	}
	return w
}

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

// restoreWorkspace opens the editors of a project's last run; they
// connect when shown.
func (a *App) restoreWorkspace(p *project.Project) {
	var w savedWorkspace
	if err := p.Local.LoadJSON(workspaceFile, &w); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Println("workspace:", err)
		}
		return
	}
	p.Workspace, _ = json.Marshal(w)
	for i, e := range w.Editors {
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
		q := query.New(a, cn, e.Database, filepath.Base(path), string(data))
		q.Path, q.Saved = path, string(data)
		if e.Unsaved != "" && e.Unsaved != string(data) {
			q.Editor.Text, q.Typed = e.Unsaved, e.Unsaved
			q.DiskConflict = "changed on disk"
			if err != nil {
				q.DiskConflict = "was deleted on disk"
			}
		}
		a.tabs = append(a.tabs, q)
		if i == w.Active {
			a.active = len(a.tabs) - 1
		}
	}
}

// openSQLFile opens a file of SQL in an editor of a connection.
func (a *App) openSQLFile(cn *connection.Conn) {
	go func() {
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
				q.Saved = q.Editor.Text
				a.AddTab(q)
			})
		})
	}()
}

// SaveSQLFile writes an editor's SQL to its file, asking for one first
// when it has none or saveAs is set.
func (a *App) SaveSQLFile(q *query.Tab, saveAs bool) {
	text := q.Editor.Text
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
