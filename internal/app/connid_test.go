package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dgopher/internal/datamodel"
	"dgopher/internal/db"
	"dgopher/internal/project"
	"dgopher/internal/store"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dashboard"
	"dgopher/internal/ui/query"
	"dgopher/internal/ui/widgets"

	"github.com/egoist/mygo/ui"
)

// A connection's new ID goes everywhere its old one was: dgopher.json, its
// tables' virtual keys and hidden values, the query files that name it, an
// open editor's text without losing what it did not save, dashboards,
// data models, the local filters and colors, the keychain and the trust
// the user gave it. Another connection's file is not touched.
func TestRenameConn(t *testing.T) {
	a := newTestApp(t)
	p := a.projects[0]
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", ""), Host: "h"})
	addConn(a, db.Config{ID: "beta", Name: "beta", Engine: db.SQLite, Database: ":memory:"})
	p.VirtualKeys = map[string][]string{"lite/main.t": {"id"}, "beta/main.t": {"k"}}
	p.HiddenValues = map[string][]string{"lite/main.t": {"secret"}}
	a.saveProject(p)
	write := func(rel, text string) string {
		path := filepath.Join(p.Queries, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(text), 0o644)
		return path
	}
	open := write("a.sql", "-- connection: lite\n\nSELECT 1;")
	nested := write("sub/b.sql", "-- comment\n-- connection: lite\n\nSELECT 2;")
	other := write("c.sql", "-- connection: beta\n\nSELECT 3;")
	upper := write("Report.SQL", "-- connection: lite\n\nSELECT 4;")
	hidden := write(".drafts/d.sql", "-- connection: lite\n\nSELECT 5;")
	outside := filepath.Join(t.TempDir(), "outside.sql")
	os.WriteFile(outside, []byte("-- connection: lite\n\nSELECT 6;"), 0o644)
	linked := os.Symlink(outside, filepath.Join(p.Queries, "link.sql")) == nil
	(&dashboard.Dashboard{Name: "D", Panels: []dashboard.Panel{{Title: "P", Connection: "lite", SQL: "SELECT 1", View: dashboard.Views[0], Width: 1}}}).Save(dashboard.File(p, "D"), nil)
	(&datamodel.Model{Name: "M", Engine: db.SQLite, Source: &datamodel.Source{Connection: "lite"}}).Save(datamodel.File(p, "M"), nil)
	p.Local.SaveJSON("filters.json", map[string][]string{"lite/main.t": {"a = 1"}})
	p.Local.SaveJSON("colors.json", map[string]any{"lite/main.t": []any{map[string]string{"column": "a", "value": "1", "color": "#fff"}}})
	withPassword := cn.Config
	withPassword.Password = "pw"
	if err := a.saveSecrets(&withPassword, true); err != nil {
		t.Fatal(err)
	}
	oldKey, oldTrust := secretKey(&cn.Config, "password"), trustKey(cn)
	a.trust(cn)
	tt := ui.NewTester(a.view, 1000, 700)
	a.openQueryFile(p, "a.sql", nil)
	tt.Frame()
	q := a.ActiveTab().(*query.Tab)
	q.Editor.Replace(len(q.Editor.Text()), len(q.Editor.Text()), " -- edited")
	testutil.SetCaret(tt, q.Editor, len(q.Editor.Text()))
	// An editor on a file of no project's queries keeps its text, as its
	// file is not rewritten.
	elsewhere := a.openFileTab(cn, "", outside, "-- connection: lite\n\nSELECT 6;")

	if err := a.renameConn(cn, "lite2"); err != nil {
		t.Fatal(err)
	}
	if cn.Config.ID != p.Prefix+"lite2" || a.connByID(p.Prefix+"lite2") != cn {
		t.Fatalf("the connection's ID %q", cn.Config.ID)
	}
	var file struct {
		Connections  []struct{ ID string } `json:"connections"`
		VirtualKeys  map[string][]string   `json:"virtualKeys"`
		HiddenValues map[string][]string   `json:"hiddenValues"`
	}
	data, _ := os.ReadFile(filepath.Join(p.Dir, "dgopher.json"))
	json.Unmarshal(data, &file)
	if ids := []string{file.Connections[0].ID, file.Connections[1].ID}; !strings.Contains(strings.Join(ids, " "), "lite2") || strings.Contains(" "+strings.Join(ids, " ")+" ", " lite ") {
		t.Errorf("dgopher.json's connections %q", ids)
	}
	if file.VirtualKeys["lite2/main.t"] == nil || file.VirtualKeys["lite/main.t"] != nil || file.VirtualKeys["beta/main.t"] == nil || file.HiddenValues["lite2/main.t"] == nil || file.HiddenValues["lite/main.t"] != nil {
		t.Errorf("dgopher.json's keys: virtual %v, hidden %v", file.VirtualKeys, file.HiddenValues)
	}
	for path, want := range map[string]string{
		open:   "-- connection: lite2\n\nSELECT 1;",
		nested: "-- comment\n-- connection: lite2\n\nSELECT 2;",
		other:  "-- connection: beta\n\nSELECT 3;",
		upper:  "-- connection: lite2\n\nSELECT 4;",
		hidden: "-- connection: lite\n\nSELECT 5;",
	} {
		if got, _ := os.ReadFile(path); string(got) != want {
			t.Errorf("%s holds %q, want %q", filepath.Base(path), got, want)
		}
	}
	if q.Editor.Text() != "-- connection: lite2\n\nSELECT 1; -- edited" || q.Saved != "-- connection: lite2\n\nSELECT 1;" {
		t.Errorf("the open editor: text %q, saved %q", q.Editor.Text(), q.Saved)
	}
	// Shown again, the caret is where it was in the text.
	a.ActivateTab(func(t widgets.Tab) bool { return t == q })
	tt.Frame()
	if start, end := q.Editor.Selection(); start != len(q.Editor.Text()) || end != len(q.Editor.Text()) {
		t.Errorf("the caret at %d-%d, want at the end, %d", start, end, len(q.Editor.Text()))
	}
	if got, _ := os.ReadFile(outside); string(got) != "-- connection: lite\n\nSELECT 6;" || elsewhere.Saved != string(got) {
		t.Errorf("the file elsewhere %q (linked %v), its editor's saved text %q", got, linked, elsewhere.Saved)
	}
	if d, err := dashboard.Load(dashboard.File(p, "D")); err != nil || d.Panels[0].Connection != "lite2" {
		t.Errorf("the dashboard: %v, %+v", err, d)
	}
	if m, err := datamodel.Load(datamodel.File(p, "M")); err != nil || m.Source.Connection != "lite2" {
		t.Errorf("the model: %v, %+v", err, m)
	}
	for _, name := range []string{"filters.json", "colors.json"} {
		var all map[string]json.RawMessage
		p.Local.LoadJSON(name, &all)
		if all["lite2/main.t"] == nil || all["lite/main.t"] != nil {
			t.Errorf("%s keys %v", name, all)
		}
	}
	if pw, err := a.st.Secrets().Get(secretKey(&cn.Config, "password")); pw != "pw" || err != nil {
		t.Errorf("the password under the new ID: %q, %v", pw, err)
	}
	if _, err := a.st.Secrets().Get(oldKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the old key still holds the password: %v", err)
	}
	if !a.trusted(cn) {
		t.Error("the renamed connection is no longer trusted")
	}
	if _, ok := a.settings.TrustedDetails[oldTrust]; ok {
		t.Error("the old ID's trust details are left")
	}
}

