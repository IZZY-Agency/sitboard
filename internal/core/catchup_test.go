package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

func catchup(t *testing.T, b *Board) contract.Catchup {
	t.Helper()
	c, err := b.Catchup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func kindsAt(c contract.Catchup) []string {
	out := []string{}
	for _, it := range c.Items {
		out = append(out, fmt.Sprintf("%s@%s", it.Kind, it.At.Format("01-02T15:04")))
	}
	return out
}

func prAt(n int, state model.PRState, opened, closed time.Time) model.PullRequest {
	return model.PullRequest{
		Repo: model.GitHubRepo(exRepo), Number: n, Title: fmt.Sprintf("Placeholder %d", n),
		URL:   fmt.Sprintf("https://github.com/example-org/example-repo/pull/%d", n),
		State: state, Checks: model.ChecksPassing, OpenedAt: opened, ClosedAt: closed,
	}
}

// Before the first "mark read" catch-up reaches 7 days back; after it, only
// what came after the marker. A turn end in the future is not listed yet.
func TestCatchupTurnsFollowTheMarkerAndTheWindow(t *testing.T) {
	e := newEnv(t)
	s := session("s1", model.StatusFinished, tNow.Add(-time.Hour))
	s.TurnEnds = []time.Time{
		tNow.Add(-8 * 24 * time.Hour), // older than the window
		tNow.Add(-6 * 24 * time.Hour),
		tNow.Add(-3 * time.Hour),
		tNow.Add(-time.Hour),
		tNow.Add(time.Hour), // a clock ahead of this one
	}
	e.board.applySync(s)

	c := catchup(t, e.board)
	if !c.Marker.IsZero() {
		t.Fatalf("marker %v before any mark read", c.Marker)
	}
	want := []string{"turn_finished@11-17T09:00", "turn_finished@11-17T07:00", "turn_finished@11-11T10:00"}
	if got := kindsAt(c); !slices.Equal(got, want) {
		t.Fatalf("items %v, want %v", got, want)
	}

	// The next run starts with a marker the last one saved: what it covers
	// stays read.
	if err := e.state.SetCatchupMarker(tNow.Add(-2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	b := e.restart(t)
	b.applySync(s)
	c = catchup(t, b)
	if !c.Marker.Equal(tNow.Add(-2 * time.Hour)) {
		t.Fatalf("marker %v", c.Marker)
	}
	if got := kindsAt(c); !slices.Equal(got, want[:1]) {
		t.Fatalf("after mark read: items %v, want %v", got, want[:1])
	}
}

// restart is a new board on the same state file, as after sitboard restarts.
func (e *testEnv) restart(t *testing.T) *Board {
	t.Helper()
	return newBoard(t, Options{Repos: e.repos, PRs: e.prs, State: e.state, Now: e.clock.Now, StaleAfter: 20 * time.Minute})
}

// A marker older than the window still stops at 7 days.
func TestCatchupOldMarkerStopsAtTheWindow(t *testing.T) {
	e := newEnv(t)
	if err := e.state.SetCatchupMarker(tNow.Add(-30 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	s := session("s1", model.StatusFinished, tNow)
	s.TurnEnds = []time.Time{tNow.Add(-8 * 24 * time.Hour), tNow.Add(-time.Hour)}
	e.board.applySync(s)
	if got := kindsAt(catchup(t, e.board)); !slices.Equal(got, []string{"turn_finished@11-17T09:00"}) {
		t.Fatalf("items %v", got)
	}
}

// An item carries the card id while the card is on the board, the agent's
// name, the project id the board uses and the card's title; never more.
func TestCatchupItemNamesTheCardAndProject(t *testing.T) {
	e := newEnv(t)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	s := session("s1", model.StatusFinished, tNow)
	s.TurnEnds = []time.Time{tNow.Add(-time.Minute)}
	e.board.applySync(s)
	card := onlyCard(t, e.board)

	c := catchup(t, e.board)
	if len(c.Items) != 1 {
		t.Fatalf("items %+v", c.Items)
	}
	it := c.Items[0]
	if it.CardID != card.ID || it.ProjectID != card.ProjectID || it.ProjectName != "alpha" || it.AgentName != "worker-s1" || it.Title != card.Title || it.PullRequest != nil {
		t.Fatalf("item %+v, card %+v", it, card)
	}
}

// Pull requests the session created: opened and merged in the window are
// listed with the pull request, a closed one only as opened, and one opened
// before the marker only as merged. Newest first.
func TestCatchupListsOpenedAndMergedPullRequests(t *testing.T) {
	e := newEnv(t)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	e.prs.prs = []model.PullRequest{
		prAt(1, model.PRStateOpen, tNow.Add(-50*time.Minute), time.Time{}),
		prAt(2, model.PRStateMerged, tNow.Add(-40*time.Minute), tNow.Add(-10*time.Minute)),
		prAt(3, model.PRStateClosed, tNow.Add(-30*time.Minute), tNow.Add(-20*time.Minute)),
		prAt(4, model.PRStateMerged, tNow.Add(-9*24*time.Hour), tNow.Add(-5*time.Minute)),
	}
	s := session("s1", model.StatusFinished, tNow)
	s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 1}, {Repo: model.GitHubRepo(exRepo), Number: 2}, {Repo: model.GitHubRepo(exRepo), Number: 3}, {Repo: model.GitHubRepo(exRepo), Number: 4}}
	e.board.applySync(s)
	e.board.tick()

	c := catchup(t, e.board)
	var got []string
	for _, it := range c.Items {
		if it.PullRequest == nil || it.Title != it.PullRequest.Title {
			t.Fatalf("item %+v has no pull request or a different title", it)
		}
		got = append(got, fmt.Sprintf("%s#%d", it.Kind, it.PullRequest.Number))
	}
	want := []string{"pr_merged#4", "pr_merged#2", "pr_opened#3", "pr_opened#2", "pr_opened#1"}
	if !slices.Equal(got, want) {
		t.Fatalf("items %v, want %v", got, want)
	}
	if c.Items[0].PullRequest.URL != "https://github.com/example-org/example-repo/pull/4" || c.Items[0].PullRequest.State != contract.PRStateMerged {
		t.Fatalf("pull request %+v", c.Items[0].PullRequest)
	}
}

// Two sessions that both list one pull request give one item per kind.
func TestCatchupListsAPullRequestOnce(t *testing.T) {
	e := newEnv(t)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	e.prs.prs = []model.PullRequest{prAt(7, model.PRStateOpen, tNow.Add(-time.Hour), time.Time{})}
	for _, id := range []string{"s1", "s2"} {
		s := session(id, model.StatusFinished, tNow)
		s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 7}}
		e.board.applySync(s)
	}
	e.board.tick()
	if got := kindsAt(catchup(t, e.board)); len(got) != 1 {
		t.Fatalf("items %v, want one", got)
	}
}

// A session that ended has left the board, but what it did is still listed,
// with no card id.
func TestCatchupKeepsWhatAnEndedSessionDid(t *testing.T) {
	e := newEnv(t)
	s := session("s1", model.StatusFinished, tNow.Add(-time.Hour))
	s.TurnEnds = []time.Time{tNow.Add(-time.Hour)}
	s.Live, s.Ended, s.EndedAt = model.Of(false), true, tNow.Add(-30*time.Minute)
	e.board.applySync(s)
	if n := len(e.board.Snapshot().Cards); n != 0 {
		t.Fatalf("%d cards for an ended session", n)
	}
	c := catchup(t, e.board)
	if len(c.Items) != 1 || c.Items[0].CardID != "" || c.Items[0].AgentName != "worker-s1" {
		t.Fatalf("items %+v", c.Items)
	}
}

// A worker that opened a pull request and ended: the merge made afterwards
// still reaches catch-up, because its pull requests are still looked up.
func TestCatchupSeesAMergeAfterTheSessionEnded(t *testing.T) {
	e := newEnv(t)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	e.prs.prs = []model.PullRequest{prAt(9, model.PRStateOpen, tNow.Add(-3*time.Hour), time.Time{})}
	s := session("s1", model.StatusFinished, tNow.Add(-2*time.Hour))
	s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 9}}
	e.board.applySync(s)
	e.board.tick()
	s.Live, s.Ended, s.EndedAt = model.Of(false), true, tNow.Add(-time.Hour)
	e.board.applySync(s)

	e.prs.mu.Lock()
	e.prs.prs = []model.PullRequest{prAt(9, model.PRStateMerged, tNow.Add(-3*time.Hour), tNow.Add(time.Minute))}
	e.prs.mu.Unlock()
	e.clock.Set(tNow.Add(endedEvery + 2*time.Minute))
	e.board.tick()

	got := kindsAt(catchup(t, e.board))
	want := []string{"pr_merged@11-17T10:01", "pr_opened@11-17T07:00"}
	if !slices.Equal(got, want) {
		t.Fatalf("items %v, want %v", got, want)
	}
}

