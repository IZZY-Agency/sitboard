// Package source defines the interfaces every source adapter implements.
//
// Frozen seam: the adapters live in internal/adapters/<kind>/ and the core
// consumes them through these interfaces only. Changes go through the coordinator.
//
// Rules for every adapter:
//   - Parse tolerantly: ignore unknown fields, never crash on a bad line.
//   - When a format is not recognised, report the fact as unknown. Never guess.
//   - A missing or failing source is reported by Probe; the board keeps working.
//   - Child processes are started with argument lists, never a shell string.
//   - Nothing from a transcript but derived fields leaves the adapter.
package source

import (
	"context"
	"errors"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// Kind names a source.
type Kind string

const (
	KindClaude Kind = "claude"
	KindCodex  Kind = "codex"
	KindAgterm Kind = "agterm"
	KindGit    Kind = "git"
	KindGitHub Kind = "github"
)

// Kinds lists every source, in the order the page shows them.
var Kinds = []Kind{KindClaude, KindCodex, KindAgterm, KindGit, KindGitHub}

// State is how a source is doing.
type State string

const (
	StateOK      State = "ok"
	StateMissing State = "missing" // the tool or its files are not on this machine
	StateOff     State = "off"     // switched off by the owner (GitHub only)
	StateFailing State = "failing" // present but not answering or not parseable
)

// Status is what Probe reports. Version is the tool's version, "" when unknown.
// Tested is true only for versions the adapter was tested with. Detail is one
// short line for the page, for example "not logged in"; it never holds a path
// outside the home-relative form, a token or transcript content.
type Status struct {
	Kind    Kind
	State   State
	Version string
	Tested  bool
	Detail  string
}

// Prober reports a source's status. Probe is cheap and safe to call at any time.
type Prober interface {
	Probe(ctx context.Context) Status
}

// Watcher is a source that pushes facts: Claude Code, Codex, agterm.
type Watcher interface {
	Kind() Kind
	Prober
	// Run emits the current facts first, then model.SourceReady{Kind:
	// string(Kind())} once that first complete pass is done, then every
	// change, until ctx ends; then it returns nil. A missing source emits
	// SourceReady and returns nil at once.
	//
	// emit is safe to call from any goroutine, but a watcher never calls it
	// concurrently for the same session key or the same terminal id: facts
	// about one session (or one terminal) arrive in the order they were
	// learned. emit must not be called after Run returns.
	//
	// When Run returns an error, the core drops every fact that source
	// emitted and shows the source as failing. It does not restart Run.
	//
	// The core calls Run once, at startup. A source that is missing at
	// startup is not picked up when it appears later; that takes a restart
	// (accepted for the proof of concept).
	Run(ctx context.Context, emit func(model.Fact)) error
}

// RepoResolver answers git questions about folders. It never changes a repo.
type RepoResolver interface {
	Prober
	Resolve(ctx context.Context, folder string) (model.Repo, error)
	Worktrees(ctx context.Context, root string) ([]model.Worktree, error)
	LocalBranches(ctx context.Context, root string) ([]string, error)
}

// PRProvider answers pull request lookups through the user's own gh. It caches
// answers and runs no timer: the core decides when to ask.
type PRProvider interface {
	Prober
	// Lookup returns the pull requests of repo ("owner/name") with the given
	// numbers, plus those whose head branch is one of branches, in one call.
	Lookup(ctx context.Context, repo string, numbers []int, branches []string) ([]model.PullRequest, error)
}

// ErrRepoNotFound is returned by Lookup for a repo GitHub does not know or
// does not show to the account: gone, renamed, or private.
var ErrRepoNotFound = errors.New("repository not found")

// Jumper focuses an existing terminal session. It can do nothing else.
type Jumper interface {
	Jump(ctx context.Context, terminalID string) error
}

// ErrUnknownTerminal is returned by Jump for an id that is malformed or not in
// the current terminal tree.
var ErrUnknownTerminal = errors.New("terminal not in the current tree")
