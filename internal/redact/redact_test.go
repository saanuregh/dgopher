package redact

import (
	"fmt"
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	for in, secret := range map[string]string{
		"ALTER USER app WITH PASSWORD 'hunter2'":                                       "hunter2",
		"ALTER ROLE app PASSWORD = 'it''s secret'":                                     "secret",
		"create user u identified with mysql_native_password by 'pw9'":                 "pw9",
		"CREATE USER 'a'@'%' IDENTIFIED BY 's3cr''et'":                                 "s3cr",
		"CREATE USER x IDENTIFIED WITH sha256_password BY 'pw123'":                     "pw123",
		"AUTH default topsecret":                                                       "topsecret",
		"auth onlypass":                                                                "onlypass",
		"CONFIG SET requirepass newpass":                                               "newpass",
		"ACL SETUSER alice on >alicepw ~* +@all":                                       "alicepw",
		"MIGRATE host 6379 key 0 5000 AUTH2 user migratepw":                            "migratepw",
		"SET PASSWORD FOR 'app'@'%' = 'newpw1'":                                        "newpw1",
		"CREATE USER 'a'@'%' IDENTIFIED BY \"dq-secret\"":                              "dq-secret",
		"MIGRATE host 6379 \"\" 0 5000 AUTH migpw KEYS a b":                            "migpw",
		"HELLO 3 AUTH alice hellopw":                                                   "hellopw",
		"SELECT * FROM mysql('db:3306', 'shop', 'orders', 'root', 'chpw')":             "chpw",
		"SELECT * FROM s3('https://b.s3.amazonaws.com/x.csv', 'AKIAKEY', 'awssecret')": "awssecret",
		"SELECT dblink_connect('host=h user=u password=dbpw')":                         "dbpw",
		"ALTER ROLE app PASSWORD $$dollarpw$$":                                         "dollarpw",
	} {
		out := Secrets(in)
		if strings.Contains(out, secret) {
			t.Errorf("%q kept its secret: %q", in, out)
		}
	}
	if s := "SELECT 'set PASSWORD ''x''' FROM passwords WHERE id = 1"; Secrets(s) != s {
		t.Errorf("an ordinary query changed: %q", Secrets(s))
	}
}

func TestRedactRedisArgs(t *testing.T) {
	for _, c := range []struct {
		args   []string
		secret string
	}{
		{[]string{"CONFIG", "SET", "requirepass", "two words"}, "two words"},
		{[]string{"CONFIG", "SET", "maxmemory", "1gb", "requirepass", "secretA"}, "secretA"},
		{[]string{"ACL", "SETUSER", "bob", "on", ">pw with space"}, "pw with space"},
		{[]string{"HELLO", "3", "AUTH", "alice", "spaced pw"}, "spaced pw"},
		{[]string{"AUTH", "default", "pw"}, "pw"},
		{[]string{"MIGRATE", "h", "6379", "", "0", "5000", "AUTH", "migpw", "KEYS", "a"}, "migpw"},
	} {
		if out := Redis(c.args); strings.Contains(out, c.secret) {
			t.Errorf("%q kept its secret: %s", c.args, out)
		}
	}
	if out := Redis([]string{"CONFIG", "SET", "maxmemory", "1gb"}); !strings.Contains(out, "1gb") {
		t.Errorf("a setting that is no secret was hidden: %s", out)
	}
	for _, c := range []struct {
		args         []string
		secret, keep string
	}{
		{[]string{"SENTINEL", "SET", "mymaster", "auth-pass", "sentpw1"}, "sentpw1", "SENTINEL SET mymaster auth-pass"},
		{[]string{"SENTINEL", "SET", "mymaster", "quorum", "2", "auth-pass", "sentpw2"}, "sentpw2", "quorum 2 auth-pass"},
		{[]string{"SENTINEL", "CONFIG", "SET", "sentinel-pass", "sentpw3"}, "sentpw3", "SENTINEL CONFIG SET sentinel-pass"},
		{[]string{"ACL", "SETUSER", "bob", "on", "<rmpw44"}, "rmpw44", "ACL SETUSER bob on <"},
	} {
		out := Redis(c.args)
		if strings.Contains(out, c.secret) || !strings.Contains(out, c.keep) {
			t.Errorf("%q became %s", c.args, out)
		}
		text := Secrets(strings.Join(c.args, " "))
		if strings.Contains(text, c.secret) || !strings.Contains(text, c.keep) {
			t.Errorf("%q as text became %s", c.args, text)
		}
	}
	if out := Redis([]string{"SENTINEL", "SET", "mymaster", "quorum", "2"}); out != "SENTINEL SET mymaster quorum 2" {
		t.Errorf("a sentinel setting that is no secret was hidden: %s", out)
	}
}

