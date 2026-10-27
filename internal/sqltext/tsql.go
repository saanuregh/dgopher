package sqltext

import (
	"strings"
)

// T-SQL needs no ';' between statements, so one batch may hold several:
// "SELECT 1 DELETE FROM t" is two. classifyTSQL reads a batch as the
// statements it holds, each where a word in tsqlStatementWords starts one,
// and counts it as the strictest. Those words are reserved, so name
// nothing unless delimited, but for those tsqlStatementWord checks
// further; one that starts no statement, as the DELETE of GRANT DELETE,
// still raises the class to its own.

// tsqlStatementWords are the words that begin a T-SQL statement.
var tsqlStatementWords = setOf("SELECT INSERT UPDATE DELETE MERGE TRUNCATE CREATE ALTER DROP GRANT DENY REVOKE " +
	"EXEC EXECUTE DECLARE SET PRINT RAISERROR THROW IF ELSE WHILE BEGIN END RETURN BREAK CONTINUE GOTO WAITFOR " +
	"USE COMMIT ROLLBACK SAVE KILL SHUTDOWN RECONFIGURE CHECKPOINT DBCC BACKUP RESTORE BULK OPEN CLOSE FETCH " +
	"DEALLOCATE READTEXT WRITETEXT UPDATETEXT SETUSER REVERT DUMP LOAD WITH ENABLE DISABLE RECEIVE SEND MOVE GET")

// tsqlWrites and tsqlSchemaChanges are the statement words that raise a
// batch to a write or a schema change wherever they stand.
var (
	tsqlWrites = setOf("INSERT UPDATE DELETE MERGE EXEC EXECUTE BULK WRITETEXT UPDATETEXT KILL SHUTDOWN " +
		"RECONFIGURE CHECKPOINT DBCC BACKUP RESTORE RECEIVE SEND MOVE GET DUMP LOAD")
	tsqlSchemaChanges = setOf("CREATE ALTER DROP TRUNCATE GRANT DENY REVOKE ENABLE DISABLE")
)

// tsqlLockHints are the table hints whose locks stay until the
// transaction ends, as FOR UPDATE's do: a read with one counts as a write.
// The isolation levels count only as hints, in parentheses, not in SET
// TRANSACTION ISOLATION LEVEL.
var (
	tsqlLockHints      = setOf("UPDLOCK XLOCK HOLDLOCK TABLOCKX")
	tsqlHeldLockLevels = setOf("SERIALIZABLE REPEATABLEREAD")
)

// tsqlNotAfter are the words after which a statement word is part of the
// statement, never the start of another: none ends a statement.
var tsqlNotAfter = setOf("GRANT DENY REVOKE FOR OF THEN OR AND NOT WITH")

// tsqlOperand are the words after which a name stands, so RECEIVE there
// is a column or a table, not the statement.
var tsqlOperand = setOf("SELECT WHERE AND OR NOT ON BY SET WHEN THEN ELSE AS DISTINCT TOP IN LIKE IS BETWEEN " +
	"FROM JOIN INTO UPDATE TABLE HAVING RETURN PRINT EXISTS CASE")

// tsqlProcedureReads are SQL Server's system procedures known only to
// read, called by their name alone or in the sys schema; any other sp_ or
// xp_ procedure counts as a write, as do those of tsqlProcedureArgLimits
// given more.
var tsqlProcedureReads = setOf("sp_help sp_helptext sp_helpindex sp_helpconstraint sp_helpdb sp_helpfile " +
	"sp_helpfilegroup sp_helplogins sp_helprole sp_helprolemember sp_helpuser sp_helprotect sp_helpsrvrole " +
	"sp_helpsrvrolemember sp_helptrigger sp_helpstats sp_helpserver sp_helpsort sp_helplanguage sp_helpdevice " +
	"sp_helpextendedproc sp_helpntgroup sp_helplinkedsrvlogin sp_columns sp_tables sp_stored_procedures " +
	"sp_sproc_columns sp_fkeys sp_pkeys sp_statistics sp_special_columns sp_table_privileges sp_column_privileges " +
	"sp_server_info sp_databases sp_datatype_info sp_who sp_who2 sp_lock sp_spaceused sp_configure sp_depends " +
	"sp_describe_first_result_set sp_describe_undeclared_parameters sp_readerrorlog sp_enumerrorlogs " +
	"xp_readerrorlog xp_fixeddrives xp_msver")

