// Package contract is the API between the server and the page: the JSON
// shapes, the ids, the paths and the version.
//
// Frozen seam. web/src/contract.gen.ts is generated from this package
// (`make contract`); a test fails when the two drift. Changes go through the
// coordinator, and any change to a shape bumps Version.
//
// JSON rules:
//   - Keys are camelCase.
//   - Slices are never null: producers use empty slices.
//   - A time field marked omitzero is absent when unknown. Times are RFC 3339, UTC.
//   - An empty string means "none" or "unknown", as each field says.
//   - Every string can come from session records, git or GitHub and is
//     untrusted: the page renders it as text, never as markup.
//   - No transcript content, no absolute path, no token ever appears here.
//     Every path is home-relative ("~/..."). A folder outside the home folder
//     is given by its base name only ("alpha", never "/srv/alpha").
package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Version of the API. The page stops and shows "reload the page" when a
// snapshot carries a different version than it was built for.
const Version = 3

// HTTP surface, all under one origin.
const (
	PathSnapshot    = "/api/v1/snapshot"     // GET: Snapshot
	PathEvents      = "/api/v1/events"       // GET: server-sent events, event EventSnapshot, data Snapshot
	PathCatchup     = "/api/v1/catchup"      // GET: Catchup
	PathLeftovers   = "/api/v1/leftovers"    // GET: Leftovers
	PathJump        = "/api/v1/jump"         // POST CardRequest: focus the card's terminal
	PathSeen        = "/api/v1/seen"         // POST CardRequest: mark the card seen
	PathCatchupRead = "/api/v1/catchup/read" // POST, empty body: move the catch-up marker
)

// EventSnapshot is the server-sent event name; its data is a Snapshot.
// The server sends one on connect, then one after each change.
const EventSnapshot = "snapshot"

// ErrBadSessionID is returned by CardID for a session id outside the safe form.
var ErrBadSessionID = errors.New("contract: session id is not 1 to 128 of A-Z a-z 0-9 . _ -")

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// CardID is "<tool>:<sessionId>", for example "claude:1234". Session ids come
// from records on disk and are untrusted; ids reach the page and the state
// file, so a session id that does not match ^[A-Za-z0-9._-]{1,128}$ is refused
// with ErrBadSessionID, and that session gets no card.
func CardID(tool, sessionID string) (string, error) {
	if !sessionIDPattern.MatchString(sessionID) {
		return "", fmt.Errorf("%w: %d bytes", ErrBadSessionID, len(sessionID))
	}
	return tool + ":" + sessionID, nil
}

// ParseCardID splits a card id that came back from the page. It accepts only
// what CardID produces: a tool listed in Tools, one ":", and a session id that
// matches ^[A-Za-z0-9._-]{1,128}$. ok is false for anything else.
func ParseCardID(id string) (tool Tool, sessionID string, ok bool) {
	t, sid, found := strings.Cut(id, ":")
	if !found || !slices.Contains(Tools, Tool(t)) || !sessionIDPattern.MatchString(sid) {
		return "", "", false
	}
	return Tool(t), sid, true
}

// ProjectID is "p_" plus the first 12 hex characters of the SHA-256 of the main
// repo root (the folder itself when there is no repo). Stable across restarts,
// and it does not reveal the path.
func ProjectID(mainRoot string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(mainRoot)))
	return "p_" + hex.EncodeToString(sum[:])[:12]
}

// Column is where a card sits. Never stored; derived from the rules in the spec.
type Column string

const (
	ColumnNeedsYou        Column = "needs_you"
	ColumnWorking         Column = "working"
	ColumnWithCoordinator Column = "with_coordinator"
	ColumnInReview        Column = "in_review"
	ColumnDone            Column = "done"
	ColumnIdle            Column = "idle"
)

// Columns in the order the board shows them.
var Columns = []Column{ColumnNeedsYou, ColumnWorking, ColumnWithCoordinator, ColumnInReview, ColumnDone, ColumnIdle}

