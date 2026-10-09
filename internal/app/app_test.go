package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/export"
	"dgopher/internal/project"
	"dgopher/internal/store"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/redis"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// newTestApp returns an app with one project, in a folder of the test.
func newTestApp(t *testing.T) *App {
	t.Helper()
	a := newBareApp(t)
	dir := filepath.Join(t.TempDir(), "proj")
	os.MkdirAll(dir, 0o755)
	if _, err := a.addProject(dir); err != nil {
		t.Fatal(err)
	}
	return a
}

// newBareApp returns an app without a project, as at first start.
func newBareApp(t *testing.T) *App {
	t.Helper()
	st, err := store.Open(t.TempDir(), store.MemorySecrets())
	if err != nil {
		t.Fatal(err)
	}
	return newApp(st)
}

func TestWelcome(t *testing.T) {
	a := newBareApp(t)
	tt := ui.NewTester(a.view, 1200, 760)
	if !tt.HasText("DGopher") || !tt.HasText("Dig into your databases.") || !testutil.HasTextContaining(tt, "No projects yet.") {
		t.Fatalf("texts %q", tt.Texts())
	}
	testutil.Snapshot(t, tt, "welcome")
	// No connection without a project: the project comes first.
	if err := tt.Click("New connection"); err != nil {
		t.Fatal(err)
	}
	if a.connForm != nil || a.newProject == nil {
		t.Fatal("a connection form opened without a project")
	}
	a.newProject.name = "billing"
	a.newProject.parent = t.TempDir()
	tt.Frame()
	testutil.Snapshot(t, tt, "new-project")
	if err := tt.Click("Create Project"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.projects) != 1 || a.connForm == nil || a.connForm.project != a.projects[0] {
		t.Fatalf("after creating the project: %d projects, form %+v", len(a.projects), a.connForm)
	}
	dir := a.projects[0].Dir
	for _, f := range []string{project.File, "queries", project.LocalDir + "/.gitignore"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("the new project has no %s: %v", f, err)
		}
	}
	tt.Frame()
	testutil.Snapshot(t, tt, "connection-form")
}

func TestConnectionFormSavesWithoutPassword(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1200, 800)
	a.openConnForm(nil)
	f := a.connForm
	f.cfg.Name, f.cfg.Host, f.cfg.Password = "local", "127.0.0.1", "s3cret"
	a.saveConnForm(f, false)
	tt.Frame()
	if len(a.conns) != 1 || a.conns[0].Config.Password != "" {
		t.Fatalf("conns %+v", a.conns)
	}
	// The password went to the keychain, not to the file.
	raw, _ := os.ReadFile(filepath.Join(a.projects[0].Dir, project.File))
	if !strings.Contains(string(raw), `"name": "local"`) || strings.Contains(string(raw), "s3cret") {
		t.Fatalf("%s:\n%s", project.File, raw)
	}
	if pw, _ := a.st.Secrets().Get(secretKey(&a.conns[0].Config, "password")); pw != "s3cret" {
		t.Fatalf("keychain has %q", pw)
	}
}

// addConn adds a connection to the app's first project, trusted as one
// the user made, with its ID prefixed as the project's are.
func addConn(a *App, cfg db.Config) *connection.Conn {
	p := a.projects[0]
	cfg.ID = p.Prefix + cfg.ID
	cn := &connection.Conn{Config: cfg, Project: p}
	cn.Reset()
	a.conns = append(a.conns, cn)
	a.trust(cn)
	return cn
}

func TestQueryFlow(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := newTestApp(t)
	cn := addConn(a, testutil.PGConfig())
	prod := testutil.PGConfig()
	prod.ID, prod.Name, prod.Env = "pgprod", "Billing (prod)", db.Production
	addConn(a, prod)
	tt := ui.NewTester(a.view, 1360, 860)

	a.activate(navNode{kind: nodeConn, conn: cn.Config.ID})
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected && cn.Schemas[""] != nil })
	a.nav.expand(navNode{kind: nodeSchema, conn: cn.Config.ID, schema: "shop"})
	a.nav.expand(navNode{kind: nodeFolder, conn: cn.Config.ID, schema: "shop"})
	testutil.WaitFor(t, tt, "tables", func() bool { return cn.Objects[connection.SchemaKey{Database: "", Schema: "shop"}] != nil })

	a.NewQueryTab(cn, "", "SELECT c.id, c.name, c.email, c.country, count(o.id) AS orders, sum(o.total) AS spent\nFROM shop.customers c\nLEFT JOIN shop.orders o ON o.customer_id = c.id\nGROUP BY c.id\nORDER BY spent DESC NULLS LAST;\n")
	testutil.WaitFor(t, tt, "query tab", func() bool { return len(a.tabs) == 1 })
	q := a.tabs[0].(*query.Tab)
	testutil.SetCaret(tt, &q.Editor, 10)
	q.Run(query.RunStatement)
	page := a.Settings().PageSize
	testutil.WaitFor(t, tt, "the first page", func() bool {
		return !q.Running && tt.HasText(fmt.Sprintf("%d rows loaded", page))
	})
	testutil.Snapshot(t, tt, "query-results")

	// Scrolling to the end reads the next page.
	header, _ := tt.Find("spent")
	testutil.WaitFor(t, tt, "the next page", func() bool {
		tt.Scroll(header.X, header.Y+200, 0, 100000)
		n, ok := resultCount(tt, q)
		return ok && n > page
	})

	// A write on development runs without asking; DELETE without WHERE asks.
	q.Editor.Text = "DELETE FROM shop.orders;"
	testutil.SetCaret(tt, &q.Editor, 3)
	q.Run(query.RunStatement)
	tt.Frame()
	if a.confirm == nil || a.confirm.TypeName {
		t.Fatalf("DELETE without WHERE on development: confirm %+v", a.confirm)
	}
	testutil.Snapshot(t, tt, "confirm-dev")
	a.confirm = nil

	// On production, it needs the connection's name typed.
	a.NewQueryTab(a.conns[1], "", "DELETE FROM shop.orders;")
	testutil.WaitFor(t, tt, "prod tab", func() bool { return len(a.tabs) == 2 })
	pq := a.tabs[1].(*query.Tab)
	testutil.SetCaret(tt, &pq.Editor, 3)
	pq.Run(query.RunStatement)
	tt.Frame()
	if a.confirm == nil || !a.confirm.TypeName {
		t.Fatalf("DELETE without WHERE on production: confirm %+v", a.confirm)
	}
	testutil.Snapshot(t, tt, "confirm-prod")
	a.confirm.Open = false
	tt.Frame()

	// Manual commit on production: an UPDATE opens a transaction.
	pq.Editor.Text = "UPDATE shop.orders SET status = 'paid' WHERE id = 1;"
	testutil.SetCaret(tt, &pq.Editor, 3)
	pq.Run(query.RunStatement)
	tt.Frame()
	if a.confirm == nil {
		t.Fatal("an UPDATE on production ran without asking")
	}
	a.confirm.Open = false
	a.confirm.OnConfirm()
	a.confirm = nil
	testutil.WaitFor(t, tt, "update", func() bool { return !pq.Running })
	if pq.Tx != db.TxOpen {
		t.Fatalf("manual commit left tx %v; texts %q", pq.Tx, tt.Texts())
	}
	testutil.Snapshot(t, tt, "manual-commit")
	pq.FinishTx(false, func(error) {})
	testutil.WaitFor(t, tt, "rollback", func() bool { return !pq.Running && pq.Tx == db.TxNone })
}

