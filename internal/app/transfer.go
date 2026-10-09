package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/safety"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// What a copy does with a table the target has.
const (
	copyCreate  = iota // makes it; one there already stops the copy
	copyAppend         // adds the rows to it, making it if it is not there
	copyReplace        // deletes its rows first
)

// copyDialog copies tables of a schema, with their rows, into a schema
// of another database, of the same engine or another.
type copyDialog struct {
	open               bool
	from               *connection.Conn
	fromDB, fromSchema string
	tables             []copyTable

	target targetPicker
	mode   int

	running bool
	cancel  context.CancelFunc
	err     string
	done    bool
}

type copyTable struct {
	name   string
	chosen bool
	status string // what was copied, or why not
}

// openCopy copies tables of a schema: those named, or all of them.
func (a *App) openCopy(cn *connection.Conn, database, schema string, tables []string) {
	x := &copyDialog{open: true, from: cn, fromDB: database, fromSchema: schema}
	for _, t := range tables {
		x.tables = append(x.tables, copyTable{name: t, chosen: true})
	}
	a.copying = x
	if len(tables) > 0 {
		return
	}
	poolOf := cn.PoolFor(database)
	a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := poolOf(ctx)
		var objs []db.Object
		if err == nil {
			objs, err = d.Dialect.Objects(ctx, d.Catalog(), schema)
		}
		return func() {
			if err != nil {
				x.err = err.Error()
				return
			}
			for _, o := range objs {
				if o.Kind == db.KindTable {
					x.tables = append(x.tables, copyTable{name: o.Name, chosen: true})
				}
			}
		}
	})
}

// copyStatements are a sample of what a copy runs on its target, for the
// safety policy to weigh.
func copyStatements(to *connection.Conn, schema string, mode int, tables []string) []string {
	d := db.DialectOf(to.Config.Engine)
	var out []string
	for _, t := range tables {
		name := db.QualifiedName(d, schema, t)
		out = append(out, "CREATE TABLE "+name+" (c int)", "INSERT INTO "+name+" VALUES (1)")
		if mode == copyReplace {
			out = append(out, "DELETE FROM "+name)
		}
	}
	return out
}

// startCopy copies the chosen tables, once the target's policy agrees.
func (a *App) startCopy(x *copyDialog) {
	to := x.target.conn(a)
	var tables []string
	for _, t := range x.tables {
		if t.chosen {
			tables = append(tables, t.name)
		}
	}
	switch {
	case to == nil:
		x.err = "Choose the database to copy into."
		return
	case len(tables) == 0:
		x.err = "Choose a table to copy."
		return
	case to.Config.ReadOnly:
		x.err = to.Config.Name + " is read-only."
		return
	case to == x.from && x.target.database == x.fromDB && x.target.schema == x.fromSchema:
		x.err = "The tables would be copied onto themselves: choose another schema."
		return
	case to == x.from && to.DB != nil && to.DB.Single():
		x.err = "This database keeps one connection, which cannot read and write at once: copy in an editor with CREATE TABLE … AS SELECT."
		return
	}
	cfg := to.Config
	v := safety.ReviewSQL(&cfg, safety.Analyze(&cfg, copyStatements(to, x.target.schema, x.mode, tables)))
	if v.Blocked != "" {
		a.RecordBlocked(to, v.Blocked, "copy of "+strings.Join(tables, ", "))
		x.err = v.Blocked
		return
	}
	run := func() { a.runCopy(x, to, tables) }
	if v.Confirm {
		preview := fmt.Sprintf("Copy %s from %s into %s.", strings.Join(tables, ", "), x.from.Config.Name, cfg.Name)
		a.AskConfirm(to, v, fmt.Sprintf("Copy %d table%s into %s?", len(tables), widgets.Plural(len(tables)), cfg.Name), "Copy", preview, run)
		return
	}
	run()
}

