package core

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

// pinLister answers OpenIssues from by and records the pinned numbers each
// call asked. during, when set, runs once inside the next call, with no lock
// held, as if the owner acted while the call was out.
type pinLister struct {
	mu     sync.Mutex
	by     map[model.ForgeRepo]model.RepoIssues
	asked  []map[model.ForgeRepo][]int
	during func()
	err    error
}

func (f *pinLister) OpenIssues(_ context.Context, repos []model.ForgeRepo, pinned map[model.ForgeRepo][]int) ([]model.RepoIssues, error) {
	f.mu.Lock()
	f.asked = append(f.asked, maps.Clone(pinned))
	during := f.during
	f.during = nil
	var out []model.RepoIssues
	for _, r := range repos {
		if ri, ok := f.by[r]; ok {
			out = append(out, ri)
		}
	}
	err := f.err
	f.mu.Unlock()
	if during != nil {
		during()
	}
	return out, err
}

func (f *pinLister) set(ri model.RepoIssues) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.by[ri.Repo] = ri
}

func (f *pinLister) calls() []map[model.ForgeRepo][]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

// newPinEnv is newIssuesEnv with a pinLister: alpha (exRepo) lists 48 and
// 45, beta (betaRepo) lists 7.
func newPinEnv(t *testing.T) (*testEnv, *pinLister) {
	t.Helper()
	e := newEnv(t)
	gh := &pinLister{by: map[model.ForgeRepo]model.RepoIssues{
		model.GitHubRepo(exRepo):   sampleIssues(model.GitHubRepo(exRepo), 12, 48, 45),
		model.GitHubRepo(betaRepo): sampleIssues(model.GitHubRepo(betaRepo), 3, 7),
	}}
	e.board = newBoard(t, Options{
		Repos: e.repos, PRs: e.prs, State: e.state, Now: e.clock.Now, StaleAfter: 20 * time.Minute,
		Issues: map[model.Forge]source.IssueLister{model.ForgeGitHub: gh},
	})
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	e.repos.set(beta, model.Repo{Root: beta, MainRoot: beta, Remote: model.GitHubRepo(betaRepo), Branch: model.Of("main")})
	lookAt(e, tNow)
	return e, gh
}

// lookAt sets the clock to at, keeps both sessions live, and ticks: a look
// runs when one is due.
func lookAt(e *testEnv, at time.Time) {
	e.clock.Set(at)
	a := busySession("a", at)
	b := busySession("b", at)
	b.Folder = beta
	e.board.applySync(a)
	e.board.applySync(b)
	e.board.tick()
}

func pinKey(n int) string { return state.PinKey("github", model.GitHubHost, exRepo, n) }

func pin(t *testing.T, e *testEnv, n int) {
	t.Helper()
	if err := e.board.SetIssuePin(model.GitHubRepo(exRepo), n, true); err != nil {
		t.Fatal(err)
	}
}

func pinnedAnswer(n int, st model.PinState) model.PinnedIssue {
	p := model.PinnedIssue{State: st}
	p.Number = n
	if st != model.PinGone {
		p.Title, p.Milestone, p.Labels, p.UpdatedAt = "Sample pinned\x1b[31m", "Sample 1.0", []string{"sample"}, tNow.Add(-time.Hour)
	}
	return p
}

// The pinned numbers go in the look's own call, most recently pinned first,
// each to its own repository; what the forge answers fills the pinned row
// outside the listed 30 (cleaned like a listed row), and the revision moves.
func TestPinnedNumbersAreAskedInTheLookAndFillTheirRows(t *testing.T) {
	e, gh := newPinEnv(t)
	pin(t, e, 48)
	e.clock.Set(tNow.Add(time.Second))
	pin(t, e, 77)
	ri := sampleIssues(model.GitHubRepo(exRepo), 12, 48, 45)
	ri.Pinned = []model.PinnedIssue{pinnedAnswer(77, model.PinOpen), pinnedAnswer(48, model.PinOpen)}
	gh.set(ri)
	before := e.board.Snapshot().Issues.Rev
	lookAt(e, tNow.Add(issuesEvery))
	calls := gh.calls()
	if len(calls) != 2 {
		t.Fatalf("%d calls, want 2 (no extra call for pins)", len(calls))
	}
	want := map[model.ForgeRepo][]int{model.GitHubRepo(exRepo): {77, 48}}
	if !maps.EqualFunc(calls[1], want, slices.Equal) {
		t.Fatalf("asked %v, want %v", calls[1], want)
	}
	if e.board.Snapshot().Issues.Rev == before {
		t.Fatal("the revision did not move")
	}
	listed, pinned := pinnedRowsOf(t, e.board, model.GitHubRepo(exRepo))
	if !slices.Equal(numbers(listed), []int{48, 45}) || len(pinned) != 1 {
		t.Fatalf("listed %v, pinned %+v", numbers(listed), pinned)
	}
	p := pinned[0]
	if p.Number != 77 || p.Title != "Sample pinned[31m" || p.Milestone != "Sample 1.0" || !slices.Equal(p.Labels, []string{"sample"}) ||
		p.State != contract.OpenIssueOpen || !p.Pinned || p.Agents == nil || p.URL != "https://github.com/example-org/example-repo/issues/77" {
		t.Fatalf("pinned row %+v", p)
	}
}

