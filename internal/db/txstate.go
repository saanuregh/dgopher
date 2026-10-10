package db

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"unsafe"

	"dgopher/internal/sqltext"

	"github.com/duckdb/duckdb-go/v2"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/stdlib"
	sqlite3 "modernc.org/sqlite/lib"
)

// readTxState reads whether a connection is inside a transaction from what
// its driver or engine keeps, however the statements that began or ended
// it looked. ok is false when it cannot tell, as for ClickHouse, or after
// a driver upgrade renamed what it reads, which TestTxStateFromDriver
// catches; the session then follows the statements' verbs (txByVerbs).
func readTxState(ctx context.Context, conn *sql.Conn) (state TxState, ok bool) {
	return readTxStateWithLastID(ctx, conn, nil)
}

// readTxStateWithLastID is readTxState given, in lastTxID, the DuckDB
// transaction id read on the connection after its previous statement,
// which it updates: DuckDB then takes one read, not two, while a
// transaction stays open (duckTxState).
func readTxStateWithLastID(ctx context.Context, conn *sql.Conn, lastTxID *int64) (state TxState, ok bool) {
	duck := false
	err := conn.Raw(func(dc any) error {
		switch c := dc.(type) {
		case *stdlib.Conn:
			state, ok = postgresTxState(c), true
		case *duckdb.Conn:
			duck = true
		default:
			state, ok = reflectedTxState(dc)
		}
		return nil
	})
	if err != nil {
		return TxNone, false
	}
	if duck {
		return duckTxState(ctx, conn, lastTxID)
	}
	return state, ok
}

// postgresTxState is what the server reported after the last statement.
func postgresTxState(c *stdlib.Conn) TxState {
	switch c.Conn().PgConn().TxStatus() {
	case 'T':
		return TxOpen
	case 'E':
		return TxFailed
	}
	return TxNone
}

// The packages whose unexported fields reflectedTxState reads, as of
// go-sql-driver/mysql v1.10.1 (mysqlConn.status) and modernc.org/sqlite
// v1.60.1 (conn.db and conn.tls).
const (
	mysqlPackage  = "github.com/go-sql-driver/mysql"
	sqlitePackage = "modernc.org/sqlite"
)

// reflectedTxState reads the transaction of a MySQL or SQLite driver
// connection, which neither driver exports.
func reflectedTxState(dc any) (TxState, bool) {
	c, ok := driverStruct(dc)
	if !ok {
		return TxNone, false
	}
	switch t := c.Type(); {
	case t.PkgPath() == mysqlPackage && t.Name() == "mysqlConn":
		return mysqlTxState(c)
	case t.PkgPath() == sqlitePackage && t.Name() == "conn":
		return sqliteTxState(c)
	}
	return TxNone, false
}

// driverStruct is the struct a driver connection points to.
func driverStruct(dc any) (reflect.Value, bool) {
	v := reflect.ValueOf(dc)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	return v.Elem(), true
}

// mysqlConnValue is the struct of a MySQL driver connection; ok is false
// for any other driver's.
func mysqlConnValue(dc any) (reflect.Value, bool) {
	c, ok := driverStruct(dc)
	if !ok || c.Type().PkgPath() != mysqlPackage || c.Type().Name() != "mysqlConn" {
		return reflect.Value{}, false
	}
	return c, true
}

// mysqlTxState reads SERVER_STATUS_IN_TRANS from the server status flags
// the driver keeps from the last OK or EOF packet. Under autocommit off
// it is clear until a statement reads or writes a table, and again after
// COMMIT or ROLLBACK: autocommit off alone holds nothing open
// (Session.AutocommitOff). An error packet leaves the flags as they were,
// which the session refreshes with a ping (afterError).
func mysqlTxState(c reflect.Value) (TxState, bool) {
	flags, ok := mysqlStatus(c)
	if !ok {
		return TxNone, false
	}
	if flags&mysqlInTrans != 0 {
		return TxOpen, true
	}
	return TxNone, true
}

