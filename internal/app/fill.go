package app

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/redact"
	"dgopher/internal/safety"
	"dgopher/internal/testdata"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

const (
	// fillLimit is how many rows one fill makes at most.
	fillLimit = 1_000_000
	// referenceLimit is how many values of a column referred to a fill
	// chooses from.
	referenceLimit  = 10_000
	fillPreviewRows = 5
)

// fillDialog fills a table with generated rows.
type fillDialog struct {
	open     bool
	conn     *connection.Conn
	database string
	obj      db.Object

	count   string // the rows to make, as typed
	cols    []fillColumn
	loading bool
	// run marks this fill's unique text, apart from earlier fills'.
	run  string
	seed uint64 // the sample's

	running bool
	cancel  context.CancelFunc
	done    int64
	err     string
}

// fillColumn is a column of the table and how its values are made.
type fillColumn struct {
	col       db.Column
	canonical string
	gen       testdata.Generator
	kind      string // the generator's kind, as its select shows it
	values    string // OneOf's choices, as typed, one per comma
	nulls     string // the share of NULLs, in percent, as typed
	refTo     string // the table and column a foreign key refers to
	// suggested is values as suggested: while it is, the choices are
	// gen.Values as they are, commas in them included.
	suggested string
	unique    bool   // the column's values must differ
	note      string // why the generator suggested may still fail
}

func (a *App) openFill(cn *connection.Conn, database string, obj db.Object) {
	if cn.Config.ReadOnly {
		a.ShowError("No rows generated", cn.Config.Name+" is read-only.")
		return
	}
	x := &fillDialog{open: true, conn: cn, database: database, obj: obj, count: "100", loading: true,
		run: fillRunMark(), seed: rand.Uint64()}
	a.filling = x
	poolOf := cn.PoolFor(database)
	engine := cn.Config.Engine
	a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cols, err := readFillColumns(ctx, poolOf, engine, obj)
		return func() {
			x.loading = false
			if err != nil {
				x.err = err.Error()
				return
			}
			x.cols = cols
		}
	})
}

// fillRunMark is a short text unique to a fill, as far as matters.
func fillRunMark() string {
	const letters = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, 4)
	for i := range b {
		b[i] = letters[rand.IntN(len(letters))]
	}
	return string(b)
}

// readFillColumns reads a table's columns with what suggests their
// generators: keys, unique indexes, enums, foreign keys and the values
// they may take, where new keys start.
func readFillColumns(ctx context.Context, poolOf func(context.Context) (*db.DB, error), engine db.Engine, obj db.Object) ([]fillColumn, error) {
	d, err := poolOf(ctx)
	if err != nil {
		return nil, err
	}
	q := d.Catalog()
	cols, err := d.Dialect.Columns(ctx, q, obj.Schema, obj.Name)
	if err != nil {
		return nil, err
	}
	indexes, err := d.Dialect.Indexes(ctx, q, obj.Schema, obj.Name)
	if err != nil {
		return nil, err
	}
	fks, err := d.Dialect.ForeignKeys(ctx, q, obj.Schema, obj.Name)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, c := range cols {
		if c.PrimaryKey {
			keys = append(keys, c.Name)
		}
	}
	// A key of several columns is unique where any of them is: each one
	// continues from its highest.
	unique := func(name string) bool {
		return slices.Contains(keys, name) || slices.ContainsFunc(indexes, func(ix db.Index) bool {
			return ix.Unique && len(ix.Columns) == 1 && ix.Columns[0] == name
		})
	}
	inSeveral := func(name string) bool {
		return len(keys) > 1 && slices.Contains(keys, name) || slices.ContainsFunc(indexes, func(ix db.Index) bool {
			return ix.Unique && len(ix.Columns) > 1 && slices.Contains(ix.Columns, name)
		})
	}
	now := time.Now()
	out := make([]fillColumn, len(cols))
	for i, c := range cols {
		fc := fillColumn{col: c, canonical: db.CanonicalType(engine, c.Type)}
		enum, err := db.EnumValues(ctx, d, c.Type)
		if err != nil {
			return nil, err
		}
		fk := slices.IndexFunc(fks, func(fk db.ForeignKey) bool { return len(fk.Columns) == 1 && fk.Columns[0] == c.Name })
		partOfFK := slices.ContainsFunc(fks, func(fk db.ForeignKey) bool { return len(fk.Columns) > 1 && slices.Contains(fk.Columns, c.Name) })
		fc.unique = unique(c.Name)
		fc.gen = testdata.Suggest(testdata.Column{Name: c.Name, Type: c.Type, Canonical: fc.canonical, Nullable: c.Nullable,
			AutoIncrement: c.AutoIncrement, PartOfForeignKey: partOfFK, Unique: fc.unique, Enum: enum, References: fk >= 0}, now)
		k := fc.gen.Kind
		switch {
		case partOfFK:
			fc.note = "Part of a foreign key of several columns, whose values together must be another table's."
		case inSeveral(c.Name) && k != testdata.Sequence && k != testdata.UUID && !(fc.gen.Unique && k.Textual()):
			fc.note = "Part of a key of several columns: a combination made twice stops the fill."
		}
		switch fc.gen.Kind {
		case testdata.Reference:
			ref := fks[fk]
			schema := ref.RefSchema
			if schema == "" {
				schema = obj.Schema
			}
			fc.refTo = ref.RefTable + "." + ref.RefColumns[0]
			fc.gen.Values, err = db.DistinctValues(ctx, d, schema, ref.RefTable, db.Column{Name: ref.RefColumns[0], Type: c.Type}, referenceLimit)
			if err != nil {
				return nil, err
			}
			if len(fc.gen.Values) == 0 && c.Nullable {
				fc.gen.Kind = testdata.Null
			}
		case testdata.Sequence:
			next, err := db.NextNumber(ctx, d, obj.Schema, obj.Name, c.Name)
			if err != nil {
				return nil, err
			}
			fc.gen.From = strconv.FormatInt(next, 10)
		}
		fc.kind = fc.gen.Kind.Label()
		fc.values = strings.Join(fc.gen.Values, ", ")
		fc.suggested = fc.values
		fc.nulls = "0"
		out[i] = fc
	}
	return out, nil
}