// A password kept only under the key of before (legacySecretKey) moves
// there under the new ID: it still asks to be typed once, never sent where
// the rule has not seen it go.
func TestRenameConnKeepsOldPasswordAsking(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "localhost", Port: 5432, User: "u",
		Proxy: db.ProxyConfig{Kind: "socks5", Host: "proxy", Port: 1080}})
	a.saveProject(a.projects[0])
	if secretKey(&cn.Config, "password") == legacySecretKey(&cn.Config, "password") {
		t.Fatal("the proxy does not set the keys apart")
	}
	if err := a.st.Secrets().Set(legacySecretKey(&cn.Config, "password"), "pw"); err != nil {
		t.Fatal(err)
	}
	if !a.passwordLost(&cn.Config) {
		t.Fatal("the old key's password does not ask")
	}
	if err := a.renameConn(cn, "pg2"); err != nil {
		t.Fatal(err)
	}
	if !a.passwordLost(&cn.Config) {
		t.Error("after the new ID, the old key's password is used without asking")
	}
	if pw, err := a.st.Secrets().Get(legacySecretKey(&cn.Config, "password")); pw != "pw" || err != nil {
		t.Errorf("the old key's password under the new ID: %q, %v", pw, err)
	}
}

// When dgopher.json cannot be written, as after a pull changed it, the
// rename is undone: the ID, the tables' settings and the secrets stay.
func TestRenameConnUndone(t *testing.T) {
	a := newTestApp(t)
	p := a.projects[0]
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:", Host: "h"})
	p.VirtualKeys = map[string][]string{"lite/main.t": {"id"}}
	a.saveProject(p)
	p.Local.SaveJSON("filters.json", map[string][]string{"lite/main.t": {"a = 1"}})
	withPassword := cn.Config
	withPassword.Password = "pw"
	if err := a.saveSecrets(&withPassword, true); err != nil {
		t.Fatal(err)
	}
	renamed := cn.Config
	renamed.ID = p.Prefix + "lite2"
	file := filepath.Join(p.Dir, project.File)
	data, _ := os.ReadFile(file)
	os.WriteFile(file, append(data, '\n'), 0o644)

	if err := a.renameConn(cn, "lite2"); err == nil {
		t.Fatal("renamed over a dgopher.json that changed")
	}
	if cn.Config.ID != p.Prefix+"lite" || p.VirtualKeys["lite/main.t"] == nil || p.VirtualKeys["lite2/main.t"] != nil {
		t.Errorf("after the failed rename: ID %q, virtual keys %v", cn.Config.ID, p.VirtualKeys)
	}
	var filters map[string]json.RawMessage
	p.Local.LoadJSON("filters.json", &filters)
	if filters["lite/main.t"] == nil || filters["lite2/main.t"] != nil {
		t.Errorf("after the failed rename: filters %v", filters)
	}
	if pw, err := a.st.Secrets().Get(secretKey(&cn.Config, "password")); pw != "pw" || err != nil {
		t.Errorf("the password under the old ID: %q, %v", pw, err)
	}
	if _, err := a.st.Secrets().Get(secretKey(&renamed, "password")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a password is left under the new ID: %v", err)
	}
}

