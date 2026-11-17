package agterm

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

const (
	idCoord  = "11111111-1111-4111-8111-111111111111"
	idWorker = "22222222-2222-4222-8222-222222222222"
	idShell  = "33333333-3333-4333-8333-333333333333"
	idCodex  = "44444444-4444-4444-8444-444444444444"
	idDone   = "55555555-5555-4555-8555-555555555555"
	idSolo   = "66666666-6666-4666-8666-666666666666"
	winA     = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "fixtures", "agterm", "0.41.1", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeCtl answers agtermctl one-shot calls from fixtures and records them.
type fakeCtl struct {
	mu    sync.Mutex
	calls [][]string
	tree  []byte
	fail  error
}

func (f *fakeCtl) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, slices.Clone(args))
	if f.fail != nil {
		return nil, f.fail
	}
	switch args[0] {
	case "version":
		return os.ReadFile(filepath.Join("..", "..", "..", "testdata", "fixtures", "agterm", "0.41.1", "version.json"))
	case "window":
		if args[1] == "list" {
			return os.ReadFile(filepath.Join("..", "..", "..", "testdata", "fixtures", "agterm", "0.41.1", "window-list.json"))
		}
		return []byte(`{"ok":true}`), nil
	case "tree":
		return f.tree, nil
	case "session":
		return []byte(`{"ok":true}`), nil
	}
	return nil, errors.New("unexpected call")
}

func (f *fakeCtl) setTree(b []byte) {
	f.mu.Lock()
	f.tree = b
	f.mu.Unlock()
}

func (f *fakeCtl) count(prefix ...string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if len(c) >= len(prefix) && slices.Equal(c[:len(prefix)], prefix) {
			n++
		}
	}
	return n
}

func newTest(t *testing.T, f *fakeCtl) *Adapter {
	a := New(Options{Bin: "agtermctl"})
	a.run = f.run
	a.found = func(string) string { return "/usr/bin/agtermctl" }
	return a
}

func TestProbeMissingWhenBinaryAbsent(t *testing.T) {
	st := New(Options{Bin: filepath.Join(t.TempDir(), "agtermctl")}).Probe(context.Background())
	if st.State != source.StateMissing || st.Kind != source.KindAgterm {
		t.Fatalf("Probe = %+v", st)
	}
	if err := New(Options{Bin: filepath.Join(t.TempDir(), "agtermctl")}).Run(context.Background(), func(model.Fact) {}); err != nil {
		t.Fatalf("Run without agterm = %v, want nil", err)
	}
}

func TestProbeMarksTestedVersions(t *testing.T) {
	f := &fakeCtl{tree: fixture(t, "tree.json")}
	st := newTest(t, f).Probe(context.Background())
	if st.State != source.StateOK || st.Version != "0.41.1" || !st.Tested {
		t.Fatalf("Probe = %+v", st)
	}
	saved := testedVersions
	testedVersions = []string{"0.0.1"}
	defer func() { testedVersions = saved }()
	if st := newTest(t, f).Probe(context.Background()); st.Tested {
		t.Fatalf("untested version marked tested: %+v", st)
	}
}

func TestProbeFailingWhenAppNotRunning(t *testing.T) {
	f := &fakeCtl{fail: &runner.ExitError{Code: 1}}
	if st := newTest(t, f).Probe(context.Background()); st.State != source.StateFailing {
		t.Fatalf("Probe = %+v", st)
	}
}

func TestTreeToTerminalFacts(t *testing.T) {
	f := &fakeCtl{tree: fixture(t, "tree.json")}
	terms, err := newTest(t, f).loadTree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.count("tree", "--json", "--window", winA) != 1 || f.count("tree") != 1 {
		t.Fatalf("tree must be read once per open window: %v", f.calls)
	}
	if _, ok := terms["not-a-uuid"]; ok {
		t.Fatal("a session with a malformed id was kept")
	}
	if len(terms) != 6 {
		t.Fatalf("got %d terminals", len(terms))
	}
	c := terms[idCoord]
	want := model.TerminalFact{ID: idCoord, Workspace: "alpha", Name: "coordinator-a", Title: "coordinator-a",
		Folder: "/home/dev/projects/alpha", Status: model.Of(model.StatusBusy), HasAgent: true}
	if c.fact != want || c.window != winA {
		t.Fatalf("coordinator = %+v (window %q)", c.fact, c.window)
	}
	statuses := map[string]model.Known[model.Status]{
		idWorker: model.Unknown[model.Status](),
		idCodex:  model.Of(model.StatusBlocked),
		idDone:   model.Of(model.StatusFinished),
		idSolo:   model.Of(model.StatusFinished),
	}
	for id, st := range statuses {
		if terms[id].fact.Status != st {
			t.Errorf("%s status = %v, want %v", id, terms[id].fact.Status, st)
		}
	}
	if terms[idDone].fact.Workspace != "beta" {
		t.Errorf("workspace = %q", terms[idDone].fact.Workspace)
	}
}

