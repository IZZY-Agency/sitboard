// Package gitsrc answers git questions about folders. It never changes a repo.
//
// Every call runs git with an argument list and a 5 s timeout. Folder paths
// and branch names come from session records and repos and are untrusted: they
// are passed as single arguments, never through a shell.
package gitsrc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/gitlab"
	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/gitenv"
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

// NewInventory returns the git resolver as a source.RepoInventory.
func NewInventory(opts Options) source.RepoInventory { return New(opts).(*resolver) }

// New returns the git resolver.
func New(opts Options) source.RepoResolver {
	bin := opts.Bin
	if bin == "" {
		bin = "git"
	}
	return &resolver{bin: bin, timeout: callTimeout}
}

// git runs one read-only git command in dir. Optional locks are off so a
// read never writes to the repo, fsmonitor is off so a repo's own config
// cannot make git start a program, and lazy fetching is off so a partial
// clone never fetches a missing object (git 2.44 and later; Inventory also
// refuses partial clones). TreeStatus refuses worktrees whose files a
// filter applies to, which status would run.
func (r *resolver) git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return r.gitWith(ctx, false, dir, args...)
}

// gitQuiet is git for an answer that is incomplete when git warns: a
// warning on stderr is an error.
func (r *resolver) gitQuiet(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return r.gitWith(ctx, true, dir, args...)
}

func (r *resolver) gitWith(ctx context.Context, quiet bool, dir string, args ...string) ([]byte, error) {
	if !validDir(dir) {
		return nil, errBadFolder
	}
	run := r.run
	if run == nil {
		run = runner.ExecWithout(r.timeout, quiet, parentGitVar, gitVars...)
	}
	full := append([]string{"-c", "core.fsmonitor=false", "-C", dir}, args...)
	return run(ctx, r.bin, full...)
}

// gitVars are set for every git call the Leftovers scan and the board make.
var gitVars = []string{"GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "LC_ALL=C"}

// parentGitVar: the variables a parent git sets for the hooks and aliases it
// runs, and the ones that pick another config file (gitenv): sitboard's git
// reads the folder it is given, with the user's own config, as the user's own
// git in that folder would.
func parentGitVar(name string) bool { return gitenv.Redirects(name) }

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

	repo.Remote, repo.OriginUnclear, err = r.origin(ctx, repo.Root)
	switch {
	case err == nil:
	case exitCode(err) > 0: // origin unreadable: no remote, as with none
		repo.Remote, repo.OriginUnclear = model.ForgeRepo{}, false
	default:
		return model.Repo{}, err
	}
	return repo, nil
}

