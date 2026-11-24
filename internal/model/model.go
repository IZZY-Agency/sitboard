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
	"math"
	"path"
	"regexp"
	"strconv"
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

// PRRef points at a pull request (GitHub) or a merge request (GitLab): the
// names say "pull request" for both. Comparable, so it keys maps.
type PRRef struct {
	Repo   ForgeRepo
	Number int // GitHub's number; GitLab's iid (shown as !12)
}

// PushedRef is a merge request that a `git push -o merge_request.create`
// announced. GitLab prints the same answer whether the push created it or one
// was already open, so the core credits Ref (also in CreatedPRs) only when
// GitLab says it was created between From and To, widened for clock skew. Kept in
// memory only.
type PushedRef struct {
	Ref      PRRef
	From, To time.Time // the push call's start and its result
}

// ArtifactURLPattern is the only form of artifact link a fact may carry: a
// claude.ai artifact address, nothing else. contract.ArtifactURLPattern, which
// the page checks links against, must stay equal to it (a test checks).
const ArtifactURLPattern = `^https://claude\.ai/(?:artifact/[A-Za-z0-9_-]{8,64}|code/artifact/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`

var artifactURLRe = regexp.MustCompile(ArtifactURLPattern)

// ValidArtifactURL reports whether u matches ArtifactURLPattern.
func ValidArtifactURL(u string) bool { return artifactURLRe.MatchString(u) }

// GitHubHost is the only host pull requests are looked up on: the git source
// turns only github.com remotes into a repo slug, and every gh call names this
// host. A link to a pull request or an issue is built for it alone, from the
// repo slug and the number, never taken from a tool's output.
const GitHubHost = "github.com"

// githubRepoPattern is "owner/name" as a link may carry it: an owner of
// letters, digits, "_" and "-" that does not start with "-", and a name of
// letters, digits, "_", "." and "-" that is not all dots (so never "." or "..").
const githubRepoPattern = `[A-Za-z0-9_][A-Za-z0-9_-]*/\.*[A-Za-z0-9_-][A-Za-z0-9_.-]*`

// PullRequestURLPattern and IssueURLPattern are the only forms of pull
// request and issue link that reach the page. contract.PullRequestURLPattern
// and contract.IssueURLPattern, which the page checks links against, must stay
// equal to them (a test checks).
const (
	PullRequestURLPattern = `^https://github\.com/` + githubRepoPattern + `/pull/[1-9][0-9]{0,9}$`
	IssueURLPattern       = `^https://github\.com/` + githubRepoPattern + `/issues/[1-9][0-9]{0,9}$`
)

var (
	pullRequestURLRe = regexp.MustCompile(PullRequestURLPattern)
	issueURLRe       = regexp.MustCompile(IssueURLPattern)
)

// PullRequestURL is the address of pull request n of repo ("owner/name") on
// GitHubHost, or "" when repo or n cannot make one that matches
// PullRequestURLPattern.
func PullRequestURL(repo string, n int) string { return githubURL(pullRequestURLRe, repo, "pull", n) }

// IssueURL is the address of issue n of repo on GitHubHost, or "" when repo or
// n cannot make one that matches IssueURLPattern.
func IssueURL(repo string, n int) string { return githubURL(issueURLRe, repo, "issues", n) }

