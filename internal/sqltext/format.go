package sqltext

import "strings"

var clauseKeywords = map[string]bool{
	"SELECT": true, "FROM": true, "WHERE": true, "GROUP": true, "ORDER": true, "HAVING": true,
	"LIMIT": true, "OFFSET": true, "UNION": true, "INTERSECT": true, "EXCEPT": true, "SET": true,
	"VALUES": true, "RETURNING": true, "WITH": true, "PREWHERE": true, "SETTINGS": true, "FORMAT": true,
}

var joinModifiers = map[string]bool{"INNER": true, "LEFT": true, "RIGHT": true, "FULL": true, "CROSS": true, "NATURAL": true, "GLOBAL": true, "ANY": true, "OUTER": true,
	"SEMI": true, "ANTI": true, "ASOF": true, "PASTE": true, "POSITIONAL": true, "ARRAY": true}

// reservedUpper lists the keywords upper-cased on MySQL and ClickHouse, where
// other keywords may be case-sensitive names (ClickHouse) or common column names.
var reservedUpper = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`SELECT FROM WHERE GROUP BY ORDER HAVING LIMIT UNION INTERSECT EXCEPT DISTINCT AS ON
	USING JOIN INNER LEFT RIGHT FULL OUTER CROSS NATURAL AND OR NOT IN IS NULL LIKE BETWEEN EXISTS
	CASE WHEN THEN ELSE END INSERT INTO VALUES UPDATE SET DELETE WITH ASC DESC CREATE ALTER DROP
	TABLE PREWHERE`) {
		reservedUpper[w] = true
	}
}

// Format is FormatWith in BlankLineAndSemicolon mode.
func Format(src string, d Dialect) string {
	return FormatWith(src, d, SplitOptions{Mode: BlankLineAndSemicolon})
}

// FormatWith pretty-prints SQL conservatively. It only changes whitespace and
// keyword case; every other token is emitted unchanged and in order. Each
// statement found by SplitWith is formatted separately, with one blank line
// between statements and ';' only where the source had one.
func FormatWith(src string, d Dialect, o SplitOptions) string {
	var b strings.Builder
	wroteAny := false
	for _, seg := range splitSegments([]rune(src), d, o) {
		toks := seg.toks
		for len(toks) > 0 && toks[len(toks)-1].Kind == Whitespace {
			toks = toks[:len(toks)-1]
		}
		text := formatStatement(toks, d)
		if text == "" && !seg.terminated {
			continue
		}
		if wroteAny {
			b.WriteString("\n\n")
		}
		b.WriteString(text)
		if seg.terminated {
			if n := len(toks); n > 0 && isLineComment(toks[n-1]) {
				b.WriteString("\n")
			}
			b.WriteString(";")
		}
		wroteAny = true
	}
	return b.String()
}

// FormatRange formats the statements overlapping the rune range [start, end)
// (or containing the caret when start == end) and returns the whole new text
// with everything else untouched, plus the rune range of the formatted part.
func FormatRange(src string, start, end int, d Dialect, o SplitOptions) (text string, newStart, newEnd int) {
	from, to := -1, -1
	for _, s := range SplitWith(src, d, o) {
		overlaps := s.Start < end && s.End > start
		if start == end {
			overlaps = start >= s.Start && start <= s.End
		}
		if !overlaps {
			continue
		}
		if from < 0 {
			from = s.Start
		}
		to = s.End
	}
	if from < 0 {
		return src, start, end
	}
	rs := []rune(src)
	formatted := []rune(FormatWith(string(rs[from:to]), d, o))
	out := make([]rune, 0, len(rs)-(to-from)+len(formatted))
	out = append(out, rs[:from]...)
	out = append(out, formatted...)
	out = append(out, rs[to:]...)
	return string(out), from, from + len(formatted)
}

func isLineComment(t Token) bool {
	return t.Kind == Comment && !strings.HasPrefix(t.Text, "/*")
}

