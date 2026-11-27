package mongo

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/mongosh"
	"dgopher/internal/redact"
	"dgopher/internal/safety"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/editor"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// pageSize is how many documents are read at a time.
const pageSize = 200

// readTimeout bounds a page of documents, and a count.
var readTimeout = 60 * time.Second

// CollectionTab browses a collection's documents under a filter, a sort
// and a projection, and edits them as pending changes saved together.
type CollectionTab struct {
	a          Host
	conn       *connection.Conn
	Database   string
	Collection string

	// The filter, sort and projection as typed, and as the documents
	// shown were found with; inputErr is why those typed do not read.
	FilterIn, SortIn, FieldsIn string
	found                      findArgs
	inputErr                   string

	read        []bson.Raw
	readGen     int // counts the finds, so that a page of an older one is dropped
	cursor      *db.DocCursor
	done        bool
	loading     bool
	fetchingAll bool
	loadErr     string
	total       int64 // the documents matching, -1 until counted
	elapsed     time.Duration
	// kind is the collection's type (collection, view, timeseries), ""
	// until read.
	kind string

	edits *pendingEdits
	view  *docView
	// shown are the documents as shown, made for shownKey: readVer, which
	// counts the changes of read, and the edits' generation.
	shown    []bson.Raw
	shownKey [2]int
	readVer  int

	// detail is the place of the document shown beside the rows, -1 for
	// none; detailJSON shows it as text to edit.
	detail     int
	detailJSON bool
	detailText string
	detailOf   int // the edits' generation detailText was written at
	detailErr  string
	detailArea editor.CodeArea
	split      float32 // the percentage of the room the rows take beside it

	field      *fieldEdit
	insert     *insertDialog
	statements *statementsDialog
	saving     bool
	saveStop   *atomic.Bool
	saveStart  time.Time
}

// findArgs are what a find reads: as typed, and as values.
type findArgs struct {
	filterText, sortText, fieldsText string
	filter, sort, fields             bson.D
}

// NewCollection opens a collection's documents, found at once.
func NewCollection(h Host, cn *connection.Conn, database, coll string) *CollectionTab {
	return NewCollectionFiltered(h, cn, database, coll, "")
}

// NewCollectionFiltered opens a collection's documents found with a
// filter, as a saved tab keeps it; one that does not read shows why.
func NewCollectionFiltered(h Host, cn *connection.Conn, database, coll, filter string) *CollectionTab {
	t := &CollectionTab{a: h, conn: cn, Database: database, Collection: coll, FilterIn: filter, total: -1, detail: -1, edits: newPendingEdits(), view: newDocView(), split: 64}
	t.detailArea = editor.CodeArea{Label: "Document", Language: editor.LanguageJSON}
	t.readKind()
	t.Find()
	return t
}

// Filter is the filter the documents shown were found with, as typed.
func (t *CollectionTab) Filter() string { return t.found.filterText }

func (t *CollectionTab) Title() string { return t.Database + "." + t.Collection }

func (t *CollectionTab) Connection() *connection.Conn { return t.conn }

func (t *CollectionTab) Icon() *ui.SVG { return widgets.IconTable }

func (t *CollectionTab) Places() []connection.Place {
	return []connection.Place{t.conn.PlaceOf(t.Database)}
}

// CloseReason says what closing loses: changes not saved.
func (t *CollectionTab) CloseReason() string {
	if n := t.edits.count(); n > 0 {
		return fmt.Sprintf("%s has %s not saved: closing drops them.", t.Title(), widgets.Count(n, "change"))
	}
	return ""
}

// Mark is the changes not saved, which stay until saved or dropped.
func (t *CollectionTab) Mark() string {
	if t.edits.count() > 0 {
		return t.CloseReason()
	}
	return ""
}

// Busy reports documents being read or saved.
func (t *CollectionTab) Busy() bool { return t.loading || t.saving }

func (t *CollectionTab) Close() {
	t.readGen++
	t.closeCursor()
	if t.saveStop != nil {
		t.saveStop.Store(true)
	}
}

// savingWhy says why the changes cannot be dropped, or the documents
// found again, while they are being saved: a save that fails part way
// applies what it saved to the documents it started from.
func (t *CollectionTab) savingWhy() string { return widgets.When(t.saving, beingSaved) }

const beingSaved = "The changes are being saved"

// beingRead is why the documents cannot be edited or saved while they are
// read: a find drops the edits when it ends, and a save applies what it
// saved to the documents it started from.
const beingRead = "The documents are being read"

