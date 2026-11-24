package codex

import (
	"bufio"
	"bytes"
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/shellcmd"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

// Limits on what one rollout may make the adapter hold.
const (
	maxLine     = 4 << 20  // a longer line is skipped
	maxMetaLine = 1 << 20  // the first line (session_meta) is read up to this
	readCap     = 64 << 20 // a rollout first opened is read from at most this far before its end
	maxPRs      = 200
	maxSpawns   = 500
	maxTurns    = 5000
	maxPending  = 1000
	maxWord     = 64 // model ids and effort words longer than this are dropped
)

// liveReadCap is readCap for a tailer made by newTailer; tests set it lower.
var liveReadCap int64 = readCap

// threadIDRe is a Codex thread id: a UUID, lowercase as Codex writes it.
var threadIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// rolloutNameRe is a rollout file's name: rollout-<local time>-<thread id>.jsonl.
// Compressed rollouts (.jsonl.zst, untouched for 7 days) are never read.
var rolloutNameRe = regexp.MustCompile(`^rollout-[0-9T:.-]+-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.jsonl$`)

// meta is what the first line of a rollout, session_meta, says about the
// thread. A forked thread repeats its parent's session_meta further down;
// only the first one is the thread's own.
type meta struct {
	ID         string
	StartedAt  time.Time
	Cwd        string
	Version    string
	Source     string // "cli", "exec", "vscode", "mcp"; "subagent" for a spawned child; "" when unknown
	Originator string
	Parent     string // the parent thread of a spawned child, "" otherwise
}

type metaLine struct {
	Type    string `json:"type"`
	Payload struct {
		ID             string          `json:"id"`
		Timestamp      string          `json:"timestamp"`
		Cwd            string          `json:"cwd"`
		CLIVersion     string          `json:"cli_version"`
		Originator     string          `json:"originator"`
		Source         json.RawMessage `json:"source"`
		ThreadSource   string          `json:"thread_source"`
		ParentThreadID string          `json:"parent_thread_id"`
	} `json:"payload"`
}

// parseMeta reads a session_meta line. ok is false when the line is not one,
// or its id is not a thread id.
func parseMeta(line []byte, now time.Time) (meta, bool) {
	var l metaLine
	if json.Unmarshal(line, &l) != nil || l.Type != "session_meta" || !threadIDRe.MatchString(l.Payload.ID) {
		return meta{}, false
	}
	p := l.Payload
	m := meta{ID: p.ID, StartedAt: parseTS(p.Timestamp, now), Originator: capWord(p.Originator), Version: capWord(p.CLIVersion)}
	m.Cwd, _ = validFolder(p.Cwd)
	var src string
	var obj map[string]json.RawMessage
	switch {
	case json.Unmarshal(p.Source, &src) == nil:
		m.Source = capWord(src)
	case json.Unmarshal(p.Source, &obj) == nil:
		if _, ok := obj["subagent"]; ok {
			m.Source = "subagent"
		}
	}
	if p.ThreadSource == "subagent" {
		m.Source = "subagent"
	}
	if m.Source == "subagent" && threadIDRe.MatchString(p.ParentThreadID) {
		m.Parent = p.ParentThreadID
	}
	return m, true
}

// readMeta reads the first line of a rollout. Links are not followed.
func readMeta(path string, now time.Time) (meta, bool) {
	f, err := openRegular(path)
	if err != nil {
		return meta{}, false
	}
	defer func() { _ = f.Close() }()
	line, err := bufio.NewReaderSize(io.LimitReader(f, maxMetaLine), 64<<10).ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) { // longer than the buffer: read it whole, within the cap
		_, _ = f.Seek(0, io.SeekStart)
		b, _ := io.ReadAll(io.LimitReader(f, maxMetaLine))
		line, _, _ = bytes.Cut(b, []byte{'\n'})
	}
	return parseMeta(bytes.TrimSpace(line), now)
}

// derived holds the facts read from a rollout. It never holds rollout text:
// only a model id, an effort word, a folder, times, pull request references
// and the names and ids of sessions it started.
type derived struct {
	Model        string
	Effort       string // as Codex wrote it, lowercase
	Folder       string
	Status       model.Known[model.Status]
	StatusSince  time.Time
	LastActivity time.Time
	TurnEnds     []time.Time
	CreatedPRs   []model.PRRef
	PushedPRs    []model.PushedRef // those of CreatedPRs a creating push announced, with the call's window
	SpawnedNames []string
	SpawnedIDs   []string
	// Total is the thread's token use, from its last token_usage_record;
	// Spans its use by quarter hour (UTC), for the last spendKeep only.
	Total model.Tokens
	Spans map[time.Time]model.Tokens
}

