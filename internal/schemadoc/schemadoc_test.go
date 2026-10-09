package schemadoc

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"
)

func TestScriptOrder(t *testing.T) {
	s := &Schema{Engine: db.Postgres,
		Tables: []Table{
			{Object: db.Object{Name: "order_lines", Kind: db.KindTable}, Definition: `CREATE TABLE order_lines (order_id int REFERENCES "Orders" (id))`},
			{Object: db.Object{Name: "recent", Kind: db.KindView}, Definition: "CREATE VIEW recent AS SELECT * FROM open_orders\n"},
			{Object: db.Object{Name: "Orders", Kind: db.KindTable}, Definition: `CREATE TABLE "Orders" (id int PRIMARY KEY, status status)`},
			{Object: db.Object{Name: "open_orders", Kind: db.KindView}, Definition: `CREATE VIEW open_orders AS SELECT * FROM "Orders"`},
			{Object: db.Object{Name: "a", Kind: db.KindTable}, Definition: "CREATE TABLE a (b_id int REFERENCES b)"},
			{Object: db.Object{Name: "b", Kind: db.KindTable}, Definition: "CREATE TABLE b (a_id int REFERENCES a)"},
			{Object: db.Object{Name: "gone", Kind: db.KindTable}, Err: "permission denied\nfor table gone"},
		},
		Items: []Item{
			{Item: db.Item{Name: "touch", Kind: db.ItemTrigger, Table: "Orders"}, Definition: "CREATE TRIGGER touch BEFORE UPDATE ON \"Orders\" EXECUTE FUNCTION stamp()"},
			{Item: db.Item{Name: "stamp", Kind: db.ItemFunction}, Definition: "CREATE FUNCTION stamp() RETURNS trigger AS $$ BEGIN RETURN NEW; END $$ LANGUAGE plpgsql\n"},
			{Item: db.Item{Name: "status", Kind: db.ItemType, Detail: "enum"}, Definition: "CREATE TYPE status AS ENUM ('open');"},
		},
	}
	got := Script(s)
	var firsts []string
	for _, stmt := range strings.Split(got, "\n\n") {
		firsts = append(firsts, strings.SplitN(stmt, "\n", 2)[0])
	}
	want := []string{
		"SET check_function_bodies = false;",
		"CREATE TYPE status AS ENUM ('open');",
		"CREATE FUNCTION stamp() RETURNS trigger AS $$ BEGIN RETURN NEW; END $$ LANGUAGE plpgsql;",
		`CREATE TABLE "Orders" (id int PRIMARY KEY, status status);`,
		"-- gone: permission denied",
		`CREATE TABLE order_lines (order_id int REFERENCES "Orders" (id));`,
		// a and b name each other: the first goes first.
		"CREATE TABLE a (b_id int REFERENCES b);",
		"CREATE TABLE b (a_id int REFERENCES a);",
		`CREATE VIEW open_orders AS SELECT * FROM "Orders";`,
		"CREATE VIEW recent AS SELECT * FROM open_orders;",
		`CREATE TRIGGER touch BEFORE UPDATE ON "Orders" EXECUTE FUNCTION stamp();`,
	}
	if !slices.Equal(firsts, want) {
		t.Fatalf("script:\n%s", got)
	}
	if !strings.Contains(got, "-- gone: permission denied\n-- for table gone") {
		t.Fatalf("a failed object:\n%s", got)
	}
	if Script(&Schema{Engine: db.MySQL}) != "" {
		t.Fatal("an empty schema has a script")
	}
}

func TestMarkdownEscapes(t *testing.T) {
	s := &Schema{Engine: db.Postgres, Tables: []Table{{
		Object:     db.Object{Name: "a|b_c", Kind: db.KindTable, Rows: 1234567, Comment: "Holds *all*\nthe <rows>"},
		Columns:    []db.Column{{Name: "id", Type: "int", PrimaryKey: true}, {Name: "x", Type: "text", Nullable: true, HasDefault: true, Default: "'a|b'::text"}},
		Definition: "CREATE TABLE x (\n```\n)",
	}}}
	md := Markdown(s, "public · Test")
	for _, want := range []string{
		"# public · Test\n",
		`[a\|b\_c](#table-a-b_c)`,
		`*about 1,234,567 rows*`,
		`Holds \*all\* the &lt;rows&gt;`,
		"| id | `int` |  |  | PK |  |",
		"| x | `text` | yes | `'a\\|b'::text` |  |  |",
		"````sql\nCREATE TABLE x (\n```\n)\n````",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("no %q in\n%s", want, md)
		}
	}
	page, err := HTML(s, "public · <Test>")
	if err != nil || !strings.Contains(page, "<title>public · &lt;Test&gt;</title>") || !strings.Contains(page, `id="table-a-b_c"`) {
		t.Fatalf("html %v:\n%s", err, page)
	}
}