// The server status flags of MySQL's that the driver keeps.
const (
	mysqlInTrans    = 1 // SERVER_STATUS_IN_TRANS
	mysqlAutocommit = 2 // SERVER_STATUS_AUTOCOMMIT
)

// mysqlStatus reads the server status flags of c, a MySQL driver
// connection.
func mysqlStatus(c reflect.Value) (uint64, bool) {
	status := c.FieldByName("status")
	if status.Kind() != reflect.Uint16 {
		return 0, false
	}
	return status.Uint(), true
}

// readMySQLAutocommitOff reads whether SERVER_STATUS_AUTOCOMMIT is clear
// on a MySQL connection, however autocommit was turned off. ok is false
// when the driver cannot tell.
func readMySQLAutocommitOff(conn *sql.Conn) (off, ok bool) {
	conn.Raw(func(dc any) error {
		c, isMySQL := mysqlConnValue(dc)
		if !isMySQL {
			return nil
		}
		var flags uint64
		flags, ok = mysqlStatus(c)
		off = ok && flags&mysqlAutocommit == 0
		return nil
	})
	return off, ok
}

// sqliteTxState calls sqlite3_get_autocommit, false inside a transaction
// however it began, on the connection's handle and thread state.
func sqliteTxState(c reflect.Value) (TxState, bool) {
	getAutocommit := reflect.ValueOf(sqlite3.Xsqlite3_get_autocommit)
	handle, tls := c.FieldByName("db"), c.FieldByName("tls")
	if handle.Kind() != reflect.Uintptr || handle.Uint() == 0 ||
		!tls.IsValid() || tls.Type() != getAutocommit.Type().In(0) || tls.IsNil() {
		return TxNone, false
	}
	// A value read from an unexported field cannot be passed to a call.
	tls = reflect.NewAt(tls.Type(), unsafe.Pointer(tls.UnsafeAddr())).Elem()
	autocommit := getAutocommit.Call([]reflect.Value{tls, reflect.ValueOf(uintptr(handle.Uint()))})[0]
	if autocommit.Int() == 0 {
		return TxOpen, true
	}
	return TxNone, true
}

// duckTxState compares the transaction id in two statements, as DuckDB
// keeps no flag to read: inside a transaction both get its id, outside
// each its own. After an error aborted the transaction, the first fails.
// An id never repeats, so a first one equal to *lastTxID, read after the
// connection's previous statement (0 for none), is a transaction still
// open, and the second read is skipped. current_transaction_id() is the
// database instance's id; txid_current()'s is the default catalog's, whose
// numbers another catalog's repeat after a USE.
func duckTxState(ctx context.Context, conn *sql.Conn, lastTxID *int64) (TxState, bool) {
	const readID = "SELECT current_transaction_id()"
	var first, second int64
	if err := conn.QueryRowContext(ctx, readID).Scan(&first); err != nil {
		if strings.Contains(err.Error(), "Current transaction is aborted") {
			return TxFailed, true
		}
		return TxNone, false
	}
	if lastTxID != nil && *lastTxID != 0 && first == *lastTxID {
		return TxOpen, true
	}
	if err := conn.QueryRowContext(ctx, readID).Scan(&second); err != nil {
		return TxNone, false
	}
	if lastTxID != nil {
		*lastTxID = second
	}
	if first == second {
		return TxOpen, true
	}
	return TxNone, true
}

// txByVerbs follows a transaction of MySQL, SQLite or DuckDB from the
// statements that ran: the fallback when the driver cannot tell
// (readTxState). In doubt it keeps the transaction open: closing one
// silently loses its work, while showing one open asks once more.
type txByVerbs struct {
	tx TxState
	// autocommitOff is MySQL's SET autocommit = 0, under which a statement
	// reading or writing a table begins a transaction.
	autocommitOff bool
}

