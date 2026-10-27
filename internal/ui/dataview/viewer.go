package dataview

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/lists"
	"dgopher/internal/project"
	"dgopher/internal/safety"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// ViewerSource is where a Viewer's rows come from.
type ViewerSource struct {
	// Keys reports whether the rows' tab takes the shortcuts pressed;
	// nil for always.
	Keys     func() bool
	Conn     *connection.Conn
	Database string
	// Statement is the SQL of the rows: "SELECT * FROM <table>" for a table (filters append
	// WHERE / ORDER BY), the statement as run for a query result, with Wrap set (filters wrap
	// it). Args are the values of its parameters, bound again on every read.
	Statement string
	Args      []any
	Wrap      bool
	// Reads is set when the statement only reads: WHERE filter, Count and smart sort work.
	Reads bool
	// Table is the table the rows edit, with its columns once read; nil when the rows are not
	// one table's, and ReadOnly then says why.
	Table    *db.Object
	ReadOnly string
	// Session is the session to read and apply on, read on the main thread; nil until one is
	// open. AdoptSession keeps one the Viewer opened, and is called off the main thread.
	Session      func() *db.Session
	AdoptSession func(*db.Session)
	// CountOnSession runs Count, distinct values and grouping on Session rather than on another
	// connection, so that they see what the rows saw. CursorOpen then says which of the owner's
	// results still reads rows, which a statement on the session would end; "" when none.
	CountOnSession bool
	CursorOpen     func() string
	// SessionBusy says why the session cannot take a statement now, "" when it can; nil when
	// the session is the Viewer's alone.
	SessionBusy func() string
	// TxChanged reports the session's transaction state after a read or an apply, as SessionTx
	// reads it.
	TxChanged func(own, inside db.TxState)
	// TxOwner names the tab whose transaction the session's statements run
	// inside, on a connection every session shares, "" when unknown; nil
	// when no tab names it.
	TxOwner func() (name string, inside bool)
	// SessionReset, when set, is told that a statement found the session's connection lost and
	// made again (db.ErrSessionReset): what the session had set, as its schema, went with it.
	SessionReset func()
	// Rerun runs the statement again for a refresh of a statement that writes, through the
	// owner's safety review; nil for a table.
	Rerun func()
	// Bars draws the owner's bars between the toolbar and the pending changes.
	Bars func(c *ui.Context)
	// ShowDDL opens the table's definition from the cell menu.
	ShowDDL func()
}

// SessionTx reads a session's transaction, off the main thread: own is the
// one the session began; inside, one another session began on a pool of
// one connection (DuckDB, an in-memory SQLite), which its statements run
// in, but which only that session commits or rolls back.
func SessionTx(sess *db.Session) (own, inside db.TxState) {
	if own, inside, ok := SharedSessionTx(sess); ok {
		return own, inside
	}
	return sess.Tx(), db.TxNone
}

// Viewer shows the rows of one source: its grid, toolbar, bars and status
// line, and writes their edits.
type Viewer struct {
	a       Host
	source  ViewerSource
	cursor  *db.Cursor
	src     Source
	grid    *Grid
	loading bool
	// loadStarted is when the read of the first page began, which the
	// skeleton waits on.
	loadStarted time.Time
	done        bool
	err         string
	errHint     string // the hint of a connection error in err
	elapsed     time.Duration
	where       string // the filter applied
	whereIn     string // the filter being typed
	count       int64  // -1 until counted
	counting    bool
	// countStarted is when the count began; countStop ends it.
	countStarted time.Time
	countStop    context.CancelFunc
	applying     bool
	// sessionReads counts the reads of Count, distinct values and
	// grouping on the session; readCancel ends them.
	sessionReads int
	readCancel   context.CancelFunc
	// released is set once the owner is done with the Viewer: what its
	// reads return then is nobody's.
	released bool
	// typedWhere is the filter as last typed, and menuFilters those the
	// Filter menu added to it, by column; where joins them.
	typedWhere  string
	menuFilters []tableFilter
	// builder is the filter builder, while it shows; built are the
	// conditions it last applied, shown again while the filter typed is
	// theirs.
	builder *filterBuilder
	built   builtFilter
	// maskChosen are the columns whose values the user hid or showed in
	// this view, by name, over the project's choice and the settings'.
	maskChosen map[string]bool
	// refs are the foreign keys of other tables pointing at this one, read
	// before the first review that deletes rows.
	refs []db.Reference
	// refSelects read each of refs' tables' rows as its grid does
	// (TableSelectSQL), its XMLType columns as text and the like; read
	// when referencing rows are first shown.
	refSelects  []string
	refsLoading bool
	refsWaiting []func()
	cancel      context.CancelFunc // ends the cursor
	// applyCancel stops an apply when the owner closes.
	applyCancel context.CancelFunc
	// applyStopped is set by Stop: the apply stops before its next
	// change, and rolls back.
	applyStopped atomic.Bool
	// applyStarted is when the apply began.
	applyStarted time.Time
	// reloadAgain reads the rows once more after the load in progress.
	reloadAgain bool
	// afterApply runs instead of a read once the changes being applied
	// are written, for what asked to apply them first.
	afterApply func()
	// fetchingAll keeps reading pages until the last; allAfterRead starts
	// it once the read in progress ends.
	fetchingAll, allAfterRead bool
	// fetchStarted is when Fetch All began.
	fetchStarted time.Time
	// sortRead is the order the rows were read in, which a cancelled
	// server sort goes back to.
	sortRead  []ui.SortOrder
	chart     Chart
	showChart bool
	// chartAsked is how the chart is to draw the result once its columns
	// are read (ShowChart), nil for none.
	chartAsked *ChartSettings
	// truncated is set when the rows end before the last: a later
	// statement closed their cursor, or the rows reached db.MaxRows.
	truncated bool
	// afterRead runs once the read in progress ends, for what waited for
	// the rows read again.
	afterRead func()

	// columns and fks are the table's, nil until read.
	columns []db.Column
	fks     []db.ForeignKey
	// fkErr is the failed read of fks, "" when they were read.
	fkErr string
	// refPreviews are the rows foreign keys' cards read, by table and
	// value; refReading is set while one is read.
	refPreviews map[string]*refPreview
	refReading  bool
	// bound names, for each column of the rows, the table's column it
	// holds, "" for none; keyMissing says which key column the rows lack.
	bound      []string
	keyMissing string
}

func NewViewer(a Host, src ViewerSource) *Viewer {
	v := &Viewer{a: a, source: src, grid: NewGrid(), count: -1}
	cn := src.Conn
	v.grid.serverSort = src.Reads
	v.grid.edits = newPendingEdits()
	v.grid.menu = v.cellMenu
	// The rows of a statement that writes are not read again: the
	// grid filters and counts those read.
	if src.Reads {
		v.grid.filterSQL, v.grid.clearSQL = v.addFilter, v.clearFilters
		v.grid.valuesMenu = v.valuesMenu
		v.grid.columnInfo, v.grid.enumValues = v.columnInfo, v.enumValues
		v.grid.distinctOf = v.distinctValues
		v.grid.groupOnServer = v.groupOnServer
		v.grid.profileOnServer = v.profileOnServer
	}
	v.grid.Exported = func(detail string, rows int) {
		e := audit.Event{Kind: audit.KindExport, Rows: int64(rows), Detail: detail}
		if v.source.Wrap {
			e.Statement = v.source.Statement
		} else {
			e.Detail = v.source.Table.Schema + "." + v.source.Table.Name + " " + detail
		}
		v.a.Record(&v.source.Conn.Config, e)
	}
	r, ok := db.SQL(cn.Config.Engine)
	v.grid.noDefaultUpdate = ok && !r.UpdateSetsDefault()
	v.grid.SQLOpts = SQLOptionsFor(cn, "", "")
	if src.Table != nil {
		v.bindTable()
	}
	return v
}

// bindTable wires what needs the rows' table: its row colors, its SQL
// and the rows that refer to its rows.
func (v *Viewer) bindTable() {
	cn := v.source.Conn
	v.grid.referencesOf = v.referencingRows
	v.grid.hasRefPreview, v.grid.fillRefPreview = v.refKeyColumn, v.refPreviewCard
	v.grid.fillHeaderCard = v.headerColumnCard
	v.grid.colorRules = rowColorRules(cn.Project, v.historyKey())
	v.grid.colorsChanged = func(rules []colorRule) { saveRowColors(cn.Project, v.historyKey(), rules) }
	v.grid.SQLOpts = SQLOptionsFor(cn, v.source.Table.Schema, v.source.Table.Name)
}

// BindTable gives a query's rows the table they were found to be read
// from, with its columns and foreign keys, for them to edit as the
// table's, with its table tab's filters, row colors and virtual key.
func (v *Viewer) BindTable(obj db.Object, cols []db.Column, fks []db.ForeignKey) {
	v.source.Table, v.source.ReadOnly = &obj, ""
	v.bindTable()
	v.SetColumns(cols, fks)
}

// SetReadOnly says why a query's rows cannot be edited, as when their
// table is not found.
func (v *Viewer) SetReadOnly(why string) { v.source.ReadOnly = why }

// Unbind takes a query's rows from the table BindTable gave them, which
// they no longer edit, saying why.
func (v *Viewer) Unbind(why string) { v.source.Table, v.source.ReadOnly = nil, why }

// SetColumns gives the Viewer the table's columns and foreign keys once
// they are read.
func (v *Viewer) SetColumns(cols []db.Column, fks []db.ForeignKey) {
	v.columns, v.fks = cols, fks
	v.bindColumns()
}

// bindColumns finds the table's column each column of the rows holds, and
// the key's columns among them. A query's column binds only when its
// statement reads a column as it is, by its name or a *: one computed,
// renamed, or whose name another shares, cannot be edited.
func (v *Viewer) bindColumns() {
	v.bound, v.keyMissing = make([]string, len(v.src.Cols)), ""
	v.grid.keyCols, v.grid.readOnlyCols = map[int]bool{}, nil
	if v.columns == nil {
		return
	}
	names := map[string]int{}
	for _, c := range v.src.Cols {
		names[c.Name]++
	}
	var items []sqltext.SelectItem
	star := false
	if v.source.Wrap {
		items, _ = sqltext.SelectItems(v.source.Statement, safety.Dialect(v.source.Conn.Config.Engine))
		star = slices.ContainsFunc(items, func(it sqltext.SelectItem) bool { return it.Star })
	}
	for i, c := range v.src.Cols {
		why := ""
		switch it := v.selectItem(items, star, i); {
		case names[c.Name] > 1:
			why = "Another column of the result is also named " + c.Name + "."
		case !v.source.Wrap || it == nil && star:
			v.bound[i] = v.tableColumn(c.Name)
		case it == nil || it.Column == "":
			why = c.Name + " is computed by the statement."
		case !strings.EqualFold(it.Column, it.Name):
			why = c.Name + " is renamed from " + it.Column + "."
		default:
			v.bound[i] = v.tableColumn(it.Column)
		}
		if why == "" && v.bound[i] == "" {
			why = c.Name + " is not a column of " + v.source.Table.Name + "."
		}
		if why != "" && v.source.Wrap {
			if v.grid.readOnlyCols == nil {
				v.grid.readOnlyCols = map[int]string{}
			}
			v.grid.readOnlyCols[i] = why
		}
	}
	key, _, err := v.keyColumns()
	if err != nil {
		return
	}
	for i, name := range v.bound {
		if inStrings(key, name) {
			v.grid.keyCols[i] = true
		}
	}
	for _, k := range key {
		if v.source.Wrap && !slices.Contains(v.bound, k) {
			v.keyMissing = "The key column " + k + " is not among the result's columns."
			return
		}
	}
}

