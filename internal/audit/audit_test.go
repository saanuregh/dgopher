package audit_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dgopher/internal/audit"
	"dgopher/internal/state"
)

func openState(t *testing.T, dir string) *state.DB {
	t.Helper()
	d, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func openLog(t *testing.T, d *state.DB) *audit.Log {
	t.Helper()
	l, err := audit.Open(d.SQL())
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// Two windows on one project write one chain.
func TestDBChainAcrossWindows(t *testing.T) {
	dir := t.TempDir()
	a, b := openLog(t, openState(t, dir)), openLog(t, openState(t, dir))
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := a
			if i%2 == 1 {
				l = b
			}
			if err := l.Record(audit.Event{Kind: audit.KindStatement, Statement: fmt.Sprint(i)}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	v, err := a.Verify()
	if err != nil || v.Broken != 0 || v.Entries != 20 {
		t.Fatalf("%+v %v", v, err)
	}
	if seq, hash := b.Head(); seq != 20 || hash == "" {
		t.Fatalf("head %d %q", seq, hash)
	}
	events, _ := a.Read(3)
	if len(events) != 3 || events[0].Seq != 20 || events[2].Seq != 18 {
		t.Fatalf("read %+v", events)
	}
}

func TestDBTampering(t *testing.T) {
	for name, tamper := range map[string]string{
		"changed":   `UPDATE audit SET event = replace(event, 'SET a = 3', 'SET a = 9') WHERE seq = 3`,
		"removed":   `DELETE FROM audit WHERE seq = 3`,
		"first":     `DELETE FROM audit WHERE seq = 1`,
		"reordered": `UPDATE audit SET id = id + 100 WHERE seq = 2`,
	} {
		t.Run(name, func(t *testing.T) {
			d := openState(t, t.TempDir())
			l := openLog(t, d)
			for i := range 5 {
				l.Record(audit.Event{Kind: audit.KindStatement, Statement: fmt.Sprintf("UPDATE t SET a = %d", i+1)})
			}
			if _, err := d.SQL().Exec(tamper); err != nil {
				t.Fatal(err)
			}
			v, err := l.Verify()
			if err != nil || v.Broken == 0 || v.Reason == "" {
				t.Fatalf("not found: %+v %v", v, err)
			}
		})
	}
}

func TestDBExportVerifies(t *testing.T) {
	d := openState(t, t.TempDir())
	l := openLog(t, d)
	for i := range 4 {
		l.Record(audit.Event{Kind: audit.KindConnect, Detail: fmt.Sprint(i)})
	}
	var buf bytes.Buffer
	if err := l.Export(&buf); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "x.jsonl")
	os.WriteFile(path, buf.Bytes(), 0o600)
	if v, err := audit.VerifyFile(path); err != nil || v.Broken != 0 || v.Entries != 4 {
		t.Fatalf("%+v %v", v, err)
	}
	os.WriteFile(path, bytes.Replace(buf.Bytes(), []byte(`"detail":"2"`), []byte(`"detail":"7"`), 1), 0o600)
	if v, _ := audit.VerifyFile(path); v.Broken != 3 {
		t.Fatalf("tampered export: %+v", v)
	}
}

// legacyLog writes n entries as an older version kept them, in
// .dgopher/audit.jsonl, which an export reproduces line for line.
func legacyLog(t *testing.T, n int, statement func(int) string) (dir string, data []byte) {
	t.Helper()
	l := openLog(t, openState(t, t.TempDir()))
	for i := range n {
		l.Record(audit.Event{Kind: audit.KindStatement, Statement: statement(i)})
	}
	var buf bytes.Buffer
	l.Export(&buf)
	dir = t.TempDir()
	os.WriteFile(filepath.Join(dir, audit.FileName), buf.Bytes(), 0o600)
	return dir, buf.Bytes()
}

// A log of files, rotated and with a write cut short at its end, comes
// over as it was, and goes on from its last entry.
func TestImportFiles(t *testing.T) {
	old, data := legacyLog(t, 6, func(i int) string { return fmt.Sprint(i) })
	// Rotated: the first three entries in a file of their own.
	lines := strings.SplitAfter(string(data), "\n")
	os.WriteFile(filepath.Join(old, "audit-000000000003.jsonl"), []byte(strings.Join(lines[:3], "")), 0o600)
	os.WriteFile(filepath.Join(old, audit.FileName), []byte(strings.Join(lines[3:], "")+`{"seq":7,"ti`), 0o600)

	d := openState(t, t.TempDir())
	tx, _ := d.SQL().Begin()
	files, n, err := audit.ImportFiles(tx, old)
	if err != nil || n != 7 || len(files) != 2 {
		t.Fatalf("%v %d %v", files, n, err)
	}
	tx.Commit()
	l := openLog(t, d)
	l.Record(audit.Event{Kind: audit.KindDisconnect})
	v, err := l.Verify()
	if err != nil || v.Broken != 0 || v.Entries != 8 {
		t.Fatalf("%+v %v", v, err)
	}
	events, _ := l.Read(2)
	if events[1].Kind != audit.KindRecovered || !strings.Contains(events[1].Detail, "damaged bytes after entry 6") {
		t.Fatalf("recovered %+v", events[1])
	}
}

// A chain already broken in the files stays broken, at the same entry.
func TestImportKeepsBrokenChain(t *testing.T) {
	old, data := legacyLog(t, 4, func(i int) string { return fmt.Sprintf("x%d", i) })
	path := filepath.Join(old, audit.FileName)
	os.WriteFile(path, bytes.Replace(data, []byte(`"x1"`), []byte(`"y1"`), 1), 0o600)
	before, _ := audit.VerifyFile(path)
	d := openState(t, t.TempDir())
	tx, _ := d.SQL().Begin()
	if _, _, err := audit.ImportFiles(tx, old); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	after, _ := openLog(t, d).Verify()
	if before.Broken != 2 || after.Broken != 2 || after.Reason != before.Reason {
		t.Fatalf("files %+v, state %+v", before, after)
	}
}

// An entry whose number was set to 0, or removed, is not the end of a
// sound chain.
func TestDBTamperedSeqZero(t *testing.T) {
	for _, tamper := range []string{
		`UPDATE audit SET event = json_set(event, '$.seq', 0) WHERE seq = 2`,
		`UPDATE audit SET event = json_remove(event, '$.seq') WHERE seq = 2`,
	} {
		d := openState(t, t.TempDir())
		l := openLog(t, d)
		for range 3 {
			l.Record(audit.Event{Kind: audit.KindConnect})
		}
		d.SQL().Exec(tamper)
		if v, err := l.Verify(); err != nil || v.Intact() {
			t.Fatalf("%s: %+v %v", tamper, v, err)
		}
	}
}

// A last entry written whole, but for its line end, is an entry.
func TestImportLastLineWithoutNewline(t *testing.T) {
	old, data := legacyLog(t, 2, func(i int) string { return fmt.Sprint(i) })
	os.WriteFile(filepath.Join(old, audit.FileName), bytes.TrimSuffix(data, []byte("\n")), 0o600)
	d := openState(t, t.TempDir())
	tx, _ := d.SQL().Begin()
	if _, n, err := audit.ImportFiles(tx, old); err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	tx.Commit()
	if events, _ := openLog(t, d).Read(0); len(events) != 2 || events[0].Kind == audit.KindRecovered {
		t.Fatalf("%+v", events)
	}
}
