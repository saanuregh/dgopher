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
	b.Grow(len(sql))
	inExec := false
	at := func(i int, prefix string) bool { return strings.HasPrefix(sql[i:], prefix) }
	escapes := func(quote byte) bool { return q.backslashes && (quote == '\'' || quote == '"' && !q.ansiQuotes) }
	// Every byte that matters is ASCII; those of other characters are copied.
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			// A string, or a quoted name, to its end.
			b.WriteByte(c)
			for i++; i < len(sql); i++ {
				b.WriteByte(sql[i])
				if sql[i] == '\\' && escapes(c) && i+1 < len(sql) {
					i++
					b.WriteByte(sql[i])
					continue
				}
				if sql[i] == c {
					break
				}
			}
			continue
		case at(i, "/*!") || at(i, "/*M!"):
			inExec = true
			b.WriteByte(' ')
			i += 2
			if sql[i] == 'M' {
				i++
			}
			for i+1 < len(sql) && sql[i+1] >= '0' && sql[i+1] <= '9' {
				i++ // the version it needs
			}
			continue
		case inExec && at(i, "*/"):
			inExec = false
			b.WriteByte(' ')
			i++
			continue
		case at(i, "/*"):
			// An ordinary comment, inside an executable one too, where it
			// ends only itself: MySQL runs what follows its */.
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				b.WriteString(sql[i:])
				return b.String()
			}
			b.WriteString(sql[i : i+2+end+2])
			i += 2 + end + 1
			continue
		case at(i, "-- ") || at(i, "--\t") || at(i, "--\n") || c == '#':
			for ; i < len(sql) && sql[i] != '\n'; i++ {
				b.WriteByte(sql[i])
			}
			if i < len(sql) {
				b.WriteByte('\n')
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
