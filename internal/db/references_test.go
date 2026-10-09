package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// ReferencedBy lists the foreign keys of other tables that point at a
// table.
func TestReferencedBy(t *testing.T) {
	type tc struct {
		cfg    Config
		it     bool
		schema string
		setup  []string
		clean  []string
	}
	lite := filepath.Join(t.TempDir(), "r.sqlite")
	os.WriteFile(lite, nil, 0o600)
	cases := []tc{
		{Config{Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dgopher", Database: "postgres"}, true, "refs_t",
			[]string{"DROP SCHEMA IF EXISTS refs_t CASCADE", "CREATE SCHEMA refs_t", "CREATE TABLE refs_t.parent (id int PRIMARY KEY)",
				"CREATE TABLE refs_t.child (id int PRIMARY KEY, parent_id int REFERENCES refs_t.parent (id))"},
			[]string{"DROP SCHEMA refs_t CASCADE"}},
		{Config{Engine: MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dgopher", Database: "shop"}, true, "shop",
			[]string{"DROP TABLE IF EXISTS refs_child", "DROP TABLE IF EXISTS refs_parent", "CREATE TABLE refs_parent (id int PRIMARY KEY)",
				"CREATE TABLE refs_child (id int PRIMARY KEY, parent_id int, FOREIGN KEY (parent_id) REFERENCES refs_parent (id))"},
			[]string{"DROP TABLE refs_child", "DROP TABLE refs_parent"}},
		{Config{Engine: SQLite, Database: lite}, false, "main",
			[]string{"CREATE TABLE parent (id int PRIMARY KEY)", "CREATE TABLE child (id int PRIMARY KEY, parent_id int REFERENCES parent (id))"}, nil},
		{Config{Engine: DuckDB, Database: ":memory:"}, false, "main",
			[]string{"CREATE TABLE parent (id int PRIMARY KEY)", "CREATE TABLE child (id int PRIMARY KEY, parent_id int REFERENCES parent (id))"}, nil},
	}
	for _, c := range cases {
		t.Run(string(c.cfg.Engine), func(t *testing.T) {
			if c.it && os.Getenv("DGOPHER_IT") == "" {
				t.Skip("set DGOPHER_IT=1")
			}
			c.cfg.Name = "r"
			ctx := context.Background()
			d, err := Open(ctx, c.cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			for _, s := range c.setup {
				if _, err := d.SQL.ExecContext(ctx, s); err != nil {
					t.Fatalf("%s: %v", s, err)
				}
			}
			defer func() {
				for _, s := range c.clean {
					d.SQL.ExecContext(ctx, s)
				}
			}()
			parent, child := "parent", "child"
			if c.cfg.Engine == MySQL {
				parent, child = "refs_parent", "refs_child"
			}
			refs, err := d.Dialect.ReferencedBy(ctx, d.SQL, c.schema, parent)
			if err != nil {
				t.Fatal(err)
			}
			if len(refs) != 1 || refs[0].Table != child || refs[0].Schema != c.schema ||
				len(refs[0].Columns) != 1 || refs[0].Columns[0] != "parent_id" || refs[0].RefColumns[0] != "id" {
				t.Fatalf("refs %+v", refs)
			}
			if none, _ := d.Dialect.ReferencedBy(ctx, d.SQL, c.schema, child); len(none) != 0 {
				t.Fatalf("child is referenced by %+v", none)
			}
		})
	}
}
