package model

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Forge is where a repository's pull or merge requests live.
type Forge string

const (
	ForgeGitHub Forge = "github"
	ForgeGitLab Forge = "gitlab"
)

// GitLabCom is the one GitLab host known without asking glab; any other host
// is GitLab only when glab is logged in to it (the core decides that).
const GitLabCom = "gitlab.com"

// ForgeRepo is a repository on a forge. GitHub: Host is GitHubHost and Path
// is "owner/name". GitLab: Host is GitLabCom or a host glab is logged in to,
// Path is "group/…/project". The zero value is no repository.
type ForgeRepo struct {
	Forge Forge
	Host  string
	Path  string
}

// GitHubRepo is the GitHub repository "owner/name".
func GitHubRepo(path string) ForgeRepo {
	return ForgeRepo{Forge: ForgeGitHub, Host: GitHubHost, Path: path}
}

// GitLabRepo is the GitLab project path on host.
func GitLabRepo(host, path string) ForgeRepo {
	return ForgeRepo{Forge: ForgeGitLab, Host: host, Path: path}
}

// IsZero reports whether r is no repository.
func (r ForgeRepo) IsZero() bool { return r == ForgeRepo{} }

// GitHub's own limits: an owner is at most 39 characters, a repository name
// at most 100. A longer one is no repository GitHub has, and is never passed
// on, shown or linked.
const (
	maxGitHubOwner = 39
	maxGitHubName  = 100
)

// githubSlugRe is what a GitHub "owner/name" must look like before it
// reaches gh; ".." and "." are refused on top.
var githubSlugRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Valid reports whether r can be asked about and linked to. Names come from
// transcripts and git remotes and are untrusted. GitHub: the host is
// GitHubHost and the path a plain "owner/name" within GitHub's length limits. GitLab: ValidGitLabHost and
// ValidGitLabPath, and never GitHubHost.
func (r ForgeRepo) Valid() bool {
	switch r.Forge {
	case ForgeGitHub:
		if r.Host != GitHubHost || !githubSlugRe.MatchString(r.Path) {
			return false
		}
		owner, name, _ := strings.Cut(r.Path, "/")
		return len(owner) <= maxGitHubOwner && len(name) <= maxGitHubName &&
			owner != "." && owner != ".." && name != "." && name != ".."
	case ForgeGitLab:
		return r.Host != GitHubHost && ValidGitLabHost(r.Host) && ValidGitLabPath(r.Path)
	}
	return false
}

// Key names r in maps and in saved keys. GitHub: "owner/name", exactly the
// slug used before forges, so saved catch-up read marks still match. GitLab:
// "gitlab:<host>/<path>", which no GitHub key can equal. "" for the zero
// value.
func (r ForgeRepo) Key() string {
	switch {
	case r.IsZero():
		return ""
	case r.Forge == ForgeGitHub:
		return r.Path
	}
	return string(r.Forge) + ":" + r.Host + "/" + r.Path
}

// Key names the pull or merge request: GitHub "owner/name#12" (as before
// forges), GitLab "gitlab:<host>/<path>!12".
func (p PRRef) Key() string {
	sep := "#"
	if p.Repo.Forge == ForgeGitLab {
		sep = "!"
	}
	return p.Repo.Key() + sep + strconv.Itoa(p.Number)
}

// ChangeRequestURL is the address of pull or merge request n of r, or ""
// when r is not Valid or n cannot be a number on its forge. Built, never read
// from gh or glab.
func ChangeRequestURL(r ForgeRepo, n int) string {
	if !r.Valid() {
		return ""
	}
	if r.Forge == ForgeGitLab {
		return gitlabURL(gitlabMergeRequestURLRe, r, "merge_requests", n)
	}
	return PullRequestURL(r.Path, n)
}

// ForgeIssueURL is the address of issue n of r, or "" as ChangeRequestURL.
func ForgeIssueURL(r ForgeRepo, n int) string {
	if !r.Valid() {
		return ""
	}
	if r.Forge == ForgeGitLab {
		return gitlabURL(gitlabIssueURLRe, r, "issues", n)
	}
	return IssueURL(r.Path, n)
}

// gitlabLinkPath is a project path as a link may carry it; ValidGitLabPath is
// stricter and is checked first.
const gitlabLinkPath = `[A-Za-z0-9_][A-Za-z0-9_.-]*(?:/[A-Za-z0-9_][A-Za-z0-9_.-]*){1,21}`

// MergeRequestURLPattern and GitLabIssueURLPattern are the only forms of
// merge request and GitLab issue link that reach the page; the host part is
// checked with ValidGitLabHost (and, on the page, against the snapshot's
// GitLab hosts). contract.MergeRequestURLPattern and
// contract.GitLabIssueURLPattern must stay equal to them (a test checks).
const (
	MergeRequestURLPattern = `^https://([a-z0-9.-]+)/(` + gitlabLinkPath + `)/-/merge_requests/([1-9][0-9]{0,9})$`
	GitLabIssueURLPattern  = `^https://([a-z0-9.-]+)/(` + gitlabLinkPath + `)/-/issues/([1-9][0-9]{0,9})$`
)

var (
	gitlabMergeRequestURLRe = regexp.MustCompile(MergeRequestURLPattern)
	gitlabIssueURLRe        = regexp.MustCompile(GitLabIssueURLPattern)
)

// GitLab numbers merge requests and issues with a signed 32-bit integer.
func gitlabURL(re *regexp.Regexp, r ForgeRepo, kind string, n int) string {
	if n < 1 || n > math.MaxInt32 {
		return ""
	}
	u := "https://" + r.Host + "/" + r.Path + "/-/" + kind + "/" + strconv.Itoa(n)
	if !re.MatchString(u) {
		return ""
	}
	return u
}

// maxGitLabPathDepth bounds a project path: a top-level group, up to 20
// levels of subgroups under it (GitLab limits a group's ancestors to 20), and
// the project itself.
const maxGitLabPathDepth = 22

var (
	hostLabel         = `[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?`
	gitlabHostRe      = regexp.MustCompile(`^` + hostLabel + `(?:\.` + hostLabel + `)+$`)
	gitlabPathSegment = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,254}$`)
)

// ValidGitLabHost reports whether h is a lowercase DNS name with at least one
// dot and no port, user or path: the only form passed to glab's --hostname.
func ValidGitLabHost(h string) bool {
	return len(h) <= 253 && gitlabHostRe.MatchString(h)
}

// ValidGitLabPath reports whether p is a GitLab project path ("group/project"
// or "group/subgroup/…/project"): 2 to 22 segments, each starting with a
// letter, digit or "_", never ".." and never ending in ".git" or ".atom"
// (GitLab refuses those names).
func ValidGitLabPath(p string) bool {
	if len(p) > 1024 {
		return false
	}
	segs := strings.Split(p, "/")
	if len(segs) < 2 || len(segs) > maxGitLabPathDepth {
		return false
	}
	for _, s := range segs {
		if !gitlabPathSegment.MatchString(s) || strings.Contains(s, "..") ||
			strings.HasSuffix(s, ".git") || strings.HasSuffix(s, ".atom") {
			return false
		}
	}
	return true
}
