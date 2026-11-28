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
// Artifacts come from the session's own Artifact publishes (action "publish"
// or none, not an asset upload) whose result line carries one tool result and
// a structured toolUseResult object with the artifact's url and title. The url
// must be a claude.ai artifact address (model.ArtifactURLPattern). The same
// url is the same artifact; the latest publish's title wins. Seen in real
// records on 2.1.322 to 2.1.326. Deletes are not followed: no real record of
// a whole artifact's delete exists to model, so a deleted artifact stays
// listed.
//
// Sessions that had already ended when sitboard started are indexed once, in
// the background after the live ones: those whose record file is still there
// (a crash) and those found only by their transcript (Claude Code removes the
// record when a session exits; the transcript keeps the session id as its
// name). Only transcripts changed within sessionKeep count, and of a transcript
// first opened for a last pass only the last endedReadCap bytes are read. A
// session found by its transcript counts only when its lines say it was
// interactive (entrypoint "cli", or none): claude -p and the SDK runs are left
// out. Its name is the one Claude Code recorded in its agent-name lines; its
// start time and status are lost with the record. A session that ends while
// sitboard runs gets one last pass too. After that last pass only the derived
// facts are kept, not the tailers, and they go once the session ended more
// than sessionKeep ago.
//
// All facts are emitted from one goroutine, so a fact and a gone for one
// session never swap. Nothing from a transcript but derived fields leaves this
// package, and only one environment variable is read from a live process.
package claude

import (
	"cmp"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
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
	// TmuxOwns asks tmux whether pane (with the tty of the session's
	// process) is a pane of that server showing that tty. A tmux pane read
	// from a process's environment is kept only when it says yes, once per
	// process. Nil: no pane is kept.
	TmuxOwns func(ctx context.Context, pane model.TmuxPane) bool
}

type adapter struct {
	opts     Options
	root     string // <Home>/.claude
	now      func() time.Time
	starts   startsFunc
	proc     func(pid int) procFacts // read once per process
	tty      ttyFunc                 // the process's terminal, asked once per process in tmux
	tick     time.Duration           // transcript growth (stat only) and re-adding the watch
	liveTick time.Duration           // full rescan with ps: liveness
	debounce time.Duration
	prober   *prober // folder lookups (procProber; tests have their own)
	prices   *prices // Claude Code's price table
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
		starts: defaultStarts(), proc: readProcFacts, tty: defaultTTY(),
		tick: 3 * time.Second, liveTick: 10 * time.Second, debounce: 100 * time.Millisecond,
		prober: procProber, prices: newPrices(filepath.Join(opts.Home, ".claude.json")),
	}
}

func (a *adapter) Kind() source.Kind { return source.KindClaude }

// Options returns the options the watcher was built with, so the program's
// wiring can be checked.
func (a *adapter) Options() Options { return a.opts }

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
	rec         record
	recFile     string
	live        model.Known[bool]
	ended       bool
	endedAt     time.Time
	termID      string
	argHeadless bool // the process was started with -p / --print
	tmux        model.TmuxPane
	launch      string // the effort level the process was started with, "" when none
	termKey     string // pid and process start the process facts were read for
	transcript  string // "" until found
	// others: the session's transcripts in other project folders, older:
	// it was resumed in another folder, and goes on in a transcript of the
	// same name there. Their use is the session's too.
	others    []string
	cwdSeen   string    // the record's folder the transcript was last looked up for
	globAt    time.Time // last search of every project folder for it
	d         derived
	subagents int
	// final: the last indexing pass after the session ended was applied; it
	// is not indexed again unless it comes back to life.
	final bool
	// found: known only from its transcript (no record file); it is sent once
	// its last pass is in, never as an empty fact first.
	found bool
	// headless: at its last pass its transcript said it was not an
	// interactive session (claude -p, the SDKs), or nothing in it could be
	// read. It is not sent (one sent while it ran is withdrawn), and nothing
	// read from it is kept.
	headless bool
	// priced is d.spend priced with the table spendTab (by identity): kept so
	// that a tick that changes nothing does not price it again.
	priced   model.Spend
	pricedOf *spendData
	spendTab uintptr
	// ends counts the times the session ended: each end has its own last pass.
	ends int
}

