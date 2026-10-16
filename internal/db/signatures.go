package db

import (
	"context"
	"fmt"
	"strings"

	"dgopher/internal/sqltext"
)

// Signature is a function's calling form, as a hover card shows it. It is
// sqltext's, as sqltext keeps the tables of engines without a catalog.
type Signature = sqltext.Signature

// SignatureReader is a dialect that reads its server's built-in functions.
type SignatureReader interface {
	Signatures(ctx context.Context, q Querier) ([]Signature, error)
}

// Signatures reads pg_catalog's functions.
func (postgresDialect) Signatures(ctx context.Context, q Querier) ([]Signature, error) {
	return scanAll(ctx, q, `SELECT p.proname, pg_get_function_arguments(p.oid), coalesce(pg_get_function_result(p.oid), ''),
  coalesce(obj_description(p.oid, 'pg_proc'), ''), p.prokind = 'a'
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = 'pg_catalog'
ORDER BY p.proname`, nil, func(scan func(...any) error) (Signature, error) {
		var s Signature
		err := scan(&s.Name, &s.Args, &s.Result, &s.Description, &s.Aggregate)
		return s, err
	})
}

// Signatures reads DuckDB's built-in scalar functions, aggregates and
// macros.
func (duckdbDialect) Signatures(ctx context.Context, q Querier) ([]Signature, error) {
	return scanAll(ctx, q, `SELECT function_name, parameters, parameter_types, coalesce(return_type, ''), coalesce(varargs, ''),
  coalesce(description, ''), function_type
FROM duckdb_functions() WHERE internal AND function_type IN ('scalar', 'aggregate', 'macro')
ORDER BY function_name`, nil, func(scan func(...any) error) (Signature, error) {
		var s Signature
		var names, types any
		var varargs, kind string
		if err := scan(&s.Name, &names, &types, &s.Result, &varargs, &s.Description, &kind); err != nil {
			return s, err
		}
		s.Args = duckdbArgs(names, types, varargs)
		s.Aggregate = kind == "aggregate"
		return s, nil
	})
}

// duckdbArgs writes a DuckDB function's arguments as "name type, ...",
// from lists of names and types either of which may be shorter or NULL,
// then "type..." when it takes more of a type.
func duckdbArgs(names, types any, varargs string) string {
	ns, ts := duckdbList(names), duckdbList(types)
	var parts []string
	for i := range max(len(ns), len(ts)) {
		var p []string
		if i < len(ns) && ns[i] != "" {
			p = append(p, ns[i])
		}
		if i < len(ts) && ts[i] != "" {
			p = append(p, ts[i])
		}
		parts = append(parts, strings.Join(p, " "))
	}
	if varargs != "" {
		parts = append(parts, varargs+"...")
	}
	return strings.Join(parts, ", ")
}

// duckdbList is a DuckDB list as the driver returns it ([]any), as text.
func duckdbList(v any) []string {
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		out := make([]string, len(l))
		for i, x := range l {
			if x != nil {
				out[i] = fmt.Sprint(x)
			}
		}
		return out
	}
	return nil
}

// Signatures reads system.functions. A server before its documentation
// columns, of version 23, gives names alone.
func (clickhouseDialect) Signatures(ctx context.Context, q Querier) ([]Signature, error) {
	sigs, err := scanAll(ctx, q, `SELECT name, syntax, returned_value, description, is_aggregate FROM system.functions ORDER BY name`, nil,
		func(scan func(...any) error) (Signature, error) {
			var s Signature
			var syntax string
			var aggregate uint8
			err := scan(&s.Name, &syntax, &s.Result, &s.Description, &aggregate)
			s.Args, s.Aggregate = clickhouseArgs(syntax), aggregate != 0
			return s, err
		})
	if err != nil && (strings.Contains(err.Error(), "UNKNOWN_IDENTIFIER") || strings.Contains(err.Error(), "Missing columns")) {
		return scanAll(ctx, q, `SELECT name, is_aggregate FROM system.functions ORDER BY name`, nil,
			func(scan func(...any) error) (Signature, error) {
				var s Signature
				var aggregate uint8
				err := scan(&s.Name, &aggregate)
				s.Aggregate = aggregate != 0
				return s, err
			})
	}
	return sigs, err
}

// clickhouseArgs is the text inside a ClickHouse syntax line's last
// top-level parentheses: an aggregate's parameters come first, as
// quantile(level)(expr).
func clickhouseArgs(syntax string) string {
	syntax, _, _ = strings.Cut(strings.TrimSpace(syntax), "\n")
	depth, start, args := 0, -1, ""
	for i, r := range syntax {
		switch r {
		case '(':
			if depth == 0 {
				start = i + 1
			}
			depth++
		case ')':
			depth--
			if depth == 0 && start >= 0 {
				args = syntax[start:i]
			}
		}
	}
	return strings.TrimSpace(args)
}
