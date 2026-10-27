package db

import (
	"bufio"
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/duckdb/duckdb-go/v2"
	"github.com/jackc/pgx/v5/pgconn"
	mssql "github.com/microsoft/go-mssqldb"
)

// BackupFormat is the form a backup is written in.
type BackupFormat string

const (
	// BackupSQL is the statements making the database again, in a file,
	// which Run SQL File restores.
	BackupSQL BackupFormat = "sql"
	// BackupArchive is PostgreSQL's custom format, which pg_restore
	// restores, all or in part.
	BackupArchive BackupFormat = "archive"
	// BackupCopy is a copy of a SQLite database, consistent as of when it
	// was made.
	BackupCopy BackupFormat = "copy"
	// BackupParquet is a folder of a DuckDB database's schema and its
	// tables' rows in Parquet files, which IMPORT DATABASE restores.
	BackupParquet BackupFormat = "parquet"
	// BackupBacpac is a SQL Server database's schema and rows in a BACPAC
	// file, which sqlpackage writes and imports.
	BackupBacpac BackupFormat = "bacpac"
	// BackupServerFile is SQL Server's BACKUP DATABASE into a file on the
	// server's disk, which RESTORE DATABASE restores.
	BackupServerFile BackupFormat = "server"
	// BackupDataPump is an Oracle schema in a dump file on the server,
	// which Data Pump's expdp writes and impdp imports.
	BackupDataPump BackupFormat = "datapump"
)

// Label names a format.
func (f BackupFormat) Label() string {
	switch f {
	case BackupSQL:
		return "SQL file"
	case BackupArchive:
		return "pg_dump archive"
	case BackupCopy:
		return "Copy of the database"
	case BackupParquet:
		return "Folder of Parquet files"
	case BackupBacpac:
		return "BACPAC file"
	case BackupServerFile:
		return "Backup file on the server"
	case BackupDataPump:
		return "Data Pump file on the server"
	}
	return string(f)
}

// OnServer reports whether a format's file is on the server's disk, at a
// path the server reads, which this computer cannot choose.
func (f BackupFormat) OnServer() bool { return f == BackupServerFile || f == BackupDataPump }

// BackupContent is what of a database a backup keeps.
type BackupContent int

const (
	BackupAll BackupContent = iota
	BackupSchemaOnly
	BackupDataOnly
)

// BackupOptions say what a backup writes, and where.
type BackupOptions struct {
	Format   BackupFormat
	Content  BackupContent
	Database string // PostgreSQL's database, or MySQL's schema; "" for the connection's
	Path     string // the file, or DuckDB's folder
}

// ToolRun is how a backup or a restore runs (RunPlanned): a tool's
// command, a statement on the connection, or a DuckDB restore
// (RestoreDuckDB), between steps of its own.
type ToolRun struct {
	Argv []string
	// Env is added to the tool's environment: passwords go there, or in
	// MySQLDefaults, never on its command line.
	Env []string
	// MySQLDefaults, when set, is an option file the tool reads first,
	// written for it alone.
	MySQLDefaults string
	// ArgsFile, when set, holds arguments, one a line, in a file written
	// for the tool alone, named after the others behind ArgsFilePrefix:
	// sqlpackage's connection, with its password.
	ArgsFile string
	// ArgsFilePrefix is how the tool is told the file's name: "@" when
	// empty, as sqlpackage reads it; "parfile=" for Oracle's Data Pump.
	ArgsFilePrefix string
	Statement      string
	// StatementDatabase is the database Statement runs on, "" for the one
	// the run backs up or restores into.
	StatementDatabase string
	// Import is a DuckDB backup folder, which RestoreDuckDB imports into
	// Stage, a database of its own, before Statements copy what it made
	// into the connection's.
	Import     string
	Stage      string
	Statements []string
	// Before and After run before the run and after it, After even when
	// Before or the run fails, unless the server refused the first Before
	// step for want of a privilege, which changed nothing. Undo runs when
	// an After step fails, to undo what Before did another way.
	Before, After, Undo []RunStep
	// Warning, when set, is asked about before the run.
	Warning string
}

