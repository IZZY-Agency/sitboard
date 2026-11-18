// Package model holds the facts that source adapters report to the core.
//
// Frozen seam: adapters (internal/adapters/...) produce these values and the
// core (internal/core) consumes them. Changes go through the coordinator.
//
// Rules for every value here:
//   - A fact that cannot be read is unknown, never guessed. Use Known[T] with
//     OK=false, the empty string where a field says so, or the zero time.Time.
//   - Every string comes from session records, git or GitHub and is untrusted.
//     It is never interpreted, only carried and, at the edge, escaped.
//   - No transcript content (prompts, code, tool output) ever enters a fact,
//     with one exception: an artifact's title (ArtifactRef.Title), text a
//     model wrote, read from the structured result of the session's own
//     publish. Adapters cap it (1 KB); the core cleans it and cuts it to the
//     length of any other title before the page gets it.
package model

import (
	"regexp"
	"time"
)

// Known is a value that may be unknown. The zero value is unknown.
type Known[T any] struct {
	V  T
	OK bool // false: the source could not tell
}

// Of returns a known value.
func Of[T any](v T) Known[T] { return Known[T]{V: v, OK: true} }

// Unknown returns an unknown value. Same as the zero value; reads better at call sites.
func Unknown[T any]() Known[T] { return Known[T]{} }

// Get returns the value and whether it is known.
func (k Known[T]) Get() (T, bool) { return k.V, k.OK }

// Tool is the agent tool that runs a session.
type Tool string

const (
	ToolClaude Tool = "claude"
	ToolCodex  Tool = "codex"
)

// Status is what an agent is doing, as far as a source can tell.
// These values predate the status spike and may be reopened by the coordinator.
type Status string

const (
	StatusBusy     Status = "busy"     // working on a turn
	StatusBlocked  Status = "blocked"  // waiting on a question or a permission
	StatusFinished Status = "finished" // turn done, waiting for a message
)

// SessionKey identifies one agent session across facts.
type SessionKey struct {
	Tool Tool
	ID   string // the tool's own session id
}

// PRRef points at a pull request. Repo is "owner/name".
type PRRef struct {
	Repo   string
	Number int
}

// ArtifactURLPattern is the only form of artifact link a fact may carry: a
// claude.ai artifact address, nothing else. contract.ArtifactURLPattern, which
// the page checks links against, must stay equal to it (a test checks).
const ArtifactURLPattern = `^https://claude\.ai/(?:artifact/[A-Za-z0-9_-]{8,64}|code/artifact/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`

var artifactURLRe = regexp.MustCompile(ArtifactURLPattern)

// ValidArtifactURL reports whether u matches ArtifactURLPattern.
func ValidArtifactURL(u string) bool { return artifactURLRe.MatchString(u) }

// ArtifactRef is a claude.ai artifact a session published. The same URL is the
// same artifact: a republish updates its title and time, it is not a second one.
type ArtifactRef struct {
	URL         string    // matches ArtifactURLPattern
	Title       string    // as the session's latest publish of it reported; "" when none
	PublishedAt time.Time // that publish; zero when unknown
}

// Fact is anything a Watcher emits. The set is closed: only this package defines facts.
type Fact interface{ isFact() }