func TestRedactMoreSQL(t *testing.T) {
	for in, secret := range map[string]string{
		"CREATE SUBSCRIPTION s CONNECTION 'host=h dbname=d password=subpw' PUBLICATION p": "subpw",
		"CREATE SECRET (TYPE s3, KEY_ID 'AKIA', SECRET 'ducksecret')":                     "ducksecret",
		"ATTACH 'host=h password=attachpw' AS p (TYPE postgres)":                          "attachpw",
		"CHANGE REPLICATION SOURCE TO SOURCE_PASSWORD='replpw'":                           "replpw",
		"CHANGE MASTER TO MASTER_PASSWORD = 'mpw'":                                        "mpw",
		"ALTER USER u IDENTIFIED BY 'x' REPLACE 'oldpw'":                                  "oldpw",
		"SET PASSWORD = PASSWORD('fnpw')":                                                 "fnpw",
		"ALTER ROLE r PASSWORD U&'unipw'":                                                 "unipw",
	} {
		if out := Secrets(in); strings.Contains(out, secret) {
			t.Errorf("%q kept its secret: %s", in, out)
		}
	}
	cases := []struct{ in, secret, keep string }{
		// MySQL and ClickHouse read \' as a quote inside the string.
		{`CREATE USER u IDENTIFIED BY 'p\'ss w0rd' PASSWORD EXPIRE`, "w0rd", "PASSWORD EXPIRE"},
		{`CREATE USER u IDENTIFIED WITH sha256_password BY 'p\'ss w0rd' SETTINGS max_threads = 4`, "w0rd", "SETTINGS max_threads = 4"},
		{`ALTER USER u IDENTIFIED BY "p\"ss w0rd" ACCOUNT LOCK`, "w0rd", "ACCOUNT LOCK"},
		{`ATTACH 'postgres://u:urlpw77@h/db' AS p (TYPE postgres)`, "urlpw77", "AS p (TYPE postgres)"},
		{`CREATE SUBSCRIPTION s CONNECTION 'postgresql://rep:subpw99@h:5432/d' PUBLICATION p`, "subpw99", "PUBLICATION p"},
		{`SELECT 1 -- postgres://u:barepw1@h/db`, "barepw1", "SELECT 1 -- postgres://u:"},
		{`ATTACH 'host=h user=u passwd=passwd77' AS p (TYPE postgres)`, "passwd77", "AS p (TYPE postgres)"},
		{`CREATE SECRET az (TYPE azure, CONNECTION_STRING 'DefaultEndpointsProtocol=https;AccountName=acct;AccountKey=azkey123==;')`, "azkey123", "TYPE azure"},
		{`CREATE SECRET h (TYPE http, EXTRA_HTTP_HEADERS MAP {'Authorization': 'Bearer tok999'})`, "tok999", "TYPE http"},
		{`CREATE SECRET h (TYPE http, EXTRA_HTTP_HEADERS MAP {'X-Api-Key': 'apikey777'}, SCOPE 's3://b')`, "apikey777", "SCOPE 's3://b'"},
		{`SELECT * FROM read_csv('x.csv', headers = {'Authorization': 'Bearer tok888'})`, "tok888", "read_csv('x.csv'"},
		{`ATTACH 'md:my_db?motherduck_token=mdtok55' AS md`, "mdtok55", "AS md"},
	}
	for _, engine := range []string{"MaterializedPostgreSQL", "MaterializedMySQL", "S3Queue", "AzureQueue",
		"icebergS3", "icebergAzure", "icebergHDFS", "icebergS3Cluster", "deltaLakeCluster", "hudiCluster",
		"ExternalDistributed", "ODBC", "JDBC"} {
		cases = append(cases, struct{ in, secret, keep string }{
			"CREATE TABLE t (id UInt64) ENGINE = " + engine + "('h:5432', 'db', 'u', 'engpw42')", "engpw42", "CREATE TABLE t (id UInt64) ENGINE = " + engine + "(",
		})
	}
	for _, c := range cases {
		if out := Secrets(c.in); strings.Contains(out, c.secret) || !strings.Contains(out, c.keep) {
			t.Errorf("%q became %s", c.in, out)
		}
	}
	for _, s := range []string{
		`SELECT 'C:\' AS p, 'it''s' FROM t WHERE x = 'y'`,
		`SELECT 'https://example.com:8080/a@b' AS link`,
		`SELECT "connection_string", url FROM t`,
		`SELECT MAP {'a': 1} AS m`,
	} {
		if out := Secrets(s); out != s {
			t.Errorf("an ordinary query changed: %q became %q", s, out)
		}
	}
}

