package query

import (
	"fmt"
	"syscall"
	"testing"

	"dgopher/internal/db"
)

// A statement that fails because the connection failed shows the hint of
// its class under the message; one stopped by the statement timeout does
// not take the connect timeout's hint.
func TestErrorPanelShowsConnectionHint(t *testing.T) {
	_, tt, _, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "SELECT * FROM nowhere")
	runWith(t, tt, q, "SELECT", RunStatement)
	if len(q.results) != 1 || q.results[0].err == "" {
		t.Fatalf("ran %+v", q.results)
	}
	refused := fmt.Errorf("read: %w", syscall.ECONNREFUSED)
	hint := db.ProblemRefused.Hint()
	q.results[0].errHint = runErrorHint(refused, false)
	tt.Frame()
	if !tt.HasText(hint) {
		t.Fatalf("no hint %q: %q", hint, tt.Texts())
	}
	if got := runErrorHint(refused, true); got != "" {
		t.Errorf("a statement timeout got the hint %q", got)
	}
	if got := runErrorHint(fmt.Errorf("no such table: nowhere"), false); got != "" {
		t.Errorf("an unknown error got the hint %q", got)
	}
}
