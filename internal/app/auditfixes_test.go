package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/lists"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// writeCSV writes a small CSV file and returns its path.
func writeCSV(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "people.csv")
	if err := os.WriteFile(path, []byte("id,name\n1,Ada\n2,Grace\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// An import dialog whose connection was dropped draws without the pool,
// and refuses to import.
func TestImportAfterDisconnect(t *testing.T) {
	a, tt, cn, _ := liteApp(t, "")
	a.startImport(cn, "", "main", nil, writeCSV(t))
	x := a.importing
	testutil.WaitFor(t, tt, "the file", func() bool { return !x.loading && x.file != nil })
	a.dropConnection(cn, "test")
	tt.Frame()
	tt.Frame()
	a.confirmImport(x)
	if x.err == "" || x.running {
		t.Fatalf("imported without a connection: err %q", x.err)
	}
}

// An open import dialog holds its connection, as the copy dialog does,
// so the idle disconnect leaves it.
func TestImportHoldsConnection(t *testing.T) {
	a, tt, cn, _ := liteApp(t, "")
	tt.Frame()
	if a.connectionHeld(cn) {
		t.Skip("held without the dialog")
	}
	a.startImport(cn, "", "main", nil, writeCSV(t))
	if !a.connectionHeld(cn) {
		t.Fatal("an open import dialog does not hold its connection")
	}
	a.importing.open = false
	tt.Frame()
	if a.connectionHeld(cn) {
		t.Fatal("a closed import dialog holds its connection")
	}
}

// A re-read that fails leaves no file to import.
func TestImportFailedRereadClearsFile(t *testing.T) {
	a, tt, cn, _ := liteApp(t, "")
	a.startImport(cn, "", "main", nil, writeCSV(t))
	x := a.importing
	testutil.WaitFor(t, tt, "the file", func() bool { return !x.loading && x.file != nil })
	x.path = filepath.Join(t.TempDir(), "gone.csv")
	a.readImportFile(x)
	testutil.WaitFor(t, tt, "the re-read", func() bool { return !x.loading })
	if x.file != nil || x.err == "" {
		t.Fatalf("after a failed re-read: file %v, err %q", x.file != nil, x.err)
	}
}

// The result of an older read never lands over a newer one's.
func TestImportStaleReadIgnored(t *testing.T) {
	a, tt, cn, _ := liteApp(t, "")
	good := writeCSV(t)
	a.startImport(cn, "", "main", nil, good)
	x := a.importing
	testutil.WaitFor(t, tt, "the file", func() bool { return !x.loading && x.file != nil })
	x.path = filepath.Join(t.TempDir(), "gone.csv")
	a.readImportFile(x)
	waitPosted(t, a)
	x.path = good
	a.readImportFile(x)
	testutil.WaitFor(t, tt, "the re-read", func() bool { return !x.loading })
	tt.Frame()
	if x.file == nil || x.err != "" {
		t.Fatalf("the older read landed: file %v, err %q", x.file != nil, x.err)
	}
}

// When the table's columns fail to load, the dialog says why.
func TestImportColumnLoadError(t *testing.T) {
	a, tt, cn, _ := liteApp(t, "")
	obj := db.Object{Schema: "nosuch", Name: "t", Kind: db.KindTable}
	a.startImport(cn, "", "", &obj, writeCSV(t))
	x := a.importing
	key := connection.ObjectKey{Database: "", Schema: "nosuch", Name: "t"}
	testutil.WaitFor(t, tt, "the load error", func() bool { return !x.loading && cn.LoadErr[key] != "" })
	tt.Frame()
	if !tt.HasText(cn.LoadErr[key]) {
		t.Fatalf("the load error %q is not shown: %q", cn.LoadErr[key], tt.Texts())
	}
	a.confirmImport(x)
	if strings.Contains(x.err, "map at least one") || !strings.Contains(x.err, cn.LoadErr[key]) {
		t.Fatalf("import error %q", x.err)
	}
}

// formWith opens a new connection form for PostgreSQL on localhost.
func formWith(t *testing.T, a *App) *connForm {
	t.Helper()
	a.openConnForm(nil)
	f := a.connForm
	setFormEngine(f, db.Postgres)
	f.cfg.Name, f.cfg.Host, f.cfg.User = "pg", "localhost", "u"
	return f
}

// A port that is not a number is refused on its field and its page, not
// saved as the default.
func TestConnFormRefusesBadPorts(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 900)
	for _, c := range []struct {
		name string
		set  func(f *connForm)
		page int
	}{
		{"port", func(f *connForm) { f.port = "5433x" }, pageGeneral},
		{"SSH port", func(f *connForm) {
			f.cfg.SSH = db.SSHConfig{Enabled: true, Host: "bastion", User: "u"}
			f.sshPort = "22x"
		}, pageNetwork},
		{"proxy port", func(f *connForm) {
			f.proxy, f.cfg.Proxy.Host = 1, "proxy"
			f.proxyPort = "99999"
		}, pageNetwork},
	} {
		n := len(a.conns)
		f := formWith(t, a)
		c.set(f)
		f.page = pageOptions
		a.saveConnForm(f, false)
		tt.Frame()
		if len(a.conns) != n || !strings.Contains(f.err, c.name) {
			t.Fatalf("%s: saved, or error %q", c.name, f.err)
		}
		if f.page != c.page {
			t.Fatalf("%s: page %d, want %d", c.name, f.page, c.page)
		}
		if !strings.Contains(strings.Join(tt.Texts(), "\n"), "must be a number") {
			t.Fatalf("%s: the field shows no error: %q", c.name, tt.Texts())
		}
		f.open = false
		tt.Frame()
	}
}

// A save error about the network page switches to it.
func TestConnFormSaveErrorSwitchesPage(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 900)
	f := formWith(t, a)
	f.cfg.SSH = db.SSHConfig{Enabled: true, User: "u"}
	tt.Frame()
	a.saveConnForm(f, false)
	if f.err == "" || f.page != pageNetwork {
		t.Fatalf("error %q on page %d", f.err, f.page)
	}
}

