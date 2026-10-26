package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
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

// A transaction that clashed with another, as TiDB's write conflict at
// COMMIT, is said to be one that may be run again; the app never runs it
// again itself. Other errors, MySQL's deadlock (SQLSTATE 40001) among
// them, keep what they said.
func TestConflictErrorHint(t *testing.T) {
	conflict := fmt.Errorf("commit: %w", &mysql.MySQLError{Number: 9007, Message: "Write conflict, txnStartTS=1, conflictStartTS=2, reason=Optimistic [try again later]"})
	if hint := DescribeError(conflict).Hint; !strings.Contains(hint, "never runs it again") {
		t.Fatalf("described %+v", DescribeError(conflict))
	}
	for _, err := range []error{
		&mysql.MySQLError{Number: 1213, SQLState: [5]byte{'4', '0', '0', '0', '1'}, Message: "Deadlock found"},
		fmt.Errorf("plain"),
	} {
		if got := DescribeError(err).Hint; got != "" {
			t.Errorf("%v: hint %q", err, got)
		}
	}
	if got := DescribeError(&pgconn.PgError{Code: "40001", Hint: "its own"}).Hint; got != "its own" {
		t.Errorf("PostgreSQL's hint replaced by %q", got)
	}
}
