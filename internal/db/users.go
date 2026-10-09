package db

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Account is a user or a role of a server.
type Account struct {
	Name string
	Host string // MySQL's: where the user connects from
	// Role is set for a role, as no one logs in as; on MySQL, where
	// roles are accounts, for one that cannot log in.
	Role bool
	// Attributes are what the account may do, or is, in a few words each.
	Attributes []string
	MemberOf   []string
}

// Label names the account as its server writes it.
func (a Account) Label() string {
	if a.Host != "" {
		return a.Name + "@" + a.Host
	}
	return a.Name
}

// Privilege is what an account may do on an object, with the statement
// taking it back, "" when it cannot be taken back alone.
type Privilege struct {
	Text   string
	Revoke string
}

// UsersSupported reports whether the app manages an engine's users.
func UsersSupported(e Engine) bool { return e == Postgres || e == MySQL || e == ClickHouse }

// accountName writes an account as the engine's statements name it.
func accountName(d Dialect, a Account) string {
	if d.Engine() == MySQL {
		return Literal(MySQL, a.Name) + "@" + Literal(MySQL, a.Host)
	}
	return d.Quote(a.Name)
}

// ListAccounts lists the users and roles of a server, the system's own
// roles of PostgreSQL left out.
func ListAccounts(ctx context.Context, d Dialect, q Querier) ([]Account, error) {
	var out []Account
	switch d.Engine() {
	case Postgres:
		err := scanRows(ctx, q, `SELECT r.rolname, r.rolcanlogin, r.rolsuper, r.rolcreatedb, r.rolcreaterole, r.rolreplication, r.rolbypassrls,
  r.rolconnlimit, coalesce(r.rolvaliduntil::text, ''),
  coalesce((SELECT string_agg(b.rolname, ',' ORDER BY b.rolname) FROM pg_auth_members m JOIN pg_roles b ON b.oid = m.roleid WHERE m.member = r.oid), '')
FROM pg_roles r WHERE r.rolname !~ '^pg_' ORDER BY r.rolname`, nil, func(scan func(...any) error) error {
			var a Account
			var login, super, createDB, createRole, replication, bypass bool
			var limit int
			var until, member string
			if err := scan(&a.Name, &login, &super, &createDB, &createRole, &replication, &bypass, &limit, &until, &member); err != nil {
				return err
			}
			a.Role = !login
			for _, f := range []struct {
				on   bool
				what string
			}{{login, "can log in"}, {super, "superuser"}, {createDB, "creates databases"}, {createRole, "creates roles"},
				{replication, "replication"}, {bypass, "bypasses row security"}} {
				if f.on {
					a.Attributes = append(a.Attributes, f.what)
				}
			}
			if limit >= 0 {
				a.Attributes = append(a.Attributes, fmt.Sprintf("at most %d connections", limit))
			}
			if until != "" {
				a.Attributes = append(a.Attributes, "password valid until "+until)
			}
			a.MemberOf = splitList(member)
			out = append(out, a)
			return nil
		})
		return out, err
	case MySQL:
		members := map[string][]string{}
		err := scanRows(ctx, q, `SELECT TO_USER, TO_HOST, FROM_USER, FROM_HOST FROM mysql.role_edges`, nil, func(scan func(...any) error) error {
			var toUser, toHost, fromUser, fromHost string
			if err := scan(&toUser, &toHost, &fromUser, &fromHost); err != nil {
				return err
			}
			key := toUser + "@" + toHost
			members[key] = append(members[key], fromUser+"@"+fromHost)
			return nil
		})
		if err != nil {
			return nil, err
		}
		err = scanRows(ctx, q, `SELECT User, Host, account_locked, password_expired, plugin, authentication_string = '' FROM mysql.user ORDER BY User, Host`, nil,
			func(scan func(...any) error) error {
				var a Account
				var locked, expired, plugin string
				var noPassword bool
				if err := scan(&a.Name, &a.Host, &locked, &expired, &plugin, &noPassword); err != nil {
					return err
				}
				// A role of MySQL is an account locked and without a password.
				a.Role = locked == "Y" && noPassword
				if locked == "Y" {
					a.Attributes = append(a.Attributes, "locked")
				}
				if expired == "Y" {
					a.Attributes = append(a.Attributes, "password expired")
				}
				a.Attributes = append(a.Attributes, "logs in with "+plugin)
				a.MemberOf = members[a.Name+"@"+a.Host]
				out = append(out, a)
				return nil
			})
		return out, err
	case ClickHouse:
		members := map[string][]string{}
		err := scanRows(ctx, q, `SELECT coalesce(user_name, role_name), granted_role_name FROM system.role_grants`, nil, func(scan func(...any) error) error {
			var who, role string
			if err := scan(&who, &role); err != nil {
				return err
			}
			members[who] = append(members[who], role)
			return nil
		})
		if err != nil {
			return nil, err
		}
		err = scanRows(ctx, q, `SELECT name, 0, toString(auth_type) FROM system.users UNION ALL SELECT name, 1, '' FROM system.roles ORDER BY 1`, nil,
			func(scan func(...any) error) error {
				var a Account
				var role uint8
				var auth string
				if err := scan(&a.Name, &role, &auth); err != nil {
					return err
				}
				a.Role = role == 1
				if auth != "" {
					a.Attributes = append(a.Attributes, "logs in with "+strings.Trim(auth, "[]'"))
				}
				a.MemberOf = members[a.Name]
				out = append(out, a)
				return nil
			})
		return out, err
	}
	return nil, fmt.Errorf("%s has no users the app manages", d.Engine().Label())
}

