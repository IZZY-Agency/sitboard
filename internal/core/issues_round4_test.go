package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// Review round 4 of the GitLab issue lister (#174): one generation per glab
// host name and one for GitHub, moved more often, never less. Each test here
// fails without its part.

// Ruling 1: every authoritative denial moves the generation, also one for a
// repository already found gone: an issue answer asked between the two
// denials is dropped whole.
func TestARepeatedDenialDropsAnIssueAnswerAskedBetween(t *testing.T) {
	g := &gatedIssues{title: "private title", open: 2}
	e := gatedEnv(t, g)
	repo := model.GitHubRepo(exRepo)
	e.prs.mu.Lock()
	e.prs.err = fmt.Errorf("github: %w", source.ErrRepoNotFound)
	e.prs.mu.Unlock()
	s := busySession("pr", tNow)
	s.CreatedPRs = []model.PRRef{{Repo: repo, Number: 1}}
	e.board.applySync(s)
	e.board.refreshForges(context.Background(), false) // the first denial
	e.board.mu.Lock()
	gone := e.board.goneRepos[repo]
	e.board.mu.Unlock()
	if !gone {
		t.Fatal("setup: the repository is not found gone")
	}
	// Access is back for a moment: a look reads private rows.
	gate := openOnEnd(t)
	g.set(func(g *gatedIssues) { g.gate = gate.ch })
	e.board.mu.Lock()
	e.board.startIssuesLocked(context.Background(), e.board.now())
	e.board.mu.Unlock()
	waitFor(t, e.board, "the look asked", func() bool { return len(g.calls()) > 0 })
	// The second denial, while the look is out.
	calls := e.prs.callsFor(exRepo)
	e.clock.Set(tNow.Add(githubEvery))
	e.board.refreshForges(context.Background(), true)
	if e.prs.callsFor(exRepo) == calls {
		t.Fatal("setup: the pull request was not looked up again")
	}
	gate.open()
	e.board.resolving.Wait()
	if listedFor(e.board, repo) {
		t.Fatal("an issue answer asked before a repeated denial was applied")
	}
}

// glDenied: host's last check is a definite no.
func glDenied(b *Board, host string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	d, ok := b.glHosts[host]
	return ok && !d.ok && d.why != nil
}

// Ruling 2: glab logged in to no host is handled first and for every host,
// even from a batch whose own host's generation moved while it was out.
func TestNoLoginsFromAnOlderIssueBatchDeniesEveryHost(t *testing.T) {
	e, _, gl, repoA, repoB := twoHostsEnv(t)
	if !listedFor(e.board, repoA) || !listedFor(e.board, repoB) {
		t.Fatal("setup: not listed")
	}
	gate := openOnEnd(t)
	gl.mu.Lock()
	gl.gate[glHost] = gate.ch
	gl.err[glHost] = fmt.Errorf("glab: %w", source.ErrNoLogins)
	gl.mu.Unlock()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
	startLook(e.board)
	waitEntered(t, gl, glHost)
	e.board.mu.Lock()
	e.board.bumpHostLocked(repoA.Host) // A's generation moves while its batch is out
	e.board.mu.Unlock()
	gate.open()
	e.board.resolving.Wait()
	if listedFor(e.board, repoB) || !glDenied(e.board, glOther) {
		t.Fatalf("no logins anywhere left the other host: listed %v, denied %v", listedFor(e.board, repoB), glDenied(e.board, glOther))
	}
}

// Ruling 2, at the merge request lookup: a lookup whose host's generation
// moved while it was out still carries glab's verdict for every host.
func TestNoLoginsFromAnOlderMergeRequestLookupDeniesEveryHost(t *testing.T) {
	e, glab, _, repoA, repoB := twoHostsEnv(t)
	if !listedFor(e.board, repoB) {
		t.Fatal("setup: not listed")
	}
	glab.mu.Lock()
	glab.err = fmt.Errorf("glab: %w", source.ErrNoLogins)
	glab.mu.Unlock()
	glab.hmu.Lock()
	glab.lookupGate = func(r model.ForgeRepo) {
		if r.Host == glHost {
			e.board.mu.Lock()
			e.board.bumpHostLocked(repoA.Host)
			e.board.mu.Unlock()
		}
	}
	glab.hmu.Unlock()
	s := busySession("mr", tNow)
	s.CreatedPRs = []model.PRRef{{Repo: repoA, Number: 1}}
	e.board.applySync(s)
	before := glab.lookupsOf(repoA)
	e.board.refreshForges(context.Background(), false)
	if glab.lookupsOf(repoA) == before {
		t.Fatal("setup: the merge request was not looked up")
	}
	if listedFor(e.board, repoB) || !glDenied(e.board, glOther) {
		t.Fatalf("no logins anywhere left the other host: listed %v, denied %v", listedFor(e.board, repoB), glDenied(e.board, glOther))
	}
}

