package dataview

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/export"
	"dgopher/internal/redact"
	"dgopher/internal/safety"
	"dgopher/internal/settings"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// ExportSource is what an export writes: the rows of a statement it runs
// again on a session of its own, or the rows already read.
type ExportSource struct {
	Conn     *connection.Conn
	Database string
	// Name names the files, and the table of SQL INSERTs.
	Name string
	SQL  string
	Args []any
	Cols []db.ColumnInfo
	// RowsRead are the rows read so far, nil when there are none.
	RowsRead func() [][]any
	Read     int
	// OnSession, when set, lends the session the rows came from, for the
	// statement to run again where it sees that session's schema,
	// temporary tables and transaction, as the rows shown did. It returns
	// the session and what to call once done with it; or a nil session to
	// run on one of the export's own, as before any statement made one;
	// or why it cannot lend it now.
	OnSession func() (sess *db.Session, done func(), why string)
}

// exportState is the export dialog.
type exportState struct {
	open bool
	src  ExportSource
	// rerun is why the statement cannot run again, "" when it can.
	rerun string
	all   bool // every row, running the statement again; else the rows read
	// limited stops a statement run again after limit rows.
	limited bool
	limit   float64

	format    string
	delim     int
	quote     int // the quote character: 0 for ", 1 for '
	quoteAll  bool
	header    bool
	nullText  string
	bom       bool
	table     string
	perInsert float64

	clipboard  bool
	folder     string
	pattern    string
	openFolder bool

	// tables, when set, are the tables of a schema to choose from, each
	// exported to a file of its own; src is the first.
	tables []exportTable
	title  string
	filter string

	running bool
	written atomic.Int64
	current atomic.Int64 // the index of the source being exported
	cancel  context.CancelFunc
	err     string
}

// exportTable is a table the user may choose to export.
type exportTable struct {
	src    ExportSource
	chosen bool
}

// chosen is the sources of the tables chosen.
func (x *exportState) chosen() []ExportSource {
	var out []ExportSource
	for _, t := range x.tables {
		if t.chosen {
			out = append(out, t.src)
		}
	}
	return out
}

const defaultExportPattern = "${table}_${timestamp}"

func OpenExport(a Host, src ExportSource) {
	p := a.Settings().Export
	x := &exportState{open: true, src: src, format: p.Format, folder: p.Folder, pattern: p.Pattern, openFolder: p.OpenFolder,
		header: true, table: src.Name, perInsert: 1, delim: delimiterIndex(','), limited: p.Limit() > 0, limit: settings.DefaultExportRowLimit}
	if p.Limit() > 0 {
		x.limit = float64(p.Limit())
	}
	if x.format == "" {
		x.format = string(export.CSV)
	}
	if x.pattern == "" {
		x.pattern = defaultExportPattern
	}
	if x.folder == "" {
		if home, err := os.UserHomeDir(); err == nil {
			x.folder = filepath.Join(home, "Downloads")
		}
	}
	x.rerun = rerunReason(src)
	x.all = x.rerun == ""
	x.title = "Export " + src.Name
	a.Dialogs().export = x
}

// OpenExportTables opens the export of tables, each a source reading all
// its rows, to choose from: those chosen is true for are chosen first.
func OpenExportTables(a Host, title string, srcs []ExportSource, chosen []bool) {
	if len(srcs) == 0 {
		return
	}
	OpenExport(a, srcs[0])
	x := a.Dialogs().export
	x.title = title
	x.tables = make([]exportTable, len(srcs))
	for i, src := range srcs {
		x.tables[i] = exportTable{src: src, chosen: chosen[i]}
	}
}

// rerunReason says why an export cannot run its statement again for
// every row: only a read may run again unasked.
func rerunReason(src ExportSource) string {
	if strings.TrimSpace(src.SQL) == "" {
		return "There is no statement to run again."
	}
	d := safety.Dialect(src.Conn.Config.Engine)
	if len(sqltext.SplitWith(safety.PolicyText(src.Conn.Config.Engine, src.SQL), d, sqltext.SplitOptions{Mode: sqltext.SemicolonOnly})) != 1 {
		return "It holds more than one statement: it is not run again for an export."
	}
	an := safety.Analyze(&src.Conn.Config, []string{src.SQL})
	if an[0].Analysis.Class != sqltext.Read {
		return an[0].Analysis.Verb + " changes the database: it is not run again for an export."
	}
	return ""
}

