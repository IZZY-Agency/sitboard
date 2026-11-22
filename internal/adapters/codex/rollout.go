package codex

import (
	"bufio"
	"bytes"
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
	id        string // the thread's id: only its own token records count
	spendOnly bool   // a spawned thread: only its token records are read
	sawRecord bool   // a valid token record of the thread was read from this file
	path      string
	off       int64
	info      os.FileInfo
	d         derived
	asking    map[string]bool // request_user_input calls without an output yet
	skipCut   bool            // the first read started mid-file: drop the cut line
	opened    bool            // the file was opened once: the readCap jump is done
	stale     bool            // the last read failed or was cut short: read again, from t.off
	cap       int64           // how far before its end a file first opened is read from
}

func newTailer(path string) *tailer {
	return &tailer{path: path, asking: map[string]bool{}, cap: readCap}
}

var errNotRegular = errors.New("codex: rollout is not a regular file")

// update reads what was appended since the last call and reports whether the
// derived facts may have changed. A file whose size and modification time are
// unchanged is not opened. A file replaced or cut short is read again from
// its start.
func (t *tailer) update(ctx context.Context, now time.Time) (changed bool, err error) {
	defer func() {
		if err != nil {
			t.stale = true // a read that failed or was cut short is repeated from t.off, not from a jump
		}
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
		clear(t.asking)
		changed = true
	}
	if !changed && !t.stale && t.info != nil && fi.Size() == t.info.Size() && fi.ModTime().Equal(t.info.ModTime()) {
		return t.prune(now), nil
	}
	if !t.opened {
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
	if _, err := f.Seek(t.off, io.SeekStart); err != nil {
		return changed, err
	}
	r := bufio.NewReaderSize(f, 256<<10)
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

// prune drops turn ends older than turnWindow and spend older than spendKeep.
func (t *tailer) prune(now time.Time) bool {
	spans := t.pruneSpans(now)
	cut := now.Add(-turnWindow)
	i := 0
	for i < len(t.d.TurnEnds) && t.d.TurnEnds[i].Before(cut) {
		i++
	}
	if i == 0 {
		return spans
	}
	t.d.TurnEnds = slices.Clone(t.d.TurnEnds[i:])
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
	if json.Unmarshal(b, &l) != nil {
		return
	}
	if t.spendOnly { // a spawned thread, or a catch-up read: its token records, nothing else
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
			t.setStatus(model.StatusBusy, at)
		case "task_complete", "turn_complete", "turn_aborted":
			t.setStatus(model.StatusFinished, at)
			clear(t.asking)
			if !at.IsZero() && len(t.d.TurnEnds) < maxTurns {
				t.d.TurnEnds = append(t.d.TurnEnds, at)
			}
		case "item_completed":
			if p.Item.Type == "CommandExecution" {
				t.command(p.Item.Cmd, p.Item.Output, at)
			}
		}
	case "response_item":
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
	if len(cmd.Runs("agtermctl", "session", "new")) > 0 {
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
