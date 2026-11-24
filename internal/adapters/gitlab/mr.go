package gitlab

import (
	"math"
	"strconv"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// Approval is what GitLab's approval rules say about a merge request. GitLab
// has no "changes requested" decision, so there is no such value.
type Approval string

const (
	ApprovalNone     Approval = ""                // no approval rule, or none needed
	Approved         Approval = "approved"        // every required approval given
	ApprovalRequired Approval = "approval_needed" // at least one approval still needed
	ApprovalUnknown  Approval = "unknown"         // not known: the instance has no approval counts (Community Edition) or did not give them
)

// MergeRequest is what GitLab says about one merge request.
type MergeRequest struct {
	Host          string // a host glab is logged in to (gitlab.com or a self-hosted one), with its web port when its addresses carry one
	Path          string // project path as addresses carry it, "group/subgroup/project", the host's subfolder in front
	IID           int    // the project's own number, shown as !IID
	Title         string
	URL           string // MergeRequestURL(Host, Path, IID): built, never read from glab (on a host with a port, GitLab's webUrl must equal it)
	SourceBranch  string
	State         model.PRState // locked (being merged) counts as open
	Draft         bool
	Checks        model.Checks // from the head pipeline
	Approval      Approval
	ClosingIssues []int // issues in the same project this merge request closes; valid only when ClosingKnown
	ClosingKnown  bool  // false: not known (not asked yet, the call failed, or the list was too long)
	FromFork      bool  // its source branch is in another project: it never matches a local branch
	OpenedAt      time.Time
	ClosedAt      time.Time // merged or closed time; zero while open
}

// MRRef points at a merge request.
type MRRef struct {
	Host string
	Path string
	IID  int
}

// MergeRequestURLPattern and IssueURLPattern are the only forms of merge
// request and issue link this adapter builds: the model's, which the contract
// repeats. The host part is checked with model.ValidGitLabSite.
const (
	MergeRequestURLPattern = model.MergeRequestURLPattern
	IssueURLPattern        = model.GitLabIssueURLPattern
)

// MergeRequestURL is the address of merge request iid of project path on
// host, or "" when they cannot make one.
func MergeRequestURL(host, path string, iid int) string {
	return gitlabURL(host, path, "merge_requests", iid)
}

// IssueURL is the address of issue n of project path on host, or "" when they
// cannot make one. GitLab.com answers issues under /-/work_items/ too; the
// /-/issues/ form works on every version.
func IssueURL(host, path string, n int) string {
	return gitlabURL(host, path, "issues", n)
}

// ValidIID reports whether n can be a merge request or issue number: GitLab
// numbers them with a signed 32-bit integer.
func ValidIID(n int) bool { return n >= 1 && n <= math.MaxInt32 }

func gitlabURL(host, path, kind string, n int) string {
	if !ValidIID(n) || !model.ValidGitLabSite(host) || !ValidPath(path) {
		return ""
	}
	// ValidGitLabSite and ValidPath are stricter than the pattern; a test checks
	// that every address built here matches it.
	return "https://" + host + "/" + path + "/-/" + kind + "/" + strconv.Itoa(n)
}

// mapState turns GitLab's MergeRequestState into sitboard's; ok=false for a
// state this adapter does not know (never shown as a guess).
func mapState(s string) (model.PRState, bool) {
	switch s {
	case "opened", "locked": // locked: being merged, goes back to opened or on to merged
		return model.PRStateOpen, true
	case "merged":
		return model.PRStateMerged, true
	case "closed":
		return model.PRStateClosed, true
	}
	return "", false
}

// mapPipeline turns a head pipeline's PipelineStatusEnum into checks. No
// pipeline is "none". Canceled counts as failing (a merge request whose
// pipeline must succeed cannot merge); skipped and manual say nothing about
// the change, so they are unknown.
func mapPipeline(status string, hasPipeline bool) model.Checks {
	if !hasPipeline {
		return model.ChecksNone
	}
	switch status {
	case "SUCCESS":
		return model.ChecksPassing
	case "FAILED", "CANCELED", "CANCELING":
		return model.ChecksFailing
	case "CREATED", "WAITING_FOR_RESOURCE", "PREPARING", "WAITING_FOR_CALLBACK", "PENDING", "RUNNING", "SCHEDULED":
		return model.ChecksRunning
	default: // SKIPPED, MANUAL, or a status added later
		return model.ChecksUnknown
	}
}

// mapApproval reads GitLab's approved flag, approvals left and approvals
// required; nil is a value GitLab did not give. A project without approval
// rules answers approved=true with 0 required and 0 left (seen on gitlab.com
// public projects): nothing to show. Without both counts nothing is known.
func mapApproval(approved *bool, left, required *int) Approval {
	switch {
	case left == nil || required == nil:
		return ApprovalUnknown
	case *left > 0:
		return ApprovalRequired
	case *required == 0:
		return ApprovalNone
	case approved != nil && *approved:
		return Approved
	default:
		return ApprovalUnknown
	}
}