// A failed test shows its whole error, which can be selected.
func TestConnFormTestErrorInFull(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 900)
	f := formWith(t, a)
	f.testResult, f.testOK = "dial tcp: refused\n\nthe second line says why", false
	tt.Frame()
	if !tt.HasText("the second line says why") && !strings.Contains(strings.Join(tt.Texts(), "\n"), "the second line says why") {
		t.Fatalf("the full error is not shown: %q", tt.Texts())
	}
}

// The palette acts on the current connection: connect, edit, duplicate,
// delete, back up, restore, import.
func TestPaletteConnectionCommands(t *testing.T) {
	a := newTestApp(t)
	file := writeSQL(t, "p.sqlite", "")
	addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.view, 1000, 700)
	tt.Frame()
	a.openPalette(false)
	titles := lists.Map(a.palette.items, func(it paletteItem) string { return it.title })
	for _, want := range []string{"Connect", "Edit Connection…", "Duplicate Connection", "Delete Connection…", "Back Up…", "Restore…", "Import File as New Table…"} {
		if !slices.Contains(titles, want) {
			t.Errorf("no %q in %q", want, titles)
		}
	}
	for _, it := range a.palette.items {
		if it.title == "Edit Connection…" {
			it.run()
		}
	}
	if a.connForm == nil || !a.connForm.open || a.connForm.editing == nil {
		t.Fatal("Edit Connection opened no form")
	}
}

// F4 on a connection in the navigator edits it; Delete asks to delete it.
func TestNavigatorConnectionKeys(t *testing.T) {
	a := newTestApp(t)
	file := writeSQL(t, "n.sqlite", "")
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.view, 1280, 800)
	node := navNode{kind: nodeConn, conn: cn.Config.ID}
	testutil.WaitFor(t, tt, "the connection row", func() bool {
		for i := range a.nav.tree.Rows() {
			if a.nav.tree.Item(i) == node {
				a.nav.row = i
				return true
			}
		}
		return false
	})
	a.focusWant = "nav"
	tt.Frame()
	tt.Frame()
	tt.Key(0, ui.KeyF4)
	testutil.WaitFor(t, tt, "the form", func() bool { return a.connForm != nil && a.connForm.open && a.connForm.editing == cn })
	a.connForm.open = false
	tt.Frame()
	a.focusWant = "nav"
	tt.Frame()
	tt.Frame()
	tt.Key(0, ui.KeyDelete)
	testutil.WaitFor(t, tt, "the question", func() bool { return a.closing != nil && a.closing.title == "Delete lite?" })
}
