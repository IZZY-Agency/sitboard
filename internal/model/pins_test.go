package model

import (
	"math"
	"slices"
	"testing"
)

// Only 1 to 2^31-1, each once, the first MaxPinsAsked in their order.
func TestPinsToAsk(t *testing.T) {
	if got := PinsToAsk([]int{7, 0, -1, math.MaxInt32 + 1, 7, math.MaxInt32, 3}); !slices.Equal(got, []int{7, math.MaxInt32, 3}) {
		t.Fatalf("got %v", got)
	}
	many := make([]int, MaxPinsAsked+10)
	for i := range many {
		many[i] = len(many) - i
	}
	if got := PinsToAsk(many); len(got) != MaxPinsAsked || got[0] != len(many) || got[MaxPinsAsked-1] != 11 {
		t.Fatalf("got %d: %v…", len(got), got[:3])
	}
	if PinsToAsk(nil) != nil {
		t.Fatal("none asks none")
	}
}
