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
//   - No transcript content, no absolute path, no token ever appears here,
//     with three exceptions: Artifact.Title (the title its own publish
//     reported), Agent.Name of a Claude session whose record has no name (the
//     agent-name its transcript records), which CatchupItem.AgentName
//     repeats, and Agent.Name of a Codex session (the thread title in
//     Codex's session_index.jsonl, which the model wrote from the
//     conversation or the person renamed; the adapter cuts it at 1,024 bytes).
//     All three are free text taken from the session's records, cleaned of
//     control and invisible characters and capped like every other title or
//     name. The only other word read from a Codex record is the effort
//     (Agent.EffortWord), held to [a-z]{1,16}.
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
const Version = 16

// HTTP surface, all under one origin.
const (
	PathSnapshot    = "/api/v1/snapshot"     // GET: Snapshot
	PathEvents      = "/api/v1/events"       // GET: server-sent events, event EventSnapshot, data Snapshot
	PathCatchup     = "/api/v1/catchup"      // GET: Catchup
	PathLeftovers   = "/api/v1/leftovers"    // GET: Leftovers
	PathJump        = "/api/v1/jump"         // POST CardRequest: focus the card's terminal
	PathSeen        = "/api/v1/seen"         // POST CardRequest: mark the card seen
	PathCatchupRead = "/api/v1/catchup/read" // POST CatchupReadRequest: move the catch-up marker
	// POST ProjectRequest: forget the grouping of a project that has parts.
	PathForgetGrouping = "/api/v1/grouping/forget"
	// POST GroupingRequest: set a folder's grouping to auto, always or never.
	PathGroupingSetting = "/api/v1/grouping/setting"
)

// ArtifactURLPattern is the only form of Artifact.URL: a claude.ai artifact
// address. The page links only a URL that matches it. Equal to
// model.ArtifactURLPattern, which the adapters check against.
const ArtifactURLPattern = `^https://claude\.ai/(?:artifact/[A-Za-z0-9_-]{8,64}|code/artifact/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`

// PullRequestURLPattern is the only form of a GitHub PullRequest.URL and
// IssueURLPattern the only form of a GitHub Issue.URL: an address on
// github.com built from the repo and the number. The page links only a URL
// that matches. Equal to model.PullRequestURLPattern and
// model.IssueURLPattern.
const (
	PullRequestURLPattern = `^https://github\.com/[A-Za-z0-9_][A-Za-z0-9_-]*/\.*[A-Za-z0-9_-][A-Za-z0-9_.-]*/pull/[1-9][0-9]{0,9}$`
	IssueURLPattern       = `^https://github\.com/[A-Za-z0-9_][A-Za-z0-9_-]*/\.*[A-Za-z0-9_-][A-Za-z0-9_.-]*/issues/[1-9][0-9]{0,9}$`
)

// MergeRequestURLPattern is the only form of a GitLab PullRequest.URL and
// GitLabIssueURLPattern the only form of a GitLab Issue.URL: built from the
// host, the project path and the number. Group 1 is the host, which must also
// be one of Snapshot.GitLabHosts; group 2 the path, group 3 the number. Equal
// to model.MergeRequestURLPattern and model.GitLabIssueURLPattern.
const (
	MergeRequestURLPattern = `^https://([a-z0-9.-]+)/([A-Za-z0-9_][A-Za-z0-9_.-]*(?:/[A-Za-z0-9_][A-Za-z0-9_.-]*){1,21})/-/merge_requests/([1-9][0-9]{0,9})$`
	GitLabIssueURLPattern  = `^https://([a-z0-9.-]+)/([A-Za-z0-9_][A-Za-z0-9_.-]*(?:/[A-Za-z0-9_][A-Za-z0-9_.-]*){1,21})/-/issues/([1-9][0-9]{0,9})$`
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

var projectIDPattern = regexp.MustCompile(`^p_[0-9a-f]{12}$`)

// ValidProjectID reports whether id has the form ProjectID produces. Ids that
// come back from the page are checked with it before they are looked up.
func ValidProjectID(id string) bool { return projectIDPattern.MatchString(id) }

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

// Effort is a session's reasoning effort level, lowest first. "" when unknown.
type Effort string

const (
	EffortUnknown Effort = ""
	EffortLow     Effort = "low"
	EffortMedium  Effort = "medium"
	EffortHigh    Effort = "high"
	EffortXHigh   Effort = "xhigh"
	EffortMax     Effort = "max"
)

// Efforts lists every effort value.
var Efforts = []Effort{EffortUnknown, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax}

// EffortSource says where Agent.Effort comes from.
//   - recorded: the level the session's last reply ran at, from its
//     transcript. After a change (/effort, /model) it is the old level until
//     the next reply.
//   - launch: before the first reply, the level the process was started with
//     (its --effort flag, or CLAUDE_CODE_EFFORT_LEVEL, which overrides the
//     flag). The first reply replaces it.
//   - none: no level is known.
type EffortSource string

const (
	EffortSourceRecorded EffortSource = "recorded"
	EffortSourceLaunch   EffortSource = "launch"
	EffortSourceNone     EffortSource = "none"
)

// EffortSources lists every effort source.
var EffortSources = []EffortSource{EffortSourceRecorded, EffortSourceLaunch, EffortSourceNone}

// JumpKind says where a card's jump goes: its agterm session, its tmux pane
// (a session in tmux inside agterm goes through tmux, which also raises the
// agterm session showing it), or none: the page shows the folder and name to
// copy instead.
type JumpKind string

const (
	JumpAgterm JumpKind = "agterm"
	JumpTmux   JumpKind = "tmux"
	JumpNone   JumpKind = "none"
)

// JumpKinds lists every jump kind.
var JumpKinds = []JumpKind{JumpAgterm, JumpTmux, JumpNone}

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
	SourceGitLab SourceKind = "gitlab"
)

