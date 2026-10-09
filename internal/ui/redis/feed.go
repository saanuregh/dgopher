package redis

import (
	"sync"
	"time"
)

// feedFlush is how often a feed's items reach the view.
const feedFlush = 150 * time.Millisecond

// feed carries items that come faster than frames, as commands monitored
// or messages published, to the UI thread in batches, keeping the latest
// keep of them.
type feed[T any] struct {
	mu      sync.Mutex
	pending []T
	keep    int
}

func (f *feed[T]) add(x T) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = keepLatest(f.pending, []T{x}, f.keep)
}

// run hands deliver what came, every feedFlush, until done closes; then
// what came last.
func (f *feed[T]) run(done <-chan struct{}, deliver func([]T)) {
	flush := func() {
		f.mu.Lock()
		batch := f.pending
		f.pending = nil
		f.mu.Unlock()
		if len(batch) > 0 {
			deliver(batch)
		}
	}
	tick := time.NewTicker(feedFlush)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			flush()
		case <-done:
			flush()
			return
		}
	}
}

// keepLatest adds batch to items, keeping the latest keep, in a new array
// once some go: the view may still hold the old one.
func keepLatest[T any](items, batch []T, keep int) []T {
	items = append(items, batch...)
	if over := len(items) - keep; over > 0 {
		items = append(items[:0:0], items[over:]...)
	}
	return items
}
