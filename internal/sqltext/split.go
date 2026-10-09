package sqltext

import "strings"

// Statement is one SQL statement; Text equals the source runes [Start, End).
type Statement struct {
	Text       string
	Start, End int
	// Delimiter is what ends it: ';', or on MySQL the DELIMITER in effect.
	Delimiter string
}

// DelimiterMode selects what ends a statement besides ';'.
type DelimiterMode int

const (
	// BlankLineAndSemicolon matches DBeaver's default "Always" blank-line delimiter.
	BlankLineAndSemicolon DelimiterMode = iota
	SemicolonOnly
)

type SplitOptions struct {
	Mode DelimiterMode
	// Delimiter is MySQL's DELIMITER in effect where the text starts, as
	// in a text read on from the middle of a file; "" for ';'.
	Delimiter string
}

// segment is the tokens of one statement, leading whitespace dropped and
// leading comments kept, so Format can preserve them.
type segment struct {
	toks       []Token
	terminated bool
	// directive marks a MySQL DELIMITER line; it is formatted but is not a statement.
	directive bool
	// delimited means the last token is the custom DELIMITER string that ended
	// the statement; it is formatted but is not part of the statement.
	delimited bool
	// bodyEnd is the index in toks of the END that closed the first routine
	// body, or -1.
	bodyEnd int
	// delim is the delimiter in effect for the segment: after it, for a
	// directive.
	delim string
}

func splitSegments(rs []rune, d Dialect, o SplitOptions) []segment {
	return splitTokens(rs, tokenizeRunes(rs, d), d, o, true)
}

// splitSegmentsWithoutBodies splits at every ';', routine bodies or not.
func splitSegmentsWithoutBodies(rs []rune, d Dialect, o SplitOptions) []segment {
	return splitTokens(rs, tokenizeRunes(rs, d), d, o, false)
}

// splitTokens splits toks, which run contiguously to the end of rs. With
// bodies, a ';' inside a routine body (CREATE PROCEDURE … BEGIN … END and
// the like) stays in its statement; a body still open at the end of the text
// is split again without bodies, so an unclosed BEGIN never merges statements.
func splitTokens(rs []rune, toks []Token, d Dialect, o SplitOptions, bodies bool) []segment {
	var out []segment
	var cur []Token
	delimiter := []rune(";")
	if d == MySQL && o.Delimiter != "" {
		delimiter = []rune(o.Delimiter)
	}
	// blocks holds the open blocks: 'B' for a body BEGIN, 'C' for CASE.
	var blocks []byte
	openBodies, parens, codeTokens := 0, 0, 0
	header := 0 // 0 unknown, 1 routine header, -1 not
	skipBlockWord := -1
	bodyEnd := -1
	var prev Token
	reset := func() {
		cur, blocks = nil, blocks[:0]
		openBodies, parens, codeTokens, header, skipBlockWord, bodyEnd = 0, 0, 0, 0, -1, -1
	}
	closeSegment := func(terminated bool) {
		out = append(out, segment{toks: cur, terminated: terminated, bodyEnd: bodyEnd, delim: string(delimiter)})
		reset()
	}
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if d == MySQL && codeTokens == 0 && isWord(t, "DELIMITER") && atLineStart(rs, t.Start) {
			if delim, lineEnd, ok := delimiterDirective(rs, t); ok && lineEndsToken(toks[i:], lineEnd) {
				line := cur
				for _, lt := range toks[i:] {
					if lt.End > lineEnd {
						break
					}
					line = append(line, lt)
				}
				out = append(out, segment{toks: line, directive: true, bodyEnd: -1, delim: string(delim)})
				reset()
				delimiter = delim
				toks, i = resumeAt(rs, toks[i:], lineEnd, d), -1
				continue
			}
		}
		if string(delimiter) != ";" {
			if q := findDelimiter(rs, toks, i, delimiter); q >= 0 {
				if q > t.Start {
					// Not lexed again: Format must write "END$$" back unchanged.
					cur = append(cur, Token{Kind: t.Kind, Start: t.Start, End: q, Text: string(rs[t.Start:q])})
				}
				end := q + len(delimiter)
				cur = append(cur, Token{Kind: Punct, Start: q, End: end, Text: string(delimiter)})
				out = append(out, segment{toks: cur, delimited: true, bodyEnd: -1, delim: string(delimiter)})
				reset()
				toks, i = resumeAt(rs, toks[i:], end, d), -1
				continue
			}
			if t.Kind != Whitespace || len(cur) > 0 {
				cur = append(cur, t)
			}
			if significant(t) {
				codeTokens++
			}
			continue
		}
		if isPunct(t, ";") && openBodies == 0 {
			closeSegment(true)
			continue
		}
		if t.Kind == Whitespace {
			if o.Mode == BlankLineAndSemicolon && parens <= 0 && len(blocks) == 0 && len(cur) > 0 && lineBreaks(t.Text) >= 2 {
				closeSegment(false)
				continue
			}
			if len(cur) > 0 {
				cur = append(cur, t)
			}
			continue
		}
		cur = append(cur, t)
		if t.Kind == Comment {
			continue
		}
		codeTokens++
		before := prev
		prev = t
		switch {
		case isPunct(t, "("):
			parens++
		case isPunct(t, ")"):
			parens--
		case i == skipBlockWord:
		case t.Kind == Keyword || t.Kind == Identifier:
			switch strings.ToUpper(t.Text) {
			case "BEGIN":
				// A statement-initial BEGIN starts a transaction, not a body.
				if !bodies || codeTokens <= 1 || parens > 0 {
					break
				}
				if header == 0 {
					header = -1
					if routineHeader(cur) {
						header = 1
					}
				}
				// A MySQL label is valid only in a stored program, where
				// the header already holds; a statement starting "name: BEGIN"
				// is the only other place it may open a body.
				label := d == MySQL && before.Kind == Operator && before.Text == ":" && codeTokens == 3
				if (header == 1 || openBodies > 0 || label) && opensBody(before, toks, i) {
					blocks = append(blocks, 'B')
					openBodies++
				}
			case "CASE":
				blocks = append(blocks, 'C')
			case "END":
				next, word := nextSignificant(toks, i)
				switch word {
				case "IF", "LOOP", "WHILE", "REPEAT", "FOR":
					skipBlockWord = next
				case "CASE":
					skipBlockWord = next
					if n := len(blocks); n > 0 && blocks[n-1] == 'C' {
						blocks = blocks[:n-1]
					}
				default:
					n := len(blocks)
					switch {
					case n == 0:
					case blocks[n-1] == 'C':
						blocks = blocks[:n-1]
					case closesBody(before, toks, next):
						blocks = blocks[:n-1]
						openBodies--
						if openBodies == 0 && bodyEnd < 0 {
							bodyEnd = len(cur) - 1
						}
					}
				}
			}
		}
	}
	if len(cur) > 0 {
		if openBodies > 0 {
			again := o
			again.Delimiter = string(delimiter)
			out = append(out, splitTokens(rs, cur, d, again, false)...)
		} else {
			closeSegment(false)
		}
	}
	return out
}

