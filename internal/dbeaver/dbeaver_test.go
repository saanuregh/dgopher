package dbeaver

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dgopher/internal/db"
	"dgopher/internal/netproxy"
	"dgopher/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }

// encrypt writes plain as DBeaver does: a random-looking IV, then the
// AES-CBC ciphertext with PKCS#5 padding.
func encrypt(t *testing.T, plain []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(dbeaverKey)
	if err != nil {
		t.Fatal(err)
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	iv := []byte("0123456789abcdef")
	out := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, plain)
	return append(iv, out...)
}

// writeProject makes a project folder holding a .dbeaver folder with
// data-sources.json and, when creds is not nil, its encrypted credentials.
func writeProject(t *testing.T, root, name, dataSources string, creds any) Project {
	t.Helper()
	dir := filepath.Join(root, name)
	meta := filepath.Join(dir, ".dbeaver")
	if err := os.MkdirAll(meta, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(meta, "data-sources.json"), []byte(dataSources), 0o600); err != nil {
		t.Fatal(err)
	}
	if creds != nil {
		plain, err := json.Marshal(creds)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(meta, "credentials-config.json"), encrypt(t, plain), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Project{Name: name, Dir: dir}
}

// Each DBeaver connection maps to the DGopher connection it describes,
// or says why it cannot be imported, and what did not carry over.
func TestRead(t *testing.T) {
	pg := db.Postgres.DefaultPort()
	for _, c := range []struct {
		name  string
		conn  string // the connection's JSON, saved under the id "c1"
		types string // the file's connection-types
		creds map[string]any
		want  Entry
		// check replaces the comparison with want, for a field or two.
		check func(t *testing.T, e Entry)
	}{
		{
			name:  "postgres with credentials",
			conn:  `{"provider":"postgresql","driver":"postgres-jdbc","name":"Shop","folder":"Work/EU","configuration":{"host":"db.example","port":"5433","database":"shop","type":"dev"}}`,
			creds: map[string]any{"c1": map[string]any{"#connection": map[string]string{"user": "ann", "password": "pw"}}},
			want:  Entry{ID: "c1", Folder: "Work/EU", Config: db.Config{Name: "Shop", Engine: db.Postgres, Host: "db.example", Port: 5433, Database: "shop", User: "ann", Password: "pw", Env: db.Development}},
		},
		{
			name: "timescale driver",
			conn: `{"provider":"postgresql","driver":"postgres-timescale-jdbc","name":"T","configuration":{"host":"h","port":"5432"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "T", Engine: db.TimescaleDB, Host: "h", Port: 5432, Env: db.Development}},
		},
		{
			name: "yugabyte driver, default port",
			conn: `{"provider":"postgresql","driver":"postgres-yugabytedb-jdbc","name":"Y","configuration":{"host":"h"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "Y", Engine: db.YugabyteDB, Host: "h", Port: db.YugabyteDB.DefaultPort(), Env: db.Development}},
		},
		{
			name: "cockroach driver",
			conn: `{"provider":"postgresql","driver":"postgres-cockroach-jdbc","name":"C","configuration":{"host":"h","port":26257}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "C", Engine: db.CockroachDB, Host: "h", Port: 26257, Env: db.Development}},
		},
		{
			name: "mariadb driver",
			conn: `{"provider":"mysql","driver":"mariaDB","name":"M","configuration":{"host":"h"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "M", Engine: db.MariaDB, Host: "h", Port: db.MariaDB.DefaultPort(), Env: db.Development}},
		},
		{
			name: "mysql provider",
			conn: `{"provider":"mysql","driver":"mysql8","name":"My","configuration":{"host":"h"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "My", Engine: db.MySQL, Host: "h", Port: db.MySQL.DefaultPort(), Env: db.Development}},
		},
		{
			name: "tidb provider",
			conn: `{"provider":"tidb","driver":"tidb","name":"Ti","configuration":{"host":"h"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "Ti", Engine: db.TiDB, Host: "h", Port: db.TiDB.DefaultPort(), Env: db.Development}},
		},
		{
			name: "clickhouse provider speaks HTTP",
			conn: `{"provider":"clickhouse","driver":"com_clickhouse","name":"CH","configuration":{"host":"h","port":"8123"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "CH", Engine: db.ClickHouse, Host: "h", Port: 8123, ClickHouseProtocol: db.ClickHouseHTTP, Env: db.Development}},
		},
		{
			name: "generic yandex clickhouse",
			conn: `{"provider":"generic","driver":"yandex_clickhouse","name":"CH","configuration":{"host":"h","port":"8123"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "CH", Engine: db.ClickHouse, Host: "h", Port: 8123, ClickHouseProtocol: db.ClickHouseHTTP, Env: db.Development}},
		},
		{
			name: "sqlserver provider",
			conn: `{"provider":"sqlserver","driver":"microsoft","name":"S","configuration":{"host":"h"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "S", Engine: db.SQLServer, Host: "h", Port: db.SQLServer.DefaultPort(), Env: db.Development}},
		},
		{
			name: "older mssql provider",
			conn: `{"provider":"mssql","driver":"mssql_jdbc_ms","name":"S","configuration":{"host":"h","port":"1433"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "S", Engine: db.SQLServer, Host: "h", Port: 1433, Env: db.Development}},
		},
		{
			name: "oracle service name",
			conn: `{"provider":"oracle","driver":"oracle_thin","name":"O","configuration":{"host":"h","database":"ORCLPDB1","provider-properties":{"@dbeaver-connection-type@":"BASIC","@dbeaver-sid-service@":"SERVICE"}}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "O", Engine: db.Oracle, Host: "h", Port: db.Oracle.DefaultPort(), Database: "ORCLPDB1", Env: db.Development}},
		},
		{
			name: "oracle SID",
			conn: `{"provider":"oracle","driver":"oracle_thin","name":"O","configuration":{"host":"h","database":"XE","provider-properties":{"@dbeaver-sid-service@":"SID"}}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "O", Engine: db.Oracle, Host: "h", Port: db.Oracle.DefaultPort(), Database: "XE", Oracle: db.OracleConfig{SID: true}, Env: db.Development}},
		},
		{
			name: "generic oracle",
			conn: `{"provider":"generic","driver":"oracle","name":"O","configuration":{"host":"h","database":"XE"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "O", Engine: db.Oracle, Host: "h", Port: db.Oracle.DefaultPort(), Database: "XE", Env: db.Development}},
		},
		{
			name: "oracle TNS is skipped",
			conn: `{"provider":"oracle","driver":"oracle_thin","name":"O","configuration":{"database":"PROD","provider-properties":{"@dbeaver-connection-type@":"TNS"}}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "O", Engine: db.Oracle, Database: "PROD", Env: db.Development}, Skip: "TNS aliases are not supported"},
		},
		{
			name: "sqlite file",
			conn: `{"provider":"sqlite","driver":"sqlite_jdbc","name":"L","configuration":{"database":"/data/app.db"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "L", Engine: db.SQLite, Database: "/data/app.db", Env: db.Development}},
		},
		{
			name: "generic sqlite",
			conn: `{"provider":"generic","driver":"sqlite_xerial","name":"L","configuration":{"database":"/data/app.db"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "L", Engine: db.SQLite, Database: "/data/app.db", Env: db.Development}},
		},
		{
			name: "duckdb file",
			conn: `{"provider":"duckdb","driver":"duckdb_jdbc","name":"D","configuration":{"database":"/data/a.duckdb"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "D", Engine: db.DuckDB, Database: "/data/a.duckdb", Env: db.Development}},
		},
		{
			name: "motherduck is skipped",
			conn: `{"provider":"duckdb","driver":"motherduck_jdbc","name":"MD","configuration":{"database":"md:"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "MD", Env: db.Development}, Skip: "MotherDuck is not supported"},
		},
		{
			name: "unsupported driver is skipped",
			conn: `{"provider":"db2","driver":"db2_iseries","name":"Z","configuration":{"host":"h"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "Z", Env: db.Development}, Skip: "DBeaver's db2/db2_iseries driver is not supported"},
		},
		{
			name: "name falls back to the id",
			conn: `{"provider":"postgresql","driver":"postgres-jdbc","configuration":{"host":"h"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "c1", Engine: db.Postgres, Host: "h", Port: pg, Env: db.Development}},
		},
		{
			name: "URL mode",
			conn: `{"provider":"postgresql","driver":"postgres-timescale-jdbc","name":"U","configuration":{"configurationType":"URL","url":"jdbc:postgresql://u.example:6543/metrics"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "U", Engine: db.TimescaleDB, Host: "u.example", Port: 6543, Database: "metrics", Env: db.Development}},
		},
		{
			name: "URL mode hides a password the error quotes",
			conn: `{"provider":"sqlserver","driver":"microsoft","name":"U","configuration":{"configurationType":"URL","url":"jdbc:sqlserver://h:1433;databaseName=d;user=sa;password=Secret1"}}`,
			check: func(t *testing.T, e Entry) {
				if e.Skip == "" || strings.Contains(e.Skip, "Secret1") {
					t.Fatalf("skip %q", e.Skip)
				}
			},
		},
		{
			name: "Oracle custom URL without a host",
			conn: `{"provider":"oracle","driver":"oracle_thin","name":"O","configuration":{"url":"jdbc:oracle:thin:@//db.example.com:1521/ORCLPDB","provider-properties":{"@dbeaver-connection-type@":"CUSTOM"}}}`,
			check: func(t *testing.T, e Entry) {
				if e.Skip == "" {
					t.Fatalf("imported %+v", e.Config)
				}
			},
		},
		{
			name: "SQLite in URL mode",
			conn: `{"provider":"sqlite","driver":"sqlite_jdbc","name":"F","configuration":{"configurationType":"URL","url":"jdbc:sqlite:/home/u/app.db"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "F", Engine: db.SQLite, Database: "/home/u/app.db", Env: db.Development}},
		},
		{
			name: "DuckDB in URL mode without a file",
			conn: `{"provider":"duckdb","driver":"duckdb_jdbc","name":"F","configuration":{"configurationType":"URL","url":"jdbc:duckdb:"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "F", Engine: db.DuckDB, Env: db.Development}, Skip: "a database file is required"},
		},
		{
			name: "ClickHouse without a port takes HTTP's",
			conn: `{"provider":"clickhouse","driver":"com_clickhouse","name":"C","configuration":{"host":"ch"}}`,
			check: func(t *testing.T, e Entry) {
				if e.Skip != "" || e.Config.Port != 8123 {
					t.Fatalf("got %+v, skip %q", e.Config, e.Skip)
				}
			},
		},
		{
			name: "MySQL TLS not required",
			conn: `{"provider":"mysql","driver":"mysql8","name":"M","configuration":{"host":"h","handlers":{"mysql_ssl":{"enabled":true,"properties":{"ssl.require":"false"}}}}}`,
			check: func(t *testing.T, e Entry) {
				if e.Config.TLS != db.TLSPrefer {
					t.Fatalf("tls %q", e.Config.TLS)
				}
			},
		},
		{
			name: "IPv6 jump host",
			conn: `{"provider":"postgresql","driver":"postgres-jdbc","name":"S","configuration":{"host":"h","handlers":{"ssh_tunnel":{"enabled":true,"properties":{"host":"b","authType":"AGENT","jumpServer0.host":"2001:db8::1"}}}}}`,
			check: func(t *testing.T, e Entry) {
				if e.Config.SSH.Jump != "[2001:db8::1]:22" {
					t.Fatalf("jump %q", e.Config.SSH.Jump)
				}
			},
		},
		{
			name: "URL mode that cannot be read",
			conn: `{"provider":"postgresql","driver":"postgres-jdbc","name":"U","configuration":{"configurationType":"URL","url":"jdbc:weird:thing"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "U", Engine: db.Postgres, Env: db.Development}, Skip: `its URL could not be read: unknown kind of URL "weird": use postgres://, mysql://, clickhouse://, redis:// or a file path`},
		},
		{
			name: "plaintext user and password",
			conn: `{"provider":"postgresql","driver":"postgres-jdbc","name":"P","configuration":{"host":"h","user":"bob","password":"plain"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "P", Engine: db.Postgres, Host: "h", Port: pg, User: "bob", Password: "plain", Env: db.Development}},
		},
		{
			name:  "credentials win over plaintext",
			conn:  `{"provider":"postgresql","driver":"postgres-jdbc","name":"P","configuration":{"host":"h","user":"bob","password":"plain"}}`,
			creds: map[string]any{"c1": map[string]any{"#connection": map[string]string{"user": "ann"}}},
			want:  Entry{ID: "c1", Config: db.Config{Name: "P", Engine: db.Postgres, Host: "h", Port: pg, User: "ann", Password: "plain", Env: db.Development}},
		},
		{
			name: "read-only",
			conn: `{"provider":"postgresql","driver":"postgres-jdbc","name":"R","read-only":true,"configuration":{"host":"h"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "R", Engine: db.Postgres, Host: "h", Port: pg, ReadOnly: true, Env: db.Development}},
		},
		{
			name: "other login model",
			conn: `{"provider":"postgresql","driver":"postgres-jdbc","name":"A","configuration":{"host":"h","auth-model":"postgres_pgpass"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "A", Engine: db.Postgres, Host: "h", Port: pg, Env: db.Development}, Notes: []string{"DBeaver's postgres_pgpass login was not imported; set the login in the connection form"}},
		},
		{
			name: "postgres TLS verify-ca with files",
			conn: `{"provider":"postgresql","driver":"postgres-jdbc","name":"T","configuration":{"host":"h","handlers":{"postgre_ssl":{"enabled":true,"properties":{"sslMode":"verify-ca","ssl.ca.cert":"/ca.pem","ssl.client.cert":"/c.pem","ssl.client.key":"/k.pem"}}}}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "T", Engine: db.Postgres, Host: "h", Port: pg, TLS: db.TLSVerifyFull, CAFile: "/ca.pem", CertFile: "/c.pem", KeyFile: "/k.pem", Env: db.Development}},
		},
		{
			name: "postgres TLS allow, old file keys",
			conn: `{"provider":"postgresql","driver":"postgres-jdbc","name":"T","configuration":{"host":"h","handlers":{"postgre_ssl":{"enabled":true,"properties":{"sslMode":"allow","rootCert":"/ca.pem","clientCert":"/c.pem","clientKey":"/k.pem"}}}}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "T", Engine: db.Postgres, Host: "h", Port: pg, TLS: db.TLSPrefer, CAFile: "/ca.pem", CertFile: "/c.pem", KeyFile: "/k.pem", Env: db.Development}},
		},
		{
			name: "postgres TLS without a mode requires",
			conn: `{"provider":"postgresql","driver":"postgres-jdbc","name":"T","configuration":{"host":"h","handlers":{"postgre_ssl":{"enabled":true,"properties":{}}}}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "T", Engine: db.Postgres, Host: "h", Port: pg, TLS: db.TLSRequire, Env: db.Development}},
		},
		{
			name: "postgres TLS disable",
			conn: `{"provider":"postgresql","driver":"postgres-jdbc","name":"T","configuration":{"host":"h","handlers":{"postgre_ssl":{"enabled":true,"properties":{"sslMode":"disable"}}}}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "T", Engine: db.Postgres, Host: "h", Port: pg, TLS: db.TLSDisable, Env: db.Development}},
		},
		{
			name: "disabled TLS handler sets nothing",
			conn: `{"provider":"postgresql","driver":"postgres-jdbc","name":"T","configuration":{"host":"h","handlers":{"postgre_ssl":{"enabled":false,"properties":{"sslMode":"require"}}}}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "T", Engine: db.Postgres, Host: "h", Port: pg, Env: db.Development}},
		},
		{
			name: "mysql TLS verifying the server",
			conn: `{"provider":"mysql","driver":"mysql8","name":"T","configuration":{"host":"h","handlers":{"mysql_ssl":{"enabled":true,"properties":{"ssl.verify.server":"true","ssl.ca.cert":"/ca.pem"}}}}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "T", Engine: db.MySQL, Host: "h", Port: db.MySQL.DefaultPort(), TLS: db.TLSVerifyFull, CAFile: "/ca.pem", Env: db.Development}},
		},
		{
			name: "mysql TLS not verifying",
			conn: `{"provider":"mysql","driver":"mysql8","name":"T","configuration":{"host":"h","handlers":{"mysql_ssl":{"enabled":true,"properties":{"ssl.verify.server":false}}}}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "T", Engine: db.MySQL, Host: "h", Port: db.MySQL.DefaultPort(), TLS: db.TLSRequire, Env: db.Development}},
		},
		{
			name: "sqlserver TLS",
			conn: `{"provider":"sqlserver","driver":"microsoft","name":"T","configuration":{"host":"h","handlers":{"mssql_ssl":{"enabled":true,"properties":{}}}}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "T", Engine: db.SQLServer, Host: "h", Port: db.SQLServer.DefaultPort(), TLS: db.TLSVerifyFull, Env: db.Development}},
		},
		{
			name: "clickhouse TLS",
			conn: `{"provider":"clickhouse","driver":"com_clickhouse","name":"T","configuration":{"host":"h","port":"8443","handlers":{"clickhouse-ssl":{"enabled":true,"properties":{"ssl.ca.cert":"/ca.pem"}}}}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "T", Engine: db.ClickHouse, Host: "h", Port: 8443, ClickHouseProtocol: db.ClickHouseHTTP, TLS: db.TLSVerifyFull, CAFile: "/ca.pem", Env: db.Development}},
		},
		{
			name:  "SSH with a password",
			conn:  `{"provider":"postgresql","driver":"postgres-jdbc","name":"S","configuration":{"host":"h","handlers":{"ssh_tunnel":{"enabled":true,"properties":{"host":"bastion","authType":"PASSWORD"}}}}}`,
			creds: map[string]any{"c1": map[string]any{"network/ssh_tunnel": map[string]string{"user": "ops", "password": "sshpw"}}},
			want:  Entry{ID: "c1", Config: db.Config{Name: "S", Engine: db.Postgres, Host: "h", Port: pg, Env: db.Development, SSH: db.SSHConfig{Enabled: true, Host: "bastion", Port: 22, User: "ops", Password: "sshpw"}}},
		},
		{
			name:  "SSH with a key, jumps and a pasted key",
			conn:  `{"provider":"postgresql","driver":"postgres-jdbc","name":"S","configuration":{"host":"h","handlers":{"ssh_tunnel":{"enabled":true,"properties":{"host":"bastion","port":"2222","authType":"PUBLIC_KEY","keyPath":"/k","jumpServer0.host":"j1","jumpServer0.port":"22","jumpServer0.name":"u1","jumpServer1.host":"j2","jumpServer1.enabled":"false","jumpServer2.host":"j3"}}}}}`,
			creds: map[string]any{"c1": map[string]any{"network/ssh_tunnel": map[string]string{"user": "ops", "password": "pass", "keyValue": "-----BEGIN", "jumpServer0.password": "jp"}}},
			want: Entry{ID: "c1", Config: db.Config{Name: "S", Engine: db.Postgres, Host: "h", Port: pg, Env: db.Development, SSH: db.SSHConfig{Enabled: true, Host: "bastion", Port: 2222, User: "ops", KeyPath: "/k", KeyPassphrase: "pass", Jump: "u1@j1:22,j3"}},
				Notes: []string{"the SSH key pasted into DBeaver was not imported; choose its file", "jump hosts log in with a key or the agent only"}},
		},
		{
			name: "SSH through the agent, plaintext user",
			conn: `{"provider":"postgresql","driver":"postgres-jdbc","name":"S","configuration":{"host":"h","handlers":{"ssh_tunnel":{"enabled":true,"user":"ops","properties":{"host":"bastion","authType":"AGENT"}}}}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "S", Engine: db.Postgres, Host: "h", Port: pg, Env: db.Development, SSH: db.SSHConfig{Enabled: true, Host: "bastion", Port: 22, User: "ops", UseAgent: true}}},
		},
		{
			name:  "SOCKS proxy with credentials",
			conn:  `{"provider":"postgresql","driver":"postgres-jdbc","name":"P","configuration":{"host":"h","handlers":{"socks_proxy":{"enabled":true,"properties":{"socks-host":"proxy","socks-port":"1081"}}}}}`,
			creds: map[string]any{"c1": map[string]any{"network/socks_proxy": map[string]string{"user": "pu", "password": "pp"}}},
			want:  Entry{ID: "c1", Config: db.Config{Name: "P", Engine: db.Postgres, Host: "h", Port: pg, Env: db.Development, Proxy: db.ProxyConfig{Kind: netproxy.SOCKS5, Host: "proxy", Port: 1081, User: "pu", Password: "pp"}}},
		},
		{
			name: "built-in prod type keeps the default commit",
			conn: `{"provider":"postgresql","driver":"postgres-jdbc","name":"E","configuration":{"host":"h","type":"prod"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "E", Engine: db.Postgres, Host: "h", Port: pg, Env: db.Production}, EnvChosen: true},
		},
		{
			name: "built-in test type",
			conn: `{"provider":"postgresql","driver":"postgres-jdbc","name":"E","configuration":{"host":"h","type":"test"}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "E", Engine: db.Postgres, Host: "h", Port: pg, Env: db.Staging}, EnvChosen: true},
		},
		{
			name: "prod with autocommit on",
			conn: `{"provider":"postgresql","driver":"postgres-jdbc","name":"E","configuration":{"host":"h","type":"prod","bootstrap":{"autocommit":true}}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "E", Engine: db.Postgres, Host: "h", Port: pg, Env: db.Production, Commit: db.CommitAuto}, EnvChosen: true},
		},
		{
			name: "dev with autocommit off",
			conn: `{"provider":"postgresql","driver":"postgres-jdbc","name":"E","configuration":{"host":"h","type":"dev","bootstrap":{"autocommit":false}}}`,
			want: Entry{ID: "c1", Config: db.Config{Name: "E", Engine: db.Postgres, Host: "h", Port: pg, Env: db.Development, Commit: db.CommitManual}},
		},
		{
			name:  "custom type confirming execution",
			conn:  `{"provider":"postgresql","driver":"postgres-jdbc","name":"E","configuration":{"host":"h","type":"live"}}`,
			types: `{"live":{"name":"Live","auto-commit":true,"confirm-execute":true}}`,
			want:  Entry{ID: "c1", Config: db.Config{Name: "E", Engine: db.Postgres, Host: "h", Port: pg, Env: db.Production, Commit: db.CommitAuto}, EnvChosen: true},
		},
		{
			name:  "custom type confirming data changes",
			conn:  `{"provider":"postgresql","driver":"postgres-jdbc","name":"E","configuration":{"host":"h","type":"qa"}}`,
			types: `{"qa":{"name":"QA","auto-commit":false,"confirm-data-change":true}}`,
			want:  Entry{ID: "c1", Config: db.Config{Name: "E", Engine: db.Postgres, Host: "h", Port: pg, Env: db.Staging, Commit: db.CommitManual}, EnvChosen: true},
		},
		{
			name:  "custom type confirming nothing",
			conn:  `{"provider":"postgresql","driver":"postgres-jdbc","name":"E","configuration":{"host":"h","type":"mine"}}`,
			types: `{"mine":{"name":"Mine"}}`,
			want:  Entry{ID: "c1", Config: db.Config{Name: "E", Engine: db.Postgres, Host: "h", Port: pg, Env: db.Development}, EnvChosen: true},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			types := c.types
			if types == "" {
				types = "{}"
			}
			var creds any
			if c.creds != nil {
				creds = c.creds
			}
			p := writeProject(t, t.TempDir(), "General", `{"connection-types":`+types+`,"connections":{"c1":`+c.conn+`}}`, creds)
			got, err := Read(p)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d entries", len(got))
			}
			if c.check != nil {
				c.check(t, got[0])
				return
			}
			if !reflect.DeepEqual(got[0], c.want) {
				t.Errorf("got\n%+v\nwant\n%+v", got[0], c.want)
			}
		})
	}
}

