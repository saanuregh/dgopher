package sqltext

import (
	"strings"
	"unicode/utf8"
)

// What Oracle Database 23ai Free was found to do (task 9.1 of the
// more-engines plan; TestIntegrationLexingSpaces in internal/db is to
// check it on each run):
//
//   - its SQL parser reads NUL and the ideographic space U+3000 as spaces,
//     besides ASCII's, and its PL/SQL compiler none of them, which it
//     rejects;
//   - its SQL parser reads the full-width forms of oraclePunctuation as the
//     ASCII punctuation they stand for, so "IN (1）FOR UPDATE" locks rows,
//     but not in PL/SQL, and none as quotes or comments;
//   - block comments do not nest, a line comment ends only at a line feed,
//     and a quoted name ends at its next '"';
//   - one call runs one SQL statement, a trailing ';' allowed, or one
//     PL/SQL block; any text after either is an error (ORA-03405).

// oraclePunctuation are the full-width forms Oracle's SQL parser reads as
// the ASCII punctuation they stand for. A sweep of the Basic Multilingual
// Plane found no other character standing for punctuation.
const oraclePunctuation = "！＆（）＊＋，－．／：；＜＝＞＠［］＾｛｜｝"

// oraclePunctuationWidth returns the length in bytes of a character of
// oraclePunctuation at s[i], or 0.
func oraclePunctuationWidth(s string, i int) int {
	if s[i] != 0xEF { // the first byte of every U+FFxx
		return 0
	}
	r, size := utf8.DecodeRuneInString(s[i:])
	if strings.ContainsRune(oraclePunctuation, r) {
		return size
	}
	return 0
}

const oraclePunctuationReason = "it holds full-width punctuation outside quotes, which Oracle reads as ASCII punctuation, so the statement is not read as the server reads it"

// oraclePunctuationOutsideQuotes reports whether a token of stmt, outside
// strings, quoted names and comments, is one of oraclePunctuation.
func oraclePunctuationOutsideQuotes(stmt string, q quoting) bool {
	if !strings.Contains(stmt, "\xef\xbc") && !strings.Contains(stmt, "\xef\xbd") {
		return false
	}
	for _, t := range tokenizeQuoted(stmt, Oracle, q) {
		if t.Kind == Operator && oraclePunctuationWidth(t.Text, 0) > 0 {
			return true
		}
	}
	return false
}

// oracleQuoteEnd returns the end of the alternative-quoted string at s[i],
// [nNuU]q'X…X', or 0 when none starts there. X is any character but a
// space, tab, line feed or quote; [, {, ( and < are closed by ], }, ) and
// >; the first X' ends the string.
func oracleQuoteEnd(s string, i int) int {
	j := i
	if j < len(s) && strings.IndexByte("nNuU", s[j]) >= 0 {
		j++
	}
	if j+2 >= len(s) || s[j] != 'q' && s[j] != 'Q' || s[j+1] != '\'' {
		return 0
	}
	j += 2
	closer, size := oracleQuoteCloser(s[j:])
	if size == 0 {
		return 0
	}
	j += size
	if k := strings.Index(s[j:], closer+"'"); k >= 0 {
		return j + k + len(closer) + 1
	}
	return len(s)
}

// oracleQuoteCloser returns what closes an alternative-quoted string whose
// delimiter starts rest, and the delimiter's length, 0 for none allowed.
func oracleQuoteCloser(rest string) (string, int) {
	open, size := utf8.DecodeRuneInString(rest)
	switch open {
	case ' ', '\t', '\n', '\'':
		return "", 0
	case '[':
		return "]", 1
	case '{':
		return "}", 1
	case '(':
		return ")", 1
	case '<':
		return ">", 1
	}
	return rest[:size], size
}

// decodeOracleQuoted returns the text an Oracle string or quoted name
// stands for: q'X…X' without its delimiters, N'…' and U'…' as '…', and
// "…", which has no escape, without its quotes.
func decodeOracleQuoted(text string) string {
	if oracleQuoteEnd(text, 0) > 0 {
		j := strings.IndexByte(text, '\'') + 1
		closer, size := oracleQuoteCloser(text[j:])
		body := text[j+size:]
		if k := strings.Index(body, closer+"'"); k >= 0 {
			return body[:k]
		}
		return body
	}
	switch {
	case len(text) > 1 && strings.IndexByte("nNuU", text[0]) >= 0 && text[1] == '\'':
		return unquoteText(text[1:])
	case strings.HasPrefix(text, `"`):
		return strings.TrimSuffix(text[1:], `"`)
	case strings.HasPrefix(text, "'"):
		return unquoteText(text)
	}
	return text
}

