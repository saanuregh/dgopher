package mongosh

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// value reads one value; expected names what may stand there, for the
// error when nothing does.
func (p *parser) value(expected string) (Value, error) {
	if p.depth >= maxDepth {
		return nil, p.errAt(p.peek(), "values are nested too deeply")
	}
	p.depth++
	defer func() { p.depth-- }()
	t := p.peek()
	switch {
	case t.is("{"):
		return p.document()
	case t.is("["):
		return p.array()
	case t.is("-"):
		p.next()
		n := p.peek()
		if n.kind == tokenNumber || n.kind == tokenIdent && n.text == "Infinity" {
			v, err := p.value(expected)
			if err != nil {
				return nil, err
			}
			return negate(v), nil
		}
		return nil, p.unexpected(n, "a number after -")
	case t.is("("):
		return nil, p.errAt(t, notJavaScript+"functions are not supported")
	case t.kind == tokenString:
		p.next()
		return p.decodeString(t)
	case t.kind == tokenNumber:
		p.next()
		return p.number(t)
	case t.kind == tokenRegex:
		p.next()
		return p.regex(t)
	case t.kind == tokenIdent:
		return p.identValue()
	}
	return nil, p.unexpected(t, expected)
}

func negate(v Value) Value {
	switch n := v.(type) {
	case int32:
		return -n
	case int64:
		if n == -math.MinInt32 {
			return int32(math.MinInt32)
		}
		return -n
	case float64:
		return -n
	}
	return v
}

func (p *parser) document() (Value, error) {
	open := p.next()
	d := bson.D{}
	for {
		t := p.next()
		var key string
		switch {
		case t.is("}"):
			return p.extendedJSON(open, d)
		case t.kind == tokenIdent, t.kind == tokenNumber:
			key = t.text
		case t.kind == tokenString:
			s, err := p.decodeString(t)
			if err != nil {
				return nil, err
			}
			key = s
		default:
			return nil, p.unexpected(t, `a key or "}"`)
		}
		if _, err := p.expect(":", `":" after the key`); err != nil {
			return nil, err
		}
		v, err := p.value("a value")
		if err != nil {
			return nil, err
		}
		d = append(d, bson.E{Key: key, Value: v})
		switch t := p.next(); {
		case t.is(","):
		case t.is("}"):
			return p.extendedJSON(open, d)
		default:
			return nil, p.afterValue(t, `"," or "}"`)
		}
	}
}

func (p *parser) array() (Value, error) {
	p.next()
	a := bson.A{}
	for {
		if p.peek().is("]") {
			p.next()
			return a, nil
		}
		v, err := p.value(`a value or "]"`)
		if err != nil {
			return nil, err
		}
		a = append(a, v)
		switch t := p.next(); {
		case t.is(","):
		case t.is("]"):
			return a, nil
		default:
			return nil, p.afterValue(t, `"," or "]"`)
		}
	}
}

func (p *parser) number(t token) (Value, error) {
	s := strings.ReplaceAll(t.text, "_", "")
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		n, err := strconv.ParseInt(s[2:], 16, 64)
		if err != nil {
			return nil, p.errAt(t, "%s is not a number", t.text)
		}
		return integer(n), nil
	}
	if !strings.ContainsAny(s, ".eE") {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return integer(n), nil
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !isRangeError(err) {
		return nil, p.errAt(t, "%s is not a number", t.text)
	}
	return f, nil
}

func isRangeError(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}

// integer is an int32 when the number fits one, as mongosh sends it, and
// otherwise an int64, which keeps digits a double would lose.
func integer(n int64) Value {
	if n >= math.MinInt32 && n <= math.MaxInt32 {
		return int32(n)
	}
	return n
}

func (p *parser) regex(t token) (Value, error) {
	end := strings.LastIndexByte(t.text, '/')
	flags := []byte(t.text[end+1:])
	for _, f := range flags {
		if !strings.ContainsRune("imsxu", rune(f)) {
			return nil, p.errAt(t, "the regular expression flag %q is not supported; use i, m, s, x or u", string(f))
		}
	}
	return bson.Regex{Pattern: t.text[1:end], Options: sortedFlags(string(flags))}, nil
}

