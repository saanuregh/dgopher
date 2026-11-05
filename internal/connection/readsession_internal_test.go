package connection

import (
	"context"
	"errors"
	"slices"
	"testing"

	"dgopher/internal/db"
)

// fakeReadSession records the statements a read runs, in order.
type fakeReadSession struct {
	calls    []string
	queryErr error
}

func (f *fakeReadSession) Exec(_ context.Context, query string, _ ...any) (int64, error) {
	f.calls = append(f.calls, "exec "+query)
	return 0, nil
}

func (f *fakeReadSession) Query(_ context.Context, query string, _ ...any) (*db.Cursor, error) {
	f.calls = append(f.calls, "query "+query)
	return nil, f.queryErr
}

func (f *fakeReadSession) Rollback(context.Context) error {
	f.calls = append(f.calls, "rollback")
	return nil
}

func (f *fakeReadSession) Close() error {
	f.calls = append(f.calls, "close")
	return nil
}

// A read begins read-only first where the engine has it, and its release
// rolls back and closes the session; on an engine without it the read
// runs alone. A failed read releases the session at once.
func TestReadOn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		begin    string
		queryErr error
		want     []string
	}{
		{"read-only", "START TRANSACTION READ ONLY", nil, []string{"exec START TRANSACTION READ ONLY", "query SELECT 1", "rollback", "close"}},
		{"none", "", nil, []string{"query SELECT 1", "close"}},
		{"failed", "START TRANSACTION READ ONLY", errors.New("boom"), []string{"exec START TRANSACTION READ ONLY", "query SELECT 1", "rollback", "close"}},
	} {
		f := &fakeReadSession{queryErr: tc.queryErr}
		_, release, err := readOn(context.Background(), f, tc.begin, "SELECT 1")
		if (err != nil) != (tc.queryErr != nil) {
			t.Fatalf("%s: err %v", tc.name, err)
		}
		if release != nil {
			release()
		}
		if !slices.Equal(f.calls, tc.want) {
			t.Fatalf("%s: calls %q, want %q", tc.name, f.calls, tc.want)
		}
	}
}
