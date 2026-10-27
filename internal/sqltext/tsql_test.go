package sqltext

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestTSQLQuoting(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []Kind
	}{
		{"bracket name", "[a b]", []Kind{QuotedIdent}},
		{"bracket name with ]] escape", "[a]]b] x", []Kind{QuotedIdent, Identifier}},
		{"bracket name holds ; and quotes", "[a;'\"b] x", []Kind{QuotedIdent, Identifier}},
		{"double-quoted name", `"a""b" x`, []Kind{QuotedIdent, Identifier}},
		{"N string", "N'it''s' x", []Kind{String, Identifier}},
		{"lower-case n string", "n'x'", []Kind{String}},
		{"no backslash escapes", `'a\' b`, []Kind{String, Identifier}},
		{"x'…' is a name then a string", "x'ab'", []Kind{Identifier, String}},
		{"line comment", "a -- c\nb", []Kind{Identifier, Comment, Identifier}},
		{"line comment ends at a lone CR", "-- c\rb", []Kind{Comment, Identifier}},
		{"block comments nest", "/* a /* b */ c */x", []Kind{Comment, Identifier}},
		{"nested opener inside a string in a comment", "/* '/*' */ x", []Kind{Comment}},
		{"variables", "@x @@ROWCOUNT @1", []Kind{Param, Param, Param}},
		{"variable with # $ @", "@x#y$z@w", []Kind{Param}},
		{"temp tables", "#t ##g #1", []Kind{Identifier, Identifier, Identifier}},
		{"names with $ # @", "a$b a#b a@b", []Kind{Identifier, Identifier, Identifier}},
		{"money", "$12.50 $.5 $3", []Kind{Number, Number, Number}},
		{"$action", "$action", []Kind{Identifier}},
		{"hex", "0x1F 0x", []Kind{Number, Number}},
		{"app parameter", ":id", []Kind{Param}},
		{"scope qualifier", "OBJECT::t", []Kind{Identifier, Operator, Identifier}},
		{"GO", "GO", []Kind{Keyword}},
		{"${var}", "${v}", []Kind{Unknown, Punct, Identifier, Punct}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			toks := Tokenize(tt.src, TSQL)
			checkCoverage(t, tt.src, toks)
			if got := kinds(toks); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v (%+v)", got, tt.want, toks)
			}
		})
	}

	decoded := []struct{ text, want string }{
		{"[a]]b]", "a]b"},
		{"[a b]", "a b"},
		{`"a""b"`, `a"b`},
		{"N'it''s'", "it's"},
		{"'it''s'", "it's"},
	}
	for _, d := range decoded {
		if got := DecodeQuoted(d.text, TSQL); got != d.want {
			t.Errorf("DecodeQuoted(%q) = %q, want %q", d.text, got, d.want)
		}
	}
}

func TestTSQLParams(t *testing.T) {
	tests := []struct {
		src  string
		want []string
	}{
		{"select * from t where id = :id and x = @x", []string{"id"}},
		{"lbl:x", nil},
		{"select '${v}' from ${t}", []string{"t"}},
		{"select [a:b], ':c', N':d' -- :e", nil},
	}
	for _, tt := range tests {
		var got []string
		for _, p := range Params(tt.src, TSQL) {
			got = append(got, p.Name)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Params(%q) = %q, want %q", tt.src, got, tt.want)
		}
	}
}

