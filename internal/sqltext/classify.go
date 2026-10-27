package sqltext

import (
	"fmt"
	"slices"
	"strings"
)

// Class is the effect category of a statement.
type Class int

const (
	Read Class = iota
	Write
	DDL
	Transaction
	Session
)

// Analysis is the safety classification of one statement.
type Analysis struct {
	Class     Class
	Verb      string
	Dangerous bool
	Reason    string
}

var verbClasses = map[string]Class{}

func init() {
	groups := map[Class]string{
		Read:        "SELECT SHOW DESCRIBE DESC EXPLAIN VALUES TABLE WITH SUMMARIZE PIVOT UNPIVOT HELP FETCH MOVE CLOSE",
		Write:       "INSERT UPDATE DELETE MERGE REPLACE UPSERT COPY LOAD CALL DO EXEC EXECUTE OPTIMIZE VACUUM ANALYZE ANALYSE REINDEX CLUSTER REFRESH LOCK IMPORT KILL SYSTEM",
		DDL:         "CREATE ALTER DROP TRUNCATE RENAME GRANT REVOKE COMMENT ATTACH DETACH EXCHANGE UNDROP",
		Transaction: "BEGIN START COMMIT END ROLLBACK ABORT SAVEPOINT RELEASE",
		Session:     "SET RESET USE LISTEN UNLISTEN DISCARD DEALLOCATE PREPARE",
	}
	for c, verbs := range groups {
		for _, v := range strings.Fields(verbs) {
			verbClasses[v] = c
		}
	}
}

// sideEffectFunctions lists, per dialect, functions whose call changes
// state, so a SELECT that calls one is a write. DuckDB uses Postgres.
var sideEffectFunctions = map[Dialect]map[string]bool{
	Postgres: setOf("pg_terminate_backend pg_cancel_backend setval nextval dblink_exec dblink lo_import lo_export lo_unlink lo_create lo_from_bytea lo_put lowrite pg_advisory_lock pg_advisory_xact_lock pg_try_advisory_lock pg_reload_conf pg_rotate_logfile pg_notify pg_switch_wal pg_create_restore_point pg_promote set_config pg_file_write pg_logical_emit_message " +
		"pg_advisory_lock_shared pg_advisory_xact_lock_shared pg_try_advisory_lock_shared pg_try_advisory_xact_lock pg_try_advisory_xact_lock_shared " +
		"pg_advisory_unlock pg_advisory_unlock_shared pg_advisory_unlock_all " +
		"pg_create_logical_replication_slot pg_create_physical_replication_slot pg_drop_replication_slot pg_copy_logical_replication_slot " +
		"pg_copy_physical_replication_slot pg_replication_slot_advance pg_logical_slot_get_changes pg_logical_slot_get_binary_changes " +
		"pg_replication_origin_create pg_replication_origin_drop pg_replication_origin_advance pg_replication_origin_session_setup " +
		"pg_replication_origin_session_reset pg_replication_origin_xact_setup pg_replication_origin_xact_reset " +
		"pg_stat_reset pg_stat_reset_shared pg_stat_reset_single_table_counters pg_stat_reset_single_function_counters pg_stat_reset_slru " +
		"pg_stat_reset_replication_slot pg_stat_reset_subscription_stats pg_stat_statements_reset " +
		"lo_truncate lo_truncate64 pg_wal_replay_pause pg_wal_replay_resume pg_backup_start pg_backup_stop pg_start_backup pg_stop_backup " +
		"pg_import_system_collations"),
	MySQL:  setOf("get_lock release_lock release_all_locks sleep benchmark nextval setval lastval"),
	SQLite: setOf("load_extension"),
}

// crdbInternalReads are the CockroachDB crdb_internal functions known
// only to read: any other, called by its qualified name, which alone
// reaches it, counts as a write, as one a later version adds.
var crdbInternalReads = setOf("node_id cluster_id cluster_name active_version node_executable_version release_series is_at_least_version " +
	"locality_value pretty_key pretty_span pretty_value table_span index_span range_stats range_stats_with_errors " +
	"lease_holder lease_holder_with_errors fingerprint show_create_all_routines show_create_all_schemas " +
	"show_create_all_tables show_create_all_triggers show_create_all_types")

// maxCodeDepth caps how many levels of code quoted inside code are read: a
// DO block that EXECUTEs a string is two levels down. Deeper code counts as
// dangerous, unread.
const maxCodeDepth = 8

// reading follows Classify into code quoted as strings: how many quotes
// deep the code at hand is, how many bytes are left to read across
// every level of one Classify call, and how its strings are quoted.
type reading struct {
	depth int
	left  *int
	q     quoting
}

// newReading starts reading a statement of n bytes. Legitimate code reads
// each quoted body once, from the one word that runs it: at most n bytes a
// level and a byte more a body, under 12 n in all over maxCodeDepth levels.
// Reading more than 16 n + 1024 bytes counts as dangerous, unread.
func newReading(n int, q quoting) reading {
	left := 16*n + 1024
	return reading{left: &left, q: q}
}

func (r reading) deeper() reading { return reading{depth: r.depth + 1, left: r.left, q: r.q} }

// readOnlyPragmas are the SQLite and DuckDB pragmas that only report.
var readOnlyPragmas = setOf("table_info table_xinfo index_list index_info index_xinfo foreign_key_list database_list compile_options function_list pragma_list collation_list module_list table_list show_tables show_tables_expanded database_size version storage_info")

func setOf(words string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(words) {
		out[w] = true
	}
	return out
}

// Classify reports the class, leading verb and danger of one statement.
// Text holding more than one statement, as the splitter sees it, is
// classified by its first and marked dangerous: if the splitter missed a
// boundary, the rest still reaches the server.
//
// As a backstop for a BEGIN or END the splitter took for a body's when it
// was a name, text whose routine body holds a ';' is also marked so when
// that body closes anywhere but at the end of its statement (after
// nothing but a MySQL label) or at an END that follows no ';'.
//
// A statement also keeps the danger of the code it holds: a PostgreSQL DO
// block's or routine's body quoted as a string, the strings EXECUTE runs
// there, a rule's action and the statement PREPARE prepares.
//
// A session or server setting can change where a string ends, so the text
// is read under each quoting rule its engine can switch to, and the
// strictest reading counts.
func Classify(stmt string, d Dialect) Analysis {
	return classifyEachQuoting(stmt, d, false)
}

