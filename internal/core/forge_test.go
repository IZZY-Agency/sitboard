package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

// shiftTimes moves every timestamp in a decoded state file by d, so a fixture
// written at a fixed time is read as if it had been written relative to now.
func shiftTimes(v any, d time.Duration) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = shiftTimes(e, d)
		}
		return x
	case string:
		if at, err := time.Parse(time.RFC3339, x); err == nil {
			return at.Add(d).UTC().Format(time.RFC3339)
		}
	}
	return v
}

// stateBeforeForges copies testdata/state-before-forges.json (a state file in
// the shape the build before forges wrote, fixed catch-up keys included) into
// a temp dir with its times moved to be relative to anchor. state.Open prunes
// by the real clock, so the times follow it; the keys do not move.
func stateBeforeForges(t *testing.T, anchor time.Time) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "state-before-forges.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	out, err := json.Marshal(shiftTimes(doc, anchor.Sub(base)))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(file, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// A state file from before forges holds read marks that are hashes of
// "pr_opened|owner/name#n", the path as written. Opened from the file as the
// old build wrote it, a GitHub pull request must still meet its mark: the
// item saved as unread stays unread, and after a write and a reopen the
// marker, every hash (byte for byte) and the other settings are all still
// there. Times are relative to a clock the store and the board share.
func TestCatchupSavedGitHubMarkStillMatches(t *testing.T) {
	const (
		mixed     = "Example-Org/Example-Repo"
		openedKey = "8db35d92f07f0172fcbe5e36d8d90f0f" // pr_opened|Example-Org/Example-Repo#9
		otherKey  = "82f467d2c4e984433654e0a37f22a5a1" // an item this board never meets
	)
	if state.CatchupKey("pr_opened|"+mixed+"#9") != openedKey {
		t.Fatal("the fixture's key is not the one the identity hashes to")
	}
	anchor := time.Now().UTC().Truncate(time.Second)
	file := stateBeforeForges(t, anchor)
	st, err := state.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	e.state = st
	e.clock.Set(anchor.Add(time.Minute))
	b := e.restart(t)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(mixed), Branch: model.Of("main")})
	opened := anchor.Add(-3 * time.Hour)
	pr := prAt(9, model.PRStateOpen, opened, time.Time{})
	pr.Repo = model.GitHubRepo(mixed)
	e.prs.prs = []model.PullRequest{pr}
	w := session("w", model.StatusFinished, anchor.Add(-2*time.Hour))
	w.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(mixed), Number: 9}}
	b.applySync(w)
	b.tick()
	got := catchup(t, b)
	if len(got.Items) != 1 || got.Items[0].Kind != contract.CatchupPROpened {
		t.Fatalf("saved as unread before forges, now %v", kindsAt(got))
	}
	if p := got.Items[0].PullRequest; p == nil || p.Forge != contract.ForgeGitHub || p.Host != model.GitHubHost {
		t.Fatalf("catch-up pull request %+v", got.Items[0].PullRequest)
	}

	// A write, then a reopen of the file this build wrote.
	if err := st.SetSeen("claude:99999999-8888-7777-6666-555555555555", anchor); err != nil {
		t.Fatal(err)
	}
	back, err := state.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	wantMarker := anchor.Add(-time.Hour)
	if !back.CatchupMarker().Equal(wantMarker) {
		t.Errorf("marker %v, want %v", back.CatchupMarker(), wantMarker)
	}
	unread := back.CatchupUnread()
	if len(unread) != 2 || !unread[openedKey].Equal(anchor.Add(-3*time.Hour)) || !unread[otherKey].Equal(anchor.Add(-4*time.Hour)) {
		t.Errorf("unread marks %v: want both fixture hashes, unchanged", unread)
	}
	if _, ok := back.SeenAt("claude:11111111-2222-3333-4444-555555555555"); !ok {
		t.Error("a seen mark is lost")
	}
	if back.Grouping()["/home/dev/side"] != state.GroupingNever {
		t.Errorf("grouping %v", back.Grouping())
	}
	if _, ok := back.Umbrellas()["/home/dev/work"]; !ok {
		t.Error("an umbrella is lost")
	}
	// And the reopened file still meets the pull request.
	e.state = back
	b2 := e.restart(t)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(mixed), Branch: model.Of("main")})
	b2.applySync(w)
	b2.tick()
	if got := catchup(t, b2); len(got.Items) != 1 || got.Items[0].Kind != contract.CatchupPROpened {
		t.Fatalf("after a reopen %v", kindsAt(got))
	}
}

