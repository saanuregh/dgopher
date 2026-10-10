package state

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dgopher/internal/audit"
	"dgopher/internal/store"
)

// legacyDir writes the files an older DGopher kept in .dgopher.
func legacyDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, data string) { os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600) }
	write("workspace.json", `{"files": ["a.sql"]}`)
	write("filters.json", `{"c/t": ["id > 1"]}`)
	write("colors.json", `{"c/t": ["red"]}`)
	var history strings.Builder
	for _, sql := range []string{"SELECT 1", "SELECT 2"} {
		line, _ := json.Marshal(store.HistoryEntry{SQL: sql})
		history.Write(append(line, '\n'))
	}
	write("history.jsonl", history.String()+"not json\n")
	// The old log's lines are what an export writes.
	src := open(t, t.TempDir())
	l, _ := audit.Open(src.SQL())
	for range 3 {
		l.Record(audit.Event{Kind: audit.KindConnect})
	}
	var log bytes.Buffer
	l.Export(&log)
	write(audit.FileName, log.String())
	return dir
}

func TestImportLegacy(t *testing.T) {
	dir := legacyDir(t)
	d := open(t, dir)
	var ws map[string][]string
	var filters map[string][]string
	if d.LoadJSON("workspace.json", &ws) != nil || ws["files"][0] != "a.sql" || d.LoadJSON("filters.json", &filters) != nil || d.LoadJSON("colors.json", new(any)) != nil {
		t.Fatalf("kv %v %v", ws, filters)
	}
	h, _ := d.History(0)
	if len(h) != 2 || h[0].SQL != "SELECT 2" {
		t.Fatalf("history %+v", h)
	}
	l, _ := audit.Open(d.SQL())
	if v, err := l.Verify(); err != nil || v.Broken != 0 || v.Entries != 3 {
		t.Fatalf("audit %+v %v", v, err)
	}
	for _, name := range []string{"workspace.json", "filters.json", "colors.json", "history.jsonl", "audit.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s left in place", name)
		}
		if _, err := os.Stat(filepath.Join(dir, name+".migrated")); err != nil {
			t.Errorf("%s not set aside: %v", name, err)
		}
	}
	// Reopened, nothing is imported twice, even with a file back.
	d.Close()
	os.WriteFile(filepath.Join(dir, "history.jsonl"), []byte(`{"sql":"again"}`+"\n"), 0o600)
	d = open(t, dir)
	if h, _ := d.History(0); len(h) != 2 {
		t.Fatalf("imported again: %d", len(h))
	}
}

// An import that cannot finish changes nothing: the files stay, and the
// project says why.
func TestImportLegacyFailureKeepsFiles(t *testing.T) {
	dir := legacyDir(t)
	os.WriteFile(filepath.Join(dir, "workspace.json"), []byte("{broken"), 0o600)
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "workspace.json") {
		t.Fatalf("err %v", err)
	}
	for _, name := range []string{"workspace.json", "history.jsonl", "audit.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	os.WriteFile(filepath.Join(dir, "workspace.json"), []byte("{}"), 0o600)
	d := open(t, dir)
	if h, _ := d.History(0); len(h) != 2 {
		t.Fatalf("after fixing: %d", len(h))
	}
}

// An old log that already set a damaged end aside holds a recovered
// entry of its own: it imports with the others, and the chain goes on.
func TestImportLegacyWithRecoveredEntry(t *testing.T) {
	dir := t.TempDir()
	src := open(t, t.TempDir())
	l, _ := audit.Open(src.SQL())
	for _, k := range []string{audit.KindConnect, audit.KindRecovered, audit.KindConnect} {
		l.Record(audit.Event{Kind: k})
	}
	var log bytes.Buffer
	l.Export(&log)
	os.WriteFile(filepath.Join(dir, audit.FileName), log.Bytes(), 0o600)
	d := open(t, dir)
	l2, _ := audit.Open(d.SQL())
	l2.Record(audit.Event{Kind: audit.KindDisconnect})
	if v, err := l2.Verify(); err != nil || !v.Intact() || v.Entries != 4 {
		t.Fatalf("%+v %v", v, err)
	}
}

// Windows opening a new project at once all open it.
func TestOpenConcurrently(t *testing.T) {
	dir := legacyDir(t)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := Open(dir)
			if err == nil {
				d.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	d := open(t, dir)
	if h, _ := d.History(0); len(h) != 2 {
		t.Fatalf("history %d", len(h))
	}
}

// An older workspace.json naming files outside the project, or under its
// .git or .dgopher, is imported without them.
func TestLegacyImportSkipsOutsidePaths(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".dgopher")
	os.MkdirAll(dir, 0o700)
	os.Symlink(root+"/..", filepath.Join(root, "up"))
	ws := `{"editors": [{"path": "../x"}, {"path": "/etc/passwd"}, {"path": ".git/config"}, {"path": ".dgopher/x"}, {"path": "up/x"}, {"path": "q/a.sql", "connection": "c"}],
		"active": 5, "dashboards": ["../d.json", "d.json"], "models": ["/m.json", "m.json"], "files": ["../f.sql", "f.sql"]}`
	os.WriteFile(filepath.Join(dir, "workspace.json"), []byte(ws), 0o600)
	d := open(t, dir)
	var got map[string]any
	if err := d.LoadJSON("workspace.json", &got); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	want := `{"active":0,"dashboards":["d.json"],"editors":[{"connection":"c","path":"q/a.sql"}],"files":["f.sql"],"models":["m.json"]}`
	if string(b) != want {
		t.Fatalf("imported %s", b)
	}
}

// A symlink whose target is missing is not inside the project, wherever it
// points, since writing through it would create its target.
func TestContainsDanglingSymlink(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(outside, "there"), 0o755)
	os.WriteFile(filepath.Join(dir, "real.sql"), nil, 0o644)
	os.Symlink(filepath.Join(outside, "planted.txt"), filepath.Join(dir, "notes.sql"))
	os.Symlink(filepath.Join(dir, "missing.sql"), filepath.Join(dir, "inward.sql"))
	os.Symlink("notes.sql", filepath.Join(dir, "chain.sql"))
	os.Symlink(filepath.Join(outside, "absent"), filepath.Join(dir, "gone"))
	os.Symlink(filepath.Join(outside, "there"), filepath.Join(dir, "away"))
	os.Symlink("real.sql", filepath.Join(dir, "alias.sql"))
	for path, want := range map[string]bool{
		"notes.sql": false, "inward.sql": false, "chain.sql": false, "gone/x.sql": false, "away/new.sql": false,
		filepath.Join(dir, "notes.sql"): false, "alias.sql": true, "real.sql": true, "deleted.sql": true, "a/b/c.sql": true,
	} {
		if got := Contains(dir, path); got != want {
			t.Errorf("Contains(%q) = %v", path, got)
		}
	}
}
