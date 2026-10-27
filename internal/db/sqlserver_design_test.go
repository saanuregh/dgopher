package db

import (
	"context"
	"testing"
	"time"

	"dgopher/internal/sqltext"
)

// SQL Server limits rows with TOP after SELECT, and DISTINCT, wherever
// the other engines write LIMIT at the end.
func TestSQLServerLimitSQL(t *testing.T) {
	for _, c := range []struct{ query, sep, want string }{
		{"SELECT a FROM t ORDER BY a", " ", "SELECT TOP (50) a FROM t ORDER BY a"},
		{"SELECT DISTINCT a FROM t WHERE a IS NOT NULL", " ", "SELECT DISTINCT TOP (50) a FROM t WHERE a IS NOT NULL"},
		{"SELECT *\nFROM [dbo].[t]", "\n", "SELECT TOP (50) *\nFROM [dbo].[t]"},
	} {
		if got := LimitSQL(SQLServer, c.query, c.sep, 50); got != c.want {
			t.Errorf("%q:\n got  %q\n want %q", c.query, got, c.want)
		}
	}
}

// A table made on SQL Server keeps its comments as MS_Description
// extended properties, added after the table.
func TestSQLServerNewTableChange(t *testing.T) {
	d := sqlserverDialect{}
	ch, err := NewTableChange(d, TableDesign{Schema: "dbo", Name: "t", Comment: "orders", Columns: []ColumnDesign{
		{Name: "id", Type: "bigint", PrimaryKey: true, AutoIncrement: true},
		{Name: "qty", Type: "int", Default: "1"},
		{Name: "note", Type: "nvarchar(50)", Nullable: true, Comment: "it's free"},
	}, Indexes: []IndexDesign{{Name: "t_qty", Columns: []string{"qty"}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `CREATE TABLE [dbo].[t] (
  [id] bigint NOT NULL IDENTITY(1,1),
  [qty] int NOT NULL DEFAULT 1,
  [note] nvarchar(50),
  PRIMARY KEY ([id])
);
CREATE INDEX [t_qty] ON [dbo].[t] ([qty]);
IF EXISTS (SELECT 1 FROM sys.extended_properties WHERE class = 1 AND major_id = OBJECT_ID(N'[dbo].[t]') AND minor_id = 0 AND name = N'MS_Description')
  EXEC sys.sp_dropextendedproperty @name = N'MS_Description', @level0type = N'SCHEMA', @level0name = N'dbo', @level1type = N'TABLE', @level1name = N't';
EXEC sys.sp_addextendedproperty @name = N'MS_Description', @value = N'orders', @level0type = N'SCHEMA', @level0name = N'dbo', @level1type = N'TABLE', @level1name = N't';
EXEC sys.sp_addextendedproperty @name = N'MS_Description', @value = N'it''s free', @level0type = N'SCHEMA', @level0name = N'dbo', @level1type = N'TABLE', @level1name = N't', @level2type = N'COLUMN', @level2name = N'note';`
	if got := ch.Text(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if !ch.Atomic {
		t.Fatal("not atomic")
	}
}

// A table changed on SQL Server: columns renamed with sp_rename, added
// with ADD, retyped with their nullability, defaults dropped by the name
// SQL Server gave them before the type changes and added again after,
// comments added, changed and dropped, the key replaced, the table
// renamed last.
func TestSQLServerAlterTableChange(t *testing.T) {
	d := sqlserverDialect{}
	was := TableDesign{Schema: "dbo", Name: "t", Comment: "orders", PrimaryKeyName: "PK__t__1", Columns: []ColumnDesign{
		{Was: "id", Name: "id", Type: "int", PrimaryKey: true},
		{Was: "qty", Name: "qty", Type: "int", Default: "((1))", Comment: "count"},
		{Was: "note", Name: "note", Type: "nvarchar(50)", Nullable: true, Comment: "free"},
		{Was: "old", Name: "old", Type: "int", Default: "((0))"},
	}}
	now := clone(was)
	now.Name, now.Comment = "orders", ""
	now.Columns[1].Type, now.Columns[1].Comment, now.Columns[1].PrimaryKey = "bigint", "how many", true
	now.Columns[2].Name, now.Columns[2].Default, now.Columns[2].Nullable, now.Columns[2].Comment = "remark", "N'none'", false, ""
	now.Columns = append(now.Columns[:3], ColumnDesign{Name: "placed", Type: "datetime2(7)", Nullable: true, Comment: "when"})
	ch, err := AlterTableChange(d, was, now)
	if err != nil {
		t.Fatal(err)
	}
	want := `ALTER TABLE [dbo].[t] DROP CONSTRAINT [PK__t__1];
EXEC sys.sp_rename N'[dbo].[t].[note]', N'remark', N'COLUMN';
DECLARE @drop nvarchar(max) = (SELECT N'ALTER TABLE [dbo].[t] DROP CONSTRAINT ' + QUOTENAME(d.name) FROM sys.default_constraints d
  WHERE d.parent_object_id = OBJECT_ID(N'[dbo].[t]') AND d.parent_column_id = COLUMNPROPERTY(d.parent_object_id, N'old', 'ColumnId'));
IF @drop IS NOT NULL EXEC sys.sp_executesql @drop;
ALTER TABLE [dbo].[t] DROP COLUMN [old];
DECLARE @drop nvarchar(max) = (SELECT N'ALTER TABLE [dbo].[t] DROP CONSTRAINT ' + QUOTENAME(d.name) FROM sys.default_constraints d
  WHERE d.parent_object_id = OBJECT_ID(N'[dbo].[t]') AND d.parent_column_id = COLUMNPROPERTY(d.parent_object_id, N'qty', 'ColumnId'));
IF @drop IS NOT NULL EXEC sys.sp_executesql @drop;
ALTER TABLE [dbo].[t] ALTER COLUMN [qty] bigint NOT NULL;
ALTER TABLE [dbo].[t] ADD DEFAULT ((1)) FOR [qty];
EXEC sys.sp_updateextendedproperty @name = N'MS_Description', @value = N'how many', @level0type = N'SCHEMA', @level0name = N'dbo', @level1type = N'TABLE', @level1name = N't', @level2type = N'COLUMN', @level2name = N'qty';
ALTER TABLE [dbo].[t] ALTER COLUMN [remark] nvarchar(50) NOT NULL;
ALTER TABLE [dbo].[t] ADD DEFAULT N'none' FOR [remark];
EXEC sys.sp_dropextendedproperty @name = N'MS_Description', @level0type = N'SCHEMA', @level0name = N'dbo', @level1type = N'TABLE', @level1name = N't', @level2type = N'COLUMN', @level2name = N'remark';
ALTER TABLE [dbo].[t] ADD [placed] datetime2(7);
EXEC sys.sp_addextendedproperty @name = N'MS_Description', @value = N'when', @level0type = N'SCHEMA', @level0name = N'dbo', @level1type = N'TABLE', @level1name = N't', @level2type = N'COLUMN', @level2name = N'placed';
ALTER TABLE [dbo].[t] ADD PRIMARY KEY ([id], [qty]);
IF EXISTS (SELECT 1 FROM sys.extended_properties WHERE class = 1 AND major_id = OBJECT_ID(N'[dbo].[t]') AND minor_id = 0 AND name = N'MS_Description')
  EXEC sys.sp_dropextendedproperty @name = N'MS_Description', @level0type = N'SCHEMA', @level0name = N'dbo', @level1type = N'TABLE', @level1name = N't';
EXEC sys.sp_rename N'[dbo].[t]', N'orders';`
	if got := ch.Text(); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	for _, st := range ch.Steps {
		if a := sqltext.ClassifyExecutable(st.SQL, sqltext.TSQL); a.Class != sqltext.Write && a.Class != sqltext.DDL {
			t.Errorf("%s: classified %v", st.SQL, a.Class)
		}
	}
}

// Renaming a table, a view and a column runs sp_rename, which takes the
// new name as it is, unquoted.
func TestSQLServerRename(t *testing.T) {
	d := sqlserverDialect{}
	for _, c := range []struct {
		obj  Object
		want string
	}{
		{Object{Schema: "dbo", Name: "a]b", Kind: KindTable}, "EXEC sys.sp_rename N'[dbo].[a]]b]', N'it''s'"},
		{Object{Schema: "s", Name: "v", Kind: KindView}, "EXEC sys.sp_rename N'[s].[v]', N'it''s'"},
	} {
		got, err := RenameObjectSQL(d, c.obj, "it's")
		if err != nil || got != c.want {
			t.Errorf("%+v: %q %v", c.obj, got, err)
		}
	}
	if got := RenameColumnSQL(d, "dbo", "t", "a", "b"); got != "EXEC sys.sp_rename N'[dbo].[t].[a]', N'b', N'COLUMN'" {
		t.Errorf("column: %q", got)
	}
}

// The keys, checks and indexes of a SQL Server table change.
func TestSQLServerKeysChange(t *testing.T) {
	r := sqlserverEngine{}
	if r.KeysOfMadeTable() != "" || r.FixedKeysNote() != "" || !r.HasComments() {
		t.Fatalf("keys %q, note %q, comments %v", r.KeysOfMadeTable(), r.FixedKeysNote(), r.HasComments())
	}
}

// An upsert on SQL Server is a MERGE of the insert's row on its key,
// held by HOLDLOCK so that two cannot insert one key; without columns to
// update it inserts only a new key. The classifier reads it as a write.
func TestSQLServerUpsert(t *testing.T) {
	r := sqlserverEngine{}
	want := `MERGE INTO [dbo].[t] WITH (HOLDLOCK) AS target
USING (VALUES (:id, :name, :n)) AS source ([id], [name], [n])
ON target.[id] = source.[id]
WHEN MATCHED THEN UPDATE SET [name] = source.[name], [n] = source.[n]
WHEN NOT MATCHED THEN INSERT ([id], [name], [n]) VALUES (source.[id], source.[name], source.[n])`
	got := r.UpsertSQL(Upsert{Table: "[dbo].[t]", Columns: []string{"[id]", "[name]", "[n]"}, Values: []string{":id", ":name", ":n"},
		Keys: []string{"[id]"}, UpdatedColumns: []string{"[name]", "[n]"}})
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if a := sqltext.ClassifyExecutable(got+";", sqltext.TSQL); a.Class != sqltext.Write {
		t.Fatalf("classified %v", a.Class)
	}
	keysOnly := r.UpsertSQL(Upsert{Table: "[t]", Columns: []string{"[a]", "[b]"}, Values: []string{":a", ":b"}, Keys: []string{"[a]", "[b]"}})
	if want := `MERGE INTO [t] WITH (HOLDLOCK) AS target
USING (VALUES (:a, :b)) AS source ([a], [b])
ON target.[a] = source.[a] AND target.[b] = source.[b]
WHEN NOT MATCHED THEN INSERT ([a], [b]) VALUES (source.[a], source.[b])`; keysOnly != want {
		t.Fatalf("keys only:\n%s", keysOnly)
	}
}

// SQL Server refuses min and max of a bit (error 8117), and the count of
// a geography's distinct values: a profile leaves them out.
func TestSQLServerComparable(t *testing.T) {
	r := sqlserverEngine{}
	for typ, want := range map[string]bool{"bit": false, "geography": false, "hierarchyid": true, "int": true, "nvarchar(50)": true} {
		if got := r.Comparable(typ); got != want {
			t.Errorf("%s: %v", typ, got)
		}
	}
}

// A SQL Server table's rows read geography, geometry and hierarchyid
// columns as text, WKT and the /1/2/ path, which edits write back; a
// table without them is read with *.
func TestSQLServerTableSelect(t *testing.T) {
	d := sqlserverDialect{}
	cols := []Column{{Name: "id", Type: "int"}, {Name: "g]", Type: "geography"}, {Name: "shape", Type: "geometry"}, {Name: "node", Type: "hierarchyid"}}
	want := "SELECT [id], [g]]].ToString() AS [g]]], [shape].ToString() AS [shape], [node].ToString() AS [node] FROM [dbo].[t]"
	if got := TableSelectSQL(d, "dbo", "t", cols); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got := TableSelectSQL(d, "dbo", "t", cols[:1]); got != "SELECT * FROM [dbo].[t]" {
		t.Fatalf("plain: %s", got)
	}
}

// A table's grid reads spatial and hierarchyid columns as text, which an
// edit writes back; a rowversion and a computed column count as written
// by the server, a computed column as generated in the table's design.
func TestIntegrationSQLServerTableValues(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d, s := sqlserverSession(t)
	for _, q := range []string{"DROP TABLE IF EXISTS dbo.it_values",
		`CREATE TABLE dbo.it_values (id int PRIMARY KEY, g geography, shape geometry, node hierarchyid, ver rowversion, twice AS id * 2)`,
		`INSERT INTO dbo.it_values (id, g, shape, node) VALUES (1, 'POINT(1 2)', 'LINESTRING(0 0, 1 1 2)', '/1/2/')`} {
		if _, err := s.Exec(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	defer d.SQL.ExecContext(ctx, "DROP TABLE IF EXISTS dbo.it_values")
	cols, err := d.Dialect.Columns(ctx, d.SQL, "dbo", "it_values")
	if err != nil {
		t.Fatal(err)
	}
	read := func() []any {
		t.Helper()
		c, err := s.Query(ctx, TableSelectSQL(d.Dialect, "dbo", "it_values", cols))
		if err != nil {
			t.Fatal(err)
		}
		rows, err := c.Fetch(1)
		if err != nil || len(rows) != 1 {
			t.Fatal(rows, err)
		}
		return rows[0]
	}
	row := read()
	if row[1] != "POINT (1 2)" || row[2] != "LINESTRING (0 0, 1 1 2)" || row[3] != "/1/2/" {
		t.Fatalf("read %#v", row)
	}
	target := &EditTarget{Dialect: d.Dialect, Schema: "dbo", Table: "it_values", Key: []string{"id"}, Columns: cols}
	stmts, err := target.Statements([]Change{{Kind: ChangeUpdate, Key: []any{1}, Values: map[string]any{"g": "POINT (3 4)", "node": Typed("/1/3/"), "shape": "POINT (5 6)"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyEdits(ctx, stmts, func(int, int64, time.Duration, error) {}); err != nil {
		t.Fatal(err)
	}
	if row := read(); row[1] != "POINT (3 4)" || row[2] != "POINT (5 6)" || row[3] != "/1/3/" {
		t.Fatalf("after the edit %#v", row)
	}
	for _, c := range cols {
		if written := c.Name == "ver" || c.Name == "twice"; written != c.HasDefault || written != c.Generated {
			t.Errorf("%s: has a default %v, generated %v", c.Name, c.HasDefault, c.Generated)
		}
	}
	design, err := ReadTableDesign(ctx, d, Object{Schema: "dbo", Name: "it_values", Kind: KindTable})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range design.Columns {
		if (c.Name == "twice") != c.Generated || c.Default != "" {
			t.Errorf("%s: generated %v, default %q", c.Name, c.Generated, c.Default)
		}
	}
}

// T-SQL's synonyms of its types are of their kinds, as a model written
// for another engine names them.
func TestSQLServerTypeSynonyms(t *testing.T) {
	for typ, want := range map[string]TypeKind{"integer": TypeKindInteger, "INTEGER": TypeKindInteger, "dec(10,2)": TypeKindDecimal,
		"double precision": TypeKindDouble, "character(3)": TypeKindChar, "character varying(20)": TypeKindVarchar,
		"national character varying(20)": TypeKindVarchar, "rowversion": TypeKindBlob} {
		if got, _, _ := TypeKindOf(SQLServer, typ); got != want {
			t.Errorf("%s: %q, want %q", typ, got, want)
		}
	}
	if _, size, scale := TypeKindOf(SQLServer, "dec(10,2)"); size != "10" || scale != "2" {
		t.Errorf("dec(10,2): %s,%s", size, scale)
	}
}

// A SQL Server table compares with a SQLite one, alike in values each
// writes its own way, and syncs.
func TestIntegrationSQLServerCompareRows(t *testing.T) {
	integration(t)
	ctx := context.Background()
	ss, _ := sqlserverSession(t)
	mustExec(t, ss, "DROP TABLE IF EXISTS dbo.it_cmp",
		"CREATE TABLE dbo.it_cmp (id int PRIMARY KEY, ok bit, total decimal(10,2), day date, name nvarchar(20), ref uniqueidentifier)",
		"INSERT INTO dbo.it_cmp VALUES (1, 1, 1.50, '2024-03-01', N'pen', '6F9619FF-8B86-D011-B42D-00C04FC964FF'), (2, 0, 3, '2024-03-02', N'ink', NULL), (3, NULL, NULL, NULL, N'pad', NULL)")
	defer ss.SQL.ExecContext(ctx, "DROP TABLE IF EXISTS dbo.it_cmp")
	lite := open(t, fixture{cfg: Config{Name: "lite", Engine: SQLite, Database: ":memory:"}, setup: []string{
		"CREATE TABLE it_cmp (id INTEGER PRIMARY KEY, ok BOOLEAN, total NUMERIC, day DATE, name TEXT, ref TEXT)",
		"INSERT INTO it_cmp VALUES (1, 1, 1.5, '2024-03-01', 'pen', '6f9619ff-8b86-d011-b42d-00c04fc964ff'), (2, 0, 3.00, '2024-03-02', 'quill', NULL), (9, 1, 0, NULL, 'nib', NULL)"}})
	compare := func() *RowComparison {
		t.Helper()
		r, err := CompareRows(ctx, CompareTable{DB: lite, Schema: "main", Table: "it_cmp"}, CompareTable{DB: ss, Schema: "dbo", Table: "it_cmp"}, func(int64) {})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := compare()
	if r.Same != 1 || r.Changed != 1 || r.OnlyInSource != 1 || r.OnlyInTarget != 1 {
		for _, d := range r.Differences {
			t.Logf("%v: %v / %v, changed %v", d.Kind, d.Source, d.Target, d.Changed)
		}
		t.Fatalf("same %d, changed %d, only in source %d, only in target %d", r.Same, r.Changed, r.OnlyInSource, r.OnlyInTarget)
	}
	stmts, err := r.Target.Statements(r.Changes(true, true, true))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := ss.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.ApplyEdits(ctx, stmts, func(int, int64, time.Duration, error) {}); err != nil {
		t.Fatal(err)
	}
	if r := compare(); r.Same != 3 || r.Changed+r.OnlyInSource+r.OnlyInTarget != 0 {
		t.Fatalf("after the sync: same %d, changed %d, only in source %d, only in target %d", r.Same, r.Changed, r.OnlyInSource, r.OnlyInTarget)
	}
}

// A full batch of an import or a copy runs on SQL Server: within the
// parameters a request takes, sp_executesql's own two among them.
func TestIntegrationSQLServerFullInsertBatch(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d, s := sqlserverSession(t)
	var cols []Column
	var row []any
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		cols, row = append(cols, Column{Name: name, Type: "int"}), append(row, len(row))
	}
	mustExec(t, d, "DROP TABLE IF EXISTS dbo.it_batch", "CREATE TABLE dbo.it_batch (a int, b int, c int, d int, e int, f int, g int)")
	defer d.SQL.ExecContext(ctx, "DROP TABLE IF EXISTS dbo.it_batch")
	rows := make([][]any, InsertBatch(SQLServer, len(cols)))
	for i := range rows {
		rows[i] = row
	}
	st := (&EditTarget{Dialect: d.Dialect, Schema: "dbo", Table: "it_batch", Columns: cols}).InsertRows(cols, rows)
	if _, err := s.Exec(ctx, st.SQL, st.Args...); err != nil {
		t.Fatalf("%d rows of %d columns: %v", len(rows), len(cols), err)
	}
}

// A row keyed by a date or time column is edited by the key the grid
// read: datetime's thirds of a millisecond, .123 stored as .1233333 and
// .997 kept, match only compared as datetime, which the driver's
// datetimeoffset parameter is not.
func TestIntegrationSQLServerEditByTimeKey(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d, s := sqlserverSession(t)
	for _, c := range []struct{ typ, value string }{
		{"datetime", "2026-01-02 03:04:05.123"},
		{"datetime", "2026-01-02 03:04:05.997"},
		{"smalldatetime", "2026-01-02 03:04:00"},
		{"datetime2", "2026-01-02 03:04:05.1234567"},
		{"datetime2(3)", "2026-01-02 03:04:05.997"},
		{"date", "2026-01-02"},
		{"time", "03:04:05.1234567"},
		{"datetimeoffset", "2026-01-02 03:04:05.1234567 +05:30"},
	} {
		t.Run(c.typ+" "+c.value, func(t *testing.T) {
			for _, q := range []string{"DROP TABLE IF EXISTS dbo.it_time_key",
				"CREATE TABLE dbo.it_time_key (k " + c.typ + " PRIMARY KEY, v int)",
				"INSERT INTO dbo.it_time_key VALUES ('" + c.value + "', 1)"} {
				if _, err := s.Exec(ctx, q); err != nil {
					t.Fatal(q, err)
				}
			}
			defer d.SQL.ExecContext(ctx, "DROP TABLE IF EXISTS dbo.it_time_key")
			cols, err := d.Dialect.Columns(ctx, d.SQL, "dbo", "it_time_key")
			if err != nil {
				t.Fatal(err)
			}
			cur, err := s.Query(ctx, TableSelectSQL(d.Dialect, "dbo", "it_time_key", cols))
			if err != nil {
				t.Fatal(err)
			}
			rows, err := cur.Fetch(1)
			cur.Close()
			if err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}
			target := &EditTarget{Dialect: d.Dialect, Schema: "dbo", Table: "it_time_key", Key: []string{"k"}, Columns: cols}
			stmts, err := target.Statements([]Change{{Kind: ChangeUpdate, Key: []any{rows[0][0]}, Values: map[string]any{"v": int64(2)}},
				{Kind: ChangeDelete, Key: []any{rows[0][0]}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.ApplyEdits(ctx, stmts, func(int, int64, time.Duration, error) {}); err != nil {
				t.Fatalf("key %#v: %v", rows[0][0], err)
			}
		})
	}
}
