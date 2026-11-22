package core

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

const (
	glHost = "gitlab.example.com"
	glPath = "example-group/example-project"
)

var (
	errFakeNotLoggedIn = fmt.Errorf("fake: %w", source.ErrNotLoggedIn)
	errFakeSubfolder   = fmt.Errorf("fake: %w", source.ErrUnsupported)
)

// fakeGitLab answers like gitlab.Source: lookups from fakePRs, hosts from
// known (a host missing there: not logged in, gitlab.com included).
type fakeGitLab struct {
	fakePRs
	hmu       sync.Mutex
	known     map[string]error
	hostCalls []string
	probe     source.Status
	probes    int
	failing   map[string]string // host → Failing's answer
	block     chan struct{}     // when set, Host waits for it to close
	// stale: hosts whose yes ran out in the provider. Their lookups make no
	// call and answer source.ErrNotChecked until Host says yes again.
	stale      map[string]bool
	inHost     int                      // Host calls running now
	maxInHost  int                      // the most Host calls running at once
	inHostFor  map[string]int           // per host
	maxPerHost int                      // the most Host calls about one host at once
	gaveUp     int                      // Host calls whose caller gave up while they waited
	entered    chan string              // when set, gets each Host call's host as it starts
	quit       chan string              // when set, gets the host of each Host call whose caller gave up
	holds      map[string]chan struct{} // host → what its Host calls wait for, besides block
	// lookupGate, when set, runs in every Lookup before it answers.
	lookupGate func(repo model.ForgeRepo)
	// probeGate, when set, runs in every Probe before it answers.
	probeGate func(ctx context.Context)
}

func (f *fakeGitLab) Host(ctx context.Context, host string) error {
	f.hmu.Lock()
	block, entered, quit := f.block, f.entered, f.quit
	if hold := f.holds[host]; hold != nil {
		block = hold
	}
	if f.inHostFor == nil {
		f.inHostFor = map[string]int{}
	}
	f.inHost++
	f.inHostFor[host]++
	f.maxInHost, f.maxPerHost = max(f.maxInHost, f.inHost), max(f.maxPerHost, f.inHostFor[host])
	f.hmu.Unlock()
	if entered != nil {
		entered <- host
	}
	gaveUp := false
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			gaveUp = true
		}
	}
	f.hmu.Lock()
	defer f.hmu.Unlock()
	f.inHost--
	f.inHostFor[host]--
	f.hostCalls = append(f.hostCalls, host)
	if gaveUp {
		f.gaveUp++
		if quit != nil {
			quit <- host
		}
		return ctx.Err()
	}
	if err, ok := f.known[host]; ok {
		if err == nil {
			delete(f.stale, host)
		}
		return err
	}
	return errFakeNotLoggedIn
}

// Lookup is fakePRs.Lookup, except on a stale host: no call, ErrNotChecked.
func (f *fakeGitLab) Lookup(ctx context.Context, repo model.ForgeRepo, numbers []int, branches []string) ([]model.PullRequest, error) {
	f.hmu.Lock()
	stale, gate := f.stale[repo.Host], f.lookupGate
	f.hmu.Unlock()
	if gate != nil {
		gate(repo)
	}
	if stale {
		return nil, fmt.Errorf("fake: %w", source.ErrNotChecked)
	}
	return f.fakePRs.Lookup(ctx, repo, numbers, branches)
}

func (f *fakeGitLab) setHost(host string, err error) {
	f.hmu.Lock()
	f.known[host] = err
	f.hmu.Unlock()
}

func (f *fakeGitLab) setProbe(st source.Status) {
	f.hmu.Lock()
	f.probe = st
	f.hmu.Unlock()
}

func (f *fakeGitLab) Probe(ctx context.Context) source.Status {
	f.hmu.Lock()
	gate := f.probeGate
	f.hmu.Unlock()
	if gate != nil {
		gate(ctx)
	}
	f.hmu.Lock()
	defer f.hmu.Unlock()
	f.probes++
	return f.probe
}

func (f *fakeGitLab) counts() (hosts, probes, lookups int) {
	f.hmu.Lock()
	hosts, probes = len(f.hostCalls), f.probes
	f.hmu.Unlock()
	f.mu.Lock()
	lookups = len(f.calls)
	f.mu.Unlock()
	return hosts, probes, lookups
}

var glOK = source.Status{Kind: source.KindGitLab, State: source.StateOK, Version: "1.131.0", Tested: true}

// newGitLabEnv is newEnv with a GitLab provider logged in to glHost.
func newGitLabEnv(t *testing.T) (*testEnv, *fakeGitLab) {
	t.Helper()
	return newGitLabEnvWith(t, nil)
}

// newGitLabEnvWith is newGitLabEnv with opts changed by change.
func newGitLabEnvWith(t *testing.T, change func(*Options)) (*testEnv, *fakeGitLab) {
	t.Helper()
	e := newEnv(t)
	gl := &fakeGitLab{known: map[string]error{glHost: nil}, probe: glOK}
	opts := Options{Repos: e.repos, PRs: e.prs, GitLab: gl, State: e.state, Now: e.clock.Now, StaleAfter: 20 * time.Minute}
	if change != nil {
		change(&opts)
	}
	e.board = newBoard(t, opts)
	return e, gl
}

func (f *fakeGitLab) asked() []string {
	f.hmu.Lock()
	defer f.hmu.Unlock()
	return slices.Clone(f.hostCalls)
}

func (f *fakeGitLab) lookupsOf(repo model.ForgeRepo) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.repo == repo.Key() {
			n++
		}
	}
	return n
}

func mrAt(repo model.ForgeRepo, n int, opened time.Time) model.PullRequest {
	return model.PullRequest{
		Repo: repo, Number: n, Title: fmt.Sprintf("Placeholder %d", n), URL: model.ChangeRequestURL(repo, n),
		State: model.PRStateOpen, Checks: model.ChecksPassing, OpenedAt: opened,
	}
}

func gitlabLine(t *testing.T, b *Board) contract.SourceStatus {
	t.Helper()
	for _, s := range b.Snapshot().Sources {
		if s.Kind == contract.SourceGitLab {
			return s
		}
	}
	t.Fatal("no GitLab source line")
	return contract.SourceStatus{}
}

func projectOf(t *testing.T, b *Board, cardID string) contract.Project {
	t.Helper()
	snap := b.Snapshot()
	for _, c := range snap.Cards {
		if c.ID != cardID {
			continue
		}
		for _, p := range snap.Projects {
			if p.ID == c.ProjectID {
				return p
			}
		}
	}
	t.Fatalf("no project for card %s", cardID)
	return contract.Project{}
}

// A merge request a session created is asked of GitLab, a pull request of
// GitHub, each provider only about its own forge; both reach the card.
func TestGitLabMergeRequestsAreLookedUp(t *testing.T) {
	e, gl := newGitLabEnv(t)
	repo := model.GitLabRepo(glHost, glPath)
	gl.prs = []model.PullRequest{mrAt(repo, 12, tNow.Add(-time.Hour))}
	e.prs.prs = []model.PullRequest{prAt(9, model.PRStateOpen, tNow.Add(-time.Hour), time.Time{})}
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: repo, Branch: model.Of("main")})
	w := busySession("w", tNow)
	w.CreatedPRs = []model.PRRef{{Repo: repo, Number: 12}, {Repo: model.GitHubRepo(exRepo), Number: 9}}
	e.board.applySync(w)
	e.board.tick()

	gl.mu.Lock()
	glCalls := slices.Clone(gl.calls)
	gl.mu.Unlock()
	if len(glCalls) != 1 || glCalls[0].repo != repo.Key() || !slices.Equal(glCalls[0].numbers, []int{12}) {
		t.Fatalf("GitLab lookups %+v", glCalls)
	}
	if e.prs.callsFor(exRepo) != 1 || e.prs.callsFor(repo.Key()) != 0 {
		t.Fatalf("GitHub lookups %+v", e.prs.calls)
	}
	c := onlyCard(t, e.board)
	if len(c.PullRequests) != 2 {
		t.Fatalf("pull requests %+v", c.PullRequests)
	}
	var mr contract.PullRequest
	for _, pr := range c.PullRequests {
		if pr.Forge == contract.ForgeGitLab {
			mr = pr
		}
	}
	if mr.Number != 12 || mr.Host != glHost || mr.URL != "https://gitlab.example.com/example-group/example-project/-/merge_requests/12" {
		t.Fatalf("merge request %+v", mr)
	}
	if hosts := e.board.Snapshot().GitLabHosts; !slices.Equal(hosts, []string{glHost}) {
		t.Fatalf("gitlabHosts %v", hosts)
	}
	if p := projectOf(t, e.board, c.ID); p.Repo != glPath || p.RepoForge != contract.ForgeGitLab || p.RepoHost != glHost {
		t.Fatalf("project %+v", p)
	}
}

