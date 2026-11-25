package app

import (
	"slices"
	"strings"
	"testing"
	"time"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// schemaApp is an app with a connection whose server has the schemas
// analytics, audit, public and sales, as the navigator lists them.
func schemaApp(t *testing.T) (*App, *ui.Tester, *connection.Conn) {
	t.Helper()
	a := newTestApp(t)
	file := writeSQL(t, "p.sqlite", "")
	cn := addConn(a, db.Config{ID: "wh", Name: "warehouse", Engine: db.SQLite, Database: file})
	tt := ui.NewTester(a.view, 1200, 760)
	connectNow(t, a, tt, cn)
	// As a server of several schemas reports them.
	cn.Schemas[""] = []string{"analytics", "audit", "public", "sales"}
	a.nav.expand(navNode{kind: nodeConn, conn: cn.Config.ID})
	tt.Frame()
	return a, tt, cn
}

// schemaRows are the schemas the navigator's tree shows.
func schemaRows(a *App) []string {
	var out []string
	for i := range a.nav.tree.Rows() {
		if n := a.nav.tree.Item(i); n.kind == nodeSchema {
			out = append(out, n.schema)
		}
	}
	return out
}

// The navigator shows the schemas ticked for a connection, all of them
// until some are, and says how many on the connection's row; the choice
// is the user's own, kept for the next run and not in the project's file.
func TestSchemaPickerShowsOnlyTheSchemasTicked(t *testing.T) {
	a, tt, cn := schemaApp(t)
	if got := schemaRows(a); !slices.Equal(got, []string{"analytics", "audit", "public", "sales"}) {
		t.Fatalf("before any choice the tree shows %q", got)
	}
	if _, ok := tt.Find("Schemas shown: 2 of 4"); ok {
		t.Fatal("the row counts schemas with none hidden")
	}
	a.openSchemaPicker(cn, "")
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	for _, s := range []string{"audit", "sales"} {
		if err := tt.Click(s); err != nil {
			t.Fatalf("%v: %q", err, tt.Texts())
		}
		tt.Frame()
	}
	testutil.Snapshot(t, tt, "schema-picker")
	if err := tt.Click("Show"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if got := schemaRows(a); !slices.Equal(got, []string{"analytics", "public"}) {
		t.Fatalf("after ticking two off, the tree shows %q", got)
	}
	if _, ok := tt.Find("Schemas shown: 2 of 4"); !ok {
		t.Errorf("the connection's row does not say 2 of 4: %q", tt.Texts())
	}
	// Kept on this computer, for the next run.
	var kept map[string][]string
	if err := cn.Project.Local.LoadJSON(shownSchemasFile, &kept); err != nil || !slices.Equal(kept["wh/"], []string{"analytics", "public"}) {
		t.Errorf("kept %v (%v)", kept, err)
	}
	if file := projectFile(t, cn.Project); strings.Contains(file, "analytics") {
		t.Errorf("the choice is in the project's shared file: %s", file)
	}
	// A schema the server no longer has is not shown, nor counted.
	cn.Schemas[""] = []string{"audit", "public", "sales"}
	tt.Frame()
	if got := schemaRows(a); !slices.Equal(got, []string{"public"}) {
		t.Errorf("with analytics gone from the server, the tree shows %q", got)
	}
	// All of them again forgets the choice.
	if err := a.showSchemas(cn, "", cn.Schemas[""]); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := schemaRows(a); len(got) != 3 {
		t.Errorf("with every schema ticked, the tree shows %q", got)
	}
}

// The dialog shows nothing with no schema ticked: a tree without schemas
// would read as a server without any.
func TestSchemaPickerNeedsOneSchema(t *testing.T) {
	a, tt, cn := schemaApp(t)
	a.openSchemaPicker(cn, "")
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	if err := tt.Click("None"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	show, ok := tt.Access("Show")
	if !ok || !show.Disabled {
		t.Fatalf("with none ticked, Show is disabled %v (found %v)", show.Disabled, ok)
	}
	if err := tt.Click("Show"); err == nil {
		tt.Frame()
	}
	if a.schemaPicker == nil || len(schemaRows(a)) != 4 {
		t.Errorf("Show with none ticked closed the dialog or hid the schemas: %q", schemaRows(a))
	}
}