// ClassifyExecutable is Classify of what MySQL runs: its executable
// comments unwrapped as each quoting rule reads where strings end, and
// those a server may skip, by its version or as MySQL skips MariaDB's,
// read skipped too. Other dialects have none.
func ClassifyExecutable(stmt string, d Dialect) Analysis {
	return classifyEachQuoting(stmt, d, d == MySQL)
}

// TokenizeExecutable is Tokenize of each reading ClassifyExecutable
// takes of stmt, the default first: under each quoting rule, MySQL's
// executable comments unwrapped, or skipped, as a server reads them.
func TokenizeExecutable(stmt string, d Dialect) [][]Token {
	var out [][]Token
	for _, q := range quotingVariants(d, stmt) {
		texts := []string{stmt}
		if d == MySQL {
			texts, _ = executableReadings(stmt, q)
		}
		for _, text := range texts {
			out = append(out, tokenizeQuoted(text, d, q))
		}
	}
	return out
}

// classifyEachQuoting classifies stmt under each of quotingVariants, each
// of its executableReadings when unwrap is set, and returns the
// strictest of each part: the highest class any reading gives, with its
// verb, and the danger any reading finds, with its reason, as the policy
// reads the class and the danger apart. The default reading wins a tie.
func classifyEachQuoting(stmt string, d Dialect, unwrap bool) Analysis {
	var strictest Analysis
	first, tooMany := true, false
	for _, q := range quotingVariants(d, stmt) {
		texts := []string{stmt}
		if unwrap {
			var many bool
			texts, many = executableReadings(stmt, q)
			tooMany = tooMany || many
		}
		for _, text := range texts {
			a := classifyQuoted(text, d, q)
			if first {
				strictest, first = a, false
				continue
			}
			if classRank(a.Class) > classRank(strictest.Class) {
				strictest.Class, strictest.Verb = a.Class, a.Verb
			}
			if a.Dangerous && !strictest.Dangerous {
				strictest.Dangerous, strictest.Reason = true, a.Reason
			}
		}
	}
	if tooMany && !strictest.Dangerous {
		strictest.Dangerous = true
		strictest.Reason = fmt.Sprintf("its executable comments name more than %d server versions or %d TiDB feature lists, which are not all read", maxExecutableVersions, maxExecutableFeatureLists)
	}
	return strictest
}

