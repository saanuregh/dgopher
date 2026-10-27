package db

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// A dump file is named in a directory object, DATA_PUMP_DIR when alone,
// .dmp added to a name without an extension; never a path.
func TestOracleDumpFile(t *testing.T) {
	for _, c := range []struct{ path, dir, file, log string }{
		{"shop", "DATA_PUMP_DIR", "shop.dmp", "shop_export.log"},
		{" shop.dmp ", "DATA_PUMP_DIR", "shop.dmp", "shop_export.log"},
		{"backups:shop-1.DMP", "BACKUPS", "shop-1.DMP", "shop-1_export.log"},
	} {
		dir, file, log, err := oracleDumpFile(c.path, "export")
		if err != nil || dir != c.dir || file != c.file || log != c.log {
			t.Errorf("%q: %s %s %s %v", c.path, dir, file, log, err)
		}
	}
	for _, bad := range []string{"/tmp/shop.dmp", `C:\shop.dmp`, "dir/shop.dmp", "shop dump.dmp", "1dir:shop.dmp", "shop'.dmp"} {
		if _, _, _, err := oracleDumpFile(bad, "export"); err == nil {
			t.Errorf("%q named a dump file", bad)
		}
	}
}

// Data Pump reaches the server as the app does, at the address of its
// tunnel or proxy, by a connect descriptor; its user and password go in
// its parameter file alone, the password quoted, never on its command
// line, which names the schema as written.
func TestOracleDataPumpRun(t *testing.T) {
	cfg := Config{Engine: Oracle, Host: "db.example.com", Port: 1521, User: "ada", Password: "it's @ secret/1", Database: "SHOP"}
	run, err := dataPumpRun(cfg, "127.0.0.1", 40001, "expdp", "Shop", "DATA_PUMP_DIR", "s.dmp", "s_export.log", "CONTENT=ALL")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"expdp", `SCHEMAS='"Shop"'`, "DIRECTORY=DATA_PUMP_DIR", "DUMPFILE=s.dmp", "LOGFILE=s_export.log", "CONTENT=ALL"}; !slices.Equal(run.Argv, want) {
		t.Fatalf("argv %q", run.Argv)
	}
	want := `USERID='ada/"it''s @ secret/1"@(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=127.0.0.1)(PORT=40001))(CONNECT_DATA=(SERVICE_NAME=SHOP)))'` + "\n"
	if run.ArgsFile != want || run.ArgsFilePrefix != "parfile=" {
		t.Fatalf("parameter file %q, prefix %q", run.ArgsFile, run.ArgsFilePrefix)
	}
	if strings.Contains(run.Shown(), "secret") {
		t.Fatalf("shown %q", run.Shown())
	}
	tls := cfg
	tls.TLS, tls.Oracle.Wallet, tls.Oracle.SID = TLSVerifyFull, "/w", true
	run, err = dataPumpRun(tls, "db.example.com", 2484, "impdp", "SHOP", "D", "s.dmp", "s_import.log")
	if err != nil || !strings.Contains(run.ArgsFile, "(PROTOCOL=TCPS)(HOST=db.example.com)(PORT=2484))(CONNECT_DATA=(SID=SHOP))(SECURITY=(MY_WALLET_DIRECTORY=/w)(SSL_SERVER_DN_MATCH=YES))") {
		t.Fatalf("TLS: %q %v", run.ArgsFile, err)
	}
	for _, c := range []Config{
		{Engine: Oracle, Host: "db", User: "ada", Password: `a"b`, Database: "S"},
		{Engine: Oracle, Host: "db", User: "ada", Password: "x\ny", Database: "S"},
		{Engine: Oracle, Host: "db", User: "ada", Password: "x", Database: "S)(X=1"},
		{Engine: Oracle, Host: "db", Database: "S"},
		{Engine: Oracle, Host: "db", User: "ada", Password: "x", Database: "S", TLS: TLSRequire},
	} {
		if _, err := dataPumpRun(c, c.Host, 1521, "expdp", "S", "D", "s.dmp", "s.log"); err == nil {
			t.Errorf("%+v: no refusal", c)
		}
	}
	r := oracleEngine{}
	if !slices.Equal(r.BackupFormats(), []BackupFormat{BackupDataPump}) || !BackupDataPump.OnServer() || r.BackupScope() != BackupScopeSchema {
		t.Fatal("formats or scope")
	}
	if c := r.BackupChoices(); !c.Content || !c.RestoresByPlan || !c.RestoreKeepsPart || c.Clean || c.Folder || c.RestoreEndsSessions {
		t.Fatalf("choices %+v", c)
	}
}

