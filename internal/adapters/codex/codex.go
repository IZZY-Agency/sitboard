// Package codex is the Codex CLI source adapter.
//
// It reads two undocumented Codex records (shapes checked on 0.167.2 to
// 0.171.0):
//
//   - <CODEX_HOME>/sessions/YYYY/MM/DD/rollout-<time>-<thread id>.jsonl, one
//     append-only JSONL file per thread (Codex's word for a session). Its
//     first line, session_meta, gives the id, folder, version, source and, for
//     a thread Codex spawned itself, its parent. Later lines give the model and
//     effort of each turn, the turn starts and ends, and the commands run with
//     their output. Rollouts untouched for 7 days are compressed (.jsonl.zst)
//     and never read.
//   - <CODEX_HOME>/thread-writer-locks/<thread id>.lock, which the process
//     that writes a thread holds open (and locked). Since 0.171 that is the
//     managed app-server daemon, not the terminal, and it keeps the locks
//     after the terminal quit, so a lock says "loaded", not "open in a
//     terminal". sitboard never takes the lock.
//
// Only terminal sessions (originator "codex-tui") and `codex exec` become
// sessions (meta.showable). A terminal session is live when its lock is held
// and a terminal client process is open in its folder; that process also
// gives the agterm terminal (tuis.go). Threads Codex spawned itself
// (thread_source "subagent", with parent_thread_id) run inside their parent's
// process, like Claude Code's in-process subagents: they are never cards, only
// a count on their parent. Threads of the desktop app and of Claude Code's
// plugin are left out.
//
// A session is shown once it has been seen live during this run. Sessions
// that had ended before sitboard started are read once at start for their
// spend alone (catchup.go): never a card. A session's name is the title
// <CODEX_HOME>/session_index.jsonl keeps for it (see names.go), which the model wrote. It never reads Codex's SQLite
// files, and never writes anything under CODEX_HOME.
package codex

