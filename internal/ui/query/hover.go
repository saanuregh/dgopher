package query

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/params"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
	code "github.com/egoist/mygo/ui/editor"
)

// hoverKind is what a name under the pointer names.
type hoverKind uint8

const (
	hoverTable hoverKind = iota + 1
	hoverColumn
	hoverColumns // a column of more than one of the statement's tables
	hoverRoutine
	hoverFunction
	hoverParam
	hoverStar
	hoverDerived // a CTE, or a subquery's alias
)

// hoverTarget is what the text at an offset names, as a card shows it.
type hoverTarget struct {
	kind       hoverKind
	start, end int
	obj        db.Object // a table, or the table of a column
	alias      string    // the alias the statement gives obj
	col        db.Column
	tables     []string // the tables a hoverColumns column is in
	item       db.Item
	fn         string
	args       int    // the arguments of a function's call
	param      string // a parameter's key
	refs       []sqltext.TableRef
	derived    derivedTable
	used       map[string]bool // a table's columns the statement names
}

// String describes the target, for tests.
func (h hoverTarget) String() string {
	switch h.kind {
	case hoverTable:
		if h.alias != "" {
			return "table " + h.obj.Name + " as " + h.alias
		}
		return "table " + h.obj.Name
	case hoverColumn:
		return "column " + h.obj.Name + "." + h.col.Name
	case hoverColumns:
		return "column " + h.col.Name + " in " + strings.Join(h.tables, ", ")
	case hoverRoutine:
		return "routine " + h.item.Name
	case hoverFunction:
		return "function " + h.fn
	case hoverParam:
		return "parameter " + h.param
	case hoverStar:
		return "star"
	case hoverDerived:
		return "derived " + h.derived.name + ": " + strings.Join(h.derived.columns, ", ")
	}
	return "none"
}

// derivedTable is a table a statement makes itself: a CTE, or a
// subquery in FROM with its alias.
type derivedTable struct {
	name    string
	cte     bool
	columns []string
	at      int // where its name is defined
}

// hover answers the editor's question about the text at off: a card of
// what it names, or nothing.
func (q *Tab) hover(r code.HoverRequest) {
	off := r.Offset
	target, ok := q.hoverTarget(off)
	if !ok {
		return
	}
	if target.kind == hoverTable {
		target.used = q.usedNames(target.start)
	}
	key := fmt.Sprintf("editor:%p:%d:%d", q, target.start, target.end)
	q.Editor.State.ShowHover(target.start, target.end, "", func(c *ui.Context) {
		widgets.CardContent(c, key, func() { q.Editor.State.Run(code.Cancel) }, func(c *ui.Context, card *widgets.Card) {
			q.hoverCard(c, card, target)
		})
	})
}

// lexedText is the editor's text read once for each version: its
// significant tokens and its statements, which hovers and links, asked
// each frame while Cmd is held, read again and again.
type lexedText struct {
	version uint64
	dialect sqltext.Dialect
	split   sqltext.SplitOptions
	read    bool
	toks    []sqltext.Token
	stmts   []sqltext.Statement
}

func (q *Tab) lexed() *lexedText {
	e := q.Editor
	l := &q.lexedText
	if v := e.State.Buffer.Version(); !l.read || l.version != v || l.dialect != e.Dialect || l.split != e.SplitOptions {
		text := e.Text()
		*l = lexedText{version: v, dialect: e.Dialect, split: e.SplitOptions, read: true,
			stmts: sqltext.SplitWith(text, e.Dialect, e.SplitOptions)}
		for _, t := range sqltext.Tokenize(text, e.Dialect) {
			if t.Kind != sqltext.Whitespace && t.Kind != sqltext.Comment {
				l.toks = append(l.toks, t)
			}
		}
	}
	return l
}

// statementTokens are the significant tokens of the statement around off.
func (q *Tab) statementTokens(off int) []sqltext.Token {
	l := q.lexed()
	start, end := 0, len(q.Editor.Text())
	if i := slices.IndexFunc(l.stmts, func(s sqltext.Statement) bool { return off >= s.Start && off <= s.End }); i >= 0 {
		start, end = l.stmts[i].Start, max(l.stmts[i].End, off)
	}
	lo, _ := slices.BinarySearchFunc(l.toks, start, func(t sqltext.Token, at int) int { return t.Start - at })
	hi := lo
	for hi < len(l.toks) && l.toks[hi].End <= end {
		hi++
	}
	return l.toks[lo:hi]
}

