package db

import (
	"context"
	"crypto/tls"
	"os/exec"
	"strings"
	"time"
)

// mariadbDialect is MySQL's Dialect but for the engine it names, so that
// the rules found through it are MariaDB's, and for its catalog.
type mariadbDialect struct{ mysqlDialect }

func (mariadbDialect) Engine() Engine { return MariaDB }

// mariadbCatalog reads MariaDB's catalog: a sequence is an item, not a
// table; a system-versioned table says so with its engine; a JSON column,
// kept as LONGTEXT checked by JSON_VALID, is json; an index has no
// expressions.
var mariadbCatalog = catalogColumns{
	tables: " AND TABLE_TYPE <> 'SEQUENCE'",
	engine: "CONCAT_WS(', ', ENGINE, IF(TABLE_TYPE = 'SYSTEM VERSIONED', 'system-versioned', NULL))",
	columnType: `IF(DATA_TYPE = 'longtext' AND EXISTS (SELECT 1 FROM information_schema.CHECK_CONSTRAINTS k
    WHERE k.CONSTRAINT_SCHEMA = COLUMNS.TABLE_SCHEMA AND k.TABLE_NAME = COLUMNS.TABLE_NAME AND k.LEVEL = 'Column'
      AND k.CONSTRAINT_NAME = COLUMNS.COLUMN_NAME AND k.CHECK_CLAUSE = CONCAT('json_valid(` + "`" + `', COLUMNS.COLUMN_NAME, '` + "`" + `)')),
  'json', COLUMN_TYPE)`,
	columnDefault: "NULLIF(COLUMN_DEFAULT, 'NULL')",
	indexPart:     "COLUMN_NAME",
	// A JSON column's check is named after the column, in each table.
	checkJoin: " AND cc.TABLE_NAME = tc.TABLE_NAME",
}

func (mariadbDialect) Objects(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return mysqlObjects(ctx, q, schema, true, mariadbCatalog)
}

func (mariadbDialect) ObjectNames(ctx context.Context, q Querier, schema string) ([]Object, error) {
	return mysqlObjects(ctx, q, schema, false, mariadbCatalog)
}

func (mariadbDialect) Columns(ctx context.Context, q Querier, schema, table string) ([]Column, error) {
	cols, err := mysqlColumns(ctx, q, schema, table, mariadbCatalog)
	return cols[table], err
}

func (mariadbDialect) SchemaColumns(ctx context.Context, q Querier, schema string) (map[string][]Column, error) {
	return mysqlColumns(ctx, q, schema, "", mariadbCatalog)
}

func (d mariadbDialect) Indexes(ctx context.Context, q Querier, schema, table string) ([]Index, error) {
	return mysqlIndexes(ctx, q, d, schema, table, mariadbCatalog)
}

// ForeignKeys: MariaDB reports an action left out as RESTRICT, the same
// as NO ACTION on InnoDB.
func (mariadbDialect) ForeignKeys(ctx context.Context, q Querier, schema, table string) ([]ForeignKey, error) {
	return mysqlForeignKeys(ctx, q, schema, table, func(a string) string {
		if a == "RESTRICT" {
			return ""
		}
		return action(a)
	})
}

// Items lists routines, triggers, events and sequences.
func (mariadbDialect) Items(ctx context.Context, q Querier, schema string) ([]Item, error) {
	return mysqlItems(ctx, q, schema, `SELECT 'sequence', TABLE_NAME, '', '', ''
FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'SEQUENCE'`)
}

func (d mariadbDialect) ItemDDL(ctx context.Context, q Querier, it Item) (string, error) {
	if it.Kind == ItemSequence {
		return showCreate(ctx, q, "SHOW CREATE SEQUENCE "+QualifiedName(d, it.Schema, it.Name), "Create Table")
	}
	return d.mysqlDialect.ItemDDL(ctx, q, it)
}

// mariadbEngine is MariaDB: MySQL's rules but for its name, how it is
// reached by default, and what its server does otherwise.
type mariadbEngine struct{ mysqlEngine }

