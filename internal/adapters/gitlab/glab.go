// Package gitlab looks up merge requests through the user's own glab. It
// never reads, stores or passes a token, never reads glab's config file, never
// logs glab's output, and runs glab with an environment it builds itself, in a
// folder that is no repository.
//
// One Lookup is one read-only GraphQL call per 50 numbers and 50 branches
// of a project, plus REST calls (one per page) for the merge requests whose
// closing issues are not known yet. Calls go only to a host glab is logged
// in to (gitlab.com included), always named with --hostname.
// Every untrusted value (project path, branch) travels as a raw string
// variable (-f), never inside the query text and never through glab's typed
// -F fields, which read files on "@".
package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// Options configure the provider.
type Options struct {
	Bin string // glab binary; "" means look it up on PATH
	Off bool   // switched off by --no-gitlab: never runs glab
}

const (
	callTimeout = 10 * time.Second
	cacheFor    = 60 * time.Second
	probeFor    = 60 * time.Second
	knownFor    = 10 * time.Minute // a host glab is logged in to: lookups go there this long after its check
	// renewAfter: a host check asks glab again about a host it said yes to
	// this long after, before lookups stop trusting the yes (knownFor). The
	// core checks every host it needs once a minute, so lookups find the
	// yes current unless its renewals fail.
	renewAfter       = knownFor - 2*time.Minute
	unknownFor       = time.Minute      // a host it is not (a login is noticed within a minute)
	rateLimitBackoff = 5 * time.Minute  // after a 429 that named no wait
	minRateLimitWait = time.Minute      // after a 429 that named a shorter one
	closingOpenFor   = 10 * time.Minute // an open merge request's description can change
	approvalsRecheck = 24 * time.Hour   // a host without approval fields is asked with them again (an upgrade)
	closingRetry     = 5 * time.Minute  // a closes_issues call that failed is tried again after this
	maxClosingCalls  = 10               // closes_issues calls per Lookup; the rest wait for the next one
)

// testedVersions are the glab versions this adapter was run against.
var testedVersions = []string{"1.131.0"}

// glab gets an environment built here, never the owner's shell environment.
// Nothing in sitboard's own environment may change where a lookup goes, which
// login it carries or which certificates it trusts, so none of these reach
// glab: a token (GITLAB_TOKEN and its twins: glab 1.131.0 sends such a token
// to every host in its config file, which would hand a self-hosted
// instance's token to gitlab.com), a host (GITLAB_HOST and co), a CI login,
// another config folder (GLAB_CONFIG_DIR, XDG_CONFIG_HOME), a proxy (see
// passProxy) or a
// certificate file. glab finds its config file in the account's home folder,
// where `glab auth login` writes it, and reads its logins from there or from
// the system keyring; a private certificate authority goes in glab's own
// per-host ca_cert setting.
//
// From the environment only the session's keyring address passes (Linux:
// Secret Service over D-Bus). It can only change which of the owner's
// stored logins glab finds, never the host it sends one to.
var passEnv = []string{"DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR"}

// passProxy is the one switch for passing sitboard's proxy settings to glab
// (proxyEnv). On: the owner's choice (#66, 22 Nov), so glab reaches GitLab
// where the machine needs a proxy. A proxy sees which hosts glab talks to;
// with TLS it cannot read the token.
var passProxy = true

// proxyEnv are the proxy variables passProxy lets through.
var proxyEnv = []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"}

// glabDirs are the standard folders glab is installed in (Homebrew on Apple
// silicon and Intel, system packages, snap), looked in when
// sitboard's own PATH lacks it: a desktop or service launch often has no
// Homebrew folder on its PATH.
var glabDirs = []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/snap/bin"}

// findGlab returns glab's absolute path, "" when not found: bin itself when
// it is a path (Options.Bin), else bin on the launch PATH, else in dirs.
func findGlab(bin string, dirs []string) string {
	if p := runner.Found(bin); p != "" {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
	}
	if strings.ContainsRune(bin, filepath.Separator) {
		return ""
	}
	for _, d := range dirs {
		if p := runner.Found(filepath.Join(d, bin)); p != "" {
			return p
		}
	}
	return ""
}

// findDefault is the provider's lookup: findGlab in glabDirs.
func findDefault(bin string) string { return findGlab(bin, glabDirs) }

// glabPath is the PATH glab gets: it runs git (git rev-parse) and nothing
// else; the glab binary itself is found by sitboard before it starts
// (findGlab).
const glabPath = "/usr/bin:/bin"

// account is the operating system account sitboard runs as.
type account struct{ home, name string }