// SourceKinds lists every source, in the order the page shows them.
var SourceKinds = []SourceKind{SourceClaude, SourceCodex, SourceAgterm, SourceGit, SourceGitHub, SourceGitLab}

// Forge is where a repository's pull or merge requests live.
type Forge string

const (
	ForgeGitHub Forge = "github"
	ForgeGitLab Forge = "gitlab"
	ForgeNone   Forge = "" // Project.RepoForge only: no repository on a forge
)

// Forges lists every forge value.
var Forges = []Forge{ForgeGitHub, ForgeGitLab, ForgeNone}

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
	// CatchupArtifactPublished: a claude.ai artifact the session published.
	CatchupArtifactPublished CatchupKind = "artifact_published"
)

// CatchupKinds lists every catch-up item kind.
var CatchupKinds = []CatchupKind{CatchupTurnFinished, CatchupPROpened, CatchupPRMerged, CatchupArtifactPublished}

// LeftoverKind is the kind of a leftover.
type LeftoverKind string

const (
	LeftoverWorktree     LeftoverKind = "worktree"
	LeftoverBranch       LeftoverKind = "branch"
	LeftoverSession      LeftoverKind = "session"
	LeftoverRemoteBranch LeftoverKind = "remoteBranch" // the branch on GitHub
)

// LeftoverKinds lists every leftover kind.
var LeftoverKinds = []LeftoverKind{LeftoverWorktree, LeftoverBranch, LeftoverSession, LeftoverRemoteBranch}

// LeftoversState says whether the leftovers list could be made.
type LeftoversState string

const (
	LeftoversReady   LeftoversState = "ready"
	LeftoversLoading LeftoversState = "loading" // the first look is under way
	LeftoversOff     LeftoversState = "off"     // GitHub is off or missing: no evidence, so nothing is listed
	LeftoversNoGit   LeftoversState = "noGit"   // git is missing
)

// LeftoversStates lists every leftovers state.
var LeftoversStates = []LeftoversState{LeftoversReady, LeftoversLoading, LeftoversOff, LeftoversNoGit}

// HeldReason says why merged work was held back from the list.
type HeldReason string

const (
	HeldCommits HeldReason = "commits" // commits after the merged pull request
	HeldChanges HeldReason = "changes" // modified, staged or untracked files
	HeldStash   HeldReason = "stash"   // a stash belongs to the branch
	HeldInUse   HeldReason = "inUse"   // a process works in the worktree
	HeldLocked  HeldReason = "locked"  // the worktree is locked
	HeldNested  HeldReason = "nested"  // another checkout sits inside the worktree
	HeldUnknown HeldReason = "unknown" // git, GitHub or the process list could not tell
)

// HeldReasons lists every held-back reason, in the order the page shows them.
var HeldReasons = []HeldReason{HeldCommits, HeldChanges, HeldStash, HeldInUse, HeldLocked, HeldNested, HeldUnknown}