// selectItem is the item of the statement's list column i of the rows
// comes from: by place when no * moves the places, else the item named
// as the column; nil for a column a * gives, or none found.
func (v *Viewer) selectItem(items []sqltext.SelectItem, star bool, i int) *sqltext.SelectItem {
	if !star && len(items) == len(v.src.Cols) {
		return &items[i]
	}
	for j := range items {
		if !items[j].Star && strings.EqualFold(items[j].Name, v.src.Cols[i].Name) {
			return &items[j]
		}
	}
	return nil
}

// tableColumn is the table's column named name: by its exact name, else
// the one alone to match it but for case, as unquoted names do.
func (v *Viewer) tableColumn(name string) string {
	found := ""
	for _, c := range v.columns {
		if c.Name == name {
			return c.Name
		}
	}
	for _, c := range v.columns {
		if strings.EqualFold(c.Name, name) {
			if found != "" {
				return ""
			}
			found = c.Name
		}
	}
	return found
}

// checkFilter refuses a filter that is not one condition that reads: it
// is sent to the server as typed, inside a SELECT.
func (v *Viewer) checkFilter(w string) error {
	if strings.TrimSpace(w) == "" {
		return nil
	}
	d := safety.Dialect(v.source.Conn.Config.Engine)
	q := safety.PolicyText(v.source.Conn.Config.Engine, "SELECT * FROM t WHERE "+w)
	if len(sqltext.SplitWith(q, d, sqltext.SplitOptions{Mode: sqltext.SemicolonOnly})) != 1 {
		return errors.New("the filter is one condition, without ';'")
	}
	if a := sqltext.ClassifyExecutable("SELECT * FROM t WHERE "+w, d); a.Class != sqltext.Read {
		return errors.New("the filter must only read")
	}
	// What the connection's policy refuses, as leaving read-only mode.
	cfg := &v.source.Conn.Config
	if verdict := safety.ReviewSQL(cfg, safety.Analyze(cfg, []string{"SELECT * FROM t WHERE " + w})); verdict.Blocked != "" {
		return errors.New(verdict.Blocked)
	}
	return nil
}

// RequestReload reads the rows again, once the user says what becomes of
// the pending changes, if any.
func (v *Viewer) RequestReload() {
	if v.applying {
		// The edits are being written: read the rows once they are.
		v.reloadAgain = true
		return
	}
	v.CheckPending(v.refresh, nil)
}

// refresh reads the rows again; a statement that writes runs again only
// through its owner, which asks first.
func (v *Viewer) refresh() {
	if !v.source.Reads && v.source.Rerun != nil {
		v.source.Rerun()
		return
	}
	v.reload()
}

// CheckPending runs then once no change is pending: as DBeaver does, it
// asks first whether to apply the changes, then runs after they are
// written, or to discard them; cancel, if set, undoes what asked. then
// must read the rows again: the rows shown hold the values from before
// the changes. While changes are being written it asks nothing: what
// asked is undone, or dropped, and what runs after the apply stays.
func (v *Viewer) CheckPending(then, cancel func()) {
	if v.applying {
		if cancel != nil {
			cancel()
		}
		return
	}
	n := v.grid.edits.count()
	if n == 0 {
		then()
		return
	}
	if cancel == nil {
		cancel = func() {}
	}
	v.a.AskPending(n, func() {
		v.afterApply = then
		v.reviewChanges()
	}, func() {
		v.discard()
		then()
	}, cancel)
}

// Discard drops the pending changes, as a disconnect does.
func (v *Viewer) Discard() { v.discard() }

// discard drops the pending changes; undo brings them back until the
// rows are read again.
func (v *Viewer) discard() {
	v.grid.checkpoint()
	v.grid.edits.clear()
	v.grid.editing, v.grid.orderKey = nil, ""
}

// FetchAll reads every row left, a page at a time; with changes pending,
// once they are applied or discarded and the rows read again.
func (v *Viewer) FetchAll() {
	if v.grid.edits.count() == 0 {
		v.fetchingAll, v.fetchStarted = true, time.Now()
		v.fetchMore()
		return
	}
	v.CheckPending(func() {
		v.allAfterRead = true
		v.reload()
	}, nil)
}

// stopFetchAll ends Fetch All once the page on its way is read: the rows
// stay, and the cursor reads on when asked. Its context is the rows'
// own, which ending would end the query, and on PostgreSQL the
// transaction it runs in.
func (v *Viewer) stopFetchAll() { v.fetchingAll, v.allAfterRead = false, false }

// countStopRefusal says why a count cannot be stopped, "" when it can: on
// DuckDB's one connection, ending its statement aborts the transaction
// open there.
func (v *Viewer) countStopRefusal() string {
	if r, ok := db.SQL(v.source.Conn.Config.Engine); !ok || !r.InterruptAbortsTransaction() || v.source.Conn.DB() == nil {
		return ""
	}
	if state, _ := v.source.Conn.DB().SharedTx(); state != db.TxNone {
		return "Stopping the count would abort the transaction open on " + v.source.Conn.Config.Name + "."
	}
	return ""
}

// dialect is the engine's, which the pool's is: read so, it holds while
// the connection is closed, as under an editor left unused.
func (v *Viewer) dialect() db.Dialect { return db.DialectOf(v.source.Conn.Config.Engine) }

func (v *Viewer) qualified() string {
	return db.QualifiedName(v.dialect(), v.source.Table.Schema, v.source.Table.Name)
}

// from is what a query of the rows reads from: the table, or the
// statement as a subquery. A line ends the statement, which may end in a
// comment.
func (v *Viewer) from() string {
	if !v.source.Wrap {
		return v.qualified()
	}
	stmt := strings.TrimSpace(v.source.Statement)
	for strings.HasSuffix(stmt, ";") {
		stmt = strings.TrimSpace(strings.TrimSuffix(stmt, ";"))
	}
	return "(\n" + stmt + "\n) q"
}

// sessionBusy says why the session cannot take a statement now.
func (v *Viewer) sessionBusy() string {
	if v.sessionReads > 0 {
		return "A count of these rows is running on the editor's connection."
	}
	if v.source.SessionBusy == nil {
		return ""
	}
	return v.source.SessionBusy()
}

// readOnlyReason says why the rows cannot be edited, "" when they can.
func (v *Viewer) readOnlyReason() string {
	if v.source.Conn.Config.ReadOnly {
		return "The connection is read-only."
	}
	if ok, why := v.dialect().Editable(); !ok {
		return why
	}
	if v.source.Table == nil {
		return v.source.ReadOnly
	}
	if v.source.Table.Kind != db.KindTable {
		return "Views are not edited from the grid."
	}
	if v.columns == nil {
		return ReadingColumns
	}
	if _, _, err := v.keyColumns(); err != nil {
		return "This " + err.Error() + ", or define a virtual key from a column's menu"
	}
	return v.keyMissing
}

// selectSQL is the query of the rows: a table's rows under the filter and
// order, a query's as run, or wrapped when filtered or sorted.
func (v *Viewer) selectSQL() string {
	var order []string
	for _, k := range sortKeys(v.grid.sort, len(v.src.Cols)) {
		idx, _ := strconv.Atoi(k.Column)
		if v.sharedName(idx) {
			continue // the server could not tell which column
		}
		o := v.dialect().Quote(v.src.Cols[idx].Name)
		if k.Descending {
			o += " DESC"
		}
		order = append(order, o)
	}
	w := strings.TrimSpace(v.where)
	if v.source.Wrap && (w == "" && len(order) == 0 || !v.source.Reads) {
		return v.source.Statement
	}
	q := v.source.Statement
	if v.source.Wrap {
		q = "SELECT * FROM " + v.from()
	}
	q += whereClause(w)
	r, isSQL := db.SQL(v.source.Conn.Config.Engine)
	if len(order) > 0 {
		q += " ORDER BY " + strings.Join(order, ", ")
	} else if key, _, err := v.keyColumns(); err == nil && !v.source.Wrap && (!isSQL || r.StableOrderByKey()) {
		// A stable order, so that pages and edits find the same rows.
		q += " ORDER BY " + db.QuoteNames(v.dialect(), key)
	}
	return q
}

// sharedName reports whether another column of the rows has the name of
// column i, which a query of the rows cannot then name.
func (v *Viewer) sharedName(i int) bool {
	for j, c := range v.src.Cols {
		if j != i && c.Name == v.src.Cols[i].Name {
			return true
		}
	}
	return false
}

