package claude

import (
	"cmp"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
)

// A session's folder is where it does its work, not where it ran its last
// command: a coordinator that runs a few read-only commands in another
// repository must not move there. Each main-chain (non-sidechain) record's
// cwd, cleaned (so "a", "a/" and "a/." are one folder), is a vote:
//   - Only the most recent voteWindow votes count (a ring), so a short visit
//     loses and a real move takes effect after about half the window. Reading
//     the transcript again from zero gives the same answer.
//   - Votes count by repository: a folder's group is the nearest folder at or
//     above it with a .git entry, the walk stopping below the home folder (a
//     home kept in git never forms a group), or the folder itself when there
//     is none. Work spread over a repository's subfolders is not outvoted by a
//     short visit elsewhere. A worktree has its own .git entry, so its votes
//     count apart from its main checkout's; that is accepted for now.
//   - The group with the most votes wins; inside it, the folder with the most
//     votes is the session's folder. Ties go to the most recent.
//   - A folder that no longer exists (a worktree removed while its session
//     kept running) has its votes credited to its nearest existing ancestor,
//     so a removed worktree keeps the session in its repository.
//   - A cwd with a control character can never be a real folder: its record
//     does not vote.
//
// The file system is never touched while a record is read. Groups and
// existence are looked up when the folder is chosen, only for folders held
// in the window, at most probeLookups and probeChecks per update, off the
// reading goroutine, with at most maxProbes batches in flight in the whole
// process (procProber). One indexing pass waits for them at most probeBudget
// in all (probeUntil); a hung mount delays the answer, never the reading. While a
// lookup is pending the previous folder stays (on the first read, none, so
// the launch folder is used): no interim pick is sent. Each folder's group is
// looked up once while it is held, and again when it comes back after being
// gone; its existence is checked again every folderRecheck, and the folder is
// chosen again on that schedule even when no new record arrives.

const (
	voteWindow    = 400
	folderRecheck = time.Minute
	probeLookups  = 32
	probeChecks   = 32
	probeBudget   = 100 * time.Millisecond
	maxDepthUp    = 64 // bounds the walk up looking for .git
	maxProbes     = 4  // lookup batches in flight in the whole process
)

// A prober runs the folder lookups of the tailers that share it.
type prober struct {
	run    func(jobs []probeJob, home string) []probeResult
	slots  chan struct{} // a token per lookup batch in flight
	budget time.Duration // how long one pass waits for its lookups
	// expiry fires at a pass's deadline; a nil channel never fires.
	expiry func(until time.Time) <-chan time.Time
}

// procProber is the product's only prober: every tailer of the process shares
// it, so its slots cap the lookup batches in flight in the whole process.
// Tests make their own, so a batch one test leaves running cannot hold a slot
// another test needs.
var procProber = &prober{
	run: runProbe, slots: make(chan struct{}, maxProbes), budget: probeBudget, expiry: at,
}

var expiredNow = func() chan time.Time { c := make(chan time.Time); close(c); return c }()

// at fires at until, or at once when until has passed.
func at(until time.Time) <-chan time.Time {
	if wait := time.Until(until); wait > 0 {
		return time.After(wait)
	}
	return expiredNow
}

// validFolder cleans a cwd and reports whether it can be a folder: absolute,
// with no control character.
func validFolder(cwd string) (string, bool) {
	if !filepath.IsAbs(cwd) || strings.ContainsFunc(cwd, unicode.IsControl) {
		return "", false
	}
	return filepath.Clean(cwd), true
}

type heldFolder struct {
	n       int       // votes in the window
	last    uint64    // the record number of its latest vote
	group   string    // "" until looked up
	gone    bool      // it did not exist when last checked
	checked time.Time // when its existence was last checked; zero: never
	anc     string    // when gone: its nearest existing ancestor
	ancGrp  string    // when gone: that ancestor's group
}

type probeJob struct {
	folder      string
	look, check bool
}

type probeResult struct {
	probeJob
	group       string
	gone        bool
	anc, ancGrp string
}

// vote counts one main-chain record in folder (already cleaned).
func (t *tailer) vote(folder string) {
	t.seq++
	if t.window == nil {
		t.window = make([]string, voteWindow)
	}
	if old := t.window[t.wpos]; old != "" {
		if h := t.held[old]; h != nil {
			if h.n--; h.n == 0 {
				delete(t.held, old)
			}
		}
	}
	t.window[t.wpos] = folder
	t.wpos = (t.wpos + 1) % voteWindow
	h := t.held[folder]
	if h == nil {
		if t.held == nil {
			t.held = map[string]*heldFolder{}
		}
		h = &heldFolder{}
		t.held[folder] = h
	}
	h.n++
	h.last = t.seq
	t.votesChanged = true
}

// chooseFolder looks up what it can and sets t.d.Folder. It reports whether
// the folder changed. Called at the end of an update.
func (t *tailer) chooseFolder(now time.Time) bool {
	if len(t.held) == 0 || !t.votesChanged && now.Sub(t.pickedAt) < folderRecheck {
		return false
	}
	t.probe(now)
	t.pickedAt = now
	if t.probing != nil || t.unresolved() {
		t.votesChanged = true // keep the previous folder; try again on the next update
		return false
	}
	t.votesChanged = false
	old := t.d.Folder
	t.d.Folder = t.pickFolder()
	return t.d.Folder != old
}

