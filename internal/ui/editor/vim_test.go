package editor

import (
	"strings"
	"testing"
)

// keys splits typed keys into tokens: a rune each, a <name> whole.
func keys(s string) []string {
	var out []string
	for s != "" {
		if s[0] == '<' {
			if i := strings.IndexByte(s, '>'); i > 0 {
				out = append(out, s[:i+1])
				s = s[i+1:]
				continue
			}
		}
		r := []rune(s)[0]
		out = append(out, string(r))
		s = s[len(string(r)):]
	}
	return out
}

// vimOn is Vim on a text, its caret where | is; the clipboard a string.
func vimOn(text string) (*Vim, *string) {
	clip := new(string)
	v := NewVim()
	at := strings.IndexByte(text, '|')
	v.Text, v.Caret = []rune(strings.Replace(text, "|", "", 1)), len([]rune(text[:at]))
	v.ReadClipboard = func() string { return *clip }
	v.WriteClipboard = func(s string) { *clip = s }
	return v, clip
}

// shown is the text with | at the caret.
func (v *Vim) shown() string {
	return string(v.Text[:v.Caret]) + "|" + string(v.Text[v.Caret:])
}

// typeIn feeds keys; in insert mode, what Vim leaves to the text area is
// typed as the text area would.
func (v *Vim) typeIn(s string) {
	for _, k := range keys(s) {
		if v.Mode == VimInsert && k == "<Esc>" {
			v.Typed(v.Text, v.Caret) // as the editor tells it
		}
		if v.Feed(k) {
			continue
		}
		switch k {
		case "<CR>":
			v.insert("\n")
		case "<BS>":
			v.Text = append(v.Text[:v.Caret-1], v.Text[v.Caret:]...)
			v.Caret--
		default:
			v.insert(k)
		}
	}
}

func TestVimMotions(t *testing.T) {
	for _, c := range []struct{ text, keys, want string }{
		{"|select a, b from t", "w", "select |a, b from t"},
		{"|select a, b from t", "3w", "select a, |b from t"},
		{"select a, b |from t", "b", "select a, |b from t"},
		{"|select a, b from t", "e", "selec|t a, b from t"},
		{"|select a.b from t", "W", "select |a.b from t"},
		{"select |a, b", "$", "select a, |b"},
		{"  sel|ect", "^", "  |select"},
		{"  sel|ect", "0", "|  select"},
		{"a|bc\nde\nfghij", "j", "abc\nd|e\nfghij"},
		{"a|bc\nde\nfghij", "jj", "abc\nde\nf|ghij"},
		{"one\ntwo\nthr|ee", "gg", "|one\ntwo\nthree"},
		{"|one\ntwo\nthree", "G", "one\ntwo\n|three"},
		{"|one\ntwo\nthree", "2G", "one\n|two\nthree"},
		{"|count(a, b)", "fb", "count(a, |b)"},
		{"|count(a, b)", "t,", "count(|a, b)"},
		{"|a,b,c", "f,;", "a,b|,c"},
		{"|sum(max(a))", "%", "sum(max(a)|)"},
		{"a\nb\n\nc|", "{", "a\nb\n|\nc"},
	} {
		v, _ := vimOn(c.text)
		v.typeIn(c.keys)
		if got := v.shown(); got != c.want {
			t.Errorf("%q %s: %q, want %q", c.text, c.keys, got, c.want)
		}
	}
}

