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
	"fmt"

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
	KindGitLab Kind = "gitlab"
)

// Kinds lists every source, in the order the page shows them.
var Kinds = []Kind{KindClaude, KindCodex, KindAgterm, KindGit, KindGitHub, KindGitLab}

// State is how a source is doing.
type State string

const (
	StateOK      State = "ok"
	StateMissing State = "missing" // the tool or its files are not on this machine
	StateOff     State = "off"     // switched off by the owner (GitHub and GitLab only)
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
}

// RepoInventory answers the git questions behind Leftovers. It never changes
// a repo. root and dir are absolute folders; commits are full ids.
type RepoInventory interface {
	// Inventory reads a repo. On an error, Worktrees holds the checkouts
	// git listed before it failed, if it got that far, and WorktreesListed
	// says whether that listing is whole.
	Inventory(ctx context.Context, root string) (model.RepoInventory, error)
	// IsAncestor reports whether commit a is an ancestor of (or equal to)
	// commit b. A commit missing from the repo is an error, not false.
	IsAncestor(ctx context.Context, root, a, b string) (bool, error)
	TreeStatus(ctx context.Context, dir string) (model.TreeStatus, error)
}

// FolderUse tells which folders running processes of this user work in.
type FolderUse interface {
	// Procs returns the user's processes, each with its parent and its
	// working folder (absolute and cleaned). A process whose folder can be
	// read two ways is listed once for each. An error means it could not
	// tell.
	Procs(ctx context.Context) ([]model.Proc, error)
}

// PRProvider answers pull or merge request lookups for one forge through the
// user's own gh or glab. It caches answers and runs no timer: the core decides
// when to ask.
type PRProvider interface {
	Prober
	// Lookup returns the pull requests of repo with the given numbers, plus
	// those whose head branch is one of branches, in one call. A repo on
	// another forge is an error.
	Lookup(ctx context.Context, repo model.ForgeRepo, numbers []int, branches []string) ([]model.PullRequest, error)
}

// IssueLister lists the open issues of repositories on one forge, read only,
// through the user's own gh or glab. It runs no timer: the core decides when
// to ask, and only about repositories its forge may be asked about.
type IssueLister interface {
	// OpenIssues answers for each of repos it could ask about, in any order;
	// a repository missing from the answer was not answered (the core keeps
	// what it knew). An error means no answer for the repositories missing
	// from the rows returned with it: rows that came with ErrRateLimited or
	// another failure are kept, but any that come with ErrNotLoggedIn are
	// dropped (the login they were read with is gone; the core also purges
	// what that login listed before). errors.Is finds ErrRateLimited
	// underneath when the forge asked to be left alone.
	// A repo on another forge is left out of the answer.
	OpenIssues(ctx context.Context, repos []model.ForgeRepo) ([]model.RepoIssues, error)
}

// PREvidence is what Leftovers asks GitHub; the GitHub PRProvider implements
// it. Unlike Lookup it never answers from its cache or with an earlier
// answer: a rate limit, a backoff, or any error GitHub reports is an error.
type PREvidence interface {
	// Evidence returns the pull requests into repo whose head branch is one
	// of branches, and which heads may have more than those.
	// repo is on GitHub; any other is an error.
	Evidence(ctx context.Context, repo model.ForgeRepo, branches []string) (model.PREvidence, error)
}

// ErrRepoNotFound is returned by Lookup for a repo GitHub does not know or
// does not show to the account: gone, renamed, or private.
var ErrRepoNotFound = errors.New("repository not found")

// Errors a forge's provider wraps so the core can tell on the page why
// lookups do not happen (GitLab: its Lookup and its host check). An adapter
// keeps its own wording; errors.Is finds these underneath.
var (
	// ErrRateLimited: the forge asked to be left alone for a while.
	ErrRateLimited = errors.New("rate limited")
	// ErrNotLoggedIn: the tool is not logged in to that host (or the host is
	// no instance of the forge).
	ErrNotLoggedIn = errors.New("not logged in")
	// ErrNoLogins: the tool is logged in to no host at all. It is also
	// ErrNotLoggedIn, and it overrules every yes given before.
	ErrNoLogins = fmt.Errorf("logged in to no host: %w", ErrNotLoggedIn)
	// ErrNotChecked: a lookup was not made, since the host's login is not
	// known now (never checked, or the last yes ran out). It is no answer:
	// the lookup waits for the host's next check.
	ErrNotChecked = errors.New("login not checked")
)

// PartialLookup is the error a GitLab Lookup returns together with the pull
// requests it could place, when GitLab's answer left some numbers it was
// asked about unsettled. The pull requests returned are good; of the numbers
// asked for and not returned:
//   - Wrong: GitLab placed them at another address than the one asked (say a
//     route glab took to another instance, or another port): the copy the
//     board holds is disproved, and the number is not "missing" either, as
//     the route may be put right.
//   - Unverified: GitLab did not say where they are, so nothing is known:
//     they stay as they were and are asked again.
//
// Every other number asked for and not returned does not exist.
type PartialLookup struct{ Wrong, Unverified []int }

func (*PartialLookup) Error() string { return "lookup answered only in part" }

// Jumper focuses an existing terminal session or pane. It can do nothing
// else. Each focus provider (agterm, tmux) is one Jumper for its own kind.
type Jumper interface {
	Jump(ctx context.Context, target model.FocusTarget) error
}

// Errors a Jumper returns.
var (
	// ErrUnknownTerminal: the target is malformed, of another kind, or no
	// longer exists.
	ErrUnknownTerminal = errors.New("terminal not in the current tree")
	// ErrNotInstalled: the program that would focus it is not found.
	ErrNotInstalled = errors.New("not installed")
	// ErrNoClient: the tmux pane exists but no tmux client is attached to
	// show it.
	ErrNoClient = errors.New("no client attached")
)

// Jumpers sends each target to the provider of its kind. A kind with no
// provider is ErrNotInstalled.
type Jumpers map[model.FocusKind]Jumper

// Jump focuses target with the provider of its kind.
func (j Jumpers) Jump(ctx context.Context, target model.FocusTarget) error {
	p, ok := j[target.Kind]
	if !ok || p == nil {
		return ErrNotInstalled
	}
	return p.Jump(ctx, target)
}