func TestEnginesSmoke(t *testing.T) {
	testutil.Integration(t)
	dir := t.TempDir()
	sqliteFile := dir + "/notes.sqlite"
	os.WriteFile(sqliteFile, nil, 0o600)
	type engineCase struct {
		cfg    db.Config
		schema string
		setup  []string
	}
	cases := []engineCase{
		{db.Config{ID: "my", Name: "Shop MySQL", Engine: db.MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop", Env: db.Staging}, "shop", []string{
			"DROP TABLE IF EXISTS notes",
			"CREATE TABLE notes (id INT AUTO_INCREMENT PRIMARY KEY, title VARCHAR(200) NOT NULL, body TEXT, created DATETIME DEFAULT CURRENT_TIMESTAMP)",
			"INSERT INTO notes (title, body) VALUES ('first', 'hello'), ('second', NULL), ('third', 'ünïcødé ✓')",
		}},
		{db.Config{ID: "ch", Name: "Events ClickHouse", Engine: db.ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dbgopher", Database: "default", Env: db.Production}, "default", []string{
			"DROP TABLE IF EXISTS default.notes",
			"CREATE TABLE default.notes (id UInt64, title String, body Nullable(String), created DateTime DEFAULT now()) ENGINE = MergeTree ORDER BY id",
			"INSERT INTO default.notes (id, title, body) VALUES (1, 'first', 'hello'), (2, 'second', NULL), (3, 'third', 'ünïcødé ✓')",
		}},
		{db.Config{ID: "sq", Name: "Notes SQLite", Engine: db.SQLite, Database: sqliteFile, Env: db.Development}, "main", []string{
			"CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT NOT NULL, body TEXT, created TEXT DEFAULT CURRENT_TIMESTAMP)",
			"INSERT INTO notes (title, body) VALUES ('first', 'hello'), ('second', NULL), ('third', 'ünïcødé ✓')",
		}},
	}
	duckFile := dir + "/notes.duckdb"
	mem, err := db.Open(context.Background(), db.Config{Name: "m", Engine: db.DuckDB, Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mem.SQL.Exec("ATTACH '" + duckFile + "' AS created; DETACH created")
	mem.Close()
	cases = append(cases, engineCase{db.Config{ID: "dk", Name: "Analytics DuckDB", Engine: db.DuckDB, Database: duckFile, Env: db.Development}, "main", []string{
		"CREATE TABLE notes (id INTEGER PRIMARY KEY, title VARCHAR NOT NULL, body VARCHAR, created TIMESTAMP DEFAULT current_timestamp)",
		"INSERT INTO notes (id, title, body) VALUES (1, 'first', 'hello'), (2, 'second', NULL), (3, 'third', 'ünïcødé ✓')",
	}})
	for _, ec := range cases {
		t.Run(ec.cfg.Name, func(t *testing.T) {
			ctx := context.Background()
			d, err := db.Open(ctx, ec.cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range ec.setup {
				if _, err := d.SQL.ExecContext(ctx, q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
			}
			d.Close()
			a := newTestApp(t)
			cn := addConn(a, ec.cfg)
			tt := ui.NewTester(a.view, 1360, 760)
			a.activate(navNode{kind: nodeConn, conn: cn.Config.ID})
			testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected && cn.Schemas[""] != nil })
			a.nav.expand(navNode{kind: nodeSchema, conn: cn.Config.ID, schema: ec.schema})
			a.nav.expand(navNode{kind: nodeFolder, conn: cn.Config.ID, schema: ec.schema})
			testutil.WaitFor(t, tt, "objects", func() bool { return cn.Objects[connection.SchemaKey{Database: "", Schema: ec.schema}] != nil })
			var notes db.Object
			for _, o := range cn.Objects[connection.SchemaKey{Database: "", Schema: ec.schema}] {
				if o.Name == "notes" {
					notes = o
				}
			}
			if notes.Name == "" {
				t.Fatalf("no notes table in %+v", cn.Objects)
			}
			a.OpenTable(cn, "", notes, dataview.PageData)
			testutil.WaitFor(t, tt, "rows", func() bool {
				return tt.HasText("3 rows") && tt.HasText("first") && tt.HasText("second") && tt.HasText("third") || testutil.HasTextContaining(tt, "Could not read the rows")
			})
			if testutil.HasTextContaining(tt, "Could not read the rows") {
				t.Fatalf("the table did not load: %q", tt.Texts())
			}
			testutil.Snapshot(t, tt, "engine-"+string(ec.cfg.Engine))
			a.NewQueryTab(cn, "", "SELECT title, body FROM notes WHERE body IS NOT NULL ORDER BY title;")
			testutil.WaitFor(t, tt, "query tab", func() bool { return len(a.tabs) == 2 })
			q := a.tabs[1].(*query.Tab)
			testutil.SetCaret(tt, &q.Editor, 3)
			q.Run(query.RunStatement)
			testutil.WaitFor(t, tt, "the query's 2 rows", func() bool { return resultRows(tt, q, 2) })
			if !tt.HasText("ünïcødé ✓") {
				t.Fatalf("the text did not round-trip: %q", tt.Texts())
			}

		})
	}
}

func TestWorkspaceRestore(t *testing.T) {
	cfgDir := t.TempDir()
	st, _ := store.Open(cfgDir, store.MemorySecrets())
	a := newApp(st)
	projDir := filepath.Join(t.TempDir(), "proj")
	os.MkdirAll(projDir, 0o755)
	p, err := a.addProject(projDir)
	if err != nil {
		t.Fatal(err)
	}
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	a.saveProject(p)
	tt := ui.NewTester(a.view, 900, 600)
	a.NewQueryTab(cn, "", "SELECT 42;")
	testutil.WaitFor(t, tt, "the editor", func() bool { _, ok := a.ActiveTab().(*query.Tab); return ok })
	q := a.ActiveTab().(*query.Tab)
	want := "-- connection: lite\n\nSELECT 42;"
	if disk, _ := os.ReadFile(q.Path); string(disk) != want || filepath.Dir(q.Path) != filepath.Join(projDir, "queries") {
		t.Fatalf("new editor's file %s: %q", q.Path, disk)
	}
	q.Editor.Text += " -- edited"
	q.Flush(false)
	a.saveWorkspace(true)
	var ws json.RawMessage
	a.projects[0].Local.LoadJSON(workspaceFile, &ws)
	if strings.Contains(string(ws), "SELECT") || !strings.Contains(string(ws), `"queries/query-1.sql"`) {
		t.Fatalf("the workspace holds SQL, or an absolute path: %s", ws)
	}

	st2, _ := store.Open(cfgDir, store.MemorySecrets())
	b := newApp(st2)
	if len(b.projects) != 1 || len(b.tabs) != 1 {
		t.Fatalf("restored %d projects, %d tabs", len(b.projects), len(b.tabs))
	}
	r := b.tabs[0].(*query.Tab)
	if r.Editor.Text != want+" -- edited" || r.Path != q.Path || r.Conn.Config.Name != "lite" {
		t.Fatalf("restored %q at %s", r.Editor.Text, r.Path)
	}
	// Shown, it waits to be asked; set to auto-connect, it connects by
	// itself.
	tt2 := ui.NewTester(b.view, 900, 600)
	for range 5 {
		tt2.Frame()
	}
	if r.Conn.Status != connection.StatusIdle || !tt2.HasText("Not connected.") {
		t.Fatalf("restored editor connected on its own: %v", r.Conn.Status)
	}
	r.Conn.Config.AutoConnect = true
	testutil.WaitFor(t, tt2, "connect", func() bool { return r.Conn.Status == connection.StatusConnected })
}

func TestImportCSV(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := newTestApp(t)
	cn := addConn(a, testutil.PGConfig())
	tt := ui.NewTester(a.view, 1200, 760)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	path := t.TempDir() + "/people.csv"
	os.WriteFile(path, []byte("\uFEFFName,EMAIL,ignored,country\nAda,ada@csv.test,x,GB\n\"Lovelace, A.\",,y,\nGrace,grace@csv.test,z,US\n"), 0o600)
	obj := db.Object{Schema: "shop", Name: "customers", Kind: db.KindTable}
	a.startImport(cn, "", "", &obj, path)
	x := a.importing
	testutil.WaitFor(t, tt, "preview", func() bool { return x.file != nil && len(x.mapping) == 4 })
	if x.mapping[0] != "name" || x.mapping[1] != "email" || x.mapping[2] != skipColumn || x.mapping[3] != "country" {
		t.Fatalf("mapping %q", x.mapping)
	}
	testutil.Snapshot(t, tt, "import-csv")
	a.confirmImport(x)
	testutil.WaitFor(t, tt, "import", func() bool { return !x.running && a.importing == nil })
	var n int
	var email *string
	cn.DB.SQL.QueryRow(`SELECT count(*) FROM shop.customers WHERE name IN ('Ada', 'Lovelace, A.', 'Grace')`).Scan(&n)
	cn.DB.SQL.QueryRow(`SELECT email FROM shop.customers WHERE name = 'Lovelace, A.'`).Scan(&email)
	if n != 3 || email != nil {
		t.Fatalf("imported %d rows, email %v", n, email)
	}
	// A failing row rolls the whole file back.
	os.WriteFile(path, []byte("name,email\nOk,ok@csv.test\nDup,ada@csv.test\n"), 0o600)
	a.startImport(cn, "", "", &obj, path)
	x = a.importing
	testutil.WaitFor(t, tt, "preview", func() bool { return x.file != nil && len(x.mapping) == 2 })
	a.confirmImport(x)
	testutil.WaitFor(t, tt, "failed import", func() bool { return !x.running && x.err != "" })
	cn.DB.SQL.QueryRow(`SELECT count(*) FROM shop.customers WHERE name = 'Ok'`).Scan(&n)
	if n != 0 {
		t.Fatalf("a failed import left %d rows", n)
	}
}

// importNew imports a file into a new table of cn, and waits for it.
func importNew(t *testing.T, a *App, tt *ui.Tester, cn *connection.Conn, schema, path string, edit func(*importState)) *importState {
	t.Helper()
	a.startImport(cn, "", schema, nil, path)
	x := a.importing
	testutil.WaitFor(t, tt, "the file", func() bool { return !x.loading })
	if x.err != "" {
		t.Fatal(x.err)
	}
	if edit != nil {
		edit(x)
	}
	a.confirmImport(x)
	if a.confirm != nil {
		a.confirm.OnConfirm()
		a.confirm = nil
	}
	testutil.WaitFor(t, tt, "the import", func() bool { return !x.running })
	return x
}

// Each format becomes a new table, its columns typed as the file's.
func TestImportNewTables(t *testing.T) {
	a := newTestApp(t)
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.view, 1200, 800)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	dir := t.TempDir()
	csv := filepath.Join(dir, "2024 Sales (EU).csv")
	os.WriteFile(csv, []byte("id,amount,paid\n1,9.50,true\n2,3.25,false\n"), 0o600)
	x := importNew(t, a, tt, cn, "main", csv, func(*importState) { testutil.Snapshot(t, tt, "import-new-table") })
	if x.err != "" || x.table != "imported_2024_sales__eu" {
		t.Fatalf("import %q into %q", x.err, x.table)
	}
	var n, paid int
	var amount float64
	cn.DB.SQL.QueryRow(`SELECT count(*), sum(amount), sum(paid) FROM "imported_2024_sales__eu"`).Scan(&n, &amount, &paid)
	if n != 2 || amount != 12.75 || paid != 1 {
		t.Fatalf("rows %d, amount %v, paid %d", n, amount, paid)
	}

	jsonl := filepath.Join(dir, "events.jsonl")
	os.WriteFile(jsonl, []byte("{\"kind\": \"login\", \"meta\": {\"ip\": \"1.2.3.4\"}}\n{\"kind\": \"logout\", \"meta\": null}\n"), 0o600)
	x = importNew(t, a, tt, cn, "main", jsonl, func(x *importState) {
		x.table = "events"
		x.newCols[0].name = "event"
	})
	var kind, meta string
	cn.DB.SQL.QueryRow(`SELECT event, meta FROM events WHERE event = 'login'`).Scan(&kind, &meta)
	if x.err != "" || kind != "login" || meta != `{"ip":"1.2.3.4"}` {
		t.Fatalf("events %q: %q %q", x.err, kind, meta)
	}
	// A failing row creates nothing.
	bad := filepath.Join(dir, "bad.csv")
	os.WriteFile(bad, []byte("id\n1\n1\n"), 0o600)
	x = importNew(t, a, tt, cn, "main", bad, func(x *importState) { x.newCols[0].typ = "INTEGER PRIMARY KEY" })
	if !strings.Contains(x.err, "the table was not created") {
		t.Fatalf("a failing import: %q", x.err)
	}
	if err := cn.DB.SQL.QueryRow(`SELECT count(*) FROM bad`).Scan(&n); err == nil {
		t.Fatal("a failed import left its table")
	}
}

// On every server engine a new table is created from a workbook, and on
// MySQL, whose CREATE TABLE commits, a failure drops it.
func TestImportNewTableServers(t *testing.T) {
	testutil.Integration(t)
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
	for _, cfg := range []db.Config{
		testutil.PGConfig(),
		{ID: "my", Name: "my", Engine: db.MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop"},
		{ID: "ch", Name: "ch", Engine: db.ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dbgopher"},
	} {
		t.Run(string(cfg.Engine), func(t *testing.T) {
			a := newTestApp(t)
			cn := addConn(a, cfg)
			tt := ui.NewTester(a.view, 1200, 800)
			a.Connect(cn, nil)
			testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
			schema := map[db.Engine]string{db.Postgres: "public", db.MySQL: "shop", db.ClickHouse: "default"}[cfg.Engine]
			table := db.QualifiedName(cn.DB.Dialect, schema, "it_orders")
			cn.DB.SQL.Exec("DROP TABLE IF EXISTS " + table)
			defer cn.DB.SQL.Exec("DROP TABLE IF EXISTS " + table)
			x := importNew(t, a, tt, cn, schema, path, func(x *importState) { x.table = "it_orders" })
			if x.err != "" {
				t.Fatal(x.err)
			}
			var n int
			var note string
			if err := cn.DB.SQL.QueryRow("SELECT count(*), max(note) FROM "+table).Scan(&n, &note); err != nil || n != 2 || note != "first" {
				t.Fatalf("rows %d, note %q: %v", n, note, err)
			}
			if cfg.Engine != db.MySQL {
				return
			}
			cn.DB.SQL.Exec("DROP TABLE " + table)
			x = importNew(t, a, tt, cn, schema, path, func(x *importState) {
				x.table = "it_orders"
				x.newCols[4].typ = "VARCHAR(2)" // "first" does not fit
			})
			if !strings.Contains(x.err, "the table was not created") {
				t.Fatalf("a failing import: %q", x.err)
			}
			if err := cn.DB.SQL.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err == nil {
				t.Fatal("the failed import left its table")
			}
		})
	}
}

func TestSnippets(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	q := query.New(a, cn, "", "q", "SELECT 1;\nSELECT count(*) FROM t;")
	a.AddTab(q)
	testutil.SetCaret(tt, &q.Editor, 15)
	a.AskSnippet(q)
	if a.snippetForm == nil || a.snippetForm.sql != "SELECT count(*) FROM t" {
		t.Fatalf("snippet form %+v", a.snippetForm)
	}
	a.snippetForm.name = "count t"
	tt.Frame()
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	p := a.projects[0]
	if len(p.Snippets) != 1 {
		t.Fatalf("snippets %+v", p.Snippets)
	}
	if raw, _ := os.ReadFile(filepath.Join(p.Dir, project.File)); !strings.Contains(string(raw), `"name": "count t"`) {
		t.Fatalf("the snippet is not in %s:\n%s", project.File, raw)
	}
	// It is in the palette, and inserts at the caret.
	a.openPalette(false)
	a.palette.query = "count t"
	tt.Frame()
	if len(a.palette.shown) == 0 || a.palette.shown[0].group != "Snippet" {
		t.Fatalf("palette %+v", a.palette.shown)
	}
	testutil.SetCaret(tt, &q.Editor, 0)
	a.insertSnippet(p.Snippets[0])
	tt.Frame()
	if !strings.HasPrefix(q.Editor.Text, "SELECT count(*) FROM tSELECT 1;") {
		t.Fatalf("text %q", q.Editor.Text)
	}
}

func TestServerActivity(t *testing.T) {
	testutil.Integration(t)
	a := newTestApp(t)
	cases := []db.Config{
		testutil.PGConfig(),
		{ID: "my", Name: "my", Engine: db.MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop"},
		{ID: "ch", Name: "ch", Engine: db.ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dbgopher"},
		{ID: "rd", Name: "rd", Engine: db.Redis, Host: "127.0.0.1", Port: 16379},
	}
	tt := ui.NewTester(a.view, 1360, 760)
	for _, cfg := range cases {
		cn := addConn(a, cfg)
		a.openActivity(cn)
		testutil.WaitFor(t, tt, cfg.Name+" activity", func() bool {
			at, ok := a.ActiveTab().(*activityTab)
			return ok && at.conn == cn
		})
		at := a.ActiveTab().(*activityTab)
		// Every page reads; one may fail only to say what the server lacks.
		for i, page := range at.pages {
			if i > 0 {
				at.showPage(i)
			}
			testutil.WaitFor(t, tt, cfg.Name+" "+page.name, func() bool {
				return !at.loading && (at.src.Cols != nil || at.metrics != nil || at.err != "")
			})
			if at.err != "" && (page.hint == "" || !strings.Contains(at.err, page.hint)) {
				t.Fatalf("%s %s: %s", cfg.Name, page.name, at.err)
			}
			testutil.Snapshot(t, tt, "activity-"+string(cfg.Engine)+"-"+strings.ToLower(page.name))
		}
	}
}

// A Metrics page reads twice for its rates.
func TestClickHouseMetrics(t *testing.T) {
	testutil.Integration(t)
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "ch", Name: "ch", Engine: db.ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dbgopher"})
	tt := ui.NewTester(a.view, 1360, 760)
	a.openActivity(cn)
	testutil.WaitFor(t, tt, "the activity", func() bool { _, ok := a.ActiveTab().(*activityTab); return ok })
	at := a.ActiveTab().(*activityTab)
	at.showPage(slices.IndexFunc(at.pages, func(p activityPage) bool { return p.metrics }))
	testutil.WaitFor(t, tt, "two reads", func() bool { return at.before != nil })
	if at.err != "" || at.metrics["uptime"] <= 0 || !tt.HasText("Queries per second") || !at.metricsAt.After(at.beforeAt) {
		t.Fatalf("metrics %v: %q", at.metrics, at.err)
	}
}

func TestProjectFolder(t *testing.T) {
	a := newTestApp(t)
	p := a.projects[0]
	dir := p.Dir
	a.openConnForm(nil)
	f := a.connForm
	f.cfg.Name, f.cfg.Host, f.cfg.PasswordEnv = "Billing (prod)", "db.internal", "DGOPHER_BILLING_PW"
	f.envChosen = true
	f.source = passwordSources[sourceEnv]
	f.env = 2
	a.saveConnForm(f, false)
	a.openConnForm(nil)
	f = a.connForm
	f.cfg.Name, f.cfg.Host, f.cfg.Password = "Alpha", "h", "s3cret"
	f.envChosen = true
	f.engine = db.MySQL.Label()
	a.saveConnForm(f, false)
	raw, err := os.ReadFile(filepath.Join(dir, project.File))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "s3cret") || !strings.Contains(text, `"id": "billing-prod"`) || !strings.Contains(text, `"passwordEnv": "DGOPHER_BILLING_PW"`) {
		t.Fatalf("project file:\n%s", text)
	}
	if strings.Index(text, `"alpha"`) > strings.Index(text, `"billing-prod"`) {
		t.Fatalf("connections not sorted by ID:\n%s", text)
	}
	// Saving again changes nothing: the file diffs only when settings do.
	a.saveProject(p)
	again, _ := os.ReadFile(filepath.Join(dir, project.File))
	if string(again) != text {
		t.Fatal("a second save changed the file")
	}
	// The app's config holds no connection.
	entries, _ := os.ReadDir(a.st.Dir())
	for _, e := range entries {
		if e.Name() != "settings.json" {
			t.Errorf("the app config holds %s", e.Name())
		}
	}
	// A query file names its connection.
	os.MkdirAll(dir+"/queries/reports", 0o755)
	os.WriteFile(dir+"/queries/reports/top.sql", []byte("-- connection: billing-prod\nSELECT 1;\n"), 0o644)
	tt := ui.NewTester(a.view, 1000, 700)
	a.ScanQueries(p, true)
	testutil.WaitFor(t, tt, "query files", func() bool { return len(p.Files) == 1 })
	a.connByID(p.Prefix + "billing-prod").Status = connection.StatusConnected // no server: the editor needs none to open
	a.openQueryFile(p, "reports/top.sql", nil)
	tt.Frame()
	q, ok := a.ActiveTab().(*query.Tab)
	if !ok || q.Conn.Config.Name != "Billing (prod)" || q.Path == "" {
		t.Fatalf("opened %+v", a.ActiveTab())
	}
	testutil.Snapshot(t, tt, "project")
	// A usage in it opens it at its place, the tab open or not.
	path := filepath.Join(dir, "queries", "reports", "top.sql")
	a.OpenQueryFile(path, 30)
	tt.Frame()
	tt.Frame()
	if q.Editor.SelEnd != 30 || a.ActiveTab() != q {
		t.Fatalf("caret %d, tab %v", q.Editor.SelEnd, a.ActiveTab())
	}
	// Listed again, the project gives back its connections.
	a2 := newBareApp(t)
	if _, err := a2.addProject(dir); err != nil || len(a2.conns) != 2 {
		t.Fatalf("reopen: %v %d", err, len(a2.conns))
	}
}

func TestKeyboard(t *testing.T) {
	a := newTestApp(t)
	file := t.TempDir() + "/k.sqlite"
	os.WriteFile(file, nil, 0o600)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.view, 1200, 760)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	cn.DB.SQL.Exec("CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT)")
	cn.DB.SQL.Exec("INSERT INTO notes (title) VALUES ('a'), ('b')")
	a.NewQueryTab(cn, "", "SELECT * FROM notes;")
	a.NewQueryTab(cn, "", "SELECT 2;")
	tt.Frame()
	tt.Key(ui.Cmd, ui.Key1)
	tt.Frame()
	if a.active != 0 {
		t.Fatalf("⌘1: tab %d", a.active)
	}
	tt.Key(ui.Cmd, ui.Key9)
	tt.Frame()
	if a.active != 1 {
		t.Fatalf("⌘9: tab %d", a.active)
	}
	// ⌘0: the navigator has the keyboard; ↵ connects and opens.
	tt.Key(ui.Cmd, ui.Key0)
	tt.Frame()
	tt.Frame()
	if a.focusWant != "" || !tt.Focused("Navigator") && !tt.Focused("lite") {
		t.Fatalf("⌘0: focus still wanted %q", a.focusWant)
	}
	// ⌘J goes from the editor to the results and back.
	tt.Key(ui.Cmd, ui.Key1)
	tt.Frame()
	q := a.tabs[0].(*query.Tab)
	testutil.SetCaret(tt, &q.Editor, 3)
	tt.Key(ui.Cmd, ui.KeyEnter)
	testutil.WaitFor(t, tt, "results", func() bool { return resultRows(tt, q, -1) })

	q.Editor.WantFocus = true
	tt.Frame()
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyJ)
	tt.Frame()
	tt.Frame()
	if a.focusWant != "" || q.Editor.HasFocus {
		t.Fatalf("⌘J: focus wanted %q, editor focused %v", a.focusWant, q.Editor.HasFocus)
	}
	// ⌘/ lists the keys.
	tt.Key(ui.Cmd, ui.KeySlash)
	tt.Frame()
	if !a.shortcutsOpen || !tt.HasText("Keyboard Shortcuts") {
		t.Fatal("⌘/ showed no shortcuts")
	}
	testutil.Snapshot(t, tt, "shortcuts")
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.shortcutsOpen || tt.HasText("Keyboard Shortcuts") {
		t.Fatal("Escape left the shortcuts open")
	}
}

func TestConnectionFromURL(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1200, 800)
	a.openConnForm(nil)
	tt.Frame()
	f := a.connForm
	f.url = "mysql://app:pw@db.internal:3307/shop?tls=true"
	a.fillFromURL(f)
	if f.engine != "MySQL" || f.cfg.Host != "db.internal" || f.port != "3307" || f.cfg.Password != "pw" || f.url != "" || f.tls != tlsLabels[db.TLSRequire] {
		t.Fatalf("form %+v", f)
	}
	tt.Frame()
	testutil.Snapshot(t, tt, "connection-from-url")
}

func TestSampleDatabase(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	testutil.Snapshot(t, tt, "welcome")
	if err := tt.Click("Try the sample"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "sample editor", func() bool { return len(a.tabs) == 1 })
	q := a.tabs[0].(*query.Tab)
	testutil.SetCaret(tt, &q.Editor, 80)
	q.Run(query.RunStatement)
	testutil.WaitFor(t, tt, "the sample query's 8 rows", func() bool { return resultRows(tt, q, 8) })

	tt.Frame()
	testutil.Snapshot(t, tt, "sample")
}

func TestDarkMode(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	tt.SetDark(true)
	a.openSample()
	testutil.WaitFor(t, tt, "sample editor", func() bool { return len(a.tabs) == 1 })
	q := a.tabs[0].(*query.Tab)
	testutil.SetCaret(tt, &q.Editor, 80)
	q.Run(query.RunStatement)
	testutil.WaitFor(t, tt, "sample query", func() bool { return resultRows(tt, q, -1) })

	testutil.Snapshot(t, tt, "dark-query")
	cn := q.Conn
	testutil.WaitFor(t, tt, "objects", func() bool { return cn.Objects[connection.SchemaKey{Database: "", Schema: "main"}] != nil })
	for _, o := range cn.Objects[connection.SchemaKey{Database: "", Schema: "main"}] {
		if o.Name == "orders" {
			a.OpenTable(cn, "", o, dataview.PageData)
		}
	}
	testutil.WaitFor(t, tt, "rows", func() bool { return testutil.HasTextContaining(tt, "rows loaded") })
	// A pending change and a chosen cell, as the user makes them: the
	// first row's ordered_at, down to the third row's status, edited.
	h, ok := tt.Find("ordered_at")
	if !ok {
		t.Fatal("no ordered_at column")
	}
	tt.ClickAt(h.X+10, h.Y+h.H+12)
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyRight)
	tt.Key(0, ui.KeyF2)
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("shipped")
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyLeft)
	tt.Frame()
	if !testutil.HasTextContaining(tt, "1 pending change") {
		t.Fatalf("no pending change: %q", tt.Texts())
	}
	testutil.Snapshot(t, tt, "dark-table")
}