// tailer reads a rollout from a remembered offset.
type tailer struct {
	id        string    // the thread's id: only its own token records count
	spendOnly bool      // a spawned thread: only its token records are read
	startup   bool      // the start-up read of an ended session: no status, no approval waits, no agterm spawns
	started   time.Time // the thread's own session_meta time, once read
	forked    bool      // another thread's session_meta followed its own: a fork's copied history comes before it
	foreign   bool      // its first session_meta is another thread's: nothing in the file is read as this one's
	sawRecord bool      // a valid token record of the thread was read from this file
	path      string
	off       int64
	info      os.FileInfo
	d         derived
	asking    map[string]bool // request_user_input calls without an output yet
	skipCut   bool            // the first read started mid-file: drop the cut line
	opened    bool            // the file was opened once: the readCap jump is done
	stale     bool            // the last read failed or was cut short: read again, from t.off
	cap       int64           // how far before its end a file first opened is read from
	whole     bool            // a file first opened larger than cap is not read at all: errTooLarge
	turns     turnHeap        // the newest maxTurns turn ends in the window, oldest on top; d.TurnEnds is it sorted
	turnsSet  bool            // turns changed since d.TurnEnds was last set from it
}

// turnHeap is a min-heap of turn end times.
type turnHeap []time.Time

func (h turnHeap) Len() int           { return len(h) }
func (h turnHeap) Less(i, j int) bool { return h[i].Before(h[j]) }
func (h turnHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *turnHeap) Push(x any)        { *h = append(*h, x.(time.Time)) }
func (h *turnHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// syncTurns sets d.TurnEnds, in time order, from the heap when it changed.
func (t *tailer) syncTurns() {
	if !t.turnsSet {
		return
	}
	t.turnsSet = false
	if len(t.turns) == 0 {
		t.d.TurnEnds = nil
		return
	}
	t.d.TurnEnds = slices.Clone([]time.Time(t.turns))
	slices.SortFunc(t.d.TurnEnds, time.Time.Compare)
}

func newTailer(path string) *tailer {
	return &tailer{path: path, asking: map[string]bool{}, cap: liveReadCap}
}

var errNotRegular = errors.New("codex: rollout is not a regular file")

// errTooLarge: a tailer that reads only whole files (whole) met one larger
// than its cap, as its own stat sees it.
var errTooLarge = errors.New("codex: rollout too large to read whole")

// update reads what was appended since the last call and reports whether the
// derived facts may have changed. A file whose size and modification time are
// unchanged is not opened. A file replaced or cut short is read again from
// its start.
func (t *tailer) update(ctx context.Context, now time.Time) (changed bool, err error) {
	defer func() {
		if err != nil {
			t.stale = true // a read that failed or was cut short is repeated from t.off, not from a jump
		}
		t.syncTurns()
	}()
	fi, err := os.Lstat(t.path)
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, errNotRegular
	}
	if t.info != nil && (!os.SameFile(t.info, fi) || fi.Size() < t.off) {
		// Replaced or cut short: what was derived is not this file's.
		t.off, t.d, t.skipCut, t.opened, t.sawRecord = 0, derived{}, false, false, false
		t.started, t.forked, t.foreign, t.turns, t.turnsSet = time.Time{}, false, false, nil, false // a replacement's fork boundary is its own
		clear(t.asking)
		changed = true
	}
	if !changed && !t.stale && t.info != nil && fi.Size() == t.info.Size() && fi.ModTime().Equal(t.info.ModTime()) {
		return t.prune(now), nil
	}
	first := !t.opened
	if first {
		if t.whole && fi.Size() > t.cap {
			return false, errTooLarge
		}
		t.opened = true
		if fi.Size() > t.cap {
			t.off, t.skipCut = fi.Size()-t.cap, true
		}
	}
	t.info, t.stale = fi, false
	if fi.Size() == t.off {
		return t.prune(now) || changed, nil
	}
	f, err := openRegular(t.path)
	if err != nil {
		return changed, err
	}
	defer func() { _ = f.Close() }()
	if ofi, err := f.Stat(); err != nil || !os.SameFile(ofi, fi) {
		return changed, errNotRegular
	}
	if first {
		// The head, read through this descriptor before anything else, holds
		// the thread's own session_meta and, in a fork, its parent's after it:
		// read so, the copied history (in a tail past a jump too) is known as
		// the parent's. A head that cannot be read whole may hide a fork: then
		// nothing from before the thread's own start (or, without it, before
		// now) is taken as its own.
		if !t.headMetas(f, now) {
			t.forked = true
			if t.started.IsZero() {
				t.started = now
			}
		}
	}
	if _, err := f.Seek(t.off, io.SeekStart); err != nil {
		return changed, err
	}
	var src io.Reader = f
	if t.whole { // past the cap by one byte at most: grown since the stat, it is too large
		src = io.LimitReader(f, t.cap+1-t.off)
	}
	r := bufio.NewReaderSize(src, 256<<10)
	var long []byte
	skipping, lineLen := false, int64(0)
	for n := 1; ; n++ {
		if n%512 == 0 && ctx.Err() != nil {
			return changed, ctx.Err() // not read to the end: stale, read again from t.off
		}
		chunk, err := r.ReadSlice('\n')
		lineLen += int64(len(chunk))
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			if !skipping {
				if len(long)+len(chunk) > maxLine {
					skipping, long = true, nil
				} else {
					long = append(long, chunk...)
				}
			}
			continue
		case errors.Is(err, io.EOF): // an incomplete last line waits for the next update
			if t.whole && t.off+lineLen > t.cap {
				return changed, errTooLarge
			}
			return t.prune(now) || changed, nil
		case err != nil:
			return changed, err
		}
		if t.skipCut {
			skipping, long, t.skipCut = true, nil, false
		}
		if !skipping && len(long)+len(chunk) <= maxLine {
			line := chunk
			if long != nil {
				line = slices.Concat(long, chunk)
			}
			t.consume(bytes.TrimSpace(line), now)
		}
		t.off += lineLen
		lineLen, long, skipping = 0, nil, false
		changed = true
	}
}