// opensBody reports whether the BEGIN at toks[i], after the significant
// token before, can open a routine body rather than be a name: a name
// follows FUNCTION, ON, AS, '.', ',' and the like, or comes before '(' or
// '.', while a body starts with a statement, label or ATOMIC word.
func opensBody(before Token, toks []Token, i int) bool {
	switch {
	case before.Kind == Operator:
		// Only a MySQL label "name: BEGIN" puts an operator before a body.
		if before.Text != ":" {
			return false
		}
	case before.Kind == Punct:
		if before.Text != ")" && before.Text != ";" {
			return false
		}
	}
	switch word(before) {
	case "FUNCTION", "PROCEDURE", "TRIGGER", "EVENT", "ON", "AS", "OF", "RETURNS", "SETOF",
		"WHEN", "AND", "OR", "NOT", "IS", "IN", "LIKE", "BY", "FROM", "SELECT", "WHERE":
		return false
	}
	next, _ := nextSignificant(toks, i)
	if next < 0 {
		return false
	}
	switch toks[next].Kind {
	case Keyword, Identifier, QuotedIdent:
		return true
	}
	return false
}

// closesBody reports whether an END, after the significant token before
// and with toks[next] after it, may close a routine body rather than be a
// name, as in "AS end", "r.end" or "end.x".
func closesBody(before Token, toks []Token, next int) bool {
	if next >= 0 && isPunct(toks[next], ".") {
		return false
	}
	return !isPunct(before, ".") && !isWord(before, "AS")
}

// bodyCloseFollowsStatement reports whether the END at toks[end] follows a
// ';', or BEGIN or ATOMIC in an empty body: every statement in a routine
// body ends with ';', so any other END that closed a body was a name.
func bodyCloseFollowsStatement(toks []Token, end int) bool {
	for j := end - 1; j >= 0; j-- {
		if significant(toks[j]) {
			return isPunct(toks[j], ";") || isWord(toks[j], "BEGIN") || isWord(toks[j], "ATOMIC")
		}
	}
	return false
}