// SessionFact is an adapter's complete current view of one agent session. Each
// one replaces the previous SessionFact with the same Key; an adapter that learns
// more later (for example from a transcript scan) emits the whole fact again.
// A zero time.Time means unknown.
//
// Ended versus gone: while a session's records exist, the adapter keeps
// reporting it with SessionFact, and once its process is gone it sends
// Ended=true. It sends SessionGone only when the records themselves disappear.
// Whether an ended session stays on the board, and for how long, is the core's
// decision (retention), never the adapter's.
type SessionFact struct {
	Key         SessionKey
	Name        string // the session's own name or title; "" when it has none
	ToolVersion string // "" when unknown

	Model       Known[string]
	Status      Known[Status]
	StatusSince time.Time

	StartedAt    time.Time
	LastActivity time.Time

	// Live: a process is running this session right now.
	// Ended: the session is over (its process is gone). Ended=true implies
	// Live is known and false (Live == Of(false)); an adapter never sends
	// Ended=true with Live unknown or true.
	Live    Known[bool]
	Ended   bool
	EndedAt time.Time // zero when not ended or when the end time is unknown

	Folder string // latest working folder, absolute; "" when unknown

	// Parent is only for tools that record a parent natively (Codex's
	// parent_thread_id). Adapters never derive it from spawn commands or
	// messages; OK=false means no native parent was recorded.
	Parent Known[SessionKey]

	// Who spawned whom, for tools without a native parent (Claude Code). These
	// list what this session's own session-creating calls (for example
	// `agtermctl session new`) produced:
	//   - SpawnedTerminalIDs: the agterm session ids those calls returned.
	//   - SpawnedNames: the names this session gave them.
	// The core matches them to other sessions: first by terminal id against a
	// session's TerminalID, then by name against a session's Name or its
	// terminal's Name. A name that matches more than one session gives no
	// parent. A parent recorded natively (Parent) wins over a match.
	SpawnedTerminalIDs []string
	SpawnedNames       []string

	// WorkerHint is set only when the session received a message from another
	// session that is NOT one of the sessions it spawned itself. A coordinator
	// receives messages from its own workers all the time; that alone never
	// sets it. Known limit, accepted for the proof of concept: the adapter
	// compares sender names with the names this session gave the sessions it
	// spawned, so if a spawned session runs under a different name, its
	// coordinator can be flagged by mistake. The core should derive the hint
	// from PeerSenders instead; adapters keep filling WorkerHint until it does.
	WorkerHint bool

	// PeerSenders are the names of the sessions that sent this session a
	// message (Claude Code: user lines with origin.kind "peer"), deduplicated
	// and capped. Never the message text. The core derives the worker hint
	// from it after matching: a sender that is not one of this session's
	// matched children is a sign that this session is someone's worker.
	PeerSenders []string

	TerminalID string  // agterm session id, "" when none
	Subagents  int     // in-process subagents seen
	CreatedPRs []PRRef // only pull requests this session created
	// Artifacts are the claude.ai artifacts this session (or one of its
	// in-process subagents) published itself, one per URL. A delete is not
	// followed, and a rename made anywhere else is not seen.
	Artifacts []ArtifactRef
	TurnEnds  []time.Time // finished turns, last 7 days, oldest first
}

// SessionGone says a session's records no longer exist for its adapter (see
// SessionFact for ended versus gone). The core drops it.
type SessionGone struct{ Key SessionKey }

// SourceReady says a watcher finished its first complete pass: every session
// or terminal that existed at start has been emitted. A watcher sends it once.
// A watcher whose source is missing sends it at once.
//
// Kind is a source.Kind value carried as a plain string, because package
// source imports this package and the reverse import would be a cycle.
type SourceReady struct{ Kind string }

// TerminalFact is the agterm adapter's complete current view of one terminal
// session. It replaces the previous TerminalFact with the same ID.
type TerminalFact struct {
	ID        string // agterm session id
	Workspace string
	Name      string
	Title     string
	Folder    string        // absolute; "" when unknown
	Status    Known[Status] // agterm's own status mark, mapped to Status
	HasAgent  bool          // false: a plain shell
}

// TerminalGone says a terminal session was closed.
type TerminalGone struct{ ID string }

// TerminalSelected says the owner selected a terminal session in agterm.
type TerminalSelected struct {
	ID string
	At time.Time
}

func (SessionFact) isFact()      {}
func (SessionGone) isFact()      {}
func (TerminalFact) isFact()     {}
func (TerminalGone) isFact()     {}
func (TerminalSelected) isFact() {}
func (SourceReady) isFact()      {}

// Repo is what git says about a folder.
type Repo struct {
	Root     string        // the repo (or worktree) root; the folder itself when it is not in a repo
	MainRoot string        // the main repo root a worktree belongs to; == Root for a main checkout or no repo
	Branch   Known[string] // OK=false on a detached HEAD or outside a repo
	Slug     string        // "owner/name" of the GitHub remote, "" when none
}

// Worktree is one git worktree of a repo.
type Worktree struct {
	Path   string
	Branch Known[string]
}

// PRState is the state of a pull request.
type PRState string

const (
	PRStateOpen   PRState = "open"
	PRStateMerged PRState = "merged"
	PRStateClosed PRState = "closed"
)

// Checks is the rolled-up state of a pull request's checks.
type Checks string

const (
	ChecksPassing Checks = "passing"
	ChecksFailing Checks = "failing"
	ChecksRunning Checks = "running"
	ChecksNone    Checks = "none"
	ChecksUnknown Checks = "unknown"
)

// Review is a pull request's review decision.
type Review string

const (
	ReviewApproved         Review = "approved"
	ReviewChangesRequested Review = "changes_requested"
	ReviewRequired         Review = "review_required"
	ReviewNone             Review = ""
)

// PullRequest is what GitHub says about one pull request.
type PullRequest struct {
	Repo          string // "owner/name"
	Number        int
	Title         string
	URL           string
	HeadBranch    string
	State         PRState
	Draft         bool
	Checks        Checks
	Review        Review
	ClosingIssues []int // issues in the same repo this pull request closes
	OpenedAt      time.Time
	ClosedAt      time.Time // merged or closed time; zero while open
}
