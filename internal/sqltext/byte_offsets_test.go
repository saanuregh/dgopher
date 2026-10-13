package sqltext

import (
	"strings"
	"testing"
)

// multiByte holds characters of two, three and four bytes, and names
// spelled with them.
const multiByte = `SELECT 'é日本🙂' AS café, "naïve" FROM t -- 日本`

func TestTokenizeByteOffsets(t *testing.T) {
	for _, d := range []Dialect{Generic, Postgres, MySQL, ClickHouse, SQLite} {
		toks := Tokenize(multiByte, d)
		checkCoverage(t, multiByte, toks)
		for _, want := range []string{"'é日本🙂'", "café", `"naïve"`, "-- 日本"} {
			at := strings.Index(multiByte, want)
			found := false
			for _, tok := range toks {
				if tok.Start == at {
					found = true
					if tok.End != at+len(want) || multiByte[tok.Start:tok.End] != want {
						t.Fatalf("dialect %d: token %+v, want %q at bytes %d..%d", d, tok, want, at, at+len(want))
					}
				}
			}
			if !found && !(d == MySQL && want == `"naïve"`) {
				t.Fatalf("dialect %d: no token starts at byte %d (%q): %+v", d, at, want, toks)
			}
		}
		first := TokenizeFirst(multiByte, d, 3)
		if last := first[len(first)-1]; last.Text != "AS" || last.Start != strings.Index(multiByte, "AS") {
			t.Fatalf("dialect %d: TokenizeFirst ends at %+v", d, last)
		}
	}
}

func TestTokenizeExecutableByteOffsets(t *testing.T) {
	src := "SELECT '日本' /*!40101 , café */, 🙂x"
	for _, toks := range TokenizeExecutable(src, MySQL) {
		text := UnwrapExecutable(src)
		checkCoverage(t, text, toks)
		at := strings.Index(text, "café")
		if !hasToken(toks, at, at+len("café")) {
			t.Fatalf("no token café at bytes %d of %q: %+v", at, text, toks)
		}
	}
}

func hasToken(toks []Token, start, end int) bool {
	for _, tok := range toks {
		if tok.Start == start && tok.End == end {
			return true
		}
	}
	return false
}

func TestSplitByteOffsets(t *testing.T) {
	src := "select 'é日本🙂';\nselect café from t;\n\nselect \"naïve\""
	stmts := SplitWith(src, Postgres, SplitOptions{})
	want := []string{"select 'é日本🙂'", "select café from t", "select \"naïve\""}
	if len(stmts) != len(want) {
		t.Fatalf("got %+v", stmts)
	}
	for i, s := range stmts {
		if s.Text != want[i] || s.Start != strings.Index(src, want[i]) || src[s.Start:s.End] != want[i] {
			t.Fatalf("%d: got %+v, want %q at byte %d", i, s, want[i], strings.Index(src, want[i]))
		}
	}
	caret := strings.Index(src, "fé") + len("f")
	if s, ok := StatementAt(src, caret, Postgres); !ok || s.Text != want[1] {
		t.Fatalf("StatementAt(%d) = %+v, %v", caret, s, ok)
	}
	if s, ok := StatementAtWith(src, len(src), Postgres, SplitOptions{Mode: SemicolonOnly}); !ok || s.Text != want[2] {
		t.Fatalf("StatementAtWith(end) = %+v, %v", s, ok)
	}
}

func TestCompletionAtByteOffsets(t *testing.T) {
	for _, tt := range []struct {
		src, prefix, qualifier string
	}{
		{"select '日本🙂', x.caf| from t x", "caf", "x"},
		{"select '日本🙂', café| from t", "café", ""},
		{"select 🙂, \"naïve\".| from t", "", "naïve"},
	} {
		caret := strings.Index(tt.src, "|")
		src := strings.Replace(tt.src, "|", "", 1)
		got := CompletionAt(src, caret, Postgres)
		if got.Prefix != tt.prefix || got.Qualifier != tt.qualifier || got.PrefixStart != caret-len(tt.prefix) || src[got.PrefixStart:caret] != tt.prefix {
			t.Fatalf("%q: got %+v, want prefix %q at byte %d", tt.src, got, tt.prefix, caret-len(tt.prefix))
		}
	}
}

