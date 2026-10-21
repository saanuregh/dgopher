package app

import (
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// escapeAsks presses Escape on a dialog and reports whether it stayed,
// asking whether to discard what was entered.
func escapeAsks(tt *ui.Tester) bool {
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	return tt.HasText("Discard your changes?")
}

// The connection form closes at Escape as it opened, asks once edited,
// and stays while Test Connection runs.
func TestConnFormKeepsEdits(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1000, 900)
	a.openConnForm(nil)
	tt.Frame()
	if escapeAsks(tt) || a.connForm != nil {
		t.Fatalf("an untouched form: %v", a.connForm != nil)
	}
	a.openConnForm(nil)
	tt.Frame()
	tt.Frame()
	f := a.connForm
	f.cfg.Host = "db.example.com"
	tt.Frame()
	if !escapeAsks(tt) || a.connForm != f {
		t.Fatalf("an edited form closed: %q", tt.Texts())
	}
	if err := tt.Click("Discard"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.connForm != nil {
		t.Fatal("discarded, still open")
	}
	a.openConnForm(nil)
	tt.Frame()
	a.connForm.testing = true
	if escapeAsks(tt) || a.connForm == nil || !a.connForm.open {
		t.Fatal("closed while testing")
	}
	a.connForm.testing = false
	a.connForm.open = false
	tt.Frame()
	// Editing a saved connection, as loaded, is no edit.
	cn := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "h", User: "u", Password: "secret", Env: db.Production})
	a.saveSecrets(&cn.Config, true, nil)
	a.openConnForm(cn)
	tt.Frame()
	tt.Frame()
	if escapeAsks(tt) || a.connForm != nil {
		t.Fatalf("an untouched edit asks: %q", tt.Texts())
	}
}

// A dialog whose job runs stays at Escape: its Stop ends the job. Once
// the job is done, it closes.
func TestJobDialogsStayWhileRunning(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "j.sqlite", "")})
	tt := ui.NewTester(a.view, 1000, 900)
	for _, c := range []struct {
		name string
		open func() *bool
		set  func(running bool)
		gone func() bool
	}{
		{"copy", func() *bool { a.copying = &copyDialog{open: true, from: cn, running: true}; return &a.copying.open },
			func(r bool) { a.copying.running = r }, func() bool { return a.copying == nil }},
		{"import", func() *bool {
			a.importing = &importState{open: true, conn: cn, running: true}
			return &a.importing.open
		},
			func(r bool) { a.importing.running = r }, func() bool { return a.importing == nil }},
		{"backup", func() *bool { a.backup = &backupDialog{open: true, conn: cn, running: true}; return &a.backup.open },
			func(r bool) { a.backup.running = r }, func() bool { return a.backup == nil }},
	} {
		open := c.open()
		tt.Frame()
		if escapeAsks(tt) || !*open || c.gone() {
			t.Fatalf("%s: closed while running", c.name)
		}
		c.set(false)
		tt.Frame()
		tt.Key(0, ui.KeyEscape)
		tt.Frame()
		if !c.gone() {
			t.Fatalf("%s: done, still open", c.name)
		}
	}
}

// What a dialog reads itself, as Run SQL File its file, is no edit: once
// read, it closes at Escape; a choice of the user's then asks.
func TestLoadedDialogClosesUntouched(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "s.sqlite", "")})
	// Long enough to be read after the dialog's first frame.
	script := writeSQL(t, "run.sql", "CREATE TABLE x (a INT);\n"+strings.Repeat("INSERT INTO x VALUES (1);\n", 50000))
	tt := ui.NewTester(a.view, 1000, 900)
	open := func() *sqlFileRun {
		a.startSQLFileRun(cn, "", script, "")
		x := a.sqlFile
		tt.Frame()
		if !x.reading {
			t.Fatal("read before the first frame")
		}
		testutil.WaitFor(t, tt, "the file read", func() bool { return !x.reading })
		return x
	}
	open()
	if escapeAsks(tt) || a.sqlFile != nil {
		t.Fatalf("read, untouched, asks: %q", tt.Texts())
	}
	x := open()
	x.oneTx = !x.oneTx
	tt.Frame()
	if !escapeAsks(tt) || a.sqlFile != x {
		t.Fatalf("a choice changed closed: %q", tt.Texts())
	}
}
