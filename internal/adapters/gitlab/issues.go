package gitlab

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	neturl "net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// issueReposPerCall caps the projects one glab call asks about. GitLab
// allows a query 250 points with a login and 200 without; read from
// queryComplexity on gitlab.com (27 Nov 2026), the query scores 5 plus 19 a
// project (currentUser takes 2), so 8 projects (157) stay under either
// limit with room to spare.
const issueReposPerCall = 8

// OpenIssues lists the open issues of repos on GitLab, read only (spike 15,
// part 2 of #133). See Provider.OpenIssues.
//
// pinned is not asked on GitLab: a pin outside a project's listed 30 keeps
// its number and link only, and RepoIssues.Pinned stays empty (#182). The
// query is the same with or without pins (TestPinsDoNotChangeTheGitLabQuery).
func (s *Source) OpenIssues(ctx context.Context, repos []model.ForgeRepo, _ map[model.ForgeRepo][]int) ([]model.RepoIssues, error) {
	return s.p.OpenIssues(ctx, repos)
}

// OpenIssues lists the open issues of repos, in one read-only GraphQL call
// per issueReposPerCall projects of one GitLab host. Project paths travel as
// raw string variables (-f), never inside the query text. Like Lookup it
// asks only a host whose login check said yes within knownFor and never
// checks a login itself; a host in its rate-limit backoff is not asked.
//
// A project GitLab does not show (gone, or not visible to the login) is
// answered as not found, but only when the answer says it came from the
// instance at the project's address (routeIs): glab routes --hostname by
// its own configuration, which may send it to another port or scheme. One
// with issues turned off is answered as disabled. A project whose answer
// failed, whose address GitLab gave as another one than the one asked, or
// whose null came from an instance not shown to be its own, is left out:
// the board keeps what it had.
//
// A host that refuses the login, or glab saying it is logged in nowhere, is
// source.ErrNotLoggedIn, and no row comes with it: the core asks one host a
// call, so it can tell which host's login went.
func (p *Provider) OpenIssues(ctx context.Context, repos []model.ForgeRepo) ([]model.RepoIssues, error) {
	if p.off {
		return nil, nil
	}
	var hosts []string
	byHost := map[string][]model.ForgeRepo{}
	seen := map[model.ForgeRepo]bool{}
	for _, r := range repos {
		if r.Forge != model.ForgeGitLab || !r.Valid() || seen[r] {
			continue
		}
		seen[r] = true
		h := model.GitLabHostname(r.Host)
		if _, ok := byHost[h]; !ok {
			hosts = append(hosts, h)
		}
		byHost[h] = append(byHost[h], r)
	}
	if len(hosts) == 0 {
		return nil, nil
	}
	if p.glab() == "" {
		p.setHealth(source.StateMissing, "glab not found")
		return nil, errNoGlab
	}
	var out []model.RepoIssues
	var firstErr error
	for _, host := range hosts {
		got, err := p.hostIssues(ctx, host, byHost[host])
		switch {
		case errors.Is(err, source.ErrNotLoggedIn):
			return nil, err
		case err != nil && firstErr == nil:
			firstErr = err
		}
		out = append(out, got...)
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, firstErr
}

// noteTokenRefused is the host's line after GitLab refused glab's token.
const noteTokenRefused = "GitLab refused glab's token"

// errTokenRefused: GitLab refused the token; the host's login is to be
// checked again before anything more is asked.
var errTokenRefused = &sharedErr{"gitlab: GitLab refused glab's token", source.ErrTokenRefused}

// errLoginChanged: GitLab answered for another account than the host's last
// answer did: a new login, whose first answer is not passed on either.
var errLoginChanged = &sharedErr{"gitlab: glab's login for this host changed", source.ErrLoginChanged}

// accountOf is the address of the account an issue answer came for
// (currentUser's webUrl), "" when GitLab gave none whole.
func accountOf(raw []byte) string {
	var r struct {
		Data struct {
			CurrentUser *struct {
				WebURL *string `json:"webUrl"`
			} `json:"currentUser"`
		} `json:"data"`
		Errors []struct {
			Path []any `json:"path"`
		} `json:"errors"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Data.CurrentUser == nil || r.Data.CurrentUser.WebURL == nil {
		return ""
	}
	for _, e := range r.Errors {
		if len(e.Path) > 0 && e.Path[0] == "currentUser" {
			return ""
		}
	}
	return *r.Data.CurrentUser.WebURL
}

// sameAccount records account as the one host's answers come for (in memory
// only) and says whether it is the one recorded before. Another account is a
// new login: host's login generation moves, what was cached from it goes
// and its yes with it (as doubtLogin), so the next check asks glab afresh.
// An answer read under an older stamp records nothing: it is dropped anyway
// (keptIssues).
func (p *Provider) sameAccount(host string, st authStamp, account string) bool {
	if account == "" {
		return true
	}
	defer p.lockAuth()()
	if p.stampLocked(host) != st {
		return true
	}
	was, seen := p.accounts[host]
	if !seen && len(p.accounts) >= 256 {
		clear(p.accounts)
	}
	p.accounts[host] = account
	if !seen || was == account {
		return true
	}
	p.newLoginLocked(host)
	if h := p.hosts[host]; h.known {
		delete(p.hosts, host)
	}
	return false
}

// issueAsk is one project as glab is asked about it and as the board names it.
type issueAsk struct {
	repo model.ForgeRepo
	w    where
}

// hostIssues answers for repos, all on host (a name, no web port).
func (p *Provider) hostIssues(ctx context.Context, host string, repos []model.ForgeRepo) ([]model.RepoIssues, error) {
	p.mu.Lock()
	h, current := p.answerLocked(host, knownFor)
	st := p.stampLocked(host)
	p.mu.Unlock()
	switch {
	case !current:
		return nil, errNotChecked
	case h.nowhere:
		return nil, errNowhere
	case !h.known:
		return nil, errNotKnown
	}
	var out []model.RepoIssues
	var asks []issueAsk
	for _, r := range repos {
		path, ok := apiPath(r.Path, h.subfolder)
		if !ok {
			// Not an address on this GitLab: nothing is asked, and nothing is there.
			out = append(out, model.RepoIssues{Repo: r, State: model.RepoIssuesNotFound})
			continue
		}
		asks = append(asks, issueAsk{repo: r, w: where{host: host, path: path, site: r.Host, web: r.Path}})
	}
	var firstErr error
	incomplete, failed, answered := false, false, false
	// Every glab process of this look starts under p.mu, after the rules of
	// stillAsk are checked once more: a logout or a 429 recorded by a host
	// check or a merge request lookup up to the start keeps the paths in.
	ctx = runner.WithGate(ctx, p.admit(ctx, host, st, h.subfolder))
	for start := 0; start < len(asks); start += issueReposPerCall {
		chunk := asks[start:min(start+issueReposPerCall, len(asks))]
		// Before every call, not only the first: the login may have gone
		// (found by a merge request lookup or a host check), its yes run out,
		// the mount moved, or a 429 been met (here or by a merge request
		// lookup) since the call before. No path goes out then. The gate
		// asks the same again as the process starts; this is the cheap way
		// out before the call is built.
		if err := p.stillAsk(host, st, h.subfolder); err != nil {
			return p.keptIssues(host, st, h.subfolder, out), err
		}
		args := []string{"api", "graphql", "--hostname", host, "-f", "query=" + buildIssuesQuery(len(chunk))}
		for i, a := range chunk {
			args = append(args, "-f", "p"+strconv.Itoa(i)+"="+a.w.path)
		}
		raw, err := p.exec(ctx, args...)
		if no := refusal(err); no != nil {
			// The gate said no as the process was to start: nothing went out.
			return p.keptIssues(host, st, h.subfolder, out), no
		}
		if err != nil {
			var ee *runner.ExitError
			if errors.As(err, &ee) && hasIssuesData(ee.Stdout) {
				raw, err = ee.Stdout, nil // partial answer: parseIssues reads the errors
			}
		}
		if ee := (*runner.ExitError)(nil); err != nil && errors.As(err, &ee) && tokenRefused(ee.Stdout) {
			// GitLab refused the token: nothing read with it is passed on,
			// and the host waits for a fresh login check, which the core
			// asks for at once (ErrNotChecked): a 401 there denies the host
			// and drops what was listed from it.
			p.doubtLogin(host)
			p.setHostHealth(host, noteTokenRefused)
			return nil, errTokenRefused
		}
		if err != nil {
			f := readFailure(err, host)
			note := f.note(host)
			p.setHostHealth(host, note)
			failed = true
			switch {
			case f.nowhere:
				p.loggedInNowhere()
				return nil, fmt.Errorf("%w: %s", errNowhere, note)
			case f.loggedOut():
				p.loggedOut(host)
				return nil, fmt.Errorf("%w: %s", errNotKnown, note)
			case f.rateLimited():
				p.limited(host, err)
				return p.keptIssues(host, st, h.subfolder, out), errRateLimited
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("gitlab: %s: %w", note, unwrapCtx(err))
			}
			if f.timedOut || !f.ran {
				// The host did not answer in time, or glab could not run: the
				// rest of the host would only wait as long again, call by call.
				break
			}
			continue
		}
		got, partial, err := parseIssues(raw, chunk)
		if err != nil {
			p.setHostHealth(host, "unexpected answer from glab")
			failed = true
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !p.sameAccount(host, st, accountOf(raw)) {
			// Another account than before: nothing read in this look is
			// passed on, and the core drops what the old login brought.
			return nil, errLoginChanged
		}
		out = append(out, got...)
		if partial {
			p.setHostHealth(host, "GitLab answered only part of a lookup")
			incomplete = true
			continue
		}
		answered = true
	}
	if answered && !failed && !incomplete {
		p.setHostHealth(host, "")
		p.setHealth(source.StateOK, "")
	}
	out = p.keptIssues(host, st, h.subfolder, out)
	if len(out) == 0 && firstErr == nil && incomplete {
		firstErr = errIncomplete
	}
	return out, firstErr
}

// stillAsk says whether host may still be asked, as at the start of the
// lookup: a yes kept within knownFor, the same login (st) and the same mount
// (sub), and no rate-limit wait. errNowhere or errNotKnown when the login is
// found gone, errRateLimited in a wait, errNotChecked otherwise.
func (p *Provider) stillAsk(host string, st authStamp, sub string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.askLocked(host, st, sub)
}

// admit is the gate of a look's glab processes (runner.WithGate): the rules
// of stillAsk, then the process's start, under one hold of p.admitCh, so no
// logout, new mount or 429 can be recorded between the last look at them
// and the start (each takes admitCh: lockAuth). p.mu is held only for the
// look, never across the start: a start the system stalls holds up no
// read of the board's. The wait for admitCh ends with ctx (the call's):
// a lookup at its deadline does not wait on another call's stalled start.
// A no is a refusedErr around stillAsk's error, or the context's. Only the
// start is held, never the run.
func (p *Provider) admit(ctx context.Context, host string, st authStamp, sub string) runner.Gate {
	return func(start func() error) error {
		select {
		case p.admitCh <- struct{}{}:
		case <-ctx.Done():
			return &refusedErr{ctx.Err()}
		}
		defer func() { <-p.admitCh }()
		if err := p.stillAsk(host, st, sub); err != nil {
			return &refusedErr{err}
		}
		return start()
	}
}

// refusedErr is admit's no: no process started. err is stillAsk's error.
type refusedErr struct{ err error }

func (e *refusedErr) Error() string { return e.err.Error() }
func (e *refusedErr) Unwrap() error { return e.err }

// refusal is the gate's no inside err (stillAsk's error), nil when err is
// none.
func refusal(err error) error {
	if r := (*refusedErr)(nil); errors.As(err, &r) {
		return r.err
	}
	return nil
}

// askLocked is stillAsk. Callers hold p.mu.
func (p *Provider) askLocked(host string, st authStamp, sub string) error {
	h, current := p.answerLocked(host, knownFor)
	switch {
	case current && h.nowhere:
		return errNowhere
	case current && !h.known:
		return errNotKnown
	case !current || p.stampLocked(host) != st || h.subfolder != sub:
		// Run out, logged in afresh since (what was read before is of the
		// old login: keptIssues drops it), or another mount.
		return errNotChecked
	case p.now().Before(p.backoff[host]):
		return errRateLimited
	}
	return nil
}

// keptIssues is out, or none when host's login changed since st or its
// mount is no longer sub: an answer read with a login found gone meanwhile,
// or for addresses under another mount, is not passed on.
func (p *Provider) keptIssues(host string, st authStamp, sub string, out []model.RepoIssues) []model.RepoIssues {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stampLocked(host) != st {
		return nil
	}
	if h, current := p.answerLocked(host, knownFor); current && h.known && h.subfolder != sub {
		return nil
	}
	return out
}

// issueFields are asked for each project: its address, to check that the
// answer is of the project asked, and its open issues, most recently
// updated first.
var issueFields = `webUrl issuesEnabled issues(state:opened,sort:UPDATED_DESC,first:` + strconv.Itoa(model.MaxRepoIssues) +
	`){count nodes{iid title updatedAt milestone{title dueDate} labels(first:` + strconv.Itoa(model.MaxIssueLabels) + `){nodes{title}}}}`

// buildIssuesQuery writes the query for n projects, $p0 to $p<n-1>. Only
// integers and fixed text go into it. currentUser's address says which
// instance answered (routeIs); it is null without a login.
func buildIssuesQuery(n int) string {
	var q strings.Builder
	q.WriteString("query(")
	for i := range n {
		if i > 0 {
			q.WriteString(",")
		}
		fmt.Fprintf(&q, "$p%d:ID!", i)
	}
	q.WriteString("){currentUser{webUrl} ")
	for i := range n {
		fmt.Fprintf(&q, "p%d:project(fullPath:$p%d){...I} ", i, i)
	}
	q.WriteString("} fragment I on Project{")
	q.WriteString(issueFields)
	q.WriteString("}")
	return q.String()
}

type glProjectIssues struct {
	WebURL        *string `json:"webUrl"`
	IssuesEnabled *bool   `json:"issuesEnabled"`
	Issues        *struct {
		Count *int `json:"count"`
		Nodes *[]*struct {
			IID       string `json:"iid"`
			Title     string `json:"title"`
			UpdatedAt string `json:"updatedAt"`
			Milestone *struct {
				Title   string  `json:"title"`
				DueDate *string `json:"dueDate"`
			} `json:"milestone"`
			Labels *struct {
				Nodes []*struct {
					Title string `json:"title"`
				} `json:"nodes"`
			} `json:"labels"`
		} `json:"nodes"`
	} `json:"issues"`
}

// hasIssuesData: glab failed, yet GitLab answered the query with data (some
// aliases may be null; the errors say why).
func hasIssuesData(out []byte) bool {
	var r struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if json.Unmarshal(out, &r) != nil {
		return false
	}
	_, ok := r.Data["p0"]
	return ok
}

// parseIssues reads the answer for asks, aliased p0, p1, ... in that order.
// An alias counts only when no error names it or anything under it: a null
// project then is one GitLab does not show (not found), when currentUser
// shows the answer came from the instance at the project's address
// (routeIs). Any alias with an error, a part missing, an address other than
// the one asked, or a null from an instance not shown to be the project's
// own, is left out and sets partial.
func parseIssues(raw []byte, asks []issueAsk) (out []model.RepoIssues, partial bool, err error) {
	var r struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors []struct {
			Path []any `json:"path"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.Data == nil {
		return nil, false, errUnexpected
	}
	hurt := map[string]bool{} // alias -> an error names it or what is under it
	for _, e := range r.Errors {
		alias := ""
		if len(e.Path) > 0 {
			alias, _ = e.Path[0].(string)
		}
		if alias == "" {
			// An error that names no project may concern any of them.
			return nil, false, errIncomplete
		}
		hurt[alias] = true
	}
	// The account's address, when GitLab gave it whole: nil without a login,
	// with an error under it, or in another shape (no route is shown then).
	var user struct {
		WebURL *string `json:"webUrl"`
	}
	if u, ok := r.Data["currentUser"]; !ok || hurt["currentUser"] || json.Unmarshal(u, &user) != nil {
		user.WebURL = nil
	}
	out = make([]model.RepoIssues, 0, len(asks))
	for i, a := range asks {
		alias := "p" + strconv.Itoa(i)
		gr, present := r.Data[alias]
		var g *glProjectIssues
		if present && json.Unmarshal(gr, &g) != nil {
			return nil, false, errUnexpected
		}
		switch {
		case !present:
			return nil, false, errUnexpected
		case hurt[alias]:
			partial = true
			continue
		case g == nil && !routeIs(user.WebURL, a.w):
			// glab routes --hostname by its own configuration (a port, a
			// scheme): an instance that found nothing is no proof unless it
			// is shown to be the one at the project's address.
			partial = true
			continue
		case g == nil:
			out = append(out, model.RepoIssues{Repo: a.repo, State: model.RepoIssuesNotFound})
			continue
		case !sameProject(g.WebURL, a.w):
			partial = true
			continue
		case g.IssuesEnabled != nil && !*g.IssuesEnabled:
			out = append(out, model.RepoIssues{Repo: a.repo, State: model.RepoIssuesDisabled})
			continue
		case g.Issues == nil || g.Issues.Count == nil || *g.Issues.Count < 0 || g.Issues.Nodes == nil:
			// No count, or no list: not an answer, never "no open issues".
			partial = true
			continue
		}
		ri := model.RepoIssues{Repo: a.repo, State: model.RepoIssuesOK, Open: *g.Issues.Count}
		for _, n := range *g.Issues.Nodes {
			if n == nil || len(ri.List) == model.MaxRepoIssues {
				continue
			}
			num, err := strconv.Atoi(n.IID)
			if err != nil || !ValidIID(num) {
				continue
			}
			is := model.OpenIssue{Number: num, Title: n.Title, UpdatedAt: parseTime(n.UpdatedAt)}
			if n.Milestone != nil {
				is.Milestone = n.Milestone.Title
				if n.Milestone.DueDate != nil {
					is.MilestoneDue = parseDue(*n.Milestone.DueDate)
				}
			}
			if n.Labels != nil {
				for _, l := range n.Labels.Nodes {
					if l != nil && len(is.Labels) < model.MaxIssueLabels {
						is.Labels = append(is.Labels, l.Title)
					}
				}
				// As GitHub's come: by name.
				slices.SortStableFunc(is.Labels, func(x, y string) int { return cmp.Compare(strings.ToLower(x), strings.ToLower(y)) })
			}
			ri.List = append(ri.List, is)
		}
		ri.Open = max(ri.Open, len(ri.List))
		out = append(out, ri)
	}
	return out, partial, nil
}

// sameProject says whether GitLab's address for a project is the one asked:
// the site with its web port, the subfolder exactly, then the project's
// path, which must be a valid GitLab path and is compared without ASCII case
// (GitLab finds a project by its path in any case and answers it as it is
// written there). No address is no proof.
func sameProject(webURL *string, w where) bool {
	site, path, ok := webAddress(webURL)
	if !ok {
		return false
	}
	rest, ok := strings.CutPrefix(path, mountOf(w))
	return ok && site == w.site && ValidPath(rest) && asciiEqualFold(rest, w.path)
}

// routeIs says whether the instance that answered is the one at w's
// address, read from the address of the account glab is logged in with
// (currentUser's webUrl, "https://<host>[:port]/<mount>/<username>", as
// GitLab builds a user's address): the instance is everything before its
// last path segment, and it must be w's site (scheme https, host and port)
// and w's mount exactly. No address (no login: currentUser is null) shows
// no route.
func routeIs(userURL *string, w where) bool {
	site, path, ok := webAddress(userURL)
	if !ok {
		return false
	}
	i := strings.LastIndexByte(path, '/')
	return i >= 0 && i < len(path)-1 && site == w.site && path[:i+1] == mountOf(w)
}

// webAddress is an https address GitLab gave, as its site (the host, with
// its port unless 443) and path; ok=false for any other shape.
func webAddress(webURL *string) (site, path string, ok bool) {
	if webURL == nil {
		return "", "", false
	}
	u, err := neturl.Parse(*webURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || u.Opaque != "" || u.RawPath != "" {
		return "", "", false
	}
	site = u.Host
	if u.Port() == "443" {
		site = u.Hostname()
	}
	return site, u.Path, true
}

// mountOf is w's mount as an address path carries it: "/" or "/<subfolder>/".
func mountOf(w where) string { return "/" + strings.TrimSuffix(w.web, w.path) }

// asciiEqualFold compares a and b, ignoring the case of ASCII letters only.
func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// parseDue reads a milestone's due date: gitlab.com gives a day
// ("2026-12-11", though its schema calls it a time); a full time is read
// too.
func parseDue(s string) time.Time {
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t
	}
	return parseTime(s)
}
