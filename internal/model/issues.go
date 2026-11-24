package model

import "time"

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
