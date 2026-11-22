package gitlab

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
	"github.com/IZZY-Agency/sitboard/internal/testguard"
)

// selfHosted stands for a self-managed instance glab is logged in to.
const (
	selfHosted = "example.com"
	project    = "example-org/example-repo"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "fixtures", "glab", "1.131.0", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeGlab records every call and answers like glab.
// noLoginOutput is glab 1.131.0's answer for a host missing from its config,
// as its error printer frames and wraps it (seen with NO_COLOR).
func noLoginOutput(host string) string {
	return "          \n   ERROR  \n          \n  x " + host + " has not been authenticated with glab; run `glab auth login --hostname " + host +
		"` to           \n  authenticate.          \n\n"
}

type fakeGlab struct {
	mu      sync.Mutex
	bins    []string // the program of each call
	calls   [][]string
	graphql func(args []string) ([]byte, error)
	rest    func(path string) ([]byte, error)
	auth    map[string]error  // host → `auth status` answer; missing means not logged in
	sub     map[string]string // host → `config get subfolder` answer
	// authGate, when set, runs in every `auth status` before it answers.
	authGate func(host string)
	inAuth   int // `auth status` calls running now
	maxAuth  int // the most at once
}

func (f *fakeGlab) run(_ context.Context, bin string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.bins = append(f.bins, bin)
	f.calls = append(f.calls, slices.Clone(args))
	f.mu.Unlock()
	switch {
	case args[0] == "version":
		return fixtureBytes["version.txt"], nil
	case args[0] == "auth":
		host := args[len(args)-1]
		f.mu.Lock()
		f.inAuth++
		f.maxAuth = max(f.maxAuth, f.inAuth)
		f.mu.Unlock()
		defer func() {
			f.mu.Lock()
			f.inAuth--
			f.mu.Unlock()
		}()
		if f.authGate != nil {
			f.authGate(host)
		}
		if err, ok := f.auth[host]; ok {
			return nil, err
		}
		return nil, &runner.ExitError{Code: 1, Stderr: []byte(noLoginOutput(host))}
	case args[0] == "config":
		return []byte(f.sub[args[len(args)-2]] + "\n"), nil
	case len(args) > 1 && args[1] == "graphql":
		return f.graphql(args)
	default:
		return f.rest(args[len(args)-1])
	}
}

func (f *fakeGlab) count(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		switch {
		case kind == "graphql" && len(c) > 1 && c[0] == "api" && c[1] == "graphql",
			kind == "rest" && len(c) > 1 && c[0] == "api" && c[1] != "graphql",
			kind == "auth" && c[0] == "auth":
			n++
		}
	}
	return n
}

var fixtureBytes = map[string][]byte{}

// clock is a fake time the provider reads.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *clock               { return &clock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)} }
func ok(out []byte) func([]string) ([]byte, error) {
	return func([]string) ([]byte, error) { return out, nil }
}

// newAdapter is the one way tests build a provider: a fake glab that is
// installed, logged in to selfHosted, and a fake clock. selfHosted is
// checked already, as the core's host check leaves it before any lookup:
// its yes is kept without a glab call (unchecked takes it back).
func newAdapter(t *testing.T, opts Options) (*Provider, *fakeGlab, *clock) {
	t.Helper()
	fixtureBytes["version.txt"] = fixture(t, "version.txt")
	p := New(opts)
	f := &fakeGlab{
		auth:    map[string]error{selfHosted: nil},
		graphql: ok(fixture(t, "lookup.json")),
		rest:    func(string) ([]byte, error) { return fixture(t, "closes-issues.json"), nil },
	}
	c := newClock()
	p.run, p.now = f.run, c.now
	p.found = func(string) string { return "/usr/local/bin/glab" }
	if !opts.Off {
		p.hosts[selfHosted] = hostAnswer{at: c.now(), known: true}
	}
	return p, f, c
}

// unchecked forgets what the provider knows about host's login, as before
// its first check.
func unchecked(p *Provider, host string) {
	p.mu.Lock()
	delete(p.hosts, host)
	p.mu.Unlock()
}

// newUnchecked is newAdapter with selfHosted's login not checked yet, for
// tests of the check itself.
func newUnchecked(t *testing.T) (*Provider, *fakeGlab, *clock) {
	t.Helper()
	p, f, c := newAdapter(t, Options{})
	unchecked(p, selfHosted)
	return p, f, c
}

// approve runs host's check as the core does before a lookup and fails the
// test unless it says yes.
func approve(t *testing.T, p *Provider, host string) {
	t.Helper()
	if !p.Known(context.Background(), host) {
		t.Fatalf("%s: the check before the lookup says no", host)
	}
}

func TestOnlyNewAdapterBuildsProviders(t *testing.T) {
	testguard.OnlyIn(t, []string{"New"}, []string{"newAdapter"})
}

func TestEnvSwitchesHazardsOff(t *testing.T) {
	e := glabEnv(account{}, func(string) (string, bool) { return "", false })
	for _, want := range []string{
		"GLAB_SEND_TELEMETRY=false", "GLAB_CHECK_UPDATE=false", "GLAB_NO_PROMPT=1", "GLAB_DEBUG_HTTP=false",
		"GLAB_DUO_CLI_AUTO_RUN=false", "GLAB_ORBIT_CLI_AUTO_RUN=false", "GLAB_ENABLE_CI_AUTOLOGIN=false",
	} {
		if !slices.Contains(e, want) {
			t.Errorf("env lacks %q", want)
		}
	}
}

// Nothing from the owner's environment that could change where a lookup
// goes, what login it carries or which certificates it trusts reaches glab:
// no token (glab sends one from the environment to every host in its
// config), no host, no CI login, no other config folder or home, no proxy,
// no certificate file, no git folder. Home and user come from the account.
func TestEnvIsBuiltNotInherited(t *testing.T) {
	owner := map[string]string{
		"PATH": "/elsewhere/bin", "HOME": "/elsewhere", "GLAB_CONFIG_DIR": "/cfg", "XDG_CONFIG_HOME": "/cfg", "XDG_CONFIG_DIRS": "/cfg",
		"HTTPS_PROXY": "http://proxy.example.com:3128", "https_proxy": "x", "HTTP_PROXY": "x", "http_proxy": "x", "ALL_PROXY": "x", "NO_PROXY": "x",
		"SSL_CERT_FILE": "/ca.pem", "SSL_CERT_DIR": "/ca",
		"GITLAB_TOKEN": "t1", "GITLAB_ACCESS_TOKEN": "t2", "OAUTH_TOKEN": "t3", "CI_JOB_TOKEN": "t4", "GITLAB_CI": "true",
		"GITLAB_HOST": "elsewhere.example.com", "GITLAB_URI": "x", "GL_HOST": "x", "GITLAB_API_HOST": "x", "GITLAB_REPO": "x",
		"GLAB_SEND_TELEMETRY": "true", "GIT_DIR": "/repo/.git", "SECRET": "x", "USER": "someone-else",
		"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/501/bus", "XDG_RUNTIME_DIR": "/run/user/501",
	}
	e := glabEnv(account{home: "/sample-home", name: "sample"}, func(k string) (string, bool) { v, ok := owner[k]; return v, ok })
	// The proxy variables pass (passProxy, on as built); ALL_PROXY never.
	want := append([]string{
		"PATH=/usr/bin:/bin", "HOME=/sample-home", "USER=sample", "LOGNAME=sample",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/501/bus", "XDG_RUNTIME_DIR=/run/user/501",
		"HTTP_PROXY=x", "HTTPS_PROXY=http://proxy.example.com:3128", "NO_PROXY=x", "http_proxy=x", "https_proxy=x",
	}, quietEnv...)
	if !slices.Equal(e, want) {
		t.Fatalf("glab's environment\n got %q\nwant %q", e, want)
	}
	// The proxy switch: on as built (the owner's choice, #66), exactly the
	// six proxy variables pass (both spellings of ALL_PROXY never); off,
	// nothing of a proxy passes.
	proxies := map[string]string{
		"HTTP_PROXY": "http://p1.example.com:3128", "http_proxy": "http://p2.example.com:3128",
		"HTTPS_PROXY": "http://p3.example.com:3128", "https_proxy": "http://p4.example.com:3128",
		"NO_PROXY": "a.example.com", "no_proxy": "b.example.com",
		"ALL_PROXY": "socks5://p5.example.com:1080", "all_proxy": "socks5://p6.example.com:1080",
		"HOME": "/elsewhere", "GITLAB_TOKEN": "t1",
		"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/501/bus",
	}
	fromProxies := func(k string) (string, bool) { v, ok := proxies[k]; return v, ok }
	base := []string{"PATH=/usr/bin:/bin", "HOME=/sample-home", "USER=sample", "LOGNAME=sample", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/501/bus"}
	if !passProxy {
		t.Error("proxy variables do not pass by default")
	}
	on := append(slices.Clone(base),
		"HTTP_PROXY=http://p1.example.com:3128", "HTTPS_PROXY=http://p3.example.com:3128", "NO_PROXY=a.example.com",
		"http_proxy=http://p2.example.com:3128", "https_proxy=http://p4.example.com:3128", "no_proxy=b.example.com")
	if got, want := glabEnv(account{home: "/sample-home", name: "sample"}, fromProxies), append(slices.Clone(on), quietEnv...); !slices.Equal(got, want) {
		t.Errorf("switch on\n got %q\nwant %q", got, want)
	}
	passProxy = false
	t.Cleanup(func() { passProxy = true })
	if got, want := glabEnv(account{home: "/sample-home", name: "sample"}, fromProxies), append(slices.Clone(base), quietEnv...); !slices.Equal(got, want) {
		t.Errorf("switch off\n got %q\nwant %q", got, want)
	}
	if e := glabEnv(account{}, func(string) (string, bool) { return "", false }); slices.ContainsFunc(e, func(kv string) bool {
		return strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "USER=")
	}) {
		t.Errorf("no account, no home: %q", e)
	}
}

// A real process sees exactly that environment and runs in glabDir, whatever
// the owner's shell and the folder sitboard was started in.
func TestRealProcessGetsBuiltEnvInNeutralFolder(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "placeholder")
	t.Setenv("GITLAB_HOST", "elsewhere.example.com")
	t.Setenv("GLAB_SEND_TELEMETRY", "true")
	t.Setenv("HOME", "/elsewhere")
	script := filepath.Join(t.TempDir(), "glab")
	body := "#!/bin/sh\nprintf '%s|%s|%s|%s|%s|%s' \"${GITLAB_TOKEN-unset}\" \"${GITLAB_HOST-unset}\" \"$GLAB_SEND_TELEMETRY\" \"$(pwd)\" \"$PATH\" \"$HOME\"\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	p, _, _ := newAdapter(t, Options{Bin: script})
	p.run = nil // a real process
	p.found = func(b string) string { return findGlab(b, nil) }
	out, err := p.exec(context.Background(), "version")
	if err != nil {
		t.Fatal(err)
	}
	acct, err := p.osAccount()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "unset|unset|false|"+glabDir+"|"+glabPath+"|"+acct.home || acct.home == "/elsewhere" {
		t.Fatalf("glab saw %q, want no token, no host, telemetry off, in %s", got, glabDir)
	}
}

