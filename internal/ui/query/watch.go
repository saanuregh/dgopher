package query

import (
	"context"
	"errors"
	"io/fs"
	"os"

	"github.com/egoist/mygo"
)

// FileWatch is a watch of files, as mygo.WatchFiles starts.
type FileWatch interface {
	Next(ctx context.Context) ([]mygo.FileEvent, error)
	Close() error
}

// ErrNotReady is WatchFiles' error before the app has started, as when
// saved projects load: watch again once it is (mygo.App.WhenReady).
var ErrNotReady = errors.New("the app has not started")

// WatchFiles starts a watch; tests replace it, as the headless tester
// runs no app to watch with.
var WatchFiles = func(ctx context.Context, path string, opts mygo.FileWatchOptions) (FileWatch, error) {
	if !mygo.App.IsReady() {
		return nil, ErrNotReady
	}
	w, err := mygo.WatchFiles(ctx, path, opts)
	if err != nil {
		return nil, err
	}
	return w, nil
}

// Follow calls changed, through post, after each change WatchFiles
// reports, until the watch ends; then ended, if set.
func Follow(w FileWatch, post func(func()), changed, ended func()) {
	go func() {
		for {
			if _, err := w.Next(context.Background()); err != nil {
				if ended != nil {
					post(ended)
				}
				return
			}
			post(changed)
		}
	}()
}

// watchFile follows the tab's file from when its path is set or changed.
// Without a watch, a change is still caught when the file is saved.
func (q *Tab) watchFile() {
	if q.watchedPath == q.Path {
		return
	}
	q.stopWatch()
	q.watchedPath = q.Path
	if q.Path == "" {
		return
	}
	w, err := WatchFiles(context.Background(), q.Path, mygo.FileWatchOptions{})
	if err != nil {
		return
	}
	q.watch = w
	path := q.Path
	Follow(w, q.a.Post, func() {
		if q.watch == w && q.Path == path {
			q.fileChanged()
		}
	}, func() {
		// The watch ends when the file's folder is removed or replaced, as
		// by a checkout: read the file, and watch it again from the next
		// frame.
		if q.watch == w && q.Path == path {
			q.watch, q.watchedPath = nil, ""
			q.fileChanged()
		}
	})
}

func (q *Tab) stopWatch() {
	if q.watch != nil {
		q.watch.Close()
		q.watch = nil
	}
}

// fileChanged takes in a change made to the file outside the editor: the
// file's text replaces the editor's unless the editor has text not saved.
func (q *Tab) fileChanged() {
	if q.closed {
		return
	}
	disk, err := os.ReadFile(q.Path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		q.DiskConflict = "was deleted on disk"
	case err != nil:
	case string(disk) == q.Saved:
		if q.DiskConflict == "was deleted on disk" || q.DiskConflict == "changed on disk" {
			q.DiskConflict = ""
		}
	case q.Editor.FileText() == q.Saved:
		q.Editor.SetFileText(string(disk))
		q.Saved, q.Typed, q.DiskConflict = string(disk), string(disk), ""
	default:
		q.DiskConflict = "changed on disk"
	}
}
