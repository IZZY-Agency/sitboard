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
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/shellcmd"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

// maxLine is the longest transcript line that is parsed. Longer lines (large
// tool outputs, API request dumps) are skipped without being held in memory.
const maxLine = 4 << 20

// turnWindow is how far back finished turns are kept.
const turnWindow = 7 * 24 * time.Hour

// spendKeep is how far back spans are kept: a day more than the 7 days the
// board shows, so the first day shown is complete whatever the length of the
// days in between (a 25-hour day); the board cuts at its local midnight.
const spendKeep = 8 * 24 * time.Hour

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
	spend              *spendData // with the subagents', counted, not priced; set by the indexer
	CreatedPRs         []model.PRRef
	PushedPRs          []model.PushedRef   // those of CreatedPRs a creating push announced, with the call's window
	TouchedIssues      []model.IssueTouch  // the issues the session's own gh/glab issue commands named
	Artifacts          []model.ArtifactRef // one per address, in the order first seen
	SpawnedNames       []string
	SpawnedTerminalIDs []string
	PeerSenders        []string
}

// pendingKind says what a tool result tells; one command can do both.
type pendingKind uint16

const (
	pendingPR pendingKind = 1 << iota
	pendingSpawn
	pendingPublish // an Artifact publish
	pendingPRMixed // with pendingPR: the call ran other gh commands too (see shellcmd.OwnPullRequests)
	pendingPRFirst // with pendingPRMixed: the create was the call's first gh command
	pendingPRLoose // with pendingPR: other gh commands too, but their order against the create is not known (see shellcmd.UnorderedPullRequests)
	pendingMRGlab  // `glab mr create`: its result may hold a bare merge request address
	pendingMRPush  // `git push -o merge_request.create`: its result may hold a "remote:" address; the call's time is the start of its window
	pendingMRMixed // with pendingMRGlab: the call ran other commands too (see shellcmd.MRCreation.Mixed)
	pendingMRFirst // with pendingMRMixed: a `glab mr create` was the call's first command that may print an address
)

// pendingCall is a call whose result is still to come: what the result tells,
// and when the call was made (the start of a creating push's window). One
// entry, so the two are evicted together.
type pendingCall struct {
	kind pendingKind
	at   time.Time
}

// tailer reads one transcript incrementally from a remembered offset. Only
// whole lines are consumed; a shrunk or replaced file is read again from zero.
type tailer struct {
	path     string
	off      int64
	info     os.FileInfo
	partial  bool // the last update stopped before the end of the file
	d        derived
	pending  map[string]pendingCall // tool-use id → what its result tells
	read     int64                  // bytes read by the last update (tests)
	headRead int64                  // bytes read for spend alone, from the start (tests)
	// readCap, when set, makes the first read of a larger file start that
	// many bytes before its end, at the first whole line (skipFirst).
	readCap   int64
	skipFirst bool
	// headPos and headEnd: the part before the capped read, still to be read
	// for spend alone, from headPos.
	headPos, headEnd int64
	id               string // the session id its file is named by
	// usageOwner is the session whose lines count for spend: its own id, or
	// for a subagent's transcript the session it runs in.
	usageOwner string
	spend      spendLog
	// claims makes a reply count once across the session's transcripts:
	// owner is this transcript's number in it.
	claims *replyClaims
	owner  uint32
	// tab is the price table of this pass, for the tier of each reply counted.
	tab    priceTable
	parsed int // lines read as JSON, from the start

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
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	return &tailer{path: path, id: id, usageOwner: id, claims: newReplyClaims(), pending: map[string]pendingCall{}, home: home, prober: pr}
}

func (t *tailer) reset() {
	t.release()
	t.off, t.skipFirst, t.parsed, t.headPos, t.headEnd = 0, false, 0, 0, 0
	t.d = derived{}
	t.spend.reset()
	clear(t.pending)
	t.window, t.wpos, t.seq, t.probing = nil, 0, 0, nil
	clear(t.held)
}

