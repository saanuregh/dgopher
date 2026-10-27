package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// SQL Server's administration: its logins, users and roles, the activity
// of its sessions, and the commands that keep a table in shape.

// ManagesAccounts: a user is a login of the server with a user of the
// same name in the connection's database; a role is the database's.
func (sqlserverEngine) ManagesAccounts() bool { return true }

// sqlserverAccountsSQL lists the logins with their users in the current
// database, the users with no login, and the database's and the server's
// roles, but public, which everyone is in, and SQL Server's own
// certificate logins (##…##).
const sqlserverAccountsSQL = `SELECT 'login', sp.name, sp.type_desc, sp.is_disabled, COALESCE(sp.default_database_name, ''), COALESCE(dp.name, ''),
  COALESCE((SELECT STRING_AGG(r.name, CHAR(31)) FROM sys.server_role_members m JOIN sys.server_principals r ON r.principal_id = m.role_principal_id
    WHERE m.member_principal_id = sp.principal_id), '') + CHAR(31) +
  COALESCE((SELECT STRING_AGG(r.name, CHAR(31)) FROM sys.database_role_members m JOIN sys.database_principals r ON r.principal_id = m.role_principal_id
    WHERE m.member_principal_id = dp.principal_id), '')
FROM sys.server_principals sp LEFT JOIN sys.database_principals dp ON dp.sid = sp.sid
WHERE sp.type IN ('S', 'U', 'G', 'E', 'X') AND sp.name NOT LIKE '##%'
UNION ALL
SELECT 'user', dp.name, dp.type_desc, 0, '', dp.name,
  COALESCE((SELECT STRING_AGG(r.name, CHAR(31)) FROM sys.database_role_members m JOIN sys.database_principals r ON r.principal_id = m.role_principal_id
    WHERE m.member_principal_id = dp.principal_id), '')
FROM sys.database_principals dp
WHERE dp.type IN ('S', 'U', 'G', 'E', 'X', 'C', 'K') AND dp.name NOT IN ('dbo', 'guest', 'INFORMATION_SCHEMA', 'sys')
  AND NOT EXISTS (SELECT 1 FROM sys.server_principals sp WHERE sp.sid = dp.sid)
UNION ALL
SELECT 'role', dp.name, dp.type_desc, dp.is_fixed_role, '', '',
  COALESCE((SELECT STRING_AGG(r.name, CHAR(31)) FROM sys.database_role_members m JOIN sys.database_principals r ON r.principal_id = m.role_principal_id
    WHERE m.member_principal_id = dp.principal_id), '')
FROM sys.database_principals dp WHERE dp.type = 'R' AND dp.name <> 'public'
UNION ALL
SELECT 'server role', sp.name, sp.type_desc, sp.is_fixed_role, '', '', ''
FROM sys.server_principals sp WHERE sp.type = 'R' AND sp.name <> 'public'
ORDER BY 1, 2`

func (sqlserverEngine) ListAccounts(ctx context.Context, q Querier) ([]Account, error) {
	return scanAll(ctx, q, sqlserverAccountsSQL, nil, func(scan func(...any) error) (Account, error) {
		var a Account
		var kind, typ, database, memberOf string
		var flag bool
		if err := scan(&kind, &a.Name, &typ, &flag, &database, &a.User, &memberOf); err != nil {
			return a, err
		}
		a.MemberOf = splitList(strings.Trim(memberOf, unitSep))
		typ = strings.ReplaceAll(strings.ToLower(typ), "_", " ")
		switch kind {
		case "login":
			a.Attributes = append(a.Attributes, strings.Replace(typ, "sql ", "SQL ", 1))
			if flag {
				a.Attributes = append(a.Attributes, "disabled")
			}
			if database != "" {
				a.Attributes = append(a.Attributes, "default database "+database)
			}
			switch a.User {
			case "":
				a.Attributes = append(a.Attributes, "no user in this database")
			case a.Name:
			default:
				a.Attributes = append(a.Attributes, "user "+a.User+" in this database")
			}
		case "user":
			a.NoLogin = true
			a.Attributes = append(a.Attributes, "user without a login")
		case "role":
			a.Role = true
			a.Attributes = append(a.Attributes, "database role")
		case "server role":
			a.Role, a.ServerRole = true, true
			a.Attributes = append(a.Attributes, "server role")
		}
		if a.Role && flag {
			a.Attributes = append(a.Attributes, "fixed")
		}
		return a, nil
	})
}

// sqlserverPrincipal is the principal of the current database an account
// is: a login's user, which shares its name unless read otherwise.
func sqlserverPrincipal(a Account) string {
	if a.Role || a.NoLogin || a.User == "" {
		return a.Name
	}
	return a.User
}

