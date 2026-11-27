package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// The navigator's tree of a document connection: its databases, each
// with its Collections and Views, and a collection's indexes. They are
// read off the UI goroutine as their node opens, as a SQL catalog is,
// and kept per connection until it reconnects or is refreshed.

// The folders of a document database.
const (
	docCollections = "collections"
	docViews       = "views"
)

// docCatalogTimeout bounds a read of the catalog.
const docCatalogTimeout = 30 * time.Second

// docCatalog is what the navigator read of a document connection.
type docCatalog struct {
	// store is the connection's store it was read from: another, after a
	// reconnect, reads it all again.
	store     db.Store
	databases []db.DocDatabase
	dbsRead   bool
	colls     map[string][]db.DocCollection
	indexes   map[docKey][]db.DocIndex
	loading   map[docKey]bool
	errs      map[docKey]string
	// reads counts the refreshes of each part, so that a read started
	// before one is dropped.
	reads map[docKey]int
}

// docKey names a part of the catalog: the databases (all ""), a
// database's collections, or a collection's indexes.
type docKey struct{ database, coll string }

// docCatalogOf is cn's catalog as read so far.
func (a *App) docCatalogOf(cn *connection.Conn) *docCatalog {
	if a.nav.docs == nil {
		a.nav.docs = map[string]*docCatalog{}
	}
	cat := a.nav.docs[cn.Config.ID]
	if cat == nil || cat.store != cn.Store {
		cat = &docCatalog{store: cn.Store, colls: map[string][]db.DocCollection{}, indexes: map[docKey][]db.DocIndex{},
			loading: map[docKey]bool{}, errs: map[docKey]string{}, reads: map[docKey]int{}}
		a.nav.docs[cn.Config.ID] = cat
	}
	return cat
}

// loadDoc reads a part of the catalog once, keeping it with keep, or why
// it could not be read.
func (a *App) loadDoc(cn *connection.Conn, key docKey, read func(ctx context.Context, d *db.Doc) (keep func(*docCatalog), err error)) {
	cat := a.docCatalogOf(cn)
	d := cn.Doc()
	if d == nil || cat.loading[key] {
		return
	}
	cat.loading[key] = true
	gen := cat.reads[key]
	a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), docCatalogTimeout)
		defer cancel()
		keep, err := read(ctx, d)
		return func() {
			if a.nav.docs[cn.Config.ID] != cat || cat.reads[key] != gen {
				return // refreshed or reconnected since
			}
			delete(cat.loading, key)
			if err != nil {
				cat.errs[key] = err.Error()
				return
			}
			keep(cat)
		}
	})
}

// docDatabaseNodes are a connected document connection's databases.
func (a *App) docDatabaseNodes(cn *connection.Conn, n navNode, open bool) []navNode {
	cat := a.docCatalogOf(cn)
	if !cat.dbsRead {
		if e := cat.errs[docKey{}]; e != "" {
			return a.catalogError(cn, n, "the databases", "Failed: "+widgets.FirstLine(e), e)
		}
		if open {
			a.loadDoc(cn, docKey{}, func(ctx context.Context, d *db.Doc) (func(*docCatalog), error) {
				dbs, err := d.Databases(ctx)
				return func(cat *docCatalog) { cat.databases, cat.dbsRead = dbs, true }, err
			})
		}
		return info(n, "Loading…")
	}
	if len(cat.databases) == 0 {
		return info(n, "No databases")
	}
	out := make([]navNode, len(cat.databases))
	for i, d := range cat.databases {
		out[i] = navNode{kind: nodeDocDatabase, conn: n.conn, database: d.Name}
	}
	return out
}

