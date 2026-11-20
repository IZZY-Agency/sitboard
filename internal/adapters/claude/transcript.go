package claude

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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// maxLine is the longest transcript line that is parsed. Longer lines (large
// tool outputs, API request dumps) are skipped without being held in memory.
const maxLine = 4 << 20

// turnWindow is how far back finished turns are kept.
const turnWindow = 7 * 24 * time.Hour

// Caps on what one transcript may contribute, so a hostile or runaway
// transcript cannot grow memory without bound.
const (
	maxPRs = 200
	// maxArtifacts counts addresses; only a publish takes a slot.
	maxArtifacts = 200
	// maxArtifactTitle caps what is kept of a title, in bytes; the core cuts
	// it again for the page.
	maxArtifactTitle = 1024
	maxTurns         = 5000
	maxSpawns        = 500
	maxPeers         = 200
	maxPending       = 1000
	maxIDsCall       = 32
)

// derived holds the facts read from one transcript. It never holds transcript
// text: only a model id, an effort level, a folder, times, pull request references, the
// addresses and titles of artifacts it published, the names and terminal ids
// of sessions this one spawned, the names of sessions it received messages
// from, the session's own name, and how it was started.
type derived struct {
	// Name is the session's name as Claude Code records it in an agent-name
	// line: the same name its record file holds while it runs. It is used
	// whenever the session's record has no name: always for a session that
	// ended (it has no record), and for a running one whose record lacks it.
	Name string
	// Entrypoint is how the session was started, as its lines say: "cli" for
	// an interactive session, "sdk-…" for claude -p and the SDKs. One
	// interactive line makes the session interactive, whatever the others
	// say: a session resumed with claude -p (or a -p run resumed by a person)
	// keeps the turns a person saw. claude -p writes no turn lines itself.
	Entrypoint         string
	Model              string
	Effort             string // the level the last main-chain reply ran at, one of model.EffortLevels; "" when it recorded none
	Folder             string
	LastActivity       time.Time
	TurnEnds           []time.Time
	CreatedPRs         []model.PRRef
	Artifacts          []model.ArtifactRef // one per address, in the order first seen
	SpawnedNames       []string
	SpawnedTerminalIDs []string
	PeerSenders        []string
}

// pendingKind says what a tool result tells; one command can do both.
type pendingKind uint8

const (
	pendingPR pendingKind = 1 << iota
	pendingSpawn
	pendingPublish // an Artifact publish
)

// tailer reads one transcript incrementally from a remembered offset. Only
// whole lines are consumed; a shrunk or replaced file is read again from zero.
type tailer struct {
	path    string
	off     int64
	info    os.FileInfo
	partial bool // the last update stopped before the end of the file
	d       derived
	pending map[string]pendingKind // tool-use id → what its result tells
	read    int64                  // bytes read by the last update (tests)
	// readCap, when set, makes the first read of a larger file start that
	// many bytes before its end, at the first whole line (skipFirst).
	readCap   int64
	skipFirst bool
	id        string // the session id its file is named by
	parsed    int    // lines read as JSON, from the start

	// The session's folder (see folder.go).
	home         string                 // the home folder, cleaned
	window       []string               // the last voteWindow voted folders, a ring
	wpos         int                    // the next slot in window
	held         map[string]*heldFolder // folders in the window
	seq          uint64                 // main-chain records with a folder, counted
	votesChanged bool
	pickedAt     time.Time          // when the folder was last chosen
	prober       *prober            // runs the folder lookups
	probing      chan []probeResult // a lookup batch still running
	probeUntil   time.Time          // how long this pass waits for lookups; zero: the prober's budget from now
}

func newTailer(path string, pr *prober) *tailer {
	home, _ := os.UserHomeDir()
	if home != "" {
		home = filepath.Clean(home)
	}
	return &tailer{path: path, id: strings.TrimSuffix(filepath.Base(path), ".jsonl"), pending: map[string]pendingKind{}, home: home, prober: pr}
}

func (t *tailer) reset() {
	t.off, t.skipFirst, t.parsed = 0, false, 0
	t.d = derived{}
	clear(t.pending)
	t.window, t.wpos, t.seq, t.probing = nil, 0, 0, nil
	clear(t.held)
}

