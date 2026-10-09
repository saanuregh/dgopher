package redact

import (
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
}
