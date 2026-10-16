package dataview

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"dgopher/internal/db"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// cellCardKind is what a cell's card shows, by what its value is.
type cellCardKind uint8

const (
	cardNone cellCardKind = iota
	cardJSON
	cardRef // the row a foreign key points at
	cardTime
	cardText
	cardValue // a plain value, whole, with its column
)

func (k cellCardKind) String() string {
	return [...]string{"none", "json", "ref", "time", "text", "value"}[k]
}

// cellCardOf is the card of a cell with value v of a column, clipped
// when its text is wider than the cell: a JSON document's, a time's, or
// a long or cut text's; none for a value the cell shows whole.
func cellCardOf(v any, name, typ string, clipped bool) cellCardKind {
	lowerType := strings.ToLower(typ)
	switch x := v.(type) {
	case nil:
		return cardNone
	case time.Time:
		return cardTime
	case int64:
		if unixLike(x) && timeName(name) {
			return cardTime
		}
		return cardNone
	case int, int32, float64, float32, bool:
		return cardNone
	}
	s, ok := v.(string)
	if !ok {
		if t, ok := v.(db.Typed); ok {
			s = string(t)
		} else if b, ok := v.([]byte); ok && utf8.Valid(b) {
			s = string(b)
		} else {
			return cardNone
		}
	}
	trimmed := strings.TrimSpace(s)
	if len(trimmed) > 1 && (trimmed[0] == '{' || trimmed[0] == '[') && json.Valid([]byte(trimmed)) {
		return cardJSON
	}
	// A date kept as text, as SQLite keeps them, in a column named as one.
	if timeName(name) && s != "" {
		if _, _, ok := parseDateText(s, true); ok {
			return cardTime
		}
	}
	if dateType(lowerType) != "" {
		if _, _, ok := parseDateText(s, dateType(lowerType) == "datetime"); ok && s != "" {
			return cardTime
		}
	}
	if clipped || utf8.RuneCountInString(s) > cellTextRunes || strings.ContainsAny(s, "\r\n") {
		return cardText
	}
	return cardNone
}

// cellTextRunes is how many runes of a value a cell shows at most.
const cellTextRunes = 200

// unixLike reports whether n reads as a time since 1970 in seconds or
// milliseconds, between 2001 and 2100.
func unixLike(n int64) bool {
	return n >= 1e9 && n < 4.1e9 || n >= 1e12 && n < 4.1e12
}

// timeName reports whether a column's name says it holds times.
func timeName(name string) bool {
	n := strings.ToLower(name)
	for _, suffix := range []string{"_at", "_time", "time", "timestamp", "_ts", "_date", "date", "_on"} {
		if strings.HasSuffix(n, suffix) {
			return true
		}
	}
	return false
}

// jsonNode is a node of a JSON document, its keys in their order.
type jsonNode struct {
	key      string // its key in an object, "" else
	index    int    // its place in an array, -1 else
	kind     byte   // '{', '[', or 0 for a scalar
	scalar   string // a scalar as JSON writes it
	children []*jsonNode
	// parent is the node holding it; id tells it from the document's
	// others, by its path; label is its key, quoted, or its place.
	parent    *jsonNode
	id, label string
}

// path is the keys and places leading to the node from the document's
// top, as JSONPath takes them.
func (n *jsonNode) path() []any {
	var out []any
	for ; n.parent != nil; n = n.parent {
		if n.index >= 0 {
			out = append(out, n.index)
		} else {
			out = append(out, n.key)
		}
	}
	slices.Reverse(out)
	return out
}

// parseJSON reads a JSON document, keeping its keys' order, which a Go
// map would lose.
func parseJSON(s string) (*jsonNode, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	n, err := readJSON(dec, nil, "", -1)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("text after the document")
	}
	return n, nil
}