// The account comes from the system's user list by numeric id; when it has
// no entry or no home, glab is not run with the launch environment's HOME.
func TestAccountFromUserListOnly(t *testing.T) {
	t.Setenv("HOME", "/elsewhere")
	t.Setenv("USER", "elsewhere")
	none := func(string) (*user.User, error) { return nil, user.UnknownUserIdError(4242) }
	if a, err := accountFor(4242, none); err == nil || a.home != "" {
		t.Errorf("no entry: %+v %v, want a setup error", a, err)
	}
	noHome := func(string) (*user.User, error) { return &user.User{Username: "sample"}, nil }
	if a, err := accountFor(4242, noHome); err == nil || a.home != "" {
		t.Errorf("no home: %+v %v, want a setup error", a, err)
	}
	var asked string
	entry := func(id string) (*user.User, error) {
		asked = id
		return &user.User{Username: "sample", HomeDir: "/sample-home"}, nil
	}
	if a, err := accountFor(4242, entry); err != nil || a != (account{home: "/sample-home", name: "sample"}) || asked != "4242" {
		t.Errorf("entry: %+v %v (asked %q)", a, err, asked)
	}
	// Through the provider's own path to real processes, as on Linux: the
	// user list has no entry for this uid, HOME and USER are set, and no
	// child starts.
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := filepath.Join(dir, "glab")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	passwd := filepath.Join(dir, "passwd")
	if err := os.WriteFile(passwd, []byte("sample:x:"+strconv.Itoa(os.Getuid()+1)+":1:Sample:/sample-other:/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, f, _ := newAdapter(t, Options{})
	p.run = nil // real processes
	p.found = func(string) string { return script }
	p.goos, p.userList = "linux", passwd
	if _, err := p.exec(context.Background(), "version"); !errors.Is(err, errNoAccount) {
		t.Errorf("exec without an account: %v", err)
	}
	if _, err := os.Stat(marker); err == nil || len(f.calls) != 0 {
		t.Errorf("glab ran without an account")
	}
	if st := p.Probe(context.Background()); st.State != source.StateFailing || st.Detail != noAccountDetail {
		t.Errorf("probe without an account: %+v", st)
	}
	// With an entry in that same file, glab runs with its home.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf %s \"$HOME\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwd, []byte("sample:x:"+strconv.Itoa(os.Getuid())+":1:Sample:/sample-home:/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := p.exec(context.Background(), "version"); err != nil || string(out) != "/sample-home" {
		t.Errorf("listed uid: %q %v, want the home from the user list", out, err)
	}
}

// glab is found once, as one absolute path used for every call: on the
// launch PATH, else in the standard Homebrew and system folders, so a
// desktop launch without Homebrew on its PATH still finds it.
func TestFindGlab(t *testing.T) {
	brew, onPath := t.TempDir(), t.TempDir()
	for _, d := range []string{brew, onPath} {
		if err := os.WriteFile(filepath.Join(d, "glab"), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", t.TempDir())
	if got := findGlab("glab", []string{t.TempDir(), brew}); got != filepath.Join(brew, "glab") {
		t.Errorf("not on PATH: %q, want the one in the standard folder", got)
	}
	if got := findGlab("glab", []string{t.TempDir()}); got != "" {
		t.Errorf("nowhere: %q", got)
	}
	t.Setenv("PATH", onPath)
	if got := findGlab("glab", []string{brew}); got != filepath.Join(onPath, "glab") {
		t.Errorf("on PATH: %q, want the launch PATH first", got)
	}
	own := filepath.Join(brew, "glab")
	if got := findGlab(own, nil); got != own {
		t.Errorf("explicit path: %q", got)
	}
	if got := findGlab(filepath.Join(t.TempDir(), "glab"), []string{brew}); got != "" {
		t.Errorf("a missing explicit path is not replaced by another glab: %q", got)
	}
	if err := os.MkdirAll(filepath.Join(brew, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brew, "sub", "glab"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := findGlab(filepath.Join("sub", "glab"), []string{brew}); got != "" {
		t.Errorf("an explicit relative path is not looked for in the standard folders: %q", got)
	}
	// The provider's own lookup goes through glabDirs in order: shown with
	// temporary folders, so it does not depend on what the machine has
	// installed (Ubuntu has /usr/bin/sh as well as /bin/sh).
	first, second := t.TempDir(), t.TempDir()
	for _, d := range []string{first, second} {
		if err := os.WriteFile(filepath.Join(d, "glab"), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	saved := glabDirs
	t.Cleanup(func() { glabDirs = saved })
	t.Setenv("PATH", t.TempDir())
	glabDirs = []string{t.TempDir(), first, second}
	if got := findDefault("glab"); got != filepath.Join(first, "glab") {
		t.Errorf("the default lookup without PATH: %q, want the first folder that has it", got)
	}
	glabDirs = []string{t.TempDir(), second}
	if got := findDefault("glab"); got != filepath.Join(second, "glab") {
		t.Errorf("the default lookup without PATH: %q", got)
	}
	glabDirs = saved
	if want := []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/snap/bin"}; !slices.Equal(glabDirs, want) {
		t.Errorf("standard folders %v, want %v in that order", glabDirs, want)
	}

	p, f, _ := newAdapter(t, Options{})
	looks := 0
	p.found = func(string) string { looks++; return "/sample-bin/glab" }
	p.Probe(context.Background())
	p.Known(context.Background(), selfHosted)
	if _, err := p.Lookup(context.Background(), selfHosted, project, []int{101}, nil); err != nil {
		t.Fatal(err)
	}
	if looks != 1 || len(f.bins) == 0 {
		t.Fatalf("looked up %d times, %d calls", looks, len(f.bins))
	}
	for _, b := range f.bins {
		if b != "/sample-bin/glab" {
			t.Errorf("ran %q, want the resolved path", b)
		}
	}
	p, f, _ = newAdapter(t, Options{})
	p.found = func(string) string { return "" }
	if _, err := p.exec(context.Background(), "version"); !errors.Is(err, errNoGlab) || len(f.bins) != 0 {
		t.Errorf("no glab: %v, %d calls", err, len(f.bins))
	}
}

// The production resolver on Linux (pure Go, as released) reads the user
// list itself: user.LookupId would fall back to HOME and USER for the
// current uid when it has no entry.
func TestResolveAccountWithoutEntry(t *testing.T) {
	t.Setenv("HOME", "/elsewhere")
	t.Setenv("USER", "elsewhere")
	uid := os.Getuid()
	passwd := filepath.Join(t.TempDir(), "passwd")
	write := func(body string) {
		if err := os.WriteFile(passwd, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	other := strconv.Itoa(uid + 1)
	write("# sample\nsample:x:" + other + ":1:Sample:/sample-other:/bin/sh\n") // no entry for uid, whatever it is (root in a container)
	if a, err := resolveAccount("linux", passwd, uid); !errors.Is(err, errNoAccount) || a.home != "" {
		t.Errorf("unlisted uid: %+v %v, want a setup error", a, err)
	}
	write("sample:x:" + strconv.Itoa(uid) + ":1:Sample::/bin/sh\n")
	if a, err := resolveAccount("linux", passwd, uid); !errors.Is(err, errNoAccount) {
		t.Errorf("no home: %+v %v", a, err)
	}
	write("sample:x:" + strconv.Itoa(uid) + ":1:Sample:relative:/bin/sh\n")
	if a, err := resolveAccount("linux", passwd, uid); !errors.Is(err, errNoAccount) {
		t.Errorf("relative home: %+v %v", a, err)
	}
	write("#sample:x:" + strconv.Itoa(uid) + ":1:Sample:/commented:/bin/sh\nsample:x:" + strconv.Itoa(uid) + "0:1:Sample:/longer-uid:/bin/sh\nsample:x:" + strconv.Itoa(uid) + ":1:Sample:/sample-home:/bin/sh\n")
	if a, err := resolveAccount("linux", passwd, uid); err != nil || a != (account{home: "/sample-home", name: "sample"}) {
		t.Errorf("entry: %+v %v", a, err)
	}
	// A malformed record does not shadow a later valid one for the uid.
	u := strconv.Itoa(uid)
	write("sample:x:" + u + ":gid:Sample:/bad-gid:/bin/sh\nsample:x:" + u + ":1:Sample:/six-fields\nsample:x:" + u + ":1:Sample:relative:/bin/sh\nsample:x:" + u + ":1:Sample:/sample-home:/bin/sh\n")
	if a, err := resolveAccount("linux", passwd, uid); err != nil || a.home != "/sample-home" {
		t.Errorf("malformed records before a valid one: %+v %v", a, err)
	}
	// A file past the read limit is refused: its last record may be cut,
	// here inside the home folder.
	body := "sample:x:" + u + ":1:Sample:/sample-home-is-long:/bin/sh\n"
	write(body)
	saved := maxUserList
	t.Cleanup(func() { maxUserList = saved })
	maxUserList = int64(strings.Index(body, "-is-long"))
	if a, err := resolveAccount("linux", passwd, uid); !errors.Is(err, errNoAccount) {
		t.Errorf("a record cut by the read limit: %+v %v", a, err)
	}
	maxUserList = int64(strings.Index(body, "/sh")) // cut inside the shell: still seven fields
	if a, err := resolveAccount("linux", passwd, uid); !errors.Is(err, errNoAccount) {
		t.Errorf("a record cut inside its shell: %+v %v", a, err)
	}
	maxUserList = int64(len(body))
	if a, err := resolveAccount("linux", passwd, uid); err != nil || a.home != "/sample-home-is-long" {
		t.Errorf("a file exactly at the limit: %+v %v", a, err)
	}
	maxUserList = saved
	if _, err := resolveAccount("linux", filepath.Join(t.TempDir(), "none"), uid); !errors.Is(err, errNoAccount) {
		t.Errorf("no user list: %v", err)
	}
	if runtime.GOOS == "darwin" {
		if a, err := resolveAccount("darwin", passwd, uid); err != nil || a.home == "/sample-home" || a.home == "/elsewhere" {
			t.Errorf("macOS asks the system, not the file or HOME: %+v %v", a, err)
		}
	}
}

// Callers that ask for glab's path at the same time share one lookup and
// one path; a miss is looked up again.
func TestGlabPathResolvedOnceForConcurrentCallers(t *testing.T) {
	p, _, _ := newAdapter(t, Options{})
	var looks atomic.Int32
	var unlocked atomic.Bool
	p.found = func(string) string {
		if p.binMu.TryLock() { // the lookup runs with the lock held
			p.binMu.Unlock()
			unlocked.Store(true)
		}
		n := looks.Add(1)
		for range 100 {
			runtime.Gosched() // let the other callers catch up
		}
		return "/sample-bin/glab-" + strconv.Itoa(int(n))
	}
	const callers = 16
	paths := make([]string, callers)
	var start, done sync.WaitGroup
	start.Add(1)
	for i := range callers {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			paths[i] = p.glab()
		}()
	}
	start.Done()
	done.Wait()
	if unlocked.Load() {
		t.Error("glab's path was looked up without the lock")
	}
	if looks.Load() != 1 {
		t.Errorf("%d lookups, want 1", looks.Load())
	}
	for _, got := range paths {
		if got != "/sample-bin/glab-1" {
			t.Fatalf("callers did not all get the first path: %q", paths)
		}
	}

	p, _, _ = newAdapter(t, Options{})
	misses := 0
	p.found = func(string) string {
		misses++
		if misses < 3 {
			return ""
		}
		return "/sample-bin/glab"
	}
	var got []string
	for range 4 {
		got = append(got, p.glab())
	}
	if want := []string{"", "", "/sample-bin/glab", "/sample-bin/glab"}; !slices.Equal(got, want) || misses != 3 {
		t.Errorf("misses looked up again, the first find kept: %q, %d lookups", got, misses)
	}
}

func TestLookupSendsValuesOnlyAsRawStrings(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	evil := "@/etc/passwd"
	if _, err := p.Lookup(context.Background(), selfHosted, project, []int{101}, []string{evil, ":branch"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if c[0] != "api" {
			continue
		}
		if !slices.Contains(c, "--hostname") || c[slices.Index(c, "--hostname")+1] != selfHosted {
			t.Errorf("call without --hostname %s: %q", selfHosted, c)
		}
		for i, a := range c {
			if a == "-F" || strings.HasPrefix(a, "--field") || a == "--input" || a == "--form" {
				t.Errorf("typed field in %q", c)
			}
			if a == "-f" && strings.HasPrefix(c[i+1], "query=") && strings.Contains(c[i+1], evil) {
				t.Errorf("branch name inside the query text: %q", c[i+1])
			}
		}
		if c[1] == "graphql" {
			for _, want := range []string{"path=" + project, "b0=:branch", "b1=" + evil} { // sorted
				if !slices.Contains(c, want) {
					t.Errorf("graphql call lacks -f %q: %q", want, c)
				}
			}
		}
	}
}

func TestParseRemote(t *testing.T) {
	scp := "git" + "@" // kept apart so the private scan does not read an address
	cases := []struct {
		raw, host, path string
	}{
		{scp + "gitlab.com:example-org/example-repo.git", "gitlab.com", "example-org/example-repo"},
		{scp + "example.com:example-org/sub/team/example-repo", "example.com", "example-org/sub/team/example-repo"},
		{"ssh://" + scp + "example.com:2222/example-org/example-repo.git", "example.com", "example-org/example-repo"},
		{"https://GitLab.com/example-org/example-repo/", "gitlab.com", "example-org/example-repo"},
		{"https://oauth2:secret@example.com/example-org/example-repo.git", "example.com", "example-org/example-repo"},
		{"git://example.com/example-org/example-repo", "example.com", "example-org/example-repo"},
		{"https://example.com:8443/example-org/example-repo", "", ""}, // web port: refused
		{"https://example.com/example-repo", "", ""},                  // one segment
		{"https://example.com/example-org/../example-repo", "", ""},
		{"https://example.com/example-org/a..b", "", ""},
		{"file://example.com/example-org/example-repo", "", ""},                       // ..
		{"https://example.com/-org/example-repo", "", ""},                             // option-like
		{"https://example.com/example-org/example-repo?x=1", "", ""},                  // query
		{"file:///srv/example-org/example-repo", "", ""},                              // not a web host
		{"ext::sh -c touch% /tmp/x", "", ""},                                          // helper
		{"/srv/example-org/example-repo", "", ""},                                     // local path
		{"localhost:example-org/example-repo", "", ""},                                // no dot in host
		{scp + "example.com:example-org/example-repo.git.git", "", ""},                // .git name
		{"https://example.com/example-org/" + strings.Repeat("a/", 21) + "b", "", ""}, // too deep
	}
	for _, c := range cases {
		host, path, ok := ParseRemote(c.raw)
		if ok != (c.host != "") || host != c.host || path != c.path {
			t.Errorf("ParseRemote(%q) = %q %q %v, want %q %q", c.raw, host, path, ok, c.host, c.path)
		}
	}
}

func TestKnown(t *testing.T) {
	p, f, c := newAdapter(t, Options{})
	unchecked(p, selfHosted)
	ctx := context.Background()
	// gitlab.com needs a login like any other host; github.com is never asked.
	if p.Known(ctx, "github.com") || f.count("auth") != 0 {
		t.Fatalf("github.com is known or asked about")
	}
	if p.Known(ctx, GitLabCom) || f.count("auth") != 1 {
		t.Fatalf("gitlab.com without a login is known, or not asked: %d auth calls", f.count("auth"))
	}
	f.auth[GitLabCom] = nil
	c.add(unknownFor)
	if !p.Known(ctx, GitLabCom) {
		t.Fatal("gitlab.com once logged in")
	}
	authsBefore := f.count("auth")
	first, second := p.Known(ctx, selfHosted), p.Known(ctx, selfHosted)
	if !first || !second || f.count("auth") != authsBefore+1 {
		t.Fatalf("a logged-in host: known, asked once; auth calls %d", f.count("auth"))
	}
	first, second = p.Known(ctx, "other.example.com"), p.Known(ctx, "other.example.com")
	if first || second || f.count("auth") != authsBefore+2 {
		t.Fatalf("a host glab has no login for: unknown, asked once; auth calls %d", f.count("auth"))
	}
	f.auth["other.example.com"] = nil // logged in since
	c.add(unknownFor + time.Second)
	if !p.Known(ctx, "other.example.com") {
		t.Fatal("a new login is noticed after a minute")
	}
	auths := f.count("auth")
	if p.Known(ctx, "-x.example.com") || p.Known(ctx, "Example.com") || p.Known(ctx, "example.com:443") || f.count("auth") != auths {
		t.Fatal("a host that is not a plain lowercase name is never asked about")
	}
	off, fo, _ := newAdapter(t, Options{Off: true})
	if off.Known(ctx, GitLabCom) || off.Known(ctx, selfHosted) || len(fo.calls) != 0 {
		t.Fatal("switched off: nothing is known and glab never runs")
	}
}

func TestBuildQuery(t *testing.T) {
	q := buildQuery([]int{7, 12}, 2, true)
	for _, want := range []string{
		`query($path:ID!,$b0:String!,$b1:String!)`, `project(fullPath:$path)`,
		`m:mergeRequests(iids:["7","12"],first:2){nodes{...F}}`,
		`b:mergeRequests(sourceBranches:[$b0,$b1],first:50,sort:CREATED_DESC){nodes{...F}}`,
		`fragment F on MergeRequest{` + mrFields + approvalFields + `}`,
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query lacks %s:\n%s", want, q)
		}
	}
	if q := buildQuery(nil, 1, true); strings.Contains(q, "iids") {
		t.Errorf("no numbers asked: no number list: %s", q)
	}
	if q := buildQuery([]int{3}, 0, true); strings.Contains(q, "sourceBranches") || strings.Contains(q, "$b") {
		t.Errorf("no branches asked: no branch list: %s", q)
	}
	many := make([]int, 70)
	for i := range many {
		many[i] = i + 1
	}
	if got := cleanIIDs(append(many, 0, -3, 1<<31, 70)); len(got) != 70 || got[0] != 1 || got[len(got)-1] != 70 {
		t.Errorf("cleanIIDs keeps %d (%d to %d), want all 70 valid ones once", len(got), got[0], got[len(got)-1])
	}
	brs := make([]string, 60)
	for i := range brs {
		brs[i] = strings.Repeat("b", i+1)
	}
	if got := cleanBranches(append(brs, "", "a\nb", strings.Repeat("x", 256))); len(got) != 60 || slices.Contains(got, "") {
		t.Errorf("cleanBranches keeps %d, want the 60 valid ones", len(got))
	}
}

func TestParseLookupFixture(t *testing.T) {
	pa, err := parseAnswer(fixture(t, "lookup.json"), selfHosted, project, shape{iids: true, branches: true, approvals: true})
	if err != nil {
		t.Fatal(err)
	}
	got := pa.mrs
	var iids []int
	for _, f := range got {
		iids = append(iids, f.mr.IID)
	}
	// 105 (a fork's branch) and 106 (an unknown state) are dropped, 101 once.
	if !slices.Equal(iids, []int{101, 102, 103, 104, 107}) {
		t.Fatalf("iids %v", iids)
	}
	at := func(s string) time.Time { t.Helper(); v, _ := time.Parse(time.RFC3339, s); return v }
	want := map[int]MergeRequest{
		101: {State: model.PRStateOpen, Checks: model.ChecksRunning, Approval: ApprovalRequired, SourceBranch: "feature-a", OpenedAt: at("2026-11-19T12:09:34Z")},
		102: {State: model.PRStateMerged, Checks: model.ChecksPassing, Approval: Approved, SourceBranch: "main", ClosedAt: at("2026-11-20T11:06:02Z")},
		103: {State: model.PRStateOpen, Checks: model.ChecksFailing, Approval: ApprovalNone}, // locked: being merged, not over
		104: {State: model.PRStateOpen, Checks: model.ChecksPassing, Approval: Approved},
		107: {State: model.PRStateOpen, Checks: model.ChecksNone, Approval: ApprovalNone, Draft: true},
	}
	for _, f := range got {
		w, m := want[f.mr.IID], f.mr
		if m.State != w.State || m.Checks != w.Checks || m.Approval != w.Approval || m.Draft != w.Draft {
			t.Errorf("!%d: %s %s %q draft=%v, want %s %s %q draft=%v", m.IID, m.State, m.Checks, m.Approval, m.Draft, w.State, w.Checks, w.Approval, w.Draft)
		}
		if !w.ClosedAt.IsZero() && !m.ClosedAt.Equal(w.ClosedAt) || w.ClosedAt.IsZero() && !m.ClosedAt.IsZero() {
			t.Errorf("!%d closed at %v, want %v", m.IID, m.ClosedAt, w.ClosedAt)
		}
		if w.SourceBranch != "" && m.SourceBranch != w.SourceBranch || !w.OpenedAt.IsZero() && !m.OpenedAt.Equal(w.OpenedAt) {
			t.Errorf("!%d branch %q opened %v", m.IID, m.SourceBranch, m.OpenedAt)
		}
		if m.URL != "https://example.com/example-org/example-repo/-/merge_requests/"+itoa(m.IID) || m.Host != selfHosted || m.Path != project {
			t.Errorf("!%d url %q", m.IID, m.URL)
		}
		if f.projectID != 1000 {
			t.Errorf("!%d project id %d", m.IID, f.projectID)
		}
	}
	if _, err := parseAnswer(fixture(t, "project-null.json"), selfHosted, project, shape{iids: true, branches: true, approvals: true}); !errors.Is(err, errNoProject) {
		t.Errorf("project null: %v", err)
	}
	if _, err := parseAnswer(fixture(t, "complexity-error.json"), selfHosted, project, shape{iids: true, branches: true, approvals: true}); err == nil || errors.Is(err, errNoProject) {
		t.Errorf("errors without data: %v", err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestMapPipeline(t *testing.T) {
	cases := map[string]model.Checks{
		"SUCCESS": model.ChecksPassing, "FAILED": model.ChecksFailing, "CANCELED": model.ChecksFailing, "CANCELING": model.ChecksFailing,
		"CREATED": model.ChecksRunning, "WAITING_FOR_RESOURCE": model.ChecksRunning, "PREPARING": model.ChecksRunning,
		"WAITING_FOR_CALLBACK": model.ChecksRunning, "PENDING": model.ChecksRunning, "RUNNING": model.ChecksRunning,
		"SCHEDULED": model.ChecksRunning, "SKIPPED": model.ChecksUnknown, "MANUAL": model.ChecksUnknown, "NEW_LATER": model.ChecksUnknown,
	}
	for s, want := range cases {
		if got := mapPipeline(s, true); got != want {
			t.Errorf("%s → %s, want %s", s, got, want)
		}
	}
	if got := mapPipeline("", false); got != model.ChecksNone {
		t.Errorf("no pipeline → %s", got)
	}
}

func TestMapApproval(t *testing.T) {
	n := func(v int) *int { return &v }
	yes, no := func() *bool { b := true; return &b }(), func() *bool { b := false; return &b }()
	cases := []struct {
		approved      *bool
		left, require *int
		want          Approval
	}{
		{no, n(1), n(1), ApprovalRequired},
		{no, n(2), n(3), ApprovalRequired},
		{yes, n(0), n(2), Approved},
		{yes, n(0), n(0), ApprovalNone}, // no approval rule (seen on public projects)
		{no, n(0), n(0), ApprovalNone},
		{yes, nil, nil, ApprovalUnknown}, // counts not given
		{yes, n(0), nil, ApprovalUnknown},
		{nil, n(0), n(2), ApprovalUnknown}, // required, none left, but not said approved
	}
	for _, c := range cases {
		if got := mapApproval(c.approved, c.left, c.require); got != c.want {
			t.Errorf("approved=%v left=%v required=%v → %q, want %q", c.approved, c.left, c.require, got, c.want)
		}
	}
	if q := buildQuery([]int{1}, 1, false); strings.Contains(q, "approv") {
		t.Errorf("no approval fields asked: %s", q)
	}
	if !lacksApprovalFields(fixture(t, "no-approval-fields.json")) || lacksApprovalFields(fixture(t, "complexity-error.json")) ||
		lacksApprovalFields([]byte(`{"errors":[{"message":"Field 'approvalsLeft' doesn't exist on type 'MergeRequest'"},{"message":"other"}]}`)) {
		t.Error("only a refusal of the approval fields alone is a Community Edition answer")
	}
	// graphql-ruby can add a spelling suggestion to the message; the
	// extensions say the same thing either way.
	undefined := func(field, msg string) string {
		return `{"message":"` + msg + `","extensions":{"code":"undefinedField","typeName":"MergeRequest","fieldName":"` + field + `"}}`
	}
	suggest := `{"errors":[` + undefined("approvalsLeft", "Field 'approvalsLeft' doesn't exist on type 'MergeRequest' (Did you mean `approved`?)") + `]}`
	if !lacksApprovalFields([]byte(suggest)) {
		t.Error("a refusal with a spelling suggestion is a Community Edition answer")
	}
	mixed := []string{
		`{"errors":[` + undefined("approvalsLeft", "x") + `,` + undefined("title", "x") + `]}`,
		`{"errors":[` + undefined("approvalsLeft", "x") + `,{"message":"y","extensions":{"code":"argumentLiteralsIncompatible","typeName":"Field","argumentName":"iids"}}]}`,
		`{"errors":[{"message":"x","extensions":{"code":"undefinedField","typeName":"Project","fieldName":"approvalsLeft"}}]}`,
		`{"errors":[{"message":"Field 'approvalsLeft' doesn't exist on type 'MergeRequest'"}]}`,
		`{"errors":[{"message":"x","extensions":{"code":"argumentNotAccepted","typeName":"MergeRequest","fieldName":"approved"}}]}`,
	}
	for _, m := range mixed {
		if lacksApprovalFields([]byte(m)) {
			t.Errorf("not a refusal of the approval fields alone: %s", m)
		}
	}
	if s, ok := mapState("locked"); !ok || s != model.PRStateOpen {
		t.Error("locked (being merged) is open, not over")
	}
	if _, ok := mapState("all"); ok {
		t.Error("an unknown state is not guessed")
	}
}

func TestURLs(t *testing.T) {
	if got := MergeRequestURL(selfHosted, "example-org/sub/example-repo", 12); got != "https://example.com/example-org/sub/example-repo/-/merge_requests/12" {
		t.Errorf("mr url %q", got)
	}
	if got := IssueURL(GitLabCom, project, 7); got != "https://gitlab.com/example-org/example-repo/-/issues/7" {
		t.Errorf("issue url %q", got)
	}
	mrRe, issueRe := regexp.MustCompile(MergeRequestURLPattern), regexp.MustCompile(IssueURLPattern)
	deep := "a/" + strings.Repeat("b/", 20) + "c" // a group, 20 subgroups, the project: the deepest GitLab allows
	if !ValidPath(deep) || ValidPath("x/"+deep) {
		t.Errorf("22 segments are a project path, 23 are not")
	}
	for _, path := range []string{project, "example-org/sub/example-repo", "_x/y.z", deep} {
		for _, host := range []string{GitLabCom, selfHosted, "a-b.c-d.example"} {
			if u := MergeRequestURL(host, path, 1<<31-1); !mrRe.MatchString(u) {
				t.Errorf("built %q does not match MergeRequestURLPattern", u)
			}
			if u := IssueURL(host, path, 1); !issueRe.MatchString(u) {
				t.Errorf("built %q does not match IssueURLPattern", u)
			}
		}
	}
	for _, bad := range []struct {
		host, path string
		n          int
	}{
		{selfHosted, project, 0}, {selfHosted, project, 1 << 31}, {"Example.com", project, 1},
		{"example.com/x", project, 1}, {selfHosted, "example-repo", 1}, {selfHosted, "example-org/../x", 1},
		{selfHosted, "example-org/example-repo/-/merge_requests/1#", 1},
	} {
		if got := MergeRequestURL(bad.host, bad.path, bad.n); got != "" {
			t.Errorf("MergeRequestURL(%q, %q, %d) = %q, want none", bad.host, bad.path, bad.n, got)
		}
	}
}

func TestLookupClosingIssues(t *testing.T) {
	p, f, c := newAdapter(t, Options{})
	ctx := context.Background()
	var restPaths []string
	f.rest = func(path string) ([]byte, error) {
		restPaths = append(restPaths, path)
		return fixture(t, "closes-issues.json"), nil
	}
	res, err := p.Lookup(ctx, selfHosted, project, []int{101, 102, 103}, []string{"feature-b", "feature-e"})
	mrs := res.MRs
	if err != nil {
		t.Fatal(err)
	}
	if len(mrs) != 5 || f.count("graphql") != 1 || f.count("rest") != 5 {
		t.Fatalf("%d merge requests, %d graphql, %d rest calls", len(mrs), f.count("graphql"), f.count("rest"))
	}
	if !slices.Contains(restPaths, "projects/1000/merge_requests/101/closes_issues?per_page=100&page=1") {
		t.Errorf("rest paths %q", restPaths)
	}
	for _, m := range mrs {
		if !m.ClosingKnown || !slices.Equal(m.ClosingIssues, []int{7, 8}) { // 9 is another project's, 7 once
			t.Errorf("!%d closes %v", m.IID, m.ClosingIssues)
		}
	}
	mrs[0].ClosingIssues[0] = 999 // a caller's change does not reach the cache
	again, err := p.Lookup(ctx, selfHosted, project, []int{101, 102, 103}, []string{"feature-b", "feature-e"})
	if err != nil || again.MRs[0].ClosingIssues[0] != 7 || f.count("graphql") != 1 {
		t.Fatalf("cache shared a slice: %v %v", again, err)
	}
	c.add(cacheFor + time.Second)
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101, 102, 103}, []string{"feature-b", "feature-e"}); err != nil {
		t.Fatal(err)
	}
	if f.count("graphql") != 2 || f.count("rest") != 5 {
		t.Fatalf("closing issues are kept while the state holds: %d rest", f.count("rest"))
	}
	c.add(closingOpenFor)
	approve(t, p, selfHosted)
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101, 102, 103}, []string{"feature-b", "feature-e"}); err != nil {
		t.Fatal(err)
	}
	if f.count("rest") != 5+4 { // the open ones (101, 104, 107, and 103: locked is being merged), not merged 102
		t.Fatalf("after ten minutes only open merge requests are asked again: %d rest", f.count("rest"))
	}
}

func TestLookupAsksClosingAgainWhenStateChanges(t *testing.T) {
	p, f, c := newAdapter(t, Options{})
	ctx := context.Background()
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); err != nil {
		t.Fatal(err)
	}
	merged := strings.Replace(string(fixture(t, "lookup.json")), `"iid":"101","title":"Placeholder open merge request","sourceBranch":"feature-a","sourceProjectId":1000,"projectId":1000,"state":"opened"`,
		`"iid":"101","title":"Placeholder open merge request","sourceBranch":"feature-a","sourceProjectId":1000,"projectId":1000,"state":"merged"`, 1)
	f.graphql = ok([]byte(merged))
	before := f.count("rest")
	c.add(cacheFor + time.Second) // well inside closingOpenFor
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); err != nil {
		t.Fatal(err)
	}
	if f.count("rest") != before+1 {
		t.Fatalf("101 merged: its closing issues are asked again; %d calls", f.count("rest")-before)
	}
}

func TestLookupCapsClosingCalls(t *testing.T) {
	p, f, c := newAdapter(t, Options{})
	f.rest = func(string) ([]byte, error) { return []byte(`[]`), nil }
	var nodes []string
	for i := 1; i <= maxClosingCalls+5; i++ {
		nodes = append(nodes, `{"iid":"`+itoa(i)+`","title":"Placeholder","sourceBranch":"feature-a","sourceProjectId":1000,"projectId":1000,"state":"opened","draft":false,"createdAt":"2026-11-19T12:09:34Z","closedAt":null,"mergedAt":null,"approved":true,"approvalsLeft":0,"approvalsRequired":0,"headPipeline":null}`)
	}
	f.graphql = ok([]byte(`{"data":{"project":{"b":{"nodes":[` + strings.Join(nodes, ",") + `]}}}}`))
	if _, err := p.Lookup(context.Background(), selfHosted, project, nil, []string{"feature-a"}); err != nil {
		t.Fatal(err)
	}
	if f.count("rest") != maxClosingCalls {
		t.Fatalf("%d closes_issues calls, want at most %d", f.count("rest"), maxClosingCalls)
	}
	c.add(cacheFor + time.Second)
	if _, err := p.Lookup(context.Background(), selfHosted, project, nil, []string{"feature-a"}); err != nil {
		t.Fatal(err)
	}
	if f.count("rest") != maxClosingCalls+5 {
		t.Fatalf("the rest are asked on the next lookup: %d", f.count("rest"))
	}
}

// hostLine is the GitLab line the core shows for selfHosted: glab's probe,
// failing with that host's last failed call.
func hostLine(ctx context.Context, p *Provider) Status {
	st := p.Probe(ctx)
	if n := p.Failing(selfHosted); n != "" && st.State == source.StateOK {
		st.State, st.Detail = source.StateFailing, n
	}
	return st
}

func TestLookupFailures(t *testing.T) {
	ctx := context.Background()
	exit := func(stdout, stderr string) error {
		return &runner.ExitError{Code: 1, Stdout: []byte(stdout), Stderr: []byte(stderr)}
	}

	p, f, _ := newAdapter(t, Options{})
	f.graphql = ok(fixture(t, "project-null.json"))
	if _, err := p.Lookup(ctx, selfHosted, project, []int{1}, nil); !errors.Is(err, source.ErrRepoNotFound) {
		t.Errorf("project null: %v", err)
	}
	if st := hostLine(ctx, p); st.State != source.StateOK {
		t.Errorf("one missing project is not the source failing: %+v", st)
	}

	p, f, _ = newAdapter(t, Options{})
	partial := fixture(t, "partial-errors.json")
	f.graphql = func([]string) ([]byte, error) { return nil, exit(string(partial), "glab: Placeholder field error") }
	if res, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); err != nil || len(res.MRs) != 1 || res.MRs[0].IID != 101 {
		t.Errorf("partial answer: %v %v", res, err)
	}

	p, f, _ = newAdapter(t, Options{})
	f.graphql = func([]string) ([]byte, error) {
		return nil, exit(string(fixture(t, "complexity-error.json")), "glab: Query has complexity")
	}
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); err == nil {
		t.Error("errors without data is a failure")
	}
	if st := hostLine(ctx, p); st.State != source.StateFailing || st.Detail != "glab failed" {
		t.Errorf("probe after a failure: %+v", st)
	}

	p, f, _ = newAdapter(t, Options{})
	f.graphql = func([]string) ([]byte, error) {
		return nil, exit(`{"errors":[{"message":"Invalid token"}]}`, "glab: Invalid token")
	}
	// GraphQL's own error list names no status, and its words are GitLab's:
	// a failure, not a logout (the host's next `glab auth status` names a
	// 401 itself).
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); err == nil || errors.Is(err, source.ErrNotLoggedIn) {
		t.Errorf("bad token: %v, want a failure that is no logout", err)
	}
	if st := hostLine(ctx, p); st.Detail != "glab failed" {
		t.Errorf("bad token detail %q", st.Detail)
	}

	p, f, _ = newAdapter(t, Options{})
	f.graphql = func([]string) ([]byte, error) {
		return nil, exit(`{"message":"404 Not Found"}`, "glab: 404 Not Found (HTTP 404)")
	}
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); err == nil || errors.Is(err, source.ErrRepoNotFound) {
		t.Errorf("a 404 from the GraphQL endpoint is the source failing, not one project: %v", err)
	}
	if st := hostLine(ctx, p); st.State != source.StateFailing {
		t.Errorf("probe after a GraphQL 404: %+v", st)
	}

	// glab's own words, from glab 1.131.0 against a server answering 429.
	p, f, c := newAdapter(t, Options{})
	f.graphql = func([]string) ([]byte, error) {
		return nil, exit("Retry later\n", "glab: HTTP 429\nglab: rate limited, retry after 1m30s\n")
	}
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); !errors.Is(err, errRateLimited) {
		t.Errorf("rate limited: %v", err)
	}
	if st := hostLine(ctx, p); st.Detail != "rate limited" {
		t.Errorf("rate limited detail %q", st.Detail)
	}
	c.add(89 * time.Second)
	if _, err := p.Lookup(ctx, selfHosted, project, []int{102}, nil); !errors.Is(err, errRateLimited) || f.count("graphql") != 1 {
		t.Errorf("no call during GitLab's wait: %v, %d calls", err, f.count("graphql"))
	}
	c.add(2 * time.Second)
	f.graphql = ok(fixture(t, "lookup.json"))
	if _, err := p.Lookup(ctx, selfHosted, project, []int{102}, nil); err != nil || f.count("graphql") != 2 {
		t.Errorf("asked again after GitLab's wait: %v", err)
	}

	p, f, c = newAdapter(t, Options{})
	f.graphql = func([]string) ([]byte, error) {
		return nil, exit(`{"message":"429 Too Many Requests"}`, "glab: 429 Too Many Requests (HTTP 429)\n")
	}
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); !errors.Is(err, errRateLimited) {
		t.Errorf("rate limited without a wait: %v", err)
	}
	c.add(rateLimitBackoff - time.Second)
	if _, err := p.Lookup(ctx, selfHosted, project, []int{102}, nil); !errors.Is(err, errRateLimited) || f.count("graphql") != 1 {
		t.Errorf("no wait named: %v left alone for %v: %d calls", err, rateLimitBackoff, f.count("graphql"))
	}

	// A number in GitLab's answer is not a status: "complexity of 429".
	p, f, _ = newAdapter(t, Options{})
	f.graphql = func([]string) ([]byte, error) {
		msg := "Query has complexity of 429, which exceeds max complexity of 401"
		return nil, exit(`{"errors":[{"message":"`+msg+`"}]}`, "glab: "+msg+"\n")
	}
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); err == nil || errors.Is(err, errRateLimited) {
		t.Errorf("a complexity error is not a rate limit: %v", err)
	}
	if st := hostLine(ctx, p); st.Detail != "glab failed" {
		t.Errorf("complexity error detail %q", st.Detail)
	}
	if _, err := p.Lookup(ctx, selfHosted, project, []int{102}, nil); errors.Is(err, errRateLimited) || f.count("graphql") != 2 {
		t.Errorf("no backoff after a complexity error: %v, %d calls", err, f.count("graphql"))
	}

	p, f, _ = newAdapter(t, Options{})
	f.rest = func(string) ([]byte, error) {
		return nil, exit(`{"message":"404 Project Not Found"}`, "glab: 404 Project Not Found (HTTP 404)")
	}
	res, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil)
	mrs := res.MRs
	if err != nil || len(mrs) == 0 || mrs[0].ClosingKnown || mrs[0].ClosingIssues != nil {
		t.Errorf("a failed closes_issues call leaves the issues unknown: %v %v", mrs, err)
	}
}

