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