// RunStep is a statement a run runs before or after its work, on a
// connection of its own, closed after it.
type RunStep struct {
	SQL string
	// Database is the database it runs on; "" for the one the run backs
	// up or restores into.
	Database string
	// Needs says what the step needs that a user may lack, told when the
	// server refuses it for want of a privilege.
	Needs string
}

// Shown is the run as the app shows and audits it, without its secrets.
func (r ToolRun) Shown() string {
	run := strings.Join(r.Argv, " ")
	switch {
	case len(r.Statements) > 0:
		run = strings.Join(r.Statements, ";\n")
	case r.Statement != "":
		run = r.Statement
	}
	return strings.Join(append(append(stepsSQL(r.Before), run), stepsSQL(r.After)...), ";\n")
}

// stepsSQL are the statements of steps.
func stepsSQL(steps []RunStep) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.SQL
	}
	return out
}

// Address is where a tool reaches the server: the local port of its SSH
// tunnel or proxy, else its host and port.
func (d *DB) Address() (string, int) {
	if d.route != nil {
		return "127.0.0.1", d.route.LocalPort()
	}
	return d.Config.Host, d.Config.port()
}

// password is what a tool logs in with: the connection's password, or its
// cloud identity's token.
func password(ctx context.Context, cfg Config) (string, error) {
	if cfg.Identity != "" {
		return identityToken(ctx, cfg)
	}
	return cfg.Password, nil
}

// PlanBackup is how the backup of a database of a connection runs.
func PlanBackup(ctx context.Context, d *DB, o BackupOptions) (ToolRun, error) {
	if r, ok := SQL(d.Config.Engine); ok {
		return r.PlanBackup(ctx, d, o)
	}
	return ToolRun{}, backupOptionsError(d.Config.Engine, o)
}

// backupOptionsError says what of a backup's options the engine cannot
// take.
func backupOptionsError(e Engine, o BackupOptions) error {
	var formats []BackupFormat
	if r, ok := SQL(e); ok {
		formats = r.BackupFormats()
	}
	if !slices.Contains(formats, o.Format) {
		return fmt.Errorf("%s backs up as %v", e.Label(), formats)
	}
	if strings.TrimSpace(o.Path) == "" {
		return errors.New("choose where the backup goes")
	}
	return nil
}

func contentFlag(c BackupContent, schemaOnly, dataOnly string) []string {
	switch c {
	case BackupSchemaOnly:
		return []string{schemaOnly}
	case BackupDataOnly:
		return []string{dataOnly}
	}
	return nil
}

// PlanRestore is how a backup restores into a database of a connection:
// a pg_dump archive with pg_restore, DuckDB's folder with RestoreDuckDB.
// clean drops what the archive makes before making it. A SQL file
// restores with Run SQL File instead.
func PlanRestore(ctx context.Context, d *DB, database, path string, clean bool) (ToolRun, error) {
	if r, ok := SQL(d.Config.Engine); ok {
		return r.PlanRestore(ctx, d, database, path, clean)
	}
	return ToolRun{}, restoreWithRunSQLFileError(d.Config.Engine)
}

// restoreWithRunSQLFileError is PlanRestore's error for an engine whose
// backups are SQL files.
func restoreWithRunSQLFileError(e Engine) error {
	return fmt.Errorf("%s restores a SQL file with Run SQL File", e.Label())
}

// ErrRestoreInTransaction refuses a DuckDB restore while a transaction is
// open on the connection, which all its editors share: the restore would
// run inside it, to be committed or rolled back with their work.
var ErrRestoreInTransaction = errors.New("a transaction is open on the connection, which its editors share: commit it or roll it back, then restore")