// pricedSpend is the session's spend priced with tab.
func (s *sess) pricedSpend(tab priceTable) model.Spend {
	id := reflect.ValueOf(tab).Pointer()
	if s.pricedOf != s.d.spend || s.spendTab != id {
		s.priced, s.pricedOf, s.spendTab = priceSpend(s.d.spend, tab), s.d.spend, id
	}
	return s.priced
}

// Run emits the live sessions at once, then derived facts as the transcripts
// are read, then every change, until ctx ends.
func (a *adapter) Run(ctx context.Context, emit func(model.Fact)) error {
	ready := model.SourceReady{Kind: string(source.KindClaude)}
	if fi, err := os.Stat(a.root); err != nil || !fi.IsDir() {
		emit(ready)
		emit(model.BackfillDone{Kind: string(source.KindClaude)}) // nothing to read
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

	r := &registry{a: a, emit: emit, sessions: map[string]*sess{}, sent: map[string]model.SessionFact{}, firsts: map[string]firstSession{}}
	watch()
	r.rescan(ctx)
	if ctx.Err() != nil {
		return nil
	}
	emit(ready) // every live session is out; the transcripts follow in the background
	ix.want(r.jobs())
	r.discover() // after the first paint: what ended before this start
	ix.want(r.jobs())
	r.checkBackfill()

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

// firstSession is the first session seen in one process.
type firstSession struct {
	pid       int
	procStart string
	id        string
}

// registry is the Run goroutine's state. Only that goroutine touches it.
type registry struct {
	a        *adapter
	emit     func(model.Fact)
	sessions map[string]*sess
	sent     map[string]model.SessionFact
	// firsts maps a live process (pid and start) to the first session seen
	// running in it. A later one is the same process after /clear: its launch
	// level may be stale (an /effort before the clear carries over), so it is
	// not shown. An entry goes only when ps says the process is gone, never
	// because its session file was missing or unreadable in one scan.
	firsts map[string]firstSession
	// backfilling: the sessions that had ended before this start are still
	// being read; BackfillDone is sent when they are.
	backfilling bool
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

	pids := make([]int, 0, len(recs)+len(r.firsts))
	for _, rec := range recs {
		pids = append(pids, rec.PID)
	}
	for _, f := range r.firsts { // the processes behind the /clear rule, record or not
		pids = append(pids, f.pid)
	}
	slices.Sort(pids)
	pids = slices.Compact(pids)
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
		s.rec, s.recFile, s.found = rec, files[id], false
		switch start, running := starts[rec.PID]; {
		case !known:
			s.live, s.ended = model.Unknown[bool](), false
		case running && (rec.ProcStart == "" || sameStart(start, rec.ProcStart)):
			s.live, s.ended, s.endedAt, s.final, s.headless = model.Of(true), false, time.Time{}, false, false
		default: // the process is gone, or its pid now belongs to another program
			if !s.ended {
				s.ended, s.endedAt = true, lastChange(s.recFile, s.transcript)
				s.ends++
			}
			s.live = model.Of(false)
		}
		if s.live.V && s.live.OK {
			if key := strconv.Itoa(rec.PID) + "|" + rec.ProcStart; key != s.termKey {
				p := a.proc(rec.PID)
				s.termKey, s.termID, s.tmux, s.launch = key, p.TermID, a.ownPane(ctx, rec.PID, starts[rec.PID], p.Tmux), p.LaunchEffort
				s.argHeadless = p.Headless
			}
			if first, seen := r.firsts[s.termKey]; !seen {
				r.firsts[s.termKey] = firstSession{pid: rec.PID, procStart: rec.ProcStart, id: id}
			} else if first.id != id {
				s.launch = ""
			}
		} else {
			s.termKey, s.termID, s.tmux, s.launch = "", "", model.TmuxPane{}, ""
		}
		if s.transcript == "" {
			s.transcript = a.findTranscript(id, rec.Cwd, now.Sub(s.globAt) >= globEvery)
			if s.transcript == "" && now.Sub(s.globAt) >= globEvery {
				s.globAt = now
			}
			if s.transcript != "" {
				s.others, s.cwdSeen = a.otherTranscripts(id, s.transcript), rec.Cwd
			}
		} else if rec.Cwd != s.cwdSeen {
			// Resumed in another folder: the transcript there is the one
			// that goes on, the older one stays the session's.
			s.cwdSeen = rec.Cwd
			if p := a.cwdTranscript(id, rec.Cwd); p != "" && p != s.transcript {
				s.others = append(slices.DeleteFunc(s.others, func(o string) bool { return o == p }), s.transcript)
				s.transcript = p
			}
		}
	}

	if known {
		for k, f := range r.firsts {
			start, running := starts[f.pid]
			if !running || (f.procStart != "" && !sameStart(start, f.procStart)) {
				delete(r.firsts, k) // the process is gone, or its pid is another program's
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
				s.termKey, s.termID, s.tmux, s.launch = "", "", model.TmuxPane{}, ""
				s.ends++
			}
			// Gone too once it ended more than sessionKeep ago: nothing of
			// it is shown any more, so nothing of it is kept.
			if s.transcript == "" || !exists(s.transcript) || (s.ended && now.Sub(s.endedAt) > sessionKeep) {
				_, sent := r.sent[id]
				delete(r.sessions, id)
				delete(r.sent, id)
				if sent || !s.found {
					r.emit(model.SessionGone{Key: model.SessionKey{Tool: model.ToolClaude, ID: id}})
				}
				continue
			}
		}
		if (s.found && !s.final) || s.headless {
			continue // not indexed yet, or not an interactive session
		}
		r.send(id, s)
	}
	r.checkBackfill()
}

// checkBackfill sends BackfillDone once, when every session that had ended
// before this start has had its last pass (or left: its transcript went, or
// it fell out of the window).
func (r *registry) checkBackfill() {
	if !r.backfilling {
		return
	}
	now := r.a.now()
	for _, s := range r.sessions {
		if s.ended && !s.final && s.transcript != "" && now.Sub(s.endedAt) <= sessionKeep {
			return
		}
	}
	r.backfilling = false
	r.emit(model.BackfillDone{Kind: string(source.KindClaude)})
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

// sessionKeep is how long an ended session is kept and read: as long as its
// spend spans are (spendKeep), a day more than the 7 days of turns, so the
// first local day the spend summary shows is whole even after a fall-back
// night. Its turns are still cut at turnWindow.
const sessionKeep = spendKeep

// globEvery is how often a session without a transcript searches every
// project folder for it; its own folder is checked on every rescan.
const globEvery = 30 * time.Second

// findTranscript returns the transcript of session id, "" when there is none
// yet. The id was validated, so it holds no path separator or wildcard.
func (a *adapter) findTranscript(id, cwd string, search bool) string {
	if p := a.cwdTranscript(id, cwd); p != "" {
		return p
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

// cwdTranscript is session id's transcript in folder cwd's project
// directory, "" when there is none.
func (a *adapter) cwdTranscript(id, cwd string) string {
	if cwd == "" {
		return ""
	}
	p := filepath.Join(a.projectsDir(), projectDirName(cwd), id+".jsonl")
	if !exists(p) {
		return ""
	}
	return p
}

// maxOthers caps the other transcripts kept for one session.
const maxOthers = 16

// otherTranscripts lists session id's transcripts in every project folder
// but main's, by name. Links are not followed.
func (a *adapter) otherTranscripts(id, main string) []string {
	matches, _ := filepath.Glob(filepath.Join(a.projectsDir(), "*", id+".jsonl"))
	var out []string
	for _, p := range matches {
		if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() && p != main && len(out) < maxOthers {
			out = append(out, p)
		}
	}
	return out
}

// discover adds the sessions that had ended before this start and left no
// record file. Their transcripts are named by session id, in their folder's
// project directory; those changed within sessionKeep are indexed once. Links
// are not followed, neither to a folder nor to a file. A transcript in two
// project directories (a session resumed in another folder) is one session:
// the one changed last is its transcript, the others are kept beside it.
func (r *registry) discover() {
	r.backfilling = true
	now := r.a.now()
	dirs, _ := os.ReadDir(r.a.projectsDir())
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(r.a.projectsDir(), d.Name())
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			id, ok := strings.CutSuffix(e.Name(), ".jsonl")
			if !ok || !e.Type().IsRegular() || !validSessionID(id) {
				continue
			}
			old := r.sessions[id]
			if old != nil && !old.found {
				continue // a session with a record: the registry has it
			}
			info, err := e.Info()
			if err != nil || now.Sub(info.ModTime()) > sessionKeep {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if old != nil && !info.ModTime().After(old.endedAt) {
				if len(old.others) < maxOthers {
					old.others = append(old.others, p)
				}
				continue
			}
			s := &sess{
				rec: record{SessionID: id}, transcript: p,
				live: model.Of(false), ended: true, endedAt: info.ModTime(), found: true, ends: 1,
			}
			if old != nil {
				s.others = append(slices.Clone(old.others), old.transcript)
			}
			r.sessions[id] = s
		}
	}
}

// jobs lists the sessions to index: the live ones, and the ended ones until
// their last pass is in, if they ended within sessionKeep.
func (r *registry) jobs() map[string]job {
	now := r.a.now()
	jobs := map[string]job{}
	for id, s := range r.sessions {
		if s.transcript == "" || s.final || (s.ended && now.Sub(s.endedAt) > sessionKeep) {
			continue
		}
		j := job{id: id, main: s.transcript, ended: s.ended, gen: s.ends, others: slices.Clone(s.others)}
		for _, p := range append([]string{s.transcript}, s.others...) {
			j.subDirs = append(j.subDirs, filepath.Join(filepath.Dir(p), id, "subagents"))
		}
		jobs[id] = j
	}
	return jobs
}

func (r *registry) applyIndex(res result) {
	s := r.sessions[res.id]
	if s == nil || s.final || s.ended != res.final || (res.final && res.gen != s.ends) {
		return // a pass from before the session ended (or ended again): its last pass follows
	}
	s.d, s.subagents, s.final = res.d, res.subagents, res.final
	if res.final && (!res.readable || !interactive(s.d.Entrypoint)) {
		s.headless, s.d, s.subagents = true, derived{}, 0
		if _, sent := r.sent[res.id]; sent { // a claude -p run seen while it ran
			delete(r.sent, res.id)
			r.emit(model.SessionGone{Key: model.SessionKey{Tool: model.ToolClaude, ID: res.id}})
		}
		r.checkBackfill()
		return
	}
	r.send(res.id, s)
	r.checkBackfill()
}

// interactive: what a transcript's entrypoint says. Headless runs are
// claude -p ("sdk-cli"), the SDKs ("sdk-…") and the MCP server ("mcp");
// everything else is a person's session: the terminal ("cli"), the IDE and
// desktop apps, remote and chat ones. None (an older Claude Code) counts.
func interactive(entrypoint string) bool {
	return !strings.HasPrefix(entrypoint, "sdk-") && entrypoint != "mcp"
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
		Key:           model.SessionKey{Tool: model.ToolClaude, ID: id},
		Name:          cmp.Or(rec.Name, s.d.Name),
		ToolVersion:   rec.Version,
		Status:        mapStatus(rec.Status),
		StatusSince:   msTime(rec.StatusUpdatedAt),
		StartedAt:     msTime(rec.StartedAt),
		Live:          s.live,
		Ended:         s.ended,
		EndedAt:       s.endedAt,
		Folder:        s.d.Folder,
		TerminalID:    s.termID,
		Tmux:          s.tmux,
		Subagents:     s.subagents,
		CreatedPRs:    slices.Clone(s.d.CreatedPRs),
		PushedPRs:     slices.Clone(s.d.PushedPRs),
		TouchedIssues: slices.Clone(s.d.TouchedIssues),
		Artifacts:     slices.Clone(s.d.Artifacts),
		Spend:         s.pricedSpend(r.a.prices.table(r.a.now())),
		WorkerHint:    workerHint(s.d),
		Backfill:      s.found,
		Headless:      s.argHeadless || !interactive(s.d.Entrypoint),
	}
	if f.Headless {
		// A claude -p run started from another session's terminal inherits
		// that terminal's AGTERM_SESSION_ID; it is not what runs there. Told
		// from the process's arguments (-p), so from the first fact on, or
		// from the transcript's entrypoint (an SDK run) once it is read.
		f.TerminalID = ""
	}
	if f.Folder == "" {
		f.Folder, _ = validFolder(rec.Cwd)
	}
	if s.live.OK && s.live.V {
		f.PID = rec.PID // its start time matched the record's
	}
	if s.d.Model != "" {
		f.Model = model.Of(s.d.Model)
	}
	switch {
	case s.d.Effort != "":
		f.Effort = model.Of(s.d.Effort)
	case s.d.Model == "" && s.launch != "":
		// No reply yet (every main-chain reply sets the model): the level the
		// process was started with, until the first reply replaces it.
		f.Effort, f.EffortAtLaunch = model.Of(s.launch), true
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