// settled reports that no folder lookup is pending: the folder chosen is
// final for what was read.
func (t *tailer) settled() bool {
	return t.probing == nil && !t.unresolved()
}

func (t *tailer) unresolved() bool {
	for _, h := range t.held {
		if h.group == "" || h.checked.IsZero() {
			return true
		}
	}
	return false
}

// probe starts the lookups the held folders need, most recent first, unless
// a batch of this tailer is still running or all the prober's slots are
// taken, and takes what arrives before probeUntil (the prober's budget from
// now when unset).
func (t *tailer) probe(now time.Time) {
	p := t.prober
	if t.probing == nil {
		jobs := t.jobs(now)
		if len(jobs) == 0 {
			return
		}
		select {
		case p.slots <- struct{}{}:
		default:
			return // the cap on batches in flight is reached: try later
		}
		ch := make(chan []probeResult, 1)
		home := t.home
		go func() {
			defer func() { <-p.slots }()
			ch <- p.run(jobs, home)
		}()
		t.probing = ch
	}
	select { // an answer already in is taken, whatever the deadline says
	case r := <-t.probing:
		t.probing = nil
		t.apply(r, now)
		return
	default:
	}
	until := t.probeUntil
	if until.IsZero() {
		until = time.Now().Add(p.budget)
	}
	select {
	case r := <-t.probing:
		t.probing = nil
		t.apply(r, now)
	case <-p.expiry(until):
	}
}

func (t *tailer) jobs(now time.Time) []probeJob {
	folders := make([]string, 0, len(t.held))
	for f := range t.held {
		folders = append(folders, f)
	}
	slices.SortFunc(folders, func(a, b string) int { return cmp.Compare(t.held[b].last, t.held[a].last) })
	var jobs []probeJob
	looks, checks := 0, 0
	for _, f := range folders {
		h := t.held[f]
		j := probeJob{folder: f}
		if h.group == "" && looks < probeLookups {
			j.look, looks = true, looks+1
		}
		if (h.checked.IsZero() || now.Sub(h.checked) >= folderRecheck) && checks < probeChecks {
			j.check, checks = true, checks+1
		}
		if j.look || j.check {
			jobs = append(jobs, j)
		}
	}
	return jobs
}

func (t *tailer) apply(results []probeResult, now time.Time) {
	for _, r := range results {
		h := t.held[r.folder]
		if h == nil {
			continue // no longer held
		}
		if r.check {
			if h.gone && !r.gone {
				h.group = "" // it came back: look its group up again
			}
			h.gone, h.checked, h.anc, h.ancGrp = r.gone, now, r.anc, r.ancGrp
		}
		if r.look && (!r.check || !r.gone) {
			h.group = r.group
		}
	}
}

// runProbe does the file system lookups. It shares nothing with the tailer.
func runProbe(jobs []probeJob, home string) []probeResult {
	out := make([]probeResult, 0, len(jobs))
	for _, j := range jobs {
		r := probeResult{probeJob: j}
		if j.look {
			r.group = repoGroup(j.folder, home)
		}
		if j.check {
			_, err := os.Stat(j.folder)
			if r.gone = errors.Is(err, fs.ErrNotExist); r.gone {
				r.anc = nearestExisting(j.folder)
				r.ancGrp = repoGroup(r.anc, home)
			}
		}
		out = append(out, r)
	}
	return out
}

// pickFolder applies the rule above; every held folder is looked up. A gone
// folder's votes count for its nearest existing ancestor.
func (t *tailer) pickFolder() string {
	type score struct {
		n    int
		last uint64
	}
	at := map[string]score{}       // effective folder -> its votes
	groupOf := map[string]string{} // effective folder -> its group
	for f, h := range t.held {
		e, g := f, cmp.Or(h.group, f)
		if h.gone {
			e, g = h.anc, h.ancGrp
		}
		s := at[e]
		s.n += h.n
		s.last = max(s.last, h.last)
		at[e] = s
		groupOf[e] = g
	}
	groups := map[string]score{}
	for e, s := range at {
		g := groups[groupOf[e]]
		g.n += s.n
		g.last = max(g.last, s.last)
		groups[groupOf[e]] = g
	}
	better := func(a, b score) bool { return a.n > b.n || a.n == b.n && a.last > b.last }
	var win string
	var ws score
	for g, s := range groups {
		if win == "" || better(s, ws) {
			win, ws = g, s
		}
	}
	var folder string
	var fs score
	for e, s := range at {
		if groupOf[e] == win && (folder == "" || better(s, fs)) {
			folder, fs = e, s
		}
	}
	return folder
}

// nearestExisting returns the nearest ancestor of a folder that exists.
func nearestExisting(folder string) string {
	d := filepath.Dir(folder)
	for range maxDepthUp {
		if _, err := os.Stat(d); err == nil {
			return d
		}
		up := filepath.Dir(d)
		if up == d {
			break
		}
		d = up
	}
	return d
}

// repoGroup returns the nearest folder at or above folder that has a .git
// entry, or folder itself. The walk stops below home and below the root:
// neither forms a group. It only looks at file names.
func repoGroup(folder, home string) string {
	d := folder
	for range maxDepthUp {
		if d == home || filepath.Dir(d) == d {
			break
		}
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		d = filepath.Dir(d)
	}
	return folder
}
