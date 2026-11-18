package core

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

func TestEmptySnapshotHasArraysNotNil(t *testing.T) {
	s := newTestBoard(t).Snapshot()
	if s.Version != contract.Version {
		t.Fatalf("version %d", s.Version)
	}
	if s.Cards == nil || s.Projects == nil || s.Sources == nil {
		t.Fatal("snapshot slices must be empty, not nil")
	}
}

// keysOf lists the JSON object keys at every level, so two documents can be
// compared by shape rather than by values.
func keysOf(v any, path string, out map[string]bool) {
	switch v := v.(type) {
	case map[string]any:
		for k, c := range v {
			out[path+"."+k] = true
			keysOf(c, path+"."+k, out)
		}
	case []any:
		for _, c := range v {
			keysOf(c, path+"[]", out)
		}
	}
}

func TestSnapshotMatchesContractGoldenShape(t *testing.T) {
	e := newEnv(t)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Slug: exRepo, Branch: model.Of("main")})
	e.prs.prs = []model.PullRequest{{Repo: exRepo, Number: 101, Title: "t", URL: "https://github.com/example-org/example-repo/pull/101", State: model.PRStateOpen, Checks: model.ChecksFailing, ClosingIssues: []int{7}}}
	s := session("s1", model.StatusFinished, tNow)
	s.CreatedPRs = []model.PRRef{{Repo: exRepo, Number: 101}}
	s.Artifacts = []model.ArtifactRef{{URL: "https://claude.ai/artifact/exampleArtifact0001", Title: "Placeholder", PublishedAt: tNow}}
	s.TerminalID = termUUID
	s.Parent = model.Of(claudeKey("c"))
	e.board.applySync(session("c", model.StatusBusy, tNow))
	e.board.applySync(s)
	e.board.applySync(model.TerminalFact{ID: termUUID, Workspace: "alpha", HasAgent: true})
	e.board.SetSources([]source.Status{{Kind: source.KindClaude, State: source.StateOK}})
	e.board.tick()

	raw, err := json.Marshal(e.board.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile("../contract/golden/snapshot.v1.json")
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
			t.Errorf("snapshot has %s, the golden does not", k)
		}
	}
	for k := range wk {
		if !gk[k] {
			t.Errorf("golden has %s, the snapshot does not", k)
		}
	}
}

func TestSeqIncreasesOnlyOnChange(t *testing.T) {
	b := newTestBoard(t)
	s0 := b.Snapshot().Seq
	b.applySync(busySession("s1", tNow))
	s1 := b.Snapshot().Seq
	if s1 != s0+1 {
		t.Fatalf("seq %d -> %d after a change", s0, s1)
	}
	b.applySync(busySession("s1", tNow))
	b.tick()
	b.SetSources(nil)
	if s2 := b.Snapshot().Seq; s2 != s1 {
		t.Fatalf("seq %d -> %d without a change", s1, s2)
	}
}

