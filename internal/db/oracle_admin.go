package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Oracle's administration: its users, roles and privileges in the
// pluggable database connected to, the activity of its sessions, and
// the commands that keep a table in shape. Most of it is read from
// Oracle's DBA_ and V$ views, which need SELECT_CATALOG_ROLE.

// ManagesAccounts: a user is a schema that may log in; a role holds
// privileges and roles. Both are the pluggable database's own.
func (oracleEngine) ManagesAccounts() bool { return true }

// oracleOwnRoles are the roles Oracle makes that are listed with the
// users' own: CONNECT holds CREATE SESSION, which a user needs to log in.
const oracleOwnRoles = "'CONNECT', 'RESOURCE', 'DB_DEVELOPER_ROLE'"

// oracleAccountsSQL lists the users and roles but Oracle's own, and the
// roles each holds.
const oracleAccountsSQL = `SELECT 'user', u.username, u.account_status, u.authentication_type, u.default_tablespace,
  (SELECT LISTAGG(p.granted_role, CHR(31)) WITHIN GROUP (ORDER BY p.granted_role) FROM dba_role_privs p WHERE p.grantee = u.username)
FROM dba_users u WHERE u.oracle_maintained = 'N'
UNION ALL
SELECT 'role', r.role, r.oracle_maintained, r.authentication_type, '-',
  (SELECT LISTAGG(p.granted_role, CHR(31)) WITHIN GROUP (ORDER BY p.granted_role) FROM dba_role_privs p WHERE p.grantee = r.role)
FROM dba_roles r WHERE r.oracle_maintained = 'N' OR r.role IN (` + oracleOwnRoles + `)
ORDER BY 1 DESC, 2`

// ListAccounts lists the users and roles from DBA_USERS and DBA_ROLES;
// without the privilege to read them, the users ALL_USERS shows, of
// whom nothing more is known.
func (oracleEngine) ListAccounts(ctx context.Context, q Querier) ([]Account, error) {
	accounts, err := scanAll(ctx, q, oracleAccountsSQL, nil, func(scan func(...any) error) (Account, error) {
		var a Account
		var kind, status, auth, tablespace string
		var memberOf sql.NullString
		if err := scan(&kind, &a.Name, &status, &auth, &tablespace, &memberOf); err != nil {
			return a, err
		}
		a.MemberOf = splitList(memberOf.String)
		if kind == "role" {
			a.Role = true
			if status == "Y" {
				a.Attributes = append(a.Attributes, "Oracle's own role")
			}
			if auth != "NONE" {
				a.Attributes = append(a.Attributes, "enabled by "+strings.ToLower(auth))
			}
			return a, nil
		}
		if status != "OPEN" {
			a.Attributes = append(a.Attributes, strings.ToLower(status))
		}
		switch auth {
		case "NONE":
			a.Attributes = append(a.Attributes, "no authentication: a schema no one logs in to")
		case "PASSWORD":
		default:
			a.Attributes = append(a.Attributes, strings.ToLower(auth)+" authentication")
		}
		a.Attributes = append(a.Attributes, "tablespace "+tablespace)
		return a, nil
	})
	if !oracleErrorIs(err, 942) { // ORA-00942: a view the user may not read
		return accounts, err
	}
	return scanAll(ctx, q, `SELECT username FROM all_users WHERE oracle_maintained = 'N' ORDER BY username`, nil,
		func(scan func(...any) error) (Account, error) {
			var a Account
			return a, scan(&a.Name)
		})
}

// oraclePrivilegesSQL lists what a grantee was granted: system
// privileges, roles, privileges on objects and on their columns, quotas
// on tablespaces, and 23ai's privileges on a schema, each as the GRANT
// that gave it and the statement taking it back ("" for none), the
// grantee's name in place of :2.
const oraclePrivilegesSQL = `SELECT 1, privilege, admin_option, '-', '-', '-' FROM dba_sys_privs WHERE grantee = :1
UNION ALL SELECT 2, granted_role, admin_option, '-', '-', '-' FROM dba_role_privs WHERE grantee = :2
UNION ALL SELECT 3, privilege, grantable, owner, table_name, '-' FROM dba_tab_privs WHERE grantee = :3
UNION ALL SELECT 4, privilege, grantable, owner, table_name, column_name FROM dba_col_privs WHERE grantee = :4
UNION ALL SELECT 5, CASE max_bytes WHEN -1 THEN 'UNLIMITED' ELSE TO_CHAR(max_bytes) END, '-', tablespace_name, '-', '-'
  FROM dba_ts_quotas WHERE username = :5
ORDER BY 1, 4, 5, 6, 2`

