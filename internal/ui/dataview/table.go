package dataview

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/editor"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

const (
	PageData = iota
	PageStructure
	PageDDL
	pageDiagram
)

// TableTab shows a table's rows, structure and definition.
type TableTab struct {
	a        Host
	Conn     *connection.Conn
	Database string
	Object   db.Object
	Page     int

	sess *db.Session
	// view is the Data page.
	view *Viewer
	// ending is set while a COMMIT or ROLLBACK is on its way.
	ending bool
	// diagram is the Diagram page, read when first shown.
	diagram *ERTab
	SessionTxState
	closed bool

	columns []db.Column
	indexes []db.Index
	fks     []db.ForeignKey
	ddl     string
	metaErr string
	metaGen int

	// design is the table form, while the structure is being changed.
	design        *designer
	readingDesign bool
}

func NewTableTab(a Host, cn *connection.Conn, database string, obj db.Object, page int) *TableTab {
	t := &TableTab{a: a, Conn: cn, Database: database, Object: obj, Page: page}
	t.view = NewViewer(a, ViewerSource{
		Keys:         func() bool { return a.KeysTo(t) },
		Conn:         cn,
		Database:     database,
		Statement:    "SELECT * FROM " + db.QualifiedName(cn.DB.Dialect, obj.Schema, obj.Name),
		Reads:        true,
		Table:        &t.Object,
		Session:      func() *db.Session { return t.sess },
		AdoptSession: t.adoptSession,
		TxChanged:    t.setTx,
		TxOwner:      t.TxOwner,
		HistoryKey:   t.historyKey(),
		Bars:         t.txBar,
		ShowDDL:      func() { t.Page = PageDDL },
	})
	// The rows wait for the columns, to be ordered by the primary key.
	t.loadMeta(t.view.reload)
	return t
}

func (t *TableTab) Title() string { return t.Object.Name }

func (t *TableTab) Connection() *connection.Conn { return t.Conn }

func (t *TableTab) CloseReason() string {
	switch {
	case t.design != nil && t.design.changed():
		return "The changes to " + t.Object.Name + "'s structure have not been applied. Closing discards them."
	case t.view.grid.edits.count() > 0:
		return fmt.Sprintf("%s to %s not applied. Closing discards them.", widgets.Count(t.view.grid.edits.count(), "change"), t.Object.Name)
	case t.Tx != db.TxNone:
		return "A transaction is open on this table's session. Closing rolls it back."
	}
	return ""
}

// Close ends the tab's work: fields are read on the main thread, what may
// block runs on another goroutine.
func (t *TableTab) Close() {
	t.closed = true
	var cursors []*db.Cursor
	if t.view.cursor != nil {
		cursors = append(cursors, t.view.cursor)
	}
	cancel, sess := t.view.cancel, t.sess
	if t.view.applyCancel != nil {
		t.view.applyCancel()
	}
	if t.design != nil && t.design.cancel != nil {
		t.design.cancel()
	}
	if t.Tx != db.TxNone {
		t.a.Record(&t.Conn.Config, audit.Event{Kind: audit.KindStatement, Database: t.Database, Statement: "ROLLBACK", Detail: "the tab closed with its transaction open"})
	}
	go func() {
		defer RecoverBackground(t.a.Post, t.a.ShowError, nil)
		connection.CloseThenCancel(cursors, cancel)
		if sess != nil {
			sess.Close()
		}
	}()
}

// adoptSession keeps a session a goroutine started for the tab.
func (t *TableTab) adoptSession(s *db.Session) {
	t.a.Post(func() {
		if t.closed || t.sess != nil {
			if t.sess != s {
				go s.Close()
			}
			return
		}
		t.sess = s
	})
}

func (t *TableTab) loadMeta(then func()) {
	cn, database, obj := t.Conn, t.Database, t.Object
	t.metaGen++
	gen := t.metaGen
	poolOf := cn.PoolFor(database) // read on the main thread
	t.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := poolOf(ctx)
		var cols []db.Column
		var ixs []db.Index
		var fks []db.ForeignKey
		var ddl string
		if err == nil {
			cols, err = d.Dialect.Columns(ctx, d.Catalog(), obj.Schema, obj.Name)
		}
		if err == nil {
			ixs, _ = d.Dialect.Indexes(ctx, d.Catalog(), obj.Schema, obj.Name)
			fks, _ = d.Dialect.ForeignKeys(ctx, d.Catalog(), obj.Schema, obj.Name)
			var derr error
			ddl, derr = d.Dialect.DDL(ctx, d.Catalog(), obj.Schema, obj)
			if derr != nil {
				ddl = sqltext.LineComment(derr.Error())
			}
		}
		return func() {
			if gen != t.metaGen {
				return
			}
			if err != nil {
				t.metaErr = err.Error()
			} else {
				t.columns, t.indexes, t.fks, t.ddl = cols, ixs, fks, ddl
				cn.Columns[connection.ObjectKey{Database: database, Schema: obj.Schema, Name: obj.Name}] = cols
				t.view.SetColumns(cols, fks)
			}
			if then != nil {
				then()
			}
		}
	})
}

