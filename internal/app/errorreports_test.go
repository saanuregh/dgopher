package app

import (
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/store"

	"github.com/go-sql-driver/mysql"
)

// failingSecrets is a keychain whose every write fails with err.
type failingSecrets struct {
	store.Secrets
	err error
}

func (s failingSecrets) Set(key, value string) error { return s.err }

// A failure's dialog carries the hint the error has, under the same title.
func TestErrorReportsCarryHint(t *testing.T) {
	clash := &mysql.MySQLError{Number: 9007, Message: "Write conflict"}
	st, err := store.Open(t.TempDir(), failingSecrets{store.MemorySecrets(), clash})
	if err != nil {
		t.Fatal(err)
	}
	a := startApp(t, st)
	a.keepPassword(&db.Config{ID: "c1", Name: "c1"}, "pw")
	al := a.frontAlert()
	if al == nil {
		t.Fatal("no dialog")
	}
	want := db.DescribeError(clash).Hint
	if want == "" {
		t.Fatal("the error has no hint to show")
	}
	if al.report.Title != "Could not save the password in the keychain" || al.report.Hint != want {
		t.Fatalf("report %q with hint %q, want the hint %q", al.report.Title, al.report.Hint, want)
	}
}