// AccountPrivileges lists what an account was granted, each with the
// statement taking it back: a privilege on a column is taken back with
// the table's, for every column, which is left to SQL.
func (oracleEngine) AccountPrivileges(ctx context.Context, q Querier, a Account) ([]Privilege, error) {
	d := oracleDialect{}
	name := d.Quote(a.Name)
	privs, err := scanAll(ctx, q, oraclePrivilegesSQL, []any{a.Name, a.Name, a.Name, a.Name, a.Name}, func(scan func(...any) error) (Privilege, error) {
		var kind int
		var what, option, owner, object, column string
		if err := scan(&kind, &what, &option, &owner, &object, &column); err != nil {
			return Privilege{}, err
		}
		with := ""
		if option == "YES" {
			with = " WITH ADMIN OPTION"
			if kind == 3 || kind == 4 {
				with = " WITH GRANT OPTION"
			}
		}
		switch kind {
		case 1:
			return Privilege{Text: "GRANT " + what + with, Revoke: "REVOKE " + what + " FROM " + name}, nil
		case 2:
			return Privilege{Text: "GRANT " + d.Quote(what) + with, Revoke: "REVOKE " + d.Quote(what) + " FROM " + name}, nil
		case 3:
			on := QualifiedName(d, owner, object)
			return Privilege{Text: "GRANT " + what + " ON " + on + with, Revoke: "REVOKE " + what + " ON " + on + " FROM " + name}, nil
		case 4:
			return Privilege{Text: "GRANT " + what + " (" + d.Quote(column) + ") ON " + QualifiedName(d, owner, object) + with}, nil
		}
		return Privilege{Text: "QUOTA " + what + " ON " + owner, Revoke: "ALTER USER " + name + " QUOTA 0 ON " + d.Quote(owner)}, nil
	})
	if err != nil {
		return nil, err
	}
	schema, err := scanAll(ctx, q, `SELECT privilege, schema, admin_option FROM dba_schema_privs WHERE grantee = :1 ORDER BY schema, privilege`,
		[]any{a.Name}, func(scan func(...any) error) (Privilege, error) {
			var what, on, option string
			if err := scan(&what, &on, &option); err != nil {
				return Privilege{}, err
			}
			text := "GRANT " + what + " ON SCHEMA " + d.Quote(on)
			if option == "YES" {
				text += " WITH ADMIN OPTION"
			}
			return Privilege{Text: text, Revoke: "REVOKE " + what + " ON SCHEMA " + d.Quote(on) + " FROM " + name}, nil
		})
	if oracleErrorIs(err, 942) {
		return privs, nil // before 23ai, which has no privileges on a schema
	}
	return append(privs, schema...), err
}

// AccountName writes an account as the server keeps it, its name
// quoted.
func (oracleEngine) AccountName(a Account) string { return oracleDialect{}.Quote(a.Name) }

// oracleUnquotedName is a name Oracle keeps in upper case when written
// unquoted.
var oracleUnquotedName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_$#]*$`)

// oracleNewAccountName writes the name of an account made: a plain name
// in upper case, as Oracle keeps one written unquoted, so that it logs
// in whatever the case typed; another as it is.
func oracleNewAccountName(name string) string {
	if oracleUnquotedName.MatchString(name) {
		name = strings.ToUpper(name)
	}
	return oracleDialect{}.Quote(name)
}

// oraclePassword writes a password as IDENTIFIED BY takes it, quoted,
// which keeps its case: it cannot hold a double quote.
func oraclePassword(password string) (string, error) {
	switch {
	case password == "":
		return "", errors.New("a user needs a password")
	case strings.Contains(password, `"`):
		return "", errors.New(`an Oracle password cannot hold a double quote (")`)
	}
	return `"` + password + `"`, nil
}

// CreateAccountSQL makes a user with a password, or a role. The user
// logs in once granted CREATE SESSION, as the CONNECT role holds.
func (oracleEngine) CreateAccountSQL(n NewAccount) (string, error) {
	name := oracleNewAccountName(n.Name)
	if n.Role {
		return createRoleSQL(name), nil
	}
	password, err := oraclePassword(n.Password)
	if err != nil {
		return "", err
	}
	return "CREATE USER " + name + " IDENTIFIED BY " + password, nil
}

