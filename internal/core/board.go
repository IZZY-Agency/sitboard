// Package core derives projects, cards and columns from the adapters' facts
// and implements app.Board.
//
// Facts arrive through Apply; the clock (Run) re-evaluates time-based rules
// and asks GitHub for pull requests. After every change the board is derived
// again from scratch, and a snapshot is published only when the derived
// content differs from the last one.
package core

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/IZZY-Agency/sitboard/internal/buildinfo"
	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

// Options configure the core.
type Options struct {
	Repos      source.RepoResolver
	PRs        source.PRProvider
	State      state.Store
	Now        func() time.Time // the only clock the core reads; nil means time.Now
	StaleAfter time.Duration    // a busy card with no activity for this long is stale
	Roots      []string         // when set, only folders under these count
}

// publishEvery caps how often subscribers get a snapshot: at most four a
// second. A burst of changes reaches them as its latest state.
const publishEvery = 250 * time.Millisecond

// Board is the core. It implements app.Board. All methods are safe for
// concurrent use.
type Board struct {
	opts  Options
	home  string
	roots []string

	mu         sync.Mutex
	sessions   map[model.SessionKey]model.SessionFact
	terminals  map[string]model.TerminalFact
	repos      map[string]model.Repo // by folder; a folder absent here is still being resolved
	repoGen    map[string]uint64     // by folder: the latest git lookup started; older answers are dropped
	plain      map[string]bool       // by folder: git answered that it is in no repository (see isPlain)
	umbrellas  map[string]time.Time  // umbrella folder -> last time an agent sat in it (live or remembered)
	umbrellaTo map[string]time.Time  // umbrella times to write to the state file on the next tick
	liveSince  map[string]time.Time  // live umbrella -> when it became live, until it is remembered
	missing    map[string]int        // remembered umbrella -> checks in a row that found it gone
	goneAt     time.Time             // when the gone check last started
	goneBusy   bool                  // a gone check's worker is running
	closed     bool                  // Run returned: no more state writes from checks
	prs        map[model.PRRef]model.PullRequest
	branchPRs  map[string]map[string][]model.PullRequest // repo -> head branch -> pull requests
	lookedUp   map[string]time.Time                      // repo -> last GitHub lookup (wall clock)
	pending    map[string]bool                           // repos with pull request refs never looked up
	asked      map[string]bool                           // refs and branches GitHub was asked about
	resolved   time.Time                                 // last time live folders were resolved again
	memSeen    map[string]time.Time                      // used when no state store is given
	selected   map[string]time.Time                      // terminal id -> last selection in agterm
	seenWrites map[string]time.Time                      // seen marks to write once b.mu is released
	writeMu    sync.Mutex                                // orders state file writes; taken after b.mu, never before
	lastTick   time.Time
	resumedAt  time.Time
	sources    []contract.SourceStatus
	ready      map[contract.SourceKind]bool // watchers that sent model.SourceReady

	seq       uint64
	view      view
	cardKeys  map[string]model.SessionKey // card id -> session, for cards on the board
	subs      map[chan contract.Snapshot]struct{}
	lastPub   time.Time // real time of the last publish
	pubTimer  *time.Timer
	refreshCh chan struct{}
	resolving sync.WaitGroup // git lookups in flight; tests wait on it
}

// view is the derived board. It is replaced, never changed in place.
type view struct {
	cards       []contract.Card
	projects    []contract.Project
	counts      contract.Counts
	plainShells int
	sources     []contract.SourceStatus
	agterm      bool
	umbrellas   []string // umbrella folders with an agent in them now, sorted
}