var _ SQLRules = mariadbEngine{}

func (mariadbEngine) Engine() Engine       { return MariaDB }
func (mariadbEngine) dialect() Dialect     { return mariadbDialect{} }
func (mariadbEngine) Label() string        { return "MariaDB" }
func (mariadbEngine) Base() Engine         { return MySQL }
func (mariadbEngine) ColorHex() string     { return "#c0765a" }
func (mariadbEngine) URLSchemes() []string { return []string{"mariadb"} }

func (mariadbEngine) CreateFile(context.Context, string) error { return noFileError(MariaDB) }

// open: MariaDB before 11.1 has no transaction_read_only; tx_read_only,
// deprecated since, still works.
func (mariadbEngine) open(_ context.Context, cfg Config, ep endpoint, tc *tls.Config, w wire) (openedPool, error) {
	sqldb, connector, err := openMySQL(cfg, ep, tc, w, "tx_read_only")
	return openedPool{sql: sqldb, connector: connector, tls: tc}, err
}

func (mariadbEngine) ReadDesignExtras(ctx context.Context, d *DB, t *TableDesign) error {
	return readMySQLDesign(ctx, d, t, mariadbDefault, mariadbCatalog)
}

// mariadbDefault writes a default as MariaDB's information_schema gives
// it: as SQL already, literals quoted, and NULL for none.
func mariadbDefault(def *string, _, _ string) string {
	if def == nil || *def == "NULL" {
		return ""
	}
	return *def
}

// DropConstraintSQL: MariaDB drops a check by DROP CONSTRAINT; it has no
// DROP CHECK.
func (m mariadbEngine) DropConstraintSQL(d Dialect, kind ConstraintKind, schema, table, name string) string {
	if kind == ConstraintCheck {
		return "ALTER TABLE " + QualifiedName(d, schema, table) + " DROP CONSTRAINT " + d.Quote(name)
	}
	return m.mysqlEngine.DropConstraintSQL(d, kind, schema, table, name)
}

// ActivityPages: MariaDB has no performance_schema.data_locks, and starts
// with performance_schema off.
func (mariadbEngine) ActivityPages() []ActivityPage {
	statistics := mysqlStatisticsPage("Statistics come from performance_schema, which MariaDB has off unless the server starts with performance_schema=ON, and the user may read.")
	statistics.EmptyHint = statistics.Hint // with performance_schema off, the page reads empty
	return []ActivityPage{mysqlSessionsPage(), {
		Name: "Locks", Live: true, Stop: sessionStatement(mysqlStopQuery), Kill: sessionStatement(mysqlEndSession),
		Query: `SELECT r.trx_mysql_thread_id AS id, p.USER AS user, REPLACE(l.lock_table, '` + "`" + `', '') AS object,
  l.lock_index AS index_name, l.lock_type AS lock_type, l.lock_mode AS mode,
  IF(r.trx_requested_lock_id <=> l.lock_id, 'WAITING', 'GRANTED') AS status,
  (SELECT GROUP_CONCAT(b.trx_mysql_thread_id) FROM information_schema.INNODB_LOCK_WAITS w
   JOIN information_schema.INNODB_TRX b ON b.trx_id = w.blocking_trx_id
   WHERE w.requested_lock_id = l.lock_id) AS waits_for,
  LEFT(r.trx_query, 300) AS query
FROM information_schema.INNODB_LOCKS l JOIN information_schema.INNODB_TRX r ON r.trx_id = l.lock_trx_id
LEFT JOIN information_schema.PROCESSLIST p ON p.ID = r.trx_mysql_thread_id
WHERE r.trx_mysql_thread_id <> CONNECTION_ID()
ORDER BY status = 'GRANTED', id`,
		Hint: "Locks come from information_schema.INNODB_LOCKS, which lists only the row locks a transaction waits for and those it waits behind, and which the user needs the PROCESS privilege to read.",
	}, statistics}
}

func (mariadbEngine) Search(ctx context.Context, d *DB, text string, definitions bool) ([]SearchHit, bool, error) {
	text = strings.ToLower(text)
	return searchWith(ctx, d, text, mariadbSearch, []any{text, definitions, text})
}