// expandPattern names an export's file from its pattern.
func expandPattern(pattern, table, connection string, now time.Time) string {
	clean := func(s string) string {
		return strings.Map(func(r rune) rune {
			if strings.ContainsRune(`/\:*?"<>|`, r) || r < ' ' {
				return '_'
			}
			return r
		}, s)
	}
	name := strings.NewReplacer("${table}", table, "${connection}", connection,
		"${timestamp}", now.Format("20060102-150405"), "${date}", now.Format("2006-01-02")).Replace(pattern)
	return clean(name)
}

func (x *exportState) options() export.Options {
	opt := export.Options{Header: x.header, NullText: x.nullText, BOM: x.bom, Table: x.table, RowsPerInsert: int(x.perInsert),
		Literal: literalOf(x.src.Conn.Config.Engine)}
	if x.src.Conn.DB != nil {
		opt.Quote = x.src.Conn.DB.Dialect.Quote
	}
	if export.Format(x.format) == export.CSV {
		opt.Delimiter = delimiterRunes[x.delim]
		opt.QuoteChar, opt.QuoteAlways = []rune{'"', '\''}[x.quote], x.quoteAll
	}
	return opt
}

// pathOf is the file a source named name is exported to.
func (x *exportState) pathOf(name string) string {
	f := export.Format(x.format)
	return filepath.Join(db.ExpandPath(x.folder), expandPattern(x.pattern, name, x.src.Conn.Config.Name, time.Now())+"."+f.Extension())
}

func ExportView(a Host, c *ui.Context) {
	x := a.Dialogs().export
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	formats := export.Formats()
	labels := make([]string, len(formats))
	label := ""
	for i, f := range formats {
		labels[i] = f.Label()
		if string(f) == x.format {
			label = labels[i]
		}
	}
	f := export.Format(x.format)
	many := x.tables != nil
	if export.NeedsFile(f) || many {
		x.clipboard = false
	}
	chosen := []ExportSource{x.src}
	if many {
		chosen = x.chosen()
	}
	example := x.src // the source the file name shows for
	if len(chosen) > 0 {
		example = chosen[0]
	}
	ui.Modal(c, &x.open, func() {
		ui.Column(c).Width(620).Gap(12).Children(func() {
			ui.Text(c, x.title).FontSize(15).Bold().SingleLine()
			if many {
				exportTablesView(c, x, len(chosen))
			}
			ui.Form(c, func() {
				ui.Field(c, "Format", func() {
					if ui.Select(c, &label, labels).Label("Format").Changed() {
						x.format = string(formats[slices.Index(labels, label)])
					}
				})
				switch f {
				case export.CSV:
					ui.Field(c, "Delimiter", func() { ui.Segmented(c, &x.delim, delimiterLabels...).Label("Delimiter") })
					ui.Field(c, "Quote", func() {
						ui.Row(c).Gap(10).Children(func() {
							ui.Segmented(c, &x.quote, `"`, "'").Label("Quote character")
							ui.Checkbox(c, &x.quoteAll, "Quote every value")
						})
					})
					fallthrough
				case export.TSV, export.Markdown:
					ui.Field(c, "", func() { ui.Checkbox(c, &x.header, "Column names first") })
					ui.Field(c, "NULL as", func() { ui.TextInput(c, &x.nullText).Placeholder("empty").Font(widgets.MonoFont) })
					ui.Field(c, "", func() { ui.Checkbox(c, &x.bom, "Byte order mark, for Excel") })
				case export.SQL:
					if !many {
						ui.Field(c, "Table", func() { ui.TextInput(c, &x.table).Font(widgets.MonoFont) })
					}
					ui.Field(c, "Rows per INSERT", func() { ui.NumberInput(c, &x.perInsert, 1, 1000, 10) })
				case export.XLSX:
					if !many {
						ui.Field(c, "Sheet", func() { ui.TextInput(c, &x.table) })
					}
					ui.Field(c, "", func() { ui.Checkbox(c, &x.header, "Column names first") })
					ui.Field(c, "NULL as", func() { ui.TextInput(c, &x.nullText).Placeholder("an empty cell").Font(widgets.MonoFont) })
				case export.Parquet, export.DuckDBFile:
					if !many {
						ui.Field(c, "Table", func() { ui.TextInput(c, &x.table).Font(widgets.MonoFont) }).Description("Written through DuckDB, with the columns' types.")
					}
				}
				if !many {
					exportRowsField(c, x)
				}
				if x.all {
					note := "Reading stops there, and the export says so."
					if many {
						note = "Reading each table stops there, and the export says so."
					}
					if f == export.XLSX {
						note += fmt.Sprintf(" An Excel sheet holds at most %d rows below its header.", export.ExcelMaxRows)
					}
					ui.Field(c, "Limit", func() {
						ui.Row(c).Gap(8).Children(func() {
							ui.Checkbox(c, &x.limited, "At most")
							ui.NumberInput(c, &x.limit, 1, 1e9, 10000).Label("Most rows").Disabled(!x.limited)
							ui.Text(c, "rows").TextColor(pal.Muted)
						})
					}).Description(note)
				}
				if !many {
					ui.Field(c, "Output", func() {
						ui.Checkbox(c, &x.clipboard, "Copy to the clipboard instead of a file").Disabled(export.NeedsFile(f))
					})
				}
				if !x.clipboard {
					ui.Field(c, "Folder", func() {
						ui.Row(c).Gap(6).Children(func() {
							ui.TextInput(c, &x.folder).Grow(1).Font(widgets.MonoFont).FontSize(12.5)
							if ui.Button(c, "Choose…").Clicked() {
								go func() {
									paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "Export to", Directory: true, CreateDirectories: true})
									if err == nil && len(paths) > 0 {
										a.Post(func() { x.folder = paths[0] })
									}
								}()
							}
						})
					})
					ui.Field(c, "File name", func() {
						ui.TextInput(c, &x.pattern).Font(widgets.MonoFont).FontSize(12.5)
					}).Description(x.pathOf(example.Name) + "   (${table}, ${connection}, ${timestamp}, ${date})")
					ui.Field(c, "", func() { ui.Checkbox(c, &x.openFolder, "Show the file when done") })
				}
			})
			if x.err != "" {
				ui.Text(c, x.err).TextColor(th.Danger).Selectable()
			}
			ui.Row(c).Gap(8).Children(func() {
				if x.running {
					ui.Spinner(c).Size(14, 14)
					progress := fmt.Sprintf("%d rows written…", x.written.Load())
					if many {
						progress = fmt.Sprintf("Table %d of %d · ", x.current.Load()+1, len(chosen)) + progress
					}
					ui.Text(c, progress).FontSize(12).TextColor(pal.Muted)
					c.After(200 * time.Millisecond)
				}
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					if x.running && x.cancel != nil {
						x.cancel()
					} else {
						x.open = false
					}
				}
				if widgets.Activated(c, ui.PrimaryButton(c, "Export").Disabled(x.running || len(chosen) == 0 || !x.all && x.src.RowsRead == nil)) {
					runExport(a, x)
				}
			})
		})
	})
	if !x.open && a.Dialogs().export == x {
		if x.cancel != nil {
			x.cancel()
		}
		a.Dialogs().export = nil
	}
}