// tsqlProcedureArgLimits are the procedures of tsqlProcedureReads that
// change the server when given more than one argument, or one named other
// than their first parameter: sp_configure sets an option, and
// sp_spaceused updates the space counts with @updateusage.
var tsqlProcedureArgLimits = map[string]string{"sp_configure": "@configname", "sp_spaceused": "@objname"}

// tsqlCodeProcedures run the strings they are given as SQL.
var tsqlCodeProcedures = setOf("sp_executesql sp_msforeachtable sp_msforeachdb")

// dbccReads are the DBCC commands that only check or report, without a
// REPAIR_ option.
var dbccReads = setOf("CHECKDB CHECKTABLE CHECKALLOC CHECKCATALOG CHECKFILEGROUP CHECKCONSTRAINTS SHOW_STATISTICS " +
	"SHOWCONTIG INPUTBUFFER OUTPUTBUFFER OPENTRAN SQLPERF USEROPTIONS TRACESTATUS HELP MEMORYSTATUS LOGINFO")

// classifyTSQL classifies a T-SQL batch, its code tokens, as the
// strictest of the statements it holds, those nested in parentheses
// (INSERT … SELECT … FROM (DELETE … OUTPUT …)) and the statement words
// that start none. A routine's statements keep their danger, as
// classifyQuoted keeps it for other dialects.
func classifyTSQL(code []Token, r reading) Analysis {
	code = tsqlColonWords(code)
	stmts, nested, raised := tsqlStatements(code)
	if len(stmts) == 0 {
		return Analysis{Class: Write}
	}
	routine := newTSQLPiece(stmts[0]).routine
	var a Analysis
	for k, stmt := range stmts {
		s := classifyTokens(stmt, TSQL, r)
		if k == 0 {
			a = s
			continue
		}
		if routine && s.Dangerous {
			s.Reason = heldReason(r, routineHolds, s)
		}
		a = stricter(a, s)
	}
	for _, run := range nested {
		a = stricter(a, classifyTokens(run, TSQL, r))
	}
	if classRank(raised) > classRank(a.Class) {
		a.Class = raised
	}
	return a
}

// BatchStatements are the texts of the statements a T-SQL batch holds, as
// classifyTSQL reads them; for another dialect, and for a batch that
// makes a routine, whose statements run when it is called, the text whole.
func BatchStatements(sql string, d Dialect) []string {
	if d != TSQL {
		return []string{sql}
	}
	var code []Token
	for _, t := range Tokenize(sql, d) {
		if significant(t) {
			code = append(code, t)
		}
	}
	stmts, _, _ := tsqlStatements(tsqlColonWords(code))
	if len(stmts) <= 1 || newTSQLPiece(stmts[0]).routine {
		return []string{sql}
	}
	out := make([]string, len(stmts))
	for i, st := range stmts {
		out[i] = sql[st[0].Start:st[len(st)-1].End]
	}
	return out
}

// stricter returns a raised to b's class, with its verb, when that is
// higher, and to b's danger when a has none.
func stricter(a, b Analysis) Analysis {
	if classRank(b.Class) > classRank(a.Class) {
		a.Class, a.Verb = b.Class, b.Verb
		if !a.Dangerous {
			a.Reason = b.Reason
		}
	}
	if b.Dangerous && !a.Dangerous {
		a.Dangerous, a.Reason = true, b.Reason
	}
	return a
}

