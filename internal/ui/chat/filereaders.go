package chat

import (
	"slices"
	"strconv"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/safety"
	"dgopher/internal/sqltext"
)

// fileReaders are, by engine, the functions that read the server's files
// or fetch URLs: a read that calls one reads more than the connection's
// data, which the chat's agent is not to.
var fileReaders = map[db.Engine]map[string]bool{
	db.Postgres: words("pg_read_file pg_read_binary_file pg_stat_file pg_ls_dir pg_ls_logdir pg_ls_waldir pg_ls_tmpdir " +
		"pg_ls_archive_statusdir pg_ls_logicalmapdir pg_ls_logicalsnapdir pg_ls_replslotdir lo_get loread dblink dblink_open dblink_fetch"),
	db.DuckDB: words("read_text read_blob read_csv read_csv_auto read_json read_json_auto read_json_objects read_json_objects_auto " +
		"read_ndjson read_ndjson_auto read_ndjson_objects read_parquet parquet_scan parquet_metadata parquet_schema " +
		"parquet_file_metadata parquet_kv_metadata glob sniff_csv read_xlsx st_read iceberg_scan delta_scan read_avro http_get " +
		"sqlite_scan sqlite_attach postgres_scan postgres_scan_pushdown postgres_query postgres_attach mysql_scan mysql_query"),
	db.MySQL:  words("load_file"),
	db.SQLite: words("readfile fsdir load_extension"),
	db.ClickHouse: words("file url urlcluster s3 s3cluster hdfs hdfscluster remote remotesecure cluster clusterallreplicas mysql " +
		"postgresql jdbc odbc mongodb redis azureblobstorage azureblobstoragecluster gcs executable input"),
}

func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

// fileReader is how a statement reads files or URLs, "" when it does not
// that the chat knows of: a call of one of fileReaders, by its name
// quoted or not; or, on DuckDB, which reads a file named wherever a
// table goes, a string or a quoted name that looks like a file's or a
// URL's, wherever it is.
func fileReader(engine db.Engine, sql string) string {
	readers := fileReaders[engine]
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
			if name := identName(t); next == "(" && readers[name] {
				return name
			}
		}
		if engine == db.DuckDB && (t.Kind == sqltext.String || t.Kind == sqltext.QuotedIdent) && looksLikeFile(identName(t)) {
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