// A host glab is not logged in to is never asked about projects, its merge
// requests never show, and the GitLab line names it. A login made later is
// noticed once its answer is a minute old.
func TestGitLabHostNotLoggedInIsNeverAsked(t *testing.T) {
	e, gl := newGitLabEnv(t)
	e.board.SetSources([]source.Status{glOK})
	repo := model.GitLabRepo("elsewhere.example.com", glPath)
	gl.prs = []model.PullRequest{mrAt(repo, 5, tNow.Add(-time.Hour))}
	w := busySession("w", tNow)
	w.CreatedPRs = []model.PRRef{{Repo: repo, Number: 5}}
	e.board.applySync(w)
	e.board.tick()
	if _, _, lookups := gl.counts(); lookups != 0 {
		t.Fatalf("%d lookups on a host glab is not logged in to", lookups)
	}
	if c := onlyCard(t, e.board); len(c.PullRequests) != 0 {
		t.Fatalf("pull requests %+v", c.PullRequests)
	}
	if l := gitlabLine(t, e.board); l.State != contract.SourceFailing || l.Detail != "not logged in to elsewhere.example.com" || l.Version != "1.131.0" || !l.Ready {
		t.Fatalf("line %+v", l)
	}

	gl.setHost(repo.Host, nil)
	e.clock.Set(tNow.Add(hostEvery - time.Second))
	e.board.tick()
	if hosts, _, lookups := gl.counts(); lookups != 0 || hosts != 1 {
		t.Fatalf("asked again within a minute: %d host checks, %d lookups", hosts, lookups)
	}
	e.clock.Set(tNow.Add(hostEvery))
	e.board.tick()
	if _, _, lookups := gl.counts(); lookups != 1 {
		t.Fatalf("%d lookups after the login", lookups)
	}
	if c := onlyCard(t, e.board); len(c.PullRequests) != 1 {
		t.Fatalf("pull requests %+v", c.PullRequests)
	}
	if l := gitlabLine(t, e.board); l.State != contract.SourceOK || l.Detail != "" {
		t.Fatalf("line after the login %+v", l)
	}
}

// GitLab under a subfolder is not supported yet: never asked, and said so.
func TestGitLabSubfolderHostSaysSo(t *testing.T) {
	e, gl := newGitLabEnv(t)
	e.board.SetSources([]source.Status{glOK})
	gl.setHost(glHost, errFakeSubfolder)
	w := busySession("w", tNow)
	w.CreatedPRs = []model.PRRef{{Repo: model.GitLabRepo(glHost, glPath), Number: 5}}
	e.board.applySync(w)
	e.board.tick()
	if _, _, lookups := gl.counts(); lookups != 0 {
		t.Fatalf("%d lookups", lookups)
	}
	if l := gitlabLine(t, e.board); l.State != contract.SourceFailing || l.Detail != "GitLab under a subfolder of gitlab.example.com is not supported yet" {
		t.Fatalf("line %+v", l)
	}
}

// Switched off (--no-gitlab) or without glab, nothing about GitLab is
// asked, its remotes name no repository, and the line stays as probed.
func TestGitLabOffAsksNothing(t *testing.T) {
	for _, st := range []source.State{source.StateOff, source.StateMissing} {
		e, gl := newGitLabEnv(t)
		e.board.SetSources([]source.Status{{Kind: source.KindGitLab, State: st}})
		repo := model.GitLabRepo(glHost, glPath)
		gl.prs = []model.PullRequest{mrAt(repo, 5, tNow.Add(-time.Hour))}
		e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: repo, Branch: model.Of("main")})
		w := busySession("w", tNow)
		w.CreatedPRs = []model.PRRef{{Repo: repo, Number: 5}}
		e.board.applySync(w)
		e.board.tick()
		if hosts, probes, lookups := gl.counts(); hosts+probes+lookups != 0 {
			t.Fatalf("%s: %d host checks, %d probes, %d lookups", st, hosts, probes, lookups)
		}
		c := onlyCard(t, e.board)
		if p := projectOf(t, e.board, c.ID); len(c.PullRequests) != 0 || p.Repo != "" || p.RepoForge != contract.ForgeNone {
			t.Fatalf("%s: card %+v project %+v", st, c.PullRequests, p)
		}
		if l := gitlabLine(t, e.board); l.State != contract.SourceState(st) {
			t.Fatalf("%s: line %+v", st, l)
		}
	}
}

// A remote names a GitLab repository only on a host glab is logged in to,
// gitlab.com included; any other host is no forge sitboard knows.
func TestGitLabRemoteNeedsAKnownHost(t *testing.T) {
	e, gl := newGitLabEnv(t)
	gl.setHost(model.GitLabCom, nil)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitLabRepo("elsewhere.example.com", glPath), Branch: model.Of("main")})
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Remote: model.GitLabRepo(model.GitLabCom, glPath), Branch: model.Of("main")})
	a := busySession("a", tNow)
	bs := busySession("b", tNow)
	bs.Folder = beta
	e.board.applySync(a)
	e.board.applySync(bs)
	e.board.tick()
	if p := projectOf(t, e.board, mustCardID(t, "claude", "a")); p.Repo != "" || p.RepoForge != contract.ForgeNone || p.RepoHost != "" {
		t.Fatalf("unknown host: %+v", p)
	}
	if p := projectOf(t, e.board, mustCardID(t, "claude", "b")); p.Repo != glPath || p.RepoForge != contract.ForgeGitLab || p.RepoHost != model.GitLabCom {
		t.Fatalf("gitlab.com: %+v", p)
	}
}

// The push window, at its edges: credited only when GitLab created the merge
// request inside one of that ref's push calls, widened by pushSkew.
func TestPushCredited(t *testing.T) {
	ref := model.PRRef{Repo: model.GitLabRepo(glHost, glPath), Number: 7}
	other := model.PRRef{Repo: ref.Repo, Number: 8}
	from := tNow.Add(-10 * time.Minute)
	to := from.Add(5 * time.Second)
	pushes := []model.PushedRef{{Ref: other, From: tNow.Add(-time.Hour), To: tNow.Add(-time.Hour)}, {Ref: ref, From: from, To: to}}
	for _, c := range []struct {
		opened time.Time
		want   bool
	}{
		// The skew is two minutes, written out: a change to it fails here.
		{from.Add(-2 * time.Minute), true},
		{from.Add(-2*time.Minute - time.Second), false},
		{to.Add(2 * time.Minute), true},
		{to.Add(2*time.Minute + time.Second), false},
		{from.Add(2 * time.Second), true},
		{tNow.Add(-time.Hour), false}, // inside the other ref's push only
		{time.Time{}, false},
	} {
		if got := pushCredited(model.PullRequest{Repo: ref.Repo, Number: 7, OpenedAt: c.opened}, ref, pushes); got != c.want {
			t.Errorf("opened %v: %v, want %v", c.opened, got, c.want)
		}
	}
	// A window with a missing or reversed end credits nothing, whatever the
	// creation time: the core does not trust the matcher on this.
	opened := model.PullRequest{Repo: ref.Repo, Number: 7, OpenedAt: from.Add(2 * time.Second)}
	for name, p := range map[string]model.PushedRef{
		"no start": {Ref: ref, To: to},
		"no end":   {Ref: ref, From: from},
		"neither":  {Ref: ref},
		"reversed": {Ref: ref, From: to.Add(time.Minute), To: from},
	} {
		if pushCredited(opened, ref, []model.PushedRef{p}) {
			t.Errorf("%s: credited", name)
		}
	}
	// Reversed so that the widened ends overlap around the creation time.
	if pushCredited(opened, ref, []model.PushedRef{{Ref: ref, From: from.Add(time.Minute), To: from}}) {
		t.Error("reversed, overlapping: credited")
	}
}