// Closed marks the pin closed (served "closed"), gone drops it at once, a
// later open clears the mark, and a listed row clears a stale one.
func TestTheForgesAnswerIsReportedToThePins(t *testing.T) {
	e, gh := newPinEnv(t)
	for _, n := range []int{48, 77, 99} {
		pin(t, e, n)
	}
	if err := e.state.ReportPins(map[string]state.PinReport{pinKey(48): state.PinClosed}); err != nil {
		t.Fatal(err)
	}
	ri := sampleIssues(model.GitHubRepo(exRepo), 12, 48, 45)
	ri.Pinned = []model.PinnedIssue{pinnedAnswer(77, model.PinClosed), pinnedAnswer(99, model.PinGone)}
	gh.set(ri)
	lookAt(e, tNow.Add(issuesEvery))
	pins, closed := e.state.Pins(), e.state.PinsClosed()
	if _, ok := pins[pinKey(99)]; ok {
		t.Fatal("a gone pin stayed")
	}
	if _, ok := closed[pinKey(77)]; !ok {
		t.Fatalf("closed %v", closed)
	}
	if _, ok := closed[pinKey(48)]; ok {
		t.Fatal("a listed (open) pin kept its closed mark")
	}
	if _, pinned := pinnedRowsOf(t, e.board, model.GitHubRepo(exRepo)); len(pinned) != 1 || pinned[0].Number != 77 || pinned[0].State != contract.OpenIssueClosed || pinned[0].Title == "" {
		t.Fatalf("pinned %+v", pinned)
	}
	ri.Pinned = []model.PinnedIssue{pinnedAnswer(77, model.PinOpen)}
	gh.set(ri)
	lookAt(e, tNow.Add(2*issuesEvery))
	if _, ok := e.state.PinsClosed()[pinKey(77)]; ok {
		t.Fatal("a reopened pin kept its closed mark")
	}
}

// No answer reports nothing: an issue missing from the answer, a failed
// call, or a repository that did not answer OK.
func TestNoAnswerReportsNothing(t *testing.T) {
	e, gh := newPinEnv(t)
	pin(t, e, 77)
	pin(t, e, 99)
	ri := sampleIssues(model.GitHubRepo(exRepo), 12, 48, 45) // 77 and 99 not answered
	gh.set(ri)
	lookAt(e, tNow.Add(issuesEvery))
	nf := model.RepoIssues{Repo: model.GitHubRepo(exRepo), State: model.RepoIssuesNotFound,
		Pinned: []model.PinnedIssue{pinnedAnswer(77, model.PinGone), pinnedAnswer(99, model.PinClosed)}}
	gh.set(nf)
	lookAt(e, tNow.Add(2*issuesEvery))
	ri.Pinned = []model.PinnedIssue{pinnedAnswer(77, model.PinGone)}
	gh.set(ri)
	gh.mu.Lock()
	gh.err = source.ErrNotLoggedIn // its rows are dropped
	gh.mu.Unlock()
	lookAt(e, tNow.Add(3*issuesEvery))
	if pins := e.state.Pins(); len(pins) != 2 || len(e.state.PinsClosed()) != 0 {
		t.Fatalf("pins %v, closed %v", pins, e.state.PinsClosed())
	}
}

