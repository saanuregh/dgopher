package dataview

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/export"
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
}

// exportState is the export dialog.
type exportState struct {
	open bool
	src  ExportSource
	// rerun is why the statement cannot run again, "" when it can.
	rerun string
	all   bool // every row, running the statement again; else the rows read

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

	running bool
	written atomic.Int64
	cancel  context.CancelFunc
	err     string
}

const defaultExportPattern = "${table}_${timestamp}"

func OpenExport(a Host, src ExportSource) {
	p := a.Settings().Export
	x := &exportState{open: true, src: src, format: p.Format, folder: p.Folder, pattern: p.Pattern, openFolder: p.OpenFolder,
		header: true, table: src.Name, perInsert: 1, delim: delimiterIndex(',')}
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
	a.Dialogs().export = x
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

func (x *exportState) path() string {
	f := export.Format(x.format)
	return filepath.Join(db.ExpandPath(x.folder), expandPattern(x.pattern, x.src.Name, x.src.Conn.Config.Name, time.Now())+"."+f.Extension())
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
	if export.NeedsFile(f) {
		x.clipboard = false
	}
	ui.Modal(c, &x.open, func() {
		ui.Column(c).Width(620).Gap(12).Children(func() {
			ui.Text(c, "Export "+x.src.Name).FontSize(15).Bold()
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
					ui.Field(c, "Table", func() { ui.TextInput(c, &x.table).Font(widgets.MonoFont) })
					ui.Field(c, "Rows per INSERT", func() { ui.NumberInput(c, &x.perInsert, 1, 1000, 10) })
				case export.Parquet, export.DuckDBFile:
					ui.Field(c, "Table", func() { ui.TextInput(c, &x.table).Font(widgets.MonoFont) }).Description("Written through DuckDB, with the columns' types.")
				}
				ui.Field(c, "Rows", func() {
					ui.Column(c).Gap(4).Children(func() {
						ui.Radio(c, &x.all, true, "Every row: the statement runs again on a session of its own").Disabled(x.rerun != "")
						if x.src.RowsRead != nil {
							ui.Radio(c, &x.all, false, fmt.Sprintf("The %d rows read", x.src.Read))
						}
						if x.rerun != "" {
							ui.Text(c, x.rerun).FontSize(12).TextColor(pal.Muted)
						}
					})
				})
				ui.Field(c, "Output", func() {
					ui.Checkbox(c, &x.clipboard, "Copy to the clipboard instead of a file").Disabled(export.NeedsFile(f))
				})
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
					}).Description(x.path() + "   (${table}, ${connection}, ${timestamp}, ${date})")
					ui.Field(c, "", func() { ui.Checkbox(c, &x.openFolder, "Show the file when done") })
				}
			})
			if x.err != "" {
				ui.Text(c, x.err).TextColor(th.Danger).Selectable()
			}
			ui.Row(c).Gap(8).Children(func() {
				if x.running {
					ui.Spinner(c).Size(14, 14)
					ui.Text(c, fmt.Sprintf("%d rows written…", x.written.Load())).FontSize(12).TextColor(pal.Muted)
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
				if ui.PrimaryButton(c, "Export").Disabled(x.running || !x.all && x.src.RowsRead == nil).Clicked() {
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

// runExport writes the rows, off the main thread, and audits it.
func runExport(a Host, x *exportState) {
	a.Settings().Export = settings.ExportPrefs{Format: x.format, Folder: x.folder, Pattern: x.pattern, OpenFolder: x.openFolder}
	a.SaveSettings()
	ctx, cancel := context.WithCancel(context.Background())
	x.running, x.cancel, x.err = true, cancel, ""
	x.written.Store(0)
	f, opt, toClip := export.Format(x.format), x.options(), x.clipboard
	path := ""
	if !toClip {
		path = x.path()
	}
	cfg, src, all := x.src.Conn.Config, x.src, x.all
	pool := x.src.Conn.DB
	var read [][]any
	if !all {
		read = src.RowsRead()
	}
	a.Background(func() func() {
		defer cancel()
		var clip strings.Builder
		err := func() error {
			cols, next, done, cut, err := exportRows(ctx, pool, src, all, read)
			if err != nil {
				return err
			}
			defer done()
			ecols := make([]export.Column, len(cols))
			names := make([]string, len(cols))
			for i, c := range cols {
				ecols[i], names[i] = export.Column{Name: c.Name, DatabaseType: c.Type}, c.Name
			}
			var w export.RowWriter
			if toClip {
				w, err = export.NewWriter(&clip, f, names, opt)
			} else {
				if _, statErr := os.Stat(path); statErr == nil {
					return fmt.Errorf("%s exists already: change the file name", path)
				}
				w, err = export.NewFileWriter(path, f, ecols, opt)
			}
			if err != nil {
				return err
			}
			for {
				rows, err := next()
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
					w.Close()
					if path != "" {
						os.Remove(path)
					}
					return err
				}
				x.written.Add(int64(len(rows)))
				if len(rows) == 0 && cut() {
					w.Close()
					if path != "" {
						os.Remove(path)
					}
					return fmt.Errorf("this database keeps one connection, whose rows are read ahead up to %d: run the query with LIMIT and OFFSET in parts, or, on DuckDB, COPY (…) TO 'file'", db.MaxRows)
				}
				if len(rows) == 0 {
					return w.Close()
				}
			}
		}()
		n := x.written.Load()
		ev := audit.Event{Kind: audit.KindExport, Statement: src.SQL, Rows: n, Detail: f.Label() + " to " + path}
		if toClip {
			ev.Detail = f.Label() + " to the clipboard"
		}
		if !all {
			ev.Detail += ", the rows read"
		}
		if err != nil {
			ev.Error = err.Error()
		}
		a.Record(&cfg, ev)
		return func() {
			x.running = false
			if err != nil {
				x.err = "The export stopped: " + err.Error()
				return
			}
			x.open = false
			if toClip {
				a.WriteClipboard(clip.String())
				a.Toast(fmt.Sprintf("Copied %d rows as %s", n, f.Label()), "", nil)
				return
			}
			a.Toast(fmt.Sprintf("Exported %d rows", n), "Show in Folder", func() { mygo.Shell.ShowItemInFolder(path) })
			if x.openFolder {
				mygo.Shell.ShowItemInFolder(path)
			}
		}
	})
}

// exportRows gives an export its rows page by page, from a statement run
// again on a session of its own, or from the rows read; an empty page
// ends them.
func exportRows(ctx context.Context, pool *db.DB, src ExportSource, all bool, read [][]any) ([]db.ColumnInfo, func() ([][]any, error), func(), func() bool, error) {
	if !all {
		sent := false
		return src.Cols, func() ([][]any, error) {
			if sent {
				return nil, nil
			}
			sent = true
			return read, nil
		}, func() {}, func() bool { return false }, nil
	}
	sess, err := connection.OpenSession(ctx, pool, src.Database)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	c, err := sess.Query(ctx, src.SQL, src.Args...)
	if err != nil {
		sess.Close()
		return nil, nil, nil, nil, err
	}
	return c.Columns, func() ([][]any, error) { return c.Fetch(5000) }, func() {
		c.Close()
		sess.Close()
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