// reload reads the first page of rows anew.
func (v *Viewer) reload() {
	if v.loading {
		v.reloadAgain = true
		return
	}
	if err := v.checkFilter(v.where); err != nil {
		v.err, v.errHint = "Filter: "+err.Error(), ""
		v.allAfterRead = false
		v.runAfterRead()
		return
	}
	if why := v.sessionBusy(); why != "" {
		v.a.ShowError("The rows were not read again", why)
		v.allAfterRead = false
		v.runAfterRead()
		return
	}
	v.loading, v.err, v.errHint, v.loadStarted = true, "", "", time.Now()
	v.fetchingAll = false
	v.sortRead = v.grid.sort
	query := v.selectSQL()
	args := v.source.Args
	pageSize := v.a.Settings().PageSize
	closeOld := v.closer()
	ctx, cancel := context.WithCancel(context.Background())
	v.cancel = cancel
	sess, pool, database, cfg := v.source.Session(), v.source.Conn.DB(), v.source.Database, v.source.Conn.Config
	go func() {
		defer RecoverBackground(v.a.Post, v.a.ShowReport, func() { v.loading = false })
		closeOld()
		start := time.Now()
		var err error
		if sess == nil {
			if sess, err = connection.OpenSession(ctx, pool, database); err == nil {
				v.source.AdoptSession(sess)
			}
		}
		var c *db.Cursor
		var rows [][]any
		if err == nil {
			c, err = sess.Query(ctx, query, args...)
		}
		if err == nil {
			rows, err = c.Fetch(pageSize)
		}
		elapsed := time.Since(start)
		v.a.RecordRun(cfg, audit.KindStatement, database, query, int64(len(rows)), elapsed, err)
		var own, inside db.TxState
		if sess != nil {
			own, inside = SessionTx(sess)
		}
		v.a.Post(func() {
			v.loading, v.elapsed = false, elapsed
			v.source.TxChanged(own, inside)
			v.noteReset(err)
			if v.reloadAgain {
				v.reloadAgain = false
				defer v.reload()
			} else {
				defer v.runAfterRead()
			}
			if err != nil {
				v.err, v.errHint = err.Error(), db.ClassifyConnectError(err).Hint()
				v.allAfterRead = false
				return
			}
			// The same rows read again: their order and their hidden and
			// pinned columns stay.
			sortBefore, hidden, pinned := v.grid.sort, v.grid.hidden, v.grid.pinned
			kept := v.keepSelection()
			v.grid.reset()
			v.grid.sort, v.grid.hidden, v.grid.pinned = sortBefore, hidden, pinned
			v.grid.edits.clear()
			v.cursor, v.src.Cols, v.src.Rows, v.done, v.truncated = c, c.Columns, rows, c.Done(), false
			v.bindColumns()
			v.restoreSelection(kept)
			if v.allAfterRead {
				v.allAfterRead = false
				v.fetchingAll, v.fetchStarted = true, time.Now()
				v.fetchMore()
			}
		})
	}()
}

// noteReset tells the owner when err is a statement's refusal to run on
// the session's connection made again.
func (v *Viewer) noteReset(err error) {
	if errors.Is(err, db.ErrSessionReset) && v.source.SessionReset != nil {
		v.source.SessionReset()
	}
}

// runAfterRead runs what waited for the rows to be read again.
func (v *Viewer) runAfterRead() {
	if then := v.afterRead; then != nil {
		v.afterRead = nil
		then()
	}
}

// Adopt shows the first page of rows its owner already read from a
// cursor, which later pages come from; stop ends the cursor's context.
func (v *Viewer) Adopt(c *db.Cursor, rows [][]any, done bool, elapsed time.Duration, stop context.CancelFunc) {
	v.cursor, v.cancel = c, stop
	v.src.Cols, v.src.Rows, v.done, v.elapsed = c.Columns, rows, done, elapsed
	v.sortRead = v.grid.sort
	v.bindColumns()
}

// CutShort ends the rows where they are: a later statement of the session
// closes, or closed, their cursor. The function it returns closes the
// cursor, off the main thread, before its context ends: on MySQL, the end
// of a context kills what its connection runs.
func (v *Viewer) CutShort() (closeCursor func()) {
	if !v.done {
		v.done, v.truncated = true, true
	}
	return v.closer()
}

// Release stops the Viewer's work for an owner done with it: an apply on
// its way, and the cursor's reads, which the function it returns ends off
// the main thread.
func (v *Viewer) Release() (closeCursor func()) {
	v.released = true
	if v.applyCancel != nil {
		v.applyCancel()
	}
	return v.closer()
}

// closer closes the cursor, then ends its context.
func (v *Viewer) closer() func() {
	var cursors []*db.Cursor
	if v.cursor != nil {
		cursors = append(cursors, v.cursor)
	}
	cancel, readCancel := v.cancel, v.readCancel
	return func() {
		if readCancel != nil {
			readCancel()
		}
		connection.CloseThenCancel(cursors, cancel)
	}
}

// HoldsCursor reports rows still to read from the server, which hold the
// session: a statement on it would end them.
func (v *Viewer) HoldsCursor() bool { return v.cursor != nil && v.cursor.HoldsConnection() }

// Counting reports a count, distinct values or grouping reading on the
// session, which a statement cancelled there would abort a transaction of.
func (v *Viewer) Counting() bool { return v.sessionReads > 0 }

// Busy reports a read or an apply on its way.
func (v *Viewer) Busy() bool { return v.loading || v.applying || v.sessionReads > 0 }

// Applying reports changes being written.
func (v *Viewer) Applying() bool { return v.applying }

// PendingCount is the number of changes not applied yet.
func (v *Viewer) PendingCount() int { return v.grid.edits.count() }

// TableName names the table the rows edit, "" when none.
func (v *Viewer) TableName() string {
	if v.source.Table == nil {
		return ""
	}
	return v.source.Table.Name
}

// Rows are the rows read.
func (v *Viewer) Rows() [][]any { return v.src.Rows }

// ShowChart shows the rows as a chart drawn as settings say, as far as
// the columns allow.
func (v *Viewer) ShowChart(settings ChartSettings) {
	v.showChart, v.chartAsked = true, &settings
}

// Columns are the columns of the rows.
func (v *Viewer) Columns() []db.ColumnInfo { return v.src.Cols }

// Done reports whether every row was read, or the rows were cut short.
func (v *Viewer) Done() bool { return v.done }

// The reasons the rows cannot be edited for a moment. The status line
// leaves them out, as most pass before they could be read; the edit
// buttons' tips say them.
const (
	ReadingColumns  = "Reading the table's columns…"
	readingRows     = "Reading the rows…"
	applyingChanges = "Applying the changes…"
)

func passing(reason string) bool {
	return reason == ReadingColumns || reason == readingRows || reason == applyingChanges
}

// ReadOnly says why the rows cannot be edited now, "" when they can.
func (v *Viewer) ReadOnly() string { return v.grid.readOnly }

func (v *Viewer) fetchMore() {
	// New rows are numbered after the rows read: no page may come between
	// while they or an edit are pending.
	if v.released || v.done || v.loading || v.cursor == nil || len(v.src.Rows) >= db.MaxRows || v.grid.edits.count() > 0 || v.grid.editing != nil {
		v.fetchingAll = false
		return
	}
	v.loading = true
	c := v.cursor
	n := min(v.a.Settings().PageSize, db.MaxRows-len(v.src.Rows))
	go func() {
		defer RecoverBackground(v.a.Post, v.a.ShowReport, func() { v.loading = false })
		rows, err := c.Fetch(n)
		done := c.Done()
		v.a.Post(func() {
			v.loading = false
			if c != v.cursor {
				return
			}
			v.src.Rows = append(v.src.Rows, rows...)
			v.done = done || len(v.src.Rows) >= db.MaxRows
			v.truncated = !done && v.done
			switch {
			case errors.Is(err, db.ErrClosedEarly):
				v.done, v.truncated, v.fetchingAll = true, true, false
			case err != nil:
				v.err, v.errHint = err.Error(), db.ClassifyConnectError(err).Hint()
				v.fetchingAll = false
			}
			if v.fetchingAll {
				v.fetchMore()
			}
		})
	}()
}

func (v *Viewer) countRows() {
	if err := v.checkFilter(v.where); err != nil {
		v.err, v.errHint = "Filter: "+err.Error(), ""
		return
	}
	query := "SELECT COUNT(*) FROM " + v.from() + whereClause(v.where)
	v.counting, v.countStarted = true, time.Now()
	v.countStop = v.queryRows(query, v.source.Args, 1, func(_ []string, rows [][]any, err error) {
		v.counting, v.countStop = false, nil
		if errors.Is(err, context.Canceled) {
			return // stopped: Count is offered again
		}
		if err == nil && (len(rows) != 1 || len(rows[0]) != 1) {
			err = errors.New("the count returned no value")
		}
		if err != nil {
			v.a.ShowError("Could not count the rows", err.Error())
			return
		}
		v.count = 0
		fmt.Sscan(db.Display(rows[0][0]), &v.count)
	})
}

// changeStatements turns the pending edits into the database's changes,
// and gives the same statements as they show and are audited: masked
// columns' values as MaskedText, as the grid shows them.
func (v *Viewer) changeStatements() (stmts, shown []db.Statement, err error) {
	key, _, err := v.keyColumns()
	if err != nil {
		return nil, nil, err
	}
	colIndex := map[string]int{}
	for i, name := range v.bound {
		if name != "" && v.grid.readOnlyCols[i] == "" {
			colIndex[name] = i
		}
	}
	for _, k := range key {
		if _, ok := colIndex[k]; !ok {
			return nil, nil, fmt.Errorf("the key column %s is not among the rows read: refresh the table", k)
		}
	}
	// A column the table does not have takes no value: an added or
	// duplicated row may hold one.
	editable := func(col int) bool { return col < len(v.bound) && v.bound[col] != "" && v.grid.readOnlyCols[col] == "" }
	keyValues := func(row int) []any {
		return lists.Map(key, func(k string) any { return v.src.Rows[row][colIndex[k]] })
	}
	edits := v.grid.edits
	var changes []db.Change
	for _, r := range slices.Sorted(maps.Keys(edits.updates)) {
		if edits.deleted[r] || len(edits.updates[r]) == 0 {
			continue
		}
		values := map[string]any{}
		for col, value := range edits.updates[r] {
			if editable(col) {
				values[v.bound[col]] = value
			}
		}
		if len(values) == 0 {
			continue
		}
		changes = append(changes, db.Change{Kind: db.ChangeUpdate, Key: keyValues(r), Values: values})
	}
	for _, row := range edits.inserted {
		values := map[string]any{}
		for col, value := range row {
			if value != unset && editable(col) {
				values[v.bound[col]] = value
			}
		}
		changes = append(changes, db.Change{Kind: db.ChangeInsert, Values: values})
	}
	for _, r := range slices.Sorted(maps.Keys(edits.deleted)) {
		changes = append(changes, db.Change{Kind: db.ChangeDelete, Key: keyValues(r)})
	}
	target := &db.EditTarget{Dialect: v.dialect(), Schema: v.source.Table.Schema, Table: v.source.Table.Name, Columns: v.columns, Key: key}
	if stmts, err = target.Statements(changes); err != nil {
		return nil, nil, err
	}
	masked := map[string]bool{}
	for col, name := range v.bound {
		if name != "" && v.grid.isMasked(col) {
			masked[name] = true
		}
	}
	if len(masked) == 0 {
		return stmts, stmts, nil
	}
	mask := func(name string, value any) any {
		if value == nil || !masked[name] {
			return value
		}
		return MaskedText
	}
	hidden := lists.Map(changes, func(ch db.Change) db.Change {
		values := make(map[string]any, len(ch.Values))
		for name, value := range ch.Values {
			values[name] = mask(name, value)
		}
		ch.Values = values
		ch.Key = slices.Clone(ch.Key)
		for i, k := range key {
			if i < len(ch.Key) {
				ch.Key[i] = mask(k, ch.Key[i])
			}
		}
		return ch
	})
	if shown, err = target.Statements(hidden); err != nil {
		return nil, nil, err
	}
	return stmts, shown, nil
}

