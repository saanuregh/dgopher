package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/state"
	"dgopher/internal/store"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// newProjectDir makes an empty folder for a project.
func newProjectDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestProjectsLoadAndPersist(t *testing.T) {
	cfgDir := t.TempDir()
	st, _ := store.Open(cfgDir, store.MemorySecrets())
	a := newApp(st)
	one, two := newProjectDir(t, "one"), newProjectDir(t, "two")
	p1, err := a.addProject(one)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := a.addProject(two)
	if err != nil {
		t.Fatal(err)
	}
	a.addConn(p1, db.Config{Name: "a", Engine: db.SQLite, Database: ":memory:"})
	a.addConn(p2, db.Config{Name: "b", Engine: db.SQLite, Database: ":memory:"})
	// Listing a folder again selects it rather than adding it twice.
	if again, _ := a.addProject(one); again != p1 || len(a.projects) != 2 {
		t.Fatalf("listed twice: %d projects", len(a.projects))
	}

	b := newApp(st)
	if len(b.projects) != 2 || b.projects[0].Dir != one || b.projects[1].Dir != two {
		t.Fatalf("projects after a restart: %+v", b.settings.Projects)
	}
	if len(b.projectConns(b.projects[0])) != 1 || len(b.projectConns(b.projects[1])) != 1 {
		t.Fatalf("connections after a restart: %d", len(b.conns))
	}
	// Removing one forgets it, without touching its folder.
	b.removeProject(b.projects[0])
	if len(b.projects) != 1 || len(b.conns) != 1 || len(b.settings.Projects) != 1 {
		t.Fatalf("after removing: %d projects, %d conns", len(b.projects), len(b.conns))
	}
	if _, err := os.Stat(filepath.Join(one, project.File)); err != nil {
		t.Fatalf("removing touched the folder: %v", err)
	}
}

// The project's own state is ignored by git, and only settings are global.
func TestProjectLocalStateIsIgnored(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	a := newTestApp(t)
	p := a.projects[0]
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	a.saveProject(p)
	a.Record(&cn.Config, audit.Event{Kind: audit.KindConnect})
	p.Local.AppendHistory(store.HistoryEntry{SQL: "SELECT 1", Connection: "lite"})
	a.saveWorkspace(true)
	cmd := exec.Command(git, "init", "-q")
	cmd.Dir = p.Dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	cmd = exec.Command(git, "status", "--porcelain", "--untracked-files=all")
	cmd.Dir = p.Dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "?? "+project.File {
		t.Fatalf("git sees:\n%s", got)
	}
	// All of it in one file: the state file, with SQLite's own beside it.
	local, _ := os.ReadDir(filepath.Join(p.Dir, project.LocalDir))
	for _, e := range local {
		switch e.Name() {
		case ".gitignore", state.FileName, state.FileName + "-wal", state.FileName + "-shm":
		default:
			t.Errorf("%s holds %s", project.LocalDir, e.Name())
		}
	}
	var ws json.RawMessage
	if h, _ := p.Local.History(0); len(h) != 1 || p.Local.LoadJSON(workspaceFile, &ws) != nil {
		t.Errorf("history %v, workspace %s", h, ws)
	}
	if events, _ := p.Audit.Read(0); len(events) != 1 {
		t.Errorf("audit %v", events)
	}
	entries, _ := os.ReadDir(a.st.Dir())
	for _, e := range entries {
		if e.Name() != "settings.json" {
			t.Errorf("the app config holds %s", e.Name())
		}
	}
}

