package query

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dgopher/internal/audit"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// lostSchema makes schema, holding table, and public.table beside it:
// where an unqualified name lands on a connection made again without the
// session's SET search_path. Both are dropped at the end.
func lostSchema(t *testing.T, schema, table string, seed ...string) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), testutil.PGConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		d.SQL.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
		d.SQL.Exec("DROP TABLE IF EXISTS public." + table)
		d.Close()
	})
	for _, s := range append([]string{"DROP SCHEMA IF EXISTS " + schema + " CASCADE", "CREATE SCHEMA " + schema,
		"CREATE TABLE " + schema + "." + table + " (id int PRIMARY KEY, v text)",
		"DROP TABLE IF EXISTS public." + table, "CREATE TABLE public." + table + " (id int PRIMARY KEY, v text)"}, seed...) {
		if _, err := d.SQL.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return d
}

// rowCount counts the rows of a table on the server.
func rowCount(t *testing.T, d *db.DB, table string) int {
	t.Helper()
	var n int
	if err := d.SQL.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A script that loses its connection after a SET search_path stops at the
// refusal that follows, even when the editor continues after errors: the
// rest would run on the new connection, in the default schema.
func TestResetStopsContinueOnError(t *testing.T) {
	testutil.Integration(t)
	d := lostSchema(t, "zz_fix_reset", "zz_fix_reset_rows")
	a := newFakeQueryHost(t)
	a.Settings().ContinueOnError = true
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.view, 1200, 760)
	q := newEditor(t, a, tt, cn, "SET search_path TO zz_fix_reset")
	t.Cleanup(func() { q.sess.Close() })
	runWith(t, tt, q, "SET", RunStatement)
	if len(q.results) != 1 || q.results[0].err != "" {
		t.Fatalf("SET: %+v", q.results)
	}
	if _, err := d.SQL.Exec("SELECT pg_terminate_backend(" + sessionValue(t, q, "SELECT pg_backend_pid()") + ")"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	// RETURNING reads the rows each INSERT wrote.
	q.Editor.Text = "INSERT INTO zz_fix_reset_rows VALUES (1, 'one') RETURNING id;\nINSERT INTO zz_fix_reset_rows VALUES (2, 'two') RETURNING id;\nINSERT INTO zz_fix_reset_rows VALUES (3, 'three') RETURNING id;"
	runWith(t, tt, q, "INSERT", RunScript)
	if a.Confirm != nil {
		t.Fatalf("asked: %+v", a.Confirm)
	}
	last := q.results[len(q.results)-1]
	if len(q.results) == 3 || !strings.Contains(last.err, "made again") {
		t.Fatalf("the run went on past the refusal: %d results, the last %q", len(q.results), last.err)
	}
	if !slices.ContainsFunc(q.messages, func(m message) bool {
		return m.err && strings.Contains(m.text, "Stopped") && strings.Contains(m.text, "connection was lost")
	}) {
		t.Fatalf("no message says why the run stopped: %+v", q.messages)
	}
	for _, table := range []string{"public.zz_fix_reset_rows", "zz_fix_reset.zz_fix_reset_rows"} {
		if n := rowCount(t, d, table); n != 0 {
			t.Errorf("%s holds %d rows", table, n)
		}
	}
}

// A connection lost while grid edits apply takes their transaction with
// it, which the server rolled back: the apply runs no undo on the
// connection made again, so the user's next statement is the one refused,
// rather than run in the default schema.
func TestApplyLostConnectionKeepsRefusal(t *testing.T) {
	testutil.Integration(t)
	d := lostSchema(t, "zz_fix_apply", "zz_fix_apply_rows", "INSERT INTO zz_fix_apply.zz_fix_apply_rows VALUES (1, 'alpha'), (2, 'beta')")
	a := newFakeQueryHost(t)
	cn := a.AddConn(testutil.PGConfig())
	tt := ui.NewTester(a.view, 1200, 760)
	q := newEditor(t, a, tt, cn, "SET search_path TO zz_fix_apply;\n\nSELECT * FROM zz_fix_apply_rows ORDER BY id;\n\nINSERT INTO zz_fix_apply_rows VALUES (3, 'gamma');")
	t.Cleanup(func() { q.sess.Close() })
	testutil.WaitFor(t, tt, "the schemas", func() bool { return cn.DefaultSchema != "" })
	runWith(t, tt, q, "SET", RunStatement)
	runWith(t, tt, q, "SELECT", RunStatement)
	waitEditable(t, tt)
	kill := "SELECT pg_terminate_backend(" + sessionValue(t, q, "SELECT pg_backend_pid()") + ")"
	editCell(t, tt, "alpha", "one")
	editCell(t, tt, "beta", "two")
	var once sync.Once
	a.afterRun = func(kind string) {
		if kind == audit.KindEdit {
			once.Do(func() {
				d.SQL.Exec(kill)
				time.Sleep(300 * time.Millisecond)
			})
		}
	}
	tt.Key(ui.Cmd, ui.KeyS)
	testutil.WaitFor(t, tt, "the review", func() bool { return a.Confirm != nil })
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "the apply", func() bool { return !q.Busy() })
	if len(a.Errors) != 1 || !strings.Contains(a.Errors[0], "lost") {
		t.Fatalf("errors %q", a.Errors)
	}
	// The changes not applied are dropped as the user's next statement runs.
	edited := q.results[0]
	testutil.SetCaret(tt, &q.Editor, strings.Index(q.Editor.Text, "INSERT"))
	q.Run(RunStatement)
	if a.Pending == nil {
		t.Fatal("no question about the changes not applied")
	}
	a.Pending.Discard()
	a.Pending = nil
	testutil.WaitFor(t, tt, "the run", func() bool { return !q.Running && len(q.results) == 1 && q.results[0] != edited })
	if r := q.results[len(q.results)-1]; !strings.Contains(r.err, "made again") {
		t.Fatalf("the statement after the lost apply: error %q", r.err)
	}
	if n := rowCount(t, d, "public.zz_fix_apply_rows"); n != 0 {
		t.Errorf("the default schema's table holds %d rows", n)
	}
	var v string
	d.SQL.QueryRow("SELECT string_agg(v, ',' ORDER BY id) FROM zz_fix_apply.zz_fix_apply_rows").Scan(&v)
	if v != "alpha,beta" {
		t.Errorf("the edited table holds %q", v)
	}
}