// Review shows the statements of the pending edits before applying them;
// once they are applied and the rows read again, then runs, if set.
func (v *Viewer) Review(then func()) {
	v.afterApply = nil
	if then != nil {
		v.afterApply = func() {
			v.afterRead = then
			v.reload()
		}
	}
	v.reviewChanges()
}

// reviewChanges is Review, keeping what runs after the apply.
func (v *Viewer) reviewChanges() {
	if v.grid.edits.count() == 0 || v.applying {
		return
	}
	if why := v.readOnlyReason(); why != "" {
		v.a.ShowError("These rows cannot be changed", why)
		return
	}
	if why := v.sessionBusy(); why != "" {
		v.a.ShowError("The changes cannot be applied now", why)
		return
	}
	if len(v.grid.edits.deleted) > 0 && v.refs == nil {
		// Read first which tables point at this one: deleting rows they
		// reference fails, or cascades to them.
		v.loadRefs(v.reviewChanges)
		return
	}
	stmts, shown, err := v.changeStatements()
	if err != nil {
		v.a.ShowError("Could not prepare the changes", err.Error())
		return
	}
	var previews []string
	sqls := make([]string, len(stmts))
	for i, s := range stmts {
		previews = append(previews, shown[i].Preview())
		sqls[i] = s.SQL
	}
	verdict := safety.ReviewSQL(&v.source.Conn.Config, safety.Analyze(&v.source.Conn.Config, sqls))
	if verdict.Blocked != "" {
		v.a.RecordBlocked(v.source.Conn, verdict.Blocked, strings.Join(previews, "\n"))
		v.a.ShowError("Not allowed", verdict.Blocked)
		return
	}
	verdict.Reasons = append([]string{widgets.OneRowEach}, verdict.Reasons...)
	if _, virtual, _ := v.keyColumns(); virtual {
		verdict.Reasons = append(verdict.Reasons, "Rows are found by a virtual key, which the database does not hold unique: a statement that matches other rows too is rolled back.")
	}
	if len(v.grid.edits.deleted) > 0 {
		for _, r := range v.refs {
			verdict.Reasons = append(verdict.Reasons, fmt.Sprintf("%s.%s points at these rows (%s → %s): the database refuses the delete, or deletes or changes its rows too, as its foreign key says.",
				r.Schema, r.Table, strings.Join(r.Columns, ", "), strings.Join(r.RefColumns, ", ")))
		}
	}
	verdict.ManyRows(&v.source.Conn.Config, db.ExistingRowChanges(stmts), v.a.Settings().ChangeLimit)
	title := fmt.Sprintf("Apply %d change%s to %s?", len(stmts), widgets.Plural(len(stmts)), v.source.Table.Name)
	v.a.AskConfirm(v.source.Conn, verdict, title, "Apply", strings.Join(previews, "\n"), func() { v.apply(stmts, shown) })
}

// applyStep, set by tests, runs in the apply before each statement
// ("statement", its index) and before the commit ("finish").
var applyStep func(stage string, i int)

// stopApply stops the apply before its next change. A statement sent
// runs to its end: cut short inside the user's transaction, SQLite would
// roll back the whole transaction, and MySQL might drop the connection.
func (v *Viewer) stopApply() { v.applyStopped.Store(true) }

// errApplyStopped is an apply stopped by the user.
var errApplyStopped = errors.New("stopped")

