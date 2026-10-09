package db

import "testing"

func TestCreateTableSQL(t *testing.T) {
	cols := []NewColumn{{"id", "bigint"}, {"name", "text"}}
	if got, want := CreateTableSQL(DialectOf(Postgres), "shop", "new", cols), "CREATE TABLE \"shop\".\"new\" (\n  \"id\" bigint,\n  \"name\" text\n)"; got != want {
		t.Errorf("postgres:\n%s\nwant\n%s", got, want)
	}
	ch := []NewColumn{{"id", "Int64"}}
	if got, want := CreateTableSQL(DialectOf(ClickHouse), "", "new", ch), "CREATE TABLE `new` (\n  `id` Nullable(Int64)\n) ENGINE = MergeTree ORDER BY tuple()"; got != want {
		t.Errorf("clickhouse:\n%s\nwant\n%s", got, want)
	}
}

func TestColumnType(t *testing.T) {
	for _, c := range []struct {
		e         Engine
		duck, out string
	}{
		{Postgres, "BIGINT", "bigint"},
		{MySQL, "VARCHAR", "LONGTEXT"},
		{SQLite, "DECIMAL(10,2)", "NUMERIC"},
		{SQLite, "DECIMAL(38,10)", "TEXT"}, // past a float's digits
		{ClickHouse, "DECIMAL(38,10)", "Decimal(38,10)"},
		{Postgres, "TIMESTAMP WITH TIME ZONE", "timestamptz"},
		{DuckDB, "JSON", "JSON"},
		{MySQL, "STRUCT(a INTEGER)", "LONGTEXT"},
	} {
		if got := ColumnType(c.e, c.duck); got != c.out {
			t.Errorf("%s %s: %s, want %s", c.e, c.duck, got, c.out)
		}
	}
}

func TestInsertRows(t *testing.T) {
	tg := &EditTarget{Dialect: DialectOf(Postgres), Schema: "s", Table: "t"}
	cols := []Column{{Name: "id", Type: "bigint"}, {Name: "note", Type: "text"}}
	st := tg.InsertRows(cols, [][]any{{Typed("1"), Typed("a")}, {Typed("2"), nil}})
	want := `INSERT INTO "s"."t" ("id", "note") VALUES ($1::text::bigint, $2), ($3::text::bigint, NULL)`
	if st.SQL != want || len(st.Args) != 3 || st.Args[2] != "2" {
		t.Fatalf("%s %v", st.SQL, st.Args)
	}
	if n := InsertBatch(SQLite, 100); n != 327 {
		t.Errorf("batch %d", n)
	}
	if n := InsertBatch(Postgres, 3); n != 1000 {
		t.Errorf("batch %d", n)
	}
}