// oidRe is a full commit id: SHA-1 or SHA-256.
var oidRe = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// Inventory reads a repo's worktrees, local and origin branches and stashes.
func (r *resolver) Inventory(ctx context.Context, root string) (model.RepoInventory, error) {
	var inv model.RepoInventory
	// The main checkout's own git folder is the shared one; a linked
	// worktree's is under it.
	out, err := r.git(ctx, root, "rev-parse", "--path-format=absolute", "--show-toplevel", "--absolute-git-dir", "--git-common-dir")
	if err != nil {
		return inv, err
	}
	lines := splitLines(out)
	if len(lines) != 3 || lines[0] != root || !filepath.IsAbs(lines[2]) || filepath.Clean(lines[1]) != filepath.Clean(lines[2]) {
		return inv, errors.New("gitsrc: not the main checkout of a repo")
	}
	inv.CommonDir = filepath.Clean(lines[2])
	// The worktrees first (listing them reads no object): a caller keeps
	// them when the rest fails, since one may sit inside another checkout.
	out, err = r.git(ctx, root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return inv, err
	}
	if inv.Worktrees, err = parseWorktrees(out); err != nil {
		return inv, err
	}
	// A partial clone fetches what it lacks when asked about it: not one to
	// read.
	out, err = r.git(ctx, root, "config", "--get-regexp", `^remote\..*\.promisor$|^extensions\.partialclone$`)
	switch {
	case err == nil && len(bytes.TrimSpace(out)) > 0:
		return inv, errors.New("gitsrc: a partial clone")
	case err != nil && exitCode(err) != 1: // 1: none set
		return inv, err
	}
	// Every worktree git has registered must be one the listing gave, on
	// what the listing says: git leaves out one whose gitdir file it cannot
	// read, and that one may have any branch checked out.
	if err := accountFor(inv.CommonDir, inv.Worktrees); err != nil {
		return inv, err
	}
	for i := range inv.Worktrees {
		w := &inv.Worktrees[i]
		if !validDir(w.Path) {
			return inv, errBadFolder // a path git could only be given as another one
		}
		// A merge, rebase, bisect... under way: the worktree holds it, and
		// a rebase or bisect goes back to its branch at the end (a rebase
		// with --update-refs to the branches it queued, too).
		if w.Busy, w.Operation, err = operation(w.GitDir); err != nil {
			return inv, err
		}
	}

	// A ref under refs/heads that names no commit is no branch (git itself
	// writes none); one that names a missing object fails the whole call.
	out, err = r.git(ctx, root, "for-each-ref", "--format=%(objectname) %(objecttype) %(refname) %(symref)", "refs/heads/", "refs/remotes/origin/")
	if err != nil {
		return inv, err
	}
	targets := map[string]bool{} // branches a symbolic ref under refs/heads points at
	for _, l := range splitLines(out) {
		f := strings.SplitN(l, " ", 4)
		if len(f) != 4 || !oidRe.MatchString(f[0]) {
			continue
		}
		oid, typ, ref, sym := f[0], f[1], f[2], f[3]
		if b, ok := strings.CutPrefix(ref, "refs/heads/"); ok && b != "" {
			if t, ok := strings.CutPrefix(sym, "refs/heads/"); ok {
				targets[t] = true
			}
			if typ != "commit" {
				continue
			}
			inv.Branches = append(inv.Branches, model.Branch{Name: b, Tip: oid, Symbolic: sym != ""})
		} else if b, ok := strings.CutPrefix(ref, "refs/remotes/origin/"); ok && b != "" && b != "HEAD" {
			inv.Remote = append(inv.Remote, model.Branch{Name: b, Tip: oid})
		}
	}
	for i := range inv.Branches {
		if targets[inv.Branches[i].Name] {
			inv.Branches[i].Symbolic = true
		}
	}
	origin, _, err := r.origin(ctx, root)
	if err != nil {
		return inv, err
	}
	if origin.Forge == model.ForgeGitHub { // Leftovers is GitHub only
		inv.Origin = origin
	}

	// %P is the stash commit's parents: the first is the commit it was made on.
	out, err = r.git(ctx, root, "stash", "list", "--format=%P%x00%gs")
	if err != nil {
		return inv, err
	}
	for _, l := range splitLines(out) {
		parents, subject, ok := strings.Cut(l, "\x00")
		base, _, _ := strings.Cut(parents, " ")
		if !ok || !oidRe.MatchString(base) {
			// A stash this cannot read still counts, with no base: the
			// scan cannot tell whose it is, so it holds merged work back.
			base = ""
		}
		inv.Stashes = append(inv.Stashes, model.Stash{Base: base, Subject: subject})
	}
	return inv, nil
}

// origin returns the repository of origin's one effective fetch address
// (url.<base>.insteadOf applied, as git fetch would): a GitHub one for
// github.com; for any other host a project path can be read from
// (gitlab.ParseRemote), a GitLab one that the core keeps only when the host
// is gitlab.com or one glab is logged in to (gitsrc runs no glab); zero when
// there is no origin, more than one address, or none of these. unclear: more
// than one address, one of them on GitHub (a GitHub repo whose name is
// unknown).
func (r *resolver) origin(ctx context.Context, root string) (remote model.ForgeRepo, unclear bool, err error) {
	out, err := r.git(ctx, root, "remote", "get-url", "--all", "origin")
	if err != nil {
		if exitCode(err) == 2 { // 2: no such remote
			return model.ForgeRepo{}, false, nil
		}
		return model.ForgeRepo{}, false, err
	}
	var urls []string
	for _, l := range strings.Split(string(out), "\n") {
		if l != "" {
			urls = append(urls, l)
		}
	}
	if len(urls) != 1 {
		return model.ForgeRepo{}, slices.ContainsFunc(urls, func(u string) bool { return slugFromURL(u) != "" }), nil
	}
	if slug := slugFromURL(urls[0]); slug != "" {
		return model.GitHubRepo(slug), false, nil
	}
	// github.com keeps its stricter two-part rule above.
	if host, path, ok := gitlab.ParseRemote(urls[0]); ok && host != model.GitHubHost {
		return model.GitLabRepo(host, path), false, nil
	}
	return model.ForgeRepo{}, false, nil
}