// AccountPrivileges lists what the account's principal may do in the
// current database, and a login what it may do on the server: GRANT and
// DENY alike, which REVOKE takes back. CONNECT, which the user itself
// is, is not taken back alone.
func (sqlserverEngine) AccountPrivileges(ctx context.Context, q Querier, a Account) ([]Privilege, error) {
	d := sqlserverDialect{}
	var out []Privilege
	if !a.ServerRole {
		name := sqlserverPrincipal(a)
		privs, err := scanAll(ctx, q, `SELECT p.state, p.permission_name, p.class,
  CASE p.class WHEN 1 THEN QUOTENAME(OBJECT_SCHEMA_NAME(p.major_id)) + '.' + QUOTENAME(OBJECT_NAME(p.major_id))
    WHEN 3 THEN QUOTENAME(SCHEMA_NAME(p.major_id)) ELSE p.class_desc END,
  COALESCE(QUOTENAME(c.name), '')
FROM sys.database_permissions p
LEFT JOIN sys.columns c ON p.class = 1 AND p.minor_id > 0 AND c.object_id = p.major_id AND c.column_id = p.minor_id
WHERE p.grantee_principal_id = DATABASE_PRINCIPAL_ID(@p1)
ORDER BY p.class, 4, p.permission_name`, []any{name}, func(scan func(...any) error) (Privilege, error) {
			var state, permission, on, column string
			var class int
			if err := scan(&state, &permission, &class, &on, &column); err != nil {
				return Privilege{}, err
			}
			if column != "" {
				permission += " (" + column + ")"
			}
			securable, shown := "", ""
			switch class {
			case 0:
			case 1:
				securable, shown = " ON OBJECT::"+on, " ON "+on
			case 3:
				securable, shown = " ON SCHEMA::"+on, " ON SCHEMA::"+on
			default:
				return Privilege{Text: sqlserverState(state) + " " + permission + " on a " + strings.ToLower(on)}, nil
			}
			p := Privilege{Text: sqlserverState(state) + " " + permission + shown}
			if permission != "CONNECT" {
				p.Revoke = "REVOKE " + permission + securable + " FROM " + d.Quote(name)
				if state == "W" {
					p.Revoke += " CASCADE"
				}
			}
			return p, nil
		})
		if err != nil {
			return nil, err
		}
		out = append(out, privs...)
	}
	if a.Role && !a.ServerRole || a.NoLogin {
		return out, nil
	}
	server, err := scanAll(ctx, q, `SELECT p.state, p.permission_name, p.class, p.class_desc
FROM sys.server_permissions p JOIN sys.server_principals sp ON sp.principal_id = p.grantee_principal_id
WHERE sp.name = @p1 ORDER BY p.class, p.permission_name`, []any{a.Name}, func(scan func(...any) error) (Privilege, error) {
		var state, permission, desc string
		var class int
		if err := scan(&state, &permission, &class, &desc); err != nil {
			return Privilege{}, err
		}
		p := Privilege{Text: sqlserverState(state) + " " + permission + " on the server"}
		switch {
		case class != 100:
			p.Text = sqlserverState(state) + " " + permission + " on an " + strings.ReplaceAll(strings.ToLower(desc), "_", " ")
		case permission != "CONNECT SQL":
			p.Revoke = "REVOKE " + permission + " FROM " + d.Quote(a.Name)
			if state == "W" {
				p.Revoke += " CASCADE"
			}
		}
		return p, nil
	})
	return append(out, server...), err
}

// sqlserverState writes a permission's state as the statement that gave
// it: G GRANT, D DENY, W GRANT WITH GRANT OPTION.
func sqlserverState(state string) string {
	switch state {
	case "D":
		return "DENY"
	case "W":
		return "GRANT WITH GRANT OPTION"
	}
	return "GRANT"
}

func (sqlserverEngine) AccountName(a Account) string { return sqlserverDialect{}.Quote(a.Name) }

// CreateAccountSQL makes a role of the database, or a login with a user
// of the same name in it. The login's password policy is the server's,
// and its default database master.
func (sqlserverEngine) CreateAccountSQL(n NewAccount) (string, error) {
	name := sqlserverDialect{}.Quote(n.Name)
	if n.Role {
		return createRoleSQL(name), nil
	}
	if n.Password == "" {
		return "", errors.New("a login needs a password")
	}
	return "CREATE LOGIN " + name + " WITH PASSWORD = " + sqlserverText(n.Password) + ";\nCREATE USER " + name + " FOR LOGIN " + name, nil
}