func TestDropFiles(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1200, 760)
	dir := t.TempDir()
	file := dir + "/dropped.sqlite"
	os.WriteFile(file, nil, 0o600)
	a.openDropped([]string{file})
	testutil.WaitFor(t, tt, "connection", func() bool { return len(a.conns) == 1 && a.conns[0].Status == connection.StatusConnected })
	a.openDropped([]string{file}) // once only
	if len(a.conns) != 1 {
		t.Fatalf("the file was added twice: %d", len(a.conns))
	}
	sqlFile := dir + "/report.sql"
	os.WriteFile(sqlFile, []byte("SELECT 42 AS answer;"), 0o600)
	a.nav.row = -1
	a.openDropped([]string{sqlFile})
	testutil.WaitFor(t, tt, "editor", func() bool { return len(a.tabs) == 1 })
	if q := a.tabs[0].(*query.Tab); q.Path != sqlFile || q.Editor.Text != "SELECT 42 AS answer;" {
		t.Fatalf("editor %q %q", q.Path, q.Editor.Text)
	}
	csv := dir + "/people's data\nDROP TABLE x;.csv" // a quote and a newline in the name
	os.WriteFile(csv, []byte("name,age\nAda,36\nGrace,85\n"), 0o600)
	a.openDropped([]string{csv})
	testutil.WaitFor(t, tt, "the csv's 2 rows", func() bool {
		q, ok := a.ActiveTab().(*query.Tab)
		return ok && q.Conn.Config.Engine == db.DuckDB && resultRows(tt, q, 2)
	})

	testutil.Snapshot(t, tt, "drop-csv")
}

