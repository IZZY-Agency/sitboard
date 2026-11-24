package shellcmd

import (
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// This file finds the GitLab merge requests a session created, the twin of
// the `gh pr create` rule, for the Claude Code and Codex tailers (#66).

// MRCreation is how a command creates merge requests, if it does: the shape
// of the address line depends on it.
type MRCreation struct {
	Glab bool // `glab mr create`: prints the new address alone on a line
	Push bool // `git push -o merge_request.create`: GitLab prints it after "remote:"
	// Mixed: the call also runs a command that may print a merge request
	// address of its own (a `glab mr view`, a file read, ...). Then a bare
	// address counts only right after glab's "Creating merge request for ..."
	// line, as for `gh pr create` (OwnPullRequests): a refused create followed
	// by a view must not credit the view's address.
	Mixed bool
	// First: in a mixed call, the first command that may print an address is
	// a `glab mr create`, so whatever glab printed first is its output (the
	// twin of CreateIsFirstGH).
	First bool
}

// Any says whether the command creates a merge request at all.
func (k MRCreation) Any() bool { return k.Glab || k.Push }

// MRCreation says how a command creates a merge request: `glab mr create`,
// or a `git push` with GitLab's push option merge_request.create. A push
// without that option is not counted: its "View merge request" line also
// names a merge request someone else opened for the branch. A creating push
// counts only when it is the call's only push: the "remote:" lines of
// several pushes are not told apart, so one of them may name a merge
// request that already existed.
func (c Command) MRCreation() MRCreation {
	var k MRCreation
	k.Glab = len(c.Runs("glab", "mr", "create")) > 0
	pushes, creates := 0, false
	for _, i := range c.Runs("git") {
		push, create := c.gitPush(i)
		if push {
			pushes++
		}
		creates = creates || create
	}
	k.Push = creates && pushes == 1
	if k.Glab {
		k.Mixed, k.First = c.printers()
	}
	return k
}

// quietPrograms are commands that print no merge request address: a call that
// runs only these beside its `glab mr create` is not mixed. Shell wrappers
// are not commands of their own; their scripts are read as commands.
var quietPrograms = []string{"cd", "pushd", "popd", "git", "export", "unset", "set", "true", ":", "env"}

// printers reads the call's commands that may print a merge request
// address, in the order the words stand: mixed says there is one besides the
// `glab mr create` runs, first that the first of them is a create. The quiet
// programs and shell wrappers are not printers; the commands inside $(...)
// and shell -c scripts are read as the commands they are.
func (c Command) printers() (mixed, first bool) {
	creates := c.Runs("glab", "mr", "create")
	seen := false
	for i, w := range c.words {
		if !c.at[i] || c.sep[i] {
			continue
		}
		base := filepath.Base(w)
		if slices.Contains(quietPrograms, base) || slices.Contains(shells, base) {
			continue
		}
		if !seen {
			first = slices.Contains(creates, i)
			seen = true
		}
		if !slices.Contains(creates, i) {
			mixed = true
		}
	}
	return mixed, first
}

// gitValueOptions are git's own options before the subcommand that take the
// next word as their value.
var gitValueOptions = []string{"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--config-env", "--exec-path", "--super-prefix", "--list-cmds", "--attr-source"}

// gitPush says whether the git run starting at word i is a push, and
// whether it has the merge_request.create option: git's own options are
// skipped, the subcommand must be push, and nothing after "--" is an option.
func (c Command) gitPush(i int) (push, creates bool) {
	j := i + 1
	for ; j < len(c.words) && !c.sep[j]; j++ {
		w := c.words[j]
		if slices.Contains(gitValueOptions, w) {
			j++
			continue
		}
		if !strings.HasPrefix(w, "-") {
			break
		}
	}
	if j >= len(c.words) || c.sep[j] || c.words[j] != "push" {
		return false, false
	}
	for j++; j < len(c.words) && !c.sep[j]; j++ {
		w := c.words[j]
		switch {
		case w == "--":
			return true, false
		case (w == "-o" || w == "--push-option") && j+1 < len(c.words) && !c.sep[j+1]:
			if isCreateOption(c.words[j+1]) {
				return true, true
			}
			j++
		case strings.HasPrefix(w, "--push-option="):
			if isCreateOption(strings.TrimPrefix(w, "--push-option=")) {
				return true, true
			}
		case strings.HasPrefix(w, "-o") && len(w) > 2:
			if isCreateOption(w[2:]) {
				return true, true
			}
		}
	}
	return true, false
}

func isCreateOption(v string) bool {
	return v == "merge_request.create" || v == "merge_request.create=true"
}

// mrLineRe matches a merge request address standing alone on a trimmed line,
// as `glab mr create` prints it without a terminal, or after git's "remote:"
// prefix as GitLab prints it on a push. The "/-/merge_requests/new?…" address
// of "To create a merge request" never matches: the number must end the line.
var mrLineRe = regexp.MustCompile(`^(remote:)?[ \t]*https://([a-z0-9.-]{1,253}(?::[0-9]{1,5})?)/([A-Za-z0-9_][A-Za-z0-9_./-]*)/-/merge_requests/([0-9]{1,10})$`)

// mrPushAnnounceRe is GitLab's own line before the address on a push, "remote:
// View merge request for <branch>:" (it is the same whether the push created
// the merge request or one was open already).
var mrPushAnnounceRe = regexp.MustCompile(`^remote:[ \t]*View merge request for .+:$`)

// mrFailureRe matches the lines glab prints when a create fails, whatever the
// reason: GitLab's 409 for a branch that has an open merge request (its text
// names the merge request by number, "Another open merge request already
// exists for this source branch: !12", never by address), any other HTTP
// error of the API ("POST https://...: 422 {...}", "failed to create merge
// request: 500 ...", "401 Unauthorized"), and glab's or git's own "ERROR",
// "error:" and "fatal:" lines. Matching more than a create's failure only
// costs a credit that needed no failure to be doubtful: a missing one.
var mrFailureRe = regexp.MustCompile(`(?i)merge request already exists|\(HTTP [1-5][0-9]{2}\)|: [45][0-9]{2}( |$)|^[45][0-9]{2} [A-Za-z]|^failed to |^(error|fatal)\b`)

// mrAnnounced says what a trimmed line announces for the address after it:
// glab's "Creating merge request for X into Y in Z" (also "Creating draft
// merge request for ...") before a created one, or GitLab's push line.
func mrAnnounced(line string) (glab, push bool) {
	if strings.HasPrefix(line, "Creating merge request for ") || strings.HasPrefix(line, "Creating draft merge request for ") {
		return true, false
	}
	return false, mrPushAnnounceRe.MatchString(line)
}

// MergeRequest is a merge request a creating command printed.
type MergeRequest struct {
	Ref model.PRRef // a GitLab ref: model.GitLabRepo(host, path)
	// Pushed is set when the address came from a creating push's "remote:"
	// line. GitLab prints the same line whether the push created the merge
	// request or one was already open, so the core credits a pushed one only
	// when its creation time falls in the call's window (the plan's 4.1).
	Pushed bool
}

// maxMRNumber is GitLab's largest iid: a 32-bit integer.
const maxMRNumber = math.MaxInt32

// MergeRequests returns the merge requests a creating command of kind k
// printed, each once: at most maxIDsCall accepted ones per output; the cap
// per session is the caller's. The output is read line by line, and a line
// counts only in the shape of the way the command created one:
//   - for a creating push, the address after "remote:" right after GitLab's
//     "remote: View merge request for <branch>:" line (blank "remote:" lines
//     aside); any other "remote:" address is not the push's;
//   - for `glab mr create`, a bare address. In a mixed call (k.Mixed) only the
//     create's own counts: an address right after glab's "Creating merge
//     request for ..." line (blank lines aside; another line cancels it); or,
//     when nothing is announced (glab prints the address alone without a
//     terminal), the first address if the create ran first (k.First) and the
//     output shows no failure (mrFailureRe: the 409 refusal, which names the
//     merge request by number, never by address, or any other error). So a
//     failed create followed by a view credits nothing, and a successful
//     create (or one whose outcome is unknown) followed by a view credits its
//     own address alone.
//
// A plain push followed by a `glab mr create` that fails therefore does not
// credit the session with the existing merge request the push named.
//
// An explicit :443 is dropped from the host: https's own port is the same address without it.
//
// The host rule: the host (with its web port, if any) must pass
// model.ValidGitLabSite and its name must not be
// github.com, so a github.com address never becomes a GitLab ref. The
// matcher does not know the session's remotes; the core asks only for hosts
// glab is logged in to.
func MergeRequests(text string, k MRCreation) []MergeRequest {
	if !k.Any() {
		return nil
	}
	var out []MergeRequest
	var firstBare *MergeRequest // mixed call: the first bare address nothing announced
	var glabAnn, pushAnn, failed bool
	announced := 0 // merge requests that glab's announcement named
	for line := range strings.SplitSeq(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || t == "remote:" {
			continue // blank lines do not cancel an announcement
		}
		if g, p := mrAnnounced(t); g || p {
			glabAnn, pushAnn = g, p
			continue
		}
		if mrFailureRe.MatchString(t) {
			failed = true
		}
		m := mrLineRe.FindStringSubmatch(t)
		ga, pa := glabAnn, pushAnn
		glabAnn, pushAnn = false, false
		if m == nil {
			continue
		}
		remote := m[1] != ""
		switch {
		case remote && (!k.Push || !pa):
			continue
		case !remote && !k.Glab:
			continue
		}
		// https's own port written out is no port (as for a clone's origin).
		site := strings.TrimSuffix(m[2], ":443")
		n, err := strconv.Atoi(m[4])
		if err != nil || n < 1 || n > maxMRNumber || model.GitLabHostname(site) == model.GitHubHost || !model.ValidGitLabSite(site) || !model.ValidGitLabPath(m[3]) {
			continue
		}
		r := MergeRequest{Ref: model.PRRef{Repo: model.GitLabRepo(site, m[3]), Number: n}, Pushed: remote}
		if !remote && k.Mixed && !ga {
			if firstBare == nil {
				firstBare = &r
			}
			continue
		}
		if !slices.ContainsFunc(out, func(o MergeRequest) bool { return o.Ref == r.Ref }) {
			out = append(out, r)
			if !remote && k.Mixed {
				announced++
			}
			if len(out) == maxIDsCall {
				break
			}
		}
	}
	// glab without a terminal prints the address alone, with no announcement:
	// then the first address is the create's when the create ran first and
	// the output shows no failure (the twin of OwnPullRequests). A create that
	// failed, with any error, credits nothing from the rest of the output.
	if k.Mixed && k.First && announced == 0 && !failed && firstBare != nil && len(out) < maxIDsCall &&
		!slices.ContainsFunc(out, func(o MergeRequest) bool { return o.Ref == firstBare.Ref }) {
		out = append(out, *firstBare)
	}
	return out
}

// Room says whether ref fits in a session's list of created pull and merge
// requests: each forge has its own budget of limit, so a flood of merge
// request addresses can never push a GitHub pull request out, nor the
// reverse.
func Room(created []model.PRRef, ref model.PRRef, limit int) bool {
	n := 0
	for _, c := range created {
		if c.Repo.Forge == ref.Repo.Forge {
			n++
		}
	}
	return n < limit
}

// AddMergeRequests adds what a finished command printed to a session's
// CreatedPRs (up to limit per forge, see Room) and the push-announced ones to
// pushed with the call's window (from its start to its result).
//
// A push candidate whose window is unusable (an end that is zero or lies
// before the start) is dropped whole: recorded in CreatedPRs without a window
// it would count as a created one, and kept with a bad window it would block
// a later good one for the same ref. A ref already kept keeps its earliest
// window, since an earlier push could not have named it before it existed;
// one that a bare line credited needs no window at all (a created one never
// has a 409), so a later bare line removes an earlier push's. Results may be
// read out of order, so a later call whose window starts earlier replaces the
// kept one (Codex knows only when a command finished: its window is that
// moment, and the earliest finish is kept).
func AddMergeRequests(created []model.PRRef, pushed []model.PushedRef, mrs []MergeRequest, from, to time.Time, limit int) ([]model.PRRef, []model.PushedRef) {
	for _, m := range mrs {
		if m.Pushed && (from.IsZero() || to.IsZero() || to.Before(from)) {
			continue
		}
		if slices.Contains(created, m.Ref) {
			if !m.Pushed {
				pushed = slices.DeleteFunc(pushed, func(p model.PushedRef) bool { return p.Ref == m.Ref })
			} else if i := slices.IndexFunc(pushed, func(p model.PushedRef) bool { return p.Ref == m.Ref }); i >= 0 && from.Before(pushed[i].From) {
				// Results can arrive out of order: a later push that merely
				// reported the merge request may be read first, and only the
				// push with the earliest start can have created it.
				pushed[i] = model.PushedRef{Ref: m.Ref, From: from, To: to}
			}
			continue
		}
		if !Room(created, m.Ref, limit) {
			continue
		}
		created = append(created, m.Ref)
		if m.Pushed {
			pushed = append(pushed, model.PushedRef{Ref: m.Ref, From: from, To: to})
		}
	}
	return created, pushed
}
