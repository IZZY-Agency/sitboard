// Package claude is the Claude Code source adapter.
//
// It reads two undocumented Claude Code records (tested on 2.1.322 to 2.1.326):
//
//   - <Home>/.claude/sessions/<pid>.json, one small file per live session,
//     watched with fsnotify. These give the board its first paint.
//   - <Home>/.claude/projects/<folder>/<sessionId>.jsonl, the transcript, and
//     <sessionId>/subagents/*.jsonl beside it. These are tailed in the
//     background, from a remembered offset, for derived facts only.
//
// All facts are emitted from one goroutine, so a fact and a gone for one
// session never swap. Nothing from a transcript but derived fields leaves this
// package, and only one environment variable is read from a live process.
package claude

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// Options configure the adapter.
type Options struct {
	Home string           // the user's home folder; records are read under <Home>/.claude
	Now  func() time.Time // clock; nil means time.Now
}

type adapter struct {
	opts     Options
	root     string // <Home>/.claude
	now      func() time.Time
	starts   startsFunc
	termID   func(pid int) string
	tick     time.Duration // transcript growth (stat only) and re-adding the watch
	liveTick time.Duration // full rescan with ps: liveness
	debounce time.Duration
	// beforeIndex, when set, runs in the indexer before each batch (tests).
	beforeIndex func()
}

// New returns the Claude Code watcher.
func New(opts Options) source.Watcher {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &adapter{
		opts: opts, root: filepath.Join(opts.Home, ".claude"), now: now,
		starts: defaultStarts(), termID: readAgtermID,
		tick: 3 * time.Second, liveTick: 10 * time.Second, debounce: 100 * time.Millisecond,
	}
}

func (a *adapter) Kind() source.Kind { return source.KindClaude }

func (a *adapter) sessionsDir() string { return filepath.Join(a.root, "sessions") }
func (a *adapter) projectsDir() string { return filepath.Join(a.root, "projects") }

var recordName = regexp.MustCompile(`^[0-9]{1,10}\.json$`)

// Probe reports Claude Code as present when its folder exists. The version is
// the one recorded by the newest live-session file.
func (a *adapter) Probe(context.Context) source.Status {
	st := source.Status{Kind: source.KindClaude}
	if fi, err := os.Stat(a.root); err != nil || !fi.IsDir() {
		st.State, st.Detail = source.StateMissing, "Claude Code not found"
		return st
	}
	st.State = source.StateOK
	entries, err := os.ReadDir(a.sessionsDir())
	if err != nil {
		st.Detail = "no live sessions"
		return st
	}
	var newest time.Time
	for _, e := range entries {
		if !recordName.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().After(newest) {
			continue
		}
		b, err := readSmall(filepath.Join(a.sessionsDir(), e.Name()))
		if err != nil {
			continue
		}
		if r, ok := parseRecord(b); ok && r.Version != "" {
			newest, st.Version = info.ModTime(), r.Version
		}
	}
	st.Tested = slices.Contains(testedVersions, st.Version)
	return st
}

// readSmall reads a session file, refusing anything implausibly large.
func readSmall(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	const limit = 64 << 10
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if len(b) > limit {
		return nil, os.ErrInvalid
	}
	return b, nil
}

// sess is what the adapter knows about one session.
type sess struct {
	rec        record
	recFile    string
	live       model.Known[bool]
	ended      bool
	endedAt    time.Time
	termID     string
	termKey    string    // pid and process start the terminal id was read for
	transcript string    // "" until found
	globAt     time.Time // last search of every project folder for it
	d          derived
	subagents  int
}

