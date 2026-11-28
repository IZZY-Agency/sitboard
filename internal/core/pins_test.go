package core

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

// pinnedRowsOf is repo's listed rows and pinned rows as Issues serves them.
func pinnedRowsOf(t *testing.T, b *Board, repo model.ForgeRepo) (listed, pinned []contract.OpenIssue) {
	t.Helper()
	l, err := b.Issues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range l.Repos {
		if r.Repo == repo.Path {
			return r.Issues, r.Pinned
		}
	}
	t.Fatalf("no repository %s", repo.Path)
	return nil, nil
}

func numbers(rows []contract.OpenIssue) []int {
	var out []int
	for _, r := range rows {
		out = append(out, r.Number)
	}
	return out
}

// No pin: the rows are as listed and no pinned row is added, never nil.
func TestNoPinAddsNothing(t *testing.T) {
	e, _ := newIssuesEnv(t)
	e.board.tick()
	listed, pinned := pinnedRowsOf(t, e.board, model.GitHubRepo(exRepo))
	if pinned == nil || len(pinned) != 0 || !slices.Equal(numbers(listed), []int{48, 45}) || listed[0].Pinned || listed[1].Pinned {
		t.Fatalf("listed %+v, pinned %#v", listed, pinned)
	}
	b := newBoard(t, Options{Now: e.clock.Now}) // no state store at all
	b.mu.Lock()
	defer b.mu.Unlock()
	if got := b.pinnedRowsLocked(model.GitHubRepo(exRepo), nil); got == nil || len(got) != 0 {
		t.Fatalf("no store gave %#v", got)
	}
}

// A pin is kept under the repository as issueKey spells it, so another case
// of a GitHub owner and name is the same pin; it bumps the issues revision
// once, and setting it again changes nothing (no write, no new revision).
func TestSetIssuePin(t *testing.T) {
	e, _ := newIssuesEnv(t)
	e.board.tick()
	rev := e.board.Snapshot().Issues.Rev
	if err := e.board.SetIssuePin(model.GitHubRepo("Example-Org/Example-Repo"), 45, true); err != nil {
		t.Fatal(err)
	}
	key := state.PinKey("github", "github.com", "example-org/example-repo", 45)
	if got := e.state.Pins(); len(got) != 1 || !got[key].Equal(tNow) {
		t.Fatalf("pins %v", got)
	}
	after := e.board.Snapshot().Issues.Rev
	if after <= rev {
		t.Fatalf("rev %d after a pin, was %d", after, rev)
	}
	l, _ := e.board.Issues(context.Background())
	if l.Rev != after {
		t.Fatalf("the list is of rev %d, the snapshot says %d", l.Rev, after)
	}
	e.clock.Set(e.clock.Now().Add(time.Minute))
	if err := e.board.SetIssuePin(model.GitHubRepo(exRepo), 45, true); err != nil {
		t.Fatal(err)
	}
	if got := e.board.Snapshot().Issues.Rev; got != after {
		t.Fatalf("pinning again moved rev %d -> %d", after, got)
	}
	if got := e.state.Pins(); !got[key].Equal(tNow) {
		t.Fatalf("pinning again changed the time: %v", got)
	}
	if err := e.board.SetIssuePin(model.GitHubRepo(exRepo), 45, false); err != nil {
		t.Fatal(err)
	}
	if len(e.state.Pins()) != 0 || e.board.Snapshot().Issues.Rev <= after {
		t.Fatalf("unpin: pins %v, rev %d", e.state.Pins(), e.board.Snapshot().Issues.Rev)
	}
	rev = e.board.Snapshot().Issues.Rev
	if err := e.board.SetIssuePin(model.GitHubRepo(exRepo), 45, false); err != nil || e.board.Snapshot().Issues.Rev != rev {
		t.Fatalf("unpinning again: %v, rev %d -> %d", err, rev, e.board.Snapshot().Issues.Rev)
	}
}