// Publishes are at least publishEvery apart, so a burst of changes reaches a
// subscriber as a few snapshots, the last one the latest. The bound comes
// from how long the burst really took, so a loaded machine does not fail it.
func TestSubscribeCoalescesBurstsToFourPerSecond(t *testing.T) {
	b := newTestBoard(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := b.Subscribe(ctx)

	received := 0
	var last contract.Snapshot
	done := make(chan struct{})
	go func() {
		defer close(done)
		for s := range ch {
			received++
			last = s
		}
	}()
	start := time.Now()
	n := 0
	for time.Since(start) < time.Second {
		n++
		b.applySync(busySession("s1", tNow.Add(time.Duration(n)*time.Second)))
		time.Sleep(2 * time.Millisecond)
	}
	waitPublished(b) // the trailing publish has landed
	took := time.Since(start)
	cancel()
	<-done
	if most := int(took/publishEvery) + 1; received > most {
		t.Fatalf("%d snapshots for a %s burst of %d changes, want at most %d", received, took, n, most)
	}
	if received == 0 || last.Seq != b.Snapshot().Seq {
		t.Fatalf("subscriber did not end on the latest snapshot: got seq %d, latest %d", last.Seq, b.Snapshot().Seq)
	}
}

func TestSubscriberGetsLatestNotBacklog(t *testing.T) {
	b := newTestBoard(t)
	ctx, cancel := context.WithCancel(context.Background())
	ch := b.Subscribe(ctx)
	for _, st := range []source.State{source.StateMissing, source.StateFailing, source.StateOK} {
		b.SetSources([]source.Status{{Kind: source.KindClaude, State: st}})
	}
	waitPublished(b)
	s := <-ch
	if s.Sources[0].State != contract.SourceOK || s.Seq != b.Snapshot().Seq {
		t.Fatalf("got seq %d state %q, want the latest", s.Seq, s.Sources[0].State)
	}
	select {
	case s := <-ch:
		t.Fatalf("backlog: another snapshot seq %d", s.Seq)
	default:
	}
	cancel()
	for range ch {
	}
}

// After Run returns nothing is published: a publish scheduled before is
// cancelled, and a change after it publishes nothing, also when the interval
// has passed and a publish would go out at once.
func TestNothingIsPublishedAfterRunReturns(t *testing.T) {
	b := newTestBoard(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := b.Subscribe(ctx)
	scheduled := func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.pubTimer != nil
	}
	states := []source.State{source.StateMissing, source.StateFailing}
	for i := 0; !scheduled(); i++ { // the second change inside the interval is scheduled
		if i == 100 {
			t.Fatal("setup: no publish was ever scheduled")
		}
		b.SetSources([]source.Status{{Kind: source.KindClaude, State: states[i%2]}})
	}
	ended, end := context.WithCancel(context.Background())
	end()
	if err := b.Run(ended); err != nil {
		t.Fatal(err)
	}
	if scheduled() {
		t.Fatal("a publish is still scheduled after Run returned")
	}
	select { // what was published before Run returned
	case <-ch:
	default:
	}
	b.mu.Lock()
	b.lastPub = time.Time{} // the interval has passed: a publish would go out at once
	b.mu.Unlock()
	b.SetSources([]source.Status{{Kind: source.KindClaude, State: source.StateOK}})
	select {
	case s := <-ch:
		t.Fatalf("published seq %d after Run returned", s.Seq)
	default:
	}
	if scheduled() {
		t.Fatal("a change after Run returned scheduled a publish")
	}
}

// waitPublished returns when no publish is scheduled: the latest change has
// been published.
func waitPublished(b *Board) {
	for {
		b.mu.Lock()
		pending := b.pubTimer != nil
		b.mu.Unlock()
		if !pending {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTickRaisesStaleFlag(t *testing.T) {
	b, clock := newClockBoard(t)
	b.applySync(busySession("s1", tNow))
	b.tick()
	seq := b.Snapshot().Seq
	for at := tNow.Add(10 * time.Second); !at.After(tNow.Add(21 * time.Minute)); at = at.Add(10 * time.Second) {
		clock.Set(at)
		b.tick()
	}
	s := b.Snapshot()
	if s.Seq == seq || !slices.Contains(s.Cards[0].Attention, contract.AttentionStale) {
		t.Fatalf("seq %d -> %d, attention %v", seq, s.Seq, s.Cards[0].Attention)
	}
}

func TestTickMovesCardOutOfDoneAfter24h(t *testing.T) {
	e := newEnv(t)
	e.prs.prs = []model.PullRequest{{Repo: exRepo, Number: 101, State: model.PRStateMerged, ClosedAt: tNow.Add(-time.Hour)}}
	s := session("s1", model.StatusFinished, tNow.Add(-2*time.Hour))
	s.CreatedPRs = []model.PRRef{{Repo: exRepo, Number: 101}}
	e.board.applySync(s)
	_ = e.board.MarkSeen("claude:s1")
	e.board.tick()
	if c := onlyCard(t, e.board); c.Column != contract.ColumnDone {
		t.Fatalf("column %q", c.Column)
	}
	e.clock.Set(tNow.Add(23*time.Hour + time.Minute))
	e.board.tick()
	if c := onlyCard(t, e.board); c.Column != contract.ColumnIdle {
		t.Fatalf("column %q after 24h, want idle", c.Column)
	}
}

func TestTickDropsEndedCardWhenItsWindowCloses(t *testing.T) {
	withFullEndedRule(t)
	e := newEnv(t)
	e.prs.prs = []model.PullRequest{{Repo: exRepo, Number: 101, State: model.PRStateClosed, ClosedAt: tNow.Add(-time.Hour)}}
	s := session("s1", model.StatusFinished, tNow.Add(-2*time.Hour))
	s.Ended, s.EndedAt = true, tNow.Add(-time.Hour)
	s.CreatedPRs = []model.PRRef{{Repo: exRepo, Number: 101}}
	e.board.applySync(s)
	e.board.tick()
	if n := len(e.board.Snapshot().Cards); n != 1 {
		t.Fatalf("%d cards", n)
	}
	e.clock.Set(tNow.Add(23*time.Hour + time.Minute))
	e.board.tick()
	if n := len(e.board.Snapshot().Cards); n != 0 {
		t.Fatalf("%d cards after the window", n)
	}
}

func TestTickWithoutChangePublishesNothing(t *testing.T) {
	b, clock := newClockBoard(t)
	b.applySync(session("s1", model.StatusFinished, tNow))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := b.Subscribe(ctx)
	time.Sleep(2 * publishEvery)
	for i := 1; i <= 5; i++ {
		clock.Set(tNow.Add(time.Duration(i) * 10 * time.Second))
		b.tick()
	}
	select {
	case s := <-ch:
		t.Fatalf("published seq %d without a change", s.Seq)
	case <-time.After(2 * publishEvery):
	}
}

func TestRunRefreshesGitHubOncePerMinutePerLiveRepo(t *testing.T) {
	e := newEnv(t)
	const other = "example-org/other-repo"
	live := session("s1", model.StatusBusy, tNow)
	live.CreatedPRs = []model.PRRef{{Repo: exRepo, Number: 101}}
	old := session("s2", model.StatusFinished, tNow.Add(-10*24*time.Hour))
	old.Ended, old.EndedAt = true, tNow.Add(-10*24*time.Hour)
	old.CreatedPRs = []model.PRRef{{Repo: other, Number: 5}}
	e.board.applySync(live)
	e.board.applySync(old)
	for i := 0; i <= 12; i++ { // T .. T+2m in 10 s ticks
		e.clock.Set(tNow.Add(time.Duration(i) * 10 * time.Second))
		e.board.tick()
	}
	if n := e.prs.callsFor(exRepo); n != 3 {
		t.Fatalf("%d lookups for the live repo over 2 minutes, want 3 (at 0, 1m, 2m)", n)
	}
	if n := e.prs.callsFor(other); n != 0 {
		t.Fatalf("%d lookups for a repo without live work", n)
	}
	e.prs.mu.Lock()
	first := e.prs.calls[0]
	e.prs.mu.Unlock()
	if !reflect.DeepEqual(first.numbers, []int{101}) {
		t.Fatalf("asked numbers %v", first.numbers)
	}
}

func TestGitHubOffNeverLooksUp(t *testing.T) {
	e := newEnv(t)
	e.board.SetSources([]source.Status{{Kind: source.KindGitHub, State: source.StateOff}})
	s := session("s1", model.StatusBusy, tNow)
	s.CreatedPRs = []model.PRRef{{Repo: exRepo, Number: 101}}
	e.board.applySync(s)
	e.board.tick()
	if n := e.prs.callsFor(exRepo); n != 0 {
		t.Fatalf("%d lookups with GitHub off", n)
	}
}

func TestNewPRRefIsLookedUpWithoutWaitingForTheMinute(t *testing.T) {
	e := newEnv(t)
	e.prs.prs = []model.PullRequest{{Repo: exRepo, Number: 101, Title: "Fresh", State: model.PRStateOpen}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.board.Run(ctx) }()
	s := session("s1", model.StatusFinished, tNow)
	s.CreatedPRs = []model.PRRef{{Repo: exRepo, Number: 101}}
	e.board.Apply(s) // Run is resolving concurrently; the loop below waits
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, ok := cardByID(t, e.board, "claude:s1"); ok && c.TitleSource == contract.TitleFromPR {
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("new pull request ref not looked up within 2s")
}

func TestRunStopsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- newTestBoard(t).Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestBranchMovesAreResolvedAgain(t *testing.T) {
	e := newEnv(t)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Slug: exRepo, Branch: model.Of("one")})
	s := session("c1", model.StatusFinished, tNow)
	s.Key = model.SessionKey{Tool: model.ToolCodex, ID: "c1"}
	e.board.applySync(s)
	e.board.tick()
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Slug: exRepo, Branch: model.Of("two")})
	e.prs.prs = []model.PullRequest{{Repo: exRepo, Number: 9, HeadBranch: "two", State: model.PRStateOpen}}
	e.clock.Set(tNow.Add(time.Minute))
	e.board.tick()
	e.clock.Set(tNow.Add(2 * time.Minute))
	e.board.tick()
	if c := onlyCard(t, e.board); len(c.PullRequests) != 1 || c.PullRequests[0].Number != 9 {
		t.Fatalf("pull requests %+v after the branch moved", c.PullRequests)
	}
}
