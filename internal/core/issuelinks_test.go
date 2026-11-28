package core

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/IZZY-Agency/sitboard/internal/state"
	"slices"
	"strings"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// looker is a live session in folder whose own issue commands named the
// touches.
func looker(id, folder string, touches ...model.IssueTouch) model.SessionFact {
	s := busySession(id, tNow)
	s.Folder = folder
	s.TouchedIssues = touches
	return s
}

func ghTouch(folder string, n int) model.IssueTouch {
	return model.IssueTouch{Forge: model.ForgeGitHub, Folder: folder, Number: n, At: tNow}
}

func cardIDOf(t *testing.T, id string) string {
	t.Helper()
	c, err := contract.CardID(string(claudeKey(id).Tool), id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// linkedEnv is newIssuesEnv after the first look: alpha lists 48 and 45, beta
// lists 7.
func linkedEnv(t *testing.T) *testEnv {
	t.Helper()
	e, _ := newIssuesEnv(t)
	e.board.tick()
	return e
}

func agentsOn(t *testing.T, b *Board, repo string, n int) []contract.IssueAgent {
	t.Helper()
	l, err := b.Issues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range l.Repos {
		if r.Repo != repo {
			continue
		}
		for _, is := range r.Issues {
			if is.Number == n {
				if is.Agents == nil {
					t.Fatalf("issue %d has null agents", n)
				}
				return is.Agents
			}
		}
	}
	t.Fatalf("issue %d of %s is not listed", n, repo)
	return nil
}

func issuesOfCard(t *testing.T, b *Board, id string) []contract.Issue {
	t.Helper()
	c, ok := cardByID(t, b, cardIDOf(t, id))
	if !ok {
		t.Fatalf("no card %s", id)
	}
	return c.Issues
}

// noAgentOf is the summary's no-agent-seen count, -1 when it is absent.
func noAgentOf(b *Board) int {
	if n := b.Snapshot().Issues.NoAgent; n != nil {
		return *n
	}
	return -1
}

func TestALookedAtIssueLinksTheCardAndCountsAsSeen(t *testing.T) {
	e := linkedEnv(t)
	if n := noAgentOf(e.board); n != 3 {
		t.Fatalf("no agent seen %d, want the 3 listed rows", n)
	}
	e.board.applySync(looker("c", alpha, ghTouch(alpha, 48)))

	is := issuesOfCard(t, e.board, "c")
	want := contract.Issue{Forge: contract.ForgeGitHub, Host: "github.com", Repo: exRepo, Number: 48,
		URL: "https://github.com/example-org/example-repo/issues/48", Kind: contract.IssueLooked}
	if len(is) != 1 || is[0] != want {
		t.Fatalf("card issues %+v, want %+v", is, want)
	}
	if got := agentsOn(t, e.board, exRepo, 48); !slices.Equal(got, []contract.IssueAgent{{CardID: cardIDOf(t, "c"), Kind: contract.IssueLooked}}) {
		t.Fatalf("agents on 48: %+v", got)
	}
	if got := agentsOn(t, e.board, exRepo, 45); len(got) != 0 {
		t.Fatalf("agents on 45: %+v", got)
	}
	if n := noAgentOf(e.board); n != 2 {
		t.Fatalf("no agent seen %d, want 2", n)
	}
	if a, b := projectNamed(t, e.board, "alpha"), projectNamed(t, e.board, "beta"); a.IssuesNoAgent == nil || *a.IssuesNoAgent != 1 || b.IssuesNoAgent == nil || *b.IssuesNoAgent != 1 {
		t.Fatalf("per project %v, %v, want 1 and 1", a.IssuesNoAgent, b.IssuesNoAgent)
	}
}

func TestAProjectWhoseListIsNotKnownHasNoNoAgentCount(t *testing.T) {
	e, gh := newIssuesEnv(t)
	delete(gh.by, model.GitHubRepo(betaRepo)) // beta is not answered for
	e.board.tick()
	if b := projectNamed(t, e.board, "beta"); b.OpenIssues.Known || b.IssuesNoAgent != nil {
		t.Fatalf("beta %+v, %v", b.OpenIssues, b.IssuesNoAgent)
	}
	if a := projectNamed(t, e.board, "alpha"); a.IssuesNoAgent == nil || *a.IssuesNoAgent != 2 {
		t.Fatalf("alpha %v, want 2", a.IssuesNoAgent)
	}
}

func TestACoordinatorGivesNoLookedAt(t *testing.T) {
	e := linkedEnv(t)
	s := looker("c", alpha, ghTouch(alpha, 48))
	s.SpawnedNames = []string{"worker"}
	e.board.applySync(s)
	if is := issuesOfCard(t, e.board, "c"); len(is) != 0 {
		t.Fatalf("coordinator's card issues %+v", is)
	}
	if got := agentsOn(t, e.board, exRepo, 48); len(got) != 0 {
		t.Fatalf("agents on 48: %+v", got)
	}
	if n := noAgentOf(e.board); n != 3 {
		t.Fatalf("no agent seen %d, want 3", n)
	}
}

func TestAnEndedSessionGivesNoLookedAt(t *testing.T) {
	old := keepEnded
	keepEnded = true // an ended session with an open pull request keeps its card
	t.Cleanup(func() { keepEnded = old })
	e := linkedEnv(t)
	e.prs.prs = []model.PullRequest{{Repo: model.GitHubRepo(exRepo), Number: 101,
		URL: "https://github.com/example-org/example-repo/pull/101", State: model.PRStateOpen, ClosingIssues: []int{45}}}
	s := looker("c", alpha, ghTouch(alpha, 48))
	s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 101}}
	s.Ended, s.EndedAt = true, tNow
	e.board.applySync(s)
	e.board.tick()
	if _, ok := cardByID(t, e.board, cardIDOf(t, "c")); !ok {
		t.Fatal("the ended session has no card, so the test proves nothing")
	}
	if got := agentsOn(t, e.board, exRepo, 45); len(got) != 1 {
		t.Fatalf("agents on 45: %+v", got) // its pull request still closes it
	}
	if got := agentsOn(t, e.board, exRepo, 48); len(got) != 0 {
		t.Fatalf("agents on 48: %+v", got)
	}
}