// errNoAccount is a setup error: the account sitboard runs as has no entry
// with a home folder in the system's user list, so glab's config and logins
// cannot be found without trusting the launch environment's HOME.
var errNoAccount = errors.New("gitlab: no home folder for this account in the system's user list")

// noAccountDetail is errNoAccount for the page's source list.
const noAccountDetail = "no home folder for this account in the system's user list"

// osAccount reads the account sitboard runs as from the system's user list:
// the provider's goos and userList are runtime.GOOS and /etc/passwd.
func (p *Provider) osAccount() (account, error) {
	return resolveAccount(p.goos, p.userList, os.Getuid())
}

// resolveAccount finds the account of uid. macOS asks the system (libc, also
// without cgo). Elsewhere the user list file is read here: the release is
// built without cgo, and there user.LookupId first asks user.Current, which
// for the current uid without an entry falls back to HOME and USER.
func resolveAccount(goos, userList string, uid int) (account, error) {
	if goos == "darwin" {
		return accountFor(uid, user.LookupId)
	}
	return userListAccount(userList, uid)
}

// maxUserList is the largest user list file read; a larger one is refused,
// so no record is ever cut off by the limit.
var maxUserList int64 = 16 << 20

// userListAccount reads uid's entry ("name:password:uid:gid:gecos:home:shell")
// from the user list file: the first complete record for uid (seven fields,
// numeric uid and gid, an absolute home folder) wins, and a malformed one
// is skipped. No file, a file past maxUserList, or no such record is
// errNoAccount.
func userListAccount(path string, uid int) (account, error) {
	f, err := os.Open(path)
	if err != nil {
		return account{}, errNoAccount
	}
	defer func() { _ = f.Close() }() // read only
	data, err := io.ReadAll(io.LimitReader(f, maxUserList+1))
	if err != nil || int64(len(data)) > maxUserList {
		return account{}, errNoAccount
	}
	want := strconv.Itoa(uid)
	for line := range strings.Lines(string(data)) {
		parts := strings.Split(strings.TrimRight(line, "\n"), ":")
		if len(parts) != 7 || strings.HasPrefix(parts[0], "#") || parts[2] != want {
			continue
		}
		if _, err := strconv.ParseUint(parts[3], 10, 32); err != nil || !strings.HasPrefix(parts[5], "/") {
			continue
		}
		return account{home: parts[5], name: parts[0]}, nil
	}
	return account{}, errNoAccount
}

// accountFor looks the account up by numeric user id: unlike user.Current,
// user.LookupId never falls back to HOME and USER from the environment. No
// entry, or one without a home folder, is errNoAccount.
func accountFor(uid int, lookupID func(string) (*user.User, error)) (account, error) {
	u, err := lookupID(strconv.Itoa(uid))
	if err != nil || u.HomeDir == "" {
		return account{}, errNoAccount
	}
	return account{home: u.HomeDir, name: u.Username}, nil
}

// quietEnv keeps glab quiet and local.
var quietEnv = []string{
	// No usage data sent to the instance, no update or news checks, no
	// prompts, no colour, no pager, no debug output.
	"GLAB_SEND_TELEMETRY=false", "GLAB_CHECK_UPDATE=false", "GLAB_SHOW_WHATS_NEW=false",
	"GLAB_NOTIFY_SKILL_UPDATES=false", "GLAB_NO_PROMPT=1", "NO_COLOR=1", "GLAB_COLOR=never",
	"GLAB_PAGER=cat", "PAGER=cat", "GLAB_DEBUG=false", "GLAB_DEBUG_HTTP=false",
	// No helper binaries downloaded or run, no CI login.
	"GLAB_DUO_CLI_AUTO_DOWNLOAD=false", "GLAB_DUO_CLI_AUTO_RUN=false",
	"GLAB_ORBIT_CLI_AUTO_DOWNLOAD=false", "GLAB_ORBIT_CLI_AUTO_RUN=false",
	"GLAB_ENABLE_CI_AUTOLOGIN=false",
}

// glabDir is the folder glab runs in. glab reads a repository's own
// .git/glab-cli/config.yml from the folder it runs in, and fails on every
// call when that file is not private (seen with 1.131.0); "/" is no
// repository, so the folder sitboard was started in changes nothing.
const glabDir = "/"

// glabEnv builds glab's environment for acct, with lookup (os.LookupEnv in
// use) consulted only for passEnv.
func glabEnv(acct account, lookup func(string) (string, bool)) []string {
	e := []string{"PATH=" + glabPath}
	if acct.home != "" {
		e = append(e, "HOME="+acct.home)
	}
	if acct.name != "" {
		e = append(e, "USER="+acct.name, "LOGNAME="+acct.name)
	}
	keys := passEnv
	if passProxy {
		keys = append(slices.Clone(passEnv), proxyEnv...)
	}
	for _, k := range keys {
		if v, ok := lookup(k); ok {
			e = append(e, k+"="+v)
		}
	}
	return append(e, quietEnv...)
}

