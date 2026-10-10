// Package safety is the write-safety policy: which statements and Redis
// commands run, which ask the user first, and which are refused, by the
// connection's environment.
package safety

import (
	"fmt"
	"slices"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/sqltext"
)

// Verdict is what the safety policy says of statements about to run.
type Verdict struct {
	// Blocked refuses them, with why.
	Blocked string
	// Confirm asks the user first, with reasons.
	Confirm bool
	Reasons []string
	// TypeName asks the user to type the connection's name, for what
	// cannot be undone on production.
	TypeName bool
	// Writes reports statements that change data or schema, which open a
	// transaction first in manual-commit mode; those that cannot run in
	// one, as VACUUM, do not count.
	Writes bool
}

// Statement is a statement with what the lexer makes of it.
type Statement struct {
	SQL      string
	Analysis sqltext.Analysis
	// Start is where the statement starts in its editor, for errors to
	// point into it; -1 when it did not come from one.
	Start int
	// Skip counts the runes the app put before the editor's text, as
	// EXPLAIN, which an error's position counts too.
	Skip int
	// Args are the values of its parameters, and Shown lists them.
	Args  []any
	Shown string
}

func Analyze(cfg *db.Config, stmts []string) []Statement {
	d := Dialect(cfg.Engine)
	out := make([]Statement, len(stmts))
	for i, s := range stmts {
		out[i] = Statement{SQL: s, Analysis: sqltext.Classify(PolicyText(cfg.Engine, s), d), Start: -1}
	}
	return out
}

// PolicyText is the text the safety policy reads: what the server will
// run, MySQL's executable comments included.
func PolicyText(e db.Engine, sql string) string {
	if e == db.MySQL {
		return sqltext.UnwrapExecutable(sql)
	}
	return sql
}

// ReviewSQL applies the safety policy of a connection to statements:
//
//   - read-only connections refuse whatever writes, as the server does;
//   - what destroys data (DROP, TRUNCATE, UPDATE or DELETE without WHERE)
//     is confirmed everywhere, by typing the connection's name on
//     production;
//   - on production every write and schema change is confirmed;
//   - on staging, schema changes are.
func ReviewSQL(cfg *db.Config, stmts []Statement) Verdict {
	var v Verdict
	env := cfg.Env
	for _, s := range stmts {
		cls := s.Analysis.Class
		mutates := cls == sqltext.Write || cls == sqltext.DDL
		if !v.Writes && WritesInTransaction(cfg.Engine, s) {
			v.Writes = true
		}
		if cfg.ReadOnly {
			if mutates {
				v.Blocked = fmt.Sprintf("%s is read-only: %s statements are not allowed. Edit the connection to allow writes.", cfg.Name, verbOf(s.Analysis))
				return v
			}
			if disablesReadOnly(PolicyText(cfg.Engine, s.SQL), Dialect(cfg.Engine)) {
				v.Blocked = cfg.Name + " is read-only: the session's read-only setting cannot be changed."
				return v
			}
		}
		if s.Analysis.Dangerous {
			v.Confirm = true
			v.Reasons = append(v.Reasons, s.Analysis.Reason)
			if env == db.Production {
				v.TypeName = true
			}
			continue
		}
		switch {
		case env == db.Production && mutates:
			v.Confirm = true
			v.Reasons = append(v.Reasons, fmt.Sprintf("%s on production", verbOf(s.Analysis)))
		case env == db.Staging && cls == sqltext.DDL:
			v.Confirm = true
			v.Reasons = append(v.Reasons, fmt.Sprintf("%s changes the schema of staging", verbOf(s.Analysis)))
		}
	}
	v.Reasons = dedupe(v.Reasons)
	return v
}

// ManyRows makes a change, a row at a time, of more rows already there
// than the limit ask on production for the connection's name, as what
// cannot be undone does: so many rows changed are as an UPDATE without
// WHERE. A limit of 0 is none.
func (v *Verdict) ManyRows(cfg *db.Config, rows, limit int) {
	if limit <= 0 || rows <= limit || cfg.Env != db.Production {
		return
	}
	v.Confirm, v.TypeName = true, true
	v.Reasons = append(v.Reasons, fmt.Sprintf("It changes or deletes %d rows already there, more than the %d a change makes without asking (Settings).", rows, limit))
}

