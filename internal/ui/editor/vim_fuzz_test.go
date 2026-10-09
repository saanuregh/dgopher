package editor

import (
	"testing"
	"unicode/utf8"
)

// vimTokens are the keys the fuzzer types, by a byte each.
var vimTokens = []string{
	"h", "j", "k", "l", "w", "W", "b", "B", "e", "E", "0", "^", "$", "g", "G", "f", "F", "t", "T", ";", ",", "%", "{", "}",
	"d", "c", "y", ">", "<", "i", "a", "I", "A", "o", "O", "x", "X", "D", "C", "Y", "s", "S", "r", "J", "~", "p", "P",
	"v", "V", "u", ".", "n", "N", "/", "?", ":", "w", "(", ")", "\"", "'", "2", "3", " ", "é", "\n",
	"<Esc>", "<CR>", "<BS>", "<Del>", "<C-r>", "<C-d>", "<C-u>", "<Left>", "<Right>", "<Up>", "<Down>", "<Home>", "<End>",
}

// No keys on any text make Vim panic, or leave its caret off the text.
func FuzzVim(f *testing.F) {
	f.Add("select a, b\nfrom t\n", []byte{20, 3, 1, 30, 60})
	f.Add("", []byte{40, 45, 26, 41, 60, 47})
	f.Add("(a)\n\n'b' \"c\"", []byte{22, 24, 21, 25, 56, 25, 57, 49, 50})
	f.Fuzz(func(t *testing.T, text string, keys []byte) {
		if !utf8.ValidString(text) || len(keys) > 200 {
			return
		}
		clip := ""
		v := NewVim()
		v.Text = []rune(text)
		v.ReadClipboard = func() string { return clip }
		v.WriteClipboard = func(s string) { clip = s }
		for _, k := range keys {
			tok := vimTokens[int(k)%len(vimTokens)]
			if v.Mode == VimInsert && tok != "<Esc>" {
				v.typeKey(tok)
				continue
			}
			v.Feed(tok)
			if v.Caret < 0 || v.Caret > len(v.Text) {
				t.Fatalf("caret %d of %d after %q", v.Caret, len(v.Text), tok)
			}
			if s, e := v.Selection(); s < 0 || e > len(v.Text) || s > e {
				t.Fatalf("selection [%d, %d) of %d", s, e, len(v.Text))
			}
		}
	})
}
