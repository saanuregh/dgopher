package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/export"
	"dgopher/internal/testutil"
	"dgopher/internal/testutil/optionalserver"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// sqlserverApp is an app with a connection to the SQL Server test
// server's shop database, connected.
func sqlserverApp(t *testing.T) (*App, *ui.Tester, *connection.Conn) {
	t.Helper()
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "sqlserver")
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "ss", Name: "ss", Engine: db.SQLServer, Host: "127.0.0.1", Port: 11433, User: "sa", Password: "Dgopher-1",
		Database: "shop", Env: db.Development})
	tt := ui.NewTester(a.view, 1300, 800)
	connectNow(t, a, tt, cn)
	return a, tt, cn
}

// A SQL Server user is made as a login with its user, granted, taken
// back from and dropped from the Users tab; its password shows neither in
// the confirmation nor in the audit log.
func TestSQLServerUsersTab(t *testing.T) {
	a, tt, cn := sqlserverApp(t)
	const secret = "Tr0ub4dor&3-it"
	cleanup := func() {
		cn.DB().SQL.Exec(`DROP USER IF EXISTS dgopher_it_grace`)
		cn.DB().SQL.Exec(`IF SUSER_ID(N'dgopher_it_grace') IS NOT NULL DROP LOGIN dgopher_it_grace`)
		cn.DB().SQL.Exec(`DROP TABLE IF EXISTS dbo.it_orders_users`)
	}
	cleanup()
	defer cleanup()
	mustExec(t, cn.DB(), `CREATE TABLE dbo.it_orders_users (id int PRIMARY KEY)`)
	a.openUsers(cn)
	testutil.WaitFor(t, tt, "the users", func() bool {
		ut, ok := a.ActiveTab().(*usersTab)
		return ok && !ut.loading && len(ut.accounts) > 0
	})
	ut := a.ActiveTab().(*usersTab)
	agree := func(what string) {
		t.Helper()
		testutil.WaitFor(t, tt, what+" confirmation", func() bool { return a.confirm != nil })
		if strings.Contains(a.confirm.Preview, secret) {
			t.Fatalf("the confirmation shows the password: %s", a.confirm.Preview)
		}
		a.confirm.OnConfirm()
		a.confirm = nil
	}
	ut.openForm(formNewUser)
	ut.form.name, ut.form.password, ut.form.repeated = "dgopher_it_grace", secret, secret
	ut.submit(ut.form)
	agree("create")
	testutil.WaitFor(t, tt, "the new user", func() bool { return !ut.loading && ut.sel >= 0 && ut.accounts[ut.sel].Name == "dgopher_it_grace" })
	if acc := ut.accounts[ut.sel]; acc.User != "dgopher_it_grace" || acc.Role {
		t.Fatalf("listed %+v", acc)
	}
	ut.openForm(formGrant)
	ut.form.schema, ut.form.table = "dbo", "it_orders_users"
	ut.submit(ut.form)
	agree("grant")
	const priv = "GRANT SELECT ON [dbo].[it_orders_users]"
	testutil.WaitFor(t, tt, "the privilege", func() bool { return tt.HasText(priv) })
	if err := tt.Click("Revoke " + priv); err != nil {
		t.Fatal(err)
	}
	agree("revoke")
	testutil.WaitFor(t, tt, "the revoke", func() bool { return !ut.loading && !tt.HasText(priv) })
	if err := tt.Click("Drop…"); err != nil {
		t.Fatal(err)
	}
	agree("drop")
	testutil.WaitFor(t, tt, "the drop", func() bool {
		return !ut.loading && !slices.ContainsFunc(ut.accounts, func(acc db.Account) bool { return acc.Name == "dgopher_it_grace" })
	})
	events, _ := a.projects[0].Audit.Read(0, true)
	for _, e := range events {
		if strings.Contains(e.Statement, secret) || strings.Contains(e.Detail, secret) {
			t.Fatalf("the audit log holds the password: %+v", e)
		}
	}
}