func TestRelativeFilePaths(t *testing.T) {
	a := newTestApp(t)
	p := a.projects[0]
	inside := filepath.Join(p.Dir, "data", "app.db")
	outside := filepath.Join(t.TempDir(), "elsewhere.db")
	a.addConn(p, db.Config{Name: "in", Engine: db.SQLite, Database: inside})
	a.addConn(p, db.Config{Name: "out", Engine: db.SQLite, Database: outside})
	raw, _ := os.ReadFile(filepath.Join(p.Dir, project.File))
	if !strings.Contains(string(raw), `"database": "data/app.db"`) || !strings.Contains(string(raw), outside) {
		t.Fatalf("paths in %s:\n%s", project.File, raw)
	}
	b := newBareApp(t)
	q, err := b.addProject(p.Dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, cn := range b.projectConns(q) {
		got[cn.Config.Name] = cn.Config.Database
	}
	if got["in"] != inside || got["out"] != outside {
		t.Fatalf("resolved %v", got)
	}
}

// A folder that cannot be read stays listed, with why, and holds nothing.
func TestMissingProjectStaysListed(t *testing.T) {
	cfgDir := t.TempDir()
	st, _ := store.Open(cfgDir, store.MemorySecrets())
	a := newApp(st)
	dir := newProjectDir(t, "gone")
	if _, err := a.addProject(dir); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(dir)
	b := newApp(st)
	if len(b.projects) != 1 || b.projects[0].Err == "" {
		t.Fatalf("projects %+v", b.projects)
	}
	tt := ui.NewTester(b.view, 1000, 700)
	b.nav.expand(navNode{kind: nodeProject, projectDir: dir})
	tt.Frame()
	if !tt.HasText("gone") {
		t.Fatalf("the missing project is not shown: %q", tt.Texts())
	}
	b.openConnForm(nil)
	if b.connForm != nil || b.newProject == nil {
		t.Fatal("a connection form opened for a project that cannot be read")
	}
}

// A dgopher.json changed on disk, as by a git pull, is not overwritten.
func TestProjectFileChangedOnDiskIsKept(t *testing.T) {
	a := newTestApp(t)
	p := a.projects[0]
	a.addConn(p, db.Config{Name: "mine", Engine: db.SQLite, Database: ":memory:"})
	path := filepath.Join(p.Dir, project.File)
	pulled := []byte("{\n  \"connections\": []\n}\n")
	os.WriteFile(path, pulled, 0o644)
	a.addConn(p, db.Config{Name: "another", Engine: db.SQLite, Database: ":memory:"})
	if disk, _ := os.ReadFile(path); string(disk) != string(pulled) {
		t.Fatalf("the pulled file was overwritten:\n%s", disk)
	}
	if a.alert == nil || !strings.Contains(a.alert.message, "changed on disk") {
		t.Fatalf("no error said why: %+v", a.alert)
	}
}

func TestAuditGoesToTheConnectionsProject(t *testing.T) {
	a := newBareApp(t)
	p1, _ := a.addProject(newProjectDir(t, "one"))
	p2, _ := a.addProject(newProjectDir(t, "two"))
	c1 := a.addConn(p1, db.Config{Name: "a", Engine: db.SQLite, Database: ":memory:"})
	c2 := a.addConn(p2, db.Config{Name: "b", Engine: db.SQLite, Database: ":memory:"})
	a.Record(&c1.Config, audit.Event{Kind: audit.KindStatement, Statement: "SELECT 'one'"})
	a.Record(&c2.Config, audit.Event{Kind: audit.KindStatement, Statement: "SELECT 'two'"})
	for _, tc := range []struct {
		p    *project.Project
		want string
	}{{p1, "one"}, {p2, "two"}} {
		events, err := tc.p.Audit.Read(0)
		if err != nil || len(events) != 1 || !strings.Contains(events[0].Statement, tc.want) {
			t.Fatalf("%s: %+v %v", tc.p.Name, events, err)
		}
	}
	// Removed, a project's log takes no more entries, and its state file
	// is closed.
	a.dropProject(p1)
	if p1.Local.SQL().Ping() == nil {
		t.Fatal("the state file of a removed project is still open")
	}
	a.Record(&c1.Config, audit.Event{Kind: audit.KindStatement, Statement: "SELECT 'late'"})
	if events, _ := p2.Audit.Read(0); len(events) != 1 {
		t.Fatalf("an entry went to another project: %+v", events)
	}
}

func TestHistoryPerProject(t *testing.T) {
	a := newBareApp(t)
	p1, _ := a.addProject(newProjectDir(t, "one"))
	p2, _ := a.addProject(newProjectDir(t, "two"))
	c1 := a.addConn(p1, db.Config{Name: "a", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	a.NewQueryTab(c1, "", "SELECT 1;")
	testutil.WaitFor(t, tt, "the editor", func() bool { _, ok := a.ActiveTab().(*query.Tab); return ok })
	q := a.ActiveTab().(*query.Tab)
	testutil.SetCaret(tt, &q.Editor, len([]rune(q.Editor.Text))-2)
	q.Run(query.RunStatement)
	testutil.WaitFor(t, tt, "the run", func() bool { return resultRows(tt, q, 1) })

	testutil.WaitFor(t, tt, "history", func() bool { h, _ := p1.Local.History(10); return len(h) == 1 })
	if h, _ := p2.Local.History(10); len(h) != 0 {
		t.Fatalf("another project's history: %+v", h)
	}
	a.openHistory()
	if a.history == nil || a.history.project != p1 {
		t.Fatal("the history did not open for the editor's project")
	}
}

func TestSidebarListsProjects(t *testing.T) {
	a := newBareApp(t)
	p1, _ := a.addProject(newProjectDir(t, "billing"))
	p2, _ := a.addProject(newProjectDir(t, "analytics"))
	a.addConn(p1, db.Config{Name: "Billing DB", Engine: db.SQLite, Database: ":memory:", Env: db.Production})
	a.addConn(p2, db.Config{Name: "Warehouse", Engine: db.DuckDB, Database: ":memory:"})
	os.WriteFile(filepath.Join(p1.Queries, "monthly.sql"), []byte("-- connection: billing-db\nSELECT 1;\n"), 0o644)
	a.ScanQueries(p1, true)
	tt := ui.NewTester(a.view, 1200, 760)
	a.nav.expand(navNode{kind: nodeProject, projectDir: p2.Dir})
	a.nav.expand(navNode{kind: nodeQueries, projectDir: p1.Dir})
	testutil.WaitFor(t, tt, "the query file", func() bool { return tt.HasText("monthly.sql") })
	for _, s := range []string{"billing", "analytics", "Billing DB", "Warehouse", "Queries"} {
		if !tt.HasText(s) {
			t.Errorf("the sidebar lacks %q: %q", s, tt.Texts())
		}
	}
	testutil.Snapshot(t, tt, "sidebar-projects")
	// A query file opens on the connection its header names.
	cn := a.projectConns(p1)[0]
	cn.Status = connection.StatusConnected
	a.activate(navNode{kind: nodeQueryFile, projectDir: p1.Dir, name: "monthly.sql"})
	if q, ok := a.ActiveTab().(*query.Tab); !ok || q.Conn != cn {
		t.Fatalf("opened %+v", a.ActiveTab())
	}
}

func TestStorageNotes(t *testing.T) {
	a := newTestApp(t)
	a.openConnForm(nil)
	f := a.connForm
	f.cfg.Name, f.cfg.Host, f.cfg.Password = "Billing", "db", "pw"
	f.envChosen = true
	cfg := f.config()
	note := a.storageNote(f, &cfg)
	id := a.projects[0].Prefix + "billing"
	for _, want := range []string{keychainName(), `"DGopher"`, `"conn/` + id + `/`, "/password\"", project.File} {
		if !strings.Contains(note, want) {
			t.Errorf("keychain note lacks %q: %s", want, note)
		}
	}
	for src, want := range map[passwordSource]string{sourceEnv: "Not stored. Read from $DGOPHER_X", sourceCommand: "Not stored. The command runs", sourceAsk: "Not stored. Asked for"} {
		f.source = passwordSources[src]
		f.cfg.PasswordEnv = "DGOPHER_X"
		cfg := f.config()
		if note := a.storageNote(f, &cfg); !strings.HasPrefix(note, want) {
			t.Errorf("%s: %s", passwordSources[src], note)
		}
	}
	// What the note promises is where the password goes.
	f.source = passwordSources[sourceKeychain]
	f.cfg.PasswordEnv = ""
	a.saveConnForm(f, false)
	if pw, _ := a.st.Secrets().Get(secretKey(&a.conns[0].Config, "password")); pw != "pw" || !strings.Contains(note, secretKey(&a.conns[0].Config, "password")) {
		t.Fatalf("saved under another account than the note said: %q", pw)
	}
	tt := ui.NewTester(a.view, 1200, 800)
	a.openConnForm(a.conns[0])
	tt.Frame()
	testutil.Snapshot(t, tt, "connection-form-storage")
}

// A source switched away from keeps nothing: a typed password does not
// reach the keychain once a command gives it.
func TestOnlyTheChosenSourceIsKept(t *testing.T) {
	a := newTestApp(t)
	a.openConnForm(nil)
	f := a.connForm
	f.cfg.Name, f.cfg.Host, f.cfg.Password = "x", "db", "typed"
	f.envChosen = true
	f.source = passwordSources[sourceCommand]
	f.cfg.PasswordCommand = "printf pw"
	a.saveConnForm(f, false)
	cn := a.conns[0]
	if pw, _ := a.st.Secrets().Get(secretKey(&cn.Config, "password")); pw != "" {
		t.Fatalf("the typed password was kept: %q", pw)
	}
	if cn.Config.PasswordCommand != "printf pw" || cn.Config.AskPassword || cn.Config.PasswordEnv != "" {
		t.Fatalf("saved %+v", cn.Config)
	}
}

func TestPasswordCommandConnects(t *testing.T) {
	testutil.Integration(t)
	if runtime.GOOS == "windows" {
		t.Skip("needs printf")
	}
	a := newTestApp(t)
	cfg := testutil.PGConfig()
	cfg.Password, cfg.PasswordCommand = "", "printf dbgopher"
	cn := addConn(a, cfg)
	tt := ui.NewTester(a.view, 1000, 700)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status != connection.StatusConnecting && cn.Status != connection.StatusIdle })
	if cn.Status != connection.StatusConnected {
		t.Fatalf("status %v: %s", cn.Status, cn.Err)
	}
	// A failing command says why, without what it printed.
	bad := testutil.PGConfig()
	bad.ID, bad.Name, bad.Password = "bad", "bad", ""
	bad.PasswordCommand = `sh -c 'printf leaked; echo denied >&2; exit 2'`
	bc := addConn(a, bad)
	a.Connect(bc, nil)
	testutil.WaitFor(t, tt, "failure", func() bool { return bc.Status == connection.StatusFailed })
	if !strings.Contains(bc.Err, "denied") || strings.Contains(bc.Err, "leaked") {
		t.Fatalf("error %q", bc.Err)
	}
	a.alert = nil
	// What the commands printed is in no audit entry: the password, nor
	// the output of the one that failed.
	var log bytes.Buffer
	a.projects[0].Audit.Export(&log)
	raw := log.Bytes()
	if strings.Contains(string(raw), "dbgopher") || strings.Contains(string(raw), "leaked") {
		t.Fatalf("command output reached the audit log:\n%s", raw)
	}
	if !strings.Contains(string(raw), "password from command") {
		t.Fatalf("the audit does not say where the password came from:\n%s", raw)
	}
}

// A command that arrives in the file asks before it runs, and again when
// it changes.
func TestChangedCommandAsksAgain(t *testing.T) {
	a := newTestApp(t)
	marker := filepath.Join(t.TempDir(), "ran")
	cn := addConn(a, db.Config{ID: "r", Name: "r", Engine: db.Redis, Host: "127.0.0.1", Port: 1, PasswordCommand: "touch " + marker})
	a.settings.TrustedShared = nil // as if it arrived in a pull
	a.Connect(cn, nil)
	if a.confirm == nil || !strings.Contains(strings.Join(a.confirm.Reasons, " "), "touch "+marker) {
		t.Fatalf("the prompt does not show the command: %+v", a.confirm)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command ran before it was approved")
	}
	a.confirm.OnConfirm()
	a.confirm = nil
	a.disconnect(cn)
	cn.Config.PasswordCommand = "touch " + marker + "-changed"
	a.Connect(cn, nil)
	if a.confirm == nil {
		t.Fatal("a changed command ran without asking")
	}
}

// A connection the user made is theirs: it connects without a prompt.
func TestOwnConnectionIsTrusted(t *testing.T) {
	a := newTestApp(t)
	file := filepath.Join(t.TempDir(), "own.sqlite")
	os.WriteFile(file, nil, 0o600)
	a.openConnForm(nil)
	f := a.connForm
	f.engine, f.engineIdx = db.SQLite.Label(), 4
	f.cfg.Name, f.cfg.Database = "own", file
	a.saveConnForm(f, false)
	tt := ui.NewTester(a.view, 1000, 700)
	cn := a.conns[0]
	a.Connect(cn, nil)
	if a.confirm != nil {
		t.Fatal("asked about a connection the user made")
	}
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
}

func TestAutosave(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	a.NewQueryTab(cn, "", "SELECT 1;")
	testutil.WaitFor(t, tt, "the editor", func() bool { _, ok := a.ActiveTab().(*query.Tab); return ok })
	q := a.ActiveTab().(*query.Tab)
	q.Editor.Text += "\nSELECT 2;"
	tt.Frame()
	if disk, _ := os.ReadFile(q.Path); strings.Contains(string(disk), "SELECT 2") {
		t.Fatal("saved while typing")
	}
	testutil.WaitFor(t, tt, "autosave", func() bool {
		disk, _ := os.ReadFile(q.Path)
		return strings.Contains(string(disk), "SELECT 2")
	})
}

func TestAutosaveNeverOverwritesChangedFile(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	a.NewQueryTab(cn, "", "SELECT 1;")
	testutil.WaitFor(t, tt, "the editor", func() bool { _, ok := a.ActiveTab().(*query.Tab); return ok })
	q := a.ActiveTab().(*query.Tab)
	pulled := "-- connection: lite\n\nSELECT 'pulled';"
	os.WriteFile(q.Path, []byte(pulled), 0o644)
	q.Editor.Text += " -- mine"
	testutil.WaitFor(t, tt, "the conflict", func() bool { return q.DiskConflict != "" })
	if disk, _ := os.ReadFile(q.Path); string(disk) != pulled {
		t.Fatalf("the pulled file was overwritten: %q", disk)
	}
	testutil.Snapshot(t, tt, "editor-conflict")
	if q.CloseReason() == "" {
		t.Fatal("closing would drop the editor's text without asking")
	}
	if err := tt.Click("Use the File"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if q.Editor.Text != pulled || q.DiskConflict != "" {
		t.Fatalf("after using the file: %q %q", q.Editor.Text, q.DiskConflict)
	}
}

func TestUntouchedEditorFileIsDeleted(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	open := func(text string) *query.Tab {
		n := len(a.tabs)
		a.NewQueryTab(cn, "", text)
		testutil.WaitFor(t, tt, "the editor", func() bool { return len(a.tabs) == n+1 })
		return a.ActiveTab().(*query.Tab)
	}
	look := open("SELECT * FROM t;")
	kept := open("")
	kept.Editor.Text += "SELECT 'kept';"
	a.closeTab(slicesIndex(a.tabs, look))
	a.closeTab(slicesIndex(a.tabs, kept))
	if _, err := os.Stat(look.Path); err == nil {
		t.Fatal("an untouched editor left its file")
	}
	if disk, err := os.ReadFile(kept.Path); err != nil || !strings.Contains(string(disk), "kept") {
		t.Fatalf("an edited editor lost its file: %v", err)
	}
}

func TestRenameQueryFile(t *testing.T) {
	a := newTestApp(t)
	p := a.projects[0]
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	a.NewQueryTab(cn, "", "SELECT 1;")
	testutil.WaitFor(t, tt, "the editor", func() bool { _, ok := a.ActiveTab().(*query.Tab); return ok })
	q := a.ActiveTab().(*query.Tab)
	old := q.Path
	a.askRename(p, old)
	if err := a.renaming.rename("monthly report"); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(p.Queries, "monthly report.sql")
	if q.Path != want || q.Name != "monthly report.sql" {
		t.Fatalf("editor at %s named %s", q.Path, q.Name)
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("the old file is still there")
	}
	// Renamed, it is kept on close, as a file the user named.
	a.closeTab(0)
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("the renamed file went on close: %v", err)
	}
	// A name in use is refused.
	os.WriteFile(filepath.Join(p.Queries, "taken.sql"), nil, 0o644)
	a.askRename(p, want)
	if err := a.renaming.rename("taken"); err == nil {
		t.Fatal("renamed over another file")
	}
}

func slicesIndex(tabs []widgets.Tab, q *query.Tab) int {
	for i, t := range tabs {
		if t == q {
			return i
		}
	}
	return -1
}

// A file someone else wrote into is not deleted when its untouched
// editor closes.
func TestUntouchedEditorKeepsOthersContent(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	a.NewQueryTab(cn, "", "")
	testutil.WaitFor(t, tt, "the editor", func() bool { _, ok := a.ActiveTab().(*query.Tab); return ok })
	q := a.ActiveTab().(*query.Tab)
	os.WriteFile(q.Path, []byte("SELECT 'someone else';"), 0o644)
	a.closeTab(0)
	if _, err := os.Stat(q.Path); err != nil {
		t.Fatal("another writer's content was deleted")
	}
}

// Declining the prompt sticks, even with an editor that connects as it
// shows, as one set to auto-connect does.
func TestDeclinedTrustPromptSticks(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "r", Name: "r", Engine: db.SQLite, Database: filepath.Join(t.TempDir(), "x.sqlite"), AutoConnect: true})
	a.settings.TrustedShared = nil
	a.tabs = append(a.tabs, query.New(a, cn, "", "q.sql", "SELECT 1;"))
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()
	first := a.confirm
	tt.Frame()
	if first == nil || a.confirm != first {
		t.Fatal("the prompt was not shown, or was replaced as frames went by")
	}
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if a.confirm != nil || cn.Status != connection.StatusFailed {
		t.Fatalf("after Cancel: prompt %v, status %v", a.confirm != nil, cn.Status)
	}
}