// generator is the column's generator as the dialog now says.
func (fc *fillColumn) generator() (testdata.Generator, error) {
	g := fc.gen
	for _, k := range testdata.Kinds {
		if k.Label() == fc.kind {
			g.Kind = k
		}
	}
	if g.Kind == testdata.OneOf && fc.values != fc.suggested {
		g.Values = nil
		for _, v := range strings.Split(fc.values, ",") {
			if v = strings.TrimSpace(v); v != "" {
				g.Values = append(g.Values, v)
			}
		}
	}
	if fc.col.Nullable && g.Kind != testdata.Default && g.Kind != testdata.Null {
		n, err := strconv.Atoi(strings.TrimSpace(fc.nulls))
		if err != nil {
			return g, errors.New("the share of NULLs is a whole percentage")
		}
		g.NullPercent = n
	}
	return g, nil
}

// fillPlan is what a fill makes: the columns it fills, their types as
// the values are written, and how their values are made.
type fillPlan struct {
	cols      []db.Column
	canonical []string
	values    []testdata.Values
}

func (x *fillDialog) plan() (fillPlan, error) {
	var p fillPlan
	for i := range x.cols {
		fc := &x.cols[i]
		g, err := fc.generator()
		if err != nil {
			return p, fmt.Errorf("%s: %w", fc.col.Name, err)
		}
		if g.Kind == testdata.Default {
			if c := fc.col; !c.Nullable && !c.HasDefault && !c.AutoIncrement {
				return p, fmt.Errorf("%s: it has no default and takes no NULL: choose how its values are made", c.Name)
			}
			continue
		}
		values, err := g.Prepare(x.run)
		if err != nil {
			return p, fmt.Errorf("%s: %w", fc.col.Name, err)
		}
		p.cols = append(p.cols, fc.col)
		p.canonical = append(p.canonical, fc.canonical)
		p.values = append(p.values, values)
	}
	if len(p.cols) == 0 {
		return p, errors.New("every column is left to its default: choose one to generate")
	}
	return p, nil
}

// rows makes the rows from..to (excluded) of a plan, as an INSERT into an
// engine takes them.
func (p fillPlan) rows(r *rand.Rand, e db.Engine, from, to int) [][]any {
	out := make([][]any, 0, to-from)
	for i := from; i < to; i++ {
		row := make([]any, len(p.cols))
		for j, values := range p.values {
			row[j] = db.InsertValue(e, values(r, i), p.canonical[j])
		}
		out = append(out, row)
	}
	return out
}

func (x *fillDialog) rowCount() (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(x.count))
	if err != nil || n < 1 || n > fillLimit {
		return 0, fmt.Errorf("make from 1 to %s rows", widgets.HumanCount(fillLimit))
	}
	return n, nil
}

// startFill fills the table, once the policy agrees.
func (a *App) startFill(x *fillDialog) {
	n, err := x.rowCount()
	if err == nil {
		_, err = x.plan()
	}
	if err != nil {
		x.err = err.Error()
		return
	}
	cn := x.conn
	if cn.DB == nil {
		x.err = cn.Config.Name + " is not connected."
		return
	}
	insert := "INSERT INTO " + db.QualifiedName(cn.DB.Dialect, x.obj.Schema, x.obj.Name) + " …"
	v := safety.ReviewSQL(&cn.Config, safety.Analyze(&cn.Config, []string{insert}))
	if v.Blocked != "" {
		a.RecordBlocked(cn, v.Blocked, insert)
		x.err = v.Blocked
		return
	}
	if v.Confirm {
		a.AskConfirm(cn, v, fmt.Sprintf("Add %s generated rows to %s?", widgets.HumanCount(int64(n)), x.obj.Name), "Add Rows", insert, func() { a.runFill(x) })
		return
	}
	a.runFill(x)
}