// tsqlColonWords splits a :name parameter token into ':' and its name,
// as the server reads a label's colon before a statement written without
// a space, "retry:DELETE …".
func tsqlColonWords(code []Token) []Token {
	var out []Token
	for i, t := range code {
		if t.Kind == Param && len(t.Text) > 1 && t.Text[0] == ':' {
			if out == nil {
				out = append([]Token(nil), code[:i]...)
			}
			kind := Identifier
			if keywordSet(TSQL)[strings.ToUpper(t.Text[1:])] {
				kind = Keyword
			}
			out = append(out, Token{Kind: Operator, Start: t.Start, End: t.Start + 1, Text: ":"},
				Token{Kind: kind, Start: t.Start + 1, End: t.End, Text: t.Text[1:]})
			continue
		}
		if out != nil {
			out = append(out, t)
		}
	}
	if out == nil {
		return code
	}
	return out
}

// tsqlPiece is what the scan of a batch knows of the statement it is in.
type tsqlPiece struct {
	verb string // its first word
	// main is the statement a WITH's common table expressions lead to,
	// "" until it is read, or else verb.
	main string
	// routine marks a CREATE or ALTER of a procedure, function or
	// trigger, whose body holds statements.
	routine bool
	sawSet  bool // an UPDATE's SET passed
}

func newTSQLPiece(code []Token) tsqlPiece {
	i := 0
	for i < len(code) && isPunct(code[i], "(") {
		i++
	}
	if i == len(code) {
		return tsqlPiece{}
	}
	p := tsqlPiece{verb: word(code[i]), main: word(code[i])}
	switch p.verb {
	case "WITH":
		p.main = ""
	case "CREATE", "ALTER":
		p.routine = tsqlRoutineHeader(code)
	}
	return p
}

// tsqlStatements cuts the code tokens of a T-SQL batch into its
// statements, without their ';'. It also returns the INSERT, UPDATE,
// DELETE and MERGE statements nested in parentheses, up to the closing
// one, and the strictest class of the statement words that start no
// statement, Read for none.
func tsqlStatements(code []Token) (stmts, nested [][]Token, raised Class) {
	dep := depths(code)
	raised = Read
	raise := func(w string) {
		c := Read
		switch {
		case tsqlSchemaChanges[w]:
			c = DDL
		case tsqlWrites[w], tsqlLockHints[w], tsqlHeldLockLevels[w]:
			c = Write
		}
		if classRank(c) > classRank(raised) {
			raised = c
		}
	}
	start, cases := 0, 0
	var p tsqlPiece
	for j := 0; j < len(code); j++ {
		t := code[j]
		if isPunct(t, ";") {
			if j > start {
				stmts = append(stmts, code[start:j])
			}
			start = j + 1
			continue
		}
		if j == start {
			p, cases = newTSQLPiece(code[j:]), 0
			continue
		}
		w := word(t)
		if w == "" || isPunct(code[j-1], ".") || isOperator(code[j-1], "::") {
			continue
		}
		if tsqlLockHints[w] {
			raise(w)
			continue
		}
		if dep[j] > 0 {
			switch {
			case tsqlHeldLockLevels[w]:
				raise(w)
			case !tsqlStatementWord(code, j):
			case isPunct(code[j-1], "(") && (w == "INSERT" || w == "UPDATE" || w == "DELETE" || w == "MERGE"):
				end := j
				for end < len(code) && dep[end] >= dep[j] {
					end++
				}
				nested = append(nested, code[j:end])
			default:
				raise(w)
			}
			continue
		}
		switch {
		case w == "CASE":
			cases++
			continue
		case w == "END" && cases > 0:
			cases--
			continue
		case cases > 0:
			continue
		}
		starts, counts := tsqlStarts(code, j, &p)
		switch {
		case starts:
			stmts = append(stmts, code[start:j])
			start, cases = j, 0
			p = newTSQLPiece(code[j:])
		case counts:
			raise(w)
		}
	}
	if start < len(code) {
		stmts = append(stmts, code[start:])
	}
	return stmts, nested, raised
}

