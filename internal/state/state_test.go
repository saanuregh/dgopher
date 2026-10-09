package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"dgopher/internal/store"
)

func open(t *testing.T, dir string) *DB {
	t.Helper()
	d, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestOpenCreatesPrivateFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".dgopher")
	d := open(t, dir)
	info, err := os.Stat(d.Path())
	if err != nil || info.Mode().Perm() != 0o600 || filepath.Dir(d.Path()) != dir || d.Dir() != dir {
		t.Fatalf("file %v %v", info, err)
	}
	var mode string
	var version int
	d.SQL().QueryRow("PRAGMA journal_mode").Scan(&mode)
	d.SQL().QueryRow("PRAGMA user_version").Scan(&version)
	if mode != "wal" || version != len(migrations) {
		t.Fatalf("journal %q, version %d", mode, version)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	d := open(t, t.TempDir())
	var v map[string]int
	if err := d.LoadJSON("w", &v); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("absent: %v", err)
	}
	d.SaveJSON("w", map[string]int{"a": 1})
	d.SaveJSON("w", map[string]int{"a": 2})
	if err := d.LoadJSON("w", &v); err != nil || v["a"] != 2 {
		t.Fatalf("%v %v", v, err)
	}
}

func TestHistoryTrimAndOrder(t *testing.T) {
	d := open(t, t.TempDir())
	now := time.Now()
	// All but the last two in one transaction: each append flushes to disk.
	tx, _ := d.SQL().Begin()
	for i := range historyMax - 1 {
		insertHistory(tx, store.HistoryEntry{Time: now.Add(time.Duration(i) * time.Second), SQL: fmt.Sprint(i)})
	}
	tx.Commit()
	for i := historyMax - 1; i <= historyMax; i++ {
		if err := d.AppendHistory(store.HistoryEntry{Time: now.Add(time.Duration(i) * time.Second), SQL: fmt.Sprint(i), Duration: time.Millisecond, Rows: int64(i), Error: "e"}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := d.History(0)
	if err != nil || len(all) != historyKeep || all[0].SQL != fmt.Sprint(historyMax) || all[len(all)-1].SQL != fmt.Sprint(historyMax+1-historyKeep) {
		t.Fatalf("%d entries, newest %q, oldest %q, %v", len(all), all[0].SQL, all[len(all)-1].SQL, err)
	}
	if e := all[0]; e.Duration != time.Millisecond || e.Rows != int64(historyMax) || e.Error != "e" || !e.Time.Equal(now.Add(time.Duration(historyMax)*time.Second)) {
		t.Fatalf("entry %+v", e)
	}
	if two, _ := d.History(2); len(two) != 2 {
		t.Fatalf("limit: %d", len(two))
	}
	if err := d.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	if h, _ := d.History(0); len(h) != 0 {
		t.Fatalf("after clear: %d", len(h))
	}
}

func TestReopenKeepsData(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	d.SaveJSON("w", []string{"x"})
	d.AppendHistory(store.HistoryEntry{SQL: "SELECT 1"})
	d.Close()
	d = open(t, dir)
	var v []string
	h, _ := d.History(0)
	if d.LoadJSON("w", &v) != nil || len(v) != 1 || len(h) != 1 {
		t.Fatalf("%v %v", v, h)
	}
}

func TestHistoryConcurrent(t *testing.T) {
	d := open(t, t.TempDir())
	var wg sync.WaitGroup
	for g := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 10 {
				if err := d.AppendHistory(store.HistoryEntry{SQL: fmt.Sprint(g, i)}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	h, _ := d.History(0)
	seen := map[string]bool{}
	for _, e := range h {
		seen[e.SQL] = true
	}
	if len(h) != 200 || len(seen) != 200 {
		t.Fatalf("got %d entries, %d unique", len(h), len(seen))
	}
}

// An error kept in the history may quote the statement's secret.
func TestHistoryRedactsErrors(t *testing.T) {
	d := open(t, t.TempDir())
	err := `Error 1064: You have an error in your SQL syntax near 'IDENTIFIED BY 's3cret' WITH' at line 1`
	if e := d.AppendHistory(store.HistoryEntry{SQL: "CREATE USER u IDENTIFIED BY 's3cret' WITH", Error: err}); e != nil {
		t.Fatal(e)
	}
	got, e := d.History(1)
	if e != nil || len(got) != 1 {
		t.Fatal(got, e)
	}
	if strings.Contains(got[0].SQL, "s3cret") || strings.Contains(got[0].Error, "s3cret") {
		t.Fatalf("the secret was kept: %+v", got[0])
	}
}

// The app's UI state file is its own, private, and keeps what is saved in
// it across opens.
func TestOpenUI(t *testing.T) {
	dir := t.TempDir()
	d, err := OpenUI(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SaveJSON("sidebar.width", 312.5); err != nil {
		t.Fatal(err)
	}
	d.Close()
	d, err = OpenUI(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var width float64
	if err := d.LoadJSON("sidebar.width", &width); err != nil || width != 312.5 {
		t.Fatalf("width %v, %v", width, err)
	}
	if err := d.LoadJSON("absent", &width); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an absent key: %v", err)
	}
	if d.Path() != filepath.Join(dir, UIFile) {
		t.Fatalf("path %s", d.Path())
	}
	if info, err := os.Stat(d.Path()); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, %v", info.Mode(), err)
	}
}
