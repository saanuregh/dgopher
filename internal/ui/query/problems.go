package query

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/keymap"
	"dgopher/internal/sqltext"
	"dgopher/internal/ui/editor"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// Limits of checking as the text is typed.
const (
	problemLimit  = 50      // problems marked at once
	checkMaxRunes = 200_000 // a longer text is not checked as it is typed
)

// catalog is what the checker knows of the database: the tables of a
// schema, and the columns of a table, each when read; ok is false for
// what is not read yet.
type catalog interface {
	objects(schema string) ([]db.Object, bool)
	columns(schema, table string) ([]db.Column, bool)
	schemas() []string
	defaultSchema() string
	// quote writes a name as the engine reads it quoted.
	quote(name string) string
}

// checkText finds the mistakes of a text that need no server to tell:
// strings, quoted names and comments left open, parentheses that do not
// match, and, with a catalog, tables and columns it has not.
func checkText(text string, d sqltext.Dialect, o sqltext.SplitOptions, cat catalog) []editor.Problem {
	if utf8.RuneCountInString(text) > checkMaxRunes {
		return nil
	}
	toks := sqltext.Tokenize(text, d)
	var out []editor.Problem
	add := func(p editor.Problem) {
		if len(out) < problemLimit {
			out = append(out, p)
		}
	}
	runes := []rune(text)
	for _, t := range toks {
		if why := unclosed(t, runes, d); why != "" {
			add(editor.Problem{Start: t.Start, End: t.Start + 1, Message: why})
		}
	}
	for _, st := range sqltext.SplitWith(text, d, o) {
		var stmt []sqltext.Token
		for _, t := range toks {
			if t.Start >= st.Start && t.End <= st.End && t.Kind != sqltext.Whitespace && t.Kind != sqltext.Comment {
				stmt = append(stmt, t)
			}
		}
		for _, p := range parentheses(stmt) {
			add(p)
		}
		if cat != nil && checksNames(stmt, st.Text, d) {
			for _, p := range unknownNames(stmt, d, cat) {
				add(p)
			}
		}
	}
	slices.SortFunc(out, func(a, b editor.Problem) int { return a.Start - b.Start })
	return out
}