// A connection's ID is as a generated one is, and the project's other
// connections do not have it.
func TestConnIDProblem(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	addConn(a, db.Config{ID: "beta", Name: "beta", Engine: db.SQLite, Database: ":memory:"})
	for id, ok := range map[string]bool{"lite": true, "lite-2": true, "uat2": true, "": false, "UAT": false, "a b": false, "-x": false, "a_b": false, "beta": false} {
		if got := a.connIDProblem(cn, id); (got == "") != ok {
			t.Errorf("connIDProblem(%q) = %q, want ok %v", id, got, ok)
		}
	}
}

// The connection form changes a connection's ID once the user agrees to
// what it rewrites; an ID another connection has is refused, unasked.
func TestConnFormChangesID(t *testing.T) {
	a := newTestApp(t)
	p := a.projects[0]
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: writeSQL(t, "x.sqlite", "")})
	addConn(a, db.Config{ID: "beta", Name: "beta", Engine: db.SQLite, Database: ":memory:"})
	a.saveProject(p)
	os.MkdirAll(p.Queries, 0o755)
	os.WriteFile(filepath.Join(p.Queries, "a.sql"), []byte("-- connection: lite\n\nSELECT 1;"), 0o644)
	// Left from a connection deleted: it runs on lite once lite takes its ID.
	os.WriteFile(filepath.Join(p.Queries, "old.sql"), []byte("-- connection: lite2\n\nSELECT 2;"), 0o644)
	tt := ui.NewTester(a.view, 1000, 760)
	a.openConnForm(cn)
	tt.Frame()
	testutil.Snapshot(t, tt, "connection-form-id")
	f := a.connForm
	if f.id != "lite" {
		t.Fatalf("the form's ID %q", f.id)
	}
	f.id = "beta"
	a.saveConnForm(f, false)
	if f.err == "" || a.closing != nil {
		t.Fatalf("another connection's ID: error %q, asked %v", f.err, a.closing != nil)
	}
	f.id, f.err = "lite2", ""
	a.saveConnForm(f, false)
	tt.Frame()
	if a.closing == nil || !tt.HasText("Change the ID of lite to lite2?") || !strings.Contains(a.closing.reason, "dgopher.json and 1 query file") ||
		!strings.Contains(a.closing.reason, "1 query file already name lite2, and will run on this connection") {
		t.Fatalf("no question: %v, %q", a.closing != nil, tt.Texts())
	}
	testutil.Snapshot(t, tt, "connection-id-question")
	if err := tt.Click("Change ID"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if cn.Config.ID != p.Prefix+"lite2" || f.open || f.err != "" {
		t.Fatalf("after agreeing: ID %q, form open %v, error %q", cn.Config.ID, f.open, f.err)
	}
}

