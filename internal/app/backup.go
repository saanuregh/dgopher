package app

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/safety"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// backupDialog backs up a database of a connection, or restores one.
type backupDialog struct {
	open     bool
	conn     *connection.Conn
	database string // PostgreSQL's database, MySQL's schema; "" for the connection's
	restore  bool

	format  int // an index of db.BackupFormats
	content int // a db.BackupContent
	path    string
	clean   bool // a restore drops what it makes first

	running bool
	cancel  context.CancelFunc
	lines   []string // the tool's progress, the latest last
	err     string
	done    bool
}

// backupSupported says whether a connection's databases are backed up by
// the app.
func backupSupported(e db.Engine) bool { return len(db.BackupFormats(e)) > 0 }

func (a *App) openBackup(cn *connection.Conn, database string, restore bool) {
	if restore && cn.Config.ReadOnly {
		a.ShowError("Not restored", cn.Config.Name+" is read-only.")
		return
	}
	a.backup = &backupDialog{open: true, conn: cn, database: database, restore: restore}
}

// what names the database the dialog works on.
func (b *backupDialog) what() string {
	name := b.database
	if name == "" {
		name = b.conn.Config.Database
	}
	if b.conn.Config.Engine.IsFile() || name == "" {
		return b.conn.Config.Name
	}
	return name + " of " + b.conn.Config.Name
}

// choosePath asks where the backup goes, or which one to restore.
func (a *App) choosePath(b *backupDialog) {
	e := b.conn.Config.Engine
	folder := e == db.DuckDB
	format := db.BackupFormats(e)[b.format]
	name := strings.NewReplacer("/", "_", " ", "_").Replace(b.what()) + "-" + time.Now().Format("20060102-1504")
	switch format {
	case db.BackupArchive:
		name += ".dump"
	case db.BackupSQL:
		name += ".sql"
	case db.BackupCopy:
		name += filepath.Ext(b.conn.Config.Database)
	}
	go func() {
		var path string
		var err error
		switch {
		case b.restore || folder:
			var paths []string
			paths, err = mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "Choose the Backup", Directory: folder, CreateDirectories: folder && !b.restore})
			if len(paths) > 0 {
				path = paths[0]
				if folder && !b.restore {
					path = filepath.Join(path, name) // EXPORT DATABASE makes the folder
				}
			}
		default:
			path, err = mygo.Dialog.Save(mygo.SaveDialogOptions{Title: "Back Up " + b.what(), DefaultPath: name})
		}
		if err != nil || path == "" {
			return
		}
		a.Post(func() { b.path, b.err = path, "" })
	}()
}

// restoreVerdict is what every restore into what asks, as it writes:
// always asked, the connection's name typed on production.
func restoreVerdict(cn *connection.Conn, what string) safety.Verdict {
	return safety.Verdict{Confirm: true, Reasons: []string{"Restoring writes the backup's tables and rows into " + what + "."},
		TypeName: cn.Config.Env == db.Production}
}

