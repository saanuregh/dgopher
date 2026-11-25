package app

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/project"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// Folders of a project's connections, as DBeaver's navigator has: each
// connection is in one, by its Config.Folder path, or at the top level.

// connFolderChildren lists a folder of a project's connections, "" for
// the top level: its folders first, then its connections, each by name,
// as DBeaver lists them.
func (a *App) connFolderChildren(p *project.Project, path string) []navNode {
	byName := func(x, y string) int { return cmp.Compare(strings.ToLower(x), strings.ToLower(y)) }
	var folders []string
	for _, f := range p.Folders {
		if project.FolderParent(f) == path {
			folders = append(folders, f)
		}
	}
	slices.SortStableFunc(folders, func(x, y string) int { return byName(project.FolderBase(x), project.FolderBase(y)) })
	var conns []*connection.Conn
	for _, cn := range a.projectConns(p) {
		if cn.Config.Folder == path {
			conns = append(conns, cn)
		}
	}
	slices.SortStableFunc(conns, func(x, y *connection.Conn) int { return byName(x.Config.Name, y.Config.Name) })
	var out []navNode
	for _, f := range folders {
		out = append(out, navNode{kind: nodeConnFolder, projectDir: p.Dir, name: f})
	}
	for _, cn := range conns {
		out = append(out, navNode{kind: nodeConn, conn: cn.Config.ID})
	}
	switch {
	case out != nil:
		return out
	case path == "":
		return []navNode{{kind: nodeInfo, projectDir: p.Dir, name: "No connections yet"}}
	}
	return []navNode{{kind: nodeInfo, projectDir: p.Dir, name: "Empty folder", folder: path}}
}

// connFolderRow draws a folder's row: muted, as a connection's is, until
// a connection in it is connected.
func (a *App) connFolderRow(c *ui.Context, p *project.Project, n navNode) {
	color := widgets.PaletteOf(c).Muted
	if a.folderConnected(p, n.name) {
		color = c.Theme().Text
	}
	ui.Icon(c, widgets.IconFolder).TextColor(color).FontSize(widgets.IconMedium)
	a.navNameText(c, project.FolderBase(n.name)).SingleLine().Grow(1).Shrink(1).TextColor(color).Tooltip(n.name)
}

// folderConnected reports whether a connection in a folder, at any
// depth, is connected.
func (a *App) folderConnected(p *project.Project, path string) bool {
	for _, cn := range a.projectConns(p) {
		if project.UnderFolder(cn.Config.Folder, path) && cn.Status == connection.StatusConnected {
			return true
		}
	}
	return false
}

// folderPath is the path of a folder named name in parent.
func folderPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}

// folderPlace names a folder in a sentence, "the top level" for "".
func folderPlace(path string) string {
	if path == "" {
		return "the top level"
	}
	return "folder " + path
}

// freeFolderPath is the path of a folder to be named name in parent, or
// why it cannot be: except is the folder renamed, which may keep its name.
func freeFolderPath(p *project.Project, parent, name, except string) (string, error) {
	name, err := project.CleanFolderName(name)
	if err != nil {
		return "", err
	}
	path := folderPath(parent, name)
	if folderTaken(p, path, except) {
		return "", errors.New("A folder " + name + " is there already.")
	}
	return path, nil
}

// folderTaken reports whether a folder other than except has path, by
// case too: Prod beside prod would be told apart by case alone.
func folderTaken(p *project.Project, path, except string) bool {
	return slices.ContainsFunc(p.Folders, func(f string) bool { return f != except && strings.EqualFold(f, path) })
}

// changeFolders changes the project's folders, or its connections' ones,
// and saves the project; a failed save undoes the change. A connection
// moved is in the audit log, as another change of its settings is.
func (a *App) changeFolders(p *project.Project, change func()) error {
	folders := slices.Clone(p.Folders)
	conns := a.projectConns(p)
	was := make([]string, len(conns))
	for i, cn := range conns {
		was[i] = cn.Config.Folder
	}
	change()
	if err := p.Save(a.ProjectConfigs(p)); err != nil {
		p.Folders = folders
		for i, cn := range conns {
			cn.Config.Folder = was[i]
		}
		return fmt.Errorf("could not save %s: %w", project.File, err)
	}
	for i, cn := range conns {
		if f := cn.Config.Folder; f != was[i] {
			a.Record(&cn.Config, audit.Event{Kind: audit.KindConfig, Detail: "connection moved to " + folderPlace(f)})
		}
	}
	return nil
}

// expandFolder opens a folder in the tree, and the folders it is in.
func (a *App) expandFolder(p *project.Project, path string) {
	a.nav.expand(navNode{kind: nodeConnections, projectDir: p.Dir})
	for ; path != ""; path = project.FolderParent(path) {
		a.nav.expand(navNode{kind: nodeConnFolder, projectDir: p.Dir, name: path})
	}
}

