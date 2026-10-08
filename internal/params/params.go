// Package params binds the values the user gives a statement's :name and
// ${var} parameters.
package params

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"dgopher/internal/db"
	"dgopher/internal/sqltext"
)

// Key is a parameter as it is written: ":id", "@id", "${table}",
// "{id:UInt32}".
func Key(p sqltext.Parameter) string {
	switch p.Kind {
	case sqltext.ParamVar:
		return "${" + p.Name + "}"
	case sqltext.ParamTyped:
		return "{" + p.Name + ":" + p.Type + "}"
	}
	if p.Sigil == 0 {
		return ":" + p.Name
	}
	return string(p.Sigil) + p.Name
}

// Keys lists the parameters of statements, each once, in order.
func Keys(stmts []string, d sqltext.Dialect) []string {
	var keys []string
	seen := map[string]bool{}
	for _, s := range stmts {
		for _, p := range sqltext.Params(s, d) {
			if k := Key(p); !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	return keys
}

// Input is a value given to a parameter, and how to send it:
// "auto", "text", "number" or "null".
type Input struct {
	Value, Kind string
}

var Kinds = []string{"auto", "text", "number", "null"}

// NumberOf reads a number as a parameter or a filter sends it: an int64,
// else a finite float; ok is false for text, and for codes such as
// "0123" that a number would change.
func NumberOf(s string) (any, bool) {
	digits := strings.TrimLeft(s, "+-")
	if digits == "" || digits[0] < '0' || digits[0] > '9' || len(digits) > 1 && digits[0] == '0' && digits[1] != '.' {
		return nil, false
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, true
	}
	if strings.Trim(digits, "0123456789") == "" {
		return nil, false // an integer past int64: a float would round it
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, false
	}
	return f, true
}

// Value is what a parameter binds as. Auto sends NULL for "null";
// on PostgreSQL text, which the server reads as the type the statement
// needs; elsewhere a number when the value reads as one, else text.
func Value(in Input, e db.Engine) (any, error) {
	switch in.Kind {
	case "null":
		return nil, nil
	case "text":
		return in.Value, nil
	case "number":
		if n, ok := NumberOf(strings.TrimSpace(in.Value)); ok {
			return n, nil
		}
		return nil, fmt.Errorf("%q is not a number", in.Value)
	}
	if strings.EqualFold(strings.TrimSpace(in.Value), "null") {
		return nil, nil
	}
	if e == db.Postgres {
		return in.Value, nil
	}
	if n, ok := NumberOf(in.Value); ok {
		return n, nil
	}
	return in.Value, nil
}

func Literal(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case string:
		return "'" + strings.ReplaceAll(x, "'", "''") + "'"
	}
	return db.Display(v)
}

// Bind writes ${var}s into the statement as text and turns
// :names into the engine's placeholders with their values. shown lists
// the values, for the history and the audit log.
func Bind(sql string, e db.Engine, d sqltext.Dialect, values map[string]Input) (string, []any, string, error) {
	params := sqltext.Params(sql, d)
	if len(params) == 0 {
		return sql, nil, "", nil
	}
	if err := mixedStyles(sql, d, params); err != nil {
		return "", nil, "", err
	}
	sort.Slice(params, func(i, j int) bool { return params[i].Start < params[j].Start })
	dialect := db.DialectOf(e)
	rs := []rune(sql)
	var b strings.Builder
	var args []any
	var shown []string
	seen := map[string]bool{}
	at := 0
	for _, p := range params {
		b.WriteString(string(rs[at:p.Start]))
		key := Key(p)
		in := values[key]
		switch p.Kind {
		case sqltext.ParamVar:
			b.WriteString(in.Value)
			if !seen[key] {
				shown = append(shown, key+" = "+in.Value)
			}
		case sqltext.ParamTyped:
			b.WriteString(string(rs[p.Start:p.End]))
			v := in.Value
			if in.Kind == "null" || in.Kind == "auto" && strings.EqualFold(strings.TrimSpace(v), "null") {
				v = `\N`
			}
			args = append(args, db.ServerParam{Name: p.Name, Value: v})
			if !seen[key] {
				shown = append(shown, key+" = "+v)
			}
		default:
			v, err := Value(in, e)
			if err != nil {
				return "", nil, "", fmt.Errorf("%s: %w", key, err)
			}
			args = append(args, v)
			b.WriteString(dialect.Placeholder(len(args)))
			if !seen[key] {
				shown = append(shown, key+" = "+Literal(v))
			}
		}
		seen[key] = true
		at = p.End
	}
	b.WriteString(string(rs[at:]))
	return b.String(), args, strings.Join(shown, ", "), nil
}

// mixedStyles refuses a statement with both :name parameters and the
// engine's own placeholders, which would be numbered or ordered wrongly.
func mixedStyles(sql string, d sqltext.Dialect, params []sqltext.Parameter) error {
	named := false
	for _, p := range params {
		named = named || p.Kind == sqltext.ParamNamed
	}
	if !named {
		return nil
	}
	for _, t := range sqltext.Tokenize(sql, d) {
		if t.Kind != sqltext.Param {
			continue
		}
		if d == sqltext.Postgres && len(t.Text) > 1 && t.Text[0] == '$' && strings.Trim(t.Text[1:], "0123456789") == "" {
			return fmt.Errorf("this statement mixes :name parameters with $1-style placeholders; use one style")
		}
		if d != sqltext.Postgres && t.Text == "?" {
			return fmt.Errorf("this statement mixes :name parameters with ? placeholders; use one style")
		}
	}
	return nil
}
