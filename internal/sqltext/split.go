package sqltext

import "strings"

// Statement is one SQL statement; Text equals the source bytes [Start, End).
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

func splitSegments(s string, d Dialect, q quoting, o SplitOptions) []segment {
	return splitTokens(s, tokenizeQuoted(s, d, q), d, q, o, true)
}

// splitSegmentsWithoutBodies splits at every ';', routine bodies or not.
func splitSegmentsWithoutBodies(s string, d Dialect, q quoting, o SplitOptions) []segment {
	return splitTokens(s, tokenizeQuoted(s, d, q), d, q, o, false)
}

// splitTokens splits toks, which run contiguously to the end of s and
// were lexed with q, as any text it lexes again is. With bodies, a ';'
// inside a routine body (CREATE PROCEDURE … BEGIN … END and the like) stays
// in its statement; a body still open at the end of the text is split again
// without bodies, so an unclosed BEGIN never merges statements. In T-SQL
// and Oracle a separator line (separatorLine) ends a statement and is no
// statement itself; in T-SQL ';' ends nothing, and in Oracle it ends
// nothing inside a PL/SQL unit, whose blocks are read as blockState's
// steps say.
func splitTokens(s string, toks []Token, d Dialect, q quoting, o SplitOptions, bodies bool) []segment {
	var out []segment
	var cur []Token
	delimiter := ";"
	if d == MySQL && o.Delimiter != "" {
		delimiter = o.Delimiter
	}
	var st blockState
	codeTokens := 0
	header := 0 // 0 unknown, 1 routine header, -1 not
	skipBlockWord := -1
	bodyEnd := -1
	var prev Token
	tsql, oracle := d == TSQL, d == Oracle
	reset := func() {
		cur = nil
		st.reset()
		codeTokens, header, skipBlockWord, bodyEnd = 0, 0, -1, -1
	}
	closeSegment := func(terminated bool) {
		out = append(out, segment{toks: cur, terminated: terminated, bodyEnd: bodyEnd, delim: delimiter})
		reset()
	}
	// flush ends the statement at the end of the text or at a separator
	// line.
	flush := func() {
		if st.openBodies == 0 {
			closeSegment(false)
			return
		}
		again := o
		again.Delimiter = delimiter
		open := cur
		reset()
		out = append(out, splitTokens(s, open, d, q, again, false)...)
	}
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if tsql || oracle {
			if next, count, ok := separatorLine(s, toks, i, d); ok {
				if len(cur) > 0 {
					flush()
				}
				// A GO with a count stays a statement, which Classify
				// reports, rather than run its batch once silently.
				out = append(out, segment{toks: toks[i:next], directive: count == "", bodyEnd: -1, delim: delimiter})
				i = next - 1
				continue
			}
		}
		if d == MySQL && codeTokens == 0 && isWord(t, "DELIMITER") && atLineStart(s, t.Start) {
			if delim, lineEnd, ok := delimiterDirective(s, t); ok && lineEndsToken(toks[i:], lineEnd) {
				line := cur
				for _, lt := range toks[i:] {
					if lt.End > lineEnd {
						break
					}
					line = append(line, lt)
				}
				out = append(out, segment{toks: line, directive: true, bodyEnd: -1, delim: delim})
				reset()
				delimiter = delim
				toks, i = resumeAt(s, toks[i:], lineEnd, d, q), -1
				continue
			}
		}
		if delimiter != ";" {
			if at := findDelimiter(s, toks, i, delimiter); at >= 0 {
				if at > t.Start {
					// Not lexed again: Format must write "END$$" back unchanged.
					cur = append(cur, Token{Kind: t.Kind, Start: t.Start, End: at, Text: s[t.Start:at]})
				}
				end := at + len(delimiter)
				cur = append(cur, Token{Kind: Punct, Start: at, End: end, Text: s[at:end]})
				out = append(out, segment{toks: cur, delimited: true, bodyEnd: -1, delim: delimiter})
				reset()
				toks, i = resumeAt(s, toks[i:], end, d, q), -1
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
		// A T-SQL batch is sent whole, as SSMS sends it: ';' ends no
		// statement there, so a variable stays in scope after one.
		if isPunct(t, ";") && st.openBodies == 0 && !tsql && !st.unit {
			closeSegment(true)
			continue
		}
		if t.Kind == Whitespace {
			if o.Mode == BlankLineAndSemicolon && st.parens <= 0 && len(st.blocks) == 0 && len(cur) > 0 && lineBreaks(t.Text) >= 2 && !st.keepsTogether(toks, i, cur, d) {
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
		switch {
		case tsql:
			st.tsqlStep(toks, i, cur, bodies)
			continue
		case oracle:
			st.oracleStep(toks, i, codeTokens == 1, bodies)
			continue
		}
		before := prev
		prev = t
		switch {
		case isPunct(t, "("):
			st.parens++
		case isPunct(t, ")"):
			st.parens--
		case i == skipBlockWord:
		case t.Kind == Keyword || t.Kind == Identifier:
			switch strings.ToUpper(t.Text) {
			case "BEGIN":
				// A statement-initial BEGIN starts a transaction, not a body.
				if !bodies || codeTokens <= 1 || st.parens > 0 {
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
				if (header == 1 || st.openBodies > 0 || label) && opensBody(before, toks, i) {
					st.blocks = append(st.blocks, 'B')
					st.openBodies++
				}
			case "CASE":
				st.blocks = append(st.blocks, 'C')
			case "END":
				next, word := nextSignificant(toks, i)
				switch word {
				case "IF", "LOOP", "WHILE", "REPEAT", "FOR":
					skipBlockWord = next
				case "CASE":
					skipBlockWord = next
					if n := len(st.blocks); n > 0 && st.blocks[n-1] == 'C' {
						st.blocks = st.blocks[:n-1]
					}
				default:
					n := len(st.blocks)
					switch {
					case n == 0:
					case st.blocks[n-1] == 'C':
						st.blocks = st.blocks[:n-1]
					case closesBody(before, toks, next):
						st.blocks = st.blocks[:n-1]
						st.openBodies--
						if st.openBodies == 0 && bodyEnd < 0 {
							bodyEnd = len(cur) - 1
						}
					}
				}
			}
		}
	}
	if len(cur) > 0 {
		flush()
	}
	return out
}

// blockState is what splitTokens knows of the blocks open in the statement
// it reads. T-SQL's and Oracle's steps (tsqlStep, oracleStep) keep it for
// theirs; other dialects keep its parens, blocks and bodies themselves.
type blockState struct {
	parens int
	// blocks holds the open blocks: 'B' for a body BEGIN, 'C' for CASE.
	blocks     []byte
	openBodies int
	// routine is 1 when a T-SQL statement creates or alters a procedure,
	// function or trigger, whose body runs to the end of its batch, -1
	// when not, 0 before it is known; control marks an IF, WHILE or ELSE
	// still waiting for the statement it runs.
	routine int
	control bool
	// unit marks an Oracle PL/SQL unit (oracleUnitStart), which runs to
	// its separator line or the end of the text.
	unit bool
}

func (st *blockState) reset() {
	*st = blockState{blocks: st.blocks[:0]}
}

// keepsTogether reports whether the blank line at toks[i] is inside a
// statement of d, whose tokens so far are cur, besides its parentheses
// and blocks: a T-SQL routine's body, an IF, WHILE or ELSE before its
// statement, or before an ELSE; an Oracle PL/SQL unit.
func (st *blockState) keepsTogether(toks []Token, i int, cur []Token, d Dialect) bool {
	switch d {
	case Oracle:
		return st.unit
	case TSQL:
	default:
		return false
	}
	if st.routine == 0 {
		st.routine = -1
		if tsqlRoutineHeader(cur) {
			st.routine = 1
		}
	}
	_, next := nextSignificant(toks, i)
	return st.routine == 1 || st.control || next == "ELSE"
}

// tsqlBeginsStatement are the words after a T-SQL BEGIN that make it a
// statement of its own rather than open a block.
var tsqlBeginsStatement = setOf("TRAN TRANSACTION DISTRIBUTED DIALOG CONVERSATION")

// tsqlStep follows T-SQL's blocks through the code token toks[i], the
// last of cur: BEGIN … END (with TRY or CATCH or not), unless BEGIN starts
// a transaction or dialog; CASE … END; and whether an IF, WHILE or ELSE
// waits for its statement. BEGIN and END are reserved, so never names.
// Without bodies, BEGIN opens nothing.
func (st *blockState) tsqlStep(toks []Token, i int, cur []Token, bodies bool) {
	t := toks[i]
	w := word(t)
	_, next := nextSignificant(toks, i)
	inCase := len(st.blocks) > 0 && st.blocks[len(st.blocks)-1] == 'C'
	switch {
	case isPunct(t, "("):
		st.parens++
	case isPunct(t, ")"):
		st.parens--
	case st.parens > 0 || w == "":
	case w == "CASE":
		st.blocks = append(st.blocks, 'C')
	case w == "END":
		if next != "CONVERSATION" && len(st.blocks) > 0 {
			if !inCase {
				st.openBodies--
			}
			st.blocks = st.blocks[:len(st.blocks)-1]
		}
	case inCase:
	case w == "BEGIN":
		st.control = false
		if bodies && !tsqlBeginsStatement[next] {
			st.blocks = append(st.blocks, 'B')
			st.openBodies++
		}
	case w == "WHILE", w == "ELSE", w == "IF" && !(next == "EXISTS" && dropsBefore(cur[:len(cur)-1])):
		st.control = true
	case tsqlStatementWords[w]:
		st.control = false
	}
}

// oracleStep follows an Oracle statement through the code token toks[i],
// its first when first: a PL/SQL unit, from its start or from a
// subquery's WITH that declares PL/SQL, which ';' does not end and whose
// blocks need no reading, as its separator line or the end of the text
// ends it; otherwise parentheses and CASE … END, which keep a blank line.
// Without bodies, no unit starts, so each ';' ends a statement.
func (st *blockState) oracleStep(toks []Token, i int, first, bodies bool) {
	t := toks[i]
	if bodies && (first && oracleUnitStart(toks, i) || !first && declaresPLSQL(toks, i)) {
		st.unit = true
	}
	switch {
	case st.unit:
	case isPunct(t, "("):
		st.parens++
	case isPunct(t, ")"):
		st.parens--
	case isWord(t, "CASE"):
		st.blocks = append(st.blocks, 'C')
	case isWord(t, "END") && len(st.blocks) > 0:
		st.blocks = st.blocks[:len(st.blocks)-1]
	}
}

// dropsBefore reports whether one of the last three code tokens of toks
// is DROP, as before the IF of "DROP TABLE IF EXISTS".
func dropsBefore(toks []Token) bool {
	seen := 0
	for j := len(toks) - 1; j >= 0 && seen < 3; j-- {
		if !significant(toks[j]) {
			continue
		}
		if word(toks[j]) == "DROP" {
			return true
		}
		seen++
	}
	return false
}

// tsqlRoutineHeader reports whether toks start with CREATE [OR ALTER] or
// ALTER, then PROC, PROCEDURE, FUNCTION or TRIGGER, whose body runs to the
// end of its batch.
func tsqlRoutineHeader(toks []Token) bool {
	var words []string
	for _, t := range toks {
		if significant(t) {
			if words = append(words, word(t)); len(words) == 4 {
				break
			}
		}
	}
	i := 1
	switch {
	case len(words) > 3 && words[0] == "CREATE" && words[1] == "OR" && words[2] == "ALTER":
		i = 3
	case len(words) > 1 && (words[0] == "CREATE" || words[0] == "ALTER"):
	default:
		return false
	}
	switch words[i] {
	case "PROC", "PROCEDURE", "FUNCTION", "TRIGGER":
		return true
	}
	return false
}

// separatorLine reads a line that ends a statement and is no statement
// itself, starting at the code token toks[i]: T-SQL's batch separator GO,
// as sqlcmd and SSMS read it, which may give a count, or Oracle's "/", as
// SQL*Plus and SQL Developer read it, which may not; alone on its line but
// for whitespace and comments that end on it. next is the index of the
// first token after the line; count is "" for none or 1.
func separatorLine(s string, toks []Token, i int, d Dialect) (next int, count string, ok bool) {
	t := toks[i]
	counted := false
	switch {
	case d == TSQL && isWord(t, "GO"):
		counted = true
	case d == Oracle && t.Kind == Operator && t.Text == "/":
	default:
		return 0, "", false
	}
	if !atLineStart(s, t.Start) {
		return 0, "", false
	}
	// An Oracle line comment runs past a carriage return, to a line feed.
	lineEnd := t.End
	for lineEnd < len(s) && s[lineEnd] != '\n' && (s[lineEnd] != '\r' || d == Oracle) {
		lineEnd++
	}
	k := i + 1
	for ; k < len(toks) && toks[k].Start < lineEnd; k++ {
		switch t := toks[k]; {
		case t.Kind == Whitespace:
		case t.Kind == Comment && t.End <= lineEnd:
		case counted && t.Kind == Number && count == "" && strings.Trim(t.Text, "0123456789") == "":
			count = t.Text
		default:
			return 0, "", false
		}
	}
	if strings.TrimLeft(count, "0") == "1" {
		count = ""
	}
	return k, count, true
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

// atLineStart reports whether only spaces and tabs precede s[at] on its line.
func atLineStart(s string, at int) bool {
	i := at - 1
	for i >= 0 && (s[i] == ' ' || s[i] == '\t' || s[i] == '\f' || s[i] == '\v') {
		i--
	}
	return i < 0 || s[i] == '\n' || s[i] == '\r'
}

// delimiterDirective reads a MySQL client "DELIMITER x" line starting at the
// word t: the rest of the line, trimmed, is the new delimiter. lineEnd is
// the start of the line break.
func delimiterDirective(s string, t Token) (delim string, lineEnd int, ok bool) {
	lineEnd = t.End
	for lineEnd < len(s) && s[lineEnd] != '\n' && s[lineEnd] != '\r' {
		lineEnd++
	}
	from, to := t.End, lineEnd
	for from < to && isSpace(s[from]) {
		from++
	}
	// The mysql client takes the first word: "DELIMITER // note" sets //.
	for to = from; to < lineEnd && !isSpace(s[to]); to++ {
	}
	if from == to {
		return "", 0, false
	}
	return s[from:to], lineEnd, true
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
func findDelimiter(s string, toks []Token, i int, delim string) int {
	t := toks[i]
	switch t.Kind {
	case String, Comment, QuotedIdent, Whitespace:
		return -1
	}
	for q := t.Start; q < t.End; q++ {
		end := q + len(delim)
		if end > len(s) || s[q:end] != delim {
			continue
		}
		if end == len(s) {
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

// tokenizeAt tokenizes s[from:to] with offsets into s.
func tokenizeAt(s string, from, to int, d Dialect, q quoting) []Token {
	toks := tokenizeQuoted(s[from:to], d, q)
	for i := range toks {
		toks[i].Start += from
		toks[i].End += from
	}
	return toks
}

// resumeAt returns the tokens of s from offset p to the end, reusing toks
// (which run to the end of s) when p is a token boundary or inside
// whitespace, and lexing again otherwise. It may overwrite one element of
// toks, which the caller no longer reads.
func resumeAt(s string, toks []Token, p int, d Dialect, q quoting) []Token {
	for k, t := range toks {
		if t.End <= p {
			continue
		}
		switch {
		case t.Start == p:
			return toks[k:]
		case t.Kind == Whitespace:
			toks[k] = Token{Kind: Whitespace, Start: p, End: t.End, Text: s[p:t.End]}
			return toks[k:]
		}
		break
	}
	if p >= len(s) {
		return nil
	}
	return tokenizeAt(s, p, len(s), d, q)
}

// SplitWith splits on ';' outside strings, comments, quoted identifiers and
// dollar quotes, and in BlankLineAndSemicolon mode also at a line holding only
// whitespace, except inside parentheses and BEGIN … END (or CASE … END)
// blocks. A ';' inside the BEGIN … END body of CREATE PROCEDURE, FUNCTION,
// TRIGGER or EVENT does not split, unless the body is never closed. For MySQL,
// a "DELIMITER x" line sets what ends a statement and is not a statement
// itself. For T-SQL, ';' splits nothing, and a GO line ends a batch and is
// not a statement, but for "GO n", which is; at a blank line, every
// BEGIN … END block keeps its statements, a procedure, function or trigger
// runs to the end of its batch, and an IF, WHILE or ELSE keeps the
// statement it runs and an ELSE after it. For Oracle, a "/" line ends a
// statement and is not one, and a PL/SQL unit (an anonymous block, or
// CREATE of a procedure, function, package, trigger, type, library, Java
// source or MLE module, or a WITH FUNCTION query) runs to its "/" line or
// the end of the text, ';' and blank lines splitting nothing in it. Text
// and Start exclude leading comments; End is the end of the last
// non-whitespace token, before any ';' or delimiter. Empty and comment-only
// statements are dropped, but those of MySQL's executable comments.
func SplitWith(src string, d Dialect, o SplitOptions) []Statement {
	var out []Statement
	for _, seg := range splitSegments(src, d, defaultQuoting(d), o) {
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
		out = append(out, Statement{Text: src[start:end], Start: start, End: end, Delimiter: seg.delim})
	}
	return out
}

// executableComment reports whether a token is MySQL's /*! … */,
// MariaDB's /*M! … */ or TiDB's /*T! … */, which the server runs as the
// statement it holds, as mysqldump writes triggers and SET statements: a
// statement of such comments is not empty.
func executableComment(t Token, d Dialect) bool {
	return d == MySQL && t.Kind == Comment && (strings.HasPrefix(t.Text, "/*!") || strings.HasPrefix(t.Text, "/*M!") || strings.HasPrefix(t.Text, "/*T!"))
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
	if prev != nil && (next == nil || !blankLineBetween(src, prev.End, caret)) {
		return *prev, true
	}
	if next != nil {
		return *next, true
	}
	return Statement{}, false
}

// blankLineBetween reports whether a whole whitespace-only line lies between
// two newlines inside s[from:to).
func blankLineBetween(s string, from, to int) bool {
	if to > len(s) {
		to = len(s)
	}
	lastNewline, blank := -1, false
	for i := from; i < to; i++ {
		switch r := s[i]; {
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
