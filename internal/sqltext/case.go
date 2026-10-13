package sqltext

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ChangeCase upper- or lower-cases the keywords and names in the byte
// range [start, end), leaving strings, quoted names and comments as they
// are. A byte that is not part of a valid UTF-8 character is kept as is.
func ChangeCase(src string, start, end int, d Dialect, upper bool) string {
	var b strings.Builder
	b.Grow(len(src))
	done := 0
	for _, t := range Tokenize(src, d) {
		if t.Kind != Keyword && t.Kind != Identifier {
			continue
		}
		from, to := max(t.Start, start), min(t.End, end)
		if from >= to {
			continue
		}
		b.WriteString(src[done:from])
		for i := from; i < to; {
			r, size := utf8.DecodeRuneInString(src[i:to])
			switch {
			case r == utf8.RuneError && size == 1:
				b.WriteByte(src[i])
			case upper:
				b.WriteRune(unicode.ToUpper(r))
			default:
				b.WriteRune(unicode.ToLower(r))
			}
			i += size
		}
		done = to
	}
	b.WriteString(src[done:])
	return b.String()
}

// ToggleComment comments out the lines the byte range [start, end)
// touches with "-- ", or uncomments them when every line that is not
// blank is a comment already. It returns the new text and the byte range
// of those lines in it.
func ToggleComment(src string, start, end int) (string, int, int) {
	first := start
	for first > 0 && src[first-1] != '\n' {
		first--
	}
	if end > start && end > 0 && src[end-1] == '\n' {
		end-- // a selection ending at a line's start leaves that line out
	}
	last := max(end, start)
	for last < len(src) && src[last] != '\n' {
		last++
	}
	lines := strings.Split(src[first:last], "\n")
	commented := true
	for _, l := range lines {
		if t := strings.TrimLeft(l, " \t"); t != "" && !strings.HasPrefix(t, "--") {
			commented = false
		}
	}
	for i, l := range lines {
		t := strings.TrimLeft(l, " \t")
		if t == "" {
			continue
		}
		indent := l[:len(l)-len(t)]
		if commented {
			t = strings.TrimPrefix(strings.TrimPrefix(t, "--"), " ")
		} else {
			t = "-- " + t
		}
		lines[i] = indent + t
	}
	mid := strings.Join(lines, "\n")
	return src[:first] + mid + src[last:], first, first + len(mid)
}