// sortedFlags puts a regular expression's options in alphabetical order,
// as BSON stores them.
func sortedFlags(s string) string {
	b := []byte(s)
	for i := 1; i < len(b); i++ {
		for j := i; j > 0 && b[j] < b[j-1]; j-- {
			b[j], b[j-1] = b[j-1], b[j]
		}
	}
	return string(b)
}

// identValue reads a value that starts with a word: a literal, a shell
// helper such as ObjectId(…), or new and a helper.
func (p *parser) identValue() (Value, error) {
	t := p.next()
	switch t.text {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	case "undefined":
		return bson.Undefined{}, nil
	case "Infinity":
		return math.Inf(1), nil
	case "NaN":
		return math.NaN(), nil
	case "MinKey", "MaxKey":
		if p.peek().is("(") {
			if a, err := p.args(); err != nil {
				return nil, err
			} else if len(a.values) > 0 {
				return nil, p.errAt(t, "%s takes no arguments", t.text)
			}
		}
		if t.text == "MinKey" {
			return bson.MinKey{}, nil
		}
		return bson.MaxKey{}, nil
	case "function":
		return nil, p.errAt(t, notJavaScript+"functions are not supported")
	case "new":
		name := p.next()
		if name.kind != tokenIdent || !p.peek().is("(") {
			return nil, p.unexpected(name, "a shell helper such as Date() after new")
		}
		return p.helper(name, true)
	}
	switch after := p.peek(); {
	case after.is("=>"):
		return nil, p.errAt(after, notJavaScript+"functions are not supported")
	case after.is("("):
		return p.helper(t, false)
	}
	return nil, p.errAt(t, notJavaScript+"%s is a variable, and variables are not supported", t.text)
}

// decodeString is the value of a string token, its escapes undone.
func (p *parser) decodeString(t token) (string, error) {
	if t.kind == tokenBad {
		return "", p.unexpected(t, "")
	}
	quote, body := t.text[0], t.text[1:len(t.text)-1]
	if quote == '`' && strings.Contains(body, "${") {
		return "", p.errAt(t, notJavaScript+"template expressions are not supported")
	}
	if !strings.Contains(body, `\`) {
		return body, nil
	}
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' || i+1 >= len(body) {
			b.WriteByte(c)
			continue
		}
		i++
		switch e := body[i]; e {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'v':
			b.WriteByte('\v')
		case '0':
			b.WriteByte(0)
		case '\n':
			// a line continuation adds nothing
		case 'x', 'u':
			r, n, ok := hexEscape(body[i:])
			if !ok {
				return "", p.errAt(t, `the escape \%c in the string is not valid`, e)
			}
			b.WriteRune(r)
			i += n - 1
		default:
			b.WriteByte(e)
		}
	}
	return b.String(), nil
}

// hexEscape reads \xHH, \uHHHH or \u{H…} from s, which starts at x or u:
// the rune and how many bytes it took.
func hexEscape(s string) (rune, int, bool) {
	digits, n := "", 0
	switch {
	case s[0] == 'x' && len(s) >= 3:
		digits, n = s[1:3], 3
	case strings.HasPrefix(s, "u{"):
		end := strings.IndexByte(s, '}')
		if end < 0 {
			return 0, 0, false
		}
		digits, n = s[2:end], end+1
	case s[0] == 'u' && len(s) >= 5:
		digits, n = s[1:5], 5
	default:
		return 0, 0, false
	}
	v, err := strconv.ParseUint(digits, 16, 32)
	if err != nil || v > utf8.MaxRune {
		return 0, 0, false
	}
	r := rune(v)
	// A surrogate pair, as JSON writes characters beyond the first plane.
	if r >= 0xD800 && r < 0xDC00 && len(s) >= n+6 && s[n] == '\\' && s[n+1] == 'u' {
		if lo, err := strconv.ParseUint(s[n+2:n+6], 16, 32); err == nil && lo >= 0xDC00 && lo < 0xE000 {
			return (r-0xD800)<<10 + rune(lo-0xDC00) + 0x10000, n + 6, true
		}
	}
	return r, n, true
}
