// Package sqltext tokenizes, splits, classifies, completes and formats SQL text.
// All offsets are rune offsets, matching the UI text widget.
package sqltext

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// Dialect selects lexing and keyword rules.
type Dialect int

const (
	Generic Dialect = iota
	Postgres
	MySQL
	ClickHouse
	SQLite
)

// Kind is the lexical category of a token.
type Kind int

const (
	Whitespace Kind = iota
	Comment
	Keyword
	Identifier
	QuotedIdent
	String
	Number
	Operator
	Punct
	Param
	Unknown
)

// Token is a lexical token covering runes [Start, End).
type Token struct {
	Kind       Kind
	Start, End int
	Text       string
}

var multiCharOperators = []string{
	"->>", "#>>", "<=>", "!~*", "->", "#>", "<=", ">=", "<>", "!=", "||", "::", "&&", "<<", ">>", "~*", "!~", "@>", "<@",
}

// postgresOperators are Postgres (and DuckDB) operators matched before multiCharOperators.
var postgresOperators = []string{"?|", "?&", "#-", "@@", "//", "**"}

var (
	multiCharOperatorRunes = operatorRunes(multiCharOperators)
	postgresOperatorRunes  = operatorRunes(slices.Concat(postgresOperators, multiCharOperators))
)

func operatorRunes(ops []string) [][]rune {
	out := make([][]rune, len(ops))
	for k, op := range ops {
		out[k] = []rune(op)
	}
	return out
}

// isIdentStart treats every rune >= 0x80 as a name character, as the servers
// do, so no non-ASCII rune can end a name and expose what follows to splitting.
func isIdentStart(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r >= 0x80
}

func isIdentPart(r rune, d Dialect) bool {
	return isIdentStart(r) || isDigit(r) || (r == '$' && (d == Postgres || d == MySQL || d == ClickHouse || d == SQLite))
}

// identPartAt reports whether rs[i] continues a name. A '$' before '{'
// never does, so "logs_${suffix}" keeps its ${suffix} variable.
func identPartAt(rs []rune, i int, d Dialect) bool {
	return isIdentPart(rs[i], d) && !(rs[i] == '$' && i+1 < len(rs) && rs[i+1] == '{')
}

// isSpace is ASCII whitespace only; no server treats a rune >= 0x80 as a separator.
func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' || r == '\v'
}