// A report is matched to the pin as it was when the look began: a pin
// removed and made again while the call was out, or one made meanwhile, is
// not dropped by that call's "gone".
func TestAReportMatchesThePinAsTheLookBegan(t *testing.T) {
	e, gh := newPinEnv(t)
	pin(t, e, 99)
	ri := sampleIssues(model.GitHubRepo(exRepo), 12, 48, 45)
	ri.Pinned = []model.PinnedIssue{pinnedAnswer(99, model.PinGone), pinnedAnswer(55, model.PinGone)}
	gh.set(ri)
	gh.mu.Lock()
	gh.during = func() {
		if err := e.board.SetIssuePin(model.GitHubRepo(exRepo), 99, false); err != nil {
			t.Error(err)
		}
		pin(t, e, 99)
		pin(t, e, 55)
	}
	gh.mu.Unlock()
	lookAt(e, tNow.Add(issuesEvery))
	pins := e.state.Pins()
	if _, ok := pins[pinKey(99)]; !ok {
		t.Fatal("a pin made again during the call was dropped by it")
	}
	if _, ok := pins[pinKey(55)]; !ok {
		t.Fatal("a pin made during the call was dropped by it")
	}
	lookAt(e, tNow.Add(2*issuesEvery)) // asked now: the same answer counts
	if pins := e.state.Pins(); len(pins) != 0 {
		t.Fatalf("pins %v", pins)
	}
}

// The rows asked by number are kept only for a pin as it was when the look
// began: a pin removed and made again while the call was out does not show
// that call's closed answer.
func TestAPinMadeAgainDuringTheCallDoesNotShowItsAnswer(t *testing.T) {
	e, gh := newPinEnv(t)
	pin(t, e, 99)
	ri := sampleIssues(model.GitHubRepo(exRepo), 12, 48, 45)
	ri.Pinned = []model.PinnedIssue{pinnedAnswer(99, model.PinClosed)}
	gh.set(ri)
	gh.mu.Lock()
	gh.during = func() {
		if err := e.board.SetIssuePin(model.GitHubRepo(exRepo), 99, false); err != nil {
			t.Error(err)
		}
		pin(t, e, 99)
	}
	gh.mu.Unlock()
	lookAt(e, tNow.Add(issuesEvery))
	if _, pinned := pinnedRowsOf(t, e.board, model.GitHubRepo(exRepo)); len(pinned) != 1 || pinned[0].State != "" || pinned[0].Title != "" {
		t.Fatalf("pinned %+v", pinned)
	}
}

// An answer of an older login generation, or for a repository found denied
// while the call was out, reports nothing (#174's rules).
func TestADroppedAnswerReportsNothing(t *testing.T) {
	for name, act := range map[string]func(b *Board){
		"generation": func(b *Board) { b.ghGen++ },
		"denied":     func(b *Board) { b.deniedLocked(model.GitHubRepo(exRepo)) },
	} {
		t.Run(name, func(t *testing.T) {
			e, gh := newPinEnv(t)
			pin(t, e, 99)
			ri := sampleIssues(model.GitHubRepo(exRepo), 12, 48, 45)
			ri.Pinned = []model.PinnedIssue{pinnedAnswer(99, model.PinGone)}
			gh.set(ri)
			gh.mu.Lock()
			gh.during = func() {
				e.board.mu.Lock()
				act(e.board)
				e.board.mu.Unlock()
			}
			gh.mu.Unlock()
			lookAt(e, tNow.Add(issuesEvery))
			if _, ok := e.state.Pins()[pinKey(99)]; !ok {
				t.Fatal("a dropped answer dropped the pin")
			}
		})
	}
}

// No pin waits for a later look: as many as can be pinned at all
// (state.MaxPins), in one repository, are all asked in its look, most
// recently pinned first, so a list answer never leaves a pin's details out.
func TestEveryPinIsAskedInOneLook(t *testing.T) {
	e, gh := newPinEnv(t)
	for n := 1; n <= state.MaxPins; n++ {
		e.clock.Set(tNow.Add(time.Duration(n) * time.Second))
		pin(t, e, 1000+n)
	}
	lookAt(e, tNow.Add(issuesEvery))
	calls := gh.calls()
	got := calls[len(calls)-1][model.GitHubRepo(exRepo)]
	if len(got) != state.MaxPins || got[0] != 1000+state.MaxPins || got[len(got)-1] != 1001 {
		t.Fatalf("asked %d numbers: %v…", len(got), got[:min(3, len(got))])
	}
}