// hoverTarget resolves what the text at off names.
func (q *Tab) hoverTarget(off int) (hoverTarget, bool) {
	e := q.Editor
	text, d := e.Text(), e.Dialect
	toks := q.statementTokens(off)
	i := slices.IndexFunc(toks, func(t sqltext.Token) bool { return t.Start <= off && off < t.End })
	if i < 0 {
		// Right after a name, as the caret is once it is typed.
		i = slices.IndexFunc(toks, func(t sqltext.Token) bool { return t.End == off && t.Start < off && isWord(t) })
	}
	if i < 0 {
		return hoverTarget{}, false
	}
	t := toks[i]
	switch {
	case t.Kind == sqltext.Param || t.Text == ":" || t.Text == "$" || t.Text == "@":
		for _, p := range sqltext.Params(text, d) {
			if p.Start <= off && off <= p.End {
				return hoverTarget{kind: hoverParam, start: p.Start, end: p.End, param: params.Key(p)}, true
			}
		}
		return hoverTarget{}, false
	case t.Text == "*":
		if i > 0 && isSelectListStart(toks[i-1]) {
			return hoverTarget{kind: hoverStar, start: t.Start, end: t.End, refs: tableRefsOf(levelTokens(toks, i), d)}, true
		}
		if i > 1 && toks[i-1].Text == "." && sqltext.IsName(toks[i-2]) {
			return hoverTarget{kind: hoverStar, start: toks[i-2].Start, end: t.End, alias: tokenName(toks[i-2], d), refs: tableRefsOf(levelTokens(toks, i), d)}, true
		}
		return hoverTarget{}, false
	case !isWord(t):
		return hoverTarget{}, false
	}
	name := tokenName(t, d)
	qualifier := ""
	if i >= 2 && toks[i-1].Text == "." && isWord(toks[i-2]) {
		qualifier = tokenName(toks[i-2], d)
	}
	refs := tableRefsOf(toks, d)
	derived := derivedTables(toks, d)
	call := i+1 < len(toks) && toks[i+1].Text == "("
	target := hoverTarget{start: t.Start, end: t.End, refs: refs}
	if call && qualifier == "" && !(i > 0 && isTableWord(toks[i-1])) {
		if it, ok := q.routine(name); ok {
			target.kind, target.item = hoverRoutine, it
			return target, true
		}
		if t.Kind != sqltext.Keyword || slices.Contains(sqltext.Functions(d), strings.ToLower(name)) {
			target.kind, target.fn, target.args = hoverFunction, name, callArgs(toks, i+1)
			return target, true
		}
		return hoverTarget{}, false
	}
	refOf := func(n string) (sqltext.TableRef, bool) {
		for _, r := range refs {
			if strings.EqualFold(r.Alias, n) {
				return r, true
			}
		}
		for _, r := range refs {
			if strings.EqualFold(r.Name, n) {
				return r, true
			}
		}
		return sqltext.TableRef{}, false
	}
	// derivedOf is the CTE or subquery a name or an alias of one names.
	derivedOf := func(n string) (derivedTable, bool) {
		if dt, ok := derived[strings.ToLower(n)]; ok {
			return dt, true
		}
		if r, ok := refOf(n); ok {
			dt, ok := derived[strings.ToLower(r.Name)]
			return dt, ok
		}
		return derivedTable{}, false
	}
	if dt, ok := derivedOf(cmp.Or(qualifier, name)); ok {
		target.kind, target.derived = hoverDerived, dt
		return target, true
	}
	if qualifier != "" {
		if r, ok := refOf(qualifier); ok {
			if obj, ok := q.object(r.Schema, r.Name); ok {
				if col, ok := q.columnOf(obj, name); ok {
					target.kind, target.obj, target.col, target.alias = hoverColumn, obj, col, r.Alias
					return target, true
				}
			}
			return hoverTarget{}, false
		}
		if obj, ok := q.object(qualifier, name); ok {
			target.kind, target.obj = hoverTable, obj
			return target, true
		}
		return hoverTarget{}, false
	}
	if r, ok := refOf(name); ok {
		if obj, ok := q.object(r.Schema, r.Name); ok {
			target.kind, target.obj = hoverTable, obj
			if strings.EqualFold(r.Alias, name) {
				target.alias = r.Alias
			}
			return target, true
		}
	}
	if !(i > 0 && toks[i-1].Text == ".") {
		if obj, ok := q.object("", name); ok && (i > 0 && isTableWord(toks[i-1]) || t.Kind != sqltext.Keyword) {
			if len(refs) == 0 || i > 0 && isTableWord(toks[i-1]) {
				target.kind, target.obj = hoverTable, obj
				return target, true
			}
		}
	}
	// A column of the statement's tables, unqualified.
	var found []db.Object
	var col db.Column
	for _, r := range refs {
		if _, ok := derived[strings.ToLower(r.Name)]; ok {
			continue
		}
		if obj, ok := q.object(r.Schema, r.Name); ok && !slices.ContainsFunc(found, func(o db.Object) bool { return o.Schema == obj.Schema && o.Name == obj.Name }) {
			if c, ok := q.columnOf(obj, name); ok {
				found, col = append(found, obj), c
				target.alias = r.Alias
			}
		}
	}
	switch len(found) {
	case 0:
	case 1:
		target.kind, target.obj, target.col = hoverColumn, found[0], col
		return target, true
	default:
		target.kind, target.col = hoverColumns, col
		for _, o := range found {
			target.tables = append(target.tables, o.Name)
		}
		return target, true
	}
	if t.Kind == sqltext.Keyword {
		return hoverTarget{}, false
	}
	if obj, ok := q.object("", name); ok {
		target.kind, target.obj = hoverTable, obj
		return target, true
	}
	if it, ok := q.routine(name); ok {
		target.kind, target.item = hoverRoutine, it
		return target, true
	}
	return hoverTarget{}, false
}