// Status is what the provider says about glab, for the page's source list.
type Status struct {
	State   source.State
	Detail  string
	Version string
	Tested  bool
}

type cached struct {
	at  time.Time
	res Result
}

type closing struct {
	at     time.Time
	state  string // the merge request's state when asked
	issues []int
}

type hostAnswer struct {
	at        time.Time
	known     bool
	subfolder string // glab's "subfolder" for this host, slashes trimmed; "" for none
	nowhere   bool   // glab said it is logged in to no host at all
}

// Provider answers merge request lookups through glab. It caches answers and
// runs no timer: its caller decides when to ask.
type Provider struct {
	bin      string
	off      bool
	timeout  time.Duration
	run      runner.Func // nil: real processes
	now      func() time.Time
	found    func(string) string // finds bin's absolute path (findGlab)
	goos     string              // runtime.GOOS: how the account is looked up (osAccount)
	userList string              // the user list file read off macOS

	// admitCh is a one-slot lock that serialises a glab process's start
	// with what would refuse it (admit): every change of a login, a mount
	// or a rate-limit wait takes it before mu (lockAuth). It is held across
	// a start, so mu never is: the board's reads of mu (Failing, Subfolder)
	// never wait on a start, and a call waiting to be admitted gives up
	// when its context ends (a merge request lookup at its deadline).
	admitCh       chan struct{}
	mu            sync.Mutex
	cache         map[string]cached     // by lookup key
	closes        map[string]closing    // by host, project id and iid
	closingFailed map[string]time.Time  // by the same key: when its last call failed
	hosts         map[string]hostAnswer // by host: is glab logged in to it
	backoff       map[string]time.Time  // host → no glab calls before this time
	noApproval    map[string]time.Time  // host → when it refused the approval fields
	forgotten     map[string]uint64     // host and project → times its answers were dropped
	health        source.State
	healthNote    string
	hostHealth    map[string]string // host → the line for its last failed call (setHostHealth)
	authNote      map[string]string // host → the line for its last login check that got no answer (setAuthNote)
	accounts      map[string]string // host name → the address of the account its last issue answer came for (sameAccount); in memory only
	nowhere       time.Time         // when glab last said it is logged in to no host
	authSem       chan struct{}     // held while a `glab auth status` runs: one at a time
	authGen       map[string]uint64 // host → its logouts so far (stampLocked)
	authAll       uint64            // times glab said it is logged in nowhere, or authGen was emptied
	authWaiting   int               // host checks waiting for authSem; tests read it
	probed        time.Time
	probe         Status
	binMu         sync.Mutex // held while glab's path is looked up and kept
	binPath       string     // bin's absolute path, once found (under binMu)
}

// New returns the GitLab merge request provider.
func New(opts Options) *Provider {
	bin := opts.Bin
	if bin == "" {
		bin = "glab"
	}
	return &Provider{
		bin: bin, off: opts.Off, timeout: callTimeout, now: time.Now, found: findDefault, goos: runtime.GOOS, userList: "/etc/passwd",
		cache: map[string]cached{}, closes: map[string]closing{}, hosts: map[string]hostAnswer{},
		backoff: map[string]time.Time{}, noApproval: map[string]time.Time{}, closingFailed: map[string]time.Time{},
		forgotten:  map[string]uint64{},
		hostHealth: map[string]string{},
		authNote:   map[string]string{},
		accounts:   map[string]string{},
		authSem:    make(chan struct{}, 1),
		admitCh:    make(chan struct{}, 1),
		authGen:    map[string]uint64{},
	}
}

// glab returns glab's absolute path, found once and used for every call
// (probing and running alike); "" when it is not found, looked for again
// next time.
func (p *Provider) glab() string {
	p.binMu.Lock()
	defer p.binMu.Unlock()
	if p.binPath == "" {
		p.binPath = p.found(p.bin)
	}
	return p.binPath
}

func (p *Provider) exec(ctx context.Context, args ...string) ([]byte, error) {
	bin := p.glab()
	if bin == "" {
		return nil, errNoGlab
	}
	run := p.run
	if run == nil {
		acct, err := p.osAccount()
		if err != nil {
			return nil, err
		}
		run = runner.Clean(p.timeout, glabDir, glabEnv(acct, os.LookupEnv))
	}
	return run(ctx, bin, args...)
}