// Attention is a reason a card needs the owner.
type Attention string

const (
	AttentionBlocked       Attention = "blocked"
	AttentionUnseen        Attention = "unseen" // finished a turn the owner has not seen
	AttentionStale         Attention = "stale"  // busy with no activity for StaleAfter
	AttentionFailingChecks Attention = "failing_checks"
)

// Attentions lists every attention reason.
var Attentions = []Attention{AttentionBlocked, AttentionUnseen, AttentionStale, AttentionFailingChecks}

// TitleSource says where a card's title came from.
type TitleSource string

const (
	TitleFromPR      TitleSource = "pr"
	TitleFromSession TitleSource = "session"
	TitleFromFolder  TitleSource = "folder"
)

// TitleSources lists every title source.
var TitleSources = []TitleSource{TitleFromPR, TitleFromSession, TitleFromFolder}

// Tool is the agent tool.
type Tool string

const (
	ToolClaude Tool = "claude"
	ToolCodex  Tool = "codex"
)

// Tools lists every agent tool.
var Tools = []Tool{ToolClaude, ToolCodex}

// AgentStatus is what the page shows as the agent's state.
type AgentStatus string

const (
	AgentStatusBusy     AgentStatus = "busy"
	AgentStatusBlocked  AgentStatus = "blocked"
	AgentStatusFinished AgentStatus = "finished"
	AgentStatusEnded    AgentStatus = "ended"
	AgentStatusUnknown  AgentStatus = "unknown"
)

// AgentStatuses lists every agent status.
var AgentStatuses = []AgentStatus{AgentStatusBusy, AgentStatusBlocked, AgentStatusFinished, AgentStatusEnded, AgentStatusUnknown}

// Tri is a yes, no or unknown answer.
type Tri string

const (
	Yes        Tri = "yes"
	No         Tri = "no"
	TriUnknown Tri = "unknown"
)

// Tris lists every Tri answer.
var Tris = []Tri{Yes, No, TriUnknown}

// ParentState says whether an agent has a coordinator. "unknown" only when no
// parent was detected and there is a sign the agent is a worker.
type ParentState string

const (
	ParentKnown   ParentState = "known"
	ParentNone    ParentState = "none"
	ParentUnknown ParentState = "unknown"
)

// ParentStates lists every parent state.
var ParentStates = []ParentState{ParentKnown, ParentNone, ParentUnknown}

// PRState is a pull request's state.
type PRState string

const (
	PRStateOpen   PRState = "open"
	PRStateMerged PRState = "merged"
	PRStateClosed PRState = "closed"
)

// PRStates lists every pull request state.
var PRStates = []PRState{PRStateOpen, PRStateMerged, PRStateClosed}

// Checks is a pull request's rolled-up checks.
type Checks string

const (
	ChecksPassing Checks = "passing"
	ChecksFailing Checks = "failing"
	ChecksRunning Checks = "running"
	ChecksNone    Checks = "none"
	ChecksUnknown Checks = "unknown"
)

// CheckValues lists every rolled-up checks value.
var CheckValues = []Checks{ChecksPassing, ChecksFailing, ChecksRunning, ChecksNone, ChecksUnknown}

// Review is a pull request's review decision; "" when there is none.
type Review string

const (
	ReviewApproved         Review = "approved"
	ReviewChangesRequested Review = "changes_requested"
	ReviewRequired         Review = "review_required"
	ReviewNone             Review = ""
)

// Reviews lists every review decision.
var Reviews = []Review{ReviewApproved, ReviewChangesRequested, ReviewRequired, ReviewNone}

// SourceKind names a source.
type SourceKind string

const (
	SourceClaude SourceKind = "claude"
	SourceCodex  SourceKind = "codex"
	SourceAgterm SourceKind = "agterm"
	SourceGit    SourceKind = "git"
	SourceGitHub SourceKind = "github"
)