// oracleUnitObjects are what CREATE makes that SQL*Plus reads as a PL/SQL
// unit, whose text holds ';' and ends at a "/" line, and
// oracleCreateModifiers the words that may come between.
var (
	oracleUnitObjects     = setOf("PROCEDURE FUNCTION PACKAGE TRIGGER TYPE LIBRARY JAVA")
	oracleCreateModifiers = setOf("OR REPLACE EDITIONABLE NONEDITIONABLE EDITIONING AND COMPILE RESOLVE NOFORCE FORCE")
)

// oracleUnitStart reports whether the statement whose first code token is
// toks[i] is a PL/SQL unit, as SQL*Plus reads it: an anonymous block,
// DECLARE, BEGIN or a <<label>>; CREATE of one of oracleUnitObjects or an
// MLE MODULE; or a query whose WITH declares PL/SQL (declaresPLSQL), as a
// subquery's may too.
func oracleUnitStart(toks []Token, i int) bool {
	var words []Token
	for k := i; k < len(toks) && len(words) < 10; k++ {
		if significant(toks[k]) {
			words = append(words, toks[k])
		}
	}
	first := words[0]
	switch {
	case first.Kind == Operator && first.Text == "<<", isWord(first, "BEGIN"), isWord(first, "DECLARE"):
		return true
	case isWord(first, "WITH"):
		return declaresPLSQL(words, 0)
	case isWord(first, "CREATE"):
		k := 1
		for k < len(words) && oracleCreateModifiers[word(words[k])] {
			k++
		}
		if k >= len(words) {
			return false
		}
		w := word(words[k])
		return oracleUnitObjects[w] || w == "MLE" && k+1 < len(words) && word(words[k+1]) == "MODULE"
	}
	return false
}

// declaresPLSQL reports whether the WITH at toks[i] declares a PL/SQL
// FUNCTION or PROCEDURE, whose body holds ';', rather than name a common
// table expression "function", as "WITH function AS (".
func declaresPLSQL(toks []Token, i int) bool {
	if !isWord(toks[i], "WITH") {
		return false
	}
	var next []Token
	for k := i + 1; k < len(toks) && len(next) < 2; k++ {
		if significant(toks[k]) {
			next = append(next, toks[k])
		}
	}
	return len(next) == 2 && (isWord(next[0], "FUNCTION") || isWord(next[0], "PROCEDURE")) &&
		!isWord(next[1], "AS") && !isPunct(next[1], "(")
}

// oracleImplicitCommits are the statements Oracle runs as DDL, besides
// ALTER but ALTER SESSION and ALTER SYSTEM: they commit the open
// transaction before they run and after. One that fails may still have
// committed: a DROP of a missing table and a CREATE of a name in use did,
// a CREATE with a duplicate column did not (verified on 23ai).
var oracleImplicitCommits = setOf("CREATE DROP TRUNCATE RENAME COMMENT GRANT REVOKE AUDIT NOAUDIT ANALYZE ASSOCIATE DISASSOCIATE FLASHBACK PURGE")

// OracleCommitsImplicitly reports whether an Oracle statement, given its
// significant words with names upper-cased, commits the open transaction
// without being asked: every DDL statement does (oracleImplicitCommits).
// A PL/SQL block may too, by its COMMIT or the DDL it runs, which only the
// server's transaction state tells.
func OracleCommitsImplicitly(words []string) bool {
	if len(words) == 0 {
		return false
	}
	if words[0] == "ALTER" {
		return len(words) > 1 && words[1] != "SESSION" && words[1] != "SYSTEM"
	}
	return oracleImplicitCommits[words[0]]
}