func TestRateLimitWait(t *testing.T) {
	exit := func(stderr string) error { return &runner.ExitError{Code: 1, Stderr: []byte(stderr)} }
	for stderr, want := range map[string]time.Duration{
		"glab: HTTP 429\nglab: rate limited, retry after 1m30s\n":             90 * time.Second,
		"glab: HTTP 429\nglab: rate limited by throttle_x, retry after 45s\n": time.Minute,   // at least a minute
		"glab: HTTP 429\nglab: rate limited, retry after 3h0m0s\n":            3 * time.Hour, // however long
		"glab: HTTP 429\nglab: rate limited, retry after 1h0m0s\n":            time.Hour,
		"glab: HTTP 429\n": rateLimitBackoff,
		"glab: HTTP 429\nglab: rate limited, retry after soon\n": rateLimitBackoff,
	} {
		if got := rateLimitWait(exit(stderr)); got != want {
			t.Errorf("rateLimitWait(%q) = %v, want %v", stderr, got, want)
		}
	}
	if got := rateLimitWait(&runner.ExitError{Code: 1, Stdout: []byte("retry after 2m0s")}); got != rateLimitBackoff {
		t.Errorf("standard output is GitLab's answer, not glab's words: %v", got)
	}
	for stderr, want := range map[string]string{
		"glab: HTTP 429\n": "rate limited", "glab: 429 Too Many Requests (HTTP 429)\n": "rate limited",
		"GET https://example.com/api/v4/user: 429 {message: 429 Too Many Requests}": "rate limited",
		"glab: 401 Unauthorized (HTTP 401)\n":                                       "not logged in to " + selfHosted, "glab: HTTP 401\n": "not logged in to " + selfHosted,
		"glab: Query has complexity of 429\n": "glab failed", "glab: 404 Not Found (HTTP 404)\n": "glab failed",
		"glab: field 401 is not known\n": "glab failed",
		// Words are not a verdict: only a status glab named, or its own
		// no-login line, says logged out; a real 401 wins over rate-limit words.
		"glab: Invalid token\n": "glab failed", "glab: unauthorized\n": "glab failed",
		"glab: 401 Unauthorized (HTTP 500)\n":                                        "glab failed",
		"glab: Unauthorized access denied (HTTP 403)\n":                              "glab failed",
		"glab: too many requests (HTTP 502)\n":                                       "glab failed",
		"glab: 401 Unauthorized (HTTP 401)\nglab: rate limited, retry after 1m30s\n": "not logged in to " + selfHosted,
		"glab: Too Many Requests, retry later (HTTP 401)\n":                          "not logged in to " + selfHosted,
		noLoginOutput(selfHosted):                                                    "not logged in to " + selfHosted,
		// Only a line glab starts itself names a status.
		"GitLab said: glab: nope (HTTP 401)\nglab: HTTP 502\n": "glab failed",
	} {
		if got := classify(exit(stderr), selfHosted); got != want {
			t.Errorf("classify(%q) = %q, want %q", stderr, got, want)
		}
	}
	// A closes_issues call reads the same verdict: a 401 is no rate limit.
	if readFailure(exit("glab: 401 Unauthorized (HTTP 401)\nglab: rate limited, retry after 1m30s\n"), selfHosted).rateLimited() {
		t.Error("a 401 with rate-limit words counts as rate limited")
	}
	if got := classify(&runner.ExitError{Code: 1, Stdout: []byte(`{"title":"Fix 429: too many requests"}`)}, selfHosted); got != "glab failed" {
		t.Errorf("standard output is not read: %q", got)
	}
}

