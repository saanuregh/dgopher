package app

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/redact"
	"dgopher/internal/safety"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// importState is the dialog importing a CSV file into a table.
type importState struct {
	open     bool
	conn     *connection.Conn
	database string
	obj      db.Object
	columns  []db.Column

	path      string
	header    []string
	preview   [][]string
	mapping   []string // for each CSV column, the table column it fills, "" to skip
	delimiter string
	emptyNull bool
	err       string

	running bool
	done    int64
	total   int64
	cancel  context.CancelFunc
}

const skipColumn = "(skip)"

// openImport starts importing a CSV file into a table.
func (a *App) openImport(cn *connection.Conn, database string, obj db.Object) {
	if cn.Config.ReadOnly {
		a.ShowError("Not allowed", cn.Config.Name+" is read-only.")
		return
	}
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "Import CSV into " + obj.Name,
			Filters: []mygo.FileFilter{{Name: "CSV files", Extensions: []string{"csv", "tsv", "txt"}}}})
		if err != nil || len(paths) == 0 {
			return
		}
		a.Post(func() { a.startImport(cn, database, obj, paths[0]) })
	}()
}

func (a *App) startImport(cn *connection.Conn, database string, obj db.Object, path string) {
	x := &importState{open: true, conn: cn, database: database, obj: obj, path: path, delimiter: ",", emptyNull: true}
	if strings.HasSuffix(strings.ToLower(path), ".tsv") {
		x.delimiter = "\\t"
	}
	a.importing = x
	connection.LoadColumns(a, cn, database, obj.Schema, obj.Name, func(cols []db.Column) {
		x.columns = cols
		x.readPreview()
	})
}

func (x *importState) comma() rune {
	if x.delimiter == "\\t" {
		return '\t'
	}
	r, _ := utf8.DecodeRuneInString(x.delimiter)
	if r == utf8.RuneError {
		return ','
	}
	return r
}

// readPreview reads the header and first rows, and maps the CSV's columns
// to the table's by name.
func (x *importState) readPreview() {
	x.err, x.header, x.preview = "", nil, nil
	f, err := os.Open(x.path)
	if err != nil {
		x.err = err.Error()
		return
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma = x.comma()
	r.FieldsPerRecord = -1
	r.ReuseRecord = false
	x.header, err = r.Read()
	if err != nil {
		x.err = "Could not read the header: " + err.Error()
		return
	}
	if len(x.header) > 0 {
		x.header[0] = strings.TrimPrefix(x.header[0], "\uFEFF") // a byte order mark
	}
	for len(x.preview) < 8 {
		rec, err := r.Read()
		if err != nil {
			break
		}
		x.preview = append(x.preview, rec)
	}
	x.mapping = make([]string, len(x.header))
	for i, h := range x.header {
		x.mapping[i] = skipColumn
		for _, c := range x.columns {
			if strings.EqualFold(strings.TrimSpace(h), c.Name) {
				x.mapping[i] = c.Name
			}
		}
	}
}

func (x *importState) targetColumns() (cols []db.Column, idx []int) {
	for i, m := range x.mapping {
		if m == skipColumn || m == "" {
			continue
		}
		for _, c := range x.columns {
			if c.Name == m {
				cols = append(cols, c)
				idx = append(idx, i)
			}
		}
	}
	return
}

// run imports the file: every row, in one transaction, all or nothing.
func (a *App) runImport(x *importState) {
	cols, idx := x.targetColumns()
	if len(cols) == 0 {
		x.err = "Map at least one CSV column to a column of the table."
		return
	}
	seen := map[string]bool{}
	for _, c := range cols {
		if seen[c.Name] {
			x.err = "The column " + c.Name + " is filled from two CSV columns."
			return
		}
		seen[c.Name] = true
	}
	cn := x.conn
	d := cn.DB
	target := &db.EditTarget{Dialect: d.Dialect, Schema: x.obj.Schema, Table: x.obj.Name, Columns: x.columns}
	ctx, cancel := context.WithCancel(context.Background())
	x.running, x.cancel, x.done, x.err = true, cancel, 0, ""
	comma := x.comma()
	emptyNull := x.emptyNull
	path := x.path
	pool, cfg, database := cn.DB, cn.Config, x.database
	started := time.Now()
	go func() {
		defer cancel()
		n, err := importRows(ctx, pool, database, target, cols, idx, path, comma, emptyNull, func(done int64) {
			a.Post(func() { x.done = done })
		})
		ev := audit.Event{Kind: audit.KindImport, Database: database, Rows: n,
			Statement: "INSERT INTO " + target.Table, Detail: "imported from " + path}
		if err != nil {
			ev.Error = err.Error()
		}
		a.Record(&cfg, ev)
		a.Post(func() {
			x.running = false
			if err != nil {
				x.err = err.Error() + "\n\nNothing was imported: the transaction was rolled back."
				if cn.Config.Engine == db.ClickHouse {
					x.err = err.Error() + "\n\nClickHouse has no transactions: the rows before this one were imported."
				}
				a.Notify(started, "Import failed", x.obj.Name+" · "+redact.Secrets(widgets.FirstLine(err.Error())), nil)
				return
			}
			x.open = false
			a.toast = &pendingToast{text: fmt.Sprintf("Imported %d rows into %s", n, x.obj.Name)}
			a.Notify(started, "Import finished", fmt.Sprintf("%d rows into %s", n, x.obj.Name), nil)
			for _, t := range a.tabs {
				if tt, ok := t.(*dataview.TableTab); ok && tt.Conn == cn && tt.Object.Name == x.obj.Name && tt.Object.Schema == x.obj.Schema {
					tt.RequestReload()
				}
			}
		})
	}()
}

func importRows(ctx context.Context, pool *db.DB, database string, target *db.EditTarget, cols []db.Column, idx []int,
	path string, comma rune, emptyNull bool, progress func(int64)) (int64, error) {
	if pool == nil {
		return 0, errors.New("not connected")
	}
	d, err := pool.Database(ctx, database)
	if err != nil {
		return 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma = comma
	r.FieldsPerRecord = -1
	if _, err := r.Read(); err != nil { // the header
		return 0, err
	}
	sess, err := d.Session(ctx)
	if err != nil {
		return 0, err
	}
	defer sess.Close()
	transactional := pool.Config.Engine != db.ClickHouse
	if transactional {
		// A database of one connection shares its transaction with every
		// editor: never import into, or roll back, someone else's.
		if sess.Tx() != db.TxNone {
			return 0, errors.New("a transaction is open on this database: commit or roll it back first")
		}
		if err := sess.Begin(ctx); err != nil {
			return 0, err
		}
	}
	var n int64
	fail := func(err error) (int64, error) {
		if !transactional {
			return n, err // the rows before stay: say how many
		}
		rctx, c := context.WithTimeout(context.Background(), 30*time.Second)
		sess.Rollback(rctx)
		c()
		return 0, err
	}
	line := 1
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			return fail(fmt.Errorf("line %d: %w", line, err))
		}
		values := map[string]any{}
		for i, c := range cols {
			v := ""
			if idx[i] < len(rec) {
				v = rec[idx[i]]
			}
			if v == "" && emptyNull {
				values[c.Name] = nil
			} else {
				values[c.Name] = db.Typed(v)
			}
		}
		stmts, err := target.Statements([]db.Change{{Kind: db.ChangeInsert, Values: values}})
		if err != nil {
			return fail(err)
		}
		for _, s := range stmts {
			if _, err := sess.Exec(ctx, s.SQL, s.Args...); err != nil {
				return fail(fmt.Errorf("line %d: %w", line, err))
			}
		}
		n++
		if n%500 == 0 {
			progress(n)
		}
	}
	if transactional {
		if err := sess.Commit(ctx); err != nil {
			return fail(err)
		}
	}
	return n, nil
}

