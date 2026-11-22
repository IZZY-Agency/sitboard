package codex

import (
	"cmp"
	"context"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// Catch-up of ended sessions (#114). Once, at
// start, the token records of the Codex threads that had ended before
// sitboard began are read, and their quarter hours go into the Codex day and
// project totals as Backfill facts: Ended, never a card, no turns, no pull
// requests, only the spend and the folder.
//
// Which threads: the ones the board shows as sessions (the terminal client
// and `codex exec`), not the desktop app's or the plugin's; a thread Codex
// spawned counts under its root when the root counts. A session seen live in
// this run (held, or ended after being held) is not read here: its own
// tailer counts it. A fork's copied history holds no token record (74 forks
// on this machine, 0.166 to 0.171, none), and a record that names another
// thread is left out in any case (tailer.record).
//
// A caught-up thread is a kept fact in the observed store (spend.go), like a
// session whose rollout left the scan: it ages out by its own quarter hours,
// is not withdrawn when its date folder leaves the scan, and is replaced by
// the live fact only when a scan has found the thread live (the live read
// carries the caught spend, so a resume never lowers the totals).
//
// Rollouts are found by their modification time, in the scanned day folders
// and in older ones (a thread started two weeks ago, resumed yesterday, writes
// to its first rollout): the cutoff is the first displayed local midnight.
//
// Bounds, all wide (this machine's last seven days: 328 rollouts, 881 MB, the
// largest 150 MB): the files read, the size of one file, and the time of the
// whole discovery and read. A bound cuts silently, like Claude Code's read cap: the cut is
// logged once, without a path, and what is not read is not counted.

var (
	catchupMaxFiles              = 4000             // rollouts read; the newest first
	catchupFileCap         int64 = 512 << 20        // a larger rollout is left out whole, not half read
	catchupBudget                = 60 * time.Second // the read of the rollouts
	catchupDiscoveryBudget       = 30 * time.Second // the walk for older rollouts, apart from the read's
	catchupWalkEntries           = maxWalkEntries   // directory entries listed looking for older rollouts
	catchupDirChunk              = 512              // directory entries read, and checked against the bounds, at a time
	catchupChunk                 = 256              // facts published in one batch
)

// firstDisplayedMidnight is the start of the oldest of the 7 local days the
// summary shows: what a rollout must have been changed since to matter.
func firstDisplayedMidnight(now time.Time) time.Time {
	l := now.Local()
	return time.Date(l.Year(), l.Month(), l.Day()-6, 0, 0, 0, 0, time.Local)
}

// olderFile is a rollout found in a day folder older than the scanned ones.
type olderFile struct {
	rolloutFile
	meta   meta
	metaOK bool
}

// olderRollouts lists the plain rollouts changed since cutoff in the day
// folders older than the scanned range, newest folder first, links not
// followed. Every directory is read in chunks of catchupDirChunk entries, and
// the entry budget (catchupWalkEntries) and ctx are checked before each chunk
// is processed, so a dense folder cannot run past them by more than a chunk:
// cut says a bound stopped it, and what was found before stays. It does not
// choose among the files: the plan takes the newest catchupMaxFiles by
// modification time. It touches no registry state, so it runs in its own
// goroutine.
func (a *adapter) olderRollouts(ctx context.Context, cutoff time.Time) (files []olderFile, cut bool) {
	now := a.now()
	l := now.Local()
	days := int(turnWindow/(24*time.Hour)) + 1
	oldest := time.Date(l.Year(), l.Month(), l.Day(), 12, 0, 0, 0, time.Local).AddDate(0, 0, -days).Format("2006-01-02")
	budget := catchupWalkEntries
	// each calls fn with the entries of dir, a chunk at a time; false when a
	// bound stopped it (or fn did).
	each := func(dir string, fn func([]os.DirEntry) bool) bool {
		f, err := os.Open(dir)
		if err != nil {
			return true
		}
		defer func() { _ = f.Close() }()
		for {
			if ctx.Err() != nil || budget <= 0 {
				return false
			}
			entries, err := f.ReadDir(catchupDirChunk)
			budget -= len(entries)
			if len(entries) > 0 && !fn(entries) {
				return false
			}
			if err != nil { // io.EOF, or a listing that cannot go on
				return true
			}
		}
	}
	subdirs := func(dir string, re *regexp.Regexp) (out []string, ok bool) {
		ok = each(dir, func(entries []os.DirEntry) bool {
			for _, e := range entries {
				if e.IsDir() && re.MatchString(e.Name()) {
					out = append(out, e.Name())
				}
			}
			return true
		})
		slices.Sort(out)
		slices.Reverse(out)
		return out, ok
	}
	sessions := a.sessionsDir()
	years, ok := subdirs(sessions, yearDirRe)
	if !ok {
		cut = true
	}
	for _, y := range years {
		months, ok := subdirs(filepath.Join(sessions, y), twoDigitRe)
		if !ok {
			cut = true
		}
		for _, m := range months {
			days, ok := subdirs(filepath.Join(sessions, y, m), twoDigitRe)
			if !ok {
				cut = true
			}
			for _, d := range days {
				if y+"-"+m+"-"+d >= oldest {
					continue // the scan lists this day
				}
				dir := filepath.Join(sessions, y, m, d)
				if !realDir(filepath.Join(sessions, y)) || !realDir(filepath.Join(sessions, y, m)) || !realDir(dir) {
					continue
				}
				if !each(dir, func(entries []os.DirEntry) bool {
					for _, e := range entries {
						if ctx.Err() != nil {
							return false
						}
						mm := rolloutNameRe.FindStringSubmatch(e.Name())
						if mm == nil || !e.Type().IsRegular() {
							continue
						}
						info, err := e.Info()
						if err != nil || info.ModTime().Before(cutoff) {
							continue
						}
						f := olderFile{rolloutFile: rolloutFile{id: mm[1], path: filepath.Join(dir, e.Name()), mod: info.ModTime(), size: info.Size()}}
						f.meta, f.metaOK = readMeta(f.path, now)
						f.metaOK = f.metaOK && f.meta.ID == f.id
						files = append(files, f)
					}
					return true
				}) {
					cut = true
				}
				if ctx.Err() != nil || budget <= 0 {
					return files, true
				}
			}
			if cut && (ctx.Err() != nil || budget <= 0) {
				return files, true
			}
		}
	}
	return files, cut
}

// catchupJob is one rollout to read, and the root it counts on.
type catchupJob struct {
	id, root, path string
	mod            time.Time
}

// caughtRoot is what a root's fact is built from; it does not depend on the
// registry's sessions, which a scan may drop before the fact is published.
type caughtRoot struct {
	meta meta
	mod  time.Time
}

type catchupPlan struct {
	jobs  []catchupJob
	roots map[string]caughtRoot
	cut   bool // a bound left some out
}

type caughtRead struct {
	job catchupJob
	d   derived
}

type catchupResult struct {
	roots map[string]caughtRoot
	reads []caughtRead
	cut   bool // a bound left some out
}

// caughtItem is one root waiting to be published: its fact, built from the
// threads read for it.
type caughtItem struct {
	id string
	caughtRoot
	parts map[string]*derived // by thread id: the root and the threads spawned under it
}

// catchable: the thread is a session the board would show, not seen live in
// this run, and not a card now (a thread of unknown liveness that changed
// recently is one).
func (r *registry) catchable(s *sess, now time.Time) bool {
	if !s.metaOK || s.meta.Parent != "" || !s.meta.showable() || s.seenLive {
		return false
	}
	return s.live.OK || now.Sub(s.mod) > recentWindow
}

// planCatchup picks the rollouts to read from the scan and from the older
// rollouts found. Registry goroutine.
func (r *registry) planCatchup(older []olderFile) catchupPlan {
	now := r.a.now()
	type thread struct {
		path string
		mod  time.Time
		meta meta
	}
	threads := map[string]thread{}
	roots := map[string]bool{}
	for id, s := range r.sessions {
		if !s.metaOK {
			continue
		}
		threads[id] = thread{s.path, s.mod, s.meta}
		if r.catchable(s, now) {
			roots[id] = true
		}
	}
	for _, f := range older {
		if _, known := r.sessions[f.id]; known || !f.metaOK {
			continue
		}
		threads[f.id] = thread{f.path, f.mod, f.meta}
		// Not seen in the scan: no lock of it is held. Its liveness is known to
		// be "not live" when the last lookup worked; otherwise a recent change
		// may be a session in use.
		if f.meta.Parent == "" && f.meta.showable() && (r.lookupOK || now.Sub(f.mod) > recentWindow) {
			roots[f.id] = true
		}
	}
	// The top of a chain of spawned threads, like topOf, through both sources.
	top := func(id string) string {
		for range maxSpawnDepth {
			t, ok := threads[id]
			if !ok {
				if o := r.obs[id]; o != nil && o.fact == nil && o.root != "" {
					return o.root
				}
				break
			}
			if t.meta.Parent == "" {
				break
			}
			id = t.meta.Parent
		}
		return id
	}
	plan := catchupPlan{roots: map[string]caughtRoot{}}
	for id, t := range threads {
		root := ""
		if t.meta.Parent == "" {
			if roots[id] {
				root = id
			}
		} else if tp := top(t.meta.Parent); roots[tp] {
			root = tp
		}
		if root != "" {
			plan.jobs = append(plan.jobs, catchupJob{id: id, root: root, path: t.path, mod: t.mod})
			plan.roots[root] = caughtRoot{meta: threads[root].meta, mod: threads[root].mod}
		}
	}
	slices.SortFunc(plan.jobs, func(a, b catchupJob) int {
		return cmp.Or(b.mod.Compare(a.mod), cmp.Compare(a.id, b.id))
	})
	if len(plan.jobs) > catchupMaxFiles {
		plan.jobs, plan.cut = plan.jobs[:catchupMaxFiles], true
	}
	return plan
}

// readCatchup reads the planned rollouts for their token records, until ctx
// ends (the time bound). It touches no registry state, so it runs in its own
// goroutine. A rollout it could not read whole is not counted.
func readCatchup(ctx context.Context, plan catchupPlan, now time.Time) catchupResult {
	res := catchupResult{roots: plan.roots, cut: plan.cut}
	for _, j := range plan.jobs {
		if ctx.Err() != nil {
			res.cut = true
			break
		}
		fi, err := os.Lstat(j.path)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if fi.Size() > catchupFileCap {
			res.cut = true
			continue
		}
		t := newTailer(j.path)
		t.id, t.spendOnly, t.cap = j.id, true, catchupFileCap
		if _, err := t.update(ctx, now); err != nil {
			if ctx.Err() != nil {
				res.cut = true
				break
			}
			continue
		}
		if t.sawRecord {
			res.reads = append(res.reads, caughtRead{job: j, d: t.d})
		}
	}
	return res
}

// applyCatchup queues what was read for publishing (publishCatchup). A root
// that is a card by now (seen live, or shown with liveness unknown) is left
// alone: its live fact stands. Registry goroutine.
func (r *registry) applyCatchup(res catchupResult) {
	byRoot := map[string]map[string]*derived{}
	for _, rd := range res.reads {
		d := rd.d
		if byRoot[rd.job.root] == nil {
			byRoot[rd.job.root] = map[string]*derived{}
		}
		byRoot[rd.job.root][rd.job.id] = &d
	}
	r.queue = nil
	for _, id := range slices.Sorted(maps.Keys(byRoot)) {
		// The fact is built from the plan's root data: the scan may have
		// dropped the session (an old folder) since. A root that is a card by
		// now is skipped at publishing.
		r.queue = append(r.queue, caughtItem{id: id, caughtRoot: res.roots[id], parts: byRoot[id]})
	}
}

// publishCatchup publishes up to catchupChunk of the queued roots as one
// batch (the board rebuilds once for it, and the registry serves its timers
// between batches), keeping each as a Backfill fact in the observed store.
// more says whether some remain. Registry goroutine.
func (r *registry) publishCatchup() (more bool) {
	n := min(len(r.queue), catchupChunk)
	var facts []model.Fact
	for _, it := range r.queue[:n] {
		if _, card := r.sent[it.id]; card {
			continue // a card since it was queued
		}
		if o := r.obs[it.id]; o != nil && o.fact != nil {
			continue // a fact kept since it was queued is newer: the thread was followed live
		}
		ds := make([]*derived, 0, len(it.parts))
		for _, k := range slices.Sorted(maps.Keys(it.parts)) {
			ds = append(ds, it.parts[k])
		}
		sp := spendOf(ds...)
		if len(sp.Spans) == 0 {
			continue // no use in the days: nothing to count
		}
		f := model.SessionFact{
			Key:          model.SessionKey{Tool: model.ToolCodex, ID: it.id},
			ToolVersion:  it.meta.Version,
			Status:       model.Unknown[model.Status](),
			StartedAt:    it.meta.StartedAt,
			LastActivity: it.mod,
			Live:         model.Of(false),
			Ended:        true,
			EndedAt:      it.mod,
			Folder:       it.meta.Cwd,
			Headless:     it.meta.Source == "exec",
			Backfill:     true,
			Spend:        sp,
		}
		o := &observed{root: it.id, fact: &f, parts: it.parts}
		r.touch(o)
		if r.obs == nil {
			r.obs = map[string]*observed{}
		}
		r.obs[it.id] = o
		facts = append(facts, f)
	}
	r.queue = r.queue[n:]
	if len(facts) > 0 {
		r.emit(model.Batch{Facts: facts})
	}
	r.boundObserved()
	return len(r.queue) > 0
}