func (a *App) runFill(x *fillDialog) {
	n, err := x.rowCount()
	var p fillPlan
	if err == nil {
		p, err = x.plan()
	}
	if err != nil {
		x.err = err.Error()
		return
	}
	cn, obj := x.conn, x.obj
	if a.filling != x {
		return // closed while the fill was being confirmed
	}
	if cn.DB == nil {
		x.err = cn.Config.Name + " is not connected."
		return
	}
	target := &db.EditTarget{Dialect: cn.DB.Dialect, Schema: obj.Schema, Table: obj.Name, Columns: p.cols}
	ctx, cancel := context.WithCancel(context.Background())
	x.running, x.cancel, x.done, x.err = true, cancel, 0, ""
	pool, cfg, database := cn.DB, cn.Config, x.database
	started := time.Now()
	go func() {
		defer cancel()
		r := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
		written, err := writeRows(ctx, a, pool, cfg, database, rowWrite{target: target, cols: p.cols, verb: "added",
			read: func(ctx context.Context, batch int, emit func([][]any) error) error {
				for from := 0; from < n; from += batch {
					if err := ctx.Err(); err != nil {
						return err
					}
					if err := emit(p.rows(r, cfg.Engine, from, min(from+batch, n))); err != nil {
						return err
					}
				}
				return nil
			},
			progress: func(done int64) { a.Post(func() { x.done = done }) }})
		ev := audit.Event{Kind: audit.KindImport, Database: database, Rows: written,
			Statement: "INSERT INTO " + obj.Name, Detail: fmt.Sprintf("%d generated rows", n)}
		if err != nil {
			ev.Error = err.Error()
		}
		a.Record(&cfg, ev)
		a.Post(func() {
			x.running, x.cancel = false, nil
			if err != nil {
				x.err = err.Error()
				a.Notify(started, "Generating rows failed", obj.Name+" · "+redact.Secrets(widgets.FirstLine(err.Error())), nil)
				return
			}
			x.open = false
			text := fmt.Sprintf("Added %s generated rows to %s", widgets.HumanCount(written), obj.Name)
			a.toast = &pendingToast{text: text}
			a.Notify(started, "Rows generated", text, nil)
			a.reloadTableTabs(cn, obj.Schema, obj.Name)
		})
	}()
}

func (a *App) fillView(c *ui.Context) {
	x := a.filling
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	_, winH := c.Size()
	labels := make([]string, len(testdata.Kinds))
	for i, k := range testdata.Kinds {
		labels[i] = k.Label()
	}
	ui.Modal(c, &x.open, func() {
		ui.Column(c).Width(980).MaxHeight(winH - 80).Gap(12).Children(func() {
			ui.Text(c, "Generate Test Data for "+x.obj.Schema+"."+x.obj.Name).FontSize(15).Bold().SingleLine()
			if x.loading {
				ui.Row(c).Gap(8).Children(func() {
					ui.Spinner(c).Size(14, 14)
					ui.Text(c, "Reading the table…").TextColor(pal.Muted)
				})
			}
			if len(x.cols) > 0 {
				ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "Rows to add")
					ui.TextInput(c, &x.count).Width(120).Label("Rows to add").Disabled(x.running).AutoFocus()
					ui.Text(c, "Keys continue from the highest; a column referring to another table takes the values it holds.").
						FontSize(12).TextColor(pal.Muted).Shrink(1)
				})
				ui.Scroll(c).MaxHeight(340).Border(1, th.Border).Radius(6).Children(func() {
					ui.Column(c).Padding(4, 0).Children(func() {
						for i := range x.cols {
							a.fillColumnRow(c, x, &x.cols[i], labels)
						}
					})
				})
				a.fillSample(c, x)
			}
			if x.err != "" {
				ui.Text(c, x.err).TextColor(th.Danger).Selectable()
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if x.running {
					ui.Spinner(c).Size(14, 14)
					ui.Text(c, widgets.HumanCount(x.done)+" rows written…").FontSize(12).TextColor(pal.Muted)
				}
				ui.Spacer(c)
				if x.running {
					if ui.Button(c, "Cancel").Clicked() {
						x.cancel()
					}
					return
				}
				if ui.Button(c, "Cancel").Clicked() {
					x.open = false
				}
				if ui.Button(c, "New Sample").Disabled(len(x.cols) == 0).Clicked() {
					x.seed = rand.Uint64()
				}
				if ui.PrimaryButton(c, "Add Rows").Disabled(len(x.cols) == 0).Clicked() {
					a.startFill(x)
				}
			})
		})
	})
	if !x.open {
		if x.cancel != nil {
			x.cancel()
		}
		a.filling = nil
	}
}