// tsqlStatementWord reports whether code[j] is a statement's word where
// it stands: ENABLE and DISABLE before TRIGGER, SEND before ON, MOVE and
// GET before CONVERSATION, and MERGE and UPDATE but as the join and
// union hints MERGE JOIN and MERGE UNION, ALTER PARTITION FUNCTION's
// MERGE RANGE and a trigger's UPDATE(column).
func tsqlStatementWord(code []Token, j int) bool {
	w := word(code[j])
	if !tsqlStatementWords[w] {
		return false
	}
	next := ""
	if j+1 < len(code) {
		next = word(code[j+1])
	}
	switch w {
	case "ENABLE", "DISABLE":
		return next == "TRIGGER"
	case "SEND":
		return next == "ON"
	case "MOVE", "GET":
		return next == "CONVERSATION"
	case "MERGE":
		return next != "JOIN" && next != "UNION" && next != "RANGE"
	case "UPDATE":
		return j+1 == len(code) || !isPunct(code[j+1], "(")
	}
	return true
}

// tsqlStarts reports whether the word code[j], outside parentheses and
// CASE and after the first of statement p, starts a statement, and if
// not, whether it still counts toward the batch's class.
func tsqlStarts(code []Token, j int, p *tsqlPiece) (starts, counts bool) {
	if !tsqlStatementWord(code, j) {
		return false, false
	}
	w, prev := word(code[j]), code[j-1]
	pw, next := word(prev), ""
	if j+1 < len(code) {
		next = word(code[j+1])
	}
	if p.verb == "WITH" && p.main == "" {
		switch w {
		case "SELECT", "INSERT", "UPDATE", "DELETE", "MERGE":
			p.main = w
			return false, false
		}
	}
	switch {
	case isPunct(prev, ","), tsqlNotAfter[pw]:
		return false, true
	case pw == "AFTER" && (p.verb == "CREATE" || p.verb == "ALTER"): // a trigger's AFTER INSERT
		return false, true
	case pw == "ON" && (w == "DELETE" || w == "UPDATE") && (next == "CASCADE" || next == "NO" || next == "SET" || next == "RESTRICT"):
		return false, true
	case pw == "BULK" && w == "INSERT":
		return false, true
	}
	switch w {
	case "THROW":
		// The statement before THROW must end with ';'.
		return pw == "BEGIN" || pw == "TRY" || pw == "CATCH" || pw == "ELSE", false
	case "RECEIVE":
		if tsqlOperand[pw] || prev.Kind == Operator || isPunct(prev, "(") {
			return false, true
		}
	case "WITH":
		return tsqlCommonTableExpression(code, j), false
	case "IF":
		if next == "EXISTS" && dropsBefore(code[max(0, j-3):j]) {
			return false, false
		}
	case "FETCH":
		if pw == "ROWS" || pw == "ROW" { // OFFSET … ROWS FETCH NEXT … ROWS ONLY
			return false, false
		}
	case "SET":
		if pw == "UPDATE" || pw == "DELETE" { // MERGE's UPDATE SET, ON DELETE SET NULL
			return false, false
		}
		if p.main == "UPDATE" && !p.sawSet {
			p.sawSet = true
			return false, false
		}
	}
	if p.verb == "ALTER" && !p.routine {
		switch w {
		case "ALTER", "DROP", "SET", "ROLLBACK": // ALTER COLUMN, DROP COLUMN, SET options
			return false, true
		}
	}
	return true, false
}

// tsqlCommonTableExpression reports whether the WITH at code[j] starts a
// statement: "WITH name [(columns)] AS (" or WITH XMLNAMESPACES, not a
// table hint or an option.
func tsqlCommonTableExpression(code []Token, j int) bool {
	k := j + 1
	if k >= len(code) {
		return false
	}
	if word(code[k]) == "XMLNAMESPACES" {
		return true
	}
	if word(code[k]) == "" && code[k].Kind != QuotedIdent {
		return false
	}
	k++
	if k < len(code) && isPunct(code[k], "(") {
		k = skipParens(code, k)
	}
	return k+1 < len(code) && word(code[k]) == "AS" && isPunct(code[k+1], "(")
}