// startsLineComment reports whether a line comment starts at rs[i].
func startsLineComment(rs []rune, i int, d Dialect) bool {
	next := rune(-1)
	if i+1 < len(rs) {
		next = rs[i+1]
	}
	switch rs[i] {
	case '-':
		if i+1 >= len(rs) || next != '-' {
			return false
		}
		if d != MySQL {
			return true
		}
		// MySQL needs whitespace or a control character after "--": "1--1" is 1 - -1.
		if i+2 >= len(rs) {
			return true
		}
		after := rs[i+2]
		return isSpace(after) || after < 0x20 || after == 0x7f
	case '#':
		switch d {
		case MySQL:
			return true
		case ClickHouse:
			// ClickHouse reads "#x" as a syntax error, not a comment.
			return next == -1 || isSpace(next) || next == '!'
		}
	}
	return false
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

func isHexDigit(r rune) bool {
	return isDigit(r) || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// Tokenize splits src into tokens that cover it completely and contiguously.
func Tokenize(src string, d Dialect) []Token {
	return tokenizeRunes([]rune(src), d)
}

// TokenizeFirst is Tokenize up to and including the nth token that is
// neither whitespace nor a comment, all of them when n is 0: a reader of a
// statement's first words is not slowed by a large statement's others.
func TokenizeFirst(src string, d Dialect, n int) []Token {
	text := ""
	if utf8.ValidString(src) {
		text = src // as string([]rune(src)), without encoding it again
	}
	return tokenizeRunesUpTo([]rune(src), text, d, n)
}

func tokenizeRunes(rs []rune, d Dialect) []Token {
	return tokenizeRunesUpTo(rs, "", d, 0)
}

// tokenizeRunesUpTo tokenizes rs, whose text is text or, when that is "",
// string(rs); it stops after limit significant tokens when limit is above
// 0.
func tokenizeRunesUpTo(rs []rune, text string, d Dialect, limit int) []Token {
	keywords := keywordSet(d)
	n := len(rs)
	if n == 0 {
		return nil
	}
	capacity := n/4 + 1
	if limit > 0 {
		capacity = min(capacity, 2*limit+1)
	}
	toks := make([]Token, 0, capacity)
	significantSeen := 0
	// Every Text is a substring of one string built from rs, so a token costs
	// no allocation of its own.
	if text == "" {
		text = string(rs)
	}
	textPos := 0
	var upper [64]byte
	at := func(i int) rune {
		if i < n {
			return rs[i]
		}
		return 0
	}
	i := 0
	for i < n {
		start := i
		kind := Unknown
		r := rs[i]
		switch {
		case isSpace(r):
			for i < n && isSpace(rs[i]) {
				i++
			}
			kind = Whitespace
		case startsLineComment(rs, i, d):
			// Postgres also ends a line comment at a lone carriage return.
			for i < n && rs[i] != '\n' && (d != Postgres || rs[i] != '\r') {
				i++
			}
			kind = Comment
		case r == '/' && at(i+1) == '*':
			i = scanBlockComment(rs, i, d == Postgres || d == ClickHouse)
			kind = Comment
		case r == '\'':
			i = scanQuoted(rs, i+1, '\'', d == MySQL || d == ClickHouse)
			kind = String
		case (r == 'E' || r == 'e') && d == Postgres && at(i+1) == '\'':
			i = scanQuoted(rs, i+2, '\'', true)
			kind = String
		case (r == 'U' || r == 'u') && d == Postgres && at(i+1) == '&' && (at(i+2) == '\'' || at(i+2) == '"'):
			quote := rs[i+2]
			i = scanQuoted(rs, i+3, quote, false)
			kind = String
			if quote == '"' {
				kind = QuotedIdent
			}
		case r == '"':
			if d == MySQL {
				i = scanQuoted(rs, i+1, '"', true)
				kind = String
			} else {
				i = scanQuoted(rs, i+1, '"', d == ClickHouse)
				kind = QuotedIdent
			}
		case r == '`' && (d == MySQL || d == ClickHouse || d == SQLite):
			i = scanQuoted(rs, i+1, '`', d == ClickHouse)
			kind = QuotedIdent
		case r == '[' && d == SQLite:
			for i++; i < n && rs[i] != ']'; i++ {
			}
			if i < n {
				i++
			}
			kind = QuotedIdent
		case r == '$' && d == ClickHouse:
			if end, ok := scanDollarQuote(rs, i); ok {
				i = end
				kind = String
			} else {
				i++
			}
		case r == '$' && d == Postgres:
			if isDigit(at(i + 1)) {
				i++
				for i < n && isDigit(rs[i]) {
					i++
				}
				kind = Param
			} else if end, ok := scanDollarQuote(rs, i); ok {
				i = end
				kind = String
			} else {
				i++
				kind = Unknown
			}
		case r == '?' && d != Postgres:
			i++
			kind = Param
		case r == ':' && at(i+1) != ':' && isIdentStart(at(i+1)):
			i++
			for i < n && identPartAt(rs, i, d) {
				i++
			}
			kind = Param
		case r == '@' && d == MySQL && (at(i+1) == '@' || isIdentStart(at(i+1))):
			i++
			if at(i) == '@' {
				i++
			}
			for i < n && (identPartAt(rs, i, d) || rs[i] == '.') {
				i++
			}
			kind = Param
		case isDigit(r) || (r == '.' && isDigit(at(i+1))):
			i = scanNumber(rs, i, d)
			kind = Number
			// ClickHouse names may start with digits: "1abc" is one word.
			if d == ClickHouse && i < n && identPartAt(rs, i, d) {
				for i < n && identPartAt(rs, i, d) {
					i++
				}
				kind = Identifier
			}
		case strings.ContainsRune("xXbBnN", r) && at(i+1) == '\'':
			i = scanQuoted(rs, i+2, '\'', d == MySQL || d == ClickHouse)
			kind = String
		case isIdentStart(r):
			for i < n && identPartAt(rs, i, d) {
				i++
			}
			kind = Identifier
			if isKeyword(keywords, rs[start:i], &upper) {
				kind = Keyword
			}
		case strings.ContainsRune("(),;.[]{}", r):
			i++
			kind = Punct
		case strings.ContainsRune("+-*/<>=!~|&%^#@:", r) || r == '?':
			i = scanOperator(rs, i, d)
			kind = Operator
		default:
			i++
		}
		width := 0
		for _, r := range rs[start:i] {
			if w := utf8.RuneLen(r); w > 0 {
				width += w
			} else {
				width += utf8.RuneLen(utf8.RuneError)
			}
		}
		toks = append(toks, Token{Kind: kind, Start: start, End: i, Text: text[textPos : textPos+width]})
		textPos += width
		if limit > 0 && kind != Whitespace && kind != Comment {
			if significantSeen++; significantSeen == limit {
				break
			}
		}
	}
	return toks
}

// isKeyword upper-cases an ASCII word into buf so the map lookup does not
// allocate; other words take the strings.ToUpper path.
func isKeyword(keywords map[string]bool, word []rune, buf *[64]byte) bool {
	if len(word) > len(buf) {
		return keywords[strings.ToUpper(string(word))]
	}
	for k, r := range word {
		if r >= 0x80 {
			return keywords[strings.ToUpper(string(word))]
		}
		if r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		buf[k] = byte(r)
	}
	return keywords[string(buf[:len(word)])]
}

// scanOperator matches the longest known operator at rs[i], but never one
// that swallows the start of a comment, which would hide a boundary.
func scanOperator(rs []rune, i int, d Dialect) int {
	ops := multiCharOperatorRunes
	if d == Postgres {
		ops = postgresOperatorRunes
	}
	for _, op := range ops {
		end := i + len(op)
		if hasPrefixAt(rs, i, op) && !commentStartsWithin(rs, i+1, end, d) {
			return end
		}
	}
	return i + 1
}

func commentStartsWithin(rs []rune, from, to int, d Dialect) bool {
	for k := from; k < to; k++ {
		if startsLineComment(rs, k, d) || (rs[k] == '/' && k+1 < len(rs) && rs[k+1] == '*') {
			return true
		}
	}
	return false
}

func hasPrefixAt(rs []rune, i int, prefix []rune) bool {
	return i+len(prefix) <= len(rs) && slices.Equal(rs[i:i+len(prefix)], prefix)
}

func scanBlockComment(rs []rune, i int, nested bool) int {
	depth := 0
	n := len(rs)
	for i < n {
		if rs[i] == '/' && i+1 < n && rs[i+1] == '*' {
			if depth == 0 || nested {
				depth++
			}
			i += 2
			continue
		}
		if rs[i] == '*' && i+1 < n && rs[i+1] == '/' {
			depth--
			i += 2
			if depth == 0 {
				return i
			}
			continue
		}
		i++
	}
	return n
}

// scanQuoted scans from just after the opening quote; a doubled quote is an escape.
func scanQuoted(rs []rune, i int, quote rune, backslash bool) int {
	n := len(rs)
	for i < n {
		switch {
		case backslash && rs[i] == '\\':
			i += 2
		case rs[i] == quote:
			if i+1 < n && rs[i+1] == quote {
				i += 2
				continue
			}
			return i + 1
		default:
			i++
		}
	}
	return n
}

func scanDollarQuote(rs []rune, i int) (int, bool) {
	n := len(rs)
	j := i + 1
	if j < n && isIdentStart(rs[j]) {
		for j < n && rs[j] != '$' && (isIdentStart(rs[j]) || isDigit(rs[j])) {
			j++
		}
	}
	if j >= n || rs[j] != '$' {
		return 0, false
	}
	delim := rs[i : j+1]
	for k := j + 1; k+len(delim) <= n; k++ {
		if slices.Equal(rs[k:k+len(delim)], delim) {
			return k + len(delim), true
		}
	}
	return n, true
}

// scanNumber scans a number. Postgres also reads 0b and 0o prefixes and "_"
// between digits; MySQL and ClickHouse read 0b; ClickHouse reads "_" too.
func scanNumber(rs []rune, i int, d Dialect) int {
	n := len(rs)
	underscore := d == Postgres || d == ClickHouse
	if rs[i] == '0' && i+1 < n {
		var isRadixDigit func(rune) bool
		switch rs[i+1] {
		case 'x', 'X':
			isRadixDigit = isHexDigit
		case 'b', 'B':
			if d == Postgres || d == MySQL || d == ClickHouse {
				isRadixDigit = func(r rune) bool { return r == '0' || r == '1' }
			}
		case 'o', 'O':
			if d == Postgres {
				isRadixDigit = func(r rune) bool { return r >= '0' && r <= '7' }
			}
		}
		if isRadixDigit != nil {
			j := i + 2
			if underscore && j+1 < n && rs[j] == '_' && isRadixDigit(rs[j+1]) {
				j++
			}
			if j < n && isRadixDigit(rs[j]) {
				return scanDigits(rs, j, isRadixDigit, underscore)
			}
		}
	}
	i = scanDigits(rs, i, isDigit, underscore)
	if i < n && rs[i] == '.' {
		i++
		if i < n && isDigit(rs[i]) {
			i = scanDigits(rs, i, isDigit, underscore)
		}
	}
	if i < n && (rs[i] == 'e' || rs[i] == 'E') {
		j := i + 1
		if j < n && (rs[j] == '+' || rs[j] == '-') {
			j++
		}
		if j < n && isDigit(rs[j]) {
			i = scanDigits(rs, j, isDigit, underscore)
		}
	}
	return i
}

// scanDigits scans digits from rs[i], with a single "_" allowed between two
// digits when underscore is set.
func scanDigits(rs []rune, i int, isDigitOf func(rune) bool, underscore bool) int {
	n := len(rs)
	for i < n {
		switch {
		case isDigitOf(rs[i]):
			i++
		case underscore && rs[i] == '_' && i > 0 && isDigitOf(rs[i-1]) && i+1 < n && isDigitOf(rs[i+1]):
			i++
		default:
			return i
		}
	}
	return i
}

func significant(t Token) bool { return t.Kind != Whitespace && t.Kind != Comment }

// word returns the upper-case text of a keyword or identifier token, else "".
func word(t Token) string {
	if t.Kind == Keyword || t.Kind == Identifier {
		return strings.ToUpper(t.Text)
	}
	return ""
}
