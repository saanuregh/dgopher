package dataview

import (
	"context"
	"slices"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/testutil/optionalserver"

	"github.com/egoist/mygo/ui"
)

// An Oracle table tab shows its rows; its distinct values and groups,
// its profile of every row, a foreign key's picker and the rows referring
// to a row read on the server, each with FETCH FIRST, its CLOB and
// XMLType columns too.
func TestIntegrationOracleTableTab(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "oracle")
	cfg := db.Config{ID: "ora", Name: "ora", Engine: db.Oracle, Host: "127.0.0.1", Port: 11521, User: "dgopher", Password: "dgopher", Database: "FREEPDB1"}
	setup, err := db.Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	drop := func() {
		setup.SQL.Exec(`DROP TABLE IF EXISTS "it_tab_child"`)
		setup.SQL.Exec(`DROP TABLE IF EXISTS "it_tab_parent"`)
	}
	drop()
	defer setup.Close()
	defer drop()
	for _, q := range []string{
		`CREATE TABLE "it_tab_parent" ("id" NUMBER(10) PRIMARY KEY, "name" VARCHAR2(20))`,
		`INSERT INTO "it_tab_parent" VALUES (1, 'Ada')`, `INSERT INTO "it_tab_parent" VALUES (2, 'Grace')`,
		`CREATE TABLE "it_tab_child" ("id" NUMBER(10) PRIMARY KEY, "parent_id" NUMBER(10) REFERENCES "it_tab_parent" ("id"), "note" CLOB, "flag" NUMBER(1), "doc" XMLTYPE)`,
		`INSERT INTO "it_tab_child" VALUES (1, 1, 'first', 1, XMLTYPE('<a/>'))`,
		`INSERT INTO "it_tab_child" VALUES (2, 1, NULL, 0, NULL)`,
		`INSERT INTO "it_tab_child" VALUES (3, 2, NULL, NULL, NULL)`,
	} {
		if _, err := setup.SQL.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	a := NewFakeHost(t)
	cn := a.AddConn(cfg)
	tt := ui.NewTester(a.View, 1200, 700)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	a.OpenTable(cn, "", db.Object{Schema: "DGOPHER", Name: "it_tab_child", Kind: db.KindTable, Rows: -1}, PageData)
	v := a.Tabs[0].(*TableTab).view
	testutil.WaitFor(t, tt, "the rows", func() bool { return len(v.src.Rows) == 3 && len(v.fks) == 1 && !v.loading })
	col := func(name string) int {
		return slices.IndexFunc(v.src.Cols, func(c db.ColumnInfo) bool { return c.Name == name })
	}
	if note := db.Display(v.src.Rows[0][col("note")]); note != "first" {
		t.Fatalf("the CLOB shown as %q", note)
	}
	var values []distinctValue
	var groups []groupRow
	var profiles []columnProfile
	var errs []error
	done := 0
	v.distinctValues(col("parent_id"), func(d []distinctValue, err error) { values, done = d, done+1; errs = append(errs, err) })
	v.groupOnServer([]int{col("parent_id")}, func(g []groupRow, err error) { groups, done = g, done+1; errs = append(errs, err) })
	testutil.WaitFor(t, tt, "the values and groups", func() bool { return done == 2 })
	v.profileOnServer(func(p []columnProfile, err error) { profiles, done = p, done+1; errs = append(errs, err) })
	testutil.WaitFor(t, tt, "the profile", func() bool { return done == 3 })
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(values) != 2 || len(groups) != 2 || len(profiles) != len(v.src.Cols) {
		t.Fatalf("values %v, groups %v, profiles %d", values, groups, len(profiles))
	}
	byName := map[string]columnProfile{}
	for _, p := range profiles {
		byName[p.name] = p
	}
	if p := byName["flag"]; p.nulls != 1 || p.distinct != 2 || p.avg != "0.5" {
		t.Fatalf("the NUMBER column profiled as %+v", p)
	}
	for _, name := range []string{"note", "doc"} {
		if p := byName[name]; p.count != 3 || p.nulls != 2 {
			t.Fatalf("the column %s profiled as %+v", name, p)
		}
	}
	fk, ok := v.refOf(col("parent_id"))
	if !ok {
		t.Fatal("no foreign key")
	}
	v.openRefPicker(0, col("parent_id"), fk)
	p := a.Dialogs().refPicker
	testutil.WaitFor(t, tt, "the parents", func() bool { return !p.loading })
	if p.err != "" || len(p.rows) != 2 {
		t.Fatalf("picker %q %v", p.err, p.rows)
	}
	p.open = false
	tt.Frame()

	a.OpenTable(cn, "", db.Object{Schema: "DGOPHER", Name: "it_tab_parent", Kind: db.KindTable, Rows: -1}, PageData)
	parent := a.Tabs[1].(*TableTab).view
	testutil.WaitFor(t, tt, "the parent rows", func() bool { return len(parent.src.Rows) == 2 && !parent.loading })
	var refs []refRows
	var rerr error
	read := false
	parent.referencingRows(0, func(r []refRows, err error) { refs, rerr, read = r, err, true })
	testutil.WaitFor(t, tt, "the referring rows", func() bool { return read })
	if rerr != nil || len(refs) != 1 || len(refs[0].rows) != 2 {
		t.Fatalf("referring rows %v: %v", refs, rerr)
	}
	if at := slices.Index(refs[0].cols, "doc"); at < 0 || strings.TrimSpace(db.Display(refs[0].rows[0][at])) != "<a/>" {
		t.Fatalf("referring rows' columns %v, rows %v", refs[0].cols, refs[0].rows)
	}
}
