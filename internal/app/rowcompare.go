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
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// rowCompareDialog compares the rows of a table with another's, of the
// same database or another, of any engine, and makes the other's as the
// first's: the source's rows are the ones kept.
type rowCompareDialog struct {
	open                          bool
	from                          *connection.Conn
	fromDB, fromSchema, fromTable string

	target targetPicker
	table  string

	running  bool
	cancel   context.CancelFunc
	progress string // what the comparison or the sync is doing
	err      string

	result *db.RowComparison
	// to, toDB, toSchema and toTable are the table result compared with.
	to                      *connection.Conn
	toDB, toSchema, toTable string
	masked                  []bool // by the result's columns
	list                    ui.ListState

	add, update, remove bool
	applied             string // what the last sync did
}

func (a *App) openRowCompare(cn *connection.Conn, database string, obj db.Object) {
	a.rowCompare = &rowCompareDialog{open: true, from: cn, fromDB: database, fromSchema: obj.Schema, fromTable: obj.Name, add: true, update: true}
}

// startCompare compares the source's rows with the table chosen.
func (a *App) startCompare(x *rowCompareDialog) {
	to := x.target.conn(a)
	switch {
	case to == nil || x.target.schema == "" || x.table == "":
		x.err = "Choose the table to compare with."
		return
	case to.DB == nil:
		x.err = to.Config.Name + " is not connected."
		return
	case to == x.from && x.target.database == x.fromDB && x.target.schema == x.fromSchema && x.table == x.fromTable:
		x.err = "Choose another table: this one is the source."
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	x.running, x.cancel, x.progress, x.err, x.result, x.applied = true, cancel, "Reading the rows…", "", nil, ""
	from, toDB, toSchema, toTable := x.from, x.target.database, x.target.schema, x.table
	fromPool, toPool := from.PoolFor(x.fromDB), to.PoolFor(toDB)
	source := db.CompareTable{Schema: x.fromSchema, Table: x.fromTable}
	target := db.CompareTable{Schema: toSchema, Table: toTable}
	show := a.Settings().ShowSensitive
	a.Background(func() func() {
		defer cancel()
		var r *db.RowComparison
		var err error
		if source.DB, err = fromPool(ctx); err == nil {
			if target.DB, err = toPool(ctx); err == nil {
				r, err = db.CompareRows(ctx, source, target, func(n int64) {
					a.Post(func() { x.progress = widgets.HumanCount(n) + " rows read…" })
				})
			}
		}
		if ctx.Err() != nil {
			err = errors.New("cancelled")
		}
		return func() {
			x.running, x.cancel = false, nil
			if err != nil {
				x.err = err.Error()
				return
			}
			x.result, x.to, x.toDB, x.toSchema, x.toTable = r, to, toDB, toSchema, toTable
			from := dataview.MaskedColumns(x.from, x.fromSchema, x.fromTable, r.Columns, show)
			into := dataview.MaskedColumns(to, toSchema, toTable, r.Columns, show)
			x.masked = make([]bool, len(r.Columns))
			for i := range x.masked {
				x.masked[i] = from[i] || into[i]
			}
		}
	})
}

// syncStatements are the statements making the target's rows as the
// source's, as the dialog says, and the same with the values the grid
// hides masked, to show and audit; hides says whether any was.
func (x *rowCompareDialog) syncStatements() (stmts, shown []db.Statement, hides bool, err error) {
	r := x.result
	changes := r.Changes(x.add, x.update, x.remove)
	if stmts, err = r.Target.Statements(changes); err != nil {
		return nil, nil, false, err
	}
	masked := map[string]bool{}
	var keyMasked []bool
	for i, name := range r.TargetColumns {
		masked[name] = x.masked[i]
	}
	for _, c := range r.Key {
		keyMasked = append(keyMasked, x.masked[c])
	}
	mask := func(v any) any {
		if v == nil {
			return nil
		}
		hides = true
		return dataview.MaskedText
	}
	for i, ch := range changes {
		values := make(map[string]any, len(ch.Values))
		for name, v := range ch.Values {
			if masked[name] {
				v = mask(v)
			}
			values[name] = v
		}
		key := slices.Clone(ch.Key)
		for j := range key {
			if keyMasked[j] {
				key[j] = mask(key[j])
			}
		}
		changes[i] = db.Change{Kind: ch.Kind, Key: key, Values: values}
	}
	shown, err = r.Target.Statements(changes)
	return stmts, shown, hides, err
}

// syncBlocked says why the target's rows cannot be changed from here, ""
// when they can.
func (x *rowCompareDialog) syncBlocked() string {
	if x.to.Config.ReadOnly {
		return x.to.Config.Name + " is read-only."
	}
	if x.to.DB == nil {
		return x.to.Config.Name + " is not connected."
	}
	if ok, why := x.to.DB.Dialect.Editable(); !ok {
		return why
	}
	return ""
}

// openSyncSQL opens the sync's statements in an editor of the target.
func (a *App) openSyncSQL(x *rowCompareDialog) {
	stmts, _, hides, err := x.syncStatements()
	switch {
	case err != nil:
		x.err = err.Error()
		return
	case hides:
		// The editor's file is the project's.
		x.err = "The changes hold values the grid hides, which an editor's file would keep: apply them from here."
		return
	}
	var b strings.Builder
	for _, st := range stmts {
		b.WriteString(st.Script(x.to.Config.Engine))
		b.WriteByte('\n')
	}
	a.NewQueryTab(x.to, x.toDB, b.String())
	x.open = false
}

// startSync makes the target's rows as the source's, in one transaction,
// once the target's policy agrees and the user does.
func (a *App) startSync(x *rowCompareDialog) {
	stmts, shown, _, err := x.syncStatements()
	if err != nil {
		x.err = err.Error()
		return
	}
	if len(stmts) == 0 {
		x.err = "Choose what to change."
		return
	}
	to, cfg := x.to, x.to.Config
	// One statement of each kind: the others are alike but for their values.
	var sample []string
	var previews []string
	for i, st := range stmts {
		verb, _, _ := strings.Cut(st.SQL, " ")
		if !slices.ContainsFunc(sample, func(s string) bool { return strings.HasPrefix(s, verb+" ") }) {
			sample = append(sample, st.SQL)
		}
		if i < 200 {
			previews = append(previews, shown[i].Preview())
		}
	}
	if len(stmts) > len(previews) {
		previews = append(previews, fmt.Sprintf("… and %d more", len(stmts)-len(previews)))
	}
	preview := strings.Join(previews, "\n")
	v := safety.ReviewSQL(&cfg, safety.Analyze(&cfg, sample))
	if v.Blocked != "" {
		a.RecordBlocked(to, v.Blocked, preview)
		x.err = v.Blocked
		return
	}
	v.Reasons = append([]string{"Each statement must change exactly one row, or every change is rolled back."}, v.Reasons...)
	if cfg.ManualCommit() {
		v.Reasons = append(v.Reasons, "They commit once applied, though the connection commits by hand.")
	}
	if !x.result.Complete() {
		v.Reasons = append(v.Reasons, fmt.Sprintf("Only the first %s differences are changed: compare again for the rest.", widgets.HumanCount(db.DifferenceLimit)))
	}
	title := fmt.Sprintf("Apply %s change%s to %s?", widgets.HumanCount(int64(len(stmts))), widgets.Plural(len(stmts)), x.toTable)
	a.AskConfirm(to, v, title, "Apply", preview, func() { a.runSync(x, stmts, shown) })
}

// runSync applies the statements, auditing each as shown.
func (a *App) runSync(x *rowCompareDialog, stmts, shown []db.Statement) {
	ctx, cancel := context.WithCancel(context.Background())
	x.running, x.cancel, x.err = true, cancel, ""
	x.progress = fmt.Sprintf("Applying %s change%s…", widgets.HumanCount(int64(len(stmts))), widgets.Plural(len(stmts)))
	pool, cfg, database := x.to.PoolFor(x.toDB), x.to.Config, x.toDB
	to, toSchema, toTable := x.to, x.toSchema, x.toTable
	detail := fmt.Sprintf("%d changes making %s's rows as %s's of %s ", len(stmts), x.toTable, x.fromTable, x.from.Config.Name)
	started := time.Now()
	a.Background(func() func() {
		defer cancel()
		d, err := pool(ctx)
		var sess *db.Session
		if err == nil {
			sess, err = d.Session(ctx)
		}
		if err == nil {
			err = sess.ApplyEdits(ctx, stmts, func(i int, n int64, took time.Duration, err error) {
				a.RecordRun(cfg, audit.KindEdit, database, shown[i].Preview(), n, took, err)
			})
			sess.Close()
		}
		if err != nil && ctx.Err() != nil {
			err = errors.New("cancelled: the changes were rolled back")
		}
		ev := audit.Event{Kind: audit.KindEdit, Database: database, Rows: int64(len(stmts)), DurationMS: time.Since(started).Milliseconds(), Detail: detail}
		if err != nil {
			ev.Detail += "rolled back"
			ev.Error = err.Error()
		} else {
			ev.Detail += "committed"
		}
		a.Record(&cfg, ev)
		return func() {
			x.running, x.cancel = false, nil
			if err != nil {
				x.err = err.Error()
				a.Notify(started, "Sync failed", x.toTable, nil)
				return
			}
			done := fmt.Sprintf("Applied %s change%s to %s.", widgets.HumanCount(int64(len(stmts))), widgets.Plural(len(stmts)), x.toTable)
			a.Notify(started, "Rows synced", done, nil)
			a.reloadTableTabs(to, toSchema, toTable)
			if a.rowCompare != x {
				return // closed meanwhile
			}
			// The tables as they are now.
			a.startCompare(x)
			x.applied = done
		}
	})
}

func (a *App) rowCompareView(c *ui.Context) {
	x := a.rowCompare
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	_, winH := c.Size()
	ui.Modal(c, &x.open, func() {
		ui.Column(c).Width(900).MaxHeight(winH - 80).Gap(12).Children(func() {
			ui.Text(c, "Compare the Rows of "+x.fromSchema+"."+x.fromTable+" · "+x.from.Config.Name).FontSize(15).Bold().SingleLine()
			ui.Form(c, func() {
				to := x.target.fields(c, a, "With", x.running)
				if x.result != nil && (to != x.to || x.target.database != x.toDB || x.target.schema != x.toSchema || x.table != x.toTable) {
					x.result = nil // of another table than the one chosen now
				}
				if to != nil && x.target.schema != "" {
					key := connection.SchemaKey{Database: x.target.database, Schema: x.target.schema}
					objs, ok := to.Objects[key]
					if !ok && to.Status == connection.StatusConnected && !to.Loading[key] && to.LoadErr[key] == "" {
						connection.LoadObjects(a, to, x.target.database, x.target.schema)
					}
					var tables []string
					for _, o := range objs {
						if o.Kind == db.KindTable {
							tables = append(tables, o.Name)
						}
					}
					if x.table != "" && ok && !slices.Contains(tables, x.table) {
						x.table = ""
					}
					if x.table == "" && slices.Contains(tables, x.fromTable) {
						x.table = x.fromTable
					}
					ui.Field(c, "Table", func() {
						ui.Select(c, &x.table, tables).Label("Table").Disabled(x.running)
					}).Description("Rows match by its primary key, over the columns both tables have. Values compare as values: 1.50 and 1.5 are alike, as are a time and the same time in UTC.")
				}
			})
			if x.result != nil {
				a.rowCompareResult(c, x)
			}
			if x.err != "" {
				ui.Text(c, x.err).TextColor(th.Danger).Selectable()
			} else if x.applied != "" {
				ui.Text(c, x.applied).TextColor(pal.Muted)
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if x.running {
					ui.Spinner(c).Size(14, 14)
					ui.Text(c, x.progress).FontSize(12).TextColor(pal.Muted)
				}
				ui.Spacer(c)
				if x.running {
					if ui.Button(c, "Cancel").Clicked() {
						x.cancel()
					}
					return
				}
				if ui.Button(c, "Close").Clicked() {
					x.open = false
				}
				if x.result == nil {
					if widgets.Activated(c, ui.PrimaryButton(c, "Compare")) {
						a.startCompare(x)
					}
					return
				}
				if ui.Button(c, "Compare Again").Clicked() {
					a.startCompare(x)
				}
				blocked := x.syncBlocked()
				if ui.Button(c, "Open as SQL").Disabled(blocked != "").Clicked() {
					a.openSyncSQL(x)
				}
				apply := ui.PrimaryButton(c, "Apply to "+x.toTable+"…").Disabled(blocked != "")
				if blocked != "" {
					apply.Tooltip(blocked)
				}
				if apply.Clicked() {
					a.startSync(x)
				}
			})
		})
	})
	if !x.open {
		if x.cancel != nil {
			x.cancel()
		}
		a.rowCompare = nil
	}
}

