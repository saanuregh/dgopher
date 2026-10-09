package app

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"dgopher/internal/datamodel"
	"dgopher/internal/project"
	"dgopher/internal/ui/dashboard"
	"dgopher/internal/ui/modelview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"
)

// fileActions rename and delete the file of a project a node names: a
// query file, a dashboard or a data model; nil for other nodes.
func (a *App) fileActions(p *project.Project, n navNode) (rename, del func()) {
	switch n.kind {
	case nodeQueryFile:
		path := filepath.Join(p.Queries, filepath.FromSlash(n.name))
		return func() { a.askRename(p, path) },
			func() { a.askDeleteFile(path, "query file", n.name, func() { a.ScanQueries(p, true) }) }
	case nodeDashboard:
		return func() { a.askRenameDashboard(p, n.name) },
			func() {
				names, paths := a.dashboardNames(p)
				a.askDeleteFile(n.name, "dashboard", nameOf(names, paths, n.name), nil)
			}
	case nodeModel:
		return func() { a.askRenameModel(p, n.name) },
			func() {
				names, paths := a.modelNames(p)
				a.askDeleteFile(n.name, "data model", nameOf(names, paths, n.name), nil)
			}
	}
	return nil, nil
}

// nameOf is the name a dashboard or a data model holds, by its path
// among those listed, else its file's.
func nameOf(names, paths []string, path string) string {
	for i, p := range paths {
		if p == path {
			return names[i]
		}
	}
	return filepath.Base(path)
}

// tabOf is the open tab of a project's file, nil for none.
func (a *App) tabOf(path string) widgets.Tab {
	for _, t := range a.everyTab() {
		switch t := t.(type) {
		case *query.Tab:
			if t.Path == path {
				return t
			}
		case *dashboard.Tab:
			if t.Path == path {
				return t
			}
		case *modelview.Tab:
			if t.Path == path {
				return t
			}
		}
	}
	return nil
}

// askRenameDashboard renames a dashboard: the name it holds, and its file,
// named after it; an open tab's with what it has not saved.
func (a *App) askRenameDashboard(p *project.Project, path string) {
	d, disk, err := dashboard.LoadFile(path)
	if err != nil {
		a.ShowError("Could not rename the dashboard", err.Error())
		return
	}
	a.renaming = &renameForm{open: true, title: "Rename " + d.Name, name: d.Name, rename: func(name string) error {
		name = strings.TrimSpace(name)
		if name == "" {
			return errors.New("Name the dashboard.")
		}
		to := dashboard.File(p, name)
		if t, ok := a.tabOf(path).(*dashboard.Tab); ok {
			return t.Rename(name, to)
		}
		d.Name = name
		_, err := d.Move(path, to, disk)
		return err
	}}
}

// askRenameModel renames a data model, as askRenameDashboard a dashboard.
func (a *App) askRenameModel(p *project.Project, path string) {
	m, was, err := datamodel.LoadFile(path)
	if err != nil {
		a.ShowError("Could not rename the data model", err.Error())
		return
	}
	a.renaming = &renameForm{open: true, title: "Rename " + m.Name, name: m.Name, rename: func(name string) error {
		name = strings.TrimSpace(name)
		if name == "" {
			return errors.New("Name the data model.")
		}
		to := datamodel.File(p, name)
		if t, ok := a.tabOf(path).(*modelview.Tab); ok {
			return t.Rename(name, to)
		}
		m.Name = name
		_, err := m.Move(path, to, was)
		return err
	}}
}

// askDeleteFile deletes a project's file, once the user agrees, closing
// its tab: what closing it would lose is said too.
func (a *App) askDeleteFile(path, what, name string, after func()) {
	tab := a.tabOf(path)
	reason := "The " + what + " is deleted from the project's folder. Git still has it, if it was committed."
	if tab != nil {
		if r := tab.CloseReason(); r != "" {
			reason += "\n\nIts tab closes: " + r
		}
	}
	a.closing = &closeRequest{open: true, title: "Delete " + name + "?", reason: reason, action: "Delete", onClose: func() {
		if tab != nil {
			if _, _, ok := a.removeTab(tab); ok {
				tab.Close() // which may save it first: it goes after
			}
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			a.ShowError("Could not delete "+name, err.Error())
			return
		}
		if after != nil {
			after()
		}
	}}
	if h, ok := tab.(txHolder); ok && h.OpenTx() {
		a.closing.txs = []txHolder{h}
	}
}
