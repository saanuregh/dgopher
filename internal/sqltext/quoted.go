package sqltext

import (
	"strconv"
	"strings"
)

// DecodeQuoted returns the text a string or quoted-name token of dialect d
// stands for, so a name spelled with escapes is the name: the quotes are
// dropped and doubled quotes undone; Postgres U&"…", U&'…' and E'…' escapes
// and ClickHouse backslash escapes are decoded. A U& escape changed with
// UESCAPE is not decoded; callers check for UESCAPE themselves.
func DecodeQuoted(text string, d Dialect) string {
	switch {
	case d == Postgres && len(text) > 3 && (text[0] == 'U' || text[0] == 'u') && text[1] == '&':
		return decodeUnicodeEscapes(unquoteText(text[2:]))
	case d == Postgres && len(text) > 1 && (text[0] == 'E' || text[0] == 'e') && text[1] == '\'':
		return decodeBackslashQuoted(text[1:], postgresEscape)
	case d == ClickHouse && len(text) > 1 && strings.IndexByte("'\"`", text[0]) >= 0:
		return decodeBackslashQuoted(text, clickHouseEscape)
	case d == SQLite && len(text) >= 2 && text[0] == '[' && text[len(text)-1] == ']':
		return text[1 : len(text)-1]
	case len(text) > 0 && strings.IndexByte("'\"`", text[0]) >= 0:
		return unquoteText(text)
	}
	return strings.Trim(text, "'\"`")
}

// unquoteText drops the quotes around s and undoes doubled quotes.
func unquoteText(s string) string {
	if len(s) < 2 {
		return s
	}
	q := s[:1]
	s = strings.TrimPrefix(s[1:], q)
	s = strings.TrimSuffix(s, q)
	return strings.ReplaceAll(s, q+q, q)
}

// decodeBackslashQuoted decodes the quoted text, whose first byte is its
// quote: a doubled quote is one quote, and escape decodes the escape at
// s[i], just after a backslash, returning the index past it.
func decodeBackslashQuoted(text string, escape func(b *strings.Builder, s string, i int) int) string {
	q := text[0]
	var b strings.Builder
	for i := 1; i < len(text); i++ {
		switch c := text[i]; {
		case c == '\\' && i+1 < len(text):
			i = escape(&b, text, i+1) - 1
		case c == q:
			if i+1 < len(text) && text[i+1] == q {
				b.WriteByte(q)
				i++
				continue
			}
			return b.String()
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// postgresEscape decodes an E'…' escape. Octal and \x escapes are bytes,
// which together may spell a UTF-8 character, as in the server.
func postgresEscape(b *strings.Builder, s string, i int) int {
	switch c := s[i]; {
	case c >= '0' && c <= '7':
		v, n := 0, 0
		for n < 3 && i+n < len(s) && s[i+n] >= '0' && s[i+n] <= '7' {
			v = v*8 + int(s[i+n]-'0')
			n++
		}
		b.WriteByte(byte(v & 0xFF))
		return i + n
	case c == 'x':
		n := 0
		for n < 2 && i+1+n < len(s) && isHexByte(s[i+1+n]) {
			n++
		}
		if n == 0 {
			b.WriteByte(c)
			return i + 1
		}
		v, _ := strconv.ParseUint(s[i+1:i+1+n], 16, 8)
		b.WriteByte(byte(v))
		return i + 1 + n
	case c == 'u' || c == 'U':
		n := 4
		if c == 'U' {
			n = 8
		}
		if r, ok := hexRune(s, i+1, n); ok {
			b.WriteRune(r)
			return i + 1 + n
		}
		b.WriteByte(c)
		return i + 1
	default:
		b.WriteByte(controlEscape(c, "bfnrt"))
		return i + 1
	}
}

// clickHouseEscape decodes a backslash escape in a ClickHouse string or
// quoted name.
func clickHouseEscape(b *strings.Builder, s string, i int) int {
	c := s[i]
	if c == 'x' && i+2 < len(s) && isHexByte(s[i+1]) && isHexByte(s[i+2]) {
		v, _ := strconv.ParseUint(s[i+1:i+3], 16, 8)
		b.WriteByte(byte(v))
		return i + 3
	}
	b.WriteByte(controlEscape(c, "abefnrtv0"))
	return i + 1
}

// controlEscape returns the character \c stands for when c is one of
// letters, else c itself.
func controlEscape(c byte, letters string) byte {
	if strings.IndexByte(letters, c) < 0 {
		return c
	}
	switch c {
	case 'a':
		return '\a'
	case 'b':
		return '\b'
	case 'e':
		return 0x1b
	case 'f':
		return '\f'
	case 'n':
		return '\n'
	case 'r':
		return '\r'
	case 't':
		return '\t'
	case 'v':
		return '\v'
	case '0':
		return 0
	}
	return c
}

// decodeUnicodeEscapes decodes \XXXX, \+XXXXXX and \\ of a U& literal.
func decodeUnicodeEscapes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		if s[i+1] == '\\' {
			b.WriteByte('\\')
			i++
			continue
		}
		start, n := i+1, 4
		if s[i+1] == '+' {
			start, n = i+2, 6
		}
		if r, ok := hexRune(s, start, n); ok {
			b.WriteRune(r)
			i = start + n - 1
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isHexByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func hexRune(s string, start, n int) (rune, bool) {
	if n == 0 || start+n > len(s) {
		return 0, false
	}
	v, err := strconv.ParseUint(s[start:start+n], 16, 32)
	if err != nil {
		return 0, false
	}
	return rune(v), true
}