// A mount change never drops or re-keys a pin: a pin whose key names no
// repository the board asks about (made under another mount) is dormant,
// kept in the state file and not shown, and it comes back when its address
// does. A GitLab pin is never asked by number (#182).
func TestAMountChangeKeepsThePinsDormantUntilTheirAddressReturns(t *testing.T) {
	repo := model.GitLabRepo(glHost, "Tools/Group/app")
	gl := &pinLister{by: map[model.ForgeRepo]model.RepoIssues{repo: sampleIssues(repo, 1, 3)}}
	e, glab := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitLab: gl}
	})
	glab.subs = map[string]string{glHost: "Tools"}
	key := state.PinKey("gitlab", glHost, "Tools/group/app", 5) // made under the mount "Tools"
	if err := e.state.SetPin(key, true, tNow); err != nil {
		t.Fatal(err)
	}
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: repo, Branch: model.Of("main")})
	at := tNow
	look := func(sub string) (asked []int, shown int) {
		t.Helper()
		glab.hmu.Lock()
		glab.subs = map[string]string{glHost: sub}
		glab.hmu.Unlock()
		e.board.mu.Lock()
		e.board.noteHostLocked(glHost, nil)
		e.board.mu.Unlock()
		lookAt(e, at)
		at = at.Add(issuesEvery)
		calls := gl.calls()
		if len(calls) == 0 {
			t.Fatal("no call")
		}
		for _, ns := range calls[len(calls)-1] {
			asked = append(asked, ns...)
		}
		e.board.mu.Lock()
		defer e.board.mu.Unlock()
		for _, ri := range e.board.issues {
			for _, p := range e.board.pinnedRowsLocked(ri.Repo, nil) {
				if p.Number == 5 {
					shown++
				}
			}
		}
		return asked, shown
	}
	if asked, shown := look("Tools"); len(asked) != 0 || shown != 1 {
		t.Fatalf("under its mount: asked %v, shown %d", asked, shown)
	}
	for _, sub := range []string{"", "tools"} { // the key is spelled otherwise now
		if asked, shown := look(sub); len(asked) != 0 || shown != 0 {
			t.Fatalf("mount %q: asked %v, shown %d", sub, asked, shown)
		}
		if _, ok := e.state.Pins()[key]; !ok {
			t.Fatalf("mount %q dropped the pin", sub)
		}
	}
	if asked, shown := look("Tools"); len(asked) != 0 || shown != 1 {
		t.Fatalf("back under its mount: asked %v, shown %d", asked, shown)
	}
}

// A GitLab pin is never asked by number and nothing reports on it (#182):
// a by-number answer that came anyway and a listed row both leave it as it
// is, kept, with no closed mark (one from an earlier build is not served),
// and outside the listed 30 it shows its number and link only.
func TestAGitLabPinIsNeverAskedNorReported(t *testing.T) {
	repo := model.GitLabRepo(glHost, "group/app")
	listed := sampleIssues(repo, 2, 3, 5)
	listed.Pinned = []model.PinnedIssue{pinnedAnswer(6, model.PinGone), pinnedAnswer(9, model.PinClosed), pinnedAnswer(10, model.PinOpen)}
	gl := &pinLister{by: map[model.ForgeRepo]model.RepoIssues{repo: listed}}
	e, glab := newGitLabEnvWith(t, func(o *Options) {
		o.Issues = map[model.Forge]source.IssueLister{model.ForgeGitLab: gl}
	})
	glab.subs = map[string]string{glHost: ""}
	key := func(n int) string { return state.PinKey("gitlab", glHost, "group/app", n) }
	for _, n := range []int{5, 6, 9, 10} {
		if err := e.state.SetPin(key(n), true, tNow); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.state.ReportPins(map[string]state.PinReport{key(5): state.PinClosed, key(10): state.PinClosed}); err != nil {
		t.Fatal(err)
	}
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: repo, Branch: model.Of("main")})
	e.board.mu.Lock()
	e.board.noteHostLocked(glHost, nil)
	e.board.mu.Unlock()
	lookAt(e, tNow)
	calls := gl.calls()
	if len(calls) == 0 {
		t.Fatal("no call")
	}
	for _, c := range calls {
		if len(c) != 0 {
			t.Fatalf("GitLab pins were asked: %v", c)
		}
	}
	pins, closed := e.state.Pins(), e.state.PinsClosed()
	for _, n := range []int{5, 6, 9, 10} {
		if _, ok := pins[key(n)]; !ok {
			t.Fatalf("pin %d was dropped", n)
		}
	}
	if len(closed) != 0 {
		t.Fatalf("closed marks of GitLab pins: %v", closed)
	}
	reports := pinReports{}
	e.board.mu.Lock()
	e.board.pinReportsLocked(pinLook{asked: map[model.ForgeRepo][]int{repo: {5, 6, 9, 10}}, serial: e.board.pinSerial}, listed, reports)
	e.board.mu.Unlock()
	if len(reports) != 0 {
		t.Fatalf("a GitLab answer reported %v", reports)
	}
	_, rows := pinnedRowsOf(t, e.board, repo)
	if len(rows) != 3 {
		t.Fatalf("pinned rows outside the list: %+v", rows)
	}
	for _, r := range rows {
		if r.Title != "" || r.URL != model.ForgeIssueURL(repo, r.Number) || r.State == contract.OpenIssueClosed {
			t.Fatalf("a GitLab pin outside the list shows more than its number and link: %+v", r)
		}
	}
}

