package app

import (
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

	"github.com/egoist/mygo/ui"
)

// oracleApp is an app with a connection to the Oracle test server's
// pluggable database as SYSTEM, who manages users and reads V$ views,
// connected.
func oracleApp(t *testing.T) (*App, *ui.Tester, *connection.Conn) {
	t.Helper()
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "oracle")
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "ora", Name: "ora", Engine: db.Oracle, Host: "127.0.0.1", Port: 11521, User: "system", Password: "dgopher",
		Database: "FREEPDB1", Env: db.Development})
	tt := ui.NewTester(a.view, 1300, 800)
	connectNow(t, a, tt, cn)
	return a, tt, cn
}

// An Oracle user is made, granted, taken back from and dropped from the
// Users tab; its password shows neither in the confirmation nor in the
// audit log.
func TestOracleUsersTab(t *testing.T) {
	a, tt, cn := oracleApp(t)
	const secret = "Tr0ub4dor&3-it"
	cleanup := func() {
		cn.DB().SQL.Exec(`DROP USER IF EXISTS "DGOPHER_IT_GRACE"`)
		cn.DB().SQL.Exec(`DROP TABLE IF EXISTS "DGOPHER"."it_orders_users"`)
	}
	cleanup()
	defer cleanup()
	mustExec(t, cn.DB(), `CREATE TABLE "DGOPHER"."it_orders_users" ("id" NUMBER PRIMARY KEY)`)
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
	ut.form.name, ut.form.password, ut.form.repeated = "DGOPHER_IT_GRACE", secret, secret
	ut.submit(ut.form)
	agree("create")
	testutil.WaitFor(t, tt, "the new user", func() bool { return !ut.loading && ut.sel >= 0 && ut.accounts[ut.sel].Name == "DGOPHER_IT_GRACE" })
	if acc := ut.accounts[ut.sel]; acc.Role {
		t.Fatalf("listed %+v", acc)
	}
	ut.openForm(formGrant)
	ut.form.schema, ut.form.table = "DGOPHER", "it_orders_users"
	ut.submit(ut.form)
	agree("grant")
	const priv = `GRANT SELECT ON "DGOPHER"."it_orders_users"`
	testutil.WaitFor(t, tt, "the privilege", func() bool {
		return !ut.loading && slices.ContainsFunc(ut.privs, func(p db.Privilege) bool { return p.Text == priv }) && tt.HasText(priv)
	})
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
		return !ut.loading && !slices.ContainsFunc(ut.accounts, func(acc db.Account) bool { return acc.Name == "DGOPHER_IT_GRACE" })
	})
	events, _ := a.projects[0].Audit.Read(0, true)
	for _, e := range events {
		if strings.Contains(e.Statement, secret) || strings.Contains(e.Detail, secret) {
			t.Fatalf("the audit log holds the password: %+v", e)
		}
	}
}

// A Data Pump file is typed as the server names it, to back up a schema
// and to restore one, where it stays.
func TestOracleBackupDialog(t *testing.T) {
	a, tt, cn := oracleApp(t)
	for _, restore := range []bool{false, true} {
		a.openBackup(cn, "DGOPHER", restore)
		tt.Frame()
		if !tt.HasText("File on the server") || tt.HasText("Choose…") || !testutil.HasTextContaining(tt, "DATA_PUMP_DIR") {
			t.Fatalf("restore %v: the server's file is not typed: %q", restore, tt.Texts())
		}
		a.backup.open = false
		tt.Frame()
	}
}

