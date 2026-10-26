package db

import (
	"context"
	"testing"
)

// The strings are as each server's documentation and release notes
// print them (and, for PostgreSQL and MySQL, the test servers); the
// flavors' own test servers confirm theirs in their PRs.
func TestIdentifyFromVersionStrings(t *testing.T) {
	postgresFamily := func(version, timescale string) func() (Engine, string) {
		return func() (Engine, string) { return postgresServer(version, timescale) }
	}
	mysqlFamily := func(version, comment string) func() (Engine, string) {
		return func() (Engine, string) { return mysqlServer(version, comment) }
	}
	cases := []struct {
		name    string
		read    func() (Engine, string)
		engine  Engine
		version string
	}{
		{"postgres", postgresFamily("PostgreSQL 17.2 (Debian 17.2-1.pgdg120+1) on x86_64-pc-linux-gnu, compiled by gcc (Debian 12.2.0-14) 12.2.0, 64-bit", ""), Postgres, "17.2"},
		{"postgres from source", postgresFamily("PostgreSQL 16.4 on aarch64-apple-darwin23.4.0, compiled by Apple clang version 15.0.0, 64-bit", ""), Postgres, "16.4"},
		{"cockroachdb", postgresFamily("CockroachDB CCL v25.2.1 (x86_64-pc-linux-gnu, built 2025/06/02 18:44:43, go1.23.7 X:nocoverageredesign)", ""), CockroachDB, "25.2.1"},
		{"cockroachdb oss", postgresFamily("CockroachDB OSS v23.1.11 (x86_64-pc-linux-gnu, built 2023/09/27 01:53:43, go1.19.13)", ""), CockroachDB, "23.1.11"},
		{"yugabytedb", postgresFamily("PostgreSQL 15.2-YB-2.25.1.0-b0 on x86_64-pc-linux-gnu, compiled by clang version 17.0.6, 64-bit", ""), YugabyteDB, "2.25.1.0"},
		{"yugabytedb 11", postgresFamily("PostgreSQL 11.2-YB-2024.2.2.1-b0 on x86_64-pc-linux-gnu, compiled by clang version 17.0.6, 64-bit", ""), YugabyteDB, "2024.2.2.1"},
		{"timescaledb", postgresFamily("PostgreSQL 17.2 (Ubuntu 17.2-1.pgdg22.04+1) on x86_64-pc-linux-gnu, compiled by gcc, 64-bit", "2.17.2"), TimescaleDB, "2.17.2"},
		{"mysql", mysqlFamily("8.4.3", "MySQL Community Server - GPL"), MySQL, "8.4.3"},
		{"percona", mysqlFamily("8.0.36-28", "Percona Server (GPL), Release 28, Revision 47601f19"), MySQL, "8.0.36"},
		{"mariadb", mysqlFamily("11.4.4-MariaDB-ubu2404", "mariadb.org binary distribution"), MariaDB, "11.4.4"},
		{"mariadb by its comment", mysqlFamily("10.11.6", "mariadb.org binary distribution"), MariaDB, "10.11.6"},
		{"tidb", mysqlFamily("8.0.11-TiDB-v8.5.1", "TiDB Server (Apache License 2.0) Community Edition, MySQL 8.0 compatible"), TiDB, "8.5.1"},
		{"tidb cloud", mysqlFamily("8.0.11-TiDB-v7.5.2-serverless", ""), TiDB, "7.5.2"},
	}
	for _, c := range cases {
		e, v := c.read()
		if e != c.engine || v != c.version {
			t.Errorf("%s: got %q %q, want %q %q", c.name, e, v, c.engine, c.version)
		}
	}
}

// A flavor runs on its parent's rules, but is colored and opened by
// URL as itself; TestEngineFactsPinned and TestEngineRulesCover cover
// the rest.
func TestFlavorOverridesEngine(t *testing.T) {
	parents := map[Engine]Engine{
		CockroachDB: Postgres, YugabyteDB: Postgres, TimescaleDB: Postgres,
		MariaDB: MySQL, TiDB: MySQL,
	}
	for flavor, parent := range parents {
		if flavor.Base() != parent || parent.Base() != parent {
			t.Errorf("%q: base %q, its parent's %q", flavor, flavor.Base(), parent.Base())
		}
		if flavor.ColorHex() == parent.ColorHex() {
			t.Errorf("%q is colored as %q", flavor, parent)
		}
		for _, scheme := range rulesOf(flavor).URLSchemes() {
			cfg, err := ParseURL(scheme + "://u@h/d")
			if err != nil || cfg.Engine != flavor {
				t.Errorf("%s:// opens %q, %v; want %q", scheme, cfg.Engine, err, flavor)
			}
		}
	}
}

// Identify tells the test servers for what they are, and finds a
// PostgreSQL server for one, whatever the connection says it is.
func TestIntegrationIdentify(t *testing.T) {
	integration(t)
	ctx := context.Background()
	for _, f := range fixtures(t) {
		e, v, err := open(t, f).Identify(ctx)
		if err != nil || e != f.cfg.Engine {
			t.Errorf("%s: identified %q %q, %v", f.cfg.Name, e, v, err)
		}
		if (f.cfg.Engine == Postgres || f.cfg.Engine == MySQL) && v == "" {
			t.Errorf("%s: no version", f.cfg.Name)
		}
	}
	cfg := fixtures(t)[0].cfg
	cfg.Engine = CockroachDB
	d, err := Open(ctx, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if e, v, err := d.Identify(ctx); err != nil || e != Postgres || v == "" {
		t.Errorf("PostgreSQL set as CockroachDB: identified %q %q, %v", e, v, err)
	}
}