// update reads what was appended since the last call. It reports whether the
// derived facts may have changed. A file whose size and modification time are
// unchanged is not opened. It stops early, at a line boundary, when ctx ends.
func (t *tailer) update(ctx context.Context, now time.Time) (changed bool, err error) {
	t.read = 0
	defer func() {
		changed = t.chooseFolder(now) || changed
	}()
	// A link is never read: a transcript is a regular file, and a link put
	// in its place could point anywhere.
	fi, err := os.Lstat(t.path)
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, errNotRegular
	}
	changed = false
	if t.info != nil && (!os.SameFile(t.info, fi) || fi.Size() < t.off) {
		t.reset()
		changed = true
	}
	if !changed && !t.partial && t.info != nil && fi.Size() == t.info.Size() && fi.ModTime().Equal(t.info.ModTime()) {
		return t.pruneTurns(now), nil
	}
	if t.info == nil && t.off == 0 && t.readCap > 0 && fi.Size() > t.readCap {
		t.off, t.skipFirst = fi.Size()-t.readCap, true
	}
	t.info, t.partial = fi, false
	if fi.Size() == t.off {
		return t.pruneTurns(now) || changed, nil
	}
	f, err := openNoFollow(t.path)
	if err != nil {
		return changed, err
	}
	defer func() { _ = f.Close() }()
	if ofi, err := f.Stat(); err != nil || !ofi.Mode().IsRegular() || !os.SameFile(ofi, fi) {
		return changed, errNotRegular // swapped since it was looked at
	}
	if _, err := f.Seek(t.off, io.SeekStart); err != nil {
		return changed, err
	}
	r := bufio.NewReaderSize(f, 256<<10)
	var long []byte // a line longer than the reader's buffer, while it fits maxLine
	skipping := false
	lineLen := int64(0)
	lines := 0
	for {
		if lines++; lines%ctxCheckLines == 0 && ctx.Err() != nil {
			t.partial = true // not read to the end: look again next time
			return changed, ctx.Err()
		}
		chunk, err := r.ReadSlice('\n')
		t.read += int64(len(chunk))
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
		case errors.Is(err, io.EOF):
			// An incomplete last line: leave it for the next update.
			return t.pruneTurns(now) || changed, nil
		case err != nil:
			return changed, err
		}
		if t.skipFirst { // the rest of a line cut by readCap
			skipping, long, t.skipFirst = true, nil, false
		}
		if !skipping {
			line := chunk
			if long != nil {
				if len(long)+len(chunk) <= maxLine {
					long = append(long, chunk...)
					line = long
				} else {
					line = nil
				}
			}
			if line != nil {
				t.consume(bytes.TrimSpace(line), now)
			}
		}
		t.off += lineLen
		lineLen, long, skipping = 0, nil, false
		changed = true
	}
}

var errNotRegular = errors.New("claude: transcript is not a regular file")

// ctxCheckLines is how often a long read checks whether it should stop.
const ctxCheckLines = 512

func (t *tailer) pruneTurns(now time.Time) bool {
	cut := now.Add(-turnWindow)
	i := 0
	for i < len(t.d.TurnEnds) && t.d.TurnEnds[i].Before(cut) {
		i++
	}
	if i == 0 {
		return false
	}
	t.d.TurnEnds = slices.Delete(t.d.TurnEnds, 0, i)
	return true
}