// quotingVariants lists the quoting rules d can be switched to that could
// read stmt differently, the default first: PostgreSQL's
// standard_conforming_strings off, MySQL's NO_BACKSLASH_ESCAPES and
// ANSI_QUOTES. Only a '\' or, for ANSI_QUOTES, a '"' can make them differ.
func quotingVariants(d Dialect, stmt string) []quoting {
	def := defaultQuoting(d)
	out := []quoting{def}
	backslash, double := strings.Contains(stmt, `\`), strings.Contains(stmt, `"`)
	switch d {
	case Postgres:
		if backslash {
			out = append(out, quoting{backslashes: true})
		}
	case MySQL:
		if backslash {
			out = append(out, quoting{backslashes: false})
		}
		if double {
			out = append(out, quoting{backslashes: true, ansiQuotes: true})
		}
		if backslash && double {
			out = append(out, quoting{backslashes: false, ansiQuotes: true})
		}
	}
	return out
}

// classRank orders classes by what they change, least first.
func classRank(c Class) int {
	switch c {
	case Read:
		return 0
	case Session:
		return 1
	case Transaction:
		return 2
	case Write:
		return 3
	}
	return 4 // DDL
}

// classifyQuoted is Classify under the one quoting rule q.
func classifyQuoted(stmt string, d Dialect, q quoting) Analysis {
	o := SplitOptions{Mode: SemicolonOnly}
	segs := splitSegments(stmt, d, q, o)
	first, count := statementCode(segs)
	if count == 0 {
		if strings.TrimSpace(stmt) == "" {
			return Analysis{Class: Read}
		}
		return Analysis{Class: Write, Dangerous: false}
	}
	r := newReading(len(stmt), q)
	a := classifyTokens(first, d, r)
	if count == 1 {
		for _, seg := range segs {
			if bodyHidesStatement(seg, d) {
				if _, n := statementCode(splitSegmentsWithoutBodies(stmt, d, q, o)); n > count {
					count = n
				}
			}
		}
	}
	if count > 1 {
		if a.Class != DDL {
			a.Class = Write
		}
		a.Dangerous = true
		a.Reason = "the text holds more than one statement"
		return a
	}
	// Read as statements, a routine's text keeps the danger of what it
	// holds: the body may be a name the splitter took for one.
	if !a.Dangerous {
		if stmts := statementsCode(splitSegmentsWithoutBodies(stmt, d, q, o)); len(stmts) > 1 {
			if inner, ok := dangerInside(stmts, d, r); ok {
				a.Dangerous = true
				a.Reason = routineHolds + inner.Reason
			}
		}
	}
	return a
}

const routineHolds = "the routine's text holds a statement that is dangerous on its own: "

// dangerInside finds a dangerous statement among stmts but the first: each
// one, and within each, what follows a word that opens a block. A MERGE's
// THEN and an ON CONFLICT's DO open its own actions, not statements.
func dangerInside(stmts [][]Token, d Dialect, r reading) (Analysis, bool) {
	for i, code := range stmts {
		merge, conflict := false, false
		for j := range code {
			starts := j == 0
			if j > 0 {
				switch word(code[j-1]) {
				case "BEGIN", "ATOMIC", "ELSE", "LOOP":
					starts = true
				case "THEN":
					starts = !merge
				case "DO":
					starts = !conflict
				}
			}
			switch w := word(code[j]); {
			case w == "CONFLICT" && j > 0 && word(code[j-1]) == "ON":
				conflict = true
			case w == "MERGE" && (starts || j > 0 && isPunct(code[j-1], ")")): // ")" ends a WITH
				merge = true
			}
			if !starts || i == 0 && j == 0 {
				continue
			}
			if inner := classifyTokens(code[j:], d, r); inner.Dangerous {
				return inner, true
			}
		}
	}
	return Analysis{}, false
}

// codeDanger finds a dangerous statement in code quoted as a string, as a
// DO block's body: each statement, what follows a word that opens a block,
// and the strings EXECUTE runs.
func codeDanger(code string, d Dialect, r reading) (Analysis, bool) {
	if r.depth > maxCodeDepth {
		return Analysis{Class: Write, Dangerous: true, Reason: fmt.Sprintf("it quotes code more than %d levels deep, too deep to check", maxCodeDepth)}, true
	}
	if *r.left -= len(code) + 1; *r.left < 0 {
		return Analysis{Class: Write, Dangerous: true, Reason: "it holds too much code to check"}, true
	}
	stmts := statementsCode(splitSegmentsWithoutBodies(code, d, r.q, SplitOptions{Mode: SemicolonOnly}))
	if len(stmts) == 0 {
		return Analysis{}, false
	}
	if a := classifyTokens(stmts[0], d, r); a.Dangerous {
		return a, true
	}
	if a, ok := dangerInside(stmts, d, r); ok {
		return a, true
	}
	for _, toks := range stmts {
		for j, t := range toks {
			if word(t) != "EXECUTE" || j+1 == len(toks) || toks[j+1].Kind != String {
				continue
			}
			if hasWord(toks, "UESCAPE") {
				return Analysis{Class: Write, Dangerous: true, Reason: uescapeReason}, true
			}
			if a, ok := codeDanger(executedText(toks[j+1:], d, r.q), d, r.deeper()); ok {
				return a, true
			}
		}
	}
	return Analysis{}, false
}

const uescapeReason = "it is written with a UESCAPE, whose escapes are not decoded"

func hasWord(toks []Token, w string) bool {
	for _, t := range toks {
		if word(t) == w {
			return true
		}
	}
	return false
}

// executedText returns the text a PL/pgSQL EXECUTE runs, from toks, which
// start with a string, up to its INTO, USING or LOOP: the strings joined
// with ||, any other operand standing as a name.
func executedText(toks []Token, d Dialect, q quoting) string {
	dep := depths(toks)
	var b strings.Builder
	start := 0
	for i := 0; i <= len(toks); i++ {
		end := i == len(toks)
		if !end && dep[i] == 0 {
			switch word(toks[i]) {
			case "INTO", "USING", "LOOP":
				end = true
			}
		}
		if !end && !(dep[i] == 0 && isOperator(toks[i], "||")) {
			continue
		}
		if operand := toks[start:i]; len(operand) == 1 && operand[0].Kind == String {
			b.WriteString(codeText(operand[0], d, q))
		} else {
			b.WriteString(" x ")
		}
		if end {
			break
		}
		start = i + 1
	}
	return b.String()
}

// doDanger finds a dangerous statement in a PostgreSQL DO block's code,
// toks after DO: "DO [LANGUAGE lang] code [LANGUAGE lang]". Code in a
// language other than SQL or PL/pgSQL is not read.
func doDanger(toks []Token, d Dialect, r reading) (Analysis, bool) {
	i := 0
	if i+1 < len(toks) && word(toks[i]) == "LANGUAGE" {
		if !sqlLanguage(toks[i+1], d, r.q) {
			return Analysis{}, false
		}
		i += 2
	}
	if i >= len(toks) || toks[i].Kind != String {
		return Analysis{}, false
	}
	if i+2 < len(toks) && word(toks[i+1]) == "LANGUAGE" && !sqlLanguage(toks[i+2], d, r.q) {
		return Analysis{}, false
	}
	return bodyDanger(toks, i, d, r)
}

// routineDanger finds a dangerous statement in the body a PostgreSQL
// CREATE FUNCTION or PROCEDURE quotes as a string after AS, among the
// routine's own clauses: up to a word that opens a block, or another
// CREATE. Code in a language other than SQL or PL/pgSQL is not read.
func routineDanger(toks []Token, d Dialect, r reading) (Analysis, bool) {
	dep := depths(toks)
	body := -1
	for i := 1; i < len(toks); i++ {
		t := toks[i]
		if dep[i] != 0 {
			continue // an argument's default, a returned table's columns
		}
		if ends(toks, i, "BEGIN", "ATOMIC", "THEN", "ELSE", "LOOP", "DO", "CREATE") {
			break
		}
		switch {
		case word(t) == "LANGUAGE" && i+1 < len(toks) && !sqlLanguage(toks[i+1], d, r.q):
			return Analysis{}, false
		case body < 0 && t.Kind == String && word(toks[i-1]) == "AS":
			body = i
		}
	}
	if body < 0 {
		return Analysis{}, false
	}
	return bodyDanger(toks, body, d, r)
}

// ends reports whether toks[i] is one of words, and no name after a '.',
// as NEW.create is.
func ends(toks []Token, i int, words ...string) bool {
	return (i == 0 || !isPunct(toks[i-1], ".")) && slices.Contains(words, word(toks[i]))
}

// bodyDanger reads the code the string toks[i] quotes, which a UESCAPE
// after it makes unknown.
//
// Code quoted as a string is read from the one word that runs it, where
// the grammar puts it, never from a word further back: each word that
// could start a statement is classified, and code read from each would be
// read again at every level.
func bodyDanger(toks []Token, i int, d Dialect, r reading) (Analysis, bool) {
	if i+1 < len(toks) && word(toks[i+1]) == "UESCAPE" {
		return Analysis{Class: Write, Dangerous: true, Reason: uescapeReason}, true
	}
	return codeDanger(codeText(toks[i], d, r.q), d, r.deeper())
}

// sqlLanguage reports whether a LANGUAGE's name is SQL or PL/pgSQL, whose
// code reads as SQL.
func sqlLanguage(t Token, d Dialect, q quoting) bool {
	name := t.Text
	switch t.Kind {
	case String:
		name = codeText(t, d, q)
	case QuotedIdent:
		name = DecodeQuoted(t.Text, d)
	}
	name = strings.ToLower(name)
	return name == "sql" || name == "plpgsql"
}

// codeText returns the text a string token quotes, to be read as code: a
// dollar quote without its tags, a string whose backslashes q reads as
// escapes with them decoded, others as DecodeQuoted decodes them.
func codeText(t Token, d Dialect, q quoting) string {
	s := t.Text
	switch {
	case strings.HasPrefix(s, "$"):
		end := strings.IndexByte(s[1:], '$')
		if end < 0 {
			return s
		}
		tag := s[:end+2]
		return strings.TrimSuffix(s[len(tag):], tag)
	case d == MySQL && (strings.HasPrefix(s, "'") || strings.HasPrefix(s, `"`)):
		if !q.backslashes {
			return unquoteText(s)
		}
		return decodeBackslashQuoted(s, mysqlEscape)
	case d == Postgres && q.backslashes && strings.HasPrefix(s, "'"):
		return decodeBackslashQuoted(s, postgresEscape)
	}
	return DecodeQuoted(s, d)
}

// mysqlEscape decodes a backslash escape in a MySQL string: \% and \_ keep
// their backslash, for LIKE.
func mysqlEscape(b *strings.Builder, s string, i int) int {
	switch c := s[i]; c {
	case '%', '_':
		b.WriteByte('\\')
		b.WriteByte(c)
	case 'Z':
		b.WriteByte(0x1a)
	default:
		b.WriteByte(controlEscape(c, "0bnrt"))
	}
	return i + 1
}

// heldReason is the reason of a statement whose code holds a dangerous
// one: what holds it, said on the outermost statement only, and why the
// inner one is dangerous.
func heldReason(r reading, holder string, inner Analysis) string {
	if r.depth > 0 {
		return inner.Reason
	}
	return holder + inner.Reason
}

// statementsCode returns the significant tokens of each statement in segs.
func statementsCode(segs []segment) [][]Token {
	var out [][]Token
	for _, seg := range segs {
		if seg.directive {
			continue
		}
		toks := seg.toks
		if seg.delimited {
			toks = toks[:len(toks)-1]
		}
		var code []Token
		for _, t := range toks {
			if significant(t) {
				code = append(code, t)
			}
		}
		if len(code) > 0 {
			out = append(out, code)
		}
	}
	return out
}

// statementCode returns the significant tokens of the first statement in
// segs and the number of statements.
func statementCode(segs []segment) (first []Token, count int) {
	stmts := statementsCode(segs)
	if len(stmts) == 0 {
		return nil, 0
	}
	return stmts[0], len(stmts)
}

// bodyHidesStatement reports whether the first routine body in seg closes
// where no routine body can: at an END that follows no ';', or with more
// than a MySQL label after it before the statement ends.
func bodyHidesStatement(seg segment, d Dialect) bool {
	if seg.bodyEnd < 0 {
		return false
	}
	if !bodyCloseFollowsStatement(seg.toks, seg.bodyEnd) {
		return true
	}
	var after []Token
	for _, t := range seg.toks[seg.bodyEnd+1:] {
		if significant(t) {
			after = append(after, t)
		}
	}
	if len(after) == 0 {
		return false
	}
	label := d == MySQL && len(after) == 1 && (word(after[0]) != "" || after[0].Kind == QuotedIdent)
	return !label
}

// depths returns each token's paren depth relative to the first token.
func depths(toks []Token) []int {
	out := make([]int, len(toks))
	depth := 0
	for i, t := range toks {
		if t.Kind == Punct && t.Text == ")" {
			depth--
		}
		out[i] = depth
		if t.Kind == Punct && t.Text == "(" {
			depth++
		}
	}
	return out
}

func isPunct(t Token, s string) bool { return t.Kind == Punct && t.Text == s }

func classifyTokens(toks []Token, d Dialect, r reading) Analysis {
	for len(toks) > 0 && isPunct(toks[0], "(") {
		toks = toks[1:]
	}
	if len(toks) == 0 {
		return Analysis{Class: Write, Reason: ""}
	}
	a := classifyVerb(toks, d, r)
	switch word(toks[0]) {
	case "EXPLAIN", "DESC", "DESCRIBE":
		// Plain EXPLAIN runs nothing; EXPLAIN ANALYZE classified its
		// statement, calls included.
		return a
	}
	if d == Postgres && word(toks[0]) != "WITH" { // WITH classified its parts
		a = withBracketStatements(a, toks, d, r)
	}
	if a.Class == Read {
		if fn := sideEffectCall(toks, d); fn != "" {
			a.Class = Write
			a.Reason = "calls " + fn + ", which has side effects"
		}
	}
	return a
}

// withBracketStatements returns a raised to the strictest class and danger
// of the statements CockroachDB's "[ <statement> ]" data sources in toks
// run: a '[' before a statement's verb, unless it follows what an array
// subscript follows, a name, a value or a closing bracket, or the word is
// a value in a list.
func withBracketStatements(a Analysis, toks []Token, d Dialect, r reading) Analysis {
	for i := 0; i+1 < len(toks); i++ {
		if !isPunct(toks[i], "[") {
			continue
		}
		if _, verb := verbClasses[word(toks[i+1])]; !verb || i > 0 && subscripted(toks[i-1]) || i+2 < len(toks) && listed(toks[i+2]) {
			continue
		}
		end, depth := i, 0
		for ; end < len(toks); end++ {
			if isPunct(toks[end], "[") {
				depth++
			} else if isPunct(toks[end], "]") {
				if depth--; depth == 0 {
					break
				}
			}
		}
		if *r.left -= toks[min(end, len(toks)-1)].End - toks[i].Start; *r.left < 0 {
			return Analysis{Class: Write, Verb: a.Verb, Dangerous: true, Reason: "it holds too much code to check"}
		}
		inner := classifyTokens(toks[i+1:end], d, r)
		if classRank(inner.Class) > classRank(a.Class) {
			a.Class = inner.Class
		}
		if inner.Dangerous && !a.Dangerous {
			a.Dangerous, a.Reason = true, "in a [ ] data source, "+inner.Reason
		}
		i = end
	}
	return a
}

// listed reports whether t, after the first word in brackets, makes the
// word a value, as in DuckDB's list [start, stop], not a statement's verb.
func listed(t Token) bool {
	return t.Kind == Punct && strings.Contains(",]:.[)", t.Text) || t.Kind == Operator && t.Text != "*" && t.Text != "-"
}

// subscripted reports whether a '[' after t opens an array subscript.
func subscripted(t Token) bool {
	switch t.Kind {
	case Identifier, QuotedIdent, String, Number, Param:
		return true
	}
	return isPunct(t, ")") || isPunct(t, "]") || word(t) == "ARRAY"
}

// analyzedVerbs are the statements MariaDB's ANALYZE runs and reports on.
var analyzedVerbs = setOf("SELECT WITH INSERT REPLACE UPDATE DELETE")

// sideEffectCall returns the first function in toks, called anywhere, that
// changes state on dialect d, or MySQL's NEXT VALUE FOR, or "". Quoted
// names are decoded first; with a UESCAPE anywhere, whose escapes are not
// decoded, any call to a U& name counts.
func sideEffectCall(toks []Token, d Dialect) string {
	fns := sideEffectFunctions[d]
	uescape := false
	for _, t := range toks {
		if word(t) == "UESCAPE" {
			uescape = true
		}
	}
	for i := 0; i+1 < len(toks); i++ {
		t := toks[i]
		if d == MySQL && i+2 < len(toks) && word(t) == "NEXT" && word(toks[i+1]) == "VALUE" && word(toks[i+2]) == "FOR" {
			return "NEXT VALUE FOR"
		}
		open := i + 1
		if word(toks[open]) == "UESCAPE" {
			open += 2
		}
		if open >= len(toks) || !isPunct(toks[open], "(") {
			continue
		}
		name := ""
		switch t.Kind {
		case Keyword, Identifier:
			name = t.Text
		case QuotedIdent:
			if uescape && len(t.Text) > 1 && (t.Text[0] == 'U' || t.Text[0] == 'u') && t.Text[1] == '&' {
				return t.Text
			}
			name = DecodeQuoted(t.Text, d)
		}
		if name = strings.ToLower(name); fns[name] {
			return name
		}
		if d == Postgres && !crdbInternalReads[name] && crdbInternalQualifies(toks, i, uescape) {
			return "crdb_internal." + name
		}
	}
	return ""
}

// crdbInternalQualifies reports whether the name toks[i] is qualified by
// crdb_internal, or, with a UESCAPE, by a U& name, which is not decoded.
func crdbInternalQualifies(toks []Token, i int, uescape bool) bool {
	if i < 2 || !isPunct(toks[i-1], ".") {
		return false
	}
	q := i - 2
	if q >= 2 && word(toks[q-1]) == "UESCAPE" {
		q -= 2
	}
	if t := toks[q].Text; uescape && toks[q].Kind == QuotedIdent && len(t) > 1 && (t[0] == 'U' || t[0] == 'u') && t[1] == '&' {
		return true
	}
	return strings.EqualFold(DecodeQuoted(toks[q].Text, Postgres), "crdb_internal")
}

func classifyVerb(toks []Token, d Dialect, r reading) Analysis {
	verb := word(toks[0])
	if verb == "" {
		return Analysis{Class: Write, Verb: strings.ToUpper(toks[0].Text)}
	}
	next := ""
	if len(toks) > 1 {
		next = word(toks[1])
	}
	dep := depths(toks)
	topLevel := func(w string) int {
		for i, t := range toks {
			if dep[i] <= 0 && word(t) == w {
				return i
			}
		}
		return -1
	}
	switch verb {
	case "WITH":
		return classifyWith(toks, d, r)
	case "EXPLAIN", "DESC", "DESCRIBE":
		return classifyExplain(toks, d, r)
	case "ANALYZE":
		// MariaDB's ANALYZE [FORMAT=JSON] <statement> runs the statement.
		if d == MySQL {
			i := 1
			if i+2 < len(toks) && word(toks[i]) == "FORMAT" && toks[i+1].Text == "=" {
				i += 3
			}
			if i < len(toks) && (analyzedVerbs[word(toks[i])] || isPunct(toks[i], "(")) {
				return classifyTokens(toks[i:], d, r)
			}
		}
	case "EXISTS", "CHECK", "CHECKSUM":
		if d == ClickHouse || next == "TABLE" {
			return Analysis{Class: Read, Verb: verb}
		}
	case "MOVE":
		// PostgreSQL's MOVE moves a cursor; ClickHouse's moves access
		// entities between storages.
		if d != Postgres {
			return Analysis{Class: Write, Verb: verb}
		}
	case "SELECT", "TABLE", "VALUES":
		if locks(toks) {
			// Its rows stay locked while the result is open, as for a write.
			return Analysis{Class: Write, Verb: verb}
		}
		if i := topLevel("INTO"); i >= 0 {
			// MySQL "INTO @var" only sets a user variable.
			if i+1 < len(toks) && toks[i+1].Kind == Param {
				return Analysis{Class: Read, Verb: verb}
			}
			return Analysis{Class: Write, Verb: verb}
		}
		return Analysis{Class: Read, Verb: verb}
	case "FROM":
		// DuckDB's FROM-first SELECT; anything that stores rows is a write.
		for _, w := range []string{"INTO", "INSERT", "UPDATE", "DELETE"} {
			if topLevel(w) >= 0 {
				return Analysis{Class: Write, Verb: verb}
			}
		}
		return Analysis{Class: Read, Verb: verb}
	case "RESET":
		// RESET MASTER, REPLICA, PERSIST and the like change server state,
		// as CockroachDB's RESET CLUSTER SETTING does.
		if d == MySQL || next == "CLUSTER" || d == Postgres && opensUnsafeInternals(toks) {
			return Analysis{Class: Write, Verb: verb}
		}
	case "SET":
		if setsServerState(toks, dep) || d == Postgres && (setsFile(toks) || searchesCrdbInternal(toks, r.q) || opensUnsafeInternals(toks)) {
			return Analysis{Class: Write, Verb: verb}
		}
	case "DO":
		// MySQL's DO only evaluates expressions.
		if d == Postgres {
			if inner, ok := doDanger(toks[1:], d, r); ok {
				return Analysis{Class: Write, Verb: verb, Dangerous: true, Reason: heldReason(r, "the DO block's code is dangerous: ", inner)}
			}
		}
	case "START":
		if next == "TRANSACTION" {
			return Analysis{Class: Transaction, Verb: verb}
		}
		return Analysis{Class: Write, Verb: verb}
	case "COMMIT", "ROLLBACK":
		if next == "PREPARED" {
			return Analysis{Class: Write, Verb: verb}
		}
	case "DECLARE":
		if d == Postgres {
			if a, ok := classifyCursor(toks, dep, d, r); ok {
				return a
			}
		}
	case "PRAGMA":
		if readOnlyPragma(toks) {
			return Analysis{Class: Read, Verb: verb}
		}
		return Analysis{Class: Write, Verb: verb}
	case "PREPARE":
		if len(toks) > 1 && word(toks[1]) == "TRANSACTION" {
			return Analysis{Class: Transaction, Verb: verb}
		}
		a := Analysis{Class: Session, Verb: verb}
		if inner, ok := preparedDanger(toks, d, r); ok {
			a.Dangerous, a.Reason = true, heldReason(r, "the statement it prepares is dangerous: ", inner)
		}
		return a
	case "UPDATE", "DELETE":
		a := Analysis{Class: Write, Verb: verb}
		where := topLevel("WHERE")
		if where < 0 {
			a.Dangerous = true
			a.Reason = verb + " without WHERE affects every row"
			return a
		}
		end := len(toks)
		for _, w := range whereEnds {
			if i := topLevel(w); i > where {
				end = min(end, i)
			}
		}
		if alwaysTrue(toks[where+1 : end]) {
			a.Dangerous = true
			a.Reason = verb + " whose WHERE is always true affects every row"
		}
		return a
	case "CREATE":
		if next == "OR" && len(toks) > 3 && word(toks[2]) == "REPLACE" && replacesTable(toks[3:]) {
			return Analysis{Class: DDL, Verb: verb, Dangerous: true, Reason: "CREATE OR REPLACE TABLE replaces the table and its rows"}
		}
		if d == Postgres {
			if action, ok := ruleAction(toks); ok {
				if inner := classifyTokens(action, d, r); inner.Dangerous {
					return Analysis{Class: DDL, Verb: verb, Dangerous: true, Reason: heldReason(r, "the rule's action is dangerous: ", inner)}
				}
			} else if routineHeader(toks) {
				if inner, ok := routineDanger(toks, d, r); ok {
					return Analysis{Class: DDL, Verb: verb, Dangerous: true, Reason: heldReason(r, routineHolds, inner)}
				}
			}
		}
	case "REPLACE":
		if next == "TABLE" { // ClickHouse
			return Analysis{Class: DDL, Verb: verb, Dangerous: true, Reason: "REPLACE TABLE replaces the table and its rows"}
		}
	case "DROP":
		obj := "object"
		if len(toks) > 1 && word(toks[1]) != "" {
			obj = strings.ToLower(word(toks[1]))
		}
		return Analysis{Class: DDL, Verb: verb, Dangerous: true, Reason: "DROP permanently removes the " + obj + " and its data"}
	case "TRUNCATE":
		return Analysis{Class: DDL, Verb: verb, Dangerous: true, Reason: "TRUNCATE deletes every row"}
	case "ALTER":
		a := Analysis{Class: DDL, Verb: verb}
		if topLevel("DROP") >= 0 {
			a.Dangerous = true
			a.Reason = "ALTER ... DROP removes a column, constraint or other part of the object and may lose data"
		}
		if i := topLevel("TRUNCATE"); i >= 0 && i+1 < len(toks) && word(toks[i+1]) == "PARTITION" {
			a.Dangerous = true
			a.Reason = "ALTER ... TRUNCATE PARTITION deletes the partition's rows"
		}
		if d == ClickHouse {
			for _, m := range []string{"DELETE", "UPDATE"} {
				if topLevel(m) >= 0 {
					a.Dangerous = true
					a.Reason = "ALTER TABLE ... " + m + " is a ClickHouse mutation that rewrites table data and cannot be rolled back"
				}
			}
			if why := clickHouseLoses(toks, dep); why != "" {
				a.Dangerous = true
				a.Reason = why
			}
		}
		return a
	}
	if c, ok := verbClasses[verb]; ok {
		return Analysis{Class: c, Verb: verb}
	}
	return Analysis{Class: Write, Verb: verb}
}

// locks reports whether a SELECT locks the rows it reads, in a subquery
// too: FOR UPDATE, FOR NO KEY UPDATE, FOR SHARE, FOR KEY SHARE, or
// MySQL's LOCK IN SHARE MODE.
func locks(toks []Token) bool {
	for i := 0; i+1 < len(toks); i++ {
		switch w, next := word(toks[i]), word(toks[i+1]); {
		case w == "FOR" && (next == "UPDATE" || next == "SHARE" || next == "NO" || next == "KEY"):
			return true
		case w == "LOCK" && next == "IN":
			return true
		}
	}
	return false
}

// clickHouseLoses says how a ClickHouse ALTER loses rows or values, ""
// when it does not: clearing a column, a TTL that expires rows, or a
// partition replaced, moved away or detached.
func clickHouseLoses(toks []Token, dep []int) string {
	for i := 1; i+1 < len(toks); i++ {
		if dep[i] > 0 {
			continue
		}
		switch w, next := word(toks[i]), word(toks[i+1]); {
		case w == "CLEAR" && next == "COLUMN":
			return "ALTER TABLE ... CLEAR COLUMN deletes the column's values"
		case w == "MODIFY" && next == "TTL":
			return "ALTER TABLE ... MODIFY TTL deletes the rows it expires"
		case w == "MATERIALIZE" && next == "TTL":
			return "ALTER TABLE ... MATERIALIZE TTL deletes the rows it expires"
		case (w == "REPLACE" || w == "DETACH") && (next == "PARTITION" || next == "PART"),
			w == "MOVE" && (next == "PARTITION" || next == "PART") && toTable(toks[i+2:]):
			return "ALTER TABLE ... " + w + " " + next + " takes the partition's rows out of the table"
		}
	}
	return ""
}

// replacesTable reports whether what follows CREATE OR REPLACE is a
// table, temporary or not.
func replacesTable(toks []Token) bool {
	if w := word(toks[0]); (w == "TEMP" || w == "TEMPORARY") && len(toks) > 1 {
		toks = toks[1:]
	}
	return word(toks[0]) == "TABLE"
}

// toTable reports whether TO TABLE follows: a partition moved to another
// table, rather than to a disk or a volume.
func toTable(toks []Token) bool {
	for i := 0; i+1 < len(toks); i++ {
		if word(toks[i]) == "TO" {
			return word(toks[i+1]) == "TABLE"
		}
	}
	return false
}

// setsServerState reports whether a SET changes more than the session: MySQL
// GLOBAL, PERSIST and PERSIST_ONLY variables, SET PASSWORD and SET DEFAULT
// ROLE, which changes an account, TiDB's SET CONFIG, which configures a
// cluster component, and SET BINDING, which switches a global plan binding,
// and CockroachDB's SET CLUSTER SETTING.
func setsServerState(toks []Token, dep []int) bool {
	if len(toks) > 1 {
		switch word(toks[1]) {
		case "PASSWORD", "CONFIG", "BINDING", "CLUSTER":
			return true
		}
	}
	if len(toks) > 2 && word(toks[1]) == "DEFAULT" && word(toks[2]) == "ROLE" {
		return true
	}
	for i := 1; i < len(toks); i++ {
		t := toks[i]
		if t.Kind == Param {
			low := strings.ToLower(t.Text)
			for _, p := range []string{"@@global.", "@@persist.", "@@persist_only."} {
				if strings.HasPrefix(low, p) {
					return true
				}
			}
		}
		if dep[i] > 0 || !(i == 1 || isPunct(toks[i-1], ",")) {
			continue
		}
		switch word(t) {
		case "GLOBAL", "PERSIST", "PERSIST_ONLY":
			return true
		}
	}
	return false
}

// fileSettings are the DuckDB settings whose value names a file or a
// directory DuckDB then writes to.
var fileSettings = setOf("log_query_path profiling_output profile_output http_logging_output temp_directory extension_directory extension_directories secret_directory home_directory")

// setsFile reports whether a SET, after any SESSION, LOCAL or GLOBAL,
// changes one of fileSettings. DuckDB reads as Postgres.
func setsFile(toks []Token) bool {
	i := 1
	if i < len(toks) {
		switch word(toks[i]) {
		case "SESSION", "LOCAL", "GLOBAL":
			i++
		}
	}
	if i >= len(toks) {
		return false
	}
	name := toks[i].Text
	if toks[i].Kind == QuotedIdent {
		name = DecodeQuoted(name, Postgres)
	}
	return fileSettings[strings.ToLower(name)]
}

// searchesCrdbInternal reports whether a SET, after any SESSION or LOCAL,
// puts CockroachDB's crdb_internal on the search path, where its functions
// are then called by their names alone: search_path or schema set to a
// value that names it, as quoting q reads it. With a UESCAPE, whose escapes
// are not decoded, any such SET does.
func searchesCrdbInternal(toks []Token, q quoting) bool {
	i := settingAt(toks)
	if i < 0 {
		return false
	}
	uescape := hasWord(toks, "UESCAPE")
	if name := strings.ToLower(DecodeQuoted(toks[i].Text, Postgres)); name != "search_path" && name != "schema" && !uescape {
		return false
	}
	for _, t := range toks[i+1:] {
		if uescape || strings.Contains(strings.ToLower(codeText(t, Postgres, q)), "crdb_internal") {
			return true
		}
	}
	return false
}

// opensUnsafeInternals reports whether a SET or RESET changes CockroachDB's
// allow_unsafe_internals, which opens crdb_internal and system to the
// session, or, with a UESCAPE, whose escapes are not decoded, any setting.
func opensUnsafeInternals(toks []Token) bool {
	i := settingAt(toks)
	return i >= 0 && (hasWord(toks, "UESCAPE") || strings.EqualFold(DecodeQuoted(toks[i].Text, Postgres), "allow_unsafe_internals"))
}

// settingAt returns the index of the setting a SET or RESET names, after
// any SESSION or LOCAL, or -1.
func settingAt(toks []Token) int {
	i := 1
	if i < len(toks) && (word(toks[i]) == "SESSION" || word(toks[i]) == "LOCAL") {
		i++
	}
	if i >= len(toks) {
		return -1
	}
	return i
}

// preparedDanger finds the danger of the statement a PREPARE prepares:
// "PREPARE name [(types)] AS statement", or MySQL's "PREPARE name FROM
// 'statement'". A statement in a variable is not known.
func preparedDanger(toks []Token, d Dialect, r reading) (Analysis, bool) {
	i := 2
	if i < len(toks) && isPunct(toks[i], "(") {
		i = skipParens(toks, i)
	}
	if i+1 >= len(toks) {
		return Analysis{}, false
	}
	switch word(toks[i]) {
	case "AS":
		if a := classifyTokens(toks[i+1:], d, r); a.Dangerous {
			return a, true
		}
	case "FROM":
		if d == MySQL && toks[i+1].Kind == String {
			return bodyDanger(toks, i+1, d, r)
		}
	}
	return Analysis{}, false
}

// ruleAction returns the action of "CREATE [OR REPLACE] RULE … DO [ALSO |
// INSTEAD] action", without the parentheses around it, from the DO before
// any other CREATE. ok is false when toks create no rule, or one without
// an action.
func ruleAction(toks []Token) (action []Token, ok bool) {
	i := 1
	if i+1 < len(toks) && word(toks[i]) == "OR" && word(toks[i+1]) == "REPLACE" {
		i += 2
	}
	if i >= len(toks) || word(toks[i]) != "RULE" {
		return nil, false
	}
	dep := depths(toks)
	for i < len(toks) && !(dep[i] == 0 && word(toks[i]) == "DO") {
		if dep[i] == 0 && ends(toks, i, "CREATE") {
			return nil, false
		}
		i++
	}
	i++
	if i < len(toks) && (word(toks[i]) == "ALSO" || word(toks[i]) == "INSTEAD") {
		i++
	}
	action = toks[min(i, len(toks)):]
	if len(action) > 1 && isPunct(action[0], "(") && skipParens(action, 0) == len(action) {
		action = action[1 : len(action)-1]
	}
	return action, len(action) > 0
}

// classifyCursor classifies Postgres "DECLARE name … CURSOR … FOR query" by
// its query.
func classifyCursor(toks []Token, dep []int, d Dialect, r reading) (Analysis, bool) {
	cursor := -1
	for i := 2; i < len(toks); i++ {
		if dep[i] > 0 {
			return Analysis{}, false
		}
		w := word(toks[i])
		if w == "" {
			return Analysis{}, false
		}
		if cursor < 0 && w == "CURSOR" {
			cursor = i
		} else if cursor >= 0 && w == "FOR" {
			if i+1 >= len(toks) {
				return Analysis{}, false
			}
			a := classifyTokens(toks[i+1:], d, r)
			a.Verb = "DECLARE"
			return a, true
		}
	}
	return Analysis{}, false
}

// readOnlyPragma reports whether toks are "PRAGMA [schema.]name" or
// "PRAGMA [schema.]name(arg)" with name a pragma that only reports.
func readOnlyPragma(toks []Token) bool {
	i := 1
	if i+1 < len(toks) && isPunct(toks[i+1], ".") {
		i += 2
	}
	if i >= len(toks) || !readOnlyPragmas[strings.ToLower(word(toks[i]))] {
		return false
	}
	for _, t := range toks[i+1:] {
		if t.Text == "=" {
			return false
		}
	}
	i++
	if i < len(toks) && isPunct(toks[i], "(") {
		i = skipParens(toks, i)
	}
	return i == len(toks)
}

func classifyWith(toks []Token, d Dialect, r reading) Analysis {
	i := 1
	if i < len(toks) && word(toks[i]) == "RECURSIVE" {
		i++
	}
	cteWrites := false
	var danger Analysis // the first dangerous CTE body's
	for i < len(toks) {
		// Skip "name [(cols)] AS [NOT] [MATERIALIZED]" up to the CTE body.
		for i < len(toks) && !(isPunct(toks[i], "(") && i > 0 && (word(toks[i-1]) == "AS" || word(toks[i-1]) == "MATERIALIZED")) {
			if isPunct(toks[i], "(") {
				i = skipParens(toks, i)
				continue
			}
			i++
		}
		if i >= len(toks) {
			break
		}
		end := skipParens(toks, i)
		for _, t := range toks[i:end] {
			switch word(t) {
			case "INSERT", "UPDATE", "DELETE", "MERGE":
				cteWrites = true
			}
		}
		body := classifyTokens(toks[i+1:max(end-1, i+1)], d, r)
		if body.Class == Write {
			cteWrites = true // as a body that locks its rows
		}
		if body.Dangerous && !danger.Dangerous {
			danger = body
		}
		i = skipSearchCycle(toks, end)
		if i < len(toks) && isPunct(toks[i], ",") {
			i++
			continue
		}
		break
	}
	if i >= len(toks) && d == ClickHouse {
		// ClickHouse "WITH <expr> AS name, … SELECT": the main statement
		// is the first SELECT outside parentheses.
		dep := depths(toks)
		for j := 1; j < len(toks); j++ {
			if dep[j] > 0 {
				continue
			}
			switch word(toks[j]) {
			case "INSERT", "UPDATE", "DELETE", "MERGE":
				cteWrites = true
			case "SELECT":
				i = j
			}
			if i == j {
				break
			}
		}
	}
	if i >= len(toks) {
		return Analysis{Class: Write, Verb: "WITH"}
	}
	a := classifyTokens(toks[i:], d, r)
	if cteWrites && a.Class == Read {
		a.Class = Write
	}
	if danger.Dangerous && !a.Dangerous {
		a.Dangerous, a.Reason = true, "in a WITH query, "+danger.Reason
	}
	return a
}

// skipSearchCycle returns the index past the Postgres SEARCH and CYCLE
// clauses that may follow a recursive CTE body at i:
// "SEARCH {BREADTH|DEPTH} FIRST BY cols SET col" and
// "CYCLE cols SET col [TO v DEFAULT v] USING col".
func skipSearchCycle(toks []Token, i int) int {
	for i < len(toks) {
		w := word(toks[i])
		if w != "SEARCH" && w != "CYCLE" {
			return i
		}
		j := i + 1
		for j < len(toks) && word(toks[j]) != "SET" {
			j++
		}
		j += 2 // SET col
		if w == "CYCLE" {
			if j < len(toks) && word(toks[j]) == "TO" {
				j += 4 // TO v DEFAULT v
			}
			if j < len(toks) && word(toks[j]) == "USING" {
				j += 2
			}
		}
		if j > len(toks) {
			return len(toks)
		}
		i = j
	}
	return i
}

// skipParens returns the index just past the parenthesis group opening at i.
func skipParens(toks []Token, i int) int {
	depth := 0
	for ; i < len(toks); i++ {
		if isPunct(toks[i], "(") {
			depth++
		} else if isPunct(toks[i], ")") {
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(toks)
}

// classifyExplain classifies EXPLAIN, DESC and DESCRIBE: Read, unless
// ANALYZE runs the statement, which is then classified. CockroachDB's
// options may follow ANALYZE too, in parentheses, and its DEBUG keeps a
// statement bundle on the server: a write. Options are names, quoted or
// not; a U& name, or a UESCAPE, counts as every option.
func classifyExplain(toks []Token, d Dialect, r reading) Analysis {
	read := Analysis{Class: Read, Verb: word(toks[0])}
	i := 1
	analyze, debug := false, false
	options := func(i int) int {
		end := skipParens(toks, i)
		for j := i + 1; j < end; j++ {
			name := ""
			switch t := toks[j]; t.Kind {
			case Keyword, Identifier:
				name = strings.ToUpper(t.Text)
			case QuotedIdent:
				if len(t.Text) > 1 && (t.Text[0] == 'U' || t.Text[0] == 'u') && t.Text[1] == '&' {
					name = "UESCAPE" // not decoded: any option
				} else {
					name = strings.ToUpper(DecodeQuoted(t.Text, d))
				}
			}
			switch name {
			case "UESCAPE":
				analyze, debug = true, d == Postgres
			case "ANALYZE", "ANALYSE":
				next := ""
				if j+1 < end {
					next = strings.ToUpper(toks[j+1].Text)
				}
				analyze = next != "FALSE" && next != "OFF" && next != "0"
			case "DEBUG":
				debug = d == Postgres
			}
		}
		return end
	}
	if i < len(toks) && isPunct(toks[i], "(") {
		i = options(i)
	}
	for i < len(toks) {
		w := word(toks[i])
		if w == "ANALYZE" || w == "ANALYSE" {
			analyze = true
			// Options, not a statement in parentheses, as PostgreSQL's
			// EXPLAIN ANALYZE (SELECT …).
			if i+2 < len(toks) && isPunct(toks[i+1], "(") && !isPunct(toks[i+2], "(") {
				if _, isVerb := verbClasses[word(toks[i+2])]; !isVerb {
					i = options(i+1) - 1
				}
			}
		} else if w == "FORMAT" && i+1 < len(toks) {
			// MySQL "FORMAT=TREE" or "FORMAT TREE"; TiDB "FORMAT='brief'".
			j := i + 1
			if toks[j].Text == "=" {
				j++
			} else if _, isVerb := verbClasses[word(toks[j])]; isVerb {
				break
			}
			if j >= len(toks) || word(toks[j]) == "" && toks[j].Kind != String {
				break
			}
			i = j
		} else if w != "VERBOSE" && w != "EXTENDED" && w != "PARTITIONS" {
			break
		}
		i++
	}
	if debug {
		return Analysis{Class: Write, Verb: read.Verb, Reason: "EXPLAIN with DEBUG keeps a statement bundle on the server"}
	}
	if !analyze || i >= len(toks) {
		return read
	}
	inner := classifyTokens(toks[i:], d, r)
	if inner.Class == Read {
		return read
	}
	return inner
}