// after returns what follows from query, which ended with err.
// rollsBackOnTimeout reports MySQL's innodb_rollback_on_timeout, asked
// only after a lock wait timeout. Under autocommit off, which statements
// read or write a table is not known here: in doubt, any statement, even a
// failed one, begins a transaction but one that ends a transaction or
// sets autocommit.
func (v txByVerbs) after(e Engine, query string, err error, rollsBackOnTimeout func() bool) txByVerbs {
	beginsAny := e == MySQL && v.autocommitOff
	if err != nil {
		switch {
		case e == MySQL:
			// The server rolls back the whole transaction of a deadlock's
			// victim, and of a lock wait timeout when told to.
			if n := mysqlErrorNumber(err); n == 1213 || n == 1205 && rollsBackOnTimeout != nil && rollsBackOnTimeout() {
				v.tx = TxNone
			} else if beginsAny {
				v.tx = TxOpen
			}
		case e == DuckDB && v.tx == TxOpen:
			// Any error but a parse error aborts DuckDB's transaction.
			var de *duckdb.Error
			if !errors.As(err, &de) || de.Type != duckdb.ErrorTypeParser {
				v.tx = TxFailed
			}
		}
		// Otherwise what a failed statement did is unsure: a failed CREATE
		// TABLE may have committed, and the transaction stays shown.
		return v
	}
	words := leadingWords(e, query)
	beginsNone := false
	if e == MySQL && wordAt(words, 0) != "SET" && commitsImplicitly(e, query, words) {
		v.tx, beginsNone = TxNone, true
	}
	switch wordAt(words, 0) {
	case "BEGIN":
		// MariaDB's BEGIN NOT ATOMIC … END is a compound statement.
		if e != MySQL || wordAt(words, 1) != "NOT" {
			v.tx = TxOpen
		}
	case "START":
		// Not START REPLICA, SLAVE or GROUP_REPLICATION.
		if wordAt(words, 1) == "TRANSACTION" {
			v.tx = TxOpen
		}
	case "COMMIT", "END", "ROLLBACK", "ABORT":
		switch ends, chains := endsTransaction(words); {
		case chains:
			v.tx = TxOpen // the next begins at once
		case ends:
			v.tx, beginsNone = TxNone, true
		}
	case "SAVEPOINT":
		// SQLite's begins a transaction outside one; MySQL's does not.
		if e == SQLite && v.tx == TxNone {
			v.tx = TxOpen
		}
	case "XA":
		switch wordAt(words, 1) {
		case "START", "BEGIN":
			v.tx = TxOpen
		case "PREPARE", "COMMIT", "ROLLBACK":
			// A prepared XA transaction leaves the session.
			v.tx, beginsNone = TxNone, true
		}
	case "SET":
		if on, ok := setsAutocommit(allWords(e, query, words)); e == MySQL && ok {
			if on && v.autocommitOff {
				v.tx = TxNone // committed: autocommit was not already 1
			}
			v.autocommitOff, beginsNone = !on, true
		}
	}
	if beginsAny && !beginsNone && v.tx == TxNone {
		v.tx = TxOpen
	}
	return v
}

// CommitsImplicitly reports whether a statement commits the open
// transaction, as MySQL's DDL, account, locking, administrative and
// replication statements do
// (https://dev.mysql.com/doc/refman/8.4/en/implicit-commit.html), except
// CREATE and DROP of a TEMPORARY table. SET autocommit = 1 counts, though
// it commits only when autocommit was 0. It is false for other engines.
func CommitsImplicitly(e Engine, sql string) bool {
	if e != MySQL {
		return false
	}
	return commitsImplicitly(e, sql, leadingWords(e, sql))
}

// SetsAutocommit reads a MySQL SET of the session's autocommit: on, and
// whether the statement assigns it at all; false on other engines.
func SetsAutocommit(e Engine, sql string) (on, ok bool) {
	if e != MySQL {
		return false, false
	}
	words := leadingWords(e, sql)
	if wordAt(words, 0) != "SET" {
		return false, false
	}
	return setsAutocommit(allWords(e, sql, words))
}

