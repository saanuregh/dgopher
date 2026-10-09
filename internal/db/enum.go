package db

import (
	"context"
	"regexp"
	"strings"
)

var (
	enumList       = regexp.MustCompile(`(?is)^enum\s*\((.*)\)$`)
	clickhouseEnum = regexp.MustCompile(`(?is)^enum(8|16)\s*\((.*)\)$`)
	wrapped        = regexp.MustCompile(`(?is)^(nullable|lowcardinality)\s*\((.*)\)$`)
)

// EnumValues lists the values a column of type typ may hold, when the
// type is an enum, in their order; nil for any other type. PostgreSQL's
// enums are read from its catalog; the other engines' types spell them.
func EnumValues(ctx context.Context, d *DB, typ string) ([]string, error) {
	typ = strings.TrimSpace(typ)
	switch d.Dialect.Engine() {
	case Postgres:
		return queryStrings(ctx, d.SQL, `SELECT enumlabel FROM pg_enum WHERE enumtypid = to_regtype($1) ORDER BY enumsortorder`, typ)
	case ClickHouse:
		for {
			m := wrapped.FindStringSubmatch(typ)
			if m == nil {
				break
			}
			typ = strings.TrimSpace(m[2])
		}
		if m := clickhouseEnum.FindStringSubmatch(typ); m != nil {
			return quotedValues(m[2], true), nil
		}
	case MySQL, DuckDB:
		if m := enumList.FindStringSubmatch(typ); m != nil {
			return quotedValues(m[1], d.Dialect.Engine() == MySQL), nil
		}
	}
	return nil, nil
}

// quotedValues reads the quoted strings of a list, a quote inside one
// doubled or, with backslashes, escaped by one; what follows each up to
// the next comma, as ClickHouse's = 1, is passed over.
func quotedValues(list string, backslashes bool) []string {
	var out []string
	var b strings.Builder
	in := false
	for i := 0; i < len(list); i++ {
		ch := list[i]
		switch {
		case !in && ch == '\'':
			in = true
			b.Reset()
		case in && backslashes && ch == '\\' && i+1 < len(list):
			i++
			b.WriteByte(list[i])
		case in && ch == '\'' && i+1 < len(list) && list[i+1] == '\'':
			i++
			b.WriteByte('\'')
		case in && ch == '\'':
			in = false
			out = append(out, b.String())
		case in:
			b.WriteByte(ch)
		}
	}
	return out
}