// partlyChecked answers the repositories in by and returns err with them, as
// the GitLab adapter does when the login's yes ran out between chunks.
type partlyChecked struct {
	mu  sync.Mutex
	by  map[model.ForgeRepo]model.RepoIssues
	err error
}

func (p *partlyChecked) OpenIssues(_ context.Context, repos []model.ForgeRepo, _ map[model.ForgeRepo][]int) ([]model.RepoIssues, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []model.RepoIssues
	for _, r := range repos {
		if ri, ok := p.by[r]; ok {
			out = append(out, ri)
		}
	}
	return out, p.err
}

// Ruling 4: rows verified in the batch's generation are applied even when a
// later chunk was not asked (ErrNotChecked); only the unanswered
// repositories count as not asked.
func TestRowsVerifiedBeforeALaterChunkWasNotCheckedAreApplied(t *testing.T) {
	pc := &partlyChecked{by: map[model.ForgeRepo]model.RepoIssues{}}
	e, _ := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitLab: pc}
	})
	repoA, repoC := model.GitLabRepo(glHost, glPath), model.GitLabRepo(glHost, glPath+"-two")
	pc.by[repoA], pc.by[repoC] = sampleIssues(repoA, 4, 1), sampleIssues(repoC, 2, 1)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: repoA, Branch: model.Of("main")})
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Remote: repoC, Branch: model.Of("main")})
	b := busySession("b", tNow)
	b.Folder = beta
	e.board.applySync(busySession("a", tNow))
	e.board.applySync(b)
	e.board.tick() // the host check runs
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick() // the first look
	if !listedFor(e.board, repoA) || !listedFor(e.board, repoC) {
		t.Fatal("setup: not listed")
	}
	// The next look: A's project is verified gone, C's chunk was not asked.
	pc.mu.Lock()
	pc.by = map[model.ForgeRepo]model.RepoIssues{repoA: {Repo: repoA, State: model.RepoIssuesNotFound}}
	pc.err = fmt.Errorf("glab: %w", source.ErrNotChecked)
	pc.mu.Unlock()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
	startLook(e.board)
	e.board.resolving.Wait()
	e.board.mu.Lock()
	ri := e.board.issues[e.board.issueKey(repoA)]
	_, triedA := e.board.issuesTried[e.board.issueKey(repoA)]
	_, triedC := e.board.issuesTried[e.board.issueKey(repoC)]
	e.board.mu.Unlock()
	if ri.State != model.RepoIssuesNotFound || len(ri.List) != 0 {
		t.Fatalf("a verified loss next to a chunk not asked was not applied: %+v", ri)
	}
	if !triedA || triedC {
		t.Fatalf("asked: A %v (want true), C %v (want false)", triedA, triedC)
	}
}

// Ruling 9: a batch waiting for a slot asks only about the repositories
// still on the board when it starts; one whose project left is not sent.
func TestAQueuedBatchDoesNotAskAboutAProjectThatLeft(t *testing.T) {
	was := issueBatchSlots
	issueBatchSlots = 1
	t.Cleanup(func() { issueBatchSlots = was })
	e, _, gl, _, repoB := twoHostsEnv(t)
	gate := openOnEnd(t)
	gl.mu.Lock()
	gl.gate[glHost] = gate.ch
	gl.mu.Unlock()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
	startLook(e.board)
	waitEntered(t, gl, glHost)
	ended := busySession("b", tNow)
	ended.Folder = beta
	ended.Ended, ended.Live = true, model.Of(false)
	e.board.Apply(ended) // not applySync: that waits for the look
	waitFor(t, e.board, "beta left the board", func() bool {
		for _, r := range e.board.issueReposLocked() {
			if r == repoB {
				return false
			}
		}
		return true
	})
	gate.open()
	e.board.resolving.Wait()
	for {
		select {
		case h := <-gl.entered:
			if h == glOther {
				t.Fatal("a queued batch asked about a project that left the board")
			}
			continue
		default:
		}
		break
	}
}

