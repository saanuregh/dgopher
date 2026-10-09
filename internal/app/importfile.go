package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/fileimport"
	"dgopher/internal/redact"
	"dgopher/internal/safety"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// importState is the dialog importing a data file into a table: one
// there, or one it creates from the file's columns.
type importState struct {
	open     bool
	conn     *connection.Conn
	database string
	schema   string
	// obj is the table the rows go into, nil to create one named table.
	obj     *db.Object
	columns []db.Column // obj's
	table   string

	path      string
	opt       fileimport.Options
	file      *fileimport.File
	loading   bool
	preview   [][]any
	total     int64
	delimiter string // as the dialog shows it
	header    bool
	xmlRows   string // as typed, applied on Enter

	mapping []string    // into a table there: for each file column, the column it fills, or skipColumn
	newCols []newColumn // into a new table: each file column as a column of it

	running bool
	done    int64
	cancel  context.CancelFunc
	err     string
}

// newColumn is a file's column as a column of the table an import
// creates.
type newColumn struct {
	keep       bool
	name, typ  string
	fileColumn string
}

const skipColumn = "(skip)"

// delimiterChoices are the delimiters a CSV file's dialog offers.
var delimiterChoices = []string{",", ";", "Tab", "|"}

// openImport imports a file into a table, or, with obj nil, into a new
// table of schema: it asks for the file first.
func (a *App) openImport(cn *connection.Conn, database, schema string, obj *db.Object) {
	if refuseReadOnly(a, cn) {
		return
	}
	title := "Import into a new table"
	if obj != nil {
		title = "Import into " + obj.Name
	}
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: title,
			Filters: []mygo.FileFilter{{Name: "Data files", Extensions: fileimport.Extensions}}})
		if err != nil || len(paths) == 0 {
			return
		}
		a.Post(func() { a.startImport(cn, database, schema, obj, paths[0]) })
	}()
}

// openImportOf imports a file already chosen, as one dropped on the
// window.
func (a *App) openImportOf(cn *connection.Conn, database, schema string, obj *db.Object, path string) {
	if !refuseReadOnly(a, cn) {
		a.startImport(cn, database, schema, obj, path)
	}
}

// refuseReadOnly says so and reports true when a connection is read-only.
func refuseReadOnly(a *App, cn *connection.Conn) bool {
	if cn.Config.ReadOnly {
		a.ShowError("Not allowed", cn.Config.Name+" is read-only.")
	}
	return cn.Config.ReadOnly
}

func (a *App) startImport(cn *connection.Conn, database, schema string, obj *db.Object, path string) {
	x := &importState{open: true, conn: cn, database: database, schema: schema, obj: obj, path: path, header: true}
	if obj != nil {
		x.schema = obj.Schema
		connection.LoadColumns(a, cn, database, obj.Schema, obj.Name, func(cols []db.Column) {
			x.columns = cols
			x.mapColumns()
		})
	} else {
		x.table = tableNameOf(path)
	}
	a.importing = x
	a.readImportFile(x)
}

// tableNameOf names a table after a file: its name, without its extension,
// in letters, digits and underscores.
func tableNameOf(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	name := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return '_'
	}, base)
	name = strings.Trim(name, "_")
	if name == "" || unicode.IsDigit([]rune(name)[0]) {
		name = "imported_" + name
	}
	return strings.TrimRight(name, "_")
}

// readImportFile reads the file as the dialog's options say, off the
// main thread: its columns, first rows and count.
func (a *App) readImportFile(x *importState) {
	x.loading, x.err = true, ""
	path, opt := x.path, x.opt
	a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		f, err := fileimport.Open(ctx, path, opt)
		var preview [][]any
		var total int64
		if err == nil {
			preview, err = f.Preview(ctx, 8)
		}
		if err == nil {
			total, err = f.Count(ctx)
		}
		return func() {
			if a.importing != x || !x.open {
				if f != nil {
					f.Close()
				}
				return
			}
			x.loading = false
			if err != nil {
				if f != nil {
					f.Close()
				}
				x.err = err.Error()
				return
			}
			if x.file != nil {
				x.file.Close()
			}
			x.file, x.preview, x.total = f, preview, total
			x.header, x.xmlRows = f.HasHeader, f.Rows
			x.delimiter = delimiterLabel(f.Delimiter)
			x.mapColumns()
		}
	})
}

func delimiterLabel(r rune) string {
	if r == '\t' {
		return "Tab"
	}
	return string(r)
}

