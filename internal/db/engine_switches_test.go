package db

import (
	"go/ast"
	"go/parser"
	gotoken "go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// engineComparison matches a switch case on, or a comparison with, one
// of the Engine constants declared in config.go, new engines included.
func engineComparison(t *testing.T) *regexp.Regexp {
	f, err := parser.ParseFile(gotoken.NewFileSet(), "config.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, decl := range f.Decls {
		g, ok := decl.(*ast.GenDecl)
		if !ok || g.Tok != gotoken.CONST {
			continue
		}
		for _, spec := range g.Specs {
			v := spec.(*ast.ValueSpec)
			if typ, ok := v.Type.(*ast.Ident); ok && typ.Name == "Engine" {
				for _, n := range v.Names {
					names = append(names, n.Name)
				}
			}
		}
	}
	if len(names) < len(Engines()) {
		t.Fatalf("found Engine constants %v in config.go, fewer than Engines() %v", names, Engines())
	}
	alt := strings.Join(names, "|")
	return regexp.MustCompile(`case (db\.)?(` + alt + `)\b|[!=]= (db\.)?(` + alt + `)\b`)
}

// keptEngineComparisons are the comparisons with an engine that are not
// engine behavior: a field only one engine has (Redis's nodes,
// ClickHouse's protocol), Redis not being SQL, a file of an engine meant
// by name, or which engine a server identified as. Each is a file under internal/ and its line, trimmed.
var keptEngineComparisons = []struct{ file, line string }{
	{"db/timescaledb.go", "if err != nil || e != Postgres {"},
	{"app/activity.go", `redis := t.conn.Config.Engine == db.Redis`},
	{"app/connform.go", `if cfg.Engine == db.ClickHouse {`},
	{"app/connform.go", `case cfg.Engine != db.Redis:`},
	{"app/connform.go", `if engine == db.ClickHouse {`},
	{"app/connform.go", `if engine == db.ClickHouse {`},
	{"app/connform.go", `if engine == db.Redis {`},
	{"app/connform.go", `if f.port == strconv.Itoa(old.DefaultPort()) || old == db.ClickHouse && isClickHouseDefaultPort(f.port, chProtocols...) {`},
	{"app/connform.go", `if e == db.DuckDB {`},
	{"app/connform.go", `if cfg.Engine == db.Redis && strings.TrimSpace(cfg.Redis.Nodes) != "" {`},
	{"app/connform.go", `if cfg.Engine == db.Redis {`},
	{"app/dropfiles.go", `if c.Config.Engine == db.DuckDB && c.Config.Database == ":memory:" {`},
	{"app/sample.go", `if cn.Config.Engine == db.SQLite && cn.Config.Database == path {`},
	{"db/config.go", `if c.Engine != ClickHouse {`},
	{"db/config.go", `if c.Engine == Redis && c.Database != "" {`},
	{"db/config.go", `case c.Engine != ClickHouse:`},
	{"db/config.go", `if c.Engine == Redis {`},
	{"db/config.go", `case c.Engine == Redis && c.Redis.Mode == RedisSentinel:`},
	{"db/config.go", `case c.Engine == ClickHouse:`},
	{"db/redis.go", `if cfg.Engine != Redis {`},
	{"db/url.go", `if _, ok := q["skip_verify"]; ok && (cfg.Engine == ClickHouse || cfg.Engine == Redis) {`},
	{"db/url.go", `if cfg.Engine == ClickHouse && q.Get("database") != "" {`},
	{"db/url.go", `if e == ClickHouse {`},
}

// Engine behavior lives in each engine's rules (engine.go), so a new
// engine that misses a hook does not compile: a comparison with an engine
// elsewhere would let it fall into another engine's answer unseen.
// internal/sqltext is left out: its Postgres, MySQL… are its own dialects.
func TestNoEngineSwitches(t *testing.T) {
	comparison := engineComparison(t)
	kept := map[[2]string]int{}
	for _, k := range keptEngineComparisons {
		kept[[2]string{k.file, k.line}]++
	}
	root := filepath.Join("..")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "sqltext" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if !comparison.MatchString(line) {
				continue
			}
			key := [2]string{rel, strings.TrimSpace(line)}
			if kept[key] == 0 {
				t.Errorf("%s:%d compares with an engine; give the engine's rules a hook instead: %s", rel, i+1, key[1])
				continue
			}
			kept[key]--
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for k, n := range kept {
		if n > 0 {
			t.Errorf("%s no longer has %q: drop it from keptEngineComparisons", k[0], k[1])
		}
	}
}
