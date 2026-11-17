// Package gitsrc answers git questions about folders. It never changes a repo.
//
// Every call runs git with an argument list and a 5 s timeout. Folder paths
// and branch names come from session records and repos and are untrusted: they
// are passed as single arguments, never through a shell.
package gitsrc

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// Options configure the resolver.
type Options struct {
	Bin string // git binary; "" means look it up on PATH
}

const callTimeout = 5 * time.Second

type resolver struct {
	bin     string
	timeout time.Duration
	run     runner.Func // nil: real processes
}

// New returns the git resolver.
func New(opts Options) source.RepoResolver {
	bin := opts.Bin
	if bin == "" {
		bin = "git"
	}
	return &resolver{bin: bin, timeout: callTimeout}
}

// git runs one read-only git command in dir. Optional locks are off so a
// read never writes to the repo, and fsmonitor is off so a repo's own config
// cannot make git start a program.
func (r *resolver) git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if !validDir(dir) {
		return nil, errBadFolder
	}
	run := r.run
	if run == nil {
		run = runner.Exec(r.timeout, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	}
	full := append([]string{"-c", "core.fsmonitor=false", "-C", dir}, args...)
	return run(ctx, r.bin, full...)
}

func (r *resolver) Probe(ctx context.Context) source.Status {
	st := source.Status{Kind: source.KindGit}
	if runner.Found(r.bin) == "" {
		st.State = source.StateMissing
		st.Detail = "git not found"
		return st
	}
	run := r.run
	if run == nil {
		run = runner.Exec(r.timeout, "LC_ALL=C")
	}
	out, err := run(ctx, r.bin, "--version")
	if err != nil {
		st.State = source.StateFailing
		st.Detail = "git does not answer"
		return st
	}
	st.State = source.StateOK
	// "git version 2.47.1" or "git version 2.39.5 (Apple Git-154)"
	if f := strings.Fields(string(out)); len(f) >= 3 && f[0] == "git" && f[1] == "version" {
		st.Version = f[2]
		st.Tested = true // git's porcelain output used here is stable across versions
	}
	return st
}

// exitCode returns the exit code of a git that ran and failed, or -1.
func exitCode(err error) int {
	var ee *runner.ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return -1
}

var errBadFolder = errors.New("gitsrc: not an absolute folder path")

// validDir accepts only a clean absolute path, so a folder taken from a
// record can never be read by git as an option.
func validDir(dir string) bool {
	return filepath.IsAbs(dir) && !strings.ContainsAny(dir, "\x00\n\r")
}

func (r *resolver) Resolve(ctx context.Context, folder string) (model.Repo, error) {
	if !validDir(folder) {
		return model.Repo{}, errBadFolder
	}
	fi, err := os.Stat(folder)
	if err != nil {
		return model.Repo{}, err
	}
	if !fi.IsDir() {
		return model.Repo{}, fmt.Errorf("not a folder")
	}
	outside := model.Repo{Root: folder, MainRoot: folder}

	out, err := r.git(ctx, folder, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir")
	if err != nil {
		if exitCode(err) > 0 { // not a work tree (outside a repo, or inside .git)
			return outside, nil
		}
		return model.Repo{}, err
	}
	lines := splitLines(out)
	if len(lines) != 2 {
		return outside, nil
	}
	repo := model.Repo{Root: lines[0], MainRoot: lines[0]}
	if common := lines[1]; filepath.Base(common) == ".git" {
		repo.MainRoot = filepath.Dir(common)
	}

	out, err = r.git(ctx, repo.Root, "symbolic-ref", "--quiet", "HEAD")
	switch {
	case err == nil:
		if b, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "refs/heads/"); ok && b != "" {
			repo.Branch = model.Of(b)
		}
	case exitCode(err) > 0: // detached HEAD: branch unknown
	default:
		return model.Repo{}, err
	}

	out, err = r.git(ctx, repo.Root, "config", "--get", "remote.origin.url")
	switch {
	case err == nil:
		repo.Slug = slugFromURL(strings.TrimSpace(string(out)))
	case exitCode(err) > 0: // no origin remote
	default:
		return model.Repo{}, err
	}
	return repo, nil
}

func (r *resolver) Worktrees(ctx context.Context, root string) ([]model.Worktree, error) {
	out, err := r.git(ctx, root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var wts []model.Worktree
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "worktree "):
			wts = append(wts, model.Worktree{Path: strings.TrimPrefix(line, "worktree ")})
		case strings.HasPrefix(line, "branch ") && len(wts) > 0:
			if b, ok := strings.CutPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/"); ok && b != "" {
				wts[len(wts)-1].Branch = model.Of(b)
			}
		}
	}
	return wts, sc.Err()
}

func (r *resolver) LocalBranches(ctx context.Context, root string) ([]string, error) {
	out, err := r.git(ctx, root, "for-each-ref", "--format=%(refname)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	var bs []string
	for _, l := range splitLines(out) {
		if b, ok := strings.CutPrefix(l, "refs/heads/"); ok && b != "" {
			bs = append(bs, b)
		}
	}
	return bs, nil
}

func splitLines(b []byte) []string {
	var out []string
	for l := range strings.SplitSeq(strings.TrimRight(string(b), "\n"), "\n") {
		if l = strings.TrimRight(l, "\r"); l != "" {
			out = append(out, l)
		}
	}
	return out
}

var slugPart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// slugFromURL returns "owner/name" for a github.com remote URL, "" otherwise.
func slugFromURL(raw string) string {
	var path string
	switch {
	case strings.HasPrefix(raw, "git@github.com:"):
		path = strings.TrimPrefix(raw, "git@github.com:")
	default:
		u, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(u.Hostname(), "github.com") {
			return ""
		}
		switch u.Scheme {
		case "https", "http", "ssh", "git":
		default:
			return ""
		}
		path = strings.TrimPrefix(u.Path, "/")
	}
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	owner, name, ok := strings.Cut(path, "/")
	if !ok || strings.Contains(name, "/") || !slugPart.MatchString(owner) || !slugPart.MatchString(name) ||
		strings.Trim(owner, ".") == "" || strings.Trim(name, ".") == "" {
		return ""
	}
	return owner + "/" + name
}
