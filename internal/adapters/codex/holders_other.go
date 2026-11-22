//go:build !linux

package codex

import (
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
)

// defaultHolders asks lsof (macOS ships it). Where it is missing, liveness is
// unknown.
func defaultHolders() holdersFunc { return lsofHolders(runner.Exec(2 * time.Second)) }