func TestAuditTrail(t *testing.T) {
	a := newTestApp(t)
	file := t.TempDir() + "/audited.sqlite"
	os.WriteFile(file, nil, 0o600)
	cfg := db.Config{ID: "lite", Name: "Ledger", Engine: db.SQLite, Database: file, Env: db.Production}
	cn := addConn(a, cfg)
	tt := ui.NewTester(a.view, 1280, 820)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	a.NewQueryTab(cn, "", "CREATE TABLE accounts (id INTEGER PRIMARY KEY, owner TEXT, balance INT);")
	testutil.WaitFor(t, tt, "tab", func() bool { return len(a.tabs) == 1 })
	q := a.tabs[0].(*query.Tab)
	testutil.SetCaret(tt, &q.Editor, 3)
	q.Run(query.RunStatement)
	tt.Frame()
	if a.confirm == nil {
		t.Fatal("CREATE on production ran without asking")
	}
	a.confirm.Open = false
	a.confirm.OnConfirm()
	a.confirm = nil
	testutil.WaitFor(t, tt, "create", func() bool { return !q.Running && q.Tx != db.TxNone })
	q.FinishTx(true, func(error) {})

	testutil.WaitFor(t, tt, "commit", func() bool { return !q.Running && q.Tx == db.TxNone })
	// A statement with a secret, and one the read-only guard refuses.
	q.Editor.Text = "ALTER USER app WITH PASSWORD 'hunter2';"
	testutil.SetCaret(tt, &q.Editor, 3)
	q.Run(query.RunStatement)
	testutil.WaitFor(t, tt, "select", func() bool { return !q.Running })
	ro := cn.Config
	cn.Config.ReadOnly = true
	q.Editor.Text = "DELETE FROM accounts WHERE id = 1;"
	testutil.SetCaret(tt, &q.Editor, 3)
	q.Run(query.RunStatement)
	a.alert = nil
	cn.Config = ro

	events, err := a.projects[0].Audit.Read(0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, e := range events {
		kinds[e.Kind]++
		if strings.Contains(e.Statement, "hunter2") {
			t.Errorf("a secret reached the audit log: %q", e.Statement)
		}
		if e.Connection != "Ledger" || e.Environment != "production" || e.User == "" {
			t.Errorf("entry without its context: %+v", e)
		}
	}
	for _, k := range []string{audit.KindConnect, audit.KindConfirm, audit.KindStatement, audit.KindBlocked} {
		if kinds[k] == 0 {
			t.Errorf("no %s entry: %v", k, kinds)
		}
	}
	if v, err := a.projects[0].Audit.Verify(); err != nil || v.Broken != 0 {
		t.Fatalf("verify: %+v %v", v, err)
	}
	a.openAudit()
	testutil.WaitFor(t, tt, "audit viewer", func() bool { return len(a.auditView.events) == len(events) })
	a.auditView.row = 1
	tt.Frame()
	testutil.Snapshot(t, tt, "audit")
	if err := tt.Click("Verify Integrity"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the check", func() bool { return tt.HasText(fmt.Sprintf("✓ Chain intact: %d entries", len(events))) })
	a.projects[0].Local.SQL().Exec("UPDATE audit SET event = replace(event, 'Ledger', 'Ledgr') WHERE seq = 2")
	tt.Click("Verify Integrity")
	testutil.WaitFor(t, tt, "the broken chain", func() bool {
		return tt.HasText("✗ Entry 2: the entry was changed after it was written")
	})
}

// A shared project file cannot have the app send an arbitrary environment
// variable, or a password kept for another host.
func TestProjectSecretsStayWithTheirHost(t *testing.T) {
	a := newTestApp(t)
	t.Setenv("GITHUB_TOKEN", "ghp_secret")
	t.Setenv("DGOPHER_PW", "allowed")
	evil := db.Config{ID: "p-x-evil", Name: "evil", Engine: db.Redis, Host: "attacker.example", PasswordEnv: "GITHUB_TOKEN"}
	cfg := evil
	a.loadSecrets(&cfg)
	if cfg.Password != "" {
		t.Fatal("a project connection read GITHUB_TOKEN")
	}
	ok := db.Config{ID: "p-x-ok", Name: "ok", Engine: db.Redis, Host: "h", PasswordEnv: "DGOPHER_PW"}
	a.loadSecrets(&ok)
	if ok.Password != "allowed" {
		t.Fatalf("DGOPHER_ variable: %q", ok.Password)
	}
	// A password kept for one host is not sent to another under the same ID.
	mine := db.Config{ID: "p-x-db", Name: "db", Engine: db.Postgres, Host: "db.internal", User: "app", Password: "kept"}
	a.saveSecrets(&mine, true)
	moved := mine
	moved.Password, moved.Host = "", "attacker.example"
	a.loadSecrets(&moved)
	if moved.Password != "" {
		t.Fatal("the password went to a changed host")
	}
	same := mine
	same.Password = ""
	a.loadSecrets(&same)
	if same.Password != "kept" {
		t.Fatalf("the password for its own host: %q", same.Password)
	}
}

// Saving a connection's name or environment keeps it connected; changing
// its host asks before closing tabs that would lose work.
func TestEditingAConnectionKeepsWork(t *testing.T) {
	a := newTestApp(t)
	file := t.TempDir() + "/k.sqlite"
	os.WriteFile(file, nil, 0o600)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file, Env: db.Development})
	tt := ui.NewTester(a.view, 1000, 700)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	q := query.New(a, cn, "", "q", "SELECT 1;")
	q.Path, q.Saved = file+".sql", ""
	q.DiskConflict = "changed on disk" // edits that cannot be saved
	a.AddTab(q)
	a.openConnForm(cn)
	a.connForm.cfg.Name = "renamed"
	a.connForm.env = 2
	a.saveConnForm(a.connForm, false)
	if cn.Status != connection.StatusConnected || len(a.tabs) != 1 || cn.Config.Env != db.Production {
		t.Fatalf("a rename disconnected: status %v, tabs %d", cn.Status, len(a.tabs))
	}
	other := t.TempDir() + "/other.sqlite"
	os.WriteFile(other, nil, 0o600)
	a.openConnForm(cn)
	a.connForm.cfg.Database = other
	a.saveConnForm(a.connForm, false)
	if a.closing == nil || len(a.tabs) != 1 {
		t.Fatalf("changing the file closed tabs without asking: closing %v, tabs %d", a.closing, len(a.tabs))
	}
	a.closing.onClose()
	if len(a.tabs) != 0 || cn.Config.Database != other {
		t.Fatalf("after agreeing: tabs %d, database %q", len(a.tabs), cn.Config.Database)
	}
}

// The session chosen in the activity stays chosen as refreshes reorder
// the sessions: Stop never reaches another one.
func TestActivityKeepsTheChosenSession(t *testing.T) {
	a := newTestApp(t)
	at := &activityTab{a: a, grid: dataview.NewGrid()}
	cols := []db.ColumnInfo{{Name: "pid"}, {Name: "user"}}
	at.adopt(dataview.Source{Cols: cols, Rows: [][]any{{int64(11), "ada"}, {int64(22), "grace"}}}, nil)
	at.grid.ViewOrder(&at.src)
	at.grid.SelRow = 1
	if row := at.selected(); db.Display(row[0]) != "22" {
		t.Fatalf("chose %v", row)
	}
	// Grace's session now comes first, a new one second.
	at.adopt(dataview.Source{Cols: cols, Rows: [][]any{{int64(22), "grace"}, {int64(33), "eve"}, {int64(11), "ada"}}}, nil)
	at.grid.ViewOrder(&at.src)
	if row := at.selected(); row == nil || db.Display(row[0]) != "22" {
		t.Fatalf("after the refresh, the choice is %v", row)
	}
	// Gone: nothing is chosen.
	at.adopt(dataview.Source{Cols: cols, Rows: [][]any{{int64(11), "ada"}}}, nil)
	at.grid.ViewOrder(&at.src)
	if row := at.selected(); row != nil {
		t.Fatalf("an ended session is still chosen: %v", row)
	}
}

// A file naming a connection the project lacks opens for its line to be
// fixed, on another connection that it never connects nor runs on.
func TestQueryFileHeaderMustNameAConnection(t *testing.T) {
	a := newTestApp(t)
	p := a.projects[0]
	prod := addConn(a, db.Config{ID: "prod", Name: "prod", Engine: db.SQLite, Database: ":memory:", Env: db.Production})
	a.nav.row = -1
	os.MkdirAll(p.Queries, 0o755)
	os.WriteFile(p.Queries+"/q.sql", []byte("-- connection: staging\nDELETE FROM t;"), 0o644)
	a.openQueryFile(p, "q.sql", nil)
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()
	q, ok := a.ActiveTab().(*query.Tab)
	if !ok || prod.Status != connection.StatusIdle || !tt.HasText("which is not a SQL connection of") {
		t.Fatalf("the file: tab %v, status %v, %q", ok, prod.Status, tt.Texts())
	}
	testutil.SetCaret(tt, &q.Editor, 30)
	q.Run(query.RunStatement)
	tt.Frame()
	if a.alert == nil || prod.Status != connection.StatusIdle {
		t.Fatalf("ran on a connection the file does not name: status %v", prod.Status)
	}
}