// SourceKinds lists every source, in the order the page shows them.
var SourceKinds = []SourceKind{SourceClaude, SourceCodex, SourceAgterm, SourceGit, SourceGitHub}

// SourceState is how a source is doing.
type SourceState string

const (
	SourceOK      SourceState = "ok"
	SourceMissing SourceState = "missing"
	SourceOff     SourceState = "off"
	SourceFailing SourceState = "failing"
)

// SourceStates lists every source state.
var SourceStates = []SourceState{SourceOK, SourceMissing, SourceOff, SourceFailing}

// CatchupKind is the kind of a catch-up item.
type CatchupKind string

const (
	CatchupTurnFinished CatchupKind = "turn_finished"
	CatchupPROpened     CatchupKind = "pr_opened"
	CatchupPRMerged     CatchupKind = "pr_merged"
)

// CatchupKinds lists every catch-up item kind.
var CatchupKinds = []CatchupKind{CatchupTurnFinished, CatchupPROpened, CatchupPRMerged}

// LeftoverKind is the kind of a leftover.
type LeftoverKind string

const (
	LeftoverWorktree LeftoverKind = "worktree"
	LeftoverBranch   LeftoverKind = "branch"
	LeftoverSession  LeftoverKind = "session"
)

// LeftoverKinds lists every leftover kind.
var LeftoverKinds = []LeftoverKind{LeftoverWorktree, LeftoverBranch, LeftoverSession}

// Snapshot is the whole board. Seq rises by one each time the content changes.
// Seq is per run: it restarts at zero when sitboard restarts, so the page
// compares it only within one connection to one run.
type Snapshot struct {
	Version       int            `json:"version"`
	Seq           uint64         `json:"seq"`
	GeneratedAt   time.Time      `json:"generatedAt"`
	Sources       []SourceStatus `json:"sources"` // one per source kind
	Projects      []Project      `json:"projects"`
	Cards         []Card         `json:"cards"`
	PlainShells   int            `json:"plainShells"` // terminal sessions with no agent, hidden
	Counts        Counts         `json:"counts"`      // totals over all projects
	AgtermPresent bool           `json:"agtermPresent"`
	Build         Build          `json:"build"`
}

// Build is the sitboard build that serves the page. Version and Commit are as
// the build set them (a release version, or "dev" and "unknown" for a local
// build); the page decides how to show them. OS and Arch are Go's GOOS and
// GOARCH.
type Build struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

// SourceStatus is one source's Probe result. Detail is one short line, "" when none.
//
// Ready is false only while a present watcher (Claude Code, Codex, agterm) is
// still on its first pass, that is, before it sent model.SourceReady. A source
// that is missing, off or failing is ready, and so are git and GitHub, which
// answer on demand. The page shows the board as still loading while any
// source is not ready.
type SourceStatus struct {
	Kind    SourceKind  `json:"kind"`
	State   SourceState `json:"state"`
	Version string      `json:"version"` // "" when unknown
	Detail  string      `json:"detail"`
	Tested  bool        `json:"tested"`
	Ready   bool        `json:"ready"`
}

// Counts per project or in total.
type Counts struct {
	Working  int `json:"working"`
	NeedsYou int `json:"needsYou"`
	InReview int `json:"inReview"`
	Stale    int `json:"stale"`
}

// Project is a git repo, or a folder when there is no repo.
//
// Projects group in one level. An umbrella is a plain folder (not inside a git
// repository, never the home folder or the root) in which an agent sits; a
// project whose main root is directly inside an umbrella is one of its parts.
// Counts are the project's own cards only: the page sums parts into their
// parent. Card.ProjectID stays the part.
type Project struct {
	ID       string `json:"id"`       // ProjectID
	Name     string `json:"name"`     // the folder's base name
	Path     string `json:"path"`     // home-relative, "~/..."; the base name only outside the home folder
	Repo     string `json:"repo"`     // "owner/name", "" when none
	ParentID string `json:"parentId"` // the umbrella this project is a part of; "" for a top-level project. A parent never has a parent.
	// Area is the base name of the folder that directly contains the top-level
	// project's folder (a part carries its parent's area). "" when that folder
	// is the home folder or the root, when the project is outside the home
	// folder, or when any folder of its path below home starts with ".".
	Area   string `json:"area"`
	Counts Counts `json:"counts"`
}

