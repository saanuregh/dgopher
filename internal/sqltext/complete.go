package sqltext

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TableRef is a table referenced by a statement; names are unquoted.
type TableRef struct {
	Schema, Name, Alias string
	// SchemaQuoted and Quoted are set for a name written quoted, whose
	// case the database keeps.
	SchemaQuoted, Quoted bool
}

// CompletionContext describes what is being typed at the caret.
type CompletionContext struct {
	Prefix      string
	PrefixStart int
	Qualifier   string
	WantTable   bool
	// InQueryFrom says the table goes in a query's FROM or JOIN, where an
	// alias may follow it.
	InQueryFrom bool
	Tables      []TableRef
}

var tableKeywords = map[string]bool{"FROM": true, "JOIN": true, "UPDATE": true, "INTO": true, "TABLE": true, "DESCRIBE": true, "DESC": true}

// CompletionAt returns the autocomplete context for the caret position.
func CompletionAt(src string, caret int, d Dialect) CompletionContext {
	caret = min(max(caret, 0), len(src))
	var ctx CompletionContext
	start := caret
	for start > 0 && identPartAt(src, start-1, d) {
		start--
	}
	// A name never starts with '$': there it is a parameter's sigil.
	for start < caret && src[start] == '$' {
		start++
	}
	ctx.Prefix = src[start:caret]
	ctx.PrefixStart = start

	all := Tokenize(src, d)
	for _, t := range all {
		// A line comment runs to the end of its line, so its end is still inside it.
		lineComment := t.Kind == Comment && !strings.HasPrefix(t.Text, "/*")
		if t.Start < caret && (caret < t.End || caret == t.End && lineComment) && (t.Kind == String || t.Kind == Comment) {
			return ctx
		}
	}

	stmtStart, stmtEnd := 0, len(src)
	if s, ok := StatementAt(src, caret, d); ok {
		stmtStart, stmtEnd = s.Start, s.End
		if caret > stmtEnd {
			stmtEnd = caret
		}
		if caret < stmtStart {
			stmtStart = caret
		}
	}
	var toks []Token
	for _, t := range all {
		if t.Start >= stmtStart && t.End <= stmtEnd && significant(t) {
			toks = append(toks, t)
		}
	}

	// Tokens before the qualifier (or prefix) decide WantTable.
	cut := start
	if start > 0 && src[start-1] == '.' {
		for _, t := range toks {
			if t.End == start-1 && (t.Kind == Identifier || t.Kind == QuotedIdent || t.Kind == Keyword) {
				ctx.Qualifier = unquote(t)
				cut = t.Start
			}
		}
	}
	var before []Token
	for _, t := range toks {
		if t.End <= cut {
			before = append(before, t)
		}
	}
	k := tableKeyword(before)
	ctx.WantTable, ctx.InQueryFrom = k >= 0, queryFrom(before, k)

	for _, ref := range tableRefs(toks, cut) {
		dup := false
		for _, r := range ctx.Tables {
			if r == ref {
				dup = true
			}
		}
		if !dup {
			ctx.Tables = append(ctx.Tables, ref)
		}
	}
	return ctx
}

func unquote(t Token) string {
	if t.Kind != QuotedIdent || len(t.Text) < 2 {
		return t.Text
	}
	q := t.Text[:1]
	if q == "[" {
		// T-SQL's ]] is one ]; SQLite's [name], which has no escape for
		// ], holds none.
		return strings.ReplaceAll(strings.TrimSuffix(t.Text[1:], "]"), "]]", "]")
	}
	inner := strings.TrimSuffix(t.Text[1:], q)
	return strings.ReplaceAll(inner, q+q, q)
}

// tableKeyword returns the index in before of the keyword whose table is
// typed next, as FROM, JOIN or INTO, the FROM of a list of them, or -1
// where no table goes.
func tableKeyword(before []Token) int {
	if len(before) == 0 {
		return -1
	}
	n := len(before) - 1
	last := before[n]
	if n > 0 && last.Kind == Keyword && (word(last) == "LATERAL" || word(last) == "ONLY") {
		if prev := before[n-1]; prev.Kind == Keyword && (word(prev) == "FROM" || word(prev) == "JOIN") {
			return n - 1
		}
	}
	if tableKeywords[word(last)] && last.Kind == Keyword {
		return n
	}
	if !isPunct(last, ",") {
		return -1
	}
	depth := 0
	for i := n - 1; i >= 0; i-- {
		t := before[i]
		switch {
		case isPunct(t, ")"):
			depth++
		case isPunct(t, "("):
			if depth == 0 {
				return -1
			}
			depth--
		case depth == 0 && t.Kind == Keyword:
			switch word(t) {
			case "FROM":
				return i
			case "AS", "ONLY", "LATERAL":
			default:
				return -1
			}
		}
	}
	return -1
}

// queryFrom reports whether the keyword at k is a query's FROM or JOIN:
// one after a SELECT within the same parentheses, not DELETE's or SHOW's.
func queryFrom(before []Token, k int) bool {
	if k < 0 || before[k].Kind != Keyword || word(before[k]) != "FROM" && word(before[k]) != "JOIN" {
		return false
	}
	depth := 0
	for i := k - 1; i >= 0; i-- {
		t := before[i]
		switch {
		case isPunct(t, ")"):
			depth++
		case isPunct(t, "("):
			if depth == 0 {
				return false
			}
			depth--
		case depth == 0 && t.Kind == Keyword && word(t) == "SELECT":
			return true
		}
	}
	return false
}

