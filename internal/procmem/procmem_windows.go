package procmem

import (
	"syscall"
	"unsafe"
)

// processMemoryCounters is PROCESS_MEMORY_COUNTERS of psapi.h.
type processMemoryCounters struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
}

var getProcessMemoryInfo = syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo")

// used is the working set, as Task Manager's memory of a process.
func used() uint64 { return uint64(counters().workingSetSize) }

// peak is the largest the working set has been.
func peak() uint64 { return uint64(counters().peakWorkingSetSize) }

// counters asks the system for the process's memory counters, all zero
// when it does not answer.
func counters() processMemoryCounters {
	process, err := syscall.GetCurrentProcess()
	if err != nil {
		return processMemoryCounters{}
	}
	var counters processMemoryCounters
	counters.cb = uint32(unsafe.Sizeof(counters))
	if ok, _, _ := getProcessMemoryInfo.Call(uintptr(process), uintptr(unsafe.Pointer(&counters)), uintptr(counters.cb)); ok == 0 {
		return processMemoryCounters{}
	}
	return counters
}
