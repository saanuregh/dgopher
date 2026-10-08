package sqltext

import (
	"strings"
	"unicode"
)

// ChangeCase upper- or lower-cases the keywords and names in the rune
// range [start, end), leaving strings, quoted names and comments as they
// are.
func ChangeCase(src string, start, end int, d Dialect, upper bool) string {
	rs := []rune(src)
	for _, t := range tokenizeRunes(rs, d) {
		if t.Kind != Keyword && t.Kind != Identifier {
			continue
		}
		for i := max(t.Start, start); i < min(t.End, end); i++ {
			if upper {
				rs[i] = unicode.ToUpper(rs[i])
			} else {
				rs[i] = unicode.ToLower(rs[i])
			}
		}
	}
	return string(rs)
}

// ToggleComment comments out the lines the rune range [start, end)
// touches with "-- ", or uncomments them when every line that is not
// blank is a comment already. It returns the new text and the range of
// those lines in it.
func ToggleComment(src string, start, end int) (string, int, int) {
	rs := []rune(src)
	first := start
	for first > 0 && rs[first-1] != '\n' {
		first--
	}
	if end > start && end > 0 && rs[end-1] == '\n' {
		end-- // a selection ending at a line's start leaves that line out
	}
	last := max(end, start)
	for last < len(rs) && rs[last] != '\n' {
		last++
	}
	lines := strings.Split(string(rs[first:last]), "\n")
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
	out := string(rs[:first]) + mid + string(rs[last:])
	return out, first, first + len([]rune(mid))
}