// No provider answers for GitLab yet: a merge request a transcript names is
// kept but never asked of the GitHub provider, and nothing shows.
func TestGitLabRefsAreNeverAskedOfGitHub(t *testing.T) {
	e := newEnv(t)
	gl := model.GitLabRepo("gitlab.example.com", "example-group/example-project")
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	w := session("w", model.StatusFinished, tNow)
	w.CreatedPRs = []model.PRRef{{Repo: gl, Number: 12}, {Repo: model.GitHubRepo(exRepo), Number: 9}}
	e.board.applySync(w)
	e.board.tick()
	e.prs.mu.Lock()
	calls := slices.Clone(e.prs.calls)
	e.prs.mu.Unlock()
	if len(calls) != 1 || calls[0].repo != exRepo {
		t.Fatalf("lookups %+v, want one for the GitHub repo", calls)
	}
	if hosts := e.board.Snapshot().GitLabHosts; hosts == nil || len(hosts) != 0 {
		t.Fatalf("gitlabHosts %#v, want empty", hosts)
	}
}

// A merge request the board knows reaches the card with its forge, host and
// the address built for them, its host is listed, and its closing issues are
// GitLab's.
func TestGitLabMergeRequestOnTheCard(t *testing.T) {
	e, _ := newGitLabEnv(t)
	gl := model.GitLabRepo("gitlab.example.com", "example-group/example-project")
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: gl, Branch: model.Of("main")})
	w := session("w", model.StatusFinished, tNow)
	w.CreatedPRs = []model.PRRef{{Repo: gl, Number: 12}}
	e.board.mu.Lock()
	e.board.prs[model.PRRef{Repo: gl, Number: 12}] = model.PullRequest{
		Repo: gl, Number: 12, Title: "t", URL: model.ChangeRequestURL(gl, 12),
		State: model.PRStateOpen, ClosingIssues: []int{3}, OpenedAt: tNow.Add(-time.Hour),
	}
	e.board.mu.Unlock()
	e.board.applySync(w)
	e.board.tick() // the host check that lets the remote show
	snap := e.board.Snapshot()
	if !slices.Equal(snap.GitLabHosts, []string{"gitlab.example.com"}) {
		t.Fatalf("gitlabHosts %v", snap.GitLabHosts)
	}
	c := onlyCard(t, e.board)
	if len(c.PullRequests) != 1 {
		t.Fatalf("pull requests %+v", c.PullRequests)
	}
	pr := c.PullRequests[0]
	if pr.Forge != contract.ForgeGitLab || pr.Host != gl.Host || pr.Repo != gl.Path || pr.Number != 12 ||
		pr.URL != "https://gitlab.example.com/example-group/example-project/-/merge_requests/12" {
		t.Fatalf("merge request %+v", pr)
	}
	if len(c.Issues) != 1 || c.Issues[0] != (contract.Issue{Forge: contract.ForgeGitLab, Host: gl.Host, Repo: gl.Path, Number: 3,
		URL: "https://gitlab.example.com/example-group/example-project/-/issues/3"}) {
		t.Fatalf("issues %+v", c.Issues)
	}
	var p contract.Project
	for _, x := range snap.Projects {
		if x.ID == c.ProjectID {
			p = x
		}
	}
	if p.Repo != gl.Path || p.RepoForge != contract.ForgeGitLab || p.RepoHost != gl.Host {
		t.Fatalf("project %+v", p)
	}
}

// A link that is not the one built for the merge request's own host, path
// and number never reaches the page.
func TestGitLabLinkMustBeTheBuiltOne(t *testing.T) {
	gl := model.GitLabRepo("gitlab.example.com", "example-group/example-project")
	for _, u := range []string{
		"https://gitlab.example.com/example-group/example-project/-/merge_requests/13",
		"https://gitlab.com/example-group/example-project/-/merge_requests/12",
		"https://github.com/example-group/example-project/pull/12",
	} {
		if got := prURL(model.PullRequest{Repo: gl, Number: 12, URL: u}); got != "" {
			t.Errorf("prURL passed %q", got)
		}
	}
}