func TestAForkWithItsUpstreamIsUnknownForAFolderTouch(t *testing.T) {
	e := linkedEnv(t)
	fork := alpha + "-fork"
	e.repos.set(fork, model.Repo{Root: fork, MainRoot: fork, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main"),
		Remotes: []model.RemoteAddr{{Repo: model.GitHubRepo("other-org/app")}}})
	e.board.applySync(looker("c", fork, ghTouch(fork, 48)))
	if got := agentsOn(t, e.board, exRepo, 48); len(got) != 0 {
		t.Fatalf("a folder touch in a fork with an upstream linked: %+v", got)
	}
	// The same command with -R names its repository: it links.
	named := model.IssueTouch{Forge: model.ForgeGitHub, Repo: model.GitHubRepo(exRepo), Number: 48, At: tNow}
	e.board.applySync(looker("c", fork, named))
	if got := agentsOn(t, e.board, exRepo, 48); len(got) != 1 {
		t.Fatalf("agents on 48 after -R: %+v", got)
	}
}

func TestATouchLinksOnlyThroughItsOwnForge(t *testing.T) {
	e := linkedEnv(t)
	// glab in a folder whose repository is on GitHub; a glab touch that names
	// a GitHub repository; a gh touch that names a GitLab one.
	e.board.applySync(looker("c", alpha,
		model.IssueTouch{Forge: model.ForgeGitLab, Folder: alpha, Number: 48, At: tNow},
		model.IssueTouch{Forge: model.ForgeGitLab, Repo: model.GitHubRepo(exRepo), Number: 48, At: tNow},
		model.IssueTouch{Forge: model.ForgeGitHub, Repo: model.GitLabRepo(glHost, glPath), Number: 48, At: tNow}))
	if got := agentsOn(t, e.board, exRepo, 48); len(got) != 0 {
		t.Fatalf("agents on 48: %+v", got)
	}
	if is := issuesOfCard(t, e.board, "c"); len(is) != 0 {
		t.Fatalf("card issues %+v", is)
	}
}

func TestATouchLinksOnlyAnIssueListedForARepositoryOnTheBoard(t *testing.T) {
	e := linkedEnv(t)
	e.board.applySync(looker("c", alpha,
		ghTouch(alpha, 99), // not among the listed rows
		model.IssueTouch{Forge: model.ForgeGitHub, Repo: model.GitHubRepo("example-org/elsewhere"), Number: 48, At: tNow})) // not on the board
	if is := issuesOfCard(t, e.board, "c"); len(is) != 0 {
		t.Fatalf("card issues %+v", is)
	}
	if n := noAgentOf(e.board); n != 3 {
		t.Fatalf("no agent seen %d, want 3", n)
	}
}