// Ended sessions' pull requests ride along in the repo's one batched call:
// a repo with a live session and an ended one is asked once per round, and
// a pull request already merged is not asked about again.
func TestEndedSessionsAddNoGitHubCalls(t *testing.T) {
	e := newEnv(t)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	e.prs.prs = []model.PullRequest{
		prAt(1, model.PRStateOpen, tNow.Add(-time.Hour), time.Time{}),
		prAt(2, model.PRStateOpen, tNow.Add(-time.Hour), time.Time{}),
		prAt(3, model.PRStateMerged, tNow.Add(-time.Hour), tNow.Add(-time.Minute)),
	}
	live := session("s1", model.StatusBusy, tNow)
	live.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 1}}
	ended := session("s2", model.StatusFinished, tNow)
	ended.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 2}, {Repo: model.GitHubRepo(exRepo), Number: 3}}
	ended.Live, ended.Ended, ended.EndedAt = model.Of(false), true, tNow.Add(-time.Minute)
	e.board.applySync(live)
	e.board.applySync(ended)
	e.board.tick()
	for round := 1; round <= 3; round++ {
		e.clock.Set(tNow.Add(time.Duration(round) * (githubEvery + time.Second)))
		e.board.tick()
	}
	if n := e.prs.callsFor(exRepo); n != 4 {
		t.Fatalf("%d calls for the repo in 4 rounds, want 4", n)
	}
	e.prs.mu.Lock()
	last := e.prs.calls[len(e.prs.calls)-1]
	e.prs.mu.Unlock()
	if !slices.Equal(last.numbers, []int{1, 2}) {
		t.Fatalf("last call asked about %v, want [1 2]: the merged one is final", last.numbers)
	}
}

// An ended session is looked up for 7 days after it ended, then no more.
func TestEndedSessionsAreLookedUpForSevenDays(t *testing.T) {
	e := newEnv(t)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	e.prs.prs = []model.PullRequest{prAt(1, model.PRStateOpen, tNow.Add(-time.Hour), time.Time{})}
	s := session("s1", model.StatusFinished, tNow)
	s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 1}}
	s.Live, s.Ended, s.EndedAt = model.Of(false), true, tNow.Add(-time.Minute)
	e.board.applySync(s)
	e.board.tick()
	if n := e.prs.callsFor(exRepo); n != 1 {
		t.Fatalf("%d calls while recently ended, want 1", n)
	}
	e.clock.Set(tNow.Add(catchupWindow + time.Hour))
	e.board.tick()
	if n := e.prs.callsFor(exRepo); n != 1 {
		t.Fatalf("%d calls after 7 days, want still 1", n)
	}
}

// Past the cap the oldest items are dropped, never the newest.
func TestCatchupKeepsTheNewestItemsUnderTheCap(t *testing.T) {
	e := newEnv(t)
	s := session("s1", model.StatusFinished, tNow)
	for i := range maxCatchupItems + 20 {
		s.TurnEnds = append(s.TurnEnds, tNow.Add(-time.Duration(maxCatchupItems+20-i)*time.Minute))
	}
	e.board.applySync(s)
	c := catchup(t, e.board)
	if len(c.Items) != maxCatchupItems {
		t.Fatalf("%d items, want %d", len(c.Items), maxCatchupItems)
	}
	if !c.Items[0].At.Equal(tNow.Add(-time.Minute)) || !c.Items[len(c.Items)-1].At.Equal(tNow.Add(-maxCatchupItems*time.Minute)) {
		t.Fatalf("kept %v .. %v", c.Items[0].At, c.Items[len(c.Items)-1].At)
	}
}

// A session outside the roots is left out, as on the board.
func TestCatchupRespectsRoots(t *testing.T) {
	e := newEnv(t)
	e.board.roots = []string{beta}
	s := session("s1", model.StatusFinished, tNow)
	s.TurnEnds = []time.Time{tNow.Add(-time.Minute)}
	e.board.applySync(s)
	if n := len(catchup(t, e.board).Items); n != 0 {
		t.Fatalf("%d items from outside the roots", n)
	}
}

// The answer has exactly the fields of the contract's golden file.
func TestCatchupMatchesContractGoldenShape(t *testing.T) {
	e := newEnv(t)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	e.prs.prs = []model.PullRequest{prAt(1, model.PRStateMerged, tNow.Add(-time.Hour), tNow.Add(-time.Minute))}
	if err := e.state.SetCatchupMarker(tNow.Add(-2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	s := session("s1", model.StatusFinished, tNow)
	s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 1}}
	s.Artifacts = []model.ArtifactRef{{URL: "https://claude.ai/artifact/exampleArtifact0001", Title: "Placeholder", PublishedAt: tNow.Add(-time.Minute)}}
	e.board.applySync(s)
	e.board.tick()

	raw, err := json.Marshal(catchup(t, e.board))
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile("../contract/golden/catchup.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(golden, &want); err != nil {
		t.Fatal(err)
	}
	gk, wk := map[string]bool{}, map[string]bool{}
	keysOf(got, "", gk)
	keysOf(want, "", wk)
	for k := range gk {
		if !wk[k] {
			t.Errorf("catch-up has %s, the golden does not", k)
		}
	}
	for k := range wk {
		if !gk[k] {
			t.Errorf("golden has %s, catch-up does not", k)
		}
	}
}