// Test Connection on a connection from the file asks first, as a connect.
func TestTestButtonAsksForAPulledCommand(t *testing.T) {
	a := newTestApp(t)
	marker := filepath.Join(t.TempDir(), "ran")
	cn := addConn(a, db.Config{ID: "r", Name: "r", Engine: db.Redis, Host: "127.0.0.1", Port: 1, PasswordCommand: "touch " + marker})
	a.settings.TrustedShared = nil
	a.openConnForm(cn)
	a.testConnection(a.connForm)
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("Test ran a pulled command without asking")
	}
	if a.confirm == nil {
		t.Fatal("Test did not ask")
	}
	// Renamed only, it stays as untrusted as it came.
	a.connForm.cfg.Name = "renamed"
	a.saveConnForm(a.connForm, false)
	if a.trusted(cn) {
		t.Fatal("a rename trusted a pulled command")
	}
}

// A pull that weakens TLS, or swaps the CA or the key, asks again.
func TestFingerprintCoversTransportSecurity(t *testing.T) {
	base := db.Config{ID: "x", Engine: db.Postgres, Host: "h", TLS: db.TLSVerifyFull, CAFile: "/etc/ca.pem", SSH: db.SSHConfig{Enabled: true, KeyPath: "~/.ssh/id"}}
	for name, edit := range map[string]func(*db.Config){
		"tls": func(c *db.Config) { c.TLS = db.TLSDisable },
		"ca":  func(c *db.Config) { c.CAFile = "certs/evil.pem" },
		"key": func(c *db.Config) { c.SSH.KeyPath = "" },
	} {
		c := base
		edit(&c)
		if sharedFingerprint(&c, "/p") == sharedFingerprint(&base, "/p") {
			t.Errorf("a change of %s keeps the trust", name)
		}
	}
}