// EndsTransaction makes statements that commit the transaction open, as
// MySQL's DDL does without being asked, ask first: Roll Back then cannot
// undo what ran in it before them.
func (v *Verdict) EndsTransaction(cfg *db.Config, stmts []Statement, open bool) {
	if !open {
		return
	}
	for _, s := range stmts {
		if CommitsImplicitly(cfg.Engine, s) {
			v.Confirm = true
			v.Reasons = append(v.Reasons, verbOf(s.Analysis)+" commits the open transaction: Roll Back will not undo what ran before it")
		}
	}
	v.Reasons = dedupe(v.Reasons)
}

// EndsRunTransaction is EndsTransaction for statements run with no
// transaction open. It follows the transaction they run in: under manual
// commit, the app opens one before they write and again after a statement
// commits it; otherwise only a typed BEGIN or START TRANSACTION opens one,
// which holds the statements after it until the transaction ends. A
// statement that commits implicitly asks only after a write it commits,
// for before one the transaction holds nothing.
func (v *Verdict) EndsRunTransaction(cfg *db.Config, stmts []Statement, manual bool) {
	held := false // a write waits in the transaction
	begun := ""   // the typed statement that began the open transaction
	for _, s := range stmts {
		switch {
		case CommitsImplicitly(cfg.Engine, s):
			if held {
				v.Confirm = true
				if manual {
					v.Reasons = append(v.Reasons, verbOf(s.Analysis)+" commits the writes before it, which manual commit holds in a transaction: Roll Back will not undo them")
				} else {
					v.Reasons = append(v.Reasons, verbOf(s.Analysis)+" commits the writes since "+begun+": rolling back after it will not undo them")
				}
			}
			held, begun = false, ""
		case CommitsOrRollsBack(cfg.Engine, s):
			held, begun = false, ""
		case WritesInTransaction(cfg.Engine, s):
			held = manual || begun != ""
		}
		if BeginsTransaction(s) {
			begun = BeginStatement(s.Analysis.Verb)
		}
	}
	v.Reasons = dedupe(v.Reasons)
}

// WritesInTransaction reports whether a statement changes data or schema
// in the transaction manual commit opens for it (Verdict.Writes): not one
// that cannot run in a transaction, as VACUUM.
func WritesInTransaction(e db.Engine, s Statement) bool {
	cls := s.Analysis.Class
	return (cls == sqltext.Write || cls == sqltext.DDL) && !OutsideTransaction(e, s.SQL)
}

// CommitsOrRollsBack reports whether a statement ends the open transaction
// as asked to: COMMIT, END, ROLLBACK or ABORT, AND CHAIN too, but not
// ROLLBACK TO a savepoint, after which the transaction goes on.
func CommitsOrRollsBack(e db.Engine, s Statement) bool {
	if s.Analysis.Class != sqltext.Transaction {
		return false
	}
	ends, _ := db.EndsTransaction(e, PolicyText(e, s.SQL))
	return ends
}

// CommitsImplicitly reports whether a statement commits the open
// transaction without being asked, as MySQL's DDL does
// (db.CommitsImplicitly), its executable comments read.
func CommitsImplicitly(e db.Engine, s Statement) bool {
	return db.CommitsImplicitly(e, PolicyText(e, s.SQL))
}

// BeginsTransaction reports whether a statement begins a transaction, as
// the classifier reads it: BEGIN or START TRANSACTION.
func BeginsTransaction(s Statement) bool {
	return s.Analysis.Class == sqltext.Transaction && (s.Analysis.Verb == "BEGIN" || s.Analysis.Verb == "START")
}

// ControlsTransaction reports whether a statement begins a transaction or
// ends the open one as asked to (BeginsTransaction, CommitsOrRollsBack).
func ControlsTransaction(e db.Engine, s Statement) bool {
	return CommitsOrRollsBack(e, s) || BeginsTransaction(s)
}

// BeginStatement names the statement a transaction's beginning verb, as
// the classifier reads it, stands for: "BEGIN", "START TRANSACTION", or ""
// for another verb.
func BeginStatement(verb string) string {
	switch verb {
	case "BEGIN":
		return "BEGIN"
	case "START":
		return "START TRANSACTION"
	}
	return ""
}

// Add takes in the verdict of more statements, as one review of them
// all would have given, for statements reviewed as they are read.
func (v *Verdict) Add(w Verdict) {
	if v.Blocked == "" {
		v.Blocked = w.Blocked
	}
	v.Confirm = v.Confirm || w.Confirm
	v.TypeName = v.TypeName || w.TypeName
	v.Writes = v.Writes || w.Writes
	v.Reasons = dedupe(append(v.Reasons, w.Reasons...))
}