func TestVimEdits(t *testing.T) {
	for _, c := range []struct{ text, keys, want string }{
		{"select |a, b", "x", "select |, b"},
		{"select |a, b", "dw", "select |, b"},
		{"select |abc d", "dw", "select |d"},
		{"select |a, b", "d$", "select| "},
		{"select a|bc", "diw", "select| "},
		{"select abc| d", "daw", "select abc|d"},
		{"where name = 'a|bc'", "ci'x<Esc>", "where name = '|x'"},
		{"count(a, |b)", "di(", "count(|)"},
		{"count(a, |b)", "da(", "coun|t"},
		{"select |a, b", "cwz<Esc>", "select |z, b"},
		{"one\nt|wo\nthree", "dd", "one\n|three"},
		{"one\ntwo\nthr|ee", "dd", "one\n|two"},
		{"one\nt|wo\nthree", "2dd", "|one"},
		{"one\nt|wo", "yyp", "one\ntwo\n|two"},
		{"o|ne\ntwo", "yyjP", "one\n|one\ntwo"},
		{"|abc", "~~", "AB|c"},
		{"|a\nb", "J", "a| b"},
		{"|abc", "rx", "|xbc"},
		{"|a\nb", ">>", "  |a\nb"},
		{"  |a", "<<", "|a"},
		{"|abc", "A!<Esc>", "abc|!"},
		{"|abc", "Ix<Esc>", "|xabc"},
		{"|one", "otwo<Esc>", "one\ntw|o"},
		{"select |a", "dwu", "select |a"},
		{"select |a", "dwu<C-r>", "select| "},
		{"|a b c d", "dw..", "|d"},
		{"|ab ab", "cwx<Esc>w.", "x |x"},
		{"|abc", "vld", "|c"},
		{"one\n|two\nthree", "Vjd", "|one"},
		{"|ab", "vly$p", "aba|b"},
		{"a\n|b\nc\nd", "jdG", "a\n|b"},
		{"a\n|b\nc\nd", "dgg", "|c\nd"},
		{"a\n|\nb", "d$", "a\n|\nb"},
		{"|foo\nbar", "dw", "|\nbar"},
		{"foo |bar\nbaz", "dw", "foo| \nbaz"},
		{"|a b c d e f", "d3w2.", "|f"},
		{"a\n|b", "dj", "a\n|b"},
		{"|abc", "f<Esc>x", "|bc"},
		{"|abc", "ixyz<Esc>u", "|abc"},
		{"|a b c d e", "dw3.", "|e"},
		{"ab|cd", "D", "a|b"},
		{"ab|cd", "Cx<Esc>", "ab|x"},
		{"  ab|cd", "Sx<Esc>", "  |x"},
		{"ab|cd", "X", "a|cd"},
		{"|one\ntwo", "Yjp", "one\ntwo\n|one"},
		{"|one\ntwo", "Vj>", "  |one\n  two"},
		{"a = \"x|y\"", "di\"", "a = \"|\""},
		{"|abc", "3x", "|"},
		{"|abc", "lv<Esc>x", "a|c"},
		{"|ab cd ef", "2cwx<Esc>", "|x ef"},
	} {
		v, _ := vimOn(c.text)
		v.typeIn(c.keys)
		if got := v.shown(); got != c.want {
			t.Errorf("%q %s: %q, want %q", c.text, c.keys, got, c.want)
		}
	}
}

func TestVimPromptAndModes(t *testing.T) {
	v, _ := vimOn("|select a from t where a = 1")
	saved := false
	v.Save = func() { saved = true }
	v.typeIn("/a<CR>")
	if v.Caret != 7 || v.Status() != "" {
		t.Fatalf("searched to %d, status %q", v.Caret, v.Status())
	}
	v.typeIn("n")
	if v.Caret != 22 {
		t.Fatalf("next match at %d", v.Caret)
	}
	v.typeIn(":w<CR>")
	if !saved {
		t.Fatal(":w did not save")
	}
	v.typeIn("i")
	if v.Mode != VimInsert || v.Status() != "-- INSERT --" {
		t.Fatalf("mode %v, status %q", v.Mode, v.Status())
	}
	v.typeIn("<Esc>v")
	if s, e := v.Selection(); v.Mode != VimVisual || e-s != 1 {
		t.Fatalf("visual %v [%d, %d)", v.Mode, s, e)
	}
	v.typeIn("<Esc>d")
	if v.Status() != "d" {
		t.Fatalf("pending %q", v.Status())
	}
}
