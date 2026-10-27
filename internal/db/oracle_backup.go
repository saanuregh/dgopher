package db

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Oracle's backups: Data Pump's expdp exports a schema into a dump file
// in a directory object of the server, where the file stays, and impdp
// imports one into a schema. Both are Oracle's client tools, run here;
// the user and password go in a parameter file only they read, the rest
// on their command line, as the app shows and audits it.

func (oracleEngine) BackupFormats() []BackupFormat { return []BackupFormat{BackupDataPump} }

// oracleDumpDirectory is the directory object a dump file named alone
// is in, which every Oracle database has.
const oracleDumpDirectory = "DATA_PUMP_DIR"

var oracleDumpName = regexp.MustCompile(`^[A-Za-z0-9_$#.\-]+$`)

// oracleDumpFile reads a dump file's name, DIRECTORY:file as Data Pump
// writes it, or file alone in DATA_PUMP_DIR; .dmp is added to a name
// without an extension. Its log goes beside it, named after it and what
// ran.
func oracleDumpFile(path, action string) (directory, file, log string, err error) {
	path = strings.TrimSpace(path)
	directory, file = oracleDumpDirectory, path
	if dir, name, ok := strings.Cut(path, ":"); ok {
		if !oracleUnquotedName.MatchString(dir) {
			return "", "", "", fmt.Errorf("%s is not a directory object's name, as DATA_PUMP_DIR", dir)
		}
		directory, file = strings.ToUpper(dir), name
	}
	if !oracleDumpName.MatchString(file) {
		return "", "", "", errors.New("a Data Pump file is named in a directory object of the server, without a path, " +
			"as DATA_PUMP_DIR:shop.dmp or shop.dmp: letters, digits, _, -, . and $ or # only")
	}
	if fileExtension(file) == "" {
		file += ".dmp"
	}
	return directory, file, strings.TrimSuffix(file, fileExtension(file)) + "_" + action + ".log", nil
}

// oracleBackupSchema is the schema a backup is of: the one named, else
// the session's current one.
func oracleBackupSchema(ctx context.Context, d *DB, schema string) (string, error) {
	if schema == "" {
		var err error
		if schema, err = d.Dialect.CurrentSchema(ctx, d.SQL); err != nil {
			return "", err
		}
	}
	if strings.ContainsAny(schema, `"'`) {
		return "", fmt.Errorf("Data Pump cannot be given the schema %s, whose name holds a quote", schema)
	}
	return schema, nil
}

// PlanBackup exports a schema with expdp into a dump file on the server,
// which a file of the name is replaced by: its schema and rows, or one
// of them.
func (oracleEngine) PlanBackup(ctx context.Context, d *DB, o BackupOptions) (ToolRun, error) {
	if err := backupOptionsError(Oracle, o); err != nil {
		return ToolRun{}, err
	}
	schema, err := oracleBackupSchema(ctx, d, o.Database)
	if err != nil {
		return ToolRun{}, err
	}
	directory, file, log, err := oracleDumpFile(o.Path, "export")
	if err != nil {
		return ToolRun{}, err
	}
	content := map[BackupContent]string{BackupAll: "ALL", BackupSchemaOnly: "METADATA_ONLY", BackupDataOnly: "DATA_ONLY"}[o.Content]
	host, port := d.Address()
	run, err := dataPumpRun(d.Config, host, port, "expdp", schema, directory, file, log, "CONTENT="+content, "REUSE_DUMPFILES=YES")
	run.Warning = "expdp writes " + file + " in the server's directory " + directory + ", replacing a file of that name there."
	return run, err
}

// PlanRestore imports a dump file on the server into a schema with
// impdp: the dump's objects of that schema only, a table it holds
// replacing the schema's of the name. clean is not asked: tables are
// always replaced.
func (oracleEngine) PlanRestore(ctx context.Context, d *DB, database, path string, _ bool) (ToolRun, error) {
	if strings.TrimSpace(path) == "" {
		return ToolRun{}, errors.New("name the dump file on the server")
	}
	schema, err := oracleBackupSchema(ctx, d, database)
	if err != nil {
		return ToolRun{}, err
	}
	directory, file, log, err := oracleDumpFile(path, "import")
	if err != nil {
		return ToolRun{}, err
	}
	host, port := d.Address()
	// The schema is there to import into: its user is not made again.
	return dataPumpRun(d.Config, host, port, "impdp", schema, directory, file, log, "TABLE_EXISTS_ACTION=REPLACE", "EXCLUDE=USER")
}

