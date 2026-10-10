package db

import (
	"bufio"
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
	}
	return string(f)
}

// BackupFormats are the forms an engine's databases are backed up in, the
// one the app offers first first; none for ClickHouse, whose BACKUP runs
// on the server.
func BackupFormats(e Engine) []BackupFormat {
	switch e {
	case Postgres:
		return []BackupFormat{BackupArchive, BackupSQL}
	case MySQL:
		return []BackupFormat{BackupSQL}
	case SQLite:
		return []BackupFormat{BackupCopy}
	case DuckDB:
		return []BackupFormat{BackupParquet}
	}
	return nil
}

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

// ToolRun is how a backup or a restore runs: a tool's command, a
// statement on the connection, or a DuckDB restore (RestoreDuckDB).
type ToolRun struct {
	Argv []string
	// Env is added to the tool's environment: passwords go there, or in
	// MySQLDefaults, never on its command line.
	Env []string
	// MySQLDefaults, when set, is an option file the tool reads first,
	// written for it alone.
	MySQLDefaults string
	Statement     string
	// Import is a DuckDB backup folder, which RestoreDuckDB imports into
	// Stage, a database of its own, before Statements copy what it made
	// into the connection's.
	Import     string
	Stage      string
	Statements []string
}

// Shown is the run as the app shows and audits it, without its secrets.
func (r ToolRun) Shown() string {
	switch {
	case len(r.Statements) > 0:
		return strings.Join(r.Statements, ";\n")
	case r.Statement != "":
		return r.Statement
	}
	return strings.Join(r.Argv, " ")
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
	cfg := d.Config
	if !slices.Contains(BackupFormats(cfg.Engine), o.Format) {
		return ToolRun{}, fmt.Errorf("%s backs up as %v", cfg.Engine.Label(), BackupFormats(cfg.Engine))
	}
	if strings.TrimSpace(o.Path) == "" {
		return ToolRun{}, errors.New("choose where the backup goes")
	}
	switch cfg.Engine {
	case SQLite:
		return ToolRun{Statement: "VACUUM INTO " + Literal(SQLite, o.Path)}, nil
	case DuckDB:
		return ToolRun{Statement: "EXPORT DATABASE " + Literal(DuckDB, o.Path) + " (FORMAT parquet)"}, nil
	case Postgres:
		run, err := postgresTool(ctx, d, "pg_dump", o.Database)
		if err != nil {
			return run, err
		}
		format := "custom"
		if o.Format == BackupSQL {
			format = "plain"
		}
		run.Argv = append(run.Argv, "--format", format, "--file", o.Path, "--verbose")
		run.Argv = append(run.Argv, contentFlag(o.Content, "--schema-only", "--data-only")...)
		return run, nil
	case MySQL:
		run, err := mysqlTool(ctx, d, "mysqldump")
		if err != nil {
			return run, err
		}
		database := o.Database
		if database == "" {
			database = cfg.Database
		}
		if database == "" {
			return ToolRun{}, errors.New("choose the database to back up")
		}
		run.Argv = append(run.Argv, "--single-transaction", "--routines", "--triggers", "--events", "--verbose", "--result-file="+o.Path)
		run.Argv = append(run.Argv, contentFlag(o.Content, "--no-data", "--no-create-info")...)
		// After --, a schema named as an option, as -Ar/tmp/x.sql, is a
		// name.
		run.Argv = append(run.Argv, "--databases", "--", database)
		return run, nil
	}
	return ToolRun{}, fmt.Errorf("%s backs up on its server: use BACKUP there", cfg.Engine.Label())
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
	switch d.Config.Engine {
	case DuckDB:
		return planDuckDBRestore(ctx, d, path)
	case Postgres:
		run, err := postgresTool(ctx, d, "pg_restore", database)
		if err != nil {
			return run, err
		}
		run.Argv = append(run.Argv, "--verbose", "--single-transaction", "--exit-on-error")
		if clean {
			run.Argv = append(run.Argv, "--clean", "--if-exists")
		}
		// After --, an archive named as an option is the archive.
		run.Argv = append(run.Argv, "--", path)
		return run, nil
	}
	return ToolRun{}, fmt.Errorf("%s restores a SQL file with Run SQL File", d.Config.Engine.Label())
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
	if roots != "" {
		run.Env = append(run.Env, "PGSSLROOTCERT="+roots)
	}
	if cfg.CertFile != "" {
		run.Env = append(run.Env, "PGSSLCERT="+ExpandPath(cfg.CertFile), "PGSSLKEY="+ExpandPath(cfg.KeyFile))
	}
	return run, nil
}

var pgVersionPattern = regexp.MustCompile(`\(PostgreSQL\) (\d+)`)

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
	host, _ := d.Address()
	return d.Config.Engine == MySQL && d.Config.TLS == TLSVerifyFull && host != d.Config.Host
}

// mysqlTool is a MySQL tool's command reaching the connection's server as
// the app does; its password goes in an option file for it alone.
func mysqlTool(ctx context.Context, d *DB, tool string) (ToolRun, error) {
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
	switch cfg.TLS {
	case "", TLSDisable:
		run.Argv = append(run.Argv, "--ssl-mode=DISABLED")
	case TLSPrefer:
		run.Argv = append(run.Argv, "--ssl-mode=PREFERRED")
	case TLSRequire:
		run.Argv = append(run.Argv, "--ssl-mode=REQUIRED")
	case TLSVerifyFull:
		if MysqldumpChecksCAOnly(d) {
			run.Argv = append(run.Argv, "--ssl-mode=VERIFY_CA")
		} else {
			run.Argv = append(run.Argv, "--ssl-mode=VERIFY_IDENTITY")
		}
	}
	if cfg.CAFile != "" {
		run.Argv = append(run.Argv, "--ssl-ca="+ExpandPath(cfg.CAFile))
	}
	if cfg.CertFile != "" {
		run.Argv = append(run.Argv, "--ssl-cert="+ExpandPath(cfg.CertFile), "--ssl-key="+ExpandPath(cfg.KeyFile))
	}
	if cfg.ClearTextPassword || cfg.Identity != "" {
		run.Argv = append(run.Argv, "--enable-cleartext-plugin")
	}
	if pw != "" {
		// An option file's value is quoted, its quotes and backslashes
		// escaped.
		run.MySQLDefaults = "[client]\npassword=\"" + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(pw) + "\"\n"
	}
	return run, nil
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
		f, err := os.CreateTemp("", "dgopher-*.cnf")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		if err := f.Chmod(0o600); err == nil {
			_, err = f.WriteString(run.MySQLDefaults)
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		// MySQL reads it only as the first option; it alone, not the
		// user's ~/.my.cnf, whose password is for some other server.
		args = append([]string{"--defaults-file=" + f.Name()}, args...)
	} else if strings.HasPrefix(filepath.Base(run.Argv[0]), "mysql") {
		args = append([]string{"--no-defaults"}, args...)
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
