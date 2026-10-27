package sqltext

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestOracleQuoting(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []Kind
	}{
		{"string with doubled quote", "'it''s' x", []Kind{String, Identifier}},
		{"no backslash escapes", `'a\' b`, []Kind{String, Identifier}},
		{"q-quote brackets", "q'[a'b]' x", []Kind{String, Identifier}},
		{"q-quote braces", "Q'{a'}' x", []Kind{String, Identifier}},
		{"q-quote parentheses end at the first )'", "q'(a'))' x", []Kind{String, Identifier}},
		{"q-quote angle brackets", "q'<a'>' x", []Kind{String, Identifier}},
		{"q-quote any character", "q'!a'!' q'#b'#' q'xabcx'", []Kind{String, String, String}},
		{"q-quote multi-byte delimiter", "q'ßa'ß' x", []Kind{String, Identifier}},
		{"q-quote closing bracket opens", "q']a]' x", []Kind{String, Identifier}},
		{"q-quote holds ; and --", "q'[a; -- b]' x", []Kind{String, Identifier}},
		{"nq and uq quotes", "nq'[a'b]' NQ'(c)' uq'<d>' Uq'!e!'", []Kind{String, String, String, String}},
		{"unterminated q-quote", "q'[a'b", []Kind{String}},
		{"q with a space is no q-quote", "q' a '", []Kind{Identifier, String}},
		{"N and U strings", "N'a''b' n'c' u'd' U'e'", []Kind{String, String, String, String}},
		{"x and b are names before a string", "x'41' b'1'", []Kind{Identifier, String, Identifier, String}},
		{"quoted name", `"a b" x`, []Kind{QuotedIdent, Identifier}},
		{"quoted name has no doubled quote", `"a""b"`, []Kind{QuotedIdent, QuotedIdent}},
		{"line comment", "a -- c\nb", []Kind{Identifier, Comment, Identifier}},
		{"line comment runs past a lone CR", "-- c\rb", []Kind{Comment}},
		{"block comments do not nest", "/* a /* b */ c */x", []Kind{Comment, Identifier, Operator, Operator, Identifier}},
		{"hint is a comment", "select /*+ full(t) */ 1", []Kind{Keyword, Comment, Number}},
		{"bind variables", ":id :1 :new", []Kind{Param, Param, Param}},
		{"names with $ and #", "a$b#c x$ y#", []Kind{Identifier, Identifier, Identifier}},
		{"$ and # start no name", "$a #b", []Kind{Unknown, Identifier, Operator, Identifier}},
		{"database link", "dual@lnk", []Kind{Identifier, Operator, Identifier}},
		{"substitution variable is text", "&x &&y", []Kind{Operator, Identifier, Operator, Identifier}},
		{"numbers", "1e10 1.5f 2d .5 1. 1.e2 1E+3 1e1d 7F 8D", []Kind{Number, Number, Number, Number, Number, Number, Number, Number, Number, Number}},
		{"a number's suffix binds before a name", "1from", []Kind{Number, Identifier}},
		{"no hex literal", "0x10", []Kind{Number, Identifier}},
		{"number then alias", "1x", []Kind{Number, Identifier}},
		{"assignment and arrow", "a:=1 b=>2", []Kind{Identifier, Operator, Operator, Number, Identifier, Operator, Operator, Number}},
		{"label", "<<l>>", []Kind{Operator, Identifier, Operator}},
		{"full-width punctuation ends a name", "a（b）", []Kind{Identifier, Operator, Identifier, Operator}},
		{"full-width letters are a name", "ａＢ＿c", []Kind{Identifier}},
		{"${var}", "${v}", []Kind{Unknown, Punct, Identifier, Punct}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toks := Tokenize(tt.src, Oracle)
			checkCoverage(t, tt.src, toks)
			if got := kinds(toks); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v (%+v)", got, tt.want, toks)
			}
		})
	}
	if toks := Tokenize("1from", Oracle); toks[0].Text != "1f" {
		t.Errorf("1from: number %q, want 1f", toks[0].Text)
	}

	decoded := []struct{ text, want string }{
		{"'it''s'", "it's"},
		{"q'[a'b]'", "a'b"},
		{"Q'{a}'", "a"},
		{"q'(a'))'", "a')"},
		{"q'<a>'", "a"},
		{"q'!a'!'", "a'"},
		{"q'ßaß'", "a"},
		{"q'[a]]'", "a]"},
		{"nq'[x]'", "x"},
		{"UQ'[y]'", "y"},
		{"N'a''b'", "a'b"},
		{"u'd'", "d"},
		{`"a b"`, "a b"},
		{"q'[unterminated", "unterminated"},
	}
	for _, d := range decoded {
		if got := DecodeQuoted(d.text, Oracle); got != d.want {
			t.Errorf("DecodeQuoted(%q) = %q, want %q", d.text, got, d.want)
		}
	}

	needsQuote := []struct {
		name string
		want bool
	}{
		{"EMP", false}, {"EMP_1$#", false}, {"NAME", false},
		{"emp", true}, {"Emp", true}, {"1EMP", true}, {"_EMP", true}, {"$A", true}, {"A B", true}, {"", true},
		{"É", true}, // folds to itself, but only ASCII names are left bare
		// Reserved words, of 19c and of 23ai.
		{"DATE", true}, {"SELECT", true}, {"LEVEL", true}, {"ROWNUM", true}, {"USER", true}, {"EXCEPT", true},
	}
	for _, n := range needsQuote {
		if got := NeedsQuoteIn(n.name, Oracle); got != n.want {
			t.Errorf("NeedsQuoteIn(%q, Oracle) = %v, want %v", n.name, got, n.want)
		}
	}
	for _, name := range []string{"emp", "EMP", "date", "a b"} {
		if got, want := NeedsQuoteIn(name, Postgres), NeedsQuote(name); got != want {
			t.Errorf("NeedsQuoteIn(%q, Postgres) = %v, NeedsQuote %v", name, got, want)
		}
	}
	for _, f := range []struct {
		name string
		d    Dialect
		want string
	}{{"emp", Oracle, "EMP"}, {"Émile", Oracle, "ÉMILE"}, {"emp", Postgres, "emp"}, {"Emp", TSQL, "Emp"}} {
		if got := FoldUnquoted(f.name, f.d); got != f.want {
			t.Errorf("FoldUnquoted(%q, %v) = %q, want %q", f.name, f.d, got, f.want)
		}
	}
}