// busyWhy says why the changes cannot be saved, or more documents read:
// they are being saved, or documents read.
func (t *CollectionTab) busyWhy() string {
	switch {
	case t.saving:
		return beingSaved
	case t.loading:
		return beingRead
	}
	return ""
}

// refused says why in a toast, and reports, when why is not "": as
// refused(t.savingWhy()) while the changes are being saved, or
// refused(t.readOnly()) when the documents cannot be edited now, as from a
// dialog opened before.
func (t *CollectionTab) refused(why string) bool {
	if why != "" {
		t.a.Toast(why, "", nil)
		return true
	}
	return false
}

// Discard drops the pending changes, as a disconnect does; not while
// they are being saved.
func (t *CollectionTab) Discard() {
	if t.refused(t.savingWhy()) {
		return
	}
	t.edits.clear()
	t.detailErr = ""
}

// Online finds the documents again when the network took the last find.
func (t *CollectionTab) Online() {
	if t.loadErr != "" && !t.loading && !t.saving {
		t.Find()
	}
}

func (t *CollectionTab) closeCursor() {
	if cur := t.cursor; cur != nil {
		t.cursor = nil
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = cur.Close(ctx) // the server ends an abandoned cursor itself
		}()
	}
}

// readKind reads whether the collection is a view, whose documents are
// not edited.
func (t *CollectionTab) readKind() {
	d, database, coll := t.conn.Doc(), t.Database, t.Collection
	if d == nil {
		return
	}
	t.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		colls, err := d.Collections(ctx, database)
		if err != nil {
			return nil // edits are refused by the server then
		}
		for _, c := range colls {
			if c.Name == coll {
				return func() { t.kind = c.Type }
			}
		}
		return nil
	})
}

// readOnly says why the documents cannot be edited, "" when they can.
func (t *CollectionTab) readOnly() string {
	cfg := &t.conn.Config
	switch {
	case cfg.ReadOnly:
		return cfg.Name + " is read-only. Edit the connection to allow writes."
	case t.kind == "view":
		return t.Collection + " is a view: its documents are another collection's."
	}
	return t.busyWhy()
}

// parseFind reads the filter, sort and projection typed.
func (t *CollectionTab) parseFind() (findArgs, error) {
	f := findArgs{filterText: t.FilterIn, sortText: t.SortIn, fieldsText: t.FieldsIn}
	var err error
	if f.filter, err = parseDocument(f.filterText, "The filter"); err != nil {
		return f, fmt.Errorf("Filter: %w", err)
	}
	if f.sort, err = parseDocument(f.sortText, "The sort"); err != nil {
		return f, fmt.Errorf("Sort: %w", err)
	}
	if f.fields, err = parseDocument(f.fieldsText, "The projection"); err != nil {
		return f, fmt.Errorf("Fields: %w", err)
	}
	return f, nil
}

// findOp is the find of f, as the console would run it.
func (t *CollectionTab) findOp(f findArgs) mongosh.Op {
	filter := f.filter
	if filter == nil {
		filter = bson.D{}
	}
	op := mongosh.Op{Collection: t.Collection, Method: "find", Args: []mongosh.Value{filter}}
	if len(f.fields) > 0 {
		op.Args = append(op.Args, f.fields)
	}
	if len(f.sort) > 0 {
		op.Chain = []mongosh.Call{{Name: "sort", Args: []mongosh.Value{f.sort}}}
	}
	op.Text = statementText(t.Collection, op)
	if len(f.sort) > 0 {
		op.Text += ".sort(" + shellOf(f.sort) + ")"
	}
	return op
}

// Find reads the documents again under the filter, sort and projection
// typed; changes not saved are asked about first. It does nothing while
// they are being saved.
func (t *CollectionTab) Find() {
	if t.refused(t.savingWhy()) {
		return
	}
	f, err := t.parseFind()
	if err != nil {
		t.inputErr = err.Error()
		return
	}
	t.inputErr = ""
	if n := t.edits.count(); n > 0 {
		t.a.AskPending(n, func() { t.Save() }, func() { t.Discard(); t.find(f) }, nil)
		return
	}
	t.find(f)
}