// fakeDataPump puts expdp and impdp on the PATH, which run the test
// server's own in its container, reached there at its own port, and
// keep their command lines in args.
func fakeDataPump(t *testing.T) (args string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake Data Pump tools are shell scripts")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed, whose container's Data Pump the test runs")
	}
	dir := t.TempDir()
	args = filepath.Join(dir, "args")
	script := `#!/bin/sh
tool=$(basename "$0")
n=$#
for a in "$@"; do
  case "$a" in
  parfile=*)
    [ "$(stat -c %a "${a#parfile=}")" = 600 ] || { echo "the parameter file is not the owner's alone" >&2; exit 1; }
    sed 's/(HOST=127\.0\.0\.1)(PORT=11521)/(HOST=localhost)(PORT=1521)/' "${a#parfile=}" |
      docker exec -i dgopher-oracle sh -c 'cat > /tmp/dgopher-it.par' || exit 1
    a=parfile=/tmp/dgopher-it.par;;
  esac
  set -- "$@" "$a"
done
shift $n
printf '%s\n' "$tool" "$@" >> ` + args + `
exec docker exec dgopher-oracle "$tool" "$@"
`
	for _, tool := range []string{"expdp", "impdp"} {
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return args
}

// A schema exports into a dump file on the server, then imports over its
// later rows, its table replaced; the password never reaches a command
// line.
func TestOracleDataPump(t *testing.T) {
	d := openOracle(t, oracleSystemConfig)
	args := fakeDataPump(t)
	ctx := context.Background()
	drop := func() { d.SQL.ExecContext(ctx, `DROP USER IF EXISTS "IT_DUMP" CASCADE`) }
	drop()
	t.Cleanup(drop)
	mustExec(t, d, `CREATE USER "IT_DUMP" NO AUTHENTICATION QUOTA UNLIMITED ON USERS`, `CREATE TABLE "IT_DUMP"."t" ("id" NUMBER PRIMARY KEY)`,
		`INSERT INTO "IT_DUMP"."t" VALUES (1)`)
	pool := func(context.Context) (*DB, error) { return d, nil }
	run, err := PlanBackup(ctx, d, BackupOptions{Format: BackupDataPump, Database: "IT_DUMP", Path: "it_dump"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Warning == "" || !slices.Contains(run.Argv, "DUMPFILE=it_dump.dmp") {
		t.Fatalf("backup: %+v", run)
	}
	var lines []string
	if err := RunPlanned(ctx, run, pool, func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatalf("%v\n%s", err, strings.Join(lines, "\n"))
	}
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "successfully completed") }) {
		t.Fatalf("expdp said %q", lines)
	}
	mustExec(t, d, `INSERT INTO "IT_DUMP"."t" VALUES (2)`)
	restore, err := PlanRestore(ctx, d, "IT_DUMP", "DATA_PUMP_DIR:it_dump.dmp", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := RunPlanned(ctx, restore, pool, func(string) {}); err != nil {
		t.Fatalf("%v\n%s", err, restore.Shown())
	}
	var n int
	if err := d.SQL.QueryRowContext(ctx, `SELECT COUNT(*) FROM "IT_DUMP"."t"`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d rows after the restore: %v", n, err)
	}
	ran, _ := os.ReadFile(args)
	if !strings.Contains(string(ran), "impdp") || strings.Contains(string(ran), "USERID") || strings.Contains(string(ran), `"dgopher"`) {
		t.Fatalf("command lines %q", ran)
	}
}