// What goes on after a rename runs once its question closed: a question it
// asks stays.
func TestRenameConnThenAsks(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"})
	a.saveProject(a.projects[0])
	tt := ui.NewTester(a.view, 1000, 760)
	a.askRenameConn(cn, "lite", "lite2", func() { a.AskDiscard("Next question?", "", "Next", func() {}) })
	tt.Frame()
	if err := tt.Click("Change ID"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.closing == nil || a.closing.title != "Next question?" {
		t.Fatalf("the next question: %+v", a.closing)
	}
}

// A new ID and a new server saved together leave no password behind: none
// under the new ID for the old server.
func TestConnFormIDAndHostLeaveNoSecret(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "pg", Name: "pg", Engine: db.Postgres, Host: "localhost", Port: 5432, User: "u", Env: db.Development})
	a.saveProject(a.projects[0])
	withPassword := cn.Config
	withPassword.Password = "pw"
	if err := a.saveSecrets(&withPassword, true); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(a.view, 1000, 760)
	a.openConnForm(cn)
	tt.Frame()
	f := a.connForm
	f.id, f.cfg.Host = "pg2", "127.0.0.1"
	a.saveConnForm(f, false)
	tt.Frame()
	if err := tt.Click("Change ID"); err != nil {
		t.Fatalf("%v: %q, form error %q", err, tt.Texts(), f.err)
	}
	testutil.WaitFor(t, tt, "the form saved", func() bool { return !f.open })
	stale := cn.Config
	stale.Host = "localhost"
	if _, err := a.st.Secrets().Get(secretKey(&stale, "password")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a password is left under the new ID for the old server: %v", err)
	}
	if pw, err := a.st.Secrets().Get(secretKey(&cn.Config, "password")); pw != "pw" || err != nil {
		t.Errorf("the password for the new server: %q, %v", pw, err)
	}
}

// The ID field remakes the ID from the name, kept apart from the project's
// other connections' but not from the connection's own.
func TestConnFormIDFromName(t *testing.T) {
	a := newTestApp(t)
	cn := addConn(a, db.Config{ID: "pg", Name: "Billing Prod", Engine: db.SQLite, Database: writeSQL(t, "b.sqlite", "")})
	addConn(a, db.Config{ID: "billing-prod", Name: "Billing", Engine: db.SQLite, Database: writeSQL(t, "c.sqlite", "")})
	a.openConnForm(cn)
	tt := ui.NewTester(a.view, 1000, 900)
	tt.Frame()
	if err := tt.Click("ID from the name"); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	tt.Frame()
	if a.connForm.id != "billing-prod-2" {
		t.Fatalf("id %q", a.connForm.id)
	}
	a.connForm.cfg.Name = "pg"
	tt.Frame()
	if err := tt.Click("ID from the name"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.connForm.id != "pg" {
		t.Fatalf("its own ID taken as another's: %q", a.connForm.id)
	}
}