// An item names its project as the board does, also when the project has
// left the board: hostile text is cleaned and capped, and a session with no
// folder is in "unknown folder".
func TestCatchupNamesTheProjectLikeTheBoard(t *testing.T) {
	e := newEnv(t)
	hostile := testHome + "/projects/\u202Eevil\x1b[31m" + strings.Repeat("x", 1000)
	e.repos.set(hostile, model.Repo{Root: hostile, MainRoot: hostile})
	gone := session("s1", model.StatusFinished, tNow)
	gone.Folder = hostile
	gone.TurnEnds = []time.Time{tNow.Add(-time.Minute)}
	gone.Live, gone.Ended, gone.EndedAt = model.Of(false), true, tNow
	nowhere := session("s2", model.StatusFinished, tNow)
	nowhere.Folder = ""
	nowhere.TurnEnds = []time.Time{tNow.Add(-2 * time.Minute)}
	e.board.applySync(gone)
	e.board.applySync(nowhere)
	if n := len(e.board.Snapshot().Projects); n != 1 {
		t.Fatalf("%d projects on the board, want only the folderless one", n)
	}

	c := catchup(t, e.board)
	if len(c.Items) != 2 {
		t.Fatalf("items %+v", c.Items)
	}
	name := c.Items[0].ProjectName
	if strings.ContainsAny(name, "\u202E\x1b") || !strings.HasPrefix(name, "evil[31m") || len([]rune(name)) > maxText+1 {
		t.Fatalf("hostile project name %q", name)
	}
	if c.Items[1].ProjectName != "unknown folder" || c.Items[1].ProjectName != e.board.Snapshot().Projects[0].Name {
		t.Fatalf("folderless project name %q, board says %q", c.Items[1].ProjectName, e.board.Snapshot().Projects[0].Name)
	}
}