// A 429 on a closes_issues call stops the others and leaves the host alone.
func TestLookupClosingStopsOnRateLimit(t *testing.T) {
	p, f, c := newAdapter(t, Options{})
	ctx := context.Background()
	f.rest = func(string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stdout: []byte("Retry later\n"), Stderr: []byte("glab: HTTP 429\nglab: rate limited, retry after 2m0s\n")}
	}
	res, err := p.Lookup(ctx, selfHosted, project, []int{101, 102, 103, 104, 105}, nil)
	mrs := res.MRs
	if err != nil || len(mrs) < 2 || f.count("rest") != 1 {
		t.Fatalf("one closes_issues call, then none: %v, %d merge requests, %d calls", err, len(mrs), f.count("rest"))
	}
	if _, err := p.Lookup(ctx, selfHosted, project, []int{110}, nil); !errors.Is(err, errRateLimited) || f.count("graphql") != 1 {
		t.Errorf("host left alone after a closes_issues 429: %v, %d calls", err, f.count("graphql"))
	}
	c.add(2*time.Minute + time.Second)
	f.rest = func(string) ([]byte, error) { return fixture(t, "closes-issues.json"), nil }
	if _, err := p.Lookup(ctx, selfHosted, project, []int{110}, nil); err != nil || f.count("graphql") != 2 {
		t.Errorf("asked again after the wait: %v", err)
	}

	// One call running out of its own time cools down like any failure: the
	// merge requests after it are still asked, and next time it waits.
	p, f, c = newAdapter(t, Options{})
	f.rest = func(path string) ([]byte, error) {
		if strings.Contains(path, "/merge_requests/101/") {
			return nil, fmt.Errorf("glab: %w", context.DeadlineExceeded)
		}
		return fixture(t, "closes-issues.json"), nil
	}
	res, err = p.Lookup(ctx, selfHosted, project, []int{101, 102, 103}, nil)
	if err != nil || len(res.MRs) < 3 || res.MRs[0].IID != 101 || res.MRs[0].ClosingKnown {
		t.Fatalf("a call out of time: %v %+v", err, res.MRs)
	}
	for _, m := range res.MRs[1:] {
		if !m.ClosingKnown {
			t.Errorf("a call out of time blocked !%d", m.IID)
		}
	}
	c.add(cacheFor + time.Second)
	rest := f.count("rest")
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); err != nil || f.count("rest") != rest {
		t.Errorf("asked again during its cooldown: %v, %d calls", err, f.count("rest")-rest)
	}
	// The caller giving up stops the pass.
	p, f, _ = newAdapter(t, Options{})
	cctx, cancel := context.WithCancel(ctx)
	f.rest = func(string) ([]byte, error) { cancel(); return nil, fmt.Errorf("glab: %w", context.Canceled) }
	if _, err := p.Lookup(cctx, selfHosted, project, []int{101, 102, 103}, nil); err != nil || f.count("rest") != 1 {
		t.Errorf("the caller gave up, the pass went on: %v, %d calls", err, f.count("rest"))
	}
	// ... and gives the merge request no cooldown: it was not its failure.
	f.rest = func(string) ([]byte, error) { return fixture(t, "closes-issues.json"), nil }
	if res, err := p.Lookup(ctx, selfHosted, project, []int{101, 102}, nil); err != nil || !res.MRs[0].ClosingKnown {
		t.Errorf("cooled down after the caller gave up: %v %+v", err, res.MRs)
	}
}