// setConnsFolder puts a project's connections, by ID, in a folder.
func (a *App) setConnsFolder(p *project.Project, ids []string, path string) {
	for _, cn := range a.projectConns(p) {
		if slices.Contains(ids, cn.Config.ID) {
			cn.Config.Folder = path
		}
	}
}

// moveConnsToFolder moves a project's connections, by ID, to a folder,
// "" for the top level.
func (a *App) moveConnsToFolder(p *project.Project, ids []string, path string) {
	err := a.changeFolders(p, func() {
		a.setConnsFolder(p, ids, path)
	})
	if err != nil {
		a.ShowReport(widgets.ErrorReportOf("Could not move the connection", err))
		return
	}
	a.expandFolder(p, path)
}

// askNewFolder asks for the name of a folder to make in parent, and
// moves the connections of move, by ID, into it, as DBeaver's New Folder
// does those chosen.
func (a *App) askNewFolder(p *project.Project, parent string, move []string) {
	a.renaming = &renameForm{open: true, title: "New Folder", action: "Create", rename: func(name string) error {
		path, err := freeFolderPath(p, parent, name, "")
		if err != nil {
			return err
		}
		err = a.changeFolders(p, func() {
			p.Folders = append(p.Folders, path)
			a.setConnsFolder(p, move, path)
		})
		if err != nil {
			return err
		}
		a.expandFolder(p, path)
		return nil
	}}
}

// askRenameFolder asks for a folder's new name, which the folders and
// connections in it take too.
func (a *App) askRenameFolder(p *project.Project, path string) {
	base := project.FolderBase(path)
	a.renaming = &renameForm{open: true, title: "Rename " + base, name: base, rename: func(name string) error {
		to, err := freeFolderPath(p, project.FolderParent(path), name, path)
		if err != nil || to == path {
			return err
		}
		before := slices.Clone(p.Folders)
		if err := a.changeFolders(p, func() { a.moveFolderPaths(p, path, to) }); err != nil {
			return err
		}
		a.carryOpen(p, before, path, to)
		return nil
	}}
}

// carryOpen opens the folders moved from from to to, of those before
// the move, that were open, and the folder to itself.
func (a *App) carryOpen(p *project.Project, before []string, from, to string) {
	a.expandFolder(p, to)
	for _, f := range before {
		moved, ok := project.MovedFolder(f, from, to)
		if ok && a.nav.isOpen(navNode{kind: nodeConnFolder, projectDir: p.Dir, name: f}) {
			a.nav.expand(navNode{kind: nodeConnFolder, projectDir: p.Dir, name: moved})
		}
	}
}

// moveFolderPaths moves the folder from, and what is in it, to to.
func (a *App) moveFolderPaths(p *project.Project, from, to string) {
	for i, f := range p.Folders {
		if moved, ok := project.MovedFolder(f, from, to); ok {
			p.Folders[i] = moved
		}
	}
	for _, cn := range a.projectConns(p) {
		if moved, ok := project.MovedFolder(cn.Config.Folder, from, to); ok {
			cn.Config.Folder = moved
		}
	}
}

// askDeleteFolder asks before deleting a folder and the folders in it.
// Its connections are kept, in its parent: DBeaver means the same, but
// leaves those of a folder inside it in a folder deleted.
func (a *App) askDeleteFolder(p *project.Project, path string) {
	base, parent := project.FolderBase(path), project.FolderParent(path)
	a.closing = &closeRequest{open: true, title: "Delete folder " + base + "?", action: "Delete",
		reason: "Connections in this folder will not be deleted: they move to " + folderPlace(parent) + ".",
		onClose: func() {
			err := a.changeFolders(p, func() {
				p.Folders = slices.DeleteFunc(p.Folders, func(f string) bool { return project.UnderFolder(f, path) })
				for _, cn := range a.projectConns(p) {
					if project.UnderFolder(cn.Config.Folder, path) {
						cn.Config.Folder = parent
					}
				}
			})
			if err != nil {
				a.ShowReport(widgets.ErrorReportOf("Could not delete the folder "+base, err))
			}
		}}
}

// newConnIn opens the form of a new connection of a project, in a folder.
func (a *App) newConnIn(p *project.Project, path string) {
	if _, run := a.sectionNew(p, nodeConnections); run != nil {
		run()
	}
	if f := a.connForm; f != nil {
		f.cfg.Folder = path
	}
}

// connFolderMenu is a folder's context menu.
func (a *App) connFolderMenu(m *ui.Menu, p *project.Project, n navNode) {
	if m.Item("New Connection…").Chosen() {
		a.newConnIn(p, n.name)
	}
	if m.Item("New Folder…").Chosen() {
		a.askNewFolder(p, n.name, nil)
	}
	m.Separator()
	if keymap.Item(m.Item("Rename…"), keymap.NavigatorRename).Chosen() {
		a.askRenameFolder(p, n.name)
	}
	if keymap.Item(m.Item("Delete…"), keymap.NavigatorDrop).Chosen() {
		a.askDeleteFolder(p, n.name)
	}
}