// New returns an empty board.
func New(opts Options) *Board {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	home, _ := os.UserHomeDir()
	b := &Board{
		opts:       opts,
		home:       filepath.Clean(home),
		sessions:   map[model.SessionKey]model.SessionFact{},
		terminals:  map[string]model.TerminalFact{},
		repos:      map[string]model.Repo{},
		repoGen:    map[string]uint64{},
		plain:      map[string]bool{},
		umbrellas:  map[string]time.Time{},
		umbrellaTo: map[string]time.Time{},
		liveSince:  map[string]time.Time{},
		missing:    map[string]int{},
		prs:        map[model.PRRef]model.PullRequest{},
		branchPRs:  map[string]map[string][]model.PullRequest{},
		lookedUp:   map[string]time.Time{},
		pending:    map[string]bool{},
		asked:      map[string]bool{},
		memSeen:    map[string]time.Time{},
		selected:   map[string]time.Time{},
		seenWrites: map[string]time.Time{},
		sources:    []contract.SourceStatus{},
		ready:      map[contract.SourceKind]bool{},
		cardKeys:   map[string]model.SessionKey{},
		subs:       map[chan contract.Snapshot]struct{}{},
		refreshCh:  make(chan struct{}, 1),
	}
	for _, r := range opts.Roots {
		if r = expandHome(r, b.home); r != "" {
			b.roots = append(b.roots, r)
		}
	}
	if opts.State != nil {
		b.umbrellas = opts.State.Umbrellas()
	}
	b.view = view{cards: []contract.Card{}, projects: []contract.Project{}, sources: []contract.SourceStatus{}}
	return b
}

// Apply takes one fact from a watcher. Safe to call from any goroutine; facts
// take effect in the order Apply was called. It never waits for git: a new
// folder is resolved in the background, and its sessions get their card when
// git has answered.
func (b *Board) Apply(f model.Fact) {
	defer b.flushSeen() // runs after the unlock below
	b.mu.Lock()
	defer b.mu.Unlock()
	switch f := f.(type) {
	case model.SessionFact:
		if f.Folder != "" {
			f.Folder = filepath.Clean(f.Folder) // "a/" and "a/." are "a"
		}
		f.CreatedPRs = slices.Clone(f.CreatedPRs)
		f.TurnEnds = slices.Clone(f.TurnEnds)
		b.sessions[f.Key] = f
		if _, started := b.repoGen[f.Folder]; f.Folder != "" && !started {
			b.startResolveLocked(f.Folder)
		}
		if b.markPendingLocked(f) {
			b.kickRefresh()
		}
	case model.SessionGone:
		delete(b.sessions, f.Key)
	case model.TerminalFact:
		b.terminals[f.ID] = f
	case model.TerminalGone:
		delete(b.terminals, f.ID)
		delete(b.selected, f.ID)
	case model.TerminalSelected:
		b.selectedLocked(f)
	case model.SourceReady:
		kind := contract.SourceKind(f.Kind)
		b.ready[kind] = true
		for i := range b.sources {
			if b.sources[i].Kind == kind && !b.sources[i].Ready {
				b.sources = slices.Clone(b.sources) // the published view shares the old slice
				b.sources[i].Ready = true
				break
			}
		}
	}
	b.rebuildLocked()
}

// startResolveLocked asks git about folder with no lock held. Only the answer
// to the latest lookup of a folder is kept. Callers hold b.mu.
func (b *Board) startResolveLocked(folder string) {
	b.repoGen[folder]++
	gen := b.repoGen[folder]
	b.resolving.Add(1)
	go func() {
		defer b.resolving.Done()
		repo, plain := b.resolve(folder)
		defer b.flushSeen()
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.repoGen[folder] != gen {
			return // a newer lookup of this folder started meanwhile
		}
		b.repos[folder] = repo
		b.plain[folder] = plain
		kick := false
		for _, s := range b.sessions {
			if s.Folder == folder && b.markPendingLocked(s) {
				kick = true
			}
		}
		b.rebuildLocked()
		if kick {
			b.kickRefresh()
		}
	}()
}

