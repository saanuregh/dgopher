package editor

import (
	"strings"
	"testing"

	"dgopher/internal/sqltext"

	"github.com/egoist/mygo/ui"
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
	v.Text, v.Caret = strings.Replace(text, "|", "", 1), at
	v.ReadClipboard = func() string { return *clip }
	v.WriteClipboard = func(s string) { *clip = s }
	return v, clip
}

// shown is the text with | at the caret.
func (v *Vim) shown() string {
	return v.Text[:v.Caret] + "|" + v.Text[v.Caret:]
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
			p := prevChar(v.Text, v.Caret)
			v.Text = v.Text[:p] + v.Text[v.Caret:]
			v.Caret = p
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

// The Edit menu's commands are the Vim keys they stand for; copy and cut
// only in Visual mode, cut left undone outside it.
func TestVimEditMenu(t *testing.T) {
	cases := []struct{ command, keys string }{
		{"undo", "u"}, {"redo", "<C-r>"}, {"paste", "P"}, {"copy", "y"}, {"cut", "d"},
		{"selectAll", "<Esc>ggVG"}, {"find", ""},
	}
	for _, mode := range []string{"", "v", "V"} {
		for _, c := range cases {
			want := c.keys
			if mode == "" && (c.command == "copy" || c.command == "cut") {
				want = ""
			}
			on := func() (*Editor, *string) {
				v, clip := vimOn("se|lect a\nfrom t")
				*clip = "pasted"
				v.typeIn("xu<C-r>" + mode + "l")
				e := New(v.Text, sqltext.Generic)
				e.Vim = v
				e.showVim()
				return e, clip
			}
			got, gotClip := on()
			took := got.vimInput(ui.InputEvent{Kind: ui.InputCommand, Text: c.command})
			fed, fedClip := on()
			if want != "" {
				fed.vimFeed(keys(want))
			}
			gs, ge := got.Vim.Selection()
			fs, fe := fed.Vim.Selection()
			wantTook := want != "" || c.command == "cut"
			if took != wantTook || got.Text() != fed.Text() || got.Vim.Caret != fed.Vim.Caret || got.Vim.Mode != fed.Vim.Mode ||
				*gotClip != *fedClip || gs != fs || ge != fe {
				t.Errorf("%q in mode %q: took %v, text %q caret %d mode %v clip %q; want as keys %q: text %q caret %d mode %v clip %q",
					c.command, mode, took, got.Text(), got.Vim.Caret, got.Vim.Mode, *gotClip, want, fed.Text(), fed.Vim.Caret, fed.Vim.Mode, *fedClip)
			}
		}
	}
}

// Motions and edits step by characters, whatever their bytes; offsets
// are bytes.
func TestVimMultibyte(t *testing.T) {
	for _, c := range []struct{ text, keys, want string }{
		{"|é日本x", "l", "é|日本x"},
		{"|é日本x", "3l", "é日本|x"},
		{"|é日本x", "9l", "é日本|x"},
		{"é日本|x", "h", "é日|本x"},
		{"é日本|x", "2h", "é|日本x"},
		{"|é日本x", "$", "é日本|x"},
		{"|日本 😀 x", "w", "日本 |😀 x"},
		{"|日本 😀 x", "2w", "日本 😀 |x"},
		{"日本 😀 |x", "b", "日本 |😀 x"},
		{"|日本 x", "e", "日|本 x"},
		{"|a日b", "fb", "a日|b"},
		{"|a日b", "tb", "a|日b"},
		{"a日|b", "F日", "a|日b"},
		{"|日本", "x", "|本"},
		{"a|😀b", "x", "a|b"},
		{"日|本", "X", "|本"},
		{"|日本", "rx", "|x本"},
		{"|日本", "2rx", "x|x"},
		{"|日", "r😀", "|😀"},
		{"|éa", "~~", "É|A"},
		{"|ıa", "~", "I|a"},
		{"日本|x\nabcd", "j", "日本x\nab|cd"},
		{"ab|cd\n日本x", "j", "abcd\n日本|x"},
		{"|日本x", "vld", "|x"},
		{"|é = 'é日'", "f'di'", "é = '|'"},
		{"|日本 x", "dw", "|x"},
		{"|日本 x", "cwé<Esc>", "|é x"},
		{"|é", "a日<Esc>", "é|日"},
		{"é|日", "i😀<Esc>", "é|😀日"},
		{"|é日本", "yyp", "é日本\n|é日本"},
		{"|é日本", "dwu", "|é日本"},
		{"|a 日本 b 日本", "/日本<CR>n", "a 日本 b |日本"},
		// Indenting puts the caret on the first line's first non-blank.
		{"日本|x\nab", ">j", "  |日本x\n  ab"},
		{"  日本|x\n  ab", "<j", "|日本x\nab"},
		{"ab|c\nd", ">j", "  |abc\n  d"},
		{"日本|x", ">>", "  |日本x"},
		{"    ab|c\nd", "v<", "  |abc\nd"},
		{"  日本|x\nd", "v<", "|日本x\nd"},
		{"日本|x\nd", "v>", "  |日本x\nd"},
	} {
		v, _ := vimOn(c.text)
		v.typeIn(c.keys)
		if got := v.shown(); got != c.want {
			t.Errorf("%q %s: %q, want %q", c.text, c.keys, got, c.want)
		}
	}
}