// A merge request a push announced but did not create (the branch had one
// open already) is not the session's: off its card, kept off when the
// session's next fact names it again, and not asked about again. One the
// push created stays, and so does a ref the session created otherwise.
func TestPushAnnouncedMergeRequestNeedsItsWindow(t *testing.T) {
	e, gl := newGitLabEnv(t)
	repo := model.GitLabRepo(glHost, glPath)
	from := tNow.Add(-10 * time.Minute)
	old, made, other := model.PRRef{Repo: repo, Number: 7}, model.PRRef{Repo: repo, Number: 8}, model.PRRef{Repo: repo, Number: 9}
	gl.prs = []model.PullRequest{mrAt(repo, 7, tNow.Add(-3*time.Hour)), mrAt(repo, 8, from.Add(3*time.Second)), mrAt(repo, 9, tNow.Add(-5*time.Hour))}
	w := busySession("w", tNow)
	w.CreatedPRs = []model.PRRef{old, made, other}
	w.PushedPRs = []model.PushedRef{{Ref: old, From: from, To: from.Add(5 * time.Second)}, {Ref: made, From: from, To: from.Add(5 * time.Second)}}
	e.board.applySync(w)
	e.board.tick()
	nums := func() []int {
		var out []int
		for _, pr := range onlyCard(t, e.board).PullRequests {
			out = append(out, pr.Number)
		}
		slices.Sort(out)
		return out
	}
	if got := nums(); !slices.Equal(got, []int{8, 9}) {
		t.Fatalf("on the card %v, want 8 and 9", got)
	}
	e.board.applySync(w) // the adapter sends the session again
	if got := nums(); !slices.Equal(got, []int{8, 9}) {
		t.Fatalf("after the next fact %v, want 8 and 9", got)
	}
	e.clock.Set(tNow.Add(githubEvery))
	e.board.tick()
	gl.mu.Lock()
	last := gl.calls[len(gl.calls)-1]
	gl.mu.Unlock()
	if slices.Contains(last.numbers, 7) || !slices.Contains(last.numbers, 8) {
		t.Fatalf("asked again %v", last.numbers)
	}
	e.board.applySync(model.SessionGone{Key: w.Key})
	e.board.mu.Lock()
	left := len(e.board.notOwn)
	e.board.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d sessions still remembered after it went", left)
	}
}

// Later proof of creation wins over an earlier push that did not create the
// merge request: a fact that lists it as created and not as pushed (a
// `glab mr create` reached the session after the push) brings it back, and
// so does a new push window that matches GitLab's creation time.
func TestLaterProofOfCreationWins(t *testing.T) {
	e, gl := newGitLabEnv(t)
	repo := model.GitLabRepo(glHost, glPath)
	ref := model.PRRef{Repo: repo, Number: 7}
	opened := tNow.Add(-3 * time.Hour)
	gl.prs = []model.PullRequest{mrAt(repo, 7, opened)}
	w := busySession("w", tNow)
	w.CreatedPRs = []model.PRRef{ref}
	w.PushedPRs = []model.PushedRef{{Ref: ref, From: tNow.Add(-time.Minute), To: tNow.Add(-time.Minute)}}
	e.board.applySync(w)
	e.board.tick()
	if n := len(onlyCard(t, e.board).PullRequests); n != 0 {
		t.Fatalf("rejected push shows %d", n)
	}
	w.PushedPRs = nil
	e.board.applySync(w)
	if n := len(onlyCard(t, e.board).PullRequests); n != 1 {
		t.Fatalf("created, not pushed: %d on the card, want 1", n)
	}

	// The same with a push window that, now, matches the creation time.
	e2, gl2 := newGitLabEnv(t)
	gl2.prs = []model.PullRequest{mrAt(repo, 7, opened)}
	v := busySession("v", tNow)
	v.CreatedPRs = []model.PRRef{ref}
	v.PushedPRs = []model.PushedRef{{Ref: ref, From: tNow.Add(-time.Minute), To: tNow.Add(-time.Minute)}}
	e2.board.applySync(v)
	e2.board.tick()
	v.PushedPRs = append(v.PushedPRs, model.PushedRef{Ref: ref, From: opened.Add(-time.Second), To: opened.Add(time.Second)})
	e2.board.applySync(v)
	if n := len(onlyCard(t, e2.board).PullRequests); n != 1 {
		t.Fatalf("matching window: %d on the card, want 1", n)
	}
}

// A push-announced merge request the board already knows (another session
// created it) is judged the moment the fact arrives: it never shows on the
// pushing session's card, while its creator keeps it.
func TestPushAnnouncedKnownMergeRequestIsJudgedAtOnce(t *testing.T) {
	e, gl := newGitLabEnv(t)
	repo := model.GitLabRepo(glHost, glPath)
	ref := model.PRRef{Repo: repo, Number: 7}
	gl.prs = []model.PullRequest{mrAt(repo, 7, tNow.Add(-3*time.Hour))}
	a := busySession("a", tNow)
	a.CreatedPRs = []model.PRRef{ref}
	e.board.applySync(a)
	e.board.tick()
	b := busySession("b", tNow)
	b.CreatedPRs = []model.PRRef{ref}
	b.PushedPRs = []model.PushedRef{{Ref: ref, From: tNow.Add(-time.Minute), To: tNow.Add(-time.Minute)}}
	e.board.applySync(b)
	if c, _ := cardByID(t, e.board, mustCardID(t, "claude", "b")); len(c.PullRequests) != 0 {
		t.Fatalf("pushing session shows %+v", c.PullRequests)
	}
	if c, _ := cardByID(t, e.board, mustCardID(t, "claude", "a")); len(c.PullRequests) != 1 {
		t.Fatalf("creator shows %+v", c.PullRequests)
	}
}

// A lookup that finds the login gone stops lookups on that host until it is
// asked about again, and the line names the host.
func TestGitLabLookupLosingTheLoginStopsAsking(t *testing.T) {
	e, gl := newGitLabEnv(t)
	e.board.SetSources([]source.Status{glOK})
	repo := model.GitLabRepo(glHost, glPath)
	gl.err = errFakeNotLoggedIn
	w := busySession("w", tNow)
	w.CreatedPRs = []model.PRRef{{Repo: repo, Number: 5}}
	e.board.applySync(w)
	e.board.tick()
	if _, _, lookups := gl.counts(); lookups != 1 {
		t.Fatalf("%d lookups", lookups)
	}
	e.board.mu.Lock()
	p := e.board.providerFor(repo)
	e.board.mu.Unlock()
	if p != nil {
		t.Fatal("still asked after the login went")
	}
	if l := gitlabLine(t, e.board); l.State != contract.SourceFailing || l.Detail != "not logged in to gitlab.example.com" {
		t.Fatalf("line %+v", l)
	}
}

// A host's failed calls (a rate limit) show on the line while its merge
// requests are in play, and another host answering afterwards does not hide
// them; glab is never probed for it. With nothing in play the line is the
// probe's again.
func TestGitLabLineFollowsTheProvider(t *testing.T) {
	e, gl := newGitLabEnv(t)
	const other = "other.example.com"
	gl.setHost(other, nil)
	e.board.SetSources([]source.Status{glOK})
	w := busySession("w", tNow)
	e.board.applySync(w)
	e.board.tick()
	gl.hmu.Lock()
	gl.failing = map[string]string{glHost: "rate limited"}
	gl.hmu.Unlock()
	w.CreatedPRs = []model.PRRef{{Repo: model.GitLabRepo(glHost, glPath), Number: 5}, {Repo: model.GitLabRepo(other, glPath), Number: 6}}
	e.board.applySync(w)
	e.board.tick()
	if l := gitlabLine(t, e.board); l.State != contract.SourceFailing || l.Detail != "rate limited" {
		t.Fatalf("line %+v", l)
	}
	if _, probes, _ := gl.counts(); probes != 0 {
		t.Fatalf("%d probes", probes)
	}
	e.board.applySync(model.SessionGone{Key: w.Key})
	e.board.tick()
	if l := gitlabLine(t, e.board); l != (contract.SourceStatus{Kind: contract.SourceGitLab, State: contract.SourceOK, Version: "1.131.0", Tested: true, Ready: true}) {
		t.Fatalf("line with nothing in play %+v", l)
	}
}

