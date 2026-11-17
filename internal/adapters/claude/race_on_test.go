//go:build race

package claude

// raceEnabled: the race detector slows decoding many times over.
const raceEnabled = true