func TestHasAgentFalseForPlainShell(t *testing.T) {
	f := &fakeCtl{tree: fixture(t, "tree.json")}
	terms, _ := newTest(t, f).loadTree(context.Background())
	for id, want := range map[string]bool{idShell: false, idSolo: false, idWorker: true, idCodex: true, idCoord: true} {
		if terms[id].fact.HasAgent != want {
			t.Errorf("%s HasAgent = %v, want %v", id, terms[id].fact.HasAgent, want)
		}
	}
}

func TestMalformedTreeReportsFailing(t *testing.T) {
	f := &fakeCtl{tree: []byte(`{"ok":true,"result":{"tree":{"workspaces":[{"sessions":"oops"`)}
	a := newTest(t, f)
	if _, err := a.loadTree(context.Background()); err == nil {
		t.Fatal("want an error for a malformed tree")
	}
	if st := a.Probe(context.Background()); st.State != source.StateFailing {
		t.Fatalf("Probe after a malformed tree = %+v", st)
	}
}

// collector gathers emitted facts.
type collector struct {
	mu    sync.Mutex
	facts []model.Fact
}

func (c *collector) emit(f model.Fact) {
	c.mu.Lock()
	c.facts = append(c.facts, f)
	c.mu.Unlock()
}

func (c *collector) snapshot() []model.Fact {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.facts)
}