// Card is one agent session that is not an in-process subagent. Workers are
// cards too; in-process subagents show only as a count on their parent's card.
type Card struct {
	ID           string        `json:"id"` // CardID
	ProjectID    string        `json:"projectId"`
	Title        string        `json:"title"`
	TitleSource  TitleSource   `json:"titleSource"`
	Column       Column        `json:"column"`
	Attention    []Attention   `json:"attention"`
	Seen         bool          `json:"seen"`
	Agent        Agent         `json:"agent"`
	PullRequests []PullRequest `json:"pullRequests"`
	Issues       []Issue       `json:"issues"`
	Subagents    int           `json:"subagents"` // in-process subagents, shown as a count
}

// Agent is the session behind a card.
type Agent struct {
	Tool         Tool        `json:"tool"`
	Name         string      `json:"name"`  // "" when the session has no name
	Model        string      `json:"model"` // "" when unknown
	Status       AgentStatus `json:"status"`
	StatusSince  time.Time   `json:"statusSince,omitzero"`
	StartedAt    time.Time   `json:"startedAt,omitzero"`
	LastActivity time.Time   `json:"lastActivity,omitzero"`
	Live         Tri         `json:"live"`
	Folder       string      `json:"folder"` // home-relative; "" when unknown
	Parent       ParentState `json:"parent"`
	ParentCardID string      `json:"parentCardId"` // "" when the parent has no card
	TerminalID   string      `json:"terminalId"`   // agterm session id, "" when none
	Workspace    string      `json:"workspace"`    // agterm workspace name, "" when none
}

// PullRequest linked to a card.
type PullRequest struct {
	Repo   string  `json:"repo"` // "owner/name"
	Number int     `json:"number"`
	Title  string  `json:"title"`
	URL    string  `json:"url"`
	State  PRState `json:"state"`
	Checks Checks  `json:"checks"`
	Review Review  `json:"review"`
	Draft  bool    `json:"draft"`
}

// Issue a linked pull request closes.
type Issue struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// Catchup is what happened since the marker, up to 7 days back, newest first.
type Catchup struct {
	Marker time.Time     `json:"marker,omitzero"` // absent before the first "mark read"
	Items  []CatchupItem `json:"items"`
}

// CatchupItem is one finished turn, opened or merged pull request.
type CatchupItem struct {
	At          time.Time    `json:"at"`
	Kind        CatchupKind  `json:"kind"`
	CardID      string       `json:"cardId"` // "" when the card has left the board
	ProjectID   string       `json:"projectId"`
	AgentName   string       `json:"agentName"`
	Title       string       `json:"title"`
	PullRequest *PullRequest `json:"pullRequest,omitempty"`
}

// Leftovers lists merged work whose worktree, branch or session still exists.
type Leftovers struct {
	Items []Leftover `json:"items"`
}

// Leftover is one worktree, branch or idle session left after a merge.
type Leftover struct {
	Kind        LeftoverKind `json:"kind"`
	ProjectID   string       `json:"projectId"`
	Path        string       `json:"path"`   // home-relative; "" for a branch or session
	Branch      string       `json:"branch"` // "" when none
	CardID      string       `json:"cardId"` // "" when none
	PullRequest *PullRequest `json:"pullRequest,omitempty"`
	IdleSince   time.Time    `json:"idleSince,omitzero"` // sessions only
}

// CardRequest is the body of POST PathJump and POST PathSeen. Any other field
// is ignored; in particular a terminal id sent by the page is never used.
type CardRequest struct {
	CardID string `json:"cardId"`
}
