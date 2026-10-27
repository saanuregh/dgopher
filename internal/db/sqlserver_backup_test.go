package db

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// A backup on the server's disk is a copy-only full backup, which leaves
// the server's own backup chain as it was, into a file it replaces; a
// name alone goes in the server's backup folder, in its separators.
func TestSQLServerBackupStatement(t *testing.T) {
	if got := sqlserverBackupSQL("it's", "/var/opt/mssql/data/x.bak"); got != "BACKUP DATABASE [it's] TO DISK = N'/var/opt/mssql/data/x.bak' WITH COPY_ONLY, INIT, CHECKSUM" {
		t.Fatalf("backup: %s", got)
	}
	for _, c := range []struct{ dir, name, want string }{
		{"/var/opt/mssql/data", "x.bak", "/var/opt/mssql/data/x.bak"},
		{"/var/opt/mssql/data/", "x.bak", "/var/opt/mssql/data/x.bak"},
		{`C:\Program Files\MSSQL\Backup`, "x.bak", `C:\Program Files\MSSQL\Backup\x.bak`},
		{"/var/opt/mssql/data", "/elsewhere/x.bak", "/elsewhere/x.bak"},
		{"/var/opt/mssql/data", `D:\x.bak`, `D:\x.bak`},
	} {
		if got := sqlserverServerPath(c.dir, c.name); got != c.want {
			t.Errorf("%s in %s: %s", c.name, c.dir, got)
		}
	}
}