// apply runs the statements in one transaction, and shows and audits them
// as shown has them. On a connection in manual commit, the transaction
// stays open for the user to commit.
func (v *Viewer) apply(stmts, shown []db.Statement) {
	if why := v.sessionBusy(); why != "" {
		v.a.ShowError("The changes were not applied", why)
		return
	}
	v.applying = true
	manual := v.source.Conn.Config.ManualCommit()
	sess, pool, database, cfg := v.source.Session(), v.source.Conn.DB(), v.source.Database, v.source.Conn.Config
	engine := cfg.Engine.Label()
	owner := ""
	if v.source.TxOwner != nil {
		if name, inside := v.source.TxOwner(); inside {
			owner = name // not one kept from an earlier transaction
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	v.applyCancel = cancel
	v.applyStopped.Store(false)
	v.applyStarted = time.Now()
	job := fmt.Sprintf("Applying %s to %s on %s: stopping it rolls them back, and nothing of it stays.", widgets.Count(len(stmts), "change"), v.TableName(), cfg.Name)
	finishJob := v.a.StartJob(v.source.Conn, job, v.stopApply)
	go func() {
		defer RecoverBackground(v.a.Post, v.a.ShowReport, func() { v.applying = false })
		defer finishJob()
		defer cancel()
		var err error
		if sess == nil {
			if sess, err = connection.OpenSession(ctx, pool, database); err != nil {
				v.a.Post(func() { v.applying = false; v.a.ShowError("Could not apply the changes", err.Error()) })
				return
			}
			v.source.AdoptSession(sess)
		}
		// MySQL's autocommit off, set on the session, holds its writes for
		// COMMIT as manual commit does: the edits' transaction stays open.
		manual := manual || sess.AutocommitOff()
		// In a transaction of our own, or, inside the user's, behind a
		// savepoint: a failed batch then leaves nothing behind either way.
		const savepoint = "dgopher_edits"
		ownTx := sess.Tx() == db.TxNone
		began, saved := false, false
		if ownTx {
			if err = sess.Begin(ctx); err == nil {
				began = true
			}
		} else if !cfg.Engine.Savepoints() {
			// Nothing is sent: SAVEPOINT would fail, and a failed change
			// would abort the transaction it ran in.
			_, inside := SessionTx(sess)
			err = errors.New(noSavepointsText(engine, owner, inside != db.TxNone))
		} else if _, err = sess.Exec(ctx, "SAVEPOINT "+savepoint); err == nil {
			saved = true
		} else {
			err = fmt.Errorf("a transaction is open and %s cannot nest the changes in it (%w): commit or roll it back first", engine, err)
		}
		for i := 0; err == nil && i < len(stmts); i++ {
			if applyStep != nil {
				applyStep("statement", i)
			}
			if v.applyStopped.Load() {
				err = errApplyStopped
				break
			}
			s := stmts[i]
			var n int64
			start := time.Now()
			n, err = sess.ExecChange(ctx, s.SQL, s.Args...)
			if err == nil && s.Want >= 0 && n >= 0 && n != s.Want {
				err = fmt.Errorf("%s\nchanged %d rows instead of %d: nothing was applied", shown[i].Preview(), n, s.Want)
			}
			v.a.RecordRun(cfg, audit.KindEdit, database, shown[i].Preview(), n, time.Since(start), err)
		}
		if applyStep != nil {
			applyStep("finish", len(stmts))
		}
		// Stopped, or the owner closing, after the last change: they roll
		// back, as before it. Past this, the end is not cut short.
		if err == nil && v.applyStopped.Load() {
			err = errApplyStopped
		}
		if err == nil {
			err = ctx.Err()
		}
		// A connection lost with the batch's transaction: the server rolled
		// it back. An undo would go to the connection made again and take
		// its one refusal (db.ErrSessionReset), which the user's next
		// statement must get instead.
		lost := errors.Is(err, db.ErrTxLost)
		// The rollback goes out on a context of its own: an owner that
		// closes stops the batch by ending ctx.
		undo, cancelUndo := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		// A COMMIT or RELEASE once sent is not cut short, by Stop, the owner
		// closing or a time limit: cut, its outcome would be unknown, and
		// no rollback would follow.
		finish := context.WithoutCancel(ctx)
		var undoErr error
		switch {
		case lost:
		case err != nil && began:
			undoErr = sess.Rollback(undo)
		case err != nil && saved:
			if _, undoErr = sess.Exec(undo, "ROLLBACK TO SAVEPOINT "+savepoint); undoErr == nil {
				_, undoErr = sess.Exec(undo, "RELEASE SAVEPOINT "+savepoint)
			}
		case saved:
			_, err = sess.Exec(finish, "RELEASE SAVEPOINT "+savepoint)
		case began && !manual:
			err = sess.Commit(finish)
		}
		cancelUndo()
		resetBy := errors.Join(err, undoErr)
		if errors.Is(undoErr, db.ErrTxLost) || errors.Is(undoErr, db.ErrSessionReset) {
			// The server rolled back as the connection went: the undo,
			// refused on the connection made again, had nothing left.
			undoErr = nil
		}
		if undoErr != nil {
			err = fmt.Errorf("%w\nThe rollback failed too: %v", err, undoErr)
		}
		outcome := audit.Event{Kind: audit.KindEdit, Database: database, Detail: fmt.Sprintf("%d grid changes ", len(stmts))}
		switch {
		case undoErr != nil:
			outcome.Detail += "failed, and so did their rollback"
			outcome.Err = err
		case errors.Is(err, errApplyStopped):
			outcome.Detail += "stopped, rolled back"
		case err != nil:
			outcome.Detail += "rolled back"
			outcome.Err = err
		case began && !manual:
			outcome.Detail += "committed"
		default:
			outcome.Detail += "applied in an open transaction, not committed yet"
		}
		v.a.Record(&cfg, outcome)
		own, inside := SessionTx(sess)
		v.a.Post(func() {
			v.applying = false
			v.source.TxChanged(own, inside)
			// A lost connection took what the session had set with it, as
			// a refusal says (noteReset); the owner reads its schema
			// without taking the refusal.
			if (lost || errors.Is(resetBy, db.ErrSessionReset)) && v.source.SessionReset != nil {
				v.source.SessionReset()
			}
			then := v.afterApply
			v.afterApply = nil
			if err != nil {
				if errors.Is(err, errApplyStopped) && undoErr == nil {
					v.a.Toast(fmt.Sprintf("Stopped: none of the %s were applied (rolled back).", widgets.Count(len(stmts), "change")), "", nil)
				} else {
					v.a.ShowReport(widgets.ErrorReportOf("The changes were not applied", err))
				}
				if v.reloadAgain {
					v.reloadAgain = false
					v.RequestReload()
				}
				return
			}
			v.grid.edits.clear()
			if then == nil {
				then = v.reload
			}
			then()
		})
	}()
}

// View draws the rows with their toolbar, bars and status line.
func (v *Viewer) View(c *ui.Context) {
	a := v.a
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	readOnly := v.readOnlyReason()
	if v.loading && readOnly == "" {
		// A page landing would number a row added meanwhile as a read
		// one, and a reload drops the edits.
		readOnly = readingRows
	}
	v.grid.readOnly = readOnly
	v.maskColumns()
	if v.applying {
		v.grid.readOnly = applyingChanges // no edit may slip in between
	}
	// Released, as a result a run replaces shown until the new one comes,
	// it takes no key.
	keys := !v.released && (v.source.Keys == nil || v.source.Keys())
	v.grid.keys = keys
	pressed := func(id string) bool { return keys && keymap.Pressed(c, id) }
	listed := func(id string) bool { return keymap.ListPressed(c, &v.grid.List, id) }
	if pressed(keymap.Apply) {
		v.Review(nil)
	}
	switch {
	case pressed(keymap.Discard) && v.grid.edits.count() > 0:
		// DBeaver's Cancel: the pending changes go; without any, a refresh.
		v.discard()
	case pressed(keymap.Discard) || pressed(keymap.Refresh):
		v.RequestReload()
	case listed(keymap.FetchNext) && !v.done:
		// A page may not come between pending changes and their rows:
		// asked, the rows are read again after the changes go.
		if v.grid.edits.count() == 0 {
			v.fetchMore()
		} else {
			v.CheckPending(v.reload, nil)
		}
	case listed(keymap.FetchAll) && !v.done:
		v.FetchAll()
	case listed(keymap.FollowKey):
		v.followKey()
	case listed(keymap.PickReference):
		v.pickRef()
	case pressed(keymap.ToggleChart):
		v.showChart = !v.showChart
	}
	widgets.Bar(c, "Data", func() {
		divider := func() { ui.Divider(c).Margin(0, 4) }
		// A ButtonBase looks the same disabled: dimmed, it shows it.
		disable := func(b ui.Element, d bool) ui.Element {
			if d {
				b.Opacity(0.4)
			}
			return b.Disabled(d)
		}
		refreshTip := keymap.Hint("Read the rows again", keymap.Discard)
		if !v.source.Reads {
			refreshTip = keymap.Hint("Run the statement again, once confirmed", keymap.Discard)
		}
		// Refresh reads the rows again under the WHERE: it ends its box.
		refresh := func() {
			if widgets.IconButton(c, widgets.IconRefresh, "Refresh").Tooltip(widgets.KeyLabel(refreshTip)).Clicked() {
				v.RequestReload()
			}
		}
		// ⌘L focuses the WHERE, then, pressed there, the quick filter.
		whereFocused := false
		box := ui.Row(c).Gap(6).Grow(1).Padding(0, 6).Radius(th.Radius).Background(th.Surface).Border(1, th.Border)
		box.Children(func() {
			ui.Icon(c, widgets.IconFilter).TextColor(pal.Muted).FontSize(13)
			placeholder := writesNotReread
			if v.source.Reads {
				// The keyword the filter goes after, as the statement would
				// read, and an example in the place of the condition.
				// Faint while there is no filter, so that an empty box does
				// not read as one applied.
				kw := pal.Syntax[sqltext.Keyword].Alpha(0.4)
				if v.whereIn != "" || v.where != "" {
					kw = kw.Alpha(0.8)
				}
				ui.Text(c, "WHERE").Font(widgets.MonoFont).FontSize(12.5).TextColor(kw)
				placeholder = "e.g. status = 'active'"
			}
			cb := ui.ComboboxBase(c, &v.whereIn)
			in := cb.Input.Font(widgets.MonoFont).FontSize(12.5).Placeholder(placeholder).Grow(1).Padding(4, 2).Label("Filter")
			if !v.source.Reads {
				in.Disabled(true).Tooltip(writesNotReread)
				refresh()
				return
			}
			suggestions := filterSuggestions(v.whereIn, v.src.Cols, filterHistory(v.source.Conn.Project, v.historyKey()))
			cb.Popup(func(panel ui.Element) {
				panel.Padding(4).Background(th.Background).Border(1, th.Border).Radius(6).MaxHeight(260)
				for _, s := range suggestions {
					item := cb.Item(s).Padding(4, 8).Radius(4)
					if item.Highlighted() {
						item.Background(widgets.ChosenRow(c))
					}
					item.Children(func() { ui.Text(c, s).Font(widgets.MonoFont).FontSize(12.5).SingleLine() })
				}
			})
			if s, ok := cb.Chosen(); ok {
				v.whereIn = completeFilter(v.whereIn, s)
			}
			if in.Submitted() {
				if v.historyKey() != "" {
					rememberFilter(v.source.Conn.Project, v.historyKey(), v.whereIn)
				}
				// Typed, the filter is the user's: the menu's are in it.
				typed := v.whereIn
				v.setFilter(func() {
					v.where, v.typedWhere, v.menuFilters = typed, typed, nil
					v.whereIn, v.count = typed, -1
				})
			}
			whereFocused = in.Focused()
			if whereFocused {
				box.Border(1, th.Accent)
			}
			if want := a.FocusWant(); *want == "filter" && keys && !whereFocused && in.Focus().Focused() {
				*want = ""
			}
			if v.where != "" && widgets.Appear(widgets.IconButton(c.Key("clear-filter"), widgets.IconX, "Clear the filter")).Clicked() {
				v.clearFilters(-1)
			}
			build := widgets.ToggleIconButton(c, widgets.IconListFilter, "Build a filter from conditions", v.builder != nil)
			if disable(build, len(v.src.Cols) == 0).Clicked() {
				if v.builder == nil {
					v.builder = v.newFilterBuilder()
				} else {
					v.builder = nil
				}
			}
			refresh()
		})
		quick := widgets.SearchBox(c, &v.grid.Filter, "Filter rows", 170)
		quick.Tooltip("Filter the rows already read, without asking the server")
		if want := a.FocusWant(); *want == "filter" && keys && (!v.source.Reads || whereFocused) && quick.Focus().Focused() {
			*want = ""
		}
		// The edit buttons keep their place, disabled while the rows cannot
		// be edited, as while they are read: shown only once they could,
		// they would move the toolbar a moment after every run.
		divider()
		tip := func(hint string) string { return cmp.Or(readOnly, hint) }
		if disable(widgets.IconButton(c, widgets.IconPlus, "Add Row").Tooltip(tip(keymap.Hint("Add a row", keymap.AddRow))), readOnly != "").Clicked() {
			v.grid.addRow(&v.src)
		}
		noRow := readOnly != "" || v.grid.SelRow < 0
		if disable(widgets.IconButton(c, widgets.IconCopy, keymap.Hint("Duplicate the chosen row", keymap.DuplicateRow)).Tooltip(tip(keymap.Hint("Duplicate the chosen row", keymap.DuplicateRow))), noRow).Clicked() {
			if rows := v.grid.selectedRows(&v.src); len(rows) > 0 {
				v.grid.duplicateRow(&v.src, rows[0], v.grid.keyCols)
			}
		}
		if disable(widgets.IconButton(c, widgets.IconTrash, "Delete").Tooltip(tip(keymap.Hint("Mark the chosen rows for deletion", keymap.DeleteRows))), noRow).Clicked() {
			v.grid.deleteSelected(&v.src)
		}
		divider()
		v.presentationButtons(c)
		divider()
		if widgets.IconButton(c, widgets.IconDownload, "Export…").Clicked() {
			OpenExport(a, v.exportSource())
		}
		widgets.MenuOn(c, widgets.IconButton(c, widgets.IconCompare, "Compare with other rows"), v.compareMenu)
		divider()
		// The panels open at the right: their switch is there too.
		if widgets.ToggleIconButton(c, widgets.IconSidebar, keymap.Hint("Value viewer", keymap.ValuePanel), v.grid.ShowValue).Clicked() {
			v.grid.ShowValue = !v.grid.ShowValue
		}
	})
	if v.builder != nil && v.source.Reads && len(v.src.Cols) > 0 {
		v.builderView(c)
	}
	if v.source.Bars != nil {
		v.source.Bars(c)
	}
	if n := v.grid.edits.count(); n > 0 {
		widgets.AppearBar(ui.Row(c.Key("pending-changes")).Padding(6, 12).Gap(10).Background(pal.Modified).Children(func() {
			ui.Text(c, fmt.Sprintf("%d pending change%s", n, widgets.Plural(n))).Bold()
			ui.Text(c, "Nothing is written until you review and apply.").TextColor(pal.Muted).Grow(1)
			// The keys in the tooltips: labels short enough to scan.
			if ui.Button(c, "Undo").Disabled(len(v.grid.undoStack) == 0).Tooltip(keymap.Hint("Undo the last change", keymap.Undo)).Clicked() {
				v.grid.undo()
			}
			if ui.Button(c, "Discard All").Tooltip(keymap.Hint("Drop every pending change", keymap.Discard)).Clicked() {
				v.discard()
			}
			if v.applying && widgets.Lasted(c, v.applyStarted) &&
				widgets.Appear(ui.Button(c.Key("stop-apply"), "Stop")).Tooltip("Stop before the next change: every change rolls back").Clicked() {
				v.stopApply()
			}
			if ui.PrimaryButton(c, "Review and Apply").Tooltip(keymap.Hint("Review the statements, then write them", keymap.Apply)).Disabled(v.applying).Clicked() {
				v.Review(nil)
			}
		}))
	}
	// Without a grid drawn, a wish to focus it goes: left, another tab's
	// grid would take it later.
	noGrid := func() {
		if *a.FocusWant() == "results" && keys {
			*a.FocusWant() = ""
		}
	}
	if v.err != "" && v.src.Cols == nil {
		ui.Text(c, v.err).TextColor(th.Danger).Padding(12).Selectable()
		ui.Spacer(c)
		noGrid()
		return
	}
	if v.err != "" {
		// The rows below are those of before: the read that failed, as
		// with a mistyped filter, changed nothing.
		ui.Row(c).Padding(6, 12).Gap(10).Background(th.Danger.Alpha(0.12)).Children(func() {
			ui.Icon(c, widgets.IconAlert).TextColor(th.Danger).FontSize(14)
			ui.Text(c, "Could not read the rows: "+widgets.FirstLine(v.err)+". The rows shown are those read before.").Grow(1).Shrink(1).Selectable().Tooltip(v.err)
			if ui.Button(c, "Details").Clicked() {
				v.a.ShowReport(widgets.ErrorReport{Title: "Could not read the rows", Hint: v.errHint, Detail: v.err})
			}
		})
	}
	if v.src.Cols == nil {
		if v.loading {
			widgets.Skeleton(c, v.loadStarted)
		} else {
			ui.Spacer(c)
		}
		noGrid()
		return
	}
	// DBeaver's smart order: the server sorts while rows remain to be
	// read, or were left unread, the grid once all are. The rows of a
	// statement that writes are not read again.
	v.grid.serverSort = (!v.done || v.truncated) && v.source.Reads
	if v.showChart {
		if s := v.chartAsked; s != nil && len(v.src.Cols) > 0 {
			v.chart.Use(*s, &v.src)
			v.chartAsked = nil
		}
		ChartView(c, &v.chart, &v.src)
		noGrid()
	} else {
		v.grid.View(c, a, &v.src)
		if *a.FocusWant() == "results" && keys {
			if v.grid.SelRow < 0 && len(v.src.Rows) > 0 {
				v.grid.SelRow = 0
			}
			if v.grid.List.Focus(c) {
				*a.FocusWant() = ""
			}
		}
	}
	if v.grid.sortChanged {
		v.grid.sortChanged = false
		restore := func() {
			v.grid.sort = v.sortRead
			v.grid.orderKey = ""
		}
		switch {
		case !v.grid.serverSort:
		case v.sortsSharedName():
			restore()
			v.a.ShowError("Cannot sort on the server", "Another column of the result has the same name, which the server cannot tell apart. Read every row (Fetch All) to sort them here.")
		default:
			v.CheckPending(v.reload, restore)
		}
	}
	if !v.done && !v.loading {
		if _, last := v.grid.List.Visible(); last >= len(v.src.Rows)-50 {
			v.fetchMore()
		}
	}
	ui.Row(c).Padding(4, 10).Gap(10).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
		// One phrase: "500 of ≈2.5k rows loaded", the count or the
		// estimate of the table's rows its total.
		n := len(v.src.Rows)
		total := ""
		switch {
		case !v.source.Reads:
		case v.count >= 0:
			total = strconv.FormatInt(v.count, 10)
		case v.count < 0 && !v.counting && !v.source.Wrap && v.source.Table.Rows >= 0 && v.where == "":
			total = "≈" + widgets.HumanCount(v.source.Table.Rows)
		}
		status := widgets.Count(n, "row")
		if total != "" {
			status = fmt.Sprintf("%d of %s rows", n, total)
		}
		switch {
		case v.truncated && n < db.MaxRows:
			status += ": the next statement closed this result; run it alone to read every row"
		case v.truncated:
			status += " (limit reached: add a LIMIT or export)"
		case !v.done:
			status += " loaded"
		}
		if v.grid.Filter != "" {
			status = fmt.Sprintf("%d matching · %s", len(v.grid.Order), status)
		}
		// The phrase, then what more there is to do with the rows.
		ui.Row(c).Gap(6).Children(func() {
			muted := func(s string) ui.Element { return ui.Text(c, s).FontSize(12).TextColor(pal.Muted) }
			// The time ends the phrase, a word's space after it.
			ui.Row(c).Gap(4).Children(func() {
				muted(status)
				if !v.truncated {
					muted("in " + widgets.FormatDuration(v.elapsed)).Tooltip("The time the rows took to read")
				}
			})
			// One button for Fetch All, there while pages are read, turning
			// to Stop once fetching every row lasts.
			switch {
			case v.fetchingAll && widgets.Lasted(c, v.fetchStarted):
				if widgets.Appear(widgets.StatusButton(c.Key("fetch-all"), "Stop")).Tooltip("Stop after the page being read; Fetch All goes on from there").Clicked() {
					v.stopFetchAll()
				}
			case !v.done || v.fetchingAll:
				fetch := widgets.Appear(widgets.StatusButton(c.Key("fetch-all"), "Fetch All")).Tooltip(keymap.Hint("Fetch every row", keymap.FetchAll))
				if v.loading || v.fetchingAll {
					fetch.Disabled(true)
				} else if fetch.Clicked() {
					v.FetchAll()
				}
			}
			switch {
			// A query's result has no table whose rows a count would add to.
			case !v.source.Reads || v.count >= 0 || v.source.CountOnSession:
			case v.counting:
				// Count turns to Stop, in place, once the count lasts.
				if !widgets.Lasted(c, v.countStarted) {
					widgets.Appear(widgets.StatusButton(c.Key("count"), "Count")).Disabled(true)
					break
				}
				stop := widgets.Appear(widgets.StatusButton(c.Key("count"), "Stop"))
				if why := v.countStopRefusal(); why != "" {
					stop.Disabled(true).Opacity(0.5).Tooltip(why)
				} else if stop.Tooltip("Stop the count").Clicked() && v.countStop != nil {
					v.countStop()
				}
			default:
				if widgets.Appear(widgets.StatusButton(c.Key("count"), "Count")).Tooltip("Count every row on the server").Clicked() {
					v.countRows()
				}
			}
		})
		ui.Spacer(c)
		if stats := v.grid.SelectionStats(&v.src); stats != "" {
			ui.Text(c, stats).FontSize(12).TextColor(pal.Muted).Selectable()
		}
		if readOnly == "" {
			readOnly = v.grid.readOnlyCols[v.grid.selCol]
		}
		if readOnly != "" && !passing(readOnly) {
			ui.Icon(c, widgets.IconLock).TextColor(pal.Muted).FontSize(12)
			ui.Text(c, widgets.FirstLine(readOnly)).FontSize(12).TextColor(pal.Muted).SingleLine().Shrink(1).Tooltip(readOnly)
			if strings.Contains(readOnly, "virtual key") && ui.Link(c, "Define a Virtual Key…", "").FontSize(12).Clicked() {
				openKeyForm(a, v)
			}
		}
	})
}