// Oracle's SQL parser reads these as spaces, as TestIntegrationLexingSpaces
// will check on the server (task 9.3); a sweep of the Basic Multilingual
// Plane on Oracle 23ai Free found no others. Its PL/SQL compiler reads
// only the ASCII tab, line feed, carriage return and space so, and the
// rest as errors.
var oracleSpaces = []rune{0x00, 0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x20, 0x3000}

func TestOracleSpaces(t *testing.T) {
	spaced := []Kind{Keyword, Number}
	for _, r := range oracleSpaces {
		t.Run(fmt.Sprintf("U+%04X", r), func(t *testing.T) {
			src := "SELECT" + string(r) + "1"
			toks := Tokenize(src, Oracle)
			checkCoverage(t, src, toks)
			if got := kinds(toks); !reflect.DeepEqual(got, spaced) {
				t.Errorf("Tokenize(%q) kinds %v, want %v", src, got, spaced)
			}
			if got := kinds(Tokenize("SELECT a"+string(r)+"FROM t", Oracle)); !reflect.DeepEqual(got, []Kind{Keyword, Identifier, Keyword, Identifier}) {
				t.Errorf("a name does not end at U+%04X: %v", r, got)
			}
			if a := Classify("SELECT * FROM t"+string(r)+"FOR UPDATE", Oracle); a.Class != Write {
				t.Errorf("FOR UPDATE after U+%04X classified %v, want Write", r, a.Class)
			}
		})
	}
	for _, r := range []rune{0x01, 0x1F, 0x7F, 0x85, 0xA0, 0x1680, 0x2000, 0x200B, 0x2028, 0x202F, 0xFEFF} {
		if toks := Tokenize("SELECT 1 AS a"+string(r), Oracle); toks[len(toks)-1].Kind == Whitespace {
			t.Errorf("U+%04X read as a space; Oracle does not", r)
		}
	}

	// Oracle's SQL parser reads these full-width forms as the ASCII
	// punctuation they stand for, as in "IN (1）FOR UPDATE", which locks
	// rows: a statement holding one outside quotes counts as a dangerous
	// write.
	for _, src := range []string{
		"SELECT * FROM dual WHERE 1 IN (1）FOR UPDATE",
		"SELECT （1） FROM dual",
		"SELECT d．dummy FROM dual d",
		"DELETE FROM t WHERE 1＝1",
		"SELECT 1 FROM dual；",
		"SELECT 'a' ｜｜ 'b' FROM dual",
		"SELECT 1 FROM dual＠lnk",
		"SELECT 1 FROM dual WHERE 1 ！= 2 AND 1 ＾= 2 AND 1 ＜ 2 AND 2 ＞ 1",
		"SELECT 1 ＋ 1, 2 － 1, 2 ＊ 2, 4 ／ 2, ：x FROM dual",
		"SELECT 1 ＆ 2 FROM dual",
	} {
		a := Classify(src, Oracle)
		if a.Class != Write || !a.Dangerous || !strings.Contains(a.Reason, "full-width") {
			t.Errorf("Classify(%q, Oracle) = %+v, want a dangerous write for its full-width punctuation", src, a)
		}
	}
	for _, src := range []string{
		"SELECT '（' FROM dual",
		"SELECT q'[；]' FROM dual",
		`SELECT 1 AS "（" FROM dual`,
		"SELECT 1 FROM dual -- （",
		"SELECT 1 /* ； */ FROM dual",
		"SELECT 1 AS ａＢ＿c FROM dual",
		"SELECT 1 AS a\u3000FROM dual",
		"SELECT 1 AS a\x00FROM dual",
	} {
		if a := Classify(src, Oracle); a.Class != Read || a.Dangerous {
			t.Errorf("Classify(%q, Oracle) = %+v, want a read", src, a)
		}
	}
}