// A server's error may quote the statement's secret where a lexer would
// not find it.
func TestError(t *testing.T) {
	for _, c := range []struct{ msg, stmt string }{
		{`Error 1064: You have an error in your SQL syntax near 'IDENTIFIED BY 's3cret' WITH' at line 1`, `CREATE USER u IDENTIFIED BY 's3cret' WITH`},
		{`syntax error at or near "PASSWORD 'it's me'"`, `ALTER USER u PASSWORD 'it''s me' x`},
		{`could not connect: password=hunter22 rejected`, `SELECT dblink_connect('host=h password=hunter22')`},
	} {
		got := Error(c.msg, c.stmt)
		for _, secret := range []string{"s3cret", "it's me", "hunter22"} {
			if strings.Contains(got, secret) {
				t.Errorf("%q kept %q", got, secret)
			}
		}
	}
	if got := Error("relation \"t\" does not exist", "SELECT * FROM t"); got != "relation \"t\" does not exist" {
		t.Errorf("an error without secrets changed: %q", got)
	}
	// MySQL quotes 80 characters near the error, cutting a long secret.
	long := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	msg := "Error 1064: near 'EXIST u IDENTIFIED BY '" + long[:57] + "' at line 1"
	if got := Error(msg, "CREATE USER IF NOT EXIST u IDENTIFIED BY '"+long+"'"); strings.Contains(got, long[:20]) {
		t.Errorf("the start of the secret was kept: %q", got)
	}
	// A connection string's password, quoted inside the string.
	if got := Error("password=hunter22 rejected", "SELECT dblink_connect('host=h password=''hunter22''')"); strings.Contains(got, "hunter22") {
		t.Errorf("kept the password: %q", got)
	}
	// The other arguments of a function taking credentials are not secrets.
	for msg, stmt := range map[string]string{
		"Table 'shop.orders' doesn't exist":  "SELECT * FROM mysql('db:3306', 'shop', 'orders', 'admin', 'pw')",
		"Cannot parse input (in CSV format)": "SELECT * FROM url('https://example.com/a.csv', 'CSV')",
	} {
		if got := Error(msg, stmt); got != msg {
			t.Errorf("%q became %q", msg, got)
		}
	}
	if got := Error("value >= 10 failed", "SELECT 1"); got != "value >= 10 failed" {
		t.Errorf("an operator was taken for an ACL password: %q", got)
	}
	for _, c := range []struct{ msg, stmt, secret, keep string }{
		{`Error 1064: near 'IDENTIFIED BY 'p\'ss w0rd' WITH' at line 1`, `CREATE USER u IDENTIFIED BY 'p\'ss w0rd' WITH`, "w0rd", "Error 1064: near"},
		{`Code: 62. DB::Exception: Syntax error at 'p'ss w0rd SETTING'`, `CREATE USER u IDENTIFIED WITH plaintext_password BY 'p\'ss w0rd' SETTING`, "w0rd", "Code: 62."},
		{`could not open postgres://u:urlpw77@h/db`, `ATTACH 'postgres://u:urlpw77@h/db' AS p`, "urlpw77", "could not open postgres://u:"},
		{`password "urlpw77" rejected`, `ATTACH 'postgres://u:urlpw77@h/db' AS p`, "urlpw77", "rejected"},
		{`could not connect to postgresql://admin:barepw88@db:5432`, `SELECT 1`, "barepw88", "@db:5432"},
		{`passwd=passwd77 rejected`, `ATTACH 'host=h passwd=passwd77' AS p`, "passwd77", "rejected"},
		{`invalid key azkey123== for acct`, `CREATE SECRET az (TYPE azure, CONNECTION_STRING 'AccountName=acct;AccountKey=azkey123==;')`, "azkey123", "for acct"},
		{`HTTP 401 with Bearer tok99999`, `CREATE SECRET h (TYPE http, EXTRA_HTTP_HEADERS MAP {'Authorization': 'Bearer tok99999'})`, "tok99999", "HTTP 401"},
		{`HTTP 403 with key apikey777`, `CREATE SECRET h (TYPE http, EXTRA_HTTP_HEADERS MAP {'X-Api-Key': 'apikey777'})`, "apikey777", "HTTP 403"},
		{`invalid token mdtok5555`, `ATTACH 'md:my_db?motherduck_token=mdtok5555'`, "mdtok5555", "invalid token"},
		{`Code: 36. DB::Exception: in MaterializedPostgreSQL('h:5432', 'db', 'u', 'engpw42')`, `CREATE TABLE t ENGINE = MaterializedPostgreSQL('h:5432', 'db', 'u', 'engpw42')`, "engpw42", "Code: 36."},
		{`ERR wrong number of arguments in SENTINEL SET mymaster auth-pass sentpw1`, `SENTINEL SET mymaster auth-pass sentpw1`, "sentpw1", "auth-pass"},
		{`ERR in SENTINEL CONFIG SET sentinel-pass sentpw3`, `SENTINEL CONFIG SET sentinel-pass sentpw3`, "sentpw3", "sentinel-pass"},
		{`ERR Error in ACL SETUSER modifier '<rmpw44': Syntax error`, `ACL SETUSER bob <rmpw44`, "rmpw44", "Syntax error"},
	} {
		if got := Error(c.msg, c.stmt); strings.Contains(got, c.secret) || !strings.Contains(got, c.keep) {
			t.Errorf("%q became %q", c.msg, got)
		}
	}
}

