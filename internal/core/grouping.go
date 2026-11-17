package core

import (
	"cmp"
	"errors"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

// Projects group in one level (see contract.Project):
//   - An umbrella is a plain folder (git answered it is in no repository; see
//     resolve), never the home folder or the root, where (a) at least two
//     agents sit at the same time, or (b) an agent that sits there is the
//     matched coordinator of an agent working inside it, up to 3 levels down;
//     or a remembered one. A single agent in a plain folder (a quick question
//     asked in a broad folder) makes no umbrella. A coordinator and its own
//     worker sitting in the same plain folder count as two agents: accepted,
//     that is a real project folder.
//   - 1. Repositories first: a project that is not an umbrella folds into the
//     umbrella that directly contains its main root (depth 1 only, so a broad
//     folder does not swallow every repository below it).
//   - 2. Then plain folders where an agent sits, deepest first: one 1 to 3
//     levels below an umbrella folds into the nearest one, unless it has
//     parts of its own (a repository from step 1, or a plain folder from this
//     step); then it is a project root and stays top-level.
//   - A parent never has a parent: whatever gets a part stays top-level.
//   - Umbrellas that stay top-level are remembered (in memory and in the
//     state file) so the grouping does not flicker when the last agent in the
//     folder ends; a plain folder that folded is not. A remembered umbrella is
//     listed in projects, with no cards of its own, whenever one of its parts
//     is. It stops counting when it has a project of its own that is no
//     longer plain (git init), and is forgotten after
//     state.UmbrellaRetention, or when its folder was gone on umbrellaMisses
//     hourly checks in a row.
//   - Counts stay each project's own; Card.ProjectID stays the part.

// maxPlainDepth is how far below an umbrella a plain folder still folds.
const maxPlainDepth = 3

// umbrellaSettle is how long a folder must hold an agent before it is
// remembered as an umbrella. A session can sit in its launch folder for a few
// seconds before its transcript is read and its real folder is known; such a
// brief umbrella groups while it lasts but is never remembered.
const umbrellaSettle = 2 * time.Minute

// The gone check for remembered umbrellas runs at most every
// umbrellaCheckEvery, off the tick, gives up after umbrellaCheckTimeout (a
// hung mount: nothing is counted, and no new check starts until the hung
// worker returns), and forgets a folder only after umbrellaMisses checks in a
// row found it gone (a drive unplugged for a moment keeps its umbrellas). The
// miss count lives in memory only, so a restart starts it again: accepted,
// the 30-day expiry is the backstop.
const (
	umbrellaCheckEvery = time.Hour
	umbrellaMisses     = 3
)

var (
	umbrellaCheckTimeout = 10 * time.Second
	statFolder           = os.Stat // replaced in tests
)

// umbrellaRewrite is how often a live umbrella's last-seen time is written
// again, so the state file is not rewritten on every change.
const umbrellaRewrite = time.Hour

// liveUmbrellaLocked reports whether the session's folder makes an umbrella.
// Callers hold b.mu.
func (b *Board) liveUmbrellaLocked(s model.SessionFact) bool {
	f := s.Folder
	if f == "" || !b.plain[f] || b.repoLocked(f).MainRoot != f {
		return false
	}
	return b.mayBeUmbrella(f)
}

// qualifiedLocked returns the plain folders that are umbrellas now: two or
// more agents in them, or a coordinator in them with a worker inside, up to
// maxPlainDepth levels down. Callers hold b.mu.
func (b *Board) qualifiedLocked(shown []model.SessionFact, parents map[model.SessionKey]model.Known[model.SessionKey], plainLive map[string]int) map[string]bool {
	out := map[string]bool{}
	for f, n := range plainLive {
		if n >= 2 {
			out[f] = true
		}
	}
	on := map[model.SessionKey]model.SessionFact{}
	for _, s := range shown {
		on[s.Key] = s
	}
	for _, s := range shown {
		p := parents[s.Key]
		if !p.OK {
			continue
		}
		c, ok := on[p.V]
		if !ok || plainLive[c.Folder] == 0 {
			continue
		}
		if d := depthBelow(c.Folder, s.Folder); d >= 1 && d <= maxPlainDepth {
			out[c.Folder] = true
		}
	}
	return out
}

// depthBelow is how many levels folder lies below root, or 0 when it is not
// below it.
func depthBelow(root, folder string) int {
	rel, err := filepath.Rel(root, folder)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}

func (b *Board) mayBeUmbrella(folder string) bool {
	return filepath.IsAbs(folder) && folder != b.home && filepath.Dir(folder) != folder
}

// groupLocked sets ParentID and Area on projects, and adds the umbrellas that
// have parts but no card of their own. mainRoots maps project id to its main
// root; live are the plain folders with an agent in them now, qualified those
// of them that are umbrellas. It returns the qualified umbrellas that stay
// top-level, sorted. Callers hold b.mu.
func (b *Board) groupLocked(projects map[string]*contract.Project, mainRoots map[string]string, live map[string]int, qualified map[string]bool, now time.Time) []string {
	umbrellas := maps.Clone(qualified)
	ownRoot := map[string]bool{}
	for _, r := range mainRoots {
		ownRoot[r] = true
	}
	for u, at := range b.umbrellas {
		if now.Sub(at) > state.UmbrellaRetention || !b.mayBeUmbrella(u) {
			continue
		}
		if ownRoot[u] && !b.plain[u] {
			continue // it has a project of its own that is no longer plain
		}
		umbrellas[u] = true
	}
	ids := slices.Sorted(maps.Keys(projects))
	parentOf := map[string]string{} // project id -> umbrella folder
	hasParts := map[string]bool{}   // umbrella folder -> it has a part

	// 1. Repositories, into the umbrella that directly contains them.
	for _, id := range ids {
		root := mainRoots[id]
		if root == "" || umbrellas[root] {
			continue
		}
		if d := filepath.Dir(root); d != root && umbrellas[d] && contract.ProjectID(d) != id {
			parentOf[id], hasParts[d] = d, true
		}
	}
	// 2. Plain folders with an agent, deepest first, into the nearest umbrella
	// 1 to maxPlainDepth levels up, unless they have parts of their own.
	plain := slices.Collect(maps.Keys(live))
	slices.SortFunc(plain, func(x, y string) int {
		return cmp.Or(cmp.Compare(depth(y), depth(x)), cmp.Compare(x, y))
	})
	folded := map[string]bool{}
	for _, c := range plain {
		if hasParts[c] {
			continue
		}
		for d, k := filepath.Dir(c), 1; k <= maxPlainDepth && d != filepath.Dir(d); d, k = filepath.Dir(d), k+1 {
			if umbrellas[d] && contract.ProjectID(d) != contract.ProjectID(c) {
				parentOf[contract.ProjectID(c)], hasParts[d], folded[c] = d, true, true
				break
			}
		}
	}

	for _, id := range ids {
		if _, part := parentOf[id]; !part && mainRoots[id] != "" {
			projects[id].Area = b.area(mainRoots[id])
		}
	}
	for _, id := range ids {
		parent, ok := parentOf[id]
		if !ok {
			continue
		}
		pid := contract.ProjectID(parent)
		if _, ok := projects[pid]; !ok { // a remembered umbrella with no card of its own
			projects[pid] = &contract.Project{
				ID:   pid,
				Name: clean(filepath.Base(parent)),
				Path: b.homeRel(parent),
				Area: b.area(parent),
			}
		}
		projects[id].ParentID = pid
		projects[id].Area = b.area(parent)
	}

	var top []string
	for u := range qualified {
		if !folded[u] {
			top = append(top, u)
		}
	}
	slices.Sort(top)
	return top
}

func depth(p string) int { return strings.Count(filepath.Clean(p), string(filepath.Separator)) }

// area is the base name of the folder that directly contains a top-level
// project's folder, or "" when that folder is the home folder or the root,
// when the project is outside the home folder, or when a folder of its path
// below home starts with ".".
func (b *Board) area(root string) string {
	if b.home == "" || b.home == "/" {
		return ""
	}
	rel, err := filepath.Rel(b.home, root)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	for seg := range strings.SplitSeq(rel, string(filepath.Separator)) {
		if strings.HasPrefix(seg, ".") {
			return ""
		}
	}
	dir := filepath.Dir(root)
	if dir == b.home || filepath.Dir(dir) == dir {
		return ""
	}
	return clean(filepath.Base(dir))
}

// settledLocked reports whether every source finished its first pass. Before
// that, a session's folder may still be its launch folder (its transcript not
// read yet), so no umbrella is remembered. Callers hold b.mu.
func (b *Board) settledLocked() bool {
	for _, s := range b.sources {
		if !s.Ready {
			return false
		}
	}
	return true
}

// noteUmbrellasLocked remembers the umbrellas that have had an agent in them
// for umbrellaSettle (or are remembered already), and queues their time for
// the state file at most once per umbrellaRewrite. Callers hold b.mu.
func (b *Board) noteUmbrellasLocked(live []string, now time.Time) {
	for u := range b.liveSince {
		if !slices.Contains(live, u) {
			delete(b.liveSince, u)
		}
	}
	for _, u := range live {
		at, known := b.umbrellas[u]
		if !known {
			since, ok := b.liveSince[u]
			if !ok {
				b.liveSince[u] = now
				continue
			}
			if now.Sub(since) < umbrellaSettle {
				continue
			}
			delete(b.liveSince, u)
		}
		if known && now.Sub(at) < umbrellaRewrite {
			continue
		}
		b.umbrellas[u] = now
		b.umbrellaTo[u] = now
	}
	if len(b.umbrellas) > state.MaxUmbrellas { // the same cut as the state file
		b.umbrellas = state.PruneUmbrellas(b.umbrellas, now)
	}
}

// flushUmbrellas runs on the tick, without b.mu: it forgets remembered
// umbrellas that are too old, writes the changes to the state file, and
// starts the hourly gone check.
func (b *Board) flushUmbrellas() {
	now := b.now()
	b.mu.Lock()
	touch := slices.Sorted(maps.Keys(b.umbrellaTo))
	clear(b.umbrellaTo)
	var old []string
	for u, at := range b.umbrellas {
		if now.Sub(at) > state.UmbrellaRetention {
			old = append(old, u)
			delete(b.umbrellas, u)
			delete(b.missing, u)
		}
	}
	if len(old) > 0 {
		b.rebuildLocked()
	}
	if !b.goneBusy && (b.goneAt.IsZero() || now.Sub(b.goneAt) >= umbrellaCheckEvery) {
		live := map[string]bool{}
		for _, u := range b.view.umbrellas {
			live[u] = true
		}
		var check []string
		for u := range b.umbrellas {
			if !live[u] {
				check = append(check, u)
			}
		}
		b.goneAt = now
		if len(check) > 0 {
			b.goneBusy = true
			b.resolving.Add(1)
			go b.checkGone(check)
		}
	}
	b.mu.Unlock()
	b.saveUmbrellas(touch, old, now)
}

// checkGone looks whether remembered umbrella folders still exist, off the
// tick, and forgets those found gone umbrellaMisses checks in a row. When the
// look takes longer than umbrellaCheckTimeout, nothing is counted, and
// goneBusy stays set until the worker returns. After Run returned, nothing
// changes and nothing is written.
func (b *Board) checkGone(paths []string) {
	defer b.resolving.Done()
	done := make(chan map[string]bool, 1)
	abandoned := false // guarded by b.mu
	go func() {
		gone := map[string]bool{}
		for _, u := range paths {
			if _, err := statFolder(u); errors.Is(err, fs.ErrNotExist) {
				gone[u] = true
			}
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if abandoned {
			b.goneBusy = false
			return
		}
		done <- gone // buffered: never blocks
	}()
	timer := time.NewTimer(umbrellaCheckTimeout)
	defer timer.Stop()
	var gone map[string]bool
	select {
	case gone = <-done:
	case <-timer.C:
	}
	b.mu.Lock()
	if gone == nil {
		select {
		case gone = <-done: // it finished just now
		default:
			abandoned = true
			b.mu.Unlock()
			log.Printf("core: checking remembered umbrella folders took over %s; skipped", umbrellaCheckTimeout)
			return
		}
	}
	b.goneBusy = false
	if b.closed {
		b.mu.Unlock()
		return
	}
	var forget []string
	for _, u := range paths {
		if _, ok := b.umbrellas[u]; !ok {
			continue
		}
		if !gone[u] {
			delete(b.missing, u)
			continue
		}
		if b.missing[u]++; b.missing[u] >= umbrellaMisses {
			forget = append(forget, u)
			delete(b.umbrellas, u)
			delete(b.missing, u)
		}
	}
	if len(forget) > 0 {
		b.rebuildLocked()
	}
	b.mu.Unlock()
	b.saveUmbrellas(nil, forget, b.now())
}

// saveUmbrellas writes umbrella changes to the state file. Callers do not
// hold b.mu.
func (b *Board) saveUmbrellas(touch, forget []string, now time.Time) {
	if b.opts.State == nil || len(touch)+len(forget) == 0 {
		return
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	if err := b.opts.State.TouchUmbrellas(touch, now); err != nil {
		log.Printf("core: could not save umbrella folders: %v", err)
	}
	if err := b.opts.State.ForgetUmbrellas(forget); err != nil {
		log.Printf("core: could not forget umbrella folders: %v", err)
	}
}
