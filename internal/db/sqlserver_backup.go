package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// SQL Server's backups: a BACPAC file on this computer, which sqlpackage
// exports and imports, and BACKUP DATABASE's file on the server's disk,
// which RESTORE DATABASE restores over a database.

func (sqlserverEngine) BackupFormats() []BackupFormat {
	return []BackupFormat{BackupBacpac, BackupServerFile}
}

func (sqlserverEngine) PlanBackup(ctx context.Context, d *DB, o BackupOptions) (ToolRun, error) {
	if err := backupOptionsError(SQLServer, o); err != nil {
		return ToolRun{}, err
	}
	database, err := sqlserverDatabase(ctx, d, o.Database)
	if err != nil {
		return ToolRun{}, err
	}
	if o.Format == BackupBacpac {
		return sqlpackagePlan(ctx, d, "Export", ExpandPath(o.Path), database)
	}
	path, err := sqlserverBackupPath(ctx, d, o.Path)
	if err != nil {
		return ToolRun{}, err
	}
	return ToolRun{Statement: sqlserverBackupSQL(database, path),
		Warning: "BACKUP DATABASE replaces the backups in " + path + " on the server, if the file is there."}, nil
}

// sqlserverBackupSQL backs a database up into a file on the server: a
// copy-only full backup, which leaves the server's own chain of backups
// as it was, replacing what the file held, with its pages' checksums
// checked.
func sqlserverBackupSQL(database, path string) string {
	return "BACKUP DATABASE " + sqlserverDialect{}.Quote(database) + " TO DISK = " + sqlserverText(path) + " WITH COPY_ONLY, INIT, CHECKSUM"
}

// PlanRestore imports a .bacpac file with sqlpackage; any other path is a
// backup file on the server, which replaces the database. clean is not
// asked: a restore always replaces the whole database.
func (sqlserverEngine) PlanRestore(ctx context.Context, d *DB, database, path string, _ bool) (ToolRun, error) {
	if strings.TrimSpace(path) == "" {
		return ToolRun{}, errors.New("choose the backup")
	}
	database, err := sqlserverDatabase(ctx, d, database)
	if err != nil {
		return ToolRun{}, err
	}
	if strings.EqualFold(fileExtension(path), ".bacpac") {
		return sqlpackagePlan(ctx, d, "Import", ExpandPath(path), database)
	}
	path, err = sqlserverBackupPath(ctx, d, path)
	if err != nil {
		return ToolRun{}, err
	}
	backup, err := sqlserverBackupFiles(ctx, d, path)
	if err != nil {
		return ToolRun{}, err
	}
	existing, err := scanAll(ctx, d.SQL, `SELECT name, CASE type WHEN 0 THEN 'D' WHEN 1 THEN 'L' WHEN 2 THEN 'S' ELSE 'F' END, physical_name
FROM sys.master_files WHERE database_id = DB_ID(@p1) ORDER BY file_id`, []any{database}, func(scan func(...any) error) (sqlserverFile, error) {
		var f sqlserverFile
		return f, scan(&f.logical, &f.kind, &f.physical)
	})
	if err != nil {
		return ToolRun{}, err
	}
	var dataDir, logDir string
	err = d.SQL.QueryRowContext(ctx, `SELECT CAST(SERVERPROPERTY('InstanceDefaultDataPath') AS nvarchar(4000)),
  CAST(SERVERPROPERTY('InstanceDefaultLogPath') AS nvarchar(4000))`).Scan(&dataDir, &logDir)
	if err != nil {
		return ToolRun{}, err
	}
	return sqlserverRestoreRun(database, path, sqlserverMoves(database, backup, existing, dataDir, logDir)), nil
}