// mapColumns maps the file's columns to the table's by name, ignoring
// case; for a new table, each becomes a column of the engine's type.
func (x *importState) mapColumns() {
	if x.file == nil {
		return
	}
	if x.obj == nil {
		x.newCols = make([]newColumn, len(x.file.Columns))
		for i, c := range x.file.Columns {
			x.newCols[i] = newColumn{keep: true, name: c.Name, typ: db.ColumnType(x.conn.Config.Engine, c.Type), fileColumn: c.Name}
		}
		return
	}
	if x.columns == nil {
		return
	}
	x.mapping = make([]string, len(x.file.Columns))
	for i, fc := range x.file.Columns {
		x.mapping[i] = skipColumn
		for _, c := range x.columns {
			if strings.EqualFold(strings.TrimSpace(fc.Name), c.Name) {
				x.mapping[i] = c.Name
			}
		}
	}
}

// plan is what the import writes: the columns it fills, by the index of
// the file's column that fills each, and the statement creating the
// table, "" for a table there.
func (x *importState) plan() (cols []db.Column, idx []int, create string, err error) {
	if x.file == nil {
		return nil, nil, "", errors.New("the file is not read yet")
	}
	if x.obj != nil {
		seen := map[string]bool{}
		for i, m := range x.mapping {
			if m == skipColumn || m == "" {
				continue
			}
			if seen[m] {
				return nil, nil, "", fmt.Errorf("the column %s is filled from two of the file's columns", m)
			}
			seen[m] = true
			for _, c := range x.columns {
				if c.Name == m {
					cols = append(cols, c)
					idx = append(idx, i)
				}
			}
		}
		if len(cols) == 0 {
			return nil, nil, "", errors.New("map at least one of the file's columns to a column of the table")
		}
		return cols, idx, "", nil
	}
	if strings.TrimSpace(x.table) == "" {
		return nil, nil, "", errors.New("name the new table")
	}
	var defs []db.NewColumn
	seen := map[string]bool{}
	for i, c := range x.newCols {
		if !c.keep {
			continue
		}
		name, typ := strings.TrimSpace(c.name), strings.TrimSpace(c.typ)
		if name == "" || typ == "" {
			return nil, nil, "", fmt.Errorf("the file's column %s needs a name and a type", c.fileColumn)
		}
		if seen[strings.ToLower(name)] {
			return nil, nil, "", fmt.Errorf("two columns are named %s", name)
		}
		seen[strings.ToLower(name)] = true
		defs = append(defs, db.NewColumn{Name: name, Type: typ})
		cols = append(cols, db.Column{Name: name, Type: typ})
		idx = append(idx, i)
	}
	if len(cols) == 0 {
		return nil, nil, "", errors.New("keep at least one column")
	}
	return cols, idx, db.CreateTableSQL(x.conn.DB.Dialect, x.schema, strings.TrimSpace(x.table), defs), nil
}

// targetName is the table the rows go into.
func (x *importState) targetName() string {
	if x.obj != nil {
		return x.obj.Name
	}
	return strings.TrimSpace(x.table)
}

// confirmImport puts the import through the safety policy: an INSERT,
// after a CREATE TABLE for a new table.
func (a *App) confirmImport(x *importState) {
	_, _, create, err := x.plan()
	if err != nil {
		x.err = err.Error()
		return
	}
	insert := "INSERT INTO " + db.QualifiedName(x.conn.DB.Dialect, x.schema, x.targetName()) + " …"
	stmts := []string{insert}
	preview := insert
	if create != "" {
		stmts = []string{create, insert}
		preview = create + ";\n\n" + insert
	}
	v := safety.ReviewSQL(&x.conn.Config, safety.Analyze(&x.conn.Config, stmts))
	if v.Blocked != "" {
		a.RecordBlocked(x.conn, v.Blocked, preview)
		x.err = v.Blocked
		return
	}
	if v.Confirm {
		a.AskConfirm(x.conn, v, "Import "+filepath.Base(x.path)+" into "+x.targetName()+"?", "Import", preview, func() { a.runImport(x) })
		return
	}
	a.runImport(x)
}

