package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/github"
	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// syncBuffer is an io.Writer safe to read while run writes to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type quietWatcher struct{ kind source.Kind }

func (w quietWatcher) Kind() source.Kind { return w.kind }
func (w quietWatcher) Probe(context.Context) source.Status {
	return source.Status{Kind: w.kind, State: source.StateMissing}
}
func (w quietWatcher) Run(ctx context.Context, _ func(model.Fact)) error { <-ctx.Done(); return nil }

type quietRepos struct{}

func (quietRepos) Probe(context.Context) source.Status {
	return source.Status{Kind: source.KindGit, State: source.StateOK}
}
func (quietRepos) Resolve(_ context.Context, f string) (model.Repo, error) {
	return model.Repo{Root: f, MainRoot: f}, nil
}
func (quietRepos) Worktrees(context.Context, string) ([]model.Worktree, error) { return nil, nil }
func (quietRepos) LocalBranches(context.Context, string) ([]string, error)     { return nil, nil }

// fakeSources keeps tests off the real machine's agents and agterm.
func fakeSources(cfg config, _ string) sources {
	return sources{
		watchers: []source.Watcher{quietWatcher{source.KindClaude}, quietWatcher{source.KindCodex}, quietWatcher{source.KindAgterm}},
		repos:    quietRepos{},
		prs:      github.New(github.Options{Off: true}),
	}
}

var linkRe = regexp.MustCompile(`http://127\.0\.0\.1:(\d+)/\?token=([0-9a-f]{64})`)

type running struct {
	out    *syncBuffer
	opened *[]string
	cancel context.CancelFunc
	done   chan error
	link   string
	port   string
	token  string
	once   sync.Once
}

