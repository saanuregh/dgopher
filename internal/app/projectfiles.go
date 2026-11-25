package app

import (
	"errors"
	"io/fs"
	"os"
	"slices"
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
	s, ok := sectionOf(n)
	if !ok || n.kind != s.file {
		return nil, nil
	}
	path, name := a.filePath(p, n), a.fileName(p, s, n.name)
	deleted := func() {
		a.filesChanged(p, s)
		if err := a.dropBookmarks(p, path); err != nil {
			a.ShowReport(widgets.ErrorReportOf("Could not remove the bookmark of "+name, err))
		}
	}
	del = func() { a.askDeleteFile(path, s.noun, name, deleted) }
	switch n.kind {
	case nodeDashboard:
		return func() { a.askRenameDashboard(p, path) }, del
	case nodeModel:
		return func() { a.askRenameModel(p, path) }, del
	}
	return func() { a.askRename(p, path) }, del
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

// askRenameDashboard renames a dashboard, as askRenameNamed: an open tab
// with what it has not saved.
func (a *App) askRenameDashboard(p *project.Project, path string) {
	d, disk, err := dashboard.LoadFile(path)
	if err != nil {
		a.ShowReport(widgets.ErrorReportOf("Could not rename the dashboard", err))
		return
	}
	a.askRenameNamed(p, dashboardsSection, path, d.Name, func(name string) (string, error) {
		to := dashboard.File(p, dashboardsSection.parent(p, path), name)
		if t, ok := a.tabOf(path).(*dashboard.Tab); ok {
			return to, t.Rename(name, to)
		}
		d.Name = name
		_, err := d.Move(path, to, disk)
		return to, err
	})
}

// askRenameModel renames a data model, as askRenameDashboard a dashboard.
func (a *App) askRenameModel(p *project.Project, path string) {
	m, was, err := datamodel.LoadFile(path)
	if err != nil {
		a.ShowReport(widgets.ErrorReportOf("Could not rename the data model", err))
		return
	}
	a.askRenameNamed(p, modelsSection, path, m.Name, func(name string) (string, error) {
		to := datamodel.File(p, modelsSection.parent(p, path), name)
		if t, ok := a.tabOf(path).(*modelview.Tab); ok {
			return to, t.Rename(name, to)
		}
		m.Name = name
		_, err := m.Move(path, to, was)
		return to, err
	})
}

// askRenameNamed asks for a new name of a dashboard or a data model,
// which names its file too, in the folder it is in: rename writes it
// there and says where. A name stays unique across the section's folders,
// as Add to Dashboard picks one by its name, and the bookmarks of the
// file follow it.
func (a *App) askRenameNamed(p *project.Project, s fileSection, path, was string, rename func(name string) (string, error)) {
	a.renaming = &renameForm{open: true, title: "Rename " + was, name: was, rename: func(name string) error {
		name = strings.TrimSpace(name)
		if name == "" {
			return errors.New("Name the " + s.noun + ".")
		}
		names, paths := a.fileNamesAndPaths(p, s)
		if i := slices.Index(names, name); i >= 0 && paths[i] != path {
			return errors.New("A " + s.noun + " of that name exists.")
		}
		to, err := rename(name)
		if err != nil {
			return err
		}
		a.filesChanged(p, s)
		a.bookmarksFollow(p, path, to)
		return nil
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
			if _, _, _, ok := a.removeTab(tab); ok {
				tab.Close() // which may save it first: it goes after
			}
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			a.ShowReport(widgets.ErrorReportOf("Could not delete "+name, err))
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