import (
	"bytes"
	"cmp"
	"context"
	"log"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/proc"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// turnWindow is how far back rollouts are looked at and turn ends are kept.
const turnWindow = 7 * 24 * time.Hour

// recentWindow: when liveness cannot be told (no lsof, no /proc), a rollout
// changed this recently is shown with liveness unknown ("recently active").
const recentWindow = 15 * time.Minute

// terminalOriginator is what the terminal client writes as originator (with
// source "vscode": it is an app-server client since 0.171). Older versions
// are expected to write source "cli"; the desktop app, Claude Code's plugin and
// `codex exec` write other originators ("Codex Desktop", "codex_work_desktop",
// "Claude Code", "codex_exec"). Checked on this machine's rollouts (#111).
const terminalOriginator = "codex-tui"

// terminal: the thread is a terminal session (not a spawned child).
func (m meta) terminal() bool {
	return m.Source != "subagent" && (m.Originator == terminalOriginator || m.Source == "cli")
}

// showable: the threads that become sessions on the board, the terminal
// client's and `codex exec`'s (the owner's choice, #93). Spawned children,
// the desktop app's threads and the plugin's are left out.
func (m meta) showable() bool { return m.terminal() || m.Source == "exec" }

// Options configure the adapter.
type Options struct {
	Home string // the user's home folder
	// CodexHome is Codex's own folder, the CODEX_HOME environment variable;
	// "" means <Home>/.codex.
	CodexHome string
	Now       func() time.Time // clock; nil means time.Now
}

type adapter struct {
	root     string
	now      func() time.Time
	holders  holdersFunc
	starts   proc.StartsFunc
	tuis     tuisFunc      // the user's terminal clients
	tick     time.Duration // rollout growth (stat only)
	liveTick time.Duration // file list and lock holders
}

// New returns the Codex watcher.
func New(opts Options) source.Watcher {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	root := opts.CodexHome
	if root == "" {
		root = filepath.Join(opts.Home, ".codex")
	}
	starts := proc.DefaultStarts()
	return &adapter{
		tuis: newTUIs(sameCodexHome(root, filepath.Join(opts.Home, ".codex"))),
		root: root, now: now,
		holders: defaultHolders(), starts: starts,
		tick: 2 * time.Second, liveTick: 4 * time.Second,
	}
}

func (a *adapter) Kind() source.Kind { return source.KindCodex }

func (a *adapter) sessionsDir() string { return filepath.Join(a.root, "sessions") }
func (a *adapter) locksDir() string    { return filepath.Join(a.root, "thread-writer-locks") }

// Probe reports Codex as present when its folder exists. The version is the
// one the newest rollout of the last week recorded.
func (a *adapter) Probe(context.Context) source.Status {
	st := source.Status{Kind: source.KindCodex}
	if fi, err := os.Stat(a.root); err != nil || !fi.IsDir() {
		st.State, st.Detail = source.StateMissing, "Codex not found"
		return st
	}
	st.State, st.Detail = source.StateOK, "approval waits show only through agterm"
	var newest time.Time
	var path string
	for _, f := range a.rollouts() {
		if f.mod.After(newest) {
			newest, path = f.mod, f.path
		}
	}
	if path != "" {
		if m, ok := readMeta(path, a.now()); ok {
			st.Version = m.Version
		}
	}
	st.Tested = slices.Contains(testedVersions, st.Version)
	return st
}

type rolloutFile struct {
	id   string
	path string
	mod  time.Time
	size int64
}

// realDir: p is a folder itself, not a link to one.
func realDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

// rollouts lists the plain rollout files changed within turnWindow, in the
// day folders of that window (Codex names them by local date). The sessions
// folder itself may be a link (people move it); no folder below it is
// followed if it is one, so that a link cannot bring in files from elsewhere.
func (a *adapter) rollouts() []rolloutFile {
	now := a.now()
	var out []rolloutFile
	// A file is looked at when it changed within turnWindow, or since the first
	// displayed local midnight when that is earlier (a week with a clock change
	// is longer than turnWindow: the first hour of its oldest day matters).
	since := now.Add(-turnWindow)
	if c := firstDisplayedMidnight(now); c.Before(since) {
		since = c
	}
	// Codex names the day folders by local date: from a day before the
	// window to a day after today (a clock change), one calendar day at a time.
	l := now.Local()
	today := time.Date(l.Year(), l.Month(), l.Day(), 12, 0, 0, 0, time.Local)
	days := int(turnWindow/(24*time.Hour)) + 1
	for i := -days; i <= 1; i++ {
		d := today.AddDate(0, 0, i)
		year := filepath.Join(a.sessionsDir(), d.Format("2006"))
		month := filepath.Join(year, d.Format("01"))
		dir := filepath.Join(month, d.Format("02"))
		if !realDir(year) || !realDir(month) || !realDir(dir) {
			continue
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			m := rolloutNameRe.FindStringSubmatch(e.Name())
			if m == nil || !e.Type().IsRegular() {
				continue
			}
			info, err := e.Info()
			if err != nil || info.ModTime().Before(since) {
				continue
			}
			out = append(out, rolloutFile{id: m[1], path: filepath.Join(dir, e.Name()), mod: info.ModTime(), size: info.Size()})
		}
	}
	return out
}

// sess is what the adapter knows about one thread.
type sess struct {
	path       string
	mod        time.Time
	meta       meta
	metaOK     bool
	tries      int // reads of an unchanged file whose first line was not (yet) a session_meta
	triedSize  int64
	triedMod   time.Time
	t          *tailer
	live       model.Known[bool]
	seenLive   bool // live at some point during this run: only those are shown
	ended      bool
	endedAt    time.Time
	termID     string
	carry      *model.Spend        // spend kept while its rollout was out of the scan (handOff)
	carryParts map[string]*derived // a caught-up thread's spend by thread id, carried into its live fact (fact)
	card       bool                // the last scan made it a card: only those are tailed between scans
}

// shown: a session the board gets.
func (s *sess) shown() bool {
	return s.metaOK && s.meta.showable() && (s.seenLive || !s.live.OK)
}

// cardOf: the session is a card now. One the catch-up counted (a Backfill fact
// in the store) is none until a scan has seen it live, whatever liveness
// lookups say meanwhile: a failed lookup only makes a recent thread look
// "recently active", and must not turn a thread known to have ended into a
// card (which would drop its spend and then withdraw it).
func (r *registry) cardOf(id string, s *sess) bool {
	if !s.shown() {
		return false
	}
	if o := r.obs[id]; o != nil && o.fact != nil && o.fact.Backfill && !s.seenLive {
		return false
	}
	return true
}

type registry struct {
	a         *adapter
	emit      func(model.Fact)
	sessions  map[string]*sess
	children  map[string]int       // parent thread id → spawned threads seen
	kids      map[string][]string  // the same, the spawned threads' ids, for spend
	obs       map[string]*observed // spend observed, kept after rollouts leave the scan (spend.go)
	queue     []caughtItem         // caught-up roots not yet published (catchup.go)
	lookupOK  bool                 // the last scan could tell who holds the locks
	useTick   uint64               // the store's clock of use
	sent      map[string]model.SessionFact
	located   map[string]string    // thread id → rollout found outside the scanned folders
	notFound  map[string]time.Time // thread id → when a search for its rollout found none
	walks     int                  // searches of the date folders made (tests)
	walkLimit int                  // entries one search may list; 0 means maxWalkEntries (tests)
	names     *names
	cache     *holderCache
	locks     string // the locks folder, links resolved (lsof reports real paths)
}

// Run emits the live sessions, then every change, until ctx ends.
func (a *adapter) Run(ctx context.Context, emit func(model.Fact)) error {
	ready := model.SourceReady{Kind: string(source.KindCodex)}
	if fi, err := os.Stat(a.root); err != nil || !fi.IsDir() {
		emit(ready)
		emit(model.BackfillDone{Kind: string(source.KindCodex)}) // nothing to read
		return nil
	}
	r := &registry{a: a, emit: emit, sessions: map[string]*sess{}, children: map[string]int{}, sent: map[string]model.SessionFact{}, located: map[string]string{}, notFound: map[string]time.Time{}, names: newNames(filepath.Join(a.root, "session_index.jsonl"))}
	r.rescan(ctx)
	if ctx.Err() != nil {
		return nil
	}
	emit(ready)
	// The catch-up of ended sessions (catchup.go): the discovery of older
	// rollouts in its own goroutine, then the plan on this one, then the read
	// in another, then the facts published in chunks, so that the ticks below
	// are served in between. The walk and the read have a time bound each.
	dctx, cancelDiscovery := context.WithTimeout(ctx, catchupDiscoveryBudget)
	defer cancelDiscovery()
	type found struct {
		files []olderFile
		cut   bool
	}
	discovering := make(chan found, 1)
	go func() {
		files, cut := a.olderRollouts(dctx, firstDisplayedMidnight(a.now()))
		discovering <- found{files, cut}
	}()
	var reading chan catchupResult
	cont := make(chan struct{}, 1) // a chunk is due
	ticker := time.NewTicker(a.tick)
	defer ticker.Stop()
	liveTicker := time.NewTicker(a.liveTick)
	defer liveTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case f := <-discovering:
			discovering = nil
			plan, now := r.planCatchup(f.files), a.now()
			plan.cut = plan.cut || f.cut
			reading = make(chan catchupResult, 1)
			// The read has a budget of its own: a slow walk cannot leave it with
			// an expired context.
			rctx, cancelRead := context.WithTimeout(ctx, catchupBudget)
			defer cancelRead()
			go func() { reading <- readCatchup(rctx, plan, now) }()
		case res := <-reading:
			reading = nil
			r.applyCatchup(res)
			if res.cut {
				log.Printf("codex: the catch-up of ended sessions was cut by a bound")
			}
			cont <- struct{}{}
		case <-cont:
			if r.publishCatchup() {
				cont <- struct{}{}
			} else {
				emit(model.BackfillDone{Kind: string(source.KindCodex)})
			}
		case <-ticker.C:
			r.tailAll(ctx)
		case <-liveTicker.C:
			r.rescan(ctx)
		}
	}
}

