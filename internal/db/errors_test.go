package db

import (
	"context"
	"errors"
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
		{cockroachdbConfig, true, "SELECT *\nFRM x", want{code: true, position: true}},
		{yugabytedbConfig, true, "SELECT *\nFROM missing_table_x", want{code: true, position: true}},
		{timescaledbConfig, true, "SELECT *\nFROM missing_table_x", want{code: true, position: true}},
		{Config{Engine: SQLite, Database: ":memory:"}, false, "SELECT * FROM missing_table_x", want{code: true}},
		{Config{Engine: DuckDB, Database: ":memory:"}, false, "SELECT *\nFRM x", want{position: true}},
	}
	for _, tc := range cases {
		t.Run(string(tc.cfg.Engine), func(t *testing.T) {
			if tc.it && os.Getenv("DGOPHER_IT") == "" {
				t.Skip("set DGOPHER_IT=1")
			}
			skipUnlessRunning(t, tc.cfg)
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
	// A serialization failure (SQLSTATE 40001) of PostgreSQL's family, as
	// CockroachDB's retry errors, is one too; the server's own hint stays.
	for _, err := range []error{&pgconn.PgError{Code: "40001", Message: "could not serialize access"}, fmt.Errorf("commit: %w", &pgconn.PgError{Code: "40001", Hint: "its own"})} {
		hint := DescribeError(err).Hint
		if !strings.HasPrefix(hint, "The transaction could not be serialized with others and was aborted: roll back and run it again from its start.") {
			t.Errorf("%v: hint %q", err, hint)
		}
		if own := (&pgconn.PgError{}); errors.As(err, &own) && own.Hint != "" && !strings.HasSuffix(hint, "\n"+own.Hint) {
			t.Errorf("the server's hint lost: %q", hint)
		}
	}
	if got := DescribeError(&pgconn.PgError{Code: "40P01", Hint: "its own"}).Hint; got != "its own" {
		t.Errorf("PostgreSQL's hint replaced by %q", got)
	}
}

// CockroachDB gives no position of a syntax error, but shows its
// statement up to the line of the error, a caret under its byte.
func TestDescribeErrorCaret(t *testing.T) {
	for detail, want := range map[string][2]int{
		"source SQL:\nSELEC 1\n^":                           {1, 1},
		"source SQL:\nSELECT 'é',\n 'ü' FRM x\n          ^": {2, 11},
		"source SQL:\nSELECT 1\n\n, 2,\n3 +\n   ^":          {4, 4},
	} {
		info := DescribeError(&pgconn.PgError{Code: "42601", Message: "syntax error", Detail: detail})
		if info.Line != want[0] || info.Column != want[1] || !info.Bytes || info.Position != 0 {
			t.Errorf("%q: %+v, want line %d, column %d in bytes", detail, info, want[0], want[1])
		}
	}
	for _, detail := range []string{"", "source SQL:\nSELECT 1", "Key (id)=(1) already exists.", "source SQL:\nx\n  y"} {
		if info := DescribeError(&pgconn.PgError{Detail: detail, Position: 3}); info.Line != 0 || info.Column != 0 || info.Bytes || info.Position != 3 {
			t.Errorf("%q: %+v", detail, info)
		}
	}
}
