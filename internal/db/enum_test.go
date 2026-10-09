package db

import (
	"context"
	"slices"
	"testing"
)

func TestEnumValues(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		engine Engine
		typ    string
		want   []string
	}{
		{MySQL, "enum('small','it''s','back\\'slash')", []string{"small", "it's", "back'slash"}},
		{DuckDB, "ENUM('a', 'b c')", []string{"a", "b c"}},
		{ClickHouse, "Nullable(Enum8('on' = 1, 'off' = -2))", []string{"on", "off"}},
		{ClickHouse, "LowCardinality(Nullable(Enum16('x' = 1)))", []string{"x"}},
		{MySQL, "varchar(20)", nil},
		{SQLite, "TEXT", nil},
	} {
		got, err := EnumValues(ctx, &DB{Dialect: DialectOf(c.engine)}, c.typ)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s %s: %q %v", c.engine, c.typ, got, err)
		}
	}
}