// Entries come in file order, the default file first, then by name;
// each file takes its own credentials file.
func TestReadOrdersFilesAndNames(t *testing.T) {
	p := writeProject(t, t.TempDir(), "General", `{"connections":{
		"b":{"provider":"postgresql","driver":"postgres-jdbc","name":"beta","configuration":{"host":"h"}},
		"a":{"provider":"postgresql","driver":"postgres-jdbc","name":"Alpha","configuration":{"host":"h"}}}}`, nil)
	meta := filepath.Join(p.Dir, ".dbeaver")
	if err := os.WriteFile(filepath.Join(meta, "data-sources-2.json"), []byte(`{"connections":{"z":{"provider":"sqlite","driver":"sqlite_jdbc","name":"Aardvark","configuration":{"database":"/x.db"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	plain, _ := json.Marshal(map[string]any{"z": map[string]any{"#connection": map[string]string{"password": "zz"}}})
	if err := os.WriteFile(filepath.Join(meta, "credentials-config-2.json"), encrypt(t, plain), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range got {
		names = append(names, e.Config.Name)
	}
	if strings.Join(names, ",") != "Alpha,beta,Aardvark" {
		t.Errorf("order %v", names)
	}
	if !got[2].HasSecrets() || got[0].HasSecrets() {
		t.Errorf("secrets: %v %v", got[0].HasSecrets(), got[2].HasSecrets())
	}
}

// Credentials decrypt with DBeaver's key; a file that does not is an
// error naming it.
func TestDecrypt(t *testing.T) {
	plain := []byte(`{"c1":{"#connection":{"user":"u"}}}`)
	got, err := decrypt(encrypt(t, plain))
	if err != nil || string(got) != string(plain) {
		t.Fatalf("decrypt: %q %v", got, err)
	}
	bad := encrypt(t, plain)
	bad[len(bad)-1] ^= 0xff
	if _, err := decrypt(bad); err == nil {
		t.Error("bad padding decrypted")
	}
	if _, err := decrypt([]byte("short")); err == nil {
		t.Error("short input decrypted")
	}

	p := writeProject(t, t.TempDir(), "General", `{"connections":{}}`, nil)
	file := filepath.Join(p.Dir, ".dbeaver", "credentials-config.json")
	if err := os.WriteFile(file, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(p); err == nil || !strings.Contains(err.Error(), file) {
		t.Errorf("Read with bad credentials: %v", err)
	}
}

// A project without a credentials file reads, without secrets.
func TestReadWithoutCredentials(t *testing.T) {
	p := writeProject(t, t.TempDir(), "General", `{"connections":{"c1":{"provider":"postgresql","driver":"postgres-jdbc","name":"P","configuration":{"host":"h"}}}}`, nil)
	got, err := Read(p)
	if err != nil || len(got) != 1 || got[0].HasSecrets() {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// DBeaver's data folders are where each platform keeps them.
func TestDataDirs(t *testing.T) {
	for _, c := range []struct {
		goos, home, appData string
		want                []string
	}{
		{"linux", "/home/u", "", []string{
			"/home/u/.local/share/DBeaverData",
			"/home/u/.local/share/.DBeaverData",
			"/home/u/.var/app/io.dbeaver.DBeaverCommunity/data/DBeaverData",
			"/home/u/snap/dbeaver-ce/current/.local/share/DBeaverData",
		}},
		{"freebsd", "/home/u", "", []string{
			"/home/u/.local/share/DBeaverData",
			"/home/u/.local/share/.DBeaverData",
			"/home/u/.var/app/io.dbeaver.DBeaverCommunity/data/DBeaverData",
			"/home/u/snap/dbeaver-ce/current/.local/share/DBeaverData",
		}},
		{"darwin", "/Users/u", "", []string{"/Users/u/Library/DBeaverData"}},
		{"windows", `C:\Users\u`, `C:\Users\u\AppData\Roaming`, []string{filepath.Join(`C:\Users\u\AppData\Roaming`, "DBeaverData")}},
		{"windows", "/u", "", []string{filepath.Join("/u", "AppData", "Roaming", "DBeaverData")}},
	} {
		if got := candidateDataDirs(c.goos, c.home, c.appData); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q", c.goos, got)
		}
	}
}

// Projects are found from a data folder, its workspace, a project
// folder or a .dbeaver folder; one without connections is left out.
func TestProjects(t *testing.T) {
	data := t.TempDir()
	ws := filepath.Join(data, "workspace6")
	writeProject(t, ws, "General", `{"connections":{}}`, nil)
	writeProject(t, ws, "Another", `{"connections":{}}`, nil)
	if err := os.MkdirAll(filepath.Join(ws, "Empty", ".dbeaver"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws, ".metadata"), 0o755); err != nil {
		t.Fatal(err)
	}
	general := Project{Name: "General", Dir: filepath.Join(ws, "General")}
	both := []Project{{Name: "Another", Dir: filepath.Join(ws, "Another")}, general}
	for _, c := range []struct {
		dir  string
		want []Project
	}{
		{data, both},
		{ws, both},
		{general.Dir, []Project{general}},
		{filepath.Join(general.Dir, ".dbeaver"), []Project{general}},
		{filepath.Join(ws, "Empty"), nil},
	} {
		got, err := Projects(c.dir)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v", c.dir, got)
		}
	}
	if _, err := Projects(filepath.Join(data, "missing")); err == nil {
		t.Error("a missing folder lists no error")
	}
}

// A project protected by a DBeaver project password cannot be read.
func TestLocked(t *testing.T) {
	for _, c := range []struct {
		name, settings, dataSources string
	}{
		{"secure project", `{"secureProject":true}`, `{"connections":{}}`},
		{"project password", `{"useProjectPassword":"true"}`, `{"connections":{}}`},
		{"encrypted data sources", "", "\x8a\x01binary"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := writeProject(t, t.TempDir(), "General", c.dataSources, nil)
			if c.settings != "" {
				if err := os.WriteFile(filepath.Join(p.Dir, ".dbeaver", "project-settings.json"), []byte(c.settings), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Read(p); !errors.Is(err, ErrLocked) {
				t.Errorf("got %v", err)
			}
		})
	}
	p := writeProject(t, t.TempDir(), "General", `{"connections":{}}`, nil)
	if err := os.WriteFile(filepath.Join(p.Dir, ".dbeaver", "project-settings.json"), []byte(`{"secureProject":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(p); err != nil {
		t.Errorf("unlocked project: %v", err)
	}
}