// fileExtension is a path's extension, in the separators of either
// system.
func fileExtension(path string) string {
	base := path[strings.LastIndexAny(path, `/\`)+1:]
	if i := strings.LastIndexByte(base, '.'); i >= 0 {
		return base[i:]
	}
	return ""
}

// sqlserverRestoreRun restores a backup file over a database on master,
// in one batch: the database is taken offline, which ends its other
// sessions, rolling back their transactions, and keeps new ones out until
// the restore brings it online; a database's single-user slot would be
// free between the two for another session to take. After, a database the
// restore failed to replace is brought back online, and one it left
// restoring is an error saying so.
func sqlserverRestoreRun(database, path string, moves []string) ToolRun {
	name, quoted := sqlserverText(database), sqlserverDialect{}.Quote(database)
	restore := "IF DB_ID(" + name + ") IS NOT NULL ALTER DATABASE " + quoted + " SET OFFLINE WITH ROLLBACK IMMEDIATE;\n" +
		"IF @@ERROR = 0 RESTORE DATABASE " + quoted + " FROM DISK = " + sqlserverText(path) + " WITH REPLACE"
	if len(moves) > 0 {
		restore += ", " + strings.Join(moves, ", ")
	}
	state := "SELECT 1 FROM sys.databases WHERE name = " + name + " AND state_desc = "
	left := sqlserverText(database + " is left restoring by the failed restore, and cannot be used: restore a backup over it again, or drop it")
	return ToolRun{
		Statement: restore, StatementDatabase: "master",
		After: []RunStep{{SQL: "IF EXISTS (" + state + "'OFFLINE') ALTER DATABASE " + quoted + " SET ONLINE;\n" +
			"IF EXISTS (" + state + "'RESTORING') THROW 50000, " + left + ", 1;", Database: "master"}},
	}
}

// sqlserverFile is a file of a database, or of a backup of one: kind is
// D for data, L for its log, S for FILESTREAM and F for full-text.
type sqlserverFile struct{ logical, kind, physical string }

// sqlserverBackupFiles reads the files a backup holds.
func sqlserverBackupFiles(ctx context.Context, d *DB, path string) ([]sqlserverFile, error) {
	rows, err := d.SQL.QueryContext(ctx, "RESTORE FILELISTONLY FROM DISK = "+sqlserverText(path))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	at := map[string]int{}
	for i, c := range cols {
		at[c] = i
	}
	var out []sqlserverFile
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		out = append(out, sqlserverFile{vals[at["LogicalName"]].String, vals[at["Type"]].String, vals[at["PhysicalName"]].String})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s holds no database files", path)
	}
	return out, nil
}

// sqlserverMoves are the MOVE clauses a restore of database puts a
// backup's files where with: onto the database's own files, when it has
// as many of each kind; else onto files of its name in the server's
// folders, as the backup's may be another database's, still in use.
func sqlserverMoves(database string, backup, existing []sqlserverFile, dataDir, logDir string) []string {
	count := func(files []sqlserverFile) map[string]int {
		n := map[string]int{}
		for _, f := range files {
			n[f.kind]++
		}
		return n
	}
	wanted, have := count(backup), count(existing)
	onto := len(wanted) == len(have)
	for kind, n := range wanted {
		onto = onto && have[kind] == n
	}
	var out []string
	used, data := map[string]int{}, 0
	for _, f := range backup {
		var to string
		if onto {
			i := used[f.kind]
			used[f.kind]++
			for _, e := range existing {
				if e.kind == f.kind {
					if i == 0 {
						to = e.physical
						break
					}
					i--
				}
			}
		} else {
			dir, ext := dataDir, ""
			switch f.kind {
			case "D":
				ext = ".ndf"
				if data == 0 {
					ext = ".mdf"
				}
				data++
			case "L":
				dir, ext = logDir, ".ldf"
			}
			to = sqlserverServerPath(dir, database+"_"+f.logical+ext)
		}
		out = append(out, "MOVE "+sqlserverText(f.logical)+" TO "+sqlserverText(to))
	}
	return out
}

// sqlserverDatabase is the database a backup is of: the one named, else
// the connection's, else the login's default.
func sqlserverDatabase(ctx context.Context, d *DB, database string) (string, error) {
	if database != "" {
		return database, nil
	}
	if d.Config.Database != "" {
		return d.Config.Database, nil
	}
	return queryString(ctx, d.SQL, "SELECT DB_NAME()")
}

// sqlserverBackupPath is a backup file's path on the server: a name
// alone is in the server's backup folder.
func sqlserverBackupPath(ctx context.Context, d *DB, path string) (string, error) {
	path = strings.TrimSpace(path)
	if strings.ContainsAny(path, `/\`) {
		return path, nil
	}
	dir, err := queryString(ctx, d.SQL, "SELECT CAST(SERVERPROPERTY('InstanceDefaultBackupPath') AS nvarchar(4000))")
	if err != nil {
		return "", err
	}
	return sqlserverServerPath(dir, path), nil
}

// sqlserverServerPath joins a name to a folder of the server, in the
// folder's separators; a name that is a path stays as it is.
func sqlserverServerPath(dir, name string) string {
	if strings.ContainsAny(name, `/\`) {
		return name
	}
	sep := "/"
	if strings.Contains(dir, `\`) {
		sep = `\`
	}
	if strings.HasSuffix(dir, "/") || strings.HasSuffix(dir, `\`) {
		return dir + name
	}
	return dir + sep + name
}

// sqlpackagePlan is sqlpackage's run of action on a database of the
// connection, through its tunnel or proxy.
func sqlpackagePlan(ctx context.Context, d *DB, action, file, database string) (ToolRun, error) {
	token := ""
	if d.Config.Identity != "" {
		var err error
		if token, err = identityToken(ctx, d.Config); err != nil {
			return ToolRun{}, err
		}
	}
	host, port := d.Address()
	return sqlpackageRun(d.Config, host, port, action, file, database, token)
}

// sqlpackageRun is sqlpackage's run of action, Export into file or
// Import from it, on a database of the server cfg names, reached at host
// and port. Its connection string, which holds the password, or the
// token, goes in its file of arguments: sqlpackage reads a password
// nowhere else but its command line. That file cannot hold a " or a \,
// which sqlpackage reads as quoting.
func sqlpackageRun(cfg Config, host string, port int, action, file, database, token string) (ToolRun, error) {
	if cfg.TLS == TLSVerifyFull && cfg.CAFile != "" {
		return ToolRun{}, errors.New("sqlpackage checks the server's certificate against the system's certificate authorities only, " +
			"not the connection's CA file: add the CA to the system's, or back up to a file on the server")
	}
	value := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
	encrypt := "Encrypt=False;TrustServerCertificate=True"
	switch cfg.TLS {
	case TLSRequire:
		encrypt = "Encrypt=True;TrustServerCertificate=True"
	case TLSVerifyFull:
		encrypt = "Encrypt=True;TrustServerCertificate=False"
		if host != cfg.Host {
			encrypt += ";HostNameInCertificate=" + value(cfg.Host)
		}
	}
	conn := "Server=tcp:" + host + "," + strconv.Itoa(port) + ";Initial Catalog=" + value(database) + ";" + encrypt +
		";Application Name=" + value(appName)
	switch {
	case token != "":
	case cfg.User == "":
		conn += ";Integrated Security=True"
	default:
		conn += ";User ID=" + value(cfg.User) + ";Password=" + value(cfg.Password)
	}
	if strings.ContainsAny(conn+token, "\"\\\n") {
		return ToolRun{}, errors.New(`sqlpackage cannot be given a password, user or database holding " or \ in the file of arguments DGopher passes it, ` +
			`never its command line: back up to a file on the server instead`)
	}
	side, fileArg := "Source", "/TargetFile:"
	if action == "Import" {
		side, fileArg = "Target", "/SourceFile:"
	}
	run := ToolRun{Argv: []string{"sqlpackage", "/Action:" + action, fileArg + file}, Env: []string{"DACFX_TELEMETRY_OPTOUT=1"},
		ArgsFile: `"/` + side + `ConnectionString:` + conn + `"` + "\n"}
	if action == "Export" {
		run.Argv = append(run.Argv, "/OverwriteFiles:True")
	}
	if token != "" {
		run.ArgsFile += `"/AccessToken:` + token + `"` + "\n"
	}
	return run, nil
}