func (oracleEngine) PasswordSQL(a Account, password string) (string, error) {
	if a.Role {
		return "", fmt.Errorf("%s is a role, which has no password", a.Name)
	}
	quoted, err := oraclePassword(password)
	if err != nil {
		return "", err
	}
	return "ALTER USER " + oracleDialect{}.Quote(a.Name) + " IDENTIFIED BY " + quoted, nil
}

// DropAccountSQL drops a role, or a user that owns nothing: Oracle
// refuses to drop one that owns objects but with CASCADE, which drops
// them too, and which the app leaves to SQL.
func (oracleEngine) DropAccountSQL(a Account) []string {
	return dropUserSQL(oracleDialect{}.Quote(a.Name), a)
}

// oracleSchemaPrivileges are the privileges a grant on every table of a
// schema gives, by the privilege on one table each stands for (23ai).
var oracleSchemaPrivileges = map[string]string{"SELECT": "SELECT ANY TABLE", "READ": "READ ANY TABLE", "INSERT": "INSERT ANY TABLE",
	"UPDATE": "UPDATE ANY TABLE", "DELETE": "DELETE ANY TABLE", "ALTER": "ALTER ANY TABLE", "INDEX": "INDEX ANY TABLE"}

// GrantSQL grants privileges on a table, or on every table of a schema,
// present and to come, with 23ai's privileges on a schema.
func (oracleEngine) GrantSQL(privileges []string, schema, table string, a Account) ([]string, error) {
	if err := grantError(privileges, schema); err != nil {
		return nil, err
	}
	d := oracleDialect{}
	if table != "" {
		return []string{"GRANT " + strings.Join(privileges, ", ") + " ON " + QualifiedName(d, schema, table) + " TO " + d.Quote(a.Name)}, nil
	}
	var onSchema []string
	for _, p := range privileges {
		on, ok := oracleSchemaPrivileges[p]
		if !ok {
			return nil, fmt.Errorf("Oracle grants %s on one table at a time: choose the table", p)
		}
		onSchema = append(onSchema, on)
	}
	return []string{"GRANT " + strings.Join(onSchema, ", ") + " ON SCHEMA " + d.Quote(schema) + " TO " + d.Quote(a.Name)}, nil
}

func (oracleEngine) GrantRoleSQL(d Dialect, role, a Account) string { return grantRoleSQL(d, role, a) }

func (oracleEngine) Privileges() []string {
	return []string{"SELECT", "INSERT", "UPDATE", "DELETE", "READ", "ALTER", "INDEX", "REFERENCES"}
}

func (oracleEngine) AccountForm() AccountFormFields {
	return AccountFormFields{GrantTargetLabel: "Schema",
		PrivilegesNote: "From the DBA_ views (SELECT_CATALOG_ROLE). A user logs in once granted CONNECT. A schema-wide grant needs 23ai."}
}

// oracleActivityHint is what Oracle's activity pages need.
const oracleActivityHint = "These come from Oracle's V$ views, which need SELECT_CATALOG_ROLE or SELECT ANY DICTIONARY; " +
	"cancelling a query and ending a session need ALTER SYSTEM."

// oracleSessionLiteral is a row's session, its first column, as ALTER
// SYSTEM names it: 'sid,serial#'.
func oracleSessionLiteral(row []any) string { return standardLiteral(Display(row[0])) }

// oracleStop cancels the query a row's session runs, which keeps the
// session and its transaction (Oracle 18c and later).
func oracleStop(row []any) string { return "ALTER SYSTEM CANCEL SQL " + oracleSessionLiteral(row) }

// oracleKill ends a row's session at once, rolling its transaction back.
func oracleKill(row []any) string {
	return "ALTER SYSTEM KILL SESSION " + oracleSessionLiteral(row) + " IMMEDIATE"
}

// oracleLockMode names a lock mode of V$LOCK's LMODE or REQUEST, m.
func oracleLockMode(m string) string {
	return `DECODE(` + m + `, 0, NULL, 1, 'null', 2, 'row share', 3, 'row exclusive', 4, 'share', 5, 'share row exclusive', 6, 'exclusive', TO_CHAR(` + m + `))`
}