// Editing a connection whose password is not in the keychain keeps its
// SSH secrets there; a duplicate gets the original's secrets.
func TestEditAndDuplicateKeepSecrets(t *testing.T) {
	a := newTestApp(t)
	a.openConnForm(nil)
	f := a.connForm
	f.cfg.Name, f.cfg.Host, f.cfg.PasswordEnv = "env", "db", "DGOPHER_X"
	f.envChosen = true
	f.source = passwordSources[sourceEnv]
	f.cfg.SSH = db.SSHConfig{Enabled: true, Host: "bastion", User: "u", Password: "sshpw"}
	a.saveConnForm(f, false)
	cn := a.conns[0]
	a.openConnForm(cn)
	a.saveConnForm(a.connForm, false)
	if pw, _ := a.st.Secrets().Get(secretKey(&cn.Config, "ssh-password")); pw != "sshpw" {
		t.Fatalf("SSH password after an edit that changed nothing: %q", pw)
	}
	a.openConnForm(nil)
	f = a.connForm
	f.cfg.Name, f.cfg.Host, f.cfg.Password = "kc", "db2", "s3cret"
	f.envChosen = true
	a.saveConnForm(f, false)
	a.duplicateConn(a.conns[1])
	dup := a.conns[2]
	if pw, _ := a.st.Secrets().Get(secretKey(&dup.Config, "password")); pw != "s3cret" {
		t.Fatalf("duplicate's password %q", pw)
	}
}

// Text that could not be saved, as when the file changed on disk, is
// still there after a restart.
func TestConflictedTextSurvivesRestart(t *testing.T) {
	cfgDir := t.TempDir()
	st, _ := store.Open(cfgDir, store.MemorySecrets())
	a := newApp(st)
	if _, err := a.addProject(newProjectDir(t, "p")); err != nil {
		t.Fatal(err)
	}
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	a.saveProject(a.projects[0])
	tt := ui.NewTester(a.view, 1000, 700)
	a.NewQueryTab(cn, "", "SELECT 1;")
	testutil.WaitFor(t, tt, "the editor", func() bool { _, ok := a.ActiveTab().(*query.Tab); return ok })
	q := a.ActiveTab().(*query.Tab)
	os.WriteFile(q.Path, []byte("SELECT 'pulled';"), 0o644)
	q.Editor.Text = "SELECT 'mine';"
	testutil.WaitFor(t, tt, "the conflict", func() bool { return q.DiskConflict != "" })
	a.saveWorkspace(true) // as on quit
	b := newApp(st)
	r := b.tabs[0].(*query.Tab)
	if r.Editor.Text != "SELECT 'mine';" || r.Saved != "SELECT 'pulled';" || r.DiskConflict == "" {
		t.Fatalf("restored %q over %q, conflict %q", r.Editor.Text, r.Saved, r.DiskConflict)
	}
}

