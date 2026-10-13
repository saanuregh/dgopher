package editor

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/sqltext"

	"github.com/egoist/mygo/ui"
	code "github.com/egoist/mygo/ui/editor"
)

// highlighterOf is the SQL highlighter of a text, as a buffer makes it.
func highlighterOf(text string, d sqltext.Dialect, o sqltext.SplitOptions) *sqlHighlighter {
	return newSQLHighlighter(code.NewBuffer(text).Snapshot(), d, o)
}

// lineTokens are the tokens of a line, as "kind:text" runs.
func lineTokens(h *sqlHighlighter, text string, line int) []string {
	src := strings.Split(text, "\n")[line]
	toks, final := h.Tokens(line, src, nil)
	if !final {
		return []string{"not final"}
	}
	var out []string
	at := 0
	for _, t := range toks {
		if t.End <= at || t.End > len(src) {
			return append(out, fmt.Sprintf("bad end %d after %d", t.End, at))
		}
		if t.Kind != code.TokenText {
			out = append(out, fmt.Sprintf("%d:%s", t.Kind, src[at:t.End]))
		}
		at = t.End
	}
	if len(toks) > 0 && at != len(src) {
		out = append(out, fmt.Sprintf("tokens end at %d of %d", at, len(src)))
	}
	return out
}

// The text is colored as sqltext lexes it in the editor's dialect, in
// byte offsets of each line, a token over lines split at them; a text
// over 2 MB is left plain.
func TestHighlightSQL(t *testing.T) {
	text := "select \"q\", `b` -- é日本😀\nfrom t where x = 'é\n日' and y = :p1;"
	tok := func(k code.TokenKind, s string) string { return fmt.Sprintf("%d:%s", k, s) }
	str, kw, prop, op := code.TokenString, code.TokenKeyword, code.TokenProperty, code.TokenOperator
	comment := tok(code.TokenComment, "-- é日本😀")
	for _, c := range []struct {
		d    sqltext.Dialect
		line int
		want []string
	}{
		{sqltext.MySQL, 0, []string{tok(kw, "select"), tok(str, `"q"`), tok(prop, "`b`"), comment}},
		{sqltext.Postgres, 0, []string{tok(kw, "select"), tok(prop, `"q"`), comment}},
		{sqltext.Postgres, 1, []string{tok(kw, "from"), tok(kw, "where"), tok(op, "="), tok(str, "'é")}},
		{sqltext.Postgres, 2, []string{tok(str, "日'"), tok(kw, "and"), tok(op, "="), tok(code.TokenVariable, ":p1")}},
	} {
		h := highlighterOf(text, c.d, sqltext.SplitOptions{})
		if got := lineTokens(h, text, c.line); fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("dialect %v line %d: %q, want %q", c.d, c.line, got, c.want)
		}
	}

	// After an edit, the text as it is now.
	buf := code.NewBuffer("select 1")
	h := newSQLHighlighter(buf.Snapshot(), sqltext.Postgres, sqltext.SplitOptions{})
	old := buf.Snapshot()
	buf.Edit(code.Edit{Start: 7, End: 8, Text: "'x'"})
	h.Edit(old, buf.Snapshot(), nil)
	if got := fmt.Sprint(lineTokens(h, "select 'x'", 0)); got != fmt.Sprint([]string{tok(kw, "select"), tok(str, "'x'")}) {
		t.Errorf("after an edit: %s", got)
	}

	big := strings.Repeat("select 1;\n", maxHighlight/10+1)
	h = highlighterOf(big, sqltext.Postgres, sqltext.SplitOptions{})
	if got := lineTokens(h, big, 0); len(got) != 0 {
		t.Errorf("a text of %d bytes colored: %v", len(big), got)
	}
	// Nor is it lexed to fold.
	long := "select\n" + strings.Repeat("1,\n", maxHighlight/3) + "1;"
	if end, ok := highlighterOf(long, sqltext.Postgres, sqltext.SplitOptions{}).FoldEnd(0); ok {
		t.Errorf("a text of %d bytes folds its first line to %d", len(long), end)
	}
}

// A statement, a group in parentheses and a block comment over several
// lines fold, the outermost of a line; statements end as the user's
// split option says.
func TestFoldEnd(t *testing.T) {
	text := strings.Join([]string{
		"select a,", // 0: statement to 2
		"  b",
		"from t;",
		"select x from t where a in (", // 3: statement to 6, parentheses to 5
		"  1,",
		"  2)",
		"  and b;",
		"select (", // 7: statement to 9
		"  (1 +",   //   8: parentheses to 9, inner to 9
		"  2));",
		"select 1", // 10: alone, before a blank line
		"",
		"select 2;",
		"/* é 日本", // 13: block comment to 14
		"😀 */",
	}, "\n")
	h := highlighterOf(text, sqltext.Postgres, sqltext.SplitOptions{})
	want := map[int]int{0: 2, 3: 6, 7: 9, 8: 9, 13: 14}
	for line := range strings.Count(text, "\n") + 1 {
		end, ok := h.FoldEnd(line)
		if w, folds := want[line]; ok != folds || ok && end != w {
			t.Errorf("line %d folds %v to %d, want %v to %d", line, ok, end, folds, w)
		}
	}
	// ; alone ends statements: the one at line 10 runs on to line 12.
	h = highlighterOf(text, sqltext.Postgres, sqltext.SplitOptions{Mode: sqltext.SemicolonOnly})
	if end, ok := h.FoldEnd(10); !ok || end != 12 {
		t.Errorf("with ; alone, line 10 folds %v to %d, want to 12", ok, end)
	}
	// The editor folds by them, the split option the editor's.
	e := New(text, sqltext.Postgres)
	tt := ui.NewTester(func(c *ui.Context) { e.View(c, 13) }, 800, 600)
	tt.Frame()
	e.State.Run(code.FoldAll)
	if got := e.State.Folded(); !slices.Contains(got, 0) || !slices.Contains(got, 13) || slices.Contains(got, 10) {
		t.Errorf("folded lines %v", got)
	}
	e.State.Run(code.UnfoldAll)
	e.SplitOptions = sqltext.SplitOptions{Mode: sqltext.SemicolonOnly}
	tt.Frame()
	e.State.Fold(10)
	if got := e.State.Folded(); !slices.Equal(got, []int{10}) {
		t.Errorf("with ; alone, folded lines %v, want [10]", got)
	}
}
