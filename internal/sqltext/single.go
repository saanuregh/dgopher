package sqltext

import "strings"

// aggregates are functions that fold rows into one: their results come
// from no row of a table.
var aggregates = map[string]bool{
	"COUNT": true, "SUM": true, "AVG": true, "MIN": true, "MAX": true,
	"ARRAY_AGG": true, "STRING_AGG": true, "GROUP_CONCAT": true, "LISTAGG": true,
	"JSON_AGG": true, "JSONB_AGG": true, "JSON_OBJECT_AGG": true, "JSONB_OBJECT_AGG": true,
	"BOOL_AND": true, "BOOL_OR": true, "EVERY": true, "BIT_AND": true, "BIT_OR": true,
	"STDDEV": true, "STDDEV_POP": true, "STDDEV_SAMP": true, "VARIANCE": true, "VAR_POP": true, "VAR_SAMP": true,
	"ANY_VALUE": true, "UNIQ": true, "GROUPARRAY": true, "ARGMAX": true, "ARGMIN": true,
}

// SingleTable returns the table a statement's rows are read from, when
// they are one table's rows each: a single SELECT from one table, without
// joins, grouping, aggregates, DISTINCT, set operations, window functions
// or a subquery in FROM. Otherwise why says, as a sentence, why not.
func SingleTable(stmt string, d Dialect) (ref TableRef, why string) {
	toks := statementTokens(stmt, d)
	if len(toks) == 0 {
		return TableRef{}, "The statement is empty."
	}
	depth := depths(toks)
	for i, t := range toks {
		if isPunct(t, ";") && depth[i] == 0 {
			return TableRef{}, "Only one statement's rows can be edited."
		}
	}
	switch word(toks[0]) {
	case "SELECT":
	case "WITH":
		return TableRef{}, "The rows come from a WITH query."
	default:
		return TableRef{}, "The statement is not a SELECT."
	}
	from, aggregate := -1, ""
	for i, t := range toks {
		w := word(t)
		switch {
		case w == "OVER":
			return TableRef{}, "The rows use a window function."
		case depth[i] > 0:
		case w == "DISTINCT" && i == 1:
			return TableRef{}, "The rows are DISTINCT values."
		case w == "UNION" || w == "INTERSECT" || w == "EXCEPT" || w == "MINUS":
			return TableRef{}, "The rows combine queries (" + w + ")."
		case w == "GROUP" || w == "HAVING":
			return TableRef{}, "The rows are grouped (GROUP BY)."
		case w == "JOIN" || w == "STRAIGHT_JOIN" || w == "APPLY":
			return TableRef{}, "The rows join more than one table."
		case w == "FROM" && from < 0:
			from = i
		}
		if aggregate == "" && depth[i] == 0 && aggregates[w] && i+1 < len(toks) && isPunct(toks[i+1], "(") {
			aggregate = strings.ToLower(w)
		}
	}
	if aggregate != "" {
		return TableRef{}, "The rows are computed by an aggregate (" + aggregate + ")."
	}
	if from < 0 {
		return TableRef{}, "The rows come from no table."
	}
	j := from + 1
	for j < len(toks) && (word(toks[j]) == "ONLY" || word(toks[j]) == "LATERAL") {
		j++
	}
	if j < len(toks) && isPunct(toks[j], "(") {
		return TableRef{}, "The rows come from a subquery."
	}
	ref, next, ok := parseTableRef(toks, j)
	if !ok {
		return TableRef{}, "The rows come from no table."
	}
	if next < len(toks) && isPunct(toks[next], "(") {
		return TableRef{}, "The rows come from a table function."
	}
	if next < len(toks) && isPunct(toks[next], ",") {
		return TableRef{}, "The rows join more than one table."
	}
	return ref, ""
}