// fillColumnRow shows a column with its generator and the generator's
// settings.
func (a *App) fillColumnRow(c *ui.Context, x *fillDialog, fc *fillColumn, labels []string) {
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	g, err := fc.generator()
	if err == nil && g.Kind != testdata.Default {
		_, err = g.Prepare(x.run)
	}
	ui.Row(c.Key("fill-"+fc.col.Name)).Padding(4, 10).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Width(200).Children(func() {
			ui.Text(c, fc.col.Name).Font(widgets.MonoFont).FontSize(12.5).SingleLine()
			ui.Text(c, fc.col.Type).FontSize(11).TextColor(pal.Muted).SingleLine()
		})
		ui.Select(c, &fc.kind, labels).Width(170).Label(fc.col.Name + " generator").Disabled(x.running)
		ui.Row(c).Gap(6).Grow(1).Shrink(1).AlignItems(ui.Center).Children(func() {
			input := func(value *string, label string, width float32) {
				ui.TextInput(c, value).Placeholder(label).Label(fc.col.Name + " " + label).Width(width).Disabled(x.running)
			}
			switch {
			case g.Kind == testdata.Sequence:
				input(&fc.gen.From, "from", 120)
			case g.Kind.Ranged():
				input(&fc.gen.From, "from", 150)
				ui.Text(c, "to").TextColor(pal.Muted)
				input(&fc.gen.To, "to", 150)
			case g.Kind == testdata.OneOf:
				ui.TextInput(c, &fc.values).Placeholder("values, by commas").Label(fc.col.Name + " values").Grow(1).Disabled(x.running)
			case g.Kind == testdata.Reference:
				ui.Text(c, fmt.Sprintf("%s values of %s", widgets.HumanCount(int64(len(g.Values))), fc.refTo)).FontSize(12).TextColor(pal.Muted)
			case g.Kind == testdata.Default:
				ui.Text(c, "Left out: the database fills it.").FontSize(12).TextColor(pal.Muted)
			case fc.unique && (g.Kind.Textual() || g.Kind == testdata.UUID):
				ui.Text(c, "Each unique.").FontSize(12).TextColor(pal.Muted)
			case fc.unique:
				ui.Text(c, "Values may repeat, which the column refuses.").FontSize(12).TextColor(th.Warning)
			}
			if fc.note != "" && err == nil {
				ui.Text(c, fc.note).FontSize(12).TextColor(th.Warning).Shrink(1)
			}
			if err != nil {
				ui.Text(c, err.Error()).FontSize(12).TextColor(th.Danger).Shrink(1)
			}
		})
		if fc.col.Nullable && g.Kind != testdata.Default && g.Kind != testdata.Null {
			ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c, &fc.nulls).Width(56).Label(fc.col.Name + " NULL percent").Disabled(x.running)
				ui.Text(c, "% NULL").FontSize(12).TextColor(pal.Muted)
			})
		}
	})
}

// fillSample shows a few rows as the fill would make them.
func (a *App) fillSample(c *ui.Context, x *fillDialog) {
	pal := widgets.PaletteOf(c)
	th := c.Theme()
	p, err := x.plan()
	if err != nil {
		return
	}
	r := rand.New(rand.NewPCG(x.seed, x.seed))
	rows := make([][]any, fillPreviewRows)
	for i := range rows {
		rows[i] = make([]any, len(p.values))
		for j, values := range p.values {
			rows[i][j] = values(r, i)
		}
	}
	const cellW = 150
	ui.Text(c, "Sample").FontSize(12).TextColor(pal.Muted)
	ui.ScrollHorizontal(c).Border(1, th.Border).Radius(6).Children(func() {
		ui.Column(c).Padding(4, 0).Children(func() {
			ui.Row(c).Padding(2, 10).Gap(10).Children(func() {
				for _, col := range p.cols {
					ui.Text(c, col.Name).Font(widgets.MonoFont).FontSize(11.5).Bold().Width(cellW).SingleLine()
				}
			})
			for _, row := range rows {
				ui.Row(c).Padding(2, 10).Gap(10).Children(func() {
					for j, v := range row {
						text := "NULL"
						if v != nil {
							text = db.Display(db.ValueText(v, p.canonical[j]))
						}
						ui.Text(c, text).Font(widgets.MonoFont).FontSize(11.5).Width(cellW).SingleLine()
					}
				})
			}
		})
	})
}