// PasswordSQL changes a login's password, or a contained user's.
func (sqlserverEngine) PasswordSQL(a Account, password string) (string, error) {
	switch {
	case a.Role:
		return "", fmt.Errorf("%s is a role, which has no password", a.Name)
	case a.NoLogin:
		return "ALTER USER " + sqlserverDialect{}.Quote(a.Name) + " WITH PASSWORD = " + sqlserverText(password), nil
	}
	return "ALTER LOGIN " + sqlserverDialect{}.Quote(a.Name) + " WITH PASSWORD = " + sqlserverText(password), nil
}

// DropAccountSQL drops a login with its user in the current database,
// but dbo, the database's owner.
func (sqlserverEngine) DropAccountSQL(a Account) []string {
	q := sqlserverDialect{}.Quote
	switch {
	case a.ServerRole:
		return []string{"DROP SERVER ROLE " + q(a.Name)}
	case a.Role:
		return []string{"DROP ROLE " + q(a.Name)}
	case a.NoLogin:
		return []string{"DROP USER " + q(a.Name)}
	}
	var out []string
	if user := sqlserverPrincipal(a); user != "dbo" {
		out = append(out, "DROP USER IF EXISTS "+q(user))
	}
	return append(out, "DROP LOGIN "+q(a.Name))
}

// GrantSQL grants privileges on a table, or on every table of a schema,
// to the account's principal in the current database: a login's user,
// made first when it may have none.
func (sqlserverEngine) GrantSQL(privileges []string, schema, table string, a Account) ([]string, error) {
	if err := grantError(privileges, schema); err != nil {
		return nil, err
	}
	if a.ServerRole {
		return nil, fmt.Errorf("%s is a role of the server, which holds no privileges on a database's tables", a.Name)
	}
	d := sqlserverDialect{}
	on := "SCHEMA::" + d.Quote(schema)
	if table != "" {
		on = "OBJECT::" + QualifiedName(d, schema, table)
	}
	principal := sqlserverPrincipal(a)
	var out []string
	if !a.Role && !a.NoLogin && a.User == "" {
		out = append(out, "IF DATABASE_PRINCIPAL_ID("+sqlserverText(a.Name)+") IS NULL CREATE USER "+d.Quote(a.Name)+" FOR LOGIN "+d.Quote(a.Name))
	}
	return append(out, "GRANT "+strings.Join(privileges, ", ")+" ON "+on+" TO "+d.Quote(principal)), nil
}

// GrantRoleSQL adds the account to a role of the database, its login's
// user, or to a role of the server, its login.
func (sqlserverEngine) GrantRoleSQL(d Dialect, role, a Account) string {
	if role.ServerRole {
		return "ALTER SERVER ROLE " + d.Quote(role.Name) + " ADD MEMBER " + d.Quote(a.Name)
	}
	return "ALTER ROLE " + d.Quote(role.Name) + " ADD MEMBER " + d.Quote(sqlserverPrincipal(a))
}

func (sqlserverEngine) Privileges() []string {
	return []string{"SELECT", "INSERT", "UPDATE", "DELETE", "EXECUTE", "REFERENCES", "ALTER", "VIEW DEFINITION"}
}

func (sqlserverEngine) AccountForm() AccountFormFields {
	return AccountFormFields{GrantTargetLabel: "Schema",
		PrivilegesNote: "In this database, and a login's on the server. Revoke takes back a GRANT or a DENY."}
}

// sqlserverStateHint is what SQL Server's activity pages need.
const sqlserverStateHint = "These come from SQL Server's dynamic management views, which need VIEW SERVER STATE " +
	"(VIEW DATABASE STATE on Azure SQL Database)."

// sqlserverSessionStatement ends the session a row's first column names:
// SQL Server stops another session's query only by ending the session,
// which rolls its transaction back.
var sqlserverSessionStatement = sessionStatement("KILL %s")