// update reads what was appended since the last call. It reports whether the
// derived facts may have changed. A file whose size and modification time are
// unchanged is not opened. It stops early, at a line boundary, when ctx ends.
// release gives up what this transcript counted, for the other transcripts
// of the session to count.
func (t *tailer) release() {
	if t.claims != nil {
		t.claims.release(t.owner)
	}
}

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
		t.headPos, t.headEnd = 0, t.off+1 // the line that holds t.off is the head's
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
	t.spend.cut = now.Add(-spendKeep).Truncate(model.SpendSpanLength)
	if t.headPos < t.headEnd {
		if err := t.scanHead(ctx, f, now); err != nil {
			return changed, err
		}
		changed = true
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
	pruned := t.spend.prune(now.Add(-spendKeep).Truncate(model.SpendSpanLength))
	i := 0
	for i < len(t.d.TurnEnds) && t.d.TurnEnds[i].Before(cut) {
		i++
	}
	if i == 0 {
		return pruned
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
	// ForkedFrom marks a line copied from a parent conversation into a fork's
	// transcript (as read from Claude Code's code, not yet seen in a real
	// fork: see forked).
	ForkedFrom json.RawMessage `json:"forkedFrom"`
	Message    struct {
		ID    string     `json:"id"`
		Model string     `json:"model"`
		Usage *usageJSON `json:"usage"` // on an assistant line: the reply's token counts
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

// forked reports whether the line is a copy of a parent conversation's line.
// Its usage is counted in the parent's transcript, so it is never counted for
// the fork. NEEDS CONFIRMING AGAINST A REAL FORK: the shape (a forkedFrom key
// with an object, and the fork's own session id on the copied line) comes from
// reading Claude Code 2.1.330, no transcript on this machine has one.
func (h *lineHead) forked() bool {
	f := bytes.TrimSpace(h.ForkedFrom)
	return len(f) > 0 && !bytes.Equal(f, []byte("null"))
}

// spendReply counts an assistant line's usage.
func (t *tailer) spendReply(h *lineHead, at, now time.Time) {
	if m := h.Message.Model; h.Message.Usage != nil && m != "" && m != "<synthetic>" && len(m) <= 128 && len(h.Message.ID) <= 256 &&
		(h.SessionID == "" || h.SessionID == t.usageOwner) && !h.forked() {
		id := h.Message.ID
		if id == "" {
			t.spend.reply(m, h.Message.Usage, at, t.tab)
			return
		}
		c, fresh := t.claims.take(id, t.owner)
		if !fresh { // counted already, here or in another transcript of the session
			c.grow(h.Message.Usage.usage(m))
			return
		}
		c.counted = t.spend.reply(m, h.Message.Usage, at, t.tab)
	}
}

// spendCost keeps a cost-state line.
func (t *tailer) spendCost(h *lineHead, line []byte, own bool) {
	var c costStateLine
	if own && !t.spend.sub && !h.forked() && decode(line, &c) {
		t.spend.costState(&c)
	}
}

// consumeSpend reads one line for spend alone: the part of a large
// transcript that is not read in full (see endedReadCap).
func (t *tailer) consumeSpend(line []byte, now time.Time) {
	isReply := bytes.Contains(line, []byte(`"usage"`))
	if len(line) == 0 || line[0] != '{' || (!isReply && !bytes.Contains(line, []byte(`"cost-state"`))) {
		return
	}
	var h lineHead
	if !decode(line, &h) {
		return
	}
	switch h.Type {
	case "assistant":
		t.spendReply(&h, parseTS(h.Timestamp, now), now)
	case "cost-state":
		t.spendCost(&h, line, h.SessionID == "" || h.SessionID == t.id)
	}
}

// scanHead reads the part of the file before the last readCap bytes for spend
// alone, so a huge transcript's days and total are whole. It runs before the
// rest is read (the cost-state line and the replies after it come later in the
// file) and can be stopped by ctx and go on from headPos.
func (t *tailer) scanHead(ctx context.Context, f *os.File, now time.Time) error {
	if _, err := f.Seek(t.headPos, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReaderSize(f, 256<<10)
	var long []byte
	skipping := false
	lineLen := int64(0)
	lines := 0
	for t.headPos < t.headEnd {
		if lines++; lines%ctxCheckLines == 0 && ctx.Err() != nil {
			t.partial = true
			return ctx.Err()
		}
		chunk, err := r.ReadSlice('\n')
		t.headRead += int64(len(chunk))
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
		case err != nil: // the file got shorter: nothing more to read there
			t.headPos = t.headEnd
			return nil
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
				t.consumeSpend(bytes.TrimSpace(line), now)
			}
		}
		t.headPos += lineLen
		lineLen, long, skipping = 0, nil, false
	}
	return nil
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
		t.spendReply(&h, at, now)
		toolUse := bytes.Contains(line, []byte(`"tool_use"`))
		prGate := toolUse && (bytes.Contains(line, []byte("create")) || bytes.Contains(line, []byte("agtermctl")) ||
			bytes.Contains(line, []byte(`"Artifact"`)))
		// The issues a call names are read from its decoded command, so the
		// gate is only a cheap filter for a Bash call that mentions gh or
		// glab. The line must be the session's own, as for spend: not a line
		// of another session, and not one copied into a fork.
		issueGate := toolUse && bytes.Contains(line, []byte("Bash")) &&
			(bytes.Contains(line, []byte("gh")) || bytes.Contains(line, []byte("glab"))) &&
			(h.SessionID == "" || h.SessionID == t.usageOwner) && !h.forked()
		if prGate || issueGate {
			t.toolUses(line, at, h.Cwd, prGate, issueGate)
		}
	case "user":
		if !h.Sidechain && h.Origin != nil && h.Origin.Kind == "peer" && h.Origin.Name != "" && len(h.Origin.Name) <= 256 {
			t.d.PeerSenders = addCapped(t.d.PeerSenders, h.Origin.Name, maxPeers)
		}
		if len(t.pending) > 0 && bytes.Contains(line, []byte(`"tool_result"`)) {
			t.toolResults(line, at)
		}
	case "cost-state":
		t.spendCost(&h, line, own)
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

// toolUses records the calls whose results carry a fact: the session's own
// `gh pr create`, its `agtermctl session new` spawns, and its Artifact
// publishes. A call's command line alone carries the issues its `gh issue`
// and `glab issue` commands name (touches): they need no result, and cwd is
// the folder the line says the call ran in.
//
// prGate and issueGate say which of the two a line is read for: a line the
// issue gate alone let in is read for touches only, so the pull request,
// spawn and artifact rules see exactly the lines they always did.
func (t *tailer) toolUses(line []byte, at time.Time, cwd string, prGate, issueGate bool) {
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
			if prGate && isPublish(b.Input) { // a line only the issue gate let in is no publish's
				t.addPending(b.ID, pendingPublish, at)
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
		cmd := shellcmd.Parse(in.Command)
		if issueGate {
			if cmds := cmd.IssueCommands(); len(cmds) > 0 {
				folder := ""
				if len(cwd) <= 4096 {
					folder, _ = validFolder(cwd)
				}
				t.d.TouchedIssues = shellcmd.AddIssueTouches(t.d.TouchedIssues, cmds, folder, at, maxPRs)
			}
		}
		if !prGate {
			continue
		}
		var kind pendingKind
		if len(cmd.Runs("gh", "pr", "create")) > 0 {
			kind |= pendingPR
			if cmd.CreatesAmongOtherGH() {
				kind |= pendingPRMixed
				if cmd.CreateIsFirstGH() {
					kind |= pendingPRFirst
				}
			} else if cmd.RunsOtherGH() {
				kind |= pendingPRLoose
			}
		}
		if k := cmd.MRCreation(); k.Any() {
			if k.Glab {
				kind |= pendingMRGlab
			}
			if k.Push {
				kind |= pendingMRPush
			}
			if k.Mixed {
				kind |= pendingMRMixed
			}
			if k.First {
				kind |= pendingMRFirst
			}
		}
		if len(cmd.Runs("agtermctl", "session", "new")) > 0 {
			kind |= pendingSpawn
			for _, n := range cmd.SpawnNames() {
				t.d.SpawnedNames = addCapped(t.d.SpawnedNames, n, maxSpawns)
			}
		}
		if kind == 0 {
			continue
		}
		t.addPending(b.ID, kind, at)
	}
}

func (t *tailer) addPending(id string, kind pendingKind, at time.Time) {
	if len(t.pending) >= maxPending {
		clear(t.pending)
	}
	t.pending[id] = pendingCall{kind, at}
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
		call, ok := t.pending[b.ToolUseID]
		kind, from := call.kind, call.at
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
			refs := shellcmd.PullRequests(text)
			switch {
			case kind&pendingPRMixed != 0:
				refs = shellcmd.OwnPullRequests(text, kind&pendingPRFirst != 0)
			case kind&pendingPRLoose != 0:
				refs = shellcmd.UnorderedPullRequests(text)
			}
			for _, ref := range refs {
				if !slices.Contains(t.d.CreatedPRs, ref) && shellcmd.Room(t.d.CreatedPRs, ref, maxPRs) {
					t.d.CreatedPRs = append(t.d.CreatedPRs, ref)
				}
			}
		}
		if kind&(pendingMRGlab|pendingMRPush) != 0 {
			k := shellcmd.MRCreation{Glab: kind&pendingMRGlab != 0, Push: kind&pendingMRPush != 0, Mixed: kind&pendingMRMixed != 0, First: kind&pendingMRFirst != 0}
			t.d.CreatedPRs, t.d.PushedPRs = shellcmd.AddMergeRequests(t.d.CreatedPRs, t.d.PushedPRs, shellcmd.MergeRequests(text, k), from, at, maxPRs)
		}
		if kind&pendingSpawn != 0 {
			for _, id := range shellcmd.TerminalIDs(text) {
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