// classifyTSQLVerb classifies one T-SQL statement by its first word. A
// word that begins no statement is a procedure called by its name alone,
// as a batch's first statement may be.
func classifyTSQLVerb(toks []Token, r reading) Analysis {
	verb, next := word(toks[0]), ""
	if len(toks) > 1 {
		next = word(toks[1])
	}
	read := Analysis{Class: Read, Verb: verb}
	write := Analysis{Class: Write, Verb: verb}
	switch verb {
	case "SELECT":
		if tsqlLocks(toks) || topLevelIndex(toks, "INTO") >= 0 { // SELECT … INTO creates a table
			return write
		}
		return read
	case "WITH":
		if next == "XMLNAMESPACES" && len(toks) > 2 && isPunct(toks[2], "(") {
			rest := toks[skipParens(toks, 2):]
			switch {
			case len(rest) == 0:
				return write
			case isPunct(rest[0], ","):
				return classifyWith(append([]Token{toks[0]}, rest[1:]...), TSQL, r)
			}
			return classifyTokens(rest, TSQL, r)
		}
		return classifyWith(toks, TSQL, r)
	case "UPDATE":
		if next == "STATISTICS" {
			return write
		}
		return classifyVerb(toks, Generic, r)
	case "ALTER":
		a := classifyVerb(toks, Generic, r)
		if !a.Dangerous && topLevelIndex(toks, "SWITCH") >= 0 {
			a.Dangerous, a.Reason = true, "ALTER TABLE ... SWITCH moves the partition's rows out of the table"
		}
		return a
	case "INSERT", "DELETE", "MERGE", "TRUNCATE", "DROP", "CREATE", "GRANT", "REVOKE", "COMMIT", "ROLLBACK", "USE", "FETCH":
		return classifyVerb(toks, Generic, r)
	case "DENY", "ENABLE", "DISABLE":
		return Analysis{Class: DDL, Verb: verb}
	case "KILL":
		if hasWord(toks, "STATUSONLY") { // reports a rollback's progress
			return read
		}
		return write
	case "DECLARE", "PRINT", "THROW", "IF", "WHILE", "ELSE", "RETURN", "BREAK", "CONTINUE", "GOTO", "WAITFOR", "READTEXT", "DEALLOCATE":
		return read
	case "SET":
		if len(toks) > 1 && toks[1].Kind == Param { // a variable, read for the calls in its value
			return read
		}
		return Analysis{Class: Session, Verb: verb}
	case "SETUSER", "REVERT":
		return Analysis{Class: Session, Verb: verb}
	case "BEGIN":
		switch next {
		case "TRAN", "TRANSACTION", "DISTRIBUTED":
			return Analysis{Class: Transaction, Verb: verb}
		case "DIALOG", "CONVERSATION":
			return write
		}
		return read // a block
	case "END":
		if next == "CONVERSATION" {
			return write
		}
		return read
	case "SAVE":
		return Analysis{Class: Transaction, Verb: verb}
	case "OPEN", "CLOSE":
		if next == "SYMMETRIC" || next == "MASTER" || next == "ALL" { // keys the session opens
			return Analysis{Class: Session, Verb: verb}
		}
		return read // a cursor
	case "RAISERROR":
		if i := topLevelIndex(toks, "WITH"); i >= 0 && hasWord(toks[i:], "LOG") { // writes the error log
			return write
		}
		return read
	case "EXEC", "EXECUTE":
		return classifyTSQLCall(toks, 1, verb, r)
	case "DBCC":
		return classifyDBCC(toks)
	case "RESTORE":
		if next == "DATABASE" || next == "LOG" {
			write.Dangerous, write.Reason = true, "RESTORE replaces the database with the backup's contents"
		}
		return write
	case "SHUTDOWN":
		write.Dangerous, write.Reason = true, "SHUTDOWN stops the SQL Server instance"
		return write
	case "BACKUP", "RECONFIGURE", "CHECKPOINT", "BULK", "WRITETEXT", "UPDATETEXT", "DUMP", "LOAD", "RECEIVE", "SEND", "MOVE", "GET":
		return write
	case "GO":
		// Only "GO n" reaches here: SplitWith keeps it a statement.
		text := "GO"
		if len(toks) > 1 {
			text += " " + toks[1].Text
		}
		write.Dangerous = true
		write.Reason = text + " asks to run the batch before it again; DGopher runs each batch once and sends this line to the server as a statement"
		return write
	}
	if len(toks) > 1 && verb != "" && isOperator(toks[1], ":") { // a label
		return read
	}
	return classifyTSQLCall(toks, 0, "EXEC", r)
}