func (c *collector) waitFor(t *testing.T, what string, ok func(model.Fact) bool) model.Fact {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, f := range c.snapshot() {
			if ok(f) {
				return f
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; got %d facts", what, len(c.snapshot()))
	return nil
}

// fakeStream hands out event streams the test writes into.
type fakeStream struct {
	mu      sync.Mutex
	starts  int
	writers chan *io.PipeWriter
	args    [][]string
}

func newFakeStream() *fakeStream { return &fakeStream{writers: make(chan *io.PipeWriter, 16)} }

func (s *fakeStream) open(ctx context.Context, _ string, args ...string) (io.ReadCloser, func() error, error) {
	s.mu.Lock()
	s.starts++
	s.args = append(s.args, args)
	s.mu.Unlock()
	r, w := io.Pipe()
	s.writers <- w
	go func() { <-ctx.Done(); _ = w.Close() }()
	return r, func() error { return nil }, nil
}

func startRun(t *testing.T, f *fakeCtl) (*Adapter, *collector, *fakeStream, context.CancelFunc, chan error) {
	t.Helper()
	a := newTest(t, f)
	s := newFakeStream()
	a.stream = s.open
	a.minBackoff = 10 * time.Millisecond
	a.debounce = 5 * time.Millisecond
	c := &collector{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, c.emit) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run = %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	return a, c, s, cancel, done
}

func isTerm(id string, pred func(model.TerminalFact) bool) func(model.Fact) bool {
	return func(f model.Fact) bool {
		tf, ok := f.(model.TerminalFact)
		return ok && tf.ID == id && pred(tf)
	}
}

func TestStatusEventUpdatesTerminalFact(t *testing.T) {
	f := &fakeCtl{tree: fixture(t, "tree.json")}
	_, c, s, _, _ := startRun(t, f)
	w := <-s.writers
	c.waitFor(t, "initial worker fact", isTerm(idWorker, func(tf model.TerminalFact) bool { return !tf.Status.OK }))
	for _, tc := range []struct {
		status string
		want   model.Status
	}{{"active", model.StatusBusy}, {"blocked", model.StatusBlocked}, {"completed", model.StatusFinished}, {"idle", model.StatusFinished}} {
		line := `{"kind":"status","session":"` + idWorker + `","ts":1794911871.5,"seq":1,"payload":{"name":"worker-a","status":"` + tc.status + `"}}` + "\n"
		if _, err := io.WriteString(w, line); err != nil {
			t.Fatal(err)
		}
		c.waitFor(t, tc.status, isTerm(idWorker, func(tf model.TerminalFact) bool { return tf.Status == model.Of(tc.want) }))
	}
	// A status event does not need a tree reload.
	if n := f.count("tree"); n != 1 {
		t.Fatalf("tree reads = %d, want 1", n)
	}
	if args := s.args[0]; !slices.Contains(args, "--json") || args[0] != "events" {
		t.Fatalf("events args = %v", args)
	}
}

func TestSourceReadyAfterFirstTreeRead(t *testing.T) {
	f := &fakeCtl{tree: fixture(t, "tree.json")}
	_, c, s, _, _ := startRun(t, f)
	<-s.writers
	c.waitFor(t, "ready", func(f model.Fact) bool { _, ok := f.(model.SourceReady); return ok })
	facts := c.snapshot()
	terms := 0
	for _, x := range facts {
		if r, ok := x.(model.SourceReady); ok {
			if r.Kind != string(source.KindAgterm) || terms != 6 {
				t.Fatalf("ready %+v after %d terminals, want 6", r, terms)
			}
			return
		}
		if _, ok := x.(model.TerminalFact); ok {
			terms++
		}
	}
}

func TestSourceReadyWhenAgtermNotRunning(t *testing.T) {
	f := &fakeCtl{fail: &runner.ExitError{Code: 1}}
	a := newTest(t, f)
	a.stream = func(context.Context, string, ...string) (io.ReadCloser, func() error, error) {
		return nil, nil, errors.New("agterm not running")
	}
	a.minBackoff = time.Hour
	c := &collector{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, c.emit) }()
	c.waitFor(t, "ready", func(f model.Fact) bool { _, ok := f.(model.SourceReady); return ok })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSelectedEventEmitsTerminalSelected(t *testing.T) {
	f := &fakeCtl{tree: fixture(t, "tree.json")}
	_, c, s, _, _ := startRun(t, f)
	w := <-s.writers
	c.waitFor(t, "initial facts", isTerm(idWorker, func(model.TerminalFact) bool { return true }))
	lines := strings.SplitAfter(string(fixture(t, "events.jsonl")), "\n")
	if _, err := io.WriteString(w, lines[1]); err != nil {
		t.Fatal(err)
	}
	got := c.waitFor(t, "selected", func(f model.Fact) bool { _, ok := f.(model.TerminalSelected); return ok }).(model.TerminalSelected)
	if got.ID != idWorker || got.At.Unix() != 1794911872 {
		t.Fatalf("selected = %+v", got)
	}
	// Selection cleared: no session id, nothing emitted, no crash.
	_, _ = io.WriteString(w, `{"kind":"session.selected","ts":1794911873,"seq":9,"payload":{}}`+"\n")
	_, _ = io.WriteString(w, "this is not json\n")
	_, _ = io.WriteString(w, lines[0])
	c.waitFor(t, "status after junk", isTerm(idWorker, func(tf model.TerminalFact) bool { return tf.Status == model.Of(model.StatusBusy) }))
	n := 0
	for _, f := range c.snapshot() {
		if _, ok := f.(model.TerminalSelected); ok {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("TerminalSelected emitted %d times", n)
	}
}

func TestClosedSessionEmitsTerminalGone(t *testing.T) {
	f := &fakeCtl{tree: fixture(t, "tree.json")}
	_, c, s, _, _ := startRun(t, f)
	w := <-s.writers
	c.waitFor(t, "shell fact", isTerm(idShell, func(model.TerminalFact) bool { return true }))
	lines := strings.SplitAfter(string(fixture(t, "events.jsonl")), "\n")
	_, _ = io.WriteString(w, lines[2])
	c.waitFor(t, "gone", func(f model.Fact) bool { g, ok := f.(model.TerminalGone); return ok && g.ID == idShell })
}

func TestTreeChangedReloadsAndDiffs(t *testing.T) {
	f := &fakeCtl{tree: fixture(t, "tree.json")}
	_, c, s, _, _ := startRun(t, f)
	w := <-s.writers
	c.waitFor(t, "solo fact", isTerm(idSolo, func(model.TerminalFact) bool { return true }))
	next := strings.Replace(string(fixture(t, "tree.json")), `"name":"worker-a","title":"worker-a"`, `"name":"worker-a","title":"renamed"`, 1)
	f.setTree([]byte(strings.Replace(next, idSolo, "77777777-7777-4777-8777-777777777777", 1)))
	_, _ = io.WriteString(w, `{"kind":"tree.changed","window":"`+winA+`","ts":1794911874,"seq":10,"payload":{}}`+"\n")
	c.waitFor(t, "renamed", isTerm(idWorker, func(tf model.TerminalFact) bool { return tf.Title == "renamed" }))
	c.waitFor(t, "solo gone", func(f model.Fact) bool { g, ok := f.(model.TerminalGone); return ok && g.ID == idSolo })
	c.waitFor(t, "new one", isTerm("77777777-7777-4777-8777-777777777777", func(model.TerminalFact) bool { return true }))
}

func TestRestartTriggersFullTreeReload(t *testing.T) {
	f := &fakeCtl{tree: fixture(t, "tree.json")}
	_, c, s, _, _ := startRun(t, f)
	w := <-s.writers
	c.waitFor(t, "initial", isTerm(idShell, func(model.TerminalFact) bool { return true }))
	// agterm restarts: the stream ends; afterwards the shell session is gone.
	f.setTree([]byte(strings.Replace(string(fixture(t, "tree.json")), idShell, "88888888-8888-4888-8888-888888888888", 1)))
	_ = w.Close()
	<-s.writers
	c.waitFor(t, "gone after restart", func(f model.Fact) bool { g, ok := f.(model.TerminalGone); return ok && g.ID == idShell })
	if f.count("tree") < 2 {
		t.Fatal("tree not reloaded after the stream restarted")
	}
}

func TestResumesFromCursor(t *testing.T) {
	// The streaming JSON carries no run id, so the adapter does not resume a
	// cursor: every (re)connect subscribes from now and reloads the full tree,
	// which is the current truth: events missed while disconnected are covered
	// by that reload.
	f := &fakeCtl{tree: fixture(t, "tree.json")}
	_, _, s, _, _ := startRun(t, f)
	<-s.writers
	for _, a := range s.args[0] {
		if a == "--after" || a == "--run" {
			t.Fatalf("events started with a cursor: %v", s.args[0])
		}
	}
}

func TestStreamEndRetriesWithBackoff(t *testing.T) {
	f := &fakeCtl{tree: fixture(t, "tree.json")}
	a := newTest(t, f)
	var mu sync.Mutex
	var starts []time.Time
	a.stream = func(context.Context, string, ...string) (io.ReadCloser, func() error, error) {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		return nil, nil, errors.New("agterm not running")
	}
	a.minBackoff = 10 * time.Millisecond
	a.maxBackoff = 40 * time.Millisecond
	if maxBackoff != 30*time.Second {
		t.Fatalf("maxBackoff = %v, want 30s", maxBackoff)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	if err := a.Run(ctx, func(model.Fact) {}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	// 400 ms with waits of 10, 20, 40, 40... ms: a few retries, never a spin.
	if len(starts) < 3 || len(starts) > 15 {
		t.Fatalf("stream started %d times", len(starts))
	}
	for i := 4; i < len(starts); i++ {
		if gap := starts[i].Sub(starts[i-1]); gap < 30*time.Millisecond {
			t.Fatalf("gap %d = %v, back-off not capped at the max", i, gap)
		}
	}
}

func TestJumpRejectsMalformedID(t *testing.T) {
	f := &fakeCtl{tree: fixture(t, "tree.json")}
	a := newTest(t, f)
	for _, id := range []string{"", "not-a-uuid", "--window", idCoord + " ", "active", "11111111"} {
		if err := a.Jump(context.Background(), id); !errors.Is(err, source.ErrUnknownTerminal) {
			t.Errorf("Jump(%q) = %v", id, err)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("runner called for malformed ids: %v", f.calls)
	}
}

// Ids read from agterm are passed on only when they have the UUID shape, so a
// hostile window or session id can never be read as an option.
func TestHostileIDsFromAgtermAreDropped(t *testing.T) {
	f := &fakeCtl{tree: []byte(`{"ok":true,"result":{"tree":{"workspaces":[{"name":"alpha","sessions":[` +
		`{"id":"--socket=/tmp/x","name":"bad"},{"id":"` + idCoord + `","name":"good","foreground":["claude"]}]}]}}}`)}
	a := newTest(t, f)
	a.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[0] == "window" && args[1] == "list" {
			return []byte(`{"ok":true,"result":{"windows":[{"id":"--socket=/tmp/evil","open":true},{"id":"-w","open":true},{"id":"` + winA + `","open":true}]}}`), nil
		}
		return f.run(ctx, name, args...)
	}
	terms, err := a.loadTree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(terms) != 1 || terms[idCoord].window != winA {
		t.Fatalf("terms = %+v", terms)
	}
	for _, c := range f.calls {
		for _, arg := range c {
			if strings.Contains(arg, "socket") || arg == "-w" {
				t.Fatalf("hostile id passed to agtermctl: %v", c)
			}
		}
	}
}

func TestJumpRejectsIDNotInTree(t *testing.T) {
	f := &fakeCtl{tree: fixture(t, "tree.json")}
	a := newTest(t, f)
	if err := a.Jump(context.Background(), "99999999-9999-4999-8999-999999999999"); !errors.Is(err, source.ErrUnknownTerminal) {
		t.Fatalf("Jump = %v", err)
	}
	if f.count("session") != 0 {
		t.Fatal("select ran for an id not in the tree")
	}
}

func TestJumpPassesIDAsOneArgument(t *testing.T) {
	f := &fakeCtl{tree: fixture(t, "tree.json")}
	a := newTest(t, f)
	if err := a.Jump(context.Background(), idWorker); err != nil {
		t.Fatal(err)
	}
	if f.count("session", "select", "--target", idWorker, "--window", winA) != 1 {
		t.Fatalf("calls = %v", f.calls)
	}
	if f.count("window", "select", winA) != 1 {
		t.Fatalf("window not raised: %v", f.calls)
	}
}

func TestJumpWithoutAgtermReturnsError(t *testing.T) {
	a := New(Options{Bin: filepath.Join(t.TempDir(), "agtermctl")})
	if err := a.Jump(context.Background(), idWorker); err == nil {
		t.Fatal("Jump without agterm must fail")
	}
}

func TestLastSelectedAtFromTheTree(t *testing.T) {
	withSel := func(workerAt, coordAt string) []byte {
		s := strings.Replace(string(fixture(t, "tree.json")), `"name":"worker-a","title":"worker-a",`, `"name":"worker-a","title":"worker-a","lastSelectedAt":`+workerAt+`,`, 1)
		return []byte(strings.Replace(s, `"name":"coordinator-a","title":"coordinator-a",`, `"name":"coordinator-a","title":"coordinator-a","lastSelectedAt":`+coordAt+`,`, 1))
	}
	f := &fakeCtl{tree: withSel("1794912659.325396", "1794900000")}
	_, c, s, _, _ := startRun(t, f)
	w := <-s.writers
	sel := func(id string, sec int64) func(model.Fact) bool {
		return func(x model.Fact) bool {
			ts, ok := x.(model.TerminalSelected)
			return ok && ts.ID == id && ts.At.Unix() == sec
		}
	}
	c.waitFor(t, "worker selection", sel(idWorker, 1794912659))
	c.waitFor(t, "coordinator selection", sel(idCoord, 1794900000))
	// Each selection comes after its terminal's fact.
	seenFact := map[string]bool{}
	for _, x := range c.snapshot() {
		switch v := x.(type) {
		case model.TerminalFact:
			seenFact[v.ID] = true
		case model.TerminalSelected:
			if !seenFact[v.ID] {
				t.Fatalf("selection of %s before its fact", v.ID)
			}
		}
	}
	count := func() int {
		n := 0
		for _, x := range c.snapshot() {
			if _, ok := x.(model.TerminalSelected); ok {
				n++
			}
		}
		return n
	}
	// An unchanged tree emits no selection again; a newer time does.
	_, _ = io.WriteString(w, `{"kind":"tree.changed","ts":1,"seq":1,"payload":{}}`+"\n")
	time.Sleep(60 * time.Millisecond)
	if n := count(); n != 2 {
		t.Fatalf("selections after an unchanged reload = %d, want 2", n)
	}
	f.setTree(withSel("1794913000", "1794900000"))
	_, _ = io.WriteString(w, `{"kind":"tree.changed","ts":2,"seq":2,"payload":{}}`+"\n")
	c.waitFor(t, "newer worker selection", sel(idWorker, 1794913000))
	if n := count(); n != 3 {
		t.Fatalf("selections = %d, want 3", n)
	}
}
