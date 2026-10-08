package db

import "testing"

func TestParseURL(t *testing.T) {
	cases := []struct {
		in   string
		want Config
	}{
		{"postgres://ada:p%40ss@db.example.com:6543/billing?sslmode=verify-full",
			Config{Engine: Postgres, Host: "db.example.com", Port: 6543, User: "ada", Password: "p@ss", Database: "billing", TLS: TLSVerifyFull, Name: "db.example.com/billing"}},
		{"mysql://root@localhost/shop", Config{Engine: MySQL, Host: "localhost", User: "root", Database: "shop", Name: "localhost/shop"}},
		{"clickhouse://default:x@ch:9440?secure=true&database=logs", Config{Engine: ClickHouse, Host: "ch", Port: 9440, User: "default", Password: "x", Database: "logs", TLS: TLSRequire, Name: "ch/logs"}},
		{"rediss://:token@cache:6380/2", Config{Engine: Redis, Host: "cache", Port: 6380, Password: "token", Database: "2", TLS: TLSRequire, Name: "cache/2"}},
		{"/data/app.sqlite", Config{Engine: SQLite, Database: "/data/app.sqlite", Name: "app.sqlite"}},
		{"~/lake.duckdb", Config{Engine: DuckDB, Database: "~/lake.duckdb", Name: "lake.duckdb"}},
	}
	for _, c := range cases {
		got, err := ParseURL(c.in)
		if err != nil || got != c.want {
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