// operation reads the files git keeps in a worktree's own git folder for an
// operation under way: whether one is (a merge, cherry-pick, revert,
// sequence, rebase or bisect), and the branches it holds: the one a rebase
// or bisect started from and goes back to, and those a rebase --update-refs
// queued. A marker that is there must be a readable regular file; anything
// else is an error.
func operation(gitDir string) (branches []string, busy bool, err error) {
	if !filepath.IsAbs(gitDir) {
		return nil, false, errors.New("gitsrc: no git folder")
	}
	for _, f := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "sequencer", "rebase-merge", "rebase-apply", "BISECT_LOG", "BISECT_START"} {
		_, err := os.Lstat(filepath.Join(gitDir, f))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		busy = true
	}
	for _, f := range []string{"rebase-merge/head-name", "rebase-apply/head-name", "BISECT_START", "rebase-merge/update-refs"} {
		b, err := marker(filepath.Join(gitDir, f))
		if err != nil {
			return nil, false, err
		}
		for _, name := range splitLines(b) {
			name = strings.TrimSpace(name)
			if br, ok := strings.CutPrefix(name, "refs/heads/"); ok {
				name = br
			} else if f == "rebase-merge/update-refs" {
				continue // its other lines are commit ids
			}
			if name == "" || name == "detached HEAD" {
				continue // a rebase or bisect that started detached holds no branch
			}
			// BISECT_START holds a branch name or a commit id: either way,
			// hold whatever branch has that name.
			branches = append(branches, name)
		}
	}
	return branches, busy, nil
}

// marker reads a file git keeps for an operation: nil when it is not there;
// one that is there must be a regular file (not a link, not a folder) it can
// read.
func marker(p string) ([]byte, error) {
	fi, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("gitsrc: %s is not a regular file", p)
	}
	return os.ReadFile(p)
}

// errUnaccounted: git has registered a worktree the listing does not show
// as it is.
var errUnaccounted = errors.New("gitsrc: a registered worktree is not accounted for")