// maxHeadMetas is how many lines at a rollout's head are looked at for its
// session_meta lines: the thread's own, then, in a fork, its parent's.
const maxHeadMetas = 4

// headMetas reads the first maxHeadMetas lines of a rollout opened for the
// first time and takes the session_meta lines among them: the thread's own
// first, then, in a fork, its parent's (up to three lines after it). whole is
// whether it read each of them (a short file's all) and the first is the
// thread's own session_meta: a line longer than maxMetaLine, or one that is
// not JSON, could be a fork's parent's. A first line that is another thread's
// session_meta makes the file foreign: nothing in it is the thread's.
func (t *tailer) headMetas(f *os.File, now time.Time) (whole bool) {
	if t.spendOnly || t.id == "" {
		return true // no turn or pull request is read from it
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false
	}
	// Whole lines, wherever a jump landed: one it cuts is still read here.
	r := bufio.NewReaderSize(io.LimitReader(f, maxHeadMetas*maxMetaLine), 64<<10)
	for i := range maxHeadMetas {
		b, err := readLine(r, maxMetaLine)
		if errors.Is(err, io.EOF) && i > 0 {
			return true // a short file: the rest is read as it is written
		}
		var l line
		if err != nil || json.Unmarshal(bytes.TrimSpace(b), &l) != nil {
			return false
		}
		if l.Type == "session_meta" {
			t.sessionMeta(l.Payload, parseTS(l.Timestamp, now))
		}
		if i == 0 && t.started.IsZero() {
			return false
		}
	}
	return true
}

// errLineTooLong: readLine met a line longer than its limit.
var errLineTooLong = errors.New("codex: line too long")

// readLine reads one whole line of at most limit bytes; a longer line is
// errLineTooLong, and an unfinished one an error.
func readLine(r *bufio.Reader, limit int) ([]byte, error) {
	var b []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(b)+len(chunk) > limit {
			return nil, errLineTooLong
		}
		b = append(b, chunk...)
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case err != nil:
			return nil, err
		}
		return b, nil
	}
}

// prune drops turn ends older than turnWindow and spend older than spendKeep.
func (t *tailer) prune(now time.Time) bool {
	spans := t.pruneSpans(now)
	cut := now.Add(-turnWindow)
	dropped := false
	for len(t.turns) > 0 && t.turns[0].Before(cut) {
		heap.Pop(&t.turns)
		dropped = true
	}
	if !dropped {
		return spans
	}
	t.turnsSet = true
	t.syncTurns()
	return true
}

// recordMark is what a token_usage_record line contains: its type.
var recordMark = []byte("token_usage_record")

