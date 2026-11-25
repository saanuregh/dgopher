package app

import (
	"cmp"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/project"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// Bookmarks, as DBeaver's: links to tables, views, routines and the
// project's files, listed under a heading of their own at the top of the
// files pane. They live in the project's bookmarks.json, shared with it.
// Unlike DBeaver's, a bookmark is made at once, named after what it links
// to, and renamed later; and files have them too.

// bookmarkList is a project's bookmarks as last read, with the bytes of
// their file, which a save checks, and when it last changed.
type bookmarkList struct {
	list []project.Bookmark
	disk []byte
	mod  time.Time
	err  error
}

// bookmarks are a project's bookmarks, read again once their file
// changes, as by a git pull.
func (a *App) bookmarks(p *project.Project) bookmarkList {
	var mod time.Time
	if info, err := os.Stat(filepath.Join(p.Dir, project.BookmarksFile)); err == nil {
		mod = info.ModTime()
	}
	if l, ok := a.bookmarkLists[p.Dir]; ok && l.mod.Equal(mod) {
		return l
	}
	var l bookmarkList
	l.list, l.disk, l.err = project.LoadBookmarks(p)
	l.mod = mod
	if a.bookmarkLists == nil {
		a.bookmarkLists = map[string]bookmarkList{}
	}
	a.bookmarkLists[p.Dir] = l
	return l
}

// changeBookmarks changes a project's bookmarks and saves them, refusing
// when their file changed since it was read.
func (a *App) changeBookmarks(p *project.Project, change func([]project.Bookmark) []project.Bookmark) error {
	l := a.bookmarks(p)
	if l.err != nil {
		return l.err
	}
	list := change(slices.Clone(l.list))
	if _, err := project.SaveBookmarks(p, list, l.disk); err != nil {
		return err
	}
	delete(a.bookmarkLists, p.Dir) // read again, with its new time
	return nil
}

// bookmarkOf is the bookmark a node can have: of a table, a view, a
// routine or another item but a partition, or of a project's file; false
// for other nodes.
func (a *App) bookmarkOf(n navNode) (*project.Project, project.Bookmark, bool) {
	if s, ok := sectionOf(n); ok && n.kind == s.file {
		p := a.projectByDir(n.projectDir)
		if p == nil {
			return nil, project.Bookmark{}, false
		}
		return p, project.Bookmark{Name: a.fileName(p, s, n.name), File: p.StoredPath(a.filePath(p, n))}, true
	}
	cn := a.connByID(n.conn)
	if cn == nil || cn.Project == nil {
		return nil, project.Bookmark{}, false
	}
	b := project.Bookmark{Connection: strings.TrimPrefix(cn.Config.ID, cn.Project.Prefix), Database: n.database, Schema: n.schema}
	switch n.kind {
	case nodeObject:
		kind := db.KindTable
		if _, obj, ok := a.object(n); ok {
			kind = obj.Kind
		}
		b.Name, b.Object, b.Kind = n.name, n.name, string(kind)
	case nodeItem:
		_, it, ok := a.item(n)
		if !ok || it.Kind == db.ItemPartition {
			return nil, project.Bookmark{}, false
		}
		b.Name, b.Object, b.Item = it.Label(), it.Label(), string(it.Kind)
	default:
		return nil, project.Bookmark{}, false
	}
	return cn.Project, b, true
}

// bookmarked reports whether a project has a bookmark of target.
func (a *App) bookmarked(p *project.Project, target string) bool {
	return slices.ContainsFunc(a.bookmarks(p).list, func(b project.Bookmark) bool { return b.Target() == target })
}

// toggleBookmark adds a bookmark of a node, or removes the one it has,
// and shows the bookmarks.
func (a *App) toggleBookmark(n navNode) {
	p, b, ok := a.bookmarkOf(n)
	if !ok {
		return
	}
	err := a.changeBookmarks(p, func(list []project.Bookmark) []project.Bookmark {
		if i := slices.IndexFunc(list, func(x project.Bookmark) bool { return x.Target() == b.Target() }); i >= 0 {
			return slices.Delete(list, i, i+1)
		}
		return append(list, b)
	})
	if err != nil {
		a.ShowReport(widgets.ErrorReportOf("Could not change the bookmarks of "+p.Name, err))
		return
	}
	a.nav.expand(navNode{kind: nodeBookmarks, projectDir: p.Dir})
}

// bookmarkItem is the menu's item that adds a node's bookmark, or
// removes it.
func (a *App) bookmarkItem(m *ui.Menu, n navNode) {
	p, b, ok := a.bookmarkOf(n)
	if !ok {
		return
	}
	label := "Add Bookmark"
	if a.bookmarked(p, b.Target()) {
		label = "Remove Bookmark"
	}
	if keymap.Item(m.Item(label), keymap.NavigatorBookmark).Chosen() {
		a.toggleBookmark(n)
	}
}

// bookmarkChildren are the nodes of a project's bookmarks, in the order
// they were made.
func (a *App) bookmarkChildren(p *project.Project) []navNode {
	l := a.bookmarks(p)
	out := make([]navNode, len(l.list))
	for i, b := range l.list {
		out[i] = navNode{kind: nodeBookmark, projectDir: p.Dir, name: b.Target()}
	}
	return out
}

// bookmark is the bookmark a node of one names.
func (a *App) bookmark(p *project.Project, n navNode) (project.Bookmark, bool) {
	for _, b := range a.bookmarks(p).list {
		if b.Target() == n.name {
			return b, true
		}
	}
	return project.Bookmark{}, false
}

// bookmarkTarget is the node of what a bookmark links to, and the
// connection of an object's; false for a file outside the sections.
func (a *App) bookmarkTarget(p *project.Project, b project.Bookmark) (navNode, *connection.Conn, bool) {
	if b.File != "" {
		path := p.ResolvePath(b.File)
		for _, s := range fileSections {
			if rel, ok := s.rel(p, path); ok && strings.EqualFold(filepath.Ext(rel), s.ext) {
				return s.fileNode(p, rel), nil, true
			}
		}
		return navNode{}, nil, false
	}
	n := navNode{kind: nodeObject, conn: p.Prefix + b.Connection, database: b.Database, schema: b.Schema, name: b.Object}
	if b.Item != "" {
		n.kind, n.name = nodeItem, b.Item+"\x00"+b.Object
	}
	cn := a.connByID(n.conn)
	return n, cn, cn != nil
}

// bookmarkMissing says why what a bookmark links to is not there, ""
// when it is, or is not known yet: an object is looked for once its
// schema is read.
func (a *App) bookmarkMissing(p *project.Project, b project.Bookmark) string {
	n, cn, ok := a.bookmarkTarget(p, b)
	switch {
	case !ok && b.File != "":
		return "Not a file of the project's queries, dashboards or data models"
	case !ok:
		return "No connection of " + p.Name + " has the ID " + b.Connection
	case b.File != "":
		if _, err := os.Stat(p.ResolvePath(b.File)); err != nil {
			return "The file is gone, moved or deleted outside DGopher"
		}
		return ""
	}
	// Known gone only once what the schema holds is read.
	key := connection.SchemaKey{Database: n.database, Schema: n.schema}
	found := true
	if n.kind == nodeObject {
		if _, loaded := cn.Objects[key]; loaded {
			_, _, found = a.object(n)
		}
	} else if _, loaded := cn.Items[key]; loaded {
		_, _, found = a.item(n)
	}
	if found {
		return ""
	}
	return "Not in " + cmp.Or(b.Schema, "the database") + " any more"
}

// bookmarkRow draws a bookmark's row: its name, then where it is, the
// warning color and why on hover when what it links to is gone.
func (a *App) bookmarkRow(c *ui.Context, p *project.Project, n navNode) {
	b, ok := a.bookmark(p, n)
	if !ok {
		return
	}
	muted := widgets.PaletteOf(c).Muted
	missing := a.bookmarkMissing(p, b)
	icon := ui.Icon(c, widgets.IconPin).TextColor(muted).FontSize(widgets.IconSmall)
	name := a.navNameText(c, b.Name).SingleLine().Shrink(1)
	if missing != "" {
		icon.TextColor(c.Theme().Warning)
		name.TextColor(c.Theme().Warning).Tooltip(missing)
	}
	ui.Spacer(c)
	ui.Text(c, a.bookmarkPlace(p, b)).SingleLine().Shrink(fileConnectionShrink).FontSize(widgets.TextMeta).TextColor(muted)
}

// bookmarkPlace says where what a bookmark links to is: an object's
// schema and connection, a file's kind.
func (a *App) bookmarkPlace(p *project.Project, b project.Bookmark) string {
	n, cn, ok := a.bookmarkTarget(p, b)
	if b.File != "" {
		if s, found := sectionOf(n); ok && found {
			return s.noun
		}
		return "file"
	}
	conn := b.Connection
	if ok {
		conn = cn.Config.Name
	}
	if b.Schema == "" {
		return conn
	}
	return b.Schema + " · " + conn
}

// openBookmark opens what a bookmark links to: a file in its tab; an
// object once its connection connects, as its row in the navigator does,
// found there first.
func (a *App) openBookmark(p *project.Project, b project.Bookmark) {
	n, cn, ok := a.bookmarkTarget(p, b)
	if !ok {
		a.ShowError("Could not open "+b.Name, a.bookmarkMissing(p, b)+".")
		return
	}
	if cn == nil {
		a.activate(n)
		return
	}
	a.Connect(cn, func() {
		a.nav.revealNode(&n)
		a.nav.openRevealed = &n
	})
}

// renameBookmark asks for a bookmark's new name.
func (a *App) renameBookmark(p *project.Project, n navNode) {
	b, ok := a.bookmark(p, n)
	if !ok {
		return
	}
	a.renaming = &renameForm{open: true, title: "Rename " + b.Name, name: b.Name, rename: func(name string) error {
		name = strings.TrimSpace(name)
		if name == "" {
			return errors.New("Name the bookmark.")
		}
		return a.changeBookmarks(p, func(list []project.Bookmark) []project.Bookmark {
			for i := range list {
				if list[i].Target() == n.name {
					list[i].Name = name
				}
			}
			return list
		})
	}}
}

// removeBookmark removes a bookmark: what it links to stays.
func (a *App) removeBookmark(p *project.Project, n navNode) {
	err := a.changeBookmarks(p, func(list []project.Bookmark) []project.Bookmark {
		return slices.DeleteFunc(list, func(b project.Bookmark) bool { return b.Target() == n.name })
	})
	if err != nil {
		a.ShowReport(widgets.ErrorReportOf("Could not remove the bookmark", err))
	}
}

// bookmarkMenu is a bookmark's context menu.
func (a *App) bookmarkMenu(m *ui.Menu, p *project.Project, n navNode) {
	if m.Item("Open").Chosen() {
		if b, ok := a.bookmark(p, n); ok {
			a.openBookmark(p, b)
		}
	}
	if keymap.Item(m.Item("Rename…"), keymap.NavigatorRename).Chosen() {
		a.renameBookmark(p, n)
	}
	if keymap.Item(m.Item("Remove Bookmark"), keymap.NavigatorDrop).Chosen() {
		a.removeBookmark(p, n)
	}
}

// bookmarkKeys handles the keys of a bookmark chosen in the navigator, as
// its menu's items.
func (a *App) bookmarkKeys(c *ui.Context, l *ui.ListState, p *project.Project, n navNode) {
	switch {
	case keymap.ListPressed(c, l, keymap.NavigatorRename):
		a.renameBookmark(p, n)
	case keymap.ListPressed(c, l, keymap.NavigatorDrop):
		a.removeBookmark(p, n)
	}
}

// moveBookmarks has the bookmarks of the files at src, or in it, a
// folder, follow them to dst.
func (a *App) moveBookmarks(p *project.Project, src, dst string) error {
	return a.changeFilesBookmarks(p, src, func(b *project.Bookmark) bool {
		b.File = p.StoredPath(dst + strings.TrimPrefix(p.ResolvePath(b.File), src))
		return true
	})
}

// bookmarksFollow has the bookmarks of a file or a folder the app moved
// follow it, saying so when they cannot: the move itself is done.
func (a *App) bookmarksFollow(p *project.Project, src, dst string) {
	if err := a.moveBookmarks(p, src, dst); err != nil {
		a.ShowReport(widgets.ErrorReportOf("Moved, but its bookmarks could not follow", err))
	}
}

// dropBookmarks removes the bookmarks of the files at path, or in it, a
// folder, deleted.
func (a *App) dropBookmarks(p *project.Project, path string) error {
	return a.changeFilesBookmarks(p, path, func(*project.Bookmark) bool { return false })
}

// changeFilesBookmarks changes the bookmarks of the files at path, or in
// it, by change, which keeps a bookmark when it returns true; it saves
// only when one of them is there.
func (a *App) changeFilesBookmarks(p *project.Project, path string, change func(*project.Bookmark) bool) error {
	under := func(b project.Bookmark) bool {
		return b.File != "" && atOrUnder(p.ResolvePath(b.File), path)
	}
	if !slices.ContainsFunc(a.bookmarks(p).list, under) {
		return nil
	}
	return a.changeBookmarks(p, func(list []project.Bookmark) []project.Bookmark {
		var out []project.Bookmark
		for _, b := range list {
			if !under(b) || change(&b) {
				out = append(out, b)
			}
		}
		return out
	})
}
