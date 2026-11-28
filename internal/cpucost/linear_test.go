package cpucost

import (
	"testing"
	"time"
)

// The verdict is about growth: a machine of any speed gives the same one.
func TestJudge(t *testing.T) {
	const ms = time.Millisecond
	for _, c := range []struct {
		name    string
		tn, t4n time.Duration
		ok      bool
	}{
		{"linear", 100 * ms, 400 * ms, true},
		{"linear, 5 times slower", 500 * ms, 2000 * ms, true},
		{"linear with cache misses", 100 * ms, 700 * ms, true},
		{"quadratic", 100 * ms, 1600 * ms, false},
		{"quadratic, 5 times faster", 20 * ms, 320 * ms, false},
		{"too small to judge", 10 * time.Microsecond, 3 * ms, true},
		{"nothing measured at n", 0, 6 * ms, false},
		{"linear, but a hang", 10 * time.Second, 40 * time.Second, false},
	} {
		if err := judge(c.tn, c.t4n); (err == nil) != c.ok {
			t.Errorf("%s (%s, %s): %v", c.name, c.tn, c.t4n, err)
		}
	}
}

// Each size is run runs times, in turns.
func TestLinearTakesTurns(t *testing.T) {
	var got []byte
	Linear(t, 3, "nothing", func() { got = append(got, 'n') }, func() { got = append(got, '4') })
	if string(got) != "n4n4n4" {
		t.Fatalf("runs: %s", got)
	}
}