// The script of a SQLite database, run on an empty one, makes the same
// objects.
func TestScriptRecreatesSQLite(t *testing.T) {
	ctx := context.Background()
	open := func(name string) *db.DB {
		path := filepath.Join(t.TempDir(), name)
		os.WriteFile(path, nil, 0o600)
		d, err := db.Open(ctx, db.Config{Name: name, Engine: db.SQLite, Database: path}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		return d
	}
	from := open("from.sqlite")
	for _, q := range []string{
		`CREATE VIEW big AS SELECT * FROM lines WHERE qty > 10`,
		`CREATE TABLE lines (id INTEGER PRIMARY KEY, order_id INTEGER REFERENCES orders (id), qty INTEGER)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, note TEXT)`,
		`CREATE INDEX lines_order ON lines (order_id)`,
		`CREATE TRIGGER no_negative BEFORE INSERT ON lines BEGIN SELECT RAISE(ABORT, 'negative'); END`,
	} {
		if _, err := from.SQL.ExecContext(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	objs, err := from.Dialect.Objects(ctx, from.SQL, "main")
	if err != nil {
		t.Fatal(err)
	}
	items, err := from.Dialect.Items(ctx, from.SQL, "main")
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	s, err := Read(ctx, from, objs, items, true, func(done, total int) { calls++ })
	if err != nil || calls != len(objs)+len(items) {
		t.Fatal(calls, err)
	}
	script := Script(s)
	to := open("to.sqlite")
	if _, err := to.SQL.ExecContext(ctx, script); err != nil {
		t.Fatalf("%v:\n%s", err, script)
	}
	again, err := to.Dialect.Objects(ctx, to.SQL, "main")
	if err != nil || len(again) != len(objs) {
		t.Fatalf("objects %+v: %v", again, err)
	}
	var n int
	if err := to.SQL.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name IN ('big', 'lines_order', 'no_negative')`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("%d of the view, index and trigger made: %v\n%s", n, err, script)
	}
	doc := Markdown(s, "main")
	if !strings.Contains(doc, "→ orders.id") || !strings.Contains(doc, "lines (order\\_id) by") {
		t.Fatalf("documentation:\n%s", doc)
	}
}

func TestReadStopsWhenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d, err := db.Open(context.Background(), db.Config{Name: "m", Engine: db.SQLite, Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := Read(ctx, d, []db.Object{{Name: "x", Kind: db.KindTable}}, nil, false, nil); err == nil {
		t.Fatal("a canceled read succeeded")
	}
}

// The script of a PostgreSQL schema, run once the schema is dropped,
// makes its objects again: types and functions before the tables using
// them, triggers after.
func TestScriptRecreatesPostgres(t *testing.T) {
	testutil.Integration(t)
	ctx := context.Background()
	d, err := db.Open(ctx, testutil.PGConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	setup := []string{"DROP SCHEMA IF EXISTS it_doc CASCADE", "CREATE SCHEMA it_doc",
		"CREATE TYPE it_doc.mood AS ENUM ('ok', 'sad')",
		"CREATE FUNCTION it_doc.stamp() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.at := now(); RETURN NEW; END $$",
		"CREATE TABLE it_doc.orders (id serial PRIMARY KEY, mood it_doc.mood, at timestamptz)",
		"CREATE TABLE it_doc.lines (order_id int REFERENCES it_doc.orders, qty int CHECK (qty > 0))",
		"CREATE FUNCTION it_doc.lines_of(o int) RETURNS bigint LANGUAGE sql AS 'SELECT count(*) FROM it_doc.lines WHERE order_id = o'",
		"CREATE VIEW it_doc.busy AS SELECT id, it_doc.lines_of(id) FROM it_doc.orders",
		"CREATE TRIGGER stamp BEFORE INSERT ON it_doc.orders FOR EACH ROW EXECUTE FUNCTION it_doc.stamp()",
		"COMMENT ON TABLE it_doc.orders IS 'What was bought'"}
	for _, q := range setup {
		if _, err := d.SQL.ExecContext(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	defer d.SQL.ExecContext(ctx, "DROP SCHEMA IF EXISTS it_doc CASCADE")
	objs, err := d.Dialect.Objects(ctx, d.SQL, "it_doc")
	if err != nil {
		t.Fatal(err)
	}
	items, err := d.Dialect.Items(ctx, d.SQL, "it_doc")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Read(ctx, d, objs, items, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	script := Script(s)
	for _, q := range setup[:2] {
		d.SQL.ExecContext(ctx, q)
	}
	if _, err := d.SQL.ExecContext(ctx, script); err != nil {
		t.Fatalf("%v:\n%s", err, script)
	}
	again, _ := d.Dialect.Objects(ctx, d.SQL, "it_doc")
	againItems, _ := d.Dialect.Items(ctx, d.SQL, "it_doc")
	if len(again) != len(objs) || len(againItems) != len(items) {
		t.Fatalf("objects %d of %d, items %d of %d:\n%s", len(again), len(objs), len(againItems), len(items), script)
	}
	doc := Markdown(s, "it_doc")
	for _, want := range []string{"What was bought", `lines\_of(o integer)`, "→ orders.id", "*on orders*"} {
		if !strings.Contains(doc, want) {
			t.Errorf("no %q in the documentation:\n%s", want, doc)
		}
	}
}