// sortsSharedName reports whether the order chosen sorts by a column
// whose name another column shares.
func (v *Viewer) sortsSharedName() bool {
	for _, k := range sortKeys(v.grid.sort, len(v.src.Cols)) {
		if idx, _ := strconv.Atoi(k.Column); v.sharedName(idx) {
			return true
		}
	}
	return false
}

// writesNotReread says why the rows of a statement that writes are not
// filtered, counted or sorted on the server.
const writesNotReread = "The statement changes the database: its rows are not read again to filter, count or sort them"

// exportSource exports the rows under their filter and order.
func (v *Viewer) exportSource() ExportSource {
	sql := v.selectSQL()
	if v.checkFilter(v.where) != nil {
		sql = "" // the filter is refused: only the rows read can go
	}
	name, schema := "query", ""
	if !v.source.Wrap {
		name, schema = v.source.Table.Name, v.source.Table.Schema
	}
	src := ExportSource{Conn: v.source.Conn, Database: v.source.Database, Schema: schema, Name: name, SQL: sql, Args: v.source.Args,
		Cols: v.src.Cols, RowsRead: func() [][]any { return v.src.Rows }, Read: len(v.src.Rows)}
	if v.source.CountOnSession {
		src.OnSession = v.lendSession
	}
	return src
}

// lendSession lends the session to an export, on the terms Count takes
// it: once no result reads rows there and nothing else runs on it. The
// export holds it, as a count does, until it calls done.
func (v *Viewer) lendSession() (sess *db.Session, done func(), why string) {
	why = cmp.Or(v.source.CursorOpen(), v.sessionBusy())
	if sess = v.source.Session(); why != "" || sess == nil {
		return nil, nil, why
	}
	v.sessionReads++
	return sess, func() {
		own, inside := SessionTx(sess)
		v.a.Post(func() {
			v.sessionReads--
			if v.sessionReads == 0 {
				v.readCancel = nil
			}
			if !v.released {
				v.source.TxChanged(own, inside)
			}
		})
	}, ""
}

// cellMenu offers to choose a foreign key's value, to follow a foreign
// key from its cell, and to filter by a cell's value: what needs a
// table, only for rows of one, and what needs a value, only for rows
// read.
func (v *Viewer) cellMenu(m *ui.Menu, row, col int) {
	if v.source.Table == nil {
		return
	}
	if fk, ok := v.refOf(col); ok && v.grid.edits != nil && v.grid.columnReadOnly(col) == "" {
		m.Separator()
		if keymap.Item(m.Item("Choose from "+fk.RefTable+"…"), keymap.PickReference).Chosen() {
			v.openRefPicker(row, col, fk)
		}
	}
	if row >= len(v.src.Rows) {
		return
	}
	name := v.tableName(col)
	value := v.src.Rows[row][col]
	m.Separator()
	m.Submenu("Navigate", func(m *ui.Menu) {
		found := false
		for _, fk := range v.fks {
			if len(fk.Columns) == 1 && fk.Columns[0] == name && value != nil && len(fk.RefColumns) == 1 {
				found = true
				if m.Item("Go to Referenced Row in " + fk.RefTable).Chosen() {
					where := v.dialect().Quote(fk.RefColumns[0]) + " = " + db.Literal(v.source.Conn.Config.Engine, value)
					v.openRefWhere(fk, where)
				}
			}
		}
		if v.refs == nil {
			v.loadRefs(nil)
			m.Item("Reading the tables that refer here…").Disabled(true)
			return
		}
		for _, r := range v.refs {
			where, ok := v.referencingWhere(r, row)
			if !ok {
				continue
			}
			found = true
			if m.Item("Rows of " + r.Table + " Referring Here").Chosen() {
				v.openTableWhere(r.Schema, r.Table, where)
			}
		}
		if !found && v.fkErr != "" {
			m.Item("Couldn't read foreign keys: " + widgets.OneLine(widgets.FirstLine(v.fkErr), 120)).Disabled(true)
		} else if !found {
			m.Item("No foreign keys from or to this cell").Disabled(true)
		}
	})
	if v.source.Table.Kind == db.KindTable && !v.source.Conn.Config.ReadOnly {
		m.Submenu("Logical Structure", func(m *ui.Menu) {
			if m.Item("Virtual Key…").Chosen() {
				openKeyForm(v.a, v)
			}
		})
	}
	m.Submenu("Generate SQL", func(m *ui.Menu) {
		rows := v.selectedReadRows()
		for _, kind := range []struct{ id, label string }{{"select", "SELECT by Key"}, {"insert", "INSERT"}, {"update", "UPDATE"}, {"delete", "DELETE"}} {
			if m.Item(kind.label).Disabled(len(rows) == 0).Chosen() {
				showSQL(v.a, kind.label+" of "+v.source.Table.Name, v.sqlGen().generate(kind.id, &v.src, rows), v.source.Conn, v.source.Database)
			}
		}
		m.Separator()
		for _, kind := range []struct{ id, label string }{{"select", "Table: SELECT"}, {"insert", "Table: INSERT"}, {"update", "Table: UPDATE"},
			{"delete", "Table: DELETE"}, {"upsert", "Table: INSERT or UPDATE"}, {"join", "Table: JOIN Referenced Tables"}} {
			if kind.id == "join" && len(v.fks) == 0 {
				continue
			}
			if m.Item(kind.label).Chosen() {
				showSQL(v.a, kind.label+" of "+v.source.Table.Name, v.sqlGen().template(kind.id, v.src.Cols, v.fks), v.source.Conn, v.source.Database)
			}
		}
		if v.source.ShowDDL != nil {
			m.Separator()
			if m.Item("DDL").Chosen() {
				v.source.ShowDDL()
			}
		}
	})

}