// An alias that comes back with no answer keeps what was last verified
// about its pin, with the revision left; a decisive answer replaces it, and
// a repository that answered anything else carries nothing on.
func TestAnUnansweredPinKeepsItsLastDetails(t *testing.T) {
	e, gh := newPinEnv(t)
	pin(t, e, 77)
	repo := model.GitHubRepo(exRepo)
	plain := sampleIssues(repo, 12, 48, 45)
	with := plain
	with.Pinned = []model.PinnedIssue{pinnedAnswer(77, model.PinOpen)}
	title := func() (string, contract.OpenIssueState) {
		t.Helper()
		_, pinned := pinnedRowsOf(t, e.board, repo)
		if len(pinned) != 1 || pinned[0].Number != 77 {
			t.Fatalf("pinned %+v", pinned)
		}
		return pinned[0].Title, pinned[0].State
	}
	at := tNow
	look := func(ri model.RepoIssues) {
		at = at.Add(issuesEvery)
		gh.set(ri)
		lookAt(e, at)
	}
	look(with)
	rev := e.board.Snapshot().Issues.Rev
	look(plain) // 77's alias unanswered
	if got, _ := title(); got == "" || e.board.Snapshot().Issues.Rev != rev {
		t.Fatalf("an unanswered alias lost the details (title %q) or moved the revision", got)
	}
	closed := plain
	closed.Pinned = []model.PinnedIssue{pinnedAnswer(77, model.PinClosed)}
	look(closed)
	if got, st := title(); got == "" || st != contract.OpenIssueClosed {
		t.Fatalf("a decisive answer did not replace them: %q %s", got, st)
	}
	look(model.RepoIssues{Repo: repo, State: model.RepoIssuesNotFound})
	look(plain)
	if got, _ := title(); got != "" {
		t.Fatalf("details carried over a repository that was not found: %q", got)
	}
	// A listed row is decisive too: once 77 was listed, nothing older is
	// carried when it leaves the list unanswered.
	look(with)
	look(sampleIssues(repo, 12, 48, 77))
	look(plain)
	if got, _ := title(); got != "" {
		t.Fatalf("details older than its listed row came back: %q", got)
	}
}

// An unpin drops a pin's details: pinned again, it starts from its number
// and link, before any look.
func TestAPinMadeAgainShowsNoEarlierDetails(t *testing.T) {
	e, gh := newPinEnv(t)
	pin(t, e, 77)
	repo := model.GitHubRepo(exRepo)
	ri := sampleIssues(repo, 12, 48, 45)
	ri.Pinned = []model.PinnedIssue{pinnedAnswer(77, model.PinClosed)}
	gh.set(ri)
	lookAt(e, tNow.Add(issuesEvery))
	if _, pinned := pinnedRowsOf(t, e.board, repo); len(pinned) != 1 || pinned[0].Title == "" {
		t.Fatalf("setup: %+v", pinned)
	}
	if err := e.board.SetIssuePin(repo, 77, false); err != nil {
		t.Fatal(err)
	}
	rev := e.board.Snapshot().Issues.Rev
	pin(t, e, 77)
	if e.board.Snapshot().Issues.Rev == rev {
		t.Fatal("a pin change left the revision")
	}
	if _, pinned := pinnedRowsOf(t, e.board, repo); len(pinned) != 1 || pinned[0].Title != "" || pinned[0].State == contract.OpenIssueClosed {
		t.Fatalf("pinned again it shows the earlier pin: %+v", pinned)
	}
}

// A look logged out again, once the login's rows are gone, drops nothing:
// the revision and the purge marker stay.
func TestALoggedOutLookAgainLeavesTheRevision(t *testing.T) {
	e, gh := newPinEnv(t)
	gh.mu.Lock()
	gh.err = fmt.Errorf("github: not logged in: %w", source.ErrNotLoggedIn)
	gh.mu.Unlock()
	lookAt(e, tNow.Add(issuesEvery))
	s := e.board.Snapshot().Issues
	if s.Purged == 0 || s.Purged != s.Rev {
		t.Fatalf("the first logged-out look was not a purge: %+v", s)
	}
	lookAt(e, tNow.Add(2*issuesEvery))
	if n := len(gh.calls()); n != 3 {
		t.Fatalf("%d calls, want 3", n)
	}
	if again := e.board.Snapshot().Issues; again.Rev != s.Rev || again.Purged != s.Purged {
		t.Fatalf("an identical logged-out look moved %+v → %+v", s, again)
	}
}