// ActivityPages read SQL Server's sessions, locks and statement
// statistics, the app's own session left out. The Locks page names the
// objects of the current database from the catalog read WITH (NOLOCK):
// OBJECT_NAME waits for the locks of a table made in a transaction,
// which the page would then wait for.
func (sqlserverEngine) ActivityPages() []ActivityPage {
	return []ActivityPage{{
		Name: "Sessions", Live: true, Kill: sqlserverSessionStatement, Hint: sqlserverStateHint,
		EmptyHint: "Without VIEW SERVER STATE, SQL Server shows a login its own sessions only.",
		Query: `SELECT s.session_id AS session, s.login_name AS login, DB_NAME(s.database_id) AS [database], s.program_name AS application,
  c.client_net_address AS client, COALESCE(r.status, s.status) AS state,
  CONVERT(varchar(12), DATEADD(millisecond, r.total_elapsed_time, 0), 114) AS running_for, r.wait_type AS waiting_on,
  NULLIF(r.blocking_session_id, 0) AS blocked_by, s.open_transaction_count AS transactions, LEFT(t.text, 400) AS query
FROM sys.dm_exec_sessions s
LEFT JOIN sys.dm_exec_requests r ON r.session_id = s.session_id
LEFT JOIN sys.dm_exec_connections c ON c.session_id = s.session_id AND c.parent_connection_id IS NULL
OUTER APPLY sys.dm_exec_sql_text(COALESCE(r.sql_handle, c.most_recent_sql_handle)) t
WHERE s.is_user_process = 1 AND s.session_id <> @@SPID
ORDER BY CASE WHEN r.session_id IS NULL THEN 1 ELSE 0 END, r.start_time, s.session_id`,
	}, {
		Name: "Locks", Live: true, Kill: sqlserverSessionStatement, Hint: sqlserverStateHint,
		Query: `SELECT l.request_session_id AS session, s.login_name AS login, l.resource_type, DB_NAME(l.resource_database_id) AS [database],
  CASE WHEN l.resource_database_id <> DB_ID() THEN NULL
    WHEN l.resource_type = 'OBJECT' THEN (SELECT SCHEMA_NAME(o.schema_id) + '.' + o.name FROM sys.objects o WITH (NOLOCK)
      WHERE o.object_id = l.resource_associated_entity_id)
    WHEN l.resource_type IN ('KEY', 'PAGE', 'RID', 'HOBT') THEN (SELECT SCHEMA_NAME(o.schema_id) + '.' + o.name
      FROM sys.partitions p WITH (NOLOCK) JOIN sys.objects o WITH (NOLOCK) ON o.object_id = p.object_id WHERE p.hobt_id = l.resource_associated_entity_id)
  END AS object,
  l.resource_description AS resource, l.request_mode AS mode, l.request_status AS status, NULLIF(r.blocking_session_id, 0) AS blocked_by,
  r.wait_time AS waited_ms, LEFT(t.text, 300) AS query
FROM sys.dm_tran_locks l
JOIN sys.dm_exec_sessions s ON s.session_id = l.request_session_id
LEFT JOIN sys.dm_exec_requests r ON r.session_id = l.request_session_id
OUTER APPLY sys.dm_exec_sql_text(r.sql_handle) t
WHERE l.request_session_id <> @@SPID AND l.resource_type <> 'DATABASE'
ORDER BY CASE l.request_status WHEN 'GRANT' THEN 1 ELSE 0 END, l.request_session_id`,
	}, {
		Name: "Statistics", Hint: sqlserverStateHint,
		EmptyHint: "SQL Server keeps these for the plans in its cache, which a restart or DBCC FREEPROCCACHE empties.",
		Query: `SELECT TOP (500) LEFT(SUBSTRING(t.text, qs.statement_start_offset / 2 + 1,
    (CASE qs.statement_end_offset WHEN -1 THEN DATALENGTH(t.text) ELSE qs.statement_end_offset END - qs.statement_start_offset) / 2 + 1), 400) AS query,
  qs.execution_count AS calls, CAST(qs.total_elapsed_time / 1000.0 AS decimal(18,1)) AS total_ms,
  CAST(qs.total_elapsed_time / 1000.0 / qs.execution_count AS decimal(18,2)) AS mean_ms, CAST(qs.max_elapsed_time / 1000.0 AS decimal(18,1)) AS max_ms,
  qs.total_rows AS [rows], qs.total_logical_reads AS logical_reads, DB_NAME(t.dbid) AS [database]
FROM sys.dm_exec_query_stats qs CROSS APPLY sys.dm_exec_sql_text(qs.sql_handle) t
ORDER BY qs.total_elapsed_time DESC`,
	}}
}

// MaintenanceCommands keep a table in shape; REBUILD takes the table
// offline while it runs, but on editions that rebuild online.
func (sqlserverEngine) MaintenanceCommands(quotedTable string) []MaintenanceCommand {
	return []MaintenanceCommand{
		{"Update Statistics", "UPDATE STATISTICS " + quotedTable},
		{"Reorganize Indexes", "ALTER INDEX ALL ON " + quotedTable + " REORGANIZE"},
		{"Rebuild Indexes (locks the table)", "ALTER INDEX ALL ON " + quotedTable + " REBUILD"},
		{"Check (DBCC CHECKTABLE)", "DBCC CHECKTABLE (" + sqlserverText(quotedTable) + ")"},
	}
}