// commitsImplicitly is CommitsImplicitly given the statement's leading
// words, read on to its end only by the rules that scan that far.
func commitsImplicitly(e Engine, sql string, words []string) bool {
	switch wordAt(words, 0) {
	case "CREATE", "ALTER", "DROP":
		object, temporary := ddlObject(words)
		if object == "" {
			// The modifiers may run past the words read.
			object, temporary = ddlObject(allWords(e, sql, words))
		}
		return implicitObjects[object] && !(temporary && object == "TABLE")
	case "RENAME", "TRUNCATE", "GRANT", "REVOKE", "FLUSH", "ANALYZE", "CHECK", "OPTIMIZE", "REPAIR":
		return true
	case "BEGIN":
		return wordAt(words, 1) != "NOT"
	case "START":
		return wordAt(words, 1) == "TRANSACTION" || wordAt(words, 1) == "REPLICA" || wordAt(words, 1) == "SLAVE"
	case "STOP":
		return wordAt(words, 1) == "REPLICA" || wordAt(words, 1) == "SLAVE"
	case "LOCK", "UNLOCK":
		return wordAt(words, 1) == "TABLES" || wordAt(words, 1) == "TABLE"
	case "INSTALL", "UNINSTALL":
		return wordAt(words, 1) == "PLUGIN"
	case "CACHE":
		return wordAt(words, 1) == "INDEX"
	case "LOAD":
		return wordAt(words, 1) == "INDEX"
	case "RESET":
		return wordAt(words, 1) != "PERSIST"
	case "CHANGE":
		return wordAt(words, 1) == "MASTER" || wordAt(words, 1) == "REPLICATION" && wordAt(words, 2) == "SOURCE"
	case "SET":
		on, ok := setsAutocommit(allWords(e, sql, words))
		return wordAt(words, 1) == "PASSWORD" || ok && on
	}
	return false
}

// implicitObjects are the objects whose CREATE, ALTER or DROP commits in
// MySQL; REFERENCE is SPATIAL REFERENCE SYSTEM's.
var implicitObjects = map[string]bool{
	"DATABASE": true, "SCHEMA": true, "EVENT": true, "FUNCTION": true, "INDEX": true, "PROCEDURE": true,
	"REFERENCE": true, "ROLE": true, "SERVER": true, "TABLE": true, "TABLESPACE": true, "TRIGGER": true,
	"USER": true, "VIEW": true,
}

// ddlModifiers are the words MySQL allows between CREATE, ALTER or DROP
// and the kind of object.
var ddlModifiers = map[string]bool{
	"OR": true, "REPLACE": true, "ALGORITHM": true, "=": true, "UNDEFINED": true, "MERGE": true,
	"TEMPTABLE": true, "DEFINER": true, "SQL": true, "SECURITY": true, "INVOKER": true, "UNIQUE": true,
	"FULLTEXT": true, "SPATIAL": true, "AGGREGATE": true, "UNDO": true, "TEMPORARY": true,
	"ONLINE": true, "OFFLINE": true, "IGNORE": true,
}

// ddlObject returns the kind of object a CREATE, ALTER or DROP names, its
// modifiers skipped, and whether TEMPORARY was among them.
func ddlObject(words []string) (object string, temporary bool) {
	for i := 1; i < len(words); i++ {
		switch w := words[i]; {
		case w == "TEMPORARY":
			temporary = true
		case w == "DEFINER" && i+1 < len(words) && words[i+1] == "=":
			// DEFINER = 'user'@'host': the account's tokens, up to the next
			// clause or the object.
			for i++; i+1 < len(words) && !implicitObjects[words[i+1]] && words[i+1] != "SQL"; i++ {
			}
		case !ddlModifiers[w]:
			return w, temporary
		}
	}
	return "", temporary
}