// accountFor ties every worktree git registered (a folder under
// <common>/worktrees) to a linked worktree of the listing, both ways: its
// gitdir file names that worktree's folder and its HEAD what the listing says
// is checked out there. It sets each worktree's own git folder (the common
// folder for the main checkout). Anything it cannot read or tie is an error.
func accountFor(common string, wts []model.Worktree) error {
	entries, err := os.ReadDir(filepath.Join(common, "worktrees"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	wts[0].GitDir = common
	if len(entries) != len(wts)-1 {
		return errUnaccounted
	}
	for _, e := range entries {
		admin := filepath.Join(common, "worktrees", e.Name())
		gitdir, err := marker(filepath.Join(admin, "gitdir"))
		if err != nil || len(gitdir) == 0 {
			return errUnaccounted
		}
		p := strings.TrimSuffix(string(gitdir), "\n")
		if !filepath.IsAbs(p) {
			p = filepath.Join(admin, p) // worktree.useRelativePaths
		}
		folder := filepath.Dir(filepath.Clean(p))
		w := findWorktree(wts[1:], folder)
		if w == nil || w.GitDir != "" {
			return errUnaccounted
		}
		head, err := marker(filepath.Join(admin, "HEAD"))
		if err != nil {
			return errUnaccounted
		}
		h := strings.TrimSuffix(string(head), "\n")
		b, onBranch := w.Branch.Get()
		switch ref, sym := strings.CutPrefix(h, "ref: "); {
		case sym && (!onBranch || ref != "refs/heads/"+b):
			return errUnaccounted
		case !sym && (onBranch || !oidRe.MatchString(h)):
			return errUnaccounted
		}
		w.GitDir = admin
	}
	return nil
}

// findWorktree returns the worktree of wts at folder, the same folder
// through links too.
func findWorktree(wts []model.Worktree, folder string) *model.Worktree {
	for i := range wts {
		if wts[i].Path == folder {
			return &wts[i]
		}
	}
	real, err := filepath.EvalSymlinks(folder)
	if err != nil {
		return nil
	}
	for i := range wts {
		if r, err := filepath.EvalSymlinks(wts[i].Path); err == nil && r == real {
			return &wts[i]
		}
	}
	return nil
}

// errWorktreeList: the worktree listing is not whole: cut short, or a record
// that does not start with an absolute path.
var errWorktreeList = errors.New("gitsrc: the worktree list is incomplete")

// parseWorktrees reads `git worktree list --porcelain -z`: records of
// NUL-ended fields, each record ended by an empty field and started by
// "worktree <path>" (a path may hold a newline). The first record is the main
// checkout. A listing that does not end a record, or a record that does not
// start with an absolute path, is an error: a cut answer could hide a
// checkout.
func parseWorktrees(out []byte) ([]model.Worktree, error) {
	if !bytes.HasSuffix(out, []byte{0, 0}) {
		return nil, errWorktreeList
	}
	var wts []model.Worktree
	open := false // a record started, its end not yet seen
	for _, f := range bytes.Split(out[:len(out)-1], []byte{0}) {
		field := string(f)
		if !open {
			p, ok := strings.CutPrefix(field, "worktree ")
			if !ok || !filepath.IsAbs(p) {
				return nil, errWorktreeList
			}
			wts = append(wts, model.Worktree{Path: p, Main: len(wts) == 0})
			open = true
			continue
		}
		w := &wts[len(wts)-1]
		switch {
		case field == "":
			open = false
		case strings.HasPrefix(field, "branch "):
			if b, ok := strings.CutPrefix(strings.TrimPrefix(field, "branch "), "refs/heads/"); ok && b != "" {
				w.Branch = model.Of(b)
			}
		case field == "locked" || strings.HasPrefix(field, "locked "):
			w.Locked = true
		case field == "prunable" || strings.HasPrefix(field, "prunable "):
			w.Prunable = true
		case field == "bare":
			w.Bare = true
		}
	}
	if len(wts) == 0 {
		return nil, errWorktreeList
	}
	return wts, nil // the last field, empty, ended the last record
}

// IsAncestor runs `git merge-base --is-ancestor a b`: exit 0 yes, 1 no,
// anything else (a missing commit) an error.
func (r *resolver) IsAncestor(ctx context.Context, root, a, b string) (bool, error) {
	if !oidRe.MatchString(a) || !oidRe.MatchString(b) {
		return false, errors.New("gitsrc: not a commit id")
	}
	_, err := r.git(ctx, root, "merge-base", "--is-ancestor", a, b)
	switch {
	case err == nil:
		return true, nil
	case exitCode(err) == 1:
		return false, nil
	default:
		return false, err
	}
}

// errCannotTell: git's answer about a worktree's files is incomplete.
var errCannotTell = errors.New("gitsrc: cannot tell whether the worktree is clean")

// TreeStatus reads one worktree: where git finds it from dir, what is
// checked out, and `git status`: untracked files one by one, ignored ones by
// matching folder, so a wholly ignored folder counts once. It is an error
// when status cannot see everything: git warned (a folder it cannot read),
// the index holds entries git was told to overlook (assume-unchanged,
// skip-worktree) or submodules, whose own changes status may not show.
func (r *resolver) TreeStatus(ctx context.Context, dir string) (model.TreeStatus, error) {
	var st model.TreeStatus
	out, err := r.git(ctx, dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--absolute-git-dir", "--git-common-dir", "--verify", "HEAD^{commit}")
	if err != nil {
		return st, err
	}
	l := splitLines(out)
	if len(l) != 4 || !oidRe.MatchString(l[3]) {
		return st, errCannotTell
	}
	st.Top, st.GitDir, st.CommonDir, st.Tip = l[0], filepath.Clean(l[1]), filepath.Clean(l[2]), l[3]
	out, err = r.git(ctx, dir, "symbolic-ref", "--quiet", "HEAD")
	switch {
	case err == nil:
		st.Head = strings.TrimSpace(string(out))
	case exitCode(err) == 1: // detached
	default:
		return st, err
	}

	out, err = r.gitQuiet(ctx, dir, "ls-files", "-v", "-s", "-z")
	if err != nil {
		return st, err
	}
	for e := range bytes.SplitSeq(out, []byte{0}) {
		// "<tag> <mode> <oid> <stage>\t<path>": a lower-case tag is
		// assume-unchanged, S skip-worktree; mode 160000 a submodule.
		if len(e) == 0 {
			continue
		}
		if len(e) < 9 || e[1] != ' ' || e[0] == 'S' || e[0] >= 'a' && e[0] <= 'z' || string(e[2:8]) == "160000" {
			return st, errCannotTell
		}
	}

	// status runs a file's clean filter, a program the config names, when
	// the file's stat changed: a worktree where a filter attribute applies
	// to any tracked file is not one to read. The same listing limited to
	// files with no filter attribute must be the whole listing.
	plain, err := r.gitQuiet(ctx, dir, "ls-files", "-v", "-s", "-z", "--", ":(attr:!filter)")
	if err != nil {
		return st, err
	}
	if !bytes.Equal(plain, out) {
		return st, errCannotTell
	}

	out, err = r.gitQuiet(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching")
	if err != nil {
		return st, err
	}
	st.Clean = true
	for e := range bytes.SplitSeq(out, []byte{0}) {
		switch {
		case len(e) == 0:
		case bytes.HasPrefix(e, []byte("!! ")):
			st.Ignored++
		case len(e) >= 3 && e[2] == ' ':
			st.Clean = false
		}
		// a bare entry is the old name of a rename; its own entry said "not clean"
	}
	return st, nil
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