// Run emits the live sessions at once, then derived facts as the transcripts
// are read, then every change, until ctx ends.
func (a *adapter) Run(ctx context.Context, emit func(model.Fact)) error {
	ready := model.SourceReady{Kind: string(source.KindClaude)}
	if fi, err := os.Stat(a.root); err != nil || !fi.IsDir() {
		emit(ready)
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ix := newIndexer(a)
	var wg sync.WaitGroup
	wg.Go(func() { ix.run(ctx) })
	defer func() { cancel(); wg.Wait() }() // the tailer stops at its next check of ctx

	w, _ := fsnotify.NewWatcher()
	watching := false
	if w != nil {
		defer func() { _ = w.Close() }()
	}
	watch := func() {
		if w != nil && !watching && w.Add(a.sessionsDir()) == nil {
			watching = true
		}
	}
	var events <-chan fsnotify.Event
	var werrs <-chan error
	if w != nil {
		events, werrs = w.Events, w.Errors
	}

	r := &registry{a: a, emit: emit, sessions: map[string]*sess{}, sent: map[string]model.SessionFact{}}
	watch()
	r.rescan(ctx)
	if ctx.Err() != nil {
		return nil
	}
	emit(ready) // every live session is out; the transcripts follow in the background
	ix.want(r.jobs())

	// The fast tick only stats transcripts; ps runs on the slow one, or when
	// a session file changes.
	ticker := time.NewTicker(a.tick)
	defer ticker.Stop()
	liveTicker := time.NewTicker(a.liveTick)
	defer liveTicker.Stop()
	var debounceC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if ev.Name == a.sessionsDir() && ev.Has(fsnotify.Remove|fsnotify.Rename) {
				watching = false // the folder went away; watch it again once it is back
			}
			if debounceC == nil {
				debounceC = time.After(a.debounce)
			}
		case _, ok := <-werrs: // a watch error: the ticker still rescans
			if !ok {
				werrs = nil
			}
		case <-debounceC:
			debounceC = nil
			r.rescan(ctx)
			ix.want(r.jobs())
		case <-ticker.C:
			if !watching {
				watch()
				if watching { // back after a gap: catch up on what was missed
					r.rescan(ctx)
				}
			}
			ix.want(r.jobs())
		case <-liveTicker.C:
			r.rescan(ctx)
			ix.want(r.jobs())
		case res := <-ix.results:
			r.applyIndex(res)
		}
	}
}

// registry is the Run goroutine's state. Only that goroutine touches it.
type registry struct {
	a        *adapter
	emit     func(model.Fact)
	sessions map[string]*sess
	sent     map[string]model.SessionFact
}

