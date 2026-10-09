package db

import "testing"

func TestCanonicalType(t *testing.T) {
	for _, c := range []struct {
		e         Engine
		typ, want string
	}{
		{Postgres, "integer", "BIGINT"}, {Postgres, "numeric(12,2)", "DECIMAL(12,2)"}, {Postgres, "numeric", "VARCHAR"},
		{Postgres, "character varying(20)", "VARCHAR"}, {Postgres, "timestamp(3) with time zone", "TIMESTAMPTZ"},
		{Postgres, "timestamp without time zone", "TIMESTAMP"}, {Postgres, "double precision", "DOUBLE"}, {Postgres, "jsonb", "JSON"},
		{Postgres, "bytea", "BLOB"}, {Postgres, "integer[]", "VARCHAR"}, {Postgres, "time without time zone", "TIME"},
		{MySQL, "tinyint(1)", "BOOLEAN"}, {MySQL, "int(11) unsigned", "BIGINT"}, {MySQL, "bigint unsigned", "UBIGINT"},
		{MySQL, "datetime(6)", "TIMESTAMP"}, {MySQL, "enum('a','b')", "VARCHAR"}, {MySQL, "decimal(10,4)", "DECIMAL(10,4)"},
		{SQLite, "INTEGER", "BIGINT"}, {SQLite, "VARCHAR(10)", "VARCHAR"}, {SQLite, "", "BLOB"}, {SQLite, "REAL", "DOUBLE"},
		{ClickHouse, "Nullable(UInt64)", "UBIGINT"}, {ClickHouse, "LowCardinality(Nullable(String))", "VARCHAR"},
		{ClickHouse, "DateTime64(3, 'UTC')", "TIMESTAMP"}, {ClickHouse, "Decimal(18, 4)", "DECIMAL(18,4)"},
		{DuckDB, "TIMESTAMP WITH TIME ZONE", "TIMESTAMPTZ"}, {DuckDB, "BIGINT", "BIGINT"},
	} {
		if got := CanonicalType(c.e, c.typ); got != c.want {
			t.Errorf("%s %q: %q, want %q", c.e, c.typ, got, c.want)
		}
	}
	d := CopyDesign(ClickHouse, []Column{{Name: "a", Type: "Nullable(String)", Nullable: true}}, ClickHouse, "x", "t")
	if d.Columns[0].Type != "String" {
		t.Fatalf("%+v", d.Columns)
	}
	d = CopyDesign(MySQL, []Column{{Name: "id", Type: "int", PrimaryKey: true}, {Name: "n", Type: "decimal(9,2)", Nullable: true}}, Postgres, "public", "t")
	if d.Columns[0].Type != "bigint" || !d.Columns[0].PrimaryKey || d.Columns[1].Type != "numeric(9,2)" || !d.Columns[1].Nullable {
		t.Fatalf("%+v", d.Columns)
	}
}