func TestClosesWinsOverLooked(t *testing.T) {
	e := linkedEnv(t)
	e.prs.prs = []model.PullRequest{{Repo: model.GitHubRepo(exRepo), Number: 101,
		URL: "https://github.com/example-org/example-repo/pull/101", State: model.PRStateOpen, ClosingIssues: []int{48}}}
	s := looker("c", alpha, ghTouch(alpha, 48), ghTouch(alpha, 45))
	s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 101}}
	e.board.applySync(s)
	e.board.tick()

	c := cardIDOf(t, "c")
	if got := agentsOn(t, e.board, exRepo, 48); !slices.Equal(got, []contract.IssueAgent{{CardID: c, Kind: contract.IssueCloses}}) {
		t.Fatalf("agents on 48: %+v", got)
	}
	if got := agentsOn(t, e.board, exRepo, 45); !slices.Equal(got, []contract.IssueAgent{{CardID: c, Kind: contract.IssueLooked}}) {
		t.Fatalf("agents on 45: %+v", got)
	}
	var kinds []contract.IssueKind
	for _, is := range issuesOfCard(t, e.board, "c") {
		kinds = append(kinds, is.Kind)
	}
	if !slices.Equal(kinds, []contract.IssueKind{contract.IssueCloses, contract.IssueLooked}) {
		t.Fatalf("card issue kinds %v", kinds)
	}
}

func TestAChangeOfLinksBumpsTheIssuesRev(t *testing.T) {
	e := linkedEnv(t)
	rev := e.board.Snapshot().Issues.Rev
	e.board.applySync(looker("c", alpha, ghTouch(alpha, 48)))
	linked := e.board.Snapshot().Issues.Rev
	if linked == rev {
		t.Fatalf("rev stayed %d after a link", rev)
	}
	if l, _ := e.board.Issues(context.Background()); l.Rev != linked {
		t.Fatalf("the list is of rev %d, the summary says %d", l.Rev, linked)
	}
	e.board.applySync(looker("c", alpha, ghTouch(alpha, 48)))
	if got := e.board.Snapshot().Issues.Rev; got != linked {
		t.Fatalf("rev moved to %d with nothing changed", got)
	}
	e.board.applySync(looker("c", alpha)) // the touch is gone
	if got := e.board.Snapshot().Issues.Rev; got == linked {
		t.Fatalf("rev stayed %d after a link went", got)
	}
	if got := agentsOn(t, e.board, exRepo, 48); len(got) != 0 {
		t.Fatalf("agents on 48: %+v", got)
	}
}

func TestGitLabLooksAtAnIssueUnderAMount(t *testing.T) {
	gl := &fakeIssues{by: map[model.ForgeRepo]model.RepoIssues{}}
	e, fg := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitLab: gl}
	})
	fg.subs = map[string]string{glHost: "gitlab"}
	repo := model.GitLabRepo(glHost, "gitlab/"+glPath)
	gl.by[repo] = sampleIssues(repo, 4, 1, 2)
	// From an ssh address, whose path has no mount: the core adds the host's.
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitLabRepo(glHost, glPath), RemoteBare: true, Branch: model.Of("main")})
	e.board.applySync(busySession("a", tNow))
	e.board.tick()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick()
	if l, _ := e.board.Issues(context.Background()); len(l.Repos) != 1 || len(l.Repos[0].Issues) != 2 {
		t.Fatalf("list %+v", l.Repos)
	}

	// An address names the mount in its path, in the host's own case; a folder
	// touch is the folder's repository.
	byURL := model.IssueTouch{Forge: model.ForgeGitLab, Repo: model.GitLabRepo(glHost, "gitlab/Example-Group/example-project"), Number: 1, At: tNow}
	byFolder := model.IssueTouch{Forge: model.ForgeGitLab, Folder: alpha, Number: 2, At: tNow}
	e.board.applySync(looker("c", alpha, byURL, byFolder))
	for n, want := range map[int]int{1: 1, 2: 1} {
		if got := agentsOn(t, e.board, "gitlab/"+glPath, n); len(got) != want {
			t.Fatalf("agents on %d: %+v", n, got)
		}
	}
	if n := noAgentOf(e.board); n != 0 {
		t.Fatalf("no agent seen %d, want 0", n)
	}
	is := issuesOfCard(t, e.board, "c")
	if len(is) != 2 || is[0].URL != "https://gitlab.example.com/gitlab/example-group/example-project/-/issues/1" {
		t.Fatalf("card issues %+v", is)
	}
}