// AccountPrivileges lists what an account may do, each with the
// statement that takes it back.
func AccountPrivileges(ctx context.Context, d Dialect, q Querier, a Account) ([]Privilege, error) {
	var out []Privilege
	switch d.Engine() {
	case Postgres:
		err := scanRows(ctx, q, `SELECT kind, name, string_agg(privilege_type, ', ' ORDER BY privilege_type) FROM (
  SELECT 'TABLE' AS kind, quote_ident(n.nspname) || '.' || quote_ident(c.relname) AS name, x.privilege_type, x.grantee
  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace CROSS JOIN LATERAL aclexplode(c.relacl) x
  WHERE c.relkind IN ('r', 'v', 'm', 'p', 'f')
  UNION ALL
  SELECT 'SCHEMA', quote_ident(n.nspname), x.privilege_type, x.grantee FROM pg_namespace n CROSS JOIN LATERAL aclexplode(n.nspacl) x
  UNION ALL
  SELECT 'DATABASE', quote_ident(d.datname), x.privilege_type, x.grantee FROM pg_database d CROSS JOIN LATERAL aclexplode(d.datacl) x
) p JOIN pg_roles r ON r.oid = p.grantee WHERE r.rolname = $1
GROUP BY kind, name ORDER BY kind = 'TABLE', kind, name`, []any{a.Name}, func(scan func(...any) error) error {
			var kind, name, privs string
			if err := scan(&kind, &name, &privs); err != nil {
				return err
			}
			out = append(out, Privilege{Text: privs + " on " + strings.ToLower(kind) + " " + name,
				Revoke: "REVOKE " + privs + " ON " + kind + " " + name + " FROM " + accountName(d, a)})
			return nil
		})
		if err != nil {
			return nil, err
		}
		for _, role := range a.MemberOf {
			out = append(out, Privilege{Text: "member of " + role, Revoke: "REVOKE " + d.Quote(role) + " FROM " + accountName(d, a)})
		}
		return out, nil
	case MySQL, ClickHouse:
		err := scanRows(ctx, q, "SHOW GRANTS FOR "+accountName(d, a), nil, func(scan func(...any) error) error {
			var grant string
			if err := scan(&grant); err != nil {
				return err
			}
			out = append(out, Privilege{Text: grant, Revoke: revokeOf(grant)})
			return nil
		})
		return out, err
	}
	return nil, fmt.Errorf("%s has no users the app manages", d.Engine().Label())
}

// revokeOf turns a GRANT that SHOW GRANTS printed into the REVOKE taking
// it back: the privileges, not the right to grant them on, which a
// REVOKE of its own takes; "" for what cannot be taken back, as MySQL's
// USAGE, which is the account itself.
func revokeOf(grant string) string {
	g := strings.TrimSuffix(strings.TrimSpace(grant), " WITH GRANT OPTION")
	g = strings.TrimSuffix(g, " WITH ADMIN OPTION")
	if !strings.HasPrefix(g, "GRANT ") || strings.HasPrefix(g, "GRANT USAGE ON *.*") {
		return ""
	}
	to := strings.LastIndex(g, " TO ")
	if to < 0 {
		return ""
	}
	return "REVOKE " + g[len("GRANT "):to] + " FROM " + g[to+len(" TO "):]
}

// NewAccount is what creating an account needs.
type NewAccount struct {
	Account
	Password string
	// CreateDB and CreateRole give a PostgreSQL user those rights.
	CreateDB, CreateRole bool
}

// CreateAccountSQL writes the statement creating an account.
func CreateAccountSQL(d Dialect, n NewAccount) (string, error) {
	name := accountName(d, n.Account)
	if n.Role {
		return "CREATE ROLE " + name, nil
	}
	switch d.Engine() {
	case Postgres:
		sql := "CREATE ROLE " + name + " WITH LOGIN"
		if n.CreateDB {
			sql += " CREATEDB"
		}
		if n.CreateRole {
			sql += " CREATEROLE"
		}
		if n.Password == "" {
			return sql, nil
		}
		pw, err := postgresPassword(n.Password)
		return sql + " PASSWORD " + pw, err
	case MySQL:
		return "CREATE USER " + name + " IDENTIFIED BY " + Literal(MySQL, n.Password), nil
	case ClickHouse:
		if n.Password == "" {
			return "CREATE USER " + name + " IDENTIFIED WITH no_password", nil
		}
		return "CREATE USER " + name + " IDENTIFIED WITH sha256_password BY " + Literal(ClickHouse, n.Password), nil
	}
	return "", fmt.Errorf("%s has no users the app manages", d.Engine().Label())
}