// `glab auth status` running out of time or answering 429 is no answer: it is
// not kept as "not logged in", and a 429 leaves the host alone.
func TestKnownWithoutAnAnswer(t *testing.T) {
	p, f, _ := newUnchecked(t)
	ctx := context.Background()
	f.auth[selfHosted] = fmt.Errorf("glab: %w", context.DeadlineExceeded)
	if p.Known(ctx, selfHosted) {
		t.Fatal("out of time is not known")
	}
	f.auth[selfHosted] = nil
	if !p.Known(ctx, selfHosted) || f.count("auth") != 2 {
		t.Fatalf("asked again after running out of time: %d calls", f.count("auth"))
	}

	// What glab 1.131.0 prints for `auth status --hostname <host>`: its own
	// error, framed and wrapped by its error printer, or one block per host
	// with a line per check (not wrapped).
	block := func(lines ...string) string {
		return selfHosted + "\n  " + strings.Join(lines, "\n  ") + "\n"
	}
	apiFailed := func(status, body string) string {
		return "x " + selfHosted + ": API call failed: GET https://" + selfHosted + "/api/v4/user: " + status + " " + body
	}
	nudge := "! To store this token more securely, run glab auth login --hostname " + selfHosted + " to move it into the operating system keyring."

	// No answer: kept for nothing, asked again at once, and said neutrally.
	for _, c := range []struct {
		err  error
		want string
	}{
		{&runner.ExitError{Code: 1, Stderr: []byte(block("x " + selfHosted + ": could not read the token: the keyring is locked"))}, "auth check failed"},
		{&runner.ExitError{Code: 1, Stderr: []byte(block("x " + selfHosted + ": could not read the token: ! No token found (checked config file, keyring, and environment variables)."))}, "auth check failed"},
		{&runner.ExitError{Code: 1, Stderr: []byte(block(apiFailed("502", "{message: upstream answered 401 }")))}, "auth check failed (HTTP 502)"},
		{&runner.ExitError{Code: 1, Stderr: []byte("x other.example.com has not been authenticated with glab; run `glab auth login --hostname " + selfHosted + "` to authenticate.")}, "auth check failed"},
		{&runner.ExitError{Code: 1, Stderr: []byte(block(apiFailed("502", "{message: invalid token}"), nudge))}, "auth check failed (HTTP 502)"},
		{&runner.ExitError{Code: 1, Stderr: []byte(block(apiFailed("503", "x "+selfHosted+" has not been authenticated with glab; run `glab auth login --hostname "+selfHosted+"` to authenticate.")))}, "auth check failed (HTTP 503)"},
		{&runner.ExitError{Code: 1, Stderr: []byte(noLoginOutput("other.example.com"))}, "auth check failed"},
		{&runner.ExitError{Code: 1, Stderr: []byte("glab: unauthorized: invalid token, run glab auth login\n")}, "auth check failed"},
		{errors.New("exec: no such file"), "auth check failed"},
	} {
		p, f, _ := newUnchecked(t)
		f.auth[selfHosted] = c.err
		_, err := p.host(ctx, selfHosted)
		if !errors.Is(err, errNoAnswer) || err.Error() != errNoAnswer.Error()+": "+c.want {
			t.Errorf("%v: %v, want no answer: %s", c.err, err, c.want)
		}
		f.auth[selfHosted] = nil
		if !p.Known(ctx, selfHosted) || f.count("auth") != 2 {
			t.Errorf("%v: not asked again at once; %d calls", c.err, f.count("auth"))
		}
	}
	// A definite answer is kept: no login in the config, no token, a 401.
	for _, msg := range []string{
		noLoginOutput(selfHosted),
		"\n   ERROR  \n\n  no GitLab instances have been authenticated with glab; run `glab auth login` to\n  authenticate.\n\n",
		block("! No token found (checked config file, keyring, and environment variables)."),
		block(apiFailed("401", "{message: 401 Unauthorized}"), nudge),
	} {
		p, f, _ := newUnchecked(t)
		f.auth[selfHosted] = &runner.ExitError{Code: 1, Stderr: []byte(msg)}
		if h, err := p.host(ctx, selfHosted); err != nil || h.known {
			t.Errorf("%q: %+v %v, want logged out", msg, h, err)
		}
		f.auth[selfHosted] = nil
		if p.Known(ctx, selfHosted) || f.count("auth") != 1 {
			t.Errorf("%q: not kept; %d calls", msg, f.count("auth"))
		}
	}

	// A 502 whose body talks of a rate limit is no answer, asked again at the
	// next check; only glab's own 429 line backs the host off.
	p, f, _ = newUnchecked(t)
	f.auth[selfHosted] = &runner.ExitError{Code: 1, Stderr: []byte(block(apiFailed("502", "{message: 429 Too Many Requests, rate limited, retry after 30m0s}")))}
	if _, err := p.host(ctx, selfHosted); !errors.Is(err, errNoAnswer) || err.Error() != errNoAnswer.Error()+": auth check failed (HTTP 502)" {
		t.Errorf("a 502 with rate-limit words: %v", err)
	}
	f.auth[selfHosted] = nil
	if !p.Known(ctx, selfHosted) || f.count("auth") != 2 {
		t.Errorf("a 502 with rate-limit words backed the host off: %d calls", f.count("auth"))
	}

	p, f, c := newUnchecked(t)
	f.auth[selfHosted] = &runner.ExitError{Code: 1, Stderr: []byte(block(apiFailed("429", "{message: 429 Too Many Requests}")))}
	if p.Known(ctx, selfHosted) {
		t.Fatal("a 429 is not known")
	}
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); !errors.Is(err, source.ErrNotChecked) || errors.Is(err, source.ErrNotLoggedIn) || f.count("graphql") != 0 {
		t.Fatalf("after a 429 the host waits for its next check, not logged out: %v", err)
	}
	f.auth[selfHosted] = nil
	if p.Known(ctx, selfHosted) || f.count("auth") != 1 {
		t.Fatalf("not asked again during the wait: %d calls", f.count("auth"))
	}
	c.add(rateLimitBackoff + time.Second)
	if !p.Known(ctx, selfHosted) || f.count("auth") != 2 {
		t.Fatalf("asked again after the wait: %d calls", f.count("auth"))
	}
	// Two 429s at once: the longer wait is kept.
	long := &runner.ExitError{Code: 1, Stderr: []byte("glab: HTTP 429\nglab: rate limited, retry after 30m0s\n")}
	p.limited(selfHosted, long)
	p.limited(selfHosted, &runner.ExitError{Code: 1, Stderr: []byte("glab: HTTP 429\n")})
	if got := p.backoff[selfHosted].Sub(c.now()); got != 30*time.Minute {
		t.Errorf("left alone for %v, want the longer 30m", got)
	}
}

func TestLookupAsksOnlyKnownHosts(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	ctx := context.Background()
	if _, err := p.Lookup(ctx, "other.example.com", project, []int{1}, nil); err == nil || f.count("graphql") != 0 {
		t.Fatalf("a host glab has no login for is never asked: %v", err)
	}
	if _, err := p.Lookup(ctx, "github.com", project, []int{1}, nil); err == nil || f.count("graphql") != 0 {
		t.Fatal("github.com is never asked")
	}
	for _, bad := range []struct{ host, path string }{{"example.com:22", project}, {selfHosted, "x"}, {selfHosted, "-x/y"}} {
		if _, err := p.Lookup(ctx, bad.host, bad.path, []int{1}, nil); err == nil {
			t.Errorf("Lookup(%q, %q) accepted", bad.host, bad.path)
		}
	}
	if _, err := p.Lookup(ctx, GitLabCom, project, []int{1}, nil); !errors.Is(err, errNotChecked) || len(f.calls) != 0 {
		t.Fatalf("gitlab.com never checked is asked about a project: %v, %q", err, f.calls)
	}
	if p.Known(ctx, GitLabCom) {
		t.Fatal("gitlab.com without a login is known")
	}
	if _, err := p.Lookup(ctx, GitLabCom, project, []int{1}, nil); !errors.Is(err, errNotKnown) || f.count("graphql") != 0 {
		t.Fatalf("gitlab.com without a login is asked about a project: %v, %d calls", err, f.count("graphql"))
	}
	off, fo, _ := newAdapter(t, Options{Off: true})
	if res, err := off.Lookup(ctx, GitLabCom, project, []int{1}, nil); res.MRs != nil || !errors.Is(err, errNotAsked) || len(fo.calls) != 0 {
		t.Fatal("switched off: no call")
	}
}

func TestProbe(t *testing.T) {
	ctx := context.Background()
	p, _, _ := newAdapter(t, Options{})
	if st := p.Probe(ctx); st.State != source.StateOK || st.Version != "1.131.0" || !st.Tested {
		t.Errorf("probe %+v", st)
	}
	p, f, _ := newAdapter(t, Options{})
	p.found = func(string) string { return "" }
	if st := p.Probe(ctx); st.State != source.StateMissing || st.Detail != "glab not found" || len(f.calls) != 0 {
		t.Errorf("missing glab: %+v", st)
	}
	if _, err := p.Lookup(ctx, GitLabCom, project, []int{1}, nil); err == nil || len(f.calls) != 0 {
		t.Error("missing glab: lookup fails without a call")
	}
	off, _, _ := newAdapter(t, Options{Off: true})
	if st := off.Probe(ctx); st.State != source.StateOff {
		t.Errorf("off: %+v", st)
	}
	for in, want := range map[string]string{"glab 1.131.0 (206ebab4f)\n": "1.131.0", "glab version 1.36.0 (2024-01-01)": "1.36.0", "": "", "other 1": ""} {
		if got := parseVersion(in); got != want {
			t.Errorf("parseVersion(%q) = %q", in, got)
		}
	}
}

// A Community Edition instance has no approvalsLeft or approvalsRequired and
// refuses a query that asks for them. The lookup is asked again without
// them, the host is remembered, and the approval state is unknown.
// Another lookup's 429 recorded while this one runs: no further GraphQL
// call starts, neither the next batch nor the retry without approvals.
func TestLookupChecksBackoffBeforeEveryCall(t *testing.T) {
	ctx := context.Background()
	rateLimited := &runner.ExitError{Code: 1, Stderr: []byte("glab: HTTP 429\nglab: rate limited, retry after 2m0s\n")}
	p, f, _ := newAdapter(t, Options{})
	f.graphql = func([]string) ([]byte, error) {
		p.limited(selfHosted, rateLimited) // another lookup met a 429
		return fixture(t, "lookup.json"), nil
	}
	var many []int
	for i := 1; i <= maxIIDs+1; i++ {
		many = append(many, i)
	}
	if res, err := p.Lookup(ctx, selfHosted, project, many, nil); !errors.Is(err, errRateLimited) || res.MRs != nil || f.count("graphql") != 1 {
		t.Errorf("next batch after a 429: %v, %d calls", err, f.count("graphql"))
	}

	p, f, _ = newAdapter(t, Options{})
	ce := fixture(t, "no-approval-fields.json")
	f.graphql = func([]string) ([]byte, error) {
		p.limited(selfHosted, rateLimited)
		return nil, &runner.ExitError{Code: 1, Stdout: ce, Stderr: []byte("glab: x\n")}
	}
	if res, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); !errors.Is(err, errRateLimited) || res.MRs != nil || f.count("graphql") != 1 {
		t.Errorf("approval retry after a 429: %v, %d calls", err, f.count("graphql"))
	}
}