// GroupingMode is the owner's setting for a folder. Auto (no setting) groups
// as sitboard sees it: a plain folder where agents sit, remembered for 30
// days, unless forgotten. Always groups the folder whenever one of its parts
// is on the board, without expiry. Never: the folder never groups.
type GroupingMode string

const (
	GroupingAuto   GroupingMode = "auto"
	GroupingAlways GroupingMode = "always"
	GroupingNever  GroupingMode = "never"
)

// GroupingModes lists every grouping mode.
var GroupingModes = []GroupingMode{GroupingAuto, GroupingAlways, GroupingNever}

// GroupingSetting is a folder the owner set to always or never group, whether
// or not it is on the board. A folder on auto is not listed.
type GroupingSetting struct {
	ProjectID string       `json:"projectId"` // ProjectID of the folder; the id to send back in a GroupingRequest
	Name      string       `json:"name"`      // the folder's base name
	Path      string       `json:"path"`      // home-relative, "~/..."; the base name only outside the home folder
	Mode      GroupingMode `json:"mode"`      // GroupingAlways or GroupingNever
}

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
	// GroupingSettings are the folders set to always or never group, sorted
	// by name, then project id.
	GroupingSettings []GroupingSetting `json:"groupingSettings"`
	// Leftovers is what the status bar shows of the list GET PathLeftovers
	// gives now; all zero when the list is not ready.
	Leftovers LeftoversSummary `json:"leftovers"`
	// Spend is the token use and estimated cost of every session sitboard
	// knows, ended ones included, by day for the last 7 days and by project.
	Spend SpendSummary `json:"spend"`
	// GitLabHosts are the only hosts a GitLab link (forge "gitlab") may point
	// at: those sitboard looked merge requests up on, sorted. The page drops
	// any GitLab link whose host is not listed.
	GitLabHosts []string `json:"gitlabHosts"`
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
	ID   string `json:"id"`   // ProjectID
	Name string `json:"name"` // the folder's base name
	Path string `json:"path"` // home-relative, "~/..."; the base name only outside the home folder
	Repo string `json:"repo"` // GitHub "owner/name" or GitLab "group/…/project", "" when none
	// RepoForge and RepoHost say where Repo is: ForgeNone and "" when none.
	RepoForge Forge  `json:"repoForge"`
	RepoHost  string `json:"repoHost"`
	ParentID  string `json:"parentId"` // the umbrella this project is a part of; "" for a top-level project. A parent never has a parent.
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
	Artifacts    []Artifact    `json:"artifacts"` // most recently published first
	Subagents    int           `json:"subagents"` // in-process subagents, shown as a count
	// Spend is the session's own, with its in-process subagents; all zero
	// when none was recorded.
	Spend Spend `json:"spend"`
}

// Tokens are token counts as the agent tool records them.
type Tokens struct {
	Input      int64 `json:"input"` // input not read from the cache
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
}

// Spend is token use with its cost estimated at API list price: the agent
// tool's own estimate, never a bill (on a subscription nobody pays it). USD is
// the cost of the use that has a price. Priced says whether all of it has one:
// when it is false, some use has no price (a model missing from the price
// table, say) and USD is only the priced part, so the page shows "≈ $X +
// unpriced", or tokens alone when USD is 0. A total is never shown as complete
// when it is not. A Codex card's Spend is tokens only (usd 0, priced false):
// Codex keeps no price on the machine.
type Spend struct {
	Tokens Tokens  `json:"tokens"`
	USD    float64 `json:"usd"` // the priced part; all of it when Priced
	Priced bool    `json:"priced"`
	// Recorded: USD is the total the tool itself wrote when the session ended
	// (it also counts calls that leave no transcript line); otherwise it is
	// computed from the counted tokens and the tool's price table.
	Recorded bool `json:"recorded"`
}

// SpendSummary is spend by day and project. A day's figures count the
// replies made that day, priced; they can add up to a little less than the
// sessions' recorded totals.
type SpendSummary struct {
	Days []SpendDay `json:"days"` // the last 7 days, oldest first; only days with use
	// Pending: older sessions are still being read, so the figures can grow.
	Pending bool `json:"pending"`
}