func start(t *testing.T, cfg config) *running {
	t.Helper()
	home := t.TempDir()
	out := &syncBuffer{}
	var opened []string
	var mu sync.Mutex
	env := runEnv{
		home:    home,
		out:     out,
		sources: fakeSources,
		open: func(l string) error {
			mu.Lock()
			opened = append(opened, l)
			mu.Unlock()
			return nil
		},
		goos:   "linux",
		getenv: func(string) string { return "" },
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{out: out, opened: &opened, cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- run(ctx, cfg, env) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m := linkRe.FindStringSubmatch(out.String()); m != nil {
			r.link, r.port, r.token = m[0], m[1], m[2]
			t.Cleanup(func() { r.stop(t) })
			return r
		}
		select {
		case err := <-r.done:
			t.Fatalf("run ended early: %v\n%s", err, out.String())
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	t.Fatalf("no link printed:\n%s", out.String())
	return nil
}

func (r *running) stop(t *testing.T) {
	r.once.Do(func() {
		r.cancel()
		select {
		case err := <-r.done:
			if err != nil {
				t.Errorf("run: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("run did not stop")
		}
	})
}

func cfgWith(port int, portSet bool) config {
	return config{port: port, portSet: portSet, noOpen: true, staleAfter: 20 * time.Minute}
}

func TestListensOnLoopbackOnly(t *testing.T) {
	ln, _, err := listen(0, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	if host != "127.0.0.1" {
		t.Fatalf("listening on %s", host)
	}
	addrs, _ := net.InterfaceAddrs()
	tried := false
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() || ipn.IP.To4() == nil {
			continue
		}
		tried = true
		c, err := net.DialTimeout("tcp", net.JoinHostPort(ipn.IP.String(), port), time.Second)
		if err == nil {
			_ = c.Close()
			t.Fatalf("reachable on %s", ipn.IP)
		}
	}
	if !tried {
		t.Log("no non-loopback IPv4 address to try")
	}
}

func TestPortTakenFallsBackAndSaysSo(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = taken.Close() }()
	_, p, _ := net.SplitHostPort(taken.Addr().String())
	port := mustAtoi(t, p)

	r := start(t, cfgWith(port, false))
	if r.port == p {
		t.Fatal("used the taken port")
	}
	if !strings.Contains(r.out.String(), "port "+p+" is taken") {
		t.Fatalf("no notice:\n%s", r.out.String())
	}

	// A port given with --port is never replaced.
	if _, _, err := listen(port, true); err == nil || !strings.Contains(err.Error(), "--port") {
		t.Fatalf("explicit taken port: %v", err)
	}
}

func TestPrintsTokenisedLinkExactlyOnceOnStart(t *testing.T) {
	for _, noOpen := range []bool{true, false} {
		cfg := cfgWith(0, true)
		cfg.noOpen = noOpen
		r := start(t, cfg)
		// Make some requests, then stop: the token must not appear again.
		c := client(t, r)
		get(t, c, "http://127.0.0.1:"+r.port+contract.PathSnapshot)
		r.stop(t)
		if n := strings.Count(r.out.String(), r.token); n != 1 {
			t.Fatalf("no-open=%v: token printed %d times:\n%s", noOpen, n, r.out.String())
		}
		if noOpen && len(*r.opened) != 0 {
			t.Fatal("browser opened with --no-open")
		}
		if !noOpen && (len(*r.opened) != 1 || (*r.opened)[0] != r.link) {
			t.Fatalf("opened %v, want the link once", *r.opened)
		}
	}
}

func TestSecondInstanceRunsOnItsOwnPort(t *testing.T) {
	one := start(t, cfgWith(0, true))
	two := start(t, cfgWith(0, true))
	if one.port == two.port || one.token == two.token {
		t.Fatalf("instances share port %s / token", one.port)
	}
	for _, r := range []*running{one, two} {
		c := client(t, r)
		if code := get(t, c, "http://127.0.0.1:"+r.port+contract.PathSnapshot); code != http.StatusOK {
			t.Fatalf("port %s: %d", r.port, code)
		}
	}
}

func TestNoGithubFlagTurnsProviderOff(t *testing.T) {
	cfg, err := parseArgs([]string{"--no-github"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	src := realSources(cfg, t.TempDir())
	if st := src.prs.Probe(context.Background()); st.State != source.StateOff {
		t.Fatalf("github state %q, want off", st.State)
	}
}

func TestInterruptShutsDownWithin2s(t *testing.T) {
	r := start(t, cfgWith(0, true))
	// Hold an event stream open: shutdown must not wait for it.
	c := client(t, r)
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+r.port+contract.PathEvents, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	// And a connection opened ahead of time that never sends a request, as
	// browsers do: Shutdown alone would wait 5 s for it.
	idle, err := net.Dial("tcp", "127.0.0.1:"+r.port)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idle.Close() }()
	time.Sleep(20 * time.Millisecond)
	began := time.Now()
	r.stop(t)
	if d := time.Since(began); d > 2*time.Second {
		t.Fatalf("shutdown took %v", d)
	}
}

func TestStatePathFollowsPlatformConvention(t *testing.T) {
	home := "/home/dev"
	none := func(string) string { return "" }
	if got := stateFile(home, "darwin", none); got != "/home/dev/Library/Application Support/sitboard/state.json" {
		t.Errorf("darwin: %s", got)
	}
	if got := stateFile(home, "linux", none); got != "/home/dev/.local/state/sitboard/state.json" {
		t.Errorf("linux default: %s", got)
	}
	xdg := func(k string) string {
		if k == "XDG_STATE_HOME" {
			return "/home/dev/xdg-state"
		}
		return ""
	}
	if got := stateFile(home, "linux", xdg); got != filepath.Join("/home/dev/xdg-state", "sitboard", "state.json") {
		t.Errorf("linux XDG: %s", got)
	}
	rel := func(string) string { return "relative/path" }
	if got := stateFile(home, "linux", rel); got != "/home/dev/.local/state/sitboard/state.json" {
		t.Errorf("relative XDG_STATE_HOME must be ignored: %s", got)
	}
}

func TestParseArgs(t *testing.T) {
	cfg, err := parseArgs([]string{"--port", "0", "--roots", " ~/a, /b ,,", "--stale-after", "5m", "--no-open"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.portSet || cfg.port != 0 || !cfg.noOpen || cfg.staleAfter != 5*time.Minute || len(cfg.roots) != 2 {
		t.Fatalf("cfg %+v", cfg)
	}
	if cfg, _ := parseArgs(nil, &bytes.Buffer{}); cfg.portSet || cfg.port != defaultPort {
		t.Fatalf("defaults %+v", cfg)
	}
	for _, bad := range [][]string{{"--port", "70000"}, {"--stale-after", "0s"}, {"extra"}} {
		if _, err := parseArgs(bad, &bytes.Buffer{}); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestLicensesFlag(t *testing.T) {
	cfg, err := parseArgs([]string{"--licenses"}, &bytes.Buffer{})
	if err != nil || !cfg.licenses {
		t.Fatalf("cfg %+v, err %v", cfg, err)
	}
	var help bytes.Buffer
	_, _ = parseArgs([]string{"--help"}, &help)
	if !strings.Contains(help.String(), "-licenses") {
		t.Errorf("--help does not mention --licenses: %s", help.String())
	}
	var out bytes.Buffer
	printLicenses(&out)
	text := out.String()
	if !strings.Contains(text, "Copyright (c) 2026 IZZY Agency") || !strings.Contains(text, "# Third-party notices") {
		t.Errorf("--licenses prints %q...", text[:min(len(text), 200)])
	}
}

func client(t *testing.T, r *running) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	if code := get(t, c, r.link); code != http.StatusOK {
		t.Fatalf("opening link: %d", code)
	}
	c.Timeout = 0
	return c
}

func get(t *testing.T, c *http.Client, u string) int {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

type failingWatcher struct{ fired chan struct{} }

func (failingWatcher) Kind() source.Kind { return source.KindClaude }
func (failingWatcher) Probe(context.Context) source.Status {
	return source.Status{Kind: source.KindClaude, State: source.StateOK}
}
func (w failingWatcher) Run(_ context.Context, emit func(model.Fact)) error {
	emit(model.SessionFact{Key: model.SessionKey{Tool: model.ToolClaude, ID: "s1"}, Status: model.Of(model.StatusBusy), Folder: "/home/dev/projects/alpha"})
	<-w.fired
	return errors.New("records unreadable")
}

func TestFailedWatcherDropsItsCardsAndShowsFailing(t *testing.T) {
	fired := make(chan struct{})
	home := t.TempDir()
	out := &syncBuffer{}
	env := runEnv{
		home: home, out: out, goos: "linux", getenv: func(string) string { return "" },
		sources: func(cfg config, h string) sources {
			s := fakeSources(cfg, h)
			s.watchers[0] = failingWatcher{fired: fired}
			return s
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfgWith(0, true), env) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	var r running
	deadline := time.Now().Add(5 * time.Second)
	for r.link == "" && time.Now().Before(deadline) {
		if m := linkRe.FindStringSubmatch(out.String()); m != nil {
			r.link, r.port, r.token = m[0], m[1], m[2]
		}
		time.Sleep(10 * time.Millisecond)
	}
	c := client(t, &r)
	snap := func() contract.Snapshot {
		resp, err := c.Get("http://127.0.0.1:" + r.port + contract.PathSnapshot)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var s contract.Snapshot
		if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	for len(snap().Cards) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("card never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(fired)
	for {
		s := snap()
		if len(s.Cards) == 0 && s.Sources[0].Kind == contract.SourceClaude && s.Sources[0].State == contract.SourceFailing {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("after the watcher failed: %+v", s)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHTTPServerTimeouts(t *testing.T) {
	srv := newHTTPServer(context.Background(), http.NotFoundHandler())
	if srv.IdleTimeout != 60*time.Second || srv.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("idle %v, read header %v", srv.IdleTimeout, srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 0 || srv.WriteTimeout != 0 {
		t.Fatalf("read %v / write %v would cut the event stream", srv.ReadTimeout, srv.WriteTimeout)
	}
}