// readJSON reads a node of a document, the key or the place (-1 for
// none) parent holds it at.
func readJSON(dec *json.Decoder, parent *jsonNode, key string, index int) (*jsonNode, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	n := &jsonNode{key: key, index: index, parent: parent}
	if parent != nil {
		n.label = strconv.Quote(key)
		if index >= 0 {
			n.label = strconv.Itoa(index)
		}
		n.id = parent.id + "\x00" + n.label
	}
	switch t := tok.(type) {
	case json.Delim:
		n.kind = byte(t)
		for i := 0; dec.More(); i++ {
			key := ""
			if t == '{' {
				k, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ = k.(string)
			}
			index := -1
			if t == '[' {
				index = i
			}
			child, err := readJSON(dec, n, key, index)
			if err != nil {
				return nil, err
			}
			n.children = append(n.children, child)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
	case string:
		b, _ := json.Marshal(t)
		n.scalar = string(b)
	case json.Number:
		n.scalar = t.String()
	case bool:
		n.scalar = strconv.FormatBool(t)
	case nil:
		n.scalar = "null"
	}
	return n, nil
}

// JSONPath writes the expression reading a JSON column at path, keys and
// array places, as the engine reads it; leaf says a scalar is read, as
// text, rather than a part of the document.
func JSONPath(engine db.Engine, column string, path []any, leaf bool) string {
	col := db.QuoteIfNeeded(db.DialectOf(engine), column)
	sqlString := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	switch engine {
	case db.Postgres:
		var b strings.Builder
		b.WriteString(col)
		for i, p := range path {
			op := "->"
			if leaf && i == len(path)-1 {
				op = "->>"
			}
			b.WriteString(op)
			switch x := p.(type) {
			case int:
				b.WriteString(strconv.Itoa(x))
			case string:
				b.WriteString(sqlString(x))
			}
		}
		return b.String()
	case db.ClickHouse:
		fn := "JSONExtractRaw"
		if leaf {
			fn = "JSONExtractString"
		}
		parts := []string{col}
		for _, p := range path {
			switch x := p.(type) {
			case int:
				parts = append(parts, strconv.Itoa(x+1)) // ClickHouse counts from 1
			case string:
				parts = append(parts, sqlString(x))
			}
		}
		return fn + "(" + strings.Join(parts, ", ") + ")"
	}
	// MySQL, SQLite and DuckDB read a JSON path, $.a[0].b.
	var b strings.Builder
	b.WriteString("$")
	for _, p := range path {
		switch x := p.(type) {
		case int:
			b.WriteString("[" + strconv.Itoa(x) + "]")
		case string:
			if plainKey(x) {
				b.WriteString("." + x)
			} else {
				b.WriteString(`."` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(x) + `"`)
			}
		}
	}
	op := "->"
	if leaf {
		op = "->>"
	}
	text := b.String()
	if engine == db.MySQL {
		// MySQL reads a backslash in a string as an escape.
		text = strings.ReplaceAll(text, `\`, `\\`)
	}
	return col + op + sqlString(text)
}

// plainKey reports whether a key goes in a JSON path unquoted.
func plainKey(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// jsonCardMax is the longest document a card shows as a tree; a longer
// one shows its start, the value editor the rest.
const jsonCardMax = 256 << 10

// jsonTreeHeight is the most a tree takes of its card before it scrolls.
const jsonTreeHeight = 240

// jsonCardLines is how many lines of a tree a card shows at most.
const jsonCardLines = 400

// gridCell is a cell of a grid's rows: its row, as Source.Rows holds
// it, and its column.
type gridCell struct{ data, col int }

// cellCardRead is what the grid last worked out of the cell with a card:
// read again only when the cell, its value or its width changes, not in
// each frame the pointer is on it.
type cellCardRead struct {
	cell  gridCell
	value string
	width float32
	kind  cellCardKind
}

// cellCard gives a cell its card, when its value has more to show than
// the cell does, asked for by Alt+click or the cell's menu: the pointer
// crosses cells all the time, so none opens on a rest. Only the cell
// asked for does any work.
func (g *Grid) cellCard(c *ui.Context, a Host, src *Source, box ui.Element, data, col int, v any, shown string) {
	cell := gridCell{data, col}
	if g.cardCell != cell {
		return
	}
	if g.editing != nil || g.isMasked(col) || v == unset || v == db.Default {
		return
	}
	name, typ := src.Cols[col].Name, src.Cols[col].Type
	text := a.Settings().ViewFormat.Format(v)
	if t, ok := v.(db.Typed); ok {
		text = string(t)
	} else if s, ok := v.(string); ok {
		text = s
	}
	width := box.Bounds().W
	r := &g.cardRead
	if r.cell != cell || r.value != text || r.width != width {
		tw, _ := c.MeasureText(0, ui.Span{Text: shown, Size: a.Settings().GridFontSize()})
		*r = cellCardRead{cell: cell, value: text, width: width, kind: cellCardOf(v, name, typ, width > 0 && tw > width-4)}
		if r.kind != cardJSON && g.hasRefPreview != nil && g.hasRefPreview(col) {
			r.kind = cardRef
		}
		if r.kind == cardNone {
			r.kind = cardValue // every cell has a card, asked for
		}
	}
	if r.kind == cardNone {
		g.cardAsked = false
		return
	}
	kind := r.kind
	key := fmt.Sprintf("cell:%p:%d:%d", g, data, col)
	if g.cardAsked {
		g.cardAsked = false
		widgets.ShowCard(c, key)
	}
	o := widgets.CardOptions{Place: widgets.PlaceBelow, Wide: kind == cardJSON || kind == cardText, OnRequest: true}
	widgets.HoverCard(c, box, widgets.CardGrid, key, o, func(c *ui.Context, card *widgets.Card) {
		switch kind {
		case cardJSON:
			g.jsonCard(c, card, a, src, data, col, text)
		case cardRef:
			g.fillRefPreview(c, card, src, data, col, v)
		case cardTime:
			timeCard(c, card, a, name, typ, v)
		case cardText:
			g.textCard(c, card, a, src, data, col, text)
		case cardValue:
			g.valueCard(c, card, a, src, data, col, v, text)
		}
	})
}

// valueCard shows a plain value, whole, and its column.
func (g *Grid) valueCard(c *ui.Context, card *widgets.Card, a Host, src *Source, data, col int, v any, text string) {
	card.Header(c, widgets.IconColumns, widgets.ToneColumn, src.Cols[col].Name, erType(src.Cols[col].Type))
	card.Section(c, func() {
		if v == nil {
			ui.Text(c, "NULL").TextColor(widgets.PaletteOf(c).Null).Italic()
			return
		}
		ui.Text(c, text).Font(widgets.MonoFont).FontSize(12.5).Selectable()
	})
	card.Actions(c, func(ac *widgets.CardActions) {
		if v != nil {
			ac.Copy(c, "Copy", text)
		}
		if ac.AnchoredButton(c, "Open in Value Editor") {
			openValueEditor(a, g, src, data, col)
		}
	})
}

// textCard shows a long or cut text whole.
func (g *Grid) textCard(c *ui.Context, card *widgets.Card, a Host, src *Source, data, col int, text string) {
	card.Header(c, widgets.IconFile, widgets.ToneValue, src.Cols[col].Name,
		widgets.Count(utf8.RuneCountInString(text), "character")+lineCount(text))
	shown := text[:min(len(text), jsonCardMax)]
	card.Section(c, func() { ui.Text(c, shown).Selectable().FontSize(12.5) })
	if len(shown) < len(text) {
		card.Note(c, "The value goes on: the value editor shows it whole.")
	}
	card.Actions(c, func(ac *widgets.CardActions) {
		ac.Copy(c, "Copy", text)
		if ac.AnchoredButton(c, "Open in Value Editor") {
			openValueEditor(a, g, src, data, col)
		}
	})
}

func lineCount(s string) string {
	if n := strings.Count(s, "\n") + 1; n > 1 {
		return " · " + strconv.Itoa(n) + " lines"
	}
	return ""
}

// jsonCard shows a JSON document as a tree, folding objects and arrays;
// a click on a key copies the expression reading it.
func (g *Grid) jsonCard(c *ui.Context, card *widgets.Card, a Host, src *Source, data, col int, text string) {
	name := src.Cols[col].Name
	// The document is read again, and its folds forgotten, when the cell
	// holds another, as rows read again.
	tree := widgets.Keep(card, "doc", func() jsonTree { return jsonTree{} })
	if len(text) <= jsonCardMax {
		tree.read(text, 0)
	} else {
		*tree = jsonTree{}
	}
	what := widgets.HumanBytes(int64(len(text)))
	if n := tree.node; n != nil {
		switch n.kind {
		case '{':
			what = "object · " + n.size() + " · " + what
		case '[':
			what = "array · " + n.size() + " · " + what
		}
	}
	card.Header(c, widgets.IconCode, widgets.ToneValue, name, what)
	card.Meta(c, tree.hint(c))
	if tree.node == nil {
		card.Section(c, func() {
			ui.Text(c, text[:min(len(text), 4096)]).Font(widgets.MonoFont).FontSize(12).Selectable()
		})
		card.Note(c, "Too long to show here: the value editor shows it whole.")
	} else {
		cut := false
		// The tree scrolls in its own box, the header and the buttons
		// staying in view.
		card.Section(c, func() {
			ui.Scroll(c).MaxHeight(jsonTreeHeight).Children(func() {
				cut = tree.view(c, g.SQLOpts.Engine, name, jsonCardLines)
			})
		})
		if cut {
			card.Note(c, "The document goes on: the value editor shows it whole.")
		}
	}
	card.Actions(c, func(ac *widgets.CardActions) {
		ac.Copy(c, "Copy", text)
		ac.CopyFunc(c, "Copy Pretty", func() string { return PrettyValue(text) })
		if ac.AnchoredButton(c, "Open in Value Editor") {
			openValueEditor(a, g, src, data, col)
		}
	})
}

// timeCard shows a time in the computer's zone and in UTC, how long ago
// it was, and as Unix time.
func timeCard(c *ui.Context, card *widgets.Card, a Host, name, typ string, v any) {
	var when time.Time
	switch x := v.(type) {
	case time.Time:
		when = x
	case int64:
		when = time.Unix(x, 0)
		if x >= 1e12 {
			when = time.UnixMilli(x)
		}
	default:
		s := fmt.Sprint(v)
		if t, ok := v.(db.Typed); ok {
			s = string(t)
		}
		_, t, ok := parseDateText(s, dateType(strings.ToLower(typ)) == "datetime")
		if !ok {
			card.Header(c, widgets.IconClock, widgets.ToneValue, name, "")
			card.Note(c, "Not a time this card can read.")
			return
		}
		when = t
	}
	card.Header(c, widgets.IconClock, widgets.ToneValue, name, typ)
	local := when.In(time.Local)
	card.Rows(c, [][2]string{
		{"Local", local.Format("2006-01-02 15:04:05 MST")},
		{"UTC", when.UTC().Format("2006-01-02 15:04:05")},
		{"Relative", relativeTime(a.Now().Sub(when))},
		{"Unix", strconv.FormatInt(when.Unix(), 10)},
	})
	card.Actions(c, func(ac *widgets.CardActions) {
		ac.Copy(c, "Copy ISO 8601", when.UTC().Format(time.RFC3339))
		ac.Copy(c, "Copy Unix", strconv.FormatInt(when.Unix(), 10))
	})
}

// relativeTime says how long ago d was, or how far ahead, roughly.
func relativeTime(d time.Duration) string {
	ago := d >= 0
	if !ago {
		d = -d
	}
	var s string
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		s = widgets.Count(int(d/time.Minute), "minute")
	case d < 48*time.Hour:
		s = widgets.Count(int(d/time.Hour), "hour")
	case d < 60*24*time.Hour:
		s = widgets.Count(int(d/(24*time.Hour)), "day")
	case d < 730*24*time.Hour:
		s = widgets.Count(int(d/(30*24*time.Hour)), "month")
	default:
		s = widgets.Count(int(d/(365*24*time.Hour)), "year")
	}
	if ago {
		return s + " ago"
	}
	return "in " + s
}

// headerCard gives a column's header its card: the table's column, as
// the viewer knows it, else the name and type the rows came with.
func (g *Grid) headerCard(c *ui.Context, src *Source, cell ui.Element, col int) {
	if cell.PressedInside() {
		g.headerPressed = true
		if c.Modifiers()&ui.Alt != 0 {
			g.cardHeader, g.headerAsked = col, true
		}
	}
	if g.cardHeader != col {
		return // only the header asked for does any work
	}
	key := fmt.Sprintf("header:%p:%d", g, col)
	if g.headerAsked {
		g.headerAsked = false
		widgets.ShowCard(c, key)
	}
	widgets.HoverCard(c, cell, widgets.CardGrid, key, widgets.CardOptions{Place: widgets.PlaceBelow, OnRequest: true}, func(c *ui.Context, card *widgets.Card) {
		if g.fillHeaderCard != nil && g.fillHeaderCard(c, card, col) {
			return
		}
		card.Header(c, widgets.IconColumns, widgets.ToneColumn, src.Cols[col].Name, "")
		if typ := src.Cols[col].Type; typ != "" {
			card.Pills(c, widgets.CardPill{Text: erType(typ), Mono: true})
		}
		card.Note(c, "A result's column, not a column of one table: its name and type only.")
	})
}

// hasCard reports whether a cell has a card: every cell does but a masked
// one, whose card would show what the grid hides, and one being added
// without a value yet.
func (g *Grid) hasCard(src *Source, data, col int) bool {
	if g.isMasked(col) || data >= len(src.Rows) {
		return false
	}
	v, _ := g.value(src, data, col)
	return v != unset && v != db.Default
}
