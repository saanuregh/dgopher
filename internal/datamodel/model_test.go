package datamodel

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/db"
)

// open opens a new SQLite file, with statements run.
func open(t *testing.T, stmts ...string) *db.DB {
	t.Helper()
	file := filepath.Join(t.TempDir(), "m.sqlite")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(context.Background(), db.Config{Name: "lite", Engine: db.SQLite, Database: file}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	for _, s := range stmts {
		if _, err := d.SQL.Exec(s); err != nil {
			t.Fatal(s, err)
		}
	}
	return d
}

// build reads every table of main into a model.
func build(t *testing.T, d *db.DB) *Model {
	t.Helper()
	tables, notes, err := Build(context.Background(), d, "main", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) > 0 {
		t.Fatalf("notes %q", notes)
	}
	return &Model{Name: "shop", Engine: db.SQLite, Tables: tables}
}

// apply runs a change, failing the test at a statement that fails.
func apply(t *testing.T, d *db.DB, ch db.SchemaChange) {
	t.Helper()
	ctx := context.Background()
	sess, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Apply(ctx, ch, func(string, int64, time.Duration, error) {}); err != nil {
		t.Fatalf("%v\n%s", err, ch.Text())
	}
}

// unchanged fails the test when two models differ.
func unchanged(t *testing.T, a, b *Model) {
	t.Helper()
	diffs, err := Compare(a, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diffs {
		if d.State != Same {
			t.Fatalf("%s differs (%d): %+v", d.Name, d.State, d.Changes)
		}
	}
}

var shop = []string{
	`CREATE TABLE customers (id INTEGER PRIMARY KEY AUTOINCREMENT, email TEXT NOT NULL UNIQUE, name TEXT DEFAULT 'none')`,
	`CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER REFERENCES customers (id) ON DELETE CASCADE,
		total NUMERIC NOT NULL CHECK (total >= 0))`,
	`CREATE INDEX orders_customer ON orders (customer_id)`,
	`CREATE INDEX orders_big ON orders (total) WHERE total > 100`,
	// A cycle of keys.
	`CREATE TABLE a (id INTEGER PRIMARY KEY, b_id INTEGER REFERENCES b (id))`,
	`CREATE TABLE b (id INTEGER PRIMARY KEY, a_id INTEGER REFERENCES a (id))`,
}

// A model of a schema, saved and read again, makes the schema again on a
// new database: each table, key, check and index, a partial one included.
func TestScriptMakesTheSchemaAgain(t *testing.T) {
	m := build(t, open(t, shop...))
	path := filepath.Join(t.TempDir(), "shop.json")
	if _, err := m.Save(path, nil); err != nil {
		t.Fatal(err)
	}
	read, data, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read, m) {
		t.Fatalf("read back\n%+v\nwas\n%+v", read, m)
	}
	if strings.Contains(string(data), `"Was"`) || strings.Contains(string(data), `"read"`) {
		t.Fatalf("the file keeps what only changes a table:\n%s", data)
	}
	if _, err := m.Save(path, nil); err == nil {
		t.Fatal("a save over a file not read")
	}

	ch, notes := Script(m, db.SQLite, "main")
	if len(notes) > 0 {
		t.Fatalf("notes %q", notes)
	}
	text := ch.Text()
	if strings.Index(text, `CREATE TABLE "main"."customers"`) > strings.Index(text, `CREATE TABLE "main"."orders"`) {
		t.Fatalf("orders made before what it points at:\n%s", text)
	}
	again := open(t)
	apply(t, again, ch)
	unchanged(t, m, build(t, again))
}

// A migration makes a database's schema like a model: a table added, a
// column added and one changed, an index replaced; what the database has
// beyond the model is kept, unless asked.
func TestMigration(t *testing.T) {
	d := open(t, shop...)
	m := build(t, d)
	orders := slices.IndexFunc(m.Tables, func(t db.TableDesign) bool { return t.Name == "orders" })
	o := &m.Tables[orders]
	// SQLite names keys itself: the key is known by what it points at.
	o.ForeignKeys[0].Name = "orders_customer_fk"
	o.Columns = append(o.Columns, db.ColumnDesign{Name: "placed", Type: "TEXT", Nullable: true})
	o.Columns[2].Default = "0"
	o.Indexes = slices.DeleteFunc(o.Indexes, func(ix db.IndexDesign) bool { return ix.Name == "orders_customer" })
	o.Indexes = append(o.Indexes, db.IndexDesign{Name: "orders_placed", Columns: []string{"placed"}})
	m.Tables = append(m.Tables, db.TableDesign{Schema: "main", Name: "items", Columns: []db.ColumnDesign{
		{Name: "id", Type: "INTEGER", PrimaryKey: true},
		{Name: "order_id", Type: "INTEGER", Nullable: true},
	}, ForeignKeys: []db.ForeignKeyDesign{{Name: "fk_order_id", Columns: []string{"order_id"}, RefSchema: "main", RefTable: "orders", RefColumns: []string{"id"}}}})
	m.Tables = slices.DeleteFunc(m.Tables, func(t db.TableDesign) bool { return t.Name == "a" || t.Name == "b" })
	if _, err := d.SQL.Exec(`INSERT INTO customers (email) VALUES ('ada@example.com')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SQL.Exec(`INSERT INTO orders (customer_id, total) VALUES (1, 5)`); err != nil {
		t.Fatal(err)
	}

	diffs, err := Compare(m, build(t, d))
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]State{}
	for _, d := range diffs {
		states[d.Name] = d.State
	}
	if want := map[string]State{"customers": Same, "orders": Changed, "items": OnlyInA, "a": OnlyInB, "b": OnlyInB}; !reflect.DeepEqual(states, want) {
		t.Fatalf("states %v", states)
	}

	ch, notes, err := Migration(m, build(t, d), "main", false)
	if err != nil || len(notes) > 0 {
		t.Fatal(err, notes)
	}
	apply(t, d, ch)
	after := build(t, d)
	diffs, _ = Compare(m, after)
	// What the database has beyond the model stays: the tables a and b,
	// and the index of orders the model lacks.
	for _, df := range diffs {
		kept := df.Name == "orders" && len(df.Changes) == 1 && df.Changes[0].Name == "orders_customer" && df.Changes[0].A == ""
		if want := map[string]State{"a": OnlyInB, "b": OnlyInB}[df.Name]; df.State != want && !kept {
			t.Fatalf("after the migration, %s is %d: %+v", df.Name, df.State, df.Changes)
		}
	}
	var total float64
	if err := d.SQL.QueryRow(`SELECT total FROM orders`).Scan(&total); err != nil || total != 5 {
		t.Fatalf("the rows of orders: %v %v", total, err)
	}

	ch, _, err = Migration(m, after, "main", true)
	if err != nil {
		t.Fatal(err)
	}
	apply(t, d, ch)
	unchanged(t, m, build(t, d))
}

// Another engine's script types the columns as that engine's, sizes kept,
// and says what it leaves out.
func TestScriptForAnotherEngine(t *testing.T) {
	m := &Model{Name: "shop", Engine: db.Postgres, Tables: []db.TableDesign{{
		Schema: "public", Name: "orders",
		Columns: []db.ColumnDesign{
			{Name: "id", Type: "integer", PrimaryKey: true, Default: "nextval('orders_id_seq'::regclass)"},
			{Name: "code", Type: "character varying(12)"},
			{Name: "total", Type: "numeric(10,2)", Default: "'0'::numeric"},
			{Name: "paid", Type: "boolean", Default: "false"},
			{Name: "placed", Type: "timestamp(3) with time zone", Nullable: true, Default: "now()"},
		},
		Indexes: []db.IndexDesign{{Name: "orders_lower", Columns: []string{"lower((code)::text)"}, Definition: "CREATE INDEX orders_lower ON public.orders USING btree (lower((code)::text))"},
			{Name: "orders_code", Columns: []string{"code"}, Unique: true, Definition: "CREATE UNIQUE INDEX orders_code ON public.orders USING btree (code)"}},
		Checks: []db.CheckDesign{{Name: "orders_total", Expression: "(total >= (0)::numeric)"}},
	}}}
	ch, notes := Script(m, db.MySQL, "shop")
	want := "CREATE TABLE `shop`.`orders` (\n" +
		"  `id` INT NOT NULL AUTO_INCREMENT,\n" +
		"  `code` VARCHAR(12) NOT NULL,\n" +
		"  `total` DECIMAL(10,2) NOT NULL DEFAULT '0',\n" +
		"  `paid` BOOLEAN NOT NULL DEFAULT false,\n" +
		"  `placed` DATETIME(6),\n" +
		"  PRIMARY KEY (`id`)\n" +
		");\n" +
		"CREATE UNIQUE INDEX `orders_code` ON `shop`.`orders` (`code`);"
	if got := ch.Text(); got != want {
		t.Fatalf("wrote\n%s\nwant\n%s", got, want)
	}
	for _, n := range []string{"identity", "orders_total", "orders_lower"} {
		if !slices.ContainsFunc(notes, func(s string) bool { return strings.Contains(s, n) }) {
			t.Errorf("no note of %s in %q", n, notes)
		}
	}
}

func TestConvertType(t *testing.T) {
	for _, c := range []struct {
		from, to  db.Engine
		typ, want string
	}{
		{db.Postgres, db.MySQL, "character varying(40)", "VARCHAR(40)"},
		{db.Postgres, db.MySQL, "character varying", "VARCHAR(255)"},
		{db.Postgres, db.ClickHouse, "numeric(12,4)", "Decimal(12,4)"},
		{db.Postgres, db.DuckDB, "timestamp without time zone", "TIMESTAMP"},
		{db.MySQL, db.Postgres, "tinyint(1)", "boolean"},
		{db.MySQL, db.Postgres, "int unsigned", "bigint"},
		{db.MySQL, db.Postgres, "bigint unsigned", "numeric(20,0)"},
		{db.MySQL, db.Postgres, "datetime(3)", "timestamp"},
		{db.SQLite, db.Postgres, "INTEGER", "bigint"},
		{db.ClickHouse, db.Postgres, "LowCardinality(Nullable(String))", "text"},
		{db.ClickHouse, db.Postgres, "DateTime64(3, 'UTC')", "timestamptz"},
		{db.DuckDB, db.MySQL, "DECIMAL(18,3)", "DECIMAL(18,3)"},
		{db.Postgres, db.MySQL, "integer[]", "LONGTEXT"},
	} {
		if got := convertType(c.from, c.to, c.typ); got != c.want {
			t.Errorf("%s %s as %s: %s, want %s", c.from, c.typ, c.to, got, c.want)
		}
	}
}

// DuckDB names keys itself, so a model's key of another name is the
// database's when it points alike: a migration keeps it, and changes the
// rest of its table, which a key dropped would stop on DuckDB.
func TestMigrationKeepsKeysNamedOtherwise(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, db.Config{Name: "duck", Engine: db.DuckDB, Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, s := range []string{
		`CREATE TABLE customers (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER REFERENCES customers (id))`,
	} {
		if _, err := d.SQL.Exec(s); err != nil {
			t.Fatal(s, err)
		}
	}
	read := func() *Model {
		tables, _, err := Build(ctx, d, "main", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return &Model{Name: "shop", Engine: db.DuckDB, Tables: tables}
	}
	m := read()
	o := &m.Tables[slices.IndexFunc(m.Tables, func(t db.TableDesign) bool { return t.Name == "orders" })]
	o.ForeignKeys[0].Name = "orders_customer"
	o.Columns = append(o.Columns, db.ColumnDesign{Name: "note", Type: "VARCHAR", Nullable: true})
	ch, notes, err := Migration(m, read(), "main", false)
	if err != nil || len(notes) > 0 {
		t.Fatal(err, notes)
	}
	apply(t, d, ch)
	unchanged(t, m, read())
}

// What the database has beyond the model stays unless asked: its primary
// key too, when the model's table has none; and SQLite's unique
// constraints are told apart by their columns, which SQLite names them
// by the places of.
func TestTargetKeeps(t *testing.T) {
	unique := func(col string) db.IndexDesign {
		return db.IndexDesign{Name: "sqlite_autoindex_t_1", Columns: []string{col}, Unique: true, Constraint: true}
	}
	a := db.TableDesign{Schema: "main", Name: "t", Columns: []db.ColumnDesign{{Name: "id", Type: "INTEGER"}, {Name: "x", Type: "TEXT", Nullable: true}},
		Indexes: []db.IndexDesign{unique("x")}}
	b := db.TableDesign{Schema: "main", Name: "t", Columns: []db.ColumnDesign{{Name: "id", Type: "INTEGER", PrimaryKey: true}, {Name: "y", Type: "TEXT"}},
		Indexes: []db.IndexDesign{unique("y")}}
	kept := target(db.SQLite, a, b, false)
	if !kept.Columns[0].PrimaryKey || len(kept.Columns) != 3 || len(kept.Indexes) != 2 {
		t.Fatalf("kept %+v", kept)
	}
	if dropped := target(db.SQLite, a, b, true); dropped.Columns[0].PrimaryKey || len(dropped.Columns) != 2 || len(dropped.Indexes) != 1 {
		t.Fatalf("dropped %+v", dropped)
	}
	if changes := diffTable(db.SQLite, a, b); !slices.ContainsFunc(changes, func(c Change) bool { return c.What == "index" && c.B == "" }) {
		t.Fatalf("changes %+v", changes)
	}
}

// Moved to MySQL, a key of text is VARCHAR, and names other engines keep
// per table or by columns are made apart.
func TestScriptNamesAndKeys(t *testing.T) {
	m := &Model{Name: "app", Engine: db.SQLite, Tables: []db.TableDesign{
		{Schema: "main", Name: "orders", Columns: []db.ColumnDesign{{Name: "id", Type: "INTEGER", PrimaryKey: true}, {Name: "customer_id", Type: "INTEGER", Nullable: true}},
			ForeignKeys: []db.ForeignKeyDesign{{Name: "fk_customer_id", Columns: []string{"customer_id"}, RefSchema: "main", RefTable: "users", RefColumns: []string{"email"}}},
			Indexes:     []db.IndexDesign{{Name: "by_customer", Columns: []string{"customer_id"}}}},
		{Schema: "main", Name: "payments", Columns: []db.ColumnDesign{{Name: "id", Type: "INTEGER", PrimaryKey: true}, {Name: "customer_id", Type: "INTEGER", Nullable: true}},
			ForeignKeys: []db.ForeignKeyDesign{{Name: "fk_customer_id", Columns: []string{"customer_id"}, RefSchema: "main", RefTable: "users", RefColumns: []string{"email"}}},
			Indexes:     []db.IndexDesign{{Name: "by_customer", Columns: []string{"customer_id"}}}},
		{Schema: "main", Name: "users", Columns: []db.ColumnDesign{{Name: "email", Type: "TEXT", PrimaryKey: true}, {Name: "joined", Type: "TEXT", Default: "datetime('now')"}}},
	}}
	ch, notes := Script(m, db.MySQL, "app")
	text := ch.Text()
	for _, want := range []string{"`email` VARCHAR(255) NOT NULL", "CREATE INDEX `payments_by_customer`", "FOREIGN KEY (`customer_id`)"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %s in\n%s", want, text)
		}
	}
	if strings.Contains(text, "fk_customer_id") || strings.Contains(text, "datetime(") {
		t.Errorf("wrote\n%s", text)
	}
	for _, want := range []string{"VARCHAR(255)", "made apart", "datetime('now')"} {
		if !slices.ContainsFunc(notes, func(s string) bool { return strings.Contains(s, want) }) {
			t.Errorf("no note of %s in %q", want, notes)
		}
	}
}

// A table whose change the engine cannot make is left whole: no key of
// it dropped, none added.
func TestMigrationLeavesTablesItCannotChange(t *testing.T) {
	z := db.TableDesign{Schema: "public", Name: "z", Columns: []db.ColumnDesign{{Name: "id", Type: "integer", PrimaryKey: true}}}
	a := &Model{Name: "a", Engine: db.Postgres, Tables: []db.TableDesign{z, {Schema: "public", Name: "x",
		Columns:     []db.ColumnDesign{{Name: "id", Type: "integer", PrimaryKey: true, AutoIncrement: true}, {Name: "y", Type: "integer", Nullable: true}},
		ForeignKeys: []db.ForeignKeyDesign{{Name: "x_y", Columns: []string{"y"}, RefSchema: "public", RefTable: "z", RefColumns: []string{"id"}}}}}}
	b := &Model{Name: "b", Engine: db.Postgres, Tables: []db.TableDesign{z, {Schema: "public", Name: "x", PrimaryKeyName: "x_pkey",
		Columns:     []db.ColumnDesign{{Name: "id", Type: "integer", PrimaryKey: true}},
		ForeignKeys: []db.ForeignKeyDesign{{Name: "x_old", Columns: []string{"id"}, RefSchema: "public", RefTable: "z", RefColumns: []string{"id"}}}}}}
	ch, notes, err := Migration(a, b, "public", true)
	if err != nil || len(ch.Steps) != 0 || len(notes) != 1 || !strings.Contains(notes[0], "x is not changed") {
		t.Fatalf("%v %q\n%s", err, notes, ch.Text())
	}
}