// Switching reopens the file on the connection it names, in the editor's
// place.
func TestSwitchConnection(t *testing.T) {
	a := newTestApp(t)
	alpha := addConn(a, db.Config{ID: "alpha", Name: "alpha", Engine: db.SQLite, Database: ":memory:"})
	beta := addConn(a, db.Config{ID: "beta", Name: "beta", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	a.NewQueryTab(alpha, "", "SELECT 1;")
	testutil.WaitFor(t, tt, "the editor", func() bool { _, ok := a.ActiveTab().(*query.Tab); return ok })
	a.NewQueryTab(alpha, "", "SELECT 2;")
	testutil.WaitFor(t, tt, "the second editor", func() bool { return len(a.tabs) == 2 })
	q := a.tabs[0].(*query.Tab)
	a.active = 0
	q.Editor.Text = strings.Replace(q.Editor.Text, "connection: alpha", "connection: beta", 1)
	tt.Frame()
	if err := tt.Click("Switch to beta"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	nq, ok := a.tabs[0].(*query.Tab)
	if !ok || nq == q || nq.Conn != beta || nq.Path != q.Path || a.active != 0 || len(a.tabs) != 2 {
		t.Fatalf("after switching: %v %v", ok, a.tabs)
	}
	if data, _ := os.ReadFile(q.Path); string(data) != nq.Editor.Text || !strings.HasPrefix(nq.Editor.Text, "-- connection: beta") {
		t.Fatalf("the file holds %q, the editor %q", data, nq.Editor.Text)
	}
	if tt.HasText("This file names") {
		t.Fatal("the bar stays after switching")
	}
}

// A shared connection asks before its first connect, and again when the
// project file sends it elsewhere.
func TestSharedConnectionAsksBeforeConnecting(t *testing.T) {
	a := newTestApp(t)
	file := t.TempDir() + "/shared.sqlite"
	os.WriteFile(file, nil, 0o600)
	cn := addConn(a, db.Config{ID: "s", Name: "shared", Engine: db.SQLite, Database: file})
	a.settings.TrustedShared = nil // as if it arrived in a pull
	tt := ui.NewTester(a.view, 1000, 700)
	a.Connect(cn, nil)
	if a.confirm == nil || cn.Status != connection.StatusIdle {
		t.Fatalf("connected without asking: status %v", cn.Status)
	}
	tt.Frame()
	testutil.Snapshot(t, tt, "shared-trust")
	a.confirm.OnConfirm()
	a.confirm = nil
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	a.disconnect(cn)
	a.Connect(cn, nil)
	if a.confirm != nil {
		t.Fatal("asked again for the same destination")
	}
	testutil.WaitFor(t, tt, "reconnect", func() bool { return cn.Status == connection.StatusConnected })
	a.disconnect(cn)
	cn.Config.Database = file + "-elsewhere"
	a.Connect(cn, nil)
	if a.confirm == nil {
		t.Fatal("a changed destination did not ask")
	}
}

// A connect that finishes after a disconnect is dropped.
func TestLateConnectIsDropped(t *testing.T) {
	a := newTestApp(t)
	file := t.TempDir() + "/late.sqlite"
	os.WriteFile(file, nil, 0o600)
	cn := addConn(a, db.Config{ID: "late", Name: "late", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.view, 1000, 700)
	a.Connect(cn, nil)
	a.disconnect(cn) // before the connect's result is applied
	time.Sleep(300 * time.Millisecond)
	tt.Frame()
	tt.Frame()
	if cn.Status != connection.StatusIdle || cn.DB != nil {
		t.Fatalf("a late connect was applied: status %v", cn.Status)
	}
}

// The dialogs keep their open flag in a local while they build: closing
// one must still reach the App.
func TestSettingsCloseWithEscape(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1200, 800)
	a.settingsOpen = true
	tt.Frame()
	if !tt.HasText("Settings") {
		t.Fatal("settings not shown")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.settingsOpen {
		t.Fatal("Escape left the settings open")
	}
}

// newAppEditor opens an editor on a new query file of the connection, as
// the active tab.
func newAppEditor(t *testing.T, a *App, tt *ui.Tester, cn *connection.Conn, text string) *query.Tab {
	t.Helper()
	n := len(a.tabs)
	a.NewQueryTab(cn, "", text)
	testutil.WaitFor(t, tt, "the editor", func() bool { return len(a.tabs) == n+1 })
	return a.ActiveTab().(*query.Tab)
}

// resultRows reports whether the editor's run ended with a result of n
// rows, any number when n < 0.
func resultRows(tt *ui.Tester, q *query.Tab, n int) bool {
	got, ok := resultCount(tt, q)
	return ok && (n < 0 || got == n)
}

// resultCount reads the row count of the editor's result from its status:
// all rows read, or a page with more to scroll to. A result cut short says
// so in other words and does not count.
func resultCount(tt *ui.Tester, q *query.Tab) (int, bool) {
	if q.Running {
		return 0, false
	}
	for _, s := range tt.Texts() {
		var n int
		if _, err := fmt.Sscanf(s, "%d row", &n); err == nil && (s == widgets.Count(n, "row") || s == widgets.Count(n, "row")+" loaded") {
			return n, true
		}
	}
	return 0, false
}

// A new editor on a Redis connection is its Redis tab, opened once.
func TestRedisTab(t *testing.T) {
	testutil.Integration(t)
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "r", Name: "Cache", Engine: db.Redis, Host: "127.0.0.1", Port: 16379, Database: "2", Env: db.Staging})
	tt := ui.NewTester(a.view, 1100, 760)
	a.NewQueryTab(cn, "", "")
	testutil.WaitFor(t, tt, "the redis tab", func() bool { return len(a.tabs) == 1 })
	if _, ok := a.tabs[0].(*redis.Tab); !ok {
		t.Fatalf("tab %T", a.tabs[0])
	}
	a.NewQueryTab(cn, "", "")
	tt.Frame()
	if len(a.tabs) != 1 || a.active != 0 {
		t.Fatalf("%d tabs, active %d", len(a.tabs), a.active)
	}
}

// Reading a table again with a change pending asks first, in the app's
// own dialog; Discard drops the change and reads the rows.
func TestPendingChangesDialog(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	a.openSample()
	testutil.WaitFor(t, tt, "sample editor", func() bool { return len(a.tabs) == 1 })
	cn := a.tabs[0].(*query.Tab).Conn
	testutil.WaitFor(t, tt, "objects", func() bool { return cn.Objects[connection.SchemaKey{Database: "", Schema: "main"}] != nil })
	for _, o := range cn.Objects[connection.SchemaKey{Database: "", Schema: "main"}] {
		if o.Name == "orders" {
			a.OpenTable(cn, "", o, dataview.PageData)
		}
	}
	testutil.WaitFor(t, tt, "rows", func() bool { return testutil.HasTextContaining(tt, " rows") && tt.HasText("status") })
	h, _ := tt.Find("status")
	tt.ClickAt(h.X+10, h.Y+h.H+12)
	tt.Key(0, ui.KeyF2)
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("changed")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if !tt.HasText("1 pending change") {
		t.Fatalf("no pending change: %q", tt.Texts())
	}
	tt.Key(0, ui.KeyF5)
	tt.Frame()
	if !tt.HasText("Apply 1 pending change first?") {
		t.Fatalf("no question: %q", tt.Texts())
	}
	testutil.Snapshot(t, tt, "pending-changes-dialog")
	if err := tt.Click("Discard"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the rows read again", func() bool { return !tt.HasText("1 pending change") && a.pending == nil })
}

// A run that drops two results with changes asks for each in turn: a
// Discard that asks the next question keeps it on screen, and the run
// starts after the last answer.
func TestPendingChangesOfTwoResults(t *testing.T) {
	a := newTestApp(t)
	file := t.TempDir() + "/two.sqlite"
	os.WriteFile(file, nil, 0o600)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.view, 1280, 800)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected && cn.DefaultSchema != "" })
	cn.DB.SQL.Exec("CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT)")
	cn.DB.SQL.Exec("INSERT INTO notes (title) VALUES ('first'), ('second')")
	q := newAppEditor(t, a, tt, cn, "SELECT * FROM notes")
	edit := func(cell string) {
		t.Helper()
		testutil.WaitFor(t, tt, "an editable result", func() bool { return tt.HasText("Double-click a cell, or press ↵, to edit") && tt.HasText(cell) })
		r, _ := tt.Find(cell)
		tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
		tt.Key(0, ui.KeyF2)
		tt.Key(ui.Cmd, ui.KeyA)
		tt.Type("changed")
		tt.Key(0, ui.KeyEnter)
		tt.Frame()
		if !tt.HasText("1 pending change") {
			t.Fatalf("no pending change: %q", tt.Texts())
		}
	}
	testutil.SetCaret(tt, &q.Editor, len([]rune(q.Editor.Text)))
	q.Run(query.RunStatement)
	edit("first")
	q.Run(query.RunNewTab)
	testutil.WaitFor(t, tt, "the second result", func() bool { return !q.Running && tt.HasText("Result 2") })
	edit("second")
	q.Run(query.RunStatement)
	tt.Frame()
	for i := range 2 {
		if !tt.HasText("Apply 1 pending change first?") {
			t.Fatalf("question %d not shown: %q", i+1, tt.Texts())
		}
		if err := tt.Click("Discard"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running && !tt.HasText("Result 2") && resultRows(tt, q, 2) })
}

