package model

import (
	"math"
	"slices"
	"time"
)

// MaxRepoIssues caps the open issues kept per repository: the ones updated
// most recently. RepoIssues.Open still counts them all.
const MaxRepoIssues = 30

// MaxIssueLabels caps the labels kept per issue.
const MaxIssueLabels = 5

// RepoIssuesState says what a forge answered about one repository's issues.
type RepoIssuesState string

const (
	RepoIssuesOK       RepoIssuesState = "ok"
	RepoIssuesDisabled RepoIssuesState = "disabled" // the repository has issues turned off
	RepoIssuesNotFound RepoIssuesState = "notFound" // gone, renamed, or not visible to the account
)

// RepoIssues is the open issues of one repository, read only.
type RepoIssues struct {
	Repo  ForgeRepo
	State RepoIssuesState
	Open  int         // every open issue on the forge, not only those in Issues
	List  []OpenIssue // the most recently updated first, at most MaxRepoIssues
	// Pinned is what the forge said about the pinned numbers it was asked
	// in the same call (OpenIssues' pinned), set only with State OK. A
	// number asked and missing here was not answered: nothing is known
	// about it.
	Pinned []PinnedIssue
}

// MaxPinsAsked caps the pinned numbers asked per repository in one call. It
// is state.MaxPins, as many as can be pinned at all, so on GitHub every pin
// of a repository is asked at each of its looks and none waits for a later
// one (core.TestEveryPinIsAskedInOneLook). Each costs GitHub one labels
// connection: 200 add about 2 points to a call.
const MaxPinsAsked = 200

// PinState is what a forge said about one pinned issue asked by number.
type PinState string

const (
	PinOpen   PinState = "open"
	PinClosed PinState = "closed"
	PinGone   PinState = "gone" // no such issue in the repository, or not visible to the login
)

// PinnedIssue is one pinned issue as its forge answered it. With State
// PinGone only Number is set.
type PinnedIssue struct {
	OpenIssue
	State PinState
}

// OpenIssue is one open issue as its forge lists it. Every string is
// untrusted.
type OpenIssue struct {
	Number       int
	Title        string
	Milestone    string    // "" when none
	MilestoneDue time.Time // zero when the milestone has no due date, or none
	Labels       []string  // at most MaxIssueLabels
	UpdatedAt    time.Time
}

// PinsToAsk is the pinned numbers of one repository an issue lister asks:
// those from 1 to 2^31-1, each once, the first MaxPinsAsked of them in their
// order (the core gives the most recently pinned first).
func PinsToAsk(numbers []int) []int {
	var out []int
	for _, n := range numbers {
		if n >= 1 && n <= math.MaxInt32 && !slices.Contains(out, n) && len(out) < MaxPinsAsked {
			out = append(out, n)
		}
	}
	return out
}
