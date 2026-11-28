package claude

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/shellcmd"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

// job asks the indexer to bring one session's transcripts up to date.
type job struct {
	id   string
	main string // the session's transcript
	// others: its transcripts in other project folders (it was resumed in
	// another folder), older; they add their spend.
	others  []string
	subDirs []string // the folders of its in-process subagents' transcripts, one per transcript
	ended   bool     // its last pass: indexed once, after the live sessions
	gen     int      // which end of the session the last pass is for
}

// result is a session's derived facts after an indexer pass.
type result struct {
	id        string
	d         derived
	subagents int
	final     bool // the last pass of an ended session
	gen       int  // for a last pass: which end of the session it is for
	readable  bool // at least one line of the main transcript could be read
}

// endedReadCap bounds what is read of one transcript that is first opened
// for an ended session's last pass: only its last endedReadCap bytes, from the
// first whole line, so one huge transcript cannot hold the indexer for long.
// What is skipped is older: it is read for spend alone (its replies and
// cost-state lines), so a huge transcript's days and totals are whole, and
// every other fact of it can only be left out, never wrong.
var endedReadCap int64 = 64 << 20 // a variable for tests

// finalTries is how many passes an ended session's last pass waits for its
// folder lookups before it is sent with the folder known so far.
const finalTries = 5

// indexer tails transcripts in the background, so the registry can paint the
// board before any transcript is read. It owns its tailers; the registry
// talks to it only through want and results.
type indexer struct {
	a       *adapter
	results chan result

	mu      sync.Mutex
	wanted  map[string]job
	wake    chan struct{}
	tailers map[string]*tailer      // by transcript path
	claims  map[string]*replyClaims // session id → which of its transcripts counted each reply
	owners  uint32                  // transcripts numbered so far, for the claims
	last    map[string]result       // last result sent, by session id
	subs    map[string][]string     // session id → subagent transcript paths
	subMod  map[string]string       // session id → its subagent folders' last changes
	done    map[string]int          // ended sessions: the end whose last pass was sent
	tries   map[string]int          // ended sessions: passes that waited for the folder
	// passUntil is how long the current pass waits for folder lookups, in
	// all: one deadline for every session, never one per session.
	passUntil time.Time
}

func newIndexer(a *adapter) *indexer {
	return &indexer{
		a: a, results: make(chan result), wake: make(chan struct{}, 1),
		tailers: map[string]*tailer{}, claims: map[string]*replyClaims{}, last: map[string]result{}, subs: map[string][]string{}, subMod: map[string]string{},
		done: map[string]int{}, tries: map[string]int{},
	}
}

// want replaces the set of sessions to keep indexed and wakes the indexer.
func (ix *indexer) want(jobs map[string]job) {
	ix.mu.Lock()
	ix.wanted = jobs
	ix.mu.Unlock()
	select {
	case ix.wake <- struct{}{}:
	default:
	}
}

func (ix *indexer) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ix.wake:
		}
		if ix.a.beforeIndex != nil {
			ix.a.beforeIndex()
		}
		ix.mu.Lock()
		jobs := ix.wanted
		ix.mu.Unlock()
		ix.forget(jobs)
		// Live sessions first, then the ended ones; small transcripts first in
		// each, so most cards fill in at once and the big ones follow.
		order := make([]job, 0, len(jobs))
		for _, j := range jobs {
			if gen, done := ix.done[j.id]; !j.ended || !done || gen != j.gen {
				order = append(order, j)
			}
		}
		sizes := map[string]int64{}
		for _, j := range order {
			if fi, err := os.Stat(j.main); err == nil {
				sizes[j.id] = fi.Size()
			}
		}
		slices.SortFunc(order, func(x, y job) int {
			if x.ended != y.ended {
				if y.ended {
					return -1
				}
				return 1
			}
			if c := sizes[x.id] - sizes[y.id]; c != 0 {
				if c < 0 {
					return -1
				}
				return 1
			}
			return strings.Compare(x.id, y.id)
		})
		ix.passUntil = time.Now().Add(ix.a.prober.budget)
		endedRan := false // each pass runs at least one ended job, so the backlog always moves
	pass:
		for _, j := range order {
			if ctx.Err() != nil {
				return
			}
			if j.ended && endedRan {
				select {
				case <-ix.wake: // new work: the live sessions go first again
					ix.wake <- struct{}{}
					break pass
				default:
				}
			}
			res, ok := ix.index(ctx, j)
			endedRan = endedRan || j.ended
			if !ok {
				continue // its folder is still being looked up: next pass
			}
			if j.ended {
				ix.finish(j)
			}
			if old, ok := ix.last[j.id]; ok && reflect.DeepEqual(old, res) {
				continue
			}
			ix.last[j.id] = res
			select {
			case ix.results <- res:
			case <-ctx.Done():
				return
			}
		}
	}
}