func (a *App) importView(c *ui.Context) {
	x := a.importing
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	options := []string{skipColumn}
	for _, col := range x.columns {
		options = append(options, col.Name)
	}
	ui.DialogBase(c, &x.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.4))
		panel.Width(760).MaxHeightPercent(90).Radius(12).Background(th.Background).Border(1, th.Border).Clip().Label("Import CSV")
		ui.Column(c).Padding(16, 20).Gap(12).Children(func() {
			ui.Text(c, "Import into "+x.obj.Schema+"."+x.obj.Name).FontSize(15).Bold()
			ui.Text(c, x.path).Font(widgets.MonoFont).FontSize(12).TextColor(pal.Muted).SingleLine()
			ui.Row(c).Gap(12).Children(func() {
				ui.Text(c, "Delimiter")
				if ui.Select(c, &x.delimiter, []string{",", ";", "\\t", "|"}).Label("Delimiter").Changed() {
					x.readPreview()
				}
				ui.Checkbox(c, &x.emptyNull, "Empty cells are NULL")
			})
			if x.columns == nil && x.err == "" {
				ui.Spinner(c)
			}
			if len(x.header) > 0 {
				ui.Text(c, "Columns: each CSV column fills the table column chosen below it.").FontSize(12).TextColor(pal.Muted)
				ui.ScrollHorizontal(c).FillWidth().Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
						for i, h := range x.header {
							ui.Column(c).Width(150).Gap(4).Children(func() {
								ui.Text(c, h).Bold().FontSize(12).SingleLine()
								ui.Select(c, &x.mapping[i], options).Label("Column for " + h)
								for _, rec := range x.preview {
									v := ""
									if i < len(rec) {
										v = rec[i]
									}
									ui.Text(c, widgets.OneLine(v, 40)).Font(widgets.MonoFont).FontSize(11).TextColor(pal.Muted).SingleLine()
								}
							})
						}
					})
				})
			}
			if x.err != "" {
				ui.Text(c, x.err).TextColor(th.Danger).Selectable()
			}
			ui.Row(c).Gap(8).Children(func() {
				if x.running {
					ui.Spinner(c).Size(14, 14)
					ui.Text(c, fmt.Sprintf("%d rows…", x.done)).TextColor(pal.Muted)
				}
				ui.Spacer(c)
				if x.running {
					if ui.Button(c, "Cancel").Clicked() && x.cancel != nil {
						x.cancel()
					}
					return
				}
				if ui.Button(c, "Cancel").Clicked() {
					x.open = false
				}
				if ui.PrimaryButton(c, "Import").Disabled(len(x.header) == 0).Clicked() {
					a.confirmImport(x)
				}
			})
		})
	})
	if !x.open && !x.running {
		a.importing = nil
	}
}

// confirmImport puts the import through the safety policy, as an INSERT.
func (a *App) confirmImport(x *importState) {
	stmt := "INSERT INTO " + db.QualifiedName(x.conn.DB.Dialect, x.obj.Schema, x.obj.Name) + " …"
	v := safety.ReviewSQL(&x.conn.Config, safety.Analyze(&x.conn.Config, []string{stmt}))
	if v.Blocked != "" {
		a.RecordBlocked(x.conn, v.Blocked, stmt)
		x.err = v.Blocked
		return
	}
	if v.Confirm {
		a.AskConfirm(x.conn, v, "Import "+baseName(x.path)+" into "+x.obj.Name+"?", "Import", stmt, func() { a.runImport(x) })
		return
	}
	a.runImport(x)
}
