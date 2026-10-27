package db

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2/lib/proto"
	"github.com/duckdb/duckdb-go/v2"
	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	mssql "github.com/microsoft/go-mssqldb"
	"github.com/sijms/go-ora/v3/network"
	"modernc.org/sqlite"
)

// ErrorInfo is what a database said about a failed statement.
type ErrorInfo struct {
	Message string
	// Code is the SQLSTATE, or the engine's error number or name.
	Code   string
	Detail string
	Hint   string
	// Position is the 1-based character of the statement where the error
	// is, 0 when unknown; Line and Column, 1-based, when the engine gives
	// those instead.
	Position     int
	Line, Column int
	// Bytes says Position and Column count bytes rather than characters,
	// as ClickHouse's do.
	Bytes bool
	// Near is the text the engine quoted as where it stopped, which finds
	// the column when it gives only a line.
	Near string
}

var (
	mysqlLine = regexp.MustCompile(`near '((?s).*)' at line (\d+)\s*$`)
	chLineCol = regexp.MustCompile(`\(line (\d+), col (\d+)\)`)
	chPos     = regexp.MustCompile(`failed at position (\d+)`)
	duckLine  = regexp.MustCompile(`(?m)^LINE (\d+):`)
	duckNear  = regexp.MustCompile(`at or near "([^"]*)"`)
	mssqlNear = regexp.MustCompile(`(?i)near '((?s).*)'\.?$`)
)

// DescribeError reads the details each driver's error carries.
func DescribeError(err error) ErrorInfo {
	info := ErrorInfo{Message: err.Error()}
	var pg *pgconn.PgError
	var my *mysql.MySQLError
	var ch *proto.Exception
	var duck *duckdb.Error
	var lite *sqlite.Error
	var ms mssql.Error
	var ora *network.OracleError
	switch {
	case errors.As(err, &pg):
		info.Message, info.Code, info.Detail, info.Hint = pg.Message, pg.Code, pg.Detail, pg.Hint
		info.Position = int(pg.Position)
		if info.Position == 0 {
			info.Line, info.Column, info.Bytes = caretOf(pg.Detail)
		}
	case errors.As(err, &my):
		info.Message = my.Message
		info.Code = strconv.Itoa(int(my.Number))
		if state := string(my.SQLState[:]); strings.Trim(state, "\x00") != "" {
			info.Code += " (" + state + ")"
		}
		if m := mysqlLine.FindStringSubmatch(my.Message); m != nil {
			info.Near = m[1]
			info.Line, _ = strconv.Atoi(m[2])
		}
	case errors.As(err, &ch):
		info.Message = strings.TrimSpace(ch.Message)
		info.Code = strconv.Itoa(int(ch.Code))
		if ch.Name != "" && ch.Name != "DB::Exception" {
			info.Code += " " + ch.Name
		}
		info.Bytes = true
		if m := chPos.FindStringSubmatch(ch.Message); m != nil {
			info.Position, _ = strconv.Atoi(m[1])
		}
		if m := chLineCol.FindStringSubmatch(ch.Message); m != nil {
			info.Line, _ = strconv.Atoi(m[1])
			info.Column, _ = strconv.Atoi(m[2])
		}
	case errors.As(err, &duck):
		info.Message = duck.Msg
		if m := duckNear.FindStringSubmatch(duck.Msg); m != nil {
			info.Near = m[1]
		}
		if m := duckLine.FindStringSubmatch(duck.Msg); m != nil {
			info.Line, _ = strconv.Atoi(m[1])
			info.Message = strings.TrimSpace(duck.Msg[:strings.Index(duck.Msg, m[0])])
			info.Detail = strings.TrimSpace(duck.Msg[strings.Index(duck.Msg, m[0]):])
		}
	case errors.As(err, &lite):
		info.Code = fmt.Sprint(lite.Code())
	case errors.As(err, &ora):
		// "ORA-00942: table or view … does not exist", which go-ora ends
		// with where the statement went wrong, 0-based.
		info.Code = fmt.Sprintf("ORA-%05d", ora.ErrCode)
		info.Message = strings.TrimSpace(strings.TrimPrefix(ora.ErrMsg, info.Code+":"))
		if at := ora.ErrPos(); at > 0 {
			info.Position, info.Bytes = at+1, true
		}
	case errors.As(err, &ms):
		// The last error of a statement is the one described; the others
		// come before it, as SQL Server sends them.
		info.Message, info.Code = ms.Message, strconv.Itoa(int(ms.Number))
		info.Line = int(ms.LineNo)
		if ms.ProcName != "" {
			// The line is the routine's, not the batch's.
			info.Line, info.Detail = 0, "In "+ms.ProcName+", line "+strconv.Itoa(int(ms.LineNo))+"."
		}
		var before []string
		for _, e := range ms.All[:max(0, len(ms.All)-1)] {
			before = append(before, e.Message)
		}
		if len(before) > 0 {
			info.Detail = strings.TrimSpace(info.Detail + "\n" + strings.Join(before, "\n"))
		}
		if m := mssqlNear.FindStringSubmatch(ms.Message); m != nil {
			info.Near = m[1]
		}
	}
	if hint := conflictHint(err); hint != "" {
		info.Hint = strings.TrimSpace(hint + "\n" + info.Hint)
	}
	if strings.Contains(info.Message, oracleXMLTypeError) {
		info.Hint = oracleXMLTypeHint
	}
	if ora != nil && ora.ErrCode == 942 && oracleCatalogView.MatchString(info.Message) {
		info.Hint = oracleCatalogHint
	}
	if errors.As(err, new(oracleNoLengthError)) || oracleTTCError(info.Message) {
		info.Hint = oracleNoLengthHint
	}
	return info
}