// runCopy copies the tables one after the other, each in a transaction
// of its own where the target has them.
func (a *App) runCopy(x *copyDialog, to *connection.Conn, tables []string) {
	ctx, cancel := context.WithCancel(context.Background())
	x.running, x.cancel, x.err, x.done = true, cancel, "", false
	for i := range x.tables {
		x.tables[i].status = ""
	}
	from, fromDB, fromSchema, toDB, toSchema, mode := x.from, x.fromDB, x.fromSchema, x.target.database, x.target.schema, x.mode
	fromPool, toPool := from.PoolFor(fromDB), to.PoolFor(toDB)
	status := func(table, s string) {
		a.Post(func() {
			if i := slices.IndexFunc(x.tables, func(t copyTable) bool { return t.name == table }); i >= 0 {
				x.tables[i].status = s
			}
		})
	}
	started := time.Now()
	go func() {
		defer cancel()
		var err error
		for _, table := range tables {
			var n int64
			n, err = a.copyTable(ctx, from, fromPool, fromSchema, to, toPool, toDB, toSchema, table, mode, func(done int64) {
				status(table, widgets.Count(done, "row")+"…")
			})
			ev := audit.Event{Kind: audit.KindImport, Database: toDB, Rows: n, Statement: "INSERT INTO " + table,
				Detail: fmt.Sprintf("copied from %s, %s.%s", from.Config.Name, fromSchema, table)}
			if err != nil {
				ev.Error = err.Error()
				status(table, "failed")
			} else {
				status(table, widgets.Count(n, "row")+" copied")
			}
			a.Record(&to.Config, ev)
			if err != nil {
				break
			}
		}
		a.Post(func() {
			x.running, x.cancel = false, nil
			to.ForgetCatalog()
			if err != nil {
				x.err = err.Error()
				a.Notify(started, "Copy failed", to.Config.Name, nil)
				return
			}
			x.done = true
			a.Notify(started, "Copy finished", fmt.Sprintf("%d tables into %s", len(tables), to.Config.Name), nil)
		})
	}()
}

// copyTable copies a table's rows into the target's schema, making the
// table there first when it is not.
func (a *App) copyTable(ctx context.Context, from *connection.Conn, fromPool func(context.Context) (*db.DB, error), fromSchema string,
	to *connection.Conn, toPool func(context.Context) (*db.DB, error), toDB, toSchema, table string, mode int, progress func(int64)) (int64, error) {
	src, err := fromPool(ctx)
	if err != nil {
		return 0, err
	}
	dst, err := toPool(ctx)
	if err != nil {
		return 0, err
	}
	srcCols, err := src.Dialect.Columns(ctx, src.Catalog(), fromSchema, table)
	if err != nil {
		return 0, err
	}
	objs, err := dst.Dialect.Objects(ctx, dst.Catalog(), toSchema)
	if err != nil {
		return 0, err
	}
	exists := slices.ContainsFunc(objs, func(o db.Object) bool { return o.Name == table })
	if exists && mode == copyCreate {
		return 0, fmt.Errorf("%s is in %s already: append to it, or replace its rows", table, toSchema)
	}
	design := db.CopyDesign(from.Config.Engine, srcCols, to.Config.Engine, toSchema, table)
	dstCols := make([]db.Column, len(design.Columns))
	for i, c := range design.Columns {
		dstCols[i] = db.Column{Name: c.Name, Type: c.Type, Nullable: c.Nullable, PrimaryKey: c.PrimaryKey}
	}
	var create, empty string
	if exists {
		// Into the columns the table has of the same names.
		have, err := dst.Dialect.Columns(ctx, dst.Catalog(), toSchema, table)
		if err != nil {
			return 0, err
		}
		var keep []db.Column
		var keepSrc []db.Column
		for i, c := range srcCols {
			if j := slices.IndexFunc(have, func(h db.Column) bool { return strings.EqualFold(h.Name, c.Name) }); j >= 0 {
				keep, keepSrc = append(keep, have[j]), append(keepSrc, srcCols[i])
			}
		}
		if len(keep) == 0 {
			return 0, fmt.Errorf("%s in %s has none of the columns of the table copied", table, toSchema)
		}
		dstCols, srcCols = keep, keepSrc
		if mode == copyReplace {
			empty = "DELETE FROM " + db.QualifiedName(dst.Dialect, toSchema, table)
			if to.Config.Engine == db.ClickHouse {
				empty = "TRUNCATE TABLE " + db.QualifiedName(dst.Dialect, toSchema, table)
			}
		}
	} else {
		ch, err := db.NewTableChange(dst.Dialect, design)
		if err != nil {
			return 0, err
		}
		create = ch.Statements()[0]
	}
	// The rows are read on a session of the source's own.
	sess, err := src.Session(ctx)
	if err != nil {
		return 0, err
	}
	defer sess.Close()
	names := make([]string, len(srcCols))
	canonical := make([]string, len(srcCols))
	for i, c := range srcCols {
		names[i] = src.Dialect.Quote(c.Name)
		canonical[i] = db.CanonicalType(from.Config.Engine, c.Type)
	}
	cursor, err := sess.Query(ctx, "SELECT "+strings.Join(names, ", ")+" FROM "+db.QualifiedName(src.Dialect, fromSchema, table))
	if err != nil {
		return 0, err
	}
	defer cursor.Close()
	target := &db.EditTarget{Dialect: dst.Dialect, Schema: toSchema, Table: table, Columns: dstCols}
	toEngine := to.Config.Engine
	return writeRows(ctx, a, to.DB, to.Config, toDB, rowWrite{target: target, cols: dstCols, create: create, empty: empty, verb: "copied",
		read: func(ctx context.Context, batch int, emit func([][]any) error) error {
			for {
				rows, err := cursor.Fetch(batch)
				if err != nil {
					return err
				}
				if len(rows) == 0 {
					if cursor.Truncated() {
						return errors.New("the source keeps one connection, whose rows are read ahead up to a limit: copy in parts")
					}
					return nil
				}
				for _, row := range rows {
					for i, v := range row {
						row[i] = db.InsertValue(toEngine, v, canonical[i])
					}
				}
				if err := emit(rows); err != nil {
					return err
				}
			}
		},
		progress: progress})
}

