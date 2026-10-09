package db

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
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

// ToolRun is how a backup or a restore runs: a tool's command, or a
// statement on the connection.
type ToolRun struct {
	Argv []string
	// Env is added to the tool's environment: passwords go there, or in
	// MySQLDefaults, never on its command line.
	Env []string
	// MySQLDefaults, when set, is an option file the tool reads first,
	// written for it alone.
	MySQLDefaults string
	Statement     string
}

// Shown is the run as the app shows and audits it, without its secrets.
func (r ToolRun) Shown() string {
	if r.Statement != "" {
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
		run.Argv = append(run.Argv, "--databases", database)
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
// a pg_dump archive with pg_restore, DuckDB's folder with IMPORT
// DATABASE. clean drops what the archive makes before making it. A SQL
// file restores with Run SQL File instead.
func PlanRestore(ctx context.Context, d *DB, database, path string, clean bool) (ToolRun, error) {
	switch d.Config.Engine {
	case DuckDB:
		return ToolRun{Statement: "IMPORT DATABASE " + Literal(DuckDB, path)}, nil
	case Postgres:
		run, err := postgresTool(ctx, d, "pg_restore", database)
		if err != nil {
			return run, err
		}
		run.Argv = append(run.Argv, "--verbose", "--single-transaction", "--exit-on-error")
		if clean {
			run.Argv = append(run.Argv, "--clean", "--if-exists")
		}
		run.Argv = append(run.Argv, path)
		return run, nil
	}
	return ToolRun{}, fmt.Errorf("%s restores a SQL file with Run SQL File", d.Config.Engine.Label())
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

// postgresTool is a PostgreSQL tool's command reaching a database of the
// connection as the app does: through its tunnel or proxy, by libpq's
// hostaddr, while TLS checks the server's own name.
func postgresTool(ctx context.Context, d *DB, tool, database string) (ToolRun, error) {
	cfg := d.Config
	if database == "" {
		database = cfg.Database
	}
	pw, err := password(ctx, cfg)
	if err != nil {
		return ToolRun{}, err
	}
	host, port := d.Address()
	run := ToolRun{Argv: []string{tool, "--host", cfg.Host, "--port", strconv.Itoa(port), "--username", cfg.User, "--dbname", database, "--no-password"}}
	run.Env = append(run.Env, "PGAPPNAME="+appName)
	if host != cfg.Host {
		run.Env = append(run.Env, "PGHOSTADDR="+host)
	}
	if pw != "" {
		run.Env = append(run.Env, "PGPASSWORD="+pw)
	}
	mode := map[TLSMode]string{"": "disable", TLSDisable: "disable", TLSPrefer: "prefer", TLSRequire: "require", TLSVerifyFull: "verify-full"}[cfg.TLS]
	run.Env = append(run.Env, "PGSSLMODE="+mode)
	if cfg.CAFile != "" {
		run.Env = append(run.Env, "PGSSLROOTCERT="+ExpandPath(cfg.CAFile))
	}
	if cfg.CertFile != "" {
		run.Env = append(run.Env, "PGSSLCERT="+ExpandPath(cfg.CertFile), "PGSSLKEY="+ExpandPath(cfg.KeyFile))
	}
	return run, nil
}

// mysqlTool is a MySQL tool's command reaching the connection's server as
// the app does; its password goes in an option file for it alone.
func mysqlTool(ctx context.Context, d *DB, tool string) (ToolRun, error) {
	cfg := d.Config
	pw, err := password(ctx, cfg)
	if err != nil {
		return ToolRun{}, err
	}
	host, port := d.Address()
	run := ToolRun{Argv: []string{tool, "--host=" + host, "--port=" + strconv.Itoa(port), "--protocol=TCP", "--user=" + cfg.User}}
	tunneled := host != cfg.Host
	switch cfg.TLS {
	case "", TLSDisable:
		run.Argv = append(run.Argv, "--ssl-mode=DISABLED")
	case TLSPrefer:
		run.Argv = append(run.Argv, "--ssl-mode=PREFERRED")
	case TLSRequire:
		run.Argv = append(run.Argv, "--ssl-mode=REQUIRED")
	case TLSVerifyFull:
		// Through a tunnel the server's name is not the address dialed:
		// its certificate is checked against the CA only.
		if tunneled {
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
		// MySQL reads it only as the first option.
		args = append([]string{"--defaults-extra-file=" + f.Name()}, args...)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), run.Env...)
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