// A SQLite table copies into Oracle, made there with its types mapped,
// and back out into DuckDB, its values as they were.
func TestOracleCopyTables(t *testing.T) {
	a, tt, ora := oracleApp(t)
	lite := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", "")})
	duck := addConn(a, db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
	for _, cn := range []*connection.Conn{lite, duck} {
		connectNow(t, a, tt, cn)
	}
	drop := func() { ora.DB().SQL.Exec(`DROP TABLE IF EXISTS "DGOPHER"."it_copy_items"`) }
	drop()
	defer drop()
	mustExec(t, lite.DB(), `CREATE TABLE it_copy_items (id INTEGER PRIMARY KEY, name TEXT, price REAL, sold BOOLEAN, at DATE, raw BLOB)`,
		`INSERT INTO it_copy_items VALUES (1, 'pen''s', 1.5, 1, '2026-01-31', x'0102'), (2, 'ink', NULL, 0, NULL, NULL)`)
	if x := copyTables(t, a, tt, lite, "main", []string{"it_copy_items"}, ora, "DGOPHER", copyCreate); x.err != "" {
		t.Fatal(x.err)
	}
	var name, at string
	var price float64
	var sold int
	var raw []byte
	if err := ora.DB().SQL.QueryRow(`SELECT "name", "price", "sold", TO_CHAR("at", 'YYYY-MM-DD'), "raw" FROM "DGOPHER"."it_copy_items" WHERE "id" = 1`).
		Scan(&name, &price, &sold, &at, &raw); err != nil || name != "pen's" || price != 1.5 || sold != 1 || at != "2026-01-31" || string(raw) != "\x01\x02" {
		t.Fatalf("copied into Oracle %q %v %v %q %v: %v", name, price, sold, at, raw, err)
	}
	if x := copyTables(t, a, tt, ora, "DGOPHER", []string{"it_copy_items"}, duck, "main", copyCreate); x.err != "" {
		t.Fatal(x.err)
	}
	var soldOut int
	if err := duck.DB().SQL.QueryRow(`SELECT name, price, sold, strftime("at", '%Y-%m-%d'), raw FROM it_copy_items WHERE id = 1`).Scan(&name, &price, &soldOut, &at, &raw); err != nil ||
		name != "pen's" || price != 1.5 || soldOut != 1 || at != "2026-01-31" || string(raw) != "\x01\x02" {
		t.Fatalf("copied out of Oracle %q %v %v %q %v: %v", name, price, soldOut, at, raw, err)
	}
}

// Generated rows fill an Oracle table: its identity and virtual columns
// left to the server, its key referring to another table's rows, its
// unique column unique, a NUMBER of one digit given numbers that fit.
func TestOracleFill(t *testing.T) {
	a, tt, cn := oracleApp(t)
	drop := func() {
		cn.DB().SQL.Exec(`DROP TABLE IF EXISTS "DGOPHER"."it_fill_orders"`)
		cn.DB().SQL.Exec(`DROP TABLE IF EXISTS "DGOPHER"."it_fill_customers"`)
	}
	drop()
	defer drop()
	mustExec(t, cn.DB(), `CREATE TABLE "DGOPHER"."it_fill_customers" ("id" NUMBER(10) PRIMARY KEY, "name" VARCHAR2(40))`,
		`INSERT INTO "DGOPHER"."it_fill_customers" VALUES (1, 'Ada')`, `INSERT INTO "DGOPHER"."it_fill_customers" VALUES (2, 'Grace')`,
		`CREATE TABLE "DGOPHER"."it_fill_orders" ("id" NUMBER(19) GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
			"customer_id" NUMBER(10) NOT NULL REFERENCES "DGOPHER"."it_fill_customers" ("id"), "code" VARCHAR2(40) NOT NULL UNIQUE,
			"lvl" NUMBER(3), "paid" NUMBER(1) NOT NULL, "total" NUMBER(12,2), "placed" TIMESTAMP WITH TIME ZONE,
			"day" DATE, "note" CLOB, "raw" RAW(16), "twice" NUMBER GENERATED ALWAYS AS ("id" * 2) VIRTUAL)`)
	x := fillTable(t, a, tt, cn, "DGOPHER", "it_fill_orders", "50", nil)
	if x.err != "" {
		t.Fatal(x.err)
	}
	if n := queryInt(t, cn.DB(), `SELECT COUNT(*) FROM "DGOPHER"."it_fill_orders" WHERE "customer_id" IN (1, 2) AND "twice" = "id" * 2 AND "paid" BETWEEN 0 AND 9`); n != 50 {
		t.Fatalf("%d rows", n)
	}
}

// A workbook imports into a new Oracle table.
func TestOracleImportNewTable(t *testing.T) {
	a, tt, cn := oracleApp(t)
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
	drop := func() { cn.DB().SQL.Exec(`DROP TABLE IF EXISTS "DGOPHER"."it_orders_import"`) }
	drop()
	defer drop()
	x := importNew(t, a, tt, cn, "DGOPHER", path, func(x *importState) { x.table = "it_orders_import" })
	if x.err != "" {
		t.Fatal(x.err)
	}
	var n int
	var note string
	var total float64
	if err := cn.DB().SQL.QueryRow(`SELECT COUNT(*), MAX(TO_CHAR("note")), SUM("total") FROM "DGOPHER"."it_orders_import"`).Scan(&n, &note, &total); err != nil ||
		n != 2 || note != "first" || total != 19.75 {
		t.Fatalf("rows %d, note %q, total %v: %v", n, note, total, err)
	}
}

// Every activity page of Oracle reads.
func TestOracleActivity(t *testing.T) {
	a, tt, cn := oracleApp(t)
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
}
