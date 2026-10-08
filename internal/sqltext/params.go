package sqltext

import "strings"

// ParamKind distinguishes the parameter syntaxes Params recognises.
type ParamKind int

const (
	ParamNamed ParamKind = iota // :name
	ParamVar                    // ${name}
	ParamTyped                  // {name:Type}, ClickHouse only
)

// Parameter is a parameter reference; Start and End are rune offsets within the statement.
type Parameter struct {
	Name       string
	Start, End int
	Kind       ParamKind
	Type       string // the type of a ParamTyped
	Sigil      rune   // ':', or '@' or '$' in SQLite, for a ParamNamed
}

// Params finds :name and ${name} outside strings, comments, quoted identifiers
// and dollar quotes; also {name:Type} in ClickHouse, and @name and $name in
// SQLite. A ':' inside [ ] is an array slice, and in MySQL and
// ClickHouse a ':' directly after a name (such as {id:UInt32}) is part of it.
func Params(stmt string, d Dialect) []Parameter {
	rs := []rune(stmt)
	toks := tokenizeRunes(rs, d)
	var out []Parameter
	brackets := 0
	skipUntil := 0
	for i, t := range toks {
		if t.Start < skipUntil {
			continue
		}
		switch {
		case isPunct(t, "["):
			brackets++
		case isPunct(t, "]"):
			brackets--
		case t.Text == "$" && t.End < len(rs) && rs[t.End] == '{':
			for j := t.End + 1; j < len(rs); j++ {
				if rs[j] == '}' {
					out = append(out, Parameter{Name: string(rs[t.End+1 : j]), Start: t.Start, End: j + 1, Kind: ParamVar})
					skipUntil = j + 1
					break
				}
			}
		case d == ClickHouse && isPunct(t, "{"):
			if p, ok := typedParam(toks[i+1:], rs, t.Start); ok {
				out = append(out, p)
				skipUntil = p.End
			}
		case d == SQLite && (t.Text == "@" || t.Text == "$") && i+1 < len(toks) &&
			(toks[i+1].Kind == Identifier || toks[i+1].Kind == Keyword) && !gluedToName(toks, i):
			n := toks[i+1]
			out = append(out, Parameter{Name: n.Text, Start: t.Start, End: n.End, Kind: ParamNamed, Sigil: rs[t.Start]})
			skipUntil = n.End
		case t.Kind == Param && rs[t.Start] == ':' && brackets <= 0:
			if (d == MySQL || d == ClickHouse) && i > 0 && toks[i-1].End == t.Start &&
				(toks[i-1].Kind == Identifier || toks[i-1].Kind == Keyword || toks[i-1].Kind == QuotedIdent) {
				continue
			}
			end := t.Start + 1
			for end < t.End && isASCIIName(rs[end], end > t.Start+1) {
				end++
			}
			if end > t.Start+1 {
				out = append(out, Parameter{Name: string(rs[t.Start+1 : end]), Start: t.Start, End: end, Kind: ParamNamed, Sigil: ':'})
			}
		}
	}
	return out
}

// typedParam reads name:Type} from the tokens after a ClickHouse '{' at start.
func typedParam(toks []Token, rs []rune, start int) (Parameter, bool) {
	k := 0
	skipSpace := func() {
		for k < len(toks) && toks[k].Kind == Whitespace {
			k++
		}
	}
	skipSpace()
	if k >= len(toks) || (toks[k].Kind != Identifier && toks[k].Kind != Keyword) {
		return Parameter{}, false
	}
	name := toks[k].Text
	k++
	skipSpace()
	if k >= len(toks) {
		return Parameter{}, false
	}
	var typeStart int
	switch t := toks[k]; {
	case t.Text == ":":
		k++
		skipSpace()
		if k >= len(toks) {
			return Parameter{}, false
		}
		typeStart = toks[k].Start
	case t.Kind == Param && rs[t.Start] == ':':
		// The lexer reads ":UInt32" as one :name token.
		typeStart = t.Start + 1
		k++
	default:
		return Parameter{}, false
	}
	depth := 0
	for ; k < len(toks); k++ {
		t := toks[k]
		switch {
		case isPunct(t, "("):
			depth++
		case isPunct(t, ")"):
			depth--
		case isPunct(t, "}") && depth == 0:
			typ := strings.TrimSpace(string(rs[typeStart:t.Start]))
			if typ == "" {
				return Parameter{}, false
			}
			return Parameter{Name: name, Type: typ, Start: start, End: t.End, Kind: ParamTyped}, true
		case isPunct(t, "{"), isPunct(t, ";"), t.Kind == Comment:
			return Parameter{}, false
		}
		if depth < 0 {
			return Parameter{}, false
		}
	}
	return Parameter{}, false
}

// gluedToName reports whether toks[i] starts right where a name ends.
func gluedToName(toks []Token, i int) bool {
	if i == 0 || toks[i-1].End != toks[i].Start {
		return false
	}
	switch toks[i-1].Kind {
	case Identifier, Keyword, QuotedIdent, Number:
		return true
	}
	return false
}

func isASCIIName(r rune, allowDigit bool) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (allowDigit && isDigit(r))
}
