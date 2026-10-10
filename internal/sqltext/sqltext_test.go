package sqltext

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func kinds(toks []Token) []Kind {
	var out []Kind
	for _, t := range toks {
		if t.Kind != Whitespace {
			out = append(out, t.Kind)
		}
	}
	return out
}

func checkCoverage(t *testing.T, src string, toks []Token) {
	t.Helper()
	rs := []rune(src)
	pos := 0
	for _, tok := range toks {
		if tok.Start != pos || tok.End <= tok.Start || tok.Text != string(rs[tok.Start:tok.End]) {
			t.Fatalf("bad token %+v at pos %d in %q", tok, pos, src)
		}
		pos = tok.End
	}
	if pos != len(rs) {
		t.Fatalf("tokens end at %d, want %d for %q", pos, len(rs), src)
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		name string
		d    Dialect
		src  string
		want []Kind
	}{
		{"line comment", Generic, "a -- c\nb", []Kind{Identifier, Comment, Identifier}},
		{"block comment", Generic, "/* x */a", []Kind{Comment, Identifier}},
		{"pg nested block", Postgres, "/* a /* b */ c */x", []Kind{Comment, Identifier}},
		{"generic not nested", Generic, "/* a /* b */ c", []Kind{Comment, Identifier}},
		{"unterminated block", Postgres, "a /* never", []Kind{Identifier, Comment}},
		{"hash comment mysql", MySQL, "a # c\nb", []Kind{Identifier, Comment, Identifier}},
		{"hash comment clickhouse", ClickHouse, "# c", []Kind{Comment}},
		{"hash operator pg", Postgres, "a #> b", []Kind{Identifier, Operator, Identifier}},
		{"string doubled quote", Generic, "'it''s'", []Kind{String}},
		{"unterminated string", Generic, "'abc", []Kind{String}},
		{"mysql backslash", MySQL, `'a\'b' x`, []Kind{String, Identifier}},
		{"mysql double quoted string", MySQL, `"a\"b"`, []Kind{String}},
		{"clickhouse backslash", ClickHouse, `'a\'b'`, []Kind{String}},
		{"pg backslash is literal", Postgres, `'a\' b`, []Kind{String, Identifier}},
		{"pg E string", Postgres, `E'a\'b' x`, []Kind{String, Identifier}},
		{"pg dollar", Postgres, "$$ a;'b $$ x", []Kind{String, Identifier}},
		{"pg dollar tag", Postgres, "$fn$ $$ ; $fn$", []Kind{String}},
		{"pg unterminated dollar", Postgres, "$x$ abc", []Kind{String}},
		{"pg quoted ident", Postgres, `"My ""T"""`, []Kind{QuotedIdent}},
		{"generic quoted ident", Generic, `"a"`, []Kind{QuotedIdent}},
		{"clickhouse quoted idents", ClickHouse, "\"a\" `b`", []Kind{QuotedIdent, QuotedIdent}},
		{"mysql backtick", MySQL, "`we``ird`", []Kind{QuotedIdent}},
		{"unterminated quoted ident", Postgres, `"abc`, []Kind{QuotedIdent}},
		{"pg positional param", Postgres, "$1 $23", []Kind{Param, Param}},
		{"question param", Generic, "?", []Kind{Param}},
		{"named param", Generic, ":name", []Kind{Param}},
		{"cast is not param", Postgres, "a::int", []Kind{Identifier, Operator, Keyword}},
		{"mysql variable", MySQL, "@v @@session.x", []Kind{Param, Param}},
		{"numbers", Generic, "1 1.5 .5 1e10 2.5E-3 0xFF", []Kind{Number, Number, Number, Number, Number, Number}},
		{"keyword case", Generic, "SeLeCt x", []Kind{Keyword, Identifier}},
		{"function stays identifier", Generic, "count(x)", []Kind{Identifier, Punct, Identifier, Punct}},
		{"pg keywords", Postgres, "returning ilike", []Kind{Keyword, Keyword}},
		{"mysql keywords", MySQL, "auto_increment engine", []Kind{Keyword, Keyword}},
		{"clickhouse keywords", ClickHouse, "prewhere final sample array join format settings engine", []Kind{Keyword, Keyword, Keyword, Keyword, Keyword, Keyword, Keyword, Keyword}},
		{"generic lacks dialect keywords", Generic, "prewhere", []Kind{Identifier}},
		{"operators", Generic, "a <= b <> c || d", []Kind{Identifier, Operator, Identifier, Operator, Identifier, Operator, Identifier}},
		{"punct", Generic, "(a, b);", []Kind{Punct, Identifier, Punct, Identifier, Punct, Punct}},
		{"unknown", Generic, "\\", []Kind{Unknown}},
		{"unicode", Generic, "'héllo — 日本' 名前", []Kind{String, Identifier}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toks := Tokenize(tt.src, tt.d)
			checkCoverage(t, tt.src, toks)
			if got := kinds(toks); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("kinds = %v, want %v (%+v)", got, tt.want, toks)
			}
		})
	}
}

func TestTokenizeNonASCIIIdent(t *testing.T) {
	for _, d := range []Dialect{Generic, Postgres, MySQL, ClickHouse, SQLite} {
		src := "SELECT 🙂x, x\u00a0y, café\u2028z FROM t"
		toks := Tokenize(src, d)
		checkCoverage(t, src, toks)
		var idents []string
		for _, tok := range toks {
			if tok.Kind == Identifier {
				idents = append(idents, tok.Text)
			}
		}
		want := []string{"🙂x", "x\u00a0y", "café\u2028z", "t"}
		if !reflect.DeepEqual(idents, want) {
			t.Fatalf("dialect %d: identifiers = %q, want %q", d, idents, want)
		}
	}
	dollar := map[Dialect]bool{Postgres: true, MySQL: true, ClickHouse: true, SQLite: true}
	for _, d := range []Dialect{Generic, Postgres, MySQL, ClickHouse, SQLite} {
		toks := Tokenize("a$b", d)
		if got := len(toks) == 1 && toks[0].Kind == Identifier; got != dollar[d] {
			t.Fatalf("dialect %d: a$b one identifier = %v, want %v (%+v)", d, got, dollar[d], toks)
		}
	}
	if toks := Tokenize("1abc", ClickHouse); len(toks) != 1 || toks[0].Kind != Identifier {
		t.Fatalf("clickhouse 1abc = %+v, want one identifier", toks)
	}
	if got := kinds(Tokenize("1abc", Postgres)); !reflect.DeepEqual(got, []Kind{Number, Identifier}) {
		t.Fatalf("postgres 1abc kinds = %v", got)
	}
}

func TestTokenizeUnicodeEscapeTokens(t *testing.T) {
	tests := []struct {
		d    Dialect
		src  string
		want []Kind
	}{
		{Postgres, `U&'d\0061t''a' x`, []Kind{String, Identifier}},
		{Postgres, `u&"x\0079""" x`, []Kind{QuotedIdent, Identifier}},
		{Postgres, `U&"a" UESCAPE '!'`, []Kind{QuotedIdent, Identifier, String}},
		{MySQL, `U&'a'`, []Kind{Identifier, Operator, String}},
	}
	for _, tt := range tests {
		toks := Tokenize(tt.src, tt.d)
		checkCoverage(t, tt.src, toks)
		if got := kinds(toks); !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("%q: kinds = %v, want %v (%+v)", tt.src, got, tt.want, toks)
		}
	}
}

func TestTokenizeRuneOffsets(t *testing.T) {
	src := "'héllo — 日本' x"
	toks := Tokenize(src, Generic)
	last := toks[len(toks)-1]
	if last.Text != "x" || last.Start != 13 || last.End != 14 {
		t.Fatalf("last token = %+v, want rune offsets 13..14", last)
	}
}

func TestKeywords(t *testing.T) {
	kw := Keywords(ClickHouse)
	has := map[string]bool{}
	for _, k := range kw {
		if k != strings.ToUpper(k) {
			t.Fatalf("keyword %q not upper-case", k)
		}
		has[k] = true
	}
	for _, k := range []string{"SELECT", "PREWHERE", "FINAL"} {
		if !has[k] {
			t.Fatalf("missing %s", k)
		}
	}
}

