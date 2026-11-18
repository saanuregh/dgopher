package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/testutil"
)

// dbeaverFixture writes a DBeaver project holding a production
// PostgreSQL connection with a saved password, a remote server left on
// DBeaver's default type, a SQLite file, and a DB2 connection DGopher
// cannot open. The password is in the configuration,
// as DBeaver still reads it there, so no encryption is needed.
func dbeaverFixture(t *testing.T, sqlite string) string {
	t.Helper()
	// Away from the user's own DBeaver: the dialog looks there first.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", home)
	dir := filepath.Join(t.TempDir(), "DBeaverData")
	meta := filepath.Join(dir, "workspace6", "General", ".dbeaver")
	if err := os.MkdirAll(meta, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"connections": {
		"postgres-jdbc-1": {"provider": "postgresql", "driver": "postgres-jdbc", "name": "Shop prod", "folder": "Prod/EU",
			"configuration": {"host": "db.example.com", "port": "5432", "database": "shop", "type": "prod",
				"user": "app", "password": "pw"}},
		"postgres-jdbc-2": {"provider": "postgresql", "driver": "postgres-jdbc", "name": "Analytics",
			"configuration": {"host": "analytics.example.com", "port": "5432", "database": "events"}},
		"sqlite-1": {"provider": "sqlite", "driver": "sqlite_jdbc", "name": "Local",
			"configuration": {"database": ` + strings.ReplaceAll(`"`+sqlite+`"`, `\`, `\\`) + `, "type": "dev"}},
		"db2-1": {"provider": "db2", "driver": "db2", "name": "DB2 legacy",
			"configuration": {"host": "mainframe", "port": "50000"}}
	}}`
	if err := os.WriteFile(filepath.Join(meta, "data-sources.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// openDBeaverFixture opens the dialog on the fixture and waits for its
// connections.
func openDBeaverFixture(t *testing.T, a *App, tt *ui.Tester, dir string) *dbeaverImport {
	t.Helper()
	a.openDBeaverImport(nil)
	d := a.dbeaverImport
	if d == nil {
		t.Fatal("the dialog did not open")
	}
	a.readDBeaver(d, dir)
	testutil.WaitFor(t, tt, "DBeaver's connections", func() bool { return !d.loading && len(d.entries) == 4 })
	return d
}

func TestImportFromDBeaver(t *testing.T) {
	a := newTestApp(t)
	sqlite := filepath.Join(t.TempDir(), "local.db")
	dir := dbeaverFixture(t, sqlite)
	p := a.projects[0]
	a.addConn(p, db.Config{Name: "Mine", Engine: db.SQLite, Database: sqlite})
	a.addConn(p, db.Config{Name: "Old", Engine: db.SQLite, Database: filepath.Join(t.TempDir(), "old.db"), Folder: "prod"})
	tt := ui.NewTester(a.view, 1200, 800)
	d := openDBeaverFixture(t, a, tt, dir)
	tt.Frame()
	testutil.Snapshot(t, tt, "import-dbeaver")

	names := []string{d.entries[0].Config.Name, d.entries[1].Config.Name, d.entries[2].Config.Name, d.entries[3].Config.Name}
	if !slices.Equal(names, []string{"Analytics", "DB2 legacy", "Local", "Shop prod"}) {
		t.Fatalf("entries %v", names)
	}
	// Its environment not chosen yet, not supported, already in the
	// project, and the one to import.
	if !slices.Equal(d.chosen, []bool{false, false, false, true}) {
		t.Fatalf("chosen %v", d.chosen)
	}
	if d.passwords {
		t.Fatal("the passwords were chosen without the user's consent")
	}
	if err := tt.Click("Import 1"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.dbeaverImport != nil {
		t.Fatal("the dialog stayed open")
	}
	cn := a.connByName("Shop prod")
	if cn == nil || cn.Project != p || cn.Config.Env != db.Production || cn.Config.Host != "db.example.com" || cn.Config.User != "app" {
		t.Fatalf("imported %+v", cn)
	}
	// Into DBeaver's folder, taking the project's prod for its Prod.
	if cn.Config.Folder != "prod/EU" || !slices.Contains(p.Folders, "prod/EU") {
		t.Fatalf("folder %q, project's %v", cn.Config.Folder, p.Folders)
	}
	if cn.Config.Password != "" || !cn.Config.AskPassword {
		t.Fatalf("without consent the password is asked for, not kept: %+v", cn.Config)
	}
	if pw, _ := a.st.Secrets().Get(secretKey(&cn.Config, "password")); pw != "" {
		t.Fatal("the password reached the keychain without consent")
	}
	if a.connByName("Local") != nil || a.connByName("DB2 legacy") != nil || a.connByName("Analytics") != nil {
		t.Fatal("a connection not chosen was imported")
	}
	if !slices.ContainsFunc(auditEvents(t, a), func(e audit.Event) bool {
		return e.Kind == audit.KindConfig && strings.HasPrefix(e.Detail, "connection added from DBeaver: Production")
	}) {
		t.Fatal("the import was not audited")
	}
	file, err := os.ReadFile(filepath.Join(p.Dir, "dgopher.json"))
	if err != nil || strings.Contains(string(file), `"pw"`) {
		t.Fatalf("the project file holds the password, or %v", err)
	}
}

func TestImportFromDBeaverWithPasswords(t *testing.T) {
	a := newTestApp(t)
	dir := dbeaverFixture(t, filepath.Join(t.TempDir(), "local.db"))
	tt := ui.NewTester(a.view, 1200, 800)
	d := openDBeaverFixture(t, a, tt, dir)
	if err := tt.Click("Import saved passwords into the keychain"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !d.passwords {
		t.Fatal("the consent was not given")
	}
	// A remote server DBeaver left on its default type waits for its
	// environment, which ticks it once chosen.
	if !d.needsEnv(0) {
		t.Fatal("Analytics was taken as Development")
	}
	d.chooseDBeaverEnv(0, db.Staging.Label())
	tt.Frame()
	if !d.chosen[0] || d.needsEnv(0) {
		t.Fatalf("chosen %v", d.chosen)
	}
	if err := tt.Click("Import 3"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	cn := a.connByName("Shop prod")
	if cn == nil || cn.Config.AskPassword || cn.Config.Password != "" {
		t.Fatalf("imported %+v", cn)
	}
	if pw, _ := a.st.Secrets().Get(secretKey(&cn.Config, "password")); pw != "pw" {
		t.Fatalf("keychain has %q", pw)
	}
	if a.connByName("Local") == nil {
		t.Fatal("the SQLite file was not imported")
	}
	if an := a.connByName("Analytics"); an == nil || an.Config.Env != db.Staging || an.Config.Commit != db.CommitDefault {
		t.Fatalf("Analytics %+v", an)
	}
}

// connByName is the connection of that name, nil for none.
func (a *App) connByName(name string) *connection.Conn {
	for _, cn := range a.conns {
		if cn.Config.Name == name {
			return cn
		}
	}
	return nil
}

func TestDBeaverFolder(t *testing.T) {
	p := &project.Project{Folders: []string{"Prod", "Prod/EU"}}
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"prod/eu", "Prod/EU"},
		{"PROD/eu/Billing", "Prod/EU/Billing"},
		{"Test", "Test"},
		{`Prod/A\B/C`, "Prod"}, // a name with \ cannot be a folder's
	} {
		if got := dbeaverFolder(p, c.in); got != c.want {
			t.Errorf("dbeaverFolder(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