// The marker goes to the time the page sends, never past now and never back.
func TestMarkCatchupReadClampsAndNeverMovesBack(t *testing.T) {
	e := newEnv(t)
	if err := e.board.MarkCatchupRead(tNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if m := e.state.CatchupMarker(); !m.Equal(tNow.Add(-time.Hour)) {
		t.Fatalf("marker %v, want the time sent", m)
	}
	// A second tab with an older list.
	if err := e.board.MarkCatchupRead(tNow.Add(-2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if m := e.state.CatchupMarker(); !m.Equal(tNow.Add(-time.Hour)) {
		t.Fatalf("marker moved back to %v", m)
	}
	// A page clock ahead of the server's.
	if err := e.board.MarkCatchupRead(tNow.Add(3 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if m := e.state.CatchupMarker(); !m.Equal(tNow) {
		t.Fatalf("marker %v, want clamped to now %v", m, tNow)
	}
}

// An item newer than the marker a run started with stays unread, though it
// happened before the start.
func TestCatchupKeepsWhatCameAfterTheStartMarker(t *testing.T) {
	e := newEnv(t)
	if err := e.state.SetCatchupMarker(tNow.Add(-30 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	b := e.restart(t)
	s := session("s1", model.StatusFinished, tNow)
	s.TurnEnds = []time.Time{tNow.Add(-30 * time.Minute), tNow.Add(-5 * time.Minute)}
	b.applySync(s)
	if got := kindsAt(catchup(t, b)); !slices.Equal(got, []string{"turn_finished@11-17T09:55"}) {
		t.Fatalf("items %v", got)
	}
}

// Review of #82, A: "Caught up" pressed while the sessions that had ended
// before the start are still being read does not mark them read unseen.
func TestCatchupBackfillAfterCaughtUpStaysUnread(t *testing.T) {
	e := newEnv(t)
	if err := e.state.SetCatchupMarker(tNow.Add(-10 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	b := e.restart(t)
	b.SetSources([]source.Status{{Kind: source.KindClaude, State: source.StateOK}})
	live := session("live", model.StatusFinished, tNow)
	live.TurnEnds = []time.Time{tNow.Add(time.Second)}
	e.clock.Set(tNow.Add(3 * time.Second))
	b.applySync(live)
	shown := catchup(t, b)
	if !shown.Pending {
		t.Fatal("not pending while the backfill runs")
	}
	if err := b.MarkCatchupRead(shown.AsOf); err != nil {
		t.Fatal(err)
	}
	// The backlog arrives: a session that ended three hours before the start.
	old := session("old", model.StatusFinished, tNow.Add(-3*time.Hour))
	old.TurnEnds = []time.Time{tNow.Add(-3 * time.Hour)}
	old.Live, old.Ended, old.EndedAt, old.Backfill = model.Of(false), true, tNow.Add(-3*time.Hour), true
	e.clock.Set(tNow.Add(8 * time.Second))
	b.applySync(old)
	b.applySync(model.BackfillDone{Kind: string(source.KindClaude)})
	got := catchup(t, b)
	if k := kindsAt(got); !slices.Equal(k, []string{"turn_finished@11-17T07:00"}) || got.Pending {
		t.Fatalf("items %v, pending %v", k, got.Pending)
	}
}

// The same for a live session whose long transcript is read after the first
// list: its turns from before the start stay unread.
func TestCatchupLiveTurnsReadLateStayUnread(t *testing.T) {
	e := newEnv(t)
	if err := e.state.SetCatchupMarker(tNow.Add(-10 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	b := e.restart(t)
	e.clock.Set(tNow.Add(3 * time.Second))
	shown := catchup(t, b) // nothing yet: the transcript is still being read
	if err := b.MarkCatchupRead(shown.AsOf); err != nil {
		t.Fatal(err)
	}
	s := session("s1", model.StatusBusy, tNow)
	s.TurnEnds = []time.Time{tNow.Add(-2 * time.Hour)}
	b.applySync(s)
	if k := kindsAt(catchup(t, b)); !slices.Equal(k, []string{"turn_finished@11-17T08:00"}) {
		t.Fatalf("items %v", k)
	}
}

// Pending only while Claude Code is there and its backfill has not ended.
func TestCatchupPending(t *testing.T) {
	e := newEnv(t)
	if catchup(t, e.board).Pending {
		t.Fatal("pending with no Claude Code source")
	}
	e.board.SetSources([]source.Status{{Kind: source.KindClaude, State: source.StateMissing}})
	if catchup(t, e.board).Pending {
		t.Fatal("pending with Claude Code missing")
	}
	e.board.SetSources([]source.Status{{Kind: source.KindClaude, State: source.StateOK}})
	if !catchup(t, e.board).Pending {
		t.Fatal("not pending")
	}
	e.board.applySync(model.BackfillDone{Kind: string(source.KindClaude)})
	if catchup(t, e.board).Pending {
		t.Fatal("still pending")
	}
}

// Artifacts the session published in the window are listed once each, with
// a checked address; one with an address outside the allowed form is not.
func TestCatchupListsPublishedArtifacts(t *testing.T) {
	e := newEnv(t)
	art := func(n int) string { return fmt.Sprintf("https://claude.ai/artifact/exampleArtifact%04d", n) }
	for _, id := range []string{"s1", "s2"} {
		s := session(id, model.StatusFinished, tNow)
		s.Artifacts = []model.ArtifactRef{
			{URL: art(1), Title: "\u202ESample plan\x1b[31m", PublishedAt: tNow.Add(-10 * time.Minute)},
			{URL: art(2), Title: "Old", PublishedAt: tNow.Add(-8 * 24 * time.Hour)},
			{URL: "https://evil.example/artifact/x", Title: "Bad", PublishedAt: tNow.Add(-time.Minute)},
			{URL: art(3), Title: "No time"},
		}
		e.board.applySync(s)
	}
	c := catchup(t, e.board)
	if len(c.Items) != 1 {
		t.Fatalf("items %+v", c.Items)
	}
	it := c.Items[0]
	if it.Kind != contract.CatchupArtifactPublished || it.Artifact == nil || it.Artifact.URL != art(1) || it.PullRequest != nil {
		t.Fatalf("item %+v", it)
	}
	if strings.ContainsAny(it.Title, "\u202E\x1b") || it.Title != it.Artifact.Title {
		t.Fatalf("title %q, artifact title %q", it.Title, it.Artifact.Title)
	}
}

// Finding 1 of the review of #80 (its scratch test, with the list's AsOf sent
// back as the page does): a merge GitHub reports only at the next lookup is
// older than a turn the owner already marked read; it must still be shown.
func TestCatchupShowsAMergeGitHubReportedLate(t *testing.T) {
	e := newEnv(t)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	e.prs.prs = []model.PullRequest{prAt(9, model.PRStateOpen, tNow.Add(-3*time.Hour), time.Time{})}
	worker := session("w", model.StatusFinished, tNow.Add(-2*time.Hour))
	worker.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 9}}
	e.board.applySync(worker)
	e.board.tick() // pull request 9 known as open
	// The coordinator merges it at now+10s; its own turn ends at now+20s.
	e.prs.mu.Lock()
	e.prs.prs = []model.PullRequest{prAt(9, model.PRStateMerged, tNow.Add(-3*time.Hour), tNow.Add(10*time.Second))}
	e.prs.mu.Unlock()
	coord := session("c", model.StatusFinished, tNow)
	coord.TurnEnds = []time.Time{tNow.Add(20 * time.Second)}
	e.clock.Set(tNow.Add(30 * time.Second))
	e.board.applySync(coord)
	shown := catchup(t, e.board)
	if err := e.board.MarkCatchupRead(shown.AsOf); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(tNow.Add(githubEvery + time.Second))
	e.board.tick() // the periodic lookup learns of the merge
	got := catchup(t, e.board)
	if len(got.Items) != 1 || got.Items[0].Kind != contract.CatchupPRMerged || !got.Items[0].At.Equal(tNow.Add(10*time.Second)) {
		t.Fatalf("after the lookup: %v, want only the merge, at its own time", kindsAt(got))
	}
}

// GitHub out (gh failing) while the owner marks newer turns read: what it
// reports once it is back is shown, by its own times.
func TestCatchupShowsWhatGitHubReportsAfterAnOutage(t *testing.T) {
	e := newEnv(t)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	e.prs.err = errors.New("github: gh failed")
	w := session("w", model.StatusFinished, tNow)
	w.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 4}}
	e.board.applySync(w)
	e.clock.Set(tNow.Add(5 * time.Minute))
	e.board.tick()
	c := session("c", model.StatusFinished, tNow)
	c.TurnEnds = []time.Time{tNow.Add(4 * time.Minute)}
	e.board.applySync(c)
	shown := catchup(t, e.board)
	if got := kindsAt(shown); !slices.Equal(got, []string{"turn_finished@11-17T10:04"}) {
		t.Fatalf("during the outage: %v", got)
	}
	if err := e.board.MarkCatchupRead(shown.AsOf); err != nil {
		t.Fatal(err)
	}
	// Back: pull request 4 was opened at +1m and merged at +2m, both before
	// the turn that was marked read.
	e.prs.mu.Lock()
	e.prs.err = nil
	e.prs.prs = []model.PullRequest{prAt(4, model.PRStateMerged, tNow.Add(time.Minute), tNow.Add(2*time.Minute))}
	e.prs.mu.Unlock()
	e.clock.Set(tNow.Add(30 * time.Minute))
	e.board.tick()
	if got := kindsAt(catchup(t, e.board)); !slices.Equal(got, []string{"pr_merged@11-17T10:02", "pr_opened@11-17T10:01"}) {
		t.Fatalf("after the outage: %v", got)
	}
}

// Lists made at the same instant still order: the AsOf the page sends back
// covers the list it showed, and an item learned in the same instant after it
// is neither lost nor shown twice.
func TestCatchupEqualTimesAtTheBoundary(t *testing.T) {
	e := newEnv(t)
	e.clock.Set(tNow.Add(time.Hour))
	a := session("a", model.StatusFinished, tNow)
	a.TurnEnds = []time.Time{tNow.Add(30 * time.Minute)}
	e.board.applySync(a)
	first := catchup(t, e.board)
	// The clock has not moved; a turn with the very same time arrives.
	b := session("b", model.StatusFinished, tNow)
	b.TurnEnds = []time.Time{tNow.Add(30 * time.Minute)}
	e.board.applySync(b)
	if err := e.board.MarkCatchupRead(first.AsOf); err != nil {
		t.Fatal(err)
	}
	second := catchup(t, e.board)
	if !second.AsOf.After(first.AsOf) {
		t.Fatalf("as of %v, then %v: not later", first.AsOf, second.AsOf)
	}
	if len(second.Items) != 1 || second.Items[0].AgentName != "worker-b" {
		t.Fatalf("after marking the first list: %+v", second.Items)
	}
	if err := e.board.MarkCatchupRead(second.AsOf); err != nil {
		t.Fatal(err)
	}
	if n := len(catchup(t, e.board).Items); n != 0 {
		t.Fatalf("%d items shown twice", n)
	}
}

// After a restart what the earlier run's marker covered stays read; what
// happened after it is shown.
func TestCatchupRestartDoesNotListReadItemsAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	open := func(at time.Time) (*Board, *fakeClock) {
		st, err := state.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		clock := &fakeClock{t: at}
		return newBoard(t, Options{Repos: &fakeRepos{repos: map[string]model.Repo{}}, PRs: &fakePRs{}, State: st, Now: clock.Now, StaleAfter: 20 * time.Minute}), clock
	}
	t.Setenv("HOME", testHome)
	s := session("s1", model.StatusFinished, tNow)
	s.TurnEnds = []time.Time{tNow.Add(-2 * time.Hour), tNow.Add(-time.Hour)}

	first, clock := open(tNow)
	first.applySync(s)
	clock.Set(tNow.Add(time.Minute))
	shown := catchup(t, first)
	if len(shown.Items) != 2 {
		t.Fatalf("first run: %v", kindsAt(shown))
	}
	if err := first.MarkCatchupRead(shown.AsOf); err != nil {
		t.Fatal(err)
	}
	first.markClosed()

	// The next run: one more turn ended while sitboard was off.
	s.TurnEnds = append(s.TurnEnds, tNow.Add(30*time.Minute))
	second, _ := open(tNow.Add(time.Hour))
	second.applySync(s)
	if got := kindsAt(catchup(t, second)); !slices.Equal(got, []string{"turn_finished@11-17T10:30"}) {
		t.Fatalf("after the restart: %v, want only the turn after the marker", got)
	}
}

// A time later than any list made covers nothing more than the newest list.
func TestMarkCatchupReadNeverCoversMoreThanTheNewestList(t *testing.T) {
	e := newEnv(t)
	e.clock.Set(tNow.Add(time.Hour))
	s := session("s1", model.StatusFinished, tNow)
	s.TurnEnds = []time.Time{tNow.Add(10 * time.Minute)}
	e.board.applySync(s)
	shown := catchup(t, e.board)
	s.TurnEnds = append(s.TurnEnds, tNow.Add(20*time.Minute))
	e.clock.Set(tNow.Add(2 * time.Hour))
	e.board.applySync(s)
	if err := e.board.MarkCatchupRead(tNow.Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if m := e.state.CatchupMarker(); !m.Equal(shown.AsOf) {
		t.Fatalf("marker %v, want the newest list's %v", m, shown.AsOf)
	}
	if got := kindsAt(catchup(t, e.board)); !slices.Equal(got, []string{"turn_finished@11-17T10:20"}) {
		t.Fatalf("items %v", got)
	}
}

// A shared pull request is credited to the same session every time.
func TestCatchupCreditsASharedPullRequestStably(t *testing.T) {
	for range 20 {
		e := newEnv(t)
		e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
		e.prs.prs = []model.PullRequest{prAt(7, model.PRStateOpen, tNow.Add(-time.Hour), time.Time{})}
		for _, id := range []string{"s9", "s1", "s5", "s3"} {
			s := session(id, model.StatusFinished, tNow)
			s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 7}}
			e.board.applySync(s)
		}
		e.board.tick()
		c := catchup(t, e.board)
		if len(c.Items) != 1 || c.Items[0].AgentName != "worker-s1" {
			t.Fatalf("credited to %+v, want worker-s1 every time", c.Items)
		}
	}
}

// An ended session keeps its own name and title: the agterm pane it ran in
// may run another session now.
func TestCatchupEndedSessionDoesNotBorrowItsOldTerminal(t *testing.T) {
	e := newEnv(t)
	s := session("s1", model.StatusFinished, tNow)
	s.Name = ""
	s.TerminalID = termUUID
	s.TurnEnds = []time.Time{tNow.Add(-time.Minute)}
	s.Live, s.Ended, s.EndedAt = model.Of(false), true, tNow
	e.board.applySync(model.TerminalFact{ID: termUUID, Workspace: "alpha", Name: "someone-else", Title: "✳ Another task", HasAgent: true})
	e.board.applySync(s)
	c := catchup(t, e.board)
	if len(c.Items) != 1 || c.Items[0].AgentName != "" || strings.Contains(c.Items[0].Title, "Another") {
		t.Fatalf("item %+v borrowed the pane's name or title", c.Items)
	}
}

// Past the cap, Total says how many there were.
func TestCatchupTotalCountsWhatTheCapLeftOut(t *testing.T) {
	e := newEnv(t)
	s := session("s1", model.StatusFinished, tNow)
	for i := range maxCatchupItems + 5 {
		s.TurnEnds = append(s.TurnEnds, tNow.Add(-time.Duration(i+1)*time.Minute))
	}
	e.board.applySync(s)
	c := catchup(t, e.board)
	if len(c.Items) != maxCatchupItems || c.Total != maxCatchupItems+5 {
		t.Fatalf("%d items, total %d", len(c.Items), c.Total)
	}
}

// A hostile pull request: its title and repo are cleaned, and its address is
// passed on only when it is the one built for its own repo and number.
func TestCatchupCleansAHostilePullRequest(t *testing.T) {
	e := newEnv(t)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	bad := prAt(3, model.PRStateOpen, tNow.Add(-time.Hour), time.Time{})
	bad.Title = "\u202Eevil\x1b[31m title"
	bad.URL = "javascript:alert(1)"
	good := prAt(4, model.PRStateOpen, tNow.Add(-2*time.Hour), time.Time{})
	e.prs.prs = []model.PullRequest{bad, good}
	s := session("s1", model.StatusFinished, tNow)
	s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 3}, {Repo: model.GitHubRepo(exRepo), Number: 4}}
	e.board.applySync(s)
	e.board.tick()
	c := catchup(t, e.board)
	if len(c.Items) != 2 {
		t.Fatalf("items %+v", c.Items)
	}
	p := c.Items[0].PullRequest
	if strings.ContainsAny(p.Title+c.Items[0].Title, "\u202E\x1b") || p.URL != "" {
		t.Fatalf("hostile pull request passed on: %+v / %q", p, c.Items[0].Title)
	}
	if u := c.Items[1].PullRequest.URL; u != "https://github.com/example-org/example-repo/pull/4" {
		t.Fatalf("good address %q", u)
	}
}

// What the board learned is forgotten once it falls out of the window.
func TestCatchupForgetsLearnedItemsOutsideTheWindow(t *testing.T) {
	e := newEnv(t)
	s := session("s1", model.StatusFinished, tNow)
	s.TurnEnds = []time.Time{tNow.Add(-time.Hour)}
	e.board.applySync(s)
	catchup(t, e.board)
	if n := len(e.board.learned); n != 1 {
		t.Fatalf("%d learned, want 1", n)
	}
	e.clock.Set(tNow.Add(catchupWindow))
	catchup(t, e.board)
	if n := len(e.board.learned); n != 0 {
		t.Fatalf("%d learned after the window passed, want 0", n)
	}
}

// pagePR cleans every field it passes on. A hostile repo name cannot reach it
// through a lookup (only valid repos are asked, and answers for another repo
// are dropped); this is the second line.
func TestPagePRCleansTheRepoName(t *testing.T) {
	p := pagePR(model.PullRequest{Repo: model.GitHubRepo("\u202Eexample-org/x\x1b[31m"), Number: 1, Title: "t"})
	if strings.ContainsAny(p.Repo, "\u202E\x1b") || p.URL != "" {
		t.Fatalf("passed on %+v", p)
	}
}

// A repo whose sessions have all ended is asked every endedEvery, not every
// minute: about 1,000 calls in 7 days instead of 10,000.
func TestEndedOnlyRepoIsAskedEveryTenMinutes(t *testing.T) {
	e := newEnv(t)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	e.prs.prs = []model.PullRequest{prAt(1, model.PRStateOpen, tNow.Add(-time.Hour), time.Time{})}
	s := session("s1", model.StatusFinished, tNow)
	s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 1}}
	s.Live, s.Ended, s.EndedAt = model.Of(false), true, tNow
	e.board.applySync(s)
	for m := 0; m <= 25; m++ {
		e.clock.Set(tNow.Add(time.Duration(m)*time.Minute + time.Second))
		e.board.tick()
	}
	if n := e.prs.callsFor(exRepo); n != 3 {
		t.Fatalf("%d calls in 25 minutes, want 3 (at 0, 10 and 20)", n)
	}
}

// A pull request GitHub did not find, or any in a repo it did not find, is
// not asked about again for an ended session.
func TestEndedSessionsStopAskingWhatGitHubDidNotFind(t *testing.T) {
	t.Run("number", func(t *testing.T) {
		e := newEnv(t)
		e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
		s := session("s1", model.StatusFinished, tNow)
		s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 404}}
		s.Live, s.Ended, s.EndedAt = model.Of(false), true, tNow
		e.board.applySync(s)
		e.board.tick()
		e.clock.Set(tNow.Add(endedEvery + time.Minute))
		e.board.tick()
		if n := e.prs.callsFor(exRepo); n != 1 {
			t.Fatalf("%d calls, want 1", n)
		}
	})
	t.Run("repo", func(t *testing.T) {
		e := newEnv(t)
		e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
		e.prs.err = fmt.Errorf("github: %w", source.ErrRepoNotFound)
		s := session("s1", model.StatusFinished, tNow)
		s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 1}, {Repo: model.GitHubRepo(exRepo), Number: 2}}
		s.Live, s.Ended, s.EndedAt = model.Of(false), true, tNow
		e.board.applySync(s)
		e.board.tick()
		for m := 1; m <= 3; m++ {
			e.clock.Set(tNow.Add(time.Duration(m) * (endedEvery + time.Minute)))
			e.board.tick()
		}
		if n := e.prs.callsFor(exRepo); n != 1 {
			t.Fatalf("%d calls, want 1", n)
		}
	})
	t.Run("a live session still asks", func(t *testing.T) {
		e := newEnv(t)
		e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
		s := session("s1", model.StatusFinished, tNow)
		s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 404}}
		e.board.applySync(s)
		e.board.tick()
		e.clock.Set(tNow.Add(githubEvery + time.Second))
		e.board.tick()
		if n := e.prs.callsFor(exRepo); n != 2 {
			t.Fatalf("%d calls, want 2", n)
		}
	})
}

