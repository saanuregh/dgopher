package sqltext

import "testing"

func TestCommentText(t *testing.T) {
	for _, end := range []string{"\n", "\r", "\v", "\f", "\x00", "\x1b", "\x7f", "\u0085", " ", " "} {
		if got := CommentText("a" + end + "DROP TABLE victims;"); got != "a DROP TABLE victims;" {
			t.Errorf("%q: got %q", end, got)
		}
	}
	for _, s := range []string{"", "plain text", "tab\there", "naïve “quotes” 表"} {
		if got := CommentText(s); got != s {
			t.Errorf("%q changed to %q", s, got)
		}
	}
}

func TestLineComment(t *testing.T) {
	if got := LineComment("plain text"); got != "-- plain text" {
		t.Errorf("got %q", got)
	}
	for _, end := range []string{"\n", "\r", "\r\n", "\v", "\f", "\u0085", " ", " "} {
		for _, d := range []Dialect{Generic, Postgres, MySQL, ClickHouse} {
			if stmts := Split(LineComment("a"+end+"DROP TABLE victims;")+"\n", d); len(stmts) != 0 {
				t.Errorf("%q, dialect %v: the comment ended, leaving %q", end, d, stmts[0].Text)
			}
		}
	}
}