// A folder of queries that cannot be read does not break the project.
func TestScanErrorKeepsProjectUsable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	a := newTestApp(t)
	p := a.projects[0]
	locked := filepath.Join(p.Queries, "locked")
	os.MkdirAll(locked, 0o000)
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	tt := ui.NewTester(a.view, 1000, 700)
	a.ScanQueries(p, true)
	testutil.WaitFor(t, tt, "the scan", func() bool { return p.ScanErr != "" })
	if p.Err != "" || a.addConn(p, db.Config{Name: "x", Engine: db.SQLite, Database: ":memory:"}) == nil {
		t.Fatalf("the project broke: %q", p.Err)
	}
}

// New Connection from a project's menu goes to that project, whatever
// editor is in front.
func TestNewConnectionFromProjectMenu(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	other, _ := a.addProject(newProjectDir(t, "other"))
	a.tabs = append(a.tabs, query.New(a, cn, "", "q.sql", ""))
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()
	if err := tt.RightClick("other"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("New Connection…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.connForm == nil || a.connForm.project != other {
		t.Fatalf("the form is for %+v", a.connForm)
	}
}

// A refused save leaves nothing half done: no keychain entry, no trust.
func TestRefusedSaveChangesNothing(t *testing.T) {
	a := newTestApp(t)
	p := a.projects[0]
	os.WriteFile(filepath.Join(p.Dir, project.File), []byte("{\"connections\": []}\n"), 0o644)
	a.openConnForm(nil)
	f := a.connForm
	f.cfg.Name, f.cfg.Host, f.cfg.Password = "x", "db", "s3cret"
	f.envChosen = true
	trusted := len(a.settings.TrustedShared)
	a.saveConnForm(f, false)
	if len(a.conns) != 0 || f.err == "" || len(a.settings.TrustedShared) != trusted {
		t.Fatalf("conns %d, err %q", len(a.conns), f.err)
	}
	cfg := f.config()
	cfg.ID = a.uniqueConnID(p, cfg.Name)
	if pw, _ := a.st.Secrets().Get(secretKey(&cfg, "password")); pw != "" {
		t.Fatal("the keychain kept a password for a connection that was not saved")
	}
}

// Test asks for a password that is asked every time, and keeps nothing.
func TestTestConnectionAsksForThePassword(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1200, 800)
	a.openConnForm(nil)
	f := a.connForm
	pg := testutil.PGConfig()
	f.cfg.Name, f.cfg.Host, f.cfg.User, f.cfg.Database = "ask", pg.Host, pg.User, pg.Database
	f.port = "15432"
	f.source = passwordSources[sourceAsk]
	a.testConnection(f)
	tt.Frame()
	if a.prompt == nil || f.testing || !tt.HasText("Password for ask") {
		t.Fatalf("no prompt before the test: prompt %v, testing %v", a.prompt != nil, f.testing)
	}
	if os.Getenv("DGOPHER_IT") == "" {
		return
	}
	a.prompt.password = pg.Password
	if err := tt.Click("Test"); err != nil {
		t.Fatal(err)
	}
	testutil.WaitFor(t, tt, "the test", func() bool { return !f.testing && f.testResult != "" })
	if !f.testOK {
		t.Fatalf("test failed: %s", f.testResult)
	}
	if f.cfg.Password != "" {
		t.Fatal("the asked password was kept in the form")
	}
}

// The number fields' steppers stay beside their input, clear of the unit
// after them.
func TestConnFormSteppersDoNotOverlap(t *testing.T) {
	a := newTestApp(t)
	a.openConnForm(nil)
	tt := ui.NewTester(a.view, 1000, 1400)
	tt.Frame()
	if err := tt.Click("Options"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	plus, ok := tt.Find("Increase")
	unit, ok2 := tt.Find("minutes")
	if !ok || !ok2 {
		t.Fatalf("no stepper or unit: %q", tt.Texts())
	}
	testutil.Snapshot(t, tt, "connection-form-numbers")
	if plus.X+plus.W > unit.X {
		t.Fatalf("the + button (x %.0f..%.0f) covers the unit (from x %.0f)", plus.X, plus.X+plus.W, unit.X)
	}
}

// Off, as by default, a connection waits to be asked: an editor restored
// at start stays unconnected, says so, and Run connects it first.
func TestRestoredEditorWaitsToConnect(t *testing.T) {
	st, _ := store.Open(t.TempDir(), store.MemorySecrets())
	a := newApp(st)
	if _, err := a.addProject(newProjectDir(t, "p")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	a.saveProject(a.projects[0])
	tt := ui.NewTester(a.view, 1000, 700)
	a.NewQueryTab(cn, "", "SELECT 1 AS one;")
	testutil.WaitFor(t, tt, "the editor", func() bool { _, ok := a.ActiveTab().(*query.Tab); return ok })
	a.saveWorkspace(true)

	b := newApp(st)
	tt = ui.NewTester(b.view, 1000, 700)
	for range 5 {
		tt.Frame()
	}
	q := b.tabs[0].(*query.Tab)
	if q.Conn.Status != connection.StatusIdle || !tt.HasText("Not connected.") {
		t.Fatalf("restored editor: status %v, %q", q.Conn.Status, tt.Texts())
	}
	testutil.SetCaret(tt, &q.Editor, 3)
	q.Run(query.RunStatement)
	testutil.WaitFor(t, tt, "the run", func() bool { return resultRows(tt, q, 1) })
}

// A restored editor opens on the connection its file names, as after a
// pull that changed the header, not on the one it last had.
func TestRestoredEditorFollowsItsHeader(t *testing.T) {
	st, _ := store.Open(t.TempDir(), store.MemorySecrets())
	a := newApp(st)
	if _, err := a.addProject(newProjectDir(t, "p")); err != nil {
		t.Fatal(err)
	}
	alpha := addConn(a, db.Config{ID: "alpha", Name: "alpha", Engine: db.SQLite, Database: ":memory:"})
	addConn(a, db.Config{ID: "beta", Name: "beta", Engine: db.SQLite, Database: ":memory:"})
	a.saveProject(a.projects[0])
	tt := ui.NewTester(a.view, 1000, 700)
	a.NewQueryTab(alpha, "", "SELECT 1;")
	testutil.WaitFor(t, tt, "the editor", func() bool { _, ok := a.ActiveTab().(*query.Tab); return ok })
	q := a.ActiveTab().(*query.Tab)
	a.saveWorkspace(true)
	os.WriteFile(q.Path, []byte("-- connection: beta\n\nSELECT 1;"), 0o644)

	b := newApp(st)
	if len(b.tabs) != 1 || b.tabs[0].Connection().Config.ID != b.projects[0].Prefix+"beta" {
		t.Fatalf("restored %v", b.tabs)
	}
}

// On, a connection opens as DGopher starts, with nothing shown.
func TestAutoConnectAtStart(t *testing.T) {
	st, _ := store.Open(t.TempDir(), store.MemorySecrets())
	a := newApp(st)
	if _, err := a.addProject(newProjectDir(t, "p")); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.sqlite"), nil, 0o600)
	os.WriteFile(filepath.Join(dir, "y.sqlite"), nil, 0o600)
	addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: filepath.Join(dir, "x.sqlite"), AutoConnect: true})
	addConn(a, db.Config{ID: "lazy", Name: "lazy", Engine: db.SQLite, Database: filepath.Join(dir, "y.sqlite")})
	a.saveProject(a.projects[0])

	b := newApp(st)
	tt := ui.NewTester(b.view, 1000, 700)
	auto, lazy := b.connByID(b.projects[0].Prefix+"lite"), b.connByID(b.projects[0].Prefix+"lazy")
	testutil.WaitFor(t, tt, "the auto-connect", func() bool { return auto.Status == connection.StatusConnected })
	if lazy.Status != connection.StatusIdle {
		t.Fatalf("a connection without auto-connect is %v", lazy.Status)
	}
}