// maxMetaTries bounds how often a first line that is not a session_meta is
// read again while the file does not change. A file that grew or was touched
// since the last read starts over: it may still be being written.
const maxMetaTries = 3

// locateRetry: how long a held lock whose rollout was not found is left alone
// before the session folders are searched for it again.
const locateRetry = time.Minute

// note records a rollout file: a new session, or the known one again.
func (r *registry) note(f rolloutFile, now time.Time) {
	s := r.sessions[f.id]
	if s == nil || s.path != f.path {
		s = &sess{path: f.path}
		r.sessions[f.id] = s
	}
	s.mod = f.mod
	if !s.metaOK {
		if f.size != s.triedSize || !f.mod.Equal(s.triedMod) {
			s.tries = 0 // the file changed since the last read: a fresh budget
		}
	}
	if !s.metaOK && s.tries < maxMetaTries {
		s.tries++
		s.triedSize, s.triedMod = f.size, f.mod
		if m, ok := readMeta(f.path, now); ok && m.ID == f.id {
			s.meta, s.metaOK = m, true
		}
	}
}

// maxSpawnDepth bounds how far up a chain of spawned threads is followed.
const maxSpawnDepth = 8

// topOf follows a spawned thread's parents up to the first thread that is not
// itself spawned, which is the one that can have a card.
func (r *registry) topOf(id string) string {
	for range maxSpawnDepth {
		p := r.sessions[id]
		if p == nil {
			// Its rollout left the scan: the card it counted on, as observed.
			if o := r.obs[id]; o != nil && o.fact == nil && o.root != "" {
				return o.root
			}
			break
		}
		if !p.metaOK || p.meta.Parent == "" {
			break
		}
		id = p.meta.Parent
	}
	return id
}