// A successful statement has no error to hide secrets in.
func TestErrorEmpty(t *testing.T) {
	if got := Error("", "ALTER USER app WITH PASSWORD 'hunter2'"); got != "" {
		t.Errorf("an empty error became %q", got)
	}
}

// (?i) folds ſ to s and K (Kelvin) to k: the word checks before the
// expressions must not skip them.
func TestSecretsFoldedLetters(t *testing.T) {
	for in, secret := range map[string]string{
		"SELECT 'host=h paſsword=foldpw1'":             "foldpw1",
		"SELECT 'md:db?motherducK_token=foldpw2'":      "foldpw2",
		"SELECT 'postgres://u:foldpw4@h/db' -- paſswd": "foldpw4",
	} {
		if out := Secrets(in); strings.Contains(out, secret) {
			t.Errorf("%q kept %q: %q", in, secret, out)
		}
	}
}

func largeInsert() string {
	var b strings.Builder
	b.WriteString("INSERT INTO customers (id, name, email, note) VALUES ")
	for i := 0; b.Len() < 600_000; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "(%d, 'Customer %d', 'customer%d@example.com', 'a note about customer %d that is long enough')", i, i, i, i)
	}
	return b.String()
}

func BenchmarkSecretsLargeInsert(b *testing.B) {
	s := largeInsert()
	b.ReportAllocs()
	for b.Loop() {
		Secrets(s)
	}
}
