package db

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestExcerpt(t *testing.T) {
	long := strings.Repeat("é", 100) + " WHERE status = 'Lost' " + strings.Repeat("x", 200)
	for _, c := range []struct{ def, text, want string }{
		{"SELECT 1\n    FROM orders\n  WHERE status = 'Lost'\n", "lost", "WHERE status = 'Lost'"},
		{"no match", "lost", ""},
		{"İstanbul lost\n", "lost", "İstanbul lost"},
	} {
		if got := excerpt(c.def, c.text); got != c.want {
			t.Errorf("excerpt(%q, %q) = %q, want %q", c.def, c.text, got, c.want)
		}
	}
	got := excerpt(long, "lost")
	if !strings.Contains(got, "'Lost'") || !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") || !utf8.ValidString(got) {
		t.Fatalf("a long line: %q", got)
	}
	if n := utf8.RuneCountInString(got); n > 125 {
		t.Fatalf("a long line kept %d characters", n)
	}
}