// glab is probed in the background (ProbeGitLab): a first probe that failed
// is tried again a minute later, and the line recovers.
func TestGitLabLineRecoversAfterAFailedProbe(t *testing.T) {
	e, gl := newGitLabEnv(t)
	gl.setProbe(source.Status{Kind: source.KindGitLab, State: source.StateFailing, Detail: "glab does not answer"})
	e.board.StartGitLabProbe(context.Background())
	e.board.hostChecks.Wait()
	if l := gitlabLine(t, e.board); l.State != contract.SourceFailing {
		t.Fatalf("first probe: %+v", l)
	}
	gl.setProbe(glOK)
	e.board.tick()
	if _, probes, _ := gl.counts(); probes != 1 {
		t.Fatalf("probed again within the minute: %d probes", probes)
	}
	e.clock.Set(tNow.Add(hostEvery))
	e.board.tick()
	if l := gitlabLine(t, e.board); l.State != contract.SourceOK || l.Version != "1.131.0" {
		t.Fatalf("after a minute: %+v", l)
	}
}

// Each forge switched off stops only its own lookups.
func TestForgesSwitchOffOnTheirOwn(t *testing.T) {
	gh, glRepo := model.GitHubRepo(exRepo), model.GitLabRepo(glHost, glPath)
	for _, c := range []struct {
		name           string
		github, gitlab source.State
	}{{"github off", source.StateOff, source.StateOK}, {"gitlab off", source.StateOK, source.StateOff}} {
		e, gl := newGitLabEnv(t)
		e.board.SetSources([]source.Status{{Kind: source.KindGitHub, State: c.github}, {Kind: source.KindGitLab, State: c.gitlab}})
		w := busySession("w", tNow)
		w.CreatedPRs = []model.PRRef{{Repo: gh, Number: 9}, {Repo: glRepo, Number: 5}}
		e.board.applySync(w)
		e.board.tick()
		_, _, glLookups := gl.counts()
		if ghAsked, glAsked := e.prs.callsFor(exRepo) > 0, glLookups > 0; ghAsked != (c.github == source.StateOK) || glAsked != (c.gitlab == source.StateOK) {
			t.Errorf("%s: GitHub asked %v, GitLab asked %v", c.name, ghAsked, glAsked)
		}
	}
}

// A merge request on a host not decided yet starts a refresh at once, which
// decides the host before it looks.
func TestNewGitLabHostKicksARefresh(t *testing.T) {
	e, _ := newGitLabEnv(t)
	w := busySession("w", tNow)
	w.CreatedPRs = []model.PRRef{{Repo: model.GitLabRepo(glHost, glPath), Number: 5}}
	e.board.applySync(w)
	if len(e.board.refreshCh) != 1 {
		t.Fatal("no refresh started")
	}
}

func (f *fakeGitLab) Failing(host string) string {
	f.hmu.Lock()
	defer f.hmu.Unlock()
	return f.failing[host]
}

// A session outside --roots never causes a GitLab host check, whether its
// folder's remote or its merge requests are on that host.
func TestOutsideRootsAsksNoGitLabHost(t *testing.T) {
	e, gl := newGitLabEnvWith(t, func(o *Options) { o.Roots = []string{"~/projects/alpha"} })
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Remote: model.GitLabRepo("other.example.com", glPath), Branch: model.Of("main")})
	out := busySession("out", tNow)
	out.Folder = beta
	out.CreatedPRs = []model.PRRef{{Repo: model.GitLabRepo("third.example.com", glPath), Number: 4}}
	e.board.applySync(out)
	e.board.tick()
	e.clock.Set(tNow.Add(hostEvery))
	e.board.tick()
	if got := gl.asked(); len(got) != 0 {
		t.Fatalf("asked about %v", got)
	}
	in := busySession("in", tNow)
	in.CreatedPRs = []model.PRRef{{Repo: model.GitLabRepo(glHost, glPath), Number: 5}}
	e.board.applySync(in)
	e.board.tick()
	if got := gl.asked(); !slices.Equal(got, []string{glHost}) {
		t.Fatalf("asked about %v, want only the session in the roots' host", got)
	}
}