// startBackup backs up as the dialog says, or restores, once agreed.
func (a *App) startBackup(b *backupDialog) {
	cn := b.conn
	cfg := cn.Config
	if b.restore && cfg.Engine != db.DuckDB && !db.IsArchive(b.path) {
		// A SQL file restores as any SQL file runs, asked once, as any
		// restore is, once read through.
		b.open = false
		a.startSQLFileRun(cn, b.database, b.path, b.what())
		return
	}
	poolOf := cn.PoolFor(b.database)
	base := cn.DB
	opts := db.BackupOptions{Format: db.BackupFormats(cfg.Engine)[b.format], Content: db.BackupContent(b.content), Database: b.database, Path: b.path}
	// planned is a restore planned before it was asked, as DuckDB's is.
	run := func(planned *db.ToolRun) {
		ctx, cancel := context.WithCancel(context.Background())
		b.running, b.cancel, b.err, b.lines, b.done = true, cancel, "", nil, false
		started := time.Now()
		restore, path, clean, database := b.restore, b.path, b.clean, b.database
		job := "Backing up " + b.what() + ": stopping it leaves the backup unfinished."
		if restore {
			job = "Restoring " + filepath.Base(path) + " into " + b.what() + ": stopping it rolls it back, and nothing of the backup stays."
		}
		stopped := func() { b.running, b.cancel, b.err = false, nil, "It stopped on an internal error." }
		dataview.RunJob(a, cn, job, cancel, stopped, func() func() {
			defer cancel()
			var tool db.ToolRun
			var err error
			switch {
			case planned != nil:
				tool = *planned
			case restore:
				tool, err = db.PlanRestore(ctx, base, database, path, clean)
			default:
				tool, err = db.PlanBackup(ctx, base, opts)
			}
			if err == nil {
				switch {
				case tool.Import != "":
					var d *db.DB
					if d, err = poolOf(ctx); err == nil {
						err = db.RestoreDuckDB(ctx, d, tool)
					}
				case tool.Statement != "":
					var d *db.DB
					if d, err = poolOf(ctx); err == nil {
						_, err = d.SQL.ExecContext(ctx, tool.Statement)
					}
				default:
					err = db.RunTool(ctx, tool, func(line string) { a.Post(func() { b.addLine(line) }) })
				}
			}
			kind, detail := audit.KindBackup, opts.Format.Label()+" to "+path
			if restore {
				kind, detail = audit.KindRestore, "from "+path
			}
			ev := audit.Event{Kind: kind, Database: database, Statement: tool.Shown(), Detail: detail, DurationMS: time.Since(started).Milliseconds()}
			ev.Err = err
			a.Record(&cfg, ev)
			return func() {
				b.running, b.cancel = false, nil
				if err != nil {
					b.err = err.Error()
					a.Notify(started, "Backup failed", b.what(), nil)
					return
				}
				b.done = true
				if restore {
					cn.ForgetCatalog()
					a.Notify(started, "Restored", b.what(), nil)
					a.toast = &pendingToast{text: "Restored " + b.what()}
					return
				}
				a.Notify(started, "Backed up", b.what(), nil)
				a.toast = &pendingToast{text: "Backed up " + b.what() + " to " + filepath.Base(path)}
			}
		})
	}
	if !b.restore {
		run(nil)
		return
	}
	// A restore writes: always asked, the connection's name typed on
	// production, and refused where the policy refuses writes.
	v := restoreVerdict(cn, b.what())
	if b.clean {
		v.Reasons = append(v.Reasons, "Drop objects first: what the backup holds is dropped from "+b.what()+" before it is made again.")
	}
	title := "Restore into " + b.what() + "?"
	if cfg.Engine != db.DuckDB {
		a.AskConfirm(cn, v, title, "Restore", b.path, func() { run(nil) })
		return
	}
	// DuckDB's restore is planned first, for the statements it runs on the
	// connection to be reviewed as they will run.
	ctx, cancel := context.WithCancel(context.Background())
	b.running, b.cancel, b.err, b.lines, b.done = true, cancel, "", nil, false
	path := b.path
	dataview.BackgroundResetOnPanic(a, func() { b.running, b.cancel = false, nil }, func() func() {
		defer cancel()
		d, err := poolOf(ctx)
		var tool db.ToolRun
		if err == nil {
			tool, err = db.PlanRestore(ctx, d, "", path, false)
		}
		return func() {
			b.running, b.cancel = false, nil
			if a.backup != b || !b.open {
				return // closed meanwhile
			}
			if err != nil {
				b.err = err.Error()
				return
			}
			v2 := safety.ReviewSQL(&cfg, safety.Analyze(&cfg, tool.Statements))
			if v2.Blocked != "" {
				a.RecordBlocked(cn, v2.Blocked, tool.Shown())
				b.err = v2.Blocked
				return
			}
			v.Reasons = append(v2.Reasons, v.Reasons...)
			a.AskConfirm(cn, v, title, "Restore", path, func() { run(&tool) })
		}
	})
}

func (b *backupDialog) addLine(line string) {
	b.lines = append(b.lines, line)
	if len(b.lines) > 200 {
		b.lines = b.lines[len(b.lines)-200:]
	}
}

