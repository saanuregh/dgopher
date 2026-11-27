package mongosh

import (
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// regexEndByScan is regexEnd read in full from every /, without what an
// earlier / of the line found.
func regexEndByScan(src string, i int) int {
	inClass := false
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case '\n':
			return 0
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if inClass {
				continue
			}
			if j == i+1 {
				return 0
			}
			k := j + 1
			for k < len(src) && isIdentPart(rune(src[k])) {
				k++
			}
			return k
		}
	}
	return 0
}

// Reading a / only up to the first bracket after one of its line did
// not end finds what reading it in full does.
func TestRegexEndAfterOpenFindsTheSame(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	const alphabet = "/[]\\\na( i"
	for range 20000 {
		b := make([]byte, r.IntN(24))
		for i := range b {
			b[i] = alphabet[r.IntN(len(alphabet))]
		}
		src := string(b)
		openLineEnd := -1
		for i := range len(src) {
			if src[i] != '/' {
				continue
			}
			end, lineEnd := regexEnd(src, i, i < openLineEnd)
			if lineEnd > 0 {
				openLineEnd = lineEnd
			}
			if want := regexEndByScan(src, i); end != want {
				t.Fatalf("%q at %d: %d, want %d", src, i, end, want)
			}
		}
	}
}

// A line of many / that open no regular expression is lexed in linear
// time: four times the text takes about four times as long, not sixteen.
func TestLexManySlashesIsLinear(t *testing.T) {
	took := func(n int) time.Duration {
		src := strings.Repeat("(/[", n)
		best := time.Hour
		for range 3 {
			start := time.Now()
			lex(src)
			best = min(best, time.Since(start))
		}
		return best
	}
	small, large := took(20000), took(80000)
	if large > 8*small+10*time.Millisecond {
		t.Errorf("lexing 4× the text took %v, against %v", large, small)
	}
}

func BenchmarkLexManySlashes(b *testing.B) {
	src := strings.Repeat("(/[", 40000) // 120 KB
	for b.Loop() {
		lex(src)
	}
}