func TestSplit(t *testing.T) {
	tests := []struct {
		name string
		d    Dialect
		src  string
		want []string
	}{
		{"basic", Generic, "select 1; select 2;", []string{"select 1", "select 2"}},
		{"no trailing semicolon", Generic, "  select 1 ;\n\n select 2  ", []string{"select 1", "select 2"}},
		{"semicolon in string", Generic, "select ';'; x", []string{"select ';'", "x"}},
		{"semicolon in comment", Generic, "select 1 -- a;b\n; x", []string{"select 1 -- a;b", "x"}},
		{"semicolon in block comment", Generic, "select /* ; */ 1", []string{"select /* ; */ 1"}},
		{"semicolon in quoted ident", Postgres, `select "a;b"; x`, []string{`select "a;b"`, "x"}},
		{"dollar quote", Postgres, "create function f() as $$ begin; end; $$ language sql; select 1", []string{"create function f() as $$ begin; end; $$ language sql", "select 1"}},
		{"mysql backtick", MySQL, "select `a;b`; x", []string{"select `a;b`", "x"}},
		{"drop empty and comment-only", Generic, ";; -- only\n; /* c */ ; select 1", []string{"select 1"}},
		{"unterminated string", Generic, "select 'abc; def", []string{"select 'abc; def"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmts := Split(tt.src, tt.d)
			var got []string
			rs := []rune(tt.src)
			for _, s := range stmts {
				if string(rs[s.Start:s.End]) != s.Text {
					t.Fatalf("Text %q does not match range", s.Text)
				}
				got = append(got, s.Text)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSplitUnicodeOffsets(t *testing.T) {
	src := "select 'héllo — 日本'; select 2"
	stmts := Split(src, Generic)
	if len(stmts) != 2 || stmts[1].Start != 21 || stmts[1].Text != "select 2" {
		t.Fatalf("got %+v", stmts)
	}
}

func TestStatementAt(t *testing.T) {
	src := "select 1;  select 2;\n\nselect 'héllo — 日本';\n"
	// rune offsets: "select 1" 0..8, "select 2" 11..19, third starts at 22.
	tests := []struct {
		name  string
		src   string
		caret int
		want  string
		ok    bool
	}{
		{"start", src, 0, "select 1", true},
		{"inside first", src, 3, "select 1", true},
		{"end of first before semicolon", src, 8, "select 1", true},
		{"after semicolon same line", src, 9, "select 1", true},
		{"between on same line as previous end", src, 10, "select 1", true},
		{"start of second", src, 11, "select 2", true},
		{"blank line directly after previous picks previous", src, 21, "select 2", true},
		{"inside unicode statement", src, 30, "select 'héllo — 日本'", true},
		{"after final semicolon", src, 41, "select 'héllo — 日本'", true},
		{"end of text on new line falls back to previous", src, 42, "select 'héllo — 日本'", true},
		{"leading blank picks next", "\n\nselect 1", 0, "select 1", true},
		{"empty", "", 0, "", false},
		{"comments only", "-- x\n", 2, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := StatementAt(tt.src, tt.caret, Generic)
			if ok != tt.ok || got.Text != tt.want {
				t.Fatalf("got %q,%v want %q,%v", got.Text, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		d         Dialect
		src       string
		class     Class
		verb      string
		dangerous bool
	}{
		{Generic, "", Read, "", false},
		{Generic, "   \n", Read, "", false},
		{Generic, "-- only a comment", Write, "", false},
		{Generic, "select 1", Read, "SELECT", false},
		{Generic, "/* c */ -- d\n ((select 1))", Read, "SELECT", false},
		{MySQL, "show tables", Read, "SHOW", false},
		{MySQL, "describe t", Read, "DESCRIBE", false},
		{MySQL, "desc t", Read, "DESC", false},
		{Postgres, "explain select 1", Read, "EXPLAIN", false},
		{Postgres, "explain delete from t", Read, "EXPLAIN", false},
		{Postgres, "explain analyze select 1", Read, "EXPLAIN", false},
		{Postgres, "explain analyze delete from t where id = 1", Write, "DELETE", false},
		{Postgres, "explain (analyze, buffers) update t set a = 1", Write, "UPDATE", true},
		{Postgres, "explain (analyze false) update t set a = 1", Read, "EXPLAIN", false},
		{Postgres, "values (1), (2)", Read, "VALUES", false},
		{Postgres, "table users", Read, "TABLE", false},
		{Postgres, "with a as (select 1) select * from a", Read, "SELECT", false},
		{Postgres, "with recursive a(n) as (select 1), b as materialized (select 2) select * from a, b", Read, "SELECT", false},
		{Postgres, "with d as (delete from t where x returning *) select * from d", Write, "SELECT", false},
		{Postgres, "with a as (select 1) delete from t", Write, "DELETE", true},
		{Postgres, "with a as (select 1) update t set x = 1 where id in (select * from a)", Write, "UPDATE", false},
		{ClickHouse, "exists table t", Read, "EXISTS", false},
		{ClickHouse, "check table t", Read, "CHECK", false},
		{Postgres, "select * into newtable from t", Write, "SELECT", false},
		{MySQL, "select * from t into outfile '/tmp/x'", Write, "SELECT", false},
		{MySQL, "select a into @v from t", Read, "SELECT", false},
		{Postgres, "select * from t where id in (select id from u)", Read, "SELECT", false},
		{Generic, "insert into t values (1)", Write, "INSERT", false},
		{Generic, "update t set a = 1 where id = 1", Write, "UPDATE", false},
		{Generic, "update t set a = 1", Write, "UPDATE", true},
		{Generic, "update t set a = (select b from u where u.id = 1)", Write, "UPDATE", true},
		{Generic, "delete from t", Write, "DELETE", true},
		{Generic, "delete from t where id in (select id from u where x)", Write, "DELETE", false},
		{Generic, "delete from t using u where t.id = u.id", Write, "DELETE", false},
		{Postgres, "delete from t where exists (select 1) ", Write, "DELETE", false},
		{Generic, "delete from t -- where id = 1", Write, "DELETE", true},
		{Generic, "delete from t where 'x' = 'where'", Write, "DELETE", false},
		{ClickHouse, "delete from t", Write, "DELETE", true},
		{ClickHouse, "delete from t where x = 1", Write, "DELETE", false},
		{Generic, "merge into t using s on t.id = s.id when matched then delete", Write, "MERGE", false},
		{MySQL, "replace into t values (1)", Write, "REPLACE", false},
		{Generic, "upsert into t values (1)", Write, "UPSERT", false},
		{Postgres, "copy t from stdin", Write, "COPY", false},
		{MySQL, "load data infile 'x' into table t", Write, "LOAD", false},
		{Generic, "call p()", Write, "CALL", false},
		{Postgres, "do $$ begin end $$", Write, "DO", false},
		{Generic, "exec p", Write, "EXEC", false},
		{Postgres, "execute stmt(1)", Write, "EXECUTE", false},
		{ClickHouse, "optimize table t final", Write, "OPTIMIZE", false},
		{Postgres, "vacuum t", Write, "VACUUM", false},
		{Postgres, "analyze t", Write, "ANALYZE", false},
		{Postgres, "reindex table t", Write, "REINDEX", false},
		{Postgres, "cluster t", Write, "CLUSTER", false},
		{Postgres, "refresh materialized view v", Write, "REFRESH", false},
		{Postgres, "lock table t", Write, "LOCK", false},
		{Generic, "import foo", Write, "IMPORT", false},
		{MySQL, "kill 12", Write, "KILL", false},
		{ClickHouse, "system drop dns cache", Write, "SYSTEM", false},
		{Generic, "create table t (a int)", DDL, "CREATE", false},
		{Generic, "alter table t add column b int", DDL, "ALTER", false},
		{Generic, "alter table t drop column b", DDL, "ALTER", true},
		{ClickHouse, "alter table t delete where x = 1", DDL, "ALTER", true},
		{ClickHouse, "alter table t update a = 1 where x = 1", DDL, "ALTER", true},
		{Postgres, "alter table t add constraint f foreign key (a) references u on delete cascade", DDL, "ALTER", false},
		{Generic, "drop table t", DDL, "DROP", true},
		{Generic, "drop index i", DDL, "DROP", true},
		{Generic, "truncate t", DDL, "TRUNCATE", true},
		{MySQL, "rename table a to b", DDL, "RENAME", false},
		{Generic, "grant select on t to u", DDL, "GRANT", false},
		{Generic, "revoke select on t from u", DDL, "REVOKE", false},
		{Postgres, "comment on table t is 'x'", DDL, "COMMENT", false},
		{ClickHouse, "attach table t", DDL, "ATTACH", false},
		{ClickHouse, "detach table t", DDL, "DETACH", false},
		{ClickHouse, "exchange tables a and b", DDL, "EXCHANGE", false},
		{ClickHouse, "undrop table t", DDL, "UNDROP", false},
		{Generic, "begin", Transaction, "BEGIN", false},
		{MySQL, "start transaction", Transaction, "START", false},
		{Generic, "commit", Transaction, "COMMIT", false},
		{Postgres, "end", Transaction, "END", false},
		{Generic, "rollback", Transaction, "ROLLBACK", false},
		{Postgres, "abort", Transaction, "ABORT", false},
		{Generic, "savepoint s", Transaction, "SAVEPOINT", false},
		{Generic, "release savepoint s", Transaction, "RELEASE", false},
		{Postgres, "prepare transaction 'x'", Transaction, "PREPARE", false},
		{Postgres, "prepare q as select 1", Session, "PREPARE", false},
		{Postgres, "set search_path = x", Session, "SET", false},
		{Postgres, "reset all", Session, "RESET", false},
		{MySQL, "use db", Session, "USE", false},
		{Postgres, "listen c", Session, "LISTEN", false},
		{Postgres, "unlisten c", Session, "UNLISTEN", false},
		{Postgres, "discard all", Session, "DISCARD", false},
		{Postgres, "deallocate q", Session, "DEALLOCATE", false},
		{Generic, "frobnicate x", Write, "FROBNICATE", false},
		{Generic, "'weird'", Write, "'WEIRD'", false},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			a := Classify(tt.src, tt.d)
			if a.Class != tt.class || a.Verb != tt.verb || a.Dangerous != tt.dangerous {
				t.Fatalf("got %+v, want class %v verb %q dangerous %v", a, tt.class, tt.verb, tt.dangerous)
			}
			if a.Dangerous && a.Reason == "" {
				t.Fatalf("dangerous without reason")
			}
		})
	}
}

func TestClassifyReason(t *testing.T) {
	if r := Classify("delete from t", Generic).Reason; r != "DELETE without WHERE affects every row" {
		t.Fatalf("reason = %q", r)
	}
	if r := Classify("drop table t", Generic).Reason; !strings.Contains(r, "table") {
		t.Fatalf("reason = %q", r)
	}
}

func TestClassifyAuditCases(t *testing.T) {
	tests := []struct {
		d         Dialect
		src       string
		class     Class
		dangerous bool
	}{
		{Postgres, "UPDATE t SET a=1 WHERE id=1 -- c\r; DROP TABLE t", Write, true},
		{Postgres, "SELECT 1 AS 🙂$a$; DROP TABLE t; --$a$", Write, true},
		{SQLite, "SELECT [a']; DROP TABLE t; --'", Write, true},
		{Postgres, "SELECT 1; DELETE FROM t", Write, true},
		{MySQL, "TABLE t INTO OUTFILE '/tmp/x'", Write, false},
		{MySQL, "VALUES ROW(1) INTO DUMPFILE '/tmp/x'", Write, false},
		{MySQL, "DESC ANALYZE DELETE t FROM t JOIN u ON t.a=u.a", Write, true},
		{MySQL, "DESCRIBE t", Read, false},
		{MySQL, "RESET MASTER", Write, false},
		{MySQL, "RESET PERSIST", Write, false},
		{MySQL, "SET GLOBAL max_connections = 10", Write, false},
		{MySQL, "SET PERSIST x = 1", Write, false},
		{MySQL, "SET @@global.x = 1", Write, false},
		{MySQL, "SET PASSWORD FOR root = 'x'", Write, false},
		{MySQL, "START REPLICA", Write, false},
		{MySQL, "START TRANSACTION", Transaction, false},
		{Postgres, "COMMIT PREPARED 'x'", Write, false},
		{Postgres, "ROLLBACK PREPARED 'x'", Write, false},
		{Postgres, "SET ROLE r", Session, false},
		{Postgres, "SELECT pg_terminate_backend(1)", Write, false},
		{Postgres, "SELECT nextval('s')", Write, false},
		{Postgres, "WITH x AS (SELECT setval('s', 1)) SELECT * FROM x", Write, false},
		{MySQL, "SELECT GET_LOCK('a', 1)", Write, false},
		{MySQL, "SELECT SLEEP(1)", Write, false},
		{SQLite, "SELECT load_extension('x')", Write, false},
		{MySQL, "CREATE PROCEDURE p() BEGIN SELECT 1; DELETE FROM t; END", DDL, true},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			a := Classify(tt.src, tt.d)
			if a.Class != tt.class || a.Dangerous != tt.dangerous {
				t.Fatalf("got %+v, want class %v dangerous %v", a, tt.class, tt.dangerous)
			}
		})
	}
}

func TestClassifyMultiStatement(t *testing.T) {
	a := Classify("SELECT 1; DELETE FROM t WHERE id = 1", Postgres)
	if a.Class != Write || !a.Dangerous || a.Verb != "SELECT" || a.Reason != "the text holds more than one statement" {
		t.Fatalf("got %+v", a)
	}
	if a := Classify("CREATE TABLE t (a int); INSERT INTO t VALUES (1)", Postgres); a.Class != DDL || !a.Dangerous {
		t.Fatalf("DDL first: got %+v", a)
	}
	for _, src := range []string{"SELECT 1;", "SELECT 1; -- done\n", "SELECT 1;;"} {
		if a := Classify(src, Postgres); a.Class != Read || a.Dangerous {
			t.Fatalf("%q: got %+v", src, a)
		}
	}
	routine := "DELIMITER //\nCREATE PROCEDURE p() BEGIN SELECT 1; SELECT 2; END//\nDELIMITER ;"
	if a := Classify(routine, MySQL); a.Class != DDL || a.Dangerous || a.Verb != "CREATE" {
		t.Fatalf("routine: got %+v", a)
	}
}

func TestClassifySideEffectReason(t *testing.T) {
	a := Classify("SELECT pg_catalog.pg_terminate_backend(1)", Postgres)
	if a.Class != Write || a.Dangerous || a.Verb != "SELECT" || a.Reason != "calls pg_terminate_backend, which has side effects" {
		t.Fatalf("got %+v", a)
	}
	if a := Classify("EXPLAIN SELECT nextval('s')", Postgres); a.Class != Read {
		t.Fatalf("plain EXPLAIN runs nothing: got %+v", a)
	}
	if a := Classify("EXPLAIN ANALYZE SELECT nextval('s')", Postgres); a.Class != Write {
		t.Fatalf("EXPLAIN ANALYZE runs the call: got %+v", a)
	}
	if a := Classify("SELECT sleep(1)", ClickHouse); a.Class != Read {
		t.Fatalf("ClickHouse has no side-effect list: got %+v", a)
	}
}

func TestClassifyReadsNotBlocked(t *testing.T) {
	tests := []struct {
		d     Dialect
		src   string
		class Class
	}{
		{ClickHouse, "WITH 1 AS x SELECT x", Read},
		{ClickHouse, "WITH (SELECT max(a) FROM t) AS m SELECT m", Read},
		{ClickHouse, "WITH 1 AS x, y AS (SELECT 2) SELECT x, y", Read},
		{Postgres, "WITH RECURSIVE t(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM t WHERE n < 3) SEARCH DEPTH FIRST BY n SET ord SELECT * FROM t", Read},
		{Postgres, "WITH RECURSIVE t(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM t WHERE n < 3) CYCLE n SET is_cycle USING path SELECT * FROM t", Read},
		{Postgres, "WITH RECURSIVE t(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM t WHERE n < 3) CYCLE n SET is_cycle TO true DEFAULT false USING path, u AS (SELECT 2) SELECT * FROM t, u", Read},
		{Postgres, "WITH RECURSIVE t(n) AS (SELECT 1) SEARCH BREADTH FIRST BY n SET ord DELETE FROM t", Write},
		{Postgres, "DECLARE c CURSOR FOR SELECT 1", Read},
		{Postgres, "DECLARE c NO SCROLL CURSOR WITH HOLD FOR SELECT * FROM t", Read},
		{Postgres, "FETCH 10 FROM c", Read},
		{Postgres, "MOVE NEXT IN c", Read},
		{Postgres, "CLOSE c", Read},
		{SQLite, "PRAGMA table_info(t)", Read},
		{SQLite, "PRAGMA main.index_list(t)", Read},
		{SQLite, "PRAGMA journal_mode = WAL", Write},
		{SQLite, "PRAGMA optimize", Write},
		{Postgres, "SUMMARIZE t", Read},
		{Postgres, "FROM t", Read},
		{Postgres, "FROM t SELECT a WHERE b > 1", Read},
		{Postgres, "PIVOT t ON a USING sum(b)", Read},
		{Postgres, "UNPIVOT t ON a, b INTO NAME k VALUE v", Read},
		{MySQL, "CHECK TABLE t", Read},
		{MySQL, "CHECKSUM TABLE t", Read},
		{MySQL, "HELP 'x'", Read},
		{MySQL, "EXPLAIN ANALYZE FORMAT=TREE SELECT 1", Read},
		{MySQL, "EXPLAIN FORMAT=JSON DELETE FROM t", Read},
		{MySQL, "EXPLAIN ANALYZE FORMAT=TREE DELETE FROM t WHERE id = 1", Write},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			if a := Classify(tt.src, tt.d); a.Class != tt.class {
				t.Fatalf("got %+v, want class %v", a, tt.class)
			}
		})
	}
}

func TestCompletionAt(t *testing.T) {
	tests := []struct {
		name string
		d    Dialect
		src  string // '|' marks the caret and is removed
		want CompletionContext
	}{
		{"after from", Generic, "select * from |", CompletionContext{PrefixStart: 14, WantTable: true}},
		{"typing table", Generic, "select * from us|", CompletionContext{Prefix: "us", PrefixStart: 14, WantTable: true}},
		{"schema qualified table", Postgres, "select * from public.us|", CompletionContext{Prefix: "us", PrefixStart: 21, Qualifier: "public", WantTable: true}},
		{"quoted qualifier", Postgres, `select * from "My Schema".|`, CompletionContext{PrefixStart: 26, Qualifier: "My Schema", WantTable: true}},
		{"alias column", Generic, "select u.na| from users u", CompletionContext{Prefix: "na", PrefixStart: 9, Qualifier: "u",
			Tables: []TableRef{{Name: "users", Alias: "u"}}}},
		{"as alias and join", Postgres, "select | from public.users as u join orders o on o.uid = u.id", CompletionContext{PrefixStart: 7,
			Tables: []TableRef{{Schema: "public", Name: "users", Alias: "u"}, {Name: "orders", Alias: "o"}}}},
		{"comma continuation", Generic, "select * from a x, |", CompletionContext{PrefixStart: 19, WantTable: true,
			Tables: []TableRef{{Name: "a", Alias: "x"}}}},
		{"comma in select list", Generic, "select a, | from t", CompletionContext{PrefixStart: 10, Tables: []TableRef{{Name: "t"}}}},
		{"left join", Generic, "select * from a left join |", CompletionContext{PrefixStart: 26, WantTable: true, Tables: []TableRef{{Name: "a"}}}},
		{"update", Generic, "update |", CompletionContext{PrefixStart: 7, WantTable: true}},
		{"insert into", Generic, "insert into |", CompletionContext{PrefixStart: 12, WantTable: true}},
		{"describe", MySQL, "describe |", CompletionContext{PrefixStart: 9, WantTable: true}},
		{"where clause", Generic, "select * from t where a|", CompletionContext{Prefix: "a", PrefixStart: 22, Tables: []TableRef{{Name: "t"}}}},
		{"other statement ignored", Generic, "select * from a; select * from b where |", CompletionContext{PrefixStart: 39, Tables: []TableRef{{Name: "b"}}}},
		{"unicode before caret", Generic, "select 'héllo — 日本', x.c| from t x", CompletionContext{Prefix: "c", PrefixStart: 23, Qualifier: "x", Tables: []TableRef{{Name: "t", Alias: "x"}}}},
		{"mysql backticks", MySQL, "select * from `my db`.`t 1` as `a b` where `a b`.|", CompletionContext{PrefixStart: 49, Qualifier: "a b",
			Tables: []TableRef{{Schema: "my db", Name: "t 1", Alias: "a b", SchemaQuoted: true, Quoted: true}}}},
		{"inside string", Generic, "select 'fro|m' from t", CompletionContext{Prefix: "fro", PrefixStart: 8}},
		{"sqlite brackets", SQLite, "SELECT [t].|", CompletionContext{PrefixStart: 11, Qualifier: "t"}},
		{"sqlite bracket table", SQLite, "select * from [my t] where [my t].|", CompletionContext{PrefixStart: 34, Qualifier: "my t",
			Tables: []TableRef{{Name: "my t", Quoted: true}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			caret := strings.Index(tt.src, "|")
			caret = len([]rune(tt.src[:caret]))
			src := strings.Replace(tt.src, "|", "", 1)
			got := CompletionAt(src, caret, tt.d)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

// significantTexts returns non-whitespace token texts, upper-casing keywords.
func significantTexts(src string, d Dialect) []string {
	var out []string
	for _, t := range Tokenize(src, d) {
		switch t.Kind {
		case Whitespace:
		case Keyword:
			out = append(out, strings.ToUpper(t.Text))
		default:
			out = append(out, t.Text)
		}
	}
	return out
}

var formatInputs = []struct {
	d   Dialect
	src string
}{
	{Generic, "select a, b, count(*) from users u left join orders o on o.uid=u.id where a=1 and b between 1 and 2 or c in (select 1) group by a order by b limit 3 offset 4"},
	{Postgres, "with x as (select 1) select * from x union all select 2; update t set a=1, b=2 where id=$1 returning *"},
	{Postgres, "insert into t (a) values ('it''s; -- not a comment') on conflict do update set a = excluded.a"},
	{Postgres, "select $$ a ; b $$, E'x\\'y', \"Quoted Ident\" from t -- trailing comment\n where a::int > 1 /* block */"},
	{MySQL, "select `a`, \"s\" # mysql comment\nfrom t where x = @v and y = ?"},
	{ClickHouse, "select * from t final prewhere a = 1 where b = 2 settings max_threads = 1 format JSON"},
	{Generic, "select a - -1, 'unterminated"},
	{Generic, "select 1;; -- trailing\n"},
	{Generic, "-- leading\nselect 1 -- end\n;"},
	{Generic, "select*from t cross join u natural join v full outer join w on true"},
	{Postgres, "select 'héllo — 日本' as \"名前\""},
	{Generic, "/* unterminated"},
	{Generic, ""},
}

func TestFormatPreservesTokens(t *testing.T) {
	for _, in := range formatInputs {
		out := Format(in.src, in.d)
		want := significantTexts(in.src, in.d)
		got := significantTexts(out, in.d)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("token sequence changed\ninput:  %q\noutput: %q\nwant %q\ngot  %q", in.src, out, want, got)
		}
	}
}

func TestFormatLayout(t *testing.T) {
	got := Format("select a, b from t join u on t.id = u.id where x = 1 and y = 2 order by a; delete from t", Generic)
	want := "SELECT\n  a,\n  b\nFROM t\nJOIN u\n  ON t.id = u.id\nWHERE x = 1\n  AND y = 2\nORDER BY a;\n\nDELETE FROM t"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func FuzzTokenize(f *testing.F) {
	for _, in := range formatInputs {
		f.Add(in.src, int(in.d))
	}
	f.Add("$a$ $b$ /* /* */ '\\", 1)
	f.Fuzz(func(t *testing.T, src string, d int) {
		dialect := Dialect(((d % 5) + 5) % 5)
		toks := Tokenize(src, dialect)
		checkCoverage(t, src, toks)
		Split(src, dialect)
		Classify(src, dialect)
		n := len([]rune(src))
		CompletionAt(src, n/2, dialect)
		StatementAt(src, n/2, dialect)
		if got, want := significantTexts(Format(src, dialect), dialect), significantTexts(src, dialect); !reflect.DeepEqual(got, want) {
			t.Fatalf("Format changed tokens for %q", src)
		}
	})
}

func TestFormatKeepsCaseSensitiveNames(t *testing.T) {
	cases := []struct {
		d         Dialect
		src, want string
	}{
		{ClickHouse, "select date, key, comment from events where date > 1", "SELECT\n  date,\n  key,\n  comment\nFROM events\nWHERE date > 1"},
		{MySQL, "select * from comment", "SELECT\n  *\nFROM comment"},
		{Postgres, "select date, key, comment from events", "SELECT\n  DATE,\n  KEY,\n  COMMENT\nFROM events"},
		{MySQL, "select first, last, format, filter, settings, final, array, global from t",
			"SELECT\n  first,\n  last,\n  format,\n  filter,\n  settings,\n  final,\n  array,\n  global\nFROM t"},
	}
	for _, c := range cases {
		if got := Format(c.src, c.d); got != c.want {
			t.Errorf("%v %q:\ngot  %q\nwant %q", c.d, c.src, got, c.want)
		}
	}
	// Keywords that are common names keep their case; only whitespace changes.
	src := "SELECT first, last, format, filter, settings, final, array, global, nulls, semi, anti, asof, paste, qualify, offset, all " +
		"FROM (SELECT 1 AS first, 1 AS last, 1 AS format, 1 AS filter, 1 AS settings, 1 AS final, 1 AS array, 1 AS global, " +
		"1 AS nulls, 1 AS semi, 1 AS anti, 1 AS asof, 1 AS paste, 1 AS qualify, 1 AS offset, 1 AS all)"
	for _, d := range []Dialect{ClickHouse, MySQL} {
		got := Format(src, d)
		if strings.Join(strings.Fields(got), " ") != src {
			t.Errorf("%v: names changed: %q", d, got)
		}
	}
}

func TestFormatJoinModifiers(t *testing.T) {
	for src, want := range map[string]string{
		"select * from a left semi join b on a.x = b.x": "\nLEFT semi JOIN b\n",
		"select * from a array join b":                  "\narray JOIN b",
		"select * from a asof join b on a.x = b.x":      "\nasof JOIN b\n",
	} {
		if got := Format(src, ClickHouse); !strings.Contains(got, want) {
			t.Errorf("%q: got %q, want it to contain %q", src, got, want)
		}
	}
	if got := Format("select * from a positional join b", Postgres); !strings.Contains(got, "\nPOSITIONAL JOIN b") {
		t.Errorf("positional: got %q", got)
	}
}

func TestFormatNeverCreatesComment(t *testing.T) {
	cases := []struct {
		d   Dialect
		src string
	}{
		{MySQL, "--seleCt"},
		{MySQL, "select 1--1 from t"},
		{MySQL, "select 1 - -1 from t"},
		{Generic, "select 1 - -1 from t"},
		{ClickHouse, "select 1 #x from t"},
		{Generic, "select 2 / *x from t"},
	}
	nonKeyword := func(src string, d Dialect) (texts []string, comments int) {
		for _, tk := range Tokenize(src, d) {
			switch tk.Kind {
			case Comment:
				comments++
			case Whitespace, Keyword:
			default:
				texts = append(texts, tk.Text)
			}
		}
		return
	}
	for _, c := range cases {
		out := Format(c.src, c.d)
		wantT, wantC := nonKeyword(c.src, c.d)
		gotT, gotC := nonKeyword(out, c.d)
		if !reflect.DeepEqual(gotT, wantT) || gotC > wantC {
			t.Errorf("%v %q -> %q: tokens %q (want %q), comments %d (want %d)", c.d, c.src, out, gotT, wantT, gotC, wantC)
		}
		if again := Format(out, c.d); again != out {
			t.Errorf("%v %q not idempotent: %q -> %q", c.d, c.src, out, again)
		}
	}
}

type textKind struct {
	Text string
	Kind Kind
}

func significantTokens(src string, d Dialect) []textKind {
	var out []textKind
	for _, tok := range Tokenize(src, d) {
		if tok.Kind != Whitespace {
			out = append(out, textKind{tok.Text, tok.Kind})
		}
	}
	return out
}

func checkTokens(t *testing.T, tests []struct {
	d    Dialect
	src  string
	want []textKind
}) {
	t.Helper()
	for _, tt := range tests {
		checkCoverage(t, tt.src, Tokenize(tt.src, tt.d))
		if got := significantTokens(tt.src, tt.d); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Tokenize(%q, %v) = %+v, want %+v", tt.src, tt.d, got, tt.want)
		}
	}
}

func TestTokenizePostgresOperators(t *testing.T) {
	checkTokens(t, []struct {
		d    Dialect
		src  string
		want []textKind
	}{
		{Postgres, "data ? 'k'", []textKind{{"data", Identifier}, {"?", Operator}, {"'k'", String}}},
		{Postgres, "data ?| x", []textKind{{"data", Identifier}, {"?|", Operator}, {"x", Identifier}}},
		{Postgres, "data ?& x", []textKind{{"data", Identifier}, {"?&", Operator}, {"x", Identifier}}},
		{Postgres, "data #- '{a}'", []textKind{{"data", Identifier}, {"#-", Operator}, {"'{a}'", String}}},
		{Postgres, "a !~* 'x'", []textKind{{"a", Identifier}, {"!~*", Operator}, {"'x'", String}}},
		{Postgres, "b @@ q", []textKind{{"b", Identifier}, {"@@", Operator}, {"q", Identifier}}},
		{Postgres, "7 // 2", []textKind{{"7", Number}, {"//", Operator}, {"2", Number}}},
		{Postgres, "2 ** 3", []textKind{{"2", Number}, {"**", Operator}, {"3", Number}}},
		{Postgres, "7 /* c */ 2", []textKind{{"7", Number}, {"/* c */", Comment}, {"2", Number}}},
		{Postgres, "7 //* c */ 2", []textKind{{"7", Number}, {"/", Operator}, {"/* c */", Comment}, {"2", Number}}},
		{Postgres, "a #--c", []textKind{{"a", Identifier}, {"#", Operator}, {"--c", Comment}}},
		{Postgres, "x = $1", []textKind{{"x", Identifier}, {"=", Operator}, {"$1", Param}}},
		{MySQL, "x = ?", []textKind{{"x", Identifier}, {"=", Operator}, {"?", Param}}},
		{SQLite, "x = ?", []textKind{{"x", Identifier}, {"=", Operator}, {"?", Param}}},
	})
}

func TestTokenizePrefixedStrings(t *testing.T) {
	checkTokens(t, []struct {
		d    Dialect
		src  string
		want []textKind
	}{
		{Postgres, "x'ff'", []textKind{{"x'ff'", String}}},
		{Postgres, "B'01'", []textKind{{"B'01'", String}}},
		{Generic, "n'abc'", []textKind{{"n'abc'", String}}},
		{SQLite, "X'ff'", []textKind{{"X'ff'", String}}},
		{MySQL, "x'ff'", []textKind{{"x'ff'", String}}},
		{MySQL, `N'a\'b'`, []textKind{{`N'a\'b'`, String}}},
		{Postgres, "n'a''b'", []textKind{{"n'a''b'", String}}},
		{Postgres, "max'a'", []textKind{{"max", Identifier}, {"'a'", String}}},
	})
}

func TestTokenizeNumbers(t *testing.T) {
	checkTokens(t, []struct {
		d    Dialect
		src  string
		want []textKind
	}{
		{Postgres, "0b101", []textKind{{"0b101", Number}}},
		{Postgres, "0o17", []textKind{{"0o17", Number}}},
		{Postgres, "1_000", []textKind{{"1_000", Number}}},
		{Postgres, "0x1F", []textKind{{"0x1F", Number}}},
		{Postgres, "0x_FF", []textKind{{"0x_FF", Number}}},
		{Postgres, "1_000.5_5", []textKind{{"1_000.5_5", Number}}},
		{Postgres, "1e", []textKind{{"1", Number}, {"e", Identifier}}},
		{Postgres, "1__0", []textKind{{"1", Number}, {"__0", Identifier}}},
		{MySQL, "0b101", []textKind{{"0b101", Number}}},
		{MySQL, "1_000", []textKind{{"1", Number}, {"_000", Identifier}}},
		{Generic, "0b101", []textKind{{"0", Number}, {"b101", Identifier}}},
	})
}

func TestCompletionLineCommentEnd(t *testing.T) {
	for _, tt := range []struct {
		d   Dialect
		src string
	}{
		{Generic, "select * from t -- from "},
		{Postgres, "select * from t -- from"},
		{MySQL, "select * from t # from "},
	} {
		got := CompletionAt(tt.src, len([]rune(tt.src)), tt.d)
		if got.WantTable || len(got.Tables) > 0 {
			t.Errorf("CompletionAt(%q) = %+v, want empty context", tt.src, got)
		}
	}
	src := "select * from t /* x */"
	if got := CompletionAt(src, len(src), Generic); len(got.Tables) == 0 {
		t.Errorf("after block comment: got %+v, want tables", got)
	}
}

func TestCompletionJoinLateral(t *testing.T) {
	for _, src := range []string{
		"select * from a join lateral ",
		"select * from a cross join lateral ",
		"select * from a left join lateral ",
		"select * from lateral ",
	} {
		got := CompletionAt(src, len(src), Postgres)
		if !got.WantTable {
			t.Errorf("CompletionAt(%q).WantTable = false, want true", src)
		}
	}
	got := CompletionAt("select | from a join lateral f x on true", 7, Postgres)
	want := []TableRef{{Name: "a"}, {Name: "f", Alias: "x"}}
	if !reflect.DeepEqual(got.Tables, want) {
		t.Errorf("tables = %+v, want %+v", got.Tables, want)
	}
}

// A routine whose body closes before the end of its statement holds more
// than the routine, whatever the splitter made of it.
func TestClassifyRoutineBodyBackstop(t *testing.T) {
	for _, c := range []struct {
		d         Dialect
		src       string
		dangerous bool
	}{
		{SQLite, "CREATE TRIGGER t AFTER INSERT ON v BEGIN SELECT 1; END DELETE FROM victim", true},
		{Postgres, "CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1; END SELECT 2", true},
		// "begin" here is a language name, so the body opened there closes
		// at "FROM end" and the DROP sits inside it.
		{Postgres, "CREATE FUNCTION f() RETURNS int LANGUAGE begin BEGIN ATOMIC SELECT 1; END; DROP TABLE victim; SELECT * FROM end", true},
		{SQLite, "CREATE TRIGGER t AFTER INSERT ON v BEGIN SELECT 1; END", false},
		{MySQL, "CREATE PROCEDURE p() BEGIN END", false},
		{MySQL, "CREATE PROCEDURE p() lbl: BEGIN SELECT 1; END lbl", false},
		{Postgres, "CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1; END;", false},
	} {
		if a := Classify(c.src, c.d); a.Dangerous != c.dangerous {
			t.Errorf("%q: got %+v, want dangerous %v", c.src, a, c.dangerous)
		}
	}
}

func TestClassifyUnicodeEscapedCalls(t *testing.T) {
	for _, c := range []struct {
		src   string
		class Class
	}{
		{`SELECT U&"pg_terminate_backend"(1)`, Write},
		{`SELECT U&"pg\005fterminate_backend"(1)`, Write},
		{`SELECT u&"pg\+00005fterminate_backend"(1)`, Write},
		// A custom escape character is not decoded: any U& call may be anything.
		{`SELECT U&"pg!005fterminate_backend" UESCAPE '!' (1)`, Write},
		{`SELECT U&"caf\00e9"(1)`, Read},
		{`SELECT "pg_terminate_""backend"(1)`, Read},
	} {
		if a := Classify(c.src, Postgres); a.Class != c.class {
			t.Errorf("%s: got %+v, want class %v", c.src, a, c.class)
		}
	}
}

func TestDecodeQuoted(t *testing.T) {
	for _, c := range []struct {
		d         Dialect
		src, want string
	}{
		{Postgres, `E'transaction_\562ead_only'`, "transaction_read_only"},
		{Postgres, `E'\x72ead'`, "read"},
		{Postgres, `E'\xc3\xa9\303\251'`, "éé"},
		{Postgres, `E'a\u00e9\n'`, "aé\n"},
		{Postgres, `E'it''s'`, "it's"},
		{Postgres, `U&"a\0062\\"`, `ab\`},
		{Postgres, `"a""b"`, `a"b`},
		{Postgres, `'plain'`, "plain"},
		{ClickHouse, `'read\x6fnly'`, "readonly"},
		{ClickHouse, "`read\\x6fnly`", "readonly"},
		{ClickHouse, "`a\\`b`", "a`b"},
		{ClickHouse, `"a\"b\\c\n"`, "a\"b\\c\n"},
		{SQLite, `[a b]`, "a b"},
		{MySQL, "`a``b`", "a`b"},
	} {
		if got := DecodeQuoted(c.src, c.d); got != c.want {
			t.Errorf("%v %s: got %q, want %q", c.d, c.src, got, c.want)
		}
	}
}

func TestClassifySetDefaultRole(t *testing.T) {
	for src, class := range map[string]Class{
		"SET DEFAULT ROLE ALL TO 'u'@'%'": Write,
		"set default role none to u":      Write,
		"SET /* c */ DEFAULT ROLE r TO u": Write,
		"SET ROLE r":                      Session,
		"SET ROLE DEFAULT":                Session,
	} {
		if a := Classify(src, MySQL); a.Class != class {
			t.Errorf("%s: got %+v, want class %v", src, a, class)
		}
	}
}

// A name the splitter took for a body's BEGIN, closed by a lone END, hides
// what lies between: a destructive statement in a routine's text keeps
// its danger, whether or not the body was real.
func TestClassifyRoutineBodyKeepsInnerDanger(t *testing.T) {
	for _, c := range []struct {
		d   Dialect
		sql string
	}{
		{Postgres, "CREATE FUNCTION f() AS TABLE SELECT * FROM t begin JOIN u ON true; DROP TABLE v; END"},
		{MySQL, "CREATE PROCEDURE p() BEGIN DELETE FROM t; END"},
		{SQLite, "CREATE TRIGGER g AFTER INSERT ON a BEGIN DROP TABLE b; END"},
	} {
		if a := Classify(c.sql, c.d); !a.Dangerous {
			t.Errorf("%q: not dangerous: %+v", c.sql, a)
		}
	}
	if a := Classify("CREATE PROCEDURE p() BEGIN UPDATE t SET a = 1 WHERE id = 2; END", MySQL); a.Dangerous {
		t.Errorf("a routine with a safe body is dangerous: %+v", a)
	}
}

// ClickHouse's FORMAT and SETTINGS start a clause only where one can be:
// as a column name they stay in the select list.
func TestFormatClickHouseClauseWordsAsNames(t *testing.T) {
	cases := map[string]string{
		"select a, format, settings from t":                    "SELECT\n  a,\n  format,\n  settings\nFROM t",
		"select format from t":                                 "SELECT\n  format\nFROM t",
		"select a as settings from t":                          "SELECT\n  a AS settings\nFROM t",
		"select a from t settings max_threads = 1 format JSON": "SELECT\n  a\nFROM t\nsettings max_threads = 1\nformat JSON",
	}
	for src, want := range cases {
		if got := Format(src, ClickHouse); got != want {
			t.Errorf("%q:\ngot  %q\nwant %q", src, got, want)
		}
	}
}

// The word being completed keeps a '$' inside a name, and leaves out a
// leading '$' sigil.
func TestCompletionPrefixDollar(t *testing.T) {
	cases := []struct {
		src    string
		d      Dialect
		prefix string
	}{
		{"SELECT * FROM x$y", SQLite, "x$y"},
		{"SELECT $na", SQLite, "na"},
		{"SELECT * FROM a$", Postgres, "a$"},
	}
	for _, c := range cases {
		if got := CompletionAt(c.src, len([]rune(c.src)), c.d).Prefix; got != c.prefix {
			t.Errorf("%q: prefix %q, want %q", c.src, got, c.prefix)
		}
	}
}

// Statements that lose rows or values without a DROP or a WHERE left out
// are dangerous too; a SELECT that locks its rows is as a write.
func TestClassifyLosesData(t *testing.T) {
	for _, c := range []struct {
		d         Dialect
		sql       string
		class     Class
		dangerous bool
	}{
		{Postgres, "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", Write, true},
		{Postgres, "WITH m AS (DELETE FROM t RETURNING *) INSERT INTO archive SELECT * FROM m", Write, true},
		{Postgres, "WITH u AS (UPDATE t SET a = 1 WHERE 1 = 1 RETURNING id) SELECT count(*) FROM u", Write, true},
		{Postgres, "WITH d AS (DELETE FROM t WHERE id = 3 RETURNING *) SELECT * FROM d", Write, false},
		{Postgres, "WITH x AS (SELECT 1) SELECT * FROM x", Read, false},
		{Postgres, "CREATE OR REPLACE TABLE t AS SELECT 1", DDL, true},
		{Postgres, "CREATE OR REPLACE VIEW v AS SELECT 1", DDL, false},
		{ClickHouse, "CREATE OR REPLACE TABLE t (a Int8) ENGINE = Memory", DDL, true},
		{ClickHouse, "REPLACE TABLE t (a Int8) ENGINE = Memory", DDL, true},
		{MySQL, "REPLACE INTO t VALUES (1)", Write, false},
		{ClickHouse, "ALTER TABLE t CLEAR COLUMN c IN PARTITION 2024", DDL, true},
		{ClickHouse, "ALTER TABLE t MODIFY TTL d + INTERVAL 1 DAY", DDL, true},
		{ClickHouse, "ALTER TABLE t REPLACE PARTITION 2024 FROM u", DDL, true},
		{ClickHouse, "ALTER TABLE t MOVE PARTITION 2024 TO TABLE u", DDL, true},
		{ClickHouse, "ALTER TABLE t DETACH PARTITION 2024", DDL, true},
		{ClickHouse, "ALTER TABLE t ADD COLUMN c Int8", DDL, false},
		{ClickHouse, "ALTER TABLE t FREEZE PARTITION 2024", DDL, false},
		{MySQL, "ALTER TABLE t TRUNCATE PARTITION p0", DDL, true},
		{Postgres, "ALTER DEFAULT PRIVILEGES IN SCHEMA s GRANT SELECT, TRUNCATE ON TABLES TO app", DDL, false},
		{Postgres, "ALTER TABLE t ADD COLUMN truncate int", DDL, false},
		{ClickHouse, "ALTER TABLE t MOVE PARTITION 2024 TO DISK 'cold'", DDL, false},
		{ClickHouse, "ALTER TABLE t MOVE PART 'all_1_1_0' TO VOLUME 'slow'", DDL, false},
		{ClickHouse, "ALTER TABLE t MATERIALIZE TTL", DDL, true},
		{Postgres, "CREATE OR REPLACE TEMP TABLE t AS SELECT 1", DDL, true},
		{Postgres, "SELECT * FROM t WHERE id = 1 FOR UPDATE", Write, false},
		{Postgres, "SELECT * FROM t FOR NO KEY UPDATE SKIP LOCKED", Write, false},
		{Postgres, "SELECT * FROM t FOR KEY SHARE", Write, false},
		{MySQL, "SELECT * FROM t FOR SHARE", Write, false},
		{MySQL, "SELECT * FROM t LOCK IN SHARE MODE", Write, false},
		{MySQL, "SELECT * FROM t FOR SYSTEM_TIME AS OF NOW()", Read, false},
		{Postgres, "SELECT * FROM t WHERE id IN (SELECT id FROM u)", Read, false},
		{Postgres, "SELECT * FROM t WHERE id IN (SELECT id FROM u FOR UPDATE)", Write, false},
		{Postgres, "WITH l AS (SELECT id FROM u FOR UPDATE) SELECT * FROM l", Write, false},
		{Postgres, "SELECT substring(name FROM 1 FOR 2) FROM t", Read, false},
		{Postgres, "MOVE NEXT IN c", Read, false},
		{ClickHouse, "MOVE USER u TO disk", Write, false},
	} {
		a := Classify(c.sql, c.d)
		if a.Class != c.class || a.Dangerous != c.dangerous {
			t.Errorf("%s: class %v, dangerous %v (%s)", c.sql, a.Class, a.Dangerous, a.Reason)
		}
	}
	for _, fn := range []string{"pg_try_advisory_xact_lock(1)", "pg_drop_replication_slot('s')", "pg_logical_slot_get_changes('s', NULL, NULL)", "pg_stat_reset()", "lo_truncate(3, 0)"} {
		if a := Classify("SELECT "+fn, Postgres); a.Class != Write {
			t.Errorf("%s: class %v", fn, a.Class)
		}
	}
}

// Code quoted as a string, a DO block's or a routine's body, keeps the
// danger of the statements it holds, and of the strings it EXECUTEs.
func TestClassifyDollarBodies(t *testing.T) {
	for _, c := range []struct {
		d         Dialect
		sql       string
		class     Class
		verb      string
		dangerous bool
	}{
		{Postgres, "DO $$ BEGIN DROP TABLE t; END $$", Write, "DO", true},
		{Postgres, "DO $x$ BEGIN TRUNCATE t; END $x$", Write, "DO", true},
		{Postgres, "DO 'BEGIN DELETE FROM t; END'", Write, "DO", true},
		{Postgres, "DO E'BEGIN DELETE FROM t; END'", Write, "DO", true},
		{Postgres, "DO LANGUAGE plpgsql $$ BEGIN UPDATE t SET a = 1; END $$", Write, "DO", true},
		{Postgres, "DO $$ BEGIN DROP TABLE t; END $$ LANGUAGE plpgsql", Write, "DO", true},
		{Postgres, "DO $$ DECLARE r record; BEGIN FOR r IN SELECT 1 LOOP DELETE FROM t; END LOOP; END $$", Write, "DO", true},
		{Postgres, "CREATE FUNCTION f() RETURNS void AS $$ BEGIN DELETE FROM t; END $$ LANGUAGE plpgsql", DDL, "CREATE", true},
		{Postgres, "CREATE OR REPLACE PROCEDURE p() LANGUAGE sql AS 'TRUNCATE t'", DDL, "CREATE", true},
		{Postgres, "CREATE FUNCTION f() RETURNS void LANGUAGE sql AS $f$ DELETE FROM t WHERE true $f$", DDL, "CREATE", true},
		{Postgres, "DO $$ BEGIN EXECUTE 'DROP TABLE t'; END $$", Write, "DO", true},
		{Postgres, "DO $$ BEGIN IF x THEN EXECUTE 'DROP TABLE ' || quote_ident(t); END IF; END $$", Write, "DO", true},
		{Postgres, "CREATE FUNCTION f() RETURNS trigger AS $$ BEGIN EXECUTE 'TRUNCATE t' USING x; RETURN NULL; END $$ LANGUAGE plpgsql", DDL, "CREATE", true},
		{Postgres, "DO $a$ BEGIN EXECUTE $b$ DROP TABLE t $b$; END $a$", Write, "DO", true},
		{Postgres, "DO $a$ BEGIN DO $b$ BEGIN DELETE FROM t; END $b$; END $a$", Write, "DO", true},
		{Postgres, "DO $a$ BEGIN CREATE FUNCTION g() RETURNS void AS $g$ BEGIN DROP TABLE t; END $g$ LANGUAGE plpgsql; END $a$", Write, "DO", true},
		{Postgres, "DO $$ BEGIN END $$", Write, "DO", false},
		{Postgres, "DO $$ BEGIN DELETE FROM t WHERE id = 1; END $$", Write, "DO", false},
		{Postgres, "DO $$ BEGIN RAISE NOTICE 'DROP TABLE t'; END $$", Write, "DO", false},
		{Postgres, "DO $$ BEGIN EXECUTE 'DELETE FROM ' || quote_ident(t) || ' WHERE id = $1' USING 1; END $$", Write, "DO", false},
		{Postgres, "CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END $$ LANGUAGE plpgsql", DDL, "CREATE", false},
		{Postgres, "CREATE FUNCTION f() RETURNS void AS $$ BEGIN INSERT INTO t VALUES (1) ON CONFLICT (id) DO UPDATE SET a = 1; END $$ LANGUAGE plpgsql", DDL, "CREATE", false},
		{Postgres, "CREATE FUNCTION f(a text DEFAULT 'drop table t') RETURNS text AS 'SELECT a' LANGUAGE sql", DDL, "CREATE", false},
		// Code in another language is not SQL: Perl's delete empties no table.
		{Postgres, "CREATE FUNCTION f() RETURNS void AS $$ my %h; delete $h{x}; $$ LANGUAGE plperl", DDL, "CREATE", false},
		{Postgres, "DO LANGUAGE plv8 $$ delete obj.x $$", Write, "DO", false},
		// MySQL's DO evaluates expressions: a string is only a value.
		{MySQL, "DO 'DROP TABLE t'", Write, "DO", false},
	} {
		a := Classify(c.sql, c.d)
		if a.Class != c.class || a.Verb != c.verb || a.Dangerous != c.dangerous {
			t.Errorf("%s: got %+v, want class %v verb %s dangerous %v", c.sql, a, c.class, c.verb, c.dangerous)
		}
		if a.Dangerous && a.Reason == "" {
			t.Errorf("%s: dangerous without a reason", c.sql)
		}
	}

	// nest wraps inner in levels of DO blocks, each quoted with its own tag.
	nest := func(levels int, inner string) string {
		s := inner
		for i := range levels {
			tag := "$t" + strconv.Itoa(i) + "$"
			s = "DO " + tag + " BEGIN " + s + "; END " + tag
		}
		return s
	}
	if a := Classify(nest(3, "DROP TABLE t"), Postgres); !a.Dangerous {
		t.Errorf("a DROP three blocks down: %+v", a)
	}
	for _, levels := range []int{3, maxCodeDepth} {
		if a := Classify(nest(levels, "SELECT 1"), Postgres); a.Dangerous {
			t.Errorf("a SELECT %d blocks down: %+v", levels, a)
		}
	}
	// Past the depth cap the code is not read, and counts as dangerous.
	for _, levels := range []int{maxCodeDepth + 1, 5000} {
		if a := Classify(nest(levels, "SELECT 1"), Postgres); !a.Dangerous || !strings.Contains(a.Reason, "too deep") {
			t.Errorf("%d nested blocks: %+v", levels, a)
		}
	}
}

// PREPARE keeps the danger of the statement it prepares.
func TestClassifyPrepare(t *testing.T) {
	for _, c := range []struct {
		d         Dialect
		sql       string
		dangerous bool
	}{
		{Postgres, "PREPARE p AS DELETE FROM t", true},
		{Postgres, "PREPARE p (int) AS UPDATE t SET a = $1", true},
		{Postgres, "PREPARE p (int, text) AS DELETE FROM t WHERE id = $1", false},
		{Postgres, "PREPARE p AS SELECT 1", false},
		{MySQL, "PREPARE s FROM 'DROP TABLE t'", true},
		{MySQL, `PREPARE s FROM "DELETE FROM t"`, true},
		{MySQL, "PREPARE s FROM 'DELETE FROM t WHERE id = ?'", false},
		{MySQL, `PREPARE s FROM 'DELETE FROM t WHERE name = \'it\'\'s\''`, false},
		{MySQL, "PREPARE s FROM @sql", false},
	} {
		a := Classify(c.sql, c.d)
		if a.Class != Session || a.Verb != "PREPARE" || a.Dangerous != c.dangerous {
			t.Errorf("%s: got %+v, want a session PREPARE, dangerous %v", c.sql, a, c.dangerous)
		}
		if a.Dangerous && a.Reason == "" {
			t.Errorf("%s: dangerous without a reason", c.sql)
		}
	}
}

// A rule keeps the danger of its action.
func TestClassifyCreateRule(t *testing.T) {
	for _, c := range []struct {
		sql       string
		dangerous bool
	}{
		{"CREATE RULE r AS ON INSERT TO t DO INSTEAD DELETE FROM u", true},
		{"CREATE OR REPLACE RULE r AS ON UPDATE TO t DO ALSO TRUNCATE u", true},
		{"CREATE RULE r AS ON DELETE TO t DO (UPDATE u SET a = 1)", true},
		{"CREATE RULE r AS ON DELETE TO t DO (DELETE FROM u WHERE 1 = 1)", true},
		{"CREATE RULE r AS ON DELETE TO t WHERE OLD.a > 1 DO INSTEAD DELETE FROM u WHERE u.id = OLD.id", false},
		{"CREATE RULE r AS ON INSERT TO t DO INSTEAD NOTHING", false},
	} {
		a := Classify(c.sql, Postgres)
		if a.Class != DDL || a.Verb != "CREATE" || a.Dangerous != c.dangerous {
			t.Errorf("%s: got %+v, want a CREATE, dangerous %v", c.sql, a, c.dangerous)
		}
	}
}

// A MERGE's WHEN … THEN DELETE or UPDATE is one of its actions, not a
// statement of its own, in a routine's body as anywhere.
func TestBeginAtomicMergeNotDangerous(t *testing.T) {
	for _, c := range []struct {
		sql       string
		dangerous bool
	}{
		{"CREATE FUNCTION f() RETURNS void LANGUAGE sql BEGIN ATOMIC MERGE INTO t USING s ON t.id = s.id WHEN MATCHED THEN DELETE; END", false},
		{"CREATE FUNCTION f() RETURNS void LANGUAGE sql BEGIN ATOMIC MERGE INTO t USING s ON t.id = s.id WHEN MATCHED THEN UPDATE SET a = s.a WHEN NOT MATCHED THEN INSERT VALUES (s.id); END", false},
		{"CREATE FUNCTION f() RETURNS void AS $$ BEGIN MERGE INTO t USING s ON t.id = s.id WHEN MATCHED THEN DELETE; END $$ LANGUAGE plpgsql", false},
		{"CREATE FUNCTION f() RETURNS void LANGUAGE sql BEGIN ATOMIC MERGE INTO t USING s ON t.id = s.id WHEN MATCHED THEN DELETE; DELETE FROM t; END", true},
	} {
		if a := Classify(c.sql, Postgres); a.Dangerous != c.dangerous {
			t.Errorf("%s: got %+v, want dangerous %v", c.sql, a, c.dangerous)
		}
	}
}

// DuckDB settings that name a file write to it: they are writes.
func TestClassifyDuckDBFileSettings(t *testing.T) {
	for src, class := range map[string]Class{
		"SET log_query_path = '/tmp/q.log'":        Write,
		"SET profiling_output TO '/tmp/p.json'":    Write,
		"SET SESSION profile_output = '/tmp/p'":    Write,
		"SET GLOBAL temp_directory = '/tmp/spill'": Write,
		`SET "log_query_path" = '/tmp/q.log'`:      Write,
		"SET threads = 4":                          Session,
		"SET search_path = x":                      Session,
	} {
		if a := Classify(src, Postgres); a.Class != class {
			t.Errorf("%s: got %+v, want class %v", src, a, class)
		}
	}
}

// Code quoted in code is read once a level, however many words before it
// could start a statement: hostile text of a few hundred bytes classifies
// at once, and text read more than that counts as dangerous, unread.
func TestClassifyNestedCodeTime(t *testing.T) {
	// nest puts levels of template into one another: each level's TAG is a
	// dollar quote's tag of its own, and its INNER the level below it, down
	// to inner.
	nest := func(levels int, template, inner string) string {
		s := inner
		for i := range levels {
			tag := "$t" + strconv.Itoa(i) + "$"
			s = strings.ReplaceAll(strings.ReplaceAll(template, "TAG", tag), "INNER", s)
		}
		return s
	}
	thenElse := "DO TAG BEGIN IF a " + strings.Repeat("THEN DO ELSE DO ", 6) + "THEN INNER; END IF; END TAG"
	routine := "CREATE FUNCTION f() RETURNS void AS TAG BEGIN " + strings.Repeat("IF a THEN CREATE FUNCTION f() ", 6) + "IF a THEN INNER; END IF; END TAG LANGUAGE plpgsql"
	for _, c := range []struct {
		name, sql string
		dangerous bool
		reason    string
	}{
		{"8 DOs a level", nest(8, strings.Repeat("DO ", 8)+"TAG INNER TAG", "SELECT 1"), false, ""},
		{"12 DOs a level", nest(8, strings.Repeat("DO ", 12)+"TAG INNER TAG", "SELECT 1"), false, ""},
		{"THEN and ELSE before DO", nest(7, thenElse, "SELECT 1"), false, ""},
		{"THEN and ELSE before DO, a DROP inside", nest(7, thenElse, "DROP TABLE t"), true, "DROP"},
		{"CREATEs before AS", nest(7, routine, "SELECT 1"), false, ""},
		{"CREATEs before AS, a DROP inside", nest(7, routine, "DROP TABLE t"), true, "DROP"},
		// A rule's action is read as the rule's and as what follows DO:
		// twice a level, past the budget.
		{"a rule's DO block", nest(maxCodeDepth, "CREATE RULE r AS ON INSERT TO t DO DO TAG INNER TAG", "SELECT 1"), true, "too much code"},
	} {
		start := time.Now()
		a := Classify(c.sql, Postgres)
		if took := time.Since(start); took > 50*time.Millisecond {
			t.Errorf("%s (%d bytes): took %v", c.name, len(c.sql), took)
		}
		if a.Dangerous != c.dangerous || !strings.Contains(a.Reason, c.reason) {
			t.Errorf("%s: got %+v, want dangerous %v for %q", c.name, a, c.dangerous, c.reason)
		}
	}

	// Legitimate code, however much of it, stays within the budget.
	body := strings.Repeat("EXECUTE 'SELECT 1'; ", 5000)
	if a := Classify("DO $$ BEGIN "+body+"END $$", Postgres); a.Dangerous {
		t.Errorf("5000 EXECUTEs: %+v", a)
	}
	// Each level's EXECUTEs are read a level deeper than its body.
	if a := Classify(nest(maxCodeDepth-1, "DO TAG BEGIN "+body+"INNER; END TAG", "SELECT 1"), Postgres); a.Dangerous {
		t.Errorf("5000 EXECUTEs a level, %d levels: %+v", maxCodeDepth-1, a)
	}
}