func (sqlserverEngine) BackupNote(restore bool) string {
	return backupNoteFor(restore,
		"A .bacpac file on this computer imports with sqlpackage, which must be installed, into this database, which must be empty. "+
			"Any other path is a backup file on the server's disk, as the server sees it, a name alone in its backup folder: "+
			"RESTORE DATABASE replaces this database with it, ending the other sessions in it first, which rolls back their transactions. "+
			"A restore that fails or is stopped part way leaves the database restoring: restore it again.",
		"A BACPAC file holds the schema and rows, on this computer: sqlpackage writes it, and must be installed. "+
			"A backup file on the server is BACKUP DATABASE's, a copy-only full backup, at a path on the server's disk as the server sees it, "+
			"a name alone in its backup folder.")
}

// BackupChoices: a restore is planned whatever the file, never run as
// SQL, keeps what it did when it fails: an import keeps the tables it
// made, and a restore leaves the database restoring; and RESTORE ends the
// database's other sessions.
func (sqlserverEngine) BackupChoices() BackupChoices {
	return BackupChoices{RestoreKeepsPart: true, RestoresByPlan: true, RestoreEndsSessions: true}
}

func (sqlserverEngine) BackupScope() BackupScope        { return BackupScopeDatabase }
func (sqlserverEngine) BackupToolChecksCAOnly(*DB) bool { return false }
