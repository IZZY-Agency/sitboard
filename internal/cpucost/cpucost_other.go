//go:build !unix

package cpucost

import "time"

// Of, where getrusage does not exist, measures wall time instead: a loaded
// machine can then fail a linearity test. sitboard runs on macOS and Linux
// only; this keeps the tests building elsewhere (go vet).
func Of(runs int, fn func()) time.Duration {
	best := time.Duration(-1)
	for range max(runs, 1) {
		start := time.Now()
		fn()
		if d := time.Since(start); best < 0 || d < best {
			best = d
		}
	}
	return best
}
