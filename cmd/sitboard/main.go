// Command sitboard starts a local board of the AI coding agents on this machine.
//
// It wires the adapters, the core and the server, listens on 127.0.0.1 only,
// prints the opening link (with the per-run token) once, and opens the browser
// unless --no-open is given.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/IZZY-Agency/sitboard"
	"github.com/IZZY-Agency/sitboard/internal/adapters/agterm"
	"github.com/IZZY-Agency/sitboard/internal/adapters/claude"
	"github.com/IZZY-Agency/sitboard/internal/adapters/codex"
	"github.com/IZZY-Agency/sitboard/internal/adapters/github"
	"github.com/IZZY-Agency/sitboard/internal/adapters/gitlab"
	"github.com/IZZY-Agency/sitboard/internal/adapters/gitsrc"
	"github.com/IZZY-Agency/sitboard/internal/adapters/procs"
	"github.com/IZZY-Agency/sitboard/internal/adapters/tmux"
	"github.com/IZZY-Agency/sitboard/internal/buildinfo"
	"github.com/IZZY-Agency/sitboard/internal/core"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/server"
	"github.com/IZZY-Agency/sitboard/internal/source"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

const defaultPort = 4317

// shutdownGrace bounds how long requests in flight may hold up exit. Event
// streams end at once (their context is cancelled first). After the grace the
// rest is closed: Shutdown alone waits up to 5 s for connections a browser
// opened ahead of time without sending a request.
const shutdownGrace = time.Second

// boardStopGrace bounds the wait for the board to stop (and save what
// catch-up learned since its last tick) before the state file closes. A
// stop takes milliseconds; a board stuck longer loses only that save.
const boardStopGrace = 2 * time.Second

func main() { os.Exit(mainCode()) }

// printLicenses writes sitboard's licence and the third-party notices.
func printLicenses(w io.Writer) {
	_, _ = io.WriteString(w, sitboard.Text())
}

func mainCode() int {
	cfg, err := parseArgs(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return 2
	}
	if cfg.version {
		fmt.Printf("sitboard %s (%s)\n", buildinfo.Version, buildinfo.Commit)
		return 0
	}
	if cfg.licenses {
		printLicenses(os.Stdout)
		return 0
	}
	home, err := os.UserHomeDir()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "sitboard: no home folder:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	env := runEnv{
		home:     home,
		out:      os.Stdout,
		sources:  realSources,
		open:     openBrowser,
		goos:     runtime.GOOS,
		getenv:   os.Getenv,
		lockWait: state.LockWait,
	}
	if err := run(ctx, cfg, env); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "sitboard:", err)
		return 1
	}
	return 0
}

type config struct {
	port       int
	portSet    bool // --port was given: never fall back to another port
	noOpen     bool
	noGitHub   bool
	noGitLab   bool
	roots      []string
	staleAfter time.Duration
	statePath  string // --state; "" is the platform's default file
	noState    bool   // --no-state: nothing is read from or written to disk
	version    bool
	licenses   bool
}

func parseArgs(args []string, stderr io.Writer) (config, error) {
	var cfg config
	var roots string
	fs := flag.NewFlagSet("sitboard", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.IntVar(&cfg.port, "port", defaultPort, "port on 127.0.0.1; 0 picks a free one")
	fs.BoolVar(&cfg.noOpen, "no-open", false, "do not open the browser")
	fs.BoolVar(&cfg.noGitHub, "no-github", false, "never call gh: no pull request lookups on GitHub")
	fs.BoolVar(&cfg.noGitLab, "no-gitlab", false, "never call glab: no merge request lookups on GitLab (with --no-github: nothing goes to GitHub or GitLab)")
	fs.StringVar(&roots, "roots", "", "comma-separated folders; only agents working under them are shown")
	fs.DurationVar(&cfg.staleAfter, "stale-after", 20*time.Minute, "a busy agent with no activity for this long is flagged stale")
	fs.StringVar(&cfg.statePath, "state", "", "`file` for seen marks, what Catch-up marked read and project groupings (default: ~/Library/Application Support/sitboard/state.json on macOS; elsewhere sitboard/state.json under $XDG_STATE_HOME, or ~/.local/state)")
	fs.BoolVar(&cfg.noState, "no-state", false, "keep seen marks, what Catch-up marked read and project groupings in memory only: no state file is read or written")
	fs.BoolVar(&cfg.version, "version", false, "print the version and exit")
	fs.BoolVar(&cfg.licenses, "licenses", false, "print sitboard's licence and the third-party notices and exit")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "sitboard: unexpected argument %q\n", fs.Arg(0))
		return cfg, errors.New("unexpected argument")
	}
	stateSet := false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "port":
			cfg.portSet = true
		case "state":
			stateSet = true
		}
	})
	if stateSet && strings.TrimSpace(cfg.statePath) == "" {
		_, _ = fmt.Fprintln(stderr, "sitboard: --state needs a file path")
		return cfg, errors.New("empty state")
	}
	if stateSet && cfg.noState {
		_, _ = fmt.Fprintln(stderr, "sitboard: use either --state or --no-state, not both")
		return cfg, errors.New("state and no-state")
	}
	if cfg.port < 0 || cfg.port > 65535 {
		_, _ = fmt.Fprintln(stderr, "sitboard: --port must be between 0 and 65535")
		return cfg, errors.New("bad port")
	}
	if cfg.staleAfter <= 0 {
		_, _ = fmt.Fprintln(stderr, "sitboard: --stale-after must be positive")
		return cfg, errors.New("bad stale-after")
	}
	cfg.roots = splitRoots(roots)
	return cfg, nil
}