// planDuckDBRestore plans the restore of a DuckDB backup folder. IMPORT
// DATABASE runs the folder's schema.sql as it is, and a folder from
// elsewhere may hold COPY … TO or ATTACH, which write any file the user
// can: so RestoreDuckDB imports the folder into a database of its own,
// which reaches no file outside the folder, and the connection then
// copies what that made into its current database.
func planDuckDBRestore(ctx context.Context, d *DB, folder string) (ToolRun, error) {
	if d.sharedTxOpen() {
		return ToolRun{}, ErrRestoreInTransaction
	}
	if strings.TrimSpace(folder) == "" {
		return ToolRun{}, errors.New("choose the backup's folder")
	}
	folder, err := filepath.Abs(ExpandPath(folder))
	if err != nil {
		return ToolRun{}, err
	}
	var target string
	if err := d.SQL.QueryRowContext(ctx, "SELECT current_database()").Scan(&target); err != nil {
		return ToolRun{}, err
	}
	token := make([]byte, 8)
	rand.Read(token)
	name := "dgopher_restore_" + hex.EncodeToString(token)
	stage := filepath.Join(os.TempDir(), name, "stage.duckdb")
	q := DialectOf(DuckDB).Quote
	return ToolRun{Import: folder, Stage: stage, Statements: []string{
		"ATTACH " + Literal(DuckDB, stage) + " AS " + q(name) + " (READ_ONLY)",
		"COPY FROM DATABASE " + q(name) + " TO " + q(target),
		"DETACH " + q(name),
	}}, nil
}

// sharedTxOpen reports whether a transaction is open on a pool of one
// connection, as the sessions sharing it last saw.
func (d *DB) sharedTxOpen() bool {
	state, _ := d.SharedTx()
	return state != TxNone
}

// ErrBackupInTransaction refuses a DuckDB backup while a transaction is
// open on the connection, which all its editors share: EXPORT DATABASE
// would write its uncommitted rows, and a failed one can abort it.
var ErrBackupInTransaction = errors.New("a transaction is open on the connection, which its editors share: commit it or roll it back, then back up")

// BackupDuckDB runs the statement of a DuckDB backup PlanBackup planned,
// refused while a transaction is open (ErrBackupInTransaction). Holding
// the one connection, no editor's statement comes between the check and
// the export.
func BackupDuckDB(ctx context.Context, d *DB, statement string) error {
	if d.sharedTxOpen() {
		return ErrBackupInTransaction
	}
	conn, err := d.SQL.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if state, ok := readTxState(ctx, conn); d.sharedTxOpen() || ok && state != TxNone {
		return ErrBackupInTransaction
	}
	_, err = conn.ExecContext(ctx, statement)
	return err
}

// RestoreDuckDB runs a DuckDB restore PlanRestore planned: it imports the
// backup folder into run.Stage, in a DuckDB of its own that reaches no file
// outside the folder, then runs run.Statements on the connection, which
// attach the stage, copy what it holds and detach it. The stage is removed
// however the restore ends.
func RestoreDuckDB(ctx context.Context, d *DB, run ToolRun) error {
	if run.Import == "" || run.Stage == "" || len(run.Statements) != 3 {
		return errors.New("not a planned DuckDB restore")
	}
	if d.sharedTxOpen() {
		return ErrRestoreInTransaction
	}
	dir := filepath.Dir(run.Stage)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := importSandboxed(ctx, run.Import, run.Stage); err != nil {
		return err
	}
	// Holding the one connection, no editor's statement comes between
	// the check and the copy. The copy attaches outside any transaction:
	// inside one, COPY FROM DATABASE copies nothing.
	conn, err := d.SQL.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if state, ok := readTxState(ctx, conn); d.sharedTxOpen() || ok && state != TxNone {
		return ErrRestoreInTransaction
	}
	attach, copyAll, detach := run.Statements[0], run.Statements[1], run.Statements[2]
	if _, err := conn.ExecContext(ctx, attach); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, copyAll)
	// Detached however the copy ended, even cancelled, for the stage to
	// be removed.
	detachCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, derr := conn.ExecContext(detachCtx, detach); err == nil {
		err = derr
	}
	return err
}