// ActivityPages read Oracle's sessions, locks and statement statistics
// in the pluggable database, the app's own session left out.
func (oracleEngine) ActivityPages() []ActivityPage {
	return []ActivityPage{{
		Name: "Sessions", Live: true, Stop: oracleStop, Kill: oracleKill, Hint: oracleActivityHint,
		Query: `SELECT s.sid || ',' || s.serial# AS "session", s.username AS "user", s.osuser AS "os user", s.machine AS "client",
  s.program AS "application", LOWER(s.status) AS "state", s.last_call_et AS "seconds in state", s.event AS "waiting on",
  s.blocking_session AS "blocked by", CASE WHEN s.taddr IS NOT NULL THEN 'yes' END AS "transaction", s.sql_id AS "sql id",
  SUBSTR(q.sql_text, 1, 400) AS "query"
FROM v$session s LEFT JOIN v$sql q ON q.sql_id = s.sql_id AND q.child_number = s.sql_child_number
WHERE s.type = 'USER' AND s.sid <> SYS_CONTEXT('USERENV', 'SID')
ORDER BY CASE s.status WHEN 'ACTIVE' THEN 0 ELSE 1 END, s.last_call_et DESC, s.sid`,
	}, {
		Name: "Locks", Live: true, Stop: oracleStop, Kill: oracleKill, Hint: oracleActivityHint,
		Query: `SELECT s.sid || ',' || s.serial# AS "session", s.username AS "user", l.type AS "lock",
  ` + oracleLockMode("l.lmode") + ` AS "held", ` + oracleLockMode("l.request") + ` AS "requested",
  CASE WHEN l.type = 'TM' THEN (SELECT o.owner || '.' || o.object_name FROM dba_objects o WHERE o.object_id = l.id1) END AS "object",
  l.ctime AS "seconds", CASE l.block WHEN 1 THEN 'yes' END AS "blocks others", s.blocking_session AS "blocked by",
  SUBSTR(q.sql_text, 1, 300) AS "query"
FROM v$lock l JOIN v$session s ON s.sid = l.sid
LEFT JOIN v$sql q ON q.sql_id = s.sql_id AND q.child_number = s.sql_child_number
WHERE s.type = 'USER' AND l.type IN ('TM', 'TX', 'UL') AND s.sid <> SYS_CONTEXT('USERENV', 'SID')
ORDER BY CASE WHEN l.request > 0 THEN 0 ELSE 1 END, l.block DESC, s.sid`,
	}, {
		Name: "Statistics", Hint: oracleActivityHint,
		EmptyHint: "Oracle keeps these for the statements in its shared pool, which ages them out.",
		Query: `SELECT SUBSTR(s.sql_text, 1, 400) AS "query", s.executions AS "calls", ROUND(s.elapsed_time / 1000, 1) AS "total ms",
  ROUND(s.elapsed_time / 1000 / NULLIF(s.executions, 0), 2) AS "mean ms", s.rows_processed AS "rows", s.buffer_gets AS "buffer gets",
  s.disk_reads AS "disk reads", s.sql_id AS "sql id"
FROM v$sqlstats s
ORDER BY s.elapsed_time DESC
FETCH FIRST 500 ROWS ONLY`,
	}}
}

// MaintenanceCommands keep a table in shape: statistics gathered as
// Oracle's own job does; the table moved, which rebuilds it and its
// indexes, locking it; its space shrunk, which moves rows and so needs
// ALTER TABLE … ENABLE ROW MOVEMENT first, left to the user as it lets
// a row's ROWID change; its structure checked.
func (oracleEngine) MaintenanceCommands(quotedTable string) []MaintenanceCommand {
	// DBMS_STATS takes a quoted name as it is, its case kept; a table of
	// no schema is the user's.
	owner, table := "USER", quotedTable
	if o, t, ok := strings.Cut(quotedTable, `"."`); ok {
		owner, table = standardLiteral(o+`"`), `"`+t
	}
	return []MaintenanceCommand{
		{"Gather Statistics", "CALL DBMS_STATS.GATHER_TABLE_STATS(" + owner + ", " + standardLiteral(table) + ")"},
		{"Move and Rebuild Indexes (locks the table)", "ALTER TABLE " + quotedTable + " MOVE UPDATE INDEXES"},
		{"Shrink Space (needs row movement enabled)", "ALTER TABLE " + quotedTable + " SHRINK SPACE CASCADE"},
		{"Validate Structure", "ANALYZE TABLE " + quotedTable + " VALIDATE STRUCTURE CASCADE ONLINE"},
	}
}