func TestParamsByteOffsets(t *testing.T) {
	src := "select '日本🙂', café, :id, ${naïve}"
	got := Params(src, Postgres)
	want := []string{":id", "${naïve}"}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, p := range got {
		if p.Start != strings.Index(src, want[i]) || src[p.Start:p.End] != want[i] {
			t.Fatalf("%d: got %+v, want %q at byte %d", i, p, want[i], strings.Index(src, want[i]))
		}
	}
	typed := "select '日本', {café:String}"
	if got := Params(typed, ClickHouse); len(got) != 1 || typed[got[0].Start:got[0].End] != "{café:String}" {
		t.Fatalf("typed: got %+v", got)
	}
	sqlite := "select '🙂', @café"
	if got := Params(sqlite, SQLite); len(got) != 1 || sqlite[got[0].Start:got[0].End] != "@café" {
		t.Fatalf("sqlite: got %+v", got)
	}
}

func TestChangeCaseByteOffsets(t *testing.T) {
	src := "select 'é日本🙂', café, naïve from t"
	from := strings.Index(src, "café")
	to := strings.Index(src, " from")
	got := ChangeCase(src, from, to, Generic, true)
	if want := "select 'é日本🙂', CAFÉ, NAÏVE from t"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := ChangeCase(src, 0, len(src), Generic, true); got != "SELECT 'é日本🙂', CAFÉ, NAÏVE FROM T" {
		t.Fatalf("whole text: %q", got)
	}
}

func TestToggleCommentByteOffsets(t *testing.T) {
	src := "select '日本'\nselect 🙂, café\nfrom t"
	at := strings.Index(src, "café")
	got, s, e := ToggleComment(src, at, at)
	if want := "select '日本'\n-- select 🙂, café\nfrom t"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got[s:e] != "-- select 🙂, café" {
		t.Fatalf("range %d..%d = %q", s, e, got[s:e])
	}
}

func TestFormatRangeByteOffsets(t *testing.T) {
	src := "select 'é日本🙂';\nselect café from t;\n\nselect  3"
	at := strings.Index(src, "café")
	text, s, e := FormatRange(src, at, at+len("café"), Generic, SplitOptions{})
	want := FormatWith("select café from t", Generic, SplitOptions{})
	if s != strings.Index(src, "select café") || text[s:e] != want {
		t.Fatalf("range %d..%d = %q, want %q at %d", s, e, text[s:e], want, strings.Index(src, "select café"))
	}
	if text != src[:s]+want+";\n\nselect  3" {
		t.Fatalf("text %q", text)
	}
}

// Invalid UTF-8 is read byte by byte, each invalid byte a name character
// as U+FFFD would be, and never panics.
func TestInvalidUTF8(t *testing.T) {
	for _, d := range []Dialect{Generic, Postgres, MySQL, ClickHouse, SQLite} {
		for _, src := range []string{"select '\xff', a\xffb from t", "\xff", "select \"\xff\" from t\xe6", "a\xe6\x97"} {
			toks := Tokenize(src, d)
			checkCoverage(t, src, toks)
			SplitWith(src, d, SplitOptions{})
			for at := 0; at <= len(src); at++ {
				CompletionAt(src, at, d)
				StatementAt(src, at, d)
				ChangeCase(src, 0, at, d, true)
				ToggleComment(src, at, at)
				FormatRange(src, at, at, d, SplitOptions{})
			}
			Params(src, d)
			Classify(src, d)
			ClassifyExecutable(src, d)
		}
		toks := Tokenize("select a\xffb from t", d)
		if !hasToken(toks, len("select "), len("select a\xffb")) {
			t.Fatalf("dialect %d: a\\xffb is not one name: %+v", d, toks)
		}
	}
	if got := ChangeCase("a\xffb", 0, 3, Generic, true); got != "A\xffB" {
		t.Fatalf("ChangeCase kept %q", got)
	}
}
