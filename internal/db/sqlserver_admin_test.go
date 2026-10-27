package db

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	secrets "dgopher/internal/redact"
)

// A user is a login of the server with a user of the same name in the
// current database; a role is the database's. Passwords are N'…'
// literals, which the audit log's redaction hides.
func TestSQLServerAccountSQL(t *testing.T) {
	d := sqlserverDialect{}
	ada := Account{Name: "ada"}
	create, err := CreateAccountSQL(d, NewAccount{Account: ada, Password: "it's-Secret-1"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "CREATE LOGIN [ada] WITH PASSWORD = N'it''s-Secret-1';\nCREATE USER [ada] FOR LOGIN [ada]"; create != want {
		t.Fatalf("create: %q", create)
	}
	if strings.Contains(secrets.Secrets(create), "Secret") {
		t.Fatalf("redacted: %s", secrets.Secrets(create))
	}
	role, err := CreateAccountSQL(d, NewAccount{Account: Account{Name: "readers", Role: true}})
	if err != nil || role != "CREATE ROLE [readers]" {
		t.Fatalf("role: %q %v", role, err)
	}
	for _, c := range []struct {
		a    Account
		want string
	}{
		{ada, "ALTER LOGIN [ada] WITH PASSWORD = N'Pw-1'"},
		{Account{Name: "bob", User: "bob", NoLogin: true}, "ALTER USER [bob] WITH PASSWORD = N'Pw-1'"},
	} {
		if got, err := PasswordSQL(d, c.a, "Pw-1"); err != nil || got != c.want {
			t.Errorf("%+v: %q %v", c.a, got, err)
		}
	}
	if _, err := PasswordSQL(d, Account{Name: "readers", Role: true}, "x"); err == nil {
		t.Error("a role's password changed")
	}
	for _, c := range []struct {
		a    Account
		want []string
	}{
		{ada, []string{"DROP USER IF EXISTS [ada]", "DROP LOGIN [ada]"}},
		{Account{Name: "ada", User: "ada_user"}, []string{"DROP USER IF EXISTS [ada_user]", "DROP LOGIN [ada]"}},
		{Account{Name: "sa", User: "dbo"}, []string{"DROP LOGIN [sa]"}},
		{Account{Name: "bob", User: "bob", NoLogin: true}, []string{"DROP USER [bob]"}},
		{Account{Name: "readers", Role: true}, []string{"DROP ROLE [readers]"}},
		{Account{Name: "auditors", Role: true, ServerRole: true}, []string{"DROP SERVER ROLE [auditors]"}},
	} {
		if got := DropAccountSQL(d, c.a); !slices.Equal(got, c.want) {
			t.Errorf("drop %+v: %q", c.a, got)
		}
	}
}

// Privileges are granted to the database's user of a login, which is
// made first when the login has none there; on a table, or on every
// table of a schema.
func TestSQLServerGrantSQL(t *testing.T) {
	d := sqlserverDialect{}
	for _, c := range []struct {
		a      Account
		table  string
		want   []string
		failed bool
	}{
		{Account{Name: "ada", User: "ada_user"}, "orders", []string{"GRANT SELECT, INSERT ON OBJECT::[it].[orders] TO [ada_user]"}, false},
		{Account{Name: "ada"}, "", []string{"IF DATABASE_PRINCIPAL_ID(N'ada') IS NULL CREATE USER [ada] FOR LOGIN [ada]",
			"GRANT SELECT, INSERT ON SCHEMA::[it] TO [ada]"}, false},
		{Account{Name: "readers", Role: true}, "orders", []string{"GRANT SELECT, INSERT ON OBJECT::[it].[orders] TO [readers]"}, false},
		{Account{Name: "sysadmin", Role: true, ServerRole: true}, "orders", nil, true},
	} {
		got, err := GrantSQL(d, []string{"SELECT", "INSERT"}, "it", c.table, c.a)
		if (err != nil) != c.failed || !slices.Equal(got, c.want) {
			t.Errorf("%+v: %q %v", c.a, got, err)
		}
	}
	for _, c := range []struct {
		role, a Account
		want    string
	}{
		{Account{Name: "db_datareader", Role: true}, Account{Name: "ada", User: "ada_user"}, "ALTER ROLE [db_datareader] ADD MEMBER [ada_user]"},
		{Account{Name: "readers", Role: true}, Account{Name: "writers", Role: true}, "ALTER ROLE [readers] ADD MEMBER [writers]"},
		{Account{Name: "sysadmin", Role: true, ServerRole: true}, Account{Name: "ada", User: "ada_user"}, "ALTER SERVER ROLE [sysadmin] ADD MEMBER [ada]"},
	} {
		if got := GrantRoleSQL(d, c.role, c.a); got != c.want {
			t.Errorf("%s to %s: %q", c.a.Name, c.role.Name, got)
		}
	}
	if !(sqlserverEngine{}).ManagesAccounts() {
		t.Fatal("accounts not managed")
	}
}

// SQL Server's activity: sessions, locks and statement statistics, read
// from its dynamic management views; KILL ends a session, and none
// cancels another session's query alone.
func TestSQLServerActivityPages(t *testing.T) {
	pages := SQLServer.ActivityPages()
	names := make([]string, len(pages))
	for i, p := range pages {
		names[i] = p.Name
		if p.Stop != nil {
			t.Errorf("%s cancels a query, which only KILL of its session does", p.Name)
		}
		if !strings.Contains(p.Hint, "VIEW SERVER STATE") {
			t.Errorf("%s: hint %q", p.Name, p.Hint)
		}
	}
	if !slices.Equal(names, []string{"Sessions", "Locks", "Statistics"}) {
		t.Fatalf("pages %q", names)
	}
	if got := pages[0].Kill([]any{int64(57)}); got != "KILL 57" {
		t.Fatalf("kill: %q", got)
	}
	if pages[2].Kill != nil || pages[2].Live || !pages[0].Live || !pages[1].Live {
		t.Fatal("statistics end sessions, or pages live as they should not")
	}
}

// A table is kept in shape by updating its statistics, rebuilding or
// reorganizing its indexes, and checking it.
func TestSQLServerMaintenanceCommands(t *testing.T) {
	var got []string
	for _, m := range (sqlserverEngine{}).MaintenanceCommands("[dbo].[t]") {
		got = append(got, m.SQL)
	}
	want := []string{"UPDATE STATISTICS [dbo].[t]", "ALTER INDEX ALL ON [dbo].[t] REORGANIZE", "ALTER INDEX ALL ON [dbo].[t] REBUILD",
		"DBCC CHECKTABLE (N'[dbo].[t]')"}
	if !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
}

// The activity pages read on the server; a session they list ends by
// its KILL; every maintenance command runs.
func TestIntegrationSQLServerActivityAndMaintenance(t *testing.T) {
	integration(t)
	ctx := context.Background()
	d, s := sqlserverSession(t)
	other, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Exec(ctx, "BEGIN TRANSACTION; DROP TABLE IF EXISTS dbo.it_activity; CREATE TABLE dbo.it_activity (id int PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	var spid int64
	if c, err := other.Query(ctx, "SELECT CAST(@@SPID AS bigint)"); err != nil {
		t.Fatal(err)
	} else if rows, err := c.Fetch(1); err != nil {
		t.Fatal(err)
	} else {
		spid = rows[0][0].(int64)
	}
	for _, p := range SQLServer.ActivityPages() {
		c, err := s.Query(ctx, p.Query)
		if err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		rows, err := c.Fetch(1000)
		if err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		if p.Name == "Statistics" {
			continue
		}
		found := slices.ContainsFunc(rows, func(r []any) bool { return Display(r[0]) == fmt.Sprint(spid) })
		if !found {
			t.Fatalf("%s: session %d not among %v", p.Name, spid, rows)
		}
	}
	if _, err := s.Exec(ctx, SQLServer.ActivityPages()[0].Kill([]any{spid})); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(ctx, "SELECT 1"); err == nil {
		t.Fatal("the killed session still runs")
	}
	mustExec(t, d, "DROP TABLE IF EXISTS dbo.it_activity", "CREATE TABLE dbo.it_activity (id int PRIMARY KEY, v int)", "CREATE INDEX it_activity_v ON dbo.it_activity (v)")
	defer d.SQL.ExecContext(ctx, "DROP TABLE IF EXISTS dbo.it_activity")
	for _, m := range (sqlserverEngine{}).MaintenanceCommands(QualifiedName(d.Dialect, "dbo", "it_activity")) {
		if _, err := s.Exec(ctx, m.SQL); err != nil {
			t.Fatalf("%s: %v", m.Label, err)
		}
	}
}
