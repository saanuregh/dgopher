package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Each engine's error gives the details it has: a code, and where in the
// statement it went wrong.
func TestDescribeError(t *testing.T) {
	type want struct {
		code     bool // a code or SQLSTATE
		position bool // a Position, or a Line
		detail   string
	}
	cases := []struct {
		cfg  Config
		it   bool // needs the integration servers
		sql  string
		want want
	}{
		{Config{Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dgopher", Database: "postgres"}, true,
			"SELECT *\nFROM missing_table_x", want{code: true, position: true}},
		{Config{Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dgopher", Database: "postgres"}, true,
			"SELECT 1/0", want{code: true}},
		{Config{Engine: MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dgopher", Database: "shop"}, true,
			"SELECT *\nFRM x", want{code: true, position: true}},
		{Config{Engine: ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dgopher", Database: "default"}, true,
			"SELECT *\nFRM x", want{code: true, position: true}},
		{Config{Engine: SQLite, Database: ":memory:"}, false, "SELECT * FROM missing_table_x", want{code: true}},
		{Config{Engine: DuckDB, Database: ":memory:"}, false, "SELECT *\nFRM x", want{position: true}},
	}
	for _, tc := range cases {
		t.Run(string(tc.cfg.Engine), func(t *testing.T) {
			if tc.it && os.Getenv("DGOPHER_IT") == "" {
				t.Skip("set DGOPHER_IT=1")
			}
			tc.cfg.Name = "x"
			if tc.cfg.Engine == SQLite {
				tc.cfg.Database = filepath.Join(t.TempDir(), "e.sqlite")
				os.WriteFile(tc.cfg.Database, nil, 0o600)
			}
			ctx := context.Background()
			d, err := Open(ctx, tc.cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			rows, err := d.SQL.QueryContext(ctx, tc.sql)
			if err == nil {
				for rows.Next() {
				}
				err = rows.Err()
				rows.Close()
			}
			if err == nil {
				t.Fatal("no error")
			}
			info := DescribeError(err)
			t.Logf("%+v", info)
			if info.Message == "" || (info.Code != "") != tc.want.code || (info.Position > 0 || info.Line > 0) != tc.want.position {
				t.Fatalf("%q → %+v", err, info)
			}
		})
	}
}
