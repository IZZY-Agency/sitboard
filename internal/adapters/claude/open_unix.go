//go:build unix

package claude

import (
	"os"
	"syscall"
)

// openNoFollow opens a transcript for reading without following a link put
// in its place after it was listed.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
