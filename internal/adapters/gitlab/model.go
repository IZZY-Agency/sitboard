package gitlab

import (
	"context"
	"errors"
	"slices"

	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// Source is the provider as sitboard's core and its source list use it: a
// source.PRProvider for GitLab repositories, whose merge requests come back
// as model.PullRequest, plus Host, the check the core makes before it asks
// about a host at all.
type Source struct{ p *Provider }

// NewSource returns the GitLab source.
func NewSource(opts Options) *Source { return &Source{p: New(opts)} }

// errOtherForge: a lookup for a repository that is not a valid GitLab one.
var errOtherForge = errors.New("gitlab: not a GitLab repository")

// Probe reports glab for the page's source list.
func (s *Source) Probe(ctx context.Context) source.Status {
	st := s.p.Probe(ctx)
	return source.Status{Kind: source.KindGitLab, State: st.State, Version: st.Version, Tested: st.Tested, Detail: st.Detail}
}

// Host says whether merge requests on host may be looked up: nil only for a
// host glab is logged in to, gitlab.com included. An error wrapping
// source.ErrNotLoggedIn is a definite no (glab is not logged in to it, or it
// is github.com), and one wrapping source.ErrNoLogins too says glab is
// logged in to no host at all; one wrapping source.ErrUnsupported says GitLab sits under a
// subfolder there; any other error is no answer (glab missing, out of time,
// the host rate limited), to be asked again later.
func (s *Source) Host(ctx context.Context, host string) error {
	h, err := s.p.host(ctx, host)
	switch {
	case err != nil:
		return err
	case h.nowhere:
		return errNowhere
	case !h.known:
		return errNotKnown
	case h.subfolder:
		return errSubfolder
	}
	return nil
}

// Failing is the page's line for the last failed call about host, "" when
// its last call answered.
func (s *Source) Failing(host string) string { return s.p.Failing(host) }

// Lookup returns the merge requests of repo with the given numbers, plus the
// newest ones whose source branch is one of branches, as pull requests. A
// branch match from a fork is already left out (query.go); a merge request
// asked for by number is kept even when its branch is in a fork, since a
// session that ran `glab mr create` from a fork's checkout made it.
func (s *Source) Lookup(ctx context.Context, repo model.ForgeRepo, numbers []int, branches []string) ([]model.PullRequest, error) {
	if repo.Forge != model.ForgeGitLab || !repo.Valid() {
		return nil, errOtherForge
	}
	res, err := s.p.Lookup(ctx, repo.Host, repo.Path, numbers, branches)
	if err != nil {
		return nil, err
	}
	out := make([]model.PullRequest, 0, len(res.MRs))
	for _, mr := range res.MRs {
		if pr, ok := pullRequest(mr); ok {
			out = append(out, pr)
		}
	}
	return out, nil
}

// pullRequest is mr as the core keeps it; ok=false when no address can be
// built for it. Closing issues GitLab did not give this time are sent as
// none, as "closes nothing" is today: an old list is never passed off as the
// current one.
func pullRequest(mr MergeRequest) (model.PullRequest, bool) {
	repo := model.GitLabRepo(mr.Host, mr.Path)
	url := model.ChangeRequestURL(repo, mr.IID)
	if url == "" {
		return model.PullRequest{}, false
	}
	var issues []int
	if mr.ClosingKnown {
		issues = slices.Clone(mr.ClosingIssues)
	}
	return model.PullRequest{
		Repo: repo, Number: mr.IID, Title: mr.Title, URL: url, HeadBranch: mr.SourceBranch,
		State: mr.State, Draft: mr.Draft, Checks: mr.Checks, Review: reviewOf(mr.Approval),
		ClosingIssues: issues, OpenedAt: mr.OpenedAt, ClosedAt: mr.ClosedAt,
	}, true
}

// reviewOf puts GitLab's approvals in the place of GitHub's review decision:
// all approvals given is "approved", one still needed is "review_required"
// (the page words it "approval needed" on a merge request). GitLab has no
// "changes requested"; no rule, or counts not known, show nothing. Built on
// the plan's recommendation (spike 14, 2.3), the owner's confirmation pending.
func reviewOf(a Approval) model.Review {
	switch a {
	case Approved:
		return model.ReviewApproved
	case ApprovalRequired:
		return model.ReviewRequired
	}
	return model.ReviewNone
}
