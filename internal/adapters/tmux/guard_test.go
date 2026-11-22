package tmux

import (
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/testguard"
)

// Tests build the adapter only through newAdapter, which replaces the runner:
// no test talks to a real tmux by accident.
func TestAdapterBuiltOnlyByNewAdapter(t *testing.T) {
	testguard.OnlyIn(t, []string{"New"}, []string{"newAdapter"})
}
