package chat

import (
	"slices"
	"strconv"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/safety"
	"dgopher/internal/sqltext"
)

// fileReader is how a statement reads files or URLs, "" when it does not
// that the chat knows of: a call of one of the engine's functions reading
// the server's files or fetching URLs, which the chat's agent is not to
// read through, by its name quoted or not; or, on an engine that reads a
// file named wherever a table goes, as DuckDB, a string or a quoted name
// that looks like a file's or a URL's, wherever it is.
func fileReader(engine db.Engine, sql string) string {
	var readers []string
	namesFiles := false
	if r, ok := db.SQL(engine); ok {
		readers, namesFiles = r.FileReadingFunctions(), r.FileNamesInStrings()
	}
	var toks []sqltext.Token
	for _, t := range sqltext.Tokenize(sql, safety.Dialect(engine)) {
		if t.Kind != sqltext.Whitespace && t.Kind != sqltext.Comment {
			toks = append(toks, t)
		}
	}
	for i, t := range toks {
		next := ""
		if i+1 < len(toks) {
			next = toks[i+1].Text
		}
		switch t.Kind {
		case sqltext.Identifier, sqltext.Keyword, sqltext.QuotedIdent:
			if name := identName(t); next == "(" && slices.Contains(readers, name) {
				return name
			}
		}
		if namesFiles && (t.Kind == sqltext.String || t.Kind == sqltext.QuotedIdent) && looksLikeFile(identName(t)) {
			return "a file or URL named as " + t.Text
		}
	}
	return ""
}

// identName is a name as the server reads it, lower case: its quotes and
// a U& prefix gone, its \XXXX escapes read.
func identName(t sqltext.Token) string {
	s := t.Text
	if len(s) > 2 && (s[0] == 'U' || s[0] == 'u') && s[1] == '&' {
		s = s[2:]
	}
	if len(s) >= 2 && strings.ContainsRune("\"'`", rune(s[0])) && s[len(s)-1] == s[0] {
		s = s[1 : len(s)-1]
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 < len(s) {
			if r, err := strconv.ParseUint(s[i+1:i+5], 16, 32); err == nil {
				b.WriteRune(rune(r))
				i += 4
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return strings.ToLower(b.String())
}

// fileExtensions are those of the files DuckDB reads as tables.
var fileExtensions = []string{".csv", ".tsv", ".txt", ".parquet", ".json", ".jsonl", ".ndjson", ".db", ".duckdb", ".sqlite",
	".sqlite3", ".xlsx", ".arrow", ".avro", ".gz", ".zst", ".bz2"}

// looksLikeFile reports whether a name is a path, a URL or a data file's.
func looksLikeFile(s string) bool {
	if strings.ContainsAny(s, "/\\") || strings.Contains(s, "://") || strings.HasPrefix(s, "~") {
		return true
	}
	return slices.ContainsFunc(fileExtensions, func(ext string) bool { return strings.HasSuffix(s, ext) })
}