// unclosed says why a string, quoted name or comment is left open, "" when
// it is not: the lexer takes such a token to the end of the text.
func unclosed(t sqltext.Token, runes []rune, d sqltext.Dialect) string {
	if t.End != len(runes) {
		return ""
	}
	text := t.Text
	switch t.Kind {
	case sqltext.Comment:
		if strings.HasPrefix(text, "/*") && !strings.HasSuffix(text, "*/") {
			return "This comment is not closed: */ ends it."
		}
		return ""
	case sqltext.String, sqltext.QuotedIdent:
	default:
		return ""
	}
	what := "string"
	if t.Kind == sqltext.QuotedIdent {
		what = "name"
	}
	if strings.HasPrefix(text, "$") {
		// A dollar-quoted string, $$…$$ or $tag$…$tag$.
		if end := strings.IndexByte(text[1:], '$'); end >= 0 {
			if tag := text[:end+2]; len(text) < 2*len(tag) || !strings.HasSuffix(text, tag) {
				return "This string is not closed: " + tag + " ends it."
			}
		}
		return ""
	}
	body := strings.TrimLeft(text, "EeUuNnBbXx&")
	if body == "" {
		return ""
	}
	open, inside := body[0], body[1:]
	if open == '[' {
		if !strings.HasSuffix(inside, "]") {
			return "This name is not closed: ] ends it."
		}
		return ""
	}
	// Inside, a quote is doubled; the closing one makes their count odd.
	backslashed := (d == sqltext.MySQL || d == sqltext.ClickHouse) && strings.HasSuffix(inside, `\`+string(open))
	if strings.Count(inside, string(open))%2 == 0 || backslashed {
		return fmt.Sprintf("This %s is not closed: %c ends it.", what, open)
	}
	return ""
}

// parentheses marks those of a statement that close nothing, or are not
// closed.
func parentheses(stmt []sqltext.Token) []editor.Problem {
	var open []sqltext.Token
	var out []editor.Problem
	for _, t := range stmt {
		switch t.Text {
		case "(":
			open = append(open, t)
		case ")":
			if len(open) == 0 {
				out = append(out, editor.Problem{Start: t.Start, End: t.End, Message: "This parenthesis closes none."})
				continue
			}
			open = open[:len(open)-1]
		}
	}
	for _, t := range open {
		out = append(out, editor.Problem{Start: t.Start, End: t.End, Message: "This parenthesis is not closed."})
	}
	return out
}

// checksNames reports whether a statement's table names must exist: not
// a statement making, changing or dropping them. Its verb is the first
// word of its code past opening parentheses, as sqltext.Classify reads
// it; only a WITH, whose verb is its main statement's, is classified in
// full, which is slow on a long routine checked as it is typed.
func checksNames(code []sqltext.Token, text string, d sqltext.Dialect) bool {
	for len(code) > 0 && code[0].Kind == sqltext.Punct && code[0].Text == "(" {
		code = code[1:]
	}
	if len(code) == 0 || code[0].Kind != sqltext.Keyword && code[0].Kind != sqltext.Identifier {
		return false
	}
	verb := strings.ToUpper(code[0].Text)
	if verb == "WITH" {
		verb = sqltext.Classify(text, d).Verb
	}
	switch verb {
	case "SELECT", "WITH", "INSERT", "UPDATE", "DELETE", "MERGE", "TABLE", "VALUES":
		return true
	}
	return false
}

// tableRef is a table a statement reads or writes, where it names it.
type tableRef struct {
	schema, name, alias string
	quoted              bool
	start, end          int // the name's runes
}

// clauseWords end a table's place in FROM: what follows them is no alias.
var clauseWords = map[string]bool{"WHERE": true, "JOIN": true, "INNER": true, "LEFT": true, "RIGHT": true, "FULL": true,
	"CROSS": true, "NATURAL": true, "ON": true, "USING": true, "GROUP": true, "ORDER": true, "LIMIT": true, "HAVING": true,
	"UNION": true, "EXCEPT": true, "INTERSECT": true, "SET": true, "VALUES": true, "RETURNING": true, "WINDOW": true,
	"OFFSET": true, "FETCH": true, "FOR": true, "LATERAL": true, "SELECT": true, "DEFAULT": true, "OUTER": true}

// tableRefs finds the tables a statement names after FROM, JOIN, UPDATE
// and INTO, with their aliases; and the names its WITH defines.
func tableRefs(stmt []sqltext.Token, d sqltext.Dialect) (refs []tableRef, ctes map[string]bool) {
	ctes = map[string]bool{}
	word := func(i int) string {
		if i < len(stmt) && stmt[i].Kind == sqltext.Keyword {
			return strings.ToUpper(stmt[i].Text)
		}
		return ""
	}
	for i := 0; i+2 < len(stmt); i++ {
		// name AS ( defines a common table expression.
		if sqltext.IsName(stmt[i]) && word(i+1) == "AS" && stmt[i+2].Text == "(" {
			ctes[strings.ToLower(tokenName(stmt[i], d))] = true
		}
	}
	for i := 0; i < len(stmt); i++ {
		switch word(i) {
		case "FROM", "JOIN", "UPDATE":
		case "INTO":
			if first := word(0); first == "SELECT" || first == "WITH" {
				continue // SELECT … INTO makes its table, or fills variables
			}
		default:
			continue
		}
		for j := i + 1; j < len(stmt); {
			// A subquery or a table function names no table: its
			// parentheses are passed over, and its alias.
			var ref tableRef
			named := sqltext.IsName(stmt[j])
			if named {
				ref = tableRef{name: tokenName(stmt[j], d), quoted: stmt[j].Kind == sqltext.QuotedIdent, start: stmt[j].Start, end: stmt[j].End}
				j++
				for j+1 < len(stmt) && stmt[j].Text == "." && sqltext.IsName(stmt[j+1]) {
					ref.schema, ref.name = ref.name, tokenName(stmt[j+1], d)
					ref.quoted, ref.start, ref.end = stmt[j+1].Kind == sqltext.QuotedIdent, stmt[j+1].Start, stmt[j+1].End
					j += 2
				}
			}
			if j < len(stmt) && stmt[j].Text == "(" {
				if named && word(i) == "INTO" {
					refs = append(refs, ref) // INSERT INTO t (its columns)
					break
				}
				named = false
				j = pastParentheses(stmt, j)
			} else if !named {
				break // a keyword: no table here
			}
			if word(j) == "AS" {
				j++
			}
			if j < len(stmt) && sqltext.IsName(stmt[j]) && !clauseWords[strings.ToUpper(stmt[j].Text)] {
				ref.alias = tokenName(stmt[j], d)
				j++
			}
			if named {
				refs = append(refs, ref)
			}
			if j >= len(stmt) || stmt[j].Text != "," || word(i) != "FROM" {
				break
			}
			j++ // FROM a, b
		}
	}
	return refs, ctes
}

// pastParentheses is the index of the token after the ")" closing the
// "(" at open, or the end.
func pastParentheses(stmt []sqltext.Token, open int) int {
	if i := sqltext.MatchingParen(stmt, open); i >= 0 {
		return i + 1
	}
	return len(stmt)
}

// unknownNames marks the tables, and the columns written table.column,
// that the catalog has not, with the closest of those it has as a fix.
func unknownNames(stmt []sqltext.Token, d sqltext.Dialect, cat catalog) []editor.Problem {
	refs, ctes := tableRefs(stmt, d)
	var out []editor.Problem
	known := map[string]tableRef{} // by alias and name, lower case
	for _, ref := range refs {
		if ref.schema == "" && ctes[strings.ToLower(ref.name)] {
			continue
		}
		schema := ref.schema
		if schema == "" {
			schema = cat.defaultSchema()
		} else if !slices.ContainsFunc(cat.schemas(), func(s string) bool { return strings.EqualFold(s, schema) }) {
			continue // a schema of another database, or not read: unknown here
		}
		objs, ok := cat.objects(schema)
		if !ok {
			continue
		}
		i := slices.IndexFunc(objs, func(o db.Object) bool { return sameIdent(o.Name, ref.name, ref.quoted) })
		if i < 0 {
			names := make([]string, len(objs))
			for k, o := range objs {
				names[k] = o.Name
			}
			out = append(out, unknown(ref.start, ref.end, "table or view", ref.name, schema, names, cat.quote))
			continue
		}
		ref.schema, ref.name = schema, objs[i].Name
		known[strings.ToLower(ref.name)] = ref
		if ref.alias != "" {
			known[strings.ToLower(ref.alias)] = ref
		}
	}
	for i := 0; i+2 < len(stmt); i++ {
		// table.column, not table.column( nor table.*.
		if !sqltext.IsName(stmt[i]) || stmt[i+1].Text != "." || !sqltext.IsName(stmt[i+2]) || i+3 < len(stmt) && (stmt[i+3].Text == "(" || stmt[i+3].Text == ".") {
			continue
		}
		ref, ok := known[strings.ToLower(tokenName(stmt[i], d))]
		if !ok || i > 0 && stmt[i-1].Text == "." {
			continue
		}
		cols, ok := cat.columns(ref.schema, ref.name)
		if !ok {
			continue
		}
		col := tokenName(stmt[i+2], d)
		if slices.ContainsFunc(cols, func(c db.Column) bool { return sameIdent(c.Name, col, stmt[i+2].Kind == sqltext.QuotedIdent) }) {
			continue
		}
		names := make([]string, len(cols))
		for k, c := range cols {
			names[k] = c.Name
		}
		out = append(out, unknown(stmt[i+2].Start, stmt[i+2].End, "column", col, ref.name, names, cat.quote))
	}
	return out
}

// sameIdent reports whether a name typed names a catalog's: exactly when
// quoted, else ignoring case.
func sameIdent(catalogName, typed string, quoted bool) bool {
	if quoted {
		return catalogName == typed
	}
	return strings.EqualFold(catalogName, typed)
}

// unknown is the problem of a name the catalog has not, with the closest
// it has as a fix when one is close enough to be a typo.
func unknown(start, end int, what, name, where string, names []string, quote func(string) string) editor.Problem {
	p := editor.Problem{Start: start, End: end, Message: fmt.Sprintf("No %s %s in %s.", what, name, where)}
	best, bestDist := "", max(1, utf8.RuneCountInString(name)/3)+1
	for _, n := range names {
		if dist := distance(strings.ToLower(name), strings.ToLower(n)); dist < bestDist {
			best, bestDist = n, dist
		}
	}
	if best != "" {
		p.Message += " Did you mean " + best + "?"
		p.Fix = best
		if needsQuote(best) {
			p.Fix = quote(best)
		}
	}
	return p
}

// distance is the Levenshtein distance of two strings, in runes.
func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// tabCatalog is the editor's connection's catalog, as read so far; what
// it has not read it starts reading, once.
type tabCatalog struct{ q *Tab }

func (c tabCatalog) objects(schema string) ([]db.Object, bool) {
	return connection.ObjectsOf(c.q.a, c.q.Conn, c.q.Database, schema)
}

func (c tabCatalog) columns(schema, table string) ([]db.Column, bool) {
	cn, key := c.q.Conn, connection.ObjectKey{Database: c.q.Database, Schema: schema, Name: table}
	cols, ok := cn.Columns[key]
	if !ok && cn.LoadErr[key] == "" {
		connection.WantColumns(c.q.a, cn, c.q.Database, schema, table)
	}
	return cols, ok
}

func (c tabCatalog) schemas() []string     { return c.q.Conn.Schemas[c.q.Database] }
func (c tabCatalog) defaultSchema() string { return c.q.currentSchema() }
func (c tabCatalog) quote(name string) string {
	return db.DialectOf(c.q.Conn.Config.Engine).Quote(name)
}

// checkProblems finds the editor's problems again when its text, or the
// catalog read, changed.
func (q *Tab) checkProblems() {
	cn := q.Conn
	read := fmt.Sprint(cn.Status, "/", len(cn.Objects), "/", len(cn.Columns), "/", len(cn.Schemas[q.Database]), "/", q.currentSchema())
	if q.Editor.Text == q.checkedText && read == q.checkedCatalog {
		return
	}
	q.checkedText, q.checkedCatalog = q.Editor.Text, read
	var cat catalog
	if cn.Status == connection.StatusConnected && cn.DB != nil {
		cat = tabCatalog{q}
	}
	q.Editor.Problems = checkText(q.Editor.Text, q.Editor.Dialect, SplitOptions(q.a.Settings()), cat)
}

// fixProblem mends the problem at the caret with its fix.
func (q *Tab) fixProblem() {
	e := &q.Editor
	if p, ok := e.ProblemAt(e.SelEnd); ok && p.Fix != "" {
		e.Replace(p.Start, p.End, p.Fix)
		q.ac.lastText = e.Text
	}
}

// problemBar says what is wrong where the caret is, and offers its fix.
func (q *Tab) problemBar(c *ui.Context) {
	e := &q.Editor
	p, ok := e.ProblemAt(e.SelEnd)
	if !ok || !e.HasFocus {
		return
	}
	th := c.Theme()
	ui.Row(c).Padding(4, 12).Gap(8).AlignItems(ui.Center).Background(th.Danger.Alpha(0.10)).Children(func() {
		ui.Icon(c, widgets.IconAlert).TextColor(th.Danger).FontSize(13)
		ui.Text(c, p.Message).FontSize(12.5).Grow(1).Shrink(1).SingleLine()
		if p.Fix != "" && ui.Button(c, keymap.Hint("Fix", keymap.QuickFix)).Clicked() {
			q.fixProblem()
		}
	})
}
