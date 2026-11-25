//go:build !linux && !windows && !darwin

package procmem

func used() uint64 { return 0 }

func peak() uint64 { return 0 }
