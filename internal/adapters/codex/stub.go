// Package codex is the Codex source adapter.
//
// Stub: reports "missing". Codex is out of the proof of concept; the
// constructor signature is frozen so it can be added later.
package codex

import (
	"context"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// Options configure the adapter.
type Options struct {
	Home string           // the user's home folder; records are read under <Home>/.codex
	Now  func() time.Time // clock; nil means time.Now
}

type adapter struct{ opts Options }

// New returns the Codex watcher.
func New(opts Options) source.Watcher { return &adapter{opts: opts} }

func (a *adapter) Kind() source.Kind { return source.KindCodex }

func (a *adapter) Probe(context.Context) source.Status {
	return source.Status{Kind: source.KindCodex, State: source.StateMissing, Detail: "adapter not built yet"}
}

// Run reports the stub as a missing source: ready at once, nothing to watch.
func (a *adapter) Run(_ context.Context, emit func(model.Fact)) error {
	emit(model.SourceReady{Kind: string(source.KindCodex)})
	return nil
}