// sources are the adapters, built per run.
type sources struct {
	watchers []source.Watcher
	repos    source.RepoResolver
	inv      source.RepoInventory // nil: Leftovers off
	folders  source.FolderUse
	prs      source.PRProvider // GitHub
	gitlab   core.GitLab       // nil: no GitLab source at all
	issues   map[model.Forge]source.IssueLister
	jumper   source.Jumper
}

func realSources(cfg config, home string) sources {
	term := agterm.New(agterm.Options{})
	gh := github.New(github.Options{Off: cfg.noGitHub})
	issues := map[model.Forge]source.IssueLister{}
	if l, ok := gh.(source.IssueLister); ok {
		issues[model.ForgeGitHub] = l
	}
	tm := tmux.New(tmux.Options{AgtermOf: claude.AgtermIDOf, Raise: term, Alive: claude.ProcessAlive})
	return sources{
		watchers: []source.Watcher{
			claude.New(claude.Options{Home: home, TmuxOwns: tm.Owns}),
			codex.New(codex.Options{Home: home, CodexHome: os.Getenv("CODEX_HOME")}),
			term,
		},
		repos:   gitsrc.New(gitsrc.Options{}),
		inv:     gitsrc.NewInventory(gitsrc.Options{}),
		folders: procs.New(),
		prs:     gh,
		issues:  issues,
		gitlab:  gitlab.NewSource(gitlab.Options{Off: cfg.noGitLab}),
		jumper: source.Jumpers{
			model.FocusAgterm: term,
			model.FocusTmux:   tm,
		},
	}
}

// runEnv is what run takes from the outside world, so tests can replace it.
type runEnv struct {
	home    string
	out     io.Writer
	sources func(config, string) sources
	open    func(link string) error
	goos    string
	getenv  func(string) string
	// lock takes the state file's lock; nil is state.Lock. lockWait is how
	// long it waits for another holder (state.LockWait outside tests).
	// openStore reads the state file; nil is state.Open.
	lock      func(ctx context.Context, file string, wait time.Duration) (*state.FileLock, error)
	lockWait  time.Duration
	openStore func(file string) (state.Store, error)
}