// A host check that hangs holds up neither a GitHub lookup nor the clock:
// it runs beside the board's loop.
func TestBlockedHostCheckBlocksNothing(t *testing.T) {
	e, gl := newGitLabEnv(t)
	gl.hmu.Lock()
	gl.block = make(chan struct{})
	gl.hmu.Unlock()
	w := busySession("w", tNow)
	w.CreatedPRs = []model.PRRef{{Repo: model.GitLabRepo("slow.example.com", glPath), Number: 5}, {Repo: model.GitHubRepo(exRepo), Number: 9}}
	e.board.applySync(w)
	done := make(chan struct{})
	go func() {
		e.board.tickCtx(context.Background())
		e.board.tickCtx(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		close(gl.block) // let the stuck tick finish, so the failure does not hang the run
		<-done
		t.Fatal("the board's tick waited for a host check")
	}
	if e.prs.callsFor(exRepo) == 0 {
		t.Error("the GitHub lookup waited for a host check")
	}
	close(gl.block)
	e.board.hostChecks.Wait()
	e.board.resolving.Wait()
}

// With more hosts due than one refresh checks, the ones left out come first
// next time: every host gets its turn.
func TestHostChecksRotate(t *testing.T) {
	e, gl := newGitLabEnv(t)
	w := busySession("w", tNow)
	for i := range hostSlots + 1 {
		w.CreatedPRs = append(w.CreatedPRs, model.PRRef{Repo: model.GitLabRepo(fmt.Sprintf("h%02d.example.com", i), glPath), Number: 1})
	}
	last := fmt.Sprintf("h%02d.example.com", hostSlots)
	e.board.applySync(w)
	e.board.tickCtx(context.Background()) // one refresh, not the one its answers ask for
	e.board.hostChecks.Wait()
	e.board.resolving.Wait()
	if got := gl.asked(); len(got) != hostSlots || slices.Contains(got, last) {
		t.Fatalf("first refresh asked %d hosts (last %v)", len(got), slices.Contains(got, last))
	}
	// A minute on, all of them are due again: the one never asked goes first.
	e.clock.Set(tNow.Add(hostEvery))
	e.board.refreshForges(context.Background(), false)
	e.board.hostChecks.Wait()
	if got := gl.asked(); !slices.Contains(got, last) {
		t.Fatalf("%s never asked: %v", last, got)
	}
}

// A lookup that finds the login gone stops lookups on that host in the same
// refresh, and what the board kept from there leaves the cards, catch-up and
// the page's host list; a new login brings it back.
func TestLoggedOutHostIsForgotten(t *testing.T) {
	e, gl := newGitLabEnv(t)
	one, two := model.GitLabRepo(glHost, glPath), model.GitLabRepo(glHost, "example-group/second")
	gl.prs = []model.PullRequest{mrAt(one, 5, tNow.Add(-time.Hour)), mrAt(two, 6, tNow.Add(-time.Hour))}
	w := busySession("w", tNow)
	w.CreatedPRs = []model.PRRef{{Repo: one, Number: 5}, {Repo: two, Number: 6}}
	e.board.applySync(w)
	e.board.tick()
	if n := len(onlyCard(t, e.board).PullRequests); n != 2 || len(e.board.Snapshot().GitLabHosts) != 1 {
		t.Fatalf("logged in: %d merge requests, hosts %v", n, e.board.Snapshot().GitLabHosts)
	}
	// The login goes; the host's own check, due too, answers after the
	// lookups this time.
	gl.err = errFakeNotLoggedIn
	gl.setHost(glHost, errFakeNotLoggedIn)
	gl.hmu.Lock()
	gl.block = make(chan struct{})
	gl.hmu.Unlock()
	e.clock.Set(tNow.Add(githubEvery))
	e.board.tickCtx(context.Background())
	gl.hmu.Lock()
	close(gl.block)
	gl.block = nil
	gl.hmu.Unlock()
	e.board.hostChecks.Wait()
	e.board.resolving.Wait()
	if a, b := gl.lookupsOf(one), gl.lookupsOf(two); a+b != 3 {
		t.Fatalf("lookups after the login went: %d and %d, want one more in all", a, b)
	}
	c := onlyCard(t, e.board)
	if len(c.PullRequests) != 0 || len(c.Issues) != 0 || c.Title != w.Name || len(e.board.Snapshot().GitLabHosts) != 0 {
		t.Fatalf("logged out: card %+v, hosts %v", c, e.board.Snapshot().GitLabHosts)
	}
	gl.err = nil
	gl.setHost(glHost, nil)
	e.clock.Set(tNow.Add(githubEvery + hostEvery))
	e.board.tick()
	if n := len(onlyCard(t, e.board).PullRequests); n != 2 {
		t.Fatalf("logged in again: %d merge requests", n)
	}
}

// A session outside the roots never marks a repo for a lookup, and with
// glab on a repo only ended sessions ask about is still asked once every
// ten minutes.
func TestExcludedSessionsMarkNothing(t *testing.T) {
	e, _ := newGitLabEnvWith(t, func(o *Options) { o.Roots = []string{"~/projects/alpha"} })
	gh := model.GitHubRepo(exRepo)
	e.prs.prs = []model.PullRequest{{Repo: gh, Number: 9, State: model.PRStateOpen}}
	ended := session("ended", model.StatusFinished, tNow.Add(-time.Minute))
	ended.Ended, ended.EndedAt = true, tNow.Add(-time.Minute)
	ended.CreatedPRs = []model.PRRef{{Repo: gh, Number: 9}}
	e.board.applySync(ended)
	e.board.tick()
	base := e.prs.callsFor(exRepo)
	out := busySession("out", tNow)
	out.Folder = beta
	out.CreatedPRs = []model.PRRef{{Repo: gh, Number: 10}}
	// Minute by minute: a lookup at any minute before the tenth fails here.
	for m := 1; m <= 9; m++ {
		e.board.applySync(out)
		e.clock.Set(tNow.Add(time.Duration(m) * time.Minute))
		e.board.tick()
		if got := e.prs.callsFor(exRepo) - base; got != 0 {
			t.Fatalf("%d lookups at minute %d, want none before the tenth", got, m)
		}
	}
	e.clock.Set(tNow.Add(10 * time.Minute))
	e.board.tick()
	if got := e.prs.callsFor(exRepo) - base; got != 1 {
		t.Fatalf("%d lookups after ten minutes, want 1", got)
	}
}

// With glab missing (or switched off) the core asks GitLab nothing at all.
func TestMissingGlabAsksNothing(t *testing.T) {
	for _, st := range []source.State{source.StateMissing, source.StateOff} {
		e, gl := newGitLabEnv(t)
		e.board.SetSources([]source.Status{{Kind: source.KindGitLab, State: st}})
		e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitLabRepo(glHost, glPath), Branch: model.Of("main")})
		w := busySession("w", tNow)
		w.CreatedPRs = []model.PRRef{{Repo: model.GitLabRepo(glHost, glPath), Number: 5}}
		e.board.applySync(w)
		e.board.tick()
		e.clock.Set(tNow.Add(hostEvery))
		e.board.tick()
		if hosts, probes, lookups := gl.counts(); hosts+probes+lookups != 0 {
			t.Errorf("%s: %d host checks, %d probes, %d lookups", st, hosts, probes, lookups)
		}
	}
}

// A lookup on a host whose yes ran out in the provider makes no call: it
// waits, pending, for the host's background check, is not counted as asked
// and purges nothing the board shows; it runs once the check says yes.
func TestExpiredApprovalWaitsForTheHostCheck(t *testing.T) {
	e, gl := newGitLabEnv(t)
	repo := model.GitLabRepo(glHost, glPath)
	gl.prs = []model.PullRequest{mrAt(repo, 5, tNow.Add(-time.Hour))}
	w := busySession("w", tNow)
	w.CreatedPRs = []model.PRRef{{Repo: repo, Number: 5}}
	e.board.applySync(w)
	e.board.tick()
	base := gl.lookupsOf(repo)
	if base != 1 || len(onlyCard(t, e.board).PullRequests) != 1 {
		t.Fatalf("logged in: %d lookups, card %+v", base, onlyCard(t, e.board))
	}
	gl.hmu.Lock()
	gl.stale = map[string]bool{glHost: true}
	gl.block = make(chan struct{})
	gl.hmu.Unlock()
	e.clock.Set(tNow.Add(githubEvery))
	n := e.board.decided()
	e.board.tickCtx(context.Background())
	e.board.refreshForges(context.Background(), false) // slots free, but the host's check runs
	e.board.mu.Lock()
	pending, asking := e.board.pending[repo], e.board.glAsking[glHost] != nil
	e.board.mu.Unlock()
	if got := gl.lookupsOf(repo); got != base || !pending || !asking {
		t.Fatalf("yes ran out: %d lookups more, pending %v, host checked %v", got-base, pending, asking)
	}
	if len(onlyCard(t, e.board).PullRequests) != 1 || len(e.board.Snapshot().GitLabHosts) != 1 {
		t.Fatalf("a lookup that waits purged the host: %+v", onlyCard(t, e.board))
	}
	for len(e.board.refreshCh) > 0 {
		<-e.board.refreshCh
	}
	gl.hmu.Lock()
	close(gl.block)
	gl.block = nil
	gl.hmu.Unlock()
	e.board.hostChecks.Wait()
	if len(e.board.refreshCh) != 1 {
		t.Fatal("the check's answer asked for no refresh: the waiting lookup would wait for the next tick")
	}
	e.board.settle(n)
	if got := gl.lookupsOf(repo); got != base+1 {
		t.Fatalf("after the check said yes: %d lookups more, want 1", got-base)
	}
	if hosts, _, _ := gl.counts(); hosts != 2 || gl.maxPerHost != 1 {
		t.Fatalf("%d host checks, %d at once on one host; want 2, one at a time", hosts, gl.maxPerHost)
	}
}

// Host checks run at most hostSlots at once and one per host: the others
// start as slots free, and a refresh while every slot is busy starts none.
func TestHostChecksShareTheSlots(t *testing.T) {
	e, gl := newGitLabEnv(t)
	gl.hmu.Lock()
	gl.block = make(chan struct{})
	gl.hmu.Unlock()
	w := busySession("w", tNow)
	for i := range hostSlots + 2 {
		w.CreatedPRs = append(w.CreatedPRs, model.PRRef{Repo: model.GitLabRepo(fmt.Sprintf("h%d.example.com", i), glPath), Number: 1})
	}
	e.board.applySync(w)
	n := e.board.decided()
	e.board.tickCtx(context.Background())
	e.board.refreshForges(context.Background(), false)
	e.board.mu.Lock()
	asking := len(e.board.glAsking)
	e.board.mu.Unlock()
	if asking != hostSlots {
		t.Fatalf("%d host checks started at once, want %d", asking, hostSlots)
	}
	gl.hmu.Lock()
	close(gl.block)
	gl.block = nil
	gl.hmu.Unlock()
	e.board.settle(n)
	gl.hmu.Lock()
	defer gl.hmu.Unlock()
	if len(gl.hostCalls) != hostSlots+2 || gl.maxInHost > hostSlots || gl.maxPerHost != 1 {
		t.Fatalf("%d checks, %d at once, %d per host at once", len(gl.hostCalls), gl.maxInHost, gl.maxPerHost)
	}
}