// topLevelIndex returns the index of the first w outside parentheses in
// toks, or -1.
func topLevelIndex(toks []Token, w string) int {
	dep := depths(toks)
	for i, t := range toks {
		if dep[i] <= 0 && word(t) == w {
			return i
		}
	}
	return -1
}

// tsqlLocks reports whether toks hold a table hint whose locks stay until
// the transaction ends.
func tsqlLocks(toks []Token) bool {
	dep := depths(toks)
	for i, t := range toks {
		if w := word(t); tsqlLockHints[w] || dep[i] > 0 && tsqlHeldLockLevels[w] {
			return true
		}
	}
	return false
}

// classifyTSQLCall classifies what EXECUTE runs, its operand from
// toks[i], or the procedure a batch's first statement names: a write,
// but for the system procedures known to read; the strings given to
// EXECUTE ( … ) and to the procedures that run SQL keep their danger.
// EXECUTE AS changes the session's user.
func classifyTSQLCall(toks []Token, i int, verb string, r reading) Analysis {
	write := Analysis{Class: Write, Verb: verb}
	if i >= len(toks) {
		return write
	}
	if word(toks[i]) == "AS" {
		return Analysis{Class: Session, Verb: verb}
	}
	if isPunct(toks[i], "(") {
		end := skipParens(toks, i)
		if inner, ok := tsqlCodeDanger(tsqlExecutedText(toks[i+1:max(end-1, i+1)]), r.deeper()); ok {
			write.Dangerous, write.Reason = true, heldReason(r, "the SQL it runs is dangerous: ", inner)
		}
		return write
	}
	if i+1 < len(toks) && toks[i].Kind == Param && toks[i+1].Text == "=" { // EXEC @status = name
		i += 2
	}
	var parts []string
	name := true // a name part may come next
	for ; i < len(toks); i++ {
		t := toks[i]
		if isPunct(t, ".") {
			if name {
				parts = append(parts, "") // master..sp_who
			}
			name = true
			continue
		}
		if !name || t.Kind != Keyword && t.Kind != Identifier && t.Kind != QuotedIdent {
			break
		}
		parts = append(parts, strings.ToLower(DecodeQuoted(t.Text, TSQL)))
		name = false
	}
	if len(parts) == 0 || name { // EXEC @variable, or no name
		return write
	}
	proc, args := parts[len(parts)-1], toks[i:]
	a := write
	if tsqlProcedureRead(proc, parts, args) {
		a.Class = Read
	}
	switch {
	case proc == "xp_cmdshell":
		a.Dangerous, a.Reason = true, "xp_cmdshell runs an operating-system command on the server"
	case tsqlCodeProcedures[proc]:
		dep := depths(args)
		for j, t := range args {
			if dep[j] != 0 || !tsqlString(t) {
				continue
			}
			if inner, ok := tsqlCodeDanger(DecodeQuoted(t.Text, TSQL), r.deeper()); ok {
				a.Dangerous, a.Reason = true, heldReason(r, "the SQL it runs is dangerous: ", inner)
				break
			}
		}
	}
	return a
}