// connFolderItems are a connection's items of its folders: a new folder
// beside it, which it moves into, as in DBeaver, and Move to Folder,
// which DBeaver lacks, for the keys.
func (a *App) connFolderItems(m *ui.Menu, cn *connection.Conn) {
	p := cn.Project
	if m.Item("New Folder…").Chosen() {
		a.askNewFolder(p, cn.Config.Folder, []string{cn.Config.ID})
	}
	if len(p.Folders) == 0 {
		return
	}
	m.Submenu("Move to Folder", func(m *ui.Menu) {
		for _, path := range slices.Concat([]string{""}, p.Folders) {
			label := path
			if path == "" {
				label = topLevel
			}
			if m.Item(label).Disabled(path == cn.Config.Folder).Chosen() {
				a.moveConnsToFolder(p, []string{cn.Config.ID}, path)
			}
		}
	})
}

// connFolderKeys handles the keys of a folder chosen in the navigator,
// as its menu's items.
func (a *App) connFolderKeys(c *ui.Context, l *ui.ListState, p *project.Project, n navNode) {
	switch {
	case keymap.ListPressed(c, l, keymap.NavigatorRename):
		a.askRenameFolder(p, n.name)
	case keymap.ListPressed(c, l, keymap.NavigatorDrop):
		a.askDeleteFolder(p, n.name)
	}
}

// connDrag is what a drag of a connection's row, or a folder's, carries.
type connDrag struct {
	project string
	ids     []string // the connections'
	folders []string // the folders' paths
}

// navDragRows is what a drag of the navigator's rows carries, as its
// list's DragRows: a connection's row or a folder's, to a folder; a
// table's, a view's or a column's, its name to an editor, written as Copy
// Name writes it; nil for the other rows, which do not drag.
func (a *App) navDragRows(rows []int) any {
	if len(rows) != 1 {
		return nil
	}
	switch n := a.nav.tree.Item(rows[0]); n.kind {
	case nodeConn:
		if cn := a.connByID(n.conn); cn != nil {
			return connDrag{project: cn.Project.Dir, ids: []string{cn.Config.ID}}
		}
	case nodeConnFolder:
		return connDrag{project: n.projectDir, folders: []string{n.name}}
	case nodeObject:
		if cn, obj, ok := a.object(n); ok && cn.DB() != nil {
			return widgets.NameDrag{Text: db.QualifiedName(cn.DB().Dialect, obj.Schema, obj.Name)}
		}
	case nodeColumn:
		if cn := a.connByID(n.conn); cn != nil && cn.DB() != nil {
			_, column, _ := strings.Cut(n.name, "\x00")
			return widgets.NameDrag{Text: cn.DB().Dialect.Quote(column)}
		}
	}
	return nil
}

// folderDropTarget lets a folder's row, or for the top level a project's
// row or name, take connections and folders dragged onto it.
func (a *App) folderDropTarget(c *ui.Context, row ui.Element, p *project.Project, n navNode) {
	path := ""
	if n.kind == nodeConnFolder {
		path = n.name
	}
	if d, ok := ui.DragOver[connDrag](row); ok && a.canDrop(p, d, path) == nil {
		row.Background(widgets.ChosenRow(c))
	}
	// A row that cannot take it does not light up, and takes nothing, as
	// in DBeaver.
	d, ok := ui.Drop[connDrag](row)
	if !ok || a.canDrop(p, d, path) != nil {
		return
	}
	before := slices.Clone(p.Folders)
	err := a.changeFolders(p, func() {
		for _, f := range d.folders {
			a.moveFolderPaths(p, f, folderPath(path, project.FolderBase(f)))
		}
		a.setConnsFolder(p, d.ids, path)
	})
	if err != nil {
		a.ShowReport(widgets.ErrorReportOf("Could not move it there", err))
		return
	}
	a.expandFolder(p, path)
	for _, f := range d.folders {
		a.carryOpen(p, before, f, folderPath(path, project.FolderBase(f)))
	}
}

// canDrop says why what is dragged cannot go into the folder path of p,
// nil when it can: a folder goes neither into itself nor into a folder
// in it, as in DBeaver, nor beside one of its name.
func (a *App) canDrop(p *project.Project, d connDrag, path string) error {
	if d.project != p.Dir {
		return errors.New("A connection stays in its project.")
	}
	for _, f := range d.folders {
		if project.UnderFolder(path, f) {
			return errors.New("A folder cannot go into itself.")
		}
		if to := folderPath(path, project.FolderBase(f)); to != f && folderTaken(p, to, f) {
			return errors.New("A folder " + project.FolderBase(f) + " is there already.")
		}
	}
	return nil
}