// finish drops what an ended session's last pass needed: only its derived
// facts are kept, in the registry.
func (ix *indexer) finish(j job) {
	ix.done[j.id] = j.gen
	delete(ix.tries, j.id)
	delete(ix.tailers, j.main)
	for _, p := range j.others {
		delete(ix.tailers, p)
	}
	for _, p := range ix.subs[j.id] {
		delete(ix.tailers, p)
	}
	delete(ix.claims, j.id) // read again from the start if it comes back
	delete(ix.subs, j.id)
	delete(ix.subMod, j.id)
}

// forget drops the tailers of sessions no longer wanted.
func (ix *indexer) forget(jobs map[string]job) {
	keep := map[string]bool{}
	for _, j := range jobs {
		keep[j.main] = true
		for _, p := range j.others {
			keep[p] = true
		}
		for _, p := range ix.subs[j.id] {
			keep[p] = true
		}
	}
	for p, t := range ix.tailers {
		if !keep[p] {
			t.release()
			delete(ix.tailers, p)
		}
	}
	for id := range ix.claims {
		if _, ok := jobs[id]; !ok {
			delete(ix.claims, id)
		}
	}
	for id := range ix.last {
		if _, ok := jobs[id]; !ok {
			delete(ix.last, id)
			delete(ix.subs, id)
			delete(ix.subMod, id)
		}
	}
	for id := range ix.done {
		if j, ok := jobs[id]; !ok || !j.ended {
			delete(ix.done, id) // gone, or alive again
		}
	}
	for id := range ix.tries {
		if _, ok := jobs[id]; !ok {
			delete(ix.tries, id)
		}
	}
}

// tail brings one transcript of session id up to date: its own, or one of
// its subagents' (sub), whose lines count for the session's spend.
// For a subagent's transcript, main is the session's main tailer, already
// brought up to date: the subagent's replies after its cost-state line are
// told by it.
func (ix *indexer) tail(ctx context.Context, path, id string, now time.Time, ended bool, main *tailer) *tailer {
	t := ix.tailers[path]
	if t == nil {
		t = newTailer(path, ix.a.prober)
		cl := ix.claims[id]
		if cl == nil {
			cl = newReplyClaims()
			ix.claims[id] = cl
		}
		ix.owners++
		t.claims, t.owner = cl, ix.owners
		if main != nil {
			t.usageOwner, t.spend.sub = id, true
		}
		if ended {
			t.readCap = endedReadCap
		}
		ix.tailers[path] = t
	}
	if main != nil {
		t.spend.follow(&main.spend)
	}
	t.tab = ix.a.prices.table(now)
	t.probeUntil = ix.passUntil
	if ended {
		t.probeUntil = time.Now().Add(ix.a.prober.budget) // its own wait, not what is left of the pass's
	}
	_, _ = t.update(ctx, now) // a vanished file keeps what was read; the registry notices the end
	return t
}

