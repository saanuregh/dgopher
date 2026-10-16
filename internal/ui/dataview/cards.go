package dataview

import (
	"strings"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// TableCardOptions are what a table's card offers besides its facts,
// by where it shows.
type TableCardOptions struct {
	// Insert, when set, makes each column a button inserting its name,
	// as the editor's card does at the caret.
	Insert func(col string)
	// Used holds the columns, in lower case, the statement being written
	// uses, which the card marks.
	Used map[string]bool
	// Join, when set, makes each related table a button writing the
	// join to it.
	Join func(r Relation)
	// Usages, when set, adds Find Usages.
	Usages func()
	// NewQuery adds New Query, an editor reading the table.
	NewQuery bool
	// Alias is the name the statement gives the table, "" for none: the
	// card leads with it.
	Alias string
	// StructureKeys are the keys opening the structure, shown by its
	// button, "" for none.
	StructureKeys string
}

// Relation is a table related to another by a foreign key: Outgoing
// when the other table's key points at it, its Columns those of the
// other table and RefColumns its own; else it points at the other, its
// Columns its own.
type Relation struct {
	Schema, Table       string
	Columns, RefColumns []string
	Outgoing            bool
}

// TableCard fills a card about a table or a view: what it is, how big,
// its comment, its columns, keys first, and the tables it joins.
func TableCard(c *ui.Context, card *widgets.Card, h Host, cn *connection.Conn, database string, obj db.Object, o TableCardOptions) {
	icon, kind := widgets.IconTable, "Table"
	switch obj.Kind {
	case db.KindView:
		icon, kind = widgets.IconView, "View"
	case db.KindMaterializedView:
		icon, kind = widgets.IconView, "Materialized view"
	}
	where := placeOf(cn, database, obj.Schema)
	if o.Alias != "" && !strings.EqualFold(o.Alias, obj.Name) {
		card.Header(c, icon, widgets.ToneTable, o.Alias, "alias of "+obj.Name)
	} else {
		card.Header(c, icon, widgets.ToneTable, obj.Name, where)
	}
	var rows, size string
	if obj.Rows >= 0 && obj.Kind == db.KindTable {
		rows = "≈ " + widgets.HumanCount(obj.Rows) + " rows (estimate)"
	}
	if obj.Bytes > 0 {
		size = widgets.HumanBytes(obj.Bytes)
	}
	if o.Alias != "" && !strings.EqualFold(o.Alias, obj.Name) {
		kind += " " + where
	}
	card.Meta(c, kind, rows, size, obj.Engine)
	card.Comment(c, obj.Comment)

	connected := cn.Status == connection.StatusConnected
	key := connection.ObjectKey{Database: database, Schema: obj.Schema, Name: obj.Name}
	cols, haveCols := cn.Columns[key]
	rel, haveRel := cn.Relations[key]
	if connected {
		cols, haveCols = connection.ColumnsOf(h, cn, database, obj.Schema, obj.Name)
		if !haveRel {
			connection.LoadRelations(h, cn, database, obj.Schema, obj.Name, nil)
		}
	}
	switch {
	case haveCols:
		refs := map[string]string{}
		for _, fk := range rel.FKs {
			if len(fk.Columns) == 1 {
				refs[strings.ToLower(fk.Columns[0])] = fk.RefTable
			}
		}
		list := make([]widgets.CardColumn, len(cols))
		for i, col := range cols {
			lower := strings.ToLower(col.Name)
			list[i] = widgets.CardColumn{Name: col.Name, Type: erType(col.Type), Key: col.PrimaryKey,
				Ref: refs[lower], Used: o.Used[lower]}
		}
		card.Columns(c, list, widgets.ColumnsOptions{Insert: o.Insert})
	case connected && cn.LoadErr[key] == "":
		card.Placeholder(c, 4)
	case connected:
		card.Note(c, "Could not read its columns: "+widgets.FirstLine(cn.LoadErr[key]))
	default:
		card.Note(c, "Connect to see its columns.")
	}
	relations(c, card, h, cn, database, obj.Schema, rel, o)

	card.Actions(c, func(a *widgets.CardActions) {
		if a.Button(c, "Open Data", "") {
			h.OpenTable(cn, database, obj, PageData)
		}
		if a.Button(c, "Structure", o.StructureKeys) {
			h.OpenTable(cn, database, obj, PageStructure)
		}
		if o.Usages != nil && a.Button(c, "Usages", "") {
			o.Usages()
		}
		if o.NewQuery && a.Button(c, "New Query", "") {
			quoted := db.QualifiedName(db.DialectOf(cn.Config.Engine), obj.Schema, obj.Name)
			h.NewQueryTab(cn, database, "SELECT *\nFROM "+quoted+"\nLIMIT 100;\n")
		}
		a.Copy(c, "Copy Name", obj.Name)
	})
}

// relations adds the tables a table joins: those it points at, then
// those pointing at it, each opening the table, or writing the join
// where the card offers that.
func relations(c *ui.Context, card *widgets.Card, h Host, cn *connection.Conn, database, schema string, rel connection.Relations, o TableCardOptions) {
	var list []Relation
	for _, fk := range rel.FKs {
		list = append(list, Relation{Schema: fk.RefSchema, Table: fk.RefTable, Columns: fk.Columns, RefColumns: fk.RefColumns})
	}
	for _, r := range rel.Refs {
		list = append(list, Relation{Schema: r.Schema, Table: r.Table, Columns: r.Columns, RefColumns: r.RefColumns, Outgoing: true})
	}
	if len(list) == 0 {
		return
	}
	pills := make([]widgets.CardPill, len(list))
	for i, r := range list {
		arrow := "→ "
		if r.Outgoing {
			arrow = "← "
		}
		open := func() {
			if o.Join != nil {
				o.Join(r)
				return
			}
			s := r.Schema
			if s == "" {
				s = schema
			}
			h.OpenTable(cn, database, objectOf(cn, database, s, r.Table), PageData)
		}
		pills[i] = widgets.CardPill{Text: arrow + r.Table, Tone: widgets.PillLink, Open: open}
	}
	card.LabeledPills(c, "Joins", pills...)
}

// objectOf is a table of the catalog by its name, or one by that name
// alone when the catalog has not read it.
func objectOf(cn *connection.Conn, database, schema, name string) db.Object {
	for _, o := range cn.Objects[connection.SchemaKey{Database: database, Schema: schema}] {
		if strings.EqualFold(o.Name, name) {
			return o
		}
	}
	return db.Object{Schema: schema, Name: name, Kind: db.KindTable, Rows: -1, Bytes: -1}
}

// placeOf names where a table is, as "shop.main": the connection and
// its schema.
func placeOf(cn *connection.Conn, database, schema string) string {
	parts := []string{cn.Config.Name}
	if database != "" {
		parts = append(parts, database)
	}
	if schema != "" {
		parts = append(parts, schema)
	}
	return strings.Join(parts, ".")
}

// ColumnCardOptions are what a column's card offers besides its facts.
type ColumnCardOptions struct {
	// NoOpen leaves out the button opening the column's table, as in the
	// table's own grid.
	NoOpen bool
}

// ColumnCard fills a card about a column of a table: its type and
// constraints as pills, the table its foreign key points at, and its
// comment.
func ColumnCard(c *ui.Context, card *widgets.Card, h Host, cn *connection.Conn, database, schema, table string, col db.Column, o ColumnCardOptions) {
	card.Header(c, widgets.IconColumns, widgets.ToneColumn, col.Name, table)
	pills := []widgets.CardPill{{Text: erType(col.Type), Mono: true}}
	if col.PrimaryKey {
		pills = append(pills, widgets.CardPill{Text: "PRIMARY KEY", Tone: widgets.PillKey})
	} else if !col.Nullable {
		pills = append(pills, widgets.CardPill{Text: "NOT NULL"})
	}
	if col.AutoIncrement {
		pills = append(pills, widgets.CardPill{Text: "auto-increment"})
	}
	if col.HasDefault && col.Default != "" {
		pills = append(pills, widgets.CardPill{Text: "default " + col.Default, Mono: true})
	}
	key := connection.ObjectKey{Database: database, Schema: schema, Name: table}
	rel, ok := cn.Relations[key]
	if !ok && cn.Status == connection.StatusConnected {
		connection.LoadRelations(h, cn, database, schema, table, nil)
	}
	for _, fk := range rel.FKs {
		if len(fk.Columns) != 1 || !strings.EqualFold(fk.Columns[0], col.Name) {
			continue
		}
		ref := fk
		target := ref.RefTable
		if len(ref.RefColumns) == 1 {
			target += "." + ref.RefColumns[0]
		}
		pills = append(pills, widgets.CardPill{Text: "→ " + target, Tone: widgets.PillLink, Open: func() {
			s := ref.RefSchema
			if s == "" {
				s = schema
			}
			h.OpenTable(cn, database, objectOf(cn, database, s, ref.RefTable), PageData)
		}})
	}
	card.Pills(c, pills...)
	card.Comment(c, col.Comment)
	card.Actions(c, func(a *widgets.CardActions) {
		if !o.NoOpen && a.Button(c, "Open "+table, "") {
			h.OpenTable(cn, database, objectOf(cn, database, schema, table), PageData)
		}
		a.Copy(c, "Copy Name", col.Name)
	})
}