// Limits of searching the date folders for rollouts outside the scanned days.
const (
	maxWalkEntries = 100000 // directory entries listed by one search
	maxNotFound    = 1024   // misses remembered
)

var (
	yearDirRe  = regexp.MustCompile(`^[0-9]{4}$`)
	twoDigitRe = regexp.MustCompile(`^[0-9]{2}$`)
)

// locate finds the rollouts of threads whose lock is held but whose file is
// not in the scanned day folders: a terminal session kept open for more than a
// week, or an old thread resumed into its own file. All the ids not yet known
// are searched for in one walk of the date folders (walkRollouts); what is
// found is kept, and a miss is not searched for again for locateRetry.
func (r *registry) locate(ctx context.Context, ids []string, now time.Time) map[string]rolloutFile {
	out := map[string]rolloutFile{}
	missing := map[string]bool{}
	for _, id := range ids {
		if p, ok := r.located[id]; ok {
			if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
				out[id] = rolloutFile{id: id, path: p, mod: fi.ModTime(), size: fi.Size()}
				continue
			}
			delete(r.located, id)
		}
		if t, ok := r.notFound[id]; ok && now.Sub(t) < locateRetry {
			continue
		}
		missing[id] = true
	}
	if len(missing) == 0 {
		return out
	}
	found, complete := r.walkRollouts(ctx, missing)
	for id, p := range found {
		if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
			r.located[id] = p
			delete(r.notFound, id)
			out[id] = rolloutFile{id: id, path: p, mod: fi.ModTime(), size: fi.Size()}
			delete(missing, id)
		}
	}
	if !complete { // cancelled: nothing is known about the rest
		return out
	}
	for id := range missing {
		r.miss(id, now)
	}
	return out
}

// miss remembers that a search for id's rollout found none. At most
// maxNotFound misses are kept: old ones go first, then any.
func (r *registry) miss(id string, now time.Time) {
	if len(r.notFound) >= maxNotFound {
		for k, t := range r.notFound {
			if now.Sub(t) >= locateRetry {
				delete(r.notFound, k)
			}
		}
		for k := range r.notFound {
			if len(r.notFound) < maxNotFound {
				break
			}
			delete(r.notFound, k)
		}
	}
	r.notFound[id] = now
}

// walkRollouts walks sessions/YYYY/MM/DD once and returns the plain rollout
// file of each wanted thread. It lists real folders only: a link, at any
// level, is not followed. It stops after maxWalkEntries entries (the rest of
// the wanted ids count as not found) or when ctx ends (complete is false).
func (r *registry) walkRollouts(ctx context.Context, want map[string]bool) (found map[string]string, complete bool) {
	r.walks++
	found = map[string]string{}
	budget := cmp.Or(r.walkLimit, maxWalkEntries)
	subdirs := func(dir string, re *regexp.Regexp) []string {
		entries, _ := os.ReadDir(dir)
		budget -= len(entries)
		var out []string
		for _, e := range entries {
			if e.IsDir() && re.MatchString(e.Name()) {
				out = append(out, filepath.Join(dir, e.Name()))
			}
		}
		return out
	}
	for _, y := range subdirs(r.a.sessionsDir(), yearDirRe) {
		for _, m := range subdirs(y, twoDigitRe) {
			for _, d := range subdirs(m, twoDigitRe) {
				if ctx.Err() != nil {
					return found, false
				}
				if budget <= 0 || len(found) == len(want) {
					return found, true
				}
				entries, _ := os.ReadDir(d)
				budget -= len(entries)
				for _, e := range entries {
					if m := rolloutNameRe.FindStringSubmatch(e.Name()); m != nil && e.Type().IsRegular() && want[m[1]] && found[m[1]] == "" {
						found[m[1]] = filepath.Join(d, e.Name())
					}
				}
			}
		}
	}
	return found, true
}