// lineHead is the part of every transcript line the adapter reads. Unknown
// fields are ignored.
type lineHead struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
	Sidechain bool   `json:"isSidechain"`
	Effort    string `json:"effort"` // on an assistant line: the effort level the reply ran at
	Entry     string `json:"entrypoint"`
	SessionID string `json:"sessionId"`
	AgentName string `json:"agentName"` // on an agent-name line
	Message   struct {
		Model string `json:"model"`
	} `json:"message"`
	Origin *struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"origin"`
}

// toolUseResultLine is the structured result Claude Code records beside a
// tool result. It is read only for an Artifact call's result.
type toolUseResultLine struct {
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

// artifactResult is the part of an Artifact result that is kept: the
// address and the title. The other keys only tell what kind of result it is.
type artifactResult struct {
	URL         string          `json:"url"`
	Title       string          `json:"title"`
	AssetUpload json.RawMessage `json:"asset_upload"`
	AssetDelete json.RawMessage `json:"asset_delete"`
	Opened      json.RawMessage `json:"opened"`
}

// lineContent is read only for lines that may carry a tool call or result.
type lineContent struct {
	Message struct {
		Content []json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// decode fills v and tolerates fields of an unexpected type: those are left
// empty and the rest is kept. Only broken JSON is an error.
func decode(line []byte, v any) bool {
	err := json.Unmarshal(line, v)
	var te *json.UnmarshalTypeError
	return err == nil || errors.As(err, &te)
}

func (t *tailer) consume(line []byte, now time.Time) {
	if len(line) == 0 || line[0] != '{' {
		return
	}
	var h lineHead
	if !decode(line, &h) {
		return
	}
	t.parsed++
	// A line that names another session (as Claude Code's own reader
	// checks) gives this one neither its name nor its turns.
	own := h.SessionID == "" || h.SessionID == t.id
	at := parseTS(h.Timestamp, now)
	if !at.IsZero() && at.After(t.d.LastActivity) {
		t.d.LastActivity = at
	}
	if !h.Sidechain && len(h.Cwd) <= 4096 {
		if f, ok := validFolder(h.Cwd); ok {
			t.vote(f)
		}
	}
	if own && !h.Sidechain && h.Entry != "" && len(h.Entry) <= 32 && (t.d.Entrypoint == "" || !interactive(t.d.Entrypoint)) {
		t.d.Entrypoint = h.Entry
	}
	switch h.Type {
	case "agent-name":
		if own && !h.Sidechain && h.AgentName != "" && len(h.AgentName) <= 1024 {
			t.d.Name = h.AgentName
		}
	case "assistant":
		// A sidechain line is an in-process subagent's: it may add pull
		// requests and spawns, never the session's own model, effort, turns or
		// messages. A reply with no effort level (none was sent, or an older
		// Claude Code) clears it, so a stale level is never kept.
		if m := h.Message.Model; !h.Sidechain && m != "" && m != "<synthetic>" && len(m) <= 128 {
			t.d.Model = m
			t.d.Effort = model.KnownEffort(h.Effort)
		}
		if bytes.Contains(line, []byte(`"tool_use"`)) &&
			(bytes.Contains(line, []byte("create")) || bytes.Contains(line, []byte("agtermctl")) ||
				bytes.Contains(line, []byte(`"Artifact"`))) {
			t.toolUses(line)
		}
	case "user":
		if !h.Sidechain && h.Origin != nil && h.Origin.Kind == "peer" && h.Origin.Name != "" && len(h.Origin.Name) <= 256 {
			t.d.PeerSenders = addCapped(t.d.PeerSenders, h.Origin.Name, maxPeers)
		}
		if len(t.pending) > 0 && bytes.Contains(line, []byte(`"tool_result"`)) {
			t.toolResults(line, at)
		}
	case "system":
		if own && !h.Sidechain && h.Subtype == "turn_duration" && !at.IsZero() && !at.Before(now.Add(-turnWindow)) {
			i, _ := slices.BinarySearchFunc(t.d.TurnEnds, at, time.Time.Compare)
			t.d.TurnEnds = slices.Insert(t.d.TurnEnds, i, at)
			if len(t.d.TurnEnds) > maxTurns { // keep the newest
				t.d.TurnEnds = slices.Delete(t.d.TurnEnds, 0, len(t.d.TurnEnds)-maxTurns)
			}
		}
	}
}

// prURLRe matches a pull request URL standing alone on a line, as
// `gh pr create` prints it (also after "already exists:").
var prURLRe = regexp.MustCompile(`(?m)^[ \t]*https://github\.com/([A-Za-z0-9][A-Za-z0-9-]{0,38})/([A-Za-z0-9._-]{1,100})/pull/([0-9]{1,9})[ \t]*\r?$`)

// toolUses records the calls whose results carry a fact: the session's own
// `gh pr create`, its `agtermctl session new` spawns, and its Artifact
// publishes.
func (t *tailer) toolUses(line []byte) {
	var c lineContent
	if !decode(line, &c) {
		return
	}
	for _, raw := range c.Message.Content {
		var b contentBlock
		if !decode(raw, &b) || b.Type != "tool_use" || b.ID == "" || len(b.ID) > 128 {
			continue
		}
		if b.Name == "Artifact" {
			if isPublish(b.Input) {
				t.addPending(b.ID, pendingPublish)
			}
			continue
		}
		if b.Name != "Bash" {
			continue
		}
		var in struct {
			Command string `json:"command"`
		}
		if !decode(b.Input, &in) {
			continue
		}
		cmd := parseCommand(in.Command)
		var kind pendingKind
		if len(cmd.runs("gh", "pr", "create")) > 0 {
			kind |= pendingPR
		}
		if len(cmd.runs("agtermctl", "session", "new")) > 0 {
			kind |= pendingSpawn
			for _, n := range cmd.spawnNames() {
				t.d.SpawnedNames = addCapped(t.d.SpawnedNames, n, maxSpawns)
			}
		}
		if kind == 0 {
			continue
		}
		t.addPending(b.ID, kind)
	}
}

func (t *tailer) addPending(id string, kind pendingKind) {
	if len(t.pending) >= maxPending {
		clear(t.pending)
	}
	t.pending[id] = kind
}

// isPublish says whether an Artifact call publishes: the action "publish" or
// none, and not an asset upload. Deletes are not followed: no real record of
// a whole artifact's delete has been seen, so a deleted artifact stays listed
// (its link opens claude.ai's own not-found page).
func isPublish(input json.RawMessage) bool {
	var in struct {
		Action string `json:"action"`
		Asset  bool   `json:"asset"`
	}
	return decode(input, &in) && (in.Action == "" || in.Action == "publish") && !in.Asset
}