// oracleReads are the functions of Oracle's DBMS_ and UTL_ packages known
// only to read, as "package.function", or "package.*" for all of a
// package's: any other of theirs, as one a later version adds, counts as a
// write (functionRules).
var oracleReads = setOf("dbms_metadata.get_ddl dbms_metadata.get_dependent_ddl dbms_metadata.get_granted_ddl " +
	"dbms_metadata.get_xml dbms_metadata.get_dependent_xml dbms_metadata.get_granted_xml dbms_metadata.get_sxml " +
	"dbms_metadata.get_sxml_ddl dbms_xplan.display dbms_xplan.display_awr dbms_xplan.display_cursor dbms_xplan.display_plan " +
	"dbms_xplan.display_sql_plan_baseline dbms_xplan.display_sqlset dbms_xplan.display_workload_repository " +
	"dbms_random.value dbms_random.normal dbms_random.string dbms_random.random " +
	"dbms_utility.get_time dbms_utility.get_cpu_time dbms_utility.format_error_stack dbms_utility.format_error_backtrace " +
	"dbms_utility.format_call_stack dbms_utility.get_hash_value dbms_utility.current_instance dbms_utility.get_endianness " +
	"dbms_lob.getlength dbms_lob.substr dbms_lob.instr dbms_lob.compare dbms_lob.getchunksize dbms_lob.isopen " +
	"dbms_lob.istemporary dbms_lob.get_storage_limit dbms_assert.* dbms_crypto.hash dbms_crypto.mac dbms_crypto.encrypt " +
	"dbms_crypto.decrypt dbms_crypto.randombytes dbms_crypto.randominteger dbms_crypto.randomnumber " +
	"dbms_session.is_role_enabled dbms_session.unique_session_id dbms_session.is_session_alive " +
	"dbms_transaction.local_transaction_id dbms_flashback.get_system_change_number dbms_sqltune.report_sql_monitor " +
	"dbms_sql_monitor.report_sql_monitor utl_raw.* utl_encode.* utl_i18n.* utl_url.* utl_match.*")

// oracleSysReads are the SYS schema's functions known only to read when
// called by their qualified name: the ODCI collection types' constructors.
var oracleSysReads = setOf("odcinumberlist odcivarchar2list odcidatelist odcirawlist odcibfilelist")

// oracleSideEffect returns the first call in toks that changes state, or
// reaches beyond the database, in Oracle, or "": a sequence's NEXTVAL;
// HTTPURITYPE; a function of functionRules' Oracle rules, as a DBMS_ or
// UTL_ package's, which, having no arguments, may be called without
// parentheses, unless it stands after FROM, JOIN, INTO or UPDATE as a
// table: after FROM or JOIN, one followed by ( is a table function's call.
// Quoted names are decoded, in any case: one that names nothing runs
// nothing.
func oracleSideEffect(toks []Token) string {
	for i, t := range toks {
		name := oracleNameOf(t)
		if name == "" {
			continue
		}
		qualified := i >= 2 && isPunct(toks[i-1], ".")
		if qualified && name == "nextval" {
			return "NEXTVAL"
		}
		if !qualified && sideEffectFunctions[Oracle][name] {
			return name
		}
		if i+1 < len(toks) && isPunct(toks[i+1], ".") {
			continue // a qualifier
		}
		call := i+1 < len(toks) && isPunct(toks[i+1], "(")
		schema := ""
		if qualified {
			schema = oracleNameOf(toks[i-2])
			if i >= 3 {
				switch word(toks[i-3]) {
				case "FROM", "JOIN":
					if !call {
						continue // a table
					}
				case "INTO", "UPDATE":
					continue // a table, and its columns' list
				}
			}
		}
		for _, r := range functionRules {
			if r.dialect != Oracle || !call && !r.byPackage || !r.writesCall(name, schema, false) {
				continue
			}
			if schema == "" {
				return name
			}
			return schema + "." + name
		}
	}
	return ""
}

// oracleNameOf returns the lower-case name a keyword, name or quoted name
// token stands for, or "".
func oracleNameOf(t Token) string {
	switch t.Kind {
	case Keyword, Identifier:
		return strings.ToLower(t.Text)
	case QuotedIdent:
		return strings.ToLower(decodeOracleQuoted(t.Text))
	}
	return ""
}

// oracleCodeCalls are the procedures that run, or schedule to run, the
// SQL or PL/SQL one of their string arguments holds, with that argument's
// name: DBMS_SQL.PARSE runs DDL as it parses it; a job runs its action
// later, in a session of its own.
var oracleCodeCalls = map[string]string{
	"dbms_sql.parse": "statement", "dbms_utility.exec_ddl_statement": "parse_string", "dbms_scheduler.create_job": "job_action",
	"dbms_job.submit": "what", "dbms_job.isubmit": "what", "dbms_job.change": "what", "dbms_job.what": "what",
}

const plsqlBlockHolds = "the PL/SQL block holds a statement that is dangerous on its own: "