// rescan lists the rollouts, asks who holds which lock, and updates every
// session.
func (r *registry) rescan(ctx context.Context) {
	if r.obs == nil {
		r.obs = map[string]*observed{}
	}
	a := r.a
	now := a.now()
	present := map[string]bool{}
	for _, f := range a.rollouts() {
		present[f.id] = true
		r.note(f, now)
	}
	r.names.update(func(id string) bool {
		s := r.sessions[id]
		return s != nil && s.metaOK && s.meta.showable()
	})

	// Only the locks of threads that may become cards are asked about: those
	// whose first line is not read yet and those of the terminal client and
	// codex exec. Not the desktop app's, not spawned children's.
	files, listed := r.lockFiles()
	held, known := map[string]int(nil), false
	if listed {
		held, _, known = r.lockHolders(ctx, files, func(id string) bool {
			s := r.sessions[id]
			return s == nil || !s.metaOK || s.meta.showable()
		})
	}
	if ctx.Err() != nil {
		return
	}
	r.lookupOK = known
	// The rollout of a held lock may lie outside the scanned day folders. One
	// found there stays known (its path is remembered) while its lock file is
	// there, whoever holds it or whether the lookup worked, and while the
	// session is shown: it ends, or expires, by the usual rules and does not
	// vanish with its folder's date.
	lostSet := map[string]bool{}
	for id := range held {
		lostSet[id] = true
	}
	for id := range r.located {
		_, locked := files[id]
		if s := r.sessions[id]; locked || (s != nil && s.seenLive) {
			lostSet[id] = true
		}
	}
	var lost []string
	for _, id := range slices.Sorted(maps.Keys(lostSet)) {
		if !present[id] {
			lost = append(lost, id)
		}
	}
	for id, f := range r.locate(ctx, lost, now) {
		present[id] = true
		r.note(f, now)
	}
	// Spawned threads are counted on the nearest thread that is not itself
	// one spawned (a spawned thread can spawn too).
	clear(r.children)
	r.kids = map[string][]string{}
	for _, id := range slices.Sorted(maps.Keys(r.sessions)) {
		if s := r.sessions[id]; s.metaOK && s.meta.Parent != "" {
			top := r.topOf(s.meta.Parent)
			r.children[top]++
			r.kids[top] = append(r.kids[top], id)
		}
	}

	// A held lock proves nothing about a terminal thread (the daemon holds it
	// after the terminal quit): it is live when a terminal client is open in
	// its folder (see tuis.go).
	var heldTerm []heldThread
	for _, id := range slices.Sorted(maps.Keys(held)) {
		if s := r.sessions[id]; s != nil && s.metaOK && s.meta.terminal() && present[id] {
			folder := s.meta.Cwd
			if s.t != nil && s.t.d.Folder != "" {
				folder = s.t.d.Folder
			}
			heldTerm = append(heldTerm, heldThread{id: id, folder: folder, mod: s.mod})
		}
	}
	live, tuisKnown := map[string]string{}, true
	if len(heldTerm) > 0 {
		var procs []tuiProc
		if procs, tuisKnown = a.tuis(ctx); tuisKnown {
			live = terminalLive(heldTerm, procs)
		}
	}
	if ctx.Err() != nil {
		return
	}

	r.handOff()
	var shown []string
	for _, id := range slices.Sorted(maps.Keys(r.sessions)) {
		s := r.sessions[id]
		s.card = false
		if !present[id] {
			r.drop(id)
			continue
		}
		if s.metaOK && !s.meta.showable() {
			// Never a card (the desktop app's thread, a spawned child): not
			// followed. It stays known so that its first line is not read
			// again and its lock is not asked about. A spawned child is read
			// for its spend while the card at the top of its chain is shown
			// (readSpawned).
			if s.meta.Parent == "" {
				r.forget(id, s)
			}
			continue
		}
		_, isHeld := held[id]
		unknown := !known
		link := ""
		if isHeld && s.metaOK && s.meta.terminal() {
			if !tuisKnown {
				isHeld, unknown = false, true
			} else if l, ok := live[id]; ok {
				link = l
			} else {
				isHeld = false // locked by the daemon, no terminal has it open
			}
		}
		switch {
		case isHeld:
			s.live, s.seenLive, s.ended, s.endedAt = model.Of(true), true, false, time.Time{}
			s.termID = link
			// A thread caught up at start (catchup.go) turned live: its caught
			// spend, with its spawned threads', is carried, so that the live
			// read never counts less (per quarter hour the maximum, withCarry),
			// and its card replaces the catch-up fact.
			if o := r.obs[id]; o != nil && o.fact != nil && o.fact.Backfill && s.carryParts == nil {
				s.carryParts = o.parts
			}
		case unknown:
			// Liveness cannot be told. A session already resolved (seen live,
			// or ended) keeps what it was: a failed lookup says nothing about
			// it, and an ended Codex rollout stays on disk, unlike a Claude
			// session file, so it must not come back as live-unknown. One
			// never resolved is shown, when its rollout changed recently, as
			// recently active with liveness unknown.
			s.termID = "" // a link nobody can verify now (lock or client lookup failed) is not kept
			if s.seenLive {
				break
			}
			s.live = model.Unknown[bool]()
			s.termID = ""
			if now.Sub(s.mod) > recentWindow {
				r.forget(id, s)
				continue
			}
		default:
			if s.seenLive && !s.ended {
				s.ended, s.endedAt = true, s.mod
			}
			s.live = model.Of(false)
			s.termID = ""
		}
		if s.ended && now.Sub(s.endedAt) > turnWindow {
			r.drop(id)
			continue
		}
		if !r.cardOf(id, s) {
			r.forget(id, s)
			continue
		}
		s.card = true
		r.read(ctx, id, s)
		shown = append(shown, id)
	}
	r.readSpawned(ctx)
	for _, id := range shown {
		r.send(id, r.sessions[id])
	}
	r.tendObserved(now)
}