func TestOracleSplit(t *testing.T) {
	blank, semicolon := BlankLineAndSemicolon, SemicolonOnly
	tests := []struct {
		name string
		mode DelimiterMode
		src  string
		want []string
	}{
		{"semicolons split", blank, "select 1 from dual; select 2 from dual;", []string{"select 1 from dual", "select 2 from dual"}},
		{"slash line separates", blank, "select 1 from dual\n/\nselect 2 from dual", []string{"select 1 from dual", "select 2 from dual"}},
		{"slash after a semicolon runs nothing again", blank, "insert into t values (1);\n/\nselect 1 from dual", []string{"insert into t values (1)", "select 1 from dual"}},
		{"slash with spaces, CRLF and a comment", blank, "select 1 from dual\r\n  /  -- run\r\nselect 2 from dual", []string{"select 1 from dual", "select 2 from dual"}},
		{"slash first", blank, "/\nselect 1 from dual", []string{"select 1 from dual"}},
		{"slash alone", blank, "/", nil},
		{"slash in a string", blank, "select 'a\n/\nb' from dual", []string{"select 'a\n/\nb' from dual"}},
		{"slash in a q-quote", blank, "select q'[a\n/\nb]' from dual", []string{"select q'[a\n/\nb]' from dual"}},
		{"slash in a block comment", blank, "select 1 /*\n/\n*/ from dual", []string{"select 1 /*\n/\n*/ from dual"}},
		{"slash in a quoted name", blank, "select \"a\n/\nb\" from dual", []string{"select \"a\n/\nb\" from dual"}},
		{"slash after code on its line", blank, "select 10 /\n2 from dual", []string{"select 10 /\n2 from dual"}},
		{"slash before code on its line", blank, "select 10\n/ 2 from dual", []string{"select 10\n/ 2 from dual"}},
		{"two slashes are no separator", blank, "select 1\n//\nfrom dual", []string{"select 1\n//\nfrom dual"}},
		{"block comment running past the slash line", blank, "select 1\n/ /*\n*/\nfrom dual", []string{"select 1\n/ /*\n*/\nfrom dual"}},
		{"anonymous block keeps its semicolons and blank lines", blank,
			"begin\n  delete from t where id = 1;\n\n  commit;\nend;\n/\nselect 1 from dual",
			[]string{"begin\n  delete from t where id = 1;\n\n  commit;\nend;", "select 1 from dual"}},
		{"declare block", semicolon,
			"declare\n  n number;\nbegin\n  select count(*) into n from t;\nend;\n/",
			[]string{"declare\n  n number;\nbegin\n  select count(*) into n from t;\nend;"}},
		{"labeled block", blank, "<<outer>>\nbegin\n  null;\nend;\n/\nselect 1 from dual", []string{"<<outer>>\nbegin\n  null;\nend;", "select 1 from dual"}},
		{"a block runs to the end without a slash", blank, "begin\n  null;\nend;\n\nselect 1 from dual;", []string{"begin\n  null;\nend;\n\nselect 1 from dual;"}},
		{"leading comment before a block", blank, "-- run me\nbegin null; end;\n/", []string{"begin null; end;"}},
		{"procedure", blank,
			"create or replace procedure p (a in number) as\nbegin\n  update t set x = a where id = 1;\n\n  commit;\nend p;\n/\nexec p(1);",
			[]string{"create or replace procedure p (a in number) as\nbegin\n  update t set x = a where id = 1;\n\n  commit;\nend p;", "exec p(1)"}},
		{"editionable function", semicolon,
			"create or replace editionable function f return number is begin return 1; end;\n/\nselect f from dual",
			[]string{"create or replace editionable function f return number is begin return 1; end;", "select f from dual"}},
		{"package and body", semicolon,
			"create package pk as\n  procedure p;\nend pk;\n/\ncreate package body pk as\n  procedure p is begin null; end;\nend pk;\n/",
			[]string{"create package pk as\n  procedure p;\nend pk;", "create package body pk as\n  procedure p is begin null; end;\nend pk;"}},
		{"trigger", semicolon,
			"create or replace trigger tr before insert on t for each row\nbegin\n  :new.id := s.nextval;\nend;\n/",
			[]string{"create or replace trigger tr before insert on t for each row\nbegin\n  :new.id := s.nextval;\nend;"}},
		{"type and type body", semicolon,
			"create type pt as object (x number);\n/\ncreate type body pt as member function f return number is begin return x; end; end;\n/",
			[]string{"create type pt as object (x number);", "create type body pt as member function f return number is begin return x; end; end;"}},
		{"noneditionable library", semicolon,
			"create or replace noneditionable library l as '/x.so';\n/\nselect 1 from dual",
			[]string{"create or replace noneditionable library l as '/x.so';", "select 1 from dual"}},
		{"java source", semicolon,
			"create or replace and compile java source named \"H\" as public class H { public static int f() { return 1; } }\n/\nselect 1 from dual",
			[]string{"create or replace and compile java source named \"H\" as public class H { public static int f() { return 1; } }", "select 1 from dual"}},
		{"mle module", semicolon,
			"create mle module m language javascript as export function f() { return 1; }\n/\nselect 1 from dual",
			[]string{"create mle module m language javascript as export function f() { return 1; }", "select 1 from dual"}},
		{"mle env is a statement", semicolon, "create mle env e; select 1 from dual", []string{"create mle env e", "select 1 from dual"}},
		{"unit header with comments", semicolon,
			"create /* x */ or replace -- y\n procedure p as begin null; end;\n/",
			[]string{"create /* x */ or replace -- y\n procedure p as begin null; end;"}},
		{"WITH FUNCTION", semicolon,
			"with function f return number is begin return 1; end;\nselect f from dual\n/\nselect 2 from dual",
			[]string{"with function f return number is begin return 1; end;\nselect f from dual", "select 2 from dual"}},
		{"WITH FUNCTION in a subquery", semicolon,
			"select /*+ with_plsql */ x from (with function f return number is begin return 1; end; select f x from dual)\n/\nselect 2 from dual",
			[]string{"select /*+ with_plsql */ x from (with function f return number is begin return 1; end; select f x from dual)", "select 2 from dual"}},
		{"a CTE named function", semicolon,
			"with function as (select 1 x from dual) select x from function; select 2 from dual",
			[]string{"with function as (select 1 x from dual) select x from function", "select 2 from dual"}},
		{"tables and views are no units", semicolon,
			"create table t (a number); create view v as select a from t; select 1 from dual",
			[]string{"create table t (a number)", "create view v as select a from t", "select 1 from dual"}},
		{"blank line splits SQL", blank, "select 1 from dual\n\nselect 2 from dual", []string{"select 1 from dual", "select 2 from dual"}},
		{"semicolon only keeps blank lines", semicolon, "select 1\n\nfrom dual", []string{"select 1\n\nfrom dual"}},
		{"CASE keeps a blank line", blank,
			"select case when 1 = 1 then 'a'\n\nelse 'b' end from dual\n\nselect 2 from dual",
			[]string{"select case when 1 = 1 then 'a'\n\nelse 'b' end from dual", "select 2 from dual"}},
		{"parentheses keep a blank line", blank, "select (1\n\n+ 2) from dual", []string{"select (1\n\n+ 2) from dual"}},
		{"BEGIN and END as names", semicolon,
			"select 1 as end from dual; drop table x; select 1 begin from dual",
			[]string{"select 1 as end from dual", "drop table x", "select 1 begin from dual"}},
		{"a routine named begin ends at its slash", semicolon,
			"create function begin return number is begin return 1; end;\n/\ndrop table victim;\nselect 1 as end from dual",
			[]string{"create function begin return number is begin return 1; end;", "drop table victim", "select 1 as end from dual"}},
		{"slash line inside a block ends it", semicolon, "begin\n  x := 10\n/\n2;\nend;", []string{"begin\n  x := 10", "2", "end"}},
		{"q-quote holding a semicolon", semicolon, "select q'[a;b]' from dual; select 2 from dual", []string{"select q'[a;b]' from dual", "select 2 from dual"}},
		{"full-width semicolon splits nothing", semicolon, "select 1 from dual；select 2 from dual", []string{"select 1 from dual；select 2 from dual"}},
		{"EXEC lines", semicolon, "exec p;\nexec q", []string{"exec p", "exec q"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := splitTexts(tt.src, Oracle, tt.mode); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %q\nwant %q", got, tt.want)
			}
		})
	}
}

