package dataview

import (
	"cmp"
	"fmt"
	"strings"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/lists"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// sqlGen writes SQL for rows of a table, with their values as literals,
// for the user to copy or run.
type sqlGen struct {
	dialect       db.Dialect
	engine        db.Engine
	schema, table string
	// key names the table's unique key; without one a row is matched on
	// every column.
	key []string
}

const noKeyNote = "-- No unique key: the row is matched on every column.\n"

func (s sqlGen) name() string { return db.QualifiedName(s.dialect, s.schema, s.table) }

func (s sqlGen) lit(v any) string { return db.Literal(s.engine, v) }

// match is the condition that finds a row: by its key, else by every
// column, NULLs compared with IS NULL.
func (s sqlGen) match(src *Source, row int) string {
	var parts []string
	for i, c := range src.Cols {
		if s.key != nil && !inStrings(s.key, c.Name) {
			continue
		}
		v := src.Rows[row][i]
		if v == nil {
			parts = append(parts, s.dialect.Quote(c.Name)+" IS NULL")
			continue
		}
		parts = append(parts, s.dialect.Quote(c.Name)+" = "+s.lit(v))
	}
	return strings.Join(parts, " AND ")
}

func inStrings(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

// generate writes a SELECT, INSERT, UPDATE or DELETE of rows.
func (s sqlGen) generate(kind string, src *Source, rows []int) string {
	var out []string
	note := ""
	if s.key == nil && kind != "insert" {
		note = noKeyNote
	}
	cols := lists.Map(src.Cols, func(c db.ColumnInfo) string { return s.dialect.Quote(c.Name) })
	switch kind {
	case "select":
		if len(s.key) == 1 {
			col := -1
			for i, c := range src.Cols {
				if c.Name == s.key[0] {
					col = i
				}
			}
			vals := lists.Map(rows, func(r int) string { return s.lit(src.Rows[r][col]) })
			return "SELECT * FROM " + s.name() + " WHERE " + s.dialect.Quote(s.key[0]) + " IN (" + strings.Join(vals, ", ") + ");"
		}
		conds := lists.Map(rows, func(r int) string { return "(" + s.match(src, r) + ")" })
		return note + "SELECT * FROM " + s.name() + " WHERE " + strings.Join(conds, "\n   OR ") + ";"
	case "insert":
		for _, r := range rows {
			vals := make([]string, len(src.Cols))
			for i := range src.Cols {
				vals[i] = s.lit(src.Rows[r][i])
			}
			out = append(out, "INSERT INTO "+s.name()+" ("+strings.Join(cols, ", ")+") VALUES ("+strings.Join(vals, ", ")+");")
		}
	case "update":
		for _, r := range rows {
			var sets []string
			for i, c := range src.Cols {
				if s.key != nil && inStrings(s.key, c.Name) {
					continue
				}
				sets = append(sets, cols[i]+" = "+s.lit(src.Rows[r][i]))
			}
			out = append(out, "UPDATE "+s.name()+" SET "+strings.Join(sets, ", ")+" WHERE "+s.match(src, r)+";")
		}
	case "delete":
		for _, r := range rows {
			out = append(out, "DELETE FROM "+s.name()+" WHERE "+s.match(src, r)+";")
		}
	}
	return note + strings.Join(out, "\n")
}

// sqlDialog shows generated SQL to copy or open in an editor.
type sqlDialog struct {
	open     bool
	title    string
	text     string
	conn     *connection.Conn
	database string
}

func showSQL(a Host, title, text string, cn *connection.Conn, database string) {
	a.Dialogs().sqlShown = &sqlDialog{open: true, title: title, text: text, conn: cn, database: database}
}

func sqlDialogView(a Host, c *ui.Context) {
	d := a.Dialogs().sqlShown
	pal := widgets.PaletteOf(c)
	widgets.Modal(c, &d.open, func() {
		ui.Column(c).Width(760).Gap(12).Children(func() {
			ui.Text(c, d.title).FontSize(15).Bold()
			ui.Scroll(c).MaxHeight(420).Radius(8).Background(pal.EditorBg).Children(func() {
				ui.Text(c, d.text).Font(widgets.MonoFont).FontSize(12.5).Padding(12).Selectable()
			})
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Copy").Clicked() {
					a.WriteClipboard(d.text)
					c.Toast("Copied")
				}
				if ui.Button(c, "Close").Clicked() {
					d.open = false
				}
				if widgets.Activated(c, ui.PrimaryButton(c, "Open in Editor")) {
					d.open = false
					a.NewQueryTab(d.conn, d.database, d.text+"\n")
				}
			})
		})
	})
	dropClosed(&a.Dialogs().sqlShown, d, d.open)
}