// resolve asks git about a folder. Without git, or when git cannot tell, the
// folder is its own project. plain reports that git answered the folder is in
// no repository: it is its own root with no branch and no remote, and it has
// no .git entry (a repository root with a detached HEAD and no remote would
// otherwise look the same). Without an answer from git it is false.
func (b *Board) resolve(folder string) (repo model.Repo, plain bool) {
	fallback := model.Repo{Root: folder, MainRoot: folder}
	if b.opts.Repos == nil {
		return fallback, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo, err := b.opts.Repos.Resolve(ctx, folder)
	if err != nil {
		return fallback, false
	}
	if repo.Root == "" {
		repo.Root = folder
	}
	if repo.MainRoot == "" {
		repo.MainRoot = repo.Root
	}
	plain = repo.Root == folder && repo.MainRoot == folder && !repo.Branch.OK && repo.Slug == ""
	if plain {
		if _, err := os.Lstat(filepath.Join(folder, ".git")); err == nil {
			plain = false
		}
	}
	return repo, plain
}

// now is the wall clock. The monotonic reading is stripped: on macOS it
// stops while the machine sleeps, which would hide a sleep from the tick.
func (b *Board) now() time.Time { return b.opts.Now().Round(0) }

// SetSources records what each adapter's Probe said, and publishes a new
// snapshot when that changed.
func (b *Board) SetSources(statuses []source.Status) {
	next := make([]contract.SourceStatus, 0, len(statuses))
	for _, s := range statuses {
		next = append(next, contract.SourceStatus{
			Kind:    contract.SourceKind(s.Kind),
			State:   contract.SourceState(s.State),
			Version: clean(s.Version),
			Detail:  clean(s.Detail),
			Tested:  s.Tested,
		})
	}
	slices.SortStableFunc(next, func(a, c contract.SourceStatus) int {
		return slices.Index(contract.SourceKinds, a.Kind) - slices.Index(contract.SourceKinds, c.Kind)
	})
	defer b.flushSeen()
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range next {
		next[i].Ready = b.isReadyLocked(next[i].Kind, next[i].State)
	}
	b.sources = next
	b.rebuildLocked()
}

// isReadyLocked applies the readiness rule of contract.SourceStatus: a source
// that is not ok, and git and GitHub (which answer on demand), are ready; a
// present watcher is ready once it sent model.SourceReady. Callers hold b.mu.
func (b *Board) isReadyLocked(kind contract.SourceKind, st contract.SourceState) bool {
	if st != contract.SourceOK || kind == contract.SourceGit || kind == contract.SourceGitHub {
		return true
	}
	return b.ready[kind]
}

// DropSource forgets every fact a watcher of kind gave, and shows the source
// as failing with detail. The caller uses it when that watcher's Run ended
// with an error, so its cards do not stay on the board forever.
func (b *Board) DropSource(kind source.Kind, detail string) {
	defer b.flushSeen()
	b.mu.Lock()
	defer b.mu.Unlock()
	switch kind {
	case source.KindClaude, source.KindCodex:
		for k := range b.sessions {
			if string(k.Tool) == string(kind) {
				delete(b.sessions, k)
			}
		}
	case source.KindAgterm:
		clear(b.terminals)
		clear(b.selected)
	}
	next := slices.Clone(b.sources)
	found := false
	for i := range next {
		if next[i].Kind == contract.SourceKind(kind) {
			next[i].State, next[i].Detail, next[i].Ready, found = contract.SourceFailing, clean(detail), true, true
		}
	}
	if !found {
		next = append(next, contract.SourceStatus{Kind: contract.SourceKind(kind), State: contract.SourceFailing, Detail: clean(detail), Ready: true})
		slices.SortStableFunc(next, func(a, c contract.SourceStatus) int {
			return slices.Index(contract.SourceKinds, a.Kind) - slices.Index(contract.SourceKinds, c.Kind)
		})
	}
	b.sources = next
	b.rebuildLocked()
}

// Snapshot returns the current board.
func (b *Board) Snapshot() contract.Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshotLocked()
}

func (b *Board) snapshotLocked() contract.Snapshot {
	v := b.view
	return contract.Snapshot{
		Version:       contract.Version,
		Seq:           b.seq,
		GeneratedAt:   b.now().UTC(),
		Sources:       v.sources,
		Projects:      v.projects,
		Cards:         v.cards,
		PlainShells:   v.plainShells,
		Counts:        v.counts,
		AgtermPresent: v.agterm,
		Build:         currentBuild(),
	}
}

// maxBuildText caps the version and commit. They are build constants, but a
// custom build can set anything through linker flags.
const maxBuildText = 64

