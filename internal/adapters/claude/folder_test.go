package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/cpucost"
)

// cwdLines writes n main-chain (or sidechain) records in folder.
func cwdLines(b *bytes.Buffer, folder string, n int, sidechain bool) {
	cwd, _ := json.Marshal(folder) // JSON escapes, so a control character stays in the value
	for range n {
		fmt.Fprintf(b, `{"type":"assistant","isSidechain":%v,"timestamp":"2026-11-17T08:00:00.000Z","cwd":%s,"message":{"model":"claude-opus-5-5"}}`+"\n", sidechain, cwd)
	}
}

// gitDir makes a folder with a .git entry, as a repository root.
func gitDir(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(p, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func folderAfter(t *testing.T, tl *tailer) string {
	t.Helper()
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	return tl.d.Folder
}

// The real shape: hundreds of records in its own project, a short visit to
// another repository, then back. The folder never leaves its own project,
// also while the transcript is read in pieces.
func TestFolderIsWhereMostWorkIs(t *testing.T) {
	root := t.TempDir()
	a, other := gitDir(t, filepath.Join(root, "a")), gitDir(t, filepath.Join(root, "b"))
	p := filepath.Join(t.TempDir(), "s.jsonl")
	var buf bytes.Buffer
	cwdLines(&buf, a, 300, false)
	writeFile(t, p, buf.Bytes())
	tl := newTailer(p, realProber(t))
	if got := folderAfter(t, tl); got != a {
		t.Fatalf("folder %q, want a", got)
	}
	buf.Reset()
	cwdLines(&buf, other, 21, false)
	appendFile(t, p, buf.Bytes())
	if got := folderAfter(t, tl); got != a {
		t.Fatalf("after a short visit: folder %q, want a", got)
	}
	buf.Reset()
	cwdLines(&buf, a, 3, false)
	appendFile(t, p, buf.Bytes())
	if got := folderAfter(t, tl); got != a {
		t.Fatalf("back home: folder %q, want a", got)
	}
}

// A session that moves for good switches once the new folder outnumbers the
// old one; a tie goes to the most recent. Sidechain records do not count.
func TestFolderSwitchesOnceOutnumbered(t *testing.T) {
	root := t.TempDir()
	a, b := gitDir(t, filepath.Join(root, "a")), gitDir(t, filepath.Join(root, "b"))
	var buf bytes.Buffer
	cwdLines(&buf, a, 10, false)
	cwdLines(&buf, b, 10, false)
	if got := realTail(t, buf.Bytes()).d.Folder; got != b {
		t.Fatalf("tie: folder %q, want the most recent (b)", got)
	}
	buf.Reset()
	cwdLines(&buf, a, 10, false)
	cwdLines(&buf, b, 9, false)
	cwdLines(&buf, b, 50, true) // an in-process subagent's records
	if got := realTail(t, buf.Bytes()).d.Folder; got != a {
		t.Fatalf("sidechain records counted: folder %q, want a", got)
	}
}

// A worker whose records are all in its worktree reports the worktree.
func TestFolderOfWorkerInWorktree(t *testing.T) {
	root := t.TempDir()
	main := gitDir(t, filepath.Join(root, "repo"))
	wt := filepath.Join(main, ".claude", "worktrees", "w1")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	cwdLines(&buf, wt, 40, false)
	if got := realTail(t, buf.Bytes()).d.Folder; got != wt {
		t.Fatalf("folder %q, want the worktree", got)
	}
}

// Records spread over a repository's subfolders count for the repository, so a
// short visit elsewhere does not win against the split votes. The folder
// reported is the most used one inside the winning repository.
func TestFolderVotesCountByRepository(t *testing.T) {
	root := t.TempDir()
	repo := gitDir(t, filepath.Join(root, "repo"))
	other := gitDir(t, filepath.Join(root, "other"))
	for _, sub := range []string{"repo/web", "repo/api", "repo/docs", "home/work/umbrella", "home/work/visit"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	cwdLines(&buf, filepath.Join(repo, "web"), 16, false)
	cwdLines(&buf, filepath.Join(repo, "api"), 15, false)
	cwdLines(&buf, filepath.Join(repo, "docs"), 15, false)
	cwdLines(&buf, other, 21, false)
	if got := realTail(t, buf.Bytes()).d.Folder; got != filepath.Join(repo, "web") {
		t.Fatalf("folder %q, want repo/web", got)
	}

}

// A shrunk or replaced file is read again from zero: the votes are rebuilt.
func TestFolderVotesRebuiltAfterReset(t *testing.T) {
	root := t.TempDir()
	a, b := gitDir(t, filepath.Join(root, "a")), gitDir(t, filepath.Join(root, "b"))
	p := filepath.Join(t.TempDir(), "s.jsonl")
	var buf bytes.Buffer
	cwdLines(&buf, a, 50, false)
	writeFile(t, p, buf.Bytes())
	tl := newTailer(p, realProber(t))
	_ = folderAfter(t, tl)
	buf.Reset()
	cwdLines(&buf, b, 3, false) // a shorter file: read from zero
	writeFile(t, p, buf.Bytes())
	if got := folderAfter(t, tl); got != b {
		t.Fatalf("after a shrink: folder %q, want b", got)
	}
}

// Ruling: a deleted folder's votes go to its nearest existing ancestor. A
// removed worktree with 399 votes keeps the session in its repository, and
// one command elsewhere does not win. A folder removed later moves on the
// recheck, with no new record, and the update reports the change.
func TestDeletedFolderVotesGoToItsNearestExistingAncestor(t *testing.T) {
	root := t.TempDir()
	repo := gitDir(t, filepath.Join(root, "repo"))
	worktrees := filepath.Join(repo, ".claude", "worktrees")
	wt := filepath.Join(worktrees, "w1")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := gitDir(t, filepath.Join(root, "other"))
	p := filepath.Join(t.TempDir(), "s.jsonl")
	var buf bytes.Buffer
	cwdLines(&buf, wt, 399, false)
	writeFile(t, p, buf.Bytes())
	tl := newTailer(p, realProber(t))
	if got := settled(t, tl, fixedNow); got != wt {
		t.Fatalf("folder %q, want the worktree while it exists", got)
	}
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	cwdLines(&buf, other, 1, false)
	appendFile(t, p, buf.Bytes())
	got := settled(t, tl, fixedNow.Add(folderRecheck))
	if got != worktrees {
		t.Fatalf("after the worktree was removed: folder %q, want its nearest existing ancestor", got)
	}
}

// Spellings of one folder (a trailing "/" or "/.") are one folder.
func TestFolderSpellingsCountAsOne(t *testing.T) {
	root := t.TempDir()
	a, other := gitDir(t, filepath.Join(root, "a")), gitDir(t, filepath.Join(root, "other"))
	var buf bytes.Buffer
	cwdLines(&buf, a, 10, false)
	cwdLines(&buf, a+"/", 10, false)
	cwdLines(&buf, a+"/.", 10, false)
	cwdLines(&buf, other, 11, false)
	if got := realTail(t, buf.Bytes()).d.Folder; got != a {
		t.Fatalf("folder %q, want %q", got, a)
	}
}

// A home folder kept in git never forms a group: the walk for .git stops
// below home, so plain folders under home count apart.
func TestHomeInGitFormsNoGroup(t *testing.T) {
	root := t.TempDir()
	home := gitDir(t, filepath.Join(root, "home"))
	t.Setenv("HOME", home)
	repo := gitDir(t, filepath.Join(home, "code", "repo"))
	var buf bytes.Buffer
	cwdLines(&buf, repo, 60, false)
	for i := range 31 {
		n := filepath.Join(home, "notes", fmt.Sprintf("n%02d", i))
		if err := os.MkdirAll(n, 0o755); err != nil {
			t.Fatal(err)
		}
		cwdLines(&buf, n, 2, false)
	}
	if got := realTail(t, buf.Bytes()).d.Folder; got != repo {
		t.Fatalf("folder %q, want the repository", got)
	}
}

// Votes count by repository with nothing evicted folder by folder: a
// repository worked in across many subfolders outweighs another with fewer
// records in one folder.
func TestRepositoryWithManySubfoldersWins(t *testing.T) {
	root := t.TempDir()
	other, mono := gitDir(t, filepath.Join(root, "other")), gitDir(t, filepath.Join(root, "mono"))
	var buf bytes.Buffer
	cwdLines(&buf, other, 100, false)
	for i := range 200 {
		sub := filepath.Join(mono, fmt.Sprintf("s%03d", i))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		cwdLines(&buf, sub, 1, false)
	}
	tl := realTail(t, buf.Bytes())
	if got := settled(t, tl, fixedNow); filepath.Dir(got) != mono {
		t.Fatalf("folder %q, want a folder of mono", got)
	}
}

// Only the most recent voteWindow records count: a real move takes effect
// after about half the window, a short visit never does, and reading the
// same transcript in pieces gives the same answer as reading it at once.
func TestOnlyRecentRecordsCount(t *testing.T) {
	root := t.TempDir()
	a, b := gitDir(t, filepath.Join(root, "a")), gitDir(t, filepath.Join(root, "b"))
	var buf bytes.Buffer
	cwdLines(&buf, a, 2000, false)
	cwdLines(&buf, b, voteWindow/2+10, false)
	whole := buf.Bytes()
	if got := realTail(t, whole).d.Folder; got != b {
		t.Fatalf("after a move for good: folder %q, want b", got)
	}
	p := filepath.Join(t.TempDir(), "s.jsonl")
	half := bytes.LastIndexByte(whole[:len(whole)/2], '\n') + 1
	writeFile(t, p, whole[:half])
	tl := newTailer(p, realProber(t))
	_ = folderAfter(t, tl)
	appendFile(t, p, whole[half:])
	if got := folderAfter(t, tl); got != b {
		t.Fatalf("read in pieces: folder %q, want b", got)
	}
	if len(tl.held) > voteWindow {
		t.Fatalf("%d folders held, window %d", len(tl.held), voteWindow)
	}
}

// 100,000 records in distinct 64-deep folders: folders are looked up only
// while held, and only a few per update. Counted, not timed: a loaded machine
// slows the reading, not the number of lookups.
func TestManyDeepFoldersIndexFast(t *testing.T) {
	root := t.TempDir()
	deep := root + strings.Repeat("/d", 63)
	var buf bytes.Buffer
	for i := range 100000 {
		cwdLines(&buf, fmt.Sprintf("%s/f%06d", deep, i), 1, false)
	}
	p := filepath.Join(t.TempDir(), "s.jsonl")
	writeFile(t, p, buf.Bytes())
	pr := realProber(t)
	var batches, looks, checks atomic.Int32
	run := pr.run
	pr.run = func(jobs []probeJob, home string) []probeResult {
		batches.Add(1)
		for _, j := range jobs {
			if j.look {
				looks.Add(1)
			}
			if j.check {
				checks.Add(1)
			}
		}
		return run(jobs, home)
	}
	tl := newTailer(p, pr)
	start := time.Now()
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	t.Logf("100,000 records in distinct 64-deep folders: %s", time.Since(start))
	if b, l, c := batches.Load(), looks.Load(), checks.Load(); b != 1 || l > probeLookups || c > probeChecks {
		t.Fatalf("one update ran %d batches, %d lookups, %d checks; want 1, at most %d, at most %d",
			b, l, c, probeLookups, probeChecks)
	}
	if len(tl.held) > voteWindow {
		t.Fatalf("%d folders held, window %d", len(tl.held), voteWindow)
	}
	if settled(t, tl, fixedNow) == "" {
		t.Fatal("no folder")
	}
}

// settled runs updates until the folder no longer waits on lookups.
func settled(t *testing.T, tl *tailer, now time.Time) string {
	t.Helper()
	for range 100 {
		if _, err := tl.update(context.Background(), now); err != nil {
			t.Fatal(err)
		}
		if !tl.votesChanged {
			return tl.d.Folder
		}
	}
	t.Fatal("the folder never settled")
	return ""
}

// blockProbes makes every lookup batch of pr hang until the test ends.
func blockProbes(t *testing.T, pr *prober) {
	t.Helper()
	gate := make(chan struct{})
	run := pr.run
	pr.run = func(jobs []probeJob, home string) []probeResult {
		<-gate
		return run(jobs, home)
	}
	t.Cleanup(func() { close(gate) }) // before testProber's, which waits for the batches
}

// expiries makes pr's deadlines fire at once and records them.
func expiries(pr *prober) func() []time.Time {
	var mu sync.Mutex
	var got []time.Time
	pr.expiry = func(until time.Time) <-chan time.Time {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, until)
		return expiredNow
	}
	return func() []time.Time {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
}

// Item 1: one indexing pass waits for lookups once, not once per session:
// every session's update waits for the pass's one deadline, and a hung
// lookup holds no update past it. A tailer outside a pass waits the budget.
func TestPassWaitsForLookupsOnce(t *testing.T) {
	pr := realProber(t)
	pr.slots = make(chan struct{}, 20) // every session gets its batch, and waits
	blockProbes(t, pr)
	got := expiries(pr)
	root := t.TempDir()
	var tls []*tailer
	for i := range 20 {
		var buf bytes.Buffer
		cwdLines(&buf, gitDir(t, filepath.Join(root, fmt.Sprintf("r%02d", i))), 5, false)
		p := filepath.Join(t.TempDir(), "s.jsonl")
		writeFile(t, p, buf.Bytes())
		tls = append(tls, newTailer(p, pr))
	}
	deadline := time.Now().Add(pr.budget)
	for _, tl := range tls {
		tl.probeUntil = deadline
		if _, err := tl.update(context.Background(), fixedNow); err != nil {
			t.Fatal(err)
		}
	}
	waits := got()
	if len(waits) != len(tls) {
		t.Fatalf("%d waits for %d sessions", len(waits), len(tls))
	}
	for i, w := range waits {
		if !w.Equal(deadline) {
			t.Fatalf("session %d waited until %v, not the pass's deadline %v", i, w, deadline)
		}
	}
	tls[0].probeUntil = time.Time{}
	before := time.Now()
	if _, err := tls[0].update(context.Background(), fixedNow.Add(folderRecheck)); err != nil {
		t.Fatal(err)
	}
	after := got()
	if len(after) != len(waits)+1 || after[len(waits)].Before(before.Add(pr.budget)) {
		t.Fatalf("outside a pass: waits %v, want one until the budget from %v", after[len(waits):], before)
	}
}

// Item 2: while lookups are pending, the previous folder stays (none on the
// first read, so the launch folder is used): no interim pick is sent.
func TestNoInterimFolder(t *testing.T) {
	root := t.TempDir()
	mono, other := gitDir(t, filepath.Join(root, "mono")), gitDir(t, filepath.Join(root, "other"))
	var buf bytes.Buffer
	for i := range 60 {
		sub := filepath.Join(mono, fmt.Sprintf("s%02d", i))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		cwdLines(&buf, sub, 4, false)
	}
	cwdLines(&buf, other, 100, false)
	for i := range 40 {
		f := filepath.Join(root, fmt.Sprintf("plain%02d", i))
		if err := os.MkdirAll(f, 0o755); err != nil {
			t.Fatal(err)
		}
		cwdLines(&buf, f, 1, false)
	}
	tl := realTail(t, buf.Bytes())
	if tl.d.Folder == other {
		t.Fatal("the first update reported an interim folder")
	}
	if got := settled(t, tl, fixedNow); filepath.Dir(got) != mono {
		t.Fatalf("settled on %q, want a folder of mono", got)
	}
}

// Item 3: lookup batches in flight are bounded for all the tailers sharing a
// prober (the product has one per process, procProber), also across a reset
// (a truncated transcript) and new tailers. Every batch hangs, so a tailer
// that started one keeps it (t.probing) for the whole test; a reset tailer's
// old batch keeps its slot.
func TestLookupBatchesAreBounded(t *testing.T) {
	pr := realProber(t)
	blockProbes(t, pr)
	expiries(pr) // do not wait
	root := t.TempDir()
	var tls []*tailer
	for i := range 20 {
		var buf bytes.Buffer
		cwdLines(&buf, gitDir(t, filepath.Join(root, fmt.Sprintf("r%02d", i))), 5, false)
		p := filepath.Join(t.TempDir(), "s.jsonl")
		writeFile(t, p, buf.Bytes())
		tl := newTailer(p, pr)
		_, _ = tl.update(context.Background(), fixedNow)
		tls = append(tls, tl)
	}
	inFlight := func() int {
		n := 0
		for _, tl := range tls {
			if tl.probing != nil {
				n++
			}
		}
		return n
	}
	if n := inFlight(); n != maxProbes || tls[0].probing == nil {
		t.Fatalf("%d of 20 tailers started a batch (the first: %v), cap %d", n, tls[0].probing != nil, maxProbes)
	}
	var buf bytes.Buffer
	cwdLines(&buf, filepath.Join(root, "r00"), 1, false)
	writeFile(t, tls[0].path, buf.Bytes()) // shorter: read from zero
	_, _ = tls[0].update(context.Background(), fixedNow)
	if tls[0].probing != nil {
		t.Fatal("after a reset, a new batch started while the old one still holds its slot")
	}
	if n := inFlight(); n != maxProbes-1 {
		t.Fatalf("%d tailers with a batch after the reset, want %d (and the reset one's old batch)", n, maxProbes-1)
	}
}

// The product shares one prober, with the cap and budget above.
func TestOneProberPerProcess(t *testing.T) {
	a := New(Options{Home: t.TempDir()}).(*adapter)
	if a.prober != procProber || cap(procProber.slots) != maxProbes || procProber.budget != probeBudget ||
		procProber.run == nil || procProber.expiry == nil {
		t.Fatal("the adapter does not use the process's prober, or it is incomplete")
	}
	// One batch through it, to the end: real lookups, the real deadline. It is
	// not timed: a loaded machine may need more than one update.
	repo := gitDir(t, filepath.Join(t.TempDir(), "repo"))
	var buf bytes.Buffer
	cwdLines(&buf, repo, 3, false)
	p := filepath.Join(t.TempDir(), "s.jsonl")
	writeFile(t, p, buf.Bytes())
	tl := newTailer(p, procProber)
	if got := settled(t, tl, fixedNow); got != repo {
		t.Fatalf("folder %q through the process's prober, want the repository", got)
	}
	drain(procProber)
}

// at is the product's deadline: one already passed fires at once, a later
// one fires at it, not before, and neither is ever nil (a nil channel would
// wait for a hung lookup for good).
func TestDeadlineFires(t *testing.T) {
	past := at(time.Now().Add(-time.Second))
	if past == nil {
		t.Fatal("a passed deadline gave a nil channel")
	}
	select {
	case <-past:
	default:
		t.Fatal("a passed deadline did not fire at once")
	}
	until := time.Now().Add(20 * time.Millisecond)
	later := at(until)
	if later == nil {
		t.Fatal("a later deadline gave a nil channel")
	}
	<-later // the test's own timeout catches one that never fires
	if now := time.Now(); now.Before(until) {
		t.Fatalf("fired %s before its deadline", until.Sub(now))
	}
}

// Reading a transcript costs the same per record at any length: the CPU
// time of one update at 4n records is about 4 times that at n, a quadratic
// step 16 times.
func TestUpdateIsLinear(t *testing.T) {
	pr := staticProber(t)
	update := func(n int) func() {
		var buf bytes.Buffer
		for i := range n {
			cwdLines(&buf, fmt.Sprintf("/w/f%03d", i%300), 1, false)
		}
		p := filepath.Join(t.TempDir(), "s.jsonl")
		writeFile(t, p, buf.Bytes())
		return func() {
			if _, err := newTailer(p, pr).update(context.Background(), fixedNow); err != nil {
				t.Fatal(err)
			}
		}
	}
	cpucost.Linear(t, 3, "one update of 25,000 records, then 100,000", update(25000), update(100000))
}

// Item 4: a folder missing at lookup time that comes back is looked up again.
func TestReappearedFolderIsLookedUpAgain(t *testing.T) {
	root := t.TempDir()
	repo := gitDir(t, filepath.Join(root, "repo"))
	wt := filepath.Join(repo, "wt")
	other := gitDir(t, filepath.Join(root, "other"))
	var buf bytes.Buffer
	cwdLines(&buf, wt, 50, false)
	cwdLines(&buf, other, 10, false)
	tl := realTail(t, buf.Bytes())
	if got := settled(t, tl, fixedNow); got != repo {
		t.Fatalf("while missing: folder %q, want its ancestor repo", got)
	}
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := settled(t, tl, fixedNow.Add(folderRecheck)); got != wt {
		t.Fatalf("after it came back: folder %q, want the worktree", got)
	}
	if g := tl.held[wt].group; g != wt {
		t.Fatalf("group %q, want the worktree itself", g)
	}
}

// Ruling: a cwd with a control character can never be a real folder: its
// record does not vote.
func TestControlCharacterFolderDoesNotVote(t *testing.T) {
	root := t.TempDir()
	a := gitDir(t, filepath.Join(root, "a"))
	var buf bytes.Buffer
	cwdLines(&buf, a, 3, false)
	cwdLines(&buf, filepath.Join(root, "b\x00c"), 10, false)
	cwdLines(&buf, filepath.Join(root, "d\x1be"), 10, false)
	tl := realTail(t, buf.Bytes())
	if got := settled(t, tl, fixedNow); got != a {
		t.Fatalf("folder %q, want a", got)
	}
	if len(tl.held) != 1 {
		t.Fatalf("%d folders held, want 1", len(tl.held))
	}
}

// testProber returns a prober of the test's own: its own slots, so a batch
// another test left running cannot hold one, and no deadline, so an update
// waits for its lookups however loaded the machine is. A nil run does the
// real lookups. The test ends only when its batches have.
func testProber(t *testing.T, run func([]probeJob, string) []probeResult) *prober {
	t.Helper()
	if run == nil {
		run = runProbe
	}
	pr := &prober{
		run: run, slots: make(chan struct{}, maxProbes), budget: probeBudget,
		expiry: func(time.Time) <-chan time.Time { return nil },
	}
	t.Cleanup(func() { drain(pr) })
	return pr
}

// drain returns when no batch of pr is in flight.
func drain(pr *prober) {
	for len(pr.slots) > 0 {
		runtime.Gosched()
	}
}

// realProber looks folders up on this machine: for fixtures in temporary
// folders.
func realProber(t *testing.T) *prober {
	t.Helper()
	return testProber(t, nil)
}

// staticProber makes every folder exist and be its own group, for tests
// about reading transcripts whose fixture paths are not on this machine (on
// macOS /home is automounted: one lookup there can outlast the budget).
func staticProber(t *testing.T) *prober {
	t.Helper()
	return testProber(t, func(jobs []probeJob, _ string) []probeResult {
		out := make([]probeResult, 0, len(jobs))
		for _, j := range jobs {
			out = append(out, probeResult{probeJob: j, group: j.folder})
		}
		return out
	})
}