func TestTSQLSplit(t *testing.T) {
	blank, semicolon := BlankLineAndSemicolon, SemicolonOnly
	tests := []struct {
		name string
		mode DelimiterMode
		src  string
		want []string
	}{
		{"GO separates batches", blank, "select 1\nGO\nselect 2", []string{"select 1", "select 2"}},
		{"go in any case, indented, with a comment", blank, "select 1\n  go  -- end\nselect 2", []string{"select 1", "select 2"}},
		{"GO with a block comment on its line", blank, "select 1\nGO /* x */\nselect 2", []string{"select 1", "select 2"}},
		{"GO with CRLF", blank, "select 1\r\nGO\r\nselect 2", []string{"select 1", "select 2"}},
		{"GO first", blank, "GO\nselect 1", []string{"select 1"}},
		{"GO 1 is GO", blank, "select 1\nGO 1\nselect 2", []string{"select 1", "select 2"}},
		{"GO with a count stays a statement", blank, "insert into t values (1)\nGO 5\nselect 1", []string{"insert into t values (1)", "GO 5", "select 1"}},
		{"GO in a string", blank, "select 'a\nGO\nb'", []string{"select 'a\nGO\nb'"}},
		{"GO in a block comment", blank, "select 1 /*\nGO\n*/ + 1", []string{"select 1 /*\nGO\n*/ + 1"}},
		{"GO in a bracket name", blank, "select [a\nGO\nb]", []string{"select [a\nGO\nb]"}},
		{"GO after code on its line", blank, "select 1 GO\nselect 2", []string{"select 1 GO\nselect 2"}},
		{"GO before code on its line", blank, "select 1\nGO select 2", []string{"select 1\nGO select 2"}},
		{"GO; is not GO", blank, "select 1\nGO;\nselect 2", []string{"select 1\nGO;\nselect 2"}},
		{"block comment running past the GO line", blank, "select 1\nGO /*\n*/\nselect 2", []string{"select 1\nGO /*\n*/\nselect 2"}},
		{"GO ends an open block", blank, "begin\nselect 1;\nGO\nselect 2; end", []string{"begin\nselect 1;", "select 2; end"}},
		{"GO line inside a statement", blank, "select\ngo\nfrom t", []string{"select", "from t"}},
		{"batch without semicolons", blank, "declare @x int = 1\nselect @x", []string{"declare @x int = 1\nselect @x"}},
		{"semicolons do not split", blank, "declare @x int = 1;\nselect @x;", []string{"declare @x int = 1;\nselect @x;"}},
		{"semicolon only splits at GO alone", semicolon, "select 1;\n\nselect 2;\nGO\nselect 3", []string{"select 1;\n\nselect 2;", "select 3"}},
		{"blank line splits", blank, "select 1\n\nselect 2", []string{"select 1", "select 2"}},
		{"semicolon only keeps blank lines", semicolon, "select 1\n\nselect 2", []string{"select 1\n\nselect 2"}},
		{"BEGIN … END keeps its statements", blank,
			"if @x = 1\nbegin\n  select 1;\n\n  select 2;\nend\nelse\nbegin\n  select 3;\nend;\n\nselect 4",
			[]string{"if @x = 1\nbegin\n  select 1;\n\n  select 2;\nend\nelse\nbegin\n  select 3;\nend;", "select 4"}},
		{"TRY and CATCH", blank,
			"begin try\n  delete from t where id = 1;\n\nend try\nbegin catch\n  select error_message();\nend catch;\n\nselect 1",
			[]string{"begin try\n  delete from t where id = 1;\n\nend try\nbegin catch\n  select error_message();\nend catch;", "select 1"}},
		{"semicolon before ELSE", blank, "if @x = 1 select 1; else select 2;", []string{"if @x = 1 select 1; else select 2;"}},
		{"blank line before ELSE", blank, "if @x = 1 select 1\n\nelse select 2", []string{"if @x = 1 select 1\n\nelse select 2"}},
		{"blank line after an IF condition", blank, "if @x = 1\n\n  delete from t where id = 1", []string{"if @x = 1\n\n  delete from t where id = 1"}},
		{"blank line after WHILE", blank, "while @i < 3\n\n  set @i += 1;\n\nselect @i", []string{"while @i < 3\n\n  set @i += 1;", "select @i"}},
		{"WHILE with a block", blank, "while @i < 3\nbegin\n\n  set @i += 1;\nend;\n\nselect @i", []string{"while @i < 3\nbegin\n\n  set @i += 1;\nend;", "select @i"}},
		{"DROP … IF EXISTS waits for nothing", blank, "drop table if exists t\n\nselect 1", []string{"drop table if exists t", "select 1"}},
		{"procedure body runs to GO", blank,
			"create procedure p as\nset nocount on;\nselect 1;\n\nselect 2;\nGO\nexec p",
			[]string{"create procedure p as\nset nocount on;\nselect 1;\n\nselect 2;", "exec p"}},
		{"procedure body runs to the end", blank,
			"CREATE OR ALTER PROC p AS BEGIN SELECT 1; END;\nEXEC p",
			[]string{"CREATE OR ALTER PROC p AS BEGIN SELECT 1; END;\nEXEC p"}},
		{"function and trigger bodies", semicolon,
			"alter trigger tr on t after insert as\nupdate t set a = 1 where 1 = 0;\ndelete from u where 1 = 0\nGO\ncreate function f() returns int as begin return 1; end",
			[]string{"alter trigger tr on t after insert as\nupdate t set a = 1 where 1 = 0;\ndelete from u where 1 = 0", "create function f() returns int as begin return 1; end"}},
		{"view splits as usual", blank, "create view v as select 1;\n\nselect 2", []string{"create view v as select 1;", "select 2"}},
		{"CASE … END", blank, "select case when a = 1 then 'x'\n\nelse 'y' end from t\n\nselect 2", []string{"select case when a = 1 then 'x'\n\nelse 'y' end from t", "select 2"}},
		{"BEGIN TRAN is no block", blank, "begin tran\n\nupdate t set a = 1 where id = 1\n\ncommit", []string{"begin tran", "update t set a = 1 where id = 1", "commit"}},
		{"BEGIN DISTRIBUTED TRANSACTION is no block", blank, "begin distributed transaction\n\nselect 1", []string{"begin distributed transaction", "select 1"}},
		{"END CONVERSATION closes nothing", blank, "begin\n end conversation @h;\n\n select 1;\nend", []string{"begin\n end conversation @h;\n\n select 1;\nend"}},
		{"END before IF closes the block", blank, "while 1 = 1 begin break; end\nif 1 = 1 select 1\n\nselect 2", []string{"while 1 = 1 begin break; end\nif 1 = 1 select 1", "select 2"}},
		{"bracket name holds a blank line", blank, "select [a\n\nb] from t\n\nselect 2", []string{"select [a\n\nb] from t", "select 2"}},
		{"unclosed BEGIN splits again", blank, "begin\nselect 1\n\nselect 2", []string{"begin\nselect 1", "select 2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := splitTexts(tt.src, TSQL, tt.mode); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestTSQLClassify(t *testing.T) {
	tests := []struct {
		src       string
		class     Class
		verb      string
		dangerous bool
	}{
		// Reads.
		{"select * from t", Read, "SELECT", false},
		{"select * from t with (nolock)", Read, "SELECT", false},
		{"with c as (select 1 as a) select * from c", Read, "SELECT", false},
		{"with xmlnamespaces ('urn:x' as x) select 1", Read, "SELECT", false},
		{"declare @x int = 5", Read, "DECLARE", false},
		{"declare @t table (a int)", Read, "DECLARE", false},
		{"print 'hi'", Read, "PRINT", false},
		{"declare @x int = 1\nselect @x", Read, "DECLARE", false},
		{"set @x = 1", Read, "SET", false},
		{"select @x = count(*) from t", Read, "SELECT", false},
		{"exec sp_help 't'", Read, "EXEC", false},
		{"sp_who2", Read, "EXEC", false},
		{"execute sys.sp_helptext N'dbo.p'", Read, "EXECUTE", false},
		{"exec [sp_columns] @table_name = 't'", Read, "EXEC", false},
		{"exec @rc = sp_help 't'", Read, "EXEC", false},
		{"exec sp_configure", Read, "EXEC", false},
		{"exec sp_configure 'show advanced options'", Read, "EXEC", false},
		{"exec sp_spaceused 't'", Read, "EXEC", false},
		{"exec xp_fixeddrives", Read, "EXEC", false},
		{"dbcc checkdb", Read, "DBCC", false},
		{"dbcc checkdb (d) with no_infomsgs", Read, "DBCC", false},
		{"dbcc sqlperf(logspace)", Read, "DBCC", false},
		{"dbcc checkident ('t', noreseed)", Read, "DBCC", false},
		{"kill 52 with statusonly", Read, "KILL", false},
		{"if exists (select 1 from t) print 'yes'", Read, "IF", false},
		{"begin try select 1 end try begin catch select error_message() end catch", Read, "BEGIN", false},
		{"waitfor delay '00:00:01'", Read, "WAITFOR", false},
		{"select * from t order by a offset 0 rows fetch next 10 rows only", Read, "SELECT", false},
		{"select * from t option (merge join)", Read, "SELECT", false},
		{"select * from a inner merge join b on a.id = b.id", Read, "SELECT", false},
		{"raiserror('x', 16, 1) with nowait", Read, "RAISERROR", false},
		{"throw 50000, 'x', 1", Read, "THROW", false},
		{"select * from [delete]", Read, "SELECT", false},
		{"select [a]]; delete from t] from x", Read, "SELECT", false},
		{"select 'a''; delete from t; --'", Read, "SELECT", false},
		{"-- delete from t\nselect 1", Read, "SELECT", false},
		{"/* /* */ delete from t */ select 1", Read, "SELECT", false},
		{"open c; fetch next from c into @a", Read, "OPEN", false},
		{"fetch next from c into @a", Read, "FETCH", false},
		{"lbl:\nprint 1", Read, "LBL", false},
		{"select a from t for xml path", Read, "SELECT", false},
		{"if @x = 1 select 1 else select 2", Read, "IF", false},

		// Session.
		{"set nocount on", Session, "SET", false},
		{"set transaction isolation level serializable", Session, "SET", false},
		{"set implicit_transactions on", Session, "SET", false},
		{"set showplan_xml on", Session, "SET", false},
		{"set xact_abort on", Session, "SET", false},
		{"use master", Session, "USE", false},
		{"execute as user = 'u'", Session, "EXECUTE", false},
		{"revert", Session, "REVERT", false},
		{"open symmetric key k decryption by password = 'p'", Session, "OPEN", false},

		// Transactions.
		{"begin tran", Transaction, "BEGIN", false},
		{"begin transaction t1 with mark 'x'", Transaction, "BEGIN", false},
		{"begin distributed transaction", Transaction, "BEGIN", false},
		{"commit", Transaction, "COMMIT", false},
		{"commit tran", Transaction, "COMMIT", false},
		{"rollback transaction sp1", Transaction, "ROLLBACK", false},
		{"save tran sp1", Transaction, "SAVE", false},
		{"select 1 commit", Transaction, "COMMIT", false},

		// Writes.
		{"insert into t values (1)", Write, "INSERT", false},
		{"insert into #t values (1)", Write, "INSERT", false},
		{"update t set a = 1 where id = 1", Write, "UPDATE", false},
		{"update t set a = 1", Write, "UPDATE", true},
		{"update t set a = case when b = 1 then 2 else 3 end where id = 1", Write, "UPDATE", false},
		{"delete from t", Write, "DELETE", true},
		{"delete t where id = 1", Write, "DELETE", false},
		{"merge into t using s on t.id = s.id when matched then update set a = s.a when not matched then insert (a) values (s.a) when not matched by source then delete;", Write, "MERGE", false},
		{"select * into #t from x", Write, "SELECT", false},
		{"select * into dbo.copy from x", Write, "SELECT", false},
		{"select * from t with (updlock)", Write, "SELECT", false},
		{"select * from t with (holdlock, rowlock)", Write, "SELECT", false},
		{"select * from t (xlock)", Write, "SELECT", false},
		{"select * from t with (serializable)", Write, "SELECT", false},
		{"select * from openrowset(bulk 'c:\\x.txt', single_clob) as f", Write, "SELECT", false},
		{"select * from openquery(srv, 'select 1')", Write, "SELECT", false},
		{"select next value for dbo.seq", Write, "SELECT", false},
		{"declare @x int = next value for dbo.seq", Write, "DECLARE", false},
		{"set @x = next value for dbo.seq", Write, "SET", false},
		{"bulk insert t from 'c:\\x.csv'", Write, "BULK", false},
		{"insert into t exec p", Write, "INSERT", false},
		{"exec dbo.p", Write, "EXEC", false},
		{"exec p", Write, "EXEC", false},
		{"myproc 1", Write, "EXEC", false},
		{"help", Write, "EXEC", false},
		{"show tables", Write, "EXEC", false},
		{"[sp_who]", Read, "EXEC", false},
		{"exec dbo.sp_help", Write, "EXEC", false},
		{"exec master.dbo.sp_who", Write, "EXEC", false},
		{"exec master..sp_who", Write, "EXEC", false},
		{"exec @proc", Write, "EXEC", false},
		{"exec sp_configure 'show advanced options', 1", Write, "EXEC", false},
		{"exec sp_configure @configvalue = 1", Write, "EXEC", false},
		{"exec sp_spaceused @updateusage = 'true'", Write, "EXEC", false},
		{"exec sp_spaceused 't', 'true'", Write, "EXEC", false},
		{"exec xp_dirtree 'c:\\'", Write, "EXEC", false},
		{"exec sp_executesql N'select 1'", Write, "EXEC", false},
		{"exec sp_executesql N'delete from t'", Write, "EXEC", true},
		{"exec sp_executesql @stmt = N'delete from t where id = @id', N'@id int', @id = 1", Write, "EXEC", false},
		{"exec ('select 1')", Write, "EXEC", false},
		{"exec ('drop table t')", Write, "EXEC", true},
		{"exec ('delete from ' + @t)", Write, "EXEC", true},
		{"exec (N'select 1; ' + N'truncate table t')", Write, "EXEC", true},
		{"exec (@sql)", Write, "EXEC", false},
		{"exec ('exec (''drop table t'')')", Write, "EXEC", true},
		{`exec ("delete from t")`, Write, "EXEC", true},
		{"exec sp_msforeachtable 'truncate table ?'", Write, "EXEC", true},
		{"exec xp_cmdshell 'dir'", Write, "EXEC", true},
		{"dbcc checkdb (d, repair_allow_data_loss)", Write, "DBCC", true},
		{"dbcc checkdb (d, repair_rebuild)", Write, "DBCC", false},
		{"dbcc freeproccache", Write, "DBCC", false},
		{"dbcc sqlperf('sys.dm_os_wait_stats', clear)", Write, "DBCC", false},
		{"dbcc checkident ('t')", Write, "DBCC", false},
		{"dbcc writepage (d, 1, 1, 0, 1, 0x00)", Write, "DBCC", true},
		{"backup database d to disk = 'x'", Write, "BACKUP", false},
		{"restore database d from disk = 'x'", Write, "RESTORE", true},
		{"restore headeronly from disk = 'x'", Write, "RESTORE", false},
		{"kill 52", Write, "KILL", false},
		{"shutdown", Write, "SHUTDOWN", true},
		{"shutdown with nowait", Write, "SHUTDOWN", true},
		{"reconfigure", Write, "RECONFIGURE", false},
		{"checkpoint", Write, "CHECKPOINT", false},
		{"receive * from q", Write, "RECEIVE", false},
		{"waitfor (receive top (1) * from q), timeout 1000", Write, "WAITFOR", false},
		{"send on conversation @h (N'x')", Write, "SEND", false},
		{"begin dialog conversation @h from service a to service 'b'", Write, "BEGIN", false},
		{"end conversation @h", Write, "END", false},
		{"writetext t.c @p 'x'", Write, "WRITETEXT", false},
		{"update statistics t", Write, "UPDATE", false},
		{"raiserror('x', 16, 1) with log", Write, "RAISERROR", false},
		{"declare c cursor for select a from t for update of a", Write, "DECLARE", false},
		{"frobnicate", Write, "EXEC", false},

		// Schema changes.
		{"create table t (a int)", DDL, "CREATE", false},
		{"create table t (a int references u(id) on delete cascade on update no action)", DDL, "CREATE", false},
		{"alter table t add c int", DDL, "ALTER", false},
		{"alter table t drop column c", DDL, "ALTER", true},
		{"alter table t alter column c bigint", DDL, "ALTER", false},
		{"alter database d set single_user with rollback immediate", DDL, "ALTER", false},
		{"alter table t switch partition 1 to t2 partition 1", DDL, "ALTER", true},
		{"alter partition function pf() merge range (10)", DDL, "ALTER", false},
		{"drop table t", DDL, "DROP", true},
		{"drop table if exists t", DDL, "DROP", true},
		{"truncate table t", DDL, "TRUNCATE", true},
		{"grant select, insert, update, delete on t to u", DDL, "GRANT", false},
		{"deny delete on t to u", DDL, "DENY", false},
		{"revoke grant option for update on t to u cascade", DDL, "REVOKE", false},
		{"grant execute on p to u with grant option", DDL, "GRANT", false},
		{"grant create table, alter any schema to u", DDL, "GRANT", false},
		{"enable trigger tr on t", DDL, "ENABLE", false},
		{"disable trigger all on database", DDL, "DISABLE", false},
		{"create trigger tr on t after insert, update, delete as begin set nocount on; select 1 end", DDL, "CREATE", false},
		{"create trigger tr on t instead of delete as delete from t2 where id in (select id from deleted)", DDL, "CREATE", false},
		{"create trigger tr on t for update as if update(a) print 'a'", DDL, "CREATE", false},
		{"create procedure p as delete from t", DDL, "CREATE", true},
		{"create or alter procedure p @a int as begin update t set a = @a where id = 1; end", DDL, "CREATE", false},
		{"create procedure p with execute as owner as select 1", DDL, "CREATE", false},
		{"create view v as select * from t", DDL, "CREATE", false},
		{"create function f() returns int as begin return 1 end", DDL, "CREATE", false},
		{"create sequence s start with 1 increment by 1", DDL, "CREATE", false},

		// Writes a batch hides.
		{"select 1 delete from t", Write, "DELETE", true},
		{"select * from t\ndelete from t where id = 1", Write, "DELETE", false},
		{"set nocount on delete from t", Write, "DELETE", true},
		{"set identity_insert t on insert into t (id) values (1)", Write, "INSERT", false},
		{"delete from t select * from x where 1 = 0", Write, "DELETE", true},
		{"delete from t output deleted.* delete from u", Write, "DELETE", true},
		{"update t set a = 1 where id = 1 set @x = 2", Write, "UPDATE", false},
		{"update t set a = 1 set @x = 2 where 1 = 0", Write, "UPDATE", true},
		{"if 1 = 1 drop table t", DDL, "DROP", true},
		{"while 1 = 1 begin delete from t end", Write, "DELETE", true},
		{"begin try drop table t end try begin catch end catch", DDL, "DROP", true},
		{"insert into t select * from (delete from x output deleted.*) as d", Write, "INSERT", true},
		{"select 1 receive * from q", Write, "RECEIVE", false},
		{"select 1 enable trigger tr on t", DDL, "ENABLE", false},
		{"select 1 dbcc freeproccache", Write, "DBCC", false},
		{"with c as (select 1 a) delete from t", Write, "DELETE", true},
		{"with c as (select id from x) update t set a = 1 from t join c on c.id = t.id where t.b = 2", Write, "UPDATE", false},
		{"with c as (select 1 a) select * into #t from c", Write, "SELECT", false},
		{"select 1 -- x\rdelete from t", Write, "DELETE", true},
		{"lbl:delete from t", Write, "DELETE", true},
		{"lbl :delete from t", Write, "DELETE", true},
		{"select * from t; delete from t where id = 1", Write, "DELETE", false},
		{"select 1; delete from t", Write, "DELETE", true},
		{"select 1;;delete from t", Write, "DELETE", true},
		{"select 1\nGO\nselect 2", Write, "SELECT", true},
		{"GO 5", Write, "GO", true},
		{"select 1 exec p", Write, "EXEC", false},
		{"select 1 sp_who", Read, "SELECT", false},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			got := Classify(tt.src, TSQL)
			if got.Class != tt.class || got.Verb != tt.verb || got.Dangerous != tt.dangerous {
				t.Fatalf("got %+v, want class %v verb %q dangerous %v", got, tt.class, tt.verb, tt.dangerous)
			}
			if got.Dangerous && got.Reason == "" {
				t.Fatalf("dangerous without a reason: %+v", got)
			}
		})
	}
}

func TestTSQLClassifyReasons(t *testing.T) {
	tests := []struct{ src, reason string }{
		{"create procedure p as delete from t", routineHolds},
		{"exec ('drop table t')", "the SQL it runs is dangerous: "},
		{"exec xp_cmdshell 'dir'", "xp_cmdshell"},
		{"GO 5", "GO 5"},
	}
	for _, tt := range tests {
		if got := Classify(tt.src, TSQL); !strings.Contains(got.Reason, tt.reason) {
			t.Errorf("Classify(%q).Reason = %q, want it to hold %q", tt.src, got.Reason, tt.reason)
		}
	}
}

func TestTSQLFormat(t *testing.T) {
	got := FormatWith("select date, [select] from t where money = 1\ngo\nexec p", TSQL, SplitOptions{})
	want := "SELECT\n  date,\n  [select]\nFROM t\nWHERE money = 1\n\ngo\n\nEXEC p"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	for _, src := range []string{
		"create procedure p as\nbegin\n  select 1;\n  -- c\n  select 2;\nend\nGO 3\nselect N'x', [a]]b], #t.c, @v from t",
		"select 1 - -1, a--b\n, 2",
	} {
		if got, want := significantTexts(FormatWith(src, TSQL, SplitOptions{}), TSQL), significantTexts(src, TSQL); !reflect.DeepEqual(got, want) {
			t.Fatalf("Format changed tokens of %q\ngot  %q\nwant %q", src, got, want)
		}
	}
}

func TestTSQLKeywordsAndFunctions(t *testing.T) {
	kw := Keywords(TSQL)
	for _, k := range []string{"SELECT", "GO", "MERGE", "NVARCHAR", "RAISERROR", "WAITFOR"} {
		if !slices.Contains(kw, k) {
			t.Errorf("keyword %s missing", k)
		}
	}
	fns := Functions(TSQL)
	for _, f := range []string{"getdate", "isnull", "datediff", "string_agg", "newid"} {
		if !slices.Contains(fns, f) {
			t.Errorf("function %s missing", f)
		}
	}
}

func FuzzTSQL(f *testing.F) {
	for _, s := range []string{
		"select 1\nGO 2\n[a]]b] N'x' /* /* */ */ -- c\r$1 @x #t",
		"begin try exec ('drop table t') end try begin catch end catch",
		"create proc p as if 1=1 begin delete t end else update t set a = case when 1=1 then 2 end",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		checkCoverage(t, src, Tokenize(src, TSQL))
		SplitWith(src, TSQL, SplitOptions{})
		Classify(src, TSQL)
		Params(src, TSQL)
		if got, want := significantTexts(FormatWith(src, TSQL, SplitOptions{}), TSQL), significantTexts(src, TSQL); !reflect.DeepEqual(got, want) {
			t.Fatalf("Format changed tokens for %q", src)
		}
	})
}