// lockFiles lists the lock files, thread id to path. ok is false when the
// folder cannot be listed: liveness cannot be told. A Codex older than the
// writer lock has no folder; the recent-rollout fallback applies until it
// appears.
func (r *registry) lockFiles() (files map[string]lockFile, ok bool) {
	dir := r.a.locksDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	if r.locks == "" {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			r.locks = real
		}
	}
	base := cmp.Or(r.locks, dir)
	files = map[string]lockFile{}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".lock")
		if !ok || !threadIDRe.MatchString(id) || !e.Type().IsRegular() {
			continue
		}
		lf := lockFile{path: filepath.Join(base, e.Name())}
		if fi, err := e.Info(); err == nil {
			lf.mod = fi.ModTime().UnixNano()
		}
		files[id] = lf
	}
	return files, true
}

// lockFile is a lock file as listed: where it is, and when it was created
// (Codex removes the file when a thread stops, so a lock taken again is a new
// file).
type lockFile struct {
	path string
	mod  int64
}

// holderRevalidate: how long a lock-holder answer is trusted at most, even
// while the lock files and the holding processes look unchanged.
const holderRevalidate = time.Minute

// holderCache remembers the last answer of the lock-holder lookup.
type holderCache struct {
	key    string    // the asked-about locks and their times
	at     time.Time // when the lookup began
	held   map[string]int
	starts map[int]string // when each holding process started
}