// Ruling 5: the selection past the cap rotates by when each repository was
// last picked, not by whether its answer counted: a look whose answer is
// dropped (the generation moved) does not let the same first hundred win
// the next look too.
func TestADroppedLookDoesNotStarveTheRepositoriesPastTheCap(t *testing.T) {
	g := &gatedIssues{title: "t", open: 1}
	e := gatedEnv(t, g)
	for i := range maxIssueRepos + 1 {
		folder := fmt.Sprintf("%s/projects/p%03d", testHome, i)
		e.repos.set(folder, model.Repo{Root: folder, MainRoot: folder, Remote: model.GitHubRepo(fmt.Sprintf("sample-org/r%03d", i)), Branch: model.Of("main")})
		s := busySession(fmt.Sprintf("p%03d", i), tNow)
		s.Folder = folder
		e.board.applySync(s)
	}
	gate := openOnEnd(t)
	g.set(func(g *gatedIssues) { g.gate = gate.ch })
	startLook(e.board)
	waitFor(t, e.board, "the first look asked", func() bool { return len(g.calls()) == 1 })
	e.board.mu.Lock()
	e.board.ghGen++ // access changed elsewhere: the first look's answer is dropped
	e.board.mu.Unlock()
	gate.open()
	e.board.resolving.Wait()
	g.set(func(g *gatedIssues) { g.gate = nil })
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	startLook(e.board)
	e.board.resolving.Wait()
	c := g.calls()
	if len(c) != 2 || len(c[0]) != maxIssueRepos {
		t.Fatalf("setup: %d calls", len(c))
	}
	var left []model.ForgeRepo
	for _, r := range c[1] {
		if !slices.Contains(c[0], r) {
			left = append(left, r)
		}
	}
	if len(left) != 2 {
		t.Fatalf("the second look asked %d of the two left out of the first", len(left))
	}
}

// Ruling 3, at the board: glab answering for another account is a new
// login for the whole host name: the rows of every site of it go at once,
// its sites are checked again, and the name is not held as after a refusal.
func TestAChangedLoginDropsWhatTheOldOneListedOnEverySite(t *testing.T) {
	e, _, gl, repoA, repoB := twoPortsEnv(t)
	gl.mu.Lock()
	gl.err[glHost] = fmt.Errorf("glab: %w", source.ErrLoginChanged)
	gl.mu.Unlock()
	mr := model.PRRef{Repo: repoB, Number: 1}
	e.board.mu.Lock()
	e.board.prs[mr] = model.PullRequest{} // a merge request the old login read
	e.board.mu.Unlock()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor + issuesEvery))
	startLook(e.board)
	e.board.resolving.Wait()
	if listedFor(e.board, repoA) || listedFor(e.board, repoB) {
		t.Fatalf("a changed login left rows: %v %v", listedFor(e.board, repoA), listedFor(e.board, repoB))
	}
	e.board.mu.Lock()
	_, held := e.board.issuesHold[model.GitLabHostname(glHost)]
	atB := e.board.glHosts[glPort].at
	_, mrKept := e.board.prs[mr]
	e.board.mu.Unlock()
	if held || !atB.IsZero() || mrKept {
		t.Fatalf("held %v, the other site's check due %v, its merge request kept %v", held, atB.IsZero(), mrKept)
	}
}

// Ruling 8: a merge request lookup's null project on GitLab is not shown to
// come from the project's own instance (the issue lister checks the route,
// a lookup does not): the issue rows stay, and the repository is due for a
// re-read at once.
func TestAGitLabLookupsNullProjectMakesTheRowsDueNotGone(t *testing.T) {
	fi := &fakeIssues{by: map[model.ForgeRepo]model.RepoIssues{}}
	e, glab := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitLab: fi}
	})
	repo := model.GitLabRepo(glHost, glPath)
	fi.by[repo] = sampleIssues(repo, 4, 1)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: repo, Branch: model.Of("main")})
	e.board.applySync(busySession("a", tNow))
	e.board.tick() // the host check runs
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick() // the first look
	if !listedFor(e.board, repo) {
		t.Fatal("setup: not listed")
	}
	glab.mu.Lock()
	glab.err = fmt.Errorf("gitlab: %w", source.ErrRepoNotFound)
	glab.mu.Unlock()
	s := busySession("mr", tNow)
	s.CreatedPRs = []model.PRRef{{Repo: repo, Number: 1}}
	e.board.applySync(s)
	before := glab.lookupsOf(repo)
	e.board.refreshForges(context.Background(), false)
	if glab.lookupsOf(repo) == before {
		t.Fatal("setup: the merge request was not looked up")
	}
	if !listedFor(e.board, repo) {
		t.Fatal("a lookup's unverified null project purged the issue rows")
	}
	calls := fi.callCount()
	glab.mu.Lock()
	glab.err = nil // the tick's own lookup moves no generation under the look
	glab.mu.Unlock()
	e.clock.Set(tNow.Add(2 * contract.IssuesFreshFor))
	e.board.tick()
	e.board.resolving.Wait()
	if fi.callCount() == calls {
		t.Fatal("the repository was not read again after the lookup's null")
	}
}