func TestNotifyWanted(t *testing.T) {
	for _, c := range []struct {
		took    time.Duration
		after   int
		focused bool
		want    bool
	}{
		{12 * time.Second, 10, false, true},
		{12 * time.Second, 10, true, false}, // the window shows it already
		{8 * time.Second, 10, false, false},
		{time.Hour, 0, false, false}, // never
	} {
		if got := notifyWanted(c.took, c.after, c.focused); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

// Work that took long tells of its end while the window is in the
// background, and a click on the notification brings its tab forward;
// 0 seconds, which the defaults would otherwise replace, stays never.
func TestNotifyLongWork(t *testing.T) {
	a := newTestApp(t)
	var shown []string
	var click func()
	focused := false
	a.windowFocused = func() bool { return focused }
	a.notify = func(title, body string, onClick func()) { shown, click = append(shown, title+": "+body), onClick }
	tt := ui.NewTester(a.view, 1000, 700)
	a.Notify(time.Now().Add(-time.Minute), "Query finished", "q on lite", func() { a.active = 7 })
	focused = true
	a.Notify(time.Now().Add(-time.Minute), "Query finished", "seen", nil)
	focused = false
	a.Notify(time.Now(), "Query finished", "quick", nil)
	if len(shown) != 1 || shown[0] != "Query finished: q on lite" {
		t.Fatalf("notified %q", shown)
	}
	click()
	tt.Frame()
	if a.active != 7 {
		t.Fatal("the click did not show what ended")
	}

	a.settings.NotifyAfter = 0
	a.SaveSettings()
	b := newApp(a.st)
	if b.settings.NotifyAfter != 0 {
		t.Fatalf("never became %d seconds", b.settings.NotifyAfter)
	}
}

// runFile surveys a file of SQL, then runs it as the dialog's Run does,
// agreeing to what the policy asks.
func runFile(t *testing.T, a *App, tt *ui.Tester, cn *connection.Conn, path string, edit func(*sqlFileRun)) *sqlFileRun {
	t.Helper()
	a.startSQLFileRun(cn, "", path)
	x := a.sqlFile
	testutil.WaitFor(t, tt, "the survey", func() bool { return !x.reading })
	if edit != nil {
		edit(x)
	}
	a.confirmSQLFile(x)
	if a.confirm != nil {
		a.confirm.OnConfirm()
		a.confirm = nil
	}
	testutil.WaitFor(t, tt, "the run", func() bool { return !x.running })
	return x
}

// A file of SQL runs without an editor: read through once to say what it
// holds and ask once, then run as it streams; all or nothing when asked.
func TestRunSQLFile(t *testing.T) {
	a := newTestApp(t)
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.view, 1200, 800)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	var b strings.Builder
	b.WriteString("CREATE TABLE gone (a int);\nDROP TABLE gone;\nCREATE TABLE t (id integer primary key, note text);\n")
	for i := range 3000 {
		fmt.Fprintf(&b, "INSERT INTO t VALUES (%d, 'row; %d');\n", i, i)
	}
	path := filepath.Join(t.TempDir(), "dump.sql")
	os.WriteFile(path, []byte(b.String()), 0o600)

	a.startSQLFileRun(cn, "", path)
	x := a.sqlFile
	testutil.WaitFor(t, tt, "the survey", func() bool { return !x.reading })
	if x.statements != 3003 || x.verbs["INSERT"] != 3000 || len(x.dangerous) != 1 || !strings.HasPrefix(x.dangerous[0], "line 2: DROP TABLE gone") || !x.verdict.Confirm {
		t.Fatalf("survey: %d statements %v, dangerous %q, verdict %+v", x.statements, x.verbs, x.dangerous, x.verdict)
	}
	testutil.Snapshot(t, tt, "run-sql-file")
	x.open = false
	tt.Frame()

	x = runFile(t, a, tt, cn, path, nil)
	var n int
	cn.DB.SQL.QueryRow(`SELECT count(*) FROM t`).Scan(&n)
	if x.err != "" || n != 3000 || x.done.Load() != 3003 {
		t.Fatalf("run: %q, %d rows, %d statements", x.err, n, x.done.Load())
	}
	var script, drops int
	events, err := a.projects[0].Audit.Read(0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == audit.KindScript && strings.Contains(e.Detail, "sha256 "+x.hash) && e.Rows == 3000 {
			script++
		}
		if e.Kind == audit.KindStatement && e.Statement == "DROP TABLE gone" {
			drops++
		}
	}
	if script != 1 || drops != 1 {
		t.Fatalf("audited %d scripts, %d drops", script, drops)
	}

	// All or nothing: a failing row leaves nothing of the file.
	os.WriteFile(path, []byte("CREATE TABLE u (id integer primary key);\nINSERT INTO u VALUES (1);\nINSERT INTO u VALUES (1);\n"), 0o600)
	x = runFile(t, a, tt, cn, path, func(x *sqlFileRun) { x.oneTx = true })
	if !strings.Contains(x.err, "nothing of the file stays") || len(x.failures) != 1 || !strings.HasPrefix(x.failures[0], "Line 3:") {
		t.Fatalf("all or nothing: %q, %q", x.err, x.failures)
	}
	if err := cn.DB.SQL.QueryRow(`SELECT count(*) FROM u`).Scan(&n); err == nil {
		t.Fatal("the failed file left its table")
	}

	// A file changed since it was read is not run.
	a.startSQLFileRun(cn, "", path)
	x = a.sqlFile
	testutil.WaitFor(t, tt, "the survey", func() bool { return !x.reading })
	os.WriteFile(path, []byte("DROP TABLE t;\n"), 0o600)
	a.runSQLFile(x)
	if !strings.Contains(x.err, "changed since it was read") || x.running {
		t.Fatalf("a changed file: %q", x.err)
	}
}

// A dump of pg_dump runs: \restrict skipped, COPY's rows loaded.
func TestRunPostgresDump(t *testing.T) {
	testutil.Integration(t)
	a := newTestApp(t)
	cn := addConn(a, testutil.PGConfig())
	tt := ui.NewTester(a.view, 1200, 800)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	cn.DB.SQL.Exec(`DROP TABLE IF EXISTS public.it_dump`)
	defer cn.DB.SQL.Exec(`DROP TABLE IF EXISTS public.it_dump`)
	dump := "\\restrict k\n\nSET client_encoding = 'UTF8';\nCREATE TABLE public.it_dump (a integer, b text);\n\nCOPY public.it_dump (a, b) FROM stdin;\n1\tsemi; colon\n2\t\\N\n\\.\n\n\\unrestrict k\n"
	path := filepath.Join(t.TempDir(), "dump.sql")
	os.WriteFile(path, []byte(dump), 0o600)
	x := runFile(t, a, tt, cn, path, nil)
	var n int
	var nulls int
	cn.DB.SQL.QueryRow(`SELECT count(*), count(*) FILTER (WHERE b IS NULL) FROM public.it_dump`).Scan(&n, &nulls)
	if x.err != "" || n != 2 || nulls != 1 {
		t.Fatalf("dump: %q %q, %d rows, %d NULL", x.err, x.failures, n, nulls)
	}
}

// The Locks page shows a session waiting for a lock, and the session it
// waits for.
func TestLocksShowWhoWaits(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	for _, c := range []struct {
		cfg               db.Config
		setup, hold, wait []string
	}{
		{cfg: testutil.PGConfig(),
			hold: []string{"BEGIN", "LOCK TABLE shop.customers IN ACCESS EXCLUSIVE MODE"},
			wait: []string{"SELECT count(*) FROM shop.customers"}},
		{cfg: db.Config{ID: "my", Name: "my", Engine: db.MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop"},
			setup: []string{"DROP TABLE IF EXISTS it_locks", "CREATE TABLE it_locks (id int primary key, n int)", "INSERT INTO it_locks VALUES (1, 0)"},
			hold:  []string{"START TRANSACTION", "SELECT * FROM it_locks WHERE id = 1 FOR UPDATE"},
			wait:  []string{"UPDATE it_locks SET n = 1 WHERE id = 1"}},
	} {
		t.Run(string(c.cfg.Engine), func(t *testing.T) {
			ctx := context.Background()
			d, err := db.Open(ctx, c.cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			for _, q := range c.setup {
				if _, err := d.SQL.ExecContext(ctx, q); err != nil {
					t.Fatal(q, err)
				}
			}
			holder, _ := d.SQL.Conn(ctx)
			waiter, _ := d.SQL.Conn(ctx)
			for _, q := range c.hold {
				if _, err := holder.ExecContext(ctx, q); err != nil {
					t.Fatal(q, err)
				}
			}
			waited := make(chan error, 1)
			go func() {
				_, err := waiter.ExecContext(ctx, c.wait[0])
				waited <- err
			}()
			defer func() {
				holder.ExecContext(ctx, "ROLLBACK")
				<-waited
				holder.Close()
				waiter.Close()
			}()
			a := newTestApp(t)
			cn := addConn(a, c.cfg)
			tt := ui.NewTester(a.view, 1360, 760)
			a.openActivity(cn)
			testutil.WaitFor(t, tt, "the activity", func() bool { _, ok := a.ActiveTab().(*activityTab); return ok })
			at := a.ActiveTab().(*activityTab)
			at.showPage(slices.IndexFunc(at.pages, func(p activityPage) bool { return p.name == "Locks" }))
			waitsFor := -1
			testutil.WaitFor(t, tt, "a lock waited for", func() bool {
				if at.loading || at.src.Cols == nil {
					return false
				}
				waitsFor = slices.IndexFunc(at.src.Cols, func(col db.ColumnInfo) bool { return col.Name == "waits_for" })
				for _, r := range at.src.Rows {
					if r[waitsFor] != nil && db.Display(r[waitsFor]) != "" {
						return true
					}
				}
				at.refresh()
				return false
			})
			if at.err != "" {
				t.Fatal(at.err)
			}
		})
	}
}

// A table's maintenance runs in an editor, through the policy: on
// production, where manual commit opens a transaction before a write,
// VACUUM, which no transaction may hold, runs without one.
func TestMaintenanceOnProduction(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := newTestApp(t)
	cfg := testutil.PGConfig()
	cfg.Env = db.Production
	cn := addConn(a, cfg)
	tt := ui.NewTester(a.view, 1200, 800)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	cmds := maintenanceCommands(db.Postgres, db.QualifiedName(cn.DB.Dialect, "shop", "orders"))
	vacuum := cmds[slices.IndexFunc(cmds, func(m maintenance) bool { return m.label == "Vacuum and Analyze" })]
	a.runInEditor(cn, "", vacuum.sql)
	testutil.WaitFor(t, tt, "the confirmation", func() bool { return a.confirm != nil })
	a.confirm.OnConfirm()
	a.confirm = nil
	q := a.ActiveTab().(*query.Tab)
	testutil.WaitFor(t, tt, "the vacuum", func() bool { return !q.Running && testutil.HasTextContaining(tt, "VACUUM ·") })
	if testutil.HasTextContaining(tt, "transaction block") || q.Tx != db.TxNone {
		t.Fatalf("vacuum: transaction %v, %q", q.Tx, tt.Texts())
	}
}

// A table and a column are renamed from the navigator, through the
// policy: at once in development, once agreed on production.
func TestRenameInDatabase(t *testing.T) {
	a := newTestApp(t)
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file, Env: db.Development})
	tt := ui.NewTester(a.view, 1200, 800)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	if _, err := cn.DB.SQL.Exec(`CREATE TABLE orders (id INTEGER PRIMARY KEY, total REAL)`); err != nil {
		t.Fatal(err)
	}
	obj := db.Object{Schema: "main", Name: "orders", Kind: db.KindTable}
	a.askRenameInDatabase(cn, "", "orders", func(name string) (string, error) {
		return db.RenameObjectSQL(cn.DB.Dialect, obj, name)
	})
	if err := a.renaming.rename(" "); err == nil {
		t.Fatal("an empty name was taken")
	}
	if err := a.renaming.rename("sales"); err != nil {
		t.Fatal(err)
	}
	var n int
	testutil.WaitFor(t, tt, "the table renamed", func() bool {
		return cn.DB.SQL.QueryRow(`SELECT count(*) FROM sales`).Scan(&n) == nil
	})

	cn.Config.Env = db.Production
	a.askRenameInDatabase(cn, "", "total", func(name string) (string, error) {
		return db.RenameColumnSQL(cn.DB.Dialect, "main", "sales", "total", name), nil
	})
	if err := a.renaming.rename("amount"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the confirmation", func() bool { return a.confirm != nil })
	if !strings.Contains(a.confirm.Preview, `RENAME COLUMN "total" TO "amount"`) {
		t.Fatalf("confirmation: %q", a.confirm.Preview)
	}
	a.confirm.OnConfirm()
	a.confirm = nil
	testutil.WaitFor(t, tt, "the column renamed", func() bool {
		return cn.DB.SQL.QueryRow(`SELECT count(amount) FROM sales`).Scan(&n) == nil
	})
}

// The search of a database finds objects by name, and views by their
// query, and opens what it found.
func TestSearchObjects(t *testing.T) {
	a := newTestApp(t)
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.view, 1200, 800)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	for _, q := range []string{`CREATE TABLE invoices (id INTEGER PRIMARY KEY, customer TEXT)`,
		`CREATE VIEW unpaid AS SELECT id FROM invoices WHERE customer = 'acme'`} {
		if _, err := cn.DB.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	a.openSearch(cn, "")
	testutil.WaitFor(t, tt, "the search tab", func() bool { _, ok := a.ActiveTab().(*searchTab); return ok })
	st := a.ActiveTab().(*searchTab)
	st.query = "custom"
	st.search()
	testutil.WaitFor(t, tt, "the hits", func() bool { return !st.searching && tt.HasText("invoices.customer") })
	if len(st.hits) != 2 || st.hits[1].Name != "unpaid" { // its query names the column
		t.Fatalf("hits %+v", st.hits)
	}
	st.query = "ACME"
	st.search()
	testutil.WaitFor(t, tt, "the view", func() bool { return !st.searching && st.searched == "ACME" })
	if len(st.hits) != 1 || st.hits[0].Name != "unpaid" || !tt.HasText(st.hits[0].Excerpt) {
		t.Fatalf("hits %+v", st.hits)
	}
	testutil.Snapshot(t, tt, "search-objects")
	st.open(st.hits[0])
	testutil.WaitFor(t, tt, "the view's definition", func() bool {
		v, ok := a.ActiveTab().(*dataview.TableTab)
		return ok && v.Object.Name == "unpaid" && v.Page == dataview.PageDDL
	})
}

// The generate dialog writes the script of the objects chosen into an
// editor, in an order that runs.
func TestGenerateScript(t *testing.T) {
	a := newTestApp(t)
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.view, 1200, 800)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	for _, q := range []string{`CREATE TABLE lines (order_id INTEGER REFERENCES orders (id))`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY)`, `CREATE TABLE scratch (x)`} {
		if _, err := cn.DB.SQL.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	a.openGenerate(cn, "", "main", generateScript)
	g := a.generating
	testutil.WaitFor(t, tt, "the objects", func() bool { return !g.loading && tt.HasText("scratch") })
	testutil.Snapshot(t, tt, "generate-script")
	g.filter = "scr"
	tt.Frame()
	if err := tt.Click("None"); err != nil {
		t.Fatal(err)
	}
	g.filter = ""
	if err := tt.Click("Open in Editor"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the script", func() bool { _, ok := a.ActiveTab().(*query.Tab); return ok })
	text := a.ActiveTab().(*query.Tab).Editor.Text
	if strings.Contains(text, "scratch") || strings.Index(text, "CREATE TABLE orders") > strings.Index(text, "CREATE TABLE lines") {
		t.Fatalf("script:\n%s", text)
	}
	if a.generating != nil {
		t.Fatal("the dialog stays open")
	}
}

// The catalog queries the app runs for the navigator show, the newest
// first.
func TestCatalogQueries(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1200, 800)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	connection.LoadObjects(a, cn, "", "main")
	testutil.WaitFor(t, tt, "the tables", func() bool { _, ok := cn.Objects[connection.SchemaKey{Schema: "main"}]; return ok })
	a.openCatalogQueries(cn)
	testutil.WaitFor(t, tt, "the log", func() bool { return testutil.HasTextContaining(tt, "FROM \"main\".sqlite_master") })
	testutil.Snapshot(t, tt, "catalog-queries")
}

// Two tabs show side by side; the one in front takes the keys, and the
// other comes in front, where it is, as the focus goes into it.
func TestSideBySide(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1300, 800)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	left := query.New(a, cn, "", "left", "SELECT 'left one';")
	right := query.New(a, cn, "", "right", "SELECT 'right one';")
	a.AddTab(right)
	a.AddTab(left)
	a.openToSide(right)
	tt.Frame()
	testutil.Snapshot(t, tt, "side-by-side")
	if a.ActiveTab() != left || !a.KeysTo(left) || a.KeysTo(right) || a.sideTab() != right {
		t.Fatalf("active %v, side %v", a.ActiveTab(), a.side)
	}
	right.Editor.WantFocus = true
	tt.Frame()
	tt.Frame()
	if a.ActiveTab() != right || a.side != left || !a.sideLeft {
		t.Fatalf("after focusing the side: active %v, side %v, left %v", a.ActiveTab(), a.side, a.sideLeft)
	}
	ran := func(sql string) bool {
		events, _ := cn.Project.Audit.Read(0)
		return slices.ContainsFunc(events, func(e audit.Event) bool { return strings.Contains(e.Statement, sql) })
	}
	tt.Key(ui.Cmd, ui.KeyEnter)
	testutil.WaitFor(t, tt, "the run", func() bool { return !right.Running && ran("right one") })
	if ran("left one") {
		t.Fatal("the editor beside ran too")
	}
	a.closeTab(slices.IndexFunc(a.tabs, func(t widgets.Tab) bool { return t == left }))
	tt.Frame()
	if a.sideTab() != nil || a.ActiveTab() != right {
		t.Fatalf("after closing the side: side %v", a.side)
	}
}

// A SQLite database backs up as a copy, audited; a SQL file restores as
// Run SQL File runs it.
func TestBackup(t *testing.T) {
	a := newTestApp(t)
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.view, 1200, 800)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	cn.DB.SQL.Exec(`CREATE TABLE t (a INT)`)
	a.openBackup(cn, "", false)
	b := a.backup
	b.path = filepath.Join(t.TempDir(), "copy.sqlite")
	tt.Frame()
	testutil.Snapshot(t, tt, "backup")
	if err := tt.Click("Back Up"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the backup", func() bool { return b.done || b.err != "" })
	if b.err != "" {
		t.Fatal(b.err)
	}
	if _, err := os.Stat(b.path); err != nil {
		t.Fatal(err)
	}
	events, _ := cn.Project.Audit.Read(0)
	if last := events[0]; last.Kind != audit.KindBackup || !strings.Contains(last.Statement, "VACUUM INTO") {
		t.Fatalf("audited %+v", last)
	}
	sql := filepath.Join(t.TempDir(), "rows.sql")
	os.WriteFile(sql, []byte("INSERT INTO t VALUES (1);\n"), 0o600)
	a.backup = nil
	a.openBackup(cn, "", true)
	a.backup.path = sql
	a.startBackup(a.backup)
	if a.sqlFile == nil || a.sqlFile.path != sql {
		t.Fatalf("the SQL file is not run: %+v", a.sqlFile)
	}
}

// copyTables copies tables of a schema into a target, in the mode
// given, and waits for the copy to end.
func copyTables(t *testing.T, a *App, tt *ui.Tester, from *connection.Conn, schema string, tables []string, to *connection.Conn, toSchema string, mode int) *copyDialog {
	t.Helper()
	a.openCopy(from, "", schema, tables)
	x := a.copying
	x.target.label = targetLabel(to)
	tt.Frame()
	x.target.schema, x.mode = toSchema, mode
	tt.Frame()
	a.startCopy(x)
	if a.confirm != nil {
		a.confirm.OnConfirm()
		a.confirm = nil
	}
	testutil.WaitFor(t, tt, "the copy", func() bool { return !x.running && (x.done || x.err != "") })
	return x
}

// Tables copy into another engine, made there with their types mapped,
// or into one there already, their rows added or replaced.
func TestCopyTables(t *testing.T) {
	a := newTestApp(t)
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	lite := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	duck := addConn(a, db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1200, 800)
	for _, cn := range []*connection.Conn{lite, duck} {
		a.Connect(cn, nil)
		testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	}
	for _, q := range []string{`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, price REAL, sold BOOLEAN, at DATE, raw BLOB)`,
		`INSERT INTO items VALUES (1, 'pen', 1.5, 1, '2026-01-31', x'0102'), (2, 'ink', NULL, 0, NULL, NULL)`} {
		if _, err := lite.DB.SQL.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	x := copyTables(t, a, tt, lite, "main", []string{"items"}, duck, "main", copyCreate)
	testutil.Snapshot(t, tt, "copy-tables")
	if x.err != "" {
		t.Fatal(x.err)
	}
	var name, at string
	var price float64
	var sold bool
	var raw []byte
	if err := duck.DB.SQL.QueryRow(`SELECT name, price, sold, CAST("at" AS VARCHAR), raw FROM items WHERE id = 1`).Scan(&name, &price, &sold, &at, &raw); err != nil ||
		name != "pen" || price != 1.5 || !sold || at != "2026-01-31" || string(raw) != "\x01\x02" {
		t.Fatalf("copied %q %v %v %q %v: %v", name, price, sold, at, raw, err)
	}
	x = copyTables(t, a, tt, lite, "main", []string{"items"}, duck, "main", copyCreate)
	if !strings.Contains(x.err, "already") {
		t.Fatalf("a table there: %q", x.err)
	}
	lite.DB.SQL.Exec(`INSERT INTO items (id, name) VALUES (3, 'cap')`)
	x = copyTables(t, a, tt, lite, "main", []string{"items"}, duck, "main", copyReplace)
	var n int
	if duck.DB.SQL.QueryRow(`SELECT count(*) FROM items`).Scan(&n); x.err != "" || n != 3 {
		t.Fatalf("replaced: %d rows, %q", n, x.err)
	}
}

// A PostgreSQL table copies into MySQL and ClickHouse, its numbers,
// times and booleans as they were.
func TestCopyTablesAcrossServers(t *testing.T) {
	testutil.Integration(t)
	a := newTestApp(t)
	pg := addConn(a, testutil.PGConfig())
	my := addConn(a, db.Config{ID: "my", Name: "my", Engine: db.MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop", Env: db.Development})
	ch := addConn(a, db.Config{ID: "ch", Name: "ch", Engine: db.ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dbgopher", Env: db.Development})
	tt := ui.NewTester(a.view, 1200, 800)
	for _, cn := range []*connection.Conn{pg, my, ch} {
		a.Connect(cn, nil)
		testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	}
	drop := func() {
		pg.DB.SQL.Exec(`DROP TABLE IF EXISTS public.it_copy`)
		my.DB.SQL.Exec("DROP TABLE IF EXISTS shop.it_copy")
		ch.DB.SQL.Exec("DROP TABLE IF EXISTS default.it_copy")
	}
	drop()
	defer drop()
	for _, q := range []string{`CREATE TABLE public.it_copy (id int PRIMARY KEY, amount numeric(10,2), at timestamptz, ok boolean, note text)`,
		`INSERT INTO public.it_copy VALUES (1, 12345678.91, '2026-01-31 10:20:30.123456+00', true, 'it''s'), (2, NULL, NULL, NULL, NULL)`} {
		if _, err := pg.DB.SQL.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	for _, c := range []struct {
		to     *connection.Conn
		schema string
		query  string
	}{
		{my, "shop", "SELECT CAST(amount AS CHAR), DATE_FORMAT(at, '%Y-%m-%d %H:%i:%s.%f'), ok, note FROM shop.it_copy WHERE id = 1"},
		{ch, "default", "SELECT toString(amount), toString(at), toString(ok), note FROM default.it_copy WHERE id = 1"},
	} {
		x := copyTables(t, a, tt, pg, "public", []string{"it_copy"}, c.to, c.schema, copyCreate)
		if x.err != "" {
			t.Fatalf("%s: %s", c.to.Config.Engine, x.err)
		}
		var amount, at, ok, note string
		if err := c.to.DB.SQL.QueryRow(c.query).Scan(&amount, &at, &ok, &note); err != nil {
			t.Fatalf("%s: %v", c.to.Config.Engine, err)
		}
		if amount != "12345678.91" || !strings.HasPrefix(at, "2026-01-31 10:20:30.123456") || note != "it's" || ok != "1" && ok != "true" {
			t.Fatalf("%s: %q %q %q %q", c.to.Config.Engine, amount, at, ok, note)
		}
	}
}

// Users are made, granted, taken back from and dropped from their tab;
// a password shows nowhere: not in the confirmation, the audit log or a
// file.
func TestUsersTab(t *testing.T) {
	testutil.Integration(t)
	testutil.SeedPostgres(t)
	a := newTestApp(t)
	cn := addConn(a, testutil.PGConfig())
	tt := ui.NewTester(a.view, 1300, 800)
	const secret = "Tr0ub4dor&3-it"
	cleanup := func() {
		cn.DB.SQL.Exec(`REASSIGN OWNED BY dgopher_it_grace TO CURRENT_USER; DROP OWNED BY dgopher_it_grace`)
		cn.DB.SQL.Exec(`DROP ROLE IF EXISTS dgopher_it_grace`)
	}
	a.openUsers(cn)
	testutil.WaitFor(t, tt, "the users", func() bool {
		ut, ok := a.ActiveTab().(*usersTab)
		return ok && !ut.loading && len(ut.accounts) > 0
	})
	cleanup()
	defer cleanup()
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

	ut.openForm(formGrant)
	ut.form.schema, ut.form.table = "shop", "orders"
	ut.submit(ut.form)
	agree("grant")
	testutil.WaitFor(t, tt, "the privilege", func() bool { return tt.HasText("SELECT on table shop.orders") })
	if err := tt.Click("Revoke SELECT on table shop.orders"); err != nil {
		t.Fatal(err)
	}
	agree("revoke")
	testutil.WaitFor(t, tt, "the revoke", func() bool { return !ut.loading && !tt.HasText("SELECT on table shop.orders") })
	if err := tt.Click("Drop…"); err != nil {
		t.Fatal(err)
	}
	agree("drop")
	testutil.WaitFor(t, tt, "the drop", func() bool {
		return !ut.loading && !slices.ContainsFunc(ut.accounts, func(acc db.Account) bool { return acc.Name == "dgopher_it_grace" })
	})

	events, _ := a.projects[0].Audit.Read(0)
	for _, e := range events {
		if strings.Contains(e.Statement, secret) || strings.Contains(e.Detail, secret) {
			t.Fatalf("the audit log holds the password: %+v", e)
		}
	}
	files, _ := filepath.Glob(filepath.Join(a.projects[0].Queries, "*.sql"))
	if len(files) != 0 {
		t.Fatalf("query files made: %v", files)
	}
}

// A schema's routines and the partitions of its tables show in the
// navigator, and open: a routine's definition in an editor, a
// partition's rows.
func TestNavigatorItems(t *testing.T) {
	testutil.Integration(t)
	a := newTestApp(t)
	cn := addConn(a, testutil.PGConfig())
	tt := ui.NewTester(a.view, 1200, 900)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	for _, q := range []string{"DROP SCHEMA IF EXISTS it_nav CASCADE", "CREATE SCHEMA it_nav",
		"CREATE FUNCTION it_nav.twice(a int) RETURNS int LANGUAGE sql AS 'SELECT a * 2'",
		"CREATE TABLE it_nav.events (id int, at date) PARTITION BY RANGE (at)",
		"CREATE TABLE it_nav.events_2026 PARTITION OF it_nav.events FOR VALUES FROM ('2026-01-01') TO ('2027-01-01')"} {
		if _, err := cn.DB.SQL.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	defer cn.DB.SQL.Exec("DROP SCHEMA IF EXISTS it_nav CASCADE")
	a.refresh(cn)
	schema := navNode{kind: nodeSchema, conn: cn.Config.ID, schema: "it_nav"}
	a.nav.expand(navNode{kind: nodeConn, conn: cn.Config.ID})
	a.nav.expand(schema)
	a.nav.expand(navNode{kind: nodeFolder, conn: cn.Config.ID, schema: "it_nav", folder: string(db.ItemFunction)})
	a.nav.expand(navNode{kind: nodeFolder, conn: cn.Config.ID, schema: "it_nav", folder: folderTables})
	a.nav.expand(navNode{kind: nodeObject, conn: cn.Config.ID, schema: "it_nav", name: "events"})
	a.nav.expand(navNode{kind: nodeFolder, conn: cn.Config.ID, schema: "it_nav", name: "events", folder: folderPartitions})
	testutil.WaitFor(t, tt, "the items", func() bool {
		return tt.HasText("Functions") && tt.HasText("twice(a integer)") && tt.HasText("Partitions") && tt.HasText("events_2026")
	})
	testutil.Snapshot(t, tt, "navigator-items")
	fn := navNode{kind: nodeItem, conn: cn.Config.ID, schema: "it_nav", name: itemName(db.Item{Kind: db.ItemFunction, Name: "twice", Detail: "a integer"})}
	a.activate(fn)
	testutil.WaitFor(t, tt, "the definition", func() bool {
		q, ok := a.ActiveTab().(*query.Tab)
		return ok && strings.Contains(q.Editor.Text, "CREATE OR REPLACE FUNCTION it_nav.twice(a integer)")
	})
	part := navNode{kind: nodeItem, conn: cn.Config.ID, schema: "it_nav", name: itemName(db.Item{Kind: db.ItemPartition, Name: "events_2026", Table: "events"})}
	a.activate(part)
	testutil.WaitFor(t, tt, "the partition's rows", func() bool {
		tb, ok := a.ActiveTab().(*dataview.TableTab)
		return ok && tb.Object.Name == "events_2026"
	})
}

func TestPartitionTemplate(t *testing.T) {
	d := db.DialectOf(db.Postgres)
	for keydef, bounds := range map[string]string{"RANGE (at)": "FROM ('…') TO ('…')", "LIST (region)": "IN ('…')", "HASH (id)": "WITH (MODULUS 4, REMAINDER 0)"} {
		got := partitionTemplate(d, db.Object{Schema: "s", Name: "events", Partitioning: keydef})
		if !strings.Contains(got, `CREATE TABLE "s"."events_new"`) || !strings.Contains(got, `PARTITION OF "s"."events"`) || !strings.Contains(got, "FOR VALUES "+bounds) {
			t.Errorf("%s:\n%s", keydef, got)
		}
	}
}

// A table's rows compare with another engine's table, and the other's are
// made as the first's, in one transaction, or open as SQL.
func TestCompareRows(t *testing.T) {
	a := newTestApp(t)
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	lite := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	duck := addConn(a, db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1200, 800)
	for _, cn := range []*connection.Conn{lite, duck} {
		a.Connect(cn, nil)
		testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	}
	for _, q := range []string{`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, secret_token TEXT)`,
		`INSERT INTO items VALUES (1, 'pen', 'a'), (2, 'ink', 'b'), (3, 'pad', 'c')`} {
		if _, err := lite.DB.SQL.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	for _, q := range []string{`CREATE TABLE items (id INTEGER PRIMARY KEY, name VARCHAR, secret_token VARCHAR)`,
		`INSERT INTO items VALUES (1, 'pen', 'a'), (2, 'quill', 'z'), (4, 'nib', 'd')`} {
		if _, err := duck.DB.SQL.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	a.openRowCompare(lite, "", db.Object{Schema: "main", Name: "items", Kind: db.KindTable})
	x := a.rowCompare
	x.target.label = targetLabel(duck)
	testutil.WaitFor(t, tt, "the table chosen", func() bool { return x.table == "items" })
	a.startCompare(x)
	testutil.WaitFor(t, tt, "the comparison", func() bool { return !x.running })
	testutil.Snapshot(t, tt, "compare-rows")
	r := x.result
	if x.err != "" || r.Same != 1 || r.Changed != 1 || r.OnlyInSource != 1 || r.OnlyInTarget != 1 {
		t.Fatalf("%q: %+v", x.err, r)
	}
	if secret := slices.Index(r.Columns, "secret_token"); !x.masked[secret] || x.valueText("z", secret) != dataview.MaskedText {
		t.Fatal("a sensitive column's values show")
	}

	if a.openSyncSQL(x); !strings.Contains(x.err, "hides") {
		t.Fatalf("hidden values went into an editor: %q", x.err)
	}
	x.err = ""
	a.startSync(x)
	if a.confirm == nil {
		t.Fatal("the sync is not confirmed")
	}
	if p := a.confirm.Preview; strings.Contains(p, "'b'") || !strings.Contains(p, dataview.MaskedText) {
		t.Fatalf("a hidden value shows in the preview: %s", p)
	}
	a.confirm.OnConfirm()
	a.confirm = nil
	testutil.WaitFor(t, tt, "the sync", func() bool { return !x.running && x.applied != "" })
	var names []string
	rows, err := duck.DB.SQL.Query(`SELECT name FROM items ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		names = append(names, n)
	}
	rows.Close()
	// The row only in the target stays: deleting it was not chosen.
	if want := []string{"pen", "ink", "pad", "nib"}; !slices.Equal(names, want) {
		t.Fatalf("synced to %v, want %v", names, want)
	}
	if r := x.result; r.Same != 3 || r.OnlyInTarget != 1 || r.Changed+r.OnlyInSource != 0 {
		t.Fatalf("compared again: %+v", r)
	}

	x.remove = true
	a.openSyncSQL(x)
	q, ok := a.ActiveTab().(*query.Tab)
	if !ok || !strings.Contains(q.Editor.Text, `DELETE FROM "main"."items" WHERE "id" = 4;`) {
		t.Fatalf("the sync's SQL: %v", a.ActiveTab())
	}
}