// A database backs up from the dialog into a file on the server, typed
// as the server sees it, then restores from it over its later rows.
func TestSQLServerBackupDialog(t *testing.T) {
	a, tt, cn := sqlserverApp(t)
	const name = "dgopher_it_app_backup"
	drop := func() {
		cn.DB().SQL.Exec("IF DB_ID(N'" + name + "') IS NOT NULL ALTER DATABASE [" + name + "] SET SINGLE_USER WITH ROLLBACK IMMEDIATE")
		cn.DB().SQL.Exec("DROP DATABASE IF EXISTS [" + name + "]")
	}
	drop()
	defer drop()
	mustExec(t, cn.DB(), "CREATE DATABASE ["+name+"]", "CREATE TABLE ["+name+"].dbo.t (id int PRIMARY KEY)", "INSERT INTO ["+name+"].dbo.t VALUES (1)")
	a.openBackup(cn, name, false)
	b := a.backup
	formats, _ := backupRules(db.SQLServer)
	b.format = slices.Index(formats, db.BackupServerFile)
	tt.Frame()
	if !tt.HasText("File on the server") || tt.HasText("Choose…") {
		t.Fatalf("the server's path is not typed: %q", tt.Texts())
	}
	b.path = name + ".bak"
	a.startBackup(b)
	testutil.WaitFor(t, tt, "the question", func() bool { return a.confirm != nil || !b.running })
	if a.confirm == nil {
		t.Fatalf("not asked: %q", b.err)
	}
	a.confirm.OnConfirm()
	a.confirm = nil
	testutil.WaitFor(t, tt, "the backup", func() bool { return !b.running })
	if b.err != "" || !b.done {
		t.Fatalf("backup: %q", b.err)
	}
	b.open = false
	tt.Frame()
	mustExec(t, cn.DB(), "INSERT INTO ["+name+"].dbo.t VALUES (2)")
	// The restore would end an editor's transaction: it is refused while
	// one is open.
	q := query.New(a, cn, name, "q", "BEGIN TRANSACTION; INSERT INTO dbo.t VALUES (3);")
	a.AddTab(q)
	q.Run(query.RunScript)
	testutil.WaitFor(t, tt, "the editor's transaction", func() bool { return q.OpenTx() })
	a.openBackup(cn, name, true)
	b = a.backup
	b.path = name + ".bak"
	a.startBackup(b)
	if a.confirm != nil || !strings.Contains(b.err, "transaction") || !q.OpenTx() {
		t.Fatalf("asked %v, error %q, transaction open %v", a.confirm != nil, b.err, q.OpenTx())
	}
	rolledBack := false
	q.FinishTx(false, func(error) { rolledBack = true })
	testutil.WaitFor(t, tt, "the rollback", func() bool { return rolledBack && !q.OpenTx() })
	b.err = ""
	a.startBackup(b)
	testutil.WaitFor(t, tt, "the question", func() bool { return a.confirm != nil || b.err != "" })
	if a.confirm == nil {
		t.Fatalf("not asked: %q", b.err)
	}
	a.confirm.OnConfirm()
	a.confirm = nil
	testutil.WaitFor(t, tt, "the restore", func() bool { return !b.running && (b.done || b.err != "") })
	if b.err != "" {
		t.Fatalf("restore: %q", b.err)
	}
	if n := queryInt(t, cn.DB(), "SELECT count(*) FROM ["+name+"].dbo.t"); n != 1 {
		t.Fatalf("%d rows after the restore", n)
	}
}

