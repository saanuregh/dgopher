package db

import "testing"

// A bracketed name holding ]] ends at its last ], so a placeholder's text
// inside it is left as it is: SQL Server writes ] in a name as ]].
func TestStatementInlineBracketEscape(t *testing.T) {
	s := Statement{SQL: `UPDATE [t] SET [a]]@p1] = @p1`, Args: []any{int64(5)}}
	if got, want := s.Script(SQLServer), `UPDATE [t] SET [a]]@p1] = 5;`; got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
