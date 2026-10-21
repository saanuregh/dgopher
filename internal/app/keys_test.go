package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/keymap"
	"dgopher/internal/testutil"
	"dgopher/internal/ui/dataview"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo/ui"
)

// A key recorded for an action is kept in keymap.json and works; one
// another action has is refused; Reset gives the action its own back.
func TestKeysEditor(t *testing.T) {
	a := newTestApp(t)
	defer keymap.Use(nil)
	tt := ui.NewTester(a.view, 1280, 800)
	a.keys = &keysEditor{open: true, recording: keymap.Format}
	testutil.WaitFor(t, tt, "the recorder", func() bool { return testutil.HasTextContaining(tt, "Press a key") })
	tt.Frame()
	tt.Key(ui.Alt|ui.Shift, ui.KeyF)
	tt.Frame()
	tt.Key(0, ui.KeyEnter) // one key, not two
	tt.Frame()
	file, _ := a.st.ReadFile(keymap.FileName)
	if !strings.Contains(string(file), `"alt-shift-f": "editor::Format"`) || !keymap.Is(keymap.Format, ui.Alt|ui.Shift, ui.KeyF) || !keymap.Is(keymap.Format, ui.Cmd|ui.Shift, ui.KeyF) {
		t.Fatalf("format's keys %v, recording %q, file:\n%s", keymap.Bindings(keymap.Format), a.keys.recording, file)
	}

	// Two keys in a row.
	a.keys.recording = keymap.Explain
	tt.Frame()
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyX)
	tt.Frame()
	if !slices.ContainsFunc(keymap.Bindings(keymap.Explain), func(b keymap.Binding) bool { return b.Keys.String() == "cmd-k cmd-x" }) {
		t.Fatalf("explain's keys %v", keymap.Bindings(keymap.Explain))
	}

	a.keys.recording = keymap.Explain
	tt.Frame()
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyP)
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if a.keys.err == "" || keymap.Is(keymap.Explain, ui.Cmd, ui.KeyP) {
		t.Fatalf("explain took the file finder's key: %q", a.keys.err)
	}
	testutil.Snapshot(t, tt, "keys-editor")

	a.keys.filter = "format"
	tt.Frame()
	if err := tt.Click("Reset"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	file, _ = a.st.ReadFile(keymap.FileName)
	if strings.Contains(string(file), "editor::Format") || keymap.Changed(keymap.Format) {
		t.Fatalf("format not reset:\n%s", file)
	}
}

