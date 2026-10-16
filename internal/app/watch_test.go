package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"dgopher/internal/testutil"
	"dgopher/internal/ui/query"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type fakeWatch struct {
	path   string
	opts   mygo.FileWatchOptions
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

// fakeWatches replaces the file watching with fakes, kept by path, or
// with a failure when fail is set.
func fakeWatches(t *testing.T, fail bool) func(path string) *fakeWatch {
	var mu sync.Mutex
	watches := map[string]*fakeWatch{}
	was := query.WatchFiles
	query.WatchFiles = func(ctx context.Context, path string, opts mygo.FileWatchOptions) (query.FileWatch, error) {
		if fail {
			return nil, errors.New("no watching here")
		}
		w := &fakeWatch{path: path, opts: opts, events: make(chan []mygo.FileEvent, 4), closed: make(chan struct{})}
		mu.Lock()
		watches[path] = w
		mu.Unlock()
		return w, nil
	}
	t.Cleanup(func() { query.WatchFiles = was })
	return func(path string) *fakeWatch {
		mu.Lock()
		defer mu.Unlock()
		return watches[path]
	}
}

func TestProjectListUpdatesOnWatch(t *testing.T) {
	watch := fakeWatches(t, false)
	a := newBareApp(t)
	p, _ := a.addProject(newProjectDir(t, "billing"))
	tt := ui.NewTester(a.view, 1000, 700)
	w := watch(p.Queries)
	if w == nil || !w.opts.Recursive {
		t.Fatalf("the queries folder is not watched recursively: %+v", w)
	}
	if !w.opts.Ignore(".git", true) || w.opts.Ignore("reports", true) {
		t.Fatal("dot folders are not ignored, or others are")
	}
	os.MkdirAll(filepath.Join(p.Queries, "reports"), 0o755)
	os.WriteFile(filepath.Join(p.Queries, "reports", "monthly.sql"), []byte("SELECT 1;"), 0o644)
	w.events <- []mygo.FileEvent{{Op: mygo.FileCreate, Path: "reports/monthly.sql"}}
	testutil.WaitFor(t, tt, "the new file", func() bool { return slices.Contains(p.Files, "reports/monthly.sql") })
	p.Scanned = time.Time{}
	os.WriteFile(filepath.Join(p.Queries, "polled.sql"), []byte("SELECT 1;"), 0o644)
	a.ScanQueries(p, false)
	for range 5 {
		tt.Frame()
	}
	if slices.Contains(p.Files, "polled.sql") {
		t.Fatal("polled while watching")
	}
	a.dropProject(p)
	select {
	case <-w.closed:
	default:
		t.Fatal("the watch outlived the project")
	}
}

func TestProjectListPollsWhenWatchFails(t *testing.T) {
	fakeWatches(t, true)
	a := newBareApp(t)
	p, _ := a.addProject(newProjectDir(t, "billing"))
	tt := ui.NewTester(a.view, 1000, 700)
	os.WriteFile(filepath.Join(p.Queries, "polled.sql"), []byte("SELECT 1;"), 0o644)
	p.Scanned = time.Time{}
	a.ScanQueries(p, false)
	testutil.WaitFor(t, tt, "the polled file", func() bool { return slices.Contains(p.Files, "polled.sql") })
}

// A folder that cannot be watched, as one too big to, is not tried again
// at every poll.
func TestProjectWatchNotRetriedEachPoll(t *testing.T) {
	tries := 0
	was := query.WatchFiles
	query.WatchFiles = func(context.Context, string, mygo.FileWatchOptions) (query.FileWatch, error) {
		tries++
		return nil, errors.New("too many entries")
	}
	t.Cleanup(func() { query.WatchFiles = was })
	a := newBareApp(t)
	p, _ := a.addProject(newProjectDir(t, "billing"))
	start := tries
	for range 5 {
		p.Scanned = time.Time{}
		a.ScanQueries(p, false)
	}
	if tries-start > 1 {
		t.Fatalf("watched %d times in five polls", tries-start)
	}
}

// Saved projects load before the app is ready to watch: their folders are
// watched once it is, without waiting out the retry delay.
func TestProjectWatchStartsWhenReady(t *testing.T) {
	watch := fakeWatches(t, false)
	ready := false
	fake := query.WatchFiles
	query.WatchFiles = func(ctx context.Context, path string, opts mygo.FileWatchOptions) (query.FileWatch, error) {
		if !ready {
			return nil, query.ErrNotReady
		}
		return fake(ctx, path, opts)
	}
	a := newBareApp(t)
	p, _ := a.addProject(newProjectDir(t, "billing"))
	if watch(p.Queries) != nil {
		t.Fatal("watched before the app was ready")
	}
	ready = true
	a.watchProjects()
	if watch(p.Queries) == nil {
		t.Fatal("not watched once the app was ready")
	}
}
