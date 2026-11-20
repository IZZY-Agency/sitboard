//go:build !darwin && !linux

package claude

// readProcFacts is not supported on this platform: no terminal link and no
// launch effort.
func readProcFacts(int) procFacts { return procFacts{} }
