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
func UsersSupported(e Engine) bool {
	r, ok := sqlRulesOf(e)
	return ok && r.ManagesAccounts()
}

// noAccountsError is the error of an account statement on an engine whose
// users the app does not manage.
func noAccountsError(e Engine) error { return fmt.Errorf("%s has no users the app manages", e.Label()) }

// accountName writes an account as the engine's statements name it.
func accountName(d Dialect, a Account) string { return rulesOfDialect(d).AccountName(a) }

// mysqlAccountName writes an account as MySQL names it, with the host.
func mysqlAccountName(a Account) string { return Literal(MySQL, a.Name) + "@" + Literal(MySQL, a.Host) }

// ListAccounts lists the users and roles of a server, the system's own
// roles of PostgreSQL left out.
func ListAccounts(ctx context.Context, d Dialect, q Querier) ([]Account, error) {
	if r, ok := sqlRulesOf(d.Engine()); ok {
		return r.ListAccounts(ctx, q)
	}
	return nil, noAccountsError(d.Engine())
}

// AccountPrivileges lists what an account may do, each with the
// statement that takes it back.
func AccountPrivileges(ctx context.Context, d Dialect, q Querier, a Account) ([]Privilege, error) {
	if r, ok := sqlRulesOf(d.Engine()); ok {
		return r.AccountPrivileges(ctx, q, a)
	}
	return nil, noAccountsError(d.Engine())
}

// showGrants reads an account's privileges from SHOW GRANTS.
func showGrants(ctx context.Context, q Querier, account string) ([]Privilege, error) {
	return scanAll(ctx, q, "SHOW GRANTS FOR "+account, nil, func(scan func(...any) error) (Privilege, error) {
		var grant string
		if err := scan(&grant); err != nil {
			return Privilege{}, err
		}
		return Privilege{Text: grant, Revoke: revokeOf(grant)}, nil
	})
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
	return rulesOfDialect(d).CreateAccountSQL(n)
}

// createRoleSQL creates a role, as every engine does.
func createRoleSQL(name string) string { return "CREATE ROLE " + name }

// PasswordSQL writes the statement changing an account's password.
func PasswordSQL(d Dialect, a Account, password string) (string, error) {
	return rulesOfDialect(d).PasswordSQL(a, password)
}

// DropAccountSQL writes the statements dropping an account.
func DropAccountSQL(d Dialect, a Account) []string { return rulesOfDialect(d).DropAccountSQL(a) }

// dropUserSQL drops a user, or a role, named name.
func dropUserSQL(name string, a Account) []string {
	if a.Role {
		return []string{"DROP ROLE " + name}
	}
	return []string{"DROP USER " + name}
}

// GrantSQL writes the statements granting privileges on a table of a
// schema, or on every table of it when table is "".
func GrantSQL(d Dialect, privileges []string, schema, table string, a Account) ([]string, error) {
	return rulesOfDialect(d).GrantSQL(privileges, schema, table, a)
}

// grantError says what a grant lacks.
func grantError(privileges []string, schema string) error {
	if len(privileges) == 0 {
		return errors.New("choose the privileges to grant")
	}
	if schema == "" {
		return errors.New("choose the schema, or database, the privileges are on")
	}
	return nil
}

// grantOnDatabaseSQL grants privileges on a database's tables, as MySQL
// and ClickHouse write it, to an account named name.
func grantOnDatabaseSQL(d Dialect, privileges []string, schema, table, name string) ([]string, error) {
	if err := grantError(privileges, schema); err != nil {
		return nil, err
	}
	on := d.Quote(schema) + ".*"
	if table != "" {
		on = QualifiedName(d, schema, table)
	}
	return []string{"GRANT " + strings.Join(privileges, ", ") + " ON " + on + " TO " + name}, nil
}

// GrantRoleSQL writes the statement making an account a member of a role.
func GrantRoleSQL(d Dialect, role Account, a Account) string {
	return "GRANT " + accountName(d, role) + " TO " + accountName(d, a)
}

// Privileges are those a grant offers, by engine.
func Privileges(e Engine) []string {
	if r, ok := sqlRulesOf(e); ok {
		return r.Privileges()
	}
	return standardPrivileges()
}

func standardPrivileges() []string {
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
