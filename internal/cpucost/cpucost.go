//go:build unix

// Package cpucost measures what a piece of work costs this process in CPU
// time, for tests that compare the cost of the same work at two sizes (is it
// linear? see Linear). Unlike wall time, CPU time does not grow while the process waits
// for a core, so a loaded machine does not fail such a test. sitboard runs on
// macOS and Linux only.
package cpucost

import (
	"runtime"
	"syscall"
	"time"
)

// Of returns the least CPU time, user and system, that fn cost this process
// over runs runs. Each run starts after a garbage collection, so it pays for
// its own garbage only. The least run is the one the rest of the process
// (other goroutines, the race detector's own work) disturbed least.
func Of(runs int, fn func()) time.Duration {
	best := time.Duration(-1)
	for range max(runs, 1) {
		runtime.GC()
		start := used()
		fn()
		if d := used() - start; best < 0 || d < best {
			best = d
		}
	}
	return best
}

// used is the CPU time this process has used so far.
func used() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		panic("getrusage: " + err.Error())
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
