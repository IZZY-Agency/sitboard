//go:build !linux

package codex

import (
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
)

func platformCwds() cwdsFunc { return lsofCwds(runner.Exec(2 * time.Second)) }
