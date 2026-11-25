package app

import (
	"cmp"
	"errors"
	"os"
	"slices"
	"strings"
	"time"

	"dgopher/internal/keymap"
	"dgopher/internal/project"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// The folders of a section of a project's files, as DBeaver's Scripts
// has them: folders on disk, made, renamed, moved and deleted from the
// navigator. Their paths follow the rules of the connections' folders
// (project.CleanFolderName, FolderParent, FolderBase, UnderFolder).

// fileFolderChildren lists a folder of a section, "" for its top: its
// folders by name, then its files, by name or, as the section is sorted,
// the last changed first.
func (a *App) fileFolderChildren(p *project.Project, s fileSection, folder string) []navNode {
	l := a.sectionFiles(p, s)
	byName := func(x, y string) int { return cmp.Compare(strings.ToLower(x), strings.ToLower(y)) }
	inFolder := func(paths []string) []string {
		var out []string
		for _, f := range paths {
			if project.FolderParent(f) == folder {
				out = append(out, f)
			}
		}
		return out
	}
	folders, files := inFolder(l.Folders), inFolder(l.Files)
	slices.SortStableFunc(folders, func(x, y string) int { return byName(project.FolderBase(x), project.FolderBase(y)) })
	// Each file's key is read once: reading it checks its section's
	// folders on disk.
	if a.sortedByModified(s) {
		modified := map[string]time.Time{}
		for _, f := range files {
			modified[f] = a.fileModified(p, s, f)
		}
		slices.SortStableFunc(files, func(x, y string) int { return modified[y].Compare(modified[x]) })
	} else {
		names := map[string]string{}
		for _, f := range files {
			names[f] = a.fileName(p, s, f)
		}
		slices.SortStableFunc(files, func(x, y string) int { return byName(names[x], names[y]) })
	}
	var out []navNode
	for _, f := range folders {
		out = append(out, s.folderNode(p, f))
	}
	for _, f := range files {
		out = append(out, s.fileNode(p, f))
	}
	if out != nil || folder == "" {
		return out
	}
	return []navNode{{kind: nodeInfo, projectDir: p.Dir, name: "Empty folder", under: noteUnder(s.folderNode(p, folder))}}
}

// fileFolderRow draws a folder's row: its icon and its own name, the
// folder's path on disk on hover.
func (a *App) fileFolderRow(c *ui.Context, p *project.Project, n navNode) {
	s, _ := sectionOf(n)
	muted := widgets.PaletteOf(c).Muted
	ui.Icon(c, widgets.IconFolder).TextColor(muted).FontSize(widgets.IconMedium)
	a.navNameText(c, project.FolderBase(n.name)).SingleLine().Grow(1).Shrink(1).Tooltip(s.path(p, n.name))
}

// folderOf is the folder a node of a section is, or is in: "" for the
// section's heading and what is at its top.
func folderOf(n navNode) string {
	switch n.kind {
	case nodeFileFolder:
		return n.name
	case nodeQueryFile, nodeDashboard, nodeModel:
		return project.FolderParent(n.name)
	}
	return ""
}

// fileFolderMenu is a folder's context menu.
func (a *App) fileFolderMenu(m *ui.Menu, p *project.Project, n navNode) {
	s, _ := sectionOf(n)
	if label, run := a.newFileIn(p, s, n.name); run != nil && m.Item(label).Chosen() {
		run()
	}
	if m.Item("New Folder…").Chosen() {
		a.askNewFileFolder(p, s, n.name, "")
	}
	m.Separator()
	a.moveToFolderMenu(m, p, n)
	if keymap.Item(m.Item("Rename…"), keymap.NavigatorRename).Chosen() {
		a.askRenameFileFolder(p, s, n.name)
	}
	if keymap.Item(m.Item("Delete…"), keymap.NavigatorDrop).Chosen() {
		a.askDeleteFileFolder(p, s, n.name)
	}
}

// fileFolderItems are a file's or a section heading's items of the
// section's folders: a new folder, which a file moves into, as a
// connection does into its, and Move to Folder for a file, as for a
// connection.
func (a *App) fileFolderItems(m *ui.Menu, p *project.Project, n navNode) {
	s, ok := sectionOf(n)
	if !ok {
		return
	}
	if n.kind == s.heading {
		if m.Item("New Folder…").Chosen() {
			a.askNewFileFolder(p, s, "", "")
		}
		a.sortMenu(m, s)
		return
	}
	if m.Item("New Folder…").Chosen() {
		a.askNewFileFolder(p, s, folderOf(n), n.name)
	}
	a.moveToFolderMenu(m, p, n)
}

// moveToFolderMenu offers the folders of its section a file or a folder
// can move to: not the one it is in, nor, for a folder, itself or one in
// it. DBeaver has no such item; it is here for the keys.
func (a *App) moveToFolderMenu(m *ui.Menu, p *project.Project, n navNode) {
	s, _ := sectionOf(n)
	folders := a.sectionFiles(p, s).Folders
	if len(folders) == 0 {
		return
	}
	m.Submenu("Move to Folder", func(m *ui.Menu) {
		for _, to := range slices.Concat([]string{""}, folders) {
			label := to
			if to == "" {
				label = topLevel
			}
			if m.Item(label).Disabled(a.canDropFile(p, s, n, to) != nil).Chosen() {
				a.moveFileTo(p, s, n, to)
			}
		}
	})
}

// fileFolderKeys handles the keys of a folder chosen in the navigator, as
// its menu's items.
func (a *App) fileFolderKeys(c *ui.Context, l *ui.ListState, p *project.Project, n navNode) {
	s, _ := sectionOf(n)
	switch {
	case keymap.ListPressed(c, l, keymap.NavigatorRename):
		a.askRenameFileFolder(p, s, n.name)
	case keymap.ListPressed(c, l, keymap.NavigatorDrop):
		a.askDeleteFileFolder(p, s, n.name)
	}
}

// fileDrag is what a drag of a file's row, or a folder's, in the files
// pane carries: its node.
type fileDrag struct{ node navNode }

// fileDragRows is what a drag of the files pane's rows carries, as its
// list's DragRows: a file or a folder of a section, to a folder; nil for
// the other rows, which do not drag.
func (a *App) fileDragRows(rows []int) any {
	if len(rows) != 1 {
		return nil
	}
	n := a.nav.files.tree.Item(rows[0])
	if s, ok := sectionOf(n); ok && n.kind != s.heading {
		return fileDrag{node: n}
	}
	return nil
}

// fileDropTarget lets a folder's row, or a section's heading for its top,
// take the files and folders of its section dragged onto it.
func (a *App) fileDropTarget(c *ui.Context, row ui.Element, p *project.Project, n navNode) {
	s, _ := sectionOf(n)
	to := folderOf(n)
	if d, ok := ui.DragOver[fileDrag](row); ok && a.canDropFile(p, s, d.node, to) == nil {
		row.Background(widgets.ChosenRow(c))
	}
	// A row that cannot take it does not light up, and takes nothing, as
	// a connection's folder.
	if d, ok := ui.Drop[fileDrag](row); ok && a.canDropFile(p, s, d.node, to) == nil {
		a.moveFileTo(p, s, d.node, to)
	}
}

// canDropFile says why a file or a folder cannot move to the folder to of
// a section, nil when it can: it stays in its project and section, goes
// neither where it is nor, a folder, into itself, nor beside one of its
// name.
func (a *App) canDropFile(p *project.Project, s fileSection, n navNode, to string) error {
	if from, ok := sectionOf(n); !ok || n.projectDir != p.Dir || from.heading != s.heading {
		return errors.New("A file stays in its project and its section.")
	}
	switch {
	case project.FolderParent(n.name) == to:
		return errors.New("It is there already.")
	case n.kind == nodeFileFolder && project.UnderFolder(to, n.name):
		return errors.New("A folder cannot go into itself.")
	}
	return a.pathFree(p, s, folderPath(to, project.FolderBase(n.name)), n.name)
}

// pathFree says why a file or a folder of a section cannot take path,
// nil when it can: except is the one moved there, which may keep its name
// in another case. Names are told apart by case too, as a case-blind disk
// would not.
func (a *App) pathFree(p *project.Project, s fileSection, path, except string) error {
	l := a.sectionFiles(p, s)
	for _, taken := range slices.Concat(l.Folders, l.Files) {
		if taken != except && strings.EqualFold(taken, path) {
			return errors.New(project.FolderBase(path) + " is there already.")
		}
	}
	exceptPath := ""
	if except != "" {
		exceptPath = s.path(p, except)
	}
	if taken(s.path(p, path), exceptPath) {
		return errors.New(project.FolderBase(path) + " is there already.")
	}
	return nil
}

// fileFolderName cleans the name of a folder of files, or says why it
// cannot be one: one whose name starts with a dot, as .git, is not
// listed (project.List), so the files moved into it would vanish.
func fileFolderName(name string) (string, error) {
	name, err := project.CleanFolderName(name)
	if err == nil && strings.HasPrefix(name, ".") {
		return "", errors.New("A folder's name may not start with a dot: such folders are hidden.")
	}
	return name, err
}

// moveFileTo moves a file or a folder of a section to the folder to, and
// shows it there.
func (a *App) moveFileTo(p *project.Project, s fileSection, n navNode, to string) {
	dest := folderPath(to, project.FolderBase(n.name))
	if err := a.moveOnDisk(p, s, n.name, dest); err != nil {
		a.ShowReport(widgets.ErrorReportOf("Could not move "+project.FolderBase(n.name), err))
		return
	}
	a.expandFileFolder(p, s, to)
	if n.kind == nodeFileFolder {
		a.carryOpenFolders(p, s, n.name, dest)
	}
}

// expandFileFolder opens a folder of a section, and those it is in.
func (a *App) expandFileFolder(p *project.Project, s fileSection, folder string) {
	a.nav.expand(s.folderNode(p, ""))
	for ; folder != ""; folder = project.FolderParent(folder) {
		a.nav.expand(s.folderNode(p, folder))
	}
}

// carryOpenFolders opens, under a folder moved from from to to, the
// folders that were open, as they were.
func (a *App) carryOpenFolders(p *project.Project, s fileSection, from, to string) {
	a.expandFileFolder(p, s, to)
	var open []navNode
	for n := range a.nav.files.tree.Open.All() {
		if n.kind != nodeFileFolder || n.projectDir != p.Dir || n.section != s.heading {
			continue
		}
		if moved, ok := project.MovedFolder(n.name, from, to); ok {
			open = append(open, s.folderNode(p, moved))
		}
	}
	for _, n := range open {
		a.nav.expand(n)
	}
}

// askNewFileFolder asks for the name of a folder to make in the folder
// parent of a section, and moves the file move, when set, into it.
func (a *App) askNewFileFolder(p *project.Project, s fileSection, parent, move string) {
	a.renaming = &renameForm{open: true, title: "New Folder", action: "Create", rename: func(name string) error {
		name, err := fileFolderName(name)
		if err != nil {
			return err
		}
		path := folderPath(parent, name)
		if err := a.pathFree(p, s, path, ""); err != nil {
			return err
		}
		if err := os.MkdirAll(s.path(p, path), 0o755); err != nil {
			return err
		}
		a.filesChanged(p, s)
		if move != "" {
			if err := a.moveOnDisk(p, s, move, folderPath(path, project.FolderBase(move))); err != nil {
				return err
			}
		}
		a.expandFileFolder(p, s, path)
		return nil
	}}
}

// askRenameFileFolder asks for a folder's new name: what is in it moves
// with it, open tabs and bookmarks too.
func (a *App) askRenameFileFolder(p *project.Project, s fileSection, path string) {
	base := project.FolderBase(path)
	a.renaming = &renameForm{open: true, title: "Rename " + base, name: base, rename: func(name string) error {
		name, err := fileFolderName(name)
		if err != nil {
			return err
		}
		to := folderPath(project.FolderParent(path), name)
		if to == path {
			return nil
		}
		if err := a.pathFree(p, s, to, path); err != nil {
			return err
		}
		if err := a.moveOnDisk(p, s, path, to); err != nil {
			return err
		}
		a.carryOpenFolders(p, s, path, to)
		return nil
	}}
}