// exportTablesView chooses the tables to export.
func exportTablesView(c *ui.Context, x *exportState, chosen int) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	filter := strings.ToLower(strings.TrimSpace(x.filter))
	var shown []int
	for i, t := range x.tables {
		if filter == "" || strings.Contains(strings.ToLower(t.src.Name), filter) {
			shown = append(shown, i)
		}
	}
	ui.Row(c).Gap(8).Children(func() {
		widgets.SearchBox(c, &x.filter, "Filter tables", 0).Grow(1)
		if ui.Button(c, "All").Disabled(x.running).Clicked() {
			for _, i := range shown {
				x.tables[i].chosen = true
			}
		}
		if ui.Button(c, "None").Disabled(x.running).Clicked() {
			for _, i := range shown {
				x.tables[i].chosen = false
			}
		}
	})
	ui.Scroll(c).Height(180).Border(1, th.Border).Radius(6).Children(func() {
		ui.Column(c).Padding(4).Children(func() {
			for _, i := range shown {
				t := &x.tables[i]
				ui.Checkbox(c.Key(fmt.Sprint("table-", i)), &t.chosen, t.src.Name).Disabled(x.running).Padding(3, 6)
			}
		})
	})
	ui.Text(c, fmt.Sprintf("%d of %d chosen, each to a file of its own", chosen, len(x.tables))).FontSize(12).TextColor(pal.Muted)
}

