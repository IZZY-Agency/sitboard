//go:build darwin || linux

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// isTerminal says whether f is a terminal: only then is the start-up block
// written in colour. A pipe, a file or /dev/null is not one.
func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), ioctlReadTermios)
	return err == nil
}
