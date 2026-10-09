package db

import "testing"

func TestLiteral(t *testing.T) {
	cases := []struct {
		e    Engine
		v    any
		want string
	}{
		{Postgres, nil, "NULL"},
		{Postgres, int64(42), "42"},
		{Postgres, "it's", "'it''s'"},
		{Postgres, `a\'b`, `'a\''b'`},
		{SQLite, "x'); DROP TABLE t; --", "'x''); DROP TABLE t; --'"},
		{MySQL, `a\'b`, `'a\\''b'`},
		{MySQL, "nul\x00", `'nul\0'`},
		{ClickHouse, `a\'b`, `'a\\\'b'`},
		{DuckDB, true, "TRUE"},
	}
	for _, c := range cases {
		if got := Literal(c.e, c.v); got != c.want {
			t.Errorf("Literal(%s, %q) = %s, want %s", c.e, c.v, got, c.want)
		}
	}
}

func TestLiteralRoundTrip(t *testing.T) {
	if testing.Short() {
		return
	}
	integration(t)
	hostile := []string{`it's`, `back\slash`, `\'; DROP TABLE x; --`, "new\nline", `trailing\`, `''`}
	for _, f := range fixtures(t) {
		d := open(t, f)
		for _, s := range hostile {
			var got string
			if err := d.SQL.QueryRow("SELECT " + Literal(f.cfg.Engine, s)).Scan(&got); err != nil || got != s {
				t.Errorf("%s: %q came back as %q (%v)", f.cfg.Name, s, got, err)
			}
		}
	}
}

func TestRedact(t *testing.T) {
	cfg := Config{Password: "hunter2!", SSH: SSHConfig{Password: "s3cr3t"}}
	err := redact(errorString("auth failed for postgres://u:hunter2%21@h/db and ssh s3cr3t"), cfg)
	if got := err.Error(); got != "auth failed for postgres://u:•••@h/db and ssh •••" {
		t.Fatalf("redacted %q", got)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

func TestUpdateToDefault(t *testing.T) {
	target := EditTarget{Dialect: DialectOf(Postgres), Schema: "s", Table: "t", Key: []string{"id"},
		Columns: []Column{{Name: "id", Type: "integer"}, {Name: "status", Type: "text"}}}
	stmts, err := target.Statements([]Change{{Kind: ChangeUpdate, Key: []any{int64(1)}, Values: map[string]any{"status": Default}}})
	if err != nil || len(stmts) != 1 || stmts[0].SQL != `UPDATE "s"."t" SET "status" = DEFAULT WHERE "id" = $1` || len(stmts[0].Args) != 1 {
		t.Fatalf("%+v %v", stmts, err)
	}
}

func TestLiteralOfBytes(t *testing.T) {
	b := []byte{0x01, 0xab, 'A'}
	want := map[Engine]string{
		Postgres:   `'\x01ab41'::bytea`,
		MySQL:      `X'01ab41'`,
		SQLite:     `X'01ab41'`,
		DuckDB:     `'\x01\xAB\x41'::BLOB`,
		ClickHouse: `unhex('01ab41')`,
	}
	for e, w := range want {
		if got := Literal(e, b); got != w {
			t.Errorf("%s: %s, want %s", e, got, w)
		}
	}
}

// TestStatementScript checks a statement's parameters are written as
// literals of its engine, and placeholders inside quoted names are not.
func TestStatementScript(t *testing.T) {
	pg := Statement{SQL: `UPDATE "t" SET "a$1" = $2::text::numeric WHERE "id" = $1`, Args: []any{int64(7), Typed("1.5")}}
	if got, want := pg.Script(Postgres), `UPDATE "t" SET "a$1" = '1.5'::text::numeric WHERE "id" = 7;`; got != want {
		t.Errorf("PostgreSQL: %s, want %s", got, want)
	}
	my := Statement{SQL: "INSERT INTO `t` (`a?`, `b`, `c`) VALUES (?, ?, ?)", Args: []any{`it's \ here`, nil, []byte{1}}}
	if got, want := my.Script(MySQL), "INSERT INTO `t` (`a?`, `b`, `c`) VALUES ('it''s \\\\ here', NULL, X'01');"; got != want {
		t.Errorf("MySQL: %s, want %s", got, want)
	}
	if got, want := my.Preview(), "INSERT INTO `t` (`a?`, `b`, `c`) VALUES ('it''s \\ here', NULL, '\x01');"; got != want {
		t.Errorf("preview: %q, want %q", got, want)
	}
}
