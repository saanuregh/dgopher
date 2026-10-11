package editor

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// findAllFromStart is FindAll as it counted each match's runes from the
// start of the text.
func findAllFromStart(text, query string) []int {
	if query == "" {
		return nil
	}
	lt, lq := strings.ToLower(text), strings.ToLower(query)
	var out []int
	for from := 0; ; {
		i := strings.Index(lt[from:], lq)
		if i < 0 {
			return out
		}
		at := from + i
		out = append(out, utf8.RuneCountInString(lt[:at]))
		from = at + len(lq)
	}
}

// overlayEach is overlay as it cut each base run by every top run.
func overlayEach(base, top []ui.TextRange) []ui.TextRange {
	if len(top) == 0 {
		return base
	}
	out := make([]ui.TextRange, 0, len(base)+len(top))
	for _, b := range base {
		parts := []ui.TextRange{b}
		for _, t := range top {
			var next []ui.TextRange
			for _, p := range parts {
				if t.End <= p.Start || t.Start >= p.End {
					next = append(next, p)
					continue
				}
				if p.Start < t.Start {
					left := p
					left.End = t.Start
					next = append(next, left)
				}
				if t.End < p.End {
					right := p
					right.Start = t.End
					next = append(next, right)
				}
			}
			parts = next
		}
		out = append(out, parts...)
	}
	out = append(out, top...)
	slices.SortFunc(out, func(x, y ui.TextRange) int { return x.Start - y.Start })
	return out
}

func TestFindAllSame(t *testing.T) {
	parts := []string{"a", "A", "ab", "é", "É", "日本", "🙂", "\n", " ", "İ", "K"}
	rng := rand.New(rand.NewPCG(5, 6))
	for range 20000 {
		var b strings.Builder
		for range rng.IntN(60) {
			b.WriteString(parts[rng.IntN(len(parts))])
		}
		query := parts[rng.IntN(len(parts))]
		if rng.IntN(2) == 0 {
			query += parts[rng.IntN(len(parts))]
		}
		if got, want := FindAll(b.String(), query), findAllFromStart(b.String(), query); !slices.Equal(got, want) {
			t.Fatalf("FindAll(%q, %q) = %v, want %v", b.String(), query, got, want)
		}
	}
}

// randomRuns returns sorted runs that do not overlap, as highlighting and
// find marks give, each with its own color.
func randomRuns(rng *rand.Rand, n, weight int) []ui.TextRange {
	var out []ui.TextRange
	for at := rng.IntN(4); len(out) < n; {
		size := 1 + rng.IntN(6)
		out = append(out, ui.TextRange{Start: at, End: at + size, Color: ui.RGBA(uint8(len(out)%7*30), 0, 0, 1), Weight: weight})
		at += size + rng.IntN(5)
	}
	return out
}

func TestOverlaySame(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	for range 20000 {
		base, top := randomRuns(rng, rng.IntN(30), 400), randomRuns(rng, rng.IntN(8), 700)
		if got, want := overlay(base, top), overlayEach(base, top); !slices.Equal(got, want) {
			t.Fatalf("overlay(%v, %v) =\n%v, want\n%v", base, top, got, want)
		}
	}
}

// BenchmarkFindAll finds 20,000 matches in 100,000 lines.
func BenchmarkFindAll(b *testing.B) {
	text := strings.Repeat("SELECT id, name FROM customers WHERE id = 1;\n", 100_000)
	for b.Loop() {
		FindAll(text, "customers WHERE id = 1;\nSELECT id, name FROM customers WHERE id = 1;\nSELECT id, name FROM customers WHERE id = 1;\nSELECT id, name FROM customers WHERE id = 1;\nSELECT id, name FROM customers WHERE id = 1;\nSELECT")
	}
}

// BenchmarkOverlay marks 2,000 matches over 20,000 highlighted runs, as
// every frame does while the find bar is open.
func BenchmarkOverlay(b *testing.B) {
	rng := rand.New(rand.NewPCG(9, 10))
	base, top := randomRuns(rng, 20_000, 400), randomRuns(rng, 2_000, 700)
	for i := range top {
		top[i].Start, top[i].End = top[i].Start*10, top[i].Start*10+3
	}
	for b.Loop() {
		overlay(base, top)
	}
}
