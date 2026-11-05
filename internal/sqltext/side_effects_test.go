package sqltext

import (
	"strings"
	"testing"
)

// MySQL reads "--" before any control character as a comment, so an
// executable comment after "--\f" on the same line is code, not comment.
func TestClassifyExecutableAfterControlCharDash(t *testing.T) {
	for _, src := range []string{
		"SELECT 1 --\f '\n/*!50000 INTO OUTFILE \"/tmp/x\" */ -- '",
		"SELECT 1 --\v '\n/*!50000 INTO OUTFILE \"/tmp/x\" */ -- '",
		"SELECT 1 --\r '\n/*!50000 INTO OUTFILE \"/tmp/x\" */ -- '",
	} {
		if a := ClassifyExecutable(src, MySQL); a.Class == Read {
			t.Errorf("%q: got %+v, want a write", src, a)
		}
	}
	// The safety policy reads the same tokens for what turns read-only off.
	src := "SELECT 1 --\f '\n/*!50000 , @@session.transaction_read_only */ -- '"
	for _, toks := range TokenizeExecutable(src, MySQL) {
		for _, tok := range toks {
			if tok.Kind != Comment && tok.Kind != String && strings.Contains(tok.Text, "read_only") {
				return
			}
		}
	}
	t.Errorf("%q: no reading holds transaction_read_only as code", src)
}

// A call of a function that changes state is a write wherever it stands:
// in a SELECT, and in the value a SET gives a variable, as is a lock a
// SET's subquery takes.
func TestClassifySideEffects(t *testing.T) {
	for _, c := range []struct {
		d   Dialect
		src string
	}{
		{Postgres, "SELECT dblink_send_query('c', 'DELETE FROM t')"},
		{Postgres, "SELECT lo_creat(-1)"},
		{Postgres, "SELECT pg_file_unlink('x')"},
		{Postgres, "SELECT pg_file_rename('x', 'y')"},
		{Postgres, "SELECT txid_current()"},
		{Postgres, "SELECT pg_current_xact_id()"},
		{MySQL, "SET @x = GET_LOCK('l', 10)"},
		{MySQL, "SET @x = (SELECT a FROM t WHERE id = 1 FOR UPDATE)"},
		{MySQL, "SET @x = SLEEP(1)"},
		{MySQL, "SET @x = NEXTVAL(s)"},
		{Postgres, "SET VARIABLE x = (SELECT nextval('s'))"},
	} {
		if a := ClassifyExecutable(c.src, c.d); a.Class != Write {
			t.Errorf("%s: got %+v, want a write", c.src, a)
		}
	}
	for _, c := range []struct {
		d   Dialect
		src string
	}{
		{MySQL, "SET @x = 1"},
		{MySQL, "SET @x = (SELECT a FROM t WHERE id = 1)"},
		{MySQL, "SET NAMES utf8mb4"},
		{Postgres, "SET search_path = public"},
	} {
		if a := ClassifyExecutable(c.src, c.d); a.Class != Session {
			t.Errorf("%s: got %+v, want a session statement", c.src, a)
		}
	}
}
