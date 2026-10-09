package dataview

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// refPickerRows is how many rows of the referenced table the picker
// lists at a time.
const refPickerRows = 200

// refPicker chooses the value of a foreign key's cell among the rows of
// the table it points at, which it shows by their key and first columns.
type refPicker struct {
	open     bool
	v        *Viewer
	fk       db.ForeignKey
	row, col int // the cell

	search   string
	searched string
	// columns are the referenced table's, read first; cols and rows those
	// listed, the key first.
	columns []db.Column
	cols    []string
	masked  []bool // the listed columns whose values are hidden
	rows    [][]any
	loading bool
	err     string
	sel     int
	list    ui.ListState
}

// refOf is the foreign key of one column a column of the rows is, if any.
func (v *Viewer) refOf(col int) (db.ForeignKey, bool) {
	name := v.tableName(col)
	for _, fk := range v.fks {
		if len(fk.Columns) == 1 && fk.Columns[0] == name && len(fk.RefColumns) == 1 {
			return fk, true
		}
	}
	return db.ForeignKey{}, false
}

// pickRef opens the picker on the chosen cell, when it is a foreign key's
// the grid may change.
func (v *Viewer) pickRef() {
	order := v.grid.ViewOrder(&v.src)
	if v.grid.SelRow < 0 || v.grid.SelRow >= len(order) {
		return
	}
	row, col := order[v.grid.SelRow], v.grid.selCol
	if fk, ok := v.refOf(col); ok && v.grid.edits != nil && v.grid.columnReadOnly(col) == "" {
		v.openRefPicker(row, col, fk)
	}
}

func (v *Viewer) openRefPicker(row, col int, fk db.ForeignKey) {
	p := &refPicker{open: true, v: v, fk: fk, row: row, col: col, loading: true}
	p.list.Selected = &p.sel
	v.a.Dialogs().refPicker = p
	schema := fk.RefSchema
	if schema == "" {
		schema = v.source.Table.Schema
	}
	poolOf := v.source.Conn.PoolFor(v.source.Database)
	v.a.Background(func() func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		d, err := poolOf(ctx)
		var cols []db.Column
		if err == nil {
			cols, err = d.Dialect.Columns(ctx, d.Catalog(), schema, fk.RefTable)
		}
		return func() {
			if err != nil {
				p.loading, p.err = false, err.Error()
				return
			}
			p.columns = cols
			p.query()
		}
	})
}

// query lists the rows whose key starts with the search, or whose shown
// text columns hold it, ordered by key.
func (p *refPicker) query() {
	v := p.v
	d, e := v.dialect(), v.source.Conn.Config.Engine
	key := p.fk.RefColumns[0]
	p.cols = []string{key}
	var texts []string
	for _, c := range p.columns {
		if c.Name != key && len(p.cols) < 4 {
			p.cols = append(p.cols, c.Name)
			if textType(c.Type) {
				texts = append(texts, c.Name)
			}
		}
	}
	schema := p.fk.RefSchema
	if schema == "" {
		schema = v.source.Table.Schema
	}
	quoted := make([]string, len(p.cols))
	for i, c := range p.cols {
		quoted[i] = d.Quote(c)
	}
	p.masked = MaskedColumns(v.source.Conn, schema, p.fk.RefTable, p.cols, v.a.Settings().ShowSensitive)
	q := "SELECT " + strings.Join(quoted, ", ") + " FROM " + db.QualifiedName(d, schema, p.fk.RefTable)
	search := strings.TrimSpace(p.search)
	if search != "" {
		conds := []string{likeSQL(e, d.Quote(key), "", search, "%", false)}
		for _, t := range texts {
			conds = append(conds, likeSQL(e, d.Quote(t), "%", search, "%", false))
		}
		q += " WHERE " + strings.Join(conds, " OR ")
	}
	q += " ORDER BY " + d.Quote(key) + " LIMIT " + strconv.Itoa(refPickerRows)
	p.loading, p.searched = true, search
	v.queryRows(q, nil, refPickerRows, func(_ []string, rows [][]any, err error) {
		p.loading, p.rows, p.sel, p.err = false, rows, 0, ""
		if err != nil {
			p.err = err.Error()
		}
	})
}