// statementTokens are the significant tokens of a statement, without its
// closing semicolons.
func statementTokens(stmt string, d Dialect) []Token {
	var toks []Token
	for _, t := range Tokenize(stmt, d) {
		if significant(t) {
			toks = append(toks, t)
		}
	}
	for len(toks) > 0 && isPunct(toks[len(toks)-1], ";") {
		toks = toks[:len(toks)-1]
	}
	return toks
}

// SelectItem is an item of a SELECT's list, as far as the lexer tells.
type SelectItem struct {
	// Name is the item's output name: its alias, else the column it
	// reads; "" when the database names it.
	Name string
	// Column is the column a plain column reference reads, "" for an
	// expression.
	Column string
	// Star is set for * and qualifier.*.
	Star bool
}

// SelectItems lists the items of a SELECT's list; ok is false for a
// statement that is not a SELECT.
func SelectItems(stmt string, d Dialect) (items []SelectItem, ok bool) {
	toks := statementTokens(stmt, d)
	if len(toks) == 0 || word(toks[0]) != "SELECT" {
		return nil, false
	}
	depth := depths(toks)
	start := 1
	if start < len(toks) && word(toks[start]) == "ALL" {
		start++
	}
	end := len(toks)
	for i := start; i < len(toks); i++ {
		if depth[i] == 0 && word(toks[i]) == "FROM" {
			end = i
			break
		}
	}
	from := start
	for i := start; i <= end; i++ {
		if i == end || depth[i] == 0 && isPunct(toks[i], ",") {
			if i > from {
				items = append(items, selectItem(toks[from:i]))
			}
			from = i + 1
		}
	}
	return items, true
}

// selectItem reads one item of a select list.
func selectItem(toks []Token) SelectItem {
	last := toks[len(toks)-1]
	if last.Text == "*" && (len(toks) == 1 || isPunct(toks[len(toks)-2], ".")) {
		return SelectItem{Star: true}
	}
	var it SelectItem
	body := toks
	switch n := len(toks); {
	case n >= 3 && word(toks[n-2]) == "AS" && isName(last):
		it.Name, body = unquote(last), toks[:n-2]
	case n >= 2 && isName(last) && !isPunct(toks[n-2], "."):
		it.Name, body = unquote(last), toks[:n-1]
	}
	plain := len(body)%2 == 1
	for i, t := range body {
		if i%2 == 0 && !isName(t) || i%2 == 1 && !isPunct(t, ".") {
			plain = false
		}
	}
	if plain {
		it.Column = unquote(body[len(body)-1])
		if it.Name == "" {
			it.Name = it.Column
		}
	}
	return it
}

// ChangedTable returns the one table an UPDATE or DELETE changes, as in
// UPDATE t SET … or DELETE FROM t WHERE …; ok is false for another
// statement, or one that changes more than one table, as MySQL's UPDATE
// a JOIN b or DELETE a FROM a JOIN b.
func ChangedTable(stmt string, d Dialect) (TableRef, bool) {
	toks := statementTokens(stmt, d)
	if len(toks) == 0 {
		return TableRef{}, false
	}
	i := 1
	switch word(toks[0]) {
	case "UPDATE":
	case "DELETE":
		if i >= len(toks) || word(toks[i]) != "FROM" {
			return TableRef{}, false // DELETE a FROM a JOIN b
		}
		i++
	default:
		return TableRef{}, false
	}
	for i < len(toks) && (word(toks[i]) == "LOW_PRIORITY" || word(toks[i]) == "IGNORE" || word(toks[i]) == "QUICK" || word(toks[i]) == "ONLY") {
		i++
	}
	ref, next, ok := parseTableRef(toks, i)
	if !ok {
		return TableRef{}, false
	}
	depth := depths(toks)
	for j := next; j < len(toks); j++ {
		if depth[j] != 0 {
			continue
		}
		switch w := word(toks[j]); {
		case w == "SET" || w == "WHERE" || w == "USING" && word(toks[0]) == "DELETE":
			return ref, w != "USING"
		case w == "JOIN" || isPunct(toks[j], ","):
			return TableRef{}, false
		}
	}
	return ref, true
}