func (r *registry) rescan(ctx context.Context) {
	a := r.a
	recs := map[string]record{}
	files := map[string]string{}
	entries, _ := os.ReadDir(a.sessionsDir())
	for _, e := range entries {
		if !recordName.MatchString(e.Name()) {
			continue
		}
		path := filepath.Join(a.sessionsDir(), e.Name())
		b, err := readSmall(path)
		if err != nil {
			continue
		}
		rec, ok := parseRecord(b)
		if !ok || strconv.Itoa(rec.PID)+".json" != e.Name() {
			continue
		}
		// Two files for one session (a resume): the newer one wins.
		if old, dup := recs[rec.SessionID]; dup && old.UpdatedAt >= rec.UpdatedAt {
			continue
		}
		recs[rec.SessionID], files[rec.SessionID] = rec, path
	}

	pids := make([]int, 0, len(recs))
	for _, rec := range recs {
		pids = append(pids, rec.PID)
	}
	slices.Sort(pids)
	starts, known := a.starts(ctx, pids)
	if ctx.Err() != nil {
		return
	}

	now := a.now()
	for id, rec := range recs {
		s := r.sessions[id]
		if s == nil {
			s = &sess{}
			r.sessions[id] = s
		}
		s.rec, s.recFile = rec, files[id]
		switch start, running := starts[rec.PID]; {
		case !known:
			s.live, s.ended = model.Unknown[bool](), false
		case running && (rec.ProcStart == "" || sameStart(start, rec.ProcStart)):
			s.live, s.ended, s.endedAt = model.Of(true), false, time.Time{}
		default: // the process is gone, or its pid now belongs to another program
			if !s.ended {
				s.ended, s.endedAt = true, lastChange(s.recFile, s.transcript)
			}
			s.live = model.Of(false)
		}
		if s.live.V && s.live.OK {
			if key := strconv.Itoa(rec.PID) + "|" + rec.ProcStart; key != s.termKey {
				s.termKey, s.termID = key, a.termID(rec.PID)
			}
		} else {
			s.termKey, s.termID = "", ""
		}
		if s.transcript == "" {
			s.transcript = a.findTranscript(id, rec.Cwd, now.Sub(s.globAt) >= globEvery)
			if s.transcript == "" && now.Sub(s.globAt) >= globEvery {
				s.globAt = now
			}
		}
	}

	ids := make([]string, 0, len(r.sessions))
	for id := range r.sessions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		s := r.sessions[id]
		if _, ok := recs[id]; !ok {
			// The session file is gone: the session ended. It stays as ended
			// while its transcript exists; then it is gone.
			if !s.ended {
				s.ended, s.endedAt, s.live = true, now, model.Of(false)
				s.termKey, s.termID = "", ""
			}
			if s.transcript == "" || !exists(s.transcript) {
				delete(r.sessions, id)
				delete(r.sent, id)
				r.emit(model.SessionGone{Key: model.SessionKey{Tool: model.ToolClaude, ID: id}})
				continue
			}
		}
		r.send(id, s)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// lastChange is the newest modification time of the given files.
func lastChange(paths ...string) time.Time {
	var t time.Time
	for _, p := range paths {
		if p == "" {
			continue
		}
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(t) {
			t = fi.ModTime()
		}
	}
	return t
}

// projectDirName is how Claude Code names a folder's transcript directory:
// every character that is not a letter or a digit becomes a dash.
func projectDirName(folder string) string {
	b := []byte(folder)
	for i, c := range b {
		if !isAlnum(c) {
			b[i] = '-'
		}
	}
	return string(b)
}

func isAlnum(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
}

// globEvery is how often a session without a transcript searches every
// project folder for it; its own folder is checked on every rescan.
const globEvery = 30 * time.Second

// findTranscript returns the transcript of session id, "" when there is none
// yet. The id was validated, so it holds no path separator or wildcard.
func (a *adapter) findTranscript(id, cwd string, search bool) string {
	if cwd != "" {
		p := filepath.Join(a.projectsDir(), projectDirName(cwd), id+".jsonl")
		if exists(p) {
			return p
		}
	}
	if !search {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(a.projectsDir(), "*", id+".jsonl"))
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

func (r *registry) jobs() map[string]job {
	jobs := map[string]job{}
	for id, s := range r.sessions {
		if s.ended || s.transcript == "" {
			continue
		}
		jobs[id] = job{id: id, main: s.transcript, subDir: filepath.Join(filepath.Dir(s.transcript), id, "subagents")}
	}
	return jobs
}

func (r *registry) applyIndex(res result) {
	s := r.sessions[res.id]
	if s == nil || s.ended {
		return
	}
	s.d, s.subagents = res.d, res.subagents
	r.send(res.id, s)
}

// send emits the session's fact when it differs from the last one sent.
func (r *registry) send(id string, s *sess) {
	f := r.fact(id, s)
	if old, ok := r.sent[id]; ok && reflect.DeepEqual(old, f) {
		return
	}
	r.sent[id] = f
	r.emit(f)
}

func (r *registry) fact(id string, s *sess) model.SessionFact {
	rec := s.rec
	f := model.SessionFact{
		Key:         model.SessionKey{Tool: model.ToolClaude, ID: id},
		Name:        rec.Name,
		ToolVersion: rec.Version,
		Status:      mapStatus(rec.Status),
		StatusSince: msTime(rec.StatusUpdatedAt),
		StartedAt:   msTime(rec.StartedAt),
		Live:        s.live,
		Ended:       s.ended,
		EndedAt:     s.endedAt,
		Folder:      s.d.Folder,
		TerminalID:  s.termID,
		Subagents:   s.subagents,
		CreatedPRs:  slices.Clone(s.d.CreatedPRs),
		WorkerHint:  workerHint(s.d),
	}
	if f.Folder == "" {
		f.Folder, _ = validFolder(rec.Cwd)
	}
	if s.d.Model != "" {
		f.Model = model.Of(s.d.Model)
	}
	f.LastActivity = s.d.LastActivity
	if u := msTime(rec.UpdatedAt); u.After(f.LastActivity) && !u.After(r.a.now().Add(24*time.Hour)) {
		f.LastActivity = u
	}
	if s.ended {
		f.Status = model.Unknown[model.Status]()
	}
	cut := r.a.now().Add(-turnWindow)
	for _, t := range s.d.TurnEnds {
		if !t.Before(cut) {
			f.TurnEnds = append(f.TurnEnds, t)
		}
	}
	f.SpawnedNames = slices.Clone(s.d.SpawnedNames)
	f.SpawnedTerminalIDs = slices.Clone(s.d.SpawnedTerminalIDs)
	f.PeerSenders = slices.Clone(s.d.PeerSenders)
	return f
}

// workerHint: the session received a message from a session it did not spawn
// itself. A coordinator hears from its own workers all the time; that is not a
// sign that it is a worker.
func workerHint(d derived) bool {
	for _, p := range d.PeerSenders {
		if !slices.Contains(d.SpawnedNames, p) {
			return true
		}
	}
	return false
}