// docChildren are a document database's folders, a folder's collections
// or views, and a collection's indexes, read once their node is open.
func (a *App) docChildren(cn *connection.Conn, n navNode, open bool) []navNode {
	cat := a.docCatalogOf(cn)
	if n.kind == nodeDocCollection {
		return a.docIndexNodes(cn, cat, n, open)
	}
	key := docKey{database: n.database}
	colls, ok := cat.colls[n.database]
	if !ok {
		if e := cat.errs[key]; e != "" {
			return a.catalogError(cn, n, "the collections", "Failed: "+widgets.FirstLine(e), e)
		}
		if open {
			database := n.database
			a.loadDoc(cn, key, func(ctx context.Context, d *db.Doc) (func(*docCatalog), error) {
				colls, err := d.Collections(ctx, database)
				return func(cat *docCatalog) { cat.colls[database] = colls }, err
			})
		}
		return info(n, "Loading…")
	}
	if n.kind == nodeDocDatabase {
		var out []navNode
		for _, folder := range []string{docCollections, docViews} {
			if countDocColls(colls, folder) > 0 {
				out = append(out, navNode{kind: nodeDocFolder, conn: n.conn, database: n.database, folder: folder})
			}
		}
		if out == nil {
			return info(n, "No collections")
		}
		return out
	}
	var out []navNode
	for _, c := range colls {
		if docFolderOf(c) == n.folder {
			out = append(out, navNode{kind: nodeDocCollection, conn: n.conn, database: n.database, name: c.Name})
		}
	}
	return out
}

// docIndexNodes are a collection's indexes; a view has none, and no
// children.
func (a *App) docIndexNodes(cn *connection.Conn, cat *docCatalog, n navNode, open bool) []navNode {
	if c, ok := a.docCollection(cn, n); !ok || c.Type == "view" {
		return nil
	}
	key := docKey{database: n.database, coll: n.name}
	idx, ok := cat.indexes[key]
	if !ok {
		if e := cat.errs[key]; e != "" {
			return a.catalogError(cn, n, "the indexes", "Failed: "+widgets.FirstLine(e), e)
		}
		if open {
			a.loadDoc(cn, key, func(ctx context.Context, d *db.Doc) (func(*docCatalog), error) {
				idx, err := d.Indexes(ctx, key.database, key.coll)
				return func(cat *docCatalog) { cat.indexes[key] = idx }, err
			})
		}
		return info(n, "Loading…")
	}
	if len(idx) == 0 {
		return info(n, "No indexes")
	}
	out := make([]navNode, len(idx))
	for i, ix := range idx {
		out[i] = navNode{kind: nodeDocIndex, conn: n.conn, database: n.database, name: n.name + "\x00" + ix.Name}
	}
	return out
}

// docFolderOf is the folder a collection shows in: a view in Views, a
// collection, time series ones too, in Collections.
func docFolderOf(c db.DocCollection) string {
	if c.Type == "view" {
		return docViews
	}
	return docCollections
}

func countDocColls(colls []db.DocCollection, folder string) int {
	n := 0
	for _, c := range colls {
		if docFolderOf(c) == folder {
			n++
		}
	}
	return n
}

// docCollection is the collection or view a node names.
func (a *App) docCollection(cn *connection.Conn, n navNode) (db.DocCollection, bool) {
	coll, _, _ := strings.Cut(n.name, "\x00")
	for _, c := range a.docCatalogOf(cn).colls[n.database] {
		if c.Name == coll {
			return c, true
		}
	}
	return db.DocCollection{}, false
}

// docIndex is the index a node names.
func (a *App) docIndex(cn *connection.Conn, n navNode) (db.DocIndex, bool) {
	coll, name, _ := strings.Cut(n.name, "\x00")
	for _, ix := range a.docCatalogOf(cn).indexes[docKey{database: n.database, coll: coll}] {
		if ix.Name == name {
			return ix, true
		}
	}
	return db.DocIndex{}, false
}