func (t *CollectionTab) find(f findArgs) {
	op := t.findOp(f)
	v := safety.ReviewMongo(&t.conn.Config, op)
	if v.Blocked != "" {
		t.a.RecordBlocked(t.conn, v.Blocked, redact.Mongo(op.Text))
		t.a.ShowError("Not allowed", v.Blocked)
		return
	}
	run := func() { t.runFind(f, op) }
	if v.Confirm {
		t.a.AskConfirm(t.conn, v, "Find in "+t.Title()+"?", "Find", redact.Mongo(op.Text), run)
		return
	}
	run()
}

func (t *CollectionTab) runFind(f findArgs, op mongosh.Op) {
	d := t.conn.Doc()
	if d == nil {
		t.loadErr = t.conn.Config.Name + " is not connected."
		return
	}
	t.readGen++
	gen := t.readGen
	t.closeCursor()
	t.found, t.loading, t.fetchingAll, t.loadErr, t.total = f, true, false, "", -1
	cfg, database, coll := t.conn.Config, t.Database, t.Collection
	dataview.BackgroundResetOnPanic(t.a, func() {
		if gen == t.readGen {
			t.loading = false
		}
	}, func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		start := time.Now()
		res, err := d.Run(ctx, database, op)
		var docs []bson.Raw
		done := true
		if err == nil && res.Cursor == nil {
			err = errors.New("the find returned no documents to read")
		}
		if err == nil {
			docs, done, err = res.Cursor.Fetch(ctx, pageSize)
		}
		took := time.Since(start)
		t.a.RecordRun(cfg, audit.KindMongo, database, op.Text, int64(len(docs)), took, err)
		return func() {
			if gen != t.readGen {
				if res.Cursor != nil {
					go res.Cursor.Close(context.Background())
				}
				return
			}
			t.loading = false
			if err != nil {
				t.loadErr = err.Error()
				if res.Cursor != nil {
					go res.Cursor.Close(context.Background())
				}
				return
			}
			t.read, t.cursor, t.done, t.elapsed = docs, res.Cursor, done, took
			t.readVer++
			t.edits.clear()
			t.detail = -1
			t.view.reset()
			if done {
				t.total = int64(len(docs))
			} else {
				t.count(gen, d, database, coll, f.filter)
			}
		}
	})
}

// count counts the documents matching, for "200 of 1,842".
func (t *CollectionTab) count(gen int, d *db.Doc, database, coll string, filter bson.D) {
	t.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		if filter == nil {
			filter = bson.D{} // a nil bson.D in an any is no nil filter
		}
		n, err := d.Count(ctx, database, coll, filter)
		return func() {
			if err == nil && gen == t.readGen {
				t.total = n
			}
		}
	})
}

// LoadMore reads the next page of documents; not while the changes are
// being saved.
func (t *CollectionTab) LoadMore() { t.fetch(false) }

// FetchAll reads every document left, up to db.MaxRows.
func (t *CollectionTab) FetchAll() { t.fetch(true) }

// StopFetchAll ends Fetch All after the page being read.
func (t *CollectionTab) StopFetchAll() { t.fetchingAll = false }

func (t *CollectionTab) fetch(all bool) {
	if t.loading || t.done || t.cursor == nil || t.refused(t.savingWhy()) {
		return
	}
	t.loading, t.fetchingAll = true, all
	gen, cur := t.readGen, t.cursor
	dataview.BackgroundResetOnPanic(t.a, func() {
		if gen == t.readGen {
			t.loading, t.fetchingAll = false, false
		}
	}, func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		start := time.Now()
		docs, done, err := cur.Fetch(ctx, pageSize)
		took := time.Since(start)
		return func() {
			if gen != t.readGen {
				return
			}
			t.loading = false
			if err != nil {
				t.loadErr, t.fetchingAll = err.Error(), false
				return
			}
			t.read = append(t.read, docs...)
			t.readVer++
			t.done, t.elapsed = done, t.elapsed+took
			if done {
				t.total = int64(len(t.read))
			}
			if t.fetchingAll && !done && len(t.read) < db.MaxRows {
				t.fetch(true)
				return
			}
			t.fetchingAll = false
		}
	})
}

// shownDocs are the documents as shown: those read, as edited, then the
// new ones.
func (t *CollectionTab) shownDocs() []bson.Raw {
	key := [2]int{t.readVer, t.edits.gen}
	if t.shown != nil && key == t.shownKey {
		return t.shown
	}
	t.shownKey = key
	t.shown = make([]bson.Raw, 0, len(t.read)+len(t.edits.inserted))
	for i := range len(t.read) + len(t.edits.inserted) {
		t.shown = append(t.shown, t.edits.shown(t.read, i))
	}
	return t.shown
}