// fillTable fills a table with generated rows, as the dialog suggests
// them after set changes them, and waits for the fill to end.
func fillTable(t *testing.T, a *App, tt *ui.Tester, cn *connection.Conn, schema, table, rows string, set func(x *fillDialog)) *fillDialog {
	t.Helper()
	a.openFill(cn, "", db.Object{Schema: schema, Name: table, Kind: db.KindTable})
	x := a.filling
	testutil.WaitFor(t, tt, "the columns", func() bool { return !x.loading })
	if x.err != "" {
		t.Fatalf("%s: %s", table, x.err)
	}
	x.count = rows
	if set != nil {
		set(x)
	}
	tt.Frame()
	a.startFill(x)
	if a.confirm != nil {
		a.confirm.OnConfirm()
		a.confirm = nil
	}
	testutil.WaitFor(t, tt, "the fill", func() bool { return !x.running })
	return x
}

func queryInt(t *testing.T, d *db.DB, q string) int {
	t.Helper()
	var n int
	if err := d.SQL.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(q, err)
	}
	return n
}

// Generated rows fill tables: unique keys and e-mails, values in their
// ranges, foreign keys taking the values of the table they refer to.
func TestFillTables(t *testing.T) {
	a := newTestApp(t)
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	lite := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.view, 1200, 800)
	a.Connect(lite, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return lite.Status == connection.StatusConnected })
	for _, q := range []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE customers (id INTEGER PRIMARY KEY, email TEXT NOT NULL UNIQUE, name VARCHAR(12), age INTEGER, born DATE, active BOOLEAN, note TEXT)`,
		`CREATE TABLE orders (code TEXT PRIMARY KEY, customer_id INTEGER NOT NULL REFERENCES customers (id), total NUMERIC(6,2))`,
	} {
		if _, err := lite.DB.SQL.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	x := fillTable(t, a, tt, lite, "main", "orders", "5", nil)
	if !strings.Contains(x.err, "no rows to refer to") {
		t.Fatalf("orders without customers: %q", x.err)
	}
	a.filling = nil
	x = fillTable(t, a, tt, lite, "main", "customers", "300", func(x *fillDialog) {
		testutil.Snapshot(t, tt, "fill-table")
		for i := range x.cols {
			if x.cols[i].col.Name == "note" {
				x.cols[i].nulls = "100"
			}
		}
	})
	if x.err != "" {
		t.Fatal(x.err)
	}
	d := lite.DB
	if n := queryInt(t, d, `SELECT count(DISTINCT email) FROM customers`); n != 300 {
		t.Fatalf("%d distinct e-mails of 300", n)
	}
	if n := queryInt(t, d, `SELECT count(*) FROM customers WHERE age NOT BETWEEN 18 AND 90 OR length(name) > 12 OR note IS NOT NULL OR active NOT IN (0, 1) OR born NOT LIKE '____-__-__'`); n != 0 {
		t.Fatalf("%d customers out of their ranges", n)
	}
	x = fillTable(t, a, tt, lite, "main", "orders", "1000", nil)
	if x.err != "" {
		t.Fatal(x.err)
	}
	if n := queryInt(t, d, `SELECT count(*) FROM orders o JOIN customers c ON c.id = o.customer_id WHERE o.total BETWEEN 0 AND 9999.99`); n != 1000 {
		t.Fatalf("%d of 1000 orders refer to a customer, their totals in range", n)
	}
	// A second fill's unique values differ from the first's.
	if x = fillTable(t, a, tt, lite, "main", "customers", "300", nil); x.err != "" {
		t.Fatal(x.err)
	}
	if n := queryInt(t, d, `SELECT count(DISTINCT email) FROM customers`); n != 600 {
		t.Fatalf("%d distinct e-mails of 600", n)
	}
	// A type no generator writes, which the column must have, stops the
	// fill before it starts.
	if _, err := d.SQL.Exec(`CREATE TABLE spans (id INTEGER PRIMARY KEY, span INET NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if x = fillTable(t, a, tt, lite, "main", "spans", "5", nil); !strings.Contains(x.err, "span: it has no default") {
		t.Fatalf("a column left out that must have a value: %q", x.err)
	}
}