// navDocRow is the row of a document database, folder, collection or
// index.
func (a *App) navDocRow(c *ui.Context, row ui.Element, cn *connection.Conn, n navNode, quiet ui.Color) {
	muted := widgets.PaletteOf(c).Muted
	meta := func(s string) ui.Element {
		return ui.Text(c, s).FontSize(widgets.TextMeta).FontFeatures("tnum").TextColor(muted).SingleLine().Shrink(1)
	}
	switch n.kind {
	case nodeDocDatabase:
		ui.Icon(c, widgets.IconDatabase).TextColor(muted).FontSize(widgets.IconMedium)
		a.navNameText(c, n.database).SingleLine().Shrink(1)
		ui.Spacer(c)
		at, ok := a.nav.paneOf(n).chosen()
		if rowAdd(c, row, ok && at == n, "Open Console on "+n.database) {
			a.openMongoConsole(cn, n.database)
		}
		for _, d := range a.docCatalogOf(cn).databases {
			if d.Name != n.database {
				continue
			}
			if d.Empty {
				meta("empty").Shrink(0)
			} else {
				meta(widgets.HumanBytes(d.SizeOnDisk)).Shrink(0).Tooltip("Its size on disk")
			}
		}
	case nodeDocFolder:
		label := "Collections"
		if n.folder == docViews {
			label = "Views"
		}
		name := navHeadingText(c, label, false)
		if quiet != ui.Transparent {
			name.TextColor(c.Theme().Text)
		}
		meta(a.navCount(n, countDocColls(a.docCatalogOf(cn).colls[n.database], n.folder)))
	case nodeDocCollection:
		coll, _ := a.docCollection(cn, n)
		ic, note := widgets.IconTable, ""
		switch {
		case coll.Type == "view":
			ic, note = widgets.IconView, "on "+coll.ViewOn
		case coll.Type == "timeseries":
			ic, note = widgets.IconClock, "time series"
		case coll.Capped:
			note = "capped"
		}
		ui.Icon(c, ic).TextColor(muted).FontSize(widgets.IconMedium)
		a.navNameText(c, n.name).SingleLine().Grow(1).Shrink(1)
		if note != "" {
			meta(note)
		}
	case nodeDocIndex:
		ix, _ := a.docIndex(cn, n)
		ui.Icon(c, widgets.IconKey).TextColor(muted).FontSize(widgets.IconSmall)
		ui.Text(c, ix.Name).SingleLine().Shrink(1).Tooltip(docIndexDetail(ix))
		ui.Spacer(c)
		if ix.Unique {
			meta("unique")
		}
	}
}

// docIndexDetail says an index's keys and options.
func docIndexDetail(ix db.DocIndex) string {
	keys := make([]string, len(ix.Keys))
	for i, k := range ix.Keys {
		keys[i] = fmt.Sprintf("%s: %v", k.Key, docKeyValue(k.Value))
	}
	s := "{ " + strings.Join(keys, ", ") + " }"
	for _, o := range []struct {
		on   bool
		what string
	}{{ix.Unique, "unique"}, {ix.Sparse, "sparse"}, {ix.Hidden, "hidden"}, {ix.Partial != nil, "partial"}} {
		if o.on {
			s += ", " + o.what
		}
	}
	if ix.ExpireAfter != nil {
		s += fmt.Sprintf(", expires after %ds", *ix.ExpireAfter)
	}
	return s
}

// docKeyValue is an index key's direction or kind, as 1, -1 or "text".
func docKeyValue(v any) any {
	if rv, ok := v.(bson.RawValue); ok {
		if s, ok := rv.StringValueOK(); ok {
			return `"` + s + `"`
		}
		if n, ok := rv.AsInt64OK(); ok {
			return n
		}
		return rv.String()
	}
	return v
}

// navDocMenu is the menu of a document database, folder, collection or
// index.
func (a *App) navDocMenu(m *ui.Menu, cn *connection.Conn, n navNode) {
	if n.kind == nodeDocCollection {
		if m.Item("Open Documents").Chosen() {
			a.openMongoCollection(cn, n.database, n.name)
		}
	}
	if m.Item("Open Console").Chosen() {
		a.openMongoConsole(cn, n.database)
	}
	if n.kind == nodeDocIndex {
		return
	}
	if m.Item("Refresh").Chosen() {
		a.refreshDocNode(cn, n)
	}
}

// refreshDocNode forgets what was read under a database, folder or
// collection, for it to be read again; a read already running is
// dropped when it ends.
func (a *App) refreshDocNode(cn *connection.Conn, n navNode) {
	cat := a.docCatalogOf(cn)
	forget := func(k docKey) {
		delete(cat.indexes, k)
		delete(cat.errs, k)
		delete(cat.loading, k)
		cat.reads[k]++
	}
	if n.kind == nodeDocCollection {
		forget(docKey{database: n.database, coll: n.name})
		return
	}
	delete(cat.colls, n.database)
	forget(docKey{database: n.database})
	// The database's collections' indexes: those read, failed or being read.
	var under []docKey
	for k := range cat.indexes {
		under = append(under, k)
	}
	for k := range cat.errs {
		under = append(under, k)
	}
	for k := range cat.loading {
		under = append(under, k)
	}
	for _, k := range under {
		if k.database == n.database && k.coll != "" {
			forget(k)
		}
	}
}