// importSandboxed imports a DuckDB backup folder into a new database at
// stage, in a DuckDB that reaches only the folder: its schema.sql runs as
// it is.
func importSandboxed(ctx context.Context, folder, stage string) error {
	sandbox, err := sql.Open("duckdb", stage)
	if err != nil {
		return err
	}
	defer sandbox.Close()
	conn, err := sandbox.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// In this order: the allowed directories are set while external access
	// is on, and the locked configuration keeps schema.sql from changing
	// either.
	for _, q := range []string{
		"SET allowed_directories = [" + Literal(DuckDB, folder) + "]",
		"SET enable_external_access = false",
		"SET lock_configuration = true",
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, "IMPORT DATABASE "+Literal(DuckDB, folder)); err != nil {
		var de *duckdb.Error
		if errors.As(err, &de) && de.Type == duckdb.ErrorTypePermission {
			return fmt.Errorf("the backup's schema.sql reaches a file outside its folder, which a restore does not allow; a view reading a file elsewhere cannot be restored: remove the view from schema.sql, restore, then make it again (%w)", err)
		}
		return err
	}
	// Closed before the connection attaches the stage: one file open in
	// two DuckDBs of one process is not safe.
	conn.Close()
	return sandbox.Close()
}

// IsArchive reports whether a file is a pg_dump archive, which pg_restore
// reads, rather than SQL.
func IsArchive(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 5)
	n, _ := f.Read(head)
	return string(head[:n]) == "PGDMP"
}

// pgSystemRoots is the first PostgreSQL whose libpq reads
// sslrootcert=system as the system's certificate authorities.
const pgSystemRoots = 16

// postgresTool is a PostgreSQL tool's command reaching a database of the
// connection as the app does: through its tunnel or proxy, by libpq's
// hostaddr, while TLS checks the server's own name.
func postgresTool(ctx context.Context, d *DB, tool, database string) (ToolRun, error) {
	cfg := d.Config
	if database == "" {
		database = cfg.Database
	}
	roots := ""
	switch {
	case cfg.CAFile != "":
		roots = ExpandPath(cfg.CAFile)
	case cfg.TLS == TLSVerifyFull:
		// The system's authorities, as the app trusts, rather than
		// whatever ~/.postgresql/root.crt holds.
		if major, ok := pgToolVersion(ctx, tool); ok && major < pgSystemRoots {
			return ToolRun{}, fmt.Errorf("%s %d cannot check the server's certificate against the system's certificate authorities, as verify-full without a CA file needs: set the connection's CA file, or install %s %d or later", tool, major, tool, pgSystemRoots)
		}
		roots = "system"
	}
	pw, err := password(ctx, cfg)
	if err != nil {
		return ToolRun{}, err
	}
	host, port := d.Address()
	// The name is a value of a connection string, quoted: a bare --dbname
	// holding = or starting postgresql:// is read as a connection string
	// of its own, which can name another server.
	conninfo := "dbname='" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(database) + "'"
	run := ToolRun{Argv: []string{tool, "--host", cfg.Host, "--port", strconv.Itoa(port), "--username", cfg.User, "--dbname", conninfo, "--no-password"}}
	// The connection's password alone, never one ~/.pgpass holds.
	run.Env = append(run.Env, "PGAPPNAME="+appName, "PGPASSFILE="+os.DevNull)
	if host != cfg.Host {
		run.Env = append(run.Env, "PGHOSTADDR="+host)
	}
	if pw != "" {
		run.Env = append(run.Env, "PGPASSWORD="+pw)
	}
	mode := map[TLSMode]string{"": "disable", TLSDisable: "disable", TLSPrefer: "prefer", TLSRequire: "require", TLSVerifyFull: "verify-full"}[cfg.TLS]
	run.Env = append(run.Env, "PGSSLMODE="+mode)
	if cfg.TLS == TLSPrefer || cfg.TLS == TLSRequire {
		// As the app's own connections: libpq 16 and later refuse to send
		// the password as it is; older ones ignore the variable.
		run.Env = append(run.Env, "PGREQUIREAUTH=!password")
	}
	if roots != "" {
		run.Env = append(run.Env, "PGSSLROOTCERT="+roots)
	}
	if cfg.CertFile != "" {
		run.Env = append(run.Env, "PGSSLCERT="+ExpandPath(cfg.CertFile), "PGSSLKEY="+ExpandPath(cfg.KeyFile))
	}
	return run, nil
}

