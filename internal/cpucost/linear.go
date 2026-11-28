package cpucost

import (
	"fmt"
	"testing"
	"time"
)

const (
	// maxGrowth is how much more 4 times the input may cost: linear work
	// costs about 4 times as much, quadratic work about 16 times.
	maxGrowth = 8
	// noiseFloor is allowed on top, so that costs too small to measure well
	// are not judged by their ratio. A caller picks sizes at which the work
	// it guards against costs far more than this.
	noiseFloor = 5 * time.Millisecond
	// hangLimit catches work that is far too slow at any growth. It is no
	// speed requirement: the slowest of the measured works costs under 2 s on
	// a developer's machine under the race detector, so a machine many times
	// slower stays well under it.
	hangLimit = 30 * time.Second
)

// Linear fails t when work grows faster than linearly: atN does it on an
// input of size n, at4N on one of size 4n. The least CPU time of runs runs of
// each is compared, so the verdict does not depend on the machine's speed.
// The two sizes take turns, so a slow stretch of the machine meets both.
func Linear(t testing.TB, runs int, what string, atN, at4N func()) {
	t.Helper()
	var tn, t4n time.Duration
	for i := range max(runs, 1) {
		a, b := Of(1, atN), Of(1, at4N)
		if i == 0 || a < tn {
			tn = a
		}
		if i == 0 || b < t4n {
			t4n = b
		}
	}
	t.Logf("%s: %s at n, %s at 4n", what, tn, t4n)
	if err := judge(tn, t4n); err != nil {
		t.Errorf("%s: %v", what, err)
	}
}

// judge says what is wrong with the costs of a work at n and at 4n.
func judge(tn, t4n time.Duration) error {
	if t4n > maxGrowth*tn+noiseFloor {
		return fmt.Errorf("grows faster than linear: %s at n, %s at 4n", tn, t4n)
	}
	if t4n > hangLimit {
		return fmt.Errorf("took %s of CPU at 4n", t4n)
	}
	return nil
}
