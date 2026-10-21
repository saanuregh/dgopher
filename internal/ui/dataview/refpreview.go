package dataview

import (
	"cmp"
	"slices"

	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// refPreview is the row a foreign key's value points at, as its card
// shows it: read once, kept while the viewer is open.
type refPreview struct {
	asked   bool // on production, the user asked for the row
	loading bool
	read    bool
	cols    []string
	row     []any // nil when no row has the value
	masked  []bool
	err     string
}

// refPreviewColumns is how many of a row's columns its card shows.
const refPreviewColumns = 8

// refPreviewCard fills the card of a foreign key's cell: the row it
// points at, read when the card first shows, or, on production, once
// asked; one read at a time.
func (v *Viewer) refPreviewCard(c *ui.Context, card *widgets.Card, src *Source, row, col int, value any) {
	fk, ok := v.refOf(col)
	if !ok || value == nil {
		return
	}
	cn := v.source.Conn
	engine := cn.Config.Engine
	literal := db.Literal(engine, value)
	card.Header(c, widgets.IconTable, widgets.ToneTable, fk.RefTable, fk.RefColumns[0]+" = "+db.Display(value))
	key := fk.RefSchema + "\x00" + fk.RefTable + "\x00" + fk.RefColumns[0] + "\x00" + literal
	if v.refPreviews == nil {
		v.refPreviews = map[string]*refPreview{}
	}
	p := v.refPreviews[key]
	if p == nil {
		p = &refPreview{}
		v.refPreviews[key] = p
	}
	where := v.dialect().Quote(fk.RefColumns[0]) + " = " + literal
	production := cn.Config.Env == db.Production
	switch {
	case production && !p.asked && !p.read:
		card.Note(c, "Production: the row is read only when you ask.")
	case p.err != "":
		card.Note(c, "Could not read the row: "+widgets.FirstLine(p.err)).Tooltip(p.err)
	case p.read && p.row == nil:
		card.Note(c, "No row of "+fk.RefTable+" has this value.")
	case p.read:
		rows := make([][2]string, 0, refPreviewColumns)
		for i, c := range p.cols[:min(len(p.cols), refPreviewColumns)] {
			text := v.a.Settings().ViewFormat.Format(p.row[i])
			switch {
			case i < len(p.masked) && p.masked[i]:
				text = MaskedText
			case p.row[i] == nil:
				text = "NULL"
			}
			rows = append(rows, [2]string{c, widgets.FirstLine(text)})
		}
		card.Rows(c, rows)
		if more := len(p.cols) - refPreviewColumns; more > 0 {
			card.Meta(c, "+ "+widgets.Count(more, "more column"))
		}
	default:
		card.Placeholder(c, 4)
		v.readRefPreview(p, fk, where)
	}
	card.Actions(c, func(a *widgets.CardActions) {
		if production && !p.asked && !p.read && a.KeepOpen(c, "Show Row") {
			p.asked = true
		}
		if p.err != "" && a.KeepOpen(c, "Try Again") {
			*p = refPreview{asked: p.asked}
		}
		if a.Button(c, "Open Row", "") {
			v.openRefWhere(fk, where)
		}
	})
}

// readRefPreview reads the row of a preview, unless another read goes
// on: the card asks again in its next frame.
func (v *Viewer) readRefPreview(p *refPreview, fk db.ForeignKey, where string) {
	if p.loading || v.refReading {
		return
	}
	p.loading, v.refReading = true, true
	schema := cmp.Or(fk.RefSchema, v.source.Table.Schema)
	q := "SELECT * FROM " + db.QualifiedName(v.dialect(), schema, fk.RefTable) + " WHERE " + where
	// A card read off the editor's session: a hover never joins, nor
	// aborts, the user's transaction.
	v.queryOnPool(q, nil, 1, func(cols []string, rows [][]any, err error) {
		p.loading, v.refReading, p.read = false, false, true
		if err != nil {
			p.err = err.Error()
			return
		}
		p.cols = cols
		if len(rows) > 0 {
			p.row = rows[0]
		}
		p.masked = MaskedColumns(v.source.Conn, schema, fk.RefTable, cols, v.a.Settings().ShowSensitive)
	})
}

// refKeyColumn reports whether a column of the rows is a foreign key's
// with a card.
func (v *Viewer) refKeyColumn(col int) bool {
	_, ok := v.refOf(col)
	return ok
}

// headerColumnCard fills a header's card with its table's column, false
// for a column that is no table's, as an expression's.
func (v *Viewer) headerColumnCard(c *ui.Context, card *widgets.Card, col int) bool {
	if v.source.Table == nil || col < len(v.bound) && v.bound[col] == "" && len(v.bound) > 0 {
		return false
	}
	name := v.tableName(col)
	i := slices.IndexFunc(v.columns, func(c db.Column) bool { return c.Name == name })
	if i < 0 {
		return false
	}
	t := v.source.Table
	ColumnCard(c, card, v.a, v.source.Conn, v.source.Database, t.Schema, t.Name, v.columns[i], ColumnCardOptions{NoOpen: true})
	return true
}