// lockHolders maps thread ids to the pid that holds their lock, and gives
// each holder's start time, asking only about the ids want accepts. known is
// false when it could not be told.
//
// Naming files makes lsof walk the open files of every process (about 0.27 s
// on a Mac with 1,200 processes, whatever the number of files, against 0.04 s
// for one pid). So it runs only when it can
// have changed: the answer is kept while the same lock files are there, every
// one of them is held, and each holder is the same process (the start time
// that ps gives, checked every scan), for at most holderRevalidate. A lock
// file nobody holds, or a new one, asks again.
func (r *registry) lockHolders(ctx context.Context, files map[string]lockFile, want func(id string) bool) (held map[string]int, starts map[int]string, known bool) {
	var paths, keys []string
	byPath := map[string]string{}
	for _, id := range slices.Sorted(maps.Keys(files)) {
		if want(id) {
			lf := files[id]
			paths = append(paths, lf.path)
			keys = append(keys, id+"|"+strconv.FormatInt(lf.mod, 10))
			byPath[lf.path] = id
		}
	}
	key := strings.Join(keys, ",")
	began := r.a.now()
	if c := r.cache; c != nil && c.key == key && began.Sub(c.at) < holderRevalidate {
		if st, ok := r.a.starts(ctx, pidsOf(c.held)); ok && stillRunning(c.starts, st) {
			return maps.Clone(c.held), maps.Clone(c.starts), true
		}
	}
	r.cache = nil
	holders, ok := r.a.holders(ctx, paths)
	if !ok {
		return nil, nil, false
	}
	held = map[string]int{}
	for p, pid := range holders {
		if id, ok := byPath[p]; ok && pid > 0 {
			held[id] = pid
		}
	}
	verified := false
	if len(held) > 0 {
		if st, ok := r.a.starts(ctx, pidsOf(held)); ok {
			// A holder ps does not list has died since lsof saw it, and one
			// that started after the lookup began is a new process on a pid
			// the holder left: neither holds the lock.
			verified, starts = true, map[int]string{}
			for id, pid := range held {
				if s := st[pid]; s == "" || startedAfter(s, began) {
					delete(held, id)
				} else {
					starts[pid] = s
				}
			}
		}
	}
	if verified && len(held) == len(paths) {
		r.cache = &holderCache{key: key, at: began, held: maps.Clone(held), starts: maps.Clone(starts)}
	}
	return held, starts, true
}

// stillRunning: every process of the cached answer is listed by ps with the
// start time it had. A pid ps does not list (the holder died) or lists with
// another start (a new process) is not it.
func stillRunning(want, got map[int]string) bool {
	for pid, s := range want {
		if s == "" || !proc.SameStart(s, got[pid]) {
			return false
		}
	}
	return len(want) > 0
}

// startedAfter: a start time as ps prints it (the adapter runs ps with
// TZ=UTC), later than t. One that does not parse is not.
func startedAfter(start string, t time.Time) bool {
	at, err := time.Parse("Mon Jan 2 15:04:05 2006", strings.Join(strings.Fields(start), " "))
	return err == nil && at.After(t)
}

// pidsOf lists the distinct pids of a holder map, sorted.
func pidsOf(held map[string]int) []int {
	pids := []int{}
	for _, pid := range held {
		pids = append(pids, pid)
	}
	slices.Sort(pids)
	return slices.Compact(pids)
}