// runImport imports the file off the main thread, all or nothing where
// the database allows.
func (a *App) runImport(x *importState) {
	cols, idx, create, err := x.plan()
	if err != nil {
		x.err = err.Error()
		return
	}
	cn := x.conn
	target := &db.EditTarget{Dialect: cn.DB.Dialect, Schema: x.schema, Table: x.targetName(), Columns: cols}
	ctx, cancel := context.WithCancel(context.Background())
	x.running, x.cancel, x.done, x.err = true, cancel, 0, ""
	f, pool, cfg, database, path := x.file, cn.DB, cn.Config, x.database, x.path
	started := time.Now()
	go func() {
		defer cancel()
		fileCols := f.Columns
		n, err := writeRows(ctx, a, pool, cfg, database, rowWrite{target: target, cols: cols, create: create, verb: "imported",
			read: func(ctx context.Context, batch int, emit func([][]any) error) error {
				_, err := f.Read(ctx, batch, func(rows [][]any) error {
					values := make([][]any, len(rows))
					for r, row := range rows {
						vals := make([]any, len(cols))
						for i, j := range idx {
							vals[i] = db.InsertValue(cfg.Engine, row[j], fileCols[j].Type)
						}
						values[r] = vals
					}
					return emit(values)
				})
				return err
			},
			progress: func(done int64) { a.Post(func() { x.done = done }) }})
		ev := audit.Event{Kind: audit.KindImport, Database: database, Rows: n,
			Statement: "INSERT INTO " + target.Table, Detail: "imported from " + path}
		if err != nil {
			ev.Error = err.Error()
		}
		a.Record(&cfg, ev)
		a.Post(func() {
			x.running = false
			if err != nil {
				x.err = err.Error()
				a.Notify(started, "Import failed", target.Table+" · "+redact.Secrets(widgets.FirstLine(err.Error())), nil)
				return
			}
			x.open = false
			if create != "" {
				// The navigator reads the schema again, the new table in it.
				cn.ForgetCatalog()
			}
			a.toast = &pendingToast{text: fmt.Sprintf("Imported %d rows into %s", n, target.Table)}
			a.Notify(started, "Import finished", fmt.Sprintf("%s into %s", widgets.Count(n, "row"), target.Table), nil)
			a.reloadTableTabs(cn, x.schema, target.Table)
		})
	}()
}

// reloadTableTabs reads again the rows of a table's open tabs, after
// rows were written into it.
func (a *App) reloadTableTabs(cn *connection.Conn, schema, table string) {
	for _, t := range a.everyTab() {
		if tt, ok := t.(*dataview.TableTab); ok && tt.Conn == cn && tt.Object.Name == table && tt.Object.Schema == schema {
			tt.RequestReload()
		}
	}
}

// rowSource hands a write its rows, as the INSERT takes them, at most n
// at a time, until it returns.
type rowSource func(ctx context.Context, n int, emit func(rows [][]any) error) error

// rowWrite is what writeRows writes: rows into a table, which create
// makes first when it is new, and empty empties first when its rows are
// to go. verb says what was done, in the errors, as "imported".
type rowWrite struct {
	target   *db.EditTarget
	cols     []db.Column
	create   string
	empty    string
	verb     string
	read     rowSource
	progress func(int64)
}

