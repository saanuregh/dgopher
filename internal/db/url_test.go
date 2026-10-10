package db

import (
	"reflect"
	"testing"
)

func TestParseURL(t *testing.T) {
	cases := []struct {
		in   string
		want Config
	}{
		{"postgres://ada:p%40ss@db.example.com:6543/billing?sslmode=verify-full",
			Config{Engine: Postgres, Host: "db.example.com", Port: 6543, User: "ada", Password: "p@ss", Database: "billing", TLS: TLSVerifyFull, Name: "db.example.com/billing"}},
		{"mysql://root@localhost/shop", Config{Engine: MySQL, Host: "localhost", User: "root", Database: "shop", Name: "localhost/shop"}},
		{"clickhouse://default:x@ch:9440?secure=true&database=logs", Config{Engine: ClickHouse, Host: "ch", Port: 9440, User: "default", Password: "x", Database: "logs", TLS: TLSVerifyFull, Name: "ch/logs"}},
		{"rediss://:token@cache:6380/2", Config{Engine: Redis, Host: "cache", Port: 6380, Password: "token", Database: "2", TLS: TLSVerifyFull, Name: "cache/2"}},
		{"/data/app.sqlite", Config{Engine: SQLite, Database: "/data/app.sqlite", Name: "app.sqlite"}},
		{"~/lake.duckdb", Config{Engine: DuckDB, Database: "~/lake.duckdb", Name: "lake.duckdb"}},
	}
	for _, c := range cases {
		got, err := ParseURL(c.in)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseURL(%q) = %+v, %v; want %+v", c.in, got, err, c.want)
		}
	}
	if _, err := ParseURL("ftp://x"); err == nil {
		t.Error("an ftp URL was taken")
	}
}

func TestNormalizeEnvironment(t *testing.T) {
	for in, want := range map[Environment]Environment{"": Development, "Dev": Development, "STAGING": Staging, "qa": Staging,
		"prod": Production, "Production": Production, "prd-eu": Production, "whatever": Production} {
		if got := NormalizeEnvironment(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// Each scheme's own TLS parameters choose the mode, in the meaning its
// tools give them; a value the app does not know, a parameter of another
// scheme, or two that disagree are an error rather than ignored.
func TestParseURLTLS(t *testing.T) {
	for in, want := range map[string]TLSMode{
		"postgres://h/d":                                  "",
		"postgres://h/d?sslmode=disable":                  TLSDisable,
		"postgres://h/d?sslmode=allow":                    TLSPrefer,
		"postgres://h/d?sslmode=prefer":                   TLSPrefer,
		"postgres://h/d?sslmode=require":                  TLSRequire,
		"postgres://h/d?sslmode=verify-ca":                TLSVerifyFull,
		"postgresql://h/d?sslmode=verify-full":            TLSVerifyFull,
		"postgres://h/d?ssl=true":                         TLSRequire,
		"postgres://h/d?sslmode=require&ssl=true":         TLSRequire,
		"postgres://h/d?application_name=x":               "",
		"mysql://h/d?ssl-mode=DISABLED":                   TLSDisable,
		"mysql://h/d?ssl-mode=preferred":                  TLSPrefer,
		"mysql://h/d?ssl-mode=REQUIRED":                   TLSRequire,
		"mysql://h/d?ssl-mode=VERIFY_CA":                  TLSVerifyFull,
		"mysql://h/d?ssl-mode=VERIFY_IDENTITY":            TLSVerifyFull,
		"mysql://h/d?tls=true":                            TLSVerifyFull,
		"mysql://h/d?tls=skip-verify":                     TLSRequire,
		"mysql://h/d?tls=preferred":                       TLSPrefer,
		"mysql://h/d?tls=false":                           TLSDisable,
		"mysql://h/d?ssl=true":                            TLSRequire,
		"mysql://h/d?ssl=false":                           TLSDisable,
		"mariadb://h/d?ssl-mode=REQUIRED&tls=skip-verify": TLSRequire,
		"clickhouse://h/d?secure=true":                    TLSVerifyFull,
		"clickhouse://h/d?secure=1":                       TLSVerifyFull,
		"clickhouse://h/d?secure":                         TLSVerifyFull,
		"clickhouse://h/d?secure=true&skip_verify=true":   TLSRequire,
		"clickhouse://h/d?secure=true&skip_verify":        TLSRequire,
		"clickhouse://h/d?secure=true&skip_verify=false":  TLSVerifyFull,
		"clickhouse://h/d?secure=false":                   TLSDisable,
		"clickhouse://h/d?secure=0":                       TLSDisable,
		"redis://h/0":                                     "",
		"rediss://h/0":                                    TLSVerifyFull,
		"rediss://h:6380/0":                               TLSVerifyFull,
		"rediss://h:6380/0?skip_verify":                   TLSRequire,
		"rediss://h:6380/0?skip_verify=1":                 TLSRequire,
	} {
		got, err := ParseURL(in)
		if err != nil || got.TLS != want {
			t.Errorf("ParseURL(%q): TLS %q, %v; want %q", in, got.TLS, err, want)
		}
	}
	for _, in := range []string{
		"postgres://h/d?sslmode=verify-everything",
		"postgres://h/d?sslmode=disable&ssl=true",
		"postgres://h/d?ssl-mode=REQUIRED",
		"postgres://h/d?sslmode=require&sslmode=disable",
		"mysql://h/d?ssl-mode=SOMETIMES",
		"mysql://h/d?tls=custom-config",
		"mysql://h/d?ssl-mode=DISABLED&tls=true",
		"mysql://h/d?sslmode=require",
		"clickhouse://h/d?secure=maybe",
		"clickhouse://h/d?sslmode=require",
		"redis://h/0?ssl=true",
		"clickhouse://h/d?skip_verify=true",
		"clickhouse://h/d?secure=false&skip_verify",
		"clickhouse://h/d?secure&skip_verify=maybe",
		"redis://h/0?skip_verify",
		"clickhouse://h/d?secure&tls_server_name=other",
		"rediss://h/0?tls_server_name=other",
	} {
		if got, err := ParseURL(in); err == nil {
			t.Errorf("ParseURL(%q) took it: TLS %q", in, got.TLS)
		}
	}
}