// Probe reports whether glab is installed and answers. Being logged in is a
// per-host question (Known): glab's own overall status fails when any one
// configured host fails, which says nothing about the others.
func (p *Provider) Probe(ctx context.Context) Status {
	if p.off {
		return Status{State: source.StateOff, Detail: "switched off"}
	}
	p.mu.Lock()
	st, fresh := p.probe, !p.probed.IsZero() && p.now().Sub(p.probed) < probeFor
	health, note := p.health, p.healthNote
	p.mu.Unlock()
	if !fresh {
		st = p.probeNow(ctx)
		p.mu.Lock()
		p.probe, p.probed = st, p.now()
		p.mu.Unlock()
	}
	if st.State == source.StateOK && health == source.StateFailing {
		st.State, st.Detail = source.StateFailing, note
	}
	return st
}

func (p *Provider) probeNow(ctx context.Context) Status {
	if p.glab() == "" {
		return Status{State: source.StateMissing, Detail: "glab not found"}
	}
	out, err := p.exec(ctx, "version")
	if errors.Is(err, errNoAccount) {
		return Status{State: source.StateFailing, Detail: noAccountDetail}
	}
	if err != nil {
		return Status{State: source.StateFailing, Detail: "glab does not answer"}
	}
	st := Status{State: source.StateOK}
	st.Version = parseVersion(string(out))
	st.Tested = slices.Contains(testedVersions, st.Version)
	return st
}

// parseVersion reads "glab 1.131.0 (206ebab4f)" or the older
// "glab version 1.36.0 (2024-01-01)"; "" when neither.
func parseVersion(out string) string {
	f := strings.Fields(out)
	switch {
	case len(f) >= 3 && f[0] == "glab" && f[1] == "version":
		return strings.TrimPrefix(f[2], "v")
	case len(f) >= 2 && f[0] == "glab":
		return strings.TrimPrefix(f[1], "v")
	}
	return ""
}

// Known reports whether host is a GitLab host this provider may ask: only
// one glab is logged in to, gitlab.com included; github.com never. Only the exit code
// of `glab auth status --hostname <host>` is read; its output names the
// account and is dropped. For a host missing from its config glab answers
// "has not been authenticated" from the config (seen with 1.131.0 for a host
// that does not resolve); for a configured host it asks the instance.
func (p *Provider) Known(ctx context.Context, host string) bool {
	h, err := p.host(ctx, host)
	return err == nil && h.known
}

var (
	errNotKnown   = &sharedErr{"gitlab: glab is not logged in to this host", source.ErrNotLoggedIn}
	errNowhere    = &sharedErr{"gitlab: glab is logged in to no host", source.ErrNoLogins}
	errNotChecked = &sharedErr{"gitlab: the login for this host is not checked now", source.ErrNotChecked}
	errNotAsked   = errors.New("gitlab: glab not asked about this host")
	errNoGlab     = errors.New("gitlab: glab not found")
	errNoAnswer   = errors.New("gitlab: glab did not answer about this host")
	errInvalidArg = errors.New("gitlab: not a project address")
)

// host is Known with the reason when there is no answer: errRateLimited
// (the host answered 429, now or before), errNoAnswer (glab ran out of time
// or the caller gave up), errNoGlab. An answer, yes or no, is kept a while;
// a missing answer is not. For a host glab is logged in to it also asks
// glab's non-secret per-host setting "subfolder" (GitLab under a subfolder
// of its host): glab puts it into its own API calls, and Lookup takes it off
// the front of a project's web path (apiPath).
// When glab said it is logged in nowhere, no host is asked for unknownFor.
func (p *Provider) host(ctx context.Context, host string) (hostAnswer, error) {
	switch {
	case p.off:
		return hostAnswer{}, errNotAsked
	case !ValidHost(host):
		return hostAnswer{}, errInvalidArg
	case host == "github.com":
		return hostAnswer{}, nil
	}
	if h, done, err := p.keptAnswer(host); done {
		return h, err
	}
	if p.glab() == "" {
		return hostAnswer{}, errNoGlab
	}
	// One `glab auth status` at a time: until one answers, glab's logins are
	// not known, and a burst of cold checks would each run one before the
	// first learns that there are none. A check that waited finds the
	// answer the one before it left (for its host, or for every host).
	if err := p.takeAuth(ctx); err != nil {
		return hostAnswer{}, fmt.Errorf("%w: %w", errNoAnswer, unwrapCtx(err))
	}
	defer func() { <-p.authSem }()
	if err := ctx.Err(); err != nil { // called off as its turn came
		return hostAnswer{}, fmt.Errorf("%w: %w", errNoAnswer, err)
	}
	if h, done, err := p.keptAnswer(host); done {
		return h, err
	}
	p.mu.Lock()
	st := p.stampLocked(host)
	p.mu.Unlock()
	if _, err := p.exec(ctx, "auth", "status", "--hostname", host); err != nil {
		if gaveUp(ctx, err) {
			return hostAnswer{}, p.noAnswer(ctx, host, err, "")
		}
		// Only glab's own line for the host can say 429: rate-limit words
		// in a response body (a 502's, say) are no answer, asked again.
		out, all, status := authAnswer(err, host)
		if status == http.StatusTooManyRequests {
			p.limited(host, err)
			p.setAuthNote(host, noteRateLimited)
			return hostAnswer{}, errRateLimited
		}
		if all {
			return p.loggedInNowhere(), nil
		}
		if !out {
			msg := "auth check failed"
			if status != 0 {
				msg += " (HTTP " + strconv.Itoa(status) + ")"
			}
			return hostAnswer{}, p.noAnswer(ctx, host, err, msg)
		}
		p.setAuthNote(host, "")
		return p.loggedOut(host), nil
	}
	out, err := p.exec(ctx, "config", "get", "--host", host, "subfolder")
	if err != nil {
		return hostAnswer{}, p.noAnswer(ctx, host, err, "auth check failed")
	}
	p.setAuthNote(host, "")
	sub := strings.Trim(strings.TrimSpace(string(out)), "/")
	if sub != "" && !validSubfolder(sub) {
		// Not a path any address can carry: such a host gets no lookups.
		return hostAnswer{}, p.noAnswer(ctx, host, errors.New("unreadable subfolder"), "unreadable subfolder in glab's config")
	}
	return p.keepYes(host, hostAnswer{at: p.now(), known: true, subfolder: sub}, st)
}