// index updates one session's transcripts and merges what they say. The
// subagents add their pull requests, artifacts, spawns and activity; the main transcript
// alone says the model, effort, folder, turns and messages received. For an
// ended session's last pass it reports false while the folder is still being
// looked up, up to finalTries passes.
func (ix *indexer) index(ctx context.Context, j job) (result, bool) {
	now := ix.a.now()
	main := ix.tail(ctx, j.main, j.id, now, j.ended, nil)
	if j.ended && !main.settled() {
		if ix.tries[j.id]++; ix.tries[j.id] < finalTries {
			return result{}, false
		}
	}
	res := result{id: j.id, d: main.d, final: j.ended, readable: main.parsed > 0}
	if j.ended {
		res.gen = j.gen
	}
	res.d.TurnEnds = slices.Clone(main.d.TurnEnds)
	res.d.CreatedPRs = slices.Clone(main.d.CreatedPRs)
	res.d.PushedPRs = slices.Clone(main.d.PushedPRs)
	res.d.TouchedIssues = slices.Clone(main.d.TouchedIssues)
	res.d.Artifacts = slices.Clone(main.d.Artifacts)
	res.d.SpawnedNames = slices.Clone(main.d.SpawnedNames)
	res.d.SpawnedTerminalIDs = slices.Clone(main.d.SpawnedTerminalIDs)
	res.d.PeerSenders = slices.Clone(main.d.PeerSenders)

	// The subagent folders are listed again only when one changed.
	subs := ix.subs[j.id]
	var mod strings.Builder
	for _, dir := range j.subDirs {
		if fi, err := os.Stat(dir); err == nil {
			mod.WriteString(dir + "\x00" + fi.ModTime().String() + "\x00")
		}
	}
	if m := mod.String(); m == "" {
		subs = nil
		delete(ix.subMod, j.id)
	} else if m != ix.subMod[j.id] {
		subs = nil
		for _, dir := range j.subDirs {
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".jsonl") {
					subs = append(subs, filepath.Join(dir, e.Name()))
				}
			}
		}
		ix.subMod[j.id] = m
	}
	ix.subs[j.id] = subs
	res.subagents = len(subs)
	var subSpend []*spendLog
	// The session's transcripts in other folders count like a subagent's:
	// their use is the session's, and its last cost-state line is the main
	// transcript's.
	for _, p := range j.others {
		if ctx.Err() != nil {
			break
		}
		ot := ix.tail(ctx, p, j.id, now, j.ended, main)
		subSpend = append(subSpend, &ot.spend)
		res.d.TouchedIssues = shellcmd.AddIssueTouchList(res.d.TouchedIssues, ot.d.TouchedIssues, maxPRs)
	}
	for _, p := range subs {
		if ctx.Err() != nil {
			break
		}
		st := ix.tail(ctx, p, j.id, now, j.ended, main)
		subSpend = append(subSpend, &st.spend)
		sd := st.d
		res.d.CreatedPRs, res.d.PushedPRs = mergeCreated(res.d.CreatedPRs, res.d.PushedPRs, sd.CreatedPRs, sd.PushedPRs)
		res.d.TouchedIssues = shellcmd.AddIssueTouchList(res.d.TouchedIssues, sd.TouchedIssues, maxPRs)
		res.d.Artifacts = mergeArtifacts(res.d.Artifacts, sd.Artifacts)
		for _, n := range sd.SpawnedNames {
			res.d.SpawnedNames = addCapped(res.d.SpawnedNames, n, maxSpawns)
		}
		for _, id := range sd.SpawnedTerminalIDs {
			res.d.SpawnedTerminalIDs = addCapped(res.d.SpawnedTerminalIDs, id, maxSpawns)
		}
		if sd.LastActivity.After(res.d.LastActivity) {
			res.d.LastActivity = sd.LastActivity
		}
	}
	res.d.spend = buildSpend(&main.spend, subSpend, now.Add(-spendKeep).Truncate(model.SpendSpanLength))
	return res, true
}

// mergeCreated adds a subagent's pull and merge requests (with the windows of
// its push-created ones) to the session's, up to maxPRs per forge. For a ref
// both have, the session keeps the EARLIEST window: only the push that named
// a merge request first can have created it. A ref that either side has
// without a window was created by `glab mr create` (or is a GitHub pull
// request), which needs none, so it ends with no window.
func mergeCreated(created []model.PRRef, pushed []model.PushedRef, subCreated []model.PRRef, subPushed []model.PushedRef) ([]model.PRRef, []model.PushedRef) {
	window := func(list []model.PushedRef, ref model.PRRef) int {
		return slices.IndexFunc(list, func(p model.PushedRef) bool { return p.Ref == ref })
	}
	for _, pr := range subCreated {
		sub := window(subPushed, pr)
		if !slices.Contains(created, pr) {
			if !shellcmd.Room(created, pr, maxPRs) {
				continue
			}
			created = append(created, pr)
			if sub >= 0 {
				pushed = append(pushed, subPushed[sub])
			}
			continue
		}
		own := window(pushed, pr)
		switch {
		case own < 0: // credited with no window already
		case sub < 0:
			pushed = slices.Delete(pushed, own, own+1)
		case subPushed[sub].From.Before(pushed[own].From):
			pushed[own] = subPushed[sub]
		}
	}
	return created, pushed
}