// run serves the board until ctx ends.
func run(ctx context.Context, cfg config, env runEnv) error {
	st, stateNotes, unlock, err := openState(ctx, cfg, env)
	if err != nil {
		if ctx.Err() != nil {
			return nil // stopped while waiting for the lock: nothing was started
		}
		return err
	}
	defer unlock()
	src := env.sources(cfg, env.home)

	board := core.New(core.Options{
		Repos:      src.repos,
		Inventory:  src.inv,
		Folders:    src.folders,
		PRs:        src.prs,
		GitLab:     src.gitlab,
		Issues:     src.issues,
		State:      st,
		StaleAfter: cfg.staleAfter,
		Roots:      cfg.roots,
	})
	statuses := make([]source.Status, 0, len(src.watchers)+3)
	for _, w := range src.watchers {
		statuses = append(statuses, w.Probe(ctx))
	}
	statuses = append(statuses, src.repos.Probe(ctx), src.prs.Probe(ctx))
	if src.gitlab != nil && cfg.noGitLab {
		// Switched off: the line says so at once and glab never runs.
		statuses = append(statuses, src.gitlab.Probe(ctx))
	}
	board.SetSources(statuses)

	// A stop asked for during the start (the lock wait can take seconds)
	// ends it before anything listens, prints a link or opens a browser.
	if ctx.Err() != nil {
		return nil
	}
	ln, note, err := listen(cfg.port, cfg.portSet)
	if err != nil {
		return err
	}
	addr := ln.Addr().String()
	token := newToken()

	// The board saves what catch-up learned since its last tick when it
	// stops; the store closes (deferred unlock) only after that, or after
	// boardStopGrace, so a stuck board cannot hang the exit.
	boardDone := make(chan struct{})
	defer func() {
		select {
		case <-boardDone:
		case <-time.After(boardStopGrace):
			log.Printf("core: did not stop within %s; what catch-up learned since its last tick may not be saved", boardStopGrace)
		}
	}()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// A watcher that stops with an error takes its facts with it and shows
	// as failing. Sources are probed once: one missing at startup is not
	// picked up until sitboard restarts (accepted for the proof of concept).
	for _, w := range src.watchers {
		go func() {
			if err := w.Run(runCtx, board.Apply); err != nil && runCtx.Err() == nil {
				log.Printf("%s: %v", w.Kind(), err)
				board.DropSource(w.Kind(), "stopped with an error; restart sitboard")
			}
		}()
	}
	go func() {
		defer close(boardDone)
		if err := board.Run(runCtx); err != nil {
			log.Printf("core: %v", err)
		}
	}()
	if src.gitlab != nil && !cfg.noGitLab {
		// glab is probed once sitboard listens, in the background: a slow or
		// stuck glab delays nothing. Logins are per host and show on its line
		// once a session's merge requests need one.
		board.StartGitLabProbe(runCtx)
	}

	srv := newHTTPServer(runCtx, server.New(server.Options{
		Board:    board,
		Jumper:   src.jumper,
		Token:    token,
		Addr:     addr,
		Assets:   server.Dist(),
		Licenses: sitboard.Text(),
	}))

	// The token is per run and dies with the process; this is the only place it is printed.
	link := "http://" + addr + "/?" + server.TokenParam + "=" + token
	for _, n := range stateNotes {
		_, _ = fmt.Fprintln(env.out, "sitboard:", n)
	}
	if note != "" {
		_, _ = fmt.Fprintln(env.out, "sitboard:", note)
	}
	_, _ = fmt.Fprintln(env.out, "sitboard:", link)
	_, _ = fmt.Fprintln(env.out, "sitboard: press Ctrl-C to stop")
	if !cfg.noOpen && env.open != nil {
		if err := env.open(link); err != nil {
			_, _ = fmt.Fprintln(env.out, "sitboard: could not open the browser; open the link above")
		}
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	cancel() // ends event streams, watchers and the clock
	shutdown, done := context.WithTimeout(context.Background(), shutdownGrace)
	defer done()
	if err := srv.Shutdown(shutdown); err != nil {
		if !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		_ = srv.Close()
	}
	return nil
}

// newHTTPServer sets the timeouts. There is no ReadTimeout or WriteTimeout:
// the event stream stays open; the POST handlers bound their own body reads.
func newHTTPServer(ctx context.Context, h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
}

// listen binds 127.0.0.1 only. When the default port is taken (most often by
// another sitboard) it falls back to a free port and says so; a port given
// with --port is never replaced.
func listen(port int, explicit bool) (net.Listener, string, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err == nil {
		return ln, "", nil
	}
	if explicit {
		return nil, "", fmt.Errorf("cannot listen on 127.0.0.1:%d (is another sitboard running? try --port 0): %w", port, err)
	}
	ln, err2 := net.Listen("tcp", "127.0.0.1:0")
	if err2 != nil {
		return nil, "", fmt.Errorf("cannot listen on 127.0.0.1: %w", err2)
	}
	_, got, _ := net.SplitHostPort(ln.Addr().String())
	return ln, fmt.Sprintf("port %d is taken (another sitboard?), using %s", port, got), nil
}

// openState opens the state store the flags ask for. Its order is the start
// order: the path is resolved and probed, then locked, then read; the port is
// taken only after (in run). It refuses a state file it could not write, so a
// board never runs while silently saving nothing, and one that another
// sitboard holds, so two boards never overwrite each other's marks. The notes,
// for the start lines, say which file is used when --state or --no-state was
// given (the default file is not named), and warn when the file could not be
// locked. unlock stops the store's writes and lets go of the lock; it is safe
// to call when there is none.
func openState(ctx context.Context, cfg config, env runEnv) (st state.Store, notes []string, unlock func(), err error) {
	unlock = func() {}
	if cfg.noState {
		return state.Memory(), []string{"no state file (--no-state): seen marks, what Catch-up marked read and groupings last until sitboard stops"}, unlock, nil
	}
	file := stateFile(env.home, env.goos, env.getenv)
	if cfg.statePath != "" {
		if file, err = statePath(cfg.statePath, env.home); err != nil {
			return nil, nil, unlock, err
		}
	}
	if file, err = state.Resolve(file); err != nil {
		return nil, nil, unlock, err
	}
	if cfg.statePath != "" {
		notes = append(notes, "state file "+file) // after links are followed: the file really used
	}
	open := env.openStore
	if open == nil {
		open = state.Open
	}
	if err := state.CheckWritable(file); err != nil {
		// A file of sitboard 0.4.0 or earlier in a folder it cannot write is
		// still served, in memory only (state.Open drops its old catch-up
		// marker and cannot save that). Such a run writes nothing, so it takes
		// no lock. Any other file there is refused, as before.
		if fi, serr := os.Stat(file); serr == nil && fi.Mode().IsRegular() {
			if st, oerr := open(file); oerr == nil && st.InMemory() {
				return st, append(notes, memoryNote), unlock, nil
			}
		}
		return nil, nil, unlock, err
	}
	lock := env.lock
	if lock == nil {
		lock = state.Lock
	}
	held, err := lock(ctx, file, env.lockWait)
	var busy *state.LockedError
	switch {
	case errors.As(err, &busy):
		return nil, nil, unlock, fmt.Errorf("%w; stop it, or give this one its own file with --state <path>, or run it with --no-state", err)
	case errors.Is(err, state.ErrLockUnsupported):
		notes = append(notes, "warning: could not lock the state file (its folder does not support locks); another sitboard on the same file would overwrite it")
	case err != nil:
		return nil, nil, unlock, err
	}
	release := func() { _ = held.Release() } // a nil lock releases nothing
	st, err = open(file)
	if err != nil {
		release()
		return nil, nil, unlock, err
	}
	if st.InMemory() {
		notes = append(notes, memoryNote)
	}
	// The store stops writing before the lock goes, so a write that comes
	// late (a watcher still stopping) never lands once another may hold it.
	return st, notes, func() { st.Close(); release() }, nil
}

// memoryNote is the start line for a state file this run cannot write.
const memoryNote = "warning: could not write the state file; seen marks, what Catch-up marks read and groupings last until sitboard stops"

// statePath makes a --state value absolute and clean; a leading "~/" is the
// home folder, as in --roots, so "--state=~/x" works without the shell. The
// value is taken as given, spaces included. "~user/x" is refused rather than
// read as a folder named "~user". A value spelt as a folder (ending in "/",
// or in a "." or ".." part) is refused rather than read as the file named like
// that folder: cleaning would turn "/a/new/." into the file "/a/new".
func statePath(p, home string) (string, error) {
	if strings.HasSuffix(p, "/") {
		return "", fmt.Errorf("--state %s ends in /: give a file path, not a folder", p)
	}
	if last := p[strings.LastIndex(p, "/")+1:]; last == "." || last == ".." {
		return "", fmt.Errorf("--state %s ends in %s: give a file path, not a folder", p, last)
	}
	if strings.HasPrefix(p, "~") && p != "~" && !strings.HasPrefix(p, "~/") {
		return "", fmt.Errorf("--state %s: only ~ and ~/ are understood; give the full path", p)
	}
	if p == "~" {
		p = home
	} else if rest, ok := strings.CutPrefix(p, "~/"); ok {
		p = filepath.Join(home, rest)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("--state %s: %w", p, err)
	}
	return abs, nil
}

// stateFile follows each platform's convention: macOS Application Support,
// elsewhere $XDG_STATE_HOME (default ~/.local/state).
func stateFile(home, goos string, getenv func(string) string) string {
	if goos == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "sitboard", "state.json")
	}
	base := getenv("XDG_STATE_HOME")
	if base == "" || !filepath.IsAbs(base) {
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "sitboard", "state.json")
}

func splitRoots(s string) []string {
	var out []string
	for r := range strings.SplitSeq(s, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never fails on supported platforms
	return hex.EncodeToString(b)
}

// openBrowser hands the link to the platform's opener. It never prints anything.
func openBrowser(link string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, link)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