// classifyOracleVerb classifies one Oracle statement, or a statement of a
// PL/SQL block, by its first word, after any <<label>>. A word that
// begins no SQL statement is a write: a PL/SQL statement, a procedure's
// call, or a SQL*Plus command, as DESCRIBE or SET SERVEROUTPUT, which the
// server refuses (ORA-00900).
func classifyOracleVerb(toks []Token, r reading) Analysis {
	for len(toks) > 2 && toks[0].Kind == Operator && toks[0].Text == "<<" {
		end := 1
		for end < len(toks) && !(toks[end].Kind == Operator && toks[end].Text == ">>") {
			end++
		}
		toks = toks[min(end+1, len(toks)):]
	}
	if len(toks) == 0 {
		return Analysis{Class: Write}
	}
	verb, next := word(toks[0]), ""
	if len(toks) > 1 {
		next = word(toks[1])
	}
	write := Analysis{Class: Write, Verb: verb}
	ddl := Analysis{Class: DDL, Verb: verb}
	if verb == "SELECT" || verb == "WITH" {
		for i := range toks {
			if declaresPLSQL(toks, i) {
				// Its PL/SQL may write, in an autonomous transaction, which
				// a READ ONLY transaction does not stop.
				return write
			}
		}
	}
	switch verb {
	case "SELECT", "INSERT", "MERGE", "UPDATE", "DELETE", "DROP", "TRUNCATE", "CREATE":
		return classifyVerb(toks, Generic, r)
	case "WITH":
		return classifyWith(toks, Oracle, r)
	case "ALTER":
		return classifyOracleAlter(toks, r)
	case "RENAME", "COMMENT", "GRANT", "REVOKE", "AUDIT", "NOAUDIT", "ANALYZE", "ASSOCIATE", "DISASSOCIATE":
		return ddl
	case "FLASHBACK":
		if to := topLevelIndex(toks, "TO"); next == "TABLE" && to >= 0 && to+2 < len(toks) && word(toks[to+1]) == "BEFORE" && word(toks[to+2]) == "DROP" {
			return ddl // brings a dropped table back
		}
		ddl.Dangerous, ddl.Reason = true, "FLASHBACK rewinds the table or database to an earlier point, losing the changes made since"
		return ddl
	case "PURGE":
		ddl.Dangerous, ddl.Reason = true, "PURGE permanently removes dropped objects from the recycle bin"
		return ddl
	case "COMMIT", "ROLLBACK":
		if next == "FORCE" || next == "WORK" && len(toks) > 2 && word(toks[2]) == "FORCE" {
			return write // ends an in-doubt distributed transaction
		}
		return Analysis{Class: Transaction, Verb: verb}
	case "SAVEPOINT":
		return Analysis{Class: Transaction, Verb: verb}
	case "SET":
		switch next {
		case "TRANSACTION", "CONSTRAINT", "CONSTRAINTS":
			return Analysis{Class: Transaction, Verb: verb}
		case "ROLE":
			return Analysis{Class: Session, Verb: verb}
		}
		return write // SQL*Plus's SET, which the server does not run
	case "EXPLAIN":
		// EXPLAIN PLAN inserts rows into PLAN_TABLE, or the table its INTO
		// names; a READ ONLY transaction refuses it.
		return write
	case "EXECUTE":
		if next == "IMMEDIATE" {
			return oracleImmediateDanger(toks[2:], write, r)
		}
	case "FORALL":
		// FORALL i IN a .. b [SAVE EXCEPTIONS] runs one DML statement.
		for j := 1; j < len(toks); j++ {
			switch word(toks[j]) {
			case "INSERT", "UPDATE", "DELETE", "MERGE", "EXECUTE":
				a := classifyOracleVerb(toks[j:], r)
				a.Verb = verb
				return a
			}
		}
		return write
	}
	return oracleCallDanger(toks, write, r)
}