// endOpenTx commits or rolls back the open transaction, when one is open
// that can: a failed one only rolls back, none ends while changes are
// being applied, and another session's is refused.
func (t *TableTab) endOpenTx(commit bool) {
	if t.RefuseEndInside(t.a, commit) {
		return
	}
	if t.Tx == db.TxNone || t.view.applying || commit && t.Tx != db.TxOpen {
		return
	}
	t.endTx(commit)
}

func (t *TableTab) endTx(commit bool) {
	sess, cfg, database := t.sess, t.Conn.Config, t.Database
	if sess == nil || t.ending {
		return
	}
	t.ending = true
	go func() {
		defer RecoverBackground(t.a.Post, t.a.ShowError, t.endTxStopped)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var err error
		start := time.Now()
		stmt := "COMMIT"
		if commit {
			err = sess.Commit(ctx)
		} else {
			stmt = "ROLLBACK"
			err = sess.Rollback(ctx)
		}
		// A stale tab's COMMIT or ROLLBACK the connection refused was never
		// sent: it is said, not audited.
		title, notEnded, refused := NotEnded(err, commit)
		if !refused {
			t.a.RecordRun(cfg, audit.KindStatement, database, stmt, -1, time.Since(start), err)
		}
		own, inside := SessionTx(sess)
		t.a.Post(func() {
			t.ending = false
			t.setTx(own, inside)
			if t.TellFinishTx(err) {
				if err != nil {
					t.RequestReload()
					return
				}
			} else if refused {
				t.a.ShowError(title, notEnded)
			} else if err != nil {
				t.a.ShowError("Could not end the transaction", err.Error())
			}
			t.RequestReload()
		})
	}()
}

// InsideTxLabel says, on a tab's bar, that its statements run inside a
// transaction another session began on the connection every tab of conn
// shares; owner names the tab that began it, "" when unknown.
func InsideTxLabel(owner, conn string, tx db.TxState) string {
	who := txOwnerName(owner)
	if tx == db.TxFailed {
		return fmt.Sprintf("Inside %s's transaction, which failed: %s must roll it back.", who, who)
	}
	return fmt.Sprintf("Inside %s's transaction: every tab of %s shares one connection, so this tab's changes commit or roll back with it.", who, conn)
}

// InsideTxEndText says why a tab does not run a statement, typed as verb,
// that would end the transaction its statements run inside: another
// session began it, as SessionTxState.RefuseEndInside says of the tab's
// buttons.
func InsideTxEndText(owner, verb string) string {
	return insideTxText(owner, "commit or roll it back there, not with "+verb+" here")
}

// InsideTxBeginText is why a tab inside owner's transaction is refused a
// statement beginning one, verb: it cannot begin one inside it.
func InsideTxBeginText(owner, verb string) string {
	return insideTxText(owner, verb+" here cannot begin another inside it; commit or roll it back there first")
}

// insideTxText says that the transaction a tab's statements run inside is
// owner's, then what the tab is to do, then that its changes are in it.
func insideTxText(owner, todo string) string {
	return fmt.Sprintf("The transaction is %s's, which began it: %s. This tab's changes are in it too.", txOwnerName(owner), todo)
}

// noSavepointsText says why changes are not applied in an open
// transaction on an engine without savepoints: a failed one would abort
// the whole transaction, the tab's own or, when inside is set, owner's.
func noSavepointsText(engine, owner string, inside bool) string {
	why := engine + " has no savepoints, so a failed change could not be undone on its own and would abort the whole transaction"
	if inside {
		return fmt.Sprintf("%s, which is %s's: commit or roll it back there first.", why, txOwnerName(owner))
	}
	return why + ": commit or roll back this tab's transaction first."
}

func txOwnerName(owner string) string {
	if owner == "" {
		return "another session"
	}
	return owner
}

// RequestReload reads the Data page's rows again, once the user agrees to
// drop the pending changes, if any.
func (t *TableTab) RequestReload() { t.view.RequestReload() }

// historyKey names the table among the project's recent filters.
func (t *TableTab) historyKey() string { return tableHistoryKey(t.Conn, t.Object) }