// A board whose state lives in memory says so in every list.
func TestCatchupSaysTheMarkerIsNotSaved(t *testing.T) {
	clock := &fakeClock{t: tNow}
	b := newBoard(t, Options{Repos: &fakeRepos{repos: map[string]model.Repo{}}, State: state.Memory(), StateInMemory: true, Now: clock.Now, StaleAfter: 20 * time.Minute})
	if !catchup(t, b).Unsaved {
		t.Fatal("unsaved not said")
	}
	e := newEnv(t)
	if catchup(t, e.board).Unsaved {
		t.Fatal("a board with a state file says unsaved")
	}
}

// A republished artifact is a new item at once, though its address was read
// (review of #80, A).
func TestCatchupShowsARepublishedArtifactAtOnce(t *testing.T) {
	e := newEnv(t)
	s := session("s1", model.StatusFinished, tNow)
	s.Artifacts = []model.ArtifactRef{{URL: "https://claude.ai/artifact/exampleArtifact0001", Title: "v1", PublishedAt: tNow.Add(-6 * 24 * time.Hour)}}
	e.board.applySync(s)
	shown := catchup(t, e.board)
	if len(shown.Items) != 1 {
		t.Fatalf("first: %v", kindsAt(shown))
	}
	if err := e.board.MarkCatchupRead(shown.AsOf); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(tNow.Add(time.Hour))
	s.Artifacts = []model.ArtifactRef{{URL: "https://claude.ai/artifact/exampleArtifact0001", Title: "v2", PublishedAt: tNow.Add(30 * time.Minute)}}
	e.board.applySync(s)
	got := catchup(t, e.board)
	if len(got.Items) != 1 || got.Items[0].Title != "v2" || !got.Items[0].At.Equal(tNow.Add(30*time.Minute)) {
		t.Fatalf("after the republish: %+v", got.Items)
	}
}