// keymap.json changed on disk is read again; the settings' keys of an
// older version move there; two keys in a row run their action.
func TestKeymapFile(t *testing.T) {
	a := newTestApp(t)
	defer keymap.Use(nil)
	tt := ui.NewTester(a.view, 1280, 800)
	tt.Frame()
	if err := a.st.WriteFile(keymap.FileName, []byte(`// mine
[{"bindings": {"cmd-k cmd-h": "workspace::QueryHistory",}}]`)); err != nil {
		t.Fatal(err)
	}
	a.keymapFile.checkedAt = time.Time{}
	a.keymapFile.modTime = time.Time{}
	tt.Frame()
	tt.Frame()
	if keymap.Label(keymap.History) == keymap.Of(ui.Cmd, ui.KeyY).Label() {
		t.Fatalf("not read again: %v %v", keymap.Bindings(keymap.History), a.keymapFile.errs)
	}
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Frame()
	if keymap.Pending() == "" {
		t.Fatal("the first key is not waiting")
	}
	tt.Key(ui.Cmd, ui.KeyH)
	tt.Frame()
	tt.Frame()
	if a.history == nil {
		t.Fatal("cmd-k cmd-h did not open the history")
	}

	b := newTestApp(t)
	b.settings.Keys = map[string][]string{"editor.format": {"Alt+Shift+F"}}
	if err := os.Remove(filepath.Join(b.st.Dir(), keymap.FileName)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	b.loadKeymap()
	file, _ := b.st.ReadFile(keymap.FileName)
	if b.settings.Keys != nil || !strings.Contains(string(file), `"alt-shift-f": "editor::Format"`) {
		t.Fatalf("not moved: %v\n%s", b.settings.Keys, file)
	}
}

// A dialog does what it is for with its key from any of its fields, and
// the key changed in the settings does it in its place.
func TestDialogKey(t *testing.T) {
	a := newTestApp(t)
	defer keymap.Use(nil)
	tt := ui.NewTester(a.view, 1000, 700)
	var renamed []string
	ask := func() {
		a.renaming = &renameForm{open: true, title: "Rename", name: "new", rename: func(n string) error { renamed = append(renamed, n); return nil }}
		tt.Frame()
		tt.Frame()
	}
	ask()
	tt.Key(ui.Cmd, ui.KeyEnter)
	tt.Frame()
	if len(renamed) != 1 || renamed[0] != "new" {
		t.Fatalf("renamed %q", renamed)
	}
	keymap.SetBindings(keymap.ConfirmDialog, []keymap.Binding{{Keys: keymap.Of(ui.Alt, ui.KeyEnter)}})
	ask()
	tt.Key(ui.Cmd, ui.KeyEnter)
	tt.Frame()
	tt.Key(ui.Alt, ui.KeyEnter)
	tt.Frame()
	if len(renamed) != 2 {
		t.Fatalf("with the key changed, renamed %q", renamed)
	}
}

// ⌘1…⌘9 put the keys in the tab chosen: an editor's text, a table's rows.
func TestTabKeyFocus(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	a.openSample()
	testutil.WaitFor(t, tt, "sample editor", func() bool { return a.tabs().Len() == 1 })
	cn := a.tabs().At(0).(*query.Tab).Conn
	testutil.WaitFor(t, tt, "objects", func() bool { return cn.Objects[connection.SchemaKey{Database: "", Schema: "main"}] != nil })
	for _, o := range cn.Objects[connection.SchemaKey{Database: "", Schema: "main"}] {
		if o.Name == "orders" {
			a.OpenTable(cn, "", o, dataview.PageData)
		}
	}
	testutil.WaitFor(t, tt, "rows", func() bool { return testutil.HasTextContaining(tt, " rows") && tt.HasText("status") })
	tt.Key(ui.Cmd, ui.Key1)
	tt.Frame()
	tt.Frame()
	if !a.tabs().At(0).(*query.Tab).Editor.HasFocus() {
		t.Fatal("⌘1 left the editor without the keys")
	}
	tt.Key(ui.Cmd, ui.Key2)
	tt.Frame()
	tt.Frame()
	if a.focusWant != "" || !tt.Focused("status") {
		t.Fatalf("⌘2 left the rows without the keys: focus wanted %q", a.focusWant)
	}
}

// A table chosen in the navigator takes the keys of its menu's items.
func TestNavigatorObjectKeys(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	a.openSample()
	testutil.WaitFor(t, tt, "sample editor", func() bool { return a.tabs().Len() == 1 })
	cn := a.tabs().At(0).(*query.Tab).Conn
	testutil.WaitFor(t, tt, "objects", func() bool { return cn.Objects[connection.SchemaKey{Database: "", Schema: "main"}] != nil })
	a.nav.expand(navNode{kind: nodeConn, conn: cn.Config.ID})
	a.nav.expand(navNode{kind: nodeSchema, conn: cn.Config.ID, schema: "main"})
	a.nav.expand(navNode{kind: nodeFolder, conn: cn.Config.ID, schema: "main", folder: folderTables})
	orders := navNode{kind: nodeObject, conn: cn.Config.ID, schema: "main", name: "orders"}
	testutil.WaitFor(t, tt, "orders", func() bool {
		for i := range a.nav.tree.Rows() {
			if a.nav.tree.Item(i) == orders {
				a.nav.row = i
				return true
			}
		}
		return false
	})
	a.focusWant = "nav"
	tt.Frame()
	tt.Frame()
	var copied string
	a.clipboard = func(s string) { copied = s }
	tt.Key(ui.Cmd, ui.KeyC)
	tt.Frame()
	if copied != `"main"."orders"` {
		t.Fatalf("copied %q", copied)
	}
	tt.Key(0, ui.KeyF4)
	testutil.WaitFor(t, tt, "the structure", func() bool {
		tb, ok := a.ActiveTab().(*dataview.TableTab)
		return ok && tb.Page == dataview.PageStructure
	})
	a.focusWant = "nav"
	tt.Frame()
	tt.Frame()
	tt.Key(0, ui.KeyDelete)
	testutil.WaitFor(t, tt, "the DROP", func() bool {
		q, ok := a.ActiveTab().(*query.Tab)
		return ok && strings.Contains(q.Editor.Text(), `DROP TABLE "main"."orders";`)
	})
}

// On a table, ⌘L focuses the WHERE; pressed there, the quick filter.
func TestFilterKeyTwice(t *testing.T) {
	a := newTestApp(t)
	tt := ui.NewTester(a.view, 1280, 800)
	a.openSample()
	testutil.WaitFor(t, tt, "sample editor", func() bool { return a.tabs().Len() == 1 })
	cn := a.tabs().At(0).(*query.Tab).Conn
	testutil.WaitFor(t, tt, "objects", func() bool { return cn.Objects[connection.SchemaKey{Database: "", Schema: "main"}] != nil })
	for _, o := range cn.Objects[connection.SchemaKey{Database: "", Schema: "main"}] {
		if o.Name == "orders" {
			a.OpenTable(cn, "", o, dataview.PageData)
		}
	}
	testutil.WaitFor(t, tt, "rows", func() bool { return testutil.HasTextContaining(tt, " rows") })
	tt.Key(ui.Cmd, ui.KeyL)
	tt.Frame()
	tt.Frame()
	if !tt.Focused("Filter") {
		t.Fatal("⌘L did not focus the WHERE")
	}
	tt.Key(ui.Cmd, ui.KeyL)
	tt.Frame()
	tt.Frame()
	if !tt.Focused("Filter rows") || a.focusWant != "" {
		t.Fatalf("⌘L again did not focus the quick filter: focus wanted %q", a.focusWant)
	}
}