// denyOne is fakePRs with one repository this login does not see: every
// lookup of it is ErrRepoNotFound.
type denyOne struct {
	*fakePRs
	bad    model.ForgeRepo
	denied *atomic.Int32
}

func (d denyOne) Lookup(ctx context.Context, repo model.ForgeRepo, numbers []int, branches []string) ([]model.PullRequest, error) {
	if repo == d.bad {
		d.denied.Add(1)
		return nil, fmt.Errorf("github: %w", source.ErrRepoNotFound)
	}
	return d.fakePRs.Lookup(ctx, repo, numbers, branches)
}

// Review round 5, P2: a repository denied on every refresh loses only its
// own answer; the healthy repositories of the same batch load and keep
// their rows look after look.
func TestOneDeniedRepositoryDoesNotVoidTheOthersInItsBatch(t *testing.T) {
	g := &gatedIssues{title: "t", open: 1}
	e := newEnv(t)
	bad := model.GitHubRepo("sample-org/r-denied")
	d := denyOne{fakePRs: e.prs, bad: bad, denied: &atomic.Int32{}}
	e.board = newBoard(t, Options{
		Repos: e.repos, PRs: d, State: e.state, Now: e.clock.Now, StaleAfter: 20 * time.Minute,
		Issues: map[model.Forge]source.IssueLister{model.ForgeGitHub: g},
	})
	var good []model.ForgeRepo
	for i := range 5 {
		folder := fmt.Sprintf("%s/projects/p%d", testHome, i)
		r := model.GitHubRepo(fmt.Sprintf("sample-org/r%d", i))
		if i == 4 {
			r = bad
		} else {
			good = append(good, r)
		}
		e.repos.set(folder, model.Repo{Root: folder, MainRoot: folder, Remote: r, Branch: model.Of("main")})
		s := busySession(fmt.Sprintf("p%d", i), tNow)
		s.Folder = folder
		if r == bad {
			s.CreatedPRs = []model.PRRef{{Repo: bad, Number: 1}}
		}
		e.board.applySync(s)
	}
	for look := range 3 {
		gate := openOnEnd(t)
		g.set(func(g *gatedIssues) { g.gate = gate.ch })
		calls := len(g.calls())
		e.clock.Set(tNow.Add(time.Duration(look+1) * issuesEvery))
		startLook(e.board)
		waitFor(t, e.board, "the look asked", func() bool { return len(g.calls()) > calls })
		denied := d.denied.Load()
		e.board.refreshForges(context.Background(), true) // the denied one, again
		if d.denied.Load() == denied {
			t.Fatalf("look %d: setup: the denied repository was not looked up", look)
		}
		gate.open()
		e.board.resolving.Wait()
		for _, r := range good {
			if !listedFor(e.board, r) {
				t.Fatalf("look %d: %v did not load next to a denied repository", look, r)
			}
		}
		if listedFor(e.board, bad) {
			t.Fatalf("look %d: the denied repository's answer was applied", look)
		}
		e.board.mu.Lock()
		_, tried := e.board.issuesTried[e.board.issueKey(bad)]
		e.board.mu.Unlock()
		if tried {
			t.Fatalf("look %d: the denied repository counts as asked", look)
		}
	}
}

// Review round 5, P3: the server serves the issue identity the page
// matches by (Project.IssuesKey, RepoIssues.Key): two spellings of one
// repository get one key, the same as the list's row.
func TestProjectsAndIssueRowsCarryTheServersKey(t *testing.T) {
	g := &gatedIssues{title: "t", open: 1}
	e := gatedEnv(t, g)
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Remote: model.GitHubRepo(strings.ToUpper(exRepo)), Branch: model.Of("main")})
	b := busySession("b", tNow)
	b.Folder = beta
	e.board.applySync(b)
	e.board.tick()
	l, err := e.board.Issues(context.Background())
	if err != nil || len(l.Repos) != 1 || l.Repos[0].Key == "" {
		t.Fatalf("list %+v, %v", l.Repos, err)
	}
	keys := map[string]bool{}
	for _, p := range e.board.Snapshot().Projects {
		if p.Repo != "" {
			keys[p.IssuesKey] = true
		}
	}
	if len(keys) != 1 || !keys[l.Repos[0].Key] {
		t.Fatalf("project keys %v, the row's %q", keys, l.Repos[0].Key)
	}
}