func TestSetIssuePinRefusals(t *testing.T) {
	e, _ := newIssuesEnv(t)
	e.board.tick()
	rev := e.board.Snapshot().Issues.Rev
	for name, c := range map[string]struct {
		repo model.ForgeRepo
		n    int
		want error
	}{
		"a repository not on the board":   {model.GitHubRepo("example-org/elsewhere"), 1, ErrUnknownRepo},
		"the same path on GitLab":         {model.GitLabRepo("gitlab.com", exRepo), 1, ErrUnknownRepo},
		"another host":                    {model.ForgeRepo{Forge: model.ForgeGitHub, Host: "git.example.com", Path: exRepo}, 1, ErrUnknownRepo},
		"number 0":                        {model.GitHubRepo(exRepo), 0, ErrBadIssue},
		"a negative number":               {model.GitHubRepo(exRepo), -1, ErrBadIssue},
		"2^31":                            {model.GitHubRepo(exRepo), math.MaxInt32 + 1, ErrBadIssue},
		"an unknown repository, number 0": {model.GitHubRepo("example-org/elsewhere"), 0, ErrBadIssue},
	} {
		if err := e.board.SetIssuePin(c.repo, c.n, true); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if len(e.state.Pins()) != 0 || e.board.Snapshot().Issues.Rev != rev {
		t.Fatalf("a refused pin changed something: %v", e.state.Pins())
	}
	if err := e.board.SetIssuePin(model.GitHubRepo(exRepo), math.MaxInt32, true); err != nil {
		t.Fatalf("2^31-1: %v", err)
	}
}

// With issues off (GitHub switched off), no repository is asked about, so
// none can be pinned.
func TestSetIssuePinNeedsIssuesOn(t *testing.T) {
	e := newEnv(t)
	if err := e.board.SetIssuePin(model.GitHubRepo(exRepo), 1, true); !errors.Is(err, ErrUnknownRepo) {
		t.Fatalf("err %v", err)
	}
}

// Past the cap, a new pin is ErrTooManyPins and changes nothing.
func TestSetIssuePinCap(t *testing.T) {
	e, _ := newIssuesEnv(t)
	e.board.tick()
	for n := 1; n <= state.MaxPins; n++ {
		if err := e.state.SetPin(state.PinKey("github", "github.com", betaRepo, n), true, tNow); err != nil {
			t.Fatal(err)
		}
	}
	rev := e.board.Snapshot().Issues.Rev
	if err := e.board.SetIssuePin(model.GitHubRepo(exRepo), 45, true); !errors.Is(err, ErrTooManyPins) {
		t.Fatalf("err %v", err)
	}
	if e.board.Snapshot().Issues.Rev != rev || len(e.state.Pins()) != state.MaxPins {
		t.Fatal("a refused pin changed something")
	}
}

// A pinned listed issue is marked and leads the rows (the others keep their
// order); pinned issues outside the listed ones come back most recently
// pinned first, with number and address only, "closed" once the forge
// reported them closed. Another repository's pins stay with it.
func TestPinnedRows(t *testing.T) {
	e, gh := newIssuesEnv(t)
	gh.by[model.GitHubRepo(exRepo)] = sampleIssues(model.GitHubRepo(exRepo), 12, 48, 47, 45)
	e.board.tick()
	pin := func(repo string, n int) {
		t.Helper()
		if err := e.board.SetIssuePin(model.GitHubRepo(repo), n, true); err != nil {
			t.Fatal(err)
		}
		e.clock.Set(e.clock.Now().Add(time.Minute))
	}
	pin(exRepo, 45)
	pin(exRepo, 900)
	pin(exRepo, 12)
	pin(betaRepo, 7)
	pin(betaRepo, 901)
	if err := e.state.ReportPins(map[string]state.PinReport{state.PinKey("github", "github.com", exRepo, 900): state.PinClosed}); err != nil {
		t.Fatal(err)
	}
	listed, pinned := pinnedRowsOf(t, e.board, model.GitHubRepo(exRepo))
	if !slices.Equal(numbers(listed), []int{45, 48, 47}) || !listed[0].Pinned || listed[1].Pinned || listed[2].Pinned {
		t.Fatalf("listed %+v", listed)
	}
	if !slices.Equal(numbers(pinned), []int{12, 900}) {
		t.Fatalf("pinned %+v", pinned)
	}
	p := pinned[0]
	if !p.Pinned || p.State != "" || p.Title != "" || p.URL != "https://github.com/example-org/example-repo/issues/12" || p.Labels == nil || p.Agents == nil {
		t.Fatalf("unknown pinned row %+v", p)
	}
	if pinned[1].State != contract.OpenIssueClosed {
		t.Fatalf("closed pinned row %+v", pinned[1])
	}
	listed, pinned = pinnedRowsOf(t, e.board, model.GitHubRepo(betaRepo))
	if !slices.Equal(numbers(listed), []int{7}) || !listed[0].Pinned || !slices.Equal(numbers(pinned), []int{901}) {
		t.Fatalf("beta: listed %+v, pinned %+v", listed, pinned)
	}
}

// A repository path too long for the page gets no address on a pinned row,
// as on listed ones.
func TestPinnedRowWithoutAPagePathHasNoURL(t *testing.T) {
	e, _ := newIssuesEnv(t)
	long := model.GitHubRepo("example-org/" + strings.Repeat("r", 600))
	if err := e.state.SetPin(state.PinKey("github", "github.com", long.Path, 3), true, tNow); err != nil {
		t.Fatal(err)
	}
	e.board.mu.Lock()
	rows := e.board.pinnedRowsLocked(long, nil)
	e.board.mu.Unlock()
	if len(rows) != 1 || rows[0].URL != "" {
		t.Fatalf("rows %+v", rows)
	}
}

// --no-state (a memory store) keeps pins for the run and says they are
// unsaved; a state file says they are saved.
func TestPinsUnsavedWithoutAStateFile(t *testing.T) {
	e, gh := newIssuesEnv(t)
	if e.board.pinsUnsavedLocked() {
		t.Fatal("a state file counts as unsaved")
	}
	mem := state.MemoryAt(e.clock.Now)
	b := newBoard(t, Options{
		Repos: e.repos, PRs: e.prs, State: mem, Now: e.clock.Now, StaleAfter: 20 * time.Minute,
		Issues: map[model.Forge]source.IssueLister{model.ForgeGitHub: gh},
	})
	b.applySync(busySession("a", tNow))
	b.tick()
	if err := b.SetIssuePin(model.GitHubRepo(exRepo), 45, true); err != nil {
		t.Fatal(err)
	}
	if len(mem.Pins()) != 1 || !b.pinsUnsavedLocked() {
		t.Fatalf("pins %v, unsaved %v", mem.Pins(), b.pinsUnsavedLocked())
	}
	if newBoard(t, Options{Now: e.clock.Now}).pinsUnsavedLocked() != true {
		t.Fatal("no store counts as saved")
	}
}

// A GitLab pin is keyed by the board's issueKey: the mount exactly, the path
// under it in lower case. So a pin set through one case of what follows the
// mount marks the row the forge listed under another (the checkout's), and
// the stored key is "gitlab|<site>|tools/group/app#5".
func TestGitLabPinOnAMountedHostMatchesItsRow(t *testing.T) {
	mixed := model.GitLabRepo(glHost, "tools/Group/App")
	gl := &fakeIssues{by: map[model.ForgeRepo]model.RepoIssues{mixed: sampleIssues(mixed, 2, 6, 5)}}
	e, glab := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitLab: gl}
	})
	glab.subs = map[string]string{glHost: "tools"}
	e.repos.set("/work/p", model.Repo{Root: "/work/p", MainRoot: "/work/p", Remote: mixed, Branch: model.Of("main")})
	s := busySession("s", tNow)
	s.Folder = "/work/p"
	e.board.applySync(s)
	e.board.tick() // the host check runs: its mount is "tools"
	e.clock.Set(tNow.Add(contract.IssuesFreshFor))
	e.board.tick() // the look asks GitLab
	if err := e.board.SetIssuePin(model.GitLabRepo(glHost, "tools/group/APP"), 5, true); err != nil {
		t.Fatal(err)
	}
	if got := e.state.Pins(); len(got) != 1 || got[state.PinKey("gitlab", glHost, "tools/group/app", 5)].IsZero() {
		t.Fatalf("pins %v", got)
	}
	if err := e.board.SetIssuePin(model.GitLabRepo(glHost, "Tools/group/app"), 5, true); !errors.Is(err, ErrUnknownRepo) {
		t.Fatalf("another mount spelling: %v", err)
	}
	listed, pinned := pinnedRowsOf(t, e.board, mixed)
	if !slices.Equal(numbers(listed), []int{5, 6}) || !listed[0].Pinned || len(pinned) != 0 {
		t.Fatalf("listed %+v, pinned %+v", listed, pinned)
	}
}
