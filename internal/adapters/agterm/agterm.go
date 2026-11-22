// Package agterm is the agterm source adapter and the jump target.
//
// It reads agterm through agtermctl (tested with agterm 0.41.1):
//
//	agtermctl version --json                      Probe
//	agtermctl window list --json                  the windows; only open ones are read
//	agtermctl tree --json --window <window id>    workspaces and sessions of one window
//	agtermctl events --json --kind <kinds>        live events, one JSON object per line
//	agtermctl session select --target <id> --window <window id>   jump
//	agtermctl window select <window id>                           jump: raise the window
//
// Every call takes an argument list, never a shell string. Ids are checked
// against the UUID shape before they are passed on. The events stream carries
// no cursor or run id in its JSON lines, so a reconnect cannot resume where it
// stopped: every (re)connect subscribes from now and reloads the whole tree,
// which is the current truth.
package agterm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// Options configure the adapter.
type Options struct {
	Bin string // agtermctl binary; "" means look it up on PATH
}

const (
	callTimeout = 5 * time.Second
	maxBackoff  = 30 * time.Second
	minBackoff  = time.Second
	// A stream that lived this long was healthy: the next retry starts fast again.
	healthyStream = 30 * time.Second
)

// testedVersions are the agterm versions this adapter was run against.
var testedVersions = []string{"0.41.1"}

// eventKinds are the agterm events the adapter listens to.
const eventKinds = "status,session.created,session.closed,session.selected,tree.changed,app.launched"

var uuidRe = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// streamFunc starts a long-running agtermctl and returns its standard output
// and a function that waits for it to end.
type streamFunc func(ctx context.Context, name string, args ...string) (io.ReadCloser, func() error, error)

// Adapter watches agterm and focuses its sessions. It implements
// source.Watcher and source.Jumper.
type Adapter struct {
	opts   Options
	bin    string
	run    runner.Func
	stream streamFunc
	found  func(string) string

	minBackoff, maxBackoff, debounce time.Duration

	mu      sync.Mutex
	treeBad bool // the last tree read could not be parsed
}

// New returns the agterm adapter.
func New(opts Options) *Adapter {
	bin := opts.Bin
	if bin == "" {
		bin = "agtermctl"
	}
	return &Adapter{
		opts: opts, bin: bin, run: runner.Exec(callTimeout), stream: execStream, found: runner.Found,
		minBackoff: minBackoff, maxBackoff: maxBackoff, debounce: 150 * time.Millisecond,
	}
}

func (a *Adapter) Kind() source.Kind { return source.KindAgterm }

func (a *Adapter) Probe(ctx context.Context) source.Status {
	st := source.Status{Kind: source.KindAgterm}
	if a.found(a.bin) == "" {
		st.State, st.Detail = source.StateMissing, "agterm not found"
		return st
	}
	out, err := a.run(ctx, a.bin, "version", "--json")
	if err != nil {
		st.State, st.Detail = source.StateFailing, "agterm is not running"
		return st
	}
	var v struct {
		Result struct {
			App struct {
				Version string `json:"version"`
			} `json:"app"`
		} `json:"result"`
	}
	if json.Unmarshal(out, &v) == nil {
		st.Version = v.Result.App.Version
		st.Tested = st.Version != "" && slices.Contains(testedVersions, st.Version)
	}
	st.State = source.StateOK
	a.mu.Lock()
	bad := a.treeBad
	a.mu.Unlock()
	if bad {
		st.State, st.Detail = source.StateFailing, "session tree not readable"
	}
	return st
}

// term is one agterm session as last read.
type term struct {
	fact     model.TerminalFact
	window   string
	fgAgent  bool // its foreground program is an agent tool
	statusOn bool // it shows a status mark other than idle

	selectedAt time.Time // when the owner last selected it (agterm keeps this across restarts)
}

// sameView reports whether two reads of a terminal give the same fact.
func (t term) sameView(o term) bool {
	return t.fact == o.fact && t.window == o.window && t.fgAgent == o.fgAgent && t.statusOn == o.statusOn
}

func (t *term) refresh() { t.fact.HasAgent = t.fgAgent || t.statusOn }

func mapStatus(s string) model.Known[model.Status] {
	switch s {
	case "active":
		return model.Of(model.StatusBusy)
	case "blocked":
		return model.Of(model.StatusBlocked)
	case "completed", "idle":
		return model.Of(model.StatusFinished)
	}
	return model.Unknown[model.Status]()
}

// agentTools are the foreground programs that make a session an agent session.
var agentTools = []string{"claude", "codex"}