// GitHub numbers pull requests and issues with a 32-bit Int.
func githubURL(re *regexp.Regexp, repo, kind string, n int) string {
	if n < 1 || n > math.MaxInt32 {
		return ""
	}
	u := "https://" + GitHubHost + "/" + repo + "/" + kind + "/" + strconv.Itoa(n)
	if !re.MatchString(u) {
		return ""
	}
	return u
}

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

	Model Known[string]
	// Effort is the reasoning effort level, one of EffortLevels or another
	// word the tool recorded. Claude Code: the level the session's last reply
	// ran at, as its transcript records it. Before the first reply, the level
	// the process was launched with (EffortAtLaunch); it is replaced by the
	// recorded one. Codex: the effort of the latest turn, as written.
	Effort         Known[string]
	EffortAtLaunch bool

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
	// PID is the process running the session while it is live, as the tool
	// recorded it and its start time confirmed; 0 when unknown or not live.
	PID int

	// Parent is only for tools that record a parent natively as a separate
	// session. None does today: Codex's parent_thread_id names the thread
	// that spawned an in-process subagent, which is counted in Subagents
	// instead. Adapters never derive it from spawn commands or messages;
	// OK=false means no native parent was recorded.
	Parent Known[SessionKey]

	// Who spawned whom, for tools without a native parent (Claude Code,
	// Codex). These
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
	// Backfill: the session had ended before sitboard started and is known
	// only from its transcript. It feeds catch-up and pull request lookups,
	// never the board: it takes no part in matching who spawned whom.
	Backfill bool
	// Headless: its transcript says it was started without a person at it
	// (claude -p, an SDK, the MCP server). Catch-up leaves it out; the board
	// shows it as before.
	Headless bool

	// PeerSenders are the names of the sessions that sent this session a
	// message (Claude Code: user lines with origin.kind "peer"), deduplicated
	// and capped. Never the message text. The core derives the worker hint
	// from it after matching: a sender that is not one of this session's
	// matched children is a sign that this session is someone's worker.
	PeerSenders []string

	TerminalID string // agterm session id, "" when none
	// Tmux is the tmux pane the session runs in, zero when none. A session in
	// tmux inside agterm has both; the jump goes through tmux.
	Tmux       TmuxPane
	Subagents  int     // in-process subagents seen
	CreatedPRs []PRRef // only pull and merge requests this session created
	// PushedPRs are those of CreatedPRs that a push option announced, with
	// the push call's times; none until the transcript matchers learn
	// GitLab's push option.
	PushedPRs []PushedRef
	// Artifacts are the claude.ai artifacts this session (or one of its
	// in-process subagents) published itself, one per URL. A delete is not
	// followed, and a rename made anywhere else is not seen.
	Artifacts []ArtifactRef
	TurnEnds  []time.Time // finished turns, last 7 days, oldest first

	// Spend is the session's token use and its estimated cost, with its
	// in-process subagents. Its zero value means none was recorded.
	Spend Spend
}

