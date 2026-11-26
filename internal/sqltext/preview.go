package sqltext

import (
	"slices"
	"strings"
)

// WritePreview is how to read the rows an UPDATE or DELETE would change, without changing them.
type WritePreview struct {
	Verb    string   // "UPDATE" or "DELETE"
	Table   TableRef // the target, as written
	Changed []string // SET columns, unquoted, in order
	Rows    string   // SELECT of the new values then the row
	Count   string   // SELECT COUNT(*) of the same rows
}

// NotAWrite is why Preview Changes refuses a statement that is no UPDATE
// or DELETE.
const NotAWrite = "Preview Changes shows the rows an UPDATE or DELETE would change."

// setEnds are the clauses that end an UPDATE's SET list.
var setEnds = []string{"WHERE", "FROM", "RETURNING", "ORDER", "LIMIT", "OFFSET", "OUTPUT"}

// PreviewWrite rewrites stmt; why is one sentence when it cannot.
// newName quotes the alias of a SET column's new value, as the engine quotes names.
func PreviewWrite(stmt string, d Dialect, newName func(col string) string) (p WritePreview, why string) {
	toks := statementTokens(stmt, d)
	if len(toks) == 0 {
		return WritePreview{}, NotAWrite
	}
	depth := depths(toks)
	verb := word(toks[0])
	if verb == "WITH" || verb == "ALTER" {
		verb = ""
		for i, t := range toks {
			if w := word(t); depth[i] == 0 && (w == "UPDATE" || w == "DELETE") {
				verb = w
				break
			}
		}
	}
	if verb != "UPDATE" && verb != "DELETE" {
		return WritePreview{}, NotAWrite
	}
	refuse := func(cause string) (WritePreview, string) {
		return WritePreview{}, "Preview Changes reads one table's rows: this " + verb + " " + cause
	}
	for i, t := range toks {
		if depth[i] != 0 {
			continue
		}
		next := ""
		if i+1 < len(toks) {
			next = word(toks[i+1])
		}
		switch w := word(t); {
		case isPunct(t, ";"):
			return refuse("is more than one statement.")
		case i == 0 && w == "WITH":
			return refuse("starts with a WITH query.")
		case i == 0 && w == "ALTER":
			return refuse("is an ALTER TABLE mutation.")
		case w == "USING":
			return refuse("uses USING.")
		case w == "JOIN" || w == "STRAIGHT_JOIN" || d == TSQL && w == "APPLY" && i > 0 && (word(toks[i-1]) == "CROSS" || word(toks[i-1]) == "OUTER"):
			return refuse("joins another table.")
		case d == TSQL && w == "OUTPUT":
			return refuse("writes its OUTPUT.")
		case w == "CURRENT" && next == "OF" && i > 0 && word(toks[i-1]) == "WHERE":
			return refuse("uses WHERE CURRENT OF a cursor.")
		case w == "ON" && next == "CLUSTER":
			return refuse("runs ON CLUSTER.")
		}
	}
	text := func(from, to int) string { return stmt[toks[from].Start:toks[to].End] }

	i := 1
	for i < len(toks) && d == MySQL && (word(toks[i]) == "LOW_PRIORITY" || word(toks[i]) == "IGNORE" || verb == "DELETE" && word(toks[i]) == "QUICK") {
		i++
	}
	top := ""
	if d == TSQL && i+1 < len(toks) && word(toks[i]) == "TOP" && isPunct(toks[i+1], "(") {
		end := MatchingParen(toks, i+1)
		if end < 0 {
			return refuse("has a TOP it cannot read.")
		}
		top = text(i, end) + " "
		i = end + 1
		if i < len(toks) && word(toks[i]) == "PERCENT" {
			return refuse("updates a percent of the rows (TOP … PERCENT).")
		}
	}
	from := verb == "DELETE" && i < len(toks) && word(toks[i]) == "FROM"
	if from {
		i++
	}
	only := ""
	if d == Postgres && i < len(toks) && word(toks[i]) == "ONLY" {
		only = "ONLY "
		i++
	}
	ref, next, ok := parseTableRef(toks, i)
	if !ok {
		return refuse("names no table it can read.")
	}
	nameEnd := next - 1
	if ref.Alias != "" {
		nameEnd--
		if word(toks[nameEnd]) == "AS" {
			nameEnd--
		}
	}
	qualifier := text(i, nameEnd)
	target := text(i, next-1)
	if ref.Alias != "" {
		qualifier = toks[next-1].Text
		if d == Oracle {
			target = text(i, nameEnd) + " " + qualifier
		}
	}
	i = next
	if i < len(toks) && (isPunct(toks[i], ",") || verb == "DELETE" && !from && word(toks[i]) == "FROM") {
		return refuse("names more than one table.")
	}

	var items []string
	// setParams is whether a SET value holds a parameter, which the count
	// must take too, as the statement's values come in its order.
	setParams := false
	if verb == "UPDATE" {
		if i >= len(toks) || word(toks[i]) != "SET" {
			return refuse("has a form it cannot read.")
		}
		i++
		start := i
		for i < len(toks) && !(depth[i] == 0 && slices.Contains(setEnds, word(toks[i]))) {
			i++
		}
		for _, a := range splitAt(toks[start:i], depth[start:i], start) {
			eq := -1
			for k := a[0]; k < a[1]; k++ {
				if depth[k] == 0 && isOperator(toks[k], "=") {
					eq = k
					break
				}
			}
			if eq <= a[0] || eq+1 >= a[1] || !IsName(toks[eq-1]) {
				return refuse("sets columns it cannot read.")
			}
			col := unquote(toks[eq-1])
			if d == MySQL {
				// MySQL sets the columns in order, so a value that reads one
				// set before it reads the new value, which a SELECT does not.
				for k := eq + 1; k < a[1]; k++ {
					if IsName(toks[k]) && slices.ContainsFunc(p.Changed, func(c string) bool { return strings.EqualFold(c, unquote(toks[k])) }) {
						return refuse("sets " + col + " from " + unquote(toks[k]) + ", which it sets before: MySQL reads the new value.")
					}
				}
			}
			p.Changed = append(p.Changed, col)
			value := text(eq+1, a[1]-1)
			if eq+2 == a[1] && word(toks[eq+1]) == "DEFAULT" {
				// DEFAULT is no value a SELECT can read: it is shown as said.
				value = "'(DEFAULT)'"
			}
			for k := eq + 1; k < a[1]; k++ {
				// DuckDB's ? lexes as PostgreSQL's operator.
				setParams = setParams || toks[k].Kind == Param || toks[k].Text == "?"
			}
			items = append(items, value+" AS "+newName(col))
		}
		if len(items) == 0 {
			return refuse("sets columns it cannot read.")
		}
	}
	if i < len(toks) && word(toks[i]) == "FROM" {
		name := "another table"
		if i+1 < len(toks) && IsName(toks[i+1]) {
			name = unquote(toks[i+1])
		}
		return refuse("also reads " + name + " (FROM).")
	}
	where := ""
	if i < len(toks) && word(toks[i]) == "WHERE" {
		i++
		start := i
		for i < len(toks) && !(depth[i] == 0 && slices.Contains(whereEnds, word(toks[i]))) {
			i++
		}
		if i > start {
			where = " WHERE " + text(start, i-1)
		}
	}
	tail := ""
	if i < len(toks) && (word(toks[i]) == "ORDER" || word(toks[i]) == "LIMIT") {
		start := i
		for i < len(toks) && !(depth[i] == 0 && word(toks[i]) == "RETURNING") {
			i++
		}
		tail = " " + text(start, i-1)
	}
	if i < len(toks) && word(toks[i]) != "RETURNING" {
		return refuse("has a form it cannot read.")
	}

	body := " FROM " + only + target + where
	items = append(items, qualifier+".*")
	p.Verb, p.Table = verb, ref
	p.Rows = "SELECT " + top + strings.Join(items, ", ") + body + tail
	p.Count = "SELECT COUNT(*)" + body
	switch {
	case setParams:
		p.Count = "SELECT COUNT(*) FROM (" + p.Rows + ") x"
	case top != "" || tail != "":
		p.Count = "SELECT COUNT(*) FROM (SELECT " + top + "1 AS n" + body + tail + ") x"
	}
	return p, ""
}

// splitAt splits tokens at their top-level commas, returning each part's
// [start, end) as indexes offset by base.
func splitAt(toks []Token, depth []int, base int) [][2]int {
	var out [][2]int
	start := 0
	for k := 0; k <= len(toks); k++ {
		if k == len(toks) || depth[k] == 0 && isPunct(toks[k], ",") {
			if k > start {
				out = append(out, [2]int{base + start, base + k})
			}
			start = k + 1
		}
	}
	return out
}