// An overlapping lookup saw the project gone while this one was asking: this
// one's older answer is not cached over that.
func TestLookupDoesNotCacheOverAnInvalidation(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	f.rest = func(string) ([]byte, error) {
		p.forget(selfHosted, project) // the other lookup met a null project
		return fixture(t, "closes-issues.json"), nil
	}
	if _, err := p.Lookup(context.Background(), selfHosted, project, []int{101}, nil); err != nil {
		t.Fatal(err)
	}
	if len(p.cache) != 0 {
		t.Errorf("an answer older than the invalidation was cached: %d", len(p.cache))
	}
	f.rest = func(string) ([]byte, error) { return fixture(t, "closes-issues.json"), nil }
	if _, err := p.Lookup(context.Background(), selfHosted, project, []int{102}, nil); err != nil || len(p.cache) != 1 {
		t.Errorf("a lookup after the invalidation is cached: %v, %d", err, len(p.cache))
	}
}

func TestLookupWithoutApprovalFields(t *testing.T) {
	p, f, c := newAdapter(t, Options{})
	ctx := context.Background()
	ce := fixture(t, "no-approval-fields.json")
	f.graphql = func(args []string) ([]byte, error) {
		for _, a := range args {
			if strings.HasPrefix(a, "query=") && (strings.Contains(a, "approvalsLeft") || strings.Contains(a, "approvalsRequired")) {
				return nil, &runner.ExitError{Code: 1, Stdout: ce, Stderr: []byte("glab: Field 'approvalsLeft' doesn't exist on type 'MergeRequest'\n")}
			}
		}
		return fixture(t, "lookup.json"), nil
	}
	res, err := p.Lookup(ctx, selfHosted, project, []int{101, 102}, nil)
	mrs := res.MRs
	if err != nil || len(mrs) == 0 || f.count("graphql") != 2 {
		t.Fatalf("asked again without the approval fields: %v, %d merge requests, %d calls", err, len(mrs), f.count("graphql"))
	}
	for _, m := range mrs {
		if m.Approval != ApprovalUnknown {
			t.Errorf("!%d approval %q, want unknown", m.IID, m.Approval)
		}
	}
	if _, err := p.Lookup(ctx, selfHosted, project, []int{103}, nil); err != nil || f.count("graphql") != 3 {
		t.Fatalf("the host is remembered: one call, %v, %d calls", err, f.count("graphql"))
	}
	f.auth[GitLabCom] = nil // gitlab.com needs a login like any host
	approve(t, p, GitLabCom)
	if _, err := p.Lookup(ctx, GitLabCom, project, []int{101}, nil); err != nil || f.count("graphql") != 5 {
		t.Fatalf("only that host: %v, %d calls", err, f.count("graphql"))
	}
	c.add(approvalsRecheck + time.Second)
	approve(t, p, selfHosted)
	if _, err := p.Lookup(ctx, selfHosted, project, []int{104}, nil); err != nil || f.count("graphql") != 7 {
		t.Fatalf("asked with approvals again after a day (an upgrade): %v, %d calls", err, f.count("graphql"))
	}
}

// node is one merge request as GitLab's GraphQL answers it.
func node(iid int, branch string, src, proj int, state string) string {
	return fmt.Sprintf(`{"iid":"%d","title":"Placeholder","sourceBranch":%q,"sourceProjectId":%d,"projectId":%d,"state":%q,`+
		`"draft":false,"createdAt":"2026-11-19T12:00:00Z","closedAt":null,"mergedAt":null,"approved":true,"approvalsLeft":0,"approvalsRequired":0,"headPipeline":null}`,
		iid, branch, src, proj, state)
}

func answerOf(m, b []string) []byte {
	var parts []string
	if m != nil {
		parts = append(parts, `"m":{"nodes":[`+strings.Join(m, ",")+`]}`)
	}
	if b != nil {
		parts = append(parts, `"b":{"nodes":[`+strings.Join(b, ",")+`]}`)
	}
	return []byte(`{"data":{"project":{` + strings.Join(parts, ",") + `}}}`)
}

// A project the account can no longer see drops its cached answers, and no
// old answer comes back while the host is rate limited.
func TestLookupNeverAnswersFromAnOldCache(t *testing.T) {
	p, f, c := newAdapter(t, Options{})
	ctx := context.Background()
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); err != nil {
		t.Fatal(err)
	}
	c.add(cacheFor + time.Second)
	f.graphql = ok(fixture(t, "project-null.json"))
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); !errors.Is(err, source.ErrRepoNotFound) {
		t.Fatalf("lost access: %v", err)
	}
	if len(p.cache) != 0 {
		t.Errorf("the lost project's answers are still cached: %d", len(p.cache))
	}

	p, f, c = newAdapter(t, Options{})
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); err != nil {
		t.Fatal(err)
	}
	c.add(cacheFor + time.Second)
	f.graphql = func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stderr: []byte("glab: HTTP 429\nglab: rate limited, retry after 2m0s\n")}
	}
	if res, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); !errors.Is(err, errRateLimited) || res.MRs != nil {
		t.Errorf("a 429 gave the old answer: %v %v", res, err)
	}
	if res, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); !errors.Is(err, errRateLimited) || res.MRs != nil {
		t.Errorf("the backoff gave the old answer: %v %v", res, err)
	}
}

// GitLab's partial answers: a field error on a pipeline makes that check
// unknown, never "no pipeline"; any other error, or a list asked for and
// missing, makes the whole lookup a failure.
func TestPartialAnswers(t *testing.T) {
	q := shape{iids: true, approvals: true}
	pa, err := parseAnswer(fixture(t, "partial-errors.json"), selfHosted, project, q)
	if err != nil || len(pa.mrs) == 0 || pa.mrs[0].mr.Checks != model.ChecksUnknown {
		t.Fatalf("pipeline error: %v %+v", err, pa.mrs)
	}
	withErr := func(path string) []byte {
		return []byte(`{"errors":[{"message":"x","path":` + path + `}],"data":{"project":{"m":{"nodes":[` + node(1, "a", 1, 1, "opened") + `]}}}}`)
	}
	if pa, err := parseAnswer(withErr(`["project","m","nodes",0,"approvalsLeft"]`), selfHosted, project, q); err != nil || pa.mrs[0].mr.Approval != ApprovalUnknown {
		t.Errorf("approval error: %v %+v", err, pa.mrs)
	}
	for _, path := range []string{`["project","m","nodes",0,"title"]`, `["project","m"]`, `["project"]`, `["project","m","nodes",0]`, `null`} {
		if _, err := parseAnswer(withErr(path), selfHosted, project, q); !errors.Is(err, errIncomplete) {
			t.Errorf("error at %s: %v, want incomplete", path, err)
		}
	}
	// An error on the non-null approved field nulls the whole node (or the
	// list): the number asked for would read as absent.
	for _, body := range []string{
		`{"errors":[{"message":"x","path":["project","m","nodes",0,"approved"]}],"data":{"project":{"m":{"nodes":[null]}}}}`,
		`{"errors":[{"message":"x","path":["project","m","nodes",0,"approved"]}],"data":{"project":{"m":{"nodes":null}}}}`,
		`{"errors":[{"message":"x","path":["project","m","nodes",1,"headPipeline"]}],"data":{"project":{"m":{"nodes":[` + node(1, "a", 1, 1, "opened") + `]}}}}`,
		`{"errors":[{"message":"x","path":["project","m","nodes",-1,"headPipeline"]}],"data":{"project":{"m":{"nodes":[` + node(1, "a", 1, 1, "opened") + `]}}}}`,
		`{"errors":[{"message":"x","path":["project","b","nodes",0,"approved"]}],"data":{"project":{"m":{"nodes":[` + node(1, "a", 1, 1, "opened") + `]},"b":{"nodes":[]}}}}`,
	} {
		if _, err := parseAnswer([]byte(body), selfHosted, project, q); !errors.Is(err, errIncomplete) {
			t.Errorf("an errored node that is gone: %v, want incomplete: %s", err, body)
		}
	}
	if _, err := parseAnswer([]byte(`{"errors":[{"message":"x"}],"data":{"project":null}}`), selfHosted, project, q); !errors.Is(err, errIncomplete) {
		t.Errorf("a null project with errors is not a missing project: %v", err)
	}
	if _, err := parseAnswer(answerOf([]string{}, nil), selfHosted, project, shape{iids: true, branches: true}); !errors.Is(err, errIncomplete) {
		t.Errorf("branches asked, no branch list: %v", err)
	}
	p, f, _ := newAdapter(t, Options{})
	f.graphql = func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stdout: withErr(`["project","m","nodes",0,"title"]`), Stderr: []byte("glab: x\n")}
	}
	if res, err := p.Lookup(context.Background(), selfHosted, project, []int{1}, nil); !errors.Is(err, errIncomplete) || res.MRs != nil {
		t.Errorf("Lookup of an incomplete answer: %v %v", res, err)
	}
}

// A lookup never checks a login itself, so the board's loop never waits for
// `glab auth status`: a host never checked, or whose yes ran out, gets no
// call and errNotChecked. The host's check renews a yes before lookups stop
// trusting it, and says why it has no answer.
func TestLookupNeverChecksTheLogin(t *testing.T) {
	ctx := context.Background()
	p, f, c := newAdapter(t, Options{})
	for _, h := range []string{GitLabCom, "other.example.com"} {
		f.auth[h] = nil
		if _, err := p.Lookup(ctx, h, project, []int{1}, nil); !errors.Is(err, source.ErrNotChecked) || len(f.calls) != 0 {
			t.Fatalf("%s never checked: %v, calls %q", h, err, f.calls)
		}
	}
	c.add(renewAfter)
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); err != nil || f.count("auth") != 0 {
		t.Fatalf("a yes renewAfter old: %v, %d auth calls", err, f.count("auth"))
	}
	approve(t, p, selfHosted)
	if f.count("auth") != 1 {
		t.Fatalf("the check renews a yes renewAfter old: %d auth calls", f.count("auth"))
	}
	c.add(knownFor)
	calls := len(f.calls)
	if _, err := p.Lookup(ctx, selfHosted, project, []int{102}, nil); !errors.Is(err, source.ErrNotChecked) || len(f.calls) != calls {
		t.Fatalf("a yes that ran out: %v, %d calls", err, len(f.calls)-calls)
	}
	approve(t, p, selfHosted)
	if _, err := p.Lookup(ctx, selfHosted, project, []int{102}, nil); err != nil {
		t.Fatalf("after the check renewed it: %v", err)
	}

	p, f, _ = newUnchecked(t)
	f.auth[selfHosted] = fmt.Errorf("glab: %w", context.DeadlineExceeded)
	if _, err := p.host(ctx, selfHosted); !errors.Is(err, errNoAnswer) || errors.Is(err, errNotKnown) {
		t.Errorf("auth check out of time: %v", err)
	}
	p, f, _ = newUnchecked(t)
	f.auth[selfHosted] = &runner.ExitError{Code: 1, Stderr: []byte(selfHosted + "\n  x " + selfHosted + ": API call failed: GET https://" + selfHosted + "/api/v4/user: 429 {message: 429 Too Many Requests}\n")}
	if _, err := p.host(ctx, selfHosted); !errors.Is(err, errRateLimited) {
		t.Errorf("auth check 429: %v", err)
	}
	if st := hostLine(ctx, p); st.Detail != "rate limited" {
		t.Errorf("auth check 429 detail %q", st.Detail)
	}
	p, f, _ = newAdapter(t, Options{})
	f.rest = func(string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stderr: []byte("glab: HTTP 429\n")}
	}
	if res, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); err != nil || len(res.MRs) == 0 {
		t.Fatalf("merge requests kept after a closes_issues 429: %v %v", res, err)
	}
	if st := hostLine(ctx, p); st.State != source.StateFailing || st.Detail != "rate limited" {
		t.Errorf("a closes_issues 429 is not healthy: %+v", st)
	}
}

// After a 429 met by another lookup on the host, no new call starts.
func TestClosingStopsOnAnotherLookupsRateLimit(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	f.rest = func(string) ([]byte, error) {
		p.limited(selfHosted, &runner.ExitError{Code: 1, Stderr: []byte("glab: HTTP 429\n")}) // another lookup's 429
		return fixture(t, "closes-issues.json"), nil
	}
	if _, err := p.Lookup(context.Background(), selfHosted, project, []int{101, 102, 103}, nil); err != nil || f.count("rest") != 1 {
		t.Errorf("calls after another lookup's 429: %v, %d", err, f.count("rest"))
	}
}

// closes_issues pages: an issue of the project on page two counts; a list
// longer than closingMaxPages pages is unknown.
func TestClosingPages(t *testing.T) {
	other := make([]string, closingPerPage)
	for i := range other {
		other[i] = fmt.Sprintf(`{"iid":%d,"project_id":2000}`, i+1)
	}
	full := []byte("[" + strings.Join(other, ",") + "]")
	p, f, _ := newAdapter(t, Options{})
	f.graphql = ok(answerOf([]string{node(1, "a", 1000, 1000, "merged")}, nil))
	f.rest = func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "&page=1") {
			return full, nil
		}
		return []byte(`[{"iid":5,"project_id":1000}]`), nil
	}
	res, err := p.Lookup(context.Background(), selfHosted, project, []int{1}, nil)
	if err != nil || len(res.MRs) != 1 || !res.MRs[0].ClosingKnown || !slices.Equal(res.MRs[0].ClosingIssues, []int{5}) || f.count("rest") != 2 {
		t.Fatalf("page two: %v %+v, %d calls", err, res.MRs, f.count("rest"))
	}
	p, f, _ = newAdapter(t, Options{})
	f.graphql = ok(answerOf([]string{node(1, "a", 1000, 1000, "merged")}, nil))
	f.rest = func(string) ([]byte, error) { return full, nil }
	res, err = p.Lookup(context.Background(), selfHosted, project, []int{1}, nil)
	if err != nil || res.MRs[0].ClosingKnown || f.count("rest") != closingMaxPages {
		t.Fatalf("too many pages: %v %+v, %d calls", err, res.MRs, f.count("rest"))
	}
}