func (a *App) copyView(c *ui.Context) {
	x := a.copying
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.Modal(c, &x.open, func() {
		ui.Column(c).Width(640).Gap(12).Children(func() {
			ui.Text(c, "Copy Tables of "+x.fromSchema+" · "+x.from.Config.Name).FontSize(15).Bold().SingleLine()
			ui.Scroll(c).MaxHeight(200).Border(1, th.Border).Radius(6).Children(func() {
				ui.Column(c).Padding(4).Children(func() {
					for i := range x.tables {
						t := &x.tables[i]
						ui.Row(c.Key("copy-"+t.name)).Gap(8).Padding(2, 6).Children(func() {
							ui.Checkbox(c, &t.chosen, t.name).Disabled(x.running).Grow(1).Shrink(1)
							ui.Text(c, t.status).FontSize(12).TextColor(pal.Muted)
						})
					}
				})
			})
			ui.Form(c, func() {
				x.target.fields(c, a, "Into", x.running)
				ui.Field(c, "A table there", func() {
					ui.Segmented(c, &x.mode, "Stops the Copy", "Gets the Rows Added", "Has Its Rows Replaced").Label("A table there").Disabled(x.running)
				}).Description("A table not there is made, its columns typed as the target holds their values. Each table copies in one transaction where the target has them.")
			})
			if x.err != "" {
				ui.Text(c, x.err).TextColor(th.Danger).Selectable()
			}
			ui.Row(c).Gap(8).Children(func() {
				if x.running {
					ui.Spinner(c).Size(14, 14)
				}
				ui.Spacer(c)
				if x.running {
					if ui.Button(c, "Cancel").Clicked() {
						x.cancel()
					}
					return
				}
				close := "Cancel"
				if x.done {
					close = "Close"
				}
				if ui.Button(c, close).Clicked() {
					x.open = false
				}
				if !x.done && ui.PrimaryButton(c, "Copy").Clicked() {
					a.startCopy(x)
				}
			})
		})
	})
	if !x.open {
		if x.cancel != nil {
			x.cancel()
		}
		a.copying = nil
	}
}
