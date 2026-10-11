package sqltext

import "strings"

// UnwrapExecutable turns MySQL's executable comments, /*! … */, /*!80000
// … */ and MariaDB's /*M! … */, into the code they hold, which the server
// runs: classified as comments, they would hide a DELETE or an INTO
// OUTFILE from the safety policy, or DDL from a reader of what commits.
// Strings and ordinary comments are copied as they are, so a quote inside
// them misleads nothing.
func UnwrapExecutable(sql string) string {
	return unwrapExecutable(sql, defaultQuoting(MySQL))
}

// unwrapExecutable is UnwrapExecutable with strings read as q reads them,
// which decides whether a /*! inside one is code.
func unwrapExecutable(sql string, q quoting) string {
	if !strings.Contains(sql, "/*!") && !strings.Contains(sql, "/*M!") {
		return sql
	}
	var b strings.Builder
	r := []rune(sql)
	inExec := false
	at := func(i int, s string) bool { return strings.HasPrefix(string(r[i:min(len(r), i+len(s))]), s) }
	escapes := func(quote rune) bool { return q.backslashes && (quote == '\'' || quote == '"' && !q.ansiQuotes) }
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			// A string, or a quoted name, to its end.
			b.WriteRune(c)
			for i++; i < len(r); i++ {
				b.WriteRune(r[i])
				if r[i] == '\\' && escapes(c) && i+1 < len(r) {
					i++
					b.WriteRune(r[i])
					continue
				}
				if r[i] == c {
					break
				}
			}
			continue
		case at(i, "/*!") || at(i, "/*M!"):
			inExec = true
			b.WriteRune(' ')
			i += 2
			if r[i] == 'M' {
				i++
			}
			for i+1 < len(r) && r[i+1] >= '0' && r[i+1] <= '9' {
				i++ // the version it needs
			}
			continue
		case inExec && at(i, "*/"):
			inExec = false
			b.WriteRune(' ')
			i++
			continue
		case at(i, "/*"):
			// An ordinary comment, inside an executable one too, where it
			// ends only itself: MySQL runs what follows its */.
			end := strings.Index(string(r[i+2:]), "*/")
			if end < 0 {
				b.WriteString(string(r[i:]))
				return b.String()
			}
			n := len([]rune(string(r[i+2:])[:end]))
			b.WriteString(string(r[i : i+2+n+2]))
			i += 2 + n + 1
			continue
		case at(i, "-- ") || at(i, "--\t") || at(i, "--\n") || c == '#':
			for ; i < len(r) && r[i] != '\n'; i++ {
				b.WriteRune(r[i])
			}
			if i < len(r) {
				b.WriteRune('\n')
			}
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}