// pgVersionPattern reads a tool's major version: YugabyteDB's ysql_dump
// says its version as YSQL's.
var pgVersionPattern = regexp.MustCompile(`\((?:PostgreSQL|YSQL)\) (\d+)`)

// pgToolVersion is the major version of an installed PostgreSQL tool, as
// its --version says; ok is false when it is not installed or does not
// say.
func pgToolVersion(ctx context.Context, tool string) (major int, ok bool) {
	path, err := exec.LookPath(tool)
	if err != nil {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return 0, false
	}
	m := pgVersionPattern.FindSubmatch(out)
	if m == nil {
		return 0, false
	}
	major, err = strconv.Atoi(string(m[1]))
	return major, err == nil
}

// MysqldumpChecksCAOnly reports whether mysqldump, under verify-full,
// checks the server's certificate against the CA file alone: through an
// SSH tunnel or a proxy the address it dials is not the server's name,
// and it has no option to name the server.
func MysqldumpChecksCAOnly(d *DB) bool {
	r, ok := SQL(d.Config.Engine)
	return ok && r.BackupToolChecksCAOnly(d)
}

// mysqlTool is a MySQL or MariaDB tool's command reaching the
// connection's server as the app does, flags writing how; its password
// goes in an option file for it alone.
func mysqlTool(ctx context.Context, d *DB, tool string, flags func(*DB) []string) (ToolRun, error) {
	cfg := d.Config
	if cfg.TLS == TLSVerifyFull && cfg.CAFile == "" {
		// VERIFY_CA and VERIFY_IDENTITY need --ssl-ca: the MySQL tools
		// never trust the system's authorities.
		return ToolRun{}, fmt.Errorf("%s checks the server's certificate only against a CA file, and the connection names none: set its CA file to back up under verify-full", tool)
	}
	pw, err := password(ctx, cfg)
	if err != nil {
		return ToolRun{}, err
	}
	host, port := d.Address()
	run := ToolRun{Argv: []string{tool, "--host=" + host, "--port=" + strconv.Itoa(port), "--protocol=TCP", "--user=" + cfg.User}}
	run.Argv = append(run.Argv, flags(d)...)
	if pw != "" {
		// An option file's value is quoted, its quotes and backslashes
		// escaped.
		run.MySQLDefaults = "[client]\npassword=\"" + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(pw) + "\"\n"
	}
	return run, nil
}

// mysqlClientFlags are how MySQL's tools connect as the connection does:
// its TLS, certificates and clear-text password.
func mysqlClientFlags(d *DB) []string {
	cfg := d.Config
	var argv []string
	switch cfg.TLS {
	case "", TLSDisable:
		argv = append(argv, "--ssl-mode=DISABLED")
	case TLSPrefer:
		argv = append(argv, "--ssl-mode=PREFERRED")
	case TLSRequire:
		argv = append(argv, "--ssl-mode=REQUIRED")
	case TLSVerifyFull:
		if MysqldumpChecksCAOnly(d) {
			argv = append(argv, "--ssl-mode=VERIFY_CA")
		} else {
			argv = append(argv, "--ssl-mode=VERIFY_IDENTITY")
		}
	}
	argv = append(argv, clientCertificateFlags(cfg)...)
	if cfg.ClearTextPassword || cfg.Identity != "" {
		argv = append(argv, "--enable-cleartext-plugin")
	}
	return argv
}