// A restore moves each file of the backup onto the target database's
// file of its kind, so that it replaces the database where it is; a
// target whose files differ gets files of its own name in the server's
// folders.
func TestSQLServerRestoreMoves(t *testing.T) {
	backup := []sqlserverFile{{"shop", "D", "/data/shop.mdf"}, {"shop_log", "L", "/data/shop_log.ldf"}}
	existing := []sqlserverFile{{"t", "D", "/data/t.mdf"}, {"t_log", "L", "/logs/t_log.ldf"}}
	if got := sqlserverMoves("t", backup, existing, "/data/", "/logs/"); !slices.Equal(got, []string{
		"MOVE N'shop' TO N'/data/t.mdf'", "MOVE N'shop_log' TO N'/logs/t_log.ldf'"}) {
		t.Fatalf("onto the target's files: %q", got)
	}
	backup = append(backup, sqlserverFile{"more", "D", "/data/more.ndf"})
	if got := sqlserverMoves("t", backup, existing, `C:\Data\`, `C:\Logs`); !slices.Equal(got, []string{
		`MOVE N'shop' TO N'C:\Data\t_shop.mdf'`, `MOVE N'shop_log' TO N'C:\Logs\t_shop_log.ldf'`, `MOVE N'more' TO N'C:\Data\t_more.ndf'`}) {
		t.Fatalf("files of its own: %q", got)
	}
}

// A restore replaces the database in one batch on master: the database
// is taken offline, ending its other sessions, so that none comes
// between, and restored over. After, it is brought back online should the
// restore have failed before replacing it, and one the restore left
// restoring is an error saying so.
func TestSQLServerRestoreRun(t *testing.T) {
	run := sqlserverRestoreRun("shop", "/b/x.bak", []string{"MOVE N'shop' TO N'/data/shop.mdf'"})
	want := "IF DB_ID(N'shop') IS NOT NULL ALTER DATABASE [shop] SET OFFLINE WITH ROLLBACK IMMEDIATE;\n" +
		"IF @@ERROR = 0 RESTORE DATABASE [shop] FROM DISK = N'/b/x.bak' WITH REPLACE, MOVE N'shop' TO N'/data/shop.mdf'"
	if run.Statement != want || run.StatementDatabase != "master" || len(run.Before) != 0 {
		t.Fatalf("restore: %+v", run)
	}
	if len(run.After) != 1 || run.After[0].Database != "master" ||
		!strings.Contains(run.After[0].SQL, "ALTER DATABASE [shop] SET ONLINE") || !strings.Contains(run.After[0].SQL, "'RESTORING'") {
		t.Fatalf("after: %+v", run.After)
	}
}

// sqlpackage reaches the server as the app does: through its tunnel or
// proxy, checking the certificate against the server's name, with the
// password, or the token, in a file of arguments it alone reads, never
// on its command line.
func TestSQLServerSqlpackageRun(t *testing.T) {
	cfg := Config{Engine: SQLServer, Host: "db.example.com", Port: 1433, User: "ada", Password: "it's;Secret 1", TLS: TLSVerifyFull}
	run, err := sqlpackageRun(cfg, "127.0.0.1", 40001, "Export", "/tmp/shop.bacpac", "shop", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"sqlpackage", "/Action:Export", "/TargetFile:/tmp/shop.bacpac", "/OverwriteFiles:True"}; !slices.Equal(run.Argv, want) {
		t.Fatalf("argv %q", run.Argv)
	}
	want := `"/SourceConnectionString:Server=tcp:127.0.0.1,40001;Initial Catalog='shop';Encrypt=True;TrustServerCertificate=False;` +
		`HostNameInCertificate='db.example.com';Application Name='DGopher';User ID='ada';Password='it''s;Secret 1'"` + "\n"
	if run.ArgsFile != want {
		t.Fatalf("arguments file:\n%s\nwant\n%s", run.ArgsFile, want)
	}
	if strings.Contains(run.Shown(), "Secret") || !slices.Contains(run.Env, "DACFX_TELEMETRY_OPTOUT=1") {
		t.Fatalf("shown %q, env %q", run.Shown(), run.Env)
	}
	cfg.TLS, cfg.Password = TLSRequire, "x"
	run, err = sqlpackageRun(cfg, "db.example.com", 1433, "Import", "/tmp/shop.bacpac", "copy", "")
	if err != nil || run.Argv[1] != "/Action:Import" || run.Argv[2] != "/SourceFile:/tmp/shop.bacpac" ||
		!strings.HasPrefix(run.ArgsFile, `"/TargetConnectionString:Server=tcp:db.example.com,1433;Initial Catalog='copy';Encrypt=True;TrustServerCertificate=True;Application`) {
		t.Fatalf("import: %+v %v", run, err)
	}
	token := Config{Engine: SQLServer, Host: "x.database.windows.net", Port: 1433, Identity: IdentityAzureSQL, TLS: TLSVerifyFull}
	run, err = sqlpackageRun(token, "x.database.windows.net", 1433, "Export", "/tmp/a.bacpac", "db", "eyJ0.abc")
	if err != nil || !strings.Contains(run.ArgsFile, "\n\"/AccessToken:eyJ0.abc\"\n") || strings.Contains(run.ArgsFile, "User ID") {
		t.Fatalf("token: %q %v", run.ArgsFile, err)
	}
	kerberos := Config{Engine: SQLServer, Host: "db", Port: 1433, TLS: TLSPrefer}
	run, err = sqlpackageRun(kerberos, "db", 1433, "Export", "/tmp/a.bacpac", "db", "")
	if err != nil || !strings.Contains(run.ArgsFile, ";Encrypt=False;") || !strings.Contains(run.ArgsFile, ";Integrated Security=True") {
		t.Fatalf("kerberos: %q %v", run.ArgsFile, err)
	}
	for _, c := range []Config{
		{Engine: SQLServer, Host: "db", User: "ada", Password: `a"b`},
		{Engine: SQLServer, Host: "db", User: "ada", Password: `a\b`},
		{Engine: SQLServer, Host: "db", User: "ada", Password: "x", TLS: TLSVerifyFull, CAFile: "/ca.pem"},
	} {
		if _, err := sqlpackageRun(c, "db", 1433, "Export", "/tmp/a.bacpac", "db", ""); err == nil {
			t.Errorf("%+v: no refusal", c)
		}
	}
}

// SQL Server's backups: a BACPAC first, which needs sqlpackage, then a
// backup file on the server; a restore is always planned, whatever the
// file.
func TestSQLServerBackupRules(t *testing.T) {
	r := sqlserverEngine{}
	if !slices.Equal(r.BackupFormats(), []BackupFormat{BackupBacpac, BackupServerFile}) || r.BackupScope() != BackupScopeDatabase {
		t.Fatalf("formats %v, scope %v", r.BackupFormats(), r.BackupScope())
	}
	if c := r.BackupChoices(); !c.RestoresByPlan || !c.RestoreKeepsPart || !c.RestoreEndsSessions || c.Content || c.Clean || c.Folder {
		t.Fatalf("choices %+v", c)
	}
	if !BackupServerFile.OnServer() || BackupBacpac.OnServer() || BackupSQL.OnServer() {
		t.Fatal("which formats are on the server")
	}
}

// A database backed up to the server's disk restores over itself after
// it changed, even with a session of its own open; it is open to all
// again after.
func TestIntegrationSQLServerBackupRestore(t *testing.T) {
	integration(t)
	ctx := context.Background()
	master, _ := sqlserverSession(t)
	const name = "dgopher_it_backup"
	drop := func() {
		master.SQL.ExecContext(ctx, "IF DB_ID(N'"+name+"') IS NOT NULL ALTER DATABASE ["+name+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE")
		master.SQL.ExecContext(ctx, "DROP DATABASE IF EXISTS ["+name+"]")
	}
	drop()
	defer drop()
	mustExec(t, master, "CREATE DATABASE ["+name+"]")
	d, err := master.Database(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, "CREATE TABLE dbo.t (id int PRIMARY KEY)", "INSERT INTO dbo.t VALUES (1)")
	run, err := PlanBackup(ctx, d, BackupOptions{Format: BackupServerFile, Database: name, Path: name + ".bak"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(run.Statement, "TO DISK = N'/var/opt/mssql/data/"+name+".bak'") || run.Warning == "" {
		t.Fatalf("backup: %+v", run)
	}
	pool := func(context.Context) (*DB, error) { return d, nil }
	if err := RunPlanned(ctx, run, pool, func(string) {}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, "INSERT INTO dbo.t VALUES (2)")
	// A session in the database, which the restore ends.
	held, err := d.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if _, err := held.Exec(ctx, "BEGIN TRANSACTION; INSERT INTO dbo.t VALUES (3)"); err != nil {
		t.Fatal(err)
	}
	restore, err := PlanRestore(ctx, d, name, name+".bak", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunPlanned(ctx, restore, pool, func(string) {}); err != nil {
		t.Fatalf("%v\n%s", err, restore.Shown())
	}
	var n int
	var mode string
	if err := master.SQL.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM ["+name+"].dbo.t), user_access_desc FROM sys.databases WHERE name = N'"+name+"'").Scan(&n, &mode); err != nil {
		t.Fatal(err)
	}
	if n != 1 || mode != "MULTI_USER" {
		t.Fatalf("%d rows, %s", n, mode)
	}
}

// A restore runs while other sessions keep logging in to the database:
// none takes it between the sessions' end and the restore, and it is open
// to all after. A restore that fails before replacing the database leaves
// it online as it was; one left restoring says so.
func TestIntegrationSQLServerRestoreConcurrent(t *testing.T) {
	integration(t)
	ctx := context.Background()
	master, _ := sqlserverSession(t)
	const name = "dgopher_it_restore_race"
	drop := func() {
		master.SQL.ExecContext(ctx, "IF DB_ID(N'"+name+"') IS NOT NULL ALTER DATABASE ["+name+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE")
		master.SQL.ExecContext(ctx, "DROP DATABASE IF EXISTS ["+name+"]")
	}
	drop()
	defer drop()
	mustExec(t, master, "CREATE DATABASE ["+name+"]")
	d, err := master.Database(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, "CREATE TABLE dbo.t (id int PRIMARY KEY)", "INSERT INTO dbo.t VALUES (1)")
	pool := func(context.Context) (*DB, error) { return d, nil }
	backup, err := PlanBackup(ctx, d, BackupOptions{Format: BackupServerFile, Database: name, Path: name + ".bak"})
	if err != nil {
		t.Fatal(err)
	}
	if err := RunPlanned(ctx, backup, pool, func(string) {}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, "INSERT INTO dbo.t VALUES (2)")
	state := func() (rows int, status, access string) {
		t.Helper()
		if err := master.SQL.QueryRowContext(ctx, "SELECT state_desc, user_access_desc FROM sys.databases WHERE name = N'"+name+"'").Scan(&status, &access); err != nil {
			t.Fatal(err)
		}
		if status == "ONLINE" {
			master.SQL.QueryRowContext(ctx, "SELECT count(*) FROM ["+name+"].dbo.t").Scan(&rows)
		}
		return rows, status, access
	}

	// Others log in again and again, each staying a moment.
	cfg := sqlserverConfig
	cfg.Database = name
	others, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer others.Close()
	others.SQL.SetMaxIdleConns(0)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				c, cancel := context.WithTimeout(ctx, 2*time.Second)
				others.SQL.ExecContext(c, "WAITFOR DELAY '00:00:00.050'")
				cancel()
			}
		}()
	}
	restore, err := PlanRestore(ctx, d, name, name+".bak", false)
	if err != nil {
		close(stop)
		t.Fatal(err)
	}
	for i := range 3 {
		if err := RunPlanned(ctx, restore, pool, func(string) {}); err != nil {
			close(stop)
			wg.Wait()
			rows, status, access := state()
			t.Fatalf("restore %d: %v\n%s\nthen %d rows, %s, %s", i, err, restore.Shown(), rows, status, access)
		}
	}
	close(stop)
	wg.Wait()
	if rows, status, access := state(); rows != 1 || status != "ONLINE" || access != "MULTI_USER" {
		t.Fatalf("%d rows, %s, %s", rows, status, access)
	}

	mustExec(t, d, "INSERT INTO dbo.t VALUES (2)")
	missing := sqlserverRestoreRun(name, "/var/opt/mssql/data/"+name+"_missing.bak", nil)
	if err := RunPlanned(ctx, missing, pool, func(string) {}); err == nil {
		t.Fatal("a missing backup restored")
	}
	if rows, status, access := state(); rows != 2 || status != "ONLINE" || access != "MULTI_USER" {
		t.Fatalf("after a failed restore: %d rows, %s, %s", rows, status, access)
	}

	mustExec(t, master, "RESTORE DATABASE ["+name+"] FROM DISK = N'/var/opt/mssql/data/"+name+".bak' WITH REPLACE, NORECOVERY")
	_, err = runSteps(ctx, pool, missing.After)
	if err == nil || !strings.Contains(err.Error(), "restoring") {
		t.Fatalf("a database left restoring: %v", err)
	}
	mustExec(t, master, "RESTORE DATABASE ["+name+"] WITH RECOVERY")
}

// With sqlpackage installed, a database exports to a BACPAC file and
// imports into an empty database.
func TestIntegrationSQLServerBacpac(t *testing.T) {
	integration(t)
	if _, err := exec.LookPath("sqlpackage"); err != nil {
		t.Skip("sqlpackage is not installed")
	}
	ctx := context.Background()
	master, _ := sqlserverSession(t)
	const name = "dgopher_it_bacpac"
	drop := func() {
		master.SQL.ExecContext(ctx, "IF DB_ID(N'"+name+"') IS NOT NULL ALTER DATABASE ["+name+"] SET SINGLE_USER WITH ROLLBACK IMMEDIATE")
		master.SQL.ExecContext(ctx, "DROP DATABASE IF EXISTS ["+name+"]")
	}
	drop()
	defer drop()
	mustExec(t, master, "CREATE DATABASE ["+name+"]", "DROP TABLE IF EXISTS dbo.it_bacpac", "CREATE TABLE dbo.it_bacpac (id int PRIMARY KEY, note nvarchar(20))",
		"INSERT INTO dbo.it_bacpac VALUES (1, N'pen')")
	defer master.SQL.ExecContext(ctx, "DROP TABLE IF EXISTS dbo.it_bacpac")
	file := filepath.Join(t.TempDir(), "shop.bacpac")
	run, err := PlanBackup(ctx, master, BackupOptions{Format: BackupBacpac, Path: file})
	if err != nil {
		t.Fatal(err)
	}
	pool := func(context.Context) (*DB, error) { return master, nil }
	if err := RunPlanned(ctx, run, pool, func(line string) { t.Log(line) }); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(file); err != nil || st.Size() == 0 {
		t.Fatal(st, err)
	}
	d, err := master.Database(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	restore, err := PlanRestore(ctx, d, name, file, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunPlanned(ctx, restore, func(context.Context) (*DB, error) { return d, nil }, func(line string) { t.Log(line) }); err != nil {
		t.Fatal(err)
	}
	var note string
	if err := master.SQL.QueryRowContext(ctx, "SELECT note FROM ["+name+"].dbo.it_bacpac WHERE id = 1").Scan(&note); err != nil || note != "pen" {
		t.Fatalf("%q %v", note, err)
	}
}
