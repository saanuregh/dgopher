package query

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"dgopher/internal/db"
	"dgopher/internal/testutil"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type fakeWatch struct {
	path   string
	events chan []mygo.FileEvent
	closed chan struct{}
	once   sync.Once
}

func (w *fakeWatch) Next(ctx context.Context) ([]mygo.FileEvent, error) {
	select {
	case e := <-w.events:
		return e, nil
	case <-w.closed:
		return nil, io.EOF
	}
}

func (w *fakeWatch) Close() error { w.once.Do(func() { close(w.closed) }); return nil }

func (w *fakeWatch) isClosed() bool {
	select {
	case <-w.closed:
		return true
	default:
		return false
	}
}

// fakeWatches replaces the file watching with fakes, each sent on the
// returned channel when started.
func fakeWatches(t *testing.T) chan *fakeWatch {
	started := make(chan *fakeWatch, 16)
	was := WatchFiles
	WatchFiles = func(ctx context.Context, path string, opts mygo.FileWatchOptions) (FileWatch, error) {
		w := &fakeWatch{path: path, events: make(chan []mygo.FileEvent, 4), closed: make(chan struct{})}
		started <- w
		return w, nil
	}
	t.Cleanup(func() { WatchFiles = was })
	return started
}

// openFileEditor opens an editor on a file holding text, and returns the
// watch of that file.
func openFileEditor(t *testing.T, text string) (*ui.Tester, *Tab, *fakeWatch) {
	t.Helper()
	started := fakeWatches(t)
	_, tt, cn, q := openEditor(t, db.Config{ID: "lite", Name: "lite", Engine: db.SQLite, Database: ":memory:"}, 1000, 700, "")
	path := filepath.Join(cn.Project.Dir, "watched.sql")
	os.WriteFile(path, []byte(text), 0o644)
	q.Editor.SetFileText(text)
	q.Path, q.Saved, q.Typed = path, text, text
	var w *fakeWatch
	testutil.WaitFor(t, tt, "the watch", func() bool {
		select {
		case w = <-started:
		default:
		}
		return w != nil
	})
	if w.path != path {
		t.Fatalf("watched %q, not %q", w.path, path)
	}
	return tt, q, w
}

func TestOpenFileReloadsWhenClean(t *testing.T) {
	tt, q, w := openFileEditor(t, "SELECT 1;")
	os.WriteFile(q.Path, []byte("SELECT 'pulled';"), 0o644)
	w.events <- []mygo.FileEvent{{Op: mygo.FileWrite, Path: "."}}
	testutil.WaitFor(t, tt, "the reload", func() bool { return q.Editor.FileText() == "SELECT 'pulled';" })
	if q.Saved != "SELECT 'pulled';" || q.DiskConflict != "" {
		t.Fatalf("after reload: saved %q, conflict %q", q.Saved, q.DiskConflict)
	}
	q.Close()
	if !w.isClosed() {
		t.Fatal("the watch outlived the tab")
	}
}

func TestOpenFileAsksWhenEdited(t *testing.T) {
	tt, q, w := openFileEditor(t, "SELECT 1;")
	q.Editor.SetFileText("SELECT 1; -- mine")
	os.WriteFile(q.Path, []byte("SELECT 'pulled';"), 0o644)
	w.events <- []mygo.FileEvent{{Op: mygo.FileWrite, Path: "."}}
	testutil.WaitFor(t, tt, "the bar", func() bool { return q.DiskConflict != "" })
	if q.Editor.FileText() != "SELECT 1; -- mine" {
		t.Fatalf("unsaved edits discarded: %q", q.Editor.FileText())
	}
	if disk, _ := os.ReadFile(q.Path); string(disk) != "SELECT 'pulled';" {
		t.Fatalf("the changed file was overwritten: %q", disk)
	}
	testutil.Settled(t, tt, "Reload") // the bar grown in
	if err := tt.Click("Reload"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if q.Editor.FileText() != "SELECT 'pulled';" || q.DiskConflict != "" {
		t.Fatalf("after reload: %q %q", q.Editor.FileText(), q.DiskConflict)
	}

	q.Editor.SetFileText("SELECT 2; -- mine")
	os.WriteFile(q.Path, []byte("SELECT 'again';"), 0o644)
	w.events <- []mygo.FileEvent{{Op: mygo.FileWrite, Path: "."}}
	testutil.WaitFor(t, tt, "the bar", func() bool { return q.DiskConflict != "" })
	if err := tt.Click("Keep Mine"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if disk, _ := os.ReadFile(q.Path); string(disk) != "SELECT 2; -- mine" || q.DiskConflict != "" {
		t.Fatalf("after keep mine: %q %q", disk, q.DiskConflict)
	}
}

func TestOpenFileMarkedWhenDeleted(t *testing.T) {
	tt, q, w := openFileEditor(t, "SELECT 1;")
	os.Remove(q.Path)
	w.events <- []mygo.FileEvent{{Op: mygo.FileRemove, Path: "."}}
	testutil.WaitFor(t, tt, "the mark", func() bool { return q.DiskConflict == "was deleted on disk" })
	if q.Editor.FileText() != "SELECT 1;" {
		t.Fatalf("the text went with the file: %q", q.Editor.FileText())
	}
}

// A watch that ends, as when the file's folder is replaced, starts again,
// and the file is read once more.
func TestOpenFileWatchedAgainAfterItEnds(t *testing.T) {
	tt, q, w := openFileEditor(t, "SELECT 1;")
	started := make(chan *fakeWatch, 4)
	was := WatchFiles
	WatchFiles = func(ctx context.Context, path string, opts mygo.FileWatchOptions) (FileWatch, error) {
		nw := &fakeWatch{path: path, events: make(chan []mygo.FileEvent, 4), closed: make(chan struct{})}
		started <- nw
		return nw, nil
	}
	t.Cleanup(func() { WatchFiles = was })
	os.WriteFile(q.Path, []byte("SELECT 2;"), 0o644)
	w.Close() // the watch ends, as io.EOF
	var again *fakeWatch
	testutil.WaitFor(t, tt, "a new watch", func() bool {
		select {
		case again = <-started:
		default:
		}
		return again != nil
	})
	if again.path != q.Path || q.Saved != "SELECT 2;" {
		t.Fatalf("watched again %q; saved text %q", again.path, q.Saved)
	}
}

// Before the app has started, as when saved projects load, WatchFiles
// reports it rather than calling MyGo, which needs the running app.
func TestWatchFilesBeforeAppStarts(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		_, err := WatchFiles(context.Background(), t.TempDir(), mygo.FileWatchOptions{})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotReady) {
			t.Fatalf("err %v, want ErrNotReady", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WatchFiles waited for an app that never starts")
	}
}
