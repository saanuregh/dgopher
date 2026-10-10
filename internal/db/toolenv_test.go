package db

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A backup tool connects only as the app tells it: the user's PG* and
// MYSQL_* variables, and MySQL's option files, choose nothing.
func TestToolsIgnoreClientSettings(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake tools are shell scripts")
	}
	dir := t.TempDir()
	for _, tool := range []string{"pg_dump", "mysqldump"} {
		script := "#!/bin/sh\necho \"args: $*\" >&2\nenv >&2\n"
		if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PGPASSWORD", "leaked-pg")
	t.Setenv("PGHOSTADDR", "203.0.113.9")
	t.Setenv("PGSERVICE", "elsewhere")
	t.Setenv("MYSQL_PWD", "leaked-my")
	t.Setenv("LIBMYSQL_ENABLE_CLEARTEXT_PLUGIN", "1")
	for _, run := range []ToolRun{
		{Argv: []string{"pg_dump", "--dbname", "x"}, Env: []string{"PGSSLMODE=require"}},
		{Argv: []string{"mysqldump", "--databases", "--", "x"}},
		{Argv: []string{"mysqldump", "--databases", "--", "x"}, MySQLDefaults: "[client]\npassword=\"p\"\n"},
	} {
		var out []string
		if err := RunTool(context.Background(), run, func(l string) { out = append(out, l) }); err != nil {
			t.Fatalf("%v: %v", run.Argv, err)
		}
		text := strings.Join(out, "\n")
		for _, leak := range []string{"leaked-pg", "203.0.113.9", "PGSERVICE", "leaked-my", "LIBMYSQL_ENABLE_CLEARTEXT_PLUGIN"} {
			if strings.Contains(text, leak) {
				t.Errorf("%v: the tool got %s:\n%s", run.Argv, leak, text)
			}
		}
		if run.Argv[0] == "pg_dump" && !strings.Contains(text, "PGSSLMODE=require") {
			t.Errorf("pg_dump lost the app's own setting:\n%s", text)
		}
		if run.Argv[0] == "mysqldump" {
			args := out[0]
			if run.MySQLDefaults != "" && !strings.HasPrefix(args, "args: --defaults-file=") ||
				run.MySQLDefaults == "" && !strings.HasPrefix(args, "args: --no-defaults") {
				t.Errorf("mysqldump reads other option files: %s", args)
			}
		}
	}
}
