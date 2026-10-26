package app

import (
	"slices"
	"testing"

	"dgopher/internal/db"
)

// Conditions of the app that became rules calls, against the engine
// comparisons they replaced, for each engine and an unknown one.
func TestRewrittenEngineConditions(t *testing.T) {
	for _, e := range pinEngines {
		// importfile.go: a table made before the rows' transaction.
		if got, old := db.SchemaChangesCommit(e), e == db.MySQL || e == db.ClickHouse; got != old {
			t.Errorf("%s: schema changes commit %v, was %v", e, got, old)
		}
		// layout.go: the status bar's commit mode.
		if got, old := e.Transactions(), e.IsSQL() && e != db.ClickHouse; got != old {
			t.Errorf("%s: transactions %v, was %v", e, got, old)
		}
		// transfer.go: how a copy that replaces rows empties the table.
		if got, old := db.TruncateToEmpty(e), e == db.ClickHouse; got != old {
			t.Errorf("%s: truncate to empty %v, was %v", e, got, old)
		}
		// nav.go: a new editor on a schema node.
		old := ""
		if e == db.Postgres {
			old = "SET search_path TO " + db.DialectOf(e).Quote("Sales") + ";\n\n"
		}
		if got := db.EditorSchemaPreamble(e, "Sales"); got != old {
			t.Errorf("%s: editor schema preamble %q, was %q", e, got, old)
		}
		// nav.go and palette.go: the Users and Privileges command.
		if got, old := db.UsersSupported(e), e == db.Postgres || e == db.MySQL || e == db.ClickHouse; got != old {
			t.Errorf("%s: users supported %v, was %v", e, got, old)
		}
		// connform.go: the password sources offered.
		if got, old := e.CloudIdentity(), e == db.Postgres || e == db.MySQL; got != old {
			t.Errorf("%s: cloud identity %v, was %v", e, got, old)
		}
		// users.go: the note under an account's privileges, and the host
		// a new MySQL account is made with.
		oldNote := ""
		if e == db.Postgres {
			oldNote = "Those on the database's own tables and schemas, and on its databases."
		}
		form := db.AccountForm(e)
		if form.PrivilegesNote != oldNote {
			t.Errorf("%s: privileges note %q, was %q", e, form.PrivilegesNote, oldNote)
		}
		if old := e == db.MySQL; form.Host != old {
			t.Errorf("%s: account host %v, was %v", e, form.Host, old)
		}
		// connform.go: the file chooser's extensions, which only a file
		// engine's form offers.
		if e.IsFile() {
			old := []string{"sqlite", "sqlite3", "db", "db3"}
			if e == db.DuckDB {
				old = []string{"duckdb", "ddb", "db"}
			}
			if got := e.FileExtensions(); !slices.Equal(got, old) {
				t.Errorf("%s: file extensions %q, were %q", e, got, old)
			}
		}
	}
}
