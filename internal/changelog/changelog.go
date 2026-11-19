// Package changelog reads CHANGELOG.md: one version's notes, and which of
// two versions is newer.
package changelog

import (
	"strconv"
	"strings"
)

// Section is the text under the heading "## <version>", also written
// "## v<version>" or "## [<version>]" and followed by more on its line, up
// to the next "## " heading, trimmed; "" when there is none.
func Section(text, version string) string {
	version = strings.TrimPrefix(version, "v")
	if version == "" {
		return ""
	}
	var body []string
	in := false
	for line := range strings.Lines(text) {
		line = strings.TrimRight(line, "\r\n")
		if heading, ok := strings.CutPrefix(line, "## "); ok {
			if in {
				break
			}
			in = headingVersion(heading) == version
			continue
		}
		if in {
			body = append(body, line)
		}
	}
	return strings.TrimSpace(strings.Join(body, "\n"))
}

// headingVersion is the version a heading's text starts with.
func headingVersion(heading string) string {
	heading = strings.TrimSpace(heading)
	if rest, ok := strings.CutPrefix(heading, "["); ok {
		v, _, _ := strings.Cut(rest, "]")
		return strings.TrimPrefix(v, "v")
	}
	v, _, _ := strings.Cut(heading, " ")
	return strings.TrimPrefix(v, "v")
}

// Newer reports whether version a is newer than b. Both are dotted numbers,
// a leading "v" allowed; a part that is not a number counts as 0, and a
// pre-release, after "-", is older than the same version without one.
func Newer(a, b string) bool {
	na, pa := parse(a)
	nb, pb := parse(b)
	for i := range max(len(na), len(nb)) {
		x, y := part(na, i), part(nb, i)
		if x != y {
			return x > y
		}
	}
	return pa == "" && pb != ""
}

func parse(v string) (parts []int, pre string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, pre, _ = strings.Cut(v, "-")
	for p := range strings.SplitSeq(v, ".") {
		n, _ := strconv.Atoi(p)
		parts = append(parts, n)
	}
	return parts, pre
}

func part(parts []int, i int) int {
	if i < len(parts) {
		return parts[i]
	}
	return 0
}
