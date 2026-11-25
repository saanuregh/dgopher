package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"dgopher/internal/connection"
	"dgopher/internal/project"
	"dgopher/internal/ui/dashboard"
	"dgopher/internal/ui/modelview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"
)

// What moving, renaming and deleting a project's files and folders does
// besides the disk: the tabs open on them follow, or close, and so do the
// bookmarks.

// movedTab is a tab open on a project's file, which follows it when it
// moves.
type movedTab interface {
	widgets.Tab
	Moved(path string)
}

// tabsUnder are the tabs open on a file at path, or, a folder, on the
// files in it.
func (a *App) tabsUnder(path string) []movedTab {
	var out []movedTab
	for _, t := range a.everyTab() {
		if mt, ok := t.(movedTab); ok {
			if at := tabPath(mt); at != "" && atOrUnder(at, path) {
				out = append(out, mt)
			}
		}
	}
	return out
}

// atOrUnder reports whether the absolute path at is path or, a folder,
// inside it.
func atOrUnder(at, path string) bool {
	return at == path || strings.HasPrefix(at, path+string(filepath.Separator))
}

// tabPath is the path of the file a tab is open on, "" for none.
func tabPath(t movedTab) string {
	switch t := t.(type) {
	case *query.Tab:
		return t.Path
	case *dashboard.Tab:
		return t.Path
	case *modelview.Tab:
		return t.Path
	}
	return ""
}

// moveOnDisk moves a file or a folder of a section, by its path relative
// to the section's folder, to another path in it: a rename, or a move to
// another folder.
func (a *App) moveOnDisk(p *project.Project, s fileSection, from, to string) error {
	if err := a.movePath(p, s.path(p, from), s.path(p, to)); err != nil {
		return err
	}
	a.filesChanged(p, s)
	return nil
}

// movePath moves a file or a folder of a project: the editors open on
// what moves save first, and every tab and bookmark of it follows it.
func (a *App) movePath(p *project.Project, src, dst string) error {
	if src == dst {
		return nil
	}
	tabs := a.tabsUnder(src)
	for _, t := range tabs {
		if q, ok := t.(*query.Tab); ok {
			q.Flush(false)
		}
	}
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	for _, t := range tabs {
		t.Moved(dst + strings.TrimPrefix(tabPath(t), src))
	}
	a.bookmarksFollow(p, src, dst)
	return nil
}

// taken reports whether a file or a folder is at path, other than the one
// at except, "" for none: a case-blind disk finds except itself at a path
// that differs from it by case alone, which a rename may take.
func taken(path, except string) bool {
	at, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if except == "" {
		return true
	}
	was, err := os.Lstat(except)
	return err != nil || !os.SameFile(at, was)
}

// filesChanged has a section's files listed again, as after the app
// changed them: the query files are scanned at once, and the dashboards
// and models read again when next asked for.
func (a *App) filesChanged(p *project.Project, s fileSection) {
	if s.heading == nodeQueries {
		a.ScanQueries(p, true)
		return
	}
	delete(a.fileLists, s.root(p))
}

// askDeleteFileFolder asks before deleting a folder of a section and all
// it holds, as DBeaver does a folder of scripts, saying how many files go
// and which tabs close with what they would lose.
func (a *App) askDeleteFileFolder(p *project.Project, s fileSection, folder string) {
	path := s.path(p, folder)
	listed, others := 0, 0
	for _, f := range a.sectionFiles(p, s).Files {
		if project.UnderFolder(f, folder) {
			listed++
		}
	}
	filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			others++
		}
		return nil
	})
	others -= listed
	reason := "The folder is deleted from the project's folder, with " + countOf(listed, s.noun, s.plural)
	if others > 0 {
		reason += " and " + countOf(others, "other file", "other files")
	}
	reason += " in it. Git still has them, if they were committed."
	tabs := a.tabsUnder(path)
	for _, t := range tabs {
		if r := t.CloseReason(); r != "" {
			reason += "\n\n" + t.Title() + " closes: " + r
		}
	}
	base := project.FolderBase(folder)
	a.closing = &closeRequest{open: true, title: "Delete folder " + base + "?", reason: reason, action: "Delete", onClose: func() {
		for _, t := range tabs {
			if _, _, _, ok := a.removeTab(t); ok {
				t.Close() // which may save it first: the folder goes after
			}
		}
		if err := os.RemoveAll(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			a.ShowReport(widgets.ErrorReportOf("Could not delete the folder "+base, err))
		} else if err := a.dropBookmarks(p, path); err != nil {
			a.ShowReport(widgets.ErrorReportOf("Could not remove the bookmarks of the folder "+base, err))
		}
		a.filesChanged(p, s)
	}}
	for _, t := range tabs {
		if h, ok := t.(txHolder); ok && h.OpenTx() {
			a.closing.txs = append(a.closing.txs, h)
		}
	}
}

// countOf says how many of a thing there are: "no queries", "1 query",
// "3 queries".
func countOf(n int, one, many string) string {
	switch n {
	case 0:
		return "no " + many
	case 1:
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// newFileIn is what makes a new file of a section in a folder of it, by
// its label: "" when the project cannot have one, as an editor without a
// SQL connection.
func (a *App) newFileIn(p *project.Project, s fileSection, folder string) (string, func()) {
	switch s.heading {
	case nodeDashboards:
		return "New Dashboard…", func() { a.newDashboard(p, folder) }
	case nodeModels:
		return "New Data Model…", func() { a.askNewModel(p, folder) }
	}
	cn := a.queryConn(p)
	if cn == nil {
		return "", nil
	}
	return "New SQL Editor", func() {
		if q := a.createQueryFile(cn, folder, "", connection.HeaderLine(cn)); q != nil {
			a.Connect(cn, nil)
		}
	}
}