// clientCertificateFlags name the connection's CA, certificate and key
// files to a MySQL or MariaDB tool.
func clientCertificateFlags(cfg Config) []string {
	var argv []string
	if cfg.CAFile != "" {
		argv = append(argv, "--ssl-ca="+ExpandPath(cfg.CAFile))
	}
	if cfg.CertFile != "" {
		argv = append(argv, "--ssl-cert="+ExpandPath(cfg.CertFile), "--ssl-key="+ExpandPath(cfg.KeyFile))
	}
	return argv
}

// toolEnv is the environment a tool runs in: the app's, without the
// variables the PostgreSQL and MySQL clients read, which would choose
// where it connects and what it sends there, as PGPASSWORD, PGSERVICE or
// MYSQL_PWD; the tool's own are added after.
func toolEnv(environ []string) []string {
	return slices.DeleteFunc(slices.Clone(environ), func(kv string) bool {
		return strings.HasPrefix(kv, "PG") || strings.HasPrefix(kv, "MYSQL_") || strings.HasPrefix(kv, "LIBMYSQL_")
	})
}

// RunTool runs a tool's command, telling each line it writes to its
// standard error, as pg_dump's progress; it stops when ctx ends.
func RunTool(ctx context.Context, run ToolRun, line func(string)) error {
	if len(run.Argv) == 0 {
		return errors.New("no command")
	}
	path, err := exec.LookPath(run.Argv[0])
	if err != nil {
		return fmt.Errorf("%s is not installed, or not on the PATH: install it to back up or restore this database", run.Argv[0])
	}
	args := slices.Clone(run.Argv[1:])
	if run.MySQLDefaults != "" {
		name, err := writePrivateFile("dgopher-*.cnf", run.MySQLDefaults)
		if err != nil {
			return err
		}
		defer os.Remove(name)
		// MySQL reads it only as the first option; it alone, not the
		// user's ~/.my.cnf, whose password is for some other server.
		args = append([]string{"--defaults-file=" + name}, args...)
	} else if base := filepath.Base(run.Argv[0]); strings.HasPrefix(base, "mysql") || strings.HasPrefix(base, "mariadb") {
		args = append([]string{"--no-defaults"}, args...)
	}
	if run.ArgsFile != "" {
		name, err := writePrivateFile("dgopher-*.args", run.ArgsFile)
		if err != nil {
			return err
		}
		defer os.Remove(name)
		args = append(args, cmp.Or(run.ArgsFilePrefix, "@")+name)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(toolEnv(os.Environ()), run.Env...)
	cmd.WaitDelay = 5 * time.Second
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var last []string
	scan := bufio.NewScanner(stderr)
	for scan.Scan() {
		text := scan.Text()
		line(text)
		last = append(last, text)
		if len(last) > 5 {
			last = last[1:]
		}
	}
	err = cmd.Wait()
	switch {
	case ctx.Err() != nil:
		return errors.New("cancelled")
	case err != nil:
		return fmt.Errorf("%s: %w: %s", run.Argv[0], err, strings.Join(last, " "))
	}
	return nil
}

// writePrivateFile writes text into a new temporary file only its owner
// reads, named after pattern, and returns its name.
func writePrivateFile(pattern, text string) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	if err := f.Chmod(0o600); err == nil {
		_, err = f.WriteString(text)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// AfterStepsWait is how long a run's After and Undo steps may take in
// all, which run even when the run was stopped.
const AfterStepsWait = 30 * time.Second

// RunPlanned runs a planned backup or restore: its Before steps, then the
// DuckDB import, the statement or the tool, then its After steps, and Undo
// should one of them fail. pool gives the database the run backs up or
// restores into, asked for only when the run needs it; line is told each
// line the tool writes. After the steps, no connection of the pool opened
// before or during them is used again: they may hold what the steps set.
func RunPlanned(ctx context.Context, run ToolRun, pool func(context.Context) (*DB, error), line func(string)) error {
	var d *DB
	database := func(ctx context.Context) (*DB, error) {
		if d != nil {
			return d, nil
		}
		var err error
		d, err = pool(ctx)
		return d, err
	}
	refused, err := runSteps(ctx, database, run.Before)
	if err == nil {
		err = runPlannedWork(ctx, run, database, line)
	}
	if len(run.After) > 0 && !refused {
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), AfterStepsWait)
		defer cancel()
		_, aerr := runSteps(actx, database, run.After)
		if aerr != nil && len(run.Undo) > 0 {
			undo := strings.Join(stepsSQL(run.Undo), "; ")
			if _, uerr := runSteps(actx, database, run.Undo); uerr != nil {
				aerr = fmt.Errorf("%w; undoing what came before failed too (%w): run %s", aerr, uerr, undo)
			} else {
				aerr = fmt.Errorf("%w; DGopher ran %s instead", aerr, undo)
			}
		}
		err = errors.Join(err, aerr)
	}
	if d != nil && len(run.Before)+len(run.After) > 0 {
		d.RetireConnections()
	}
	return err
}