// gitLabMountEnv is a GitLab board with a host under the mount "tools" and one
// issue, 1, listed for the project; the project's folder alpha is as origin
// (bare ssh, so the core adds the mount) with the given other remotes.
func gitLabMountEnv(t *testing.T, repo model.Repo) *testEnv {
	t.Helper()
	gl := &fakeIssues{by: map[model.ForgeRepo]model.RepoIssues{}}
	e, fg := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitLab: gl}
	})
	fg.subs = map[string]string{glHost: "tools"}
	gl.by[model.GitLabRepo(glHost, "tools/"+glPath)] = sampleIssues(model.GitLabRepo(glHost, "tools/"+glPath), 1, 1)
	repo.Root, repo.MainRoot, repo.Branch = alpha, alpha, model.Of("main")
	repo.Remote, repo.RemoteBare = model.GitLabRepo(glHost, glPath), true
	e.repos.set(alpha, repo)
	e.board.applySync(busySession("a", tNow))
	e.board.tick()
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick()
	return e
}

func looked(t *testing.T, e *testEnv) int {
	t.Helper()
	e.board.applySync(looker("c", alpha, model.IssueTouch{Forge: model.ForgeGitLab, Folder: alpha, Number: 1, At: tNow}))
	return len(agentsOn(t, e.board, "tools/"+glPath, 1))
}

// Every fetch address of every other remote counts, by the core's own
// identity: the mount exactly, the path under it without case.
func TestAFolderTouchComparesEveryRemoteByTheMountAwareIdentity(t *testing.T) {
	cases := []struct {
		name    string
		remotes []model.RemoteAddr
		unclear bool
		want    int
		gh      bool // remote.*.gh-resolved is set: gh's own default, none of glab's
	}{
		{"no other remote", nil, false, 1, false},
		{"the same project by https, path case differs under the mount", []model.RemoteAddr{
			{Repo: model.GitLabRepo(glHost, "tools/Example-Group/Example-Project")}}, false, 1, false},
		{"the same project by bare ssh", []model.RemoteAddr{{Repo: model.GitLabRepo(glHost, glPath), Bare: true}}, false, 1, false},
		{"the mount differs in case", []model.RemoteAddr{{Repo: model.GitLabRepo(glHost, "Tools/"+glPath)}}, false, 0, false},
		{"a bare ssh address of another project", []model.RemoteAddr{{Repo: model.GitLabRepo(glHost, "example-group/other"), Bare: true}}, false, 0, false},
		{"a GitLab host the core may not use", []model.RemoteAddr{{Repo: model.GitLabRepo("gitlab.elsewhere.example", glPath)}}, false, 0, false},
		{"a remote on no forge", []model.RemoteAddr{{}}, false, 1, false},
		{"remotes that could not all be told", nil, true, 0, false},
		{"a gh default repository does not concern glab", nil, false, 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := gitLabMountEnv(t, model.Repo{Remotes: c.remotes, RemotesUnclear: c.unclear, GHResolved: c.gh})
			if got := looked(t, e); got != c.want {
				t.Fatalf("agents on the issue: %d, want %d", got, c.want)
			}
		})
	}
}