// tableHistoryKey names a table among its project's recent filters, row
// colors and virtual keys, for its table tab and the query results read
// from it alike.
func tableHistoryKey(cn *connection.Conn, obj db.Object) string {
	return strings.TrimPrefix(cn.Config.ID, cn.Project.Prefix) + "/" + obj.Schema + "." + obj.Name
}

// txBar offers to end the transaction the session holds open, or says
// whose transaction its statements run inside.
func (t *TableTab) txBar(c *ui.Context) {
	th := c.Theme()
	label := "Changes applied in an open transaction (manual commit)."
	if t.InsideTx != db.TxNone {
		label = InsideTxLabel(t.owner, t.Conn.Config.Name, t.InsideTx)
	}
	if t.InTx() {
		ui.Row(c).Padding(6, 12).Gap(10).Background(th.Warning.Alpha(0.16)).Children(func() {
			ui.Icon(c, widgets.IconAlert).TextColor(th.Warning).FontSize(14)
			ui.Text(c, label).Grow(1)
			if ui.PrimaryButton(c, "Commit").Disabled(t.view.applying).Tooltip(keymap.Hint("Commit the transaction", keymap.Commit)).Clicked() {
				t.endOpenTx(true)
			}
			if ui.Button(c, "Roll Back").Disabled(t.view.applying).Tooltip(keymap.Hint("Roll back the transaction", keymap.Rollback)).Clicked() {
				t.endOpenTx(false)
			}
		})
	}
}

func (t *TableTab) View(c *ui.Context) {
	a := t.a
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	if want := a.FocusWant(); *want == "editor" && t.Page == PageData && a.KeysTo(t) {
		*want = "results" // the tab chosen by its key: its rows take the keys
	}
	if a.KeysTo(t) && keymap.Pressed(c, keymap.Commit) {
		t.endOpenTx(true)
	}
	if a.KeysTo(t) && keymap.Pressed(c, keymap.Rollback) {
		t.endOpenTx(false)
	}
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(6, 10).Gap(10).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ic := widgets.IconTable
			if t.Object.Kind != db.KindTable {
				ic = widgets.IconView
			}
			ui.Icon(c, ic).TextColor(pal.Muted).FontSize(14)
			ui.Text(c, t.Object.Schema+"."+t.Object.Name).Bold().SingleLine().Shrink(1)
			if t.Object.Engine != "" {
				ui.Badge(c, t.Object.Engine)
			}
			ui.Spacer(c)
			ui.Segmented(c, &t.Page, "Data", "Structure", "DDL", "Diagram").Label("Table page")
		})
		switch t.Page {
		case PageData:
			t.view.View(c)
		case PageStructure:
			t.structureView(c, a)
		case PageDDL:
			t.ddlView(c, a)
		case pageDiagram:
			if t.diagram == nil {
				t.diagram = newTableDiagram(a, t.Conn, t.Database, t.Object)
			}
			t.diagram.View(c)
		}
	})
}