// writeRows writes rows into a table of a database, in multi-row INSERTs
// on a session of its own, after creating the table when create is set.
// In one transaction where the database has them: a failing row leaves
// nothing, the table created included. Where creating a table commits, on
// MySQL, or nothing is transactional, on ClickHouse, a failure drops the
// table it created; on ClickHouse rows written into a table there stay.
func writeRows(ctx context.Context, a *App, pool *db.DB, cfg db.Config, database string, w rowWrite) (int64, error) {
	if pool == nil {
		return 0, errors.New("not connected")
	}
	d, err := pool.Database(ctx, database)
	if err != nil {
		return 0, err
	}
	sess, err := d.Session(ctx)
	if err != nil {
		return 0, err
	}
	defer sess.Close()
	engine, target := cfg.Engine, w.target
	transactional := engine != db.ClickHouse
	// A database of one connection shares its transaction with every
	// editor: never write into, or roll back, someone else's.
	if transactional && sess.Tx() != db.TxNone {
		return 0, errors.New("a transaction is open on this database: commit or roll it back first")
	}
	exec := func(sql string, args ...any) error {
		start := time.Now()
		_, err := sess.Exec(ctx, sql, args...)
		if !strings.HasPrefix(sql, "INSERT") {
			a.RecordRun(cfg, audit.KindStatement, database, sql, -1, time.Since(start), err)
		}
		return err
	}
	ddlCommits := w.create != "" && (engine == db.MySQL || engine == db.ClickHouse)
	if ddlCommits {
		if err := exec(w.create); err != nil {
			return 0, err
		}
	}
	if transactional {
		if err := sess.Begin(ctx); err != nil {
			return 0, err
		}
	}
	var n int64
	fail := func(err error) (int64, error) {
		if ctx.Err() != nil {
			err = errors.New("cancelled")
		}
		rctx, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		if transactional {
			sess.Rollback(rctx)
			n = 0
		}
		if ddlCommits {
			// Only the table this write created, and nothing was in it.
			drop := "DROP TABLE " + db.QualifiedName(target.Dialect, target.Schema, target.Table)
			_, derr := sess.Exec(rctx, drop)
			a.RecordRun(cfg, audit.KindStatement, database, drop, -1, 0, derr)
			n = 0
		}
		switch {
		case n > 0:
			return n, fmt.Errorf("%w\n\nClickHouse has no transactions: the %d rows before stay in %s", err, n, target.Table)
		case w.create != "":
			return 0, fmt.Errorf("%w\n\nNothing was %s, and the table was not created", err, w.verb)
		}
		return 0, fmt.Errorf("%w\n\nNothing was %s: the transaction was rolled back", err, w.verb)
	}
	if w.create != "" && !ddlCommits {
		if err := exec(w.create); err != nil {
			return fail(err)
		}
	}
	if w.empty != "" {
		if err := exec(w.empty); err != nil {
			return fail(err)
		}
	}
	batch := db.InsertBatch(engine, len(w.cols))
	err = w.read(ctx, batch, func(rows [][]any) error {
		st := target.InsertRows(w.cols, rows)
		if err := exec(st.SQL, st.Args...); err != nil {
			return fmt.Errorf("rows %d to %d: %w", n+1, n+int64(len(rows)), err)
		}
		n += int64(len(rows))
		w.progress(n)
		return nil
	})
	if err != nil {
		return fail(err)
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
	ui.DialogBase(c, &x.open, func(backdrop, panel ui.Element) {
		backdrop.Background(widgets.Backdrop)
		_, winH := c.Size()
		panel.Width(860).MaxHeight(winH-40).Radius(12).Background(th.Background).Border(1, th.Border).Clip().Label("Import")
		ui.Column(c).Padding(16, 20).Gap(12).Children(func() {
			title := "Import " + filepath.Base(x.path) + " into "
			if x.obj != nil {
				title += x.schema + "." + x.obj.Name
			} else {
				title += "a new table"
			}
			ui.Text(c, title).FontSize(15).Bold()
			format, _ := fileimport.FormatOf(x.path)
			ui.Row(c).Gap(8).Children(func() {
				ui.Badge(c, format.Label())
				ui.Text(c, x.path).Font(widgets.MonoFont).FontSize(12).TextColor(pal.Muted).SingleLine().Shrink(1)
			})
			a.importOptions(c, x, format)
			if x.obj == nil {
				ui.Form(c, func() {
					ui.Field(c, "New table", func() {
						ui.Row(c).Gap(6).Children(func() {
							if x.schema != "" {
								ui.Text(c, x.schema+".").Font(widgets.MonoFont).TextColor(pal.Muted)
							}
							ui.TextInput(c, &x.table).Font(widgets.MonoFont).Grow(1).Label("Table name")
						})
					})
				})
			}
			switch {
			case x.loading:
				ui.Row(c).Gap(8).Children(func() {
					ui.Spinner(c).Size(14, 14)
					ui.Text(c, "Reading the file…").TextColor(pal.Muted)
				})
			case x.file != nil:
				a.importColumns(c, x)
			}
			if x.err != "" {
				ui.Text(c, x.err).TextColor(th.Danger).Selectable()
			}
			ui.Row(c).Gap(8).Children(func() {
				switch {
				case x.running:
					ui.Spinner(c).Size(14, 14)
					ui.Text(c, fmt.Sprintf("%d of %d rows…", x.done, x.total)).TextColor(pal.Muted)
				case x.file != nil:
					ui.Text(c, widgets.Count(x.total, "row")).TextColor(pal.Muted)
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
				if ui.PrimaryButton(c, "Import").Disabled(x.file == nil || x.loading).Clicked() {
					a.confirmImport(x)
				}
			})
		})
	})
	if !x.open && !x.running {
		if x.file != nil {
			x.file.Close()
		}
		a.importing = nil
	}
}

// importOptions shows how the file is read, which the user may change:
// the file is read again.
func (a *App) importOptions(c *ui.Context, x *importState, format fileimport.Format) {
	reread := false
	ui.Row(c).Gap(14).Wrap().Children(func() {
		switch format {
		case fileimport.CSV:
			ui.Row(c).Gap(6).Children(func() {
				ui.Text(c, "Delimiter")
				if ui.Select(c, &x.delimiter, delimiterChoices).Label("Delimiter").Changed() {
					x.opt.Delimiter = []rune(strings.Replace(x.delimiter, "Tab", "\t", 1))[0]
					reread = true
				}
			})
		case fileimport.Excel:
			if x.file != nil && len(x.file.Sheets) > 1 {
				sheet := x.file.Sheet
				ui.Row(c).Gap(6).Children(func() {
					ui.Text(c, "Sheet")
					if ui.Select(c, &sheet, x.file.Sheets).Label("Sheet").Changed() {
						x.opt.Sheet, reread = sheet, true
					}
				})
			}
		case fileimport.XML:
			ui.Row(c).Gap(6).Children(func() {
				ui.Text(c, "Rows are the elements")
				if ui.TextInput(c, &x.xmlRows).Font(widgets.MonoFont).Width(260).Label("Row elements").Submitted() {
					x.opt.Rows, reread = x.xmlRows, true
				}
			})
		}
		if format == fileimport.CSV || format == fileimport.Excel {
			if ui.Checkbox(c, &x.header, "The first row names the columns").Changed() {
				x.opt.Header = fileimport.HeaderNone
				if x.header {
					x.opt.Header = fileimport.HeaderFirstRow
				}
				reread = true
			}
		}
		if format == fileimport.CSV || format == fileimport.Excel || format == fileimport.XML {
			if ui.Checkbox(c, &x.opt.AllText, "Read every column as text").Changed() {
				reread = true
			}
		}
	})
	if reread && !x.running {
		a.readImportFile(x)
	}
}

// importColumns shows each of the file's columns with its first values,
// and what it becomes: the table's column it fills, or a column of the
// new table, with the statement creating it.
func (a *App) importColumns(c *ui.Context, x *importState) {
	pal := widgets.PaletteOf(c)
	options := []string{skipColumn}
	for _, col := range x.columns {
		options = append(options, col.Name)
	}
	what := "Each of the file's columns fills the table's column chosen below it."
	if x.obj == nil {
		what = "Each of the file's columns kept becomes a column of the new table, with the name and type below it."
	}
	ui.Text(c, what).FontSize(12).TextColor(pal.Muted)
	ui.ScrollHorizontal(c).FillWidth().Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Start).Padding(0, 0, 6, 0).Children(func() {
			for i, fc := range x.file.Columns {
				ui.Column(c).Width(170).Gap(4).Children(func() {
					ui.Text(c, fc.Name).Bold().FontSize(12).SingleLine().Tooltip(fc.Name)
					ui.Text(c, fc.Type).FontSize(11).TextColor(pal.Muted).SingleLine()
					switch {
					case x.obj != nil && i < len(x.mapping):
						ui.Select(c, &x.mapping[i], options).Label("Column for " + fc.Name)
					case x.obj == nil && i < len(x.newCols):
						nc := &x.newCols[i]
						ui.Checkbox(c, &nc.keep, "Keep").Label("Keep " + fc.Name)
						ui.TextInput(c, &nc.name).Font(widgets.MonoFont).FontSize(12).Disabled(!nc.keep).Label("Name of " + fc.Name)
						ui.TextInput(c, &nc.typ).Font(widgets.MonoFont).FontSize(12).Disabled(!nc.keep).Label("Type of " + fc.Name)
					}
					for _, row := range x.preview {
						v := "NULL"
						if row[i] != nil {
							v = fmt.Sprint(db.ValueText(row[i], fc.Type))
						}
						ui.Text(c, widgets.OneLine(v, 40)).Font(widgets.MonoFont).FontSize(11).TextColor(pal.Muted).SingleLine()
					}
				})
			}
		})
	})
	if x.obj == nil {
		if _, _, create, err := x.plan(); err == nil {
			ui.Text(c, create+";").Font(widgets.MonoFont).FontSize(11.5).Padding(8, 10).Radius(6).Background(pal.Hover).Selectable().MaxHeight(160)
		}
	}
}