// A command run in a folder under the session's own (Codex's tool working
// directory) is looked up as that folder, never as its parent.
func TestATouchFromASubfolderResolvesItsOwnFolder(t *testing.T) {
	e := linkedEnv(t)
	web := alpha + "/web"
	e.repos.set(web, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	e.board.applySync(looker("c", alpha, ghTouch(web, 48)))
	if got := agentsOn(t, e.board, exRepo, 48); len(got) != 1 {
		t.Fatalf("a touch from %s did not link: %+v", web, got)
	}
}

func TestATouchFromANestedCheckoutIsThatCheckoutsRepository(t *testing.T) {
	e := linkedEnv(t)
	nested := alpha + "/vendor/other"
	e.repos.set(nested, model.Repo{Root: nested, MainRoot: nested, Remote: model.GitHubRepo(betaRepo), Branch: model.Of("main")})
	e.board.applySync(looker("c", alpha, ghTouch(nested, 7)))
	if got := agentsOn(t, e.board, betaRepo, 7); len(got) != 1 {
		t.Fatalf("the nested checkout's issue 7: %+v", got)
	}
	// The parent's repository did not stand in for it.
	e.board.applySync(looker("c", alpha, ghTouch(nested, 48)))
	if got := agentsOn(t, e.board, exRepo, 48); len(got) != 0 {
		t.Fatalf("issue 48 of the parent linked from the nested checkout: %+v", got)
	}
}

func TestTouchFoldersAreCappedPerBoard(t *testing.T) {
	e := linkedEnv(t)
	var touches []model.IssueTouch
	for i := range maxTouchFolders + 1 {
		f := fmt.Sprintf("%s/sub%02d", alpha, i)
		e.repos.set(f, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
		touches = append(touches, ghTouch(f, 48))
	}
	e.board.applySync(looker("c", alpha, touches[:maxTouchFolders]...))
	if got := agentsOn(t, e.board, exRepo, 48); len(got) != 1 {
		t.Fatalf("a touch within the cap did not link: %+v", got)
	}
	// The one past the cap is never looked up, so its issue stays unseen.
	e.board.applySync(looker("c", alpha, touches[maxTouchFolders]))
	if got := agentsOn(t, e.board, exRepo, 48); len(got) != 0 {
		t.Fatalf("a touch past the cap linked: %+v", got)
	}
	if n := len(e.board.touchFolders); n != maxTouchFolders {
		t.Fatalf("%d touch folders looked up, want %d", n, maxTouchFolders)
	}
}

// An unknown count is not a known zero: absent before any list is known and
// when issues are off, 0 only when a list is known and every row has an agent.
func TestTheSummaryNoAgentCountIsAbsentUntilKnown(t *testing.T) {
	e, _ := newIssuesEnv(t)
	if n := noAgentOf(e.board); n != -1 {
		t.Fatalf("before the first look: %d, want absent", n)
	}
	e.board.tick()
	if n := noAgentOf(e.board); n != 3 {
		t.Fatalf("after the look: %d, want 3", n)
	}
	// Every listed row has an agent: a known zero is present.
	e.board.applySync(looker("c", alpha, ghTouch(alpha, 48), ghTouch(alpha, 45)))
	e.board.applySync(looker("d", beta, ghTouch(beta, 7)))
	if n := noAgentOf(e.board); n != 0 {
		t.Fatalf("with every row seen: %d, want a known 0", n)
	}
	raw, err := json.Marshal(e.board.Snapshot().Issues)
	if err != nil || !strings.Contains(string(raw), `"noAgent":0`) {
		t.Fatalf("a known zero is dropped from the JSON: %s %v", raw, err)
	}
	// Issues off: absent again, though counts were known (the off answer
	// returns before any count is set; this holds that).
	e.board.SetSources([]source.Status{{Kind: source.KindGitHub, State: source.StateOff, Detail: "switched off"}})
	e.board.tick()
	if n := noAgentOf(e.board); n != -1 {
		t.Fatalf("with issues off: %d, want absent", n)
	}
}

// closing makes session id's own pull request, numbered pr, close issue n.
func closing(e *testEnv, id string, pr, n int) {
	e.prs.prs = append(e.prs.prs, model.PullRequest{Repo: model.GitHubRepo(exRepo), Number: pr,
		URL: fmt.Sprintf("https://github.com/example-org/example-repo/pull/%d", pr), State: model.PRStateOpen, ClosingIssues: []int{n}})
	s := looker(id, alpha)
	s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: pr}}
	e.board.applySync(s)
	e.board.tick()
}

// Rev moves with the links of the rows the endpoint serves: a pull request
// that closes an issue outside the listed rows (and not pinned) is on its
// card only; one that closes a listed or a pinned one moves it.
func TestRevMovesOnlyWithLinksOfServedRows(t *testing.T) {
	e := linkedEnv(t)
	rev := e.board.Snapshot().Issues.Rev
	closing(e, "c", 101, 99) // not listed, not pinned
	if got := e.board.Snapshot().Issues.Rev; got != rev {
		t.Fatalf("rev moved %d -> %d for a closure of an issue no row is served for", rev, got)
	}
	closing(e, "d", 102, 48) // listed
	listedRev := e.board.Snapshot().Issues.Rev
	if listedRev == rev {
		t.Fatal("rev did not move for a link on a listed row")
	}
	if err := e.board.SetIssuePin(model.GitHubRepo(exRepo), 99, true); err != nil {
		t.Fatal(err)
	}
	pinnedRev := e.board.Snapshot().Issues.Rev
	closing(e, "f", 103, 99) // pinned now: the row outside the list carries the agent
	if got := e.board.Snapshot().Issues.Rev; got == pinnedRev {
		t.Fatal("rev did not move for a link on a pinned row")
	}
}

func (f *fakeRepos) touched() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.touch)
}