// PasswordSQL writes the statement changing an account's password.
func PasswordSQL(d Dialect, a Account, password string) (string, error) {
	name := accountName(d, a)
	switch d.Engine() {
	case Postgres:
		pw, err := postgresPassword(password)
		return "ALTER ROLE " + name + " WITH PASSWORD " + pw, err
	case MySQL:
		return "ALTER USER " + name + " IDENTIFIED BY " + Literal(MySQL, password), nil
	case ClickHouse:
		return "ALTER USER " + name + " IDENTIFIED WITH sha256_password BY " + Literal(ClickHouse, password), nil
	}
	return "", fmt.Errorf("%s has no users the app manages", d.Engine().Label())
}

// DropAccountSQL writes the statements dropping an account. A
// PostgreSQL role cannot be dropped while it owns objects or holds
// privileges: what it owns in the database passes to the user dropping
// it, never dropped, then its privileges are revoked. Those of other
// databases stay, and the DROP says where.
func DropAccountSQL(d Dialect, a Account) []string {
	name := accountName(d, a)
	switch {
	case d.Engine() == Postgres:
		return []string{"REASSIGN OWNED BY " + name + " TO CURRENT_USER", "DROP OWNED BY " + name, "DROP ROLE " + name}
	case a.Role:
		return []string{"DROP ROLE " + name}
	}
	return []string{"DROP USER " + name}
}

// GrantSQL writes the statements granting privileges on a table of a
// schema, or on every table of it when table is "". On PostgreSQL a
// schema's tables are reached only through the schema, whose USAGE is
// granted too.
func GrantSQL(d Dialect, privileges []string, schema, table string, a Account) ([]string, error) {
	if len(privileges) == 0 {
		return nil, errors.New("choose the privileges to grant")
	}
	if schema == "" {
		return nil, errors.New("choose the schema, or database, the privileges are on")
	}
	privs, name := strings.Join(privileges, ", "), accountName(d, a)
	switch d.Engine() {
	case Postgres:
		on := "ALL TABLES IN SCHEMA " + d.Quote(schema)
		if table != "" {
			on = "TABLE " + QualifiedName(d, schema, table)
		}
		return []string{"GRANT USAGE ON SCHEMA " + d.Quote(schema) + " TO " + name, "GRANT " + privs + " ON " + on + " TO " + name}, nil
	case MySQL, ClickHouse:
		on := d.Quote(schema) + ".*"
		if table != "" {
			on = QualifiedName(d, schema, table)
		}
		return []string{"GRANT " + privs + " ON " + on + " TO " + name}, nil
	}
	return nil, fmt.Errorf("%s has no users the app manages", d.Engine().Label())
}

// GrantRoleSQL writes the statement making an account a member of a role.
func GrantRoleSQL(d Dialect, role Account, a Account) string {
	return "GRANT " + accountName(d, role) + " TO " + accountName(d, a)
}

// Privileges are those a grant offers, by engine.
func Privileges(e Engine) []string {
	if e == ClickHouse {
		return []string{"SELECT", "INSERT", "ALTER", "ALL"}
	}
	return []string{"SELECT", "INSERT", "UPDATE", "DELETE", "ALL PRIVILEGES"}
}

// postgresPassword writes a password for PostgreSQL's CREATE or ALTER
// ROLE: hashed here as SCRAM-SHA-256, as psql's \password does, so that
// the password itself never reaches the server or its log. PostgreSQL
// normalizes a password of other than ASCII (SASLprep) before hashing it;
// such a password goes as it is, for the server to.
func postgresPassword(password string) (string, error) {
	for i := 0; i < len(password); i++ {
		if password[i] >= 0x80 {
			return Literal(Postgres, password), nil
		}
	}
	verifier, err := scramVerifier(password, nil)
	if err != nil {
		return "", err
	}
	return Literal(Postgres, verifier), nil
}

// scramIterations is how many times PostgreSQL's SCRAM-SHA-256 hashes a
// password, as its default.
const scramIterations = 4096

// scramVerifier is the SCRAM-SHA-256 verifier PostgreSQL keeps for a
// password: SCRAM-SHA-256$iterations:salt$StoredKey:ServerKey (RFC 5802),
// with a new random salt when salt is nil.
func scramVerifier(password string, salt []byte) (string, error) {
	if salt == nil {
		salt = make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return "", err
		}
	}
	salted, err := pbkdf2.Key(sha256.New, password, salt, scramIterations, sha256.Size)
	if err != nil {
		return "", err
	}
	mac := func(key []byte, msg string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(msg))
		return h.Sum(nil)
	}
	stored := sha256.Sum256(mac(salted, "Client Key"))
	b64 := base64.StdEncoding.EncodeToString
	return "SCRAM-SHA-256$" + strconv.Itoa(scramIterations) + ":" + b64(salt) + "$" + b64(stored[:]) + ":" + b64(mac(salted, "Server Key")), nil
}