// A saved marker ahead of the clock at start (the clock was stepped back):
// what the board learns before the clock passes it is still unread (review
// of #80, B).
func TestCatchupClockBehindTheSavedMarker(t *testing.T) {
	t.Setenv("HOME", testHome)
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetCatchupMarker(tNow.Add(10 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{t: tNow}
	b := newBoard(t, Options{Repos: &fakeRepos{repos: map[string]model.Repo{}}, PRs: &fakePRs{}, State: st, Now: clock.Now, StaleAfter: 20 * time.Minute})
	s := session("s1", model.StatusFinished, tNow)
	s.TurnEnds = []time.Time{tNow.Add(time.Minute)}
	clock.Set(tNow.Add(2 * time.Minute))
	b.applySync(s)
	first := catchup(t, b)
	if !first.AsOf.After(tNow.Add(10 * time.Minute)) {
		t.Fatalf("as of %v, not past the saved marker", first.AsOf)
	}
	clock.Set(tNow.Add(20 * time.Minute))
	if got := kindsAt(catchup(t, b)); !slices.Equal(got, []string{"turn_finished@11-17T10:01"}) {
		t.Fatalf("once the clock passed the marker: %v, want the turn", got)
	}
}

// Review of #82, 3: repos with live work are asked first; the backlog of
// ended sessions after, newest first.
func TestLiveReposAreAskedFirst(t *testing.T) {
	e := newEnv(t)
	endedAt := func(id, repo string, at time.Time) model.SessionFact {
		s := session(id, model.StatusFinished, at)
		s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(repo), Number: 1}}
		s.Live, s.Ended, s.EndedAt = model.Of(false), true, at
		return s
	}
	e.board.applySync(endedAt("e1", "example-org/aaa", tNow.Add(-3*time.Hour)))
	e.board.applySync(endedAt("e2", "example-org/bbb", tNow.Add(-time.Hour)))
	live := session("l1", model.StatusBusy, tNow)
	live.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo("example-org/zzz"), Number: 1}}
	e.board.applySync(live)
	e.board.tick()
	var order []string
	for _, c := range e.prs.calls {
		order = append(order, c.repo)
	}
	if !slices.Equal(order, []string{"example-org/zzz", "example-org/bbb", "example-org/aaa"}) {
		t.Fatalf("order %v", order)
	}
}

