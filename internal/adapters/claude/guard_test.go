package claude

import (
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/testguard"
)

// No test reaches the process's prober: a batch one test left running would
// hold a slot another needs. Tests make adapters with newAdapter and tailers
// with a prober of their own.
func TestNoTestUsesTheProcessProber(t *testing.T) {
	testguard.OnlyIn(t, []string{"procProber", "New"}, []string{"newAdapter", "TestOneProberPerProcess"})
}
