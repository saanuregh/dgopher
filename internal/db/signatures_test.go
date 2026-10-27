package db

import (
	"context"
	"testing"
)

func TestDuckDBSignatures(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, Config{Name: "duck", Engine: DuckDB, Database: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sigs, err := duckdbDialect{}.Signatures(ctx, d.Catalog())
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]Signature{}
	for _, s := range sigs {
		if _, ok := found[s.Name]; !ok {
			found[s.Name] = s
		}
	}
	for _, name := range []string{"round", "abs"} {
		s, ok := found[name]
		if !ok || s.Args == "" || s.Description == "" || s.Result == "" {
			t.Errorf("%s: %+v", name, s)
		}
	}
	if !found["sum"].Aggregate {
		t.Errorf("sum is not an aggregate: %+v", found["sum"])
	}
	t.Logf("%d signatures; round %+v", len(sigs), found["round"])
}

func TestDuckDBArgs(t *testing.T) {
	for _, c := range []struct {
		names, types []any
		varargs      string
		want         string
	}{
		{[]any{"x", "y"}, []any{"INTEGER", "VARCHAR"}, "", "x INTEGER, y VARCHAR"},
		{[]any{"x"}, []any{"INTEGER", "VARCHAR"}, "", "x INTEGER, VARCHAR"},
		{nil, []any{"INTEGER"}, "ANY", "INTEGER, ANY..."},
		{[]any{"a", "b"}, nil, "", "a, b"},
		{nil, nil, "", ""},
	} {
		if got := duckdbArgs(c.names, c.types, c.varargs); got != c.want {
			t.Errorf("%v %v %q: %q, want %q", c.names, c.types, c.varargs, got, c.want)
		}
	}
}

func TestClickHouseArgsFromSyntax(t *testing.T) {
	for in, want := range map[string]string{
		"round(x[, N])":                 "x[, N]",
		"toDateTime(expr[, time_zone])": "expr[, time_zone]",
		"quantile(level)(expr)":         "expr",
		"now()":                         "",
		"no parentheses":                "",
		"":                              "",
		"f(a, g(b))":                    "a, g(b)",
	} {
		if got := clickhouseArgs(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestIntegrationSignatures(t *testing.T) {
	integration(t)
	ctx := context.Background()
	for _, c := range []struct {
		cfg  Config
		name string
	}{
		{Config{Name: "pg", Engine: Postgres, Host: "127.0.0.1", Port: 15432, User: "postgres", Password: "dgopher", Database: "postgres"}, "round"},
		{cockroachdbConfig, "round"},
		{Config{Name: "ch", Engine: ClickHouse, Host: "127.0.0.1", Port: 19000, User: "default", Password: "dgopher", Database: "default"}, "round"},
	} {
		t.Run(c.cfg.Name, func(t *testing.T) {
			skipUnlessRunning(t, c.cfg)
			d, err := Open(ctx, c.cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			sigs, err := d.Dialect.(SignatureReader).Signatures(ctx, d.Catalog())
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range sigs {
				if s.Name == c.name && s.Args != "" {
					return
				}
			}
			t.Fatalf("no %s with arguments in %d signatures", c.name, len(sigs))
		})
	}
}