func (t *tailer) toolResults(line []byte, at time.Time) {
	var c lineContent
	if !decode(line, &c) {
		return
	}
	results := 0
	for _, raw := range c.Message.Content {
		var b contentBlock
		if decode(raw, &b) && b.Type == "tool_result" {
			results++
		}
	}
	for _, raw := range c.Message.Content {
		var b contentBlock
		if !decode(raw, &b) || b.Type != "tool_result" {
			continue
		}
		kind, ok := t.pending[b.ToolUseID]
		if !ok {
			continue
		}
		delete(t.pending, b.ToolUseID)
		if kind == pendingPublish {
			// The line's toolUseResult belongs to its one result; with
			// several, it is not known whose it is, so nothing is shown.
			if !b.IsError && results == 1 {
				t.publishResult(line, at)
			}
			continue
		}
		text := resultText(b.Content)
		if kind&pendingPR != 0 {
			for _, m := range prURLRe.FindAllStringSubmatch(text, maxIDsCall) {
				n, err := strconv.Atoi(m[3])
				if err != nil || n <= 0 {
					continue
				}
				ref := model.PRRef{Repo: m[1] + "/" + m[2], Number: n}
				if !slices.Contains(t.d.CreatedPRs, ref) && len(t.d.CreatedPRs) < maxPRs {
					t.d.CreatedPRs = append(t.d.CreatedPRs, ref)
				}
			}
		}
		if kind&pendingSpawn != 0 {
			for _, id := range terminalIDs(text) {
				t.d.SpawnedTerminalIDs = addCapped(t.d.SpawnedTerminalIDs, id, maxSpawns)
			}
		}
	}
}

// publishResult records a successful publish. A user line carries one tool
// result, and its toolUseResult is that result's structured form: an object
// on success, a string on an error. Only an object with an artifact address
// counts.
func (t *tailer) publishResult(line []byte, at time.Time) {
	var l toolUseResultLine
	if !decode(line, &l) {
		return
	}
	raw := bytes.TrimSpace(l.ToolUseResult)
	if len(raw) == 0 || raw[0] != '{' {
		return
	}
	var r artifactResult
	if !decode(raw, &r) {
		return
	}
	if given(r.AssetUpload) || given(r.AssetDelete) || given(r.Opened) || !model.ValidArtifactURL(r.URL) {
		return
	}
	t.d.Artifacts = noteArtifact(t.d.Artifacts, model.ArtifactRef{URL: r.URL, Title: capTitle(r.Title), PublishedAt: at})
}

// given reports whether a key was present with a value other than null.
func given(raw json.RawMessage) bool {
	v := bytes.TrimSpace(raw)
	return len(v) > 0 && !bytes.Equal(v, []byte("null"))
}

// noteArtifact records a publish: within one transcript, a later line always
// wins. A new address beyond the cap is dropped.
func noteArtifact(list []model.ArtifactRef, r model.ArtifactRef) []model.ArtifactRef {
	for i := range list {
		if list[i].URL == r.URL {
			list[i] = r
			return list
		}
	}
	if len(list) >= maxArtifacts {
		return list
	}
	return append(list, r)
}

// mergeArtifacts adds another transcript's artifacts (a subagent's): for each
// address the later of the two wins; on a tie, or with no time, the one
// already there stays.
func mergeArtifacts(list, more []model.ArtifactRef) []model.ArtifactRef {
	for _, r := range more {
		i := slices.IndexFunc(list, func(x model.ArtifactRef) bool { return x.URL == r.URL })
		switch {
		case i >= 0:
			if r.PublishedAt.After(list[i].PublishedAt) {
				list[i] = r
			}
		case len(list) < maxArtifacts:
			list = append(list, r)
		}
	}
	return list
}

// capTitle keeps at most maxArtifactTitle bytes, cut on a rune boundary.
func capTitle(s string) string {
	if len(s) <= maxArtifactTitle {
		return s
	}
	s = s[:maxArtifactTitle]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// resultText returns a tool result's text, which is a string or a list of
// text blocks. It is matched against and dropped, never kept.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, x := range blocks {
		if x.Type == "text" {
			b.WriteString(x.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func addCapped(list []string, v string, limit int) []string {
	if slices.Contains(list, v) || len(list) >= limit {
		return list
	}
	return append(list, v)
}

// parseTS reads a record time. Times more than a day in the future are
// treated as unknown rather than trusted.
func parseTS(s string, now time.Time) time.Time {
	if s == "" {
		return time.Time{}
	}
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || at.After(now.Add(24*time.Hour)) {
		return time.Time{}
	}
	return at
}