// dataPumpRun is a Data Pump tool's run on a schema of the server cfg
// names, reached at host and port: its user and password in its
// parameter file, the rest on its command line.
func dataPumpRun(cfg Config, host string, port int, tool, schema, directory, file, log string, more ...string) (ToolRun, error) {
	userid, err := oracleUserID(cfg, host, port)
	if err != nil {
		return ToolRun{}, err
	}
	argv := append([]string{tool, `SCHEMAS='"` + schema + `"'`, "DIRECTORY=" + directory, "DUMPFILE=" + file, "LOGFILE=" + log}, more...)
	return ToolRun{Argv: argv, ArgsFile: "USERID=" + userid + "\n", ArgsFilePrefix: "parfile="}, nil
}

// oracleUserID is Data Pump's USERID of a connection, in single quotes
// as its parameter file reads one: the user, its password quoted, and a
// connect descriptor of the address it reaches the server at, over TCPS
// with the connection's wallet where it uses TLS.
func oracleUserID(cfg Config, host string, port int) (string, error) {
	if cfg.User == "" || cfg.Password == "" {
		return "", errors.New("Data Pump logs in with the connection's user and password, which it does not have")
	}
	user := cfg.User
	if !oracleUnquotedName.MatchString(user) {
		user = `"` + user + `"`
	}
	protocol, security := "TCP", ""
	switch cfg.TLS {
	case TLSRequire, TLSVerifyFull:
		if cfg.Oracle.Wallet == "" {
			return "", errors.New("Data Pump reaches a server over TLS with an Oracle wallet: set the connection's wallet")
		}
		match := "NO"
		if cfg.TLS == TLSVerifyFull {
			match = "YES"
		}
		protocol, security = "TCPS", "(SECURITY=(MY_WALLET_DIRECTORY="+ExpandPath(cfg.Oracle.Wallet)+")(SSL_SERVER_DN_MATCH="+match+"))"
	}
	data := "(SERVICE_NAME=" + cfg.Database + ")"
	if cfg.Oracle.SID {
		data = "(SID=" + cfg.Database + ")"
	}
	userid := user + `/"` + strings.ReplaceAll(cfg.Password, "'", "''") + `"@(DESCRIPTION=(ADDRESS=(PROTOCOL=` + protocol + `)(HOST=` + host +
		`)(PORT=` + strconv.Itoa(port) + `))(CONNECT_DATA=` + data + `)` + security + `)`
	if strings.Contains(cfg.Password, `"`) || strings.ContainsAny(cfg.User, `"'`) ||
		strings.ContainsAny(host+cfg.Database+cfg.Oracle.Wallet, `()='"`) || strings.ContainsAny(userid, "\r\n\x00") {
		return "", errors.New(`Data Pump cannot be given a user, password, host, service or wallet holding a quote, a parenthesis, = or a line break ` +
			`in the parameter file DGopher passes it, never its command line`)
	}
	return "'" + userid + "'", nil
}

func (oracleEngine) BackupNote(restore bool) string {
	return backupNoteFor(restore,
		"impdp, Oracle's Data Pump client, which must be installed, imports a dump file on the server, as DATA_PUMP_DIR:shop.dmp or shop.dmp, "+
			"into this schema: the dump's objects of this schema only, a table it holds replacing this schema's of the name, "+
			"other objects already there reported as errors. A dump of another schema imports nothing. "+
			"It needs READ and WRITE on the directory, and DATAPUMP_IMP_FULL_DATABASE for another user's schema.",
		"expdp, Oracle's Data Pump client, which must be installed, exports this schema into a dump file on the server, "+
			"in a directory object, as DATA_PUMP_DIR:shop.dmp or shop.dmp, where it stays, with its log. "+
			"It needs READ and WRITE on the directory, and DATAPUMP_EXP_FULL_DATABASE for another user's schema.")
}

// BackupChoices: a backup keeps the schema, the rows or both; every
// restore is planned, and keeps what it imported when it fails.
func (oracleEngine) BackupChoices() BackupChoices {
	return BackupChoices{Content: true, RestoresByPlan: true, RestoreKeepsPart: true, RestoreOnServer: true}
}

func (oracleEngine) BackupScope() BackupScope        { return BackupScopeSchema }
func (oracleEngine) BackupToolChecksCAOnly(*DB) bool { return false }