// Every number asked is asked: more than 50 make more calls, never fewer
// numbers. A full branch page says the branches may have more.
func TestLookupBatchesAndBranchPage(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	f.rest = func(string) ([]byte, error) { return []byte(`[]`), nil }
	asked := map[string]bool{}
	f.graphql = func(args []string) ([]byte, error) {
		for _, a := range args {
			if q, ok := strings.CutPrefix(a, "query="); ok {
				ids := regexp.MustCompile(`"(\d+)"`).FindAllStringSubmatch(q, -1)
				if len(ids) > maxIIDs {
					t.Errorf("%d numbers in one call", len(ids))
				}
				for _, m := range ids {
					asked[m[1]] = true
				}
			}
		}
		return answerOf([]string{}, nil), nil
	}
	nums := make([]int, 120)
	for i := range nums {
		nums[i] = i + 1
	}
	if _, err := p.Lookup(context.Background(), selfHosted, project, nums, nil); err != nil || f.count("graphql") != 3 || len(asked) != 120 {
		t.Fatalf("120 numbers: %v, %d calls, %d asked", err, f.count("graphql"), len(asked))
	}
	var b []string
	for i := range branchFirst {
		b = append(b, node(i+1, "main", 2000+i, 1000, "opened")) // every one from a fork
	}
	p, f, _ = newAdapter(t, Options{})
	f.graphql = ok(answerOf(nil, b))
	res, err := p.Lookup(context.Background(), selfHosted, project, nil, []string{"main"})
	if err != nil || res.BranchesComplete || len(res.MRs) != 0 {
		t.Fatalf("a full page of forks: %v complete=%v %d", err, res.BranchesComplete, len(res.MRs))
	}
	p, f, _ = newAdapter(t, Options{})
	f.graphql = ok(answerOf(nil, b[:3]))
	if res, err := p.Lookup(context.Background(), selfHosted, project, nil, []string{"main"}); err != nil || !res.BranchesComplete {
		t.Fatalf("a short page is complete: %v %v", res, err)
	}
}

// A fork's merge request asked by number says it is from a fork, so it is
// never taken for the local branch of the same name.
func TestForkProvenance(t *testing.T) {
	pa, err := parseAnswer(answerOf([]string{node(9, "main", 2000, 1000, "opened")}, []string{node(10, "main", 1000, 1000, "opened")}),
		selfHosted, project, shape{iids: true, branches: true, approvals: true})
	if err != nil || len(pa.mrs) != 2 || !pa.mrs[0].mr.FromFork || pa.mrs[1].mr.FromFork {
		t.Fatalf("fork flags: %v %+v", err, pa.mrs)
	}
}

// GitLab under a subfolder is not supported yet: said so, not asked.
func TestLookupSubfolderHost(t *testing.T) {
	p, f, _ := newUnchecked(t)
	f.sub = map[string]string{selfHosted: "gitlab"}
	approve(t, p, selfHosted)
	if _, err := p.Lookup(context.Background(), selfHosted, project, []int{1}, nil); !errors.Is(err, errSubfolder) || errors.Is(err, source.ErrRepoNotFound) || f.count("graphql") != 0 {
		t.Errorf("subfolder host: %v, %d calls", err, f.count("graphql"))
	}
	if !p.Known(context.Background(), selfHosted) {
		t.Error("the host is still one glab is logged in to")
	}
}

// When a merge request's state changes and the refresh of its closing issues
// fails, the old list is not passed off as current.
func TestClosingFailedRefreshIsUnknown(t *testing.T) {
	p, f, c := newAdapter(t, Options{})
	ctx := context.Background()
	f.graphql = ok(answerOf([]string{node(1, "a", 1000, 1000, "opened")}, nil))
	if res, err := p.Lookup(ctx, selfHosted, project, []int{1}, nil); err != nil || !res.MRs[0].ClosingKnown {
		t.Fatalf("first: %v %v", res, err)
	}
	c.add(cacheFor + time.Second)
	f.graphql = ok(answerOf([]string{node(1, "a", 1000, 1000, "merged")}, nil))
	f.rest = func(string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stderr: []byte("glab: HTTP 500\n")}
	}
	if res, err := p.Lookup(ctx, selfHosted, project, []int{1}, nil); err != nil || res.MRs[0].ClosingKnown || res.MRs[0].ClosingIssues != nil {
		t.Fatalf("failed refresh after a state change: %v %+v", err, res.MRs)
	}
}

// Merge requests whose closes_issues calls fail wait their turn, so the
// others get asked.
func TestClosingFailuresDoNotStarveOthers(t *testing.T) {
	p, f, c := newAdapter(t, Options{})
	ctx := context.Background()
	var m []string
	nums := make([]int, maxClosingCalls+2)
	for i := range nums {
		nums[i] = i + 1
		m = append(m, node(i+1, "a", 1000, 1000, "merged"))
	}
	f.graphql = ok(answerOf(m, nil))
	f.rest = func(path string) ([]byte, error) {
		for i := 1; i <= maxClosingCalls; i++ {
			if strings.Contains(path, "/merge_requests/"+itoa(i)+"/") {
				return nil, &runner.ExitError{Code: 1, Stderr: []byte("glab: HTTP 500\n")}
			}
		}
		return []byte(`[]`), nil
	}
	if _, err := p.Lookup(ctx, selfHosted, project, nums, nil); err != nil {
		t.Fatal(err)
	}
	c.add(cacheFor + time.Second)
	res, err := p.Lookup(ctx, selfHosted, project, nums, nil)
	if err != nil {
		t.Fatal(err)
	}
	last := res.MRs[len(res.MRs)-1]
	if !last.ClosingKnown {
		t.Errorf("!%d never got its turn: %d rest calls", last.IID, f.count("rest"))
	}
}

// A GraphQL 401 is "not logged in" to the core, and the yes kept for the host
// goes: no other project there is asked about, and the "no" is kept a minute.
func TestLookup401IsNotLoggedIn(t *testing.T) {
	ctx := context.Background()
	p, f, _ := newAdapter(t, Options{})
	f.graphql = func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stdout: []byte(`{"message":"401 Unauthorized"}`), Stderr: []byte("glab: 401 Unauthorized (HTTP 401)")}
	}
	_, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil)
	if !errors.Is(err, source.ErrNotLoggedIn) {
		t.Fatalf("401: %v, want not logged in", err)
	}
	auths, calls := f.count("auth"), f.count("graphql")
	if _, err := p.Lookup(ctx, selfHosted, "example-group/other", []int{1}, nil); !errors.Is(err, source.ErrNotLoggedIn) ||
		f.count("auth") != auths || f.count("graphql") != calls {
		t.Fatalf("after a 401: %v, %d auth and %d graphql calls more", err, f.count("auth")-auths, f.count("graphql")-calls)
	}
}

// Failures are kept per host: a host whose login check met a 429 shows it,
// and another host answering afterwards does not clear it.
func TestFailingIsPerHost(t *testing.T) {
	ctx := context.Background()
	p, f, _ := newAdapter(t, Options{})
	const other = "other.example.com"
	f.auth[other] = &runner.ExitError{Code: 1, Stderr: []byte(other + "\n  x " + other + ": API call failed: GET https://" + other + "/api/v4/user: 429 {message: 429 Too Many Requests}\n")}
	if _, err := p.host(ctx, other); !errors.Is(err, errRateLimited) {
		t.Fatalf("auth 429: %v", err)
	}
	f.graphql = ok(fixture(t, "lookup.json"))
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); err != nil {
		t.Fatal(err)
	}
	if got := p.Failing(other); got != noteRateLimited {
		t.Errorf("%s after another host answered: %q", other, got)
	}
	if got := p.Failing(selfHosted); got != "" {
		t.Errorf("%s answered: %q", selfHosted, got)
	}
}

// A lookup's failure is a logout only on a status glab named (401) or its
// own no-login line: "unauthorized" in another failure keeps the host's
// yes, and a real 401 wins over rate-limit words, leaving no backoff.
func TestLogoutOnlyFromGlabsOwnVerdict(t *testing.T) {
	ctx := context.Background()
	p, f, _ := newAdapter(t, Options{})
	exit := func(stderr string) func([]string) ([]byte, error) {
		return func([]string) ([]byte, error) { return nil, &runner.ExitError{Code: 1, Stderr: []byte(stderr)} }
	}
	f.graphql = exit("glab: Unauthorized: project access denied (HTTP 403)\n")
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); err == nil || errors.Is(err, source.ErrNotLoggedIn) {
		t.Fatalf("403 with \"unauthorized\": %v, want a failure that is no logout", err)
	}
	f.graphql = ok(fixture(t, "lookup.json"))
	if _, err := p.Lookup(ctx, selfHosted, "example-group/other", []int{1}, nil); err != nil {
		t.Fatalf("the host's yes went with a 403: %v", err)
	}
	f.graphql = exit("glab: 401 Unauthorized (HTTP 401)\nglab: rate limited, retry after 1m30s\n")
	if _, err := p.Lookup(ctx, selfHosted, "example-group/third", []int{1}, nil); !errors.Is(err, source.ErrNotLoggedIn) || errors.Is(err, source.ErrRateLimited) {
		t.Fatalf("401 with rate-limit words: %v, want not logged in", err)
	}
	if p.backingOff(selfHosted) || p.Failing(selfHosted) != noteLoggedOut(selfHosted) {
		t.Fatalf("401 with rate-limit words: backoff %v, line %q", p.backingOff(selfHosted), p.Failing(selfHosted))
	}
}

// One `glab auth status` runs at a time: cold checks of other hosts wait for
// the first, and when it says glab is logged in nowhere they run none. That
// answer also overrules the yes kept for a host before: no lookup goes there.
func TestOneLoginCheckAtATime(t *testing.T) {
	ctx := context.Background()
	p, f, c := newAdapter(t, Options{})
	s := &Source{p: p}
	nowhere := &runner.ExitError{Code: 1, Stderr: []byte("ERROR\n no GitLab instances have been authenticated with glab; run `glab auth login` to authenticate.\n")}
	hosts := []string{"a.example.com", "b.example.com", "c.example.com", "d.example.com"}
	for _, h := range hosts {
		f.auth[h] = nowhere
	}
	release := make(chan struct{})
	f.authGate = func(string) { <-release }
	errs := make(chan error, len(hosts))
	for _, h := range hosts {
		go func() { errs <- s.Host(ctx, h) }()
	}
	waitUntil(t, "one check runs and the others wait for it", func() bool {
		p.mu.Lock()
		waiting := p.authWaiting
		p.mu.Unlock()
		return f.count("auth") == 1 && waiting == len(hosts)-1
	})
	close(release)
	for range hosts {
		if err := <-errs; !errors.Is(err, source.ErrNoLogins) || !errors.Is(err, source.ErrNotLoggedIn) {
			t.Errorf("logged in nowhere: %v", err)
		}
	}
	if f.count("auth") != 1 || f.maxAuth != 1 {
		t.Fatalf("%d auth calls, %d at once; want 1", f.count("auth"), f.maxAuth)
	}
	calls := len(f.calls)
	if err := s.Host(ctx, selfHosted); !errors.Is(err, source.ErrNoLogins) {
		t.Errorf("a yes kept from before: %v", err)
	}
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); !errors.Is(err, source.ErrNotLoggedIn) {
		t.Errorf("lookup on a host said yes before: %v", err)
	}
	if len(f.calls) != calls {
		t.Fatalf("glab ran after it said it is logged in nowhere: %q", f.calls[calls:])
	}
	// A minute on, the old yes does not come back: the host is checked anew.
	c.add(unknownFor)
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); !errors.Is(err, source.ErrNotChecked) || len(f.calls) != calls {
		t.Fatalf("a minute after logged in nowhere: %v, %d calls", err, len(f.calls)-calls)
	}
}

