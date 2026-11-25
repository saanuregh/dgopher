// Package procmem tells how much memory this process uses, as the system
// counts it: the figure its task manager shows. Go's own count of its heap
// leaves out what the C libraries the app links take, as the databases'
// and the drawing's.
package procmem

// Used is the memory the system counts for this process, in bytes, and
// whether the system could be asked: false on one this package does not
// know how to ask.
func Used() (bytes uint64, ok bool) {
	bytes = used()
	return bytes, bytes > 0
}

// Peak is the most memory the system has counted for this process since
// it started, in bytes, and whether the system keeps it: where it does
// not, the caller has only the amounts it read itself to go by.
func Peak() (bytes uint64, ok bool) {
	bytes = peak()
	return bytes, bytes > 0
}
