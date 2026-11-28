//go:build !darwin && !linux

package main

import "os"

// isTerminal is false where sitboard has no terminal check: plain lines.
func isTerminal(*os.File) bool { return false }
