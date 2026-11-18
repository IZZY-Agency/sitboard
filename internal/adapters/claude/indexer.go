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
)

// job asks the indexer to bring one live session's transcripts up to date.
type job struct {
	id     string
	main   string // the session's transcript
	subDir string // its in-process subagents' transcripts
}

// result is a session's derived facts after an indexer pass.
type result struct {
	id        string
	d         derived
	subagents int
}

// indexer tails transcripts in the background, so the registry can paint the
// board before any transcript is read. It owns its tailers; the registry
// talks to it only through want and results.
type indexer struct {
	a       *adapter
	results chan result

	mu      sync.Mutex
	wanted  map[string]job
	wake    chan struct{}
	tailers map[string]*tailer   // by transcript path
	last    map[string]result    // last result sent, by session id
	subs    map[string][]string  // session id → subagent transcript paths
	subMod  map[string]time.Time // session id → subagent folder's last change
	// passUntil is how long the current pass waits for folder lookups, in
	// all: one deadline for every session, never one per session.
	passUntil time.Time
}

func newIndexer(a *adapter) *indexer {
	return &indexer{
		a: a, results: make(chan result), wake: make(chan struct{}, 1),
		tailers: map[string]*tailer{}, last: map[string]result{}, subs: map[string][]string{}, subMod: map[string]time.Time{},
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
		// Small transcripts first: most cards fill in at once, the big ones follow.
		order := make([]job, 0, len(jobs))
		for _, j := range jobs {
			order = append(order, j)
		}
		sizes := map[string]int64{}
		for _, j := range order {
			if fi, err := os.Stat(j.main); err == nil {
				sizes[j.id] = fi.Size()
			}
		}
		slices.SortFunc(order, func(x, y job) int {
			if c := sizes[x.id] - sizes[y.id]; c != 0 {
				if c < 0 {
					return -1
				}
				return 1
			}
			return strings.Compare(x.id, y.id)
		})
		ix.passUntil = time.Now().Add(ix.a.prober.budget)
		for _, j := range order {
			if ctx.Err() != nil {
				return
			}
			res := ix.index(ctx, j)
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

// forget drops the tailers of sessions no longer wanted.
func (ix *indexer) forget(jobs map[string]job) {
	keep := map[string]bool{}
	for _, j := range jobs {
		keep[j.main] = true
		for _, p := range ix.subs[j.id] {
			keep[p] = true
		}
	}
	for p := range ix.tailers {
		if !keep[p] {
			delete(ix.tailers, p)
		}
	}
	for id := range ix.last {
		if _, ok := jobs[id]; !ok {
			delete(ix.last, id)
			delete(ix.subs, id)
			delete(ix.subMod, id)
		}
	}
}

func (ix *indexer) tail(ctx context.Context, path string, now time.Time) *tailer {
	t := ix.tailers[path]
	if t == nil {
		t = newTailer(path, ix.a.prober)
		ix.tailers[path] = t
	}
	t.probeUntil = ix.passUntil
	_, _ = t.update(ctx, now) // a vanished file keeps what was read; the registry notices the end
	return t
}

// index updates one session's transcripts and merges what they say. The
// subagents add their pull requests, artifacts, spawns and activity; the main transcript
// alone says the model, folder, turns and messages received.
func (ix *indexer) index(ctx context.Context, j job) result {
	now := ix.a.now()
	main := ix.tail(ctx, j.main, now)
	res := result{id: j.id, d: main.d}
	res.d.TurnEnds = slices.Clone(main.d.TurnEnds)
	res.d.CreatedPRs = slices.Clone(main.d.CreatedPRs)
	res.d.Artifacts = slices.Clone(main.d.Artifacts)
	res.d.SpawnedNames = slices.Clone(main.d.SpawnedNames)
	res.d.SpawnedTerminalIDs = slices.Clone(main.d.SpawnedTerminalIDs)
	res.d.PeerSenders = slices.Clone(main.d.PeerSenders)

	// The subagent folder is listed again only when it changed.
	subs := ix.subs[j.id]
	if fi, err := os.Stat(j.subDir); err != nil {
		subs = nil
		delete(ix.subMod, j.id)
	} else if !fi.ModTime().Equal(ix.subMod[j.id]) {
		subs = nil
		entries, _ := os.ReadDir(j.subDir)
		for _, e := range entries {
			if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".jsonl") {
				subs = append(subs, filepath.Join(j.subDir, e.Name()))
			}
		}
		ix.subMod[j.id] = fi.ModTime()
	}
	ix.subs[j.id] = subs
	res.subagents = len(subs)
	for _, p := range subs {
		if ctx.Err() != nil {
			break
		}
		sd := ix.tail(ctx, p, now).d
		for _, pr := range sd.CreatedPRs {
			if !slices.Contains(res.d.CreatedPRs, pr) && len(res.d.CreatedPRs) < maxPRs {
				res.d.CreatedPRs = append(res.d.CreatedPRs, pr)
			}
		}
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
	return res
}