// At most maxEndedRepos repos are asked about only for ended sessions: the
// newest.
func TestEndedOnlyReposAreCapped(t *testing.T) {
	e := newEnv(t)
	for i := range maxEndedRepos + 5 {
		s := session(fmt.Sprintf("e%02d", i), model.StatusFinished, tNow)
		s.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(fmt.Sprintf("example-org/r%02d", i)), Number: 1}}
		// The newest have the highest numbers: name order is not age order.
		s.Live, s.Ended, s.EndedAt = model.Of(false), true, tNow.Add(-time.Duration(maxEndedRepos+5-i)*time.Minute)
		e.board.applySync(s)
	}
	e.board.tick()
	asked := map[string]bool{}
	for _, c := range e.prs.calls {
		asked[c.repo] = true
	}
	if len(asked) != maxEndedRepos || !asked[fmt.Sprintf("example-org/r%02d", maxEndedRepos+4)] || asked["example-org/r04"] {
		t.Fatalf("asked %d repos: %v", len(asked), asked)
	}
}

// A headless run (claude -p, an SDK) is not in catch-up, live or ended.
func TestCatchupLeavesOutHeadlessRuns(t *testing.T) {
	e := newEnv(t)
	s := session("p1", model.StatusBusy, tNow)
	s.TurnEnds, s.Headless = []time.Time{tNow.Add(-time.Minute)}, true
	e.board.applySync(s)
	if n := len(catchup(t, e.board).Items); n != 0 {
		t.Fatalf("%d items", n)
	}
	if _, ok := cardByID(t, e.board, "claude:p1"); !ok {
		t.Fatal("the board changed: no card")
	}
}

// Review of #82 (second round), 1: "Caught up" pressed while the backlog is
// read, a backfilled turn arrives, sitboard restarts before the owner looks:
// the turn is still unread in the next run, and read once marked.
func TestCatchupUnreadSurvivesARestartMidBacklog(t *testing.T) {
	e := newEnv(t)
	if err := e.state.SetCatchupMarker(tNow.Add(-10 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	b := e.restart(t)
	b.SetSources([]source.Status{{Kind: source.KindClaude, State: source.StateOK}})
	e.clock.Set(tNow.Add(3 * time.Second))
	if err := b.MarkCatchupRead(catchup(t, b).AsOf); err != nil {
		t.Fatal(err)
	}
	old := session("old", model.StatusFinished, tNow.Add(-3*time.Hour))
	old.TurnEnds = []time.Time{tNow.Add(-3 * time.Hour)}
	old.Live, old.Ended, old.EndedAt, old.Backfill = model.Of(false), true, tNow.Add(-3*time.Hour), true
	e.clock.Set(tNow.Add(8 * time.Second))
	b.applySync(old)
	e.clock.Set(tNow.Add(10 * time.Second))
	b.tick() // no list: the clock learns it
	b.markClosed()

	e.clock.Set(tNow.Add(time.Minute))
	second := e.restart(t)
	second.applySync(old)
	shown := catchup(t, second)
	if k := kindsAt(shown); !slices.Equal(k, []string{"turn_finished@11-17T07:00"}) {
		t.Fatalf("after the restart: %v", k)
	}
	if err := second.MarkCatchupRead(shown.AsOf); err != nil {
		t.Fatal(err)
	}
	if n := len(e.state.CatchupUnread()); n != 0 {
		t.Fatalf("%d still saved as unread after it was read", n)
	}
	second.markClosed()
	third := e.restart(t)
	third.applySync(old)
	if k := kindsAt(catchup(t, third)); len(k) != 0 {
		t.Fatalf("read, yet listed again: %v", k)
	}
}

// A merge GitHub reports after "Caught up" (older than the marker) is still
// unread after a restart.
func TestCatchupLateMergeSurvivesARestart(t *testing.T) {
	e := newEnv(t)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Remote: model.GitHubRepo(exRepo), Branch: model.Of("main")})
	e.prs.prs = []model.PullRequest{prAt(9, model.PRStateOpen, tNow.Add(-3*time.Hour), time.Time{})}
	worker := session("w", model.StatusFinished, tNow.Add(-2*time.Hour))
	worker.CreatedPRs = []model.PRRef{{Repo: model.GitHubRepo(exRepo), Number: 9}}
	e.board.applySync(worker)
	e.board.tick()
	e.prs.mu.Lock()
	e.prs.prs = []model.PullRequest{prAt(9, model.PRStateMerged, tNow.Add(-3*time.Hour), tNow.Add(10*time.Second))}
	e.prs.mu.Unlock()
	e.clock.Set(tNow.Add(30 * time.Second))
	if err := e.board.MarkCatchupRead(catchup(t, e.board).AsOf); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(tNow.Add(githubEvery + time.Second))
	e.board.tick() // the lookup learns of the merge
	e.clock.Set(tNow.Add(githubEvery + 11*time.Second))
	e.board.tick() // and the clock learns the item
	e.board.markClosed()

	e.clock.Set(tNow.Add(time.Hour))
	second := e.restart(t)
	second.applySync(worker)
	second.tick()
	got := catchup(t, second)
	if len(got.Items) != 1 || got.Items[0].Kind != contract.CatchupPRMerged {
		t.Fatalf("after the restart: %v", kindsAt(got))
	}
}