// Auto-connect is off for a new connection, and the form keeps it once
// ticked.
func TestConnFormAutoConnect(t *testing.T) {
	a := newTestApp(t)
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	a.openConnForm(nil)
	tt := ui.NewTester(a.view, 1000, 1400)
	tt.Frame()
	f := a.connForm
	if f.cfg.AutoConnect {
		t.Fatal("a new connection auto-connects")
	}
	f.cfg.Name, f.cfg.Engine, f.cfg.Database = "lite", db.SQLite, file
	tt.Frame()
	if err := tt.Click("Options"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Connect as DGopher starts, and when its editors are shown"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	a.saveConnForm(f, false)
	tt.Frame()
	if len(a.conns) != 1 || !a.conns[0].Config.AutoConnect {
		t.Fatalf("saved %+v", a.conns)
	}
}

// The form puts what most connections need first: a file has no Network
// page, and a page holding a choice other than the default says so.
func TestConnFormPages(t *testing.T) {
	a := newTestApp(t)
	a.openConnForm(nil)
	tt := ui.NewTester(a.view, 1000, 900)
	tt.Frame()
	testutil.Snapshot(t, tt, "connection-form-general")
	f := a.connForm
	if !tt.HasText("Network") || !tt.HasText("Host") {
		t.Fatalf("server form: %q", tt.Texts())
	}
	f.cfg.SSH.Enabled = true
	tt.Frame()
	if _, ok := tt.Find("Network •"); !ok {
		t.Fatalf("the SSH tunnel is not marked: %q", tt.Texts())
	}
	a.syncEngineChoice(f, slices.Index(db.Engines(), db.DuckDB))
	f.engineIdx = slices.Index(db.Engines(), db.DuckDB)
	tt.Frame()
	if tt.HasText("Network") || !tt.HasText("File") {
		t.Fatalf("file form: %q", tt.Texts())
	}
}

// A color is chosen from the swatches, and the environment's swatch
// takes it back.
func TestConnFormColor(t *testing.T) {
	a := newTestApp(t)
	a.openConnForm(nil)
	tt := ui.NewTester(a.view, 1000, 900)
	tt.Frame()
	if err := tt.Click("Violet"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := a.connForm.config().Color; got != "#7c3aed" {
		t.Fatalf("color %q", got)
	}
	if err := tt.Click("The environment's (Development)"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := a.connForm.config().Color; got != "" {
		t.Fatalf("color %q after choosing the environment's", got)
	}
}

// A color mistyped in a shared project file is drawn as the environment's
// rather than stopping the app.
func TestMalformedColor(t *testing.T) {
	cfg := db.Config{Env: db.Production, Color: "red"}
	if got, want := widgets.EnvColor(&cfg), widgets.EnvironmentColor(db.Production); got != want {
		t.Fatalf("color %v, want the environment's %v", got, want)
	}
	a := newTestApp(t)
	a.openConnForm(nil)
	f := a.connForm
	f.cfg.Name, f.cfg.Color = "pg", "red"
	a.saveConnForm(f, false)
	if !strings.Contains(f.err, "#rrggbb") || len(a.conns) != 0 {
		t.Fatalf("saved a malformed color: %q, %d connections", f.err, len(a.conns))
	}
}

// slowSecrets is a keychain that takes a while to answer, as the Secret
// Service can.
type slowSecrets struct{ store.Secrets }

func (s slowSecrets) Get(key string) (string, error) {
	time.Sleep(300 * time.Millisecond)
	return s.Secrets.Get(key)
}

// Connect reads the keychain off the main thread: the window keeps
// drawing while it answers.
func TestConnectLoadsSecretsInBackground(t *testing.T) {
	st, _ := store.Open(t.TempDir(), slowSecrets{store.MemorySecrets()})
	a := newApp(st)
	if _, err := a.addProject(newProjectDir(t, "p")); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(a.view, 1000, 700)
	file := filepath.Join(t.TempDir(), "x.sqlite")
	os.WriteFile(file, nil, 0o600)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	start := time.Now()
	a.Connect(cn, nil)
	if d := time.Since(start); d > 100*time.Millisecond || cn.Status != connection.StatusConnecting {
		t.Fatalf("Connect took %v, status %v", d, cn.Status)
	}
	testutil.WaitFor(t, tt, "the connection", func() bool { return cn.Status == connection.StatusConnected })

	// A password asked for is asked once the keychain had none, one
	// connection after the other; closing the prompt, as Escape does,
	// cancels it.
	ask := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "127.0.0.1", Port: 1, User: "u", AskPassword: true})
	ask2 := addConn(a, db.Config{ID: "pg2", Name: "pg2", Engine: db.Postgres, Host: "127.0.0.1", Port: 1, User: "u", AskPassword: true})
	a.Connect(ask, nil)
	a.Connect(ask2, nil)
	if a.prompt != nil {
		t.Fatal("the password was asked before the keychain answered")
	}
	testutil.WaitFor(t, tt, "the password prompts", func() bool { return a.prompt != nil && len(a.promptQueue) == 1 })
	first := a.prompt.conn
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if first.Status != connection.StatusFailed || a.prompt == nil || a.prompt.conn == first {
		t.Fatalf("after Escape: status %v, prompt %+v", first.Status, a.prompt)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if ask.Status != connection.StatusFailed || ask2.Status != connection.StatusFailed || a.prompt != nil {
		t.Fatalf("statuses %v %v, prompt %+v", ask.Status, ask2.Status, a.prompt)
	}

	// Disconnected while the keychain answers: nothing opens.
	gone := addConn(a, db.Config{ID: "lite2", Name: "lite2", Engine: db.SQLite, Database: file})
	a.Connect(gone, nil)
	a.disconnect(gone)
	time.Sleep(400 * time.Millisecond)
	for range 5 {
		tt.Frame()
	}
	if gone.Status != connection.StatusIdle {
		t.Fatalf("a disconnected connection went on: %v", gone.Status)
	}
}

// Reloaded, a project records in its log as before, on its new state file.
func TestAuditAfterReload(t *testing.T) {
	a := newBareApp(t)
	p, _ := a.addProject(newProjectDir(t, "one"))
	cn := a.addConn(p, db.Config{Name: "a", Engine: db.SQLite, Database: ":memory:"})
	a.saveProject(p)
	a.Record(&cn.Config, audit.Event{Kind: audit.KindStatement, Statement: "SELECT 'before'"})
	a.reloadProject(p)
	q := a.projects[0]
	if q == p || q.Audit == nil {
		t.Fatalf("not reloaded: %+v", q)
	}
	a.Record(&a.conns[0].Config, audit.Event{Kind: audit.KindStatement, Statement: "SELECT 'after'"})
	events, err := q.Audit.Read(0)
	if err != nil || len(events) < 2 || !strings.Contains(events[0].Statement, "after") {
		t.Fatalf("%+v %v", events, err)
	}
	if v, err := q.Audit.Verify(); err != nil || !v.Intact() {
		t.Fatalf("%+v %v", v, err)
	}
}

// A connection to one server keeps the keychain key, and the trust, it
// had before Redis topologies: its saved password is still found.
func TestSecretKeyUnchangedForOneServer(t *testing.T) {
	cfg := db.Config{ID: "p.r", Engine: db.Redis, Host: "cache", Port: 6379, User: "u", Database: "2"}
	where := fmt.Sprintf("%s|%s|%d|%s|%s|%v|%s|%d|%s", cfg.Engine, cfg.Host, cfg.Port, cfg.User, cfg.Database, false, "", 0, "")
	sum := sha256.Sum256([]byte(where))
	if got, want := secretKey(&cfg, "password"), "conn/p.r/"+hex.EncodeToString(sum[:6])+"/password"; got != want {
		t.Fatalf("key %s, want %s", got, want)
	}
	// A node added, as by a pull, is a new destination: a new key, and
	// the trust asked again.
	cluster := cfg
	cluster.Redis = db.RedisConfig{Mode: db.RedisCluster, Nodes: "evil:6379"}
	if secretKey(&cluster, "password") == secretKey(&cfg, "password") || sharedFingerprint(&cluster, "/p") == sharedFingerprint(&cfg, "/p") {
		t.Fatal("a cluster's nodes do not change where the password is kept, or the trust")
	}
}

// The form keeps what the chosen topology uses, and the sentinels'
// password goes to the keychain, never to dgopher.json.
func TestConnFormSentinel(t *testing.T) {
	a := newTestApp(t)
	a.openConnForm(nil)
	tt := ui.NewTester(a.view, 1000, 1000)
	f := a.connForm
	a.syncEngineChoice(f, slices.Index(db.Engines(), db.Redis))
	f.engineIdx = slices.Index(db.Engines(), db.Redis)
	f.redisMode = slices.Index(redisModes, db.RedisSentinel)
	tt.Frame()
	if !tt.HasText("A sentinel") || !tt.HasText("Master name") || !tt.HasText("Sentinel password") {
		t.Fatalf("sentinel form: %q", tt.Texts())
	}
	f.cfg.Name, f.cfg.Host, f.cfg.Redis.Nodes, f.cfg.Redis.Master = "cache", "10.0.0.1", " 10.0.0.2:26379 ", "mymaster"
	f.envChosen = true
	f.cfg.Redis.SentinelPassword = "sentinel-secret"
	if got := f.config(); got.Port != 0 || got.Redis.Nodes != "10.0.0.2:26379" {
		t.Fatalf("config %+v", got)
	}
	a.saveConnForm(f, false)
	if len(a.conns) != 1 || a.conns[0].Config.Redis.SentinelPassword != "" {
		t.Fatalf("saved %+v", a.conns)
	}
	data, _ := os.ReadFile(filepath.Join(a.projects[0].Dir, project.File))
	if strings.Contains(string(data), "sentinel-secret") || !strings.Contains(string(data), `"master": "mymaster"`) {
		t.Fatalf("dgopher.json: %s", data)
	}
	cfg := a.conns[0].Config
	a.loadSecrets(&cfg)
	if cfg.Redis.SentinelPassword != "sentinel-secret" {
		t.Fatal("the sentinels' password is not in the keychain")
	}

	// Back to one server: what only a topology used goes.
	a.openConnForm(a.conns[0])
	a.connForm.redisMode = 0
	if got := a.connForm.config(); got.Redis != (db.RedisConfig{}) {
		t.Fatalf("one server keeps %+v", got.Redis)
	}
}

// A cloud identity takes the password's place, on PostgreSQL and MySQL
// only, and keeps no password of the form; it is part of what a shared
// connection is trusted for.
func TestCloudIdentityForm(t *testing.T) {
	a := newTestApp(t)
	a.openConnForm(nil)
	f := a.connForm
	f.engine = db.MySQL.Label()
	f.cfg.Name, f.cfg.Host, f.cfg.User, f.cfg.Password = "Orders", "orders.rds.amazonaws.com", "app", "typed"
	f.envChosen = true
	f.source, f.identity = passwordSources[sourceIdentity], db.IdentityAWS.Label()
	f.cfg.IdentityRegion = " eu-west-1 "
	cfg := f.config()
	if cfg.Identity != db.IdentityAWS || cfg.IdentityRegion != "eu-west-1" || cfg.Password != "" || sourceOf(&cfg) != sourceIdentity {
		t.Fatalf("config %+v", cfg)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("an identity without TLS: %v", err)
	}
	f.tls = tlsLabels[db.TLSRequire]
	if cfg = f.config(); cfg.Validate() != nil {
		t.Fatal(cfg.Validate())
	}
	before := sharedFingerprint(&cfg, "/p")
	cfg.IdentityProfile = "prod"
	if sharedFingerprint(&cfg, "/p") == before {
		t.Fatal("the identity is not part of the fingerprint")
	}
	if plain := (db.Config{ID: "x"}); sharedFingerprint(&plain, "/p") == "" {
		t.Fatal("no fingerprint")
	}
	f.engine = db.Redis.Label()
	if slices.Contains(passwordSourcesOf(db.Redis), passwordSources[sourceIdentity]) || f.config().Identity != "" {
		t.Fatal("Redis offered a cloud identity")
	}
}

// A connection to another machine is not saved as development unless the
// user chose it: the default asks nothing before a write. An edit that
// points it elsewhere asks again; a local one never asks.
func TestConnectionEnvironmentChosen(t *testing.T) {
	a := newTestApp(t)
	a.openConnForm(nil)
	f := a.connForm
	f.cfg.Name, f.cfg.Host = "orders", "orders.example.com"
	a.saveConnForm(f, false)
	if len(a.conns) != 0 || !f.envAsked || !strings.Contains(f.err, "orders.example.com") {
		t.Fatalf("saved without an environment: conns %d, err %q", len(a.conns), f.err)
	}
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	if err := tt.Click("Development"); err != nil { // the one shown, picked
		t.Fatal(err)
	}
	tt.Frame()
	a.saveConnForm(f, false)
	if len(a.conns) != 1 {
		t.Fatalf("not saved once chosen: %q", f.err)
	}

	// Editing what does not move it asks nothing; moving it asks again.
	a.openConnForm(a.conns[0])
	f = a.connForm
	f.cfg.Name = "orders 2"
	a.saveConnForm(f, false)
	if f.err != "" || a.conns[0].Config.Name != "orders 2" {
		t.Fatalf("a rename asked: %q", f.err)
	}
	a.openConnForm(a.conns[0])
	f = a.connForm
	f.cfg.Host = "orders-prod.example.com"
	a.saveConnForm(f, false)
	if a.conns[0].Config.Host != "orders.example.com" || !f.envAsked {
		t.Fatalf("moved without asking: %q", a.conns[0].Config.Host)
	}

	a.openConnForm(nil)
	f = a.connForm
	f.cfg.Name, f.cfg.Host = "local", "127.0.0.1"
	a.saveConnForm(f, false)
	if f.err != "" || len(a.conns) != 2 {
		t.Fatalf("a local connection asked: %q", f.err)
	}
}

func TestRemote(t *testing.T) {
	for _, c := range []struct {
		cfg  db.Config
		want bool
	}{
		{db.Config{Engine: db.Postgres, Host: "localhost:5432"}, false},
		{db.Config{Engine: db.Postgres, Host: "[::1]:5432"}, false},
		{db.Config{Engine: db.Postgres, Host: "/var/run/postgresql"}, false},
		{db.Config{Engine: db.Postgres, Host: "db.example.com"}, true},
		{db.Config{Engine: db.Postgres, Host: "localhost", SSH: db.SSHConfig{Enabled: true, Host: "bastion"}}, true},
		{db.Config{Engine: db.Redis, Host: "localhost", Redis: db.RedisConfig{Nodes: "127.0.0.1:7001, 10.0.0.2:7002"}}, true},
		{db.Config{Engine: db.SQLite, Database: "/tmp/x.db"}, false},
	} {
		if got := remote(&c.cfg); got != c.want {
			t.Errorf("%+v: remote %v", c.cfg, got)
		}
	}
}

// A field's description shows below its input, not over it, as a row
// that grew in the field's column drew it.
func TestFieldDescriptionBelowInput(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1200, 900)
	a.openConnForm(nil)
	f := a.connForm
	f.page, f.tls = pageNetwork, tlsLabels[db.TLSRequire]
	tt.Frame()
	tt.Frame()
	testutil.Snapshot(t, tt, "connection-form-network")
	input, ok := tt.Find("Choose the Client Certificate")
	if !ok {
		t.Fatalf("no certificate field: %q", tt.Texts())
	}
	note, ok := tt.Find("A PEM certificate the server may ask the connection to log in with.")
	if !ok {
		t.Fatal("no description")
	}
	if note.Y < input.Y+input.H {
		t.Fatalf("the description at %v overlaps the input %v", note, input)
	}
}

// A section's row makes another of its kind, without its menu and without
// opening or closing the section.
func TestSectionNewButtons(t *testing.T) {
	a := newTestApp(t)
	addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()
	if r, ok := tt.Find("Queries"); ok {
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
	}
	tt.Frame()
	testutil.Snapshot(t, tt, "section-new")
	dashboards := navNode{kind: nodeDashboards, projectDir: a.projects[0].Dir}
	if err := tt.Click("New Dashboard…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.renaming == nil || a.renaming.title != "New Dashboard" || a.nav.tree.Open.Has(dashboards) {
		t.Fatalf("asked %+v, section open %v", a.renaming, a.nav.tree.Open.Has(dashboards))
	}
	tt.Key(0, ui.KeyEscape)
	if err := tt.Click("New Data Model…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.newModel == nil || a.newModel.project != a.projects[0] {
		t.Fatal("no data model asked for")
	}
	if !tt.HasText("New SQL Editor") || !tt.HasText("New Connection…") {
		t.Fatalf("not a button for each section: %q", tt.Texts())
	}
}

// The tree goes no deeper than it needs: one project shows its sections,
// its name above them, and a database of one schema its folders, the
// schema named on its row.
func TestNavigatorShallow(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 700)
	a.openSample()
	testutil.WaitFor(t, tt, "sample editor", func() bool { return len(a.tabs) == 1 })
	cn := a.tabs[0].(*query.Tab).Conn
	conn := navNode{kind: nodeConn, conn: cn.Config.ID}
	a.nav.expand(conn)
	testutil.WaitFor(t, tt, "the schema's folders", func() bool {
		kids := a.navChildren(conn)
		return len(kids) > 0 && kids[0].kind == nodeFolder && kids[0].schema == "main"
	})
	if roots := a.navRoots(); roots[0].kind != nodeConnections {
		t.Fatalf("roots %v", roots)
	}
	if !tt.HasText("main") || !tt.HasText(a.projectLabel(a.projects[0])) {
		t.Fatalf("the schema or the project is not named: %q", tt.Texts())
	}
	testutil.Snapshot(t, tt, "navigator-shallow")

	if _, err := a.addProject(newProjectDir(t, "second")); err != nil {
		t.Fatal(err)
	}
	if roots := a.navRoots(); len(roots) != 2 || roots[0].kind != nodeProject {
		t.Fatalf("with two projects, roots %v", roots)
	}
}

// A note of the navigator, as "No connections yet", is never chosen: the
// choice goes past it the way it moves, and a click on it leaves the
// choice where it was.
func TestNavigatorSkipsNotes(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 400)
	tt.Frame()
	chosen := func() navNode {
		if a.nav.row < 0 || a.nav.row >= a.nav.tree.Rows() {
			return navNode{}
		}
		return a.nav.tree.Item(a.nav.row)
	}
	dir := a.projects[0].Dir
	if err := tt.Click("Connections"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if got := chosen(); got != (navNode{kind: nodeQueries, projectDir: dir}) {
		t.Fatalf("down from Connections chose %+v", got)
	}
	tt.Key(0, ui.KeyUp)
	tt.Frame()
	if got := chosen(); got != (navNode{kind: nodeConnections, projectDir: dir}) {
		t.Fatalf("up from Queries chose %+v", got)
	}
	if err := tt.Click("No connections yet"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := chosen(); got.kind == nodeInfo {
		t.Fatalf("a click chose the note")
	}
}
