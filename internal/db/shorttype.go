package db

import "strings"

// ShortType is a column's type as a narrow place shows it, as a diagram's
// box or the navigator's row: the SQL standard's long names by the short
// ones PostgreSQL also takes, as varchar for character varying.
func ShortType(typ string) string {
	for _, a := range shortTypeAliases {
		if rest, ok := strings.CutPrefix(typ, a.long); ok && (rest == "" || rest[0] == '(' || rest[0] == '[') {
			typ = a.short + rest
			break
		}
	}
	// A precision before the zone, as timestamp(3) with time zone.
	for _, z := range []struct{ suffix, base, short string }{
		{" with time zone", "timestamp", "timestamptz"}, {" with time zone", "time", "timetz"},
		{" without time zone", "timestamp", "timestamp"}, {" without time zone", "time", "time"},
	} {
		if head, ok := strings.CutSuffix(typ, z.suffix); ok {
			if rest, ok := strings.CutPrefix(head, z.base); ok && (rest == "" || rest[0] == '(') {
				return z.short + rest
			}
		}
	}
	return typ
}

var shortTypeAliases = []struct{ long, short string }{
	{"character varying", "varchar"}, {"character", "char"}, {"bit varying", "varbit"},
}