// A touch folder is asked through ResolveTouch (no os.Stat of its own in the
// shared resolver slots), never through Resolve.
func TestATouchFolderIsAskedWithoutAFilesystemProbe(t *testing.T) {
	e := linkedEnv(t)
	web := alpha + "/web"
	e.repos.set(web, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	e.board.applySync(looker("c", alpha, ghTouch(web, 48)))
	if !slices.Contains(e.repos.touched(), web) {
		t.Fatalf("the touch folder was not asked through ResolveTouch: %v", e.repos.touched())
	}
	e.repos.mu.Lock()
	defer e.repos.mu.Unlock()
	if slices.Contains(e.repos.asked, web) {
		t.Fatalf("the touch folder was asked through Resolve: %v", e.repos.asked)
	}
}

// A touch folder is asked again with the session folders: a failed answer is
// unknown and retried; a changed remote or a nested checkout made there later
// is credited by the current identity (an accepted limit: a touch is not
// bound to the identity at its time).
func TestATouchFolderIsAskedAgainOnTheSchedule(t *testing.T) {
	e := linkedEnv(t)
	web := alpha + "/web"
	repo := model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")}
	e.repos.set(web, repo)
	e.repos.mu.Lock()
	e.repos.fail = map[string]bool{web: true}
	e.repos.mu.Unlock()
	e.board.applySync(looker("c", alpha, ghTouch(web, 48)))
	if got := agentsOn(t, e.board, exRepo, 48); len(got) != 0 {
		t.Fatalf("a failed lookup linked: %+v", got)
	}
	e.repos.mu.Lock()
	e.repos.fail = nil
	e.repos.mu.Unlock()
	e.clock.Set(e.clock.Now().Add(githubEvery))
	e.board.tick()
	e.board.resolving.Wait()
	if got := agentsOn(t, e.board, exRepo, 48); len(got) != 1 {
		t.Fatalf("the folder was not asked again: %+v", got)
	}
	// A nested checkout of another repository appears there: the touch is
	// credited by that identity now.
	e.repos.set(web, model.Repo{Root: web, MainRoot: web, Remote: model.GitHubRepo(betaRepo), Branch: model.Of("main")})
	e.clock.Set(e.clock.Now().Add(githubEvery))
	e.board.tick()
	e.board.resolving.Wait()
	if got := agentsOn(t, e.board, exRepo, 48); len(got) != 0 {
		t.Fatalf("the old identity still holds: %+v", got)
	}
}

// gh reads a default repository from remote.*.gh-resolved before the remotes:
// a gh touch with no repository in such a checkout is unknown; -R is not.
func TestAGhTouchInACheckoutWithADefaultRepositoryIsUnknown(t *testing.T) {
	e := linkedEnv(t)
	set := alpha + "-set"
	e.repos.set(set, model.Repo{Root: set, MainRoot: set, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main"), GHResolved: true})
	e.board.applySync(looker("c", set, ghTouch(set, 48)))
	if got := agentsOn(t, e.board, exRepo, 48); len(got) != 0 {
		t.Fatalf("a folder touch with gh-resolved linked: %+v", got)
	}
	named := model.IssueTouch{Forge: model.ForgeGitHub, Repo: model.GitHubRepo(exRepo), Number: 48, At: tNow}
	e.board.applySync(looker("c", set, named))
	if got := agentsOn(t, e.board, exRepo, 48); len(got) != 1 {
		t.Fatalf("-R after gh-resolved: %+v", got)
	}
}

// The pin of a repository that left the board is not served, so a link on it
// does not move Rev.
func TestRevIgnoresAPinOfARepositoryOffTheBoard(t *testing.T) {
	e := linkedEnv(t)
	gone := model.GitHubRepo("example-org/gone")
	if err := e.state.SetPin(state.PinKey("github", "github.com", "example-org/gone", 5), true, tNow); err != nil {
		t.Fatal(err)
	}
	e.board.tick()
	rev := e.board.Snapshot().Issues.Rev
	e.prs.prs = append(e.prs.prs, model.PullRequest{Repo: gone, Number: 7, URL: "https://github.com/example-org/gone/pull/7",
		State: model.PRStateOpen, ClosingIssues: []int{5}})
	s := looker("c", alpha)
	s.CreatedPRs = []model.PRRef{{Repo: gone, Number: 7}}
	e.board.applySync(s)
	e.board.tick()
	if got := e.board.Snapshot().Issues.Rev; got != rev {
		t.Fatalf("rev moved %d -> %d for a pin of a repository the endpoint serves no row for", rev, got)
	}
}
