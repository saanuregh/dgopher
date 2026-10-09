package sqltext

import (
	"reflect"
	"strings"
	"testing"
)

func splitTexts(src string, d Dialect, mode DelimiterMode) []string {
	var got []string
	rs := []rune(src)
	for _, s := range SplitWith(src, d, SplitOptions{Mode: mode}) {
		if string(rs[s.Start:s.End]) != s.Text {
			got = append(got, "RANGE MISMATCH "+s.Text)
			continue
		}
		got = append(got, s.Text)
	}
	return got
}

func TestSplitBlankLines(t *testing.T) {
	tests := []struct {
		name string
		d    Dialect
		src  string
		want []string
	}{
		{"blank line splits", Generic, "select 1\n\nselect 2", []string{"select 1", "select 2"}},
		{"whitespace-only line splits", Generic, "select 1\n  \t\r\nselect 2\n", []string{"select 1", "select 2"}},
		{"single newline does not split", Generic, "select 1\nfrom t", []string{"select 1\nfrom t"}},
		{"blank line after line comment splits", Generic, "select 1 -- c\n\nselect 2", []string{"select 1 -- c", "select 2"}},
		{"inside parentheses", Generic, "select (\n\n1)", []string{"select (\n\n1)"}},
		{"inside string", Generic, "select 'a\n\nb'", []string{"select 'a\n\nb'"}},
		{"inside block comment", Generic, "select /* a\n\nb */ 1", []string{"select /* a\n\nb */ 1"}},
		{"inside dollar body", Postgres, "create function f() returns int as $$\nselect 1\n\nselect 2\n$$ language sql", []string{"create function f() returns int as $$\nselect 1\n\nselect 2\n$$ language sql"}},
		{"inside begin end", MySQL, "create procedure p()\nbegin\n  select case when x then 1\n\n else 2 end\n\n  select 2\nend\n\nselect 3",
			[]string{"create procedure p()\nbegin\n  select case when x then 1\n\n else 2 end\n\n  select 2\nend", "select 3"}},
		{"end if is not a block end", MySQL, "create procedure p()\nbegin\n if x then select 1\n end if\n\n select 2\nend", []string{"create procedure p()\nbegin\n if x then select 1\n end if\n\n select 2\nend"}},
		{"begin transaction is not a block", Postgres, "begin;\n\nselect 1\n\nselect 2", []string{"begin", "select 1", "select 2"}},
		{"semicolon still splits", Generic, "select 1; select 2\nfrom t", []string{"select 1", "select 2\nfrom t"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := splitTexts(tt.src, tt.d, BlankLineAndSemicolon); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
	if got := Split("select 1\n\nselect 2", Generic); len(got) != 2 {
		t.Fatalf("Split must default to BlankLineAndSemicolon, got %+v", got)
	}
}

func TestSplitSemicolonOnly(t *testing.T) {
	tests := []struct {
		src  string
		want []string
	}{
		{"select 1\n\nselect 2", []string{"select 1\n\nselect 2"}},
		{"select 1;\n\nselect 2;", []string{"select 1", "select 2"}},
		{"-- c\nselect 1; /* d */ select 2", []string{"select 1", "select 2"}},
	}
	for _, tt := range tests {
		if got := splitTexts(tt.src, Generic, SemicolonOnly); !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("%q: got %q, want %q", tt.src, got, tt.want)
		}
	}
}

func TestStatementAtBlankLineCaret(t *testing.T) {
	src := "select 1\n\nselect 2"
	// "select 1" 0..8, '\n' 8, blank line at 9, "select 2" starts at 10.
	src2 := "select 1;\n\n\nselect 2"
	// "select 1" 0..8, ';' 8, line after at 10, blank, "select 2" at 12.
	tests := []struct {
		name  string
		src   string
		caret int
		want  string
	}{
		{"end of statement line", src, 8, "select 1"},
		{"on blank line directly after", src, 9, "select 1"},
		{"line after semicolon", src2, 10, "select 1"},
		{"blank line between picks next", src2, 11, "select 2"},
		{"after last statement past blank line", "select 1\n\n\n", 11, "select 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := StatementAtWith(tt.src, tt.caret, Generic, SplitOptions{})
			if !ok || got.Text != tt.want {
				t.Fatalf("got %q,%v want %q", got.Text, ok, tt.want)
			}
		})
	}
}

func TestLeadingCommentsExcluded(t *testing.T) {
	for _, mode := range []DelimiterMode{BlankLineAndSemicolon, SemicolonOnly} {
		src := "-- connection: x\n\nSELECT 1"
		got := SplitWith(src, Generic, SplitOptions{Mode: mode})
		if len(got) != 1 || got[0].Text != "SELECT 1" || got[0].Start != 18 {
			t.Fatalf("mode %d: got %+v", mode, got)
		}
		got = SplitWith("/* a */ -- b\nSELECT 1 -- tail", Generic, SplitOptions{Mode: mode})
		if len(got) != 1 || got[0].Text != "SELECT 1 -- tail" {
			t.Fatalf("mode %d: got %+v", mode, got)
		}
		if got := SplitWith("-- only\n\n/* c */\n", Generic, SplitOptions{Mode: mode}); len(got) != 0 {
			t.Fatalf("mode %d: comment-only got %+v", mode, got)
		}
	}
}

func TestParams(t *testing.T) {
	type p struct {
		Name string
		Kind ParamKind
	}
	tests := []struct {
		d    Dialect
		src  string
		want []p
	}{
		{Postgres, "select * from ${tbl} where id = :id and x::int > 1", []p{{"tbl", ParamVar}, {"id", ParamNamed}}},
		{Postgres, "do $$ begin a := 1; end $$", nil},
		{Generic, "a := :b", []p{{"b", ParamNamed}}},
		{Postgres, "select arr[1:2], arr[lo:hi], arr[:n] from t", nil},
		{Generic, "select '12:30', \"a:b\", :1 -- :c\n/* :d */", nil},
		{MySQL, "select a:b, :c", []p{{"c", ParamNamed}}},
		{Generic, "select ${a.b c}", []p{{"a.b c", ParamVar}}},
	}
	for _, tt := range tests {
		params := Params(tt.src, tt.d)
		var got []p
		rs := []rune(tt.src)
		for _, x := range params {
			got = append(got, p{x.Name, x.Kind})
			s := string(rs[x.Start:x.End])
			if (x.Kind == ParamNamed && s != ":"+x.Name) || (x.Kind == ParamVar && s != "${"+x.Name+"}") {
				t.Fatalf("%q: range %q does not match %+v", tt.src, s, x)
			}
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("%q: got %+v want %+v", tt.src, got, tt.want)
		}
	}
}

func TestFormatKeepsBlankLines(t *testing.T) {
	tests := []struct{ src, want string }{
		{"select a from t\n\n\n\nselect b from u", "SELECT\n  a\nFROM t\n\nSELECT\n  b\nFROM u"},
		{"select 1;\nselect 2", "SELECT\n  1;\n\nSELECT\n  2"},
		{"-- head\n\nselect 1", "-- head\n\nSELECT\n  1"},
	}
	for _, tt := range tests {
		if got := Format(tt.src, Generic); got != tt.want {
			t.Fatalf("%q: got %q want %q", tt.src, got, tt.want)
		}
	}
	if got := FormatWith("select 1\n\nselect 2", Generic, SplitOptions{Mode: SemicolonOnly}); got != "SELECT\n  1\nSELECT\n  2" {
		t.Fatalf("semicolon only: got %q", got)
	}
}

func TestFormatRange(t *testing.T) {
	src := "select  1;\nselect a from t;\n\nselect  3"
	// second statement spans runes 11..26.
	text, s, e := FormatRange(src, 13, 14, Generic, SplitOptions{})
	want := "select  1;\nSELECT\n  a\nFROM t;\n\nselect  3"
	if text != want {
		t.Fatalf("got %q want %q", text, want)
	}
	if got := string([]rune(text)[s:e]); got != "SELECT\n  a\nFROM t" {
		t.Fatalf("range %d..%d = %q", s, e, got)
	}
	text, s, e = FormatRange("x; select  1", 5, 5, Generic, SplitOptions{})
	if text != "x; SELECT\n  1" || string([]rune(text)[s:e]) != "SELECT\n  1" {
		t.Fatalf("caret range: %q %d %d", text, s, e)
	}
	if text, _, _ := FormatRange("a;\n\n", 3, 4, Generic, SplitOptions{}); text != "a;\n\n" {
		t.Fatalf("no statement: %q", text)
	}
}

func TestSplitHiddenBoundaries(t *testing.T) {
	tests := []struct {
		name string
		d    Dialect
		src  string
		want []string
	}{
		{"pg carriage return ends line comment", Postgres, "UPDATE t SET a=1 WHERE id=1 -- c\r; DROP TABLE t", []string{"UPDATE t SET a=1 WHERE id=1 -- c", "DROP TABLE t"}},
		{"pg emoji starts identifier", Postgres, "SELECT 1 AS 🙂$a$; DROP TABLE t; --$a$", []string{"SELECT 1 AS 🙂$a$", "DROP TABLE t"}},
		{"pg no-break space is identifier part", Postgres, "SELECT x\u00a0$a$; DROP TABLE t; --$a$", []string{"SELECT x\u00a0$a$", "DROP TABLE t"}},
		{"sqlite bracket identifier", SQLite, "SELECT [a']; DROP TABLE t; --'", []string{"SELECT [a']", "DROP TABLE t"}},
		{"sqlite backtick identifier", SQLite, "SELECT `a'`; DROP TABLE t; --'", []string{"SELECT `a'`", "DROP TABLE t"}},
		{"sqlite semicolon in bracket identifier", SQLite, "SELECT [a;b] FROM t; SELECT 2", []string{"SELECT [a;b] FROM t", "SELECT 2"}},
		{"mysql dashes without space are minus", MySQL, "SELECT 1--1; DROP TABLE t", []string{"SELECT 1--1", "DROP TABLE t"}},
		{"clickhouse backslash in quoted identifier", ClickHouse, `SELECT "x\"" ; DROP TABLE t`, []string{`SELECT "x\""`, "DROP TABLE t"}},
		{"clickhouse dollar string", ClickHouse, "SELECT $$a;b$$; SELECT 2", []string{"SELECT $$a;b$$", "SELECT 2"}},
		{"clickhouse nested block comment", ClickHouse, "/* a /* b */ ; DROP TABLE t */ SELECT 1", []string{"SELECT 1"}},
		{"clickhouse hash space is comment", ClickHouse, "SELECT # x; DROP TABLE t", []string{"SELECT # x; DROP TABLE t"}},
		{"clickhouse hash word is not comment", ClickHouse, "SELECT #x; DROP TABLE t", []string{"SELECT #x", "DROP TABLE t"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := splitTexts(tt.src, tt.d, SemicolonOnly); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSplitProcedureBodies(t *testing.T) {
	tests := []struct {
		name string
		d    Dialect
		src  string
		want []string
	}{
		{"mysql procedure", MySQL, "CREATE PROCEDURE p() BEGIN SELECT 1; IF x THEN SELECT 2; END IF; END; SELECT 3",
			[]string{"CREATE PROCEDURE p() BEGIN SELECT 1; IF x THEN SELECT 2; END IF; END", "SELECT 3"}},
		{"mysql definer and nested begin", MySQL, "CREATE DEFINER=`root`@`%` PROCEDURE p() BEGIN lbl: BEGIN SELECT 1; END lbl; END; SELECT 2",
			[]string{"CREATE DEFINER=`root`@`%` PROCEDURE p() BEGIN lbl: BEGIN SELECT 1; END lbl; END", "SELECT 2"}},
		{"postgres begin atomic", Postgres, "CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1; SELECT 2; END; SELECT 3",
			[]string{"CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1; SELECT 2; END", "SELECT 3"}},
		{"sqlite trigger", SQLite, "CREATE TRIGGER t AFTER INSERT ON a BEGIN DELETE FROM b; UPDATE c SET x=1; END; SELECT 1",
			[]string{"CREATE TRIGGER t AFTER INSERT ON a BEGIN DELETE FROM b; UPDATE c SET x=1; END", "SELECT 1"}},
		{"unclosed begin falls back", MySQL, "CREATE PROCEDURE p() BEGIN SELECT 1; DROP TABLE t",
			[]string{"CREATE PROCEDURE p() BEGIN SELECT 1", "DROP TABLE t"}},
		{"case inside body", MySQL, "CREATE PROCEDURE p() BEGIN SELECT CASE WHEN a THEN 1 ELSE 2 END; CASE x WHEN 1 THEN SELECT 1; END CASE; SELECT 3; END; SELECT 4",
			[]string{"CREATE PROCEDURE p() BEGIN SELECT CASE WHEN a THEN 1 ELSE 2 END; CASE x WHEN 1 THEN SELECT 1; END CASE; SELECT 3; END", "SELECT 4"}},
		{"case end does not close the body", MySQL, "CREATE PROCEDURE p() BEGIN SELECT CASE WHEN a THEN 1 END; DROP TABLE t",
			[]string{"CREATE PROCEDURE p() BEGIN SELECT CASE WHEN a THEN 1 END", "DROP TABLE t"}},
		{"case in plain statement does not hold semicolons", Generic, "SELECT CASE; DROP TABLE t; SELECT END",
			[]string{"SELECT CASE", "DROP TABLE t", "SELECT END"}},
		{"begin transaction", MySQL, "BEGIN; SELECT 1; COMMIT", []string{"BEGIN", "SELECT 1", "COMMIT"}},
		{"begin in a plain statement does not open a body", Postgres, "SELECT 1 AS begin; DROP TABLE t; SELECT end",
			[]string{"SELECT 1 AS begin", "DROP TABLE t", "SELECT end"}},
		{"postgres dollar body unchanged", Postgres, "CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END $$ LANGUAGE plpgsql; DO $$ BEGIN NULL; END $$; SELECT 1",
			[]string{"CREATE FUNCTION f() RETURNS int AS $$ BEGIN RETURN 1; END $$ LANGUAGE plpgsql", "DO $$ BEGIN NULL; END $$", "SELECT 1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, mode := range []DelimiterMode{SemicolonOnly, BlankLineAndSemicolon} {
				if got := splitTexts(tt.src, tt.d, mode); !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("mode %d: got %q, want %q", mode, got, tt.want)
				}
			}
		})
	}
	t.Run("blank lines inside a body", func(t *testing.T) {
		src := "CREATE PROCEDURE p()\nBEGIN\n  SELECT 1;\n\n  SELECT 2;\nEND;\n\nSELECT 3"
		want := []string{"CREATE PROCEDURE p()\nBEGIN\n  SELECT 1;\n\n  SELECT 2;\nEND", "SELECT 3"}
		if got := splitTexts(src, MySQL, BlankLineAndSemicolon); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestSplitDelimiter(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{"procedure", "DELIMITER //\nCREATE PROCEDURE p() BEGIN SELECT 1; END//\nDELIMITER ;\nSELECT 2;",
			[]string{"CREATE PROCEDURE p() BEGIN SELECT 1; END", "SELECT 2"}},
		{"delimiter inside string does not end", "DELIMITER //\nSELECT '//'; SELECT 1//\nDELIMITER ;\nSELECT 2",
			[]string{"SELECT '//'; SELECT 1", "SELECT 2"}},
		{"delimiter inside a word", "delimiter $$\nCREATE PROCEDURE p() BEGIN SELECT 1; END$$\nCALL p()$$\ndelimiter ;\nSELECT 2",
			[]string{"CREATE PROCEDURE p() BEGIN SELECT 1; END", "CALL p()", "SELECT 2"}},
		{"words after the delimiter are ignored", "DELIMITER // note\nSELECT 1//\nDELIMITER ; again\nSELECT 2;",
			[]string{"SELECT 1", "SELECT 2"}},
		{"delimiter not at line start is a word", "SELECT 1; DELIMITER //\nSELECT 2",
			[]string{"SELECT 1", "DELIMITER //\nSELECT 2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, mode := range []DelimiterMode{SemicolonOnly, BlankLineAndSemicolon} {
				if got := splitTexts(tt.src, MySQL, mode); !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("mode %d: got %q, want %q", mode, got, tt.want)
				}
			}
		})
	}
	if got := splitTexts("DELIMITER //\nSELECT 1//", Postgres, SemicolonOnly); len(got) != 1 {
		t.Fatalf("DELIMITER is MySQL only, got %q", got)
	}
	if got := Format("DELIMITER //\nCREATE PROCEDURE p() BEGIN SELECT 1; END//\nDELIMITER ;\nSELECT 2;", MySQL); !strings.Contains(got, "END//") || !strings.HasPrefix(got, "DELIMITER //") {
		t.Fatalf("Format must keep the delimiter lines, got %q", got)
	}
}

func TestSplitBeginColumn(t *testing.T) {
	for _, d := range []Dialect{Generic, Postgres, MySQL, SQLite} {
		got := splitTexts("SELECT begin FROM events\n\nDELETE FROM events", d, BlankLineAndSemicolon)
		if want := []string{"SELECT begin FROM events", "DELETE FROM events"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("dialect %d: got %q, want %q", d, got, want)
		}
	}
	got := splitTexts("SELECT CASE WHEN a THEN 1\n\nELSE 2 END FROM t", Generic, BlankLineAndSemicolon)
	if want := []string{"SELECT CASE WHEN a THEN 1\n\nELSE 2 END FROM t"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("case keeps blank-line protection: got %q", got)
	}
}

func TestSplitCRBlankLine(t *testing.T) {
	for _, src := range []string{"SELECT 1\r\r\rSELECT 2", "SELECT 1\r\n\r\nSELECT 2", "SELECT 1\r\rSELECT 2", "SELECT 1\n\r\nSELECT 2"} {
		if got := splitTexts(src, Generic, BlankLineAndSemicolon); !reflect.DeepEqual(got, []string{"SELECT 1", "SELECT 2"}) {
			t.Fatalf("%q: got %q", src, got)
		}
	}
	if got := splitTexts("SELECT 1\r\nFROM t", Generic, BlankLineAndSemicolon); len(got) != 1 {
		t.Fatalf("one CRLF is one line break, got %q", got)
	}
}

func TestParamsClickHouseTyped(t *testing.T) {
	src := "select {id:UInt32}, {name: String}, {a : Array(UInt32)}, {n:Nullable(String)}"
	got := Params(src, ClickHouse)
	want := []struct{ name, typ, text string }{
		{"id", "UInt32", "{id:UInt32}"},
		{"name", "String", "{name: String}"},
		{"a", "Array(UInt32)", "{a : Array(UInt32)}"},
		{"n", "Nullable(String)", "{n:Nullable(String)}"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	rs := []rune(src)
	for i, w := range want {
		p := got[i]
		if p.Kind != ParamTyped || p.Name != w.name || p.Type != w.typ || string(rs[p.Start:p.End]) != w.text {
			t.Fatalf("%d: got %+v (%q) want %+v", i, p, string(rs[p.Start:p.End]), w)
		}
	}
	for _, c := range []struct {
		src string
		d   Dialect
	}{
		{"select '{id:UInt32}', `{id:UInt32}` -- {id:UInt32}", ClickHouse},
		{"select {id:UInt32}", MySQL},
	} {
		if got := Params(c.src, c.d); len(got) != 0 {
			t.Fatalf("%q: got %+v", c.src, got)
		}
	}
}

func TestParamsSQLiteAtDollar(t *testing.T) {
	src := "select @a, $b, :c, $1, @ d"
	got := Params(src, SQLite)
	want := []struct {
		name  string
		sigil rune
	}{{"a", '@'}, {"b", '$'}, {"c", ':'}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	rs := []rune(src)
	for i, w := range want {
		p := got[i]
		if p.Kind != ParamNamed || p.Name != w.name || p.Sigil != w.sigil || string(rs[p.Start:p.End]) != string(w.sigil)+w.name {
			t.Fatalf("%d: got %+v want %+v", i, p, w)
		}
	}
	if got := Params("select $1, @a", Postgres); len(got) != 0 {
		t.Fatalf("Postgres: got %+v", got)
	}
}

// BEGIN and END used as names inside a routine header or after it must not
// open or close a body, or the statements between them merge.
func TestSplitBeginEndNames(t *testing.T) {
	tests := []struct {
		name string
		d    Dialect
		src  string
		want int
	}{
		{"function named begin", Postgres, "CREATE FUNCTION begin() AS 1; DROP TABLE victim; SELECT 1 AS end", 3},
		{"qualified begin column", Postgres, "CREATE OR REPLACE FUNCTION f(r) AS r.begin;\nDELETE FROM victim;\nDROP TABLE victim;\nSELECT 1 AS end", 4},
		{"trigger named begin", SQLite, "CREATE TRIGGER begin AFTER INSERT ON victim BEGIN SELECT 1; END; DROP TABLE victim; SELECT 1 AS end", 3},
		{"trigger on table begin", SQLite, "CREATE TRIGGER t AFTER INSERT ON begin BEGIN SELECT 1; END; DROP TABLE victim; SELECT 1 AS end", 3},
		{"update of column begin", SQLite, "CREATE TRIGGER t AFTER UPDATE OF x, begin ON v BEGIN SELECT 1; END; DROP TABLE victim; SELECT * FROM end", 3},
		{"when new.begin", SQLite, "CREATE TRIGGER t AFTER INSERT ON v WHEN new.begin > 0 BEGIN SELECT 1; SELECT 2; END; DROP TABLE victim; SELECT r.end FROM r;", 3},
		{"setof begin", Postgres, "CREATE FUNCTION f() RETURNS SETOF begin LANGUAGE sql BEGIN ATOMIC SELECT 1; END; DROP TABLE victim; SELECT * FROM end", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, mode := range []DelimiterMode{SemicolonOnly, BlankLineAndSemicolon} {
				got := splitTexts(tt.src, tt.d, mode)
				if len(got) != tt.want {
					t.Fatalf("mode %d: got %d statements %q, want %d", mode, len(got), got, tt.want)
				}
				for _, s := range got {
					if strings.HasPrefix(s, "DROP") {
						if a := Classify(s, tt.d); !a.Dangerous || a.Verb != "DROP" {
							t.Fatalf("mode %d: %q classified %+v", mode, s, a)
						}
					}
				}
			}
			if a := Classify(tt.src, tt.d); !a.Dangerous {
				t.Fatalf("whole text classified %+v", a)
			}
		})
	}
}

// SQLite takes '$' inside a name, so x$y is a name, not x then $y; a sigil
// glued to a name is never a parameter.
func TestParamsSQLiteDollarInName(t *testing.T) {
	toks := Tokenize("SELECT 1 AS x$y", SQLite)
	if last := toks[len(toks)-1]; last.Kind != Identifier || last.Text != "x$y" {
		t.Fatalf("got %+v", toks)
	}
	if got := Params("SELECT 1 AS x$y", SQLite); len(got) != 0 {
		t.Fatalf("x$y: got %+v", got)
	}
	var names []string
	for _, p := range Params("SELECT [a]@b, \"c\"$d, (@e), =$f", SQLite) {
		names = append(names, p.Name)
	}
	if want := []string{"e", "f"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("got %q, want %q", names, want)
	}
}

func TestParamsVarAfterName(t *testing.T) {
	for _, d := range []Dialect{Generic, Postgres, MySQL, ClickHouse, SQLite} {
		var names []string
		for _, p := range Params("SELECT * FROM logs_${suffix} WHERE a = ${v}", d) {
			if p.Kind == ParamVar {
				names = append(names, p.Name)
			}
		}
		if want := []string{"suffix", "v"}; !reflect.DeepEqual(names, want) {
			t.Errorf("dialect %d: got %q, want %q", d, names, want)
		}
	}
}

// A text read on from the middle of a MySQL file starts under the
// DELIMITER in effect there, and each statement says which ends it.
func TestSplitFromDelimiter(t *testing.T) {
	src := "CREATE TRIGGER t BEFORE INSERT ON x FOR EACH ROW BEGIN SET NEW.a = 1; END;;\nDELIMITER ;\nSELECT 1;"
	got := SplitWith(src, MySQL, SplitOptions{Mode: SemicolonOnly, Delimiter: ";;"})
	if len(got) != 2 || !strings.HasSuffix(got[0].Text, "END") || got[0].Delimiter != ";;" || got[1].Text != "SELECT 1" || got[1].Delimiter != ";" {
		t.Fatalf("%+v", got)
	}
}

// mysqldump writes SET statements and triggers in executable comments,
// which MySQL runs: they are statements, not comments to drop.
func TestSplitKeepsExecutableComments(t *testing.T) {
	src := "/*!40101 SET NAMES utf8mb4 */;\n-- a comment\n;\n/*!50003 CREATE*/ /*!50003 TRIGGER t BEFORE INSERT ON x FOR EACH ROW SET NEW.a = 1 */;"
	got := SplitWith(src, MySQL, SplitOptions{Mode: SemicolonOnly})
	if len(got) != 2 || got[0].Text != "/*!40101 SET NAMES utf8mb4 */" || !strings.HasPrefix(got[1].Text, "/*!50003 CREATE*/") {
		t.Fatalf("%+v", got)
	}
	if got := SplitWith("/* note */;", MySQL, SplitOptions{}); len(got) != 0 {
		t.Fatalf("a plain comment kept: %+v", got)
	}
}