// The editor runs the statement at the caret: inside a PL/SQL unit, the
// whole unit.
func TestOracleStatementAtIsTheUnit(t *testing.T) {
	src := "begin\n  update t set a = 1 where id = 1;\n\n  commit;\nend;\n/\nselect 2 from dual"
	for _, caret := range []int{0, strings.Index(src, "update"), strings.Index(src, "commit")} {
		st, ok := StatementAtWith(src, caret, Oracle, SplitOptions{})
		if !ok || st.Text != "begin\n  update t set a = 1 where id = 1;\n\n  commit;\nend;" {
			t.Fatalf("caret %d: got %q, %v", caret, st.Text, ok)
		}
	}
	if st, _ := StatementAtWith(src, len(src), Oracle, SplitOptions{}); st.Text != "select 2 from dual" {
		t.Fatalf("after the slash: got %q", st.Text)
	}
}

func TestOracleClassify(t *testing.T) {
	tests := []struct {
		src       string
		class     Class
		verb      string
		dangerous bool
	}{
		// Reads.
		{"select * from t", Read, "SELECT", false},
		{"select /*+ full(t) */ * from t", Read, "SELECT", false},
		{"with c as (select 1 a from dual) select a from c", Read, "SELECT", false},
		{"with c (n) as (select 1 from dual union all select n + 1 from c where n < 3) search depth first by n set o cycle n set y to 'Y' default 'N' select * from c", Read, "SELECT", false},
		{"select * from t as of timestamp systimestamp - interval '1' hour", Read, "SELECT", false},
		{"select level from dual connect by level <= 3", Read, "SELECT", false},
		{"select * from t order by a fetch first 10 rows only", Read, "SELECT", false},
		{"select * from t@lnk", Read, "SELECT", false},
		{"select dbms_metadata.get_ddl('TABLE', 'T') from dual", Read, "SELECT", false},
		{"select * from table(dbms_xplan.display)", Read, "SELECT", false},
		{"select * from table(dbms_xplan.display_cursor(null, null, 'ALLSTATS LAST'))", Read, "SELECT", false},
		{"select sys.dbms_xplan.display_cursor() from dual", Read, "SELECT", false},
		{"select dbms_random.value from dual", Read, "SELECT", false},
		{"select dbms_random.value(1, 10) from dual", Read, "SELECT", false},
		{"select dbms_transaction.local_transaction_id from dual", Read, "SELECT", false},
		{"select utl_raw.cast_to_varchar2(hextoraw('41')) from dual", Read, "SELECT", false},
		{"select sys_context('userenv', 'current_schema') from dual", Read, "SELECT", false},
		{"select * from table(sys.odcinumberlist(1, 2))", Read, "SELECT", false},
		{"select s.currval from dual", Read, "SELECT", false},
		{"select my_func(1) from dual", Read, "SELECT", false},
		{"select * from utl_owner.t", Read, "SELECT", false},
		{"select * from t where a = q'[;]'", Read, "SELECT", false},
		{"select 1 from dual -- ; delete from t", Read, "SELECT", false},
		{"select 'a''; delete from t; --' from dual", Read, "SELECT", false},
		{"select 1 from dual;", Read, "SELECT", false},
		{"(select 1 from dual)", Read, "SELECT", false},

		// Session.
		{"alter session set nls_date_format = 'YYYY-MM-DD'", Session, "ALTER", false},
		{"alter session set current_schema = hr", Session, "ALTER", false},
		{"alter session set container = pdb1", Session, "ALTER", false},
		{"alter session set isolation_level = serializable", Session, "ALTER", false},
		{"alter session enable parallel dml", Session, "ALTER", false},
		{"alter session force parallel query", Session, "ALTER", false},
		{"alter session set tracefile_identifier = 'x'", Session, "ALTER", false},
		{"set role all", Session, "SET", false},

		// Transactions.
		{"commit", Transaction, "COMMIT", false},
		{"commit work comment 'x'", Transaction, "COMMIT", false},
		{"rollback", Transaction, "ROLLBACK", false},
		{"rollback to savepoint a", Transaction, "ROLLBACK", false},
		{"savepoint a", Transaction, "SAVEPOINT", false},
		{"set transaction read only", Transaction, "SET", false},
		{"set transaction isolation level serializable", Transaction, "SET", false},
		{"set constraints all deferred", Transaction, "SET", false},

		// Writes.
		{"insert into t values (1)", Write, "INSERT", false},
		{"insert all into a values (1) into b values (2) select 1 from dual", Write, "INSERT", false},
		{"merge into t using s on (t.id = s.id) when matched then update set a = s.a when not matched then insert (a) values (s.a)", Write, "MERGE", false},
		{"update t set a = 1 where id = 1", Write, "UPDATE", false},
		{"update t set a = 1", Write, "UPDATE", true},
		{"delete from t", Write, "DELETE", true},
		{"delete t where id = 1", Write, "DELETE", false},
		{"select * from t for update", Write, "SELECT", false},
		{"select * from t for update of a nowait", Write, "SELECT", false},
		{"select * from t for update skip locked", Write, "SELECT", false},
		{"select * into x from t", Write, "SELECT", false},
		{"select s.nextval from dual", Write, "SELECT", false},
		{"select \"S\".\"NEXTVAL\" from dual", Write, "SELECT", false},
		{"with c as (select s.nextval n from dual) select n from c", Write, "SELECT", false},
		{"select dbms_lock.request(1) from dual", Write, "SELECT", false},
		{"select sys.dbms_pipe.send_message('p') from dual", Write, "SELECT", false},
		{`select "DBMS_PIPE"."SEND_MESSAGE"('p') from dual`, Write, "SELECT", false},
		{"select dbms_stats.get_prefs('x') from dual", Write, "SELECT", false},
		{"select dbms_xplan.diff_plan('a', 'b') from dual", Write, "SELECT", false},
		{"select utl_http.request('http://x') from dual", Write, "SELECT", false},
		{"select utl_inaddr.get_host_name from dual", Write, "SELECT", false},
		{"select httpuritype('http://x').getclob() from dual", Write, "SELECT", false},
		{"select urifactory.geturi('http://x').getclob() from dual", Write, "SELECT", false},
		{"select apex_web_service.make_rest_request(p_url => 'http://x', p_http_method => 'GET') from dual", Write, "SELECT", false},
		{"select * from utl_http.request_pieces('http://x')", Write, "SELECT", false},
		{"select * from dual join dbms_pipe.receive_message('p') on 1 = 1", Write, "SELECT", false},
		{"select * from dbms_xplan.display()", Read, "SELECT", false},
		{"select * from table(dbms_xplan.display())", Read, "SELECT", false},
		{"select * from dbms_x.t", Read, "SELECT", false},
		{"select * from dual join dbms_x.t on 1 = 1", Read, "SELECT", false},
		{"select dbms_x(1) from dual", Write, "SELECT", false},
		{"select sys.f(1) from dual", Write, "SELECT", false},
		{"with function f return number is begin return 1; end; select f from dual", Write, "WITH", false},
		{"with procedure p is begin null; end; select 1 from dual", Write, "WITH", false},
		{"select /*+ with_plsql */ x from (with function f return number is begin return 1; end; select f x from dual)", Write, "SELECT", false},
		{"select * from (with c as (select 1 a from dual) select a from c)", Read, "SELECT", false},
		{"call p()", Write, "CALL", false},
		{"call dbms_stats.gather_table_stats('A', 'B')", Write, "CALL", false},
		{"exec p", Write, "EXEC", false},
		{"execute p(1)", Write, "EXECUTE", false},
		{"begin null; end;", Write, "BEGIN", false},
		{"declare n number; begin n := 1; end;", Write, "DECLARE", false},
		{"<<l>> begin null; end;", Write, "BEGIN", false},
		{"begin update t set a = 1 where id = 1; end;", Write, "BEGIN", false},
		{"begin execute immediate v_sql; end;", Write, "BEGIN", false},
		{"lock table t in exclusive mode", Write, "LOCK", false},
		{"explain plan for select * from t", Write, "EXPLAIN", false},
		{"explain plan set statement_id = 'x' into my_plan for delete from t", Write, "EXPLAIN", false},
		{"alter session set events '10046 trace name context forever'", Write, "ALTER", false},
		{"alter session set sql_trace = true", Write, "ALTER", false},
		{"alter session set nls_sort = binary sql_trace = true", Write, "ALTER", false},
		{"alter session set \"_optimizer_x\" = false", Write, "ALTER", false},
		{"alter session set _optimizer_x = false", Write, "ALTER", false},
		{"alter session disable guard", Write, "ALTER", false},
		{"alter system kill session '1,2'", Write, "ALTER", false},
		{"alter system set open_cursors = 300", Write, "ALTER", false},
		{"commit force '1.2.3'", Write, "COMMIT", false},
		{"rollback force '1.2.3'", Write, "ROLLBACK", false},
		{"administer key management set keystore open identified by x", Write, "ADMINISTER", false},
		{"desc t", Write, "DESC", false},
		{"describe t", Write, "DESCRIBE", false},
		{"set serveroutput on", Write, "SET", false},
		{"show user", Write, "SHOW", false},
		{"frobnicate", Write, "FROBNICATE", false},

		// Schema changes.
		{"create table t (a number)", DDL, "CREATE", false},
		{"create table t as select * from u", DDL, "CREATE", false},
		{"create or replace view v as select 1 a from dual", DDL, "CREATE", false},
		{"create index i on t (a)", DDL, "CREATE", false},
		{"create sequence s", DDL, "CREATE", false},
		{"create database link l connect to u identified by p using 'x'", DDL, "CREATE", false},
		{"create or replace procedure p as begin update t set a = 1 where id = 1; commit; end;", DDL, "CREATE", false},
		{"create or replace trigger tr before insert on t for each row begin :new.id := s.nextval; end;", DDL, "CREATE", false},
		{"create or replace trigger tr before delete on t begin null; end;", DDL, "CREATE", false},
		{"create package body pk as procedure p is begin null; end; end;", DDL, "CREATE", false},
		{"create type pt as object (x number);", DDL, "CREATE", false},
		{"create restore point rp", DDL, "CREATE", false},
		{"alter table t add c number", DDL, "ALTER", false},
		{"alter table t modify c varchar2(10)", DDL, "ALTER", false},
		{"alter table t drop column c", DDL, "ALTER", true},
		{"alter table t drop partition p1", DDL, "ALTER", true},
		{"alter table t set unused column c", DDL, "ALTER", true},
		{"alter table t set unused (c, d)", DDL, "ALTER", true},
		{"alter table t exchange partition p1 with table u", DDL, "ALTER", true},
		{"alter table t truncate partition p1", DDL, "ALTER", true},
		{"alter table t truncate subpartition p1", DDL, "ALTER", true},
		{"alter index i rebuild", DDL, "ALTER", false},
		{"alter procedure p compile", DDL, "ALTER", false},
		{"alter user u identified by p", DDL, "ALTER", false},
		{"alter database datafile 'x' resize 10m", DDL, "ALTER", false},
		{"drop table t", DDL, "DROP", true},
		{"drop table t cascade constraints purge", DDL, "DROP", true},
		{"drop user u cascade", DDL, "DROP", true},
		{"truncate table t", DDL, "TRUNCATE", true},
		{"rename a to b", DDL, "RENAME", false},
		{"comment on table t is 'x'", DDL, "COMMENT", false},
		{"comment on column t.a is 'x'", DDL, "COMMENT", false},
		{"grant select on t to u", DDL, "GRANT", false},
		{"revoke select on t from u", DDL, "REVOKE", false},
		{"audit select on t", DDL, "AUDIT", false},
		{"noaudit select on t", DDL, "NOAUDIT", false},
		{"analyze table t compute statistics", DDL, "ANALYZE", false},
		{"associate statistics with functions f using s", DDL, "ASSOCIATE", false},
		{"disassociate statistics from functions f", DDL, "DISASSOCIATE", false},
		{"flashback table t to before drop", DDL, "FLASHBACK", false},
		{"flashback table t to before drop rename to u", DDL, "FLASHBACK", false},
		{"flashback table t to timestamp systimestamp - interval '1' hour", DDL, "FLASHBACK", true},
		{"flashback table t to scn 123", DDL, "FLASHBACK", true},
		{"flashback database to restore point rp", DDL, "FLASHBACK", true},
		{"purge recyclebin", DDL, "PURGE", true},
		{"purge table t", DDL, "PURGE", true},

		// What blocks and routines hold.
		{"begin delete from t; end;", Write, "BEGIN", true},
		{"declare n number; begin delete from t; end;", Write, "DECLARE", true},
		{"begin if x then delete from t; end if; end;", Write, "BEGIN", true},
		{"begin if x then null; elsif y then delete from t; else null; end if; end;", Write, "BEGIN", true},
		{"begin for r in (select * from t) loop delete from u; end loop; end;", Write, "BEGIN", true},
		{"begin forall i in 1 .. n delete from t; end;", Write, "BEGIN", true},
		{"begin forall i in 1 .. n delete from t where id = x(i); end;", Write, "BEGIN", false},
		{"begin <<l>> delete from t; end;", Write, "BEGIN", true},
		{"begin null; exception when others then delete from t; end;", Write, "BEGIN", true},
		{"begin execute immediate 'drop table t'; end;", Write, "BEGIN", true},
		{"begin execute immediate q'[truncate table t]'; end;", Write, "BEGIN", true},
		{"begin execute immediate 'drop table ' || v; end;", Write, "BEGIN", true},
		{"begin execute immediate 'delete from t where id = :1' using 5; end;", Write, "BEGIN", false},
		{"begin execute immediate 'begin execute immediate ''drop table t''; end;'; end;", Write, "BEGIN", true},
		{"declare c integer; begin c := dbms_sql.open_cursor; dbms_sql.parse(c, 'truncate table t', dbms_sql.native); end;", Write, "DECLARE", true},
		{"begin sys.dbms_utility.exec_ddl_statement('drop table t'); end;", Write, "BEGIN", true},
		{"execute immediate 'drop table t'", Write, "EXECUTE", true},
		{"call dbms_utility.exec_ddl_statement('drop table t')", Write, "CALL", true},
		{"exec dbms_utility.exec_ddl_statement('drop table t')", Write, "EXEC", true},
		{"execute sys.dbms_utility.exec_ddl_statement('drop table t')", Write, "EXECUTE", true},
		{"call dbms_utility.exec_ddl_statement('create table u (a number)')", Write, "CALL", false},
		{"begin dbms_utility.exec_ddl_statement(parse_string => 'drop table t'); end;", Write, "BEGIN", true},
		{"declare c integer; begin dbms_sql.parse(c => c, statement => 'drop table t', language_flag => dbms_sql.native); end;", Write, "DECLARE", true},
		{"declare c integer; begin dbms_sql.parse(c => c, statement => 'delete from t where id = 1', language_flag => dbms_sql.native); end;", Write, "DECLARE", false},
		{"begin execute immediate (q'[drop table t]'); end;", Write, "BEGIN", true},
		{"begin execute immediate ('drop table ' || v); end;", Write, "BEGIN", true},
		{"begin dbms_utility.exec_ddl_statement((('drop table t'))); end;", Write, "BEGIN", true},
		{"begin dbms_scheduler.create_job(job_name => 'j', job_type => 'PLSQL_BLOCK', job_action => 'BEGIN DELETE FROM t; END;', enabled => true); end;", Write, "BEGIN", true},
		{"begin dbms_scheduler.create_job('j', 'PLSQL_BLOCK', 'BEGIN DELETE FROM t; END;'); end;", Write, "BEGIN", true},
		{"begin dbms_scheduler.create_job(job_name => 'j', job_type => 'PLSQL_BLOCK', job_action => 'BEGIN DELETE FROM t WHERE id = 1; END;', comments => 'drop table nightly'); end;", Write, "BEGIN", false},
		{"declare j number; begin dbms_job.submit(j, 'DELETE FROM t;'); end;", Write, "DECLARE", true},
		{"declare j number; begin dbms_job.submit(job => j, what => 'DELETE FROM t;'); end;", Write, "DECLARE", true},
		{"declare j number; begin dbms_job.submit(j, 'DELETE FROM t WHERE id = 1;'); end;", Write, "DECLARE", false},
		{"create or replace procedure p as begin delete from t; end;", DDL, "CREATE", true},
		{"create or replace procedure p as begin execute immediate 'drop table t'; end;", DDL, "CREATE", true},
		{"create or replace procedure p as begin for r in (with function f return number is begin return 1; end; select f from dual) loop null; end loop; end;", DDL, "CREATE", false},
		{"with function f return number is begin delete from t; return 1; end; select f from dual", Write, "WITH", true},

		// What one text holds besides its statement.
		{"select 1 from dual; delete from t where id = 1", Write, "SELECT", true},
		{"select 1 from dual\n/\nselect 2 from dual", Write, "SELECT", true},
		{"begin null; end;\n/\ndelete from t where id = 1", Write, "BEGIN", true},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			got := Classify(tt.src, Oracle)
			if got.Class != tt.class || got.Verb != tt.verb || got.Dangerous != tt.dangerous {
				t.Fatalf("got %+v, want class %v verb %q dangerous %v", got, tt.class, tt.verb, tt.dangerous)
			}
			if got.Dangerous && got.Reason == "" {
				t.Fatalf("dangerous without a reason: %+v", got)
			}
		})
	}
}

