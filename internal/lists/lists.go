// Package lists holds generic helpers for slices.
package lists

// Map returns f of each element of s, in order. The result has len(s)
// and is never nil.
func Map[T, U any](s []T, f func(T) U) []U {
	out := make([]U, len(s))
	for i, v := range s {
		out[i] = f(v)
	}
	return out
}
