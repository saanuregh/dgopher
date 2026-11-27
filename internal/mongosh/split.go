package mongosh

import "strings"

// Span is a statement's byte offsets [From, To) in a script.
type Span struct{ From, To int }

// Split finds the statements of a script. A statement ends at a ; or at
// a line break once it is complete: its brackets are closed, its last
// token could end it, and the next line does not continue its chain with
// a dot. Strings, regular expressions and comments hide what they hold.
// A span leaves out the ; and the comments around a statement.
func Split(script string) []Span {
	var spans []Span
	start, depth := -1, 0
	var last token
	flush := func() {
		if start >= 0 {
			spans = append(spans, Span{From: start, To: last.end})
			start = -1
		}
	}
	for _, t := range lex(script) {
		if depth == 0 && t.is(";") {
			flush()
			continue
		}
		if start >= 0 && depth == 0 && t.newline && endsStatement(last) && !t.is(".") {
			flush()
		}
		if start < 0 {
			start = t.start
		}
		switch {
		case t.is("("), t.is("["), t.is("{"):
			depth++
		case t.is(")"), t.is("]"), t.is("}"):
			depth = max(depth-1, 0)
		}
		last = t
	}
	flush()
	return spans
}

// endsStatement reports whether a statement may end with t: not with an
// operator or separator, which needs what follows.
func endsStatement(t token) bool {
	return t.kind != tokenPunct || !strings.Contains(".,:=+-*/%&|!?<>([{", t.text[:1])
}
