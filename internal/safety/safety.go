// Package safety is the write-safety policy: which statements and Redis
// commands run, which ask the user first, and which are refused, by the
// connection's environment.
package safety

import (
	"fmt"
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
	// transaction first in manual-commit mode.
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
		return unwrapExecutable(sql)
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
		if mutates {
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

func verbOf(a sqltext.Analysis) string {
	if a.Verb == "" {
		return "This statement"
	}
	return a.Verb
}

// disablesReadOnly reports whether a statement could turn the session's
// read-only mode off: SET and BEGIN … READ WRITE, set_config(), and the
// settings that hold the mode. It reads the tokens, so that comments and
// spacing between the words hide nothing.
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

// unwrapExecutable turns MySQL's executable comments, /*! … */, /*!80000
// … */ and MariaDB's /*M! … */, into the code they hold, which the server
// runs: classified as comments, they would hide a DELETE or an INTO
// OUTFILE from the policy. Strings and ordinary comments are copied as
// they are, so a quote inside them misleads nothing.
func unwrapExecutable(sql string) string {
	if !strings.Contains(sql, "/*!") && !strings.Contains(sql, "/*M!") {
		return sql
	}
	var b strings.Builder
	r := []rune(sql)
	inExec := false
	at := func(i int, s string) bool { return strings.HasPrefix(string(r[i:min(len(r), i+len(s))]), s) }
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			// A string, or a quoted name, to its end.
			b.WriteRune(c)
			for i++; i < len(r); i++ {
				b.WriteRune(r[i])
				if r[i] == '\\' && c != '`' && i+1 < len(r) {
					i++
					b.WriteRune(r[i])
					continue
				}
				if r[i] == c {
					break
				}
			}
			continue
		case at(i, "/*!") || at(i, "/*M!"):
			inExec = true
			b.WriteRune(' ')
			i += 2
			if r[i] == 'M' {
				i++
			}
			for i+1 < len(r) && r[i+1] >= '0' && r[i+1] <= '9' {
				i++ // the version it needs
			}
			continue
		case inExec && at(i, "*/"):
			inExec = false
			b.WriteRune(' ')
			i++
			continue
		case !inExec && at(i, "/*"):
			end := strings.Index(string(r[i+2:]), "*/")
			if end < 0 {
				b.WriteString(string(r[i:]))
				return b.String()
			}
			n := len([]rune(string(r[i+2:])[:end]))
			b.WriteString(string(r[i : i+2+n+2]))
			i += 2 + n + 1
			continue
		case at(i, "-- ") || at(i, "--\t") || at(i, "--\n") || c == '#':
			for ; i < len(r) && r[i] != '\n'; i++ {
				b.WriteRune(r[i])
			}
			if i < len(r) {
				b.WriteRune('\n')
			}
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

// Dialect maps an engine to the lexer's dialect.
func Dialect(e db.Engine) sqltext.Dialect {
	switch e {
	case db.Postgres, db.DuckDB:
		return sqltext.Postgres
	case db.MySQL:
		return sqltext.MySQL
	case db.ClickHouse:
		return sqltext.ClickHouse
	case db.SQLite:
		return sqltext.SQLite
	}
	return sqltext.Generic
}