type line struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type payload struct {
	Type   string `json:"type"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
	Cwd    string `json:"cwd"`
	Name   string `json:"name"`
	CallID string `json:"call_id"`
	Item   struct {
		Type   string          `json:"type"`
		Cmd    json.RawMessage `json:"command"`
		Output string          `json:"aggregated_output"`
	} `json:"item"`
}

// consume reads one rollout line. Text it looks at (a command and its output)
// is matched against and dropped, never kept.
func (t *tailer) consume(b []byte, now time.Time) {
	// A thread read for its spend alone skips every line that does not name the
	// record type, without decoding it: most of a rollout is other lines.
	if t.spendOnly && !bytes.Contains(b, recordMark) {
		return
	}
	var l line
	if t.foreign || json.Unmarshal(b, &l) != nil {
		return
	}
	if t.spendOnly { // a spawned thread, followed live or caught up: its token records, nothing else
		if l.Type == "token_usage_record" {
			t.record(l.Payload, parseTS(l.Timestamp, now))
		}
		return
	}
	at := parseTS(l.Timestamp, now)
	if at.After(t.d.LastActivity) {
		t.d.LastActivity = at
	}
	var p payload
	switch l.Type {
	case "session_meta":
		t.sessionMeta(l.Payload, at)
	case "token_usage_record":
		t.record(l.Payload, at)
	case "turn_context":
		if json.Unmarshal(l.Payload, &p) != nil {
			return
		}
		if w := capWord(p.Model); w != "" {
			t.d.Model = w
		}
		t.d.Effort = effortWord(p.Effort)
		if f, ok := validFolder(p.Cwd); ok {
			t.d.Folder = f
		}
	case "event_msg":
		if json.Unmarshal(l.Payload, &p) != nil {
			return
		}
		switch p.Type {
		case "task_started", "turn_started":
			if !t.startup {
				t.setStatus(model.StatusBusy, at)
			}
		case "task_complete", "turn_complete", "turn_aborted":
			if !t.startup {
				t.setStatus(model.StatusFinished, at)
				clear(t.asking)
			}
			t.turnEnd(at, now)
		case "item_completed":
			if p.Item.Type == "CommandExecution" && !t.inherited(at) {
				t.command(p.Item.Cmd, p.Item.Output, at)
			}
		}
	case "response_item":
		if t.startup { // approval waits are live state; an ended session has none
			return
		}
		if json.Unmarshal(l.Payload, &p) != nil || p.CallID == "" || len(p.CallID) > 128 {
			return
		}
		// A request_user_input call waits for the person until its output is
		// written. Only the synchronous tool blocks; the _async one returns
		// at once. Command and patch approvals are never written down (checked
		// live on 0.171.0, #111: while the prompt was open, the rollout ended
		// on a custom_tool_call named "exec" with no output and a
		// token_usage_record, the same as a command that is running).
		switch p.Type {
		case "function_call", "custom_tool_call":
			if p.Name == "request_user_input" && len(t.asking) < maxPending {
				t.asking[p.CallID] = true
				t.setStatus(model.StatusBlocked, at)
			}
		case "function_call_output", "custom_tool_call_output":
			if t.asking[p.CallID] {
				delete(t.asking, p.CallID)
				if len(t.asking) == 0 {
					t.setStatus(model.StatusBusy, at)
				}
			}
		}
	}
}

// sessionMeta notes the thread's own session_meta (its first) and, after it,
// another thread's: a fork repeats its parent's, followed by the parent's
// history, which is not the fork's own.
func (t *tailer) sessionMeta(raw json.RawMessage, at time.Time) {
	var m struct {
		ID string `json:"id"`
	}
	if t.id == "" || json.Unmarshal(raw, &m) != nil {
		return
	}
	switch {
	case m.ID != t.id && t.started.IsZero():
		t.foreign = true // another thread's rollout
	case m.ID == t.id && t.started.IsZero():
		t.started = at
	case m.ID != t.id && !t.started.IsZero():
		t.forked = true
	}
}

// inherited: an event from before the thread's own start, in a rollout that
// holds another thread's session_meta after its own, is a fork's copy of its
// parent's history. Nothing of it is the fork's turn or command.
func (t *tailer) inherited(at time.Time) bool {
	return t.forked && !t.started.IsZero() && at.Before(t.started)
}

// turnEnd records a finished turn: not one outside turnWindow, nor a fork's
// copy, and once maxTurns are held the oldest by time go first, whatever order
// they were written in, so that a long rollout's expired turns cannot crowd
// out its recent ones.
func (t *tailer) turnEnd(at, now time.Time) {
	if at.IsZero() || at.Before(now.Add(-turnWindow)) || t.inherited(at) {
		return
	}
	switch {
	case len(t.turns) < maxTurns:
		heap.Push(&t.turns, at)
	case at.After(t.turns[0]):
		t.turns[0] = at
		heap.Fix(&t.turns, 0)
	default:
		return // older than every turn held
	}
	t.turnsSet = true
}

func (t *tailer) setStatus(s model.Status, at time.Time) {
	t.d.Status, t.d.StatusSince = model.Of(s), at
}

// command reads a finished command for the pull requests it created and the
// agterm sessions it started. Codex records the command as an argument list,
// usually a shell and its script (["/bin/zsh", "-lc", "<script>"]); it is
// quoted back into one line for the shared parser, which reads the script.
func (t *tailer) command(raw json.RawMessage, out string, at time.Time) {
	var argv []string
	if json.Unmarshal(raw, &argv) != nil || len(argv) == 0 {
		var s string
		if json.Unmarshal(raw, &s) != nil || s == "" {
			return
		}
		argv = []string{s}
	}
	var line string
	if len(argv) == 1 {
		line = argv[0]
	} else {
		q := make([]string, len(argv))
		for i, a := range argv {
			q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		line = strings.Join(q, " ")
	}
	cmd := shellcmd.Parse(line)
	if len(cmd.Runs("gh", "pr", "create")) > 0 {
		refs := shellcmd.PullRequests(out)
		switch {
		case cmd.CreatesAmongOtherGH(): // the output may hold another pull request's address
			refs = shellcmd.OwnPullRequests(out, cmd.CreateIsFirstGH())
		case cmd.RunsOtherGH(): // so may it, and their order is not known
			refs = shellcmd.UnorderedPullRequests(out)
		}
		for _, ref := range refs {
			if !slices.Contains(t.d.CreatedPRs, ref) && shellcmd.Room(t.d.CreatedPRs, ref, maxPRs) {
				t.d.CreatedPRs = append(t.d.CreatedPRs, ref)
			}
		}
	}
	if k := cmd.MRCreation(); k.Any() {
		// A rollout records a command once, when it finished: that moment is
		// both ends of the window (the core widens it by two minutes).
		t.d.CreatedPRs, t.d.PushedPRs = shellcmd.AddMergeRequests(t.d.CreatedPRs, t.d.PushedPRs, shellcmd.MergeRequests(out, k), at, at, maxPRs)
	}
	if !t.startup && len(cmd.Runs("agtermctl", "session", "new")) > 0 {
		for _, n := range cmd.SpawnNames() {
			t.d.SpawnedNames = addCapped(t.d.SpawnedNames, n, maxSpawns)
		}
		for _, id := range shellcmd.TerminalIDs(out) {
			t.d.SpawnedIDs = addCapped(t.d.SpawnedIDs, id, maxSpawns)
		}
	}
}

func addCapped(list []string, v string, limit int) []string {
	if slices.Contains(list, v) || len(list) >= limit {
		return list
	}
	return append(list, v)
}

// effortWord keeps an effort word as Codex wrote it when it is a plain word:
// lowercase letters only, at most 16. Anything else is unknown ("").
func effortWord(s string) string {
	if s == "" || len(s) > 16 {
		return ""
	}
	for _, c := range []byte(s) {
		if c < 'a' || c > 'z' {
			return ""
		}
	}
	return s
}

// capWord keeps a short identifier (a model id, a version, a source) without
// control or space characters; anything else is unknown ("").
func capWord(s string) string {
	if len(s) > maxWord {
		return ""
	}
	for _, c := range []byte(s) {
		if c <= ' ' || c == 0x7f {
			return ""
		}
	}
	return s
}

// validFolder accepts an absolute, clean-able path.
func validFolder(p string) (string, bool) {
	if p == "" || !filepath.IsAbs(p) || len(p) > 4096 || strings.ContainsRune(p, 0) {
		return "", false
	}
	return filepath.Clean(p), true
}

// parseTS reads a line's time. A time more than a day ahead is unknown.
func parseTS(s string, now time.Time) time.Time {
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || at.After(now.Add(24*time.Hour)) {
		return time.Time{}
	}
	return at
}

// openRegular opens a regular file without following a link in its place.
func openRegular(path string) (*os.File, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errNotRegular
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if ofi, err := f.Stat(); err != nil || !os.SameFile(ofi, fi) {
		_ = f.Close()
		return nil, errNotRegular
	}
	return f, nil
}
