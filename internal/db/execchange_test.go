package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
)

// triggerConn answers SQL Server's change batch as a trigger that selects
// a row would: the trigger's result first, then @@ROWCOUNT's. Any other
// query reads one row of 0.
type triggerConn struct{}

func (triggerConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("no prepare") }
func (triggerConn) Close() error                        { return nil }
func (triggerConn) Begin() (driver.Tx, error)           { return nil, errors.New("no transactions") }
func (triggerConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}
func (triggerConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "@@ROWCOUNT") {
		return &resultSets{sets: [][]string{{"1"}, {"5"}}}, nil
	}
	return &resultSets{sets: [][]string{{"0"}}}, nil
}

type resultSets struct {
	sets [][]string
	row  int
}

func (r *resultSets) Columns() []string { return []string{"n"} }
func (r *resultSets) Close() error      { return nil }
func (r *resultSets) Next(dest []driver.Value) error {
	if r.row >= len(r.sets[0]) {
		return io.EOF
	}
	dest[0] = r.sets[0][r.row]
	r.row++
	return nil
}
func (r *resultSets) HasNextResultSet() bool { return len(r.sets) > 1 }
func (r *resultSets) NextResultSet() error {
	if len(r.sets) < 2 {
		return io.EOF
	}
	r.sets, r.row = r.sets[1:], 0
	return nil
}

type triggerConnector struct{}

func (triggerConnector) Connect(context.Context) (driver.Conn, error) { return triggerConn{}, nil }
func (triggerConnector) Driver() driver.Driver                        { return nil }

// A trigger's result read before the change's own row count is not taken
// for it: SQL Server's change batch reads @@ROWCOUNT last.
func TestExecChangeSkipsTriggerResults(t *testing.T) {
	pool := sql.OpenDB(triggerConnector{})
	t.Cleanup(func() { pool.Close() })
	d := &DB{Config: Config{Name: "trigger-fake", Engine: SQLServer}, SQL: pool}
	s, err := d.Session(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n, err := s.ExecChange(t.Context(), "UPDATE t SET a = 1 WHERE id = @p1", 1)
	if err != nil || n != 5 {
		t.Fatalf("ExecChange = %d, %v; want 5", n, err)
	}
}