// nextSignificant returns the index and upper-cased text of the first
// significant token after toks[idx], or -1 and "".
func nextSignificant(toks []Token, idx int) (int, string) {
	for j := idx + 1; j < len(toks); j++ {
		if significant(toks[j]) {
			return j, strings.ToUpper(toks[j].Text)
		}
	}
	return -1, ""
}

func isWord(t Token, w string) bool {
	return (t.Kind == Keyword || t.Kind == Identifier) && strings.EqualFold(t.Text, w)
}

// lineBreaks counts line breaks in s, each "\n", "\r\n" or lone "\r".
func lineBreaks(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\n':
			n++
		case '\r':
			n++
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
		}
	}
	return n
}

// routineHeader reports whether toks start with CREATE [OR REPLACE]
// [DEFINER = value] [AGGREGATE | TEMP | TEMPORARY] PROCEDURE | FUNCTION |
// TRIGGER | EVENT, whose BEGIN opens a body.
func routineHeader(toks []Token) bool {
	var sig []Token
	for _, t := range toks {
		if significant(t) {
			sig = append(sig, t)
		}
	}
	i := 0
	word := func(w string) bool {
		if i < len(sig) && isWord(sig[i], w) {
			i++
			return true
		}
		return false
	}
	if !word("CREATE") {
		return false
	}
	if word("OR") && !word("REPLACE") {
		return false
	}
	if word("DEFINER") {
		if i+1 >= len(sig) || sig[i].Text != "=" {
			return false
		}
		// The value ('u'@'h', `u`@`h`, CURRENT_USER()) is written without spaces.
		i += 2
		for i < len(sig) && sig[i].Start == sig[i-1].End {
			i++
		}
	}
	_ = word("AGGREGATE") || word("TEMP") || word("TEMPORARY")
	return word("PROCEDURE") || word("FUNCTION") || word("TRIGGER") || word("EVENT")
}

// atLineStart reports whether only spaces and tabs precede rs[at] on its line.
func atLineStart(rs []rune, at int) bool {
	i := at - 1
	for i >= 0 && (rs[i] == ' ' || rs[i] == '\t' || rs[i] == '\f' || rs[i] == '\v') {
		i--
	}
	return i < 0 || rs[i] == '\n' || rs[i] == '\r'
}

// delimiterDirective reads a MySQL client "DELIMITER x" line starting at the
// word t: the rest of the line, trimmed, is the new delimiter. lineEnd is
// the start of the line break.
func delimiterDirective(rs []rune, t Token) (delim []rune, lineEnd int, ok bool) {
	lineEnd = t.End
	for lineEnd < len(rs) && rs[lineEnd] != '\n' && rs[lineEnd] != '\r' {
		lineEnd++
	}
	from, to := t.End, lineEnd
	for from < to && isSpace(rs[from]) {
		from++
	}
	// The mysql client takes the first word: "DELIMITER // note" sets //.
	for to = from; to < lineEnd && !isSpace(rs[to]); to++ {
	}
	if from == to {
		return nil, 0, false
	}
	return rs[from:to], lineEnd, true
}

// lineEndsToken reports whether no string, comment or quoted name in toks
// runs past the line end p, so the DELIMITER line is whole on its own.
func lineEndsToken(toks []Token, p int) bool {
	for _, t := range toks {
		if t.End > p {
			return t.Start >= p || t.Kind == Whitespace
		}
	}
	return true
}

// findDelimiter returns the first offset inside toks[i] where delim starts
// and ends at a token boundary, or -1. It may start inside a word, as in
// "END$$". Strings, comments and quoted names never hold a delimiter.
func findDelimiter(rs []rune, toks []Token, i int, delim []rune) int {
	t := toks[i]
	switch t.Kind {
	case String, Comment, QuotedIdent, Whitespace:
		return -1
	}
	for q := t.Start; q < t.End; q++ {
		end := q + len(delim)
		if end > len(rs) || string(rs[q:end]) != string(delim) {
			continue
		}
		if end == len(rs) {
			return q
		}
		for k := i; k < len(toks) && toks[k].Start < end; k++ {
			if toks[k].End == end {
				return q
			}
		}
	}
	return -1
}

// tokenizeAt tokenizes rs[from:to] with offsets into rs.
func tokenizeAt(rs []rune, from, to int, d Dialect) []Token {
	toks := tokenizeRunes(rs[from:to], d)
	for i := range toks {
		toks[i].Start += from
		toks[i].End += from
	}
	return toks
}