// textType reports whether a column's type holds text, which the picker
// searches.
func textType(typ string) bool {
	t := strings.ToLower(typ)
	return slices.ContainsFunc([]string{"char", "text", "string", "clob", "name"}, func(s string) bool { return strings.Contains(t, s) })
}

// choose puts a row's key in the cell.
func (p *refPicker) choose(i int) {
	if i < 0 || i >= len(p.rows) {
		return
	}
	v := p.v
	var value any
	if k := p.rows[i][0]; k != nil {
		value = db.Typed(db.Display(k))
	}
	v.grid.setValue(&v.src, p.row, p.col, value)
	p.open = false
}

func refPickerView(a Host, c *ui.Context) {
	p := a.Dialogs().refPicker
	th := c.Theme()
	pal := widgets.PaletteOf(c)
	ui.DialogBase(c, &p.open, func(backdrop, panel ui.Element) {
		backdrop.Background(widgets.Backdrop)
		panel.Width(720).Height(520).Radius(12).Background(th.Background).Border(1, th.Border).Clip().Label("Choose a value")
		ui.Row(c).Padding(12, 16).Gap(10).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			ui.Text(c, p.v.src.Cols[p.col].Name+" → "+p.fk.RefTable).FontSize(15).Bold().SingleLine().Shrink(1)
			in := widgets.SearchBox(c, &p.search, "Search the key or the text, then Enter", 0).AutoFocus().Grow(1)
			switch {
			case in.Shortcut(0, ui.KeyDown):
				p.sel = min(p.sel+1, len(p.rows)-1)
				p.list.ScrollIntoView(p.sel)
			case in.Shortcut(0, ui.KeyUp):
				p.sel = max(p.sel-1, 0)
				p.list.ScrollIntoView(p.sel)
			case in.Submitted():
				if strings.TrimSpace(p.search) != p.searched {
					p.query()
				} else {
					p.choose(p.sel)
				}
			}
			if p.loading {
				ui.Spinner(c).Size(14, 14)
			}
		})
		if p.err != "" {
			ui.Text(c, p.err).TextColor(th.Danger).Padding(10, 16).Selectable()
		}
		ui.Row(c).Padding(6, 16).Gap(12).BorderWidth(0, 0, 1, 0).BorderColor(th.Border).Children(func() {
			for i, name := range p.cols {
				t := ui.Text(c, name).FontSize(12).Bold().TextColor(pal.Muted).SingleLine()
				if i == 0 {
					t.Width(140)
				} else {
					t.Grow(1).Shrink(1)
				}
			}
		})
		list := ui.List(c, &p.list, len(p.rows), func(i int) {
			ui.Row(c).Padding(5, 16).Gap(12).Children(func() {
				for j, v := range p.rows[i] {
					text := cellText(db.Display(v), 80)
					switch {
					case v == nil:
						text = "NULL"
					case p.masked[j]:
						text = MaskedText
					}
					t := ui.Text(c, text).SingleLine()
					if j == 0 {
						t.Font(widgets.MonoFont).Width(140)
					} else {
						t.Grow(1).Shrink(1).TextColor(widgets.RowColor(c, pal.Muted, i == p.sel))
					}
				}
			})
		}).Grow(1).Label("Rows").Dividers(1, th.Border).Children(func() {
			if !p.loading && len(p.rows) == 0 && p.err == "" {
				ui.Text(c, "No rows match.").TextColor(pal.Muted).Padding(16)
			}
		})
		if list.Submitted() {
			p.choose(p.sel)
		}
		ui.Row(c).Padding(10, 16).Gap(8).BorderWidth(1, 0, 0, 0).BorderColor(th.Border).Children(func() {
			note := "The first " + strconv.Itoa(refPickerRows) + " rows by key are listed."
			ui.Text(c, note).FontSize(12).TextColor(pal.Muted).Grow(1)
			if ui.Button(c, "Cancel").Clicked() {
				p.open = false
			}
			if ui.PrimaryButton(c, "Choose").Disabled(p.sel < 0 || p.sel >= len(p.rows)).Clicked() {
				p.choose(p.sel)
			}
		})
	})
	if !p.open {
		a.Dialogs().refPicker = nil
	}
}
