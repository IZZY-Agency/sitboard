//go:build !darwin && !linux

package proc

func readArgsEnv(int, Pick) bool { return false }