// glab logged in nowhere overrules every yes the board kept: a host whose own
// check is not due loses its merge requests and leaves the page's list too.
func TestLoggedInNowhereOverrulesEveryYes(t *testing.T) {
	e, gl := newGitLabEnv(t)
	const other = "other.example.com"
	gl.setHost(other, nil)
	one, two := model.GitLabRepo(other, glPath), model.GitLabRepo(glHost, glPath)
	gl.prs = []model.PullRequest{mrAt(one, 5, tNow.Add(-time.Hour)), mrAt(two, 6, tNow.Add(-time.Hour))}
	w := busySession("w", tNow)
	w.CreatedPRs = []model.PRRef{{Repo: one, Number: 5}}
	e.board.applySync(w)
	e.board.tick()
	e.clock.Set(tNow.Add(30 * time.Second))
	w.CreatedPRs = append(w.CreatedPRs, model.PRRef{Repo: two, Number: 6})
	e.board.applySync(w)
	e.board.tick()
	if n := len(onlyCard(t, e.board).PullRequests); n != 2 || len(e.board.Snapshot().GitLabHosts) != 2 {
		t.Fatalf("logged in to both: %d merge requests, hosts %v", n, e.board.Snapshot().GitLabHosts)
	}
	gl.setHost(other, fmt.Errorf("fake: %w", source.ErrNoLogins))
	e.clock.Set(tNow.Add(hostEvery)) // other's check is due, glHost's is not
	e.board.tick()
	if got := gl.asked(); len(slices.DeleteFunc(slices.Clone(got), func(h string) bool { return h != glHost })) != 1 {
		t.Fatalf("%s was checked again: %v", glHost, got)
	}
	if n := len(onlyCard(t, e.board).PullRequests); n != 0 || len(e.board.Snapshot().GitLabHosts) != 0 {
		t.Fatalf("logged in nowhere: %d merge requests, hosts %v", n, e.board.Snapshot().GitLabHosts)
	}
}

// setBlock sets what Host waits for (nil: nothing).
func (f *fakeGitLab) setBlock(c chan struct{}) {
	f.hmu.Lock()
	f.block = c
	f.hmu.Unlock()
}

// enter makes Host report each call as it starts, and returns where.
func (f *fakeGitLab) enter() chan string {
	c := make(chan string, 16)
	f.hmu.Lock()
	f.entered = c
	f.hmu.Unlock()
	return c
}

// quits makes Host report each call whose caller gave up, and returns where.
func (f *fakeGitLab) quits() chan string {
	c := make(chan string, 16)
	f.hmu.Lock()
	f.quit = c
	f.hmu.Unlock()
	return c
}

// calledOff waits for a Host call blocked on block to be given up; after
// five seconds it lets the call go (so the test ends) and fails.
func calledOff(t *testing.T, quit chan string, block chan struct{}) {
	t.Helper()
	select {
	case <-quit:
	case <-time.After(5 * time.Second):
		close(block)
		t.Fatal("the host check was not called off")
	}
}

// glabHostWithMR is a GitLab env whose session has merge request 5 on
// glHost, looked up once, with the host's next check and lookup due.
func glabHostWithMR(t *testing.T) (*testEnv, *fakeGitLab, model.ForgeRepo) {
	t.Helper()
	e, gl := newGitLabEnv(t)
	repo := model.GitLabRepo(glHost, glPath)
	gl.prs = []model.PullRequest{mrAt(repo, 5, tNow.Add(-time.Hour))}
	w := busySession("w", tNow)
	w.CreatedPRs = []model.PRRef{{Repo: repo, Number: 5}}
	e.board.applySync(w)
	e.board.tick()
	if n := len(onlyCard(t, e.board).PullRequests); n != 1 || gl.lookupsOf(repo) != 1 {
		t.Fatalf("logged in: %d merge requests, %d lookups", n, gl.lookupsOf(repo))
	}
	e.clock.Set(tNow.Add(hostEvery))
	return e, gl, repo
}

// A lookup that started before a host check found the login gone brings
// back nothing it learned: not the merge request, not the host on the
// page, and it marks nothing as asked.
func TestOlderLookupRestoresNothingAfterAPurge(t *testing.T) {
	e, gl, repo := glabHostWithMR(t)
	gl.setHost(glHost, errFakeNotLoggedIn)
	gl.setBlock(make(chan struct{}))
	started, release := make(chan struct{}), make(chan struct{})
	gl.hmu.Lock()
	gl.lookupGate = func(model.ForgeRepo) { close(started); <-release }
	gl.hmu.Unlock()
	done := make(chan struct{})
	go func() { e.board.tickCtx(context.Background()); close(done) }()
	<-started // the lookup is out, the check waits
	gl.hmu.Lock()
	close(gl.block)
	gl.block = nil
	gl.hmu.Unlock()
	e.board.hostChecks.Wait() // the check said no: the host is purged
	close(release)
	<-done
	e.board.mu.Lock()
	_, lookedUp := e.board.lookedUp[repo]
	asked := e.board.asked[model.PRRef{Repo: repo, Number: 5}]
	e.board.mu.Unlock()
	if c := onlyCard(t, e.board); len(c.PullRequests) != 0 || len(e.board.Snapshot().GitLabHosts) != 0 || lookedUp || asked {
		t.Fatalf("the older lookup came back: card %+v, hosts %v, looked up %v, asked %v", c, e.board.Snapshot().GitLabHosts, lookedUp, asked)
	}
}

// A host check that started before a lookup met a 401 does not undo it
// with its older yes: lookups there wait for the next check.
func TestOlderYesDoesNotUndoA401(t *testing.T) {
	e, gl, repo := glabHostWithMR(t)
	gl.setBlock(make(chan struct{})) // the check will say yes, late
	entered := gl.enter()
	first := true
	gl.hmu.Lock()
	gl.lookupGate = func(model.ForgeRepo) { // the check is out first
		if first {
			first = false
			<-entered
		}
	}
	gl.hmu.Unlock()
	gl.mu.Lock()
	gl.err = errFakeNotLoggedIn
	gl.mu.Unlock()
	n := e.board.decided()
	e.board.tickCtx(context.Background()) // the lookup meets the 401
	gl.mu.Lock()
	gl.err = nil
	gl.mu.Unlock()
	gl.hmu.Lock()
	close(gl.block)
	gl.block = nil
	gl.hmu.Unlock()
	e.board.settle(n)
	e.board.mu.Lock()
	ok := e.board.glHosts[glHost].ok
	e.board.mu.Unlock()
	if ok || gl.lookupsOf(repo) != 2 || len(onlyCard(t, e.board).PullRequests) != 0 || len(e.board.Snapshot().GitLabHosts) != 0 {
		t.Fatalf("the older yes won: ok %v, %d lookups, card %+v", ok, gl.lookupsOf(repo), onlyCard(t, e.board))
	}
	// The next check, a minute on, decides again.
	e.clock.Set(tNow.Add(2 * hostEvery))
	e.board.tick()
	if len(onlyCard(t, e.board).PullRequests) != 1 {
		t.Fatalf("the next check's yes: card %+v", onlyCard(t, e.board))
	}
}

// A host check whose last session left is called off: the provider gives
// up waiting (no glab runs) and nothing is recorded about the host. One
// still queued when its session leaves never reaches the provider.
func TestCheckWithoutItsSessionIsCalledOff(t *testing.T) {
	e, gl := newGitLabEnv(t)
	const other = "other.example.com"
	gl.setHost(other, nil)
	block := make(chan struct{})
	gl.setBlock(block)
	quit := gl.quits()
	w := busySession("w", tNow)
	w.CreatedPRs = []model.PRRef{{Repo: model.GitLabRepo(other, glPath), Number: 5}}
	e.board.applySync(w)
	n := e.board.decided()
	entered := gl.enter()
	e.board.tickCtx(context.Background())
	<-entered
	e.board.applySync(model.SessionGone{Key: w.Key})
	e.board.refreshForges(context.Background(), false)
	calledOff(t, quit, block)
	e.board.settle(n)
	e.board.mu.Lock()
	_, decided := e.board.glHosts[other]
	asking := len(e.board.glAsking)
	e.board.mu.Unlock()
	gl.hmu.Lock()
	gaveUp := gl.gaveUp
	gl.hmu.Unlock()
	if gaveUp != 1 || decided || asking != 0 {
		t.Fatalf("session gone: %d calls given up, host decided %v, %d checks out", gaveUp, decided, asking)
	}

	// Queued: started, with its session gone before it ran.
	e.board.applySync(w)
	e.board.mu.Lock()
	st := e.board.hostStampLocked(other)
	e.board.mu.Unlock()
	e.board.applySync(model.SessionGone{Key: w.Key})
	gl.setBlock(nil)
	ctx, stop := context.WithCancel(context.Background())
	e.board.mu.Lock()
	e.board.glAsking[other] = stop
	e.board.hostChecks.Add(1)
	e.board.mu.Unlock()
	calls, _, _ := gl.counts()
	e.board.checkHost(context.Background(), ctx, other, st)
	if got, _, _ := gl.counts(); got != calls {
		t.Fatalf("a queued check without its session asked the provider")
	}
}