func TestOracleClassifyReasons(t *testing.T) {
	tests := []struct{ src, reason string }{
		{"create or replace procedure p as begin delete from t; end;", routineHolds},
		{"begin delete from t; end;", "the PL/SQL block holds a statement that is dangerous on its own: "},
		{"begin execute immediate 'drop table t'; end;", "the SQL it runs is dangerous: "},
		{"purge recyclebin", "PURGE"},
		{"flashback table t to scn 1", "FLASHBACK"},
		{"alter table t set unused column c", "SET UNUSED"},
		{"alter table t exchange partition p with table u", "EXCHANGE"},
		{"SELECT （1） FROM dual", "full-width"},
	}
	for _, tt := range tests {
		if got := Classify(tt.src, Oracle); !strings.Contains(got.Reason, tt.reason) {
			t.Errorf("Classify(%q).Reason = %q, want it to hold %q", tt.src, got.Reason, tt.reason)
		}
	}
}

func TestOracleParams(t *testing.T) {
	tests := []struct {
		src  string
		want []string
	}{
		// Oracle names a bind by a number too, which the editor asks for
		// as it asks for a name.
		{"select * from t where id = :id and b = :1 and c = :12", []string{"id", "1", "12"}},
		{"select :1, :1 from dual", []string{"1", "1"}},
		{"begin :x := 1; end;", []string{"x"}},
		// A trigger's :new and :old are no parameters, and DDL takes none.
		{"create or replace trigger tr before insert on t for each row begin :new.id := :old.id; end;", nil},
		{"select q'[:a]', ':b', \":c\" from dual -- :d", nil},
		{"select * from ${t} where a = :a", []string{"t", "a"}},
	}
	for _, tt := range tests {
		var got []string
		for _, p := range Params(tt.src, Oracle) {
			got = append(got, p.Name)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Params(%q) = %q, want %q", tt.src, got, tt.want)
		}
	}
}