// rowCompareResult shows what the comparison found: how many rows of
// each kind, the differences, and what a sync changes.
func (a *App) rowCompareResult(c *ui.Context, x *rowCompareDialog) {
	r := x.result
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	src, dst := x.fromTable, x.toTable
	if src == dst {
		src, dst = "the source", x.toTable+" of "+x.to.Config.Name
	}
	ui.Row(c).Gap(16).Children(func() {
		for _, s := range []struct {
			n     int64
			label string
		}{{r.Same, "alike"}, {r.Changed, "changed"}, {r.OnlyInSource, "only in " + src}, {r.OnlyInTarget, "only in " + dst}} {
			ui.Text(c, widgets.HumanCount(s.n)+" "+s.label).FontSize(13)
		}
	})
	if len(r.Differences) == 0 {
		ui.Text(c, "The rows are alike.").TextColor(pal.Muted)
		return
	}
	note := "A change shows the target's value, then the source's."
	if !r.Complete() {
		note += fmt.Sprintf(" The first %s differences are kept.", widgets.HumanCount(db.DifferenceLimit))
	}
	ui.Text(c, note).FontSize(12).TextColor(pal.Muted)
	ui.List(c, &x.list, len(r.Differences), func(i int) {
		d := r.Differences[i]
		kind, color := "Changed", th.Warning
		switch d.Kind {
		case db.ChangeInsert:
			kind, color = "Only in "+src, th.Success
		case db.ChangeDelete:
			kind, color = "Only in "+dst, th.Danger
		}
		ui.Row(c).Padding(4, 10).Gap(12).Children(func() {
			ui.Text(c, kind).FontSize(12).TextColor(color).Width(150).SingleLine()
			row := d.Source
			if row == nil {
				row = d.Target
			}
			ui.Text(c, x.cellsText(row, r.Key)).Font(widgets.MonoFont).FontSize(12).Width(140).SingleLine()
			detail := ""
			if d.Kind == db.ChangeUpdate {
				var parts []string
				for _, col := range d.Changed {
					parts = append(parts, r.Columns[col]+": "+x.valueText(d.Target[col], col)+" → "+x.valueText(d.Source[col], col))
				}
				detail = strings.Join(parts, "   ")
			} else {
				var rest []int
				for col := range r.Columns {
					if !slices.Contains(r.Key, col) {
						rest = append(rest, col)
					}
				}
				detail = x.cellsText(row, rest)
			}
			ui.Text(c, detail).Font(widgets.MonoFont).FontSize(12).TextColor(pal.Muted).SingleLine().Grow(1).Shrink(1)
		})
	}).MaxHeight(320).Border(1, th.Border).Radius(6)
	ui.Column(c).Gap(4).Children(func() {
		ui.Text(c, "Make "+dst+" as "+src+":").FontSize(13)
		ui.Checkbox(c, &x.add, fmt.Sprintf("Add the %s rows only in %s", widgets.HumanCount(r.OnlyInSource), src)).Disabled(x.running || r.OnlyInSource == 0)
		ui.Checkbox(c, &x.update, fmt.Sprintf("Update the %s changed rows", widgets.HumanCount(r.Changed))).Disabled(x.running || r.Changed == 0)
		ui.Checkbox(c, &x.remove, fmt.Sprintf("Delete the %s rows only in %s", widgets.HumanCount(r.OnlyInTarget), dst)).Disabled(x.running || r.OnlyInTarget == 0)
	})
}

// cellsText writes the named columns of a row as name: value.
func (x *rowCompareDialog) cellsText(row []any, cols []int) string {
	parts := make([]string, len(cols))
	for i, col := range cols {
		parts[i] = x.result.Columns[col] + ": " + x.valueText(row[col], col)
	}
	return strings.Join(parts, "   ")
}

func (x *rowCompareDialog) valueText(v any, col int) string {
	switch {
	case v == nil:
		return "NULL"
	case x.masked[col]:
		return dataview.MaskedText
	}
	text := strings.ReplaceAll(db.Display(v), "\n", "↵")
	if len([]rune(text)) > 60 {
		text = string([]rune(text)[:60]) + "…"
	}
	return text
}