// currentBuild describes this build for the page, cleaned like any other
// string.
func currentBuild() contract.Build {
	capped := func(s string) string {
		s = clean(s)
		if utf8.RuneCountInString(s) > maxBuildText {
			s = string([]rune(s)[:maxBuildText]) + "…"
		}
		return s
	}
	return contract.Build{
		Version: capped(buildinfo.Version),
		Commit:  capped(buildinfo.Commit),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
}

// rebuildLocked derives the board again and publishes it when its content
// changed. Callers hold b.mu.
func (b *Board) rebuildLocked() {
	now := b.now()
	next, keys := b.deriveLocked(now)
	if b.settledLocked() {
		b.noteUmbrellasLocked(next.umbrellas, now)
	}
	if reflect.DeepEqual(next, b.view) {
		return
	}
	b.view = next
	b.cardKeys = keys
	b.seq++
	b.schedulePublishLocked()
}

// Subscribe delivers the latest snapshot after each change until ctx ends.
func (b *Board) Subscribe(ctx context.Context) <-chan contract.Snapshot {
	ch := make(chan contract.Snapshot, 1)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subs, ch)
		close(ch)
		b.mu.Unlock()
	}()
	return ch
}

// schedulePublishLocked publishes now, or at the end of the current 250 ms
// window when a publish just happened. Callers hold b.mu.
func (b *Board) schedulePublishLocked() {
	if b.pubTimer != nil {
		return // a publish is already scheduled; it will carry this change
	}
	wait := publishEvery - time.Since(b.lastPub)
	if wait <= 0 {
		b.publishLocked()
		return
	}
	b.pubTimer = time.AfterFunc(wait, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.pubTimer = nil
		b.publishLocked()
	})
}

// publishLocked replaces whatever a subscriber has not read yet with the
// newest snapshot, so nobody gets a backlog. Callers hold b.mu.
func (b *Board) publishLocked() {
	b.lastPub = time.Now()
	snap := b.snapshotLocked()
	for ch := range b.subs {
		select {
		case <-ch:
		default:
		}
		ch <- snap
	}
}

// Catchup is out of the proof of concept: it reports the marker and no items.
func (b *Board) Catchup(context.Context) (contract.Catchup, error) {
	c := contract.Catchup{Items: []contract.CatchupItem{}}
	if b.opts.State != nil {
		c.Marker = b.opts.State.CatchupMarker()
	}
	return c, nil
}

// Leftovers is out of the proof of concept: it reports no items.
func (b *Board) Leftovers(context.Context) (contract.Leftovers, error) {
	return contract.Leftovers{Items: []contract.Leftover{}}, nil
}

// ErrUnknownCard is returned for a card id that is not on the board.
var ErrUnknownCard = errors.New("core: unknown card")

// MarkSeen marks a card seen now.
func (b *Board) MarkSeen(cardID string) error {
	defer b.flushSeen()
	b.mu.Lock()
	defer b.mu.Unlock()
	key, ok := b.cardKeys[cardID]
	if !ok {
		return ErrUnknownCard
	}
	at := b.now()
	// A turn end recorded in the future must still count as seen.
	if end := b.lastTurnEndLocked(b.sessions[key]); end.After(at) {
		at = end
	}
	if err := b.setSeenLocked(cardID, at); err != nil {
		return err
	}
	b.rebuildLocked()
	return nil
}

func (b *Board) MarkCatchupRead() error {
	if b.opts.State == nil {
		return errors.New("core: no state store")
	}
	return b.opts.State.SetCatchupMarker(b.now())
}

// TerminalFor resolves a card on the board to its terminal in the current
// agterm tree.
func (b *Board) TerminalFor(cardID string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key, ok := b.cardKeys[cardID]
	if !ok {
		return "", false
	}
	id := b.sessions[key].TerminalID
	if id == "" {
		return "", false
	}
	if _, ok := b.terminals[id]; !ok {
		return "", false
	}
	return id, true
}

// expandHome turns "~/x" into an absolute, clean path.
func expandHome(p, home string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if p == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		p = filepath.Join(home, rest)
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return filepath.Clean(p)
}

func logf(format string, args ...any) { log.Printf("core: "+format, args...) }
