// Package mongosh parses and classifies the subset of MongoDB shell
// (mongosh) statements the console runs: method chains on db.<collection>,
// database methods such as runCommand, `use` and `show`. It runs no
// JavaScript, so every statement is classified before it is sent.
package mongosh

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type tokenKind int

const (
	tokenEnd tokenKind = iota
	tokenIdent
	tokenNumber
	tokenString
	tokenRegex
	tokenPunct
	// tokenBad is a string, regular expression or comment that does not end.
	tokenBad
)

type token struct {
	kind       tokenKind
	text       string
	start, end int
	// newline reports a line break between this token and the one before.
	newline bool
}

func (t token) is(punct string) bool { return t.kind == tokenPunct && t.text == punct }

// lex reads the tokens of a text. It never fails: what does not end runs
// to the end of the text as a tokenBad, and any other character is a
// one-character tokenPunct, for the parser to name.
func lex(src string) []token {
	var toks []token
	newline := false
	// openLineEnd is the end of a line where a regular expression did not
	// end, for regexEnd.
	openLineEnd := -1
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\n':
			newline = true
			i++
			continue
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			i++
			continue
		case strings.HasPrefix(src[i:], "//"):
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		case strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				toks = append(toks, token{kind: tokenBad, text: src[i:], start: i, end: len(src), newline: newline})
				return toks
			}
			if strings.Contains(src[i:i+2+end], "\n") {
				newline = true
			}
			i += end + 4
			continue
		}
		start := i
		kind := tokenPunct
		switch {
		case c == '"' || c == '\'' || c == '`':
			kind, i = lexString(src, i)
		case c == '/' && regexAllowed(toks):
			end, lineEnd := regexEnd(src, i, i < openLineEnd)
			if lineEnd > 0 {
				openLineEnd = lineEnd
			}
			if end > 0 {
				kind, i = tokenRegex, end
			} else {
				i++
			}
		case c >= '0' && c <= '9':
			kind, i = tokenNumber, numberEnd(src, i)
		case isIdentStart(src[i:]):
			kind, i = tokenIdent, identEnd(src, i)
		case strings.HasPrefix(src[i:], "=>"):
			i += 2
		default:
			_, size := utf8.DecodeRuneInString(src[i:])
			i += size
		}
		toks = append(toks, token{kind: kind, text: src[start:i], start: start, end: i, newline: newline})
		newline = false
	}
	return toks
}

func lexString(src string, i int) (tokenKind, int) {
	quote := src[i]
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case '\n':
			if quote != '`' {
				return tokenBad, j
			}
		case quote:
			return tokenString, j + 1
		}
	}
	return tokenBad, len(src)
}

// regexAllowed reports whether a / starts a regular expression rather
// than dividing: it does where a value may start.
func regexAllowed(toks []token) bool {
	if len(toks) == 0 {
		return true
	}
	t := toks[len(toks)-1]
	return t.kind == tokenPunct && strings.Contains("(,:[{;=!&|?+-*%<>~^", t.text[len(t.text)-1:])
}

// regexEnd is the end of the regular expression literal at i, flags
// included, or 0 when it does not end on its line; then lineEnd is where
// that line ends, when it was read to its end.
//
// afterOpen says that one at an earlier / of the line did not end. Read
// from there, this / was inside a class or escaped, or it would have
// ended there; from the first [ or ] after it both are read alike, so
// this one does not end after that either. It is read only up to there,
// so that a line of many / is lexed in linear time.
func regexEnd(src string, i int, afterOpen bool) (end, lineEnd int) {
	inClass := false
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case '\n':
			return 0, j
		case '[', ']':
			if afterOpen {
				return 0, 0
			}
			inClass = src[j] == '['
		case '/':
			if inClass {
				continue
			}
			if j == i+1 {
				return 0, 0 // "//" is a comment, not an empty pattern
			}
			k := j + 1
			for k < len(src) && isIdentPart(rune(src[k])) {
				k++
			}
			return k, 0
		}
	}
	return 0, len(src)
}

func numberEnd(src string, i int) int {
	if strings.HasPrefix(src[i:], "0x") || strings.HasPrefix(src[i:], "0X") {
		i += 2
		for i < len(src) && strings.IndexByte("0123456789abcdefABCDEF", src[i]) >= 0 {
			i++
		}
		return i
	}
	digits := func() {
		for i < len(src) && (src[i] >= '0' && src[i] <= '9' || src[i] == '_') {
			i++
		}
	}
	digits()
	if i+1 < len(src) && src[i] == '.' && src[i+1] >= '0' && src[i+1] <= '9' {
		i++
		digits()
	}
	if i < len(src) && (src[i] == 'e' || src[i] == 'E') {
		j := i + 1
		if j < len(src) && (src[j] == '+' || src[j] == '-') {
			j++
		}
		if j < len(src) && src[j] >= '0' && src[j] <= '9' {
			i = j
			digits()
		}
	}
	// A letter right after a number belongs to it, for the parser to refuse
	// whole, as 12abc.
	for i < len(src) && isIdentPart(rune(src[i])) {
		i++
	}
	return i
}

func isIdentStart(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return r == '_' || r == '$' || unicode.IsLetter(r)
}

func isIdentPart(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func identEnd(src string, i int) int {
	for i < len(src) {
		r, size := utf8.DecodeRuneInString(src[i:])
		if !isIdentPart(r) {
			break
		}
		i += size
	}
	return i
}

// position is the 1-based line and column, in characters, of a byte
// offset.
func position(src string, offset int) (line, col int) {
	offset = min(offset, len(src))
	before := src[:offset]
	line = strings.Count(before, "\n") + 1
	col = utf8.RuneCountInString(before[strings.LastIndexByte(before, '\n')+1:]) + 1
	return line, col
}

// TokenKind is what a Token is.
type TokenKind int

const (
	// Punct is any other character, or =>.
	Punct TokenKind = iota
	// Word is a name or a number.
	Word
	// String is a string, one that does not end included.
	String
	// Regex is a regular expression literal.
	Regex
)

// Token is a token of shell text, as the console's parser reads it.
type Token struct {
	Kind       TokenKind
	Start, End int
}

// Tokens reads the tokens of a text as the parser does, without its
// comments, so that what tells strings, regular expressions and comments
// apart agrees with what runs.
func Tokens(src string) []Token {
	var out []Token
	for _, t := range lex(src) {
		k := Punct
		switch t.kind {
		case tokenIdent, tokenNumber:
			k = Word
		case tokenString:
			k = String
		case tokenRegex:
			k = Regex
		case tokenBad:
			if !strings.ContainsRune(`"'`+"`", rune(t.text[0])) {
				continue // a comment that does not end
			}
			k = String
		}
		out = append(out, Token{Kind: k, Start: t.start, End: t.end})
	}
	return out
}