// SpendDay is one day's spend. Spend is Claude Code's (tokens and its cost
// estimate); Codex is Codex's tokens, beside it and never in it: Codex keeps
// no price, so its use would make every total it joined "unpriced". A day is
// listed when either has use.
type SpendDay struct {
	Day      string         `json:"day"` // the local date on the machine, "2006-01-02"
	Spend    Spend          `json:"spend"`
	Codex    Tokens         `json:"codex"`
	Projects []ProjectSpend `json:"projects"` // by name
}

// ProjectSpend is one project's spend within a day. A project with no card
// on the board (all its sessions ended) is named here only.
type ProjectSpend struct {
	ProjectID string `json:"projectId"` // ProjectID; "" for sessions with no known folder
	Name      string `json:"name"`      // the folder's base name; "" with no folder
	Path      string `json:"path"`      // home-relative, "~/..."; the base name only outside the home folder
	Spend     Spend  `json:"spend"`     // Claude Code's, as in SpendDay
	Codex     Tokens `json:"codex"`     // Codex's tokens, as in SpendDay
}

// Agent is the session behind a card.
type Agent struct {
	Tool  Tool   `json:"tool"`
	Name  string `json:"name"`  // "" when the session has no name
	Model string `json:"model"` // "" when unknown
	// Effort is the reasoning effort level; EffortSource says where it came
	// from. EffortUnknown always goes with EffortSourceNone.
	Effort       Effort       `json:"effort"`
	EffortSource EffortSource `json:"effortSource"`
	// EffortWord is an effort word the tool recorded that is not one of the
	// Effort levels (Codex's "ultra", "minimal"), as written: lowercase
	// letters only, at most 16. "" otherwise. When it is set, Effort is
	// EffortUnknown: the word has no place on the ramp.
	EffortWord   string      `json:"effortWord"`
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
	// Jump is where a jump to this card goes. The tmux pane and socket stay
	// on the server.
	Jump JumpKind `json:"jump"`
}

// PullRequest linked to a card: a GitHub pull request, or a GitLab merge
// request (forge "gitlab", shown as !Number). The name stays for both.
type PullRequest struct {
	Forge  Forge  `json:"forge"`  // ForgeGitHub or ForgeGitLab, never ForgeNone
	Host   string `json:"host"`   // "github.com"; for GitLab one of Snapshot.GitLabHosts
	Repo   string `json:"repo"`   // GitHub "owner/name"; GitLab "group/…/project"
	Number int    `json:"number"` // GitLab: the merge request's iid
	Title  string `json:"title"`
	// URL matches PullRequestURLPattern (GitHub) or MergeRequestURLPattern
	// (GitLab) and is the address built from host, repo and number; "" when
	// none can be built.
	URL    string  `json:"url"`
	State  PRState `json:"state"`
	Checks Checks  `json:"checks"`
	Review Review  `json:"review"`
	Draft  bool    `json:"draft"`
}

// Artifact is a claude.ai artifact the card's session published itself. The
// link is private to the owner, like everything else here.
type Artifact struct {
	Title string `json:"title"` // as the session's latest publish of it reported; "" when none
	URL   string `json:"url"`   // matches ArtifactURLPattern
}

