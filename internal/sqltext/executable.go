package sqltext

import (
	"math"
	"slices"
	"strconv"
	"strings"
)

// UnwrapExecutable turns MySQL's executable comments, /*! … */, /*!80000
// … */, MariaDB's /*M! … */ and TiDB's /*T! … */ and /*T![feature] … */,
// into the code they hold, which the server
// runs: classified as comments, they would hide a DELETE or an INTO
// OUTFILE from the safety policy, or DDL from a reader of what commits.
// Strings and ordinary comments are copied as they are, so a quote inside
// them misleads nothing.
func UnwrapExecutable(sql string) string {
	return unwrapExecutable(sql, defaultQuoting(MySQL), runsEverything)
}

// executableServer is a server reading executable comments: MariaDB, which
// runs /*M! … */ too, TiDB, which runs /*T! … */ too, except those whose
// feature list it lacks, in lacking, or MySQL, for which both are ordinary
// comments, and its version, which a versioned comment must not exceed to
// run. everything runs every one, as UnwrapExecutable reads.
type executableServer struct {
	everything bool
	mariadb    bool
	tidb       bool
	lacking    map[string]bool
	version    int
}

var runsEverything = executableServer{everything: true}

// runs reports whether the server runs an executable comment needing
// version, 0 for none, which mariadbOnly marks /*M! … */.
func (s executableServer) runs(mariadbOnly bool, version int) bool {
	switch {
	case s.everything:
		return true
	case version > s.version:
		return false
	}
	// MariaDB skips /*! … */ of MySQL 5.7 and later (versions 50700 to
	// 99999), whose syntax it may not have.
	return !s.mariadb || mariadbOnly || version < 50700 || version > 99999
}

// maxExecutableVersions caps how many server versions a statement's
// executable comments are read at; one naming more counts as dangerous.
const maxExecutableVersions = 8

// maxExecutableFeatureLists caps how many distinct TiDB feature lists,
// /*T![feature, …] … */, a statement's comments are read with, each run and
// skipped in every combination; one naming more counts as dangerous.
const maxExecutableFeatureLists = 4

// executableReadings are the texts a server may run of sql, its strings
// read as q reads them: every executable comment unwrapped first, then as
// MariaDB, TiDB and MySQL of each version a comment names, and older than
// all, run them, TiDB lacking each combination of the feature lists its
// comments name. Which run changes only at those versions and lists, so
// these are all the readings. tooMany reports more versions than
// maxExecutableVersions, of which only the oldest and newest are read, or
// more lists than maxExecutableFeatureLists, of which all or none are
// lacked.
func executableReadings(sql string, q quoting) (readings []string, tooMany bool) {
	readings = []string{unwrapExecutable(sql, q, runsEverything)}
	if !hasExecutable(sql) {
		return readings, false
	}
	// Every version and feature list written after an opener, inside a
	// string or not: a skipped comment can move where strings end.
	versions := []int{0}
	var lists []string
	for i := 0; i < len(sql); i++ {
		if strings.HasPrefix(sql[i:], "/*T!") {
			if list, _, ok := featureList(sql, i+len("/*T!")); ok && !slices.Contains(lists, list) {
				lists = append(lists, list)
			}
			continue
		}
		if !strings.HasPrefix(sql[i:], "/*!") && !strings.HasPrefix(sql[i:], "/*M!") {
			continue
		}
		if v, ok := executableVersion(sql, i); ok {
			versions = append(versions, v)
		}
	}
	slices.Sort(versions)
	versions = slices.Compact(versions)
	if len(versions)-1 > maxExecutableVersions {
		tooMany = true
		versions = []int{versions[0], versions[len(versions)-1]}
	}
	lackings := []map[string]bool{nil}
	if len(lists) > maxExecutableFeatureLists {
		tooMany = true
		all := map[string]bool{}
		for _, l := range lists {
			all[l] = true
		}
		lackings = append(lackings, all)
	} else {
		for mask := 1; mask < 1<<len(lists); mask++ {
			lacking := map[string]bool{}
			for j, l := range lists {
				if mask&(1<<j) != 0 {
					lacking[l] = true
				}
			}
			lackings = append(lackings, lacking)
		}
	}
	add := func(s executableServer) {
		for _, v := range versions {
			s.version = v
			if text := unwrapExecutable(sql, q, s); !slices.Contains(readings, text) {
				readings = append(readings, text)
			}
		}
	}
	add(executableServer{mariadb: true})
	for _, lacking := range lackings {
		add(executableServer{tidb: true, lacking: lacking})
	}
	add(executableServer{})
	return readings, tooMany
}