func (t *TableTab) structureView(c *ui.Context, a Host) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	if t.design != nil {
		t.design.View(c)
		return
	}
	if t.metaErr != "" {
		ui.Text(c, t.metaErr).TextColor(th.Danger).Padding(12)
		return
	}
	if t.Object.Kind == db.KindTable {
		ui.Row(c).Padding(6, 10).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			why := t.editBlocked()
			if widgets.ToolButton(c, widgets.IconPencil, "Edit Structure", "Change the columns, keys, indexes and checks").Disabled(why != "").Tooltip(why).Clicked() {
				t.editStructure()
			}
			if t.readingDesign {
				ui.Spinner(c).Size(12, 12)
			}
			if why != "" {
				ui.Text(c, why).FontSize(12).TextColor(pal.Muted)
			}
		})
	}
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(12, 16).Gap(16).Children(func() {
			section := func(title string, n int) {
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, title).Bold()
					ui.Badge(c, fmt.Sprint(n))
				})
			}
			section("Columns", len(t.columns))
			cols := []ui.TableColumn{{Title: "", ID: "k", Width: 28, Fixed: true}, {Title: "Name", Width: 200}, {Title: "Type", Width: 200}, {Title: "Null", Width: 90}, {Title: "Default", Width: 220}, {Title: "Comment"}}
			ui.Table(c, nil, cols, len(t.columns), func(r, col int) {
				cl := t.columns[r]
				switch col {
				case 0:
					if cl.PrimaryKey {
						ui.Icon(c, widgets.IconKey).TextColor(c.Theme().Warning).FontSize(12)
					}
				case 1:
					ui.Text(c, cl.Name).Bold().SingleLine()
				case 2:
					ui.Text(c, cl.Type).Font(widgets.MonoFont).FontSize(12).SingleLine()
				case 3:
					if cl.Nullable {
						ui.Text(c, "yes").TextColor(pal.Muted)
					} else {
						ui.Text(c, "NOT NULL").FontSize(11).SingleLine()
					}
				case 4:
					if cl.HasDefault {
						ui.Text(c, cl.Default).Font(widgets.MonoFont).FontSize(12).SingleLine()
					}
				case 5:
					ui.Text(c, cl.Comment).TextColor(pal.Muted).SingleLine()
				}
			}).Height(float32(min(len(t.columns), 16)*34 + 40)).Label("Columns")
			section("Indexes", len(t.indexes))
			for _, ix := range t.indexes {
				ui.Row(c).Gap(8).Children(func() {
					switch {
					case ix.Primary:
						ui.Badge(c, "PRIMARY")
					case ix.Unique:
						ui.Badge(c, "UNIQUE")
					}
					ui.Text(c, ix.Name).Bold()
					ui.Text(c, strings.Join(ix.Columns, ", ")).TextColor(pal.Muted)
				})
				if ix.Definition != "" {
					ui.Text(c, ix.Definition).Font(widgets.MonoFont).FontSize(11.5).TextColor(pal.Muted).Selectable()
				}
			}
			if len(t.fks) > 0 || t.Conn.Config.Engine != db.ClickHouse {
				section("Foreign keys", len(t.fks))
			}
			for _, fk := range t.fks {
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, strings.Join(fk.Columns, ", ")).Bold()
					ui.Text(c, "→")
					if ui.Link(c, fk.RefSchema+"."+fk.RefTable, "").Clicked() {
						t.view.openRef(fk)
					}
					ui.Text(c, "("+strings.Join(fk.RefColumns, ", ")+")").TextColor(pal.Muted)
				})
			}
		})
	})
}

func (t *TableTab) ddlView(c *ui.Context, a Host) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Row(c).Padding(6, 10).Gap(8).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
		if widgets.ToolButton(c, widgets.IconCopy, "Copy", "Copy the definition").Clicked() {
			a.WriteClipboard(t.ddl)
		}
		if widgets.ToolButton(c, widgets.IconCode, "Open in Editor", "Open the definition in a SQL editor").Clicked() {
			a.NewQueryTab(t.Conn, t.Database, t.ddl)
		}
	})
	if t.ddl == "" && t.metaErr == "" {
		ui.Row(c).Grow(1).Center().Children(func() { ui.Spinner(c) })
		return
	}
	ui.Scroll(c).Grow(1).Background(pal.EditorBg).Children(func() {
		ui.Text(c, t.ddl).Font(widgets.MonoFont).FontSize(a.Settings().EditorFont).FixedLineHeight(a.Settings().EditorFont*editor.LineHeight).
			Padding(12, 16).Selectable()
	})
}

// editBlocked says why the structure cannot be changed now, "" when it
// can.
func (t *TableTab) editBlocked() string {
	switch {
	case t.Conn.Config.ReadOnly:
		return "The connection is read-only."
	case t.view.grid.edits.count() > 0:
		return "Apply or discard the changes to the rows first."
	case t.InTx():
		// The change would wait on the locks the transaction holds.
		return "End the open transaction first."
	}
	return ""
}

// editStructure reads the table's design and opens the table form on it.
func (t *TableTab) editStructure() {
	if t.readingDesign || t.editBlocked() != "" {
		return
	}
	t.readingDesign = true
	poolOf, obj := t.Conn.PoolFor(t.Database), t.Object
	BackgroundResetOnPanic(t.a, func() { t.readingDesign = false }, func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := poolOf(ctx)
		var design db.TableDesign
		if err == nil {
			design, err = db.ReadTableDesign(ctx, d, obj)
		}
		return func() {
			t.readingDesign = false
			if err != nil {
				t.a.ShowError("Could not read the structure of "+obj.Name, err.Error())
				return
			}
			t.design = newDesigner(t.a, t.Conn, t.Database, &design, design, t.structureChanged)
		}
	})
}

// structureChanged opens the table again as it now is: its rows and
// structure, under its new name if it has one.
func (t *TableTab) structureChanged(made db.TableDesign) {
	obj := t.Object
	obj.Name, obj.Comment = made.Name, made.Comment
	t.a.ReplaceTab(t, NewTableTab(t.a, t.Conn, t.Database, obj, PageStructure))
	t.a.Toast("Changed the structure of "+made.Name, "", nil)
}