// selectedReadRows are the chosen rows that were read, not added.
func (v *Viewer) selectedReadRows() []int {
	return slices.DeleteFunc(v.grid.selectedRows(&v.src), func(r int) bool { return r >= len(v.src.Rows) })
}

// sqlGen writes SQL for rows of the table.
func (v *Viewer) sqlGen() sqlGen {
	key, _, _ := v.keyColumns()
	return sqlGen{dialect: v.dialect(), engine: v.source.Conn.Config.Engine, schema: v.source.Table.Schema, table: v.source.Table.Name, key: key}
}

// loadRefs reads the foreign keys of other tables that point at this one,
// then runs then.
func (v *Viewer) loadRefs(then func()) {
	if then != nil {
		v.refsWaiting = append(v.refsWaiting, then)
	}
	if v.refsLoading {
		return
	}
	v.refsLoading = true
	poolOf, schema, name := v.source.Conn.PoolFor(v.source.Database), v.source.Table.Schema, v.source.Table.Name
	BackgroundResetOnPanic(v.a, func() { v.refsLoading, v.refsWaiting = false, nil }, func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		refs := []db.Reference{}
		if d, err := poolOf(ctx); err == nil {
			if found, err := d.Dialect.ReferencedBy(ctx, d.Catalog(), schema, name); err == nil {
				refs = found
			}
		}
		return func() {
			v.refs, v.refsLoading = refs, false
			waiting := v.refsWaiting
			v.refsWaiting = nil
			for _, then := range waiting {
				then()
			}
		}
	})
}

// referencingWhere is the filter of the rows of a referencing table that
// point at a row of this one.
func (v *Viewer) referencingWhere(r db.Reference, row int) (string, bool) {
	var parts []string
	for i, col := range r.Columns {
		at := -1
		for j := range v.src.Cols {
			if i < len(r.RefColumns) && v.tableName(j) == r.RefColumns[i] {
				at = j
			}
		}
		if at < 0 || v.src.Rows[row][at] == nil {
			return "", false
		}
		parts = append(parts, v.dialect().Quote(col)+" = "+db.Literal(v.source.Conn.Config.Engine, v.src.Rows[row][at]))
	}
	return strings.Join(parts, " AND "), len(parts) > 0
}

// openTableWhere opens a table of the connection, filtered.
func (v *Viewer) openTableWhere(schema, table, where string) {
	obj := db.Object{Schema: schema, Name: table, Kind: db.KindTable, Rows: -1, Bytes: -1}
	for _, o := range v.source.Conn.Objects[connection.SchemaKey{Database: v.source.Database, Schema: schema}] {
		if o.Name == table {
			obj = o
		}
	}
	v.a.OpenTable(v.source.Conn, v.source.Database, obj, PageData)
	v.filterActiveRef(where)
}

// openRefWhere opens the table a foreign key refers to, filtered.
func (v *Viewer) openRefWhere(fk db.ForeignKey, where string) {
	v.openRef(fk)
	v.filterActiveRef(where)
}

// filterActiveRef filters the table tab just opened, when it is another
// Viewer's, on where.
func (v *Viewer) filterActiveRef(where string) {
	if rt, ok := v.a.ActiveTab().(*TableTab); ok && rt.view != v {
		rv := rt.view
		rv.where, rv.whereIn, rv.typedWhere, rv.menuFilters, rv.count = where, where, where, nil, -1
		if rv.columns != nil {
			rv.RequestReload()
		}
	}
}

// openRef opens the table a foreign key refers to.
func (v *Viewer) openRef(fk db.ForeignKey) {
	schema := cmp.Or(fk.RefSchema, v.source.Table.Schema)
	for _, o := range v.source.Conn.Objects[connection.SchemaKey{Database: v.source.Database, Schema: schema}] {
		if o.Name == fk.RefTable {
			v.a.OpenTable(v.source.Conn, v.source.Database, o, PageData)
			return
		}
	}
	v.a.OpenTable(v.source.Conn, v.source.Database, db.Object{Schema: schema, Name: fk.RefTable, Kind: db.KindTable, Rows: -1, Bytes: -1}, PageData)
}

// tableFilter is a condition the Filter menu put on a column.
type tableFilter struct {
	col  int
	sql  string
	cond rowCond // for the filter builder to show
}

// addFilter adds a condition of the Filter menu to the table's filter,
// read on the server.
func (v *Viewer) addFilter(c rowCond) {
	f := tableFilter{col: c.col, cond: c, sql: condSQL(v.dialect(), v.source.Conn.Config.Engine, v.src.Cols[c.col].Name, c)}
	v.setFilter(func() {
		v.menuFilters = append(slices.Clip(v.menuFilters), f)
		v.joinFilters()
	})
}

// clearFilters drops the menu's conditions on a column, or every filter
// for -1.
func (v *Viewer) clearFilters(col int) {
	v.setFilter(func() {
		if col < 0 {
			v.typedWhere, v.menuFilters = "", nil
		} else {
			v.menuFilters = slices.DeleteFunc(slices.Clone(v.menuFilters), func(f tableFilter) bool { return f.col == col })
		}
		v.joinFilters()
	})
}

// setFilter changes the filter by change, then reads the rows under it;
// Cancel on the question about pending changes puts back the filter, and
// the box shows it.
func (v *Viewer) setFilter(change func()) {
	where, typed, menu, count := v.where, v.typedWhere, v.menuFilters, v.count
	change()
	if v.applying {
		v.reloadAgain = true // read under the new filter once the changes are written
		return
	}
	v.CheckPending(v.refresh, func() {
		v.where, v.whereIn, v.typedWhere, v.menuFilters, v.count = where, where, typed, menu, count
	})
}

// joinFilters makes the filter of the one typed and the menu's.
func (v *Viewer) joinFilters() {
	var parts []string
	if w := strings.TrimSpace(v.typedWhere); w != "" {
		parts = append(parts, "("+w+")")
	}
	for _, f := range v.menuFilters {
		parts = append(parts, f.sql)
	}
	v.where = strings.Join(parts, " AND ")
	if len(parts) == 1 && strings.TrimSpace(v.typedWhere) != "" {
		v.where = v.typedWhere
	}
	v.whereIn, v.count = v.where, -1
}

// followKey opens the row the chosen cell refers to by foreign key, as
// Alt+Space does in DBeaver.
func (v *Viewer) followKey() {
	order := v.grid.ViewOrder(&v.src)
	if v.grid.SelRow < 0 || v.grid.SelRow >= len(order) || order[v.grid.SelRow] >= len(v.src.Rows) {
		return
	}
	row, col := order[v.grid.SelRow], v.grid.selCol
	value := v.src.Rows[row][col]
	for _, fk := range v.fks {
		if len(fk.Columns) == 1 && fk.Columns[0] == v.tableName(col) && value != nil && len(fk.RefColumns) == 1 {
			v.openRefWhere(fk, v.dialect().Quote(fk.RefColumns[0])+" = "+db.Literal(v.source.Conn.Config.Engine, value))
			return
		}
	}
}

// tableName is the name in the table of column i of the rows.
func (v *Viewer) tableName(i int) string {
	if i < len(v.bound) && v.bound[i] != "" {
		return v.bound[i]
	}
	return v.src.Cols[i].Name
}

// historyKey names the rows among the project's recent filters, row
// colors and virtual keys.
// historyKey names the rows' table among its project's recent filters,
// row colors, virtual keys and hidden values, by its connection's ID as it
// is now; "" for rows of no table.
func (v *Viewer) historyKey() string {
	if v.source.Table == nil {
		return ""
	}
	return tableHistoryKey(v.source.Conn, *v.source.Table)
}

// distinctValues reads a column's most frequent values, under the
// rows' filter, on the server.
func (v *Viewer) distinctValues(col int, then func([]distinctValue, error)) {
	if err := v.checkFilter(v.where); err != nil {
		then(nil, err)
		return
	}
	name := v.dialect().Quote(v.src.Cols[col].Name)
	q := "SELECT " + name + ", count(*) AS n FROM " + v.from() + whereClause(v.where)
	q = db.LimitSQL(v.dialect().Engine(), q+" GROUP BY "+name+" ORDER BY n DESC", " ", maxDistinct)
	v.queryRows(q, v.source.Args, maxDistinct, func(_ []string, rows [][]any, err error) {
		vals := make([]distinctValue, 0, len(rows))
		for _, r := range rows {
			var n int64
			fmt.Sscan(db.Display(r[1]), &n)
			vals = append(vals, distinctValue{v: r[0], count: n})
		}
		then(vals, err)
	})
}

// queryRows runs a read on the rows' connection, off the main thread,
// audited, and hands its rows to then on the main thread.
// It returns what stops the read, nil for one on the session, which
// stopping could abort the session's transaction.
func (v *Viewer) queryRows(q string, args []any, limit int, then func(cols []string, rows [][]any, err error)) (stop context.CancelFunc) {
	if v.source.CountOnSession {
		v.queryOnSession(q, args, limit, then)
		return nil
	}
	return v.queryOnPool(q, args, limit, then)
}

// queryOnPool runs queryRows' statement on a connection of the pool,
// never the editor's session, whose transaction it would join.
func (v *Viewer) queryOnPool(q string, args []any, limit int, then func(cols []string, rows [][]any, err error)) (stop context.CancelFunc) {
	poolOf := v.source.Conn.PoolFor(v.source.Database)
	cfg, database := v.source.Conn.Config, v.source.Database
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	BackgroundResetOnPanic(v.a, func() { then(nil, nil, errors.New("stopped on an internal error")) }, func() func() {
		defer cancel()
		start := time.Now()
		var cols []string
		var out [][]any
		d, err := poolOf(ctx)
		if err == nil {
			var rows *sql.Rows
			if rows, err = d.SQL.QueryContext(ctx, q, args...); err == nil {
				cols, _ = rows.Columns()
				for rows.Next() && len(out) < limit {
					vals := make([]any, len(cols))
					ptrs := make([]any, len(cols))
					for i := range vals {
						ptrs[i] = &vals[i]
					}
					if err = rows.Scan(ptrs...); err != nil {
						break
					}
					for i, val := range vals {
						if b, ok := val.([]byte); ok && utf8.Valid(b) {
							vals[i] = string(b)
						}
					}
					out = append(out, vals)
				}
				if err == nil {
					err = rows.Err()
				}
				rows.Close()
			}
		}
		v.a.RecordRun(cfg, audit.KindStatement, database, q, int64(len(out)), time.Since(start), err)
		return func() { then(cols, out, err) }
	})
	return cancel
}

