package db

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duckdb/duckdb-go/v2"
	"github.com/go-sql-driver/mysql"
)

// fakeMySQL accepts one connection and greets it as a MySQL server whose
// OK packet carries status, the flags the driver keeps from it: enough to
// read them back without a server.
func fakeMySQL(t *testing.T, status uint16) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		write := func(seq byte, payload []byte) {
			n := len(payload)
			c.Write(append([]byte{byte(n), byte(n >> 8), byte(n >> 16), seq}, payload...))
		}
		// Long password, long flag, protocol 4.1, transactions, secure
		// connection and plugin authentication.
		caps := uint32(1<<0 | 1<<2 | 1<<9 | 1<<13 | 1<<15 | 1<<19)
		hello := []byte{10}
		hello = append(hello, "8.4.0\x00"...)
		hello = append(hello, 1, 0, 0, 0) // connection id
		hello = append(hello, "abcdefgh"...)
		hello = append(hello, 0, byte(caps), byte(caps>>8), 33, 2, 0, byte(caps>>16), byte(caps>>24), 21)
		hello = append(hello, make([]byte, 10)...)
		hello = append(hello, "ijklmnopqrst\x00mysql_native_password\x00"...)
		write(0, hello)
		head := make([]byte, 4)
		if _, err := io.ReadFull(c, head); err != nil {
			return
		}
		io.ReadFull(c, make([]byte, int(head[0])|int(head[1])<<8|int(head[2])<<16))
		write(2, []byte{0, 0, 0, byte(status), byte(status >> 8), 0, 0})
		io.Copy(io.Discard, c)
	}()
	return ln.Addr().String()
}