// Tokens are token counts as the tool records them for its replies.
type Tokens struct {
	Input      int64 // input not read from the cache
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

// Add returns the sum of t and u.
func (t Tokens) Add(u Tokens) Tokens {
	return Tokens{t.Input + u.Input, t.Output + u.Output, t.CacheRead + u.CacheRead, t.CacheWrite + u.CacheWrite}
}

// IsZero reports whether no token was counted.
func (t Tokens) IsZero() bool { return t == Tokens{} }

// Spend is token use with its cost estimated at API list price. USD is an
// estimate, never a bill, and means something only when Priced: a model
// without a price makes the whole figure unpriced, so no part of a cost is
// ever guessed.
type Spend struct {
	Tokens Tokens
	USD    float64
	Priced bool
	// Recorded: USD is the total the tool itself wrote when the session
	// ended (Claude Code's cost-state line), which also counts calls that
	// leave no transcript line. Otherwise it is computed from the counted
	// tokens and the tool's price table.
	Recorded bool
	// Spans split the counted tokens and their computed cost by the quarter
	// hour they were used in (UTC), oldest first, for the last 7 days only. A
	// quarter hour never straddles a local midnight, in any time zone. Their
	// sum can be lower than the whole (older spans, and a
	// recorded total).
	Spans []SpendSpan
}

// SpendSpanLength is the length of a SpendSpan: the largest step that every
// time zone's midnight (offsets are whole quarter hours) falls on.
const SpendSpanLength = 15 * time.Minute

// SpendSpan is the spend within one SpendSpanLength.
type SpendSpan struct {
	At     time.Time // the start of the span, UTC
	Tokens Tokens
	USD    float64
	Priced bool
}

// EffortLevels are the reasoning effort levels sitboard ranks, lowest first
// (Claude Code's). Another word a tool records (Codex's "ultra") is passed on
// as written, without a place on the ramp.
var EffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// KnownEffort returns the EffortLevels constant equal to s, otherwise "".
func KnownEffort(s string) string {
	for _, l := range EffortLevels {
		if s == l {
			return l
		}
	}
	return ""
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

// BackfillDone says a watcher has read the sessions that had ended before
// sitboard started (Kind as in SourceReady). Until it comes, catch-up may
// still grow with items from earlier sessions.
//
// Cut: that read stopped at a bound (its time budget, or a file past the
// size it reads), so some of those sessions were not read.
type BackfillDone struct {
	Kind string
	Cut  bool
}

// Batch is facts taken together, in order: the board applies them all and
// rebuilds once. A watcher publishes the sessions it read at start this way,
// in bounded chunks, so that thousands of ended sessions do not cost one
// rebuild each. A Batch holds no Batch.
type Batch struct{ Facts []Fact }

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
func (BackfillDone) isFact()     {}
func (Batch) isFact()            {}

// Repo is what git says about a folder.
type Repo struct {
	Root     string        // the repo (or worktree) root; the folder itself when it is not in a repo
	MainRoot string        // the main repo root a worktree belongs to; == Root for a main checkout or no repo
	Branch   Known[string] // OK=false on a detached HEAD or outside a repo
	// Remote is the repository on a forge that origin's one fetch address
	// names; zero when none, several, or on no forge sitboard knows.
	Remote ForgeRepo
	// RemoteBare: Remote is on GitLab and came from an ssh, git or scp-like
	// address, whose path has no GitLab subfolder in front (the core adds
	// the host's) and whose port, if any, was git's, not the web one.
	RemoteBare bool
	// OriginUnclear: origin has more than one fetch address, one of them on
	// GitHub. Remote is zero: which GitHub repository it is cannot be told.
	OriginUnclear bool
}

// Worktree is one git worktree of a repo, as `git worktree list` gives it.
type Worktree struct {
	Path     string
	Branch   Known[string] // OK=false on a detached HEAD
	Main     bool          // the main checkout (listed first)
	Locked   bool
	Prunable bool // its folder is gone
	Bare     bool
	// Busy are the branches an operation under way in this worktree holds:
	// the one a rebase or bisect will return to, and those a rebase
	// --update-refs queued; none when none.
	Busy []string
	// GitDir is the worktree's own git folder (the common one for the main
	// checkout), absolute.
	GitDir string
	// Operation: a merge, cherry-pick, revert, rebase, bisect or sequence is
	// under way here; removing the worktree would discard it.
	Operation bool
}

// Proc is one running process of this user and the folder it works in.
type Proc struct {
	PID, PPID int
	Cwd       string // absolute and cleaned
}

// Branch is a local branch and the commit it points at.
type Branch struct {
	Name string // without refs/heads/
	Tip  string // full commit id
	// Symbolic: the ref is a symbolic ref, or another symbolic ref under
	// refs/heads points at it; deleting it would delete or break another.
	Symbolic bool
}

// Stash is one entry of `git stash list`.
type Stash struct {
	Base    string // the commit the stash was made on
	Subject string // "On <branch>: ..." or "WIP on <branch>: ..."; git's own reflog subject
}

// RepoInventory is what git says about a repo's worktrees, branches and
// stashes, read in one round of read-only calls.
type RepoInventory struct {
	Worktrees []Worktree
	// WorktreesListed: Worktrees holds every checkout git has registered,
	// each on a folder that can be named. It matters only beside an error:
	// without it, a checkout of this repo may sit anywhere, inside a
	// worktree of another repo too.
	WorktreesListed bool
	Branches        []Branch // local branches
	Remote          []Branch // branches under refs/remotes/origin/, without the prefix; HEAD left out
	Stashes         []Stash
	// CommonDir is the repo's shared git folder, absolute.
	CommonDir string
	// Origin is origin's one fetch address as a GitHub repository; zero
	// otherwise (none, several, or elsewhere). Leftovers is GitHub only.
	Origin ForgeRepo
	// OriginUnclear: origin has more than one address, one of them on
	// GitHub (as model.Repo.OriginUnclear). Read before the stashes: it holds
	// beside a later error too, as Origin does.
	OriginUnclear bool
}

// TreeStatus is what git says about one worktree: where it found it, what
// is checked out there, and its files.
type TreeStatus struct {
	Clean   bool // nothing modified, staged or untracked
	Ignored int  // ignored entries (a wholly ignored folder counts once)
	// Top, GitDir and CommonDir are the worktree's folder, its own git
	// folder and the repo's shared one, absolute, as git finds them from the
	// folder: a folder replaced since the inventory shows other ones.
	Top, GitDir, CommonDir string
	Head                   string // the checked-out ref, "refs/heads/<b>"; "" when detached
	Tip                    string // the checked-out commit
}

// PREvidence is GitHub's fresh answer about head branch names, for
// Leftovers.
type PREvidence struct {
	PRs []PullRequest
	// Incomplete names the heads whose pull requests may not all be in PRs:
	// more than one page of them, or an answer with a part missing.
	Incomplete map[string]bool
	// Bases names the heads that are the base branch of an open pull
	// request into the repository (a stacked one): the branch on GitHub is
	// still in use.
	Bases map[string]bool
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

// PullRequest is what GitHub says about one pull request, or GitLab about
// one merge request.
type PullRequest struct {
	Repo          ForgeRepo
	Number        int // GitLab: the iid
	Title         string
	URL           string // ChangeRequestURL(Repo, Number): built, never read from gh or glab
	HeadBranch    string
	State         PRState
	Draft         bool
	Checks        Checks
	Review        Review
	ClosingIssues []int // issues in the same repo this pull request closes
	OpenedAt      time.Time
	ClosedAt      time.Time // merged or closed time; zero while open
	// HeadOID is the commit the pull request's head branch pointed at last
	// (GitHub keeps it after the branch is deleted); "" when unknown.
	// A fork's pull request has none: its commits are another repository's.
	HeadOID    string
	BaseBranch string // the branch it targets; "" when unknown
	// BaseIsDefault: the pull request targets the repo's default branch.
	BaseIsDefault bool
	// HeadRefOID is the commit the head branch on GitHub points at now; ""
	// when that branch is gone or unknown.
	HeadRefOID string
}

// TmuxPane is a tmux pane on one tmux server, read from the TMUX and TMUX_PANE
// variables of the session's process. Zero when the session is not in tmux.
// Those variables are inherited and can be stale (a program started from a
// tmux pane, a restarted server), so the pane is kept only once tmux confirms
// it shows TTY, the terminal of the session's own process; a jump confirms it
// again.
type TmuxPane struct {
	Socket string // the server's socket, an absolute path
	Pane   string // the pane id, "%" and digits
	TTY    string // the session's process's terminal, "/dev/..."
	// PID and Start are the session's process and its start time as ps
	// printed it (lstart) when the pane was confirmed, so a click can confirm
	// it is still the same process before focusing the pane.
	PID   int
	Start string
}

var (
	tmuxPaneRe   = regexp.MustCompile(`^%[0-9]{1,9}$`)
	tmuxSocketRe = regexp.MustCompile(`^/[A-Za-z0-9._@+/-]{1,200}$`)
	ttyRe        = regexp.MustCompile(`^/dev/[A-Za-z0-9._/-]{1,64}$`)
)

// ValidPlace says whether p's pane and socket have their shapes: a pane id
// "%N" and an absolute, clean socket path of plain characters (no space,
// comma or control character). The tty is not looked at.
func (p TmuxPane) ValidPlace() bool {
	return tmuxPaneRe.MatchString(p.Pane) && tmuxSocketRe.MatchString(p.Socket) && path.Clean(p.Socket) == p.Socket
}

// Valid says whether p has the shape of a tmux pane on a tmux socket showing
// a terminal: ValidPlace, and a clean tty path under /dev of plain
// characters. Only a valid pane is kept or passed to tmux.
func (p TmuxPane) Valid() bool {
	return p.ValidPlace() && ValidTTY(p.TTY)
}

// ValidTTY says whether tty has the shape of a terminal's path: /dev/ and
// plain characters, clean.
func ValidTTY(tty string) bool {
	return ttyRe.MatchString(tty) && path.Clean(tty) == tty
}

// FocusKind names the program that can focus a session.
type FocusKind string

// The focus kinds.
const (
	FocusAgterm FocusKind = "agterm"
	FocusTmux   FocusKind = "tmux"
)

// FocusTarget is where a card's jump goes: an agterm session (ID is its
// session id) or a tmux pane (ID is the pane id, Socket its server's socket).
type FocusTarget struct {
	Kind   FocusKind
	ID     string
	Socket string
	TTY    string // tmux: the terminal the pane must show, the session's own
	PID    int    // tmux: the session's process, which must still run...
	Start  string // ...with this start time, as ps printed it
}
