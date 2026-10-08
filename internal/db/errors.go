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
)

// DescribeError reads the details each driver's error carries.
func DescribeError(err error) ErrorInfo {
	info := ErrorInfo{Message: err.Error()}
	var pg *pgconn.PgError
	var my *mysql.MySQLError
	var ch *proto.Exception
	var duck *duckdb.Error
	var lite *sqlite.Error
	switch {
	case errors.As(err, &pg):
		info.Message, info.Code, info.Detail, info.Hint = pg.Message, pg.Code, pg.Detail, pg.Hint
		info.Position = int(pg.Position)
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
	}
	return info
}