// featureList reads the feature list of the TiDB comment whose /*T! ends
// at sql[i], as TiDB reads it: "[name, …]" without spaces, its names of
// letters, digits, _, $ and bytes of other characters. It returns the
// list, where it ends, and false when there is none, where TiDB runs the
// comment from sql[i].
func featureList(sql string, i int) (list string, end int, ok bool) {
	if i >= len(sql) || sql[i] != '[' {
		return "", i, false
	}
	name := false
	for j := i + 1; j < len(sql); j++ {
		switch c := sql[j]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '$', c >= 0x80:
			name = true
		case c == ',' && name:
			name = false
		case c == ']' && name:
			return sql[i+1 : j], j + 1, true
		default:
			return "", i, false
		}
	}
	return "", i, false
}

// hasExecutable reports whether sql may hold an executable comment.
func hasExecutable(sql string) bool {
	return strings.Contains(sql, "/*!") || strings.Contains(sql, "/*M!") || strings.Contains(sql, "/*T!")
}

// executableVersion reads the version of the executable comment opening
// at sql[i]: 0 for none, and false when it names none.
func executableVersion(sql string, i int) (int, bool) {
	j := i + len("/*!")
	if sql[i+2] == 'M' {
		j++
	}
	end := j
	for end < len(sql) && sql[end] >= '0' && sql[end] <= '9' {
		end++
	}
	if end == j {
		return 0, false
	}
	v, err := strconv.Atoi(sql[j:end])
	if err != nil {
		return math.MaxInt, true
	}
	return v, true
}

// unwrapExecutable is UnwrapExecutable with strings read as q reads them,
// which decides whether a /*! inside one is code, as server s runs the
// comments: one it skips becomes a space.
func unwrapExecutable(sql string, q quoting, s executableServer) string {
	if !hasExecutable(sql) {
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
		case at(i, "/*M!") && !s.everything && !s.mariadb, at(i, "/*T!") && !s.everything && !s.tidb:
			// Another server's ordinary comment, to its first */.
			b.WriteByte(' ')
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			i += 2 + end + 1
			continue
		case at(i, "/*T!"):
			b.WriteByte(' ')
			list, end, ok := featureList(sql, i+len("/*T!"))
			if ok && s.lacking[list] {
				// TiDB's ordinary comment, to its first */.
				stop := strings.Index(sql[end:], "*/")
				if stop < 0 {
					return b.String()
				}
				i = end + stop + 1
				continue
			}
			inExec = true
			i = end - 1
			continue
		case at(i, "/*!") || at(i, "/*M!"):
			version, _ := executableVersion(sql, i)
			b.WriteByte(' ')
			if !s.runs(sql[i+2] == 'M', version) {
				// Skipped as a comment, which may hold one comment: the
				// servers' lexers nest one level there.
				i = skippedCommentEnd(sql, i) - 1
				continue
			}
			inExec = true
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

// skippedCommentEnd returns where the comment opening at sql[i] ends when
// the server skips it: after its */, one /* … */ inside it passed over
// whole, quotes not read; len(sql) when it does not end.
func skippedCommentEnd(sql string, i int) int {
	nested := false
	for j := i + 2; j+1 < len(sql); j++ {
		switch {
		case !nested && sql[j] == '/' && sql[j+1] == '*':
			nested = true
			j++
		case sql[j] == '*' && sql[j+1] == '/':
			if !nested {
				return j + 2
			}
			nested = false
			j++
		}
	}
	return len(sql)
}