// A host check under way when the GitLab source switches off is called off
// and changes nothing.
func TestCheckIsCalledOffWithTheSource(t *testing.T) {
	e, gl := newGitLabEnv(t)
	block := make(chan struct{})
	gl.setBlock(block)
	quit := gl.quits()
	w := busySession("w", tNow)
	w.CreatedPRs = []model.PRRef{{Repo: model.GitLabRepo(glHost, glPath), Number: 5}}
	e.board.applySync(w)
	n := e.board.decided()
	entered := gl.enter()
	e.board.tickCtx(context.Background())
	<-entered
	e.board.SetSources([]source.Status{{Kind: source.KindGitLab, State: source.StateOff, Detail: "switched off"}})
	calledOff(t, quit, block)
	e.board.settle(n)
	e.board.mu.Lock()
	_, decided := e.board.glHosts[glHost]
	e.board.mu.Unlock()
	gl.hmu.Lock()
	gaveUp := gl.gaveUp
	gl.hmu.Unlock()
	if gaveUp != 1 || decided || len(e.board.Snapshot().GitLabHosts) != 0 {
		t.Fatalf("switched off: %d calls given up, host decided %v", gaveUp, decided)
	}

	// A lookup under way when the source switches off records nothing.
	e2, gl2, repo := glabHostWithMR(t)
	gl2.hmu.Lock()
	gl2.lookupGate = func(model.ForgeRepo) {
		e2.board.SetSources([]source.Status{{Kind: source.KindGitLab, State: source.StateOff, Detail: "switched off"}})
	}
	gl2.hmu.Unlock()
	e2.board.tickCtx(context.Background())
	e2.board.mu.Lock()
	at := e2.board.lookedUp[repo]
	e2.board.mu.Unlock()
	if gl2.lookupsOf(repo) != 2 || !at.Equal(tNow) {
		t.Fatalf("switched off during a lookup: %d lookups, looked up at %v", gl2.lookupsOf(repo), at)
	}

	// Switched off and on again while the lookup is out: still nothing.
	e3, gl3, repo3 := glabHostWithMR(t)
	gl3.hmu.Lock()
	gl3.lookupGate = func(model.ForgeRepo) {
		e3.board.SetSources([]source.Status{{Kind: source.KindGitLab, State: source.StateOff, Detail: "switched off"}})
		e3.board.SetSources([]source.Status{glOK})
	}
	gl3.hmu.Unlock()
	e3.board.tickCtx(context.Background())
	e3.board.mu.Lock()
	at3 := e3.board.lookedUp[repo3]
	e3.board.mu.Unlock()
	if gl3.lookupsOf(repo3) != 2 || !at3.Equal(tNow) {
		t.Fatalf("switched off and on during a lookup: %d lookups, looked up at %v", gl3.lookupsOf(repo3), at3)
	}
}

// The first probe runs with the host checks: when sitboard stops while it
// waits for glab, the board's Run waits for it, and it shows nothing. One
// asked to start after the stop does not start; one whose answer is older
// than the line set meanwhile is dropped.
func TestProbeStopsWithTheBoard(t *testing.T) {
	e, gl := newGitLabEnv(t)
	saw := make(chan struct{})
	gl.hmu.Lock()
	gl.probeGate = func(ctx context.Context) {
		<-ctx.Done()
		close(saw)
		for range 1000 { // slow to give up: Run waits all the same
			runtime.Gosched()
		}
	}
	gl.hmu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan error, 1)
	go func() { ran <- e.board.Run(ctx) }()
	e.board.StartGitLabProbe(ctx)
	cancel()
	if err := <-ran; err != nil {
		t.Fatal(err)
	}
	select {
	case <-saw:
	default:
		t.Fatal("Run returned before the probe it started gave up")
	}
	e.board.mu.Lock()
	probing := e.board.glProbing
	e.board.mu.Unlock()
	if probing || slices.ContainsFunc(e.board.Snapshot().Sources, func(s contract.SourceStatus) bool { return s.Kind == contract.SourceGitLab }) {
		t.Fatalf("after the stop: probing %v, sources %+v", probing, e.board.Snapshot().Sources)
	}
	e.board.StartGitLabProbe(ctx)
	e.board.hostChecks.Wait()
	if _, probes, _ := gl.counts(); probes != 1 {
		t.Fatalf("%d probes, want only the first", probes)
	}

	// Asked to start once the board stopped, or for a sitboard stopping: none.
	e3, gl3 := newGitLabEnv(t)
	gone, end := context.WithCancel(context.Background())
	end()
	e3.board.StartGitLabProbe(gone)
	e3.board.markClosed()
	e3.board.StartGitLabProbe(context.Background())
	e3.board.hostChecks.Wait()
	if _, probes, _ := gl3.counts(); probes != 0 {
		t.Fatalf("%d probes after the stop", probes)
	}

	// A probe that answers after the line was set anew is dropped.
	e2, gl2 := newGitLabEnv(t)
	release := make(chan struct{})
	gl2.hmu.Lock()
	gl2.probeGate = func(context.Context) { <-release }
	gl2.hmu.Unlock()
	e2.board.StartGitLabProbe(context.Background())
	off := source.Status{Kind: source.KindGitLab, State: source.StateOff, Detail: "switched off"}
	e2.board.SetSources([]source.Status{off})
	close(release)
	e2.board.hostChecks.Wait()
	if l := gitlabLine(t, e2.board); l.State != contract.SourceOff {
		t.Fatalf("an older probe overwrote the line: %+v", l)
	}
}

// glab logged in to no host drops what any check under way would learn: an
// older yes for another host, not decided yet, is not applied.
func TestLoggedInNowhereDropsChecksUnderWay(t *testing.T) {
	e, gl := newGitLabEnv(t)
	const early, late = "a.example.com", "b.example.com"
	gl.setHost(early, nil)
	gl.setHost(late, fmt.Errorf("fake: %w", source.ErrNoLogins))
	release, lateGo := make(chan struct{}), make(chan struct{})
	gl.hmu.Lock()
	gl.holds = map[string]chan struct{}{early: release, late: lateGo}
	gl.hmu.Unlock()
	entered := gl.enter()
	w := busySession("w", tNow)
	w.CreatedPRs = []model.PRRef{{Repo: model.GitLabRepo(early, glPath), Number: 1}, {Repo: model.GitLabRepo(late, glPath), Number: 2}}
	e.board.applySync(w)
	n := e.board.decided()
	e.board.tickCtx(context.Background())
	for range 2 {
		<-entered
	}
	close(lateGo)
	for e.board.decided() == n { // late's no lands while early's check waits
		runtime.Gosched()
	}
	close(release)
	e.board.hostChecks.Wait()
	e.board.mu.Lock()
	ok := e.board.glHosts[early].ok
	e.board.mu.Unlock()
	if ok {
		t.Fatal("a yes asked before glab said it is logged in nowhere was applied")
	}
}