// queryOnSession runs queryRows' statement on the session, once no result
// reads rows there and nothing else runs on it.
func (v *Viewer) queryOnSession(q string, args []any, limit int, then func(cols []string, rows [][]any, err error)) {
	why := cmp.Or(v.source.CursorOpen(), v.sessionBusy())
	sess := v.source.Session()
	if why == "" && sess == nil {
		why = "The editor's connection is not open."
	}
	if why != "" {
		then(nil, nil, errors.New(why))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	v.sessionReads++
	v.readCancel = cancel
	cfg, database := v.source.Conn.Config, v.source.Database
	go func() {
		defer RecoverBackground(v.a.Post, v.a.ShowReport, func() {
			v.sessionReads--
			if v.sessionReads == 0 {
				v.readCancel = nil
			}
			if !v.released {
				then(nil, nil, errors.New("stopped on an internal error"))
			}
		})
		start := time.Now()
		var cols []string
		var out [][]any
		c, err := sess.Query(ctx, q, args...)
		if err == nil {
			for _, col := range c.Columns {
				cols = append(cols, col.Name)
			}
			out, err = c.Fetch(limit)
			c.Close() // before its context ends: on MySQL that kills what the connection runs
		}
		cancel()
		v.a.RecordRun(cfg, audit.KindStatement, database, q, int64(len(out)), time.Since(start), err)
		own, inside := SessionTx(sess)
		v.a.Post(func() {
			v.sessionReads--
			if v.sessionReads == 0 {
				v.readCancel = nil
			}
			if v.released {
				return
			}
			v.source.TxChanged(own, inside)
			v.noteReset(err)
			then(cols, out, err)
		})
	}()
}

// profileOnServer profiles every column over every row, under the
// filter, on the server: DuckDB summarizes them, the others count.
func (v *Viewer) profileOnServer(then func([]columnProfile, error)) {
	if err := v.checkFilter(v.where); err != nil {
		then(nil, err)
		return
	}
	e := v.dialect().Engine()
	cols := slices.Clone(v.src.Cols)
	if r, ok := db.SQL(e); ok {
		for i := range cols {
			// A column the table's rows read as text is profiled as the
			// table's type, which the profile reads.
			if typ := v.columnInfo(i).Type; r.ShownColumnSQL("c", typ) != "" {
				cols[i].Type = typ
			}
		}
	}
	q := profileQuery(v.dialect(), cols, v.from(), strings.TrimSpace(v.where))
	format := func(x any) string { return cellText(v.a.Settings().ViewFormat.Format(x), 40) }
	v.queryRows(q, v.source.Args, 10_000, func(names []string, rows [][]any, err error) {
		if err != nil {
			then(nil, err)
			return
		}
		then(serverProfiles(e, cols, names, rows, format))
	})
}

// groupOnServer counts the rows, under their filter, by the values of
// columns.
func (v *Viewer) groupOnServer(cols []int, then func([]groupRow, error)) {
	if err := v.checkFilter(v.where); err != nil {
		then(nil, err)
		return
	}
	names := lists.Map(cols, func(c int) string { return v.dialect().Quote(v.src.Cols[c].Name) })
	list := strings.Join(names, ", ")
	q := "SELECT " + list + ", count(*) AS n FROM " + v.from() + whereClause(v.where)
	q = db.LimitSQL(v.dialect().Engine(), q+" GROUP BY "+list+" ORDER BY n DESC", " ", 1000)
	v.queryRows(q, v.source.Args, 1000, func(_ []string, rows [][]any, err error) {
		var out []groupRow
		for _, r := range rows {
			var n int64
			fmt.Sscan(db.Display(r[len(r)-1]), &n)
			out = append(out, groupRow{vals: r[:len(r)-1], count: n})
		}
		then(out, err)
	})
}

// referencingRows reads, for each table that refers to this one, up to
// 50 of its rows that refer to a row.
func (v *Viewer) referencingRows(row int, then func([]refRows, error)) {
	if v.refs == nil {
		v.loadRefs(func() { v.referencingRows(row, then) })
		return
	}
	if v.refSelects == nil && len(v.refs) > 0 {
		v.loadRefSelects(func() { v.referencingRows(row, then) })
		return
	}
	var out []refRows
	left := 0
	var firstErr error
	for i, r := range v.refs {
		where, ok := v.referencingWhere(r, row)
		if !ok {
			continue
		}
		left++
		ref := r
		q := v.refSelects[i] + " WHERE " + where
		v.queryRows(db.LimitSQL(v.dialect().Engine(), q, " ", 50), nil, 50, func(cols []string, rows [][]any, err error) {
			if err != nil && firstErr == nil {
				firstErr = err
			}
			out = append(out, refRows{title: ref.Schema + "." + ref.Table, where: where, cols: cols, rows: rows,
				open: func() { v.openTableWhere(ref.Schema, ref.Table, where) }})
			if left--; left == 0 {
				sort.Slice(out, func(i, j int) bool { return out[i].title < out[j].title })
				then(out, firstErr)
			}
		})
	}
	if left == 0 {
		then(nil, nil)
	}
}

// loadRefSelects reads the columns of refs' tables for refSelects, then
// runs then.
func (v *Viewer) loadRefSelects(then func()) {
	poolOf, refs, dialect := v.source.Conn.PoolFor(v.source.Database), v.refs, v.dialect()
	BackgroundResetOnPanic(v.a, func() {}, func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		selects := make([]string, len(refs))
		d, err := poolOf(ctx)
		for i, r := range refs {
			var cols []db.Column
			if err == nil {
				cols, _ = d.Dialect.Columns(ctx, d.Catalog(), r.Schema, r.Table)
			}
			selects[i] = db.TableSelectSQL(dialect, r.Schema, r.Table, cols)
		}
		return func() {
			v.refSelects = selects
			then()
		}
	})
}

// keyColumns are the columns that tell the rows' table's rows apart: its
// primary key, else the virtual key the project defines for it.
func (v *Viewer) keyColumns() ([]string, bool, error) { return v.keyOf(v.columns) }

func (v *Viewer) keyOf(cols []db.Column) ([]string, bool, error) {
	key, err := db.KeyColumns(cols)
	if err == nil {
		return key, false, nil
	}
	if vk := v.source.Conn.Project.VirtualKeys[v.historyKey()]; len(vk) > 0 {
		for _, k := range vk {
			if !slices.ContainsFunc(cols, func(c db.Column) bool { return c.Name == k }) {
				return nil, false, fmt.Errorf("virtual key names the column %s, which the table no longer has", k)
			}
		}
		return vk, true, nil
	}
	return nil, false, err
}

// setVirtualKey makes columns the virtual key of a table, in its
// project's file; none takes it away.
func setVirtualKey(a Host, v *Viewer, cols []string) error {
	p := v.source.Conn.Project
	if err := p.Writable(); err != nil {
		return err
	}
	if p.VirtualKeys == nil {
		p.VirtualKeys = map[string][]string{}
	}
	if len(cols) == 0 {
		delete(p.VirtualKeys, v.historyKey())
	} else {
		p.VirtualKeys[v.historyKey()] = cols
	}
	if err := p.Save(a.ProjectConfigs(p)); err != nil {
		return err
	}
	v.bindColumns()
	a.Record(&v.source.Conn.Config, audit.Event{Kind: audit.KindConfig, Detail: fmt.Sprintf("virtual key of %s.%s: %v", v.source.Table.Schema, v.source.Table.Name, cols)})
	return nil
}

// keyForm chooses the columns of a virtual key.
type keyForm struct {
	open    bool
	v       *Viewer
	on      []bool
	err     string
	dismiss widgets.DismissGuard
}

func openKeyForm(a Host, v *Viewer) {
	f := &keyForm{open: true, v: v, on: make([]bool, len(v.columns))}
	cur, _, _ := v.keyColumns()
	for i, c := range v.columns {
		f.on[i] = slices.Contains(cur, c.Name)
	}
	a.Dialogs().keyForm = f
}

func keyFormView(a Host, c *ui.Context) {
	f := a.Dialogs().keyForm
	pal := widgets.PaletteOf(c)
	dlg := widgets.Modal(c, &f.open, func() {
		ui.Column(c).Width(460).Gap(10).Children(func() {
			ui.Text(c, "Virtual Key of "+f.v.source.Table.Name).FontSize(15).Bold()
			ui.Text(c, "Choose the columns whose values tell each row apart. The key is kept in "+project.File+" for the team; each change must still match exactly one row.").FontSize(12).TextColor(pal.Muted)
			ui.Scroll(c).MaxHeight(300).Children(func() {
				ui.Column(c).Gap(4).Children(func() {
					for i, col := range f.v.columns {
						ui.Checkbox(c, &f.on[i], col.Name+"  "+col.Type)
					}
				})
			})
			if f.err != "" {
				ui.Text(c, f.err).FontSize(12).TextColor(c.Theme().Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Remove the Key").Clicked() {
					if err := setVirtualKey(a, f.v, nil); err != nil {
						f.err = err.Error()
						return
					}
					f.open = false
				}
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					f.open = false
				}
				if widgets.Activated(c, ui.PrimaryButton(c, "Save")) {
					var cols []string
					for i, on := range f.on {
						if on {
							cols = append(cols, f.v.columns[i].Name)
						}
					}
					if len(cols) == 0 {
						f.err = "Choose at least one column."
						return
					}
					if err := setVirtualKey(a, f.v, cols); err != nil {
						f.err = err.Error()
						return
					}
					f.open = false
				}
			})
		})
	})
	f.dismiss.Keep(c, dlg, &f.open, f.on, false)
	dropClosed(&a.Dialogs().keyForm, f, f.open)
}

// columnInfo is a column of the rows as the value editor needs it: with
// the type the table's catalog gives, where the column is the table's.
func (v *Viewer) columnInfo(col int) columnInfo {
	info := columnInfo{Type: v.src.Cols[col].Type, Engine: v.source.Conn.Config.Engine}
	if name := v.tableName(col); col < len(v.bound) && v.bound[col] != "" {
		if i := slices.IndexFunc(v.columns, func(c db.Column) bool { return c.Name == name }); i >= 0 {
			info.Type = v.columns[i].Type
		}
	}
	return info
}

// enumValues reads the values a column's enum type may hold, none for
// another type.
func (v *Viewer) enumValues(col int, then func([]string)) {
	typ := v.columnInfo(col).Type
	poolOf := v.source.Conn.PoolFor(v.source.Database)
	v.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var values []string
		if d, err := poolOf(ctx); err == nil {
			values, _ = db.EnumValues(ctx, d, typ) // without them, the text editor stays
		}
		return func() { then(values) }
	})
}