// Covers reports whether agreeing to v agreed to w as well: it confirms
// no less, and blocks nothing.
func (v Verdict) Covers(w Verdict) bool {
	return w.Blocked == "" && (v.Confirm || !w.Confirm) && (v.TypeName || !w.TypeName)
}

// OutsideTransaction reports whether a statement cannot run in a
// transaction, which manual commit must then not open for it:
// PostgreSQL's VACUUM, CREATE and DROP of a DATABASE or a TABLESPACE,
// ALTER SYSTEM and the CONCURRENTLY forms of index commands, and the
// VACUUM of SQLite and DuckDB.
func OutsideTransaction(e db.Engine, sql string) bool {
	if e != db.Postgres && e != db.SQLite && e != db.DuckDB {
		return false
	}
	var words []string
	for _, t := range sqltext.Tokenize(sql, Dialect(e)) {
		if t.Kind == sqltext.Keyword || t.Kind == sqltext.Identifier {
			words = append(words, strings.ToUpper(t.Text))
		}
	}
	if len(words) == 0 {
		return false
	}
	switch e {
	case db.SQLite, db.DuckDB:
		return words[0] == "VACUUM"
	case db.Postgres:
		switch words[0] {
		case "VACUUM":
			return true
		case "ALTER":
			return len(words) > 1 && words[1] == "SYSTEM"
		case "REINDEX":
			return slices.Contains(words, "CONCURRENTLY")
		case "CREATE", "DROP":
			if len(words) > 1 && (words[1] == "DATABASE" || words[1] == "TABLESPACE") {
				return true
			}
			// CREATE [UNIQUE] INDEX CONCURRENTLY, DROP INDEX CONCURRENTLY:
			// the word only there, never a table of that name.
			i := slices.Index(words[:min(3, len(words))], "INDEX")
			return i > 0 && i+1 < len(words) && words[i+1] == "CONCURRENTLY"
		}
	}
	return false
}

func verbOf(a sqltext.Analysis) string {
	if a.Verb == "" {
		return "This statement"
	}
	return a.Verb
}

// disablesReadOnly reports whether a statement could turn the session's
// read-only mode off: SET and BEGIN … READ WRITE, set_config(), the
// settings that hold the mode, DuckDB's access_mode among them, and an
// ATTACH's READ_WRITE. It reads the tokens, so that comments and spacing
// between the words hide nothing.
func disablesReadOnly(sql string, d sqltext.Dialect) bool {
	var words []string
	for _, t := range sqltext.Tokenize(sql, d) {
		switch t.Kind {
		case sqltext.Whitespace, sqltext.Comment:
			continue
		case sqltext.String, sqltext.QuotedIdent:
			words = append(words, strings.ToLower(sqltext.DecodeQuoted(t.Text, d)))
		default:
			words = append(words, strings.ToLower(t.Text))
		}
	}
	for i, w := range words {
		switch {
		// A custom escape character is not decoded: the name could be anything.
		case w == "uescape",
			strings.Contains(w, "read_only"), strings.Contains(w, "readonly"), strings.Contains(w, "query_only"),
			strings.Contains(w, "access_mode"), strings.Contains(w, "read_write"),
			w == "set_config", w == "read" && i+1 < len(words) && words[i+1] == "write":
			return true
		}
	}
	return false
}

// ReviewRedis applies the policy to a Redis command.
func ReviewRedis(cfg *db.Config, kv *db.KV, args []string) Verdict {
	var v Verdict
	if len(args) == 0 {
		return v
	}
	name := strings.ToUpper(args[0])
	if why := db.StatefulCommand(args); why != "" {
		v.Blocked = why
		return v
	}
	readOnly := kv != nil && kv.IsReadOnly(args...)
	if !readOnly {
		v.Writes = true
	}
	if cfg.ReadOnly && !readOnly {
		v.Blocked = fmt.Sprintf("%s is read-only: %s writes. Edit the connection to allow writes.", cfg.Name, name)
		return v
	}
	if risk := db.RiskOf(args); risk != "" {
		v.Confirm = true
		v.Reasons = append(v.Reasons, risk)
		if cfg.Env == db.Production {
			v.TypeName = true
		}
		return v
	}
	if cfg.Env == db.Production && !readOnly {
		v.Confirm = true
		v.Reasons = append(v.Reasons, name+" changes data on production")
	}
	return v
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	out := xs[:0]
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// Dialect maps an engine to the lexer's dialect.
func Dialect(e db.Engine) sqltext.Dialect { return db.LexDialect(e) }