// The editor runs StatementAtWith's statement at the caret, and each of
// SplitWith's in a selection or script: in T-SQL, a batch.
func TestTSQLStatementAtIsTheBatch(t *testing.T) {
	src := "declare @x int = 1;\nset @x += 1;\nselect @x;\nGO\nselect 2"
	for _, caret := range []int{0, strings.Index(src, "set"), strings.Index(src, "select @x") + 3} {
		st, ok := StatementAtWith(src, caret, TSQL, SplitOptions{})
		if !ok || st.Text != "declare @x int = 1;\nset @x += 1;\nselect @x;" {
			t.Fatalf("caret %d: got %q, %v", caret, st.Text, ok)
		}
	}
	if st, _ := StatementAtWith(src, len(src), TSQL, SplitOptions{}); st.Text != "select 2" {
		t.Fatalf("after GO: got %q", st.Text)
	}
	selected := src[:strings.Index(src, "GO")]
	if got := splitTexts(selected, TSQL, BlankLineAndSemicolon); len(got) != 1 {
		t.Fatalf("a selected batch splits into %q", got)
	}
}

// Completion and the result grid's table read a bracketed name with its
// ]] as one ].
func TestTSQLBracketNamesDecoded(t *testing.T) {
	src := "SELECT * FROM [s]]x].[a]]b] AS [c]]d] WHERE [c]]d]."
	ctx := CompletionAt(src, len(src), TSQL)
	if ctx.Qualifier != "c]d" || len(ctx.Tables) != 1 || ctx.Tables[0].Name != "a]b" || ctx.Tables[0].Schema != "s]x" || ctx.Tables[0].Alias != "c]d" {
		t.Fatalf("%+v", ctx)
	}
	if ref, why := SingleTable("SELECT [v]]w] FROM [a]]b]", TSQL); why != "" || ref.Name != "a]b" {
		t.Fatalf("%+v %s", ref, why)
	}
}
