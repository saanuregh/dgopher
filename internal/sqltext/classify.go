package sqltext

import "strings"

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
	Postgres: setOf("pg_terminate_backend pg_cancel_backend setval nextval dblink_exec dblink lo_import lo_export lo_unlink lo_create lo_from_bytea lo_put lowrite pg_advisory_lock pg_advisory_xact_lock pg_try_advisory_lock pg_reload_conf pg_rotate_logfile pg_notify pg_switch_wal pg_create_restore_point pg_promote set_config pg_file_write pg_logical_emit_message"),
	MySQL:    setOf("get_lock release_lock release_all_locks sleep benchmark"),
	SQLite:   setOf("load_extension"),
}

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
func Classify(stmt string, d Dialect) Analysis {
	rs := []rune(stmt)
	o := SplitOptions{Mode: SemicolonOnly}
	segs := splitSegments(rs, d, o)
	first, count := statementCode(segs)
	if count == 0 {
		if strings.TrimSpace(stmt) == "" {
			return Analysis{Class: Read}
		}
		return Analysis{Class: Write, Dangerous: false}
	}
	a := classifyTokens(first, d)
	if count == 1 {
		for _, seg := range segs {
			if bodyHidesStatement(seg, d) {
				if _, n := statementCode(splitSegmentsWithoutBodies(rs, d, o)); n > count {
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
		if stmts := statementsCode(splitSegmentsWithoutBodies(rs, d, o)); len(stmts) > 1 {
			if inner, ok := dangerInside(stmts, d); ok {
				a.Dangerous = true
				a.Reason = "the routine's text holds a statement that is dangerous on its own: " + inner.Reason
			}
		}
	}
	return a
}

// dangerInside finds a dangerous statement among stmts: each one, and
// within each, what follows a word that opens a block.
func dangerInside(stmts [][]Token, d Dialect) (Analysis, bool) {
	for i, code := range stmts {
		for j := range code {
			starts := j == 0 && i > 0
			if j > 0 {
				switch word(code[j-1]) {
				case "BEGIN", "ATOMIC", "THEN", "ELSE", "DO", "LOOP":
					starts = true
				}
			}
			if !starts {
				continue
			}
			if inner := classifyTokens(code[j:], d); inner.Dangerous {
				return inner, true
			}
		}
	}
	return Analysis{}, false
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

func classifyTokens(toks []Token, d Dialect) Analysis {
	for len(toks) > 0 && isPunct(toks[0], "(") {
		toks = toks[1:]
	}
	if len(toks) == 0 {
		return Analysis{Class: Write, Reason: ""}
	}
	a := classifyVerb(toks, d)
	switch word(toks[0]) {
	case "EXPLAIN", "DESC", "DESCRIBE":
		// Plain EXPLAIN runs nothing; EXPLAIN ANALYZE classified its
		// statement, calls included.
		return a
	}
	if a.Class == Read {
		if fn := sideEffectCall(toks, d); fn != "" {
			a.Class = Write
			a.Reason = "calls " + fn + ", which has side effects"
		}
	}
	return a
}

// sideEffectCall returns the first function in toks, called anywhere, that
// changes state on dialect d, or "". Quoted names are decoded first; with a
// UESCAPE anywhere, whose escapes are not decoded, any call to a U& name
// counts.
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
	}
	return ""
}

func classifyVerb(toks []Token, d Dialect) Analysis {
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
		return classifyWith(toks, d)
	case "EXPLAIN", "DESC", "DESCRIBE":
		return classifyExplain(toks, d)
	case "EXISTS", "CHECK", "CHECKSUM":
		if d == ClickHouse || next == "TABLE" {
			return Analysis{Class: Read, Verb: verb}
		}
	case "SELECT", "TABLE", "VALUES":
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
		if d == MySQL {
			// RESET MASTER, REPLICA, PERSIST and the like change server state.
			return Analysis{Class: Write, Verb: verb}
		}
	case "SET":
		if setsServerState(toks, dep) {
			return Analysis{Class: Write, Verb: verb}
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
			if a, ok := classifyCursor(toks, dep, d); ok {
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
		return Analysis{Class: Session, Verb: verb}
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
		if d == ClickHouse {
			for _, m := range []string{"DELETE", "UPDATE"} {
				if topLevel(m) >= 0 {
					a.Dangerous = true
					a.Reason = "ALTER TABLE ... " + m + " is a ClickHouse mutation that rewrites table data and cannot be rolled back"
				}
			}
		}
		return a
	}
	if c, ok := verbClasses[verb]; ok {
		return Analysis{Class: c, Verb: verb}
	}
	return Analysis{Class: Write, Verb: verb}
}

// setsServerState reports whether a SET changes more than the session: MySQL
// GLOBAL, PERSIST and PERSIST_ONLY variables, SET PASSWORD and SET DEFAULT
// ROLE, which changes an account.
func setsServerState(toks []Token, dep []int) bool {
	if len(toks) > 1 && word(toks[1]) == "PASSWORD" {
		return true
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

// classifyCursor classifies Postgres "DECLARE name … CURSOR … FOR query" by
// its query.
func classifyCursor(toks []Token, dep []int, d Dialect) (Analysis, bool) {
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
			a := classifyTokens(toks[i+1:], d)
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

func classifyWith(toks []Token, d Dialect) Analysis {
	i := 1
	if i < len(toks) && word(toks[i]) == "RECURSIVE" {
		i++
	}
	cteWrites := false
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
	a := classifyTokens(toks[i:], d)
	if cteWrites && a.Class == Read {
		a.Class = Write
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
// ANALYZE runs the statement, which is then classified.
func classifyExplain(toks []Token, d Dialect) Analysis {
	read := Analysis{Class: Read, Verb: word(toks[0])}
	i := 1
	analyze := false
	if i < len(toks) && isPunct(toks[i], "(") {
		end := skipParens(toks, i)
		for j := i + 1; j < end; j++ {
			if w := word(toks[j]); w == "ANALYZE" || w == "ANALYSE" {
				next := ""
				if j+1 < end {
					next = strings.ToUpper(toks[j+1].Text)
				}
				analyze = next != "FALSE" && next != "OFF" && next != "0"
			}
		}
		i = end
	}
	for i < len(toks) {
		w := word(toks[i])
		if w == "ANALYZE" || w == "ANALYSE" {
			analyze = true
		} else if w == "FORMAT" && i+1 < len(toks) {
			// MySQL "FORMAT=TREE" or "FORMAT TREE".
			j := i + 1
			if toks[j].Text == "=" {
				j++
			} else if _, isVerb := verbClasses[word(toks[j])]; isVerb {
				break
			}
			if j >= len(toks) || word(toks[j]) == "" {
				break
			}
			i = j
		} else if w != "VERBOSE" && w != "EXTENDED" && w != "PARTITIONS" {
			break
		}
		i++
	}
	if !analyze || i >= len(toks) {
		return read
	}
	inner := classifyTokens(toks[i:], d)
	if inner.Class == Read {
		return read
	}
	return inner
}