// Saved as unread, then a run that marks a list before that session is read
// again (the backlog still being read) keeps it saved.
func TestCatchupUnreadKeptUntilItIsLearnedAgain(t *testing.T) {
	e := newEnv(t)
	k := state.CatchupKey(fmt.Sprintf("turn|claude|old|%d", tNow.Add(-3*time.Hour).UnixNano()))
	if err := e.state.SetCatchup(tNow.Add(time.Minute), map[string]time.Time{k: tNow.Add(-3 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	e.clock.Set(tNow.Add(time.Hour))
	b := e.restart(t)
	if err := b.MarkCatchupRead(catchup(t, b).AsOf); err != nil {
		t.Fatal(err)
	}
	b.markClosed()
	third := e.restart(t)
	old := session("old", model.StatusFinished, tNow.Add(-3*time.Hour))
	old.TurnEnds = []time.Time{tNow.Add(-3 * time.Hour)}
	third.applySync(old)
	if n := len(catchup(t, third).Items); n != 1 {
		t.Fatalf("%d items", n)
	}
}

// Before any list of this run, the clock learns nothing: a mark then (a tab
// left from the last run) cannot cover what no list showed.
func TestCatchupNothingLearnedBeforeTheFirstList(t *testing.T) {
	e := newEnv(t)
	s := session("s1", model.StatusFinished, tNow)
	s.TurnEnds = []time.Time{tNow.Add(-time.Minute)}
	e.board.applySync(s)
	e.clock.Set(tNow.Add(10 * time.Second))
	e.board.tick()
	if err := e.board.MarkCatchupRead(tNow.Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if n := len(catchup(t, e.board).Items); n != 1 {
		t.Fatalf("%d items", n)
	}
}

// What the clock learns comes after every list made and after the marker,
// even when the clock reads the same as then: a list that did not hold it
// never covers it.
func TestCatchupClockLearnsAfterListsAndMarker(t *testing.T) {
	turn := func(b *Board) {
		s := session("s1", model.StatusFinished, tNow)
		s.TurnEnds = []time.Time{tNow.Add(-time.Minute)}
		b.applySync(s)
	}
	t.Run("after a list", func(t *testing.T) {
		e := newEnv(t)
		e.clock.Set(tNow.Add(10 * time.Second))
		shown := catchup(t, e.board)
		turn(e.board)
		e.board.tick() // same clock reading as the list
		if err := e.board.MarkCatchupRead(shown.AsOf); err != nil {
			t.Fatal(err)
		}
		e.clock.Set(tNow.Add(11 * time.Second))
		if n := len(catchup(t, e.board).Items); n != 1 {
			t.Fatalf("%d items", n)
		}
	})
	t.Run("after a mark", func(t *testing.T) {
		e := newEnv(t)
		e.clock.Set(tNow.Add(10 * time.Second))
		if err := e.board.MarkCatchupRead(tNow.Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
		turn(e.board)
		e.board.tick() // same clock reading as the mark
		e.clock.Set(tNow.Add(11 * time.Second))
		if n := len(catchup(t, e.board).Items); n != 1 {
			t.Fatalf("%d items", n)
		}
	})
}

// catchupWrites counts the catch-up writes.
type catchupWrites struct {
	state.Store
	mu     sync.Mutex
	writes int
}

func (c *catchupWrites) SetCatchup(m time.Time, u map[string]time.Time) error {
	c.mu.Lock()
	c.writes++
	c.mu.Unlock()
	return c.Store.SetCatchup(m, u)
}

// What the clock learns is saved in one write per tick, not one per item,
// and only when it holds something new.
func TestCatchupUnreadIsOneWritePerTick(t *testing.T) {
	e := newEnv(t)
	st := &catchupWrites{Store: e.state}
	b := newBoard(t, Options{Repos: e.repos, PRs: e.prs, State: st, Now: e.clock.Now, StaleAfter: 20 * time.Minute})
	e.clock.Set(tNow.Add(10 * time.Second))
	if err := b.MarkCatchupRead(catchup(t, b).AsOf); err != nil {
		t.Fatal(err)
	}
	s := session("s1", model.StatusFinished, tNow)
	s.TurnEnds = []time.Time{tNow.Add(-3 * time.Minute), tNow.Add(-2 * time.Minute), tNow.Add(-time.Minute)}
	b.applySync(s)
	st.writes = 0
	e.clock.Set(tNow.Add(20 * time.Second))
	b.tick()
	e.clock.Set(tNow.Add(30 * time.Second))
	b.tick() // nothing new
	if st.writes != 1 || len(e.state.CatchupUnread()) != 3 {
		t.Fatalf("%d writes, %d saved", st.writes, len(e.state.CatchupUnread()))
	}
}

// When sitboard stops, what arrived since the last tick is saved.
func TestCatchupUnreadSavedAtStop(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = e.board.Run(ctx); close(done) }()
	time.Sleep(100 * time.Millisecond) // past Run's first tick
	e.clock.Set(tNow.Add(10 * time.Second))
	if err := e.board.MarkCatchupRead(catchup(t, e.board).AsOf); err != nil {
		t.Fatal(err)
	}
	s := session("s1", model.StatusFinished, tNow)
	s.TurnEnds = []time.Time{tNow.Add(-time.Minute)}
	e.board.applySync(s)
	cancel()
	<-done
	if n := len(e.state.CatchupUnread()); n != 1 {
		t.Fatalf("%d saved", n)
	}
}

// From sb-rev82's review: a clock stepped back after a tick learned an item.
// The list shows it, and "Caught up" on that list clears it.
func TestCatchupClockBackAfterTick(t *testing.T) {
	e := newEnv(t)
	if err := e.state.SetCatchupMarker(tNow.Add(-10 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	b := e.restart(t)
	b.applySync(session("live1", model.StatusFinished, tNow))
	e.clock.Set(tNow.Add(3 * time.Second))
	if err := b.MarkCatchupRead(catchup(t, b).AsOf); err != nil {
		t.Fatal(err)
	}
	old := session("ended1", model.StatusFinished, tNow.Add(-3*time.Hour))
	old.Live, old.Ended, old.EndedAt, old.Backfill = model.Of(false), true, tNow.Add(-3*time.Hour), true
	old.TurnEnds = []time.Time{tNow.Add(-3 * time.Hour)}
	b.applySync(old)
	e.clock.Set(tNow.Add(time.Hour))
	b.learnTick()                           // learned at now+1h
	e.clock.Set(tNow.Add(10 * time.Second)) // the clock steps back
	shown := catchup(t, b)
	if len(shown.Items) != 1 {
		t.Fatalf("shown %v", kindsAt(shown))
	}
	if err := b.MarkCatchupRead(shown.AsOf); err != nil {
		t.Fatal(err)
	}
	if after := catchup(t, b); len(after.Items) != 0 {
		t.Fatalf("still listed after Caught up on the list that showed it: %v", kindsAt(after))
	}
}

// A save the store refused (it is closed) is not taken as saved.
func TestCatchupUnreadNotTakenAsSavedAfterClose(t *testing.T) {
	e := newEnv(t)
	e.clock.Set(tNow.Add(10 * time.Second))
	if err := e.board.MarkCatchupRead(catchup(t, e.board).AsOf); err != nil {
		t.Fatal(err)
	}
	s := session("s1", model.StatusFinished, tNow)
	s.TurnEnds = []time.Time{tNow.Add(-time.Minute)}
	e.board.applySync(s)
	e.state.Close()
	e.clock.Set(tNow.Add(20 * time.Second))
	e.board.learnTick()
	if len(e.board.unreadSaved) != 0 {
		t.Fatalf("taken as saved: %v", e.board.unreadSaved)
	}
}
