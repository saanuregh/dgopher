package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	secrets "dgopher/internal/redact"
)

// A user is made with its password quoted, which the audit log's
// redaction hides; a name typed plain is made in upper case, as Oracle
// keeps it, so that it logs in whatever its case.
func TestOracleAccountSQL(t *testing.T) {
	d := oracleDialect{}
	create, err := CreateAccountSQL(d, NewAccount{Account: Account{Name: "ada"}, Password: "it's-Secret-1"})
	if err != nil || create != `CREATE USER "ADA" IDENTIFIED BY "it's-Secret-1"` {
		t.Fatalf("create: %q %v", create, err)
	}
	if strings.Contains(secrets.Secrets(create), "Secret") {
		t.Fatalf("redacted: %s", secrets.Secrets(create))
	}
	if got, _ := CreateAccountSQL(d, NewAccount{Account: Account{Name: "Mixed Case"}, Password: "x"}); got != `CREATE USER "Mixed Case" IDENTIFIED BY "x"` {
		t.Errorf("a name that is not plain: %q", got)
	}
	if _, err := CreateAccountSQL(d, NewAccount{Account: Account{Name: "ada"}, Password: `a"b`}); err == nil {
		t.Error("a password with a double quote")
	}
	if got, _ := CreateAccountSQL(d, NewAccount{Account: Account{Name: "readers", Role: true}}); got != `CREATE ROLE "READERS"` {
		t.Errorf("role: %q", got)
	}
	if got, err := PasswordSQL(d, Account{Name: "ADA"}, "Pw-1"); err != nil || got != `ALTER USER "ADA" IDENTIFIED BY "Pw-1"` {
		t.Errorf("password: %q %v", got, err)
	}
	if _, err := PasswordSQL(d, Account{Name: "R", Role: true}, "x"); err == nil {
		t.Error("a role's password changed")
	}
	if got := DropAccountSQL(d, Account{Name: "ADA"}); !slices.Equal(got, []string{`DROP USER "ADA"`}) {
		t.Errorf("drop: %q", got)
	}
	for _, c := range []struct {
		privs  []string
		table  string
		want   []string
		failed bool
	}{
		{[]string{"SELECT", "INSERT"}, "orders", []string{`GRANT SELECT, INSERT ON "S"."orders" TO "ADA"`}, false},
		{[]string{"SELECT", "UPDATE"}, "", []string{`GRANT SELECT ANY TABLE, UPDATE ANY TABLE ON SCHEMA "S" TO "ADA"`}, false},
		{[]string{"REFERENCES"}, "", nil, true},
	} {
		got, err := GrantSQL(d, c.privs, "S", c.table, Account{Name: "ADA"})
		if (err != nil) != c.failed || !slices.Equal(got, c.want) {
			t.Errorf("%v on %q: %q %v", c.privs, c.table, got, err)
		}
	}
	if got := GrantRoleSQL(d, Account{Name: "CONNECT", Role: true}, Account{Name: "ADA"}); got != `GRANT "CONNECT" TO "ADA"` {
		t.Errorf("role grant: %q", got)
	}
}

// The activity pages cancel a session's query, which keeps the session,
// and end a session, by its SID and serial number.
func TestOracleActivityPages(t *testing.T) {
	pages := Oracle.ActivityPages()
	names := make([]string, len(pages))
	for i, p := range pages {
		names[i] = p.Name
		if !strings.Contains(p.Hint, "SELECT_CATALOG_ROLE") {
			t.Errorf("%s: hint %q", p.Name, p.Hint)
		}
	}
	if !slices.Equal(names, []string{"Sessions", "Locks", "Statistics"}) {
		t.Fatalf("pages %q", names)
	}
	row := []any{"57,1234"}
	if got := pages[0].Stop(row); got != "ALTER SYSTEM CANCEL SQL '57,1234'" {
		t.Errorf("stop: %q", got)
	}
	if got := pages[0].Kill(row); got != "ALTER SYSTEM KILL SESSION '57,1234' IMMEDIATE" {
		t.Errorf("kill: %q", got)
	}
	if pages[2].Kill != nil || pages[2].Live {
		t.Error("statistics end sessions, or read live")
	}
	var got []string
	for _, m := range (oracleEngine{}).MaintenanceCommands(`"S"."it's"`) {
		got = append(got, m.SQL)
	}
	if want := []string{`CALL DBMS_STATS.GATHER_TABLE_STATS('"S"', '"it''s"')`, `ALTER TABLE "S"."it's" MOVE UPDATE INDEXES`,
		`ALTER TABLE "S"."it's" SHRINK SPACE CASCADE`, `ANALYZE TABLE "S"."it's" VALIDATE STRUCTURE CASCADE ONLINE`}; !slices.Equal(got, want) {
		t.Fatalf("maintenance %q", got)
	}
}

// oracleSessionID is a session's 'sid,serial#', as the activity pages
// name it, read by admin, who may read V$SESSION.
func oracleSessionID(t *testing.T, admin, s *Session) string {
	t.Helper()
	sid, _ := queryOne(t, s, `SELECT SYS_CONTEXT('USERENV', 'SID') FROM dual`)
	row, _ := queryOne(t, admin, `SELECT sid || ',' || serial# FROM v$session WHERE sid = `+Display(sid[0]))
	return Display(row[0])
}