// resumeAt returns the tokens of rs from offset p to the end, reusing toks
// (which run to the end of rs) when p is a token boundary or inside
// whitespace, and lexing again otherwise. It may overwrite one element of
// toks, which the caller no longer reads.
func resumeAt(rs []rune, toks []Token, p int, d Dialect) []Token {
	for k, t := range toks {
		if t.End <= p {
			continue
		}
		switch {
		case t.Start == p:
			return toks[k:]
		case t.Kind == Whitespace:
			toks[k] = Token{Kind: Whitespace, Start: p, End: t.End, Text: string(rs[p:t.End])}
			return toks[k:]
		}
		break
	}
	if p >= len(rs) {
		return nil
	}
	return tokenizeAt(rs, p, len(rs), d)
}

// Split is SplitWith in BlankLineAndSemicolon mode.
func Split(src string, d Dialect) []Statement {
	return SplitWith(src, d, SplitOptions{Mode: BlankLineAndSemicolon})
}

// SplitWith splits on ';' outside strings, comments, quoted identifiers and
// dollar quotes, and in BlankLineAndSemicolon mode also at a line holding only
// whitespace, except inside parentheses and BEGIN … END (or CASE … END)
// blocks. A ';' inside the BEGIN … END body of CREATE PROCEDURE, FUNCTION,
// TRIGGER or EVENT does not split, unless the body is never closed. For MySQL,
// a "DELIMITER x" line sets what ends a statement and is not a statement
// itself. Text and Start exclude leading comments; End is the end of the last
// non-whitespace token, before any ';' or delimiter. Empty and comment-only
// statements are dropped, but those of MySQL's executable comments.
func SplitWith(src string, d Dialect, o SplitOptions) []Statement {
	rs := []rune(src)
	var out []Statement
	for _, seg := range splitSegments(rs, d, o) {
		if seg.directive {
			continue
		}
		toks := seg.toks
		if seg.delimited {
			toks = toks[:len(toks)-1]
		}
		start := -1
		for _, t := range toks {
			if t.Kind != Comment && t.Kind != Whitespace || executableComment(t, d) {
				start = t.Start
				break
			}
		}
		if start < 0 {
			continue
		}
		end := start
		for _, t := range toks {
			if t.Kind != Whitespace {
				end = t.End
			}
		}
		out = append(out, Statement{Text: string(rs[start:end]), Start: start, End: end, Delimiter: seg.delim})
	}
	return out
}

// executableComment reports whether a token is MySQL's /*! … */, which
// MySQL runs as the statement it holds, as mysqldump writes triggers and
// SET statements: a statement of such comments is not empty.
func executableComment(t Token, d Dialect) bool {
	return d == MySQL && t.Kind == Comment && strings.HasPrefix(t.Text, "/*!")
}

// StatementAt is StatementAtWith in BlankLineAndSemicolon mode.
func StatementAt(src string, caret int, d Dialect) (Statement, bool) {
	return StatementAtWith(src, caret, d, SplitOptions{Mode: BlankLineAndSemicolon})
}

// StatementAtWith returns the statement containing caret. Between statements
// it returns the previous statement when no whole blank line lies between its
// end and the caret's line, else the next statement, else the previous one.
func StatementAtWith(src string, caret int, d Dialect, o SplitOptions) (Statement, bool) {
	stmts := SplitWith(src, d, o)
	rs := []rune(src)
	var prev, next *Statement
	for i := range stmts {
		s := &stmts[i]
		if caret >= s.Start && caret <= s.End {
			return *s, true
		}
		if s.End < caret {
			prev = s
		} else if next == nil && s.Start > caret {
			next = s
		}
	}
	if prev != nil && (next == nil || !blankLineBetween(rs, prev.End, caret)) {
		return *prev, true
	}
	if next != nil {
		return *next, true
	}
	return Statement{}, false
}

// blankLineBetween reports whether a whole whitespace-only line lies between
// two newlines inside rs[from:to).
func blankLineBetween(rs []rune, from, to int) bool {
	if to > len(rs) {
		to = len(rs)
	}
	lastNewline, blank := -1, false
	for i := from; i < to; i++ {
		switch r := rs[i]; {
		case r == '\n':
			if lastNewline >= 0 && blank {
				return true
			}
			lastNewline, blank = i, true
		case r != ' ' && r != '\t' && r != '\r':
			blank = false
		}
	}
	return false
}
