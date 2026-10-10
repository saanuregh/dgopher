package dataview

import (
	"context"
	"strings"
	"testing"

	"dgopher/internal/connection"
	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo/ui"
)

// A change the app writes runs in a transaction of its own, which cannot
// begin inside another session's on a connection every session shares:
// DuckDB's BEGIN there would fail and abort that transaction. It is
// refused before anything is sent, before it is asked and once agreed, and
// the transaction stays open.
func TestApplyChangeRefusedInSharedTx(t *testing.T) {
	a := NewFakeHost(t)
	cn := a.AddConn(db.Config{ID: "duck", Name: "duck", Engine: db.DuckDB, Database: ":memory:"})
	tt := ui.NewTester(a.View, 1000, 700)
	a.Connect(cn, nil)
	testutil.WaitFor(t, tt, "connect", func() bool { return cn.Status == connection.StatusConnected })
	ctx := context.Background()
	owner, err := cn.DB.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.Close() })
	run := func(stmts ...string) {
		t.Helper()
		for _, s := range stmts {
			if _, err := owner.Exec(ctx, s); err != nil {
				t.Fatalf("%s: %v", s, err)
			}
		}
	}
	run("CREATE TABLE t (a INT)", "BEGIN", "INSERT INTO t VALUES (1)")
	ch := db.SchemaChange{Steps: []db.Step{{SQL: "CREATE TABLE u (a INT)"}}, Atomic: true}
	var done []error
	ApplyChange(a, cn, "", "Create table u?", ch, "Review the statements before they run.", nil, func(err error) { done = append(done, err) })
	tt.Frame()
	if a.Confirm != nil || len(a.Errors) != 1 || !strings.Contains(a.Errors[0], "commit or roll it back first") || len(done) != 0 {
		t.Fatalf("asked %v, errors %q, done %v", a.Confirm != nil, a.Errors, done)
	}
	if owner.Tx() != db.TxOpen || !owner.OwnsTx() {
		t.Fatalf("the open transaction: %v, owned %v", owner.Tx(), owner.OwnsTx())
	}
	a.Errors = nil

	// A transaction begun while the change waits to be agreed.
	run("COMMIT")
	ApplyChange(a, cn, "", "Create table u?", ch, "Review the statements before they run.", nil, func(err error) { done = append(done, err) })
	if a.Confirm == nil {
		t.Fatal("not asked")
	}
	run("BEGIN", "INSERT INTO t VALUES (2)")
	a.Confirm.OnConfirm()
	a.Confirm = nil
	testutil.WaitFor(t, tt, "the change to end", func() bool { return len(done) == 1 })
	if done[0] == nil || !strings.Contains(done[0].Error(), "commit or roll it back first") {
		t.Fatalf("the change agreed to: %v", done[0])
	}
	if owner.Tx() != db.TxOpen || !owner.OwnsTx() {
		t.Fatalf("the open transaction: %v, owned %v", owner.Tx(), owner.OwnsTx())
	}
	run("ROLLBACK")
	var n int
	if err := cn.DB.SQL.QueryRow("SELECT count(*) FROM information_schema.tables WHERE table_name = 'u'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("table u: %d, %v", n, err)
	}
}