func TestOracleCommitsImplicitly(t *testing.T) {
	words := func(s string) []string {
		var out []string
		for _, t := range Tokenize(s, Oracle) {
			switch t.Kind {
			case Whitespace, Comment:
			case Keyword, Identifier:
				out = append(out, strings.ToUpper(t.Text))
			default:
				out = append(out, t.Text)
			}
		}
		return out
	}
	commits := []string{
		"create table t (a number)", "create or replace procedure p as begin null; end;", "alter table t add c number",
		"alter index i rebuild", "alter user u identified by p", "drop table t", "truncate table t", "rename a to b",
		"comment on table t is 'x'", "grant select on t to u", "revoke select on t from u", "audit select on t",
		"noaudit select on t", "analyze table t compute statistics", "associate statistics with functions f using s",
		"disassociate statistics from functions f", "flashback table t to before drop", "purge recyclebin",
	}
	keeps := []string{
		"select 1 from dual", "insert into t values (1)", "alter session set nls_date_format = 'YYYY'",
		"alter system set open_cursors = 300", "lock table t in share mode", "explain plan for select 1 from dual",
		"call p()", "begin null; end;", "set transaction read only", "savepoint a", "commit", "",
	}
	for _, s := range commits {
		if !OracleCommitsImplicitly(words(s)) {
			t.Errorf("OracleCommitsImplicitly(%q) = false, want true", s)
		}
	}
	for _, s := range keeps {
		if OracleCommitsImplicitly(words(s)) {
			t.Errorf("OracleCommitsImplicitly(%q) = true, want false", s)
		}
	}
}