// exportRowsField chooses between running the statement again and the
// rows read.
func exportRowsField(c *ui.Context, x *exportState) {
	pal := widgets.PaletteOf(c)
	ui.Field(c, "Rows", func() {
		ui.Column(c).Gap(4).Children(func() {
			where := "on a session of its own"
			if x.src.OnSession != nil {
				where = "on the editor's session, which sees what the rows shown saw"
			}
			ui.Radio(c, &x.all, true, "Run the statement again, "+where).Disabled(x.rerun != "")
			if x.src.RowsRead != nil {
				ui.Radio(c, &x.all, false, fmt.Sprintf("The %d rows read", x.src.Read))
			}
			if x.rerun != "" {
				ui.Text(c, x.rerun).FontSize(12).TextColor(pal.Muted)
			}
		})
	})
}

// exportPage is how many rows an export reads at a time.
const exportPage = 5000

// runExport writes the rows, off the main thread, and audits it: of the
// source, or of each table chosen, one file each.
func runExport(a Host, x *exportState) {
	prefs := settings.ExportPrefs{Format: x.format, Folder: x.folder, Pattern: x.pattern, OpenFolder: x.openFolder, Unlimited: !x.limited}
	if x.limited {
		prefs.RowLimit = int(x.limit)
	}
	a.Settings().Export = prefs
	a.SaveSettings()
	srcs := []ExportSource{x.src}
	if x.tables != nil {
		srcs = x.chosen()
	}
	f, toClip := export.Format(x.format), x.clipboard
	paths := make([]string, len(srcs))
	for i, src := range srcs {
		if toClip {
			continue
		}
		paths[i] = x.pathOf(src.Name)
		if _, err := os.Stat(paths[i]); err == nil {
			x.err = paths[i] + " exists already: change the file name"
			return
		}
	}
	cfg, all := x.src.Conn.Config, x.all
	var sess *db.Session
	release := func() {}
	if all && x.src.OnSession != nil {
		s, done, why := x.src.OnSession()
		if why != "" {
			x.err = "Not exported: " + why
			return
		}
		if s != nil {
			sess, release = s, done
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	x.running, x.cancel, x.err = true, cancel, ""
	x.written.Store(0)
	x.current.Store(0)
	started := time.Now()
	limit := 0
	if all && x.limited {
		limit = int(x.limit)
	}
	pool, opt := x.src.Conn.DB, x.options()
	var read [][]any
	if !all {
		read = x.src.RowsRead()
	}
	a.Background(func() func() {
		defer cancel()
		defer release()
		var clip strings.Builder
		var stopped []string // the sources the limit stopped, with rows left
		var err error
		for i, src := range srcs {
			x.current.Store(int64(i))
			o := opt
			if x.tables != nil {
				o.Table = src.Name
			}
			var n int64
			var limited bool
			n, limited, err = exportOne(ctx, pool, sess, src, all, read, f, o, paths[i], &clip, limit, &x.written)
			ev := audit.Event{Kind: audit.KindExport, Statement: src.SQL, Rows: n, Detail: f.Label() + " to " + paths[i]}
			if toClip {
				ev.Detail = f.Label() + " to the clipboard"
			}
			switch {
			case !all:
				ev.Detail += ", the rows read"
			case sess != nil:
				ev.Detail += ", run again on the editor's session"
			}
			if limited {
				stopped = append(stopped, src.Name)
				ev.Detail += ", stopped at its limit of " + strconv.Itoa(limit) + " rows"
			}
			if err != nil {
				ev.Error = err.Error()
				if len(srcs) > 1 {
					err = fmt.Errorf("%s: %w", src.Name, err)
				}
			}
			a.Record(&cfg, ev)
			if err != nil {
				break
			}
		}
		n := x.written.Load()
		return func() {
			x.running = false
			name := x.src.Name
			if len(srcs) > 1 {
				name = fmt.Sprintf("%d tables", len(srcs))
			}
			if err != nil {
				x.err = "The export stopped: " + err.Error()
				a.Notify(started, "Export failed", name+" · "+redact.Secrets(widgets.FirstLine(err.Error())), nil)
				return
			}
			note := ""
			switch {
			case len(stopped) == 1 && len(srcs) == 1:
				note = fmt.Sprintf(": the limit of %d rows stopped it, with rows left", limit)
			case len(stopped) > 0:
				note = fmt.Sprintf(": the limit of %d rows stopped %s, with rows left", limit, strings.Join(stopped, ", "))
			}
			a.Notify(started, "Export finished", fmt.Sprintf("%d rows of %s%s", n, name, note), nil)
			x.open = false
			if toClip {
				a.WriteClipboard(clip.String())
				a.Toast(fmt.Sprintf("Copied %d rows as %s%s", n, f.Label(), note), "", nil)
				return
			}
			what := fmt.Sprintf("Exported %d rows%s", n, note)
			if len(srcs) > 1 {
				what = fmt.Sprintf("Exported %d rows of %d tables%s", n, len(srcs), note)
			}
			last := paths[len(paths)-1]
			a.Toast(what, "Show in Folder", func() { mygo.Shell.ShowItemInFolder(last) })
			if x.openFolder {
				mygo.Shell.ShowItemInFolder(last)
			}
		}
	})
}

// exportOne writes the rows of a source to a file at path, or to clip
// when path is "", counting them in written as it goes. It stops after
// limit rows when limit is set, and says whether rows were left. A file
// it fails to finish is removed.
func exportOne(ctx context.Context, pool *db.DB, sess *db.Session, src ExportSource, all bool, read [][]any, f export.Format, opt export.Options,
	path string, clip *strings.Builder, limit int, written *atomic.Int64) (n int64, limited bool, err error) {
	cols, next, done, cut, err := exportRows(ctx, pool, sess, src, all, read)
	if err != nil {
		return 0, false, err
	}
	defer done()
	ecols := make([]export.Column, len(cols))
	names := make([]string, len(cols))
	for i, c := range cols {
		ecols[i], names[i] = export.Column{Name: c.Name, DatabaseType: c.Type}, c.Name
	}
	var w export.RowWriter
	if path == "" {
		w, err = export.NewWriter(clip, f, names, opt)
	} else {
		w, err = export.NewFileWriter(path, f, ecols, opt)
	}
	if err != nil {
		return 0, false, err
	}
	fail := func(err error) (int64, bool, error) {
		w.Close()
		if path != "" {
			os.Remove(path)
		}
		return n, false, err
	}
	for {
		page := exportPage
		if limit > 0 {
			// One row past the limit says whether rows remain.
			page = min(page, limit-int(n)+1)
		}
		rows, err := next(page)
		if limit > 0 && int(n)+len(rows) > limit {
			rows, limited = rows[:limit-int(n)], true
		}
		if err == nil {
			for _, r := range rows {
				if err = w.Write(r); err != nil {
					break
				}
			}
		}
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			return fail(err)
		}
		n += int64(len(rows))
		written.Add(int64(len(rows)))
		if limited {
			return n, true, w.Close()
		}
		if len(rows) == 0 && cut() {
			return fail(fmt.Errorf("this database keeps one connection, whose rows are read ahead up to %d: run the query with LIMIT and OFFSET in parts, or, on DuckDB, COPY (…) TO 'file'", db.MaxRows))
		}
		if len(rows) == 0 {
			return n, false, w.Close()
		}
	}
}

// exportRows gives an export its rows, at most n at a time, from the
// statement run again, on a lent session or one of its own, or from the
// rows read; an empty page ends them.
func exportRows(ctx context.Context, pool *db.DB, lent *db.Session, src ExportSource, all bool, read [][]any) ([]db.ColumnInfo, func(n int) ([][]any, error), func(), func() bool, error) {
	if !all {
		sent := false
		return src.Cols, func(int) ([][]any, error) {
			if sent {
				return nil, nil
			}
			sent = true
			return read, nil
		}, func() {}, func() bool { return false }, nil
	}
	sess := lent
	if sess == nil {
		var err error
		if sess, err = connection.OpenSession(ctx, pool, src.Database); err != nil {
			return nil, nil, nil, nil, err
		}
	} else {
		// A lent session's statement is never cancelled: that would abort
		// its transaction, or on MySQL end its connection. Closing the
		// rows stops it, once they are read to their end.
		ctx = context.Background()
	}
	c, err := sess.Query(ctx, src.SQL, src.Args...)
	if err != nil {
		if lent == nil {
			sess.Close()
		}
		return nil, nil, nil, nil, err
	}
	return c.Columns, c.Fetch, func() {
		c.Close()
		if lent == nil {
			sess.Close()
		}
	}, c.Truncated, nil
}

func literalOf(e db.Engine) export.Literal {
	switch e {
	case db.Postgres, db.DuckDB:
		return export.LiteralPostgres
	case db.MySQL:
		return export.LiteralMySQL
	case db.ClickHouse:
		return export.LiteralClickHouse
	}
	return export.LiteralStandard
}
