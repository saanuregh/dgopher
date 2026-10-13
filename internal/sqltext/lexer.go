// Package sqltext tokenizes, splits, classifies, completes and formats SQL text.
// All offsets are byte offsets into the Go string.
package sqltext

import (
	"slices"
	"strings"
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

// Token is a lexical token covering bytes [Start, End).
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

var postgresAndMultiCharOperators = slices.Concat(postgresOperators, multiCharOperators)

// isIdentStart treats every byte >= 0x80 as a name character, as the servers
// do, so no non-ASCII character can end a name and expose what follows to
// splitting. Every byte of a multi-byte character is >= 0x80, so a name
// never ends inside one, and an invalid byte reads as U+FFFD would.
func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentPart(c byte, d Dialect) bool {
	return isIdentStart(c) || isDigit(c) || (c == '$' && (d == Postgres || d == MySQL || d == ClickHouse || d == SQLite))
}

// identPartAt reports whether s[i] continues a name. A '$' before '{'
// never does, so "logs_${suffix}" keeps its ${suffix} variable.
func identPartAt(s string, i int, d Dialect) bool {
	return isIdentPart(s[i], d) && !(s[i] == '$' && i+1 < len(s) && s[i+1] == '{')
}

// isSpace is ASCII whitespace only; no server treats a character >= 0x80 as a separator.
func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// startsLineComment reports whether a line comment starts at s[i].
func startsLineComment(s string, i int, d Dialect) bool {
	switch s[i] {
	case '-':
		if i+1 >= len(s) || s[i+1] != '-' {
			return false
		}
		if d != MySQL {
			return true
		}
		// MySQL needs whitespace or a control character after "--": "1--1" is 1 - -1.
		if i+2 >= len(s) {
			return true
		}
		after := s[i+2]
		return isSpace(after) || after < 0x20 || after == 0x7f
	case '#':
		switch d {
		case MySQL:
			return true
		case ClickHouse:
			// ClickHouse reads "#x" as a syntax error, not a comment.
			return i+1 >= len(s) || isSpace(s[i+1]) || s[i+1] == '!'
		}
	}
	return false
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// quoting is how '…' and "…" are read, which a session or server setting
// can switch: backslash escapes in them, and MySQL's ANSI_QUOTES, which
// makes "…" a name.
type quoting struct{ backslashes, ansiQuotes bool }

// defaultQuoting is how d reads strings unless a setting switches it.
func defaultQuoting(d Dialect) quoting {
	return quoting{backslashes: d == MySQL || d == ClickHouse}
}

// Tokenize splits src into tokens that cover it completely and contiguously.
func Tokenize(src string, d Dialect) []Token {
	return tokenizeQuoted(src, d, defaultQuoting(d))
}

// TokenizeFirst is Tokenize up to and including the nth token that is
// neither whitespace nor a comment, all of them when n is 0: a reader of a
// statement's first words is not slowed by a large statement's others.
func TokenizeFirst(src string, d Dialect, n int) []Token {
	return tokenizeUpTo(src, d, defaultQuoting(d), n)
}

// tokenizeQuoted is Tokenize with strings read as q reads them.
func tokenizeQuoted(src string, d Dialect, q quoting) []Token {
	return tokenizeUpTo(src, d, q, 0)
}

// tokenizeUpTo tokenizes s with strings read as q reads them; it stops
// after limit significant tokens when limit is above 0. SQL's syntax
// characters are all ASCII, so s is read byte by byte; every Text is a
// substring of s.
func tokenizeUpTo(s string, d Dialect, q quoting, limit int) []Token {
	keywords := keywordSet(d)
	n := len(s)
	if n == 0 {
		return nil
	}
	capacity := n/4 + 1
	if limit > 0 {
		capacity = min(capacity, 2*limit+1)
	}
	toks := make([]Token, 0, capacity)
	significantSeen := 0
	var upper [64]byte
	at := func(i int) byte {
		if i < n {
			return s[i]
		}
		return 0
	}
	i := 0
	for i < n {
		start := i
		kind := Unknown
		c := s[i]
		switch {
		case isSpace(c):
			for i < n && isSpace(s[i]) {
				i++
			}
			kind = Whitespace
		case startsLineComment(s, i, d):
			// Postgres also ends a line comment at a lone carriage return.
			for i < n && s[i] != '\n' && (d != Postgres || s[i] != '\r') {
				i++
			}
			kind = Comment
		case c == '/' && at(i+1) == '*':
			i = scanBlockComment(s, i, d == Postgres || d == ClickHouse)
			kind = Comment
		case c == '\'':
			i = scanQuoted(s, i+1, '\'', q.backslashes)
			kind = String
		case (c == 'E' || c == 'e') && d == Postgres && at(i+1) == '\'':
			i = scanQuoted(s, i+2, '\'', true)
			kind = String
		case (c == 'U' || c == 'u') && d == Postgres && at(i+1) == '&' && (at(i+2) == '\'' || at(i+2) == '"'):
			quote := s[i+2]
			i = scanQuoted(s, i+3, quote, false)
			kind = String
			if quote == '"' {
				kind = QuotedIdent
			}
		case c == '"':
			switch {
			case d == MySQL && q.ansiQuotes:
				i = scanQuoted(s, i+1, '"', false)
				kind = QuotedIdent
			case d == MySQL:
				i = scanQuoted(s, i+1, '"', q.backslashes)
				kind = String
			default:
				i = scanQuoted(s, i+1, '"', d == ClickHouse)
				kind = QuotedIdent
			}
		case c == '`' && (d == MySQL || d == ClickHouse || d == SQLite):
			i = scanQuoted(s, i+1, '`', d == ClickHouse)
			kind = QuotedIdent
		case c == '[' && d == SQLite:
			for i++; i < n && s[i] != ']'; i++ {
			}
			if i < n {
				i++
			}
			kind = QuotedIdent
		case c == '$' && d == ClickHouse:
			if end, ok := scanDollarQuote(s, i); ok {
				i = end
				kind = String
			} else {
				i++
			}
		case c == '$' && d == Postgres:
			if isDigit(at(i + 1)) {
				i++
				for i < n && isDigit(s[i]) {
					i++
				}
				kind = Param
			} else if end, ok := scanDollarQuote(s, i); ok {
				i = end
				kind = String
			} else {
				i++
				kind = Unknown
			}
		case c == '?' && d != Postgres:
			i++
			kind = Param
		case c == ':' && at(i+1) != ':' && isIdentStart(at(i+1)):
			i++
			for i < n && identPartAt(s, i, d) {
				i++
			}
			kind = Param
		case c == '@' && d == MySQL && (at(i+1) == '@' || isIdentStart(at(i+1))):
			i++
			if at(i) == '@' {
				i++
			}
			for i < n && (identPartAt(s, i, d) || s[i] == '.') {
				i++
			}
			kind = Param
		case isDigit(c) || (c == '.' && isDigit(at(i+1))):
			i = scanNumber(s, i, d)
			kind = Number
			// ClickHouse names may start with digits: "1abc" is one word.
			if d == ClickHouse && i < n && identPartAt(s, i, d) {
				for i < n && identPartAt(s, i, d) {
					i++
				}
				kind = Identifier
			}
		case strings.IndexByte("xXbBnN", c) >= 0 && at(i+1) == '\'':
			i = scanQuoted(s, i+2, '\'', q.backslashes)
			kind = String
		case isIdentStart(c):
			for i < n && identPartAt(s, i, d) {
				i++
			}
			kind = Identifier
			if isKeyword(keywords, s[start:i], &upper) {
				kind = Keyword
			}
		case strings.IndexByte("(),;.[]{}", c) >= 0:
			i++
			kind = Punct
		case strings.IndexByte("+-*/<>=!~|&%^#@:", c) >= 0 || c == '?':
			i = scanOperator(s, i, d)
			kind = Operator
		default:
			i++
		}
		toks = append(toks, Token{Kind: kind, Start: start, End: i, Text: s[start:i]})
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
func isKeyword(keywords map[string]bool, word string, buf *[64]byte) bool {
	if len(word) > len(buf) {
		return keywords[strings.ToUpper(word)]
	}
	for k := 0; k < len(word); k++ {
		c := word[k]
		if c >= 0x80 {
			return keywords[strings.ToUpper(word)]
		}
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		buf[k] = c
	}
	return keywords[string(buf[:len(word)])]
}

// scanOperator matches the longest known operator at s[i], but never one
// that swallows the start of a comment, which would hide a boundary.
func scanOperator(s string, i int, d Dialect) int {
	ops := multiCharOperators
	if d == Postgres {
		ops = postgresAndMultiCharOperators
	}
	for _, op := range ops {
		end := i + len(op)
		if strings.HasPrefix(s[i:], op) && !commentStartsWithin(s, i+1, end, d) {
			return end
		}
	}
	return i + 1
}

func commentStartsWithin(s string, from, to int, d Dialect) bool {
	for k := from; k < to; k++ {
		if startsLineComment(s, k, d) || (s[k] == '/' && k+1 < len(s) && s[k+1] == '*') {
			return true
		}
	}
	return false
}

func scanBlockComment(s string, i int, nested bool) int {
	depth := 0
	n := len(s)
	for i < n {
		// Where comments do not nest, an opener inside one is text: its
		// star may begin the */ that closes it, as in /*/*/.
		if s[i] == '/' && i+1 < n && s[i+1] == '*' && (depth == 0 || nested) {
			depth++
			i += 2
			continue
		}
		if s[i] == '*' && i+1 < n && s[i+1] == '/' {
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

// scanQuoted scans from just after the opening quote; a doubled quote is an
// escape. A backslash escapes the byte after it: the bytes that follow
// a multi-byte character's first are >= 0x80 and so never a quote.
func scanQuoted(s string, i int, quote byte, backslash bool) int {
	n := len(s)
	for i < n {
		switch {
		case backslash && s[i] == '\\':
			i += 2
		case s[i] == quote:
			if i+1 < n && s[i+1] == quote {
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

func scanDollarQuote(s string, i int) (int, bool) {
	n := len(s)
	j := i + 1
	if j < n && isIdentStart(s[j]) {
		for j < n && s[j] != '$' && (isIdentStart(s[j]) || isDigit(s[j])) {
			j++
		}
	}
	if j >= n || s[j] != '$' {
		return 0, false
	}
	delim := s[i : j+1]
	if k := strings.Index(s[j+1:], delim); k >= 0 {
		return j + 1 + k + len(delim), true
	}
	return n, true
}

// scanNumber scans a number. Postgres also reads 0b and 0o prefixes and "_"
// between digits; MySQL and ClickHouse read 0b; ClickHouse reads "_" too.
func scanNumber(s string, i int, d Dialect) int {
	n := len(s)
	underscore := d == Postgres || d == ClickHouse
	if s[i] == '0' && i+1 < n {
		var isRadixDigit func(byte) bool
		switch s[i+1] {
		case 'x', 'X':
			isRadixDigit = isHexDigit
		case 'b', 'B':
			if d == Postgres || d == MySQL || d == ClickHouse {
				isRadixDigit = func(c byte) bool { return c == '0' || c == '1' }
			}
		case 'o', 'O':
			if d == Postgres {
				isRadixDigit = func(c byte) bool { return c >= '0' && c <= '7' }
			}
		}
		if isRadixDigit != nil {
			j := i + 2
			if underscore && j+1 < n && s[j] == '_' && isRadixDigit(s[j+1]) {
				j++
			}
			if j < n && isRadixDigit(s[j]) {
				return scanDigits(s, j, isRadixDigit, underscore)
			}
		}
	}
	i = scanDigits(s, i, isDigit, underscore)
	if i < n && s[i] == '.' {
		i++
		if i < n && isDigit(s[i]) {
			i = scanDigits(s, i, isDigit, underscore)
		}
	}
	if i < n && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < n && (s[j] == '+' || s[j] == '-') {
			j++
		}
		if j < n && isDigit(s[j]) {
			i = scanDigits(s, j, isDigit, underscore)
		}
	}
	return i
}

// scanDigits scans digits from s[i], with a single "_" allowed between two
// digits when underscore is set.
func scanDigits(s string, i int, isDigitOf func(byte) bool, underscore bool) int {
	n := len(s)
	for i < n {
		switch {
		case isDigitOf(s[i]):
			i++
		case underscore && s[i] == '_' && i > 0 && isDigitOf(s[i-1]) && i+1 < n && isDigitOf(s[i+1]):
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