// formatStatement formats tokens of one statement (whitespace tokens included).
func formatStatement(toks []Token, d Dialect) string {
	var b strings.Builder
	var prev *Token
	gap := false // original source had whitespace before the current token
	depth := 0
	clause := ""
	inJoin := false
	betweenPending := false
	for idx := range toks {
		t := toks[idx]
		if t.Kind == Whitespace {
			gap = true
			continue
		}
		w := ""
		if t.Kind == Keyword {
			w = strings.ToUpper(t.Text)
		}
		if isPunct(t, ")") {
			depth--
		}
		sep := ""
		if prev != nil {
			switch {
			case isLineComment(*prev):
				sep = "\n"
			case !gap:
				sep = ""
			case isPunct(t, ",") || isPunct(t, ")") || isPunct(*prev, "("):
				sep = ""
			default:
				sep = " "
			}
			if depth == 0 && w != "" {
				prevWord := ""
				if prev.Kind == Keyword {
					prevWord = strings.ToUpper(prev.Text)
				}
				switch {
				case w == "JOIN" && !joinModifiers[prevWord]:
					sep = "\n"
				case joinModifiers[w] && w != "OUTER" && w != "ANY" && nextKeywordIs(toks, idx, "JOIN", "OUTER", "ANY", "SEMI", "ANTI", "ASOF", "PASTE", "ARRAY"):
					if !joinModifiers[prevWord] {
						sep = "\n"
					}
				case w == "ON" && inJoin:
					sep = "\n  "
				case (w == "AND" || w == "OR") && (clause == "WHERE" || clause == "HAVING" || clause == "PREWHERE"):
					if w == "AND" && betweenPending {
						betweenPending = false
					} else {
						sep = "\n  "
					}
				case clauseKeywords[w] && !(w == "SET" && clause != "UPDATE") && !(w == "FROM" && prevWord == "DELETE") && !nameNotClause(toks, idx, d):
					sep = "\n"
				}
			}
			if depth == 0 && clause == "SELECT" && isPunct(*prev, ",") {
				sep = "\n  "
			}
			if depth == 0 && clause == "SELECT" && prev.Kind == Keyword && (strings.ToUpper(prev.Text) == "SELECT" || strings.ToUpper(prev.Text) == "DISTINCT") && w != "DISTINCT" && w != "ALL" {
				sep = "\n  "
			}
			if isLineComment(*prev) && !strings.HasPrefix(sep, "\n") {
				sep = "\n"
			}
			// Splitting adjacent operator text could create a comment ("--" on MySQL, "# " on ClickHouse).
			if !gap && prev.Kind == Operator {
				sep = ""
			}
		}
		b.WriteString(sep)
		if w != "" && (reservedUpper[w] || (d != MySQL && d != ClickHouse)) {
			b.WriteString(w)
		} else {
			b.WriteString(t.Text)
		}
		if depth == 0 && w != "" {
			switch {
			case w == "JOIN":
				inJoin = true
			case w == "BETWEEN":
				betweenPending = true
			case w == "UPDATE" && clause == "":
				clause = "UPDATE"
			case clauseKeywords[w] && !(w == "SET" && clause != "UPDATE") && !nameNotClause(toks, idx, d):
				clause = w
				if w != "FROM" {
					inJoin = false
				}
			}
		}
		if isPunct(t, "(") {
			depth++
		}
		cur := t
		prev = &cur
		gap = false
	}
	return b.String()
}

// nameNotClause reports a ClickHouse FORMAT or SETTINGS written as a
// column name: a clause has a name after it, and never follows a comma, a
// dot, AS or SELECT.
func nameNotClause(toks []Token, idx int, d Dialect) bool {
	w := strings.ToUpper(toks[idx].Text)
	if d != ClickHouse || (w != "FORMAT" && w != "SETTINGS") {
		return false
	}
	for i := idx - 1; i >= 0; i-- {
		if !significant(toks[i]) {
			continue
		}
		if isPunct(toks[i], ",") || isPunct(toks[i], ".") {
			return true
		}
		switch word(toks[i]) {
		case "AS", "SELECT", "DISTINCT":
			return true
		}
		break
	}
	for _, t := range toks[idx+1:] {
		if significant(t) {
			return t.Kind != Identifier && t.Kind != QuotedIdent
		}
	}
	return true
}

func nextKeywordIs(toks []Token, idx int, words ...string) bool {
	for _, t := range toks[idx+1:] {
		if !significant(t) {
			continue
		}
		u := strings.ToUpper(t.Text)
		for _, w := range words {
			if u == w {
				return true
			}
		}
		return false
	}
	return false
}