// Leftovers has evidence from GitHub only: a GitLab repo is never looked at.
func TestLeftoversSkipGitLabRepos(t *testing.T) {
	e, _ := newGitLabEnv(t)
	gl := model.GitLabRepo("gitlab.example.com", "example-group/example-project")
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: gl, Branch: model.Of("main")})
	e.board.applySync(session("w", model.StatusFinished, tNow))
	b := e.board
	b.mu.Lock()
	repos, _, _ := b.leftoverInputsLocked()
	b.mu.Unlock()
	if len(repos) != 0 {
		t.Fatalf("GitLab repo looked at: %+v", repos)
	}
}

// A session counts as a leftover on GitHub evidence only: one whose created
// change is a GitLab merge request, merged or not, is no leftover, even in a
// GitHub checkout, and its folder stays protected from the worktree list.
func TestLeftoverSessionsNeedGitHubEvidence(t *testing.T) {
	e := newEnv(t)
	gl := model.GitLabRepo("gitlab.example.com", "example-group/example-project")
	gh := model.GitHubRepo(exRepo)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: gh, Branch: model.Of("main")})
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Remote: gh, Branch: model.Of("main")})
	merged := func(r model.ForgeRepo, n int) model.PullRequest {
		return model.PullRequest{Repo: r, Number: n, State: model.PRStateMerged, OpenedAt: tNow.Add(-72 * time.Hour), ClosedAt: tNow.Add(-48 * time.Hour)}
	}
	e.board.mu.Lock()
	e.board.prs[model.PRRef{Repo: gh, Number: 9}] = merged(gh, 9)
	e.board.prs[model.PRRef{Repo: gl, Number: 12}] = merged(gl, 12)
	e.board.mu.Unlock()
	onGH := session("on-github", model.StatusFinished, tNow.Add(-time.Hour))
	onGH.Folder, onGH.CreatedPRs = alpha, []model.PRRef{{Repo: gh, Number: 9}}
	onGL := session("on-gitlab", model.StatusFinished, tNow.Add(-time.Hour))
	onGL.Folder, onGL.CreatedPRs = beta, []model.PRRef{{Repo: gl, Number: 12}}
	mixed := session("mixed", model.StatusFinished, tNow.Add(-time.Hour))
	mixed.Folder, mixed.CreatedPRs = beta, []model.PRRef{{Repo: gh, Number: 9}, {Repo: gl, Number: 12}}
	e.board.applySync(onGH)
	e.board.applySync(onGL)
	e.board.applySync(mixed)
	e.board.tick()
	for _, id := range []string{"on-github", "on-gitlab", "mixed"} {
		if err := e.board.MarkSeen(mustCardID(t, "claude", id)); err != nil {
			t.Fatal(err)
		}
	}
	b := e.board
	b.mu.Lock()
	_, sessions, protected := b.leftoverInputsLocked()
	b.mu.Unlock()
	if len(sessions) != 1 || sessions[0].folder != alpha {
		t.Fatalf("leftover sessions %+v, want only the one that created the GitHub change", sessions)
	}
	if !slices.Contains(protected, beta) || slices.Contains(protected, alpha) {
		t.Fatalf("protected folders %v: the GitLab sessions' folder must stay in, the leftover's out", protected)
	}
}

// What reaches the page stays bounded: a repository name too long for GitHub
// never becomes a project's repo, even when git resolves it.
func TestLongGitHubOriginNeverReachesThePage(t *testing.T) {
	e := newEnv(t)
	long := model.GitHubRepo(strings.Repeat("o", 400) + "/" + strings.Repeat("n", 400))
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: long, Branch: model.Of("main")})
	e.board.applySync(session("w", model.StatusFinished, tNow))
	snap := e.board.Snapshot()
	if len(snap.Projects) != 1 {
		t.Fatalf("projects %+v", snap.Projects)
	}
	if p := snap.Projects[0]; p.Repo != "" || p.RepoForge != contract.ForgeNone || p.RepoHost != "" {
		t.Fatalf("project %+v: a repository GitHub could not have must not reach the page", p)
	}
}