func (a *App) backupView(c *ui.Context) {
	b := a.backup
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	e := b.conn.Config.Engine
	formats := db.BackupFormats(e)
	note := backupNote(e, b.restore)
	if !b.restore && b.conn.DB != nil && db.MysqldumpChecksCAOnly(b.conn.DB) {
		note += " Through the tunnel or proxy, verify-full checks the server's certificate against the CA file only, not the server's name, which mysqldump cannot be told."
	}
	ui.Modal(c, &b.open, func() {
		ui.Column(c).Width(620).Gap(12).Children(func() {
			title := "Back Up " + b.what()
			if b.restore {
				title = "Restore into " + b.what()
			}
			ui.Text(c, title).FontSize(15).Bold()
			ui.Form(c, func() {
				if !b.restore {
					if len(formats) > 1 {
						labels := make([]string, len(formats))
						for i, f := range formats {
							labels[i] = f.Label()
						}
						ui.Field(c, "Format", func() { ui.Segmented(c, &b.format, labels...).Label("Format").Disabled(b.running) })
					}
					if e == db.Postgres || e == db.MySQL {
						ui.Field(c, "Keep", func() {
							ui.Segmented(c, &b.content, "Schema and Data", "Schema Only", "Data Only").Label("Keep").Disabled(b.running)
						})
					}
				} else if e == db.Postgres {
					ui.Field(c, "", func() {
						ui.Checkbox(c, &b.clean, "Drop what the backup makes before making it").Disabled(b.running)
					}).Description("A pg_dump archive restores in one transaction with pg_restore; a SQL file runs as Run SQL File runs it.")
				}
				label := "File"
				if e == db.DuckDB {
					label = "Folder"
				}
				ui.Field(c, label, func() {
					ui.Row(c).Gap(6).Children(func() {
						ui.TextInput(c, &b.path).Font(widgets.MonoFont).FontSize(12.5).Grow(1).Label(label).Disabled(b.running).AutoFocus()
						if ui.Button(c, "Choose…").Disabled(b.running).Clicked() {
							a.choosePath(b)
						}
					})
				}).Description(note)
			})
			if len(b.lines) > 0 {
				ui.Scroll(c).MaxHeight(160).Radius(6).Background(pal.EditorBg).Children(func() {
					ui.Text(c, strings.Join(b.lines, "\n")).Font(widgets.MonoFont).FontSize(11.5).Padding(8, 10).Selectable()
				})
			}
			if b.err != "" {
				ui.Text(c, b.err).TextColor(th.Danger).Selectable()
			}
			ui.Row(c).Gap(8).Children(func() {
				if b.running {
					ui.Spinner(c).Size(14, 14)
				}
				ui.Spacer(c)
				if b.running {
					if ui.Button(c, "Cancel").Clicked() {
						b.cancel()
					}
					return
				}
				close := "Cancel"
				if b.done {
					close = "Close"
				}
				if ui.Button(c, close).Clicked() {
					b.open = false
				}
				action := "Back Up"
				if b.restore {
					action = "Restore…"
				}
				if !b.done && ui.PrimaryButton(c, action).Disabled(strings.TrimSpace(b.path) == "").Clicked() {
					a.startBackup(b)
				}
			})
		})
	})
	if !b.open {
		if b.cancel != nil {
			b.cancel()
		}
		a.backup = nil
	}
}

// backupNote says how an engine's databases are backed up, or restored.
func backupNote(e db.Engine, restore bool) string {
	switch {
	case restore && e == db.DuckDB:
		return "IMPORT DATABASE reads the folder in a DuckDB of its own, which reaches no file outside it; what it makes is then copied into the database, which must not have its tables."
	case restore && e == db.Postgres:
		return "A .dump of pg_dump, or a SQL file."
	case restore && e == db.SQLite:
		return "A SQL file runs as Run SQL File runs it; a copy of the database opens as a connection of its own."
	case restore:
		return "A SQL file, as mysqldump writes, runs as Run SQL File runs it."
	case e == db.SQLite:
		return "VACUUM INTO writes a copy of the database as it is, while it stays in use: open the copy as a connection to read it."
	case e == db.DuckDB:
		return "EXPORT DATABASE writes the schema and each table's rows, in Parquet, in a new folder."
	case e == db.Postgres:
		return "pg_dump writes it, reaching the server as the connection does; it must be installed, of the server's version or later."
	}
	return "mysqldump writes it, in one transaction, with routines, triggers and events; it must be installed."
}

// backupTarget is the database a navigator node backs up: a PostgreSQL
// database's, or a MySQL schema, which is a database.
func backupTarget(cn *connection.Conn, n navNode) (string, bool) {
	switch {
	case cn.Config.Engine == db.Postgres && n.kind == nodeDatabase:
		return n.database, true
	case cn.Config.Engine == db.MySQL && n.kind == nodeSchema:
		return n.schema, true
	}
	return "", false
}