type treeSession struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Title          string   `json:"title"`
	Cwd            string   `json:"cwd"`
	Status         *string  `json:"status"`
	Foreground     []string `json:"foreground"`
	LastSelectedAt float64  `json:"lastSelectedAt"` // seconds since the epoch; 0 when never
}

// loadTree reads every open window's tree. Only the first word of a session's
// foreground command is looked at, to tell an agent from a plain shell; the
// rest of the command line can hold a prompt and is dropped.
func (a *Adapter) loadTree(ctx context.Context) (map[string]term, error) {
	out, err := a.run(ctx, a.bin, "window", "list", "--json")
	if err != nil {
		return nil, fmt.Errorf("agterm: window list: %w", err)
	}
	var wl struct {
		OK     bool `json:"ok"`
		Result struct {
			Windows []struct {
				ID   string `json:"id"`
				Open bool   `json:"open"`
			} `json:"windows"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &wl); err != nil || !wl.OK {
		a.setTreeBad(true)
		return nil, errors.New("agterm: window list not readable")
	}
	terms := map[string]term{}
	for _, w := range wl.Result.Windows {
		if !w.Open || !uuidRe.MatchString(w.ID) {
			continue
		}
		out, err := a.run(ctx, a.bin, "tree", "--json", "--window", w.ID)
		if err != nil {
			return nil, fmt.Errorf("agterm: tree: %w", err)
		}
		if err := parseTree(out, w.ID, terms); err != nil {
			a.setTreeBad(true)
			return nil, err
		}
	}
	a.setTreeBad(false)
	return terms, nil
}

func (a *Adapter) setTreeBad(bad bool) {
	a.mu.Lock()
	a.treeBad = bad
	a.mu.Unlock()
}

func parseTree(out []byte, window string, terms map[string]term) error {
	var tr struct {
		OK     bool `json:"ok"`
		Result struct {
			Tree struct {
				Workspaces []struct {
					Name     string            `json:"name"`
					Sessions []json.RawMessage `json:"sessions"`
				} `json:"workspaces"`
			} `json:"tree"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &tr); err != nil || !tr.OK {
		return errors.New("agterm: tree not readable")
	}
	for _, ws := range tr.Result.Tree.Workspaces {
		for _, raw := range ws.Sessions {
			var s treeSession
			if json.Unmarshal(raw, &s) != nil || !uuidRe.MatchString(s.ID) {
				continue // one bad session never hides the others
			}
			t := term{
				fact: model.TerminalFact{
					ID: strings.ToUpper(s.ID), Workspace: ws.Name, Name: s.Name, Title: s.Title,
					Folder: s.Cwd, Status: model.Unknown[model.Status](),
				},
				window: window,
			}
			if !filepath.IsAbs(t.fact.Folder) {
				t.fact.Folder = ""
			}
			if s.Status != nil {
				t.fact.Status = mapStatus(*s.Status)
				t.statusOn = *s.Status != "" && *s.Status != "idle"
			}
			t.selectedAt = fromUnix(s.LastSelectedAt)
			if len(s.Foreground) > 0 {
				t.fgAgent = slices.Contains(agentTools, filepath.Base(s.Foreground[0]))
			}
			t.refresh()
			terms[t.fact.ID] = t
		}
	}
	return nil
}

// Run emits the current terminals, then every change, until ctx ends. All
// emits happen on this goroutine, so facts for one id never swap.
func (a *Adapter) Run(ctx context.Context, emit func(model.Fact)) error {
	sentReady := false
	ready := func() {
		if !sentReady {
			sentReady = true
			emit(model.SourceReady{Kind: string(source.KindAgterm)})
		}
	}
	if a.found(a.bin) == "" {
		ready()
		return nil
	}
	cur := map[string]term{}
	backoff := a.minBackoff
	for ctx.Err() == nil {
		started := time.Now()
		a.follow(ctx, emit, cur, ready)
		if ctx.Err() != nil {
			break
		}
		ready() // agterm not running: there was nothing to read
		if time.Since(started) > healthyStream {
			backoff = a.minBackoff
		}
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
		backoff = min(backoff*2, a.maxBackoff)
	}
	return nil
}

type event struct {
	Kind    string  `json:"kind"`
	Session string  `json:"session"`
	Ts      float64 `json:"ts"`
	Payload struct {
		Status *string `json:"status"`
	} `json:"payload"`
}

// follow subscribes to the event stream, reloads the tree, calls ready, and
// applies events until the stream ends or ctx is done.
func (a *Adapter) follow(ctx context.Context, emit func(model.Fact), cur map[string]term, ready func()) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rc, wait, err := a.stream(sctx, a.bin, "events", "--json", "--kind", eventKinds)
	if err != nil {
		a.reload(sctx, emit, cur) // the tree may still be readable
		return
	}
	lines := make(chan []byte)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(rc)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			select {
			case lines <- slices.Clone(sc.Bytes()):
			case <-sctx.Done():
				return
			}
		}
	}()
	defer func() {
		cancel()
		_ = rc.Close()
		for range lines { // drain so the reader ends
		}
		_ = wait()
	}()

	a.reload(sctx, emit, cur)
	ready()
	var reloadC <-chan time.Time
	schedule := func() {
		if reloadC == nil {
			reloadC = time.After(a.debounce)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-reloadC:
			reloadC = nil
			a.reload(sctx, emit, cur)
		case line, ok := <-lines:
			if !ok {
				return
			}
			var ev event
			if json.Unmarshal(line, &ev) != nil {
				continue
			}
			id := strings.ToUpper(ev.Session)
			switch ev.Kind {
			case "status":
				t, known := cur[id]
				if !known || ev.Payload.Status == nil {
					schedule()
					continue
				}
				t.fact.Status = mapStatus(*ev.Payload.Status)
				t.statusOn = *ev.Payload.Status != "" && *ev.Payload.Status != "idle"
				t.refresh()
				if !t.sameView(cur[id]) {
					cur[id] = t
					emit(t.fact)
				}
			case "session.closed":
				if _, known := cur[id]; known {
					delete(cur, id)
					emit(model.TerminalGone{ID: id})
				}
			case "session.selected":
				if uuidRe.MatchString(id) {
					at := fromUnix(ev.Ts)
					if t, known := cur[id]; known && at.After(t.selectedAt) {
						t.selectedAt = at
						cur[id] = t
					}
					emit(model.TerminalSelected{ID: id, At: at})
				}
			default: // session.created, tree.changed, app.launched
				schedule()
			}
		}
	}
}