// tsqlProcedureRead reports whether a call of proc, named by parts and
// given args, only reads: a procedure of tsqlProcedureReads, in the sys
// schema or unqualified, as a qualified name may reach a user's procedure
// of that name, within its tsqlProcedureArgLimits.
func tsqlProcedureRead(proc string, parts []string, args []Token) bool {
	if len(parts) > 1 && parts[len(parts)-2] != "sys" {
		return false
	}
	for _, rule := range functionRules {
		if rule.dialect != TSQL || rule.namePrefix == "" || !strings.HasPrefix(proc, rule.namePrefix) {
			continue
		}
		if rule.writesCall(proc, "", false) {
			return false
		}
		first, limited := tsqlProcedureArgLimits[proc]
		return !limited || tsqlArgsWithin(args, first)
	}
	return false
}

// tsqlArgsWithin reports whether args hold at most one argument, and
// that one, when named, is first.
func tsqlArgsWithin(args []Token, first string) bool {
	if len(args) == 0 {
		return true
	}
	dep := depths(args)
	for j, t := range args {
		if dep[j] == 0 && isPunct(t, ",") {
			return false
		}
	}
	named := len(args) > 1 && args[0].Kind == Param && args[1].Text == "="
	return !named || strings.EqualFold(args[0].Text, first)
}

// tsqlString reports whether t is a string: '…', N'…', or "…", a string
// under SET QUOTED_IDENTIFIER OFF.
func tsqlString(t Token) bool {
	return t.Kind == String || t.Kind == QuotedIdent && strings.HasPrefix(t.Text, `"`)
}

// tsqlExecutedText returns the SQL text EXECUTE ( … ) runs, from the
// tokens in its parentheses: the strings joined with +, any other operand
// standing as a name.
func tsqlExecutedText(toks []Token) string {
	dep := depths(toks)
	var b strings.Builder
	start := 0
	for i := 0; i <= len(toks); i++ {
		if i < len(toks) && !(dep[i] == 0 && isOperator(toks[i], "+")) {
			continue
		}
		if operand := toks[start:i]; len(operand) == 1 && tsqlString(operand[0]) {
			b.WriteString(DecodeQuoted(operand[0].Text, TSQL))
		} else {
			b.WriteString(" x ")
		}
		start = i + 1
	}
	return b.String()
}

// tsqlCodeDanger finds a dangerous statement in T-SQL code quoted as a
// string, each batch read as classifyTSQL reads it.
func tsqlCodeDanger(code string, r reading) (Analysis, bool) {
	if a, over := codeOverLimit(code, r); over {
		return a, true
	}
	for _, stmt := range statementsCode(splitSegments(code, TSQL, r.q, SplitOptions{Mode: SemicolonOnly})) {
		if a := classifyTSQL(stmt, r); a.Dangerous {
			return a, true
		}
	}
	return Analysis{}, false
}

// classifyDBCC classifies a DBCC command: a write but for dbccReads and
// CHECKIDENT … NORESEED; one that may delete data to repair, or writes
// pages directly, is dangerous.
func classifyDBCC(toks []Token) Analysis {
	cmd := ""
	if len(toks) > 1 {
		cmd = word(toks[1])
	}
	a := Analysis{Class: Write, Verb: "DBCC"}
	repair := false
	for _, t := range toks {
		repair = repair || strings.HasPrefix(word(t), "REPAIR_")
	}
	switch {
	case hasWord(toks, "REPAIR_ALLOW_DATA_LOSS"):
		a.Dangerous, a.Reason = true, "DBCC ... REPAIR_ALLOW_DATA_LOSS may delete data to repair the database"
	case cmd == "WRITEPAGE":
		a.Dangerous, a.Reason = true, "DBCC WRITEPAGE writes database pages directly"
	case repair, cmd == "SQLPERF" && hasWord(toks, "CLEAR"):
	case cmd == "CHECKIDENT":
		if hasWord(toks, "NORESEED") {
			a.Class = Read
		}
	case dbccReads[cmd]:
		a.Class = Read
	}
	return a
}