// A SQLite table copies into SQL Server, made there with its types
// mapped, and back out into DuckDB, its values as they were.
func TestSQLServerCopyTables(t *testing.T) {
	a, tt, ss := sqlserverApp(t)
	lite := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", "")})
	duck := addConn(a, db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
	for _, cn := range []*connection.Conn{lite, duck} {
		connectNow(t, a, tt, cn)
	}
	drop := func() { ss.DB().SQL.Exec("DROP TABLE IF EXISTS dbo.it_copy_items") }
	drop()
	defer drop()
	mustExec(t, lite.DB(), `CREATE TABLE it_copy_items (id INTEGER PRIMARY KEY, name TEXT, price REAL, sold BOOLEAN, at DATE, raw BLOB)`,
		`INSERT INTO it_copy_items VALUES (1, 'pen''s', 1.5, 1, '2026-01-31', x'0102'), (2, 'ink', NULL, 0, NULL, NULL)`)
	if x := copyTables(t, a, tt, lite, "main", []string{"it_copy_items"}, ss, "dbo", copyCreate); x.err != "" {
		t.Fatal(x.err)
	}
	var name, at string
	var price float64
	var sold bool
	var raw []byte
	if err := ss.DB().SQL.QueryRow(`SELECT name, price, sold, CONVERT(char(10), [at], 23), raw FROM dbo.it_copy_items WHERE id = 1`).Scan(&name, &price, &sold, &at, &raw); err != nil ||
		name != "pen's" || price != 1.5 || !sold || at != "2026-01-31" || string(raw) != "\x01\x02" {
		t.Fatalf("copied into SQL Server %q %v %v %q %v: %v", name, price, sold, at, raw, err)
	}
	if x := copyTables(t, a, tt, ss, "dbo", []string{"it_copy_items"}, duck, "main", copyCreate); x.err != "" {
		t.Fatal(x.err)
	}
	if err := duck.DB().SQL.QueryRow(`SELECT name, price, sold, CAST("at" AS VARCHAR), raw FROM it_copy_items WHERE id = 1`).Scan(&name, &price, &sold, &at, &raw); err != nil ||
		name != "pen's" || price != 1.5 || !sold || at != "2026-01-31" || string(raw) != "\x01\x02" {
		t.Fatalf("copied out of SQL Server %q %v %v %q %v: %v", name, price, sold, at, raw, err)
	}
}

// Generated rows fill a SQL Server table: its identity, rowversion and
// computed columns left to the server, its key referring to another
// table's rows.
func TestSQLServerFill(t *testing.T) {
	a, tt, cn := sqlserverApp(t)
	drop := func() {
		cn.DB().SQL.Exec("DROP TABLE IF EXISTS dbo.it_fill_orders")
		cn.DB().SQL.Exec("DROP TABLE IF EXISTS dbo.it_fill_customers")
	}
	drop()
	defer drop()
	mustExec(t, cn.DB(), `CREATE TABLE dbo.it_fill_customers (id int PRIMARY KEY, name nvarchar(40))`,
		`INSERT INTO dbo.it_fill_customers VALUES (1, N'Ada'), (2, N'Grace')`,
		`CREATE TABLE dbo.it_fill_orders (id int IDENTITY(1,1) PRIMARY KEY, customer_id int NOT NULL REFERENCES dbo.it_fill_customers (id),
			code nvarchar(40) NOT NULL UNIQUE, level tinyint, paid bit NOT NULL, total money, ref uniqueidentifier, placed datetimeoffset(7),
			note nvarchar(max), ver rowversion, twice AS id * 2, bin varbinary(16) NOT NULL)`)
	x := fillTable(t, a, tt, cn, "dbo", "it_fill_orders", "50", nil)
	if x.err != "" {
		t.Fatal(x.err)
	}
	if n := queryInt(t, cn.DB(), "SELECT count(*) FROM dbo.it_fill_orders WHERE customer_id IN (1, 2) AND level BETWEEN 0 AND 100"); n != 50 {
		t.Fatalf("%d rows", n)
	}
}

// A workbook imports into a new SQL Server table.
func TestSQLServerImportNewTable(t *testing.T) {
	a, tt, cn := sqlserverApp(t)
	path := filepath.Join(t.TempDir(), "orders.xlsx")
	w, err := export.NewFileWriter(path, export.XLSX, []export.Column{{Name: "id", DatabaseType: "int"}, {Name: "total", DatabaseType: "numeric(10,2)"},
		{Name: "placed", DatabaseType: "timestamp"}, {Name: "paid", DatabaseType: "bool"}, {Name: "note", DatabaseType: "text"}}, export.Options{Header: true})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	w.Write([]any{1, "12.50", at, true, "first"})
	w.Write([]any{2, "7.25", at.Add(time.Hour), false, nil})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	drop := func() { cn.DB().SQL.Exec("DROP TABLE IF EXISTS dbo.it_orders_import") }
	drop()
	defer drop()
	x := importNew(t, a, tt, cn, "dbo", path, func(x *importState) { x.table = "it_orders_import" })
	if x.err != "" {
		t.Fatal(x.err)
	}
	var n int
	var note string
	var total float64
	if err := cn.DB().SQL.QueryRow("SELECT count(*), max(note), sum(total) FROM dbo.it_orders_import").Scan(&n, &note, &total); err != nil || n != 2 || note != "first" || total != 19.75 {
		t.Fatalf("rows %d, note %q, total %v: %v", n, note, total, err)
	}
}

// Every activity page of SQL Server reads.
func TestSQLServerActivity(t *testing.T) {
	a, tt, cn := sqlserverApp(t)
	a.openActivity(cn)
	testutil.WaitFor(t, tt, "the activity", func() bool { at, ok := a.ActiveTab().(*activityTab); return ok && at.conn == cn })
	at := a.ActiveTab().(*activityTab)
	for i, page := range at.pages {
		if i > 0 {
			at.showPage(i)
		}
		testutil.WaitFor(t, tt, page.Name, func() bool { return !at.loading && (at.src.Cols != nil || at.err != "") })
		if at.err != "" {
			t.Fatalf("%s: %s", page.Name, at.err)
		}
	}
	if _, err := os.Stat(filepath.Join(a.projects[0].Dir, "queries")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(a.projects[0].Dir, "queries"))
		if len(entries) != 0 {
			t.Fatalf("query files made: %v", entries)
		}
	}
}