func TestOracleFormat(t *testing.T) {
	got := FormatWith("select a from dual\n/\nselect b from dual", Oracle, SplitOptions{})
	if want := "SELECT\n  a\nFROM dual\n\n/\n\nSELECT\n  b\nFROM dual"; got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	for _, src := range []string{
		"create or replace procedure p as\nbegin\n  -- c\n  update t set a = q'[;]' where id = :id;\nend;\n/\nselect \"a b\", n'x', 1.5f from t@lnk",
		"select 1 - -1, a--b\n, 2 from dual",
		"select 1 -- c\n/ from dual",
	} {
		formatted := FormatWith(src, Oracle, SplitOptions{})
		if got, want := significantTexts(formatted, Oracle), significantTexts(src, Oracle); !reflect.DeepEqual(got, want) {
			t.Fatalf("Format changed tokens of %q\ngot  %q\nwant %q", src, got, want)
		}
		if got, want := len(SplitWith(formatted, Oracle, SplitOptions{})), len(SplitWith(src, Oracle, SplitOptions{})); got != want {
			t.Fatalf("Format of %q made %d statements of %d:\n%s", src, got, want, formatted)
		}
	}
}

func TestOracleKeywordsAndFunctions(t *testing.T) {
	kw := Keywords(Oracle)
	for _, k := range []string{"SELECT", "VARCHAR2", "CONNECT", "PRIOR", "MINUS", "ROWNUM", "SYSDATE", "MERGE", "BEGIN", "EXCEPTION"} {
		if !slices.Contains(kw, k) {
			t.Errorf("keyword %s missing", k)
		}
	}
	fns := Functions(Oracle)
	for _, f := range []string{"nvl", "decode", "to_char", "to_date", "listagg", "sys_context", "regexp_substr", "add_months"} {
		if !slices.Contains(fns, f) {
			t.Errorf("function %s missing", f)
		}
	}
}

func FuzzOracle(f *testing.F) {
	for _, s := range []string{
		"select q'[a'b]', nq'!c!', N'd', \"e\" from dual@l where x = :1 -- c\r/* /* */ 1.5f",
		"begin\n  execute immediate 'drop table t';\nend;\n/\nselect 1 from dual;",
		"create or replace package body p as procedure x is begin delete t; end; end;\n/",
		"with function f return number is begin return 1; end; select f from dual\n/",
		"select （1）；\u3000\x00 from dual",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		checkCoverage(t, src, Tokenize(src, Oracle))
		SplitWith(src, Oracle, SplitOptions{})
		Classify(src, Oracle)
		Params(src, Oracle)
		if got, want := significantTexts(FormatWith(src, Oracle, SplitOptions{}), Oracle), significantTexts(src, Oracle); !reflect.DeepEqual(got, want) {
			t.Fatalf("Format changed tokens for %q", src)
		}
	})
}