func isWord(t sqltext.Token) bool {
	return t.Kind == sqltext.Identifier || t.Kind == sqltext.QuotedIdent || t.Kind == sqltext.Keyword
}

// isSelectListStart reports whether a * after t is a select list's.
func isSelectListStart(t sqltext.Token) bool {
	switch strings.ToUpper(t.Text) {
	case "SELECT", ",", "DISTINCT", "ALL":
		return true
	}
	return false
}

// isTableWord reports whether a table's name follows t.
func isTableWord(t sqltext.Token) bool {
	switch strings.ToUpper(t.Text) {
	case "FROM", "JOIN", "UPDATE", "INTO", "TABLE":
		return true
	}
	return false
}

// tableRefsOf are the tables a statement's tokens name, with aliases.
func tableRefsOf(toks []sqltext.Token, d sqltext.Dialect) []sqltext.TableRef {
	refs, _ := tableRefs(toks, d)
	out := make([]sqltext.TableRef, len(refs))
	for i, r := range refs {
		out[i] = sqltext.TableRef{Schema: r.schema, Name: r.name, Alias: r.alias, Quoted: r.quoted}
	}
	// A subquery's alias, which tableRefs passes over, names its derived
	// table.
	for i := 0; i+1 < len(toks); i++ {
		if toks[i].Text != ")" {
			continue
		}
		j := i + 1
		if j < len(toks) && strings.EqualFold(toks[j].Text, "AS") {
			j++
		}
		if j < len(toks) && sqltext.IsName(toks[j]) && !clauseWords[strings.ToUpper(toks[j].Text)] {
			if open := openingParen(toks, i); open > 0 && isTableWord(toks[open-1]) || open > 0 && toks[open-1].Text == "," {
				out = append(out, sqltext.TableRef{Name: tokenName(toks[j], d), Alias: tokenName(toks[j], d)})
			}
		}
	}
	return out
}