// Generated rows fill PostgreSQL's and MySQL's types: enums, UUIDs, JSON,
// times with their zone, booleans, decimals; keys continue from the
// highest.
func TestFillTablesOnServers(t *testing.T) {
	testutil.Integration(t)
	a := newTestApp(t)
	pg := addConn(a, testutil.PGConfig())
	my := addConn(a, db.Config{ID: "my", Name: "my", Engine: db.MySQL, Host: "127.0.0.1", Port: 13306, User: "root", Password: "dbgopher", Database: "shop", Env: db.Development})
	tt := ui.NewTester(a.view, 1200, 800)
	for _, cn := range []*connection.Conn{pg, my} {
		a.Connect(cn, nil)
		testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	}
	setup := map[*connection.Conn][]string{
		pg: {`DROP TABLE IF EXISTS public.it_fill`, `DROP TYPE IF EXISTS it_mood`, `CREATE TYPE it_mood AS ENUM ('calm', 'busy')`,
			`CREATE TABLE public.it_fill (id int PRIMARY KEY, ref uuid NOT NULL, mood it_mood, doc jsonb, at timestamptz, ok boolean, price numeric(8,3), day date, tm time)`,
			`INSERT INTO public.it_fill (id, ref) VALUES (41, gen_random_uuid())`},
		my: {`DROP TABLE IF EXISTS shop.it_fill`,
			"CREATE TABLE shop.it_fill (id int PRIMARY KEY, mood enum('calm','busy'), doc json, at datetime, ok tinyint(1), price decimal(8,3), day date, tm time, title varchar(10))",
			`INSERT INTO shop.it_fill (id) VALUES (41)`},
	}
	for _, cn := range []*connection.Conn{pg, my} {
		for _, q := range setup[cn] {
			if _, err := cn.DB.SQL.Exec(q); err != nil {
				t.Fatal(q, err)
			}
		}
		schema := map[*connection.Conn]string{pg: "public", my: "shop"}[cn]
		if x := fillTable(t, a, tt, cn, schema, "it_fill", "200", nil); x.err != "" {
			t.Fatalf("%s: %s", cn.Config.Engine, x.err)
		}
		if n := queryInt(t, cn.DB, `SELECT count(*) FROM `+schema+`.it_fill WHERE id > 41 AND id <= 241 AND mood IS NOT NULL AND doc IS NOT NULL AND ok IS NOT NULL`); n != 200 {
			t.Fatalf("%s: %d of 200 rows generated in full after the key 41", cn.Config.Engine, n)
		}
	}
	pg.DB.SQL.Exec(`DROP TABLE IF EXISTS public.it_fill`)
	pg.DB.SQL.Exec(`DROP TYPE IF EXISTS it_mood`)
	my.DB.SQL.Exec(`DROP TABLE IF EXISTS shop.it_fill`)
}