// Issue a linked pull request closes: on the same forge, host and repo.
type Issue struct {
	Forge  Forge  `json:"forge"`
	Host   string `json:"host"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	// URL matches IssueURLPattern (GitHub) or GitLabIssueURLPattern (GitLab);
	// "" when none can be built.
	URL string `json:"url"`
}

// Catchup is what the board learned since the marker, up to 7 days back,
// newest first by the time each thing happened. An item counts as unread when
// the board learned it after the marker, even when it happened earlier (a
// merge GitHub reports a minute late). Items that happened before this run of
// sitboard started count by their own time, so a restart does not list again
// what was already read.
type Catchup struct {
	Marker time.Time `json:"marker,omitzero"` // absent before the first "mark read"
	// AsOf is when this list was made. The page sends it back as UpTo: it
	// covers exactly the items in this list and nothing learned later.
	AsOf  time.Time `json:"asOf"`
	Total int       `json:"total"` // unread items before the cap; > len(Items) when some were left out
	// Unsaved: the marker lives in memory only (--no-state), so every start
	// of sitboard begins with no marker again.
	Unsaved bool `json:"unsaved,omitempty"`
	// Pending: sessions that had ended before sitboard started are still
	// being read, so items from them may still arrive.
	Pending bool          `json:"pending,omitempty"`
	Items   []CatchupItem `json:"items"`
}

// CatchupItem is one finished turn, opened or merged pull request, or
// published artifact.
type CatchupItem struct {
	At          time.Time    `json:"at"`
	Kind        CatchupKind  `json:"kind"`
	CardID      string       `json:"cardId"` // "" when the card has left the board
	ProjectID   string       `json:"projectId"`
	ProjectName string       `json:"projectName"` // as Project.Name: the project may have left the board
	AgentName   string       `json:"agentName"`
	Title       string       `json:"title"`
	PullRequest *PullRequest `json:"pullRequest,omitempty"` // pr_opened and pr_merged only
	Artifact    *Artifact    `json:"artifact,omitempty"`    // artifact_published only
}

// CatchupReadRequest is the body of POST PathCatchupRead. UpTo is the AsOf of
// the list the page showed: the marker moves there, never past the newest
// list made, never past the server's now and never back, so an item the board
// learned after that list stays unread. Any other field is ignored.
type CatchupReadRequest struct {
	UpTo time.Time `json:"upTo"`
}

// Leftovers lists merged work whose worktree, branch or session still exists,
// for the repositories of the projects on the board. Only what positive
// evidence shows to hold nothing more than a merged pull request is listed; merged work
// that still holds something is only counted, by reason. The list is for reading: sitboard removes nothing
// and offers no way to.
type Leftovers struct {
	State LeftoversState `json:"state"`
	AsOf  time.Time      `json:"asOf,omitzero"` // when the list was made; absent before the first
	Items []Leftover     `json:"items"`         // the newest merge first
	Held  []HeldCount    `json:"held"`          // in HeldReasons order; reasons with none are absent
	// Partial: the list may be incomplete: a repository could not be looked
	// at, had more branches than one look checks (100), or GitHub may not
	// have given every pull request of a branch.
	Partial bool `json:"partial"`
}

// LeftoversSummary is the leftovers list in numbers, for the status bar.
type LeftoversSummary struct {
	Items   int  `json:"items"`   // items listed
	Held    int  `json:"held"`    // merged work held back, every reason
	Partial bool `json:"partial"` // the list may be incomplete
}

// HeldCount is how many merged branches, or sessions that could be closed,
// were held back for one reason.
type HeldCount struct {
	Reason HeldReason `json:"reason"`
	Count  int        `json:"count"`
}

// Leftover is one merged pull request whose parts are still around.
type Leftover struct {
	ProjectID   string         `json:"projectId"`
	PullRequest PullRequest    `json:"pullRequest"`
	MergedAt    time.Time      `json:"mergedAt,omitzero"`
	Parts       []LeftoverPart `json:"parts"` // session, worktree, branch, then the branch on GitHub
	// Waiting: the worktree's only users are leftover sessions.
	Waiting bool `json:"waiting"`
	// OwnSession: waiting, and every session in the worktree is a part of
	// this row (not of another pull request's row).
	OwnSession bool `json:"ownSession"`
}

// LeftoverPart is one thing left over.
type LeftoverPart struct {
	Kind LeftoverKind `json:"kind"`
	// Path: worktrees: the worktree; branches: the clone's main checkout.
	// Home-relative and exact (nothing cleaned): sitboard lists no part
	// whose path is outside the home folder. "" otherwise.
	Path      string    `json:"path"`
	Branch    string    `json:"branch"`             // branches: the name, exact; "" otherwise
	CardID    string    `json:"cardId"`             // sessions: the card; "" otherwise
	Name      string    `json:"name"`               // sessions: the agent's name; "" when it has none
	IdleSince time.Time `json:"idleSince,omitzero"` // sessions only
	Ignored   int       `json:"ignored"`            // worktrees: how many git-ignored entries it holds
}

// CardRequest is the body of POST PathJump and POST PathSeen. Any other field
// is ignored; in particular a terminal id sent by the page is never used.
type CardRequest struct {
	CardID string `json:"cardId"`
}

// ProjectRequest is the body of POST PathForgetGrouping. Any other field is
// ignored; the server resolves the folder from the project id itself.
type ProjectRequest struct {
	ProjectID string `json:"projectId"`
}

// GroupingRequest is the body of POST PathGroupingSetting. ProjectID is a
// grouping on the board or a folder in Snapshot.GroupingSettings; the server
// resolves the folder from it. Any other field is ignored.
type GroupingRequest struct {
	ProjectID string       `json:"projectId"`
	Mode      GroupingMode `json:"mode"`
}
