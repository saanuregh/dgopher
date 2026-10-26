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
	if r, ok := SQL(d.Dialect.Engine()); ok {
		return r.EnumValues(ctx, d, typ)
	}
	return nil, nil
}

// enumListValues are the values of a type written ENUM('a', 'b'), nil for
// any other type; with backslashes, a backslash escapes in them.
func enumListValues(typ string, backslashes bool) []string {
	if m := enumList.FindStringSubmatch(strings.TrimSpace(typ)); m != nil {
		return quotedValues(m[1], backslashes)
	}
	return nil
}

// clickhouseInnerType unwraps ClickHouse's Nullable(…) and
// LowCardinality(…), however nested, to the type they hold.
func clickhouseInnerType(typ string) string {
	for {
		m := wrapped.FindStringSubmatch(typ)
		if m == nil {
			return typ
		}
		typ = strings.TrimSpace(m[2])
	}
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