// levelTokens are the tokens of the query the token at i is in: those
// inside the parentheses around it, or the statement's, with what their
// own parentheses hold left out, so that a subquery's or a CTE's tables
// are not taken for the query's.
func levelTokens(toks []sqltext.Token, i int) []sqltext.Token {
	from, to := 0, len(toks)
	depth := 0
	for j := i - 1; j >= 0; j-- {
		switch toks[j].Text {
		case ")":
			depth++
		case "(":
			if depth == 0 {
				from = j + 1
				if end := sqltext.MatchingParen(toks, j); end >= 0 {
					to = end
				}
				j = -1
			} else {
				depth--
			}
		}
	}
	var out []sqltext.Token
	for j := from; j < to; j++ {
		out = append(out, toks[j])
		if toks[j].Text == "(" {
			end := sqltext.MatchingParen(toks[:to], j)
			if end < 0 {
				break
			}
			out = append(out, toks[end])
			j = end
		}
	}
	return out
}

// openingParen is the index of the "(" the ")" at close closes, -1 for
// none.
func openingParen(toks []sqltext.Token, close int) int {
	depth := 0
	for i := close; i >= 0; i-- {
		switch toks[i].Text {
		case ")":
			depth++
		case "(":
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// derivedTables are the CTEs of a statement and its subqueries' aliases,
// by lower-case name, with the columns they give.
func derivedTables(toks []sqltext.Token, d sqltext.Dialect) map[string]derivedTable {
	out := map[string]derivedTable{}
	for i := 0; i+2 < len(toks); i++ {
		// name [(columns)] AS ( body )
		if !sqltext.IsName(toks[i]) || i > 0 && toks[i-1].Text == "." {
			continue
		}
		j := i + 1
		var named []string
		if toks[j].Text == "(" && (i == 0 || strings.EqualFold(toks[i-1].Text, "WITH") || toks[i-1].Text == "," || strings.EqualFold(toks[i-1].Text, "RECURSIVE")) {
			end := sqltext.MatchingParen(toks, j)
			if end < 0 {
				continue
			}
			for _, t := range toks[j+1 : end] {
				if sqltext.IsName(t) {
					named = append(named, tokenName(t, d))
				}
			}
			j = end + 1
		}
		if j+1 >= len(toks) || !strings.EqualFold(toks[j].Text, "AS") || toks[j+1].Text != "(" {
			continue
		}
		end := sqltext.MatchingParen(toks, j+1)
		if end < 0 {
			continue
		}
		cols := named
		if cols == nil {
			cols = selectOutputs(toks[j+2:end], d)
		}
		out[strings.ToLower(tokenName(toks[i], d))] = derivedTable{name: tokenName(toks[i], d), cte: true, columns: cols, at: toks[i].Start}
	}
	for i := 0; i+1 < len(toks); i++ {
		if toks[i].Text != "(" || i == 0 || !(isTableWord(toks[i-1]) || toks[i-1].Text == ",") {
			continue
		}
		end := sqltext.MatchingParen(toks, i)
		if end < 0 || i+1 >= len(toks) || !strings.EqualFold(toks[i+1].Text, "SELECT") {
			continue
		}
		j := end + 1
		if j < len(toks) && strings.EqualFold(toks[j].Text, "AS") {
			j++
		}
		if j < len(toks) && sqltext.IsName(toks[j]) && !clauseWords[strings.ToUpper(toks[j].Text)] {
			name := tokenName(toks[j], d)
			out[strings.ToLower(name)] = derivedTable{name: name, columns: selectOutputs(toks[i+1:end], d), at: toks[j].Start}
		}
	}
	return out
}

// selectOutputs are the names of the columns a SELECT gives: an alias, or
// the column a plain name or t.name reads, "?" for an expression.
func selectOutputs(toks []sqltext.Token, d sqltext.Dialect) []string {
	if len(toks) == 0 || !strings.EqualFold(toks[0].Text, "SELECT") {
		return nil
	}
	var out []string
	var item []sqltext.Token
	flush := func() {
		if len(item) == 0 {
			return
		}
		last := item[len(item)-1]
		switch {
		case len(item) >= 2 && strings.EqualFold(item[len(item)-2].Text, "AS") && sqltext.IsName(last):
			out = append(out, tokenName(last, d))
		case sqltext.IsName(last) && (len(item) == 1 || item[len(item)-2].Text == "." || sqltext.IsName(item[len(item)-2]) || item[len(item)-2].Text == ")"):
			out = append(out, tokenName(last, d))
		default:
			out = append(out, "?")
		}
		item = item[:0]
	}
	depth := 0
	for _, t := range toks[1:] {
		switch {
		case t.Text == "(":
			depth++
		case t.Text == ")":
			depth--
		case depth == 0 && t.Text == ",":
			flush()
			continue
		case depth == 0 && t.Kind == sqltext.Keyword && clauseWords[strings.ToUpper(t.Text)] || depth == 0 && strings.EqualFold(t.Text, "FROM"):
			flush()
			return out
		case depth == 0 && len(item) == 0 && strings.EqualFold(t.Text, "DISTINCT"):
			continue
		}
		item = append(item, t)
	}
	flush()
	return out
}

// callArgs counts the arguments of a call whose "(" is at open.
func callArgs(toks []sqltext.Token, open int) int {
	end := sqltext.MatchingParen(toks, open)
	if end < 0 {
		end = len(toks)
	}
	if end == open+1 {
		return 0
	}
	n, depth := 1, 0
	for _, t := range toks[open+1 : end] {
		switch t.Text {
		case "(":
			depth++
		case ")":
			depth--
		case ",":
			if depth == 0 {
				n++
			}
		}
	}
	return n
}

// object is a table or a view of the catalog, by schema ("" for the
// editor's) and name; reading the schema when it is not read.
func (q *Tab) object(schema, name string) (db.Object, bool) {
	if schema == "" {
		schema = q.currentSchema()
	}
	cn := q.Conn
	if cn.Status != connection.StatusConnected {
		return db.Object{}, false
	}
	objs, _ := connection.ObjectsOf(q.a, cn, q.Database, schema)
	i := slices.IndexFunc(objs, func(o db.Object) bool { return strings.EqualFold(o.Name, name) })
	if i < 0 {
		return db.Object{}, false
	}
	o := objs[i]
	if o.Schema == "" {
		o.Schema = schema
	}
	return o, true
}

// columnOf is a column of a table, its columns read when they are not.
func (q *Tab) columnOf(obj db.Object, name string) (db.Column, bool) {
	cols, _ := connection.ColumnsOf(q.a, q.Conn, q.Database, obj.Schema, obj.Name)
	i := slices.IndexFunc(cols, func(c db.Column) bool { return strings.EqualFold(c.Name, name) })
	if i < 0 {
		return db.Column{}, false
	}
	return cols[i], true
}

// routine is a function or a procedure of the editor's schema.
func (q *Tab) routine(name string) (db.Item, bool) {
	items := q.Conn.Items[connection.SchemaKey{Database: q.Database, Schema: q.currentSchema()}]
	i := slices.IndexFunc(items, func(it db.Item) bool {
		return (it.Kind == db.ItemFunction || it.Kind == db.ItemProcedure) && strings.EqualFold(it.Name, name)
	})
	if i < 0 {
		return db.Item{}, false
	}
	return items[i], true
}

// hoverCard fills the card of a target.
func (q *Tab) hoverCard(c *ui.Context, card *widgets.Card, h hoverTarget) {
	if q.closed {
		card.Header(c, widgets.IconCode, widgets.ToneTable, "Closed", "")
		card.Note(c, "The editor this card is of was closed.")
		return
	}
	cn := q.Conn
	switch h.kind {
	case hoverTable:
		o := dataview.TableCardOptions{
			Insert:        func(col string) { q.insertColumn(h, col) },
			Used:          h.used,
			Join:          func(r dataview.Relation) { q.insertJoin(h, r, h.start) },
			Usages:        func() { q.GoTo(h.start); q.findUsages() },
			Alias:         h.alias,
			StructureKeys: "F12",
		}
		if card.Pinned() {
			// The text has moved on since: the join goes in the statement
			// at the caret, and the usages, of a place, are left out.
			o.Join = func(r dataview.Relation) { q.insertJoin(h, r, q.Editor.Caret()) }
			o.Usages = nil
		}
		dataview.TableCard(c, card, q.a, cn, q.Database, h.obj, o)
	case hoverColumn:
		dataview.ColumnCard(c, card, q.a, cn, q.Database, h.obj.Schema, h.obj.Name, h.col, dataview.ColumnCardOptions{})
	case hoverColumns:
		card.Header(c, widgets.IconColumns, widgets.ToneColumn, h.col.Name, "in "+strings.Join(h.tables, ", "))
		card.Note(c, "More than one of the statement's tables has it: qualify it to say which.")
	case hoverRoutine:
		kind := "Function"
		if h.item.Kind == db.ItemProcedure {
			kind = "Procedure"
		}
		card.Header(c, widgets.IconCode, widgets.ToneFunction, h.item.Name, h.item.Schema)
		card.Mono(c, h.item.Name+"("+h.item.Detail+")")
		card.Meta(c, kind)
		card.Actions(c, func(a *widgets.CardActions) {
			if a.Button(c, "Open Definition", "") {
				dataview.OpenItemDefinition(q.a, cn, q.Database, h.item)
			}
			a.Copy(c, "Copy Name", h.item.Name)
		})
	case hoverFunction:
		q.functionCard(c, card, h)
	case hoverParam:
		q.paramCard(c, card, h)
	case hoverStar:
		q.starCard(c, card, h)
	case hoverDerived:
		q.derivedCard(c, card, h)
	}
}

// functionCard shows a built-in function's signatures, the ones taking as
// many arguments as the call gives first.
func (q *Tab) functionCard(c *ui.Context, card *widgets.Card, h hoverTarget) {
	card.Header(c, widgets.IconCode, widgets.ToneFunction, h.fn, "")
	sigs, read := q.Conn.Signatures[strings.ToLower(h.fn)]
	if q.Conn.Signatures == nil {
		if q.Conn.Status == connection.StatusConnected {
			connection.LoadSignatures(q.a, q.Conn, nil)
			card.Placeholder(c, 2)
			return
		}
		read = false
	}
	if !read || len(sigs) == 0 {
		card.Note(c, "No signature known for this function.")
		return
	}
	// Sorted once while the card shows: those taking as many arguments as
	// the call gives first.
	sigs = *widgets.Keep(card, "signatures", func() []db.Signature {
		sorted := slices.Clone(sigs)
		slices.SortStableFunc(sorted, func(a, b db.Signature) int {
			return arityDistance(a.Args, h.args) - arityDistance(b.Args, h.args)
		})
		return sorted
	})
	sig := sigs[card.Pages(c, len(sigs))]
	line := sig.Name + "(" + sig.Args + ")"
	if sig.Result != "" {
		line += " → " + sig.Result
	}
	card.Mono(c, line)
	card.Comment(c, sig.Description)
	kind := "Built-in"
	if sig.Aggregate {
		kind = "Built-in aggregate"
	}
	card.Meta(c, kind)
}

// arityDistance is how far a signature's arguments are from n.
func arityDistance(args string, n int) int {
	if strings.Contains(args, "...") {
		return 0
	}
	count := 0
	if strings.TrimSpace(args) != "" {
		count = 1 + strings.Count(args, ",")
	}
	if count > n {
		return count - n
	}
	return n - count
}

// paramCard shows the value a parameter is sent with.
func (q *Tab) paramCard(c *ui.Context, card *widgets.Card, h hoverTarget) {
	card.Header(c, widgets.IconCode, widgets.ToneParam, h.param, "parameter")
	q.readKeptParams()
	in := q.paramsBarState.keptOf(h.param)
	if in == blankParam || in.Value == "" {
		card.Note(c, "Asked when run: no value is kept for it on this connection.")
	} else {
		card.Rows(c, [][2]string{{"value", in.Value}, {"sent as", in.Kind}})
	}
	card.Actions(c, func(a *widgets.CardActions) { a.Copy(c, "Copy Name", h.param) })
}

// starCard shows the columns a * reads, and expands it to them.
func (q *Tab) starCard(c *ui.Context, card *widgets.Card, h hoverTarget) {
	refs := h.refs
	if h.alias != "" {
		refs = slices.DeleteFunc(slices.Clone(refs), func(r sqltext.TableRef) bool {
			return !strings.EqualFold(r.Alias, h.alias) && !strings.EqualFold(r.Name, h.alias)
		})
	}
	derived := derivedTables(q.statementTokens(h.start), q.Editor.Dialect)
	var cols []widgets.CardColumn
	var names []string
	reading := false
	qualify := len(refs) > 1 || h.alias != ""
	for _, r := range refs {
		prefix := ""
		if qualify {
			prefix = cmp.Or(r.Alias, r.Name) + "."
		}
		if dt, ok := derived[strings.ToLower(r.Name)]; ok {
			for _, c := range dt.columns {
				cols = append(cols, widgets.CardColumn{Name: prefix + c})
				names = append(names, prefix+c)
			}
			continue
		}
		obj, ok := q.object(r.Schema, r.Name)
		if !ok {
			continue
		}
		list, ok := connection.ColumnsOf(q.a, q.Conn, q.Database, obj.Schema, obj.Name)
		if !ok {
			reading = reading || q.Conn.Loading[connection.ObjectKey{Database: q.Database, Schema: obj.Schema, Name: obj.Name}]
			continue
		}
		for _, c := range list {
			cols = append(cols, widgets.CardColumn{Name: prefix + c.Name, Type: c.Type, Key: c.PrimaryKey})
			names = append(names, prefix+q.quoteName(c.Name))
		}
	}
	label := "*"
	if h.alias != "" {
		label = h.alias + ".*"
	}
	card.Header(c, widgets.IconColumns, widgets.ToneColumn, label, widgets.Count(len(cols), "column"))
	switch {
	case reading:
		card.Placeholder(c, 3)
		return
	case len(cols) == 0:
		card.Note(c, "The statement's tables are not read yet.")
		return
	}
	card.Columns(c, cols, widgets.ColumnsOptions{})
	switch {
	case slices.ContainsFunc(names, func(n string) bool { return strings.HasSuffix(n, "?") }):
		card.Note(c, "An expression without a name gives a column: name it with AS to expand the *.")
	default:
		card.Actions(c, func(a *widgets.CardActions) {
			if a.AnchoredButton(c, "Expand") {
				q.replaceText(h.start, h.end, strings.Join(names, ", "))
			}
		})
	}
}

// derivedCard shows a CTE's or a subquery's columns.
func (q *Tab) derivedCard(c *ui.Context, card *widgets.Card, h hoverTarget) {
	dt := h.derived
	where := "subquery"
	if dt.cte {
		where = "common table expression"
	}
	card.Header(c, widgets.IconTable, widgets.ToneTable, dt.name, where)
	card.Meta(c, "Defined in this statement")
	cols := make([]widgets.CardColumn, len(dt.columns))
	for i, c := range dt.columns {
		cols[i] = widgets.CardColumn{Name: c}
	}
	if len(cols) > 0 {
		card.Columns(c, cols, widgets.ColumnsOptions{Insert: func(col string) {
			q.Editor.State.Insert(dt.name + "." + col)
		}})
	}
	card.Actions(c, func(a *widgets.CardActions) {
		if a.AnchoredButton(c, "Go to Definition") {
			q.GoTo(dt.at)
		}
		a.Copy(c, "Copy Name", dt.name)
	})
}

// usedNames are the names, in lower case, the statement around off uses.
func (q *Tab) usedNames(off int) map[string]bool {
	used := map[string]bool{}
	for _, t := range q.statementTokens(off) {
		if sqltext.IsName(t) {
			used[strings.ToLower(tokenName(t, q.Editor.Dialect))] = true
		}
	}
	return used
}

// quoteName writes a name as the editor's dialect reads it, quoted only
// where it must be.
func (q *Tab) quoteName(name string) string {
	return db.QuoteIfNeeded(db.DialectOf(q.Conn.Config.Engine), name)
}

// insertColumn inserts a column of the hovered table at the caret, by
// the table's alias, or its name where the statement reads more tables.
func (q *Tab) insertColumn(h hoverTarget, col string) {
	text := q.quoteName(col)
	switch {
	case h.alias != "":
		text = h.alias + "." + text
	default:
		for _, r := range h.refs {
			if strings.EqualFold(r.Name, h.obj.Name) && r.Alias != "" {
				text = r.Alias + "." + text
				break
			}
		}
		if !strings.Contains(text, ".") && len(h.refs) > 1 {
			text = q.quoteName(h.obj.Name) + "." + text
		}
	}
	q.Editor.State.Insert(text)
	q.Editor.WantFocus = true
}

// insertJoin writes the join of the hovered table to a related one, after
// the statement's FROM clause and its joins.
func (q *Tab) insertJoin(h hoverTarget, r dataview.Relation, at int) {
	from := q.quoteName(h.obj.Name)
	for _, ref := range h.refs {
		if strings.EqualFold(ref.Name, h.obj.Name) && ref.Alias != "" {
			from = ref.Alias
		}
	}
	if h.alias != "" {
		from = h.alias
	}
	table := q.quoteName(r.Table)
	if r.Schema != "" && !strings.EqualFold(r.Schema, q.currentSchema()) {
		table = q.quoteName(r.Schema) + "." + table
	}
	to := q.quoteName(r.Table)
	// A table the statement reads already, as one joined to itself, goes
	// by an alias of its own.
	if strings.EqualFold(r.Table, h.obj.Name) || slices.ContainsFunc(h.refs, func(ref sqltext.TableRef) bool { return strings.EqualFold(ref.Name, r.Table) }) {
		to = q.quoteName(r.Table + "_2")
		table += " " + to
	}
	var on []string
	for i := range min(len(r.Columns), len(r.RefColumns)) {
		if r.Outgoing {
			// Its key points at the hovered table's.
			on = append(on, to+"."+q.quoteName(r.Columns[i])+" = "+from+"."+q.quoteName(r.RefColumns[i]))
		} else {
			on = append(on, to+"."+q.quoteName(r.RefColumns[i])+" = "+from+"."+q.quoteName(r.Columns[i]))
		}
	}
	join := "JOIN " + table + " ON " + strings.Join(on, " AND ")
	toks := q.statementTokens(at)
	at = len(q.Editor.Text())
	if len(toks) > 0 {
		at = toks[len(toks)-1].End
		if toks[len(toks)-1].Text == ";" && len(toks) > 1 {
			at = toks[len(toks)-2].End
		}
	}
	seenFrom, depth := false, 0
	for i, t := range toks {
		switch t.Text {
		case "(":
			depth++
		case ")":
			depth--
		}
		word := strings.ToUpper(t.Text)
		if depth == 0 && word == "FROM" {
			seenFrom = true
		}
		if seenFrom && depth == 0 && i > 0 && joinEnds[word] {
			at = toks[i-1].End
			break
		}
	}
	q.replaceText(at, at, "\n"+join)
}

// joinEnds are the words ending a FROM clause and its joins.
var joinEnds = map[string]bool{"WHERE": true, "GROUP": true, "ORDER": true, "LIMIT": true, "HAVING": true, "UNION": true,
	"EXCEPT": true, "INTERSECT": true, "WINDOW": true, "OFFSET": true, "FETCH": true, "RETURNING": true, "FOR": true,
	"QUALIFY": true, "PREWHERE": true, "SETTINGS": true, "FORMAT": true}

// replaceText replaces the text from start to end, as an edit undone at
// once.
func (q *Tab) replaceText(start, end int, text string) {
	q.Editor.Select(start, end)
	q.Editor.State.Insert(text)
	q.Editor.WantFocus = true
}

// linkAt is the link holding off while Cmd is held: a name the catalog
// or the statement knows, which a click opens.
func (q *Tab) linkAt(off int) (start, end int, ok bool) {
	// Asked in each frame Cmd is held: worked out again only as the
	// pointer or the text moves.
	l := &q.lastLink
	if v := q.Editor.State.Buffer.Version(); !l.asked || l.off != off || l.version != v {
		*l = linkAsked{asked: true, off: off, version: v}
		if h, found := q.hoverTarget(off); found {
			switch h.kind {
			case hoverTable, hoverColumn, hoverRoutine, hoverDerived:
				l.start, l.end, l.ok = h.start, h.end, true
			}
		}
	}
	return l.start, l.end, l.ok
}

// linkAsked is the link linkAt last found, at an offset of a version of
// the text.
type linkAsked struct {
	asked, ok       bool
	off, start, end int
	version         uint64
}

// openLink opens what the link at off names: a CTE's or a subquery's
// definition in the text, else what Go to Definition opens.
func (q *Tab) openLink(off int) {
	if h, ok := q.hoverTarget(off); ok && h.kind == hoverDerived {
		q.GoTo(h.derived.at)
		return
	}
	q.GoTo(off)
	q.goToDefinition()
}