// validSubfolder: glab's subfolder setting as a run of path segments ("gitlab",
// "apps/gitlab"), each one a GitLab path could start with; at most 8.
func validSubfolder(s string) bool {
	segs := strings.Split(s, "/")
	if len(segs) > 8 {
		return false
	}
	for _, g := range segs {
		if !gitlabSegment.MatchString(g) || strings.Contains(g, "..") {
			return false
		}
	}
	return true
}

var gitlabSegment = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,254}$`)

// gaveUp: a check's glab call ended by time, glab's own limit or its
// caller's, not by an answer.
func gaveUp(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// noAnswer is a host check that got no answer from glab: errNoAnswer, with
// msg (or why the call ended) for the host's line, which keeps it until a
// check answers. A caller that gave up (ctx ended) leaves the line as it is:
// that says nothing about the host.
func (p *Provider) noAnswer(ctx context.Context, host string, err error, msg string) error {
	if gaveUp(ctx, err) {
		err = unwrapCtx(err)
		msg = "glab did not answer in time"
	} else if msg == "" {
		msg = "auth check failed"
	}
	if ctx.Err() == nil {
		p.setAuthNote(host, msg)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %w", errNoAnswer, err)
	}
	return fmt.Errorf("%w: %s", errNoAnswer, msg)
}

// keptAnswer is host's check answered without glab: from the answer kept
// while it is current (done), or the host's rate-limit wait. Otherwise glab
// has to be asked.
func (p *Provider) keptAnswer(host string) (h hostAnswer, done bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if h, fresh := p.answerLocked(host, renewAfter); fresh {
		return h, true, nil
	}
	if p.now().Before(p.backoff[host]) {
		return hostAnswer{}, true, errRateLimited
	}
	return hostAnswer{}, false, nil
}

// takeAuth waits for the one `glab auth status` slot, or for ctx to end.
func (p *Provider) takeAuth(ctx context.Context) error {
	p.mu.Lock()
	p.authWaiting++
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.authWaiting--
		p.mu.Unlock()
	}()
	select {
	case p.authSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// answerLocked is the answer kept about host while it is current: while
// glab said it is logged in nowhere (unknownFor), a no for every host, a
// yes kept before included; else a yes for keep (knownFor for a lookup;
// renewAfter for a host check, which so renews it early), a no for
// unknownFor. Callers hold p.mu.
func (p *Provider) answerLocked(host string, keep time.Duration) (hostAnswer, bool) {
	if !p.nowhere.IsZero() && p.now().Sub(p.nowhere) < unknownFor {
		return hostAnswer{at: p.nowhere, nowhere: true}, true
	}
	h, ok := p.hosts[host]
	if !ok {
		return hostAnswer{}, false
	}
	ttl := unknownFor
	if h.known {
		ttl = keep
	}
	return h, p.now().Sub(h.at) < ttl
}

// noTokenRe is glab's line for a host with no token anywhere.
var noTokenRe = regexp.MustCompile(`(?m)^\s*! No token found \(checked config file, keyring, and environment variables\)\.\s*$`)

// authAnswer reads a failed `glab auth status --hostname host` (glab
// 1.131.0). It is a definite "no working login" only when glab says so in its
// own words: its whole error is that the host (or, all, every host) is not
// in its config, its line for the host says no token was found, or its line
// for the host says the instance answered HTTP 401 (that host only, whatever
// the body says). The status is taken from that line, never searched for in
// the response body after it. Anything else (a token glab could not read,
// another status, glab failing to run) is no answer; status is the HTTP
// status when glab named one, else 0.
//
// glab's own error is framed ("ERROR") and wrapped by its error printer,
// so it is compared with all whitespace collapsed; the per-host lines are
// printed as they are.
func authAnswer(err error, host string) (loggedOut, all bool, status int) {
	var ee *runner.ExitError
	if !errors.As(err, &ee) {
		return false, false, 0
	}
	if said, every := noLoginSaid(ee.Stderr, host); said {
		return true, every, 0
	}
	h := regexp.QuoteMeta(host)
	if m := regexp.MustCompile(`(?m)^\s*x ` + h + `: API call failed: [A-Z]+ \S+: ([0-9]{3})(?: |$)`).FindStringSubmatch(string(ee.Stderr)); m != nil {
		status, _ = strconv.Atoi(m[1])
		return status == http.StatusUnauthorized, false, status
	}
	return noTokenRe.Match(ee.Stderr), false, 0
}

var (
	// hostNoLoginRe and nowhereRe are glab's whole error, whitespace
	// collapsed, when host has no login in its config, and when no host has.
	hostNoLoginRe = "^(ERROR )?x %s has not been authenticated with glab; run `glab auth login --hostname %s` to authenticate\\.?$"
	nowhereRe     = regexp.MustCompile("^(ERROR )?no GitLab instances have been authenticated with glab; run `glab auth login` to authenticate\\.?$")
)

// noLoginSaid: glab's whole error (framed and wrapped by its error printer,
// so compared with all whitespace collapsed) is that host has no login in
// its config, or (all) that no host has one.
func noLoginSaid(stderr []byte, host string) (said, all bool) {
	h := regexp.QuoteMeta(host)
	whole := strings.Join(strings.Fields(string(stderr)), " ")
	if nowhereRe.MatchString(whole) {
		return true, true
	}
	return regexp.MustCompile(fmt.Sprintf(hostNoLoginRe, h, h)).MatchString(whole), false
}

// loggedInNowhere records that glab is logged in to no host: every yes
// kept goes, with every answer cached, and no host is asked for unknownFor.
func (p *Provider) loggedInNowhere() hostAnswer {
	defer p.lockAuth()()
	p.nowhere = p.now()
	p.authAll++
	clear(p.hosts)
	clear(p.authNote)
	p.dropAnswersLocked("")
	return hostAnswer{at: p.nowhere, nowhere: true}
}

// authStamp is the adapter's login generation for a host when a check or a
// lookup started: what it learned is kept only while the stamp still holds.
type authStamp struct{ all, host uint64 }

// lockAuth takes admitCh, then mu, for a change admit must not see half of;
// the function it returns lets both go.
func (p *Provider) lockAuth() func() {
	p.admitCh <- struct{}{}
	p.mu.Lock()
	return func() {
		p.mu.Unlock()
		<-p.admitCh
	}
}

// stampLocked is host's authStamp now. Callers hold p.mu.
func (p *Provider) stampLocked(host string) authStamp {
	return authStamp{all: p.authAll, host: p.authGen[host]}
}

// keepYes keeps a yes about host's login from a check that started at st,
// unless the login changed since: then the answer kept now stands (a
// definite one, as loggedOut and loggedInNowhere keep), or, with none kept
// (a refused token drops the yes), the check is inconclusive (errNoAnswer):
// its yes is of an older login, and it read no no.
func (p *Provider) keepYes(host string, h hostAnswer, st authStamp) (hostAnswer, error) {
	defer p.lockAuth()()
	if p.stampLocked(host) != st {
		if cur, ok := p.answerLocked(host, knownFor); ok {
			return cur, nil
		}
		return hostAnswer{}, fmt.Errorf("%w: the login changed during the check", errNoAnswer)
	}
	if len(p.hosts) > 256 {
		clear(p.hosts)
	}
	p.hosts[host] = h
	p.nowhere = time.Time{}
	return h, nil
}

// loggedOut records that host has no working login (a no from its check,
// or a 401 on a lookup): the yes kept goes, and with it every answer cached
// from the host, so nothing asked under the old login is served after a new
// one; checks and lookups that started before keep nothing they learn.
func (p *Provider) loggedOut(host string) hostAnswer {
	defer p.lockAuth()()
	p.newLoginLocked(host)
	if len(p.hosts) > 256 {
		clear(p.hosts)
	}
	h := hostAnswer{at: p.now()}
	p.hosts[host] = h
	return h
}

// doubtLogin forgets the yes kept for host (GitLab refused its token), as a
// new login: a check or lookup that started before keeps nothing it learns
// (a yes it read included), the answers cached under the old one go, and
// the next check runs `glab auth status` afresh.
func (p *Provider) doubtLogin(host string) {
	defer p.lockAuth()()
	p.newLoginLocked(host)
	if h := p.hosts[host]; h.known {
		delete(p.hosts, host)
	}
}

// newLoginLocked moves host's login generation and drops what was cached
// from it. Callers hold admitCh and p.mu (lockAuth).
func (p *Provider) newLoginLocked(host string) {
	if len(p.authGen) > 4096 {
		clear(p.authGen)
		p.authAll++
	}
	p.authGen[host]++
	p.dropAnswersLocked(host)
}

// dropAnswersLocked drops the lookups and closing issues cached for host,
// by its name (every web port of it: glab keeps one login per name), "" for
// all. Every key starts with the site, the host with its web port if any.
// Callers hold p.mu.
func (p *Provider) dropAnswersLocked(host string) {
	of := func(k string) bool {
		site, _, _ := strings.Cut(k, "\x00")
		return host == "" || model.GitLabHostname(site) == host
	}
	for k := range p.cache {
		if of(k) {
			delete(p.cache, k)
		}
	}
	for k := range p.closes {
		if of(k) {
			delete(p.closes, k)
		}
	}
	for k := range p.closingFailed {
		if of(k) {
			delete(p.closingFailed, k)
		}
	}
}

// setHealth records a failure of glab itself (not found), for every host.
func (p *Provider) setHealth(st source.State, note string) {
	p.mu.Lock()
	p.health, p.healthNote = st, note
	p.mu.Unlock()
}

// setHostHealth records how the last call about host went: note "" when it
// answered, else the line for the page. One host's answer never clears
// another's failure.
func (p *Provider) setHostHealth(host, note string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if note == "" {
		delete(p.hostHealth, host)
		return
	}
	if len(p.hostHealth) > 256 {
		clear(p.hostHealth)
	}
	p.hostHealth[host] = note
}

// setAuthNote records how host's last login check went: note "" when glab
// answered it (yes or a definite no), else the line for the page.
func (p *Provider) setAuthNote(host, note string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if note == "" {
		delete(p.authNote, host)
		return
	}
	if len(p.authNote) > 256 {
		clear(p.authNote)
	}
	p.authNote[host] = note
}

// Failing is the page's line for host: its last login check that got no
// answer, else its last failed call, "" when neither failed. A rate limit
// shows only while the host is left alone for it: no call is made then,
// and afterwards nothing says it still holds.
func (p *Provider) Failing(host string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	waiting := p.now().Before(p.backoff[host])
	for _, note := range []string{p.authNote[host], p.hostHealth[host]} {
		if note != "" && (note != noteRateLimited || waiting) {
			return note
		}
	}
	return ""
}

var errRateLimited = &sharedErr{"gitlab: rate limited", source.ErrRateLimited}

// sharedErr is an error of this adapter that is also one of the errors the
// core tells apart (source.ErrRateLimited and its kin), under its own words.
type sharedErr struct {
	msg    string
	shared error
}

func (e *sharedErr) Error() string { return e.msg }
func (e *sharedErr) Unwrap() error { return e.shared }

const noteRateLimited = "rate limited"

// noteLoggedOut is the line for a host that refused glab's login.
func noteLoggedOut(host string) string { return "not logged in to " + host }

// The forms glab 1.131.0 prints on standard error: `glab api` says
// "glab: HTTP 429" or "glab: 429 Too Many Requests (HTTP 429)" (GitLab's
// message, then the status glab saw), then "glab: rate limited, retry after
// 1m30s" when GitLab sent Retry-After or RateLimit-Reset; other commands pass
// on the API library's "GET <address>: 401 {message: …}". Only standard
// error is read, and only glab's own lines: standard output holds GitLab's
// answer, whose text (a title, "complexity of 429") says nothing about the
// call, and GitLab's message inside glab's line is no status either.
var (
	glabStatusRe = regexp.MustCompile(`(?m)^glab: (?:.*\(HTTP ([0-9]{3})\)|HTTP ([0-9]{3}))[ \t\r]*$`)
	apiStatusRe  = regexp.MustCompile(`(?m)^(?:GET|POST|PUT|PATCH|DELETE|HEAD) \S+: ([0-9]{3})(?: |$)`)
	glabLimitRe  = regexp.MustCompile(`(?m)^glab: rate limited\b`)
	retryAfterRe = regexp.MustCompile(`retry after ([0-9][0-9hms.]*)`)
)

// callFailure is a failed glab call as glab itself put it. The verdicts that
// change what sitboard asks (logged out, rate limited) come only from here,
// never from a line meant for the page or from words in GitLab's answer.
type callFailure struct {
	timedOut bool
	ran      bool // glab ran and exited non-zero
	status   int  // the HTTP status glab's own line named; 0 when none
	noLogin  bool // glab's own line: it has no login for the host
	nowhere  bool // glab's whole error: it has no login for any host
	limited  bool // a 429, or glab's own rate-limit line
}

// readFailure reads a failed glab call about host.
func readFailure(err error, host string) callFailure {
	if errors.Is(err, context.DeadlineExceeded) {
		return callFailure{timedOut: true}
	}
	var ee *runner.ExitError
	if !errors.As(err, &ee) {
		return callFailure{}
	}
	f := callFailure{ran: true}
	if m := glabStatusRe.FindSubmatch(ee.Stderr); m != nil {
		f.status, _ = strconv.Atoi(string(m[1]) + string(m[2]))
	} else if m := apiStatusRe.FindSubmatch(ee.Stderr); m != nil {
		f.status, _ = strconv.Atoi(string(m[1]))
	}
	said, all := noLoginSaid(ee.Stderr, host)
	f.noLogin, f.nowhere = said || noTokenRe.Match(ee.Stderr), all
	f.limited = f.status == http.StatusTooManyRequests || glabLimitRe.Match(ee.Stderr)
	return f
}

// tokenRefused: GitLab's whole GraphQL answer is that the token glab sent
// is refused, with no data: {"errors":[{"message":"Invalid token"}]}, as
// gitlab.com answers a revoked or expired token (glab 1.131.0 adds
// "glab: Invalid token" on standard error, with no status). It is no
// logout by itself (the host's next `glab auth status` names a 401 itself),
// but the yes kept for the host no longer stands: see doubtLogin.
func tokenRefused(stdout []byte) bool {
	var a struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
			Path    []any  `json:"path"`
		} `json:"errors"`
	}
	if json.Unmarshal(stdout, &a) != nil || (len(a.Data) > 0 && string(a.Data) != "null") || len(a.Errors) != 1 {
		return false
	}
	return a.Errors[0].Message == "Invalid token" && len(a.Errors[0].Path) == 0
}

// loggedOut: the host refused glab's login (a 401 glab named) or glab says it
// has none for the host. It wins over rate-limit words.
func (f callFailure) loggedOut() bool { return f.status == http.StatusUnauthorized || f.noLogin }

// rateLimited: the host asked to be left alone, and did not refuse the login.
func (f callFailure) rateLimited() bool { return f.limited && !f.loggedOut() }

// note is the failure as one short line for the page. A missing project is
// not a failure: GraphQL answers it with a null project. A 404 from the
// GraphQL endpoint itself means the instance's API is not where glab looks
// (for example GitLab under a subfolder), which is the source failing.
func (f callFailure) note(host string) string {
	switch {
	case f.timedOut:
		return "glab did not answer in time"
	case !f.ran:
		return "glab could not run"
	case f.loggedOut():
		return noteLoggedOut(host)
	case f.rateLimited():
		return noteRateLimited
	default:
		return "glab failed"
	}
}

// classify turns a glab failure into one short line for the page. glab's
// output is read here and dropped.
func classify(err error, host string) string { return readFailure(err, host).note(host) }

// rateLimitWait is how long to leave a host alone after a 429: the wait glab
// passed on from GitLab however long, at least minRateLimitWait, or
// rateLimitBackoff when GitLab named none.
func rateLimitWait(err error) time.Duration {
	var ee *runner.ExitError
	if !errors.As(err, &ee) {
		return rateLimitBackoff
	}
	m := retryAfterRe.FindStringSubmatch(strings.ToLower(string(ee.Stderr)))
	if m == nil {
		return rateLimitBackoff
	}
	d, perr := time.ParseDuration(m[1])
	if perr != nil {
		return rateLimitBackoff
	}
	return max(d, minRateLimitWait)
}

// limited records that host answered 429 and says until when it is left alone.
func (p *Provider) limited(host string, err error) {
	defer p.lockAuth()()
	until := p.now().Add(rateLimitWait(err))
	if until.After(p.backoff[host]) {
		p.backoff[host] = until
	}
}

func unwrapCtx(err error) error {
	for _, e := range []error{context.DeadlineExceeded, context.Canceled} {
		if errors.Is(err, e) {
			return e
		}
	}
	return errors.New("glab failed")
}