// setsAutocommit reads a MySQL SET's assignment of the session's
// autocommit: on, and whether it assigns it at all. A value other than 1,
// ON or TRUE counts as off, which keeps a transaction shown open.
func setsAutocommit(words []string) (on, ok bool) {
	global := false
	for i := 1; i < len(words); i++ {
		w := strings.ToUpper(words[i])
		switch w {
		case "GLOBAL", "PERSIST", "PERSIST_ONLY":
			global = true // until another scope: MySQL carries it to the next assignments
			continue
		case "SESSION", "LOCAL":
			global = false
			continue
		}
		if strings.HasPrefix(w, "@") && !strings.HasPrefix(w, "@@") {
			continue // a user variable
		}
		scope, name, scoped := strings.Cut(strings.TrimLeft(w, "@"), ".")
		if !scoped {
			scope, name = "", scope
		}
		if name != "AUTOCOMMIT" || scope == "GLOBAL" || scope == "PERSIST" || scope == "PERSIST_ONLY" || scope == "" && global {
			continue
		}
		j := i + 1
		for j < len(words) && (words[j] == "=" || words[j] == ":" || words[j] == ":=") {
			j++
		}
		value := ""
		if j < len(words) {
			value = strings.ToUpper(strings.Trim(words[j], `'"`))
		}
		on, ok = value == "1" || value == "ON" || value == "TRUE", true
	}
	return on, ok
}

// EndsTransaction reports whether a statement ends the open transaction
// as asked to, COMMIT, END, ROLLBACK or ABORT but not ROLLBACK TO a
// savepoint, after which the transaction goes on; and whether it chains,
// AND CHAIN beginning the next transaction at once.
func EndsTransaction(e Engine, sql string) (ends, chains bool) {
	return endsTransaction(leadingWords(e, sql))
}

func endsTransaction(words []string) (ends, chains bool) {
	switch wordAt(words, 0) {
	case "COMMIT", "END", "ROLLBACK", "ABORT":
	default:
		return false, false
	}
	rest := words[1:]
	if len(rest) > 0 && (rest[0] == "WORK" || rest[0] == "TRANSACTION") {
		rest = rest[1:]
	}
	switch {
	case len(rest) > 0 && rest[0] == "TO":
		return false, false
	case len(rest) > 1 && rest[0] == "AND" && rest[1] == "CHAIN":
		return true, true
	}
	return true, false
}

// wordAt is words[i], "" past the end.
func wordAt(words []string, i int) string {
	if i < len(words) {
		return words[i]
	}
	return ""
}

// leadingWordCount is how many words of a statement leadingWords reads:
// more than any rule here reads from fixed places, so that a large
// statement, as an INSERT of a dump, is not read to its end. A rule that
// scans on, as through SET's assignments, reads them all (allWords).
const leadingWordCount = 32

// leadingWords is statementWords up to its first leadingWordCount words.
func leadingWords(e Engine, query string) []string {
	return statementWords(e, query, leadingWordCount)
}

// allWords is every word of a statement whose leading words are words.
func allWords(e Engine, query string, words []string) []string {
	if len(words) < leadingWordCount {
		return words
	}
	return statementWords(e, query, 0)
}

// statementWords returns the text of a statement's tokens without its
// comments and whitespace, words in upper case, up to limit of them when
// limit is above 0. MySQL runs what an executable comment holds (/*! … */,
// MariaDB's /*M! … */), as mysqldump writes CREATE VIEW: its words count.
func statementWords(e Engine, query string, limit int) []string {
	if e == MySQL {
		query = sqltext.UnwrapExecutable(query)
	}
	var words []string
	for _, t := range sqltext.TokenizeFirst(query, LexDialect(e), limit) {
		switch t.Kind {
		case sqltext.Whitespace, sqltext.Comment:
		case sqltext.Keyword, sqltext.Identifier:
			words = append(words, strings.ToUpper(t.Text))
		default:
			words = append(words, t.Text)
		}
	}
	return words
}

// LexDialect is the lexer's dialect of an engine.
func LexDialect(e Engine) sqltext.Dialect {
	switch e {
	case Postgres, DuckDB:
		return sqltext.Postgres
	case MySQL:
		return sqltext.MySQL
	case ClickHouse:
		return sqltext.ClickHouse
	case SQLite:
		return sqltext.SQLite
	}
	return sqltext.Generic
}

// mysqlErrorNumber is the number of a MySQL server error, 0 for any other
// error.
func mysqlErrorNumber(err error) uint16 {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number
	}
	return 0
}