// template writes a statement for the table, its values as :name
// parameters for the parameters dialog to ask: select, insert, update,
// delete, upsert, or join with the tables its foreign keys point at.
func (s sqlGen) template(kind string, cols []db.ColumnInfo, fks []db.ForeignKey) string {
	quoted := make([]string, len(cols))
	params := make([]string, len(cols))
	for i, c := range cols {
		quoted[i], params[i] = s.dialect.Quote(c.Name), ":"+paramName(c.Name)
	}
	keyMatch := func() string {
		parts := lists.Map(s.key, func(k string) string { return s.dialect.Quote(k) + " = :" + paramName(k) })
		return strings.Join(parts, " AND ")
	}
	var sets, upserts []string
	for i, c := range cols {
		if inStrings(s.key, c.Name) {
			continue
		}
		sets = append(sets, quoted[i]+" = "+params[i])
		switch s.engine {
		case db.MySQL:
			upserts = append(upserts, quoted[i]+" = VALUES("+quoted[i]+")")
		default:
			upserts = append(upserts, quoted[i]+" = EXCLUDED."+quoted[i])
		}
	}
	insert := "INSERT INTO " + s.name() + " (" + strings.Join(quoted, ", ") + ")\nVALUES (" + strings.Join(params, ", ") + ")"
	switch kind {
	case "select":
		return "SELECT " + strings.Join(quoted, ", ") + "\nFROM " + s.name() + ";"
	case "insert":
		return insert + ";"
	case "update":
		if len(sets) == 0 {
			return "-- Every column is in the key: there is nothing to update."
		}
		if s.key == nil {
			return noKeyNote + "UPDATE " + s.name() + "\nSET " + strings.Join(sets, ", ") + "\nWHERE ;"
		}
		return "UPDATE " + s.name() + "\nSET " + strings.Join(sets, ", ") + "\nWHERE " + keyMatch() + ";"
	case "delete":
		if s.key == nil {
			return noKeyNote + "DELETE FROM " + s.name() + "\nWHERE ;"
		}
		return "DELETE FROM " + s.name() + "\nWHERE " + keyMatch() + ";"
	case "upsert":
		switch {
		case s.key == nil:
			return "-- No unique key: the database cannot tell an existing row from a new one.\n" + insert + ";"
		case s.engine == db.ClickHouse:
			return "-- ClickHouse has no upsert: a ReplacingMergeTree keeps the last row of a key.\n" + insert + ";"
		case s.engine == db.MySQL && len(upserts) == 0:
			return "INSERT IGNORE" + strings.TrimPrefix(insert, "INSERT") + ";"
		case s.engine == db.MySQL:
			return insert + "\nON DUPLICATE KEY UPDATE " + strings.Join(upserts, ", ") + ";"
		}
		keys := db.QuoteNames(s.dialect, s.key)
		if len(upserts) == 0 {
			return insert + "\nON CONFLICT (" + keys + ") DO NOTHING;"
		}
		return insert + "\nON CONFLICT (" + keys + ") DO UPDATE SET " + strings.Join(upserts, ", ") + ";"
	case "join":
		var b strings.Builder
		b.WriteString("SELECT *\nFROM " + s.name() + " t")
		used := map[string]int{"t": 1}
		for _, fk := range fks {
			schema := cmp.Or(fk.RefSchema, s.schema)
			// Quoted, as a table may be named as a keyword; numbered when a
			// table is joined twice.
			name := fk.RefTable
			if used[name]++; used[name] > 1 {
				name = fmt.Sprintf("%s_%d", name, used[name])
			}
			alias := s.dialect.Quote(name)
			var on []string
			for i, col := range fk.Columns {
				if i < len(fk.RefColumns) {
					on = append(on, alias+"."+s.dialect.Quote(fk.RefColumns[i])+" = t."+s.dialect.Quote(col))
				}
			}
			b.WriteString("\nJOIN " + db.QualifiedName(s.dialect, schema, fk.RefTable) + " " + alias + " ON " + strings.Join(on, " AND "))
		}
		return b.String() + ";"
	}
	return ""
}

// paramName is a column's name as a :name parameter can write it.
func paramName(column string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return '_'
	}, column)
}