// classifyOracleAlter classifies an ALTER: ALTER SESSION changes the
// session, but for its settings that write trace files on the server
// (EVENTS, SQL_TRACE), set a hidden parameter ("_name"), or let it change
// a logical standby's tables (DISABLE GUARD); ALTER SYSTEM changes the
// instance; any other changes the schema, and is dangerous when it drops,
// truncates, exchanges a partition, or sets columns unused, which loses
// them as dropping does.
func classifyOracleAlter(toks []Token, r reading) Analysis {
	write := Analysis{Class: Write, Verb: "ALTER"}
	next := ""
	if len(toks) > 1 {
		next = word(toks[1])
	}
	switch next {
	case "SESSION":
		if len(toks) > 3 && word(toks[2]) == "DISABLE" && word(toks[3]) == "GUARD" {
			return write
		}
		for _, t := range toks[2:] {
			name := strings.ToUpper(oracleNameOf(t))
			if name == "EVENTS" || name == "SQL_TRACE" || strings.HasPrefix(name, "_") {
				return write
			}
		}
		return Analysis{Class: Session, Verb: "ALTER"}
	case "SYSTEM":
		return write
	}
	a := classifyVerb(toks, Generic, r)
	if a.Dangerous {
		return a
	}
	dep := depths(toks)
	for i := 1; i+1 < len(toks); i++ {
		if dep[i] > 0 {
			continue
		}
		switch w, next := word(toks[i]), word(toks[i+1]); {
		case w == "SET" && next == "UNUSED":
			a.Dangerous, a.Reason = true, "ALTER TABLE ... SET UNUSED makes the columns unreachable, losing their data as dropping them does"
		case w == "EXCHANGE" && (next == "PARTITION" || next == "SUBPARTITION"):
			a.Dangerous, a.Reason = true, "ALTER TABLE ... EXCHANGE PARTITION moves the partition's rows out of the table"
		case w == "TRUNCATE" && next == "SUBPARTITION":
			a.Dangerous, a.Reason = true, "ALTER TABLE ... TRUNCATE SUBPARTITION deletes the subpartition's rows"
		}
	}
	return a
}

// oracleImmediateDanger returns a, dangerous when the SQL that EXECUTE
// IMMEDIATE runs, the strings of toks joined by ||, up to its INTO, USING,
// RETURNING or BULK COLLECT, is.
func oracleImmediateDanger(toks []Token, a Analysis, r reading) Analysis {
	dep := depths(toks)
	end := len(toks)
scan:
	for i, t := range toks {
		if dep[i] == 0 {
			switch word(t) {
			case "INTO", "USING", "RETURNING", "RETURN", "BULK":
				end = i
				break scan
			}
		}
	}
	if end == 0 {
		return a
	}
	if inner, ok := codeDanger(executedText(unwrapParens(toks[:end]), Oracle, r.q), Oracle, r.deeper()); ok {
		a.Dangerous, a.Reason = true, heldReason(r, "the SQL it runs is dangerous: ", inner)
	}
	return a
}

// oracleCallDanger returns a, dangerous when toks, after any CALL, EXEC
// or EXECUTE, call one of oracleCodeCalls whose string arguments, each read
// as the strings joined by ||, in any parentheses, hold dangerous SQL: those
// passed by position, and the one oracleCodeCalls names passed by name.
func oracleCallDanger(toks []Token, a Analysis, r reading) Analysis {
	if len(toks) > 0 {
		switch word(toks[0]) {
		case "CALL", "EXEC", "EXECUTE":
			toks = toks[1:]
		}
	}
	open := 0
	var parts []string
	for open < len(toks) {
		name := oracleNameOf(toks[open])
		if name == "" {
			return a
		}
		parts = append(parts, name)
		if open+1 < len(toks) && isPunct(toks[open+1], ".") {
			open += 2
			continue
		}
		open++
		break
	}
	if len(parts) < 2 || open >= len(toks) || !isPunct(toks[open], "(") {
		return a
	}
	code, ok := oracleCodeCalls[parts[len(parts)-2]+"."+parts[len(parts)-1]]
	if !ok {
		return a
	}
	end := skipParens(toks, open)
	args := toks[open+1 : max(end-1, open+1)]
	dep := depths(args)
	start := 0
	for i := 0; i <= len(args); i++ {
		if i < len(args) && !(dep[i] == 0 && isPunct(args[i], ",")) {
			continue
		}
		arg := args[start:i]
		if len(arg) >= 3 && isOperator(arg[1], "=") && isOperator(arg[2], ">") {
			if oracleNameOf(arg[0]) != code {
				arg = nil
			} else {
				arg = arg[3:]
			}
		}
		if arg = unwrapParens(arg); len(arg) > 0 && arg[0].Kind == String {
			if inner, ok := codeDanger(executedText(arg, Oracle, r.q), Oracle, r.deeper()); ok {
				a.Dangerous, a.Reason = true, heldReason(r, "the SQL it runs is dangerous: ", inner)
				return a
			}
		}
		start = i + 1
	}
	return a
}

// unwrapParens returns toks without the parentheses around all of them.
func unwrapParens(toks []Token) []Token {
	for len(toks) >= 2 && isPunct(toks[0], "(") && isPunct(toks[len(toks)-1], ")") && closes(toks) {
		toks = toks[1 : len(toks)-1]
	}
	return toks
}