func (mariadbEngine) Explain(statement string, analyze bool) (ExplainRun, bool) {
	if analyze {
		return explainWithPrefix("ANALYZE FORMAT=JSON ", statement), true
	}
	return explainWithPrefix("EXPLAIN FORMAT=JSON ", statement), true
}

func (mariadbEngine) ParsePlan(_ []string, rows [][]any) (*Plan, bool) {
	return parseMariaDBPlan(planText(rows))
}

func (mariadbEngine) AdvisePlanStep(n *PlanNode, add func(n *PlanNode, format string, args ...any)) {
	adviseMySQLFamilyStep("MariaDB", n, add)
	if n.Op == mariadbBlockJoin && len(n.Children) == 1 && rowsOf(n.Children[0]) >= adviseRows {
		add(n, "A block nested-loop join compares the rows of %s with each batch of the rows before it: an index on its join columns may let MariaDB look them up instead.",
			n.Children[0].Target)
	}
}

// ListAccounts reads MariaDB's accounts from mysql.global_priv, whose
// mysql.user view lacks account_locked, and its roles, which have no
// host, from mysql.roles_mapping.
func (mariadbEngine) ListAccounts(ctx context.Context, q Querier) ([]Account, error) {
	members := map[string][]string{}
	err := scanRows(ctx, q, `SELECT User, Host, Role FROM mysql.roles_mapping ORDER BY Role`, nil, func(scan func(...any) error) error {
		var user, host, role string
		if err := scan(&user, &host, &role); err != nil {
			return err
		}
		members[user+"@"+host] = append(members[user+"@"+host], role)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return scanAll(ctx, q, `SELECT User, Host, COALESCE(JSON_VALUE(Priv, '$.is_role'), 0) = 1, COALESCE(JSON_VALUE(Priv, '$.account_locked'), 0) = 1,
  COALESCE(JSON_VALUE(Priv, '$.password_last_changed'), 1) = 0, COALESCE(JSON_VALUE(Priv, '$.plugin'), '')
FROM mysql.global_priv ORDER BY User, Host`, nil,
		func(scan func(...any) error) (Account, error) {
			var a Account
			var locked, expired bool
			var plugin string
			if err := scan(&a.Name, &a.Host, &a.Role, &locked, &expired, &plugin); err != nil {
				return a, err
			}
			if locked {
				a.Attributes = append(a.Attributes, "locked")
			}
			if expired {
				a.Attributes = append(a.Attributes, "password expired")
			}
			if plugin != "" {
				a.Attributes = append(a.Attributes, "logs in with "+plugin)
			}
			a.MemberOf = members[a.Name+"@"+a.Host]
			return a, nil
		})
}

func (mariadbEngine) AccountPrivileges(ctx context.Context, q Querier, a Account) ([]Privilege, error) {
	privileges, err := showGrants(ctx, q, mariadbAccountName(a))
	for i, p := range privileges {
		privileges[i] = mariadbPrivilege(p.Text)
	}
	return privileges, err
}

// mariadbPrivilege is a grant SHOW GRANTS printed, without the password
// hash, or the plugin's string, MariaDB writes into it after IDENTIFIED,
// which follows the grantee: the words between quotes are names.
func mariadbPrivilege(grant string) Privilege {
	words := unquotedIndexes(grant)
	to := -1
	for _, i := range words {
		if strings.HasPrefix(grant[i:], " TO ") {
			to = i
		}
	}
	start, end := -1, len(grant)
	for _, i := range words {
		switch {
		case i <= to:
		case start < 0 && strings.HasPrefix(grant[i:], " IDENTIFIED "):
			start = i
		case start >= 0 && (strings.HasPrefix(grant[i:], " REQUIRE ") || strings.HasPrefix(grant[i:], " WITH ")):
			end = min(end, i)
		}
	}
	if to >= 0 && start >= 0 {
		grant = grant[:start] + grant[end:]
	}
	return Privilege{Text: grant, Revoke: revokeOf(grant)}
}

// unquotedIndexes lists where each space of s outside quotes is.
func unquotedIndexes(s string) []int {
	var out []int
	var quote byte
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '`' || c == '\'' || c == '"':
			quote = c
		case c == ' ':
			out = append(out, i)
		}
	}
	return out
}