// Every activity page reads, as SYSTEM, and lists another session with
// its lock; Stop cancels that session's query, keeping the session and
// its transaction, and Kill ends it. Every maintenance command runs.
func TestOracleActivityAndMaintenance(t *testing.T) {
	d := openOracle(t, oracleSystemConfig)
	ctx := context.Background()
	mustExec(t, d, `DROP TABLE IF EXISTS "DGOPHER"."it_activity"`, `CREATE TABLE "DGOPHER"."it_activity" ("id" NUMBER PRIMARY KEY, "v" NUMBER)`,
		`CREATE INDEX "DGOPHER"."it_activity_v" ON "DGOPHER"."it_activity" ("v")`, `INSERT INTO "DGOPHER"."it_activity" VALUES (1, 1)`)
	t.Cleanup(func() { mustExec(t, d, `DROP TABLE IF EXISTS "DGOPHER"."it_activity"`) })
	s, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	other, err := openOracleAgain(t, oracleConfig).Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(ctx, `UPDATE "DGOPHER"."it_activity" SET "v" = 2`); err != nil || other.Tx() != TxOpen {
		t.Fatalf("the other session's transaction: %v %v", err, other.Tx())
	}
	id := oracleSessionID(t, s, other)
	pages := Oracle.ActivityPages()
	for _, p := range pages {
		c, err := s.Query(ctx, p.Query)
		if err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		rows, err := c.Fetch(1000)
		if err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		if p.Name != "Statistics" && !slices.ContainsFunc(rows, func(r []any) bool { return Display(r[0]) == id }) {
			t.Fatalf("%s: session %s not among %v", p.Name, id, rows)
		}
	}

	// A query of the other session, cancelled from this one.
	done := make(chan error, 1)
	go func() {
		_, err := other.Exec(ctx, "BEGIN DBMS_SESSION.SLEEP(20); END;")
		done <- err
	}()
	time.Sleep(time.Second)
	if _, err := s.Exec(ctx, pages[0].Stop([]any{id})); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "ORA-01013") {
			t.Fatalf("the cancelled query: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the query was not cancelled")
	}
	if row, _ := queryOne(t, other, `SELECT "v" FROM "DGOPHER"."it_activity"`); Display(row[0]) != "2" || other.Tx() != TxOpen {
		t.Fatalf("after the cancel: %v, %v", row, other.Tx())
	}
	if _, err := s.Exec(ctx, pages[0].Kill([]any{id})); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(ctx, "SELECT 1 FROM dual"); err == nil {
		t.Fatal("the ended session still runs")
	}
	mustExec(t, d, `ALTER TABLE "DGOPHER"."it_activity" ENABLE ROW MOVEMENT`)
	for _, m := range (oracleEngine{}).MaintenanceCommands(QualifiedName(d.Dialect, "DGOPHER", "it_activity")) {
		if _, err := s.Exec(ctx, m.SQL); err != nil {
			t.Fatalf("%s: %v", m.Label, err)
		}
	}
}

// A user without SELECT_CATALOG_ROLE sees the users ALL_USERS lists, and
// is told what the activity pages need; a grant on every table of a
// schema reaches a table made after it (23ai).
func TestOracleAccountsPrivileges(t *testing.T) {
	user := openOracle(t, oracleConfig)
	ctx := context.Background()
	accounts, err := ListAccounts(ctx, user.Dialect, user.SQL)
	if err != nil || !slices.ContainsFunc(accounts, func(a Account) bool { return a.Name == "DGOPHER" }) {
		t.Fatalf("accounts without the catalog: %+v %v", accounts, err)
	}
	if _, err := user.SQL.QueryContext(ctx, Oracle.ActivityPages()[0].Query); err == nil || !strings.Contains(DescribeError(err).Hint, "SELECT_CATALOG_ROLE") {
		t.Fatalf("activity without the catalog: %v", err)
	}

	d := openOracleAgain(t, oracleSystemConfig)
	grace := Account{Name: "DGOPHER_IT_GRACE"}
	cleanup := func() {
		for _, q := range append(DropAccountSQL(d.Dialect, grace), `DROP TABLE IF EXISTS "DGOPHER"."it_schema_grant"`) {
			d.SQL.ExecContext(ctx, q)
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	create, err := CreateAccountSQL(d.Dialect, NewAccount{Account: Account{Name: "dgopher_it_grace"}, Password: "Correct-Horse-1"})
	if err != nil {
		t.Fatal(err)
	}
	grants, err := GrantSQL(d.Dialect, []string{"SELECT"}, "DGOPHER", "", grace)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, append([]string{create, GrantRoleSQL(d.Dialect, Account{Name: "CONNECT", Role: true}, grace)}, grants...)...)
	mustExec(t, d, `CREATE TABLE "DGOPHER"."it_schema_grant" ("id" NUMBER)`)
	privs, err := AccountPrivileges(ctx, d.Dialect, d.SQL, grace)
	if err != nil {
		t.Fatal(err)
	}
	texts := fmt.Sprint(privs)
	for _, want := range []string{`GRANT SELECT ANY TABLE ON SCHEMA "DGOPHER"`, `GRANT "CONNECT"`} {
		if !strings.Contains(texts, want) {
			t.Fatalf("no %s in %s", want, texts)
		}
	}
	as := oracleConfig
	as.User, as.Password = "dgopher_it_grace", "Correct-Horse-1"
	login := openOracleAgain(t, as)
	var n int
	if err := login.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM "DGOPHER"."it_schema_grant"`).Scan(&n); err != nil {
		t.Fatalf("reading a table made after the grant: %v", err)
	}
	login.Close()
	for _, p := range privs {
		if p.Revoke != "" {
			if _, err := d.SQL.ExecContext(ctx, p.Revoke); err != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("%s: %v", p.Revoke, err)
			}
		}
	}
}