// The transaction comes from what each driver or engine keeps, read
// through unexported fields for MySQL and SQLite: an upgrade that renames
// them fails here rather than falling back to the statements' verbs.
func TestTxStateFromDriver(t *testing.T) {
	ctx := context.Background()
	t.Run("mysql", func(t *testing.T) {
		const inTrans, autocommit = 1, 2
		for _, c := range []struct {
			status        uint16
			want          TxState
			autocommitOff bool
		}{
			{autocommit, TxNone, false},
			{inTrans | autocommit, TxOpen, false},
			// Autocommit off opens nothing until a statement begins one.
			{0, TxNone, true},
			{inTrans, TxOpen, true},
		} {
			cfg := mysql.NewConfig()
			cfg.User, cfg.Net, cfg.Addr = "u", "tcp", fakeMySQL(t, c.status)
			connector, err := mysql.NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			pool := sql.OpenDB(connector)
			conn, err := pool.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			state, ok := readTxState(ctx, conn)
			off, offOK := readMySQLAutocommitOff(conn)
			conn.Close()
			pool.Close()
			if !ok || !offOK {
				t.Fatal("the MySQL driver's status flags were not found: go-sql-driver/mysql changed mysqlConn.status")
			}
			if state != c.want || off != c.autocommitOff {
				t.Errorf("status %#x read as %v, autocommit off %v; want %v, %v", c.status, state, off, c.want, c.autocommitOff)
			}
		}
	})
	t.Run("sqlite", func(t *testing.T) {
		pool, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "s.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		conn, err := pool.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		read := func(want TxState) {
			t.Helper()
			state, ok := readTxState(ctx, conn)
			if !ok {
				t.Fatal("sqlite3_get_autocommit was not reached: modernc.org/sqlite changed conn.db or conn.tls")
			}
			if state != want {
				t.Fatalf("state %v, want %v", state, want)
			}
		}
		read(TxNone)
		for _, step := range []struct {
			sql  string
			want TxState
		}{
			{"/* why */ BEGIN", TxOpen},
			{"ROLLBACK", TxNone},
			{"SAVEPOINT a", TxOpen},
			{"RELEASE a", TxNone},
		} {
			if _, err := conn.ExecContext(ctx, step.sql); err != nil {
				t.Fatalf("%s: %v", step.sql, err)
			}
			read(step.want)
		}
	})
	t.Run("duckdb", func(t *testing.T) {
		pool, err := sql.Open("duckdb", "")
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		conn, err := pool.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		// Read without an id remembered, and with the one the read after
		// the previous statement left, as DB.lastTxID.
		var last int64
		for _, step := range []struct {
			sql   string
			fails bool
			want  TxState
		}{
			{"CREATE TABLE t (a INT PRIMARY KEY)", false, TxNone},
			{"INSERT INTO t VALUES (0)", false, TxNone},
			{"-- why\nBEGIN", false, TxOpen},
			{"INSERT INTO t VALUES (1)", false, TxOpen},
			{"SELEC 1", true, TxOpen}, // a parse error leaves it usable
			{"INSERT INTO t VALUES (2)", false, TxOpen},
			{"INSERT INTO t VALUES (1)", true, TxFailed},
			{"SELECT 1", true, TxFailed},
			{"ROLLBACK", false, TxNone},
			{"SELECT 1", false, TxNone},
			{"BEGIN", false, TxOpen},
			{"COMMIT", false, TxNone},
		} {
			if _, err := conn.ExecContext(ctx, step.sql); (err != nil) != step.fails {
				t.Fatalf("%s: %v", step.sql, err)
			}
			for _, remembered := range []*int64{nil, &last} {
				state, ok := readTxStateWithLastID(ctx, conn, remembered)
				if !ok || state != step.want {
					t.Fatalf("after %s (id remembered: %v): %v (read %v), want %v", step.sql, remembered != nil, state, ok, step.want)
				}
			}
		}
	})
	t.Run("duckdb sessions", func(t *testing.T) {
		d, err := Open(ctx, Config{Name: "d", Engine: DuckDB, Database: ":memory:"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		a, _ := d.Session(ctx)
		defer a.Close()
		b, _ := d.Session(ctx)
		defer b.Close()
		for _, step := range []struct {
			s     *Session
			sql   string
			fails bool
			want  TxState
			owner *Session
		}{
			{a, "CREATE TABLE t (a INT PRIMARY KEY)", false, TxNone, nil},
			{b, "INSERT INTO t VALUES (0)", false, TxNone, nil},
			{a, "-- why\nBEGIN", false, TxOpen, a},
			{b, "INSERT INTO t VALUES (1)", false, TxOpen, a},
			{a, "INSERT INTO t VALUES (2)", false, TxOpen, a},
			{b, "SELEC 1", true, TxOpen, a},
			{a, "INSERT INTO t VALUES (1)", true, TxFailed, a},
			{b, "SELECT 1", true, TxFailed, a},
			{a, "ROLLBACK", false, TxNone, nil},
			{b, "INSERT INTO t VALUES (3)", false, TxNone, nil},
			{b, "BEGIN", false, TxOpen, b},
			{a, "SELECT 1", false, TxOpen, b},
			{a, "COMMIT", false, TxNone, nil}, // typed, Exec sends it; Commit would refuse (ErrNotOwner)
			{a, "SELECT 1", false, TxNone, nil},
		} {
			if _, err := step.s.Exec(ctx, step.sql); (err != nil) != step.fails {
				t.Fatalf("%s: %v", step.sql, err)
			}
			state, owner := d.SharedTx()
			if state != step.want || owner != step.owner || a.Tx() != step.want || b.Tx() != step.want {
				t.Fatalf("after %s: shared %v owned by a %v, b %v; a sees %v, b %v; want %v owned by a %v, b %v",
					step.sql, state, owner == a, owner == b, a.Tx(), b.Tx(), step.want, step.owner == a, step.owner == b)
			}
		}
		var n int
		d.SQL.QueryRow("SELECT count(*) FROM t").Scan(&n)
		if n != 2 {
			t.Fatalf("%d rows, want the two committed outside the rolled-back transaction", n)
		}
	})
}

// Sessions on SQLite and DuckDB see transactions their statements' first
// word hides.
func TestSessionTxFromDriver(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, sqliteFile(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s, _ := d.Session(ctx)
	defer s.Close()
	for _, step := range []struct {
		sql  string
		want TxState
	}{
		{"/* why */ BEGIN", TxOpen},
		{"ROLLBACK", TxNone},
		{"SAVEPOINT a", TxOpen},
		{"RELEASE a", TxNone},
	} {
		if _, err := s.Exec(ctx, step.sql); err != nil {
			t.Fatalf("%s: %v", step.sql, err)
		}
		if got := s.Tx(); got != step.want {
			t.Fatalf("SQLite: after %s: %v, want %v", step.sql, got, step.want)
		}
	}

	duck, err := Open(ctx, Config{Name: "d", Engine: DuckDB, Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer duck.Close()
	ds, _ := duck.Session(ctx)
	defer ds.Close()
	ds.Exec(ctx, "CREATE TABLE t (a INT PRIMARY KEY)")
	if _, err := ds.Exec(ctx, "-- why\nBEGIN"); err != nil || ds.Tx() != TxOpen {
		t.Fatalf("DuckDB: after a commented BEGIN: %v %v", ds.Tx(), err)
	}
	ds.Exec(ctx, "INSERT INTO t VALUES (1)")
	if _, err := ds.Exec(ctx, "INSERT INTO t VALUES (1)"); err == nil {
		t.Fatal("DuckDB took a duplicate key")
	}
	if ds.Tx() != TxFailed {
		t.Fatalf("DuckDB: %v after an error aborted the transaction, want TxFailed", ds.Tx())
	}
	if err := ds.Commit(ctx); !errors.Is(err, ErrTxFailed) {
		t.Fatalf("DuckDB: commit of an aborted transaction: %v", err)
	}
	var n int
	duck.SQL.QueryRow("SELECT count(*) FROM t").Scan(&n)
	if n != 0 || ds.Tx() != TxNone {
		t.Fatalf("DuckDB: %d rows, %v after the commit of an aborted transaction", n, ds.Tx())
	}
}

// BenchmarkDuckDBExecState measures a tiny INSERT through a session, which
// reads the transaction it left, in autocommit and inside a transaction;
// raw is the same INSERT on the pool, which reads nothing.
func BenchmarkDuckDBExecState(b *testing.B) {
	ctx := context.Background()
	d, err := Open(ctx, Config{Name: "d", Engine: DuckDB, Database: ":memory:"}, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer d.Close()
	s, err := d.Session(ctx)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Exec(ctx, "CREATE TABLE t (a INT)"); err != nil {
		b.Fatal(err)
	}
	b.Run("raw", func(b *testing.B) {
		for b.Loop() {
			if _, err := d.SQL.ExecContext(ctx, "INSERT INTO t VALUES (1)"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("autocommit", func(b *testing.B) {
		for b.Loop() {
			if _, err := s.Exec(ctx, "INSERT INTO t VALUES (1)"); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("transaction", func(b *testing.B) {
		if err := s.Begin(ctx); err != nil {
			b.Fatal(err)
		}
		for b.Loop() {
			if _, err := s.Exec(ctx, "INSERT INTO t VALUES (1)"); err != nil {
				b.Fatal(err)
			}
		}
		if err := s.Rollback(ctx); err != nil {
			b.Fatal(err)
		}
	})
}

// The fallback when the driver cannot tell: what the statements' verbs
// imply, comments skipped.
func TestLexerFallbackVerbs(t *testing.T) {
	deadlock := &mysql.MySQLError{Number: 1213, Message: "Deadlock found"}
	lockWait := &mysql.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded"}
	duplicate := &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}
	exists := &mysql.MySQLError{Number: 1050, Message: "Table already exists"}
	duckConstraint := &duckdb.Error{Type: duckdb.ErrorTypeConstraint, Msg: "Constraint Error: duplicate key"}
	duckParser := &duckdb.Error{Type: duckdb.ErrorTypeParser, Msg: "Parser Error: syntax error"}
	none, open, off := txByVerbs{}, txByVerbs{tx: TxOpen}, txByVerbs{autocommitOff: true}
	offOpen := txByVerbs{tx: TxOpen, autocommitOff: true}
	for _, c := range []struct {
		e          Engine
		before     txByVerbs
		sql        string
		err        error
		rollsBack  bool // innodb_rollback_on_timeout
		want       TxState
		wantAutoff bool
	}{
		{e: MySQL, before: none, sql: "/* why */ START TRANSACTION", want: TxOpen},
		{e: MySQL, before: none, sql: "-- why\nBEGIN", want: TxOpen},
		{e: MySQL, before: none, sql: "# why\nbegin work", want: TxOpen},
		{e: MySQL, before: none, sql: "BEGIN NOT ATOMIC SELECT 1; END", want: TxNone},
		{e: MySQL, before: none, sql: "BEGIN", err: duplicate, want: TxNone},
		{e: MySQL, before: open, sql: "COMMIT", want: TxNone},
		{e: MySQL, before: open, sql: "COMMIT AND CHAIN", want: TxOpen},
		{e: MySQL, before: open, sql: "commit work and no chain release", want: TxNone},
		{e: MySQL, before: open, sql: "ROLLBACK AND CHAIN", want: TxOpen},
		{e: MySQL, before: open, sql: "ROLLBACK TO SAVEPOINT s", want: TxOpen},
		{e: MySQL, before: open, sql: "ROLLBACK WORK TO s", want: TxOpen},
		{e: MySQL, before: open, sql: "/* c */ ROLLBACK", want: TxNone},
		{e: MySQL, before: open, sql: "SAVEPOINT s", want: TxOpen},
		{e: MySQL, before: none, sql: "SAVEPOINT s", want: TxNone},
		{e: MySQL, before: open, sql: "RELEASE SAVEPOINT s", want: TxOpen},
		{e: MySQL, before: none, sql: "XA START 'x'", want: TxOpen},
		{e: MySQL, before: none, sql: "XA BEGIN 'x'", want: TxOpen},
		{e: MySQL, before: open, sql: "XA END 'x'", want: TxOpen},
		{e: MySQL, before: open, sql: "XA PREPARE 'x'", want: TxNone},
		{e: MySQL, before: open, sql: "XA ROLLBACK 'x'", want: TxNone},
		{e: MySQL, before: none, sql: "XA COMMIT 'x'", want: TxNone},
		// Autocommit off begins no transaction itself; then, in doubt, any
		// statement but one ending a transaction begins one.
		{e: MySQL, before: none, sql: "SET autocommit = 0", want: TxNone, wantAutoff: true},
		{e: MySQL, before: none, sql: "SET @@session.autocommit := OFF", want: TxNone, wantAutoff: true},
		{e: MySQL, before: none, sql: "SET sql_mode = '', autocommit = 0", want: TxNone, wantAutoff: true},
		{e: MySQL, before: none, sql: "SET GLOBAL autocommit = 0", want: TxNone},
		{e: MySQL, before: none, sql: "SET @@global.autocommit = 0", want: TxNone},
		{e: MySQL, before: off, sql: "SET autocommit = 0", want: TxNone, wantAutoff: true},
		{e: MySQL, before: offOpen, sql: "SET autocommit = 0", want: TxOpen, wantAutoff: true},
		{e: MySQL, before: off, sql: "INSERT INTO t VALUES (1)", want: TxOpen, wantAutoff: true},
		{e: MySQL, before: off, sql: "SELECT 1", want: TxOpen, wantAutoff: true},
		{e: MySQL, before: off, sql: "INSERT INTO t VALUES (1)", err: duplicate, want: TxOpen, wantAutoff: true},
		{e: MySQL, before: off, sql: "COMMIT", want: TxNone, wantAutoff: true},
		{e: MySQL, before: offOpen, sql: "COMMIT", want: TxNone, wantAutoff: true},
		{e: MySQL, before: offOpen, sql: "ROLLBACK", want: TxNone, wantAutoff: true},
		{e: MySQL, before: offOpen, sql: "ROLLBACK TO SAVEPOINT s", want: TxOpen, wantAutoff: true},
		{e: MySQL, before: off, sql: "XA COMMIT 'x'", want: TxNone, wantAutoff: true},
		{e: MySQL, before: off, sql: "START TRANSACTION", want: TxOpen, wantAutoff: true},
		{e: MySQL, before: off, sql: "SET autocommit = 1", want: TxNone},
		{e: MySQL, before: offOpen, sql: "SET autocommit = ON", want: TxNone},
		{e: MySQL, before: open, sql: "SET autocommit = 1", want: TxOpen}, // already 1: no commit
		{e: MySQL, before: open, sql: "CREATE TABLE t (a INT)", want: TxNone},
		{e: MySQL, before: open, sql: "create temporary table t (a int)", want: TxOpen},
		{e: MySQL, before: open, sql: "DROP TEMPORARY TABLE t", want: TxOpen},
		{e: MySQL, before: open, sql: "ALTER TABLE t ADD b INT", want: TxNone},
		{e: MySQL, before: open, sql: "TRUNCATE t", want: TxNone},
		{e: MySQL, before: open, sql: "LOCK TABLES t WRITE", want: TxNone},
		{e: MySQL, before: open, sql: "GRANT SELECT ON *.* TO u", want: TxNone},
		{e: MySQL, before: open, sql: "/*!50001 CREATE ALGORITHM=UNDEFINED */ /*!50001 VIEW v AS SELECT 1 */", want: TxNone},
		{e: MySQL, before: open, sql: "START TRANSACTION", want: TxOpen},
		{e: MySQL, before: off, sql: "CREATE TABLE t (a INT)", want: TxNone, wantAutoff: true},
		{e: MySQL, before: offOpen, sql: "CREATE TABLE t (a INT)", want: TxNone, wantAutoff: true},
		{e: MySQL, before: none, sql: "START REPLICA", want: TxNone},
		{e: MySQL, before: none, sql: "START SLAVE", want: TxNone},
		{e: MySQL, before: none, sql: "START GROUP_REPLICATION", want: TxNone},
		{e: MySQL, before: open, sql: "UPDATE t SET a = 1", err: deadlock, want: TxNone},
		{e: MySQL, before: off, sql: "UPDATE t SET a = 1", err: deadlock, want: TxNone, wantAutoff: true},
		{e: MySQL, before: offOpen, sql: "UPDATE t SET a = 1", err: deadlock, want: TxNone, wantAutoff: true},
		{e: MySQL, before: open, sql: "UPDATE t SET a = 1", err: lockWait, rollsBack: true, want: TxNone},
		{e: MySQL, before: open, sql: "UPDATE t SET a = 1", err: lockWait, want: TxOpen},
		{e: MySQL, before: open, sql: "INSERT INTO t VALUES (1)", err: duplicate, want: TxOpen},
		// A failed CREATE TABLE may have committed: open stays, so that
		// nothing is closed without asking.
		{e: MySQL, before: open, sql: "CREATE TABLE t (a INT)", err: exists, want: TxOpen},

		{e: SQLite, before: none, sql: "/* why */ BEGIN IMMEDIATE", want: TxOpen},
		{e: SQLite, before: none, sql: "SAVEPOINT a", want: TxOpen},
		{e: SQLite, before: open, sql: "ROLLBACK TRANSACTION TO SAVEPOINT a", want: TxOpen},
		{e: SQLite, before: open, sql: "RELEASE a", want: TxOpen},
		{e: SQLite, before: open, sql: "END TRANSACTION", want: TxNone},
		{e: SQLite, before: open, sql: "ROLLBACK", want: TxNone},
		{e: SQLite, before: open, sql: "CREATE TABLE t (a)", want: TxOpen},

		{e: DuckDB, before: none, sql: "-- why\nBEGIN TRANSACTION", want: TxOpen},
		{e: DuckDB, before: open, sql: "INSERT INTO t VALUES (1)", err: duckConstraint, want: TxFailed},
		{e: DuckDB, before: open, sql: "SELEC 1", err: duckParser, want: TxOpen},
		{e: DuckDB, before: none, sql: "INSERT INTO t VALUES (1)", err: duckConstraint, want: TxNone},
		{e: DuckDB, before: txByVerbs{tx: TxFailed}, sql: "ROLLBACK", want: TxNone},
		{e: DuckDB, before: txByVerbs{tx: TxFailed}, sql: "COMMIT", want: TxNone},
		{e: DuckDB, before: open, sql: "ABORT", want: TxNone},
	} {
		after := c.before.after(c.e, c.sql, c.err, func() bool { return c.rollsBack })
		if after.tx != c.want || after.autocommitOff != c.wantAutoff {
			t.Errorf("%s from %+v: %q (error %v): %v, autocommit off %v; want %v, %v",
				c.e, c.before, c.sql, c.err, after.tx, after.autocommitOff, c.want, c.wantAutoff)
		}
	}
}

func TestCommitsImplicitly(t *testing.T) {
	commits := []string{
		"CREATE TABLE t (a int)",
		"create or replace view v as select 1",
		"CREATE DEFINER=`root`@`%` PROCEDURE p() SELECT 1",
		"CREATE ALGORITHM=MERGE DEFINER = CURRENT_USER SQL SECURITY INVOKER VIEW v AS SELECT 1",
		"CREATE UNIQUE INDEX i ON t (a)",
		"CREATE TABLE t2 SELECT * FROM t",
		"CREATE SPATIAL REFERENCE SYSTEM 4120 NAME 'x' DEFINITION 'y'",
		"CREATE TRIGGER tr BEFORE INSERT ON t FOR EACH ROW SET NEW.a = 1",
		"CREATE EVENT e ON SCHEDULE EVERY 1 DAY DO SELECT 1",
		"CREATE SCHEMA s",
		"CREATE ROLE r",
		"CREATE USER u",
		"ALTER TABLE t ADD c INT",
		"ALTER USER u IDENTIFIED BY 'x'",
		"ALTER DATABASE d CHARACTER SET utf8mb4",
		"DROP TABLE t",
		"DROP DATABASE d",
		"DROP USER u",
		"RENAME TABLE a TO b",
		"TRUNCATE TABLE t",
		"TRUNCATE t",
		"GRANT ALL ON *.* TO u",
		"REVOKE ALL ON *.* FROM u",
		"SET PASSWORD = 'x'",
		"SET autocommit = 1",
		"BEGIN",
		"START TRANSACTION",
		"LOCK TABLES t READ",
		"UNLOCK TABLES",
		"ANALYZE TABLE t",
		"OPTIMIZE NO_WRITE_TO_BINLOG TABLE t",
		"CHECK TABLE t",
		"REPAIR TABLE t",
		"FLUSH TABLES",
		"RESET MASTER",
		"START REPLICA",
		"STOP SLAVE",
		"CHANGE REPLICATION SOURCE TO SOURCE_HOST='h'",
		"CHANGE MASTER TO MASTER_HOST='h'",
		"INSTALL PLUGIN p SONAME 'x.so'",
		"UNINSTALL PLUGIN p",
		"CACHE INDEX t IN c",
		"LOAD INDEX INTO CACHE t",
		"/* note */ CREATE TABLE t (a int)",
		"-- note\nDROP VIEW v",
		"/*!50001 CREATE ALGORITHM=UNDEFINED */ /*!50013 DEFINER=`root`@`%` SQL SECURITY DEFINER */ /*!50001 VIEW v AS SELECT 1 */",
	}
	for _, q := range commits {
		if !CommitsImplicitly(MySQL, q) {
			t.Errorf("MySQL %q: no implicit commit", q)
		}
	}
	keeps := []string{
		"CREATE TEMPORARY TABLE t (a int)",
		"CREATE TEMPORARY TABLE t SELECT 1",
		"DROP TEMPORARY TABLE t",
		"INSERT INTO t VALUES (1)",
		"UPDATE t SET a = 1",
		"SELECT 1",
		"CALL p()",
		"SET autocommit = 0",
		"SET sql_mode = ''",
		"RESET PERSIST",
		"START GROUP_REPLICATION",
		"BEGIN NOT ATOMIC SELECT 1; END",
		"LOAD DATA INFILE 'x' INTO TABLE t",
		"LOCK INSTANCE FOR BACKUP",
		"COMMIT",
		"ROLLBACK",
		"SAVEPOINT s",
		"XA START 'x'",
		"-- CREATE TABLE t\nSELECT 1",
		"/* DROP TABLE t */ SELECT 1",
	}
	for _, q := range keeps {
		if CommitsImplicitly(MySQL, q) {
			t.Errorf("MySQL %q: an implicit commit", q)
		}
	}
	// Rules that scan on read past the leading words.
	assignments := strings.Repeat("@a = 1, ", leadingWordCount)
	if !CommitsImplicitly(MySQL, "SET "+assignments+"autocommit = 1") || CommitsImplicitly(MySQL, "SET "+assignments+"autocommit = 0") {
		t.Errorf("MySQL: autocommit set after %d words", leadingWordCount)
	}
	if !CommitsImplicitly(MySQL, "CREATE "+strings.Repeat("OR REPLACE ", leadingWordCount)+"VIEW v AS SELECT 1") {
		t.Errorf("MySQL: a view named after %d modifiers", leadingWordCount)
	}
	if v := (txByVerbs{}).after(MySQL, "SET "+assignments+"autocommit = 0", nil, nil); !v.autocommitOff {
		t.Errorf("MySQL: autocommit off after %d words not followed", leadingWordCount)
	}
	for _, e := range []Engine{Postgres, SQLite, DuckDB, ClickHouse} {
		if CommitsImplicitly(e, "CREATE TABLE t (a int)") || CommitsImplicitly(e, "BEGIN") {
			t.Errorf("%s: an implicit commit", e)
		}
	}
}

// A large INSERT, as a dump holds, is read only as far as its first words.
func BenchmarkCommitsImplicitlyLargeInsert(b *testing.B) {
	var sb strings.Builder
	sb.WriteString("INSERT INTO `t` VALUES ")
	for i := 0; sb.Len() < 600<<10; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(1,'some text, with a quote \\' in it',3.5,NULL)")
	}
	insert := sb.String()
	b.SetBytes(int64(len(insert)))
	for b.Loop() {
		if CommitsImplicitly(MySQL, insert) {
			b.Fatal("an INSERT commits")
		}
	}
}
