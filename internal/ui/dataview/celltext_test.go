package dataview

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// cellTextWhole is cellText as it read the whole value.
func cellTextWhole(s string, max int) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i] + " ⏎…"
	}
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}

func TestCellTextSame(t *testing.T) {
	parts := []string{"a", "é", "日本", "🙂", "\n", "\r\n", "\r", " ", "x\xffy", "⏎"}
	rng := rand.New(rand.NewPCG(3, 4))
	for range 20000 {
		var b strings.Builder
		for range rng.IntN(40) {
			b.WriteString(parts[rng.IntN(len(parts))])
		}
		s, max := b.String(), rng.IntN(30)
		if got, want := cellText(s, max), cellTextWhole(s, max); got != want {
			t.Fatalf("cellText(%q, %d) = %q, want %q", s, max, got, want)
		}
	}
}

// BenchmarkCellText shows a 1 MB value on one line, as a JSON column
// holds, cut to a cell.
func BenchmarkCellText(b *testing.B) {
	s := strings.Repeat(`{"k":"v"},`, 100_000)
	for b.Loop() {
		cellText(s, 200)
	}
}