// reload reads the tree and emits what changed. A failed read keeps the
// current view: a transient error must not empty the board.
func (a *Adapter) reload(ctx context.Context, emit func(model.Fact), cur map[string]term) {
	next, err := a.loadTree(ctx)
	if err != nil {
		return
	}
	ids := make([]string, 0, len(next))
	for id := range next {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if old, ok := cur[id]; !ok || !old.sameView(next[id]) {
			emit(next[id].fact)
		}
	}
	// The last selection agterm remembers, so a session the owner looked at
	// before sitboard started (or while the stream was down) counts as seen.
	for _, id := range ids {
		at := next[id].selectedAt
		if old, ok := cur[id]; !at.IsZero() && (!ok || at.After(old.selectedAt)) {
			emit(model.TerminalSelected{ID: id, At: at})
		} else if ok && old.selectedAt.After(at) {
			t := next[id]
			t.selectedAt = old.selectedAt // keep the newer time from an event
			next[id] = t
		}
	}
	gone := make([]string, 0)
	for id := range cur {
		if _, ok := next[id]; !ok {
			gone = append(gone, id)
		}
	}
	slices.Sort(gone)
	for _, id := range gone {
		emit(model.TerminalGone{ID: id})
	}
	clear(cur)
	for id, t := range next {
		cur[id] = t
	}
}

func fromUnix(ts float64) time.Time {
	if ts <= 0 || math.IsNaN(ts) || math.IsInf(ts, 0) || ts > 1e11 {
		return time.Time{}
	}
	sec, frac := math.Modf(ts)
	return time.Unix(int64(sec), int64(frac*1e9))
}

// Jump focuses the agterm session target.ID. It accepts only an agterm
// target whose id exists in the current agterm tree, read fresh for this call.
func (a *Adapter) Jump(ctx context.Context, target model.FocusTarget) error {
	terminalID := target.ID
	if target.Kind != model.FocusAgterm || !uuidRe.MatchString(terminalID) {
		return source.ErrUnknownTerminal
	}
	if a.found(a.bin) == "" {
		return source.ErrNotInstalled
	}
	terms, err := a.loadTree(ctx)
	if err != nil {
		return err
	}
	t, ok := terms[strings.ToUpper(terminalID)]
	if !ok {
		return source.ErrUnknownTerminal
	}
	if _, err := a.run(ctx, a.bin, "session", "select", "--target", t.fact.ID, "--window", t.window); err != nil {
		return fmt.Errorf("agterm: select: %w", err)
	}
	if _, err := a.run(ctx, a.bin, "window", "select", t.window); err != nil {
		return fmt.Errorf("agterm: raise window: %w", err)
	}
	return nil
}

func execStream(ctx context.Context, name string, args ...string) (io.ReadCloser, func() error, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return out, cmd.Wait, nil
}