// A GitHub lookup that answers after the board stopped records nothing.
func TestLookupAfterTheStopRecordsNothing(t *testing.T) {
	e := newEnv(t)
	repo := model.GitHubRepo(exRepo)
	e.prs.prs = []model.PullRequest{mrAt(repo, 9, tNow.Add(-time.Hour))}
	w := busySession("w", tNow)
	w.CreatedPRs = []model.PRRef{{Repo: repo, Number: 9}}
	e.board.applySync(w)
	ctx, cancel := context.WithCancel(context.Background())
	e.prs.mu.Lock()
	e.prs.gate = cancel
	e.prs.mu.Unlock()
	e.board.tickCtx(ctx)
	e.board.mu.Lock()
	_, lookedUp := e.board.lookedUp[repo]
	e.board.mu.Unlock()
	if lookedUp || len(onlyCard(t, e.board).PullRequests) != 0 {
		t.Fatalf("after the stop: looked up %v, card %+v", lookedUp, onlyCard(t, e.board))
	}
}

// A host check under way when the board stops (its ctx ends, or Run
// returned) records nothing.
func TestCheckIsCalledOffWithTheBoard(t *testing.T) {
	for _, how := range []string{"ctx", "closed"} {
		e, gl := newGitLabEnv(t)
		block := make(chan struct{})
		gl.setBlock(block)
		quit, entered := gl.quits(), gl.enter()
		w := busySession("w", tNow)
		w.CreatedPRs = []model.PRRef{{Repo: model.GitLabRepo(glHost, glPath), Number: 5}}
		e.board.applySync(w)
		ctx, cancel := context.WithCancel(context.Background())
		e.board.tickCtx(ctx)
		<-entered
		if how == "ctx" {
			cancel()
			calledOff(t, quit, block)
		} else {
			e.board.markClosed()
			close(block)
		}
		e.board.hostChecks.Wait()
		cancel()
		e.board.mu.Lock()
		_, decided := e.board.glHosts[glHost]
		e.board.mu.Unlock()
		if decided {
			t.Errorf("%s: a check that ended after the stop was recorded", how)
		}
	}
}

// A host's stamp changes with every bump, also when the count of hosts
// reaches its cap and starts afresh.
func TestHostStampsSurviveTheCap(t *testing.T) {
	b := newGitLabEnvBoard(t)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bumpHostLocked("a.example.com")
	st := b.hostStampLocked("a.example.com")
	for i := len(b.glGen); i < 4096; i++ {
		b.bumpHostLocked(fmt.Sprintf("h%d.example.com", i))
	}
	b.bumpHostLocked("new.example.com") // past the cap: the counts start afresh
	b.bumpHostLocked("a.example.com")
	if b.hostStampLocked("a.example.com") == st {
		t.Fatal("a stamp from before the cap matches one after it")
	}
}

func newGitLabEnvBoard(t *testing.T) *Board {
	t.Helper()
	e, _ := newGitLabEnv(t)
	return e.board
}

// When the last session needing a host leaves, what is under way about the
// host is dropped at once: a lookup that answers after the session came
// back records nothing, and a check waiting its turn is called off without
// waiting for a refresh.
func TestLeavingDropsWhatIsUnderWayAtOnce(t *testing.T) {
	e, gl, repo := glabHostWithMR(t)
	w := onlySession(t, e.board)
	first := true
	gl.hmu.Lock()
	gl.lookupGate = func(model.ForgeRepo) {
		if first {
			first = false
			e.board.applySync(model.SessionGone{Key: w.Key})
			e.board.applySync(w)
		}
	}
	gl.hmu.Unlock()
	e.board.tickCtx(context.Background())
	e.board.hostChecks.Wait()
	e.board.mu.Lock()
	at := e.board.lookedUp[repo]
	e.board.mu.Unlock()
	if !at.Equal(tNow) {
		t.Fatalf("a lookup from before the session left and came back was recorded at %v", at)
	}

	e2, gl2 := newGitLabEnv(t)
	const other = "other.example.com"
	gl2.setHost(other, nil)
	block := make(chan struct{})
	gl2.setBlock(block)
	quit, entered := gl2.quits(), gl2.enter()
	w2 := busySession("w", tNow)
	w2.CreatedPRs = []model.PRRef{{Repo: model.GitLabRepo(other, glPath), Number: 5}}
	e2.board.applySync(w2)
	e2.board.tickCtx(context.Background())
	<-entered
	e2.board.applySync(model.SessionGone{Key: w2.Key}) // no refresh after it
	calledOff(t, quit, block)
	e2.board.applySync(w2)
	e2.board.hostChecks.Wait()
	e2.board.mu.Lock()
	_, decided := e2.board.glHosts[other]
	e2.board.mu.Unlock()
	if decided {
		t.Fatal("a check from before the session left and came back was recorded")
	}
}

// onlySession is the board's one session fact.
func onlySession(t *testing.T, b *Board) model.SessionFact {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.sessions) != 1 {
		t.Fatalf("%d sessions", len(b.sessions))
	}
	for _, s := range b.sessions {
		return s
	}
	return model.SessionFact{}
}

// The GitLab line shows a host's newest failure: a check that got no answer
// after an earlier no names the failure, an answer clears it, and a host
// named only by a session's remote shows its failure too.
func TestGitLabLineShowsTheNewestFailure(t *testing.T) {
	e, gl := newGitLabEnv(t)
	e.board.SetSources([]source.Status{glOK})
	fail := func(host, note string) {
		gl.hmu.Lock()
		if gl.failing == nil {
			gl.failing = map[string]string{}
		}
		gl.failing[host] = note
		gl.hmu.Unlock()
	}
	line := func(want string) {
		t.Helper()
		if l := gitlabLine(t, e.board); (want == "") != (l.State == contract.SourceOK) || l.Detail != want {
			t.Fatalf("line %+v, want %q", l, want)
		}
	}
	w := busySession("w", tNow)
	w.CreatedPRs = []model.PRRef{{Repo: model.GitLabRepo(glHost, glPath), Number: 5}}
	gl.setHost(glHost, errFakeNotLoggedIn)
	e.board.applySync(w)
	e.board.tick()
	line("not logged in to " + glHost)
	gl.setHost(glHost, errors.New("fake: no answer"))
	fail(glHost, "glab did not answer in time")
	e.clock.Set(tNow.Add(hostEvery))
	e.board.tick()
	line("glab did not answer in time")
	gl.setHost(glHost, errFakeNotLoggedIn) // a no again, an older lookup note left
	fail(glHost, "rate limited")
	e.clock.Set(tNow.Add(2 * hostEvery))
	e.board.tick()
	line("not logged in to " + glHost)
	gl.setHost(glHost, nil)
	fail(glHost, "")
	e.clock.Set(tNow.Add(3 * hostEvery))
	e.board.tick()
	line("")

	const other = "other.example.com"
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Remote: model.GitLabRepo(other, glPath), Branch: model.Of("main")})
	r := busySession("r", tNow)
	r.Folder = beta
	gl.setHost(other, errFakeNotLoggedIn) // a remote on a host glab does not know: no failure
	e.board.applySync(r)
	e.board.tick()
	e.clock.Set(tNow.Add(4 * hostEvery)) // the folder's remote is known now; its host is due
	e.board.tick()
	line("")
	gl.setHost(other, errors.New("fake: no answer"))
	fail(other, "auth check failed")
	e.clock.Set(tNow.Add(5 * hostEvery))
	e.board.tick()
	line("auth check failed")
}

// With GitLab off, a refresh looks at no session for GitLab's sake.
func TestGitLabOffScansNothing(t *testing.T) {
	e, _ := newGitLabEnv(t)
	e.board.SetSources([]source.Status{{Kind: source.KindGitLab, State: source.StateMissing, Detail: "glab not found"}})
	w := busySession("w", tNow)
	w.CreatedPRs = []model.PRRef{{Repo: model.GitLabRepo(glHost, glPath), Number: 5}, {Repo: model.GitHubRepo(exRepo), Number: 9}}
	e.board.applySync(w)
	e.board.mu.Lock()
	before := e.board.glScans
	e.board.mu.Unlock()
	e.board.tick()
	e.board.mu.Lock()
	after := e.board.glScans
	e.board.mu.Unlock()
	if after != before {
		t.Fatalf("%d GitLab scans with GitLab missing", after-before)
	}
}