// TableAlias is a short alias for the table name, as DBeaver gives: the
// first letters of its words, which start after what is not a letter or
// digit and where camel case starts one, in lower case. One that is a
// keyword, or in taken in any case, takes the first number from 2 that
// sets it apart.
func TableAlias(name string, taken []string, d Dialect) string {
	var b strings.Builder
	prev := ' '
	for i := 0; i < len(name); {
		r, size := utf8.DecodeRuneInString(name[i:])
		i += size
		if unicode.IsLetter(r) {
			next, _ := utf8.DecodeRuneInString(name[i:])
			if i == len(name) {
				next = ' '
			}
			after := !unicode.IsLetter(prev) && !unicode.IsDigit(prev)
			camel := unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsUpper(prev) && unicode.IsLower(next))
			if after || camel {
				b.WriteRune(unicode.ToLower(r))
			}
		}
		prev = r
	}
	alias := b.String()
	if alias == "" {
		alias = "t"
	}
	free := func(s string) bool {
		if keywordSet(d)[strings.ToUpper(s)] {
			return false
		}
		for _, t := range taken {
			if strings.EqualFold(t, s) {
				return false
			}
		}
		return true
	}
	for n := 2; !free(alias); n++ {
		if s := alias + strconv.Itoa(n); free(s) {
			return s
		}
	}
	return alias
}

// NeedsQuote reports whether an identifier must be quoted to keep its
// case and characters: anything but lower-case letters, digits after the
// first, and underscores.
func NeedsQuote(s string) bool {
	if s == "" {
		return true
	}
	for i, r := range s {
		if r == '_' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return true
	}
	return false
}

// NeedsQuoteIn is NeedsQuote for dialect d. Oracle folds an unquoted name
// to upper case, so there a name needs quotes unless it is upper-case
// ASCII letters, then digits, '_', '$' and '#', and no reserved word.
func NeedsQuoteIn(s string, d Dialect) bool {
	if d != Oracle {
		return NeedsQuote(s)
	}
	if s == "" || oracleReserved[s] {
		return true
	}
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
		case i > 0 && (r >= '0' && r <= '9' || r == '_' || r == '$' || r == '#'):
		default:
			return true
		}
	}
	return false
}

// FoldUnquoted returns the name an unquoted name s stands for in d: in
// Oracle, s in upper case; elsewhere s as written.
func FoldUnquoted(s string, d Dialect) string {
	if d == Oracle {
		return strings.ToUpper(s)
	}
	return s
}

// IsName reports whether a token may name a table, column or alias.
func IsName(t Token) bool { return t.Kind == Identifier || t.Kind == QuotedIdent }

// MatchingParen is the index of the ")" closing the "(" at open, or -1 if
// none closes it.
func MatchingParen(toks []Token, open int) int {
	depth := 0
	for i := open; i < len(toks); i++ {
		switch toks[i].Text {
		case "(":
			depth++
		case ")":
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// tableRefs collects tables after FROM/JOIN/UPDATE/INTO and comma-continued FROM lists,
// skipping the reference being typed at typingStart.
func tableRefs(toks []Token, typingStart int) []TableRef {
	var out []TableRef
	for i := 0; i < len(toks); i++ {
		w := word(toks[i])
		if toks[i].Kind != Keyword || (w != "FROM" && w != "JOIN" && w != "UPDATE" && w != "INTO") {
			continue
		}
		j := i + 1
		for (w == "FROM" || w == "JOIN") && j < len(toks) && (word(toks[j]) == "ONLY" || word(toks[j]) == "LATERAL") {
			j++
		}
		for {
			ref, next, ok := parseTableRef(toks, j)
			if !ok {
				break
			}
			typing := toks[j].Start == typingStart && ref.Alias == ""
			if !typing {
				out = append(out, ref)
			}
			j = next
			if w == "FROM" && j < len(toks) && isPunct(toks[j], ",") {
				j++
				continue
			}
			break
		}
	}
	return out
}

func parseTableRef(toks []Token, i int) (TableRef, int, bool) {
	if i >= len(toks) || !IsName(toks[i]) {
		return TableRef{}, i, false
	}
	parts := []Token{toks[i]}
	i++
	for i+1 < len(toks) && isPunct(toks[i], ".") && IsName(toks[i+1]) {
		parts = append(parts, toks[i+1])
		i += 2
	}
	last := parts[len(parts)-1]
	ref := TableRef{Name: unquote(last), Quoted: last.Kind == QuotedIdent}
	if len(parts) > 1 {
		schema := parts[len(parts)-2]
		ref.Schema, ref.SchemaQuoted = unquote(schema), schema.Kind == QuotedIdent
	}
	if i < len(toks) && word(toks[i]) == "AS" {
		i++
	}
	if i < len(toks) && IsName(toks[i]) {
		ref.Alias = unquote(toks[i])
		i++
	}
	return ref, i, true
}