// runPlannedWork runs what a plan runs between its steps.
func runPlannedWork(ctx context.Context, run ToolRun, database func(context.Context) (*DB, error), line func(string)) error {
	if run.Import == "" && run.Statement == "" {
		return RunTool(ctx, run, line)
	}
	d, err := database(ctx)
	if err == nil && run.StatementDatabase != "" {
		d, err = d.Database(ctx, run.StatementDatabase)
	}
	switch {
	case err != nil:
		return err
	case run.Import != "":
		return RestoreDuckDB(ctx, d, run)
	}
	if r, ok := SQL(d.Config.Engine); ok && r.BackupChoices().Folder {
		return BackupDuckDB(ctx, d, run.Statement)
	}
	_, err = d.SQL.ExecContext(ctx, run.Statement)
	return err
}

// runSteps runs steps in order, each on a connection of its own to its
// database, closed after, up to the first that fails; refused reports that
// the server refused the first for want of a privilege, which then
// changed nothing.
func runSteps(ctx context.Context, database func(context.Context) (*DB, error), steps []RunStep) (refused bool, err error) {
	for i, step := range steps {
		var d *DB
		if d, err = database(ctx); err == nil {
			d, err = d.Database(ctx, step.Database)
		}
		if err == nil {
			err = runStep(ctx, d, step.SQL)
		}
		if err == nil {
			continue
		}
		if !privilegeRefused(err) {
			return false, err
		}
		if step.Needs != "" {
			err = fmt.Errorf("%s (%w)", step.Needs, err)
		}
		return i == 0, err
	}
	return false, nil
}

// runStep runs a statement on a connection of d's own, closed after it
// rather than given back to the pool, with what the statement set; on a
// pool of one connection, on that one.
func runStep(ctx context.Context, d *DB, statement string) error {
	if d.single {
		_, err := d.SQL.ExecContext(ctx, statement)
		return err
	}
	conn, err := d.SQL.Conn(ctx)
	if err != nil {
		return err
	}
	defer discard(conn)
	_, err = conn.ExecContext(ctx, statement)
	return err
}

// privilegeRefused reports whether the server refused a statement for
// want of a privilege.
func privilegeRefused(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return pg.Code == "42501"
	}
	var ms mssql.Error
	if errors.As(err, &ms) {
		// Permission denied on an object (229, 230), in a database (262),
		// to alter one (5011) or to restore one (3110).
		return slices.Contains([]int32{229, 230, 262, 3110, 5011}, ms.Number)
	}
	return false
}
