package editor

import (
	"sort"
	"time"

	"dgopher/internal/sqltext"

	code "github.com/egoist/mygo/ui/editor"
)

// maxHighlight is the size, in bytes, of the longest text colored: a
// longer one is lexed whole at each edit too slowly to color as it is
// typed.
const maxHighlight = 2_000_000

// sqlLanguage is the language of a SQL editor's buffer: highlighting and
// folding in a dialect, statements ending as o says.
func sqlLanguage(d sqltext.Dialect, o sqltext.SplitOptions) *code.Language {
	return &code.Language{
		Name:         "SQL",
		LineComment:  "--",
		BlockComment: [2]string{"/*", "*/"},
		Brackets:     []code.BracketPair{{Open: "(", Close: ")"}, {Open: "[", Close: "]"}, {Open: "{", Close: "}"}},
		AutoClose: []code.BracketPair{
			{Open: "(", Close: ")"}, {Open: "[", Close: "]"}, {Open: "{", Close: "}"},
			{Open: `"`, Close: `"`, NotIn: []code.TokenKind{code.TokenString, code.TokenComment}},
			{Open: "'", Close: "'", NotIn: []code.TokenKind{code.TokenString, code.TokenComment}},
		},
		TabSize: 2,
		Highlighter: func(text code.Snapshot, _ func()) code.Highlighter {
			return newSQLHighlighter(text, d, o)
		},
	}
}

// sqlHighlighter colors a SQL text as sqltext lexes it, the whole text at
// once for each version, and folds its statements, groups in parentheses
// and block comments.
type sqlHighlighter struct {
	dialect sqltext.Dialect
	split   sqltext.SplitOptions
	text    code.Snapshot
	// src is the text of version srcOf, and tokens its tokens, nil for a
	// text too long to color.
	src         string
	srcOf       uint64
	hasSrc      bool
	tokens      []sqltext.Token
	tokensOf    uint64
	hasTokens   bool
	foldEnds    map[int]int // the last line each line's fold hides
	foldsOf     uint64
	hasFoldEnds bool
}

func newSQLHighlighter(text code.Snapshot, d sqltext.Dialect, o sqltext.SplitOptions) *sqlHighlighter {
	return &sqlHighlighter{dialect: d, split: o, text: text}
}

func (h *sqlHighlighter) Edit(_, new code.Snapshot, _ []code.Change) { h.text = new }

func (h *sqlHighlighter) Highlight(int, time.Duration) bool {
	h.lex()
	return true
}

func (h *sqlHighlighter) Tokens(i int, line string, dst []code.Token) ([]code.Token, bool) {
	h.lex()
	if h.tokens == nil {
		return dst, true
	}
	start := h.text.LineStart(i)
	end := start + len(line)
	at := start
	j := sort.Search(len(h.tokens), func(k int) bool { return h.tokens[k].End > start })
	for ; j < len(h.tokens) && h.tokens[j].Start < end; j++ {
		t := h.tokens[j]
		if t.Start > at {
			dst = append(dst, code.Token{End: t.Start - start, Kind: code.TokenText})
		}
		to := min(t.End, end)
		dst = append(dst, code.Token{End: to - start, Kind: tokenKind(t.Kind)})
		at = to
	}
	if at < end {
		dst = append(dst, code.Token{End: end - start, Kind: code.TokenText})
	}
	return dst, true
}

// source is the text as it is, read once for each version.
func (h *sqlHighlighter) source() string {
	if v := h.text.Version(); !h.hasSrc || h.srcOf != v {
		h.src, h.srcOf, h.hasSrc = h.text.String(), v, true
	}
	return h.src
}

// lex lexes the text, when another version than the last lexed.
func (h *sqlHighlighter) lex() {
	v := h.text.Version()
	if h.hasTokens && h.tokensOf == v {
		return
	}
	h.tokensOf, h.hasTokens, h.tokens = v, true, nil
	if h.text.Len() > maxHighlight {
		return
	}
	h.tokens = sqltext.Tokenize(h.source(), h.dialect)
	if h.tokens == nil {
		h.tokens = []sqltext.Token{}
	}
}

// FoldEnd is the last line the fold of a line hides: of the outermost
// block starting on it that takes more than a line. A text too long to
// color does not fold either, as finding its blocks lexes it whole.
func (h *sqlHighlighter) FoldEnd(line int) (int, bool) {
	if v := h.text.Version(); !h.hasFoldEnds || h.foldsOf != v {
		h.foldsOf, h.hasFoldEnds, h.foldEnds = v, true, nil
		if h.text.Len() <= maxHighlight {
			h.foldEnds = h.findFolds()
		}
	}
	end, ok := h.foldEnds[line]
	return end, ok
}

// findFolds finds the blocks that fold: statements, groups in
// parentheses and block comments over more than a line, by the line each
// starts on, the outermost of a line kept.
func (h *sqlHighlighter) findFolds() map[int]int {
	src := h.source()
	ends := map[int]int{}
	add := func(start, end int) {
		end = min(end, len(src))
		first, last := h.text.LineOf(start), h.text.LineOf(max(end-1, start))
		if first < last && last > ends[first] {
			ends[first] = last
		}
	}
	for _, st := range sqltext.SplitWith(src, h.dialect, h.split) {
		add(st.Start, st.End)
	}
	h.lex()
	var open []int
	for _, t := range h.tokens {
		switch {
		case t.Kind == sqltext.Comment:
			add(t.Start, t.End)
		case t.Kind == sqltext.Punct && src[t.Start] == '(':
			open = append(open, t.Start)
		case t.Kind == sqltext.Punct && src[t.Start] == ')' && len(open) > 0:
			add(open[len(open)-1], t.End)
			open = open[:len(open)-1]
		}
	}
	return ends
}

// tokenKind is the editor's kind of a sqltext token, which the theme
// colors as the palette colors the sqltext kind.
func tokenKind(k sqltext.Kind) code.TokenKind {
	switch k {
	case sqltext.Keyword:
		return code.TokenKeyword
	case sqltext.String:
		return code.TokenString
	case sqltext.Number:
		return code.TokenNumber
	case sqltext.Comment:
		return code.TokenComment
	case sqltext.QuotedIdent:
		return code.TokenProperty
	case sqltext.Param:
		return code.TokenVariable
	case sqltext.Operator:
		return code.TokenOperator
	}
	return code.TokenText
}
