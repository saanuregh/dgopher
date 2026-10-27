package dataview

import (
	"context"
	"slices"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"
	"dgopher/internal/testutil/optionalserver"

	"github.com/egoist/mygo/ui"
)

// A SQL Server table's tab shows spatial and hierarchyid values as text,
// and the features that read a limited number of rows read them with
// TOP: a column's values, groups, the profile, the rows referring to a
// row and the reference picker.
func TestIntegrationSQLServerTableTab(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "sqlserver")
	cfg := db.Config{ID: "ss", Name: "ss", Engine: db.SQLServer, Host: "127.0.0.1", Port: 11433, User: "sa", Password: "Dgopher-1", Database: "shop"}
	setup, err := db.Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	drop := func() {
		setup.SQL.Exec("DROP TABLE IF EXISTS dbo.it_tab_child")
		setup.SQL.Exec("DROP TABLE IF EXISTS dbo.it_tab_parent")
	}
	drop()
	defer setup.Close()
	defer drop()
	for _, q := range []string{
		"CREATE TABLE dbo.it_tab_parent (id int PRIMARY KEY, name nvarchar(20))",
		"INSERT INTO dbo.it_tab_parent VALUES (1, N'Ada'), (2, N'Grace')",
		"CREATE TABLE dbo.it_tab_child (id int PRIMARY KEY, parent_id int REFERENCES dbo.it_tab_parent (id), g geography, h hierarchyid, flag bit)",
		"INSERT INTO dbo.it_tab_child VALUES (1, 1, 'POINT(1 2)', '/1/', 1), (2, 1, NULL, '/1/2/', 0), (3, 2, NULL, NULL, NULL)",
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
	a.OpenTable(cn, "", db.Object{Schema: "dbo", Name: "it_tab_child", Kind: db.KindTable, Rows: -1}, PageData)
	v := a.Tabs[0].(*TableTab).view
	testutil.WaitFor(t, tt, "the rows", func() bool { return len(v.src.Rows) == 3 && len(v.fks) == 1 && !v.loading })
	col := func(name string) int {
		return slices.IndexFunc(v.src.Cols, func(c db.ColumnInfo) bool { return c.Name == name })
	}
	if g, h := v.src.Rows[0][col("g")], v.src.Rows[1][col("h")]; g != "POINT (1 2)" || h != "/1/2/" {
		t.Fatalf("shown %#v and %#v", g, h)
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

	a.OpenTable(cn, "", db.Object{Schema: "dbo", Name: "it_tab_parent", Kind: db.KindTable, Rows: -1}, PageData)
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
}

// A grid edit of a row whose table has a trigger, applied on a session
// under SET NOCOUNT ON, is counted as the one row it changed.
func TestIntegrationSQLServerEditCountsOwnRows(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "sqlserver")
	cfg := db.Config{ID: "ss", Name: "ss", Engine: db.SQLServer, Host: "127.0.0.1", Port: 11433, User: "sa", Password: "Dgopher-1", Database: "shop"}
	setup, err := db.Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	drop := func() {
		setup.SQL.Exec("DROP TABLE IF EXISTS dbo.it_grid_counted")
		setup.SQL.Exec("DROP TABLE IF EXISTS dbo.it_grid_counted_log")
	}
	drop()
	defer setup.Close()
	defer drop()
	for _, q := range []string{
		"CREATE TABLE dbo.it_grid_counted (id int PRIMARY KEY, v int)", "CREATE TABLE dbo.it_grid_counted_log (id int)",
		"INSERT INTO dbo.it_grid_counted VALUES (1, 1), (2, 2)",
		"CREATE TRIGGER dbo.it_grid_counted_logged ON dbo.it_grid_counted AFTER UPDATE AS INSERT INTO dbo.it_grid_counted_log SELECT id FROM inserted UNION ALL SELECT id FROM inserted",
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
	a.OpenTable(cn, "", db.Object{Schema: "dbo", Name: "it_grid_counted", Kind: db.KindTable, Rows: -1}, PageData)
	tb := a.Tabs[0].(*TableTab)
	v := tb.view
	testutil.WaitFor(t, tt, "the rows", func() bool { return len(v.src.Rows) == 2 && tb.columns != nil && !v.loading })
	for _, nocount := range []bool{false, true} {
		if nocount {
			sess, err := connection.OpenSession(context.Background(), cn.DB(), "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := sess.Exec(context.Background(), "SET NOCOUNT ON"); err != nil {
				t.Fatal(err)
			}
			v.source.AdoptSession(sess)
		}
		v.grid.setValue(&v.src, 0, 1, db.Typed(map[bool]string{false: "5", true: "6"}[nocount]))
		v.reviewChanges()
		testutil.WaitFor(t, tt, "the review", func() bool { return a.Confirm != nil })
		a.Confirm.OnConfirm()
		a.Confirm = nil
		testutil.WaitFor(t, tt, "the apply", func() bool { return !v.applying && !v.loading })
		var got int
		setup.SQL.QueryRow("SELECT v FROM dbo.it_grid_counted WHERE id = 1").Scan(&got)
		if want := map[bool]int{false: 5, true: 6}[nocount]; got != want || v.grid.edits.count() != 0 {
			t.Fatalf("NOCOUNT %v: v %d, %d edits left; errors %v", nocount, got, v.grid.edits.count(), a.Errors)
		}
	}
}

// A filter or a reference search for text holding [ matches it as
// written: SQL Server's LIKE reads [ as the start of a set of characters.
func TestIntegrationSQLServerLikeBrackets(t *testing.T) {
	testutil.Integration(t)
	optionalserver.SkipUnless(t, "sqlserver")
	cfg := db.Config{ID: "ss", Name: "ss", Engine: db.SQLServer, Host: "127.0.0.1", Port: 11433, User: "sa", Password: "Dgopher-1", Database: "shop"}
	d, err := db.Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	defer d.SQL.Exec("DROP TABLE IF EXISTS dbo.it_like")
	for _, q := range []string{"DROP TABLE IF EXISTS dbo.it_like", "CREATE TABLE dbo.it_like (s nvarchar(20))",
		`INSERT INTO dbo.it_like VALUES (N'a[b]c'), (N'abc'), (N'a[^b]'), (N'x_y'), (N'50%'), (N'back\[slash')`} {
		if _, err := d.SQL.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	for search, want := range map[string]int{"[b]": 1, "[^b]": 1, "]": 2, "_": 1, "%": 1, `\[`: 1, "b": 4} {
		var n int
		if err := d.SQL.QueryRow("SELECT count(*) FROM dbo.it_like WHERE " + likeSQL(db.SQLServer, "[s]", "%", search, "%", false)).Scan(&n); err != nil || n != want {
			t.Errorf("contains %q: %d rows (%v), want %d", search, n, err, want)
		}
	}
}
