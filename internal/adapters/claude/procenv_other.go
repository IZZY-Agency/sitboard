//go:build !darwin && !linux

package claude

// readAgtermID is not supported on this platform: no terminal link.
func readAgtermID(int) string { return "" }