// caretOf reads where CockroachDB's detail of a syntax error puts its
// caret: under the statement's lines up to the error's, at its byte. 0
// when the detail is none such.
func caretOf(detail string) (line, column int, bytes bool) {
	source, ok := strings.CutPrefix(detail, "source SQL:\n")
	if !ok {
		return 0, 0, false
	}
	lines := strings.Split(source, "\n")
	caret := lines[len(lines)-1]
	if len(lines) < 2 || strings.TrimLeft(caret, " ") != "^" {
		return 0, 0, false
	}
	return len(lines) - 1, len(caret), true
}

// conflictHint is the hint of an error that ends a transaction clashing
// with another, which may succeed run again from its start: TiDB's write
// conflict (9007), a serialization failure (SQLSTATE 40001) of
// PostgreSQL's family, as CockroachDB's retry errors, SQL Server's
// deadlock victim (1205) and snapshot update conflict (3960), and Oracle's
// deadlock (ORA-00060) and serialization failure (ORA-08177). The app
// never runs it again itself. "" for any other error.
func conflictHint(err error) string {
	var pg *pgconn.PgError
	var ms mssql.Error
	var ora *network.OracleError
	switch {
	case errors.As(err, &ora) && ora.ErrCode == 60:
		return "The statement was chosen to break a deadlock and rolled back; the rest of the transaction stays. Roll back and run it again from its start, it may succeed; DGopher never runs it again on its own."
	case errors.As(err, &ora) && ora.ErrCode == 8177:
		return "The serializable transaction clashed with another that changed the same rows. Roll back and run it again from its start, it may succeed; DGopher never runs it again on its own."
	case errors.As(err, &ms) && ms.Number == 1205:
		return "The transaction was chosen as the victim of a deadlock and rolled back. Run it again from its start, it may succeed; DGopher never runs it again on its own."
	case errors.As(err, &ms) && ms.Number == 3960:
		return "The snapshot transaction clashed with another that changed the same rows, and was rolled back. Run it again from its start, it may succeed; DGopher never runs it again on its own."
	case mysqlErrorNumber(err) == 9007:
		return "The transaction clashed with another that changed the same rows. Run again from its start, it may succeed; DGopher never runs it again on its own."
	case errors.As(err, &pg) && pg.Code == "40001":
		return "The transaction could not be serialized with others and was aborted: roll back and run it again from its start."
	}
	return ""
}
