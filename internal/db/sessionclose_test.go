package db

import (
	"database/sql"
	"errors"
	"testing"
)

// Closing an Oracle session whose rollback fails says that the
// transaction may have been committed: go-ora's close logs off, and Oracle
// commits on logoff. The fake driver cannot end a transaction, so the
// rollback Close sends first fails.
func TestSessionCloseFailedRollbackOnDriverThatCommits(t *testing.T) {
	var ran []string
	pool := sql.OpenDB(&fakeConnector{ran: &ran})
	t.Cleanup(func() { pool.Close() })
	d := &DB{Config: Config{Name: "close-fake", Engine: Oracle}, SQL: pool}
	s := &Session{db: d}
	if err := s.connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.verbs.tx = TxOpen
	err := s.Close()
	if !errors.Is(err, ErrTxMayBeCommitted) {
		t.Fatalf("Close: %v, want ErrTxMayBeCommitted", err)
	}
	if s.conn != nil {
		t.Fatal("the connection was kept")
	}
}
