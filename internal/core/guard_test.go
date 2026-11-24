package core

import (
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/testguard"
)

// Every test board comes from newBoard, which closes it when its test ends:
// a board made with New keeps its timers and goroutines into the next test.
func TestBoardsComeFromNewBoard(t *testing.T) {
	testguard.OnlyIn(t, []string{"New"}, []string{"newBoard"})
}

// The state store prunes against the clock it is opened with: a test that
// opens it with the machine's clock starts failing once the date moves on.
func TestStateOpensOnTheBoardsClock(t *testing.T) {
	testguard.NoCall(t, "state", "Open")
	testguard.NoCall(t, "state", "Memory")
}
