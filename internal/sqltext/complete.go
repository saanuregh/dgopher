package sqltext

import "strings"

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
	Tables      []TableRef
}

var tableKeywords = map[string]bool{"FROM": true, "JOIN": true, "UPDATE": true, "INTO": true, "TABLE": true, "DESCRIBE": true, "DESC": true}

// CompletionAt returns the autocomplete context for the caret position.
func CompletionAt(src string, caret int, d Dialect) CompletionContext {
	rs := []rune(src)
	if caret < 0 {
		caret = 0
	}
	if caret > len(rs) {
		caret = len(rs)
	}
	var ctx CompletionContext
	start := caret
	for start > 0 && identPartAt(rs, start-1, d) {
		start--
	}
	// A name never starts with '$': there it is a parameter's sigil.
	for start < caret && rs[start] == '$' {
		start++
	}
	ctx.Prefix = string(rs[start:caret])
	ctx.PrefixStart = start

	all := tokenizeRunes(rs, d)
	for _, t := range all {
		// A line comment runs to the end of its line, so its end is still inside it.
		lineComment := t.Kind == Comment && !strings.HasPrefix(t.Text, "/*")
		if t.Start < caret && (caret < t.End || caret == t.End && lineComment) && (t.Kind == String || t.Kind == Comment) {
			return ctx
		}
	}

	stmtStart, stmtEnd := 0, len(rs)
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
	if start > 0 && rs[start-1] == '.' {
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
	ctx.WantTable = wantTable(before)

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
		// SQLite [name] has no escape for ']'.
		return strings.TrimSuffix(t.Text[1:], "]")
	}
	inner := strings.TrimSuffix(t.Text[1:], q)
	return strings.ReplaceAll(inner, q+q, q)
}

func wantTable(before []Token) bool {
	if len(before) == 0 {
		return false
	}
	last := before[len(before)-1]
	if len(before) > 1 && last.Kind == Keyword && (word(last) == "LATERAL" || word(last) == "ONLY") {
		if prev := before[len(before)-2]; prev.Kind == Keyword && (word(prev) == "FROM" || word(prev) == "JOIN") {
			return true
		}
	}
	if tableKeywords[word(last)] && last.Kind == Keyword {
		return true
	}
	if !isPunct(last, ",") {
		return false
	}
	depth := 0
	for i := len(before) - 2; i >= 0; i-- {
		t := before[i]
		switch {
		case isPunct(t, ")"):
			depth++
		case isPunct(t, "("):
			if depth == 0 {
				return false
			}
			depth--
		case depth == 0 && t.Kind == Keyword:
			switch word(t) {
			case "FROM":
				return true
			case "AS", "ONLY", "LATERAL":
			default:
				return false
			}
		}
	}
	return false
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
