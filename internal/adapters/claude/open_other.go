//go:build !unix

package claude

import "os"

// openNoFollow opens a transcript for reading. Without O_NOFOLLOW the check
// that the open file is the regular file that was looked at (tailer.update)
// is what refuses a link.
func openNoFollow(path string) (*os.File, error) { return os.Open(path) }