// tailAll reads what the shown sessions' rollouts grew by.
func (r *registry) tailAll(ctx context.Context) {
	ids := make([]string, 0, len(r.sessions))
	for id, s := range r.sessions {
		// Only what the last scan made a card: a thread it left out (an ended one
		// of unknown liveness, caught up or not yet) is "shown" by its unknown
		// liveness alone, and must not become a card between two scans.
		if s.card && r.cardOf(id, s) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	if r.obs == nil {
		r.obs = map[string]*observed{}
	}
	for _, id := range ids {
		r.read(ctx, id, r.sessions[id])
	}
	r.readSpawned(ctx)
	for _, id := range ids {
		r.send(id, r.sessions[id])
	}
	r.boundObserved()
}

// read reads what a session's rollout grew by.
func (r *registry) read(ctx context.Context, id string, s *sess) {
	if s.t == nil {
		s.t = newTailer(s.path)
		s.t.id = id
		s.t.spendOnly = s.meta.Parent != "" // a spawned thread: its token records only
	}
	// An unreadable rollout keeps what was read; the tailer reads it again
	// from where it stopped next time.
	_, _ = s.t.update(ctx, r.a.now())
}

// readSpawned reads the threads Codex spawned under a shown session, for
// their spend; the others are not followed.
func (r *registry) readSpawned(ctx context.Context) {
	counted := map[string]bool{}
	for top, ids := range r.kids {
		if t := r.sessions[top]; t != nil && t.card && r.cardOf(top, t) {
			for _, id := range ids {
				counted[id] = true
			}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(r.sessions)) {
		s := r.sessions[id]
		if !s.metaOK || s.meta.Parent == "" {
			continue
		}
		if counted[id] {
			r.read(ctx, id, s)
			// Only a read that found a valid record replaces what was saved:
			// a failed or empty first read of a returning child never dips
			// its parent's count.
			if s.t.sawRecord {
				r.observeSpawned(id, r.topOf(s.meta.Parent), &s.t.d)
			}
		} else {
			s.t = nil
		}
	}
}

// forget stops reading a session that is not shown, withdrawing it when it
// was sent.
func (r *registry) forget(id string, s *sess) {
	s.t = nil
	if _, sent := r.sent[id]; sent {
		delete(r.sent, id)
		r.emit(model.SessionGone{Key: model.SessionKey{Tool: model.ToolCodex, ID: id}})
	}
}

// drop removes a session whose rollout went or fell out of the window. One
// seen live with use in the days is kept in the store until that use ages out.
func (r *registry) drop(id string) {
	if s := r.sessions[id]; s != nil {
		if f, sent := r.sent[id]; sent && s.seenLive && r.obs != nil && r.keep(id, f) {
			delete(r.sent, id)
			r.emit(*r.obs[id].fact)
			s.t = nil
		} else {
			r.forget(id, s)
		}
	}
	delete(r.sessions, id)
	delete(r.located, id)
}

func (r *registry) send(id string, s *sess) {
	f := r.fact(id, s)
	if old, ok := r.sent[id]; ok && reflect.DeepEqual(old, f) {
		return
	}
	r.sent[id] = f
	r.emit(f)
}

func (r *registry) fact(id string, s *sess) model.SessionFact {
	var d derived
	if s.t != nil {
		d = s.t.d
	}
	f := model.SessionFact{
		Key:          model.SessionKey{Tool: model.ToolCodex, ID: id},
		Name:         r.names.byID[id],
		ToolVersion:  s.meta.Version,
		Status:       d.Status,
		StatusSince:  d.StatusSince,
		StartedAt:    s.meta.StartedAt,
		LastActivity: d.LastActivity,
		Live:         s.live,
		Ended:        s.ended,
		EndedAt:      s.endedAt,
		Folder:       cmp.Or(d.Folder, s.meta.Cwd),
		TerminalID:   s.termID,
		Subagents:    r.children[id],
		CreatedPRs:   slices.Clone(d.CreatedPRs),
		PushedPRs:    slices.Clone(d.PushedPRs),
		Headless:     s.meta.Source == "exec",
	}
	// Its spend, with that of the threads Codex spawned under it. A session
	// not (yet) seen live, shown because liveness cannot be told, may have
	// ended before sitboard started: its tokens show, but nothing of it goes
	// into the days (the catch-up counts such a session once, when it is
	// known to have ended: catchup.go).
	f.Spend = withCarry(r.threadSpend(id, &d, s.carryParts), s.carry, r.a.now())
	if !s.seenLive {
		f.Spend.Spans = nil
	}
	if d.Model != "" {
		f.Model = model.Of(d.Model)
	}
	if d.Effort != "" {
		f.Effort = model.Of(d.Effort)
	}
	if s.ended {
		f.Status = model.Unknown[model.Status]()
	}
	if len(d.TurnEnds) > 0 {
		f.TurnEnds = slices.Clone(d.TurnEnds)
	}
	f.SpawnedNames = slices.Clone(d.SpawnedNames)
	f.SpawnedTerminalIDs = slices.Clone(d.SpawnedIDs)
	return f
}

// agtermVar is the only environment variable read from a Codex process.
const agtermVar = "AGTERM_SESSION_ID"

var terminalIDRe = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

func pickAgtermID(env [][]byte) string {
	prefix := []byte(agtermVar + "=")
	for _, e := range env {
		if v, ok := bytes.CutPrefix(e, prefix); ok {
			if terminalIDRe.Match(v) {
				return strings.ToUpper(string(v))
			}
			return ""
		}
	}
	return ""
}

// sameCodexHome says whether a terminal client's CODEX_HOME is the watched
// one: a set value must name it, an unset one means the default home, which is
// the watched one only when sitboard watches the default.
func sameCodexHome(watched, def string) func(home string, set bool) bool {
	return func(home string, set bool) bool {
		if !set || home == "" {
			home = def
		}
		return folderKey(home) == folderKey(watched)
	}
}
