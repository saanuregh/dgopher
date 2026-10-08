package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/store"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/redis"

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
	a.startImport(cn, "", obj, path)
	x := a.importing
	testutil.WaitFor(t, tt, "preview", func() bool { return len(x.header) == 4 })
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
	a.startImport(cn, "", obj, path)
	x = a.importing
	testutil.WaitFor(t, tt, "preview", func() bool { return len(x.header) == 2 })
	a.confirmImport(x)
	testutil.WaitFor(t, tt, "failed import", func() bool { return !x.running && x.err != "" })
	cn.DB.SQL.QueryRow(`SELECT count(*) FROM shop.customers WHERE name = 'Ok'`).Scan(&n)
	if n != 0 {
		t.Fatalf("a failed import left %d rows", n)
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
			return ok && at.conn == cn && !at.loading && (at.src.Cols != nil || at.err != "")
		})
		at := a.ActiveTab().(*activityTab)
		if at.err != "" {
			t.Fatalf("%s: %s", cfg.Name, at.err)
		}
		testutil.Snapshot(t, tt, "activity-"+string(cfg.Engine))
	}
}

func TestProjectFolder(t *testing.T) {
	a := newTestApp(t)
	p := a.projects[0]
	dir := p.Dir
	a.openConnForm(nil)
	f := a.connForm
	f.cfg.Name, f.cfg.Host, f.cfg.PasswordEnv = "Billing (prod)", "db.internal", "DGOPHER_BILLING_PW"
	f.source = passwordSources[sourceEnv]
	f.env = 2
	a.saveConnForm(f, false)
	a.openConnForm(nil)
	f = a.connForm
	f.cfg.Name, f.cfg.Host, f.cfg.Password = "Alpha", "h", "s3cret"
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
	a.openQueryFile(p, "reports/top.sql")
	tt.Frame()
	q, ok := a.ActiveTab().(*query.Tab)
	if !ok || q.Conn.Config.Name != "Billing (prod)" || q.Path == "" {
		t.Fatalf("opened %+v", a.ActiveTab())
	}
	testutil.Snapshot(t, tt, "project")
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
	a.openQueryFile(p, "q.sql")
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
		if _, err := fmt.Sscanf(s, "%d rows", &n); err == nil && (s == fmt.Sprintf("%d rows", n) || s == fmt.Sprintf("%d rows loaded", n)) {
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
		testutil.WaitFor(t, tt, "an editable result", func() bool { return tt.HasText("Double-click a cell to edit") && tt.HasText(cell) })
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