// A path is on the page whole or not at all. One too long to show whole
// (clean's 300 characters) keeps its number, loses its link, and is never cut
// to a prefix that names another repository: not on the project, not on the
// merge request, not on the issues it closes.
func TestLongGitLabPathIsWholeOrAbsent(t *testing.T) {
	const host = "gitlab.example.com"
	pathOf := func(n int, tag string) string { // n characters in segments of at most 200
		k := n/200 + 1
		var segs []string
		for i := 0; i < k; i++ {
			segs = append(segs, strings.Repeat(tag, (n-(k-1))/k+map[bool]int{true: 1}[i < (n-(k-1))%k]))
		}
		return strings.Join(segs, "/")
	}
	for _, tc := range []struct {
		n    int
		show bool
	}{{300, true}, {301, false}, {900, false}} {
		e, _ := newGitLabEnv(t)
		gl := model.GitLabRepo(host, pathOf(tc.n, "a"))
		if !gl.Valid() {
			t.Fatalf("a %d-character path must be a valid GitLab path", tc.n)
		}
		e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: gl, Branch: model.Of("main")})
		w := session("w", model.StatusFinished, tNow)
		w.CreatedPRs = []model.PRRef{{Repo: gl, Number: 12}}
		e.board.mu.Lock()
		e.board.prs[model.PRRef{Repo: gl, Number: 12}] = model.PullRequest{
			Repo: gl, Number: 12, Title: "t", URL: model.ChangeRequestURL(gl, 12),
			State: model.PRStateOpen, ClosingIssues: []int{3}, OpenedAt: tNow.Add(-time.Hour),
		}
		e.board.mu.Unlock()
		e.board.applySync(w)
		e.board.tick()
		snap := e.board.Snapshot()
		c := onlyCard(t, e.board)
		if len(c.PullRequests) != 1 || len(c.Issues) != 1 {
			t.Fatalf("%d: pull requests %+v, issues %+v", tc.n, c.PullRequests, c.Issues)
		}
		pr, is, p := c.PullRequests[0], c.Issues[0], snap.Projects[0]
		if pr.Number != 12 || is.Number != 3 {
			t.Errorf("%d: numbers lost: %+v %+v", tc.n, pr, is)
		}
		if tc.show {
			if pr.Repo != gl.Path || pr.URL != model.ChangeRequestURL(gl, 12) || is.Repo != gl.Path || is.URL != model.ForgeIssueURL(gl, 3) || p.Repo != gl.Path {
				t.Errorf("%d: a path that fits must stay whole and linked: %+v %+v %+v", tc.n, pr, is, p)
			}
			continue
		}
		if pr.Repo != "" || pr.URL != "" || is.Repo != "" || is.URL != "" || p.Repo != "" || p.RepoForge != contract.ForgeNone || p.RepoHost != "" {
			t.Errorf("%d: a path too long must be absent, never cut: %+v %+v %+v", tc.n, pr, is, p)
		}
	}
	// Two such projects closing issue 3 stay two issues, not one blank one.
	e := newEnv(t)
	a, z := model.GitLabRepo(host, pathOf(400, "a")), model.GitLabRepo(host, pathOf(400, "z"))
	w := session("w", model.StatusFinished, tNow)
	w.CreatedPRs = []model.PRRef{{Repo: a, Number: 1}, {Repo: z, Number: 1}}
	e.board.mu.Lock()
	for _, r := range []model.ForgeRepo{a, z} {
		e.board.prs[model.PRRef{Repo: r, Number: 1}] = model.PullRequest{Repo: r, Number: 1, State: model.PRStateOpen, ClosingIssues: []int{3}, OpenedAt: tNow.Add(-time.Hour)}
	}
	e.board.mu.Unlock()
	e.board.applySync(w)
	if c := onlyCard(t, e.board); len(c.Issues) != 2 {
		t.Fatalf("issues %+v, want two", c.Issues)
	}
}
