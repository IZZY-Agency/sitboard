//go:build unix

package cpucost

import (
	"testing"
	"time"
)

// Work costs CPU time; waiting costs none.
func TestCountsWorkNotWaiting(t *testing.T) {
	spin := func() {
		x := 0
		for i := range 50_000_000 {
			x ^= i
		}
		_ = x
	}
	work := Of(3, spin)
	wait := Of(3, func() { time.Sleep(50 * time.Millisecond) })
	t.Logf("work %s, a 50 ms wait %s", work, wait)
	if work <= 0 {
		t.Fatalf("work cost %s", work)
	}
	if wait >= 50*time.Millisecond {
		t.Fatalf("a 50 ms wait cost %s of CPU", wait)
	}
}