func (mariadbEngine) AccountForm() AccountFormFields {
	return AccountFormFields{Host: true, RoleWithoutHost: true, GrantTargetLabel: "Database"}
}

func (mariadbEngine) AccountName(a Account) string { return mariadbAccountName(a) }

// mariadbAccountName writes an account as MariaDB names it: a user with
// its host, a role, which has none, alone.
func mariadbAccountName(a Account) string {
	if a.Role {
		return Literal(MariaDB, a.Name)
	}
	return Literal(MariaDB, a.Name) + "@" + Literal(MariaDB, a.Host)
}

func (mariadbEngine) CreateAccountSQL(n NewAccount) (string, error) {
	return mysqlCreateAccountSQL(MariaDB, mariadbAccountName, n), nil
}

func (mariadbEngine) PasswordSQL(a Account, password string) (string, error) {
	return mysqlPasswordSQL(MariaDB, mariadbAccountName, a, password), nil
}

func (mariadbEngine) DropAccountSQL(a Account) []string {
	return dropUserSQL(mariadbAccountName(a), a)
}

func (mariadbEngine) GrantSQL(privileges []string, schema, table string, a Account) ([]string, error) {
	return grantOnDatabaseSQL(mariadbDialect{}, privileges, schema, table, mariadbAccountName(a))
}

func (mariadbEngine) PlanBackup(ctx context.Context, d *DB, o BackupOptions) (ToolRun, error) {
	tool, isMariaDB := mariadbDumpTool(ctx)
	if !isMariaDB {
		return planMySQLDump(ctx, d, o, tool, func(d *DB) []string {
			// MySQL's mysqldump reads column statistics MariaDB does not
			// keep, and fails.
			return append(mysqlClientFlags(d), "--column-statistics=0")
		})
	}
	return planMySQLDump(ctx, d, o, tool, mariadbClientFlags)
}

// mariadbDumpTool is the tool that backs up MariaDB: mariadb-dump, else
// mysqldump, which an older MariaDB installed under that name and MySQL
// installs as its own; isMariaDB reports which client it is.
func mariadbDumpTool(ctx context.Context) (tool string, isMariaDB bool) {
	if _, err := exec.LookPath("mariadb-dump"); err == nil {
		return "mariadb-dump", true
	}
	path, err := exec.LookPath("mysqldump")
	if err != nil {
		return "mariadb-dump", true // which RunTool says is missing
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, path, "--version").Output()
	return "mysqldump", strings.Contains(string(out), "MariaDB")
}

// mariadbClientFlags are how MariaDB's tools connect as the connection
// does. They have no --ssl-mode: TLS is on, and the certificate checked,
// unless told otherwise.
func mariadbClientFlags(d *DB) []string {
	var argv []string
	switch d.Config.TLS {
	case "", TLSDisable:
		argv = append(argv, "--skip-ssl")
	case TLSPrefer:
		argv = append(argv, "--skip-ssl-verify-server-cert")
	case TLSRequire:
		argv = append(argv, "--ssl", "--skip-ssl-verify-server-cert")
	case TLSVerifyFull:
		argv = append(argv, "--ssl", "--ssl-verify-server-cert")
	}
	return append(argv, clientCertificateFlags(d.Config)...)
}

func (mariadbEngine) BackupNote(restore bool) string {
	return backupNoteFor(restore, "A SQL file, as mariadb-dump writes, runs as Run SQL File runs it.",
		"mariadb-dump, or mysqldump where it is missing, writes it, in one transaction, with routines, triggers and events; one must be installed.")
}

// BackupToolChecksCAOnly: mariadb-dump has no check of the CA alone; under
// verify-full it checks the server's name too.
func (mariadbEngine) BackupToolChecksCAOnly(*DB) bool { return false }