// waitUntil polls cond until it holds, failing the test after five seconds.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("not reached: %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// hostArg is the --hostname of a glab call.
func hostArg(args []string) string {
	if i := slices.Index(args, "--hostname"); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// unauthorized is glab's answer when the instance refuses its login.
var unauthorized = &runner.ExitError{Code: 1, Stdout: []byte(`{"message":"401 Unauthorized"}`), Stderr: []byte("glab: 401 Unauthorized (HTTP 401)\n")}

// A confirmed logout drops what the adapter kept from that host: after a new
// login nothing is served from what was asked under the old one, closing
// issues included, while another host keeps its answers. So does glab
// saying it is logged in nowhere, for every host.
func TestLogoutForgetsTheHostsAnswers(t *testing.T) {
	ctx := context.Background()
	p, f, c := newAdapter(t, Options{})
	const other = "other.example.com"
	f.auth[other] = nil
	approve(t, p, other)
	asks := 0 // closes_issues calls of a lookup that knows none
	for _, h := range []string{selfHosted, other} {
		rest := f.count("rest")
		if _, err := p.Lookup(ctx, h, project, []int{101, 102}, nil); err != nil {
			t.Fatalf("%s: %v", h, err)
		}
		asks = f.count("rest") - rest
	}
	if asks == 0 {
		t.Fatal("the fixture closes no issues")
	}
	good := f.graphql
	f.graphql = func(args []string) ([]byte, error) {
		if hostArg(args) == selfHosted {
			return nil, unauthorized
		}
		return good(args)
	}
	if _, err := p.Lookup(ctx, selfHosted, "example-group/other", []int{1}, nil); !errors.Is(err, source.ErrNotLoggedIn) {
		t.Fatalf("401: %v", err)
	}
	f.graphql = good
	relogin(t, p, selfHosted) // at once: the cached answers have not run out
	gq, rest := f.count("graphql"), f.count("rest")
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101, 102}, nil); err != nil || f.count("graphql") != gq+1 || f.count("rest") != rest+asks {
		t.Fatalf("after a new login: %v, %d graphql and %d rest calls, want 1 and %d", err, f.count("graphql")-gq, f.count("rest")-rest, asks)
	}
	rest = f.count("rest")
	if _, err := p.Lookup(ctx, other, project, []int{101, 102, 103}, nil); err != nil || f.count("rest") != rest {
		t.Fatalf("%s lost its closing issues with the other host's logout: %v, %d rest calls", other, err, f.count("rest")-rest)
	}

	// Logged in nowhere: every host's answers go.
	nowhere := &runner.ExitError{Code: 1, Stderr: []byte("ERROR\n no GitLab instances have been authenticated with glab; run `glab auth login` to authenticate.\n")}
	f.auth["third.example.com"] = nowhere
	if p.Known(ctx, "third.example.com") {
		t.Fatal("logged in nowhere is known")
	}
	f.auth["third.example.com"] = nil
	relogin(t, p, other)
	gq, rest = f.count("graphql"), f.count("rest")
	if _, err := p.Lookup(ctx, other, project, []int{101, 102, 103}, nil); err != nil || f.count("graphql") != gq+1 || f.count("rest") != rest+asks {
		t.Fatalf("after logged in nowhere and a new login: %v, %d graphql and %d rest calls, want 1 and %d", err, f.count("graphql")-gq, f.count("rest")-rest, asks)
	}
	if c.now().Sub(newClock().now()) >= cacheFor {
		t.Fatal("the clock passed the cache's lifetime: the test proves nothing")
	}
}

// relogin is a new login on host noticed at once, without the clock moving:
// the no (or logged in nowhere) kept is forgotten and host's check runs.
func relogin(t *testing.T, p *Provider, host string) {
	t.Helper()
	p.mu.Lock()
	p.nowhere = time.Time{}
	delete(p.hosts, host)
	p.mu.Unlock()
	approve(t, p, host)
}

// What a lookup or a check started before a logout learns does not outlive
// it: a lookup's answer is not cached (closing issues included), and an
// older yes from `glab auth status` does not undo the newer 401.
func TestOlderAnswersDoNotOutliveALogout(t *testing.T) {
	ctx := context.Background()
	p, f, c := newAdapter(t, Options{})
	good := f.graphql
	logout := func() {
		f.graphql = func([]string) ([]byte, error) { return nil, unauthorized }
		if _, err := p.Lookup(ctx, selfHosted, "example-group/other", []int{1}, nil); !errors.Is(err, source.ErrNotLoggedIn) {
			t.Errorf("401: %v", err)
		}
		f.graphql = good
	}
	once := sync.Once{}
	f.rest = func(string) ([]byte, error) {
		once.Do(logout) // the login goes while this lookup asks for closing issues
		return fixture(t, "closes-issues.json"), nil
	}
	rest := f.count("rest")
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); err != nil {
		t.Fatalf("the older lookup: %v", err)
	}
	asks := f.count("rest") - rest
	relogin(t, p, selfHosted) // at once: a cached answer would still be served
	gq, rest := f.count("graphql"), f.count("rest")
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); err != nil || f.count("graphql") != gq+1 || f.count("rest") != rest+asks {
		t.Fatalf("served what a lookup older than the logout learned: %v, %d graphql and %d rest calls", err, f.count("graphql")-gq, f.count("rest")-rest)
	}

	// The check renewing the yes is in flight when a lookup meets a 401.
	c.add(renewAfter)
	f.authGate = func(string) { logout() }
	if p.Known(ctx, selfHosted) {
		t.Fatal("an older yes undid a newer 401")
	}
	f.authGate = nil
	calls := len(f.calls)
	if _, err := p.Lookup(ctx, selfHosted, project, []int{102}, nil); !errors.Is(err, source.ErrNotLoggedIn) || len(f.calls) != calls {
		t.Fatalf("after the 401: %v, %d calls", err, len(f.calls)-calls)
	}
}

// A host check that gets no answer shows on the host's line until a check
// answers; a rate-limit note goes when the host's wait ends. Another host's
// line stays clear throughout, and a caller giving up is no failure.
func TestHostCheckFailuresShow(t *testing.T) {
	ctx := context.Background()
	p, f, c := newAdapter(t, Options{})
	const other = "other.example.com"
	line := func(want string) {
		t.Helper()
		if got := p.Failing(other); got != want {
			t.Errorf("%s: %q, want %q", other, got, want)
		}
		if got := p.Failing(selfHosted); got != "" {
			t.Errorf("%s: %q, want nothing", selfHosted, got)
		}
	}
	check := func() {
		t.Helper()
		_, _ = p.host(ctx, other)
	}

	f.auth[other] = fmt.Errorf("glab: %w", context.DeadlineExceeded)
	check()
	line("glab did not answer in time")
	f.auth[other] = &runner.ExitError{Code: 1, Stderr: []byte(other + "\n  x " + other + ": API call failed: GET https://" + other + "/api/v4/user: 502 {message: bad gateway}\n")}
	check()
	line("auth check failed (HTTP 502)")
	f.auth[other] = nil
	check()
	line("")

	// A check the caller gave up on while glab ran says nothing about the
	// host.
	gone, cancel := context.WithCancel(ctx)
	unchecked(p, other)
	f.authGate = func(string) { cancel() }
	f.auth[other] = context.Canceled
	if _, err := p.host(gone, other); !errors.Is(err, errNoAnswer) {
		t.Fatalf("given up: %v", err)
	}
	f.authGate = nil
	line("")

	// A definite no clears an earlier failure: the board names the logout.
	f.auth[other] = errors.New("exec: no such file")
	check()
	line("auth check failed")
	delete(f.auth, other)
	check()
	line("")

	// glab saying it is logged in nowhere answers for every host.
	f.auth[other] = errors.New("exec: no such file")
	unchecked(p, other)
	check()
	line("auth check failed")
	unchecked(p, selfHosted)
	f.auth[selfHosted] = &runner.ExitError{Code: 1, Stderr: []byte("\n   ERROR  \n\n  no GitLab instances have been authenticated with glab; run `glab auth login` to\n  authenticate.\n\n")}
	if h, err := p.host(ctx, selfHosted); err != nil || !h.nowhere {
		t.Fatalf("logged in nowhere: %+v %v", h, err)
	}
	line("")
	c.add(unknownFor)
	f.auth[selfHosted] = nil

	// A 429 on the check shows while the host is left alone, then goes.
	unchecked(p, other)
	f.auth[other] = &runner.ExitError{Code: 1, Stderr: []byte(other + "\n  x " + other + ": API call failed: GET https://" + other + "/api/v4/user: 429 {message: 429 Too Many Requests}\n")}
	check()
	line(noteRateLimited)
	c.add(rateLimitBackoff)
	line("")

	// So does a lookup's rate-limit note, though no lookup answers since.
	f.auth[other] = nil
	check()
	f.graphql = func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stderr: []byte("glab: HTTP 429\n")}
	}
	if _, err := p.Lookup(ctx, other, project, []int{101}, nil); !errors.Is(err, source.ErrRateLimited) {
		t.Fatalf("lookup 429: %v", err)
	}
	line(noteRateLimited)
	c.add(rateLimitBackoff - time.Second)
	line(noteRateLimited)
	c.add(time.Second)
	line("")
}

// A host check called off while it waits for its turn at `glab auth status`
// (the board's session for the host left) runs no glab and leaves the
// host's line as it was.
func TestCalledOffCheckRunsNoGlab(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	const first, second = "a.example.com", "b.example.com"
	f.auth[first], f.auth[second] = nil, nil
	release := make(chan struct{})
	f.authGate = func(string) { <-release }
	ran := make(chan error, 1)
	go func() { _, err := p.host(context.Background(), first); ran <- err }()
	waitUntil(t, "the first check runs", func() bool { return f.count("auth") == 1 })
	ctx, cancel := context.WithCancel(context.Background())
	waited := make(chan error, 1)
	go func() { _, err := p.host(ctx, second); waited <- err }()
	waitUntil(t, "the second check waits", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.authWaiting == 1
	})
	cancel()
	close(release)
	if err := <-ran; err != nil {
		t.Fatal(err)
	}
	if err := <-waited; !errors.Is(err, errNoAnswer) {
		t.Fatalf("called off: %v", err)
	}
	if n := f.count("auth"); n != 1 || p.Failing(second) != "" {
		t.Fatalf("%d auth calls, line %q; want only the first, no line", n, p.Failing(second))
	}
}

// A 401 on a closing-issues call is a logout like one on GraphQL: the
// lookup returns nothing and caches nothing, and the host loses its yes.
func TestClosingIssues401IsALogout(t *testing.T) {
	ctx := context.Background()
	p, f, _ := newAdapter(t, Options{})
	f.rest = func(string) ([]byte, error) { return nil, unauthorized }
	res, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil)
	if !errors.Is(err, source.ErrNotLoggedIn) || len(res.MRs) != 0 {
		t.Fatalf("closing issues 401: %+v %v, want not logged in and nothing", res, err)
	}
	f.rest = func(string) ([]byte, error) { return fixture(t, "closes-issues.json"), nil }
	calls := len(f.calls)
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); !errors.Is(err, source.ErrNotLoggedIn) || len(f.calls) != calls {
		t.Fatalf("after the 401: %v, %d calls; want the host's yes gone, no call", err, len(f.calls)-calls)
	}
	p.mu.Lock()
	cached, closes := len(p.cache), len(p.closes)
	p.mu.Unlock()
	if cached != 0 || closes != 0 {
		t.Fatalf("after the 401: %d lookups and %d closing lists cached", cached, closes)
	}
}

// Who a logout covers comes from glab's whole diagnostic: a 401 for one
// host stays that host's, whatever its body says; glab's own "no instances"
// answer, on a check or a lookup, covers every host.
func TestLogoutScope(t *testing.T) {
	ctx := context.Background()
	const other = "other.example.com"
	nowhereMsg := "\n   ERROR  \n\n  no GitLab instances have been authenticated with glab; run `glab auth login` to\n  authenticate.\n\n"

	// A check's 401 whose body quotes the global words: this host only.
	p, f, _ := newAdapter(t, Options{})
	f.auth[other] = &runner.ExitError{Code: 1, Stderr: []byte(other + "\n  x " + other + ": API call failed: GET https://" + other +
		"/api/v4/user: 401 {message: no GitLab instances have been authenticated with glab}\n")}
	if h, err := p.host(ctx, other); err != nil || h.known || h.nowhere {
		t.Fatalf("a host's 401: %+v %v, want that host logged out only", h, err)
	}
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); err != nil {
		t.Fatalf("another host's 401 took %s's yes: %v", selfHosted, err)
	}

	// A lookup's 401 quoting them: this host only.
	p, f, _ = newAdapter(t, Options{})
	approveOther(t, p, f, other)
	f.graphql = func(args []string) ([]byte, error) {
		if hostArg(args) == other {
			return nil, &runner.ExitError{Code: 1, Stderr: []byte("glab: 401 Unauthorized: no GitLab instances have been authenticated with glab (HTTP 401)\n")}
		}
		return fixture(t, "lookup.json"), nil
	}
	if _, err := p.Lookup(ctx, other, project, []int{101}, nil); !errors.Is(err, source.ErrNotLoggedIn) || errors.Is(err, source.ErrNoLogins) {
		t.Fatalf("a lookup's 401: %v, want this host logged out only", err)
	}
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); err != nil {
		t.Fatalf("another host's lookup 401 took %s's yes: %v", selfHosted, err)
	}

	// glab's own "no instances" answer to a lookup: every host.
	p, f, _ = newAdapter(t, Options{})
	approveOther(t, p, f, other)
	f.graphql = func(args []string) ([]byte, error) {
		if hostArg(args) == other {
			return nil, &runner.ExitError{Code: 1, Stderr: []byte(nowhereMsg)}
		}
		return fixture(t, "lookup.json"), nil
	}
	if _, err := p.Lookup(ctx, other, project, []int{101}, nil); !errors.Is(err, source.ErrNoLogins) {
		t.Fatalf("no instances on a lookup: %v, want logged in nowhere", err)
	}
	calls := len(f.calls)
	if _, err := p.Lookup(ctx, selfHosted, project, []int{101}, nil); !errors.Is(err, source.ErrNotLoggedIn) || len(f.calls) != calls {
		t.Fatalf("after no instances: %s %v, %d calls; want its yes gone", selfHosted, err, len(f.calls)-calls)
	}
}

// approveOther logs the fake glab in to host and runs its check.
func approveOther(t *testing.T, p *Provider, f *fakeGlab, host string) {
	t.Helper()
	f.auth[host] = nil
	approve(t, p, host)
}

// A check called off by the time it gets its turn runs no glab: the turn
// and the call-off can come at once, and the call-off wins.
func TestCalledOffAtItsTurnRunsNoGlab(t *testing.T) {
	p, f, _ := newUnchecked(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 32 { // the slot is free and ctx done: either can be picked
		if _, err := p.host(ctx, selfHosted); !errors.Is(err, errNoAnswer) {
			t.Fatalf("called off: %v", err)
		}
	}
	if n := f.count("auth"); n != 0 {
		t.Fatalf("%d auth calls after the call-off", n)
	}
}
